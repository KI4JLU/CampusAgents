package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/golang-jwt/jwt/v5"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/adminproviders"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/agents"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/apidocs"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/apikeyauth"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/apikeys"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/auth"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/authhandler"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/modelproxy"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/users"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/widgets"
)

const testJWTSecret = "router-test-secret-0123456789abcdef"

// ---------------------------------------------------------------------------
// Fakes — just enough store surface for the router to boot and for requests
// that pass the middleware to terminate without Postgres/Redis.
// ---------------------------------------------------------------------------

type fakeBlacklistStore struct{}

func (fakeBlacklistStore) Get(context.Context, string) (string, error) {
	return "", errors.New("not found")
}
func (fakeBlacklistStore) Set(context.Context, string, any, time.Duration) error { return nil }
func (fakeBlacklistStore) Exists(context.Context, ...string) (int64, error)      { return 0, nil }
func (fakeBlacklistStore) Pipeline(context.Context, func(auth.PipelineExecer) error) error {
	return errors.New("no pipeline in tests") // forces the individual-check fallback
}

type fakeAuthStore struct{}

func (fakeAuthStore) GetUserByUsername(context.Context, string) (*users.UserRow, error) {
	return nil, nil
}
func (fakeAuthStore) CreateUser(context.Context, users.UserCreate) (*users.UserRow, error) {
	return nil, nil
}
func (fakeAuthStore) GetActiveAuthProviders(context.Context) ([]adminproviders.AuthProviderRow, error) {
	return nil, nil
}

type fakeUsersStore struct{}

func (fakeUsersStore) GetUserByID(context.Context, string) (*users.UserRow, error) { return nil, nil }
func (fakeUsersStore) GetUserByUsername(context.Context, string) (*users.UserRow, error) {
	return nil, nil
}
func (fakeUsersStore) SearchUserByTerm(context.Context, string) (*users.UserRow, error) {
	return nil, nil
}
func (fakeUsersStore) UpdateUser(context.Context, string, users.UserUpdate) (*users.UserRow, error) {
	return nil, nil
}

type fakeApiKeysStore struct{}

func (fakeApiKeysStore) CreateApiKey(context.Context, apikeys.ApiKeyCreate) (*apikeys.ApiKeyRow, error) {
	return &apikeys.ApiKeyRow{}, nil
}
func (fakeApiKeysStore) GetApiKeysByUser(context.Context, string) ([]apikeys.ApiKeyRow, error) {
	return nil, nil
}
func (fakeApiKeysStore) CountApiKeysByUser(context.Context, string) (int, error) { return 0, nil }
func (fakeApiKeysStore) DeleteApiKey(context.Context, string, string) (bool, error) {
	return false, nil
}

type fakeApiKeyAuthStore struct{}

func (fakeApiKeyAuthStore) GetApiKeysByPrefix(context.Context, string) ([]apikeyauth.ApiKeyCandidate, error) {
	return nil, nil
}
func (fakeApiKeyAuthStore) GetUserByID(context.Context, string) (*apikeyauth.UserInfo, error) {
	return nil, nil
}
func (fakeApiKeyAuthStore) UpdateApiKeyLastUsed(context.Context, string) error { return nil }

// fakeBlobStore backs both widgets.Store/AgentStore and agents.Store, plus the
// CountByAgent reverse-reference lookup.
type fakeBlobStore struct{}

func (fakeBlobStore) List(context.Context) ([]json.RawMessage, error)      { return nil, nil }
func (fakeBlobStore) Get(context.Context, string) (json.RawMessage, error) { return nil, nil }
func (fakeBlobStore) Delete(context.Context, string) (bool, error)         { return false, nil }
func (fakeBlobStore) CountByAgent(context.Context, string) (int, error)    { return 0, nil }
func (fakeBlobStore) Upsert(_ context.Context, _ string, data []byte) (json.RawMessage, error) {
	return json.RawMessage(data), nil
}

// newTestRouter wires the full production handler tree (spec validation,
// credential extraction, generated routes, escape hatches) over fakes.
func newTestRouter(t *testing.T) http.Handler {
	t.Helper()

	blacklist := auth.NewBlacklist(fakeBlacklistStore{}, false)
	authH := authhandler.NewHandler(fakeAuthStore{}, testJWTSecret, blacklist)
	proxyH := modelproxy.NewHandler("", "http://upstream.invalid")

	handler, err := newRouter(routerDeps{
		auth:     authH,
		users:    users.NewHandler(fakeUsersStore{}),
		apiKeys:  apikeys.NewHandler(fakeApiKeysStore{}),
		widgets:  widgets.NewHandler(fakeBlobStore{}, fakeBlobStore{}, proxyH),
		agents:   agents.NewHandler(fakeBlobStore{}, fakeBlobStore{}),
		proxy:    proxyH,
		jwtMW:    auth.NewMiddleware(testJWTSecret, blacklist),
		apiKeyMW: apikeyauth.NewMiddleware(fakeApiKeyAuthStore{}),
	})
	if err != nil {
		t.Fatalf("newRouter: %v", err)
	}
	return handler
}

// signTestJWT mints a token the way authhandler.signToken does, so the
// middleware accepts it.
func signTestJWT(t *testing.T, role string) string {
	t.Helper()
	now := time.Now()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"id":       "00000000-0000-0000-0000-000000000001",
		"username": "tester",
		"role":     role,
		"jti":      "test-jti",
		"iat":      now.Unix(),
		"exp":      now.Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

// ---------------------------------------------------------------------------
// TestSpecSecurity walks EVERY operation in the embedded OpenAPI document and
// asserts the router's auth behaviour matches the spec's security clauses:
// operations without `security: []` must reject an unauthenticated request
// with 401, public ones must not. Because the walk is driven by the spec, a
// new endpoint is covered the moment it is added there.
// ---------------------------------------------------------------------------

func TestSpecSecurity(t *testing.T) {
	loader := openapi3.NewLoader()
	doc, err := loader.LoadFromData(apidocs.Raw())
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}

	handler := newTestRouter(t)

	for path, item := range doc.Paths.Map() {
		for method, op := range item.Operations() {
			url := strings.ReplaceAll(path, "{id}", "test-id")

			// An absent security clause inherits the root default (bearerJWT);
			// an explicit empty list marks the operation public.
			protected := op.Security == nil || len(*op.Security) > 0

			t.Run(method+" "+path, func(t *testing.T) {
				rec := httptest.NewRecorder()
				req := httptest.NewRequest(method, url, strings.NewReader("{}"))
				req.Header.Set("Content-Type", "application/json")
				handler.ServeHTTP(rec, req)

				if protected && rec.Code != http.StatusUnauthorized {
					t.Fatalf("unauthenticated %s %s = %d, want 401 (spec requires auth)", method, url, rec.Code)
				}
				if !protected && (rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden) {
					t.Fatalf("public %s %s = %d, must not require auth", method, url, rec.Code)
				}
			})
		}
	}
}

// TestSpecSecurity_RoleScopes: a bearerJWT scope in the spec names the
// required role. An admin JWT must be refused (403) on superadmin-scoped
// operations; a superadmin JWT must pass the middleware (the fake store then
// yields 404, proving the request reached the handler).
func TestSpecSecurity_RoleScopes(t *testing.T) {
	handler := newTestRouter(t)

	for _, target := range []string{"/api/widgets/test-id", "/api/agents/test-id"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodDelete, target, nil)
		req.Header.Set("Authorization", "Bearer "+signTestJWT(t, auth.RoleAdmin))
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("admin DELETE %s = %d, want 403", target, rec.Code)
		}

		rec = httptest.NewRecorder()
		req = httptest.NewRequest(http.MethodDelete, target, nil)
		req.Header.Set("Authorization", "Bearer "+signTestJWT(t, auth.RoleSuperAdmin))
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("superadmin DELETE %s = %d, want 404 from the empty fake store", target, rec.Code)
		}
	}
}

// TestSpecSecurity_JWTAccepted: a plain valid JWT passes the middleware on a
// bearerJWT operation and reaches the handler.
func TestSpecSecurity_JWTAccepted(t *testing.T) {
	handler := newTestRouter(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/widgets", nil)
	req.Header.Set("Authorization", "Bearer "+signTestJWT(t, auth.RoleAdmin))
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/widgets with valid JWT = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !strings.Contains(got, `"widgets":[]`) {
		t.Fatalf("unexpected body: %s", got)
	}
}
