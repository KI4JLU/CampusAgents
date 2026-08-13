// Package safego provides helpers for launching goroutines with panic recovery.
package safego

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/logctx"
)

// Go launches fn in a new goroutine with panic recovery. If fn panics the
// stack trace is logged and the goroutine terminates cleanly instead of
// crashing the process. Prefer GoCtx when the caller has a request context
// in scope so panic logs carry request_id / user_id / kb_id correlation.
func Go(fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("goroutine panic recovered",
					"error", r,
					"stack", string(debug.Stack()),
				)
			}
		}()
		fn()
	}()
}

// GoCtx is like Go but logs panic recoveries through the request-scoped
// logger attached to ctx (via logctx.Attach upstream). The captured
// request_id / user_id / kb_id / trace_id fields are carried into the
// panic log line so a recovered crash inside a chat post-response task
// can be correlated with the originating request. ctx is used for
// logging correlation only — fn captures its own context via closure if
// it needs cancellation.
func GoCtx(ctx context.Context, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logctx.From(ctx).Error("goroutine panic recovered",
					"error", r,
					"stack", string(debug.Stack()),
				)
			}
		}()
		fn()
	}()
}

// RecoverError recovers from a panic and stores it as an error in *errp.
// The stack trace is embedded in the error so the caller — which has the
// structured context (request_id, kb_id, …) — decides how to surface it.
// Logging here would duplicate the caller's eventual error log without that
// context, since safego has no view of the request scope.
//
// Intended for use as a deferred call inside goroutines where panics must be
// captured as errors (e.g. goroutines coordinated via WaitGroup).
//
//	var err error
//	go func() {
//	    defer safego.RecoverError(&err)
//	    // ... work ...
//	}()
//
// Passing a nil *error is a misuse — without somewhere to surface the panic,
// it would be silently swallowed. We log a generic error in that case so the
// process still notices, but callers should always pass a real pointer.
func RecoverError(errp *error) {
	if r := recover(); r != nil {
		err := fmt.Errorf("panic: %v\n%s", r, debug.Stack())
		if errp != nil {
			*errp = err
			return
		}
		slog.Error("goroutine panic recovered with nil errp", "error", err)
	}
}
