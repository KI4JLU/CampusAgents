package widgets

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/modelproxy"
)

// ---------------------------------------------------------------------------
// Integration test doubles
//
// These tests exercise the full widget-chat pipeline end to end:
//
//   POST /api/widgets/{id}/chat
//     → resolve the widget's Knowledge-Base from stored config
//     → proxy to the (Knowledge-Base) upstream with that KB as the model
//     → stream/return the answer (incl. its inline sources) back to the client
//
// Two seams are faked so the test is hermetic (no Postgres, no HRZ/justRAG):
//   - fakeStore stands in for the Postgres widgets store.
//   - a httptest server stands in for the OpenAI-compatible Knowledge-Base
//     endpoint; it records the request it received so we can assert the backend
//     sent the widget's KB (not a client-supplied value) and returns a canned
//     answer that carries sources in the message content (exactly how justRAG
//     returns them — see the "**Quellen:**" block).
// ---------------------------------------------------------------------------

// fakeStore is an in-memory widgets.Store.
type fakeStore struct {
	data map[string]json.RawMessage
}

func (s *fakeStore) List(context.Context) ([]json.RawMessage, error) {
	out := []json.RawMessage{}
	for _, v := range s.data {
		out = append(out, v)
	}
	return out, nil
}

func (s *fakeStore) Get(_ context.Context, id string) (json.RawMessage, error) {
	v, ok := s.data[id]
	if !ok {
		return nil, nil
	}
	return v, nil
}

func (s *fakeStore) Upsert(_ context.Context, id string, data []byte) (json.RawMessage, error) {
	s.data[id] = json.RawMessage(data)
	return json.RawMessage(data), nil
}

func (s *fakeStore) Delete(_ context.Context, id string) (bool, error) {
	if _, ok := s.data[id]; !ok {
		return false, nil
	}
	delete(s.data, id)
	return true, nil
}

// widgetJSON builds a stored-widget blob with the fields the chat path reads.
func widgetJSON(id, kb, status string, maxTokens int) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"id":              id,
		"name":            "Test",
		"knowledgeBaseId": kb,
		"routing":         "public",
		"status":          status,
		"icon":            "Bot",
		"config":          map[string]any{"maxTokensPerAnswer": maxTokens, "title": "Test"},
	})
	return b
}

// capturedUpstream records the last request body the fake KB endpoint saw.
type capturedUpstream struct {
	body []byte
}

// newFakeKB returns a fake Knowledge-Base endpoint. It records the incoming
// request and answers either a single JSON completion or an SSE stream,
// depending on the request's "stream" flag. The answer text carries a sources
// block, mirroring how justRAG embeds citations in the content.
func newFakeKB(cap *capturedUpstream, answer string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cap.body, _ = io.ReadAll(r.Body)

		var req struct {
			Stream bool `json:"stream"`
		}
		_ = json.Unmarshal(cap.body, &req)

		if !req.Stream {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []any{map[string]any{
					"message":       map[string]any{"role": "assistant", "content": answer},
					"finish_reason": "stop",
				}},
			})
			return
		}

		// Stream the answer as two deltas (prose, then the sources block) so the
		// test also proves multi-chunk content — including sources — is relayed.
		w.Header().Set("Content-Type", "text/event-stream")
		mid := strings.Index(answer, "**Quellen:**")
		if mid <= 0 {
			mid = len(answer)
		}
		writeSSE(w, map[string]string{"content": answer[:mid]})
		writeSSE(w, map[string]string{"content": answer[mid:]})
		writeSSE(w, map[string]any{"choices": []any{map[string]any{"finish_reason": "stop"}}})
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
}

func writeSSE(w http.ResponseWriter, delta any) {
	// The upstream frames content as OpenAI chunks: {"choices":[{"delta":{...}}]}.
	var chunk map[string]any
	if d, ok := delta.(map[string]string); ok {
		chunk = map[string]any{"choices": []any{map[string]any{"delta": d}}}
	} else {
		chunk = delta.(map[string]any)
	}
	raw, _ := json.Marshal(chunk)
	_, _ = io.WriteString(w, "data: "+string(raw)+"\n\n")
}

// fakeAgentStore is an in-memory widgets.AgentStore. A nil map (or missing id)
// resolves to no agent, exercising the legacy fallback path.
type fakeAgentStore struct {
	data map[string]json.RawMessage
}

func (s *fakeAgentStore) Get(_ context.Context, id string) (json.RawMessage, error) {
	v, ok := s.data[id]
	if !ok {
		return nil, nil
	}
	return v, nil
}

// agentJSON builds a stored-agent blob with the brain fields the widget path reads.
func agentJSON(id, model string, maxTokens int) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"id":        id,
		"name":      "Agent",
		"model":     model,
		"maxTokens": maxTokens,
		"rules":     []any{},
	})
	return b
}

// newTestHandler wires a widgets.Handler over the fake store + a modelproxy
// pointed at the fake KB endpoint, and returns a mux that routes the chat path
// (so r.PathValue("id") is populated exactly as in production). A nil agent
// store exercises the legacy fallback (brain resolved from the widget itself).
func newTestHandler(t *testing.T, store Store, kbURL string) http.Handler {
	t.Helper()
	return newTestHandlerWithAgents(t, store, nil, kbURL)
}

func newTestHandlerWithAgents(t *testing.T, store Store, agents AgentStore, kbURL string) http.Handler {
	t.Helper()
	proxy := modelproxy.NewHandler("test-key", kbURL)
	h := NewHandler(store, agents, proxy)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/widgets/{id}/chat", h.Chat)
	return mux
}

// widgetLinkedJSON builds a widget blob that references an agent by id. Its own
// legacy knowledgeBaseId is set to a decoy so the test proves the model is
// taken from the agent, not the widget.
func widgetLinkedJSON(id, agentID, decoyKB, status string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"id":              id,
		"name":            "Test",
		"agentId":         agentID,
		"knowledgeBaseId": decoyKB,
		"routing":         "public",
		"status":          status,
		"icon":            "Bot",
		"config":          map[string]any{"maxTokensPerAnswer": 111, "title": "Test"},
	})
	return b
}

func chatRequest(id, body string) *http.Request {
	return httptest.NewRequest(http.MethodPost, "/api/widgets/"+id+"/chat", strings.NewReader(body))
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// The happy path: a question is answered using the widget's KB, and the sources
// embedded in the answer are relayed to the client verbatim.
func TestChat_ResolvesKBAndRelaysAnswerWithSources(t *testing.T) {
	const kb = "kb-1ca5e21b-9b38-4be2-b242-073d50e3c3bb"
	const answer = "Das PE Programm ist das Fortbildungsprogramm der JLU [1].\n\n" +
		"**Quellen:**\n[1] JLU-Fortbildungsprogramm_2026.pdf, p. 11-13"

	store := &fakeStore{data: map[string]json.RawMessage{
		"pe-bot": widgetJSON("pe-bot", kb, "active", 1500),
	}}
	var cap capturedUpstream
	kbSrv := newFakeKB(&cap, answer)
	defer kbSrv.Close()
	handler := newTestHandler(t, store, kbSrv.URL)

	// The client tries to override the model and the token cap — both must be
	// ignored in favour of the widget's stored config.
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, chatRequest("pe-bot",
		`{"messages":[{"role":"user","content":"Was ist das PE Programm?"}],"stream":false,"model":"HACK","maxTokens":99999}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}

	var resp struct {
		Reply        string `json:"reply"`
		FinishReason string `json:"finishReason"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	// The answer and its sources block reach the client unchanged.
	if !strings.Contains(resp.Reply, "PE Programm") {
		t.Errorf("reply missing answer text: %q", resp.Reply)
	}
	if !strings.Contains(resp.Reply, "**Quellen:**") || !strings.Contains(resp.Reply, "Fortbildungsprogramm_2026.pdf") {
		t.Errorf("reply missing sources block: %q", resp.Reply)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("finishReason = %q, want stop", resp.FinishReason)
	}

	// The backend queried the widget's KB, not the client-supplied model, and
	// applied the widget's token cap, not the client's.
	var up struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
	}
	if err := json.Unmarshal(cap.body, &up); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	if up.Model != kb {
		t.Errorf("upstream model = %q, want the widget's KB %q", up.Model, kb)
	}
	if up.MaxTokens != 1500 {
		t.Errorf("upstream max_tokens = %d, want 1500 (widget config, not client 99999)", up.MaxTokens)
	}
}

// Phase 3: a widget linked to an agent runs on the AGENT's model and token cap,
// not the widget's own (legacy) knowledgeBaseId / maxTokensPerAnswer.
func TestChat_ResolvesModelFromLinkedAgent(t *testing.T) {
	const agentModel = "kb-from-agent"
	store := &fakeStore{data: map[string]json.RawMessage{
		// widget's own knowledgeBaseId is a decoy that must be ignored.
		"w1": widgetLinkedJSON("w1", "agent-1", "kb-DECOY", "active"),
	}}
	agentSt := &fakeAgentStore{data: map[string]json.RawMessage{
		"agent-1": agentJSON("agent-1", agentModel, 1234),
	}}
	var cap capturedUpstream
	kbSrv := newFakeKB(&cap, "ok")
	defer kbSrv.Close()
	handler := newTestHandlerWithAgents(t, store, agentSt, kbSrv.URL)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, chatRequest("w1",
		`{"messages":[{"role":"user","content":"hi"}],"stream":false}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	var up struct {
		Model     string `json:"model"`
		MaxTokens int    `json:"max_tokens"`
	}
	if err := json.Unmarshal(cap.body, &up); err != nil {
		t.Fatalf("decode upstream body: %v", err)
	}
	if up.Model != agentModel {
		t.Errorf("upstream model = %q, want the agent's model %q (not the widget decoy)", up.Model, agentModel)
	}
	if up.MaxTokens != 1234 {
		t.Errorf("upstream max_tokens = %d, want 1234 (agent, not widget's 111)", up.MaxTokens)
	}
}

// A widget whose agentId points at a missing agent degrades to its own legacy
// fields instead of failing — the migration-window safety net.
func TestChat_FallsBackToWidgetWhenAgentMissing(t *testing.T) {
	store := &fakeStore{data: map[string]json.RawMessage{
		"w1": widgetLinkedJSON("w1", "ghost-agent", "kb-legacy", "active"),
	}}
	agentSt := &fakeAgentStore{data: map[string]json.RawMessage{}} // agent not found
	var cap capturedUpstream
	kbSrv := newFakeKB(&cap, "ok")
	defer kbSrv.Close()
	handler := newTestHandlerWithAgents(t, store, agentSt, kbSrv.URL)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, chatRequest("w1", `{"messages":[{"role":"user","content":"hi"}],"stream":false}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var up struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(cap.body, &up)
	if up.Model != "kb-legacy" {
		t.Errorf("upstream model = %q, want widget's legacy kb %q", up.Model, "kb-legacy")
	}
}

// Streaming: content chunks — including the sources block — are relayed as SSE.
func TestChat_StreamsAnswerAndSources(t *testing.T) {
	const answer = "Antwort mit Beleg [2].\n\n**Quellen:**\n[2] doc.pdf, p. 5"
	store := &fakeStore{data: map[string]json.RawMessage{
		"pe-bot": widgetJSON("pe-bot", "kb-real", "active", 2000),
	}}
	var cap capturedUpstream
	kbSrv := newFakeKB(&cap, answer)
	defer kbSrv.Close()
	handler := newTestHandler(t, store, kbSrv.URL)

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, chatRequest("pe-bot",
		`{"messages":[{"role":"user","content":"Frage"}],"stream":true}`))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	content, done := parseSSE(t, rec.Body.String())
	if !strings.Contains(content, "Antwort mit Beleg") {
		t.Errorf("streamed content missing prose: %q", content)
	}
	if !strings.Contains(content, "**Quellen:**") || !strings.Contains(content, "doc.pdf") {
		t.Errorf("streamed content missing sources: %q", content)
	}
	if !done {
		t.Errorf("stream never sent a done event")
	}
}

// parseSSE joins the streamed "content" deltas and reports whether a done event
// arrived, mirroring how widget.js consumes the stream.
func parseSSE(t *testing.T, raw string) (content string, done bool) {
	t.Helper()
	var sb strings.Builder
	for _, block := range strings.Split(raw, "\n\n") {
		line := strings.TrimSpace(block)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			continue
		}
		var ev struct {
			Content string `json:"content"`
			Done    bool   `json:"done"`
		}
		if err := json.Unmarshal([]byte(payload), &ev); err != nil {
			continue
		}
		sb.WriteString(ev.Content)
		if ev.Done {
			done = true
		}
	}
	return sb.String(), done
}

func TestChat_UnknownWidgetReturns404(t *testing.T) {
	store := &fakeStore{data: map[string]json.RawMessage{}}
	handler := newTestHandler(t, store, "http://unused")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, chatRequest("nope", `{"messages":[{"role":"user","content":"hi"}]}`))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestChat_PausedWidgetIsRefused(t *testing.T) {
	store := &fakeStore{data: map[string]json.RawMessage{
		"paused-bot": widgetJSON("paused-bot", "kb-real", "paused", 2000),
	}}
	handler := newTestHandler(t, store, "http://unused")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, chatRequest("paused-bot", `{"messages":[{"role":"user","content":"hi"}]}`))

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestChat_WidgetWithoutKBReturns400(t *testing.T) {
	store := &fakeStore{data: map[string]json.RawMessage{
		"no-kb": widgetJSON("no-kb", "", "active", 2000),
	}}
	handler := newTestHandler(t, store, "http://unused")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, chatRequest("no-kb", `{"messages":[{"role":"user","content":"hi"}]}`))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

// The public chat endpoint is unauthenticated, so it is rate-limited per IP.
func TestChat_RateLimited(t *testing.T) {
	const answer = "ok"
	store := &fakeStore{data: map[string]json.RawMessage{
		"pe-bot": widgetJSON("pe-bot", "kb-real", "active", 2000),
	}}
	var cap capturedUpstream
	kbSrv := newFakeKB(&cap, answer)
	defer kbSrv.Close()

	// Build a handler whose limiter allows only 2 requests per window.
	proxy := modelproxy.NewHandler("test-key", kbSrv.URL)
	h := &Handler{store: store, chat: proxy, limiter: newRateLimiter(time.Minute, 2)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/widgets/{id}/chat", h.Chat)

	send := func() int {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, chatRequest("pe-bot", `{"messages":[{"role":"user","content":"hi"}],"stream":false}`))
		return rec.Code
	}

	if c := send(); c != http.StatusOK {
		t.Fatalf("request 1: status = %d, want 200", c)
	}
	if c := send(); c != http.StatusOK {
		t.Fatalf("request 2: status = %d, want 200", c)
	}
	if c := send(); c != http.StatusTooManyRequests {
		t.Fatalf("request 3: status = %d, want 429", c)
	}
}

// The rate limiter opens a fresh window after the previous one elapses.
func TestRateLimiter_WindowReset(t *testing.T) {
	rl := newRateLimiter(20*time.Millisecond, 1)
	if !rl.allow("ip") {
		t.Fatal("first call should be allowed")
	}
	if rl.allow("ip") {
		t.Fatal("second call within window should be blocked")
	}
	time.Sleep(30 * time.Millisecond)
	if !rl.allow("ip") {
		t.Fatal("call after window reset should be allowed")
	}
}
