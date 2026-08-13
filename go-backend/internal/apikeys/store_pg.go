package apikeys

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/pgxutil"
)

// PGStore is a PostgreSQL-backed implementation of the apikeys Store interface.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ Store = (*PGStore)(nil)

// apiKeyRow is an internal struct with db tags for scanning api_keys rows.
type apiKeyRow struct {
	ID         string     `db:"id"`
	Name       string     `db:"name"`
	KeyPrefix  string     `db:"key_prefix"`
	LastUsedAt *time.Time `db:"last_used_at"`
	ExpiresAt  *time.Time `db:"expires_at"`
	CreatedAt  time.Time  `db:"created_at"`
}

// CreateApiKey inserts a new API key and returns the stored row (without key_hash).
func (s *PGStore) CreateApiKey(ctx context.Context, data ApiKeyCreate) (*ApiKeyRow, error) {
	const sql = `
		INSERT INTO api_keys (user_id, name, key_hash, key_prefix, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, name, key_prefix, last_used_at, expires_at, created_at`

	rows, err := pgxutil.QueryRows[apiKeyRow](ctx, s.pool, sql,
		data.UserID, data.Name, data.KeyHash, data.KeyPrefix, data.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("create api key: no row returned")
	}
	r := rows[0]
	return &ApiKeyRow{
		ID:         r.ID,
		Name:       r.Name,
		KeyPrefix:  r.KeyPrefix,
		LastUsedAt: r.LastUsedAt,
		ExpiresAt:  r.ExpiresAt,
		CreatedAt:  r.CreatedAt,
	}, nil
}

// CountApiKeysByUser returns the number of API keys owned by userID.
func (s *PGStore) CountApiKeysByUser(ctx context.Context, userID string) (int, error) {
	const sql = `SELECT COUNT(*)::int FROM api_keys WHERE user_id = $1`
	var count int
	err := s.pool.QueryRow(ctx, sql, userID).Scan(&count)
	return count, err
}

// GetApiKeysByUser returns all API keys for userID ordered by created_at DESC.
func (s *PGStore) GetApiKeysByUser(ctx context.Context, userID string) ([]ApiKeyRow, error) {
	const sql = `
		SELECT id, name, key_prefix, last_used_at, expires_at, created_at
		FROM api_keys
		WHERE user_id = $1
		ORDER BY created_at DESC`

	rows, err := pgxutil.QueryRows[apiKeyRow](ctx, s.pool, sql, userID)
	if err != nil {
		return nil, err
	}

	result := make([]ApiKeyRow, len(rows))
	for i, r := range rows {
		result[i] = ApiKeyRow(r)
	}
	return result, nil
}

// DeleteApiKey deletes the key with the given id scoped to userID.
// Returns true if a row was deleted, false if not found.
func (s *PGStore) DeleteApiKey(ctx context.Context, id, userID string) (bool, error) {
	const sql = `DELETE FROM api_keys WHERE id = $1 AND user_id = $2 RETURNING id`

	rows, err := pgxutil.QueryRows[struct {
		ID string `db:"id"`
	}](ctx, s.pool, sql, id, userID)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}
