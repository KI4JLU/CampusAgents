// Package specmw enforces the OpenAPI spec (internal/apidocs/openapi.yaml) at
// runtime. Two middlewares compose, in order:
//
//  1. ExtractCredentials resolves the Authorization header into auth.Claims —
//     JWT or jrag_ API key — WITHOUT enforcing anything. It only parses,
//     verifies, and stashes the result (or the client-safe failure reason)
//     in the request context.
//  2. Validator matches every request against the spec and rejects it when
//     the route is unknown, parameters or body fail schema validation, or the
//     operation's security requirements are not met by the extracted
//     credentials. Security therefore lives in the spec, not in per-route
//     wrapper code: `security: []` is public, `bearerJWT` requires a JWT,
//     `apiKey` a jrag_ key, and a bearerJWT scope names the required role
//     (superadmin passes every role scope, mirroring auth.RequireRole).
package specmw

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/apikeyauth"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/auth"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/httputil"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/logctx"
)

// Security scheme names — must match components.securitySchemes in the spec.
const (
	schemeJWT    = "bearerJWT"
	schemeAPIKey = "apiKey"
)

// maxBodyBytes caps every request body before the validator buffers it for
// schema validation. Mirrors the 1 MiB cap the widget/agent upsert handlers
// already enforced; the largest legitimate bodies (chat histories) stay far
// below it.
const maxBodyBytes = 1 << 20

type credKind int

const (
	credNone credKind = iota
	credJWT
	credAPIKey
)

// credential is the outcome of ExtractCredentials for one request: which kind
// of credential was presented, the claims if it verified, or the client-safe
// reason it did not.
type credential struct {
	kind   credKind
	claims *auth.Claims
	err    error
}

// authError returns the client-safe error a failed security requirement
// should surface: the concrete verification failure when a credential was
// presented, otherwise the generic prompt to authenticate.
func (c *credential) authError() error {
	if c.err != nil {
		return c.err
	}
	return errors.New("Authentication required")
}

type credCtxKey struct{}

func credFromContext(ctx context.Context) *credential {
	if c, ok := ctx.Value(credCtxKey{}).(*credential); ok {
		return c
	}
	return &credential{}
}

// ExtractCredentials resolves the Authorization header into auth claims
// without enforcing anything (enforcement is the Validator's job, driven by
// the spec). Bearer tokens with the jrag_ prefix are verified as API keys,
// all others as JWTs — the same dispatch the old combinedAuth used. On
// success the claims land in the context via auth.WithUser, so handlers and
// the access log keep working unchanged.
func ExtractCredentials(jwtMW *auth.Middleware, keyMW *apikeyauth.Middleware) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

			ctx := r.Context()
			cred := &credential{}

			header := r.Header.Get("Authorization")
			token, isBearer := strings.CutPrefix(header, "Bearer ")
			switch {
			case header == "":
				// Anonymous request; public routes allow it, protected ones 401.
			case !isBearer:
				cred.err = errors.New("Authentication required")
			case strings.HasPrefix(token, "jrag_"):
				claims, err := keyMW.Verify(ctx, token)
				if err != nil {
					cred.err = err
				} else {
					cred.kind, cred.claims = credAPIKey, claims
				}
			default:
				claims, err := jwtMW.Verify(ctx, token)
				if err != nil {
					cred.err = err
				} else {
					cred.kind, cred.claims = credJWT, claims
				}
			}

			if cred.claims != nil {
				ctx = auth.WithUser(ctx, cred.claims)
				logctx.SetCapturedUserID(ctx, cred.claims.ID)
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(ctx, credCtxKey{}, cred)))
		})
	}
}

// errInsufficientRole marks a security failure that is a role problem (403),
// not a credential problem (401).
var errInsufficientRole = errors.New("Insufficient permissions")

// authenticate is the openapi3filter.AuthenticationFunc: it decides whether
// the extracted credential satisfies one security requirement of the matched
// operation. Operations listing several requirements (e.g. bearerJWT OR
// apiKey on the model proxy) pass when any single one does.
func authenticate(_ context.Context, input *openapi3filter.AuthenticationInput) error {
	r := input.RequestValidationInput.Request
	cred := credFromContext(r.Context())

	switch input.SecuritySchemeName {
	case schemeJWT:
		if cred.kind != credJWT {
			return cred.authError()
		}
		for _, role := range input.Scopes {
			// A scope names the required role. Superadmin passes every role
			// scope, mirroring the old auth.RequireRole bypass.
			if cred.claims.Role != role && cred.claims.Role != auth.RoleSuperAdmin {
				logctx.From(r.Context()).Warn("auth.permission_denied",
					"user_id", cred.claims.ID,
					"user_role", cred.claims.Role,
					"required_role", role,
					"path", r.URL.Path,
					"method", r.Method,
				)
				return errInsufficientRole
			}
		}
		return nil
	case schemeAPIKey:
		if cred.kind != credAPIKey {
			return cred.authError()
		}
		return nil
	default:
		return fmt.Errorf("unknown security scheme %q", input.SecuritySchemeName)
	}
}

// Validator builds the spec-driven request middleware from the raw OpenAPI
// document. It must wrap ONLY routes that appear in the spec — mount routes
// like /api/docs outside it.
func Validator(specBytes []byte) (func(http.Handler) http.Handler, error) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(specBytes)
	if err != nil {
		return nil, fmt.Errorf("specmw: parse OpenAPI document: %w", err)
	}
	if err := doc.Validate(loader.Context); err != nil {
		return nil, fmt.Errorf("specmw: invalid OpenAPI document: %w", err)
	}

	return nethttpmiddleware.OapiRequestValidatorWithOptions(doc, &nethttpmiddleware.Options{
		// The single server entry is `/` (same origin); Host matching against
		// it would reject every request.
		DoNotValidateServers: true,
		Options: openapi3filter.Options{
			AuthenticationFunc: authenticate,
		},
		ErrorHandlerWithOpts: writeError,
	}), nil
}

// writeError maps validation failures onto the API's {"error": …} wire shape:
// 401/403 for security, 400 for schema violations, 404/405 for unknown
// routes.
func writeError(ctx context.Context, err error, w http.ResponseWriter, r *http.Request, opts nethttpmiddleware.ErrorHandlerOpts) {
	var secErr *openapi3filter.SecurityRequirementsError
	var reqErr *openapi3filter.RequestError

	switch {
	case errors.As(err, &secErr):
		status := http.StatusUnauthorized
		msg := "Authentication required"
		for _, e := range secErr.Errors {
			if errors.Is(e, errInsufficientRole) {
				// The credential was valid, the role was not: a 403, and the
				// role message wins over any 401 text from a sibling scheme.
				status = http.StatusForbidden
				msg = errInsufficientRole.Error()
				break
			}
			if e != nil && msg == "Authentication required" {
				msg = e.Error()
			}
		}
		httputil.WriteErrorCtx(ctx, w, status, msg)
	case errors.As(err, &reqErr):
		// Schema violations produce verbose multi-line messages; the first
		// line carries the useful part (same trim nethttp-middleware applies).
		msg, _, _ := strings.Cut(reqErr.Error(), "\n")
		httputil.WriteErrorCtx(ctx, w, http.StatusBadRequest, msg)
	case opts.MatchedRoute == nil:
		if errors.Is(err, routers.ErrMethodNotAllowed) {
			httputil.WriteErrorCtx(ctx, w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		httputil.WriteErrorCtx(ctx, w, http.StatusNotFound, "not found")
	default:
		logctx.From(ctx).ErrorContext(ctx, "specmw: unexpected validation error", "error", err)
		httputil.WriteErrorCtx(ctx, w, http.StatusInternalServerError, "An unexpected error occurred.")
	}
}
