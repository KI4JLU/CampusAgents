package auth

import (
	"context"
	"errors"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/logctx"
)

type userKey struct{}

// WithUser returns a derived context that carries the given claims under the
// internal user-context key. Other packages and tests should use this helper
// instead of constructing the key themselves — keeping the key unexported
// preserves type safety: a stray context.WithValue with key string("user")
// would silently fail to satisfy UserFromContext.
func WithUser(ctx context.Context, claims *Claims) context.Context {
	return context.WithValue(ctx, userKey{}, claims)
}

type Middleware struct {
	secret    string
	blacklist *Blacklist
}

func NewMiddleware(secret string, blacklist *Blacklist) *Middleware {
	return &Middleware{secret: secret, blacklist: blacklist}
}

// Verify parses tokenStr as a JWT and runs the blacklist checks, returning
// the claims or a client-safe error (its message is what an HTTP 401 body
// should carry). It performs no HTTP handling and never rejects a request
// itself: enforcement is the spec-validation middleware's job
// (internal/specmw), which calls Verify through its credential-extraction
// step and matches the outcome against the operation's security clause.
func (m *Middleware) Verify(ctx context.Context, tokenStr string) (*Claims, error) {
	claims, err := ParseToken(tokenStr, m.secret)
	if err != nil {
		logctx.From(ctx).Warn("auth.jwt_invalid", "error", err.Error())
		return nil, errors.New("Invalid or expired token")
	}

	checks := m.blacklist.CheckAll(ctx, claims.JTI, claims.ID, claims.IssuedAt)
	if checks.IsBlacklisted {
		logctx.From(ctx).Warn("auth.token_revoked", "user_id", claims.ID, "jti", claims.JTI)
		return nil, errors.New("Token has been revoked")
	}
	if checks.IsUserTokenInvalidated {
		logctx.From(ctx).Warn("auth.token_user_invalidated", "user_id", claims.ID, "jti", claims.JTI)
		return nil, errors.New("Token has been invalidated")
	}
	if checks.IsTokenBeforeServerBoot {
		logctx.From(ctx).Warn("auth.token_pre_boot", "user_id", claims.ID, "jti", claims.JTI)
		return nil, errors.New("Token issued before server restart")
	}
	return claims, nil
}

func UserFromContext(ctx context.Context) *Claims {
	user, _ := ctx.Value(userKey{}).(*Claims)
	return user
}
