// Package migrate applies database migrations (via goose) and seeds the admin
// user plus the OIDC auth provider from configuration.
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/config"
)

// openSQL opens a database/sql handle over pgx for goose, which works against
// the standard library interface rather than pgxpool.
func openSQL(databaseURL string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	return stdlib.OpenDB(*cfg), nil
}

// migrationLockKey serialises concurrent migration runs (e.g. multiple
// init-containers in a scaled rollout) via a Postgres session advisory lock.
const migrationLockKey int64 = 0x636261646d696e30 // ASCII "cbadmin0"

// Run applies all pending migrations from the embedded FS.
func Run(ctx context.Context, databaseURL string, migrations fs.FS) error {
	db, err := openSQL(databaseURL)
	if err != nil {
		return fmt.Errorf("migrate: open db: %w", err)
	}
	defer db.Close()

	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrate: acquire conn: %w", err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("migrate: advisory lock: %w", err)
	}
	defer func() {
		if _, err := conn.ExecContext(context.WithoutCancel(ctx), "SELECT pg_advisory_unlock($1)", migrationLockKey); err != nil {
			slog.Warn("migrate: release advisory lock", "error", err)
		}
	}()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations)
	if err != nil {
		return fmt.Errorf("migrate: create provider: %w", err)
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrate: up: %w", err)
	}
	for _, r := range results {
		slog.Info("applied migration", "version", r.Source.Version, "name", r.Source.Path)
	}
	return nil
}

// Status prints the current migration version.
func Status(ctx context.Context, databaseURL string, migrations fs.FS) error {
	db, err := openSQL(databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations)
	if err != nil {
		return err
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return err
	}
	slog.Info("migration status", "current_version", version)
	return nil
}

// RunAndSeed applies migrations then seeds the admin user and OIDC provider.
func RunAndSeed(ctx context.Context, cfg *config.Config, migrations fs.FS) error {
	if err := Run(ctx, cfg.DatabaseURL, migrations); err != nil {
		return err
	}
	return Seed(ctx, cfg)
}
