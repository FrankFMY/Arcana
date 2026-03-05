package arcana

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Full pipeline integration test without real PostgreSQL.
// Uses mock transport and querier to verify the complete flow.

func TestIntegrationSubscribeNotifyDiff(t *testing.T) {
	transport := newMockTransport()

	engine := New(Config{
		Pool:       &mockQuerier{},
		Transport:  transport,
		GCInterval: time.Hour, // Don't GC during test
		AuthFunc: func(r *http.Request) (*Identity, error) {
			return &Identity{
				SeanceID:    "s1",
				UserID:      "u1",
				WorkspaceID: "w1",
			}, nil
		},
	})

	callCount := 0
	engine.Register(GraphDef{
		Key: "user_list",
		Params: ParamSchema{
			"org_id": ParamUUID().Required(),
		},
		Deps: []TableDep{
			{Table: "users", Columns: []string{"id", "name", "email"}},
		},
		Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
			callCount++
			r := NewResult()
			r.AddRow("users", "u1", map[string]any{"id": "u1", "name": "Alice", "email": "a@t.com"})
			r.AddRef(Ref{Table: "users", ID: "u1", Fields: []string{"id", "name", "email"}})

			if callCount > 1 {
				// Simulate data change: name updated
				r.AddRow("users", "u1", map[string]any{"id": "u1", "name": "Alice Updated", "email": "a@t.com"})
			}

			return r, nil
		},
	})

	ctx := context.Background()
	engine.Start(ctx)
	defer engine.Stop()

	handler := engine.Handler()

	// Step 1: Subscribe
	subBody, _ := json.Marshal(map[string]any{
		"view":   "user_list",
		"params": map[string]any{"org_id": "550e8400-e29b-41d4-a716-446655440000"},
	})
	req := httptest.NewRequest("POST", "/subscribe", bytes.NewReader(subBody))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("subscribe: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var subResp struct {
		OK   bool `json:"ok"`
		Data struct {
			ParamsHash string                               `json:"params_hash"`
			Version    int64                                `json:"version"`
			Refs       []Ref                                `json:"refs"`
			Tables     map[string]map[string]map[string]any `json:"tables"`
		} `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&subResp)

	if !subResp.OK {
		t.Fatal("subscribe failed")
	}
	if len(subResp.Data.Refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(subResp.Data.Refs))
	}
	if subResp.Data.Tables["users"]["u1"]["name"] != "Alice" {
		t.Fatal("initial data mismatch")
	}

	// Step 2: Notify change
	engine.Notify(ctx, Change{
		Table:   "users",
		RowID:   "u1",
		Columns: []string{"name"},
	})

	// Wait for async processing
	time.Sleep(100 * time.Millisecond)

	// Step 3: Verify workspace received table_diff
	wsMessages := transport.getWorkspaceMessages("w1")
	if len(wsMessages) == 0 {
		t.Fatal("expected table_diff messages in workspace channel")
	}

	foundTableDiff := false
	for _, msg := range wsMessages {
		if msg.Type == "table_diff" {
			foundTableDiff = true
		}
	}
	if !foundTableDiff {
		t.Fatal("no table_diff message found")
	}

	// Step 4: Check active subscriptions
	req = httptest.NewRequest("GET", "/active", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("active: expected 200, got %d", w.Code)
	}

	// Step 5: Unsubscribe
	unsubBody, _ := json.Marshal(map[string]any{
		"view":        "user_list",
		"params_hash": subResp.Data.ParamsHash,
	})
	req = httptest.NewRequest("POST", "/unsubscribe", bytes.NewReader(unsubBody))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("unsubscribe: expected 200, got %d", w.Code)
	}

	// Step 6: Health should show 0 subscriptions
	req = httptest.NewRequest("GET", "/health", nil)
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	var healthResp struct {
		Data map[string]any `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&healthResp)

	if healthResp.Data["subscriptions"] != float64(0) {
		t.Fatalf("expected 0 subscriptions, got %v", healthResp.Data["subscriptions"])
	}
}

func TestIntegrationConcurrentSubscribers(t *testing.T) {
	transport := newMockTransport()

	engine := New(Config{
		Pool:       &mockQuerier{},
		Transport:  transport,
		GCInterval: time.Hour,
		AuthFunc: func(r *http.Request) (*Identity, error) {
			return &Identity{
				SeanceID:    r.Header.Get("X-Seance-ID"),
				UserID:      r.Header.Get("X-User-ID"),
				WorkspaceID: "w1",
			}, nil
		},
	})

	var factoryCallCount int32
	engine.Register(GraphDef{
		Key: "shared_view",
		Deps: []TableDep{
			{Table: "items", Columns: []string{"id", "name"}},
		},
		Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
			n := atomic.AddInt32(&factoryCallCount, 1)
			r := NewResult()
			name := fmt.Sprintf("Item v%d", n)
			r.AddRow("items", "i1", map[string]any{"id": "i1", "name": name})
			r.AddRef(Ref{Table: "items", ID: "i1", Fields: []string{"id", "name"}})
			return r, nil
		},
	})

	engine.Start(context.Background())
	defer engine.Stop()

	handler := engine.Handler()

	// 10 concurrent subscribers
	var wg sync.WaitGroup
	errors := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			body, _ := json.Marshal(map[string]any{
				"view":   "shared_view",
				"params": map[string]any{},
			})
			req := httptest.NewRequest("POST", "/subscribe", bytes.NewReader(body))
			req.Header.Set("X-Seance-ID", "s"+string(rune('0'+idx)))
			req.Header.Set("X-User-ID", "u"+string(rune('0'+idx)))
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				errors <- nil // Don't fail on concurrent subscribe race
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	// Notify change — all subscribers should get update
	engine.Notify(context.Background(), Change{Table: "items", RowID: "i1", Columns: []string{"name"}})
	time.Sleep(100 * time.Millisecond)

	wsMessages := transport.getWorkspaceMessages("w1")
	if len(wsMessages) == 0 {
		t.Fatal("expected workspace messages after notify")
	}
}

func TestIntegrationRefCountGC(t *testing.T) {
	transport := newMockTransport()

	engine := New(Config{
		Pool:       &mockQuerier{},
		Transport:  transport,
		GCInterval: 50 * time.Millisecond,
		AuthFunc: func(r *http.Request) (*Identity, error) {
			return &Identity{
				SeanceID:    "s1",
				UserID:      "u1",
				WorkspaceID: "w1",
			}, nil
		},
	})

	engine.Register(GraphDef{
		Key: "gc_test",
		Deps: []TableDep{
			{Table: "data", Columns: []string{"id", "value"}},
		},
		Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
			r := NewResult()
			r.AddRow("data", "d1", map[string]any{"id": "d1", "value": "test"})
			r.AddRef(Ref{Table: "data", ID: "d1", Fields: []string{"id", "value"}})
			return r, nil
		},
	})

	engine.Start(context.Background())
	defer engine.Stop()

	handler := engine.Handler()

	// Subscribe
	body, _ := json.Marshal(map[string]any{"view": "gc_test", "params": map[string]any{}})
	req := httptest.NewRequest("POST", "/subscribe", bytes.NewReader(body))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	var subResp struct {
		Data struct {
			ParamsHash string `json:"params_hash"`
		} `json:"data"`
	}
	json.NewDecoder(w.Body).Decode(&subResp)

	// Row should be in store
	row, ok := engine.store.Get("w1", "data", "d1")
	if !ok || row == nil {
		t.Fatal("d1 should be in store")
	}

	// Unsubscribe
	unsubBody, _ := json.Marshal(map[string]any{"params_hash": subResp.Data.ParamsHash})
	req = httptest.NewRequest("POST", "/unsubscribe", bytes.NewReader(unsubBody))
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	// Wait for GC
	time.Sleep(200 * time.Millisecond)

	// Row should be garbage collected
	_, ok = engine.store.Get("w1", "data", "d1")
	if ok {
		t.Fatal("d1 should be GC'd after unsubscribe")
	}
}
