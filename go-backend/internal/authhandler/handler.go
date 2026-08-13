// Package authhandler implements the auth endpoints: login, logout, refresh,
// the public provider listing (strict, generated from the OpenAPI spec), and
// the OIDC broker (oidc.go, mounted manually).
package authhandler

import (
	"context"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/adminproviders"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/api"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/auth"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/logctx"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/users"
)

// dummyHash is used in constant-time comparisons when the requested username
// does not exist, preventing user enumeration via timing attacks.
const dummyHash = "$2b$10$5gEqnyws7hSy6rfxgXeOOuOKRY7T9VdAnZx/mpHtO8XfwJVq5ASIi"

// ---------------------------------------------------------------------------
// Store interface
// ---------------------------------------------------------------------------

// Store is the persistence interface used by this handler package.
type Store interface {
	GetUserByUsername(ctx context.Context, username string) (*users.UserRow, error)
	CreateUser(ctx context.Context, data users.UserCreate) (*users.UserRow, error)
	// GetActiveAuthProviders backs GET /api/auth/providers so the login page
	// can detect the configured OIDC provider.
	GetActiveAuthProviders(ctx context.Context) ([]adminproviders.AuthProviderRow, error)
}

// ---------------------------------------------------------------------------
// Handler
// ---------------------------------------------------------------------------

// LoginFailureRecorder is called to record a failed login attempt for
// rate-limiting purposes. The context is the request context (request id and
// logging attached by the middleware chain).
type LoginFailureRecorder interface {
	RecordFailure(ctx context.Context)
}

// Handler holds the dependencies for the auth endpoints.
type Handler struct {
	store          Store
	jwtSecret      string
	blacklist      *auth.Blacklist
	failRecorders  []LoginFailureRecorder
	allowedOrigins []string
}

// NewHandler creates a new Handler.
func NewHandler(store Store, jwtSecret string, blacklist *auth.Blacklist) *Handler {
	return &Handler{store: store, jwtSecret: jwtSecret, blacklist: blacklist}
}

// SetAllowedOrigins configures the CORS allowlist used to validate the OIDC
// `return_to` parameter, so the cross-origin widget-test portal can complete an
// SSO round-trip back to itself without opening a redirect vulnerability.
func (h *Handler) SetAllowedOrigins(origins []string) {
	h.allowedOrigins = origins
}

// SetFailureRecorders configures rate limiters that should be incremented
// only on failed login attempts (not on every request).
func (h *Handler) SetFailureRecorders(recorders ...LoginFailureRecorder) {
	h.failRecorders = recorders
}

// recordLoginFailure increments all configured rate-limit counters.
func (h *Handler) recordLoginFailure(ctx context.Context) {
	for _, rec := range h.failRecorders {
		rec.RecordFailure(ctx)
	}
}

// oidcActive reports whether an OIDC provider is currently active. Used to
// auto-disable local password login (except for superadmins) when SSO is
// configured. A lookup error fails open (treated as "no OIDC") so a transient
// DB hiccup never locks superadmins out of the breakglass.
func (h *Handler) oidcActive(ctx context.Context) bool {
	rows, err := h.store.GetActiveAuthProviders(ctx)
	if err != nil {
		return false
	}
	for _, p := range rows {
		if p.Type == OIDCProviderType {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// POST /api/auth/login
// ---------------------------------------------------------------------------

// Login implements the login operation. The spec middleware has already
// validated the body shape (username/password present, length bounds).
func (h *Handler) Login(ctx context.Context, request api.LoginRequestObject) (api.LoginResponseObject, error) {
	req := request.Body

	// Look up user by username.
	user, err := h.store.GetUserByUsername(ctx, req.Username)
	if err != nil {
		return api.Login500JSONResponse{
			InternalErrorJSONResponse: api.InternalErrorJSONResponse{Error: "Internal server error"},
		}, nil
	}

	// Attempt local authentication. Local password login is disabled — for
	// everyone, with no breakglass — whenever an OIDC provider is active or
	// DISABLE_LOCAL_AUTH=true, so an OIDC-only deployment has no password path.
	localAllowed := localAuthEnabled(h.oidcActive(ctx))
	localAuthOK := false

	if user != nil &&
		user.PasswordHash != "" &&
		!strings.HasPrefix(user.PasswordHash, "$ldap$") &&
		localAllowed {
		// User exists with a local password hash — compare directly.
		err = bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password))
		localAuthOK = (err == nil)
	} else {
		// No local user or local auth disabled for this user — run a dummy
		// compare to spend the same time, preventing username enumeration via
		// timing.
		_ = bcrypt.CompareHashAndPassword([]byte(dummyHash), []byte(req.Password))
	}

	if localAuthOK {
		resp, err := h.loginResponse(ctx, user)
		if err != nil {
			return api.Login500JSONResponse{
				InternalErrorJSONResponse: api.InternalErrorJSONResponse{Error: "Failed to generate token"},
			}, nil
		}
		return api.Login200JSONResponse(resp), nil
	}

	// Local authentication failed. (OIDC is handled by the dedicated
	// /api/auth/oidc/* broker endpoints, not this password flow.)
	logctx.From(ctx).Warn("auth.login_failed",
		"username", req.Username,
		"user_exists", user != nil,
	)
	h.recordLoginFailure(ctx)
	return api.Login401JSONResponse{Error: "Invalid username or password"}, nil
}

// ---------------------------------------------------------------------------
// POST /api/auth/logout
// ---------------------------------------------------------------------------

// Logout revokes the presented JWT. The spec middleware has already verified
// the token and stored the parsed claims in the context — a forged or expired
// token never reaches this handler, so we can only ever blacklist a JTI that
// belonged to a genuine, still-valid session.
func (h *Handler) Logout(ctx context.Context, _ api.LogoutRequestObject) (api.LogoutResponseObject, error) {
	claims := auth.UserFromContext(ctx)
	if claims == nil {
		logctx.From(ctx).Info("auth.logout", "decoded", false)
		return api.Logout200JSONResponse{Message: "Logged out"}, nil
	}

	expTime := time.Unix(claims.ExpiresAt, 0)
	h.blacklist.Add(ctx, claims.JTI, expTime)

	logctx.From(ctx).Info("auth.logout", "user_id", claims.ID, "jti", claims.JTI)
	return api.Logout200JSONResponse{Message: "Logged out"}, nil
}

// ---------------------------------------------------------------------------
// POST /api/auth/refresh
// ---------------------------------------------------------------------------

// RefreshToken rotates the presented JWT: issues a fresh one and blacklists
// the old JTI.
func (h *Handler) RefreshToken(ctx context.Context, _ api.RefreshTokenRequestObject) (api.RefreshTokenResponseObject, error) {
	claims := auth.UserFromContext(ctx)
	if claims == nil {
		return api.RefreshToken401JSONResponse{
			UnauthorizedJSONResponse: api.UnauthorizedJSONResponse{Error: "Authentication required"},
		}, nil
	}

	tokenStr, err := h.signToken(claims.ID, claims.Username, claims.Role)
	if err != nil {
		return api.RefreshToken500JSONResponse{
			InternalErrorJSONResponse: api.InternalErrorJSONResponse{Error: "Failed to generate token"},
		}, nil
	}

	// Invalidate the previous token so a leaked copy can't keep working until
	// its natural expiry alongside the freshly issued one. signToken mints a
	// new JTI, so the new token isn't affected by blacklisting the old one.
	h.blacklist.Add(ctx, claims.JTI, time.Unix(claims.ExpiresAt, 0))

	resp := api.LoginResponse{Token: tokenStr}
	resp.User.Id = claims.ID
	resp.User.Username = claims.Username
	resp.User.Role = api.Role(claims.Role)
	return api.RefreshToken200JSONResponse(resp), nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// loginResponse signs a fresh token for user and builds the shared login/
// refresh wire shape.
func (h *Handler) loginResponse(ctx context.Context, user *users.UserRow) (api.LoginResponse, error) {
	tokenStr, err := h.signToken(user.ID, user.Username, user.Role)
	if err != nil {
		logctx.From(ctx).Error("auth.login_token_error", "user_id", user.ID, "error", err.Error())
		return api.LoginResponse{}, err
	}
	logctx.From(ctx).Info("auth.login_success",
		"user_id", user.ID,
		"username", user.Username,
		"role", user.Role,
		"method", user.AuthMethod,
	)
	resp := api.LoginResponse{Token: tokenStr}
	resp.User.Id = user.ID
	resp.User.Username = user.Username
	resp.User.Role = api.Role(user.Role)
	resp.User.AuthMethod = user.AuthMethod
	return resp, nil
}

func (h *Handler) signToken(userID, username, role string) (string, error) {
	jti := uuid.NewString()
	now := time.Now()
	exp := now.Add(24 * time.Hour)

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":       userID,
		"username": username,
		"role":     role,
		"jti":      jti,
		"iat":      now.Unix(),
		"exp":      exp.Unix(),
	})

	return token.SignedString([]byte(h.jwtSecret))
}
