package arcana

import (
	"context"
	"testing"
)

func testGraphDef(key string, deps []TableDep) GraphDef {
	return GraphDef{
		Key:  key,
		Deps: deps,
		Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
			return NewResult(), nil
		},
	}
}

func TestRegistryRegister(t *testing.T) {
	r := NewRegistry()

	err := r.Register(
		testGraphDef("org_labors", []TableDep{
			{Table: "labors", Columns: []string{"id", "user_id", "role"}},
			{Table: "users", Columns: []string{"id", "name", "email"}},
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	if r.GraphCount() != 1 {
		t.Fatalf("expected 1 graph, got %d", r.GraphCount())
	}
}

func TestRegistryDuplicate(t *testing.T) {
	r := NewRegistry()

	err := r.Register(testGraphDef("g1", nil))
	if err != nil {
		t.Fatal(err)
	}

	err = r.Register(testGraphDef("g1", nil))
	if err == nil {
		t.Fatal("expected duplicate error")
	}
}

func TestRegistryEmptyKey(t *testing.T) {
	r := NewRegistry()
	err := r.Register(testGraphDef("", nil))
	if err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestRegistryGet(t *testing.T) {
	r := NewRegistry()
	r.Register(testGraphDef("g1", nil))

	def, ok := r.Get("g1")
	if !ok {
		t.Fatal("expected to find g1")
	}
	if def.Key != "g1" {
		t.Fatalf("expected g1, got %s", def.Key)
	}

	_, ok = r.Get("nonexistent")
	if ok {
		t.Fatal("should not find nonexistent")
	}
}

func TestRegistryGetByTable(t *testing.T) {
	r := NewRegistry()

	r.Register(
		testGraphDef("g1", []TableDep{
			{Table: "users", Columns: []string{"id", "name"}},
		}),
		testGraphDef("g2", []TableDep{
			{Table: "users", Columns: []string{"id", "email"}},
			{Table: "orders", Columns: []string{"id", "total"}},
		}),
		testGraphDef("g3", []TableDep{
			{Table: "orders", Columns: []string{"id", "status"}},
		}),
	)

	userGraphs := r.GetByTable("users")
	if len(userGraphs) != 2 {
		t.Fatalf("expected 2 user graphs, got %d", len(userGraphs))
	}

	orderGraphs := r.GetByTable("orders")
	if len(orderGraphs) != 2 {
		t.Fatalf("expected 2 order graphs, got %d", len(orderGraphs))
	}

	noneGraphs := r.GetByTable("phones")
	if len(noneGraphs) != 0 {
		t.Fatalf("expected 0 graphs for phones, got %d", len(noneGraphs))
	}
}

func TestRegistryRepTable(t *testing.T) {
	r := NewRegistry()

	r.Register(
		testGraphDef("g1", []TableDep{
			{Table: "users", Columns: []string{"id", "name", "email"}},
		}),
		testGraphDef("g2", []TableDep{
			{Table: "users", Columns: []string{"id", "name", "avatar_url"}},
		}),
	)

	rep := r.RepTable()
	userCols := rep["users"]

	expected := []string{"avatar_url", "email", "id", "name"}
	if len(userCols) != len(expected) {
		t.Fatalf("expected %d columns, got %d: %v", len(expected), len(userCols), userCols)
	}
	for i, col := range expected {
		if userCols[i] != col {
			t.Fatalf("expected %s at pos %d, got %s", col, i, userCols[i])
		}
	}
}

func TestRegistryHasColumn(t *testing.T) {
	r := NewRegistry()
	r.Register(testGraphDef("g1", []TableDep{
		{Table: "users", Columns: []string{"id", "name"}},
	}))

	if !r.HasColumn("users", "name") {
		t.Fatal("should have users.name")
	}
	if r.HasColumn("users", "password") {
		t.Fatal("should not have users.password")
	}
	if r.HasColumn("orders", "id") {
		t.Fatal("should not have orders.id")
	}
}

func TestRegistryKeys(t *testing.T) {
	r := NewRegistry()
	r.Register(
		testGraphDef("b_graph", nil),
		testGraphDef("a_graph", nil),
	)

	keys := r.Keys()
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys, got %d", len(keys))
	}
	if keys[0] != "a_graph" || keys[1] != "b_graph" {
		t.Fatalf("expected sorted keys, got %v", keys)
	}
}
