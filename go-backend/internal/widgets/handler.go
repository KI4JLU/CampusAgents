// Package widgets provides handlers for managing chatbot widget configurations.
//
// A widget is stored as a single JSONB blob (the full admin object). Endpoints:
//
//   - GET    /api/widgets           (JWT) — full list for the admin UI
//   - PUT    /api/widgets/{id}      (JWT) — create/update from the admin UI
//   - DELETE /api/widgets/{id}      (superadmin) — remove a widget
//   - GET    /api/widgets/{id}      (public) — reduced config for the embedded widget.js
//   - POST   /api/widgets/{id}/chat (public) — per-widget chat (chat.go, mounted manually)
//
// The public projection mirrors the previous Node backend (server/widgets-store.mjs):
// Lucide icon names are mapped to Material-Symbols names and only enabled rules
// are exposed, as plain text.
package widgets

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/api"
)

// maxBodyBytes caps the public chat endpoint's body reads (chat.go); admin
// bodies are capped globally by the spec middleware.
const maxBodyBytes = 1 << 20 // 1 MiB

// Store is the database interface required by Handler.
type Store interface {
	List(ctx context.Context) ([]json.RawMessage, error)
	Get(ctx context.Context, id string) (json.RawMessage, error)
	Upsert(ctx context.Context, id string, data []byte) (json.RawMessage, error)
	Delete(ctx context.Context, id string) (bool, error)
}

// AgentStore resolves an agent's stored JSON by id. It is satisfied by
// *agents.PGStore. The widget path uses it to source the "brain" (model,
// system prompt, rules, token cap) from the linked agent (Ebene 1) rather than
// from the widget's own embedded config.
type AgentStore interface {
	Get(ctx context.Context, id string) (json.RawMessage, error)
}

// agentBrain is the subset of an agent the widget path consumes. Field names
// match the agent JSON shape (see migrations/main/0003_agents.sql).
type agentBrain struct {
	Model        string       `json:"model"`
	SystemPrompt string       `json:"systemPrompt"`
	Rules        []widgetRule `json:"rules"`
	MaxTokens    int          `json:"maxTokens"`
}

// Handler holds the dependencies for the widget endpoints.
type Handler struct {
	store   Store
	agents  AgentStore
	chat    ChatProxy
	limiter *rateLimiter
}

// NewHandler creates a Handler using the given widget Store, agent store (for
// resolving the linked brain), and chat proxy.
func NewHandler(store Store, agents AgentStore, chat ChatProxy) *Handler {
	return &Handler{
		store:   store,
		agents:  agents,
		chat:    chat,
		limiter: newRateLimiter(chatRateWindow, chatRateMax),
	}
}

// resolveBrain returns the "brain" a widget runs on. The agent linked via
// widget.agentId (Ebene 1) is the source of truth. During the migration window
// a widget may still carry its own legacy fields and may not be linked yet, so
// this falls back to the widget's embedded config when there is no agentId or
// the referenced agent has gone missing — nothing breaks mid-rollout. A genuine
// store error is propagated so the caller can return 500.
func (h *Handler) resolveBrain(ctx context.Context, wgt widget) (agentBrain, error) {
	// Legacy fallback: the fields that used to live on the widget itself.
	fallback := agentBrain{Model: wgt.KnowledgeBaseID}
	if wgt.Config != nil {
		fallback.SystemPrompt = wgt.Config.StartPrompt
		fallback.Rules = wgt.Config.Rules
		fallback.MaxTokens = wgt.Config.MaxTokensPerAnswer
	}

	if h.agents == nil || strings.TrimSpace(wgt.AgentID) == "" {
		return fallback, nil
	}
	raw, err := h.agents.Get(ctx, wgt.AgentID)
	if err != nil {
		return agentBrain{}, err
	}
	if raw == nil {
		// Linked agent was deleted/missing — degrade to the widget's own fields.
		return fallback, nil
	}
	var b agentBrain
	if err := json.Unmarshal(raw, &b); err != nil {
		return agentBrain{}, err
	}
	// Defensive: if the agent somehow has no model, keep the widget's legacy one.
	if strings.TrimSpace(b.Model) == "" {
		b.Model = fallback.Model
	}
	return b, nil
}

// widgetRule is one configurable behaviour rule (only enabled rules are public).
type widgetRule struct {
	Text    string `json:"text"`
	Enabled bool   `json:"enabled"`
}

// widgetConfig holds the presentation/behaviour fields relevant to the backend.
// Unknown fields (rate limits, etc.) are ignored on parse but preserved in
// storage because the stored JSON keeps every field the admin UI sent.
type widgetConfig struct {
	StartPrompt        string       `json:"startPrompt"`
	Templates          []string     `json:"templates"`
	Rules              []widgetRule `json:"rules"`
	FeedbackButtons    bool         `json:"feedbackButtons"`
	MaxTokensPerAnswer int          `json:"maxTokensPerAnswer"`
	Title              string       `json:"title"`
	Greeting           string       `json:"greeting"`
	AccentColor        string       `json:"accentColor"`
	Position           string       `json:"position"`
}

// widget is the subset of the stored object the backend parses for the public
// projection and the chat path. Config is a pointer so a missing "config" key
// can be told apart from an empty one. AgentID (Ebene 1) links the widget to
// the agent that provides its brain; empty on widgets not yet migrated/linked.
type widget struct {
	ID              string        `json:"id"`
	AgentID         string        `json:"agentId"`
	KnowledgeBaseID string        `json:"knowledgeBaseId"`
	Routing         string        `json:"routing"`
	Status          string        `json:"status"`
	Icon            string        `json:"icon"`
	Name            string        `json:"name"`
	Config          *widgetConfig `json:"config"`
}

// iconMap translates Lucide icon names (admin UI) to Material-Symbols names
// (the font widget.js loads). Mirrors ICON_MAP in the former Node backend.
var iconMap = map[string]string{
	"Bot":           "smart_toy",
	"Languages":     "translate",
	"LineChart":     "analytics",
	"Headset":       "headset_mic",
	"MessageSquare": "chat",
	"Brain":         "psychology",
	"Sparkles":      "auto_awesome",
	"Headphones":    "headphones",
	"Globe":         "language",
	"MessageCircle": "chat_bubble",
}

func mapIcon(name string) string {
	if m, ok := iconMap[name]; ok {
		return m
	}
	return "smart_toy"
}

// ListWidgets implements GET /api/widgets (admin) — returns { "widgets": [ ...full... ] }.
func (h *Handler) ListWidgets(ctx context.Context, _ api.ListWidgetsRequestObject) (api.ListWidgetsResponseObject, error) {
	rows, err := h.store.List(ctx)
	if err != nil {
		return nil, err
	}
	widgets := make([]api.Widget, 0, len(rows))
	for _, raw := range rows {
		var w api.Widget
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil, err
		}
		widgets = append(widgets, w)
	}
	return api.ListWidgets200JSONResponse{Widgets: widgets}, nil
}

// UpsertWidget implements PUT /api/widgets/{id} (admin) — creates or replaces
// a widget. The typed body round-trips through api.Widget, whose
// AdditionalProperties (on the widget and its config) preserve fields the
// backend does not model, so the stored JSONB keeps everything the admin UI
// sent. Status enum and config presence are enforced by the spec middleware.
func (h *Handler) UpsertWidget(ctx context.Context, request api.UpsertWidgetRequestObject) (api.UpsertWidgetResponseObject, error) {
	body := request.Body

	if msg := validate(request.Id, body); msg != "" {
		return api.UpsertWidget400JSONResponse{
			BadRequestJSONResponse: api.BadRequestJSONResponse{Error: msg},
		}, nil
	}

	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	stored, err := h.store.Upsert(ctx, request.Id, data)
	if err != nil {
		return nil, err
	}
	var out api.Widget
	if err := json.Unmarshal(stored, &out); err != nil {
		return nil, err
	}
	return api.UpsertWidget200JSONResponse(out), nil
}

// DeleteWidget implements DELETE /api/widgets/{id} (superadmin) — removes a
// widget. Returns 204 on success and 404 when no widget with that id exists.
// The superadmin role is enforced by the spec middleware (bearerJWT scope),
// not here.
func (h *Handler) DeleteWidget(ctx context.Context, request api.DeleteWidgetRequestObject) (api.DeleteWidgetResponseObject, error) {
	existed, err := h.store.Delete(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	if !existed {
		return api.DeleteWidget404JSONResponse{
			NotFoundJSONResponse: api.NotFoundJSONResponse{Error: "Widget nicht gefunden."},
		}, nil
	}
	return api.DeleteWidget204Response{}, nil
}

// GetPublicWidgetConfig implements GET /api/widgets/{id} (public) — the
// reduced config for the embedded widget.js. 404 when the widget does not
// exist.
func (h *Handler) GetPublicWidgetConfig(ctx context.Context, request api.GetPublicWidgetConfigRequestObject) (api.GetPublicWidgetConfigResponseObject, error) {
	raw, err := h.store.Get(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	if raw == nil {
		// no-store on the 404: it is heuristically cacheable by default, so a
		// shared cache could otherwise pin "widget does not exist" past the point
		// where it does — and widget.js reacts to that by silently rendering its
		// generic built-in defaults.
		return api.GetPublicWidgetConfig404JSONResponse{
			Body:    api.Error{Error: "Widget nicht gefunden."},
			Headers: api.GetPublicWidgetConfig404ResponseHeaders{CacheControl: "no-store"},
		}, nil
	}

	var wgt widget
	if err := json.Unmarshal(raw, &wgt); err != nil {
		return nil, err
	}
	brain, err := h.resolveBrain(ctx, wgt)
	if err != nil {
		return nil, err
	}
	// no-cache (not no-store): shared caches may keep a copy but must
	// revalidate every time, so publishing a change in the admin panel takes
	// effect on the next page load. Without the header those caches fall back
	// to heuristic freshness and pin embedded widgets to a stale config —
	// silently, since widget.js only detects a *failed* fetch, not an old one.
	return api.GetPublicWidgetConfig200JSONResponse{
		Body:    toPublic(wgt, brain),
		Headers: api.GetPublicWidgetConfig200ResponseHeaders{CacheControl: "no-cache"},
	}, nil
}

// validate enforces the invariants the verbatim upsert needs beyond what the
// schema already guarantees: the body id must match the URL, and name must be
// non-blank (the schema only requires its presence).
func validate(id string, wgt *api.Widget) string {
	if wgt.Id != id {
		return "Widget-id im Body muss zur URL passen."
	}
	if strings.TrimSpace(wgt.Name) == "" {
		return "name is required"
	}
	return ""
}

// toPublic projects a stored widget onto the public config for widget.js. The
// presentation fields (title, greeting, colour, templates, …) come from the
// widget itself (the front, Ebene 3); the brain fields (model, system prompt,
// rules, token cap) come from the resolved agent (Ebene 1). The wire shape is
// unchanged, so widget.js and the standalone page need no changes.
func toPublic(wgt widget, brain agentBrain) api.WidgetPublicConfig {
	cfg := wgt.Config
	if cfg == nil {
		cfg = &widgetConfig{}
	}

	templates := cfg.Templates
	if templates == nil {
		templates = []string{}
	}

	rules := []string{}
	for _, ru := range brain.Rules {
		if ru.Enabled {
			rules = append(rules, ru.Text)
		}
	}

	var maxTokens *int
	if brain.MaxTokens > 0 {
		mt := brain.MaxTokens
		maxTokens = &mt
	}

	return api.WidgetPublicConfig{
		Id:              wgt.ID,
		Status:          api.WidgetPublicConfigStatus(wgt.Status),
		KnowledgeBaseId: brain.Model,
		Routing:         wgt.Routing,
		Title:           cfg.Title,
		Greeting:        cfg.Greeting,
		AccentColor:     cfg.AccentColor,
		Position:        cfg.Position,
		Icon:            mapIcon(wgt.Icon),
		Templates:       templates,
		Rules:           rules,
		StartPrompt:     brain.SystemPrompt,
		FeedbackButtons: cfg.FeedbackButtons,
		MaxTokens:       maxTokens,
	}
}
