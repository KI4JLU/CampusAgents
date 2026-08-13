// Package apikeyauth provides HTTP middleware that authenticates requests using
// API keys issued by the JustRAG system (prefixed with "jrag_").
package apikeyauth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/auth"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/logctx"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/safego"
)

const (
	tokenPrefix = "jrag_"
	prefixLen   = 13 // first 13 chars of the token become key_prefix
	throttleMs  = int64(60_000)
)

// ApiKeyCandidate holds the fields needed to verify a candidate API key from
// the database.
type ApiKeyCandidate struct {
	ID        string
	UserID    string
	KeyHash   string
	ExpiresAt *time.Time
}

// UserInfo carries the minimal user fields needed to build auth.Claims.
type UserInfo struct {
	ID       string
	Username string
	Role     string
}

// Store is the persistence interface required by Middleware.
type Store interface {
	// GetApiKeysByPrefix returns all API keys whose key_prefix matches.
	GetApiKeysByPrefix(ctx context.Context, prefix string) ([]ApiKeyCandidate, error)
	// GetUserByID returns the user with the given UUID, or nil if not found.
	GetUserByID(ctx context.Context, id string) (*UserInfo, error)
	// UpdateApiKeyLastUsed sets last_used_at = NOW() for the given key ID.
	// Implementations should treat this as fire-and-forget.
	UpdateApiKeyLastUsed(ctx context.Context, id string) error
}

// Middleware authenticates HTTP requests via API keys.
type Middleware struct {
	store      Store
	lastUsed   sync.Map // map[string]int64 — keyID → unix millisecond timestamp
	throttleMs int64
}

// NewMiddleware creates a Middleware backed by store.
func NewMiddleware(store Store) *Middleware {
	return &Middleware{
		store:      store,
		throttleMs: throttleMs,
	}
}

// Verify validates token as a JustRAG API key and returns the owning user's
// claims, or a client-safe error (its message is what an HTTP 401 body should
// carry). It performs no HTTP handling and never rejects a request itself:
// enforcement is the spec-validation middleware's job (internal/specmw),
// which calls Verify through its credential-extraction step.
func (m *Middleware) Verify(ctx context.Context, token string) (*auth.Claims, error) {
	if !strings.HasPrefix(token, tokenPrefix) {
		logctx.From(ctx).Warn("auth.apikey_format_invalid")
		return nil, errors.New("invalid API key format")
	}

	if len(token) < prefixLen {
		logctx.From(ctx).Warn("auth.apikey_format_invalid", "reason", "too_short")
		return nil, errors.New("invalid API key")
	}

	prefix := token[:prefixLen]

	candidates, err := m.store.GetApiKeysByPrefix(ctx, prefix)
	if err != nil {
		logctx.From(ctx).Warn("auth.apikey_lookup_failed", "prefix", prefix, "error", err.Error())
		return nil, errors.New("invalid API key")
	}
	if len(candidates) == 0 {
		logctx.From(ctx).Warn("auth.apikey_unknown_prefix", "prefix", prefix)
		return nil, errors.New("invalid API key")
	}

	var matched *ApiKeyCandidate
	for i := range candidates {
		if bcrypt.CompareHashAndPassword([]byte(candidates[i].KeyHash), []byte(token)) == nil {
			matched = &candidates[i]
			break
		}
	}
	if matched == nil {
		logctx.From(ctx).Warn("auth.apikey_mismatch", "prefix", prefix)
		return nil, errors.New("invalid API key")
	}

	// Check expiry.
	if matched.ExpiresAt != nil && time.Now().After(*matched.ExpiresAt) {
		logctx.From(ctx).Warn("auth.apikey_expired", "key_id", matched.ID, "user_id", matched.UserID)
		return nil, errors.New("API key has expired")
	}

	// Load the owning user.
	user, err := m.store.GetUserByID(ctx, matched.UserID)
	if err != nil || user == nil {
		logctx.From(ctx).Warn("auth.apikey_user_missing", "key_id", matched.ID, "user_id", matched.UserID, "error", errString(err))
		return nil, errors.New("invalid API key")
	}

	// Throttled last-used update (at most once per minute per key).
	m.maybeUpdateLastUsed(ctx, matched.ID)

	logctx.From(ctx).Info("auth.apikey_validated", "key_id", matched.ID, "user_id", user.ID)
	return &auth.Claims{
		ID:       user.ID,
		Username: user.Username,
		Role:     user.Role,
	}, nil
}

// errString returns err.Error() when non-nil, otherwise the empty string —
// used to pass an optional error into slog without a separate branch.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// maybeUpdateLastUsed fires UpdateApiKeyLastUsed if more than m.throttleMs
// milliseconds have elapsed since the last update for this key.
func (m *Middleware) maybeUpdateLastUsed(ctx context.Context, keyID string) {
	nowMs := time.Now().UnixMilli()

	prev, loaded := m.lastUsed.Load(keyID)
	if loaded {
		if nowMs-prev.(int64) < m.throttleMs {
			return
		}
	}

	m.lastUsed.Store(keyID, nowMs)

	// Fire-and-forget in background with a short timeout so it doesn't block
	// the request or pile up on a slow database.
	safego.GoCtx(ctx, func() {
		// Detach cancellation (the update should outlive the request) but keep
		// tracing + request-id values from ctx for observability.
		tctx, tcancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer tcancel()
		if err := m.store.UpdateApiKeyLastUsed(tctx, keyID); err != nil {
			logctx.From(tctx).Warn("apikeyauth: update last_used failed", "key_id", keyID, "error", err)
		}
	})
}
