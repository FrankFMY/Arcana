package arcana

import (
	"testing"
)

func TestDiffFieldsAdd(t *testing.T) {
	old := map[string]any{"id": "u1"}
	new := map[string]any{"id": "u1", "name": "Alice"}

	ops := DiffFields(old, new)
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].Op != "add" || ops[0].Path != "/name" || ops[0].Value != "Alice" {
		t.Fatalf("unexpected op: %+v", ops[0])
	}
}

func TestDiffFieldsReplace(t *testing.T) {
	old := map[string]any{"id": "u1", "name": "Alice"}
	new := map[string]any{"id": "u1", "name": "Bob"}

	ops := DiffFields(old, new)
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].Op != "replace" || ops[0].Path != "/name" || ops[0].Value != "Bob" {
		t.Fatalf("unexpected op: %+v", ops[0])
	}
}

func TestDiffFieldsRemove(t *testing.T) {
	old := map[string]any{"id": "u1", "name": "Alice", "email": "a@t.com"}
	new := map[string]any{"id": "u1", "name": "Alice"}

	ops := DiffFields(old, new)
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].Op != "remove" || ops[0].Path != "/email" {
		t.Fatalf("unexpected op: %+v", ops[0])
	}
}

func TestDiffFieldsNoChange(t *testing.T) {
	old := map[string]any{"id": "u1", "name": "Alice"}
	new := map[string]any{"id": "u1", "name": "Alice"}

	ops := DiffFields(old, new)
	if len(ops) != 0 {
		t.Fatalf("expected 0 ops, got %d", len(ops))
	}
}

func TestDiffFieldsMixed(t *testing.T) {
	old := map[string]any{"id": "u1", "name": "Alice", "role": "admin"}
	new := map[string]any{"id": "u1", "name": "Bob", "email": "b@t.com"}

	ops := DiffFields(old, new)

	opMap := make(map[string]PatchOp)
	for _, op := range ops {
		opMap[op.Path] = op
	}

	if opMap["/name"].Op != "replace" {
		t.Fatal("name should be replaced")
	}
	if opMap["/role"].Op != "remove" {
		t.Fatal("role should be removed")
	}
	if opMap["/email"].Op != "add" {
		t.Fatal("email should be added")
	}
}

func TestDiffFieldsNilValues(t *testing.T) {
	old := map[string]any{"id": "u1", "name": "Alice"}
	new := map[string]any{"id": "u1", "name": nil}

	ops := DiffFields(old, new)
	if len(ops) != 1 {
		t.Fatalf("expected 1 op, got %d", len(ops))
	}
	if ops[0].Op != "replace" || ops[0].Path != "/name" {
		t.Fatalf("unexpected op: %+v", ops[0])
	}
}

func TestDiffRefsAdd(t *testing.T) {
	old := []Ref{
		{Table: "users", ID: "u1", Fields: []string{"id", "name"}},
	}
	new := []Ref{
		{Table: "users", ID: "u1", Fields: []string{"id", "name"}},
		{Table: "users", ID: "u2", Fields: []string{"id", "name"}},
	}

	ops := DiffRefs(old, new)

	var addOps []PatchOp
	for _, op := range ops {
		if op.Op == "add" {
			addOps = append(addOps, op)
		}
	}
	if len(addOps) != 1 {
		t.Fatalf("expected 1 add op, got %d", len(addOps))
	}

	ref, ok := addOps[0].Value.(Ref)
	if !ok {
		t.Fatal("value should be Ref")
	}
	if ref.ID != "u2" {
		t.Fatalf("expected u2, got %s", ref.ID)
	}
}

func TestDiffRefsSoftDelete(t *testing.T) {
	old := []Ref{
		{Table: "users", ID: "u1", Fields: []string{"id"}},
		{Table: "users", ID: "u2", Fields: []string{"id"}},
	}
	new := []Ref{
		{Table: "users", ID: "u1", Fields: []string{"id"}},
	}

	ops := DiffRefs(old, new)

	var softDeletes []PatchOp
	for _, op := range ops {
		if op.Op == "replace" && op.Value == nil {
			softDeletes = append(softDeletes, op)
		}
	}
	if len(softDeletes) != 1 {
		t.Fatalf("expected 1 soft-delete (replace with null) op, got %d", len(softDeletes))
	}
}

func TestDiffRefsNoChange(t *testing.T) {
	refs := []Ref{
		{Table: "users", ID: "u1", Fields: []string{"id", "name"}},
	}
	ops := DiffRefs(refs, refs)
	if len(ops) != 0 {
		t.Fatalf("expected 0 ops, got %d", len(ops))
	}
}

func TestDiffRefsNestedChange(t *testing.T) {
	old := []Ref{
		{
			Table: "labors", ID: "l1", Fields: []string{"id"},
			Nested: map[string]Ref{
				"user": {Table: "users", ID: "u1", Fields: []string{"id", "name"}},
			},
		},
	}
	new := []Ref{
		{
			Table: "labors", ID: "l1", Fields: []string{"id"},
			Nested: map[string]Ref{
				"user": {Table: "users", ID: "u2", Fields: []string{"id", "name"}},
			},
		},
	}

	ops := DiffRefs(old, new)
	if len(ops) != 1 {
		t.Fatalf("expected 1 replace op, got %d", len(ops))
	}
	if ops[0].Op != "replace" {
		t.Fatalf("expected replace, got %s", ops[0].Op)
	}
}

func TestDiffRefsFieldChange(t *testing.T) {
	old := []Ref{
		{Table: "users", ID: "u1", Fields: []string{"id", "name"}},
	}
	new := []Ref{
		{Table: "users", ID: "u1", Fields: []string{"id", "name", "email"}},
	}

	ops := DiffRefs(old, new)
	if len(ops) != 1 {
		t.Fatalf("expected 1 replace, got %d", len(ops))
	}
	if ops[0].Op != "replace" {
		t.Fatalf("expected replace, got %s", ops[0].Op)
	}
}

func TestDiffRefsEmpty(t *testing.T) {
	ops := DiffRefs(nil, nil)
	if len(ops) != 0 {
		t.Fatalf("expected 0 ops, got %d", len(ops))
	}

	ops = DiffRefs(nil, []Ref{{Table: "t", ID: "1", Fields: []string{"id"}}})
	if len(ops) != 1 || ops[0].Op != "add" {
		t.Fatal("expected 1 add op")
	}

	ops = DiffRefs([]Ref{{Table: "t", ID: "1", Fields: []string{"id"}}}, nil)
	if len(ops) != 1 || ops[0].Op != "replace" || ops[0].Value != nil {
		t.Fatal("expected 1 soft-delete op (replace with null)")
	}
}

func TestDiffRefsCompleteReplacement(t *testing.T) {
	old := []Ref{
		{Table: "users", ID: "u1", Fields: []string{"id"}},
		{Table: "users", ID: "u2", Fields: []string{"id"}},
	}
	new := []Ref{
		{Table: "users", ID: "u3", Fields: []string{"id"}},
		{Table: "users", ID: "u4", Fields: []string{"id"}},
	}

	ops := DiffRefs(old, new)
	adds := 0
	softDeletes := 0
	for _, op := range ops {
		switch {
		case op.Op == "add":
			adds++
		case op.Op == "replace" && op.Value == nil:
			softDeletes++
		}
	}
	if adds != 2 || softDeletes != 2 {
		t.Fatalf("expected 2 adds + 2 soft-deletes, got %d adds + %d soft-deletes", adds, softDeletes)
	}
}
