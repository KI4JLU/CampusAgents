package users

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/pgxutil"
)

// PGStore is a PostgreSQL-backed implementation of the users Store interface.
type PGStore struct {
	pool *pgxpool.Pool
}

// NewStore creates a new PGStore backed by pool.
func NewStore(pool *pgxpool.Pool) *PGStore {
	return &PGStore{pool: pool}
}

// Compile-time interface assertion.
var _ Store = (*PGStore)(nil)

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

func toUserRow(r userDBRow) *UserRow {
	return &UserRow{
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

// GetUserByID returns the user with the given UUID, or nil if not found.
func (s *PGStore) GetUserByID(ctx context.Context, id string) (*UserRow, error) {
	row, err := pgxutil.QueryOne[userDBRow](ctx, s.pool, userSelectSQL+` WHERE id = $1`, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return toUserRow(*row), nil
}

// GetUserByUsername returns the user with the given username, or nil if not found.
func (s *PGStore) GetUserByUsername(ctx context.Context, username string) (*UserRow, error) {
	row, err := pgxutil.QueryOne[userDBRow](ctx, s.pool, userSelectSQL+` WHERE username = $1`, username)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return toUserRow(*row), nil
}

// SearchUserByTerm returns the first user whose username, first_name, last_name, or
// full name matches the search term (case-insensitive LIKE), or nil if not found.
func (s *PGStore) SearchUserByTerm(ctx context.Context, term string) (*UserRow, error) {
	pattern := "%" + pgxutil.EscapeLike(strings.ToLower(term)) + "%"
	const searchSQL = userSelectSQL + `
WHERE LOWER(username) LIKE $1 ESCAPE '\'
   OR LOWER(first_name) LIKE $1 ESCAPE '\'
   OR LOWER(last_name) LIKE $1 ESCAPE '\'
   OR LOWER(CONCAT(first_name, ' ', last_name)) LIKE $1 ESCAPE '\'`
	row, err := pgxutil.QueryOne[userDBRow](ctx, s.pool, searchSQL, pattern)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return toUserRow(*row), nil
}

// UpdateUser applies the non-nil fields in data to the user with the given ID and returns
// the updated row.
func (s *PGStore) UpdateUser(ctx context.Context, id string, data UserUpdate) (*UserRow, error) {
	// base=1: $1 is reserved for the WHERE id clause, so the first SET
	// assignment binds $2.
	b := pgxutil.NewClauseBuilder(1)

	if data.FirstName != nil {
		b.Add("first_name = $%d", *data.FirstName)
	}
	if data.LastName != nil {
		b.Add("last_name = $%d", *data.LastName)
	}
	if data.Email != nil {
		b.Add("email = $%d", *data.Email)
	}
	if data.PasswordHash != nil {
		b.Add("password_hash = $%d", *data.PasswordHash)
	}

	if b.Len() == 0 {
		// Nothing to update — return the current row unchanged.
		return s.GetUserByID(ctx, id)
	}

	updateSQL := fmt.Sprintf(
		`UPDATE users SET %s WHERE id = $1 RETURNING id, username, password_hash, auth_method, first_name, last_name, email, role, external_id, created_at`,
		strings.Join(b.Clauses(), ", "),
	)
	// Prepend id as $1
	allArgs := append([]any{id}, b.Args()...)

	row, err := pgxutil.QueryOne[userDBRow](ctx, s.pool, updateSQL, allArgs...)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	return toUserRow(*row), nil
}

// CreateUser inserts a new user and returns the stored row.
func (s *PGStore) CreateUser(ctx context.Context, data UserCreate) (*UserRow, error) {
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
