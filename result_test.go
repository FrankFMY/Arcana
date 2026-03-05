package arcana

import (
	"sync"
	"testing"
)

func TestResultAddRow(t *testing.T) {
	r := NewResult()

	r.AddRow("users", "u1", map[string]any{"id": "u1", "name": "Alice"})
	r.AddRow("users", "u2", map[string]any{"id": "u2", "name": "Bob"})

	tables := r.Tables()
	if len(tables["users"]) != 2 {
		t.Fatalf("expected 2 users, got %d", len(tables["users"]))
	}
	if tables["users"]["u1"]["name"] != "Alice" {
		t.Fatal("u1 name mismatch")
	}
}

func TestResultAddRowMerge(t *testing.T) {
	r := NewResult()

	r.AddRow("users", "u1", map[string]any{"id": "u1", "name": "Alice"})
	r.AddRow("users", "u1", map[string]any{"email": "alice@test.com"})

	tables := r.Tables()
	u1 := tables["users"]["u1"]
	if u1["name"] != "Alice" {
		t.Fatal("name should be preserved")
	}
	if u1["email"] != "alice@test.com" {
		t.Fatal("email should be merged")
	}
}

func TestResultAddRef(t *testing.T) {
	r := NewResult()

	r.AddRef(Ref{Table: "labors", ID: "l1", Fields: []string{"id", "role"}})
	r.AddRef(Ref{
		Table:  "labors",
		ID:     "l2",
		Fields: []string{"id", "role"},
		Nested: map[string]Ref{
			"user": {Table: "users", ID: "u1", Fields: []string{"id", "name"}},
		},
	})

	refs := r.Refs()
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %d", len(refs))
	}
	if refs[1].Nested["user"].Table != "users" {
		t.Fatal("nested ref mismatch")
	}
}

func TestResultRowCount(t *testing.T) {
	r := NewResult()
	r.AddRow("users", "u1", map[string]any{"id": "u1"})
	r.AddRow("orders", "o1", map[string]any{"id": "o1"})
	r.AddRow("orders", "o2", map[string]any{"id": "o2"})

	if r.RowCount() != 3 {
		t.Fatalf("expected 3, got %d", r.RowCount())
	}
}

func TestResultTablesDeepCopy(t *testing.T) {
	r := NewResult()
	r.AddRow("users", "u1", map[string]any{"id": "u1", "name": "Alice"})

	tables := r.Tables()
	tables["users"]["u1"]["name"] = "Modified"

	original := r.Tables()
	if original["users"]["u1"]["name"] != "Alice" {
		t.Fatal("Tables() should return a deep copy")
	}
}

func TestResultSetTotal(t *testing.T) {
	r := NewResult()

	if r.Total() != 0 {
		t.Fatalf("expected 0, got %d", r.Total())
	}

	r.SetTotal(42)
	if r.Total() != 42 {
		t.Fatalf("expected 42, got %d", r.Total())
	}

	r.SetTotal(100)
	if r.Total() != 100 {
		t.Fatalf("expected 100, got %d", r.Total())
	}
}

func TestResultConcurrentAccess(t *testing.T) {
	r := NewResult()
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			r.AddRow("t", "r"+string(rune('0'+idx%10)), map[string]any{"v": idx})
		}(i)
		go func(idx int) {
			defer wg.Done()
			r.AddRef(Ref{Table: "t", ID: "r" + string(rune('0'+idx%10))})
		}(i)
	}

	wg.Wait()
	// No panic = pass (race detector will catch issues)
}
