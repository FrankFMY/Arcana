package arcana

import (
	"context"
	"testing"
)

func setupManager(t *testing.T) (*Manager, *mockTransport) {
	t.Helper()

	reg := NewRegistry()
	reg.Register(GraphDef{
		Key: "user_list",
		Params: ParamSchema{
			"org_id": ParamUUID().Required(),
			"limit":  ParamInt().Default(50),
		},
		Deps: []TableDep{
			{Table: "users", Columns: []string{"id", "name", "email"}},
		},
		Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
			id := IdentityFromCtx(ctx)
			if id == nil {
				return nil, ErrForbidden
			}

			r := NewResult()
			r.AddRow("users", "u1", map[string]any{"id": "u1", "name": "Alice", "email": "a@t.com"})
			r.AddRow("users", "u2", map[string]any{"id": "u2", "name": "Bob", "email": "b@t.com"})
			r.AddRef(Ref{
				Table: "users", ID: "u1", Fields: []string{"id", "name", "email"},
			})
			r.AddRef(Ref{
				Table: "users", ID: "u2", Fields: []string{"id", "name", "email"},
			})
			return r, nil
		},
	})

	transport := newMockTransport()
	cfg := &Config{}
	cfg.withDefaults()
	store := NewDataStore()

	mgr := NewManager(reg, store, transport, &mockQuerier{}, cfg, nil)
	return mgr, transport
}

func TestManagerSubscribe(t *testing.T) {
	mgr, _ := setupManager(t)

	resp, err := mgr.Subscribe(context.Background(), SubscribeRequest{
		GraphKey:    "user_list",
		Params:      map[string]any{"org_id": "550e8400-e29b-41d4-a716-446655440000"},
		SeanceID:    "s1",
		UserID:      "u1",
		WorkspaceID: "w1",
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.ParamsHash == "" {
		t.Fatal("expected params hash")
	}
	if resp.Version != 1 {
		t.Fatalf("expected version 1, got %d", resp.Version)
	}
	if len(resp.Refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(resp.Refs))
	}
	if len(resp.Tables["users"]) != 2 {
		t.Fatalf("expected 2 users in tables, got %d", len(resp.Tables["users"]))
	}
	if mgr.SubscriptionCount() != 1 {
		t.Fatalf("expected 1 subscription, got %d", mgr.SubscriptionCount())
	}
}

func TestManagerSubscribeUnknownGraph(t *testing.T) {
	mgr, _ := setupManager(t)

	_, err := mgr.Subscribe(context.Background(), SubscribeRequest{
		GraphKey: "nonexistent",
		Params:   map[string]any{},
		SeanceID: "s1",
	})
	if err == nil {
		t.Fatal("expected error for unknown graph")
	}
}

func TestManagerSubscribeInvalidParams(t *testing.T) {
	mgr, _ := setupManager(t)

	// Missing required param
	_, err := mgr.Subscribe(context.Background(), SubscribeRequest{
		GraphKey:    "user_list",
		Params:      map[string]any{},
		SeanceID:    "s1",
		WorkspaceID: "w1",
	})
	if err == nil {
		t.Fatal("expected error for missing required param")
	}
}

func TestManagerSubscribeDuplicate(t *testing.T) {
	mgr, _ := setupManager(t)

	req := SubscribeRequest{
		GraphKey:    "user_list",
		Params:      map[string]any{"org_id": "550e8400-e29b-41d4-a716-446655440000"},
		SeanceID:    "s1",
		UserID:      "u1",
		WorkspaceID: "w1",
	}

	_, err := mgr.Subscribe(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}

	_, err = mgr.Subscribe(context.Background(), req)
	if err == nil {
		t.Fatal("expected duplicate subscription error")
	}
}

func TestManagerUnsubscribe(t *testing.T) {
	mgr, _ := setupManager(t)

	resp, _ := mgr.Subscribe(context.Background(), SubscribeRequest{
		GraphKey:    "user_list",
		Params:      map[string]any{"org_id": "550e8400-e29b-41d4-a716-446655440000"},
		SeanceID:    "s1",
		UserID:      "u1",
		WorkspaceID: "w1",
	})

	err := mgr.Unsubscribe("s1", resp.ParamsHash)
	if err != nil {
		t.Fatal(err)
	}

	if mgr.SubscriptionCount() != 0 {
		t.Fatal("expected 0 subscriptions after unsubscribe")
	}
}

func TestManagerUnsubscribeNotFound(t *testing.T) {
	mgr, _ := setupManager(t)

	err := mgr.Unsubscribe("s1", "nonexistent-hash")
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestManagerUnsubscribeAll(t *testing.T) {
	mgr, _ := setupManager(t)

	// Subscribe twice with different params
	mgr.Subscribe(context.Background(), SubscribeRequest{
		GraphKey:    "user_list",
		Params:      map[string]any{"org_id": "550e8400-e29b-41d4-a716-446655440000"},
		SeanceID:    "s1",
		UserID:      "u1",
		WorkspaceID: "w1",
	})
	mgr.Subscribe(context.Background(), SubscribeRequest{
		GraphKey:    "user_list",
		Params:      map[string]any{"org_id": "660e8400-e29b-41d4-a716-446655440000"},
		SeanceID:    "s1",
		UserID:      "u1",
		WorkspaceID: "w1",
	})

	if mgr.SubscriptionCount() != 2 {
		t.Fatalf("expected 2 subscriptions, got %d", mgr.SubscriptionCount())
	}

	mgr.UnsubscribeAll("s1")

	if mgr.SubscriptionCount() != 0 {
		t.Fatal("expected 0 subscriptions after unsubscribe all")
	}
}

func TestManagerGetActive(t *testing.T) {
	mgr, _ := setupManager(t)

	mgr.Subscribe(context.Background(), SubscribeRequest{
		GraphKey:    "user_list",
		Params:      map[string]any{"org_id": "550e8400-e29b-41d4-a716-446655440000"},
		SeanceID:    "s1",
		UserID:      "u1",
		WorkspaceID: "w1",
	})

	active := mgr.GetActive("s1")
	if len(active) != 1 {
		t.Fatalf("expected 1 active, got %d", len(active))
	}

	none := mgr.GetActive("s2")
	if len(none) != 0 {
		t.Fatalf("expected 0 active for s2, got %d", len(none))
	}
}

func TestManagerGetByGraphKey(t *testing.T) {
	mgr, _ := setupManager(t)

	mgr.Subscribe(context.Background(), SubscribeRequest{
		GraphKey:    "user_list",
		Params:      map[string]any{"org_id": "550e8400-e29b-41d4-a716-446655440000"},
		SeanceID:    "s1",
		UserID:      "u1",
		WorkspaceID: "w1",
	})
	mgr.Subscribe(context.Background(), SubscribeRequest{
		GraphKey:    "user_list",
		Params:      map[string]any{"org_id": "550e8400-e29b-41d4-a716-446655440000"},
		SeanceID:    "s2",
		UserID:      "u2",
		WorkspaceID: "w1",
	})

	subs := mgr.GetByGraphKey("user_list")
	if len(subs) != 2 {
		t.Fatalf("expected 2 subscriptions, got %d", len(subs))
	}

	none := mgr.GetByGraphKey("nonexistent")
	if len(none) != 0 {
		t.Fatalf("expected 0 for nonexistent, got %d", len(none))
	}
}

func TestManagerRefCountOnSubscribeUnsubscribe(t *testing.T) {
	mgr, _ := setupManager(t)

	resp, _ := mgr.Subscribe(context.Background(), SubscribeRequest{
		GraphKey:    "user_list",
		Params:      map[string]any{"org_id": "550e8400-e29b-41d4-a716-446655440000"},
		SeanceID:    "s1",
		UserID:      "u1",
		WorkspaceID: "w1",
	})

	// Rows should have refcount > 0
	row, ok := mgr.store.Get("w1", "users", "u1")
	if !ok {
		t.Fatal("u1 should be in store")
	}
	if row.RefCount <= 0 {
		t.Fatalf("expected positive refcount, got %d", row.RefCount)
	}

	// Unsubscribe → refcount should decrease
	mgr.Unsubscribe("s1", resp.ParamsHash)

	// After GC, unreferenced rows should be removable
	mgr.store.GCAll()

	_, ok = mgr.store.Get("w1", "users", "u1")
	if ok {
		t.Fatal("u1 should be GC'd after unsubscribe")
	}
}

func TestManagerSubscriptionLimit(t *testing.T) {
	reg := NewRegistry()
	for i := 0; i < 110; i++ {
		key := "graph_" + string(rune('a'+i%26)) + string(rune('0'+i/26))
		reg.Register(GraphDef{
			Key: key,
			Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
				return NewResult(), nil
			},
		})
	}

	transport := newMockTransport()
	cfg := &Config{MaxSubscriptionsPerSeance: 5}
	cfg.withDefaults()

	mgr := NewManager(reg, NewDataStore(), transport, &mockQuerier{}, cfg, nil)

	keys := reg.Keys()
	for i := 0; i < 5; i++ {
		_, err := mgr.Subscribe(context.Background(), SubscribeRequest{
			GraphKey: keys[i], SeanceID: "s1", WorkspaceID: "w1",
		})
		if err != nil {
			t.Fatalf("subscribe %d failed: %v", i, err)
		}
	}

	_, err := mgr.Subscribe(context.Background(), SubscribeRequest{
		GraphKey: keys[5], SeanceID: "s1", WorkspaceID: "w1",
	})
	if err != ErrTooManySubscriptions {
		t.Fatalf("expected ErrTooManySubscriptions, got %v", err)
	}
}

func TestManagerSyncCatchUp(t *testing.T) {
	mgr, _ := setupManager(t)

	resp, err := mgr.Subscribe(context.Background(), SubscribeRequest{
		GraphKey:    "user_list",
		Params:      map[string]any{"org_id": "550e8400-e29b-41d4-a716-446655440000"},
		SeanceID:    "s1",
		UserID:      "u1",
		WorkspaceID: "w1",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate 3 version updates via UpdateSubscription
	for i := int64(2); i <= 4; i++ {
		mgr.UpdateSubscription(findSubID(mgr, "s1"), []Ref{
			{Table: "users", ID: "u1", Fields: []string{"id", "name"}},
		}, i, &VersionEntry{
			Version:   i,
			RefsPatch: []PatchOp{{Op: "replace", Path: "/0", Value: Ref{Table: "users", ID: "u1", Fields: []string{"id", "name"}}}},
		})
	}

	// Client reconnects at version 1, server is at version 4
	syncResp, err := mgr.Sync(context.Background(), SyncRequest{
		SeanceID:    "s1",
		WorkspaceID: "w1",
		Views: []SyncViewRequest{
			{View: "user_list", ParamsHash: resp.ParamsHash, Version: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(syncResp.Views) != 1 {
		t.Fatalf("expected 1 view response, got %d", len(syncResp.Views))
	}

	viewSync := syncResp.Views[0]
	if viewSync.Mode != "catch_up" {
		t.Fatalf("expected catch_up mode, got %s", viewSync.Mode)
	}
	if len(viewSync.Patches) != 3 {
		t.Fatalf("expected 3 patches, got %d", len(viewSync.Patches))
	}
}

func TestManagerSyncSnapshot(t *testing.T) {
	mgr, _ := setupManager(t)

	resp, err := mgr.Subscribe(context.Background(), SubscribeRequest{
		GraphKey:    "user_list",
		Params:      map[string]any{"org_id": "550e8400-e29b-41d4-a716-446655440000"},
		SeanceID:    "s1",
		UserID:      "u1",
		WorkspaceID: "w1",
	})
	if err != nil {
		t.Fatal(err)
	}

	subID := findSubID(mgr, "s1")

	// Push version far ahead beyond SnapshotThreshold (default 50)
	mgr.UpdateSubscription(subID, []Ref{
		{Table: "users", ID: "u1", Fields: []string{"id", "name"}},
	}, 100, nil)

	// Client at version 1, server at 100 → snapshot
	syncResp, err := mgr.Sync(context.Background(), SyncRequest{
		SeanceID:    "s1",
		WorkspaceID: "w1",
		Views: []SyncViewRequest{
			{View: "user_list", ParamsHash: resp.ParamsHash, Version: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	viewSync := syncResp.Views[0]
	if viewSync.Mode != "snapshot" {
		t.Fatalf("expected snapshot mode, got %s", viewSync.Mode)
	}
	if len(viewSync.Refs) == 0 {
		t.Fatal("expected refs in snapshot")
	}
	if len(viewSync.Tables) == 0 {
		t.Fatal("expected tables in snapshot")
	}
}

func TestManagerSyncNotFound(t *testing.T) {
	mgr, _ := setupManager(t)

	_, err := mgr.Sync(context.Background(), SyncRequest{
		SeanceID:    "s1",
		WorkspaceID: "w1",
		Views: []SyncViewRequest{
			{View: "user_list", ParamsHash: "nonexistent", Version: 1},
		},
	})
	if err == nil {
		t.Fatal("expected error for unknown subscription")
	}
}

// findSubID finds the first subscription ID for a seance.
func findSubID(mgr *Manager, seanceID string) string {
	subs := mgr.GetActive(seanceID)
	if len(subs) == 0 {
		return ""
	}
	return subs[0].ID
}
