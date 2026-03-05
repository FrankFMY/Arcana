package arcana

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func setupHandler(t *testing.T) http.Handler {
	t.Helper()

	reg := NewRegistry()
	reg.Register(GraphDef{
		Key: "user_list",
		Params: ParamSchema{
			"org_id": ParamUUID().Required(),
		},
		Deps: []TableDep{
			{Table: "users", Columns: []string{"id", "name"}},
		},
		Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
			r := NewResult()
			r.AddRow("users", "u1", map[string]any{"id": "u1", "name": "Alice"})
			r.AddRef(Ref{Table: "users", ID: "u1", Fields: []string{"id", "name"}})
			return r, nil
		},
	})

	cfg := &Config{}
	cfg.withDefaults()
	transport := newMockTransport()
	store := NewDataStore()
	mgr := NewManager(reg, store, transport, &mockQuerier{}, cfg, nil)

	return newHandler(mgr, reg, func(r *http.Request) (*Identity, error) {
		return &Identity{
			SeanceID:    "s1",
			UserID:      "u1",
			WorkspaceID: "w1",
		}, nil
	})
}

func TestHandlerHealth(t *testing.T) {
	h := setupHandler(t)

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp successResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if !resp.OK {
		t.Fatal("expected ok")
	}
}

func TestHandlerSchema(t *testing.T) {
	h := setupHandler(t)

	req := httptest.NewRequest("GET", "/schema", nil)
	w := httptest.NewRecorder()

	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]any
	json.NewDecoder(w.Body).Decode(&resp)

	data := resp["data"].(map[string]any)
	users := data["users"].([]any)
	if len(users) != 2 { // id, name
		t.Fatalf("expected 2 columns, got %d", len(users))
	}
}

func TestHandlerSubscribeUnsubscribe(t *testing.T) {
	h := setupHandler(t)

	// Subscribe
	body, _ := json.Marshal(map[string]any{
		"view":   "user_list",
		"params": map[string]any{"org_id": "550e8400-e29b-41d4-a716-446655440000"},
	})
	req := httptest.NewRequest("POST", "/subscribe", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("subscribe: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var subResp struct {
		OK   bool `json:"ok"`
		Data struct {
			ParamsHash string `json:"params_hash"`
			Version    int64  `json:"version"`
		} `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&subResp)

	if !subResp.OK {
		t.Fatal("subscribe should succeed")
	}
	if subResp.Data.ParamsHash == "" {
		t.Fatal("expected params hash")
	}

	// Active
	req = httptest.NewRequest("GET", "/active", nil)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("active: expected 200, got %d", w.Code)
	}

	// Unsubscribe
	unsubBody, _ := json.Marshal(map[string]any{
		"view":        "user_list",
		"params_hash": subResp.Data.ParamsHash,
	})
	req = httptest.NewRequest("POST", "/unsubscribe", bytes.NewReader(unsubBody))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unsubscribe: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandlerSubscribeInvalidParams(t *testing.T) {
	h := setupHandler(t)

	// Missing required param
	body, _ := json.Marshal(map[string]any{
		"view":   "user_list",
		"params": map[string]any{},
	})
	req := httptest.NewRequest("POST", "/subscribe", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandlerSubscribeUnknownView(t *testing.T) {
	h := setupHandler(t)

	body, _ := json.Marshal(map[string]any{
		"view":   "nonexistent",
		"params": map[string]any{},
	})
	req := httptest.NewRequest("POST", "/subscribe", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestHandlerNoAuth(t *testing.T) {
	reg := NewRegistry()
	cfg := &Config{}
	cfg.withDefaults()
	mgr := NewManager(reg, NewDataStore(), newMockTransport(), &mockQuerier{}, cfg, nil)

	// Auth func that returns error
	h := newHandler(mgr, reg, func(r *http.Request) (*Identity, error) {
		return nil, ErrForbidden
	})

	req := httptest.NewRequest("POST", "/subscribe", bytes.NewReader([]byte("{}")))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for unauthorized, got %d", w.Code)
	}
}

func TestHandlerSync(t *testing.T) {
	h := setupHandler(t)

	body, _ := json.Marshal(map[string]any{
		"views":  []any{},
		"tables": map[string]any{},
	})
	req := httptest.NewRequest("POST", "/sync", bytes.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}
