package httputil

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/logctx"
)

// errorResponse is the wire shape for every JSON error body: {"error": msg}.
// A typed struct (vs. a per-call map[string]string) makes the contract
// explicit and avoids a map allocation on every error response.
type errorResponse struct {
	Error string `json:"error"`
}

// WriteJSONCtx writes v as JSON with the given status code and logs encode
// failures with the request context (request_id, user_id, kb_id, trace_id).
// Prefer this over WriteJSON in HTTP handlers — pass r.Context() so the rare
// "encode after WriteHeader" failure log is correlated with the request.
func WriteJSONCtx(ctx context.Context, w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logctx.From(ctx).ErrorContext(ctx, "httputil: JSON encode failed", "error", err)
	}
}

// WriteErrorCtx writes a JSON error response: {"error": msg}, with request
// context for any failure log.
func WriteErrorCtx(ctx context.Context, w http.ResponseWriter, status int, msg string) {
	WriteJSONCtx(ctx, w, status, errorResponse{Error: msg})
}

// WriteInternalErrorCtx logs the raw err internally (so the unredacted detail
// is never lost) and returns HTTP 500 with a SanitizeError-redacted message to
// the client. The raw error only ever appears in the server logs, correlated
// with the request via ctx.
func WriteInternalErrorCtx(ctx context.Context, w http.ResponseWriter, err error) {
	logctx.From(ctx).ErrorContext(ctx, "httputil: internal error", "error", err)
	WriteErrorCtx(ctx, w, http.StatusInternalServerError, SanitizeError(err))
}

// WriteJSON writes v as JSON with the given status code. Logs encode failures
// without request correlation. Prefer WriteJSONCtx in HTTP handlers.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("httputil: JSON encode failed", "error", err)
	}
}

// WriteError writes a JSON error response: {"error": msg}. Prefer
// WriteErrorCtx in HTTP handlers.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, errorResponse{Error: msg})
}

// WriteInternalError logs the raw err internally and returns HTTP 500 with a
// SanitizeError-redacted message. Prefer WriteInternalErrorCtx in HTTP handlers
// so the log line carries request correlation.
func WriteInternalError(w http.ResponseWriter, err error) {
	slog.Error("httputil: internal error", "error", err)
	WriteError(w, http.StatusInternalServerError, SanitizeError(err))
}

// SSEWriteTimeout is the sliding per-frame write deadline for SSE streams.
// It replaces the server-wide WriteTimeout (which EnableSSE opts out of)
// with a bound on how long a single frame write may block: a half-open
// client that stopped reading without sending a FIN otherwise blocks
// w.Write forever once the kernel send buffer fills — pinning the handler
// goroutine and, on relay paths, keeping the worker producing into a dead
// connection.
//
// The window must comfortably exceed the longest legitimate gap between
// frames on ANY SSE path, because http.ResponseController write deadlines
// cannot be extended once exceeded — an expired deadline poisons the
// connection even if no write was attempted while it was expired. Current
// worst cases: sserelay heartbeats every 15s; chat streams abort after
// 120s of upstream inactivity. 5 minutes clears both with a wide margin.
const SSEWriteTimeout = 5 * time.Minute

// RearmSSEWriteDeadline slides the SSE write deadline forward. Call before
// each SSE frame write. The SetWriteDeadline error is ignored for the same
// reason as in EnableSSE.
func RearmSSEWriteDeadline(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(SSEWriteTimeout))
}

// EnableSSE prepares w for Server-Sent Events: it sets the standard SSE
// response headers (including X-Accel-Buffering: no so nginx does not buffer
// the stream) and swaps the server-wide WriteTimeout for the first sliding
// SSEWriteTimeout window (frame writers re-arm it via RearmSSEWriteDeadline).
// SSE streams stay open far longer than any request timeout; per-handler
// inactivity / context cancellation governs their lifetime instead. Call once,
// before writing any event. The returned ResponseController can be used to
// Flush; callers that flush another way may ignore it.
func EnableSSE(w http.ResponseWriter) *http.ResponseController {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	// SetWriteDeadline only errors when the ResponseWriter doesn't support
	// deadlines, which never happens for the stdlib server these handlers run
	// under; match the prior call sites and ignore it.
	_ = rc.SetWriteDeadline(time.Now().Add(SSEWriteTimeout))
	return rc
}

// pathHeuristic flags strings that look like absolute filesystem paths
// (Unix `/segment...`, Windows `C:\...` or UNC `\\host\share`). The Unix
// branch requires the path to follow whitespace, a paren, or the start of
// the string so plain `/` or `\` characters in valid error text — HTTP
// status codes like "400/Bad Request", URL paths embedded in third-party
// errors, regex patterns, math expressions — pass through. A single
// segment is enough: short bare paths like `/tmp`, `/var`, `/etc` show
// up in standard-library "open /tmp: permission denied" and Unix-socket
// errors like "dial unix /tmp/redis.sock", and leaking even a one-segment
// path is the kind of internal detail this filter exists to redact.
var pathHeuristic = regexp.MustCompile(`(?:^|[\s(])(?:/[A-Za-z0-9._-]+){1,}|[A-Za-z]:\\|\\\\[A-Za-z0-9._-]+\\`)

// SanitizeError returns a client-safe string for err. It replaces messages
// that look like they contain file paths, SQL/DB errors, or credential-
// adjacent terms with generic phrasing so internal details do not leak to
// HTTP clients. Returns a generic message when err is nil or empty.
func SanitizeError(err error) string {
	if err == nil {
		return "An unexpected error occurred."
	}
	msg := err.Error()
	if msg == "" || msg == "An unknown error occurred" {
		return "An unexpected error occurred."
	}
	lower := strings.ToLower(msg)

	if pathHeuristic.MatchString(msg) {
		return "An internal error occurred. Please contact support."
	}
	if strings.Contains(lower, "sql") || strings.Contains(lower, "database") {
		return "A database error occurred. Please try again later."
	}
	// Sensitive terms: only match qualified phrases — bare "connection",
	// "token", etc. show up in legitimate user-facing messages ("connection
	// timeout: retry in 30s", "token limit exceeded for model X"). The
	// phrases below are scoped to credential / infrastructure leaks.
	for _, term := range []string{
		"password", "secret", "credential",
		"api_key", "api key",
		"auth token", "api token", "jwt token", "bearer token",
		"session token", "access token", "refresh token",
		"db connection", "database connection", "redis connection",
		"tls connection", "tcp connection",
	} {
		if strings.Contains(lower, term) {
			return "A system error occurred. Please contact support."
		}
	}
	return msg
}
