package arcana

import (
	"context"
	"sync"
	"testing"
)

// mockTransport captures sent messages.
type mockTransport struct {
	mu                sync.Mutex
	seanceMessages    map[string][]Message
	workspaceMessages map[string][]Message
	disconnected      []string
}

func newMockTransport() *mockTransport {
	return &mockTransport{
		seanceMessages:    make(map[string][]Message),
		workspaceMessages: make(map[string][]Message),
	}
}

func (m *mockTransport) SendToSeance(_ context.Context, seanceID string, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seanceMessages[seanceID] = append(m.seanceMessages[seanceID], msg)
	return nil
}

func (m *mockTransport) SendToWorkspace(_ context.Context, workspaceID string, msg Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.workspaceMessages[workspaceID] = append(m.workspaceMessages[workspaceID], msg)
	return nil
}

func (m *mockTransport) DisconnectSeance(_ context.Context, seanceID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disconnected = append(m.disconnected, seanceID)
	return nil
}

func (m *mockTransport) getSeanceMessages(seanceID string) []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seanceMessages[seanceID]
}

func (m *mockTransport) getWorkspaceMessages(wsID string) []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.workspaceMessages[wsID]
}

// mockSubStore implements SubscriptionStore.
type mockSubStore struct {
	mu   sync.Mutex
	subs map[string][]*Subscription // graphKey → subs
}

func newMockSubStore() *mockSubStore {
	return &mockSubStore{
		subs: make(map[string][]*Subscription),
	}
}

func (m *mockSubStore) Add(sub *Subscription) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.subs[sub.GraphKey] = append(m.subs[sub.GraphKey], sub)
}

func (m *mockSubStore) GetByGraphKey(graphKey string) []*Subscription {
	m.mu.Lock()
	defer m.mu.Unlock()
	subs := m.subs[graphKey]
	out := make([]*Subscription, len(subs))
	copy(out, subs)
	return out
}

func (m *mockSubStore) UpdateSubscription(subID string, refs []Ref, version int64, entry *VersionEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, subs := range m.subs {
		for _, sub := range subs {
			if sub.ID == subID {
				sub.LastRefs = refs
				sub.Version = version
				if entry != nil {
					sub.VersionHistory = append(sub.VersionHistory, *entry)
				}
				return
			}
		}
	}
}

// mockQuerier implements Querier for tests.
type mockQuerier struct{}

func (m *mockQuerier) Query(_ context.Context, _ string, _ ...any) (Rows, error) {
	return &mockRows{}, nil
}

func (m *mockQuerier) QueryRow(_ context.Context, _ string, _ ...any) Row {
	return &mockRow{}
}

type mockRows struct{}

func (r *mockRows) Next() bool        { return false }
func (r *mockRows) Scan(_ ...any) error { return nil }
func (r *mockRows) Close()            {}
func (r *mockRows) Err() error        { return nil }

type mockRow struct{}

func (r *mockRow) Scan(_ ...any) error { return nil }

func TestInvalidatorColumnsFilter(t *testing.T) {
	reg := NewRegistry()
	reg.Register(GraphDef{
		Key: "user_list",
		Deps: []TableDep{
			{Table: "users", Columns: []string{"id", "name", "email"}},
		},
		Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
			r := NewResult()
			r.AddRow("users", "u1", map[string]any{"id": "u1", "name": "Alice", "email": "a@t.com"})
			r.AddRef(Ref{Table: "users", ID: "u1", Fields: []string{"id", "name", "email"}})
			return r, nil
		},
	})

	store := NewDataStore()
	transport := newMockTransport()
	subStore := newMockSubStore()

	subStore.Add(&Subscription{
		ID:          "sub1",
		SeanceID:    "s1",
		WorkspaceID: "w1",
		UserID:      "u1",
		GraphKey:    "user_list",
		Params:      map[string]any{},
		Version:     1,
	})

	inv := NewInvalidator(reg, store, transport, subStore, &mockQuerier{}, nil)

	// Change to a column the graph cares about → should trigger
	inv.Invalidate(context.Background(), Change{
		Table:   "users",
		RowID:   "u1",
		Columns: []string{"name"},
	})

	wsMessages := transport.getWorkspaceMessages("w1")
	if len(wsMessages) == 0 {
		t.Fatal("expected table_diff messages")
	}

	// Reset
	transport.mu.Lock()
	transport.workspaceMessages = make(map[string][]Message)
	transport.mu.Unlock()

	// Change to a column the graph does NOT care about → should NOT trigger
	inv.Invalidate(context.Background(), Change{
		Table:   "users",
		RowID:   "u1",
		Columns: []string{"password_hash"},
	})

	wsMessages = transport.getWorkspaceMessages("w1")
	if len(wsMessages) != 0 {
		t.Fatalf("expected no messages for unrelated column, got %d", len(wsMessages))
	}
}

func TestInvalidatorNoSubscriptions(t *testing.T) {
	reg := NewRegistry()
	reg.Register(testGraphDef("g1", []TableDep{
		{Table: "users", Columns: []string{"id"}},
	}))

	transport := newMockTransport()
	subStore := newMockSubStore()
	inv := NewInvalidator(reg, NewDataStore(), transport, subStore, &mockQuerier{}, nil)

	// No subscriptions → no messages
	inv.Invalidate(context.Background(), Change{Table: "users", RowID: "u1"})

	if len(transport.workspaceMessages) != 0 {
		t.Fatal("expected no messages")
	}
}

func TestInvalidatorUnrelatedTable(t *testing.T) {
	reg := NewRegistry()
	reg.Register(testGraphDef("g1", []TableDep{
		{Table: "users", Columns: []string{"id"}},
	}))

	transport := newMockTransport()
	subStore := newMockSubStore()
	subStore.Add(&Subscription{
		ID: "sub1", SeanceID: "s1", WorkspaceID: "w1", GraphKey: "g1",
	})

	inv := NewInvalidator(reg, NewDataStore(), transport, subStore, &mockQuerier{}, nil)

	// Change to unrelated table → no messages
	inv.Invalidate(context.Background(), Change{Table: "orders", RowID: "o1"})

	if len(transport.workspaceMessages) != 0 {
		t.Fatal("expected no messages for unrelated table")
	}
}

func TestInvalidatorViewDiff(t *testing.T) {
	reg := NewRegistry()
	reg.Register(GraphDef{
		Key: "user_list",
		Deps: []TableDep{
			{Table: "users", Columns: []string{"id", "name"}},
		},
		Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
			// Factory always returns current state (u1 + u2)
			r := NewResult()
			r.AddRow("users", "u1", map[string]any{"id": "u1", "name": "Alice"})
			r.AddRow("users", "u2", map[string]any{"id": "u2", "name": "Bob"})
			r.AddRef(Ref{Table: "users", ID: "u1", Fields: []string{"id", "name"}})
			r.AddRef(Ref{Table: "users", ID: "u2", Fields: []string{"id", "name"}})
			return r, nil
		},
	})

	store := NewDataStore()
	transport := newMockTransport()
	subStore := newMockSubStore()

	// Initial state: subscription has u1 ref
	subStore.Add(&Subscription{
		ID:          "sub1",
		SeanceID:    "s1",
		WorkspaceID: "w1",
		GraphKey:    "user_list",
		Params:      map[string]any{},
		LastRefs:    []Ref{{Table: "users", ID: "u1", Fields: []string{"id", "name"}}},
		Version:     1,
	})

	// Pre-populate store with initial data
	store.Upsert("w1", "users", "u1", map[string]any{"id": "u1", "name": "Alice"})

	inv := NewInvalidator(reg, store, transport, subStore, &mockQuerier{}, nil)

	// Trigger invalidation — factory returns u1 + u2 now
	inv.Invalidate(context.Background(), Change{Table: "users", RowID: "u2"})

	seanceMessages := transport.getSeanceMessages("s1")
	foundViewDiff := false
	for _, msg := range seanceMessages {
		if msg.Type == "view_diff" {
			foundViewDiff = true
			data := msg.Data.(map[string]any)
			if data["version"] != int64(2) {
				t.Fatalf("expected version 2, got %v", data["version"])
			}
		}
	}
	if !foundViewDiff {
		t.Fatal("expected view_diff message")
	}
}

func TestComputeParamsHash(t *testing.T) {
	h1 := ComputeParamsHash("g1", map[string]any{"a": 1, "b": 2})
	h2 := ComputeParamsHash("g1", map[string]any{"b": 2, "a": 1})
	h3 := ComputeParamsHash("g2", map[string]any{"a": 1, "b": 2})

	if h1 != h2 {
		t.Fatal("same params in different order should produce same hash")
	}
	if h1 == h3 {
		t.Fatal("different graph key should produce different hash")
	}
}
