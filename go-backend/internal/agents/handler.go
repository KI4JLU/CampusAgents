// Package agents provides handlers for managing Agents — the reusable "brain"
// (Ebene 1): model, system prompt, rules, token cap, and (post-MVP) tools and
// knowledge. An agent is stored as a single JSONB blob keyed by its UUID id.
//
// Endpoints (all JWT-protected; agents are never exposed unauthenticated —
// widget.js consumes the resolved public config via the widgets endpoint):
//
//   - GET    /api/agents       — full list for the admin UI
//   - PUT    /api/agents/{id}   — create/update from the admin UI
//   - DELETE /api/agents/{id}   — remove an agent (superadmin only)
//
// A widget references an agent by id (widgets.data->>'agentId'). Deleting an
// agent that is still referenced by a widget is rejected with 409 so a live
// widget cannot be left pointing at a missing brain.
package agents

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/api"
)

// Store is the database interface required by Handler.
type Store interface {
	List(ctx context.Context) ([]json.RawMessage, error)
	Get(ctx context.Context, id string) (json.RawMessage, error)
	Upsert(ctx context.Context, id string, data []byte) (json.RawMessage, error)
	Delete(ctx context.Context, id string) (bool, error)
}

// WidgetRefs reports how many widgets reference a given agent. It is satisfied
// by the widgets store, whose table owns the reverse reference (agentId). The
// agents handler uses it to guard deletion of an in-use agent.
type WidgetRefs interface {
	CountByAgent(ctx context.Context, agentID string) (int, error)
}

// Handler holds the dependencies for the agent endpoints.
type Handler struct {
	store   Store
	widgets WidgetRefs
}

// NewHandler creates a Handler using the given agent Store and widget-reference
// counter.
func NewHandler(store Store, widgets WidgetRefs) *Handler {
	return &Handler{store: store, widgets: widgets}
}

// ListAgents implements GET /api/agents — returns { "agents": [ ...full... ] }.
func (h *Handler) ListAgents(ctx context.Context, _ api.ListAgentsRequestObject) (api.ListAgentsResponseObject, error) {
	rows, err := h.store.List(ctx)
	if err != nil {
		return nil, err
	}
	agents := make([]api.Agent, 0, len(rows))
	for _, raw := range rows {
		var a api.Agent
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		agents = append(agents, a)
	}
	return api.ListAgents200JSONResponse{Agents: agents}, nil
}

// UpsertAgent implements PUT /api/agents/{id} — creates or replaces an agent.
// The typed body round-trips through api.Agent, whose AdditionalProperties
// preserve fields the backend does not model (tools, knowledge, …), so the
// stored JSONB keeps everything the admin UI sent.
func (h *Handler) UpsertAgent(ctx context.Context, request api.UpsertAgentRequestObject) (api.UpsertAgentResponseObject, error) {
	body := request.Body

	if msg := validate(request.Id, body); msg != "" {
		return api.UpsertAgent400JSONResponse{
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
	var out api.Agent
	if err := json.Unmarshal(stored, &out); err != nil {
		return nil, err
	}
	return api.UpsertAgent200JSONResponse(out), nil
}

// DeleteAgent implements DELETE /api/agents/{id} (superadmin) — removes an
// agent. Returns 409 when the agent is still referenced by one or more
// widgets, 404 when no agent with that id exists, and 204 on success. The
// superadmin role is enforced by the spec middleware (bearerJWT scope), not
// here.
func (h *Handler) DeleteAgent(ctx context.Context, request api.DeleteAgentRequestObject) (api.DeleteAgentResponseObject, error) {
	// Guard: refuse to orphan a widget that still points at this agent.
	used, err := h.widgets.CountByAgent(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	if used > 0 {
		return api.DeleteAgent409JSONResponse{
			Error: "Agent wird noch von einem oder mehreren Widgets verwendet und kann nicht gelöscht werden.",
		}, nil
	}

	existed, err := h.store.Delete(ctx, request.Id)
	if err != nil {
		return nil, err
	}
	if !existed {
		return api.DeleteAgent404JSONResponse{
			NotFoundJSONResponse: api.NotFoundJSONResponse{Error: "Agent nicht gefunden."},
		}, nil
	}
	return api.DeleteAgent204Response{}, nil
}

// validate enforces the invariants the verbatim upsert needs beyond what the
// schema already guarantees: the body id must match the URL, and the required
// fields must be non-blank (the schema only requires their presence).
func validate(id string, a *api.Agent) string {
	if a.Id != id {
		return "Agent-id im Body muss zur URL passen."
	}
	if strings.TrimSpace(a.Name) == "" {
		return "name is required"
	}
	if strings.TrimSpace(a.Model) == "" {
		return "model is required"
	}
	return ""
}
