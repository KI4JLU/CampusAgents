package auth

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/logctx"
)

const (
	tokenBlacklistPrefix  = "token:blacklist:"
	userInvalidatedPrefix = "user:tokens:invalidated:"
	serverBootKey         = "server:boot:timestamp"
)

// ErrKeyNotFound is returned by BlacklistStore.Get (and StringCmd.Result via
// the pipeline path) when the requested key is absent. It MUST be distinct
// from generic transport errors so callers can distinguish "the key has
// naturally expired / was never set" from "Redis is unreachable" — the former
// means there is no constraint to enforce, the latter is what fail-closed
// production behaviour is for.
var ErrKeyNotFound = errors.New("auth: key not found")

type BlacklistStore interface {
	Get(ctx context.Context, key string) (string, error)
	Set(ctx context.Context, key string, value any, expiration time.Duration) error
	Exists(ctx context.Context, keys ...string) (int64, error)
	Pipeline(ctx context.Context, fn func(pipe PipelineExecer) error) error
}

// PipelineExecer abstracts a Redis pipeline for batching commands.
type PipelineExecer interface {
	Get(ctx context.Context, key string) StringCmd
	Exists(ctx context.Context, keys ...string) IntCmd
}

// StringCmd abstracts the result of a string command.
type StringCmd interface {
	Result() (string, error)
}

// IntCmd abstracts the result of an integer command.
type IntCmd interface {
	Result() (int64, error)
}

type Blacklist struct {
	store        BlacklistStore
	isProduction bool
}

func NewBlacklist(store BlacklistStore, isProduction bool) *Blacklist {
	return &Blacklist{store: store, isProduction: isProduction}
}

func (b *Blacklist) Add(ctx context.Context, jti string, expiresAt time.Time) {
	ttl := time.Until(expiresAt)
	if ttl <= 0 {
		return
	}
	key := tokenBlacklistPrefix + jti
	if err := b.store.Set(ctx, key, "1", ttl); err != nil {
		slog.Error("failed to blacklist token", "jti", jti, "error", err)
	}
}

func (b *Blacklist) IsBlacklisted(ctx context.Context, jti string) bool {
	key := tokenBlacklistPrefix + jti
	count, err := b.store.Exists(ctx, key)
	if err != nil {
		slog.Error("failed to check blacklist", "jti", jti, "error", err)
		return b.isProduction
	}
	return count > 0
}

// InvalidateUserTokens marks every JWT issued before "now" for userID as
// revoked. The Redis Set is best-effort: a failure does NOT bubble up to the
// caller because the surrounding mutation (role change / delete) has already
// committed. We log with request context so the operator can still correlate
// the failed invalidation back to the originating request.
func (b *Blacklist) InvalidateUserTokens(ctx context.Context, userID string) {
	key := userInvalidatedPrefix + userID
	nowMs := strconv.FormatInt(time.Now().UnixMilli(), 10)
	if err := b.store.Set(ctx, key, nowMs, 24*time.Hour); err != nil {
		logctx.From(ctx).ErrorContext(ctx, "failed to invalidate user tokens", "userId", userID, "error", err)
	}
}

func (b *Blacklist) IsUserTokenInvalidated(ctx context.Context, userID string, tokenIAT int64) bool {
	key := userInvalidatedPrefix + userID
	val, err := b.store.Get(ctx, key)
	if err != nil {
		return false
	}

	invalidatedAt, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return false
	}

	tokenIssuedMs := tokenIAT * 1000
	return tokenIssuedMs < invalidatedAt
}

func (b *Blacklist) RecordServerBoot(ctx context.Context) {
	nowMs := strconv.FormatInt(time.Now().UnixMilli(), 10)
	if err := b.store.Set(ctx, serverBootKey, nowMs, 25*time.Hour); err != nil {
		slog.Error("failed to record server boot", "error", err)
	}
}

func (b *Blacklist) IsTokenBeforeServerBoot(ctx context.Context, tokenIAT int64) bool {
	val, err := b.store.Get(ctx, serverBootKey)
	if err != nil {
		// Boot key naturally expired (TTL=25h) or was never set: there is no
		// constraint to enforce. Returning b.isProduction here previously
		// rejected every token after ~25h of uptime.
		if errors.Is(err, ErrKeyNotFound) {
			return false
		}
		return b.isProduction
	}

	bootTime, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return b.isProduction
	}

	tokenIssuedMs := tokenIAT * 1000
	return tokenIssuedMs < bootTime
}

// CheckAllResult holds the results of all three blacklist checks.
type CheckAllResult struct {
	IsBlacklisted           bool
	IsUserTokenInvalidated  bool
	IsTokenBeforeServerBoot bool
}

// CheckAll performs all three token validity checks in a single Redis pipeline.
// On pipeline error, falls back to individual checks.
func (b *Blacklist) CheckAll(ctx context.Context, jti, userID string, tokenIAT int64) CheckAllResult {
	blacklistKey := tokenBlacklistPrefix + jti
	userKey := userInvalidatedPrefix + userID

	var existsCmd IntCmd
	var userValCmd StringCmd
	var bootValCmd StringCmd

	err := b.store.Pipeline(ctx, func(pipe PipelineExecer) error {
		existsCmd = pipe.Exists(ctx, blacklistKey)
		userValCmd = pipe.Get(ctx, userKey)
		bootValCmd = pipe.Get(ctx, serverBootKey)
		return nil
	})

	if err != nil {
		// Fallback to individual checks on pipeline error.
		return CheckAllResult{
			IsBlacklisted:           b.IsBlacklisted(ctx, jti),
			IsUserTokenInvalidated:  b.IsUserTokenInvalidated(ctx, userID, tokenIAT),
			IsTokenBeforeServerBoot: b.IsTokenBeforeServerBoot(ctx, tokenIAT),
		}
	}

	var result CheckAllResult

	// Check blacklist
	count, err := existsCmd.Result()
	if err != nil {
		result.IsBlacklisted = b.isProduction
	} else {
		result.IsBlacklisted = count > 0
	}

	// Check user token invalidation
	val, err := userValCmd.Result()
	if err == nil {
		if invalidatedAt, parseErr := strconv.ParseInt(val, 10, 64); parseErr == nil {
			tokenIssuedMs := tokenIAT * 1000
			result.IsUserTokenInvalidated = tokenIssuedMs < invalidatedAt
		}
	}

	// Check server boot. Treat key-missing as "no constraint" — the 25h TTL
	// will outlive any still-valid 24h JWT, so an absent key means the
	// invalidate-on-restart guard has aged out naturally and there is nothing
	// to enforce. Only genuine transport errors fall through to b.isProduction.
	val, err = bootValCmd.Result()
	if err != nil {
		if errors.Is(err, ErrKeyNotFound) {
			result.IsTokenBeforeServerBoot = false
		} else {
			result.IsTokenBeforeServerBoot = b.isProduction
		}
	} else {
		if bootTime, parseErr := strconv.ParseInt(val, 10, 64); parseErr == nil {
			tokenIssuedMs := tokenIAT * 1000
			result.IsTokenBeforeServerBoot = tokenIssuedMs < bootTime
		} else {
			result.IsTokenBeforeServerBoot = b.isProduction
		}
	}

	return result
}
