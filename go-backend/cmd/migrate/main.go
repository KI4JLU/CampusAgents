// Command migrate applies database migrations and seeds the admin user plus the
// OIDC auth provider.
//
// Usage:
//
//	migrate              # apply migrations, then seed
//	migrate --status     # print the current migration version
//	migrate --seed-only  # only seed (no migrations)
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/config"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/migrate"
	mainmigrations "github.com/KI4JLU/CampusAgents/go-backend/migrations/main"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	status := flag.Bool("status", false, "print current migration version and exit")
	seedOnly := flag.Bool("seed-only", false, "only seed (no migrations)")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	switch {
	case *status:
		if err := migrate.Status(ctx, cfg.DatabaseURL, mainmigrations.FS); err != nil {
			slog.Error("status failed", "error", err)
			os.Exit(1)
		}
	case *seedOnly:
		if err := migrate.Seed(ctx, cfg); err != nil {
			slog.Error("seed failed", "error", err)
			os.Exit(1)
		}
	default:
		if err := migrate.RunAndSeed(ctx, cfg, mainmigrations.FS); err != nil {
			slog.Error("migrate failed", "error", err)
			os.Exit(1)
		}
		slog.Info("migrations + seed complete")
	}
}
