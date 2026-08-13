package authhandler

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/adminproviders"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/pgxutil"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/users"
)

// PGStore is a PostgreSQL-backed implementation of authhandler.Store.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertions. Asserting OIDCStore too means a future
// signature drift on any OIDC method fails the build here, rather than silently
// flipping the runtime h.store.(OIDCStore) assertion to ok=false and disabling
// all OIDC login.
var (
	_ Store     = (*PGStore)(nil)
	_ OIDCStore = (*PGStore)(nil)
)

// ---------------------------------------------------------------------------
// User operations
// ---------------------------------------------------------------------------

const userSelectSQL = `SELECT id, username, password_hash, auth_method, first_name, last_name, email, role, external_id, created_at FROM users`

// userDBRow is an internal struct with db tags for scanning user rows.
type userDBRow struct {
	ID           string    `db:"id"`
	Username     string    `db:"username"`
	PasswordHash string    `db:"password_hash"`
	AuthMethod   string    `db:"auth_method"`
	FirstName    *string   `db:"first_name"`
	LastName     *string   `db:"last_name"`
	Email        *string   `db:"email"`
	Role         string    `db:"role"`
	ExternalID   *string   `db:"external_id"`
	CreatedAt    time.Time `db:"created_at"`
}

func toUserRow(r userDBRow) *users.UserRow {
	return &users.UserRow{
		ID:           r.ID,
		Username:     r.Username,
		PasswordHash: r.PasswordHash,
		AuthMethod:   r.AuthMethod,
		FirstName:    r.FirstName,
		LastName:     r.LastName,
		Email:        r.Email,
		Role:         r.Role,
		ExternalID:   r.ExternalID,
		CreatedAt:    r.CreatedAt,
	}
}

// GetUserByUsername returns the user with the given username, or nil if not found.
func (s *PGStore) GetUserByUsername(ctx context.Context, username string) (*users.UserRow, error) {
	row, err := pgxutil.QueryOne[userDBRow](ctx, s.pool, userSelectSQL+` WHERE username = $1`, username)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return toUserRow(*row), nil
}

// CreateUser inserts a new user and returns the stored row.
func (s *PGStore) CreateUser(ctx context.Context, data users.UserCreate) (*users.UserRow, error) {
	const sql = `
		INSERT INTO users (username, password_hash, auth_method, first_name, last_name, email, role, external_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id, username, password_hash, auth_method, first_name, last_name, email, role, external_id, created_at`

	row, err := pgxutil.QueryOne[userDBRow](ctx, s.pool, sql,
		data.Username, data.PasswordHash, data.AuthMethod,
		data.FirstName, data.LastName, data.Email, data.Role, data.ExternalID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("CreateUser: no row returned")
	}
	return toUserRow(*row), nil
}

// ---------------------------------------------------------------------------
// OIDC-specific lookups
// ---------------------------------------------------------------------------

// GetActiveOIDCProvider returns the single active OIDC auth_providers row, or
// nil if no active OIDC provider is configured. The single-active invariant is
// enforced by the adminproviders handler at write time.
func (s *PGStore) GetActiveOIDCProvider(ctx context.Context) (*adminproviders.AuthProviderRow, error) {
	const sql = `SELECT ` + authProviderSelectCols + ` FROM auth_providers
	             WHERE is_active = true AND type = $1
	             ORDER BY created_at DESC LIMIT 1`
	rows, err := pgxutil.QueryRows[authProviderDBRow](ctx, s.pool, sql, OIDCProviderType)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := toAuthProviderRow(rows[0])
	return &r, nil
}

// GetUserByExternalID returns the user whose external_id matches the given
// OIDC `sub`. Used as the first lookup on every OIDC login after the user
// has been linked or auto-provisioned.
func (s *PGStore) GetUserByExternalID(ctx context.Context, externalID string) (*users.UserRow, error) {
	row, err := pgxutil.QueryOne[userDBRow](ctx, s.pool, userSelectSQL+` WHERE external_id = $1`, externalID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return toUserRow(*row), nil
}

// GetUsersByUsername returns every user row whose username matches
// case-insensitively. Used by the OIDC LDAP→OIDC link branch: usernames are
// stable across an employee's lifecycle (emails are not), so the IdP admin
// asked us to key the link on `preferred_username` instead of email.
//
// users.username carries a unique constraint, but it is case-sensitive in
// Postgres, so legacy mixed-case dupes ("JDoe" vs "jdoe") are possible —
// the caller fails closed if >1 rows come back.
func (s *PGStore) GetUsersByUsername(ctx context.Context, username string) ([]*users.UserRow, error) {
	rows, err := pgxutil.QueryRows[userDBRow](ctx, s.pool, userSelectSQL+` WHERE LOWER(username) = LOWER($1)`, username)
	if err != nil {
		return nil, err
	}
	out := make([]*users.UserRow, len(rows))
	for i := range rows {
		out[i] = toUserRow(rows[i])
	}
	return out, nil
}

// CountOIDCUsers returns how many users have been provisioned/linked via OIDC
// (auth_method = 'oidc'). The broker uses a zero count to detect the very first
// SSO login and bootstrap that user as superadmin.
func (s *PGStore) CountOIDCUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM users WHERE auth_method = 'oidc'`).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// LinkUserExternalID stamps an existing user row with an OIDC `sub` and flips
// its auth_method to 'oidc'. Used for the LDAP→OIDC migration link step.
// Returns the updated row.
func (s *PGStore) LinkUserExternalID(ctx context.Context, userID, externalID string) (*users.UserRow, error) {
	const sql = `UPDATE users SET external_id = $2, auth_method = 'oidc'
	             WHERE id = $1
	             RETURNING id, username, password_hash, auth_method, first_name, last_name, email, role, external_id, created_at`
	row, err := pgxutil.QueryOne[userDBRow](ctx, s.pool, sql, userID, externalID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("LinkUserExternalID: user %s not found", userID)
	}
	return toUserRow(*row), nil
}

// ---------------------------------------------------------------------------
// Auth providers
// ---------------------------------------------------------------------------

// authProviderDBRow is an internal struct with db tags for scanning auth_providers rows.
type authProviderDBRow struct {
	ID        string          `db:"id"`
	Type      string          `db:"type"`
	Name      string          `db:"name"`
	Config    json.RawMessage `db:"config"`
	IsActive  bool            `db:"is_active"`
	CreatedAt time.Time       `db:"created_at"`
}

func toAuthProviderRow(r authProviderDBRow) adminproviders.AuthProviderRow {
	return adminproviders.AuthProviderRow{
		ID:        r.ID,
		Type:      r.Type,
		Name:      r.Name,
		Config:    r.Config,
		IsActive:  r.IsActive,
		CreatedAt: r.CreatedAt,
	}
}

const authProviderSelectCols = `id, type, name, config, is_active, created_at`

// ApplyPendingInvites is a no-op in this deployment. JustRAG promoted bulk
// knowledge-base invites here on first OIDC login; chatbotadmin has no such
// tables, so there is nothing to migrate. Kept to satisfy the OIDCStore
// interface and the best-effort call in the OIDC callback.
func (s *PGStore) ApplyPendingInvites(_ context.Context, _, _ string) error {
	return nil
}

// GetActiveAuthProviders returns all active auth provider rows ordered by created_at DESC.
func (s *PGStore) GetActiveAuthProviders(ctx context.Context) ([]adminproviders.AuthProviderRow, error) {
	const sql = `SELECT ` + authProviderSelectCols + ` FROM auth_providers WHERE is_active = true ORDER BY created_at DESC`

	rows, err := pgxutil.QueryRows[authProviderDBRow](ctx, s.pool, sql)
	if err != nil {
		return nil, err
	}

	result := make([]adminproviders.AuthProviderRow, len(rows))
	for i, r := range rows {
		result[i] = toAuthProviderRow(r)
	}
	return result, nil
}
