package agents

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/KI4JLU/CampusAgents/go-backend/internal/api"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// fakeStore is an in-memory agents.Store.
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

// fakeRefs stands in for the widgets store: it reports a fixed reference count
// per agent id, so the delete guard can be exercised without Postgres.
type fakeRefs struct {
	byAgent map[string]int
}

func (r *fakeRefs) CountByAgent(_ context.Context, agentID string) (int, error) {
	return r.byAgent[agentID], nil
}

// testAgent builds the typed body the strict handler receives. Extra fields
// beyond the modelled ones land in AdditionalProperties, as they would after
// the generated decoder ran.
func testAgent(id, name, model string) *api.Agent {
	return &api.Agent{
		Id:    id,
		Name:  name,
		Model: model,
		AdditionalProperties: map[string]interface{}{
			"tools": []interface{}{},
		},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestUpsert_CreatesAgent(t *testing.T) {
	store := &fakeStore{data: map[string]json.RawMessage{}}
	h := NewHandler(store, &fakeRefs{})

	const id = "a1"
	resp, err := h.UpsertAgent(context.Background(), api.UpsertAgentRequestObject{
		Id:   id,
		Body: testAgent(id, "JLU Assistent", "jlu/gpt-oss-20b"),
	})
	if err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	if _, ok := resp.(api.UpsertAgent200JSONResponse); !ok {
		t.Fatalf("response = %T, want UpsertAgent200JSONResponse", resp)
	}
	stored, ok := store.data[id]
	if !ok {
		t.Fatalf("agent %q was not stored", id)
	}
	// The unmodelled field must survive the typed round-trip into storage.
	var raw map[string]any
	if err := json.Unmarshal(stored, &raw); err != nil {
		t.Fatalf("stored agent is not JSON: %v", err)
	}
	if _, ok := raw["tools"]; !ok {
		t.Fatalf("additional property was dropped on store: %s", stored)
	}
}

func TestUpsert_RejectsIDMismatch(t *testing.T) {
	h := NewHandler(&fakeStore{data: map[string]json.RawMessage{}}, &fakeRefs{})

	resp, err := h.UpsertAgent(context.Background(), api.UpsertAgentRequestObject{
		Id:   "url-id",
		Body: testAgent("body-id", "Name", "model"),
	})
	if err != nil {
		t.Fatalf("UpsertAgent: %v", err)
	}
	if _, ok := resp.(api.UpsertAgent400JSONResponse); !ok {
		t.Fatalf("response = %T, want UpsertAgent400JSONResponse for id mismatch", resp)
	}
}

func TestUpsert_RequiresNameAndModel(t *testing.T) {
	h := NewHandler(&fakeStore{data: map[string]json.RawMessage{}}, &fakeRefs{})

	cases := map[string]*api.Agent{
		"missing name":  testAgent("a1", "  ", "model"),
		"missing model": testAgent("a1", "Name", " "),
	}
	for label, body := range cases {
		t.Run(label, func(t *testing.T) {
			resp, err := h.UpsertAgent(context.Background(), api.UpsertAgentRequestObject{Id: "a1", Body: body})
			if err != nil {
				t.Fatalf("UpsertAgent: %v", err)
			}
			if _, ok := resp.(api.UpsertAgent400JSONResponse); !ok {
				t.Fatalf("response = %T, want UpsertAgent400JSONResponse (%s)", resp, label)
			}
		})
	}
}

// An agent still referenced by a widget must not be deletable (409), and it
// must remain in the store.
func TestDelete_RefusesWhenAgentInUse(t *testing.T) {
	store := &fakeStore{data: map[string]json.RawMessage{
		"a1": json.RawMessage(`{"id":"a1","name":"In Use","model":"m"}`),
	}}
	refs := &fakeRefs{byAgent: map[string]int{"a1": 2}}
	h := NewHandler(store, refs)

	resp, err := h.DeleteAgent(context.Background(), api.DeleteAgentRequestObject{Id: "a1"})
	if err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if _, ok := resp.(api.DeleteAgent409JSONResponse); !ok {
		t.Fatalf("response = %T, want DeleteAgent409JSONResponse for in-use agent", resp)
	}
	if _, ok := store.data["a1"]; !ok {
		t.Fatalf("in-use agent was deleted despite 409")
	}
}

// An unreferenced agent deletes cleanly (204).
func TestDelete_SucceedsWhenUnused(t *testing.T) {
	store := &fakeStore{data: map[string]json.RawMessage{
		"a1": json.RawMessage(`{"id":"a1","name":"Standalone","model":"m"}`),
	}}
	h := NewHandler(store, &fakeRefs{byAgent: map[string]int{"a1": 0}})

	resp, err := h.DeleteAgent(context.Background(), api.DeleteAgentRequestObject{Id: "a1"})
	if err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if _, ok := resp.(api.DeleteAgent204Response); !ok {
		t.Fatalf("response = %T, want DeleteAgent204Response", resp)
	}
	if _, ok := store.data["a1"]; ok {
		t.Fatalf("agent was not deleted")
	}
}

func TestDelete_UnknownAgentReturns404(t *testing.T) {
	h := NewHandler(&fakeStore{data: map[string]json.RawMessage{}}, &fakeRefs{})

	resp, err := h.DeleteAgent(context.Background(), api.DeleteAgentRequestObject{Id: "nope"})
	if err != nil {
		t.Fatalf("DeleteAgent: %v", err)
	}
	if _, ok := resp.(api.DeleteAgent404JSONResponse); !ok {
		t.Fatalf("response = %T, want DeleteAgent404JSONResponse", resp)
	}
}

func TestList_ReturnsAgentsEnvelope(t *testing.T) {
	store := &fakeStore{data: map[string]json.RawMessage{
		"a1": json.RawMessage(`{"id":"a1","name":"One","model":"m"}`),
	}}
	h := NewHandler(store, &fakeRefs{})

	resp, err := h.ListAgents(context.Background(), api.ListAgentsRequestObject{})
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	list, ok := resp.(api.ListAgents200JSONResponse)
	if !ok {
		t.Fatalf("response = %T, want ListAgents200JSONResponse", resp)
	}
	if len(list.Agents) != 1 {
		t.Fatalf("agents length = %d, want 1", len(list.Agents))
	}
}
