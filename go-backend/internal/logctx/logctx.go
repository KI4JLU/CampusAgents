// Package logctx returns slog loggers enriched with well-known fields pulled
// from context.Context: request_id, user_id, kb_id. Pipeline code uses
//
//	logctx.From(ctx).Info("rag.vector_search", "duration_ms", d, ...)
//
// The base logger defaults to slog.Default() but can be overridden via
// SetBase for tests.
//
// Hot-path optimization: handlers that emit many log lines per request should
// call Attach(ctx) once after WithUser/WithKB are set. Attach precomputes the
// enriched logger and stores it in ctx so subsequent From(ctx) calls reuse it
// instead of re-running the four-`.With(...)` chain on every line. WithUser
// and WithKB automatically refresh the cache when one is already attached, so
// downstream value changes stay reflected without an explicit re-Attach.
package logctx

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/requestid"
	"go.opentelemetry.io/otel/trace"
)

type kbKey struct{}
type userKey struct{}
type loggerKey struct{}
type captureKey struct{}

// UserIDCapture is a per-request mutable cell that outer middleware
// (typically the access-log wrapper around the mux) allocates so it can
// observe the authenticated user ID that gets stamped onto a CHILD
// context further down the chain. Without this hop the outer middleware
// only ever sees the original request, never the auth-augmented one.
//
// Concurrency: written by the auth middleware on the request goroutine
// before the inner handler runs, read by the same goroutine after it
// returns — no synchronization needed. The pointer is shared, the
// surrounding ctx tree is not.
type UserIDCapture struct {
	UserID string
}

// WithUserIDCapture attaches c so SetCapturedUserID can find it later.
// Outer middleware calls this once per request; nothing downstream needs
// to be aware of the capture mechanism beyond auth, which writes to it.
func WithUserIDCapture(ctx context.Context, c *UserIDCapture) context.Context {
	if ctx == nil || c == nil {
		return ctx
	}
	return context.WithValue(ctx, captureKey{}, c)
}

// SetCapturedUserID writes id into the UserIDCapture attached to ctx.
// No-op when no capture was attached (e.g., a request bypassing the
// logging middleware), so it is safe to call unconditionally.
func SetCapturedUserID(ctx context.Context, id string) {
	if ctx == nil || id == "" {
		return
	}
	if c, ok := ctx.Value(captureKey{}).(*UserIDCapture); ok && c != nil {
		c.UserID = id
	}
}

var base atomic.Pointer[slog.Logger]

// SetBase overrides the logger From returns when no enrichment applies.
// Pass nil to revert to slog.Default(). Safe for use in tests.
func SetBase(l *slog.Logger) {
	base.Store(l)
}

func baseLogger() *slog.Logger {
	if l := base.Load(); l != nil {
		return l
	}
	return slog.Default()
}

// buildLogger materializes the request-stable portion of the enriched logger
// (request_id / user_id / kb_id). Span IDs are deliberately excluded — they
// can change mid-request as new spans are started, so From applies them on
// each call instead of caching them.
func buildLogger(ctx context.Context) *slog.Logger {
	l := baseLogger()
	if ctx == nil {
		return l
	}
	if id := requestid.FromContext(ctx); id != "" {
		l = l.With("request_id", id)
	}
	if uid, ok := ctx.Value(userKey{}).(string); ok && uid != "" {
		l = l.With("user_id", uid)
	}
	if kbID, ok := ctx.Value(kbKey{}).(string); ok && kbID != "" {
		l = l.With("kb_id", kbID)
	}
	return l
}

// Attach precomputes the request-stable logger from ctx and stores it under a
// private key so From(ctx) can reuse it without rebuilding. Call once at the
// top of a handler after WithUser/WithKB are set. Idempotent — re-attaching
// rebuilds from the current ctx state.
func Attach(ctx context.Context) context.Context {
	if ctx == nil {
		return ctx
	}
	return context.WithValue(ctx, loggerKey{}, buildLogger(ctx))
}

// From returns a logger pre-decorated with request_id / user_id / kb_id when
// each is present in ctx, plus trace_id / span_id when a span is active.
// Reuses the cached logger from Attach when available.
func From(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return baseLogger()
	}
	var l *slog.Logger
	if cached, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok && cached != nil {
		l = cached
	} else {
		l = buildLogger(ctx)
	}
	if span := trace.SpanFromContext(ctx); span.SpanContext().IsValid() {
		sc := span.SpanContext()
		l = l.With("trace_id", sc.TraceID().String(), "span_id", sc.SpanID().String())
	}
	return l
}

// WithUser attaches user_id to ctx for downstream From calls. Empty input is a no-op.
// Refreshes the cached logger when one is already attached so the new user_id
// is reflected without callers needing to re-Attach.
func WithUser(ctx context.Context, userID string) context.Context {
	if ctx == nil || userID == "" {
		return ctx
	}
	ctx = context.WithValue(ctx, userKey{}, userID)
	if _, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		ctx = context.WithValue(ctx, loggerKey{}, buildLogger(ctx))
	}
	return ctx
}

// WithKB attaches kb_id to ctx for downstream From calls. Empty input is a no-op.
// Refreshes the cached logger when one is already attached so the new kb_id
// is reflected without callers needing to re-Attach.
func WithKB(ctx context.Context, kbID string) context.Context {
	if ctx == nil || kbID == "" {
		return ctx
	}
	ctx = context.WithValue(ctx, kbKey{}, kbID)
	if _, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		ctx = context.WithValue(ctx, loggerKey{}, buildLogger(ctx))
	}
	return ctx
}
