package arcana

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

func TestEngineLifecycle(t *testing.T) {
	transport := newMockTransport()

	engine := New(Config{
		Pool:       &mockQuerier{},
		Transport:  transport,
		GCInterval: 100 * time.Millisecond,
	})

	err := engine.Register(GraphDef{
		Key: "user_list",
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
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	err = engine.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Should not be able to register after start
	err = engine.Register(GraphDef{Key: "late_graph"})
	if err != ErrAlreadyStarted {
		t.Fatalf("expected ErrAlreadyStarted, got %v", err)
	}

	// Should not be able to start twice
	err = engine.Start(ctx)
	if err != ErrAlreadyStarted {
		t.Fatalf("expected ErrAlreadyStarted, got %v", err)
	}

	// Notify should not panic
	engine.Notify(ctx, Change{Table: "users", RowID: "u1", Columns: []string{"name"}})
	engine.NotifyTable(ctx, "users", "u1", []string{"name"})

	// Wait a bit for GC to run
	time.Sleep(200 * time.Millisecond)

	err = engine.Stop()
	if err != nil {
		t.Fatal(err)
	}

	// Should not be able to stop twice
	err = engine.Stop()
	if err != ErrNotStarted {
		t.Fatalf("expected ErrNotStarted, got %v", err)
	}
}

func TestEngineHandler(t *testing.T) {
	engine := New(Config{
		Pool:      &mockQuerier{},
		Transport: newMockTransport(),
		AuthFunc: func(r *http.Request) (*Identity, error) {
			return &Identity{SeanceID: "s1", UserID: "u1", WorkspaceID: "w1"}, nil
		},
	})

	engine.Register(GraphDef{
		Key: "test_graph",
		Deps: []TableDep{
			{Table: "users", Columns: []string{"id"}},
		},
		Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
			return NewResult(), nil
		},
	})

	engine.Start(context.Background())
	defer engine.Stop()

	handler := engine.Handler()

	// Test health endpoint
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

func TestEngineRegistry(t *testing.T) {
	engine := New(Config{
		Pool:      &mockQuerier{},
		Transport: newMockTransport(),
	})

	engine.Register(GraphDef{
		Key: "g1",
		Deps: []TableDep{
			{Table: "users", Columns: []string{"id", "name"}},
		},
		Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
			return NewResult(), nil
		},
	})

	reg := engine.Registry()
	if reg.GraphCount() != 1 {
		t.Fatalf("expected 1 graph, got %d", reg.GraphCount())
	}

	rep := reg.RepTable()
	if len(rep["users"]) != 2 {
		t.Fatalf("expected 2 columns, got %v", rep["users"])
	}
}

func TestEngineStopNoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()

	engine := New(Config{
		Pool:       &mockQuerier{},
		Transport:  newMockTransport(),
		GCInterval: 50 * time.Millisecond,
	})

	engine.Register(GraphDef{
		Key: "leak_test",
		Deps: []TableDep{{Table: "t", Columns: []string{"id"}}},
		Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
			return NewResult(), nil
		},
	})

	engine.Start(context.Background())

	// Engine running: GC goroutine + ExplicitNotifier goroutine should exist
	time.Sleep(100 * time.Millisecond)

	engine.Stop()
	time.Sleep(100 * time.Millisecond)

	after := runtime.NumGoroutine()
	// Allow some slack for runtime goroutines, but the delta should be small
	if after > before+2 {
		t.Fatalf("goroutine leak: before=%d, after=%d (delta=%d)", before, after, after-before)
	}
}
