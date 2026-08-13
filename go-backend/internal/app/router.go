package app

import (
	"context"
	"net/http"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/agents"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/api"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/apidocs"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/apikeyauth"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/apikeys"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/auth"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/authhandler"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/httputil"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/modelproxy"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/specmw"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/users"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/widgets"
)

type routerDeps struct {
	auth     *authhandler.Handler
	users    *users.Handler
	apiKeys  *apikeys.Handler
	widgets  *widgets.Handler
	agents   *agents.Handler
	proxy    *modelproxy.Handler
	jwtMW    *auth.Middleware
	apiKeyMW *apikeyauth.Middleware
	version  string
}

// apiServer composes the handler packages into the generated
// api.StrictServerInterface. Explicit delegation (instead of embedding)
// because every handler type is named Handler, so embedded field names would
// collide. Adding an operation to the spec adds a method here — the compiler
// enforces the list stays complete.
type apiServer struct {
	deps routerDeps
}

var _ api.StrictServerInterface = apiServer{}

func (s apiServer) Login(ctx context.Context, r api.LoginRequestObject) (api.LoginResponseObject, error) {
	return s.deps.auth.Login(ctx, r)
}

func (s apiServer) Logout(ctx context.Context, r api.LogoutRequestObject) (api.LogoutResponseObject, error) {
	return s.deps.auth.Logout(ctx, r)
}

func (s apiServer) RefreshToken(ctx context.Context, r api.RefreshTokenRequestObject) (api.RefreshTokenResponseObject, error) {
	return s.deps.auth.RefreshToken(ctx, r)
}

func (s apiServer) ListAuthProviders(ctx context.Context, r api.ListAuthProvidersRequestObject) (api.ListAuthProvidersResponseObject, error) {
	return s.deps.auth.ListAuthProviders(ctx, r)
}

func (s apiServer) GetUser(ctx context.Context, r api.GetUserRequestObject) (api.GetUserResponseObject, error) {
	return s.deps.users.GetUser(ctx, r)
}

func (s apiServer) UpdateUser(ctx context.Context, r api.UpdateUserRequestObject) (api.UpdateUserResponseObject, error) {
	return s.deps.users.UpdateUser(ctx, r)
}

func (s apiServer) ListApiKeys(ctx context.Context, r api.ListApiKeysRequestObject) (api.ListApiKeysResponseObject, error) {
	return s.deps.apiKeys.ListApiKeys(ctx, r)
}

func (s apiServer) CreateApiKey(ctx context.Context, r api.CreateApiKeyRequestObject) (api.CreateApiKeyResponseObject, error) {
	return s.deps.apiKeys.CreateApiKey(ctx, r)
}

func (s apiServer) DeleteApiKey(ctx context.Context, r api.DeleteApiKeyRequestObject) (api.DeleteApiKeyResponseObject, error) {
	return s.deps.apiKeys.DeleteApiKey(ctx, r)
}

func (s apiServer) ListAgents(ctx context.Context, r api.ListAgentsRequestObject) (api.ListAgentsResponseObject, error) {
	return s.deps.agents.ListAgents(ctx, r)
}

func (s apiServer) UpsertAgent(ctx context.Context, r api.UpsertAgentRequestObject) (api.UpsertAgentResponseObject, error) {
	return s.deps.agents.UpsertAgent(ctx, r)
}

func (s apiServer) DeleteAgent(ctx context.Context, r api.DeleteAgentRequestObject) (api.DeleteAgentResponseObject, error) {
	return s.deps.agents.DeleteAgent(ctx, r)
}

func (s apiServer) ListWidgets(ctx context.Context, r api.ListWidgetsRequestObject) (api.ListWidgetsResponseObject, error) {
	return s.deps.widgets.ListWidgets(ctx, r)
}

func (s apiServer) UpsertWidget(ctx context.Context, r api.UpsertWidgetRequestObject) (api.UpsertWidgetResponseObject, error) {
	return s.deps.widgets.UpsertWidget(ctx, r)
}

func (s apiServer) DeleteWidget(ctx context.Context, r api.DeleteWidgetRequestObject) (api.DeleteWidgetResponseObject, error) {
	return s.deps.widgets.DeleteWidget(ctx, r)
}

func (s apiServer) GetPublicWidgetConfig(ctx context.Context, r api.GetPublicWidgetConfigRequestObject) (api.GetPublicWidgetConfigResponseObject, error) {
	return s.deps.widgets.GetPublicWidgetConfig(ctx, r)
}

func (s apiServer) ListModels(ctx context.Context, r api.ListModelsRequestObject) (api.ListModelsResponseObject, error) {
	return s.deps.proxy.ListModels(ctx, r)
}

func (s apiServer) Healthz(context.Context, api.HealthzRequestObject) (api.HealthzResponseObject, error) {
	return api.Healthz200TextResponse("ok"), nil
}

// newRouter builds the HTTP handler tree:
//
//	outer mux:   /api/docs, /api/docs/scalar.js, /api/openapi.yaml (unvalidated)
//	  └─ ExtractCredentials  (Authorization header → claims in context)
//	     └─ specmw.Validator (route/schema/security enforcement from the spec)
//	        └─ inner mux:    generated routes (strict, typed)
//	                         + manual escape hatches (OIDC redirects, SSE chat)
//
// Auth is NOT wired per route here: the spec's security requirements are the
// single source of truth, enforced by the validator. The escape hatches are
// operations the strict model cannot express (streaming writers, redirects,
// cookies); they are still described in the spec and validated against it.
func newRouter(d routerDeps) (http.Handler, error) {
	strict := api.NewStrictHandlerWithOptions(apiServer{deps: d}, nil, api.StrictHTTPServerOptions{
		// Body-decode failures the validator did not already reject.
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "invalid request body")
		},
		// A strict handler returned an error: log it raw, answer sanitized.
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			httputil.WriteInternalErrorCtx(r.Context(), w, err)
		},
	})

	mux := http.NewServeMux()
	api.HandlerFromMux(strict, mux)

	// ---- escape hatches (excluded from codegen, see internal/api/config.yaml)
	mux.HandleFunc("GET /api/auth/oidc/login", d.auth.OIDCLogin)
	mux.HandleFunc("GET /api/auth/oidc/callback", d.auth.OIDCCallback)
	mux.HandleFunc("GET /api/auth/oidc/logout", d.auth.OIDCLogout)
	mux.HandleFunc("POST /api/auth/oidc/logout", d.auth.OIDCBackchannelLogout)
	mux.HandleFunc("POST /api/widgets/{id}/chat", d.widgets.Chat)
	mux.HandleFunc("POST /api/chat", d.proxy.Chat)

	validator, err := specmw.Validator(apidocs.Raw())
	if err != nil {
		return nil, err
	}
	protected := specmw.ExtractCredentials(d.jwtMW, d.apiKeyMW)(validator(mux))

	// The docs endpoints sit outside the validated tree: they are not part of
	// the API contract itself, and the validator would 404 them.
	outer := http.NewServeMux()
	outer.HandleFunc("GET /api/docs", apidocs.UI)
	outer.HandleFunc("GET /api/docs/scalar.js", apidocs.ScalarJS)
	outer.HandleFunc("GET /api/openapi.yaml", apidocs.Spec)
	outer.Handle("/", protected)
	return outer, nil
}
