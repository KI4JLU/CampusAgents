package migrate

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"

	"golang.org/x/crypto/bcrypt"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/authhandler"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/config"
)

// bcryptCost 12 (~250ms) defends the hand-picked admin password against
// offline brute-force if the hash leaks. Seed runs once at migrate time so the
// latency is irrelevant. Login uses CompareHashAndPassword, which reads the
// cost from the stored hash, so older cost-10 hashes keep working.
const bcryptCost = 12

// Seed upserts the admin superadmin (from ADMIN_PASSWORD) and re-seeds the
// single OIDC auth provider from the environment. Both are idempotent so they
// can run on every deploy and pick up configuration changes.
func Seed(ctx context.Context, cfg *config.Config) error {
	db, err := openSQL(cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("seed: open db: %w", err)
	}
	defer db.Close()

	// ---- admin user ----------------------------------------------------------
	if cfg.AdminPassword == "" {
		slog.Info("ADMIN_PASSWORD not set, skipping admin seed")
	} else {
		hash, err := bcrypt.GenerateFromPassword([]byte(cfg.AdminPassword), bcryptCost)
		if err != nil {
			return fmt.Errorf("seed: hash password: %w", err)
		}
		if _, err := db.ExecContext(ctx,
			`INSERT INTO users (username, password_hash, first_name, last_name, role, auth_method)
			 VALUES ($1, $2, 'Admin', 'User', 'superadmin', 'local')
			 ON CONFLICT (username) DO UPDATE
			     SET password_hash = EXCLUDED.password_hash,
			         role          = 'superadmin'`,
			cfg.AdminUsername, string(hash)); err != nil {
			return fmt.Errorf("seed: upsert admin: %w", err)
		}
		slog.Info("admin user seeded", "username", cfg.AdminUsername)
	}

	// ---- OIDC provider -------------------------------------------------------
	if !cfg.OIDC.Enabled() {
		slog.Info("OIDC not configured (OIDC_ISSUER_URL/CLIENT_ID/REDIRECT_URI), skipping provider seed")
		return nil
	}
	if err := seedOIDCProvider(ctx, db, cfg.OIDC); err != nil {
		return fmt.Errorf("seed: oidc provider: %w", err)
	}
	slog.Info("OIDC provider seeded", "name", cfg.OIDC.Name, "issuer", cfg.OIDC.IssuerURL)
	return nil
}

// oidcConfigJSON mirrors authhandler.OIDCConfig — the JSON shape persisted in
// auth_providers.config when type='oidc'. Defined locally to avoid coupling the
// migrate package to the handler's internal struct.
type oidcConfigJSON struct {
	IssuerURL             string   `json:"issuerURL"`
	ClientID              string   `json:"clientID"`
	ClientSecret          string   `json:"clientSecret"`
	Scopes                []string `json:"scopes,omitempty"`
	RedirectURI           string   `json:"redirectURI"`
	SuccessRedirect       string   `json:"successRedirect,omitempty"`
	PostLogoutRedirectURI string   `json:"postLogoutRedirectURI,omitempty"`
	EndSessionEndpoint    string   `json:"endSessionEndpoint,omitempty"`
}

func seedOIDCProvider(ctx context.Context, db *sql.DB, seed config.OIDCSeed) error {
	// Encrypt the client_secret at rest with AUTH_PROVIDER_SECRET_KEY, exactly
	// as the admin CRUD path would. An empty secret (public/PKCE client) stays
	// empty.
	encryptedSecret, err := authhandler.EncryptProviderSecret(seed.ClientSecret)
	if err != nil {
		return fmt.Errorf("encrypt client_secret: %w", err)
	}

	cfgJSON, err := json.Marshal(oidcConfigJSON{
		IssuerURL:             seed.IssuerURL,
		ClientID:              seed.ClientID,
		ClientSecret:          encryptedSecret,
		Scopes:                seed.Scopes,
		RedirectURI:           seed.RedirectURI,
		SuccessRedirect:       seed.SuccessRedirect,
		PostLogoutRedirectURI: seed.PostLogoutRedirectURI,
		EndSessionEndpoint:    seed.EndSessionEndpoint,
	})
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// Re-seed from scratch so env changes (new issuer, rotated secret) take
	// effect. A single active OIDC provider is the invariant the broker relies
	// on, so we clear any existing OIDC rows first.
	if _, err := db.ExecContext(ctx, `DELETE FROM auth_providers WHERE type = 'oidc'`); err != nil {
		return fmt.Errorf("clear existing oidc providers: %w", err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO auth_providers (type, name, config, is_active)
		 VALUES ('oidc', $1, $2, true)`,
		seed.Name, cfgJSON); err != nil {
		return fmt.Errorf("insert oidc provider: %w", err)
	}
	return nil
}
