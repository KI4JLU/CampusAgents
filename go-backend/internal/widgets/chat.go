package widgets

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/httputil"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/modelproxy"
)

// ChatProxy is the subset of the model proxy the widget chat endpoint needs.
// *modelproxy.Handler satisfies it.
type ChatProxy interface {
	ProxyChat(w http.ResponseWriter, r *http.Request, p modelproxy.ChatParams)
}

// Rate limit for the PUBLIC per-widget chat endpoint. This endpoint is
// unauthenticated by design (the widget is embedded on public pages), so a
// per-IP fixed-window limiter is the first line of abuse defence.
const (
	chatRateWindow = time.Minute
	chatRateMax    = 30
)

// chatChatRequest is the body the embedded widget.js posts. Model and token cap
// are intentionally NOT read from here — they come from the widget's stored
// config so a client cannot point the public endpoint at an arbitrary model.
type chatChatRequest struct {
	Messages []modelproxy.ChatMessage `json:"messages"`
	Stream   bool                     `json:"stream"`
}

// Chat handles POST /api/widgets/{id}/chat (PUBLIC). It resolves the model and
// token limit from the widget's stored config, rate-limits per IP, and proxies
// to the upstream LLM.
func (h *Handler) Chat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	raw, err := h.store.Get(r.Context(), id)
	if err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	if raw == nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusNotFound, "Widget nicht gefunden.")
		return
	}

	var wgt widget
	if err := json.Unmarshal(raw, &wgt); err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	if wgt.Status != "active" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusForbidden, "Dieses Widget ist derzeit nicht verfügbar.")
		return
	}

	// The model and token cap come from the widget's linked agent (Ebene 1),
	// not from the client — a caller cannot point the public endpoint at an
	// arbitrary model or raise the token cap.
	brain, err := h.resolveBrain(r.Context(), wgt)
	if err != nil {
		httputil.WriteInternalErrorCtx(r.Context(), w, err)
		return
	}
	if strings.TrimSpace(brain.Model) == "" {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "Für dieses Widget ist kein Agent/Modell konfiguriert.")
		return
	}

	if !h.limiter.allow(clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		httputil.WriteErrorCtx(r.Context(), w, http.StatusTooManyRequests, "Zu viele Anfragen. Bitte später erneut versuchen.")
		return
	}

	var req chatChatRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes)).Decode(&req); err != nil {
		httputil.WriteErrorCtx(r.Context(), w, http.StatusBadRequest, "Ungültiger Request")
		return
	}

	var maxTokens *int
	if brain.MaxTokens > 0 {
		mt := brain.MaxTokens
		maxTokens = &mt
	}

	h.chat.ProxyChat(w, r, modelproxy.ChatParams{
		Model:     brain.Model,
		MaxTokens: maxTokens,
		Messages:  req.Messages,
		Stream:    req.Stream,
	})
}

// clientIP extracts the caller's IP, preferring the nginx-set forwarding
// headers over the direct connection address.
func clientIP(r *http.Request) string {
	if v := r.Header.Get("X-Real-IP"); v != "" {
		return strings.TrimSpace(v)
	}
	if v := r.Header.Get("X-Forwarded-For"); v != "" {
		return strings.TrimSpace(strings.Split(v, ",")[0])
	}
	return r.RemoteAddr
}

// rateLimiter is a per-key fixed-window counter. A background sweeper drops
// expired entries so the map cannot grow unbounded under many distinct IPs.
type rateLimiter struct {
	mu     sync.Mutex
	hits   map[string]*rlEntry
	window time.Duration
	max    int
}

type rlEntry struct {
	count   int
	resetAt time.Time
}

func newRateLimiter(window time.Duration, max int) *rateLimiter {
	rl := &rateLimiter{hits: make(map[string]*rlEntry), window: window, max: max}
	go rl.sweep()
	return rl
}

// allow records a hit for key and reports whether it is within the quota.
func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	e := rl.hits[key]
	if e == nil || now.After(e.resetAt) {
		e = &rlEntry{resetAt: now.Add(rl.window)}
		rl.hits[key] = e
	}
	e.count++
	return e.count <= rl.max
}

func (rl *rateLimiter) sweep() {
	ticker := time.NewTicker(rl.window)
	defer ticker.Stop()
	for now := range ticker.C {
		rl.mu.Lock()
		for k, e := range rl.hits {
			if now.After(e.resetAt) {
				delete(rl.hits, k)
			}
		}
		rl.mu.Unlock()
	}
}
