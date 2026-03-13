package arcana

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func setupDevToolsEngine(t *testing.T) *Engine {
	t.Helper()

	cfg := Config{
		Pool:      &mockQuerier{},
		Transport: newMockTransport(),
	}
	cfg.withDefaults()

	e := New(cfg)
	e.Register(GraphDef{
		Key: "tasks",
		Params: ParamSchema{
			"project_id": ParamUUID().Required(),
		},
		Deps: []TableDep{
			{Table: "tasks", Columns: []string{"id", "title", "status"}},
		},
		Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
			return NewResult(), nil
		},
	})
	e.RegisterMutation(MutationDef{
		Key: "create_task",
		Handler: func(ctx context.Context, q Querier, p Params) (*MutationResult, error) {
			return &MutationResult{Data: map[string]string{"id": "new"}}, nil
		},
	})

	return e
}

func TestDevToolsStateEndpoint(t *testing.T) {
	e := setupDevToolsEngine(t)
	h := e.DevToolsHandler()

	req := httptest.NewRequest("GET", "/state", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var state devtoolsStateResponse
	if err := json.NewDecoder(w.Body).Decode(&state); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(state.Graphs) != 1 || state.Graphs[0] != "tasks" {
		t.Fatalf("expected graphs=[tasks], got %v", state.Graphs)
	}

	if len(state.Mutations) != 1 || state.Mutations[0] != "create_task" {
		t.Fatalf("expected mutations=[create_task], got %v", state.Mutations)
	}

	if state.Schema["tasks"] == nil || len(state.Schema["tasks"]) != 3 {
		t.Fatalf("expected 3 columns for tasks table, got %v", state.Schema["tasks"])
	}
}

func TestDevToolsHTMLPage(t *testing.T) {
	e := setupDevToolsEngine(t)
	h := e.DevToolsHandler()

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Fatalf("expected text/html, got %s", ct)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Arcana DevTools") {
		t.Fatal("expected page to contain 'Arcana DevTools'")
	}
}
