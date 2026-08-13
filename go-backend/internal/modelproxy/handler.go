// Package modelproxy proxies the frontend's /api/models and /api/chat calls to
// the HRZ (Uni Gießen) OpenAI-compatible endpoint, server-side, so the API key
// never reaches the browser. It mirrors the request/response contract the
// frontend already expects (previously served by the Vite dev middleware).
package modelproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/api"
	"github.com/KI4JLU/CampusAgents/go-backend/internal/httputil"
)

// Handler proxies model + chat requests to the upstream OpenAI-compatible API.
type Handler struct {
	apiKey  string
	baseURL string
	client  *http.Client
}

// NewHandler builds a proxy targeting baseURL with the given bearer apiKey.
func NewHandler(apiKey, baseURL string) *Handler {
	return &Handler{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		// No overall timeout: chat streaming responses are long-lived. The
		// upstream request carries its own context for cancellation.
		client: &http.Client{},
	}
}

// notConfiguredMsg is the 503 body shared by the strict ListModels and the
// http-style chat path when KI_API_KEY is missing.
const notConfiguredMsg = "KI_API_KEY ist nicht gesetzt. Bitte in der Backend-Konfiguration eintragen."

func (h *Handler) configured(w http.ResponseWriter, r *http.Request) bool {
	if h.apiKey == "" {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusServiceUnavailable,
			map[string]string{"error": notConfiguredMsg})
		return false
	}
	return true
}

func (h *Handler) upstream(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var rdr *bytes.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.apiKey)
	req.Header.Set("Content-Type", "application/json")
	return h.client.Do(req)
}

// ---------------------------------------------------------------------------
// GET /api/models
// ---------------------------------------------------------------------------

// ListModels implements GET /api/models (strict).
func (h *Handler) ListModels(ctx context.Context, _ api.ListModelsRequestObject) (api.ListModelsResponseObject, error) {
	if h.apiKey == "" {
		return api.ListModels503JSONResponse{
			NotConfiguredJSONResponse: api.NotConfiguredJSONResponse{Error: notConfiguredMsg},
		}, nil
	}
	resp, err := h.upstream(ctx, http.MethodGet, "/models", nil)
	if err != nil {
		return api.ListModels502JSONResponse{
			BadGatewayJSONResponse: api.BadGatewayJSONResponse{
				Error: fmt.Sprintf("Modelle konnten nicht geladen werden: %s", err.Error()),
			},
		}, nil
	}
	defer resp.Body.Close()

	var upstream struct {
		Data []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			OwnedBy string `json:"owned_by"`
			Created int64  `json:"created"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&upstream); err != nil {
		return api.ListModels502JSONResponse{
			BadGatewayJSONResponse: api.BadGatewayJSONResponse{Error: "Modelle konnten nicht gelesen werden."},
		}, nil
	}

	models := make([]api.ModelInfo, 0, len(upstream.Data))
	for _, m := range upstream.Data {
		models = append(models, api.ModelInfo{Id: m.ID, Name: m.Name, OwnedBy: m.OwnedBy, Created: m.Created})
	}
	// Nach Anzeigename sortieren (fällt auf die ID zurück, falls kein Name).
	sort.Slice(models, func(i, j int) bool {
		ni, nj := models[i].Name, models[j].Name
		if ni == "" {
			ni = models[i].Id
		}
		if nj == "" {
			nj = models[j].Id
		}
		return strings.ToLower(ni) < strings.ToLower(nj)
	})

	return api.ListModels200JSONResponse{Models: models}, nil
}

// ---------------------------------------------------------------------------
// POST /api/chat
// ---------------------------------------------------------------------------

// ChatMessage is one turn in a chat completion (role + content).
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model string `json:"model"`
	// knowledgeBaseId is the field the embedded widget/admin send; it aliases
	// Model so callers can use either name.
	KnowledgeBaseID string        `json:"knowledgeBaseId"`
	Messages        []ChatMessage `json:"messages"`
	MaxTokens       int           `json:"maxTokens"`
	Stream          bool          `json:"stream"`
}

// ChatParams is a chat request whose model and token cap have already been
// resolved from trusted state (the admin's selection, or a widget's stored
// config) rather than taken verbatim from an untrusted client.
type ChatParams struct {
	Model     string
	MaxTokens *int
	Messages  []ChatMessage
	Stream    bool
}

// upstreamChatRequest is the OpenAI-shaped body sent to the HRZ endpoint.
type upstreamChatRequest struct {
	Model     string        `json:"model"`
	Messages  []ChatMessage `json:"messages"`
	MaxTokens *int          `json:"max_tokens,omitempty"`
	Stream    bool          `json:"stream"`
}

// Chat handles POST /api/chat (streaming and non-streaming) for authenticated
// callers, taking the model + token cap directly from the request body.
func (h *Handler) Chat(w http.ResponseWriter, r *http.Request) {
	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": "Ungültiger Request"})
		return
	}

	model := req.Model
	if model == "" {
		model = req.KnowledgeBaseID
	}

	var maxTokens *int
	if req.MaxTokens > 0 {
		mt := req.MaxTokens
		maxTokens = &mt
	}

	h.ProxyChat(w, r, ChatParams{
		Model:     model,
		MaxTokens: maxTokens,
		Messages:  req.Messages,
		Stream:    req.Stream,
	})
}

// ProxyChat validates already-resolved params and proxies them to the upstream
// LLM. Callers (the authenticated /api/chat handler and the public per-widget
// chat handler) resolve Model/MaxTokens from trusted state before calling this.
func (h *Handler) ProxyChat(w http.ResponseWriter, r *http.Request, p ChatParams) {
	if !h.configured(w, r) {
		return
	}
	if p.Model == "" {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": "Kein Modell ausgewählt."})
		return
	}
	if len(p.Messages) == 0 {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadRequest, map[string]string{"error": "Keine Nachrichten übergeben."})
		return
	}

	body, err := json.Marshal(upstreamChatRequest{
		Model:     p.Model,
		Messages:  p.Messages,
		MaxTokens: p.MaxTokens,
		Stream:    p.Stream,
	})
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "Request konnte nicht erstellt werden."})
		return
	}

	if p.Stream {
		h.chatStream(w, r, body)
		return
	}
	h.chatOnce(w, r, body)
}

func (h *Handler) chatOnce(w http.ResponseWriter, r *http.Request, body []byte) {
	resp, err := h.upstream(r.Context(), http.MethodPost, "/chat/completions", body)
	if err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadGateway,
			map[string]string{"error": fmt.Sprintf("Antwort konnte nicht generiert werden: %s", err.Error())})
		return
	}
	defer resp.Body.Close()

	var upstream struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&upstream); err != nil {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusBadGateway,
			map[string]string{"error": "Antwort konnte nicht gelesen werden."})
		return
	}

	reply := ""
	var finishReason *string
	if len(upstream.Choices) > 0 {
		reply = upstream.Choices[0].Message.Content
		finishReason = upstream.Choices[0].FinishReason
	}
	httputil.WriteJSONCtx(r.Context(), w, http.StatusOK, map[string]any{"reply": reply, "finishReason": finishReason})
}

func (h *Handler) chatStream(w http.ResponseWriter, r *http.Request, body []byte) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		httputil.WriteJSONCtx(r.Context(), w, http.StatusInternalServerError, map[string]string{"error": "Streaming wird nicht unterstützt."})
		return
	}
	send := func(v any) {
		raw, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", raw)
		flusher.Flush()
	}

	resp, err := h.upstream(r.Context(), http.MethodPost, "/chat/completions", body)
	if err != nil {
		send(map[string]string{"error": fmt.Sprintf("Antwort konnte nicht generiert werden: %s", err.Error())})
		return
	}
	defer resp.Body.Close()

	var finishReason *string
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) > 0 {
			if c := chunk.Choices[0].Delta.Content; c != "" {
				send(map[string]string{"content": c})
			}
			if chunk.Choices[0].FinishReason != nil {
				finishReason = chunk.Choices[0].FinishReason
			}
		}
	}
	send(map[string]any{"done": true, "finishReason": finishReason})
}
