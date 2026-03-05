package arcana

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestStoreUpsertNew(t *testing.T) {
	s := NewDataStore()

	old, diff, isNew := s.Upsert("w1", "users", "u1", map[string]any{
		"id": "u1", "name": "Alice",
	})

	if !isNew {
		t.Fatal("expected new row")
	}
	if old != nil {
		t.Fatal("expected nil old fields")
	}
	if len(diff) != 2 {
		t.Fatalf("expected 2 add ops, got %d", len(diff))
	}

	fields := s.GetFields("w1", "users", "u1")
	if fields["name"] != "Alice" {
		t.Fatalf("expected Alice, got %v", fields["name"])
	}
}

func TestStoreUpsertUpdate(t *testing.T) {
	s := NewDataStore()

	s.Upsert("w1", "users", "u1", map[string]any{"id": "u1", "name": "Alice", "email": "a@t.com"})
	old, diff, isNew := s.Upsert("w1", "users", "u1", map[string]any{"id": "u1", "name": "Bob", "email": "a@t.com"})

	if isNew {
		t.Fatal("should not be new")
	}
	if old["name"] != "Alice" {
		t.Fatalf("old name should be Alice, got %v", old["name"])
	}
	if len(diff) != 1 {
		t.Fatalf("expected 1 replace op, got %d", len(diff))
	}
	if diff[0].Op != "replace" || diff[0].Path != "/name" {
		t.Fatalf("unexpected diff: %+v", diff[0])
	}
}

func TestStoreUpsertNoChange(t *testing.T) {
	s := NewDataStore()

	s.Upsert("w1", "users", "u1", map[string]any{"id": "u1", "name": "Alice"})
	_, diff, _ := s.Upsert("w1", "users", "u1", map[string]any{"id": "u1", "name": "Alice"})

	if len(diff) != 0 {
		t.Fatalf("expected no diff, got %d ops", len(diff))
	}
}

func TestStoreGet(t *testing.T) {
	s := NewDataStore()
	s.Upsert("w1", "users", "u1", map[string]any{"id": "u1"})

	row, ok := s.Get("w1", "users", "u1")
	if !ok || row == nil {
		t.Fatal("expected row")
	}

	_, ok = s.Get("w1", "users", "nonexistent")
	if ok {
		t.Fatal("expected not found")
	}

	_, ok = s.Get("w2", "users", "u1")
	if ok {
		t.Fatal("expected not found in different workspace")
	}
}

func TestStoreDelete(t *testing.T) {
	s := NewDataStore()
	s.Upsert("w1", "users", "u1", map[string]any{"id": "u1"})

	row, ok := s.Delete("w1", "users", "u1")
	if !ok || row == nil {
		t.Fatal("expected deleted row")
	}

	_, ok = s.Get("w1", "users", "u1")
	if ok {
		t.Fatal("should be deleted")
	}

	// Delete nonexistent
	_, ok = s.Delete("w1", "users", "u1")
	if ok {
		t.Fatal("should not find deleted row")
	}
}

func TestStoreRefCount(t *testing.T) {
	s := NewDataStore()
	s.Upsert("w1", "users", "u1", map[string]any{"id": "u1"})

	s.IncrRef("w1", "users", "u1")
	s.IncrRef("w1", "users", "u1")

	row, _ := s.Get("w1", "users", "u1")
	if atomic.LoadInt32(&row.RefCount) != 2 {
		t.Fatalf("expected refcount 2, got %d", row.RefCount)
	}

	shouldGC := s.DecrRef("w1", "users", "u1")
	if shouldGC {
		t.Fatal("refcount is 1, should not GC")
	}

	shouldGC = s.DecrRef("w1", "users", "u1")
	if !shouldGC {
		t.Fatal("refcount is 0, should GC")
	}
}

func TestStoreGC(t *testing.T) {
	s := NewDataStore()
	s.Upsert("w1", "users", "u1", map[string]any{"id": "u1"})
	s.Upsert("w1", "users", "u2", map[string]any{"id": "u2"})

	// u1 has refs, u2 does not
	s.IncrRef("w1", "users", "u1")

	s.GC("w1", "users", "u1")
	_, ok := s.Get("w1", "users", "u1")
	if !ok {
		t.Fatal("u1 should NOT be GC'd (refcount > 0)")
	}

	s.GC("w1", "users", "u2")
	_, ok = s.Get("w1", "users", "u2")
	if ok {
		t.Fatal("u2 should be GC'd (refcount = 0)")
	}
}

func TestStoreGCAll(t *testing.T) {
	s := NewDataStore()
	s.Upsert("w1", "users", "u1", map[string]any{"id": "u1"})
	s.Upsert("w1", "users", "u2", map[string]any{"id": "u2"})
	s.Upsert("w1", "orders", "o1", map[string]any{"id": "o1"})

	s.IncrRef("w1", "users", "u1")

	s.GCAll()

	_, ok := s.Get("w1", "users", "u1")
	if !ok {
		t.Fatal("u1 should survive GC")
	}
	_, ok = s.Get("w1", "users", "u2")
	if ok {
		t.Fatal("u2 should be collected")
	}
	_, ok = s.Get("w1", "orders", "o1")
	if ok {
		t.Fatal("o1 should be collected")
	}
}

func TestStoreVersion(t *testing.T) {
	s := NewDataStore()
	s.Upsert("w1", "users", "u1", map[string]any{"id": "u1", "name": "Alice"})

	v := s.RowVersion("w1", "users", "u1")
	if v != 1 {
		t.Fatalf("expected version 1, got %d", v)
	}

	s.Upsert("w1", "users", "u1", map[string]any{"id": "u1", "name": "Bob"})
	v = s.RowVersion("w1", "users", "u1")
	if v != 2 {
		t.Fatalf("expected version 2, got %d", v)
	}

	// No change → no version bump
	s.Upsert("w1", "users", "u1", map[string]any{"id": "u1", "name": "Bob"})
	v = s.RowVersion("w1", "users", "u1")
	if v != 2 {
		t.Fatalf("expected version still 2, got %d", v)
	}
}

func TestStoreConcurrency(t *testing.T) {
	s := NewDataStore()
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ws := "w1"
			id := "u1"
			s.Upsert(ws, "users", id, map[string]any{"v": idx})
			s.GetFields(ws, "users", id)
			s.IncrRef(ws, "users", id)
			s.DecrRef(ws, "users", id)
			s.RowVersion(ws, "users", id)
		}(i)
	}

	wg.Wait()
}

func TestStoreIncrRefNonexistent(t *testing.T) {
	s := NewDataStore()
	// Should not panic
	s.IncrRef("w1", "users", "nonexistent")
	s.DecrRef("w1", "users", "nonexistent")
	s.GC("w1", "users", "nonexistent")
}
