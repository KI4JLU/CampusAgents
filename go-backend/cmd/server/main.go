// Command server runs the chatbotadmin authentication + model-proxy backend.
package main

import (
	"log/slog"
	"os"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/app"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/config"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	if err := app.RunServer(cfg, version); err != nil {
		slog.Error("server error", "error", err)
		os.Exit(1)
	}
}
