// Package app wires the configuration, datastores, handlers, and HTTP router
// together and runs the server with graceful shutdown.
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/agents"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/apikeyauth"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/apikeys"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/auth"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/authhandler"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/config"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/database"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/middleware"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/modelproxy"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/redisclient"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/users"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/widgets"
)

// RunServer builds dependencies and serves until SIGINT/SIGTERM.
func RunServer(cfg *config.Config, version string) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// ---- datastores ----------------------------------------------------------
	pool, err := database.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	redis := redisclient.New(cfg.Redis)
	if err := redis.Ping(ctx); err != nil {
		slog.Warn("redis ping failed at startup; token revocation will fail closed in production", "error", err)
	}
	defer redis.Close()

	blacklist := auth.NewBlacklist(redis.NewBlacklistAdapter(), cfg.IsProduction)
	// Tokens issued before this boot are rejected (defence against a leaked
	// secret outliving a restart).
	blacklist.RecordServerBoot(ctx)

	// ---- handlers ------------------------------------------------------------
	authStore := authhandler.NewStore(pool)
	authH := authhandler.NewHandler(authStore, cfg.JWTSecret, blacklist)
	// The OIDC broker validates the `return_to` parameter (used by the
	// cross-origin widget-test portal) against the same CORS allowlist.
	authH.SetAllowedOrigins(cfg.AllowedOrigins)

	usersH := users.NewHandler(users.NewStore(pool))
	apiKeysH := apikeys.NewHandler(apikeys.NewStore(pool))

	jwtMW := auth.NewMiddleware(cfg.JWTSecret, blacklist)
	apiKeyMW := apikeyauth.NewMiddleware(apikeyauth.NewStore(pool))

	proxyH := modelproxy.NewHandler(cfg.KIAPIKey, cfg.KIBaseURL)
	// The two stores are shared across handlers: the widgets handler resolves a
	// widget's brain from the agent store, and the agents handler counts how
	// many widgets reference an agent (delete guard) via the widget store.
	widgetStore := widgets.NewStore(pool)
	agentStore := agents.NewStore(pool)
	// The public per-widget chat endpoint proxies to the same upstream LLM.
	widgetsH := widgets.NewHandler(widgetStore, agentStore, proxyH)
	agentsH := agents.NewHandler(agentStore, widgetStore)

	mux, err := newRouter(routerDeps{
		auth:     authH,
		users:    usersH,
		apiKeys:  apiKeysH,
		widgets:  widgetsH,
		agents:   agentsH,
		proxy:    proxyH,
		jwtMW:    jwtMW,
		apiKeyMW: apiKeyMW,
		version:  version,
	})
	if err != nil {
		return err
	}

	handler := middleware.CORS(cfg.AllowedOrigins)(middleware.RequestContext(mux))

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// ---- serve with graceful shutdown ---------------------------------------
	serveErr := make(chan error, 1)
	go func() {
		slog.Info("server listening", "addr", srv.Addr, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		slog.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
