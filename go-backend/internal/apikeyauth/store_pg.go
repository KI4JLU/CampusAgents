package apikeyauth

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/pgxutil"
)

// PGStore is a PostgreSQL-backed implementation of apikeyauth.Store.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ Store = (*PGStore)(nil)

// ---------------------------------------------------------------------------
// Internal scan structs
// ---------------------------------------------------------------------------

// apiKeyCandidateRow is an internal struct for scanning rows returned by
// GetApiKeysByPrefix.
type apiKeyCandidateRow struct {
	ID        string     `db:"id"`
	UserID    string     `db:"user_id"`
	KeyHash   string     `db:"key_hash"`
	ExpiresAt *time.Time `db:"expires_at"`
}

// userDBRow is an internal struct for scanning minimal user fields.
type userDBRow struct {
	ID       string `db:"id"`
	Username string `db:"username"`
	Role     string `db:"role"`
}

// ---------------------------------------------------------------------------
// Store implementation
// ---------------------------------------------------------------------------

// GetApiKeysByPrefix returns all API keys whose key_prefix matches prefix.
// Used by the API key auth middleware to find candidate keys for bcrypt comparison.
func (s *PGStore) GetApiKeysByPrefix(ctx context.Context, prefix string) ([]ApiKeyCandidate, error) {
	const sql = `SELECT id, user_id, key_hash, expires_at FROM api_keys WHERE key_prefix = $1`

	rows, err := pgxutil.QueryRows[apiKeyCandidateRow](ctx, s.pool, sql, prefix)
	if err != nil {
		return nil, err
	}
	result := make([]ApiKeyCandidate, len(rows))
	for i, r := range rows {
		result[i] = ApiKeyCandidate(r)
	}
	return result, nil
}

// GetUserByID returns the user with the given UUID, or nil if not found.
func (s *PGStore) GetUserByID(ctx context.Context, id string) (*UserInfo, error) {
	const sql = `SELECT id, username, role FROM users WHERE id = $1`

	rows, err := pgxutil.QueryRows[userDBRow](ctx, s.pool, sql, id)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	r := rows[0]
	return &UserInfo{
		ID:       r.ID,
		Username: r.Username,
		Role:     r.Role,
	}, nil
}

// UpdateApiKeyLastUsed sets last_used_at = NOW() for the API key with the given id.
// This is used in a fire-and-forget fashion by the auth middleware.
func (s *PGStore) UpdateApiKeyLastUsed(ctx context.Context, id string) error {
	const sql = `UPDATE api_keys SET last_used_at = NOW() WHERE id = $1`
	_, err := s.pool.Exec(ctx, sql, id)
	return err
}
