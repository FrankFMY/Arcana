package arcana

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func testRegistry() *Registry {
	r := NewRegistry()
	r.Register(
		GraphDef{
			Key: "org_labors",
			Params: ParamSchema{
				"org_id": ParamUUID().Required(),
				"limit":  ParamInt().Default(50),
				"sort":   ParamString().OneOf("ASC", "DESC").Default("DESC"),
			},
			Deps: []TableDep{
				{Table: "labors", Columns: []string{"id", "user_id", "role", "status"}},
				{Table: "users", Columns: []string{"id", "name", "email", "avatar_url"}},
			},
			Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
				return NewResult(), nil
			},
		},
		GraphDef{
			Key: "order_list",
			Params: ParamSchema{
				"org_id": ParamUUID().Required(),
			},
			Deps: []TableDep{
				{Table: "users", Columns: []string{"id", "name"}},
				{Table: "orders", Columns: []string{"id", "total", "status", "created_at"}},
			},
			Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
				return NewResult(), nil
			},
		},
	)
	return r
}

func TestGenerateTables(t *testing.T) {
	reg := testRegistry()
	var buf bytes.Buffer

	err := GenerateTables(&buf, reg)
	if err != nil {
		t.Fatal(err)
	}

	output := buf.String()

	// Should contain Tables interface
	if !strings.Contains(output, "export interface Tables") {
		t.Fatal("missing Tables interface")
	}

	// users.email should be optional (only in org_labors, not in order_list)
	if !strings.Contains(output, "email?:") {
		t.Fatal("email should be optional (not in all graphs)")
	}

	// users.id should be required (in both graphs)
	if !strings.Contains(output, "id: string") {
		t.Fatal("id should be required")
	}

	// users.name should be required (in both graphs)
	if !strings.Contains(output, "name: string") {
		t.Fatal("name should be required")
	}

	// orders.total should be number
	if !strings.Contains(output, "total") {
		t.Fatal("missing total field")
	}

	// created_at should be string type
	if !strings.Contains(output, "created_at") {
		t.Fatal("missing created_at field")
	}
}

func TestGenerateViews(t *testing.T) {
	reg := testRegistry()
	var buf bytes.Buffer

	err := GenerateViews(&buf, reg)
	if err != nil {
		t.Fatal(err)
	}

	output := buf.String()

	// Should contain Views interface
	if !strings.Contains(output, "export interface Views") {
		t.Fatal("missing Views interface")
	}

	// Should contain org_labors view
	if !strings.Contains(output, "org_labors:") {
		t.Fatal("missing org_labors view")
	}

	// org_id should be required
	if !strings.Contains(output, "org_id: string") {
		t.Fatal("org_id should be required string")
	}

	// limit should be optional
	if !strings.Contains(output, "limit?:") {
		t.Fatal("limit should be optional")
	}

	// sort should have literal types
	if !strings.Contains(output, `"ASC"`) || !strings.Contains(output, `"DESC"`) {
		t.Fatal("sort should have literal union type")
	}

	// Should list deps
	if !strings.Contains(output, "labors:") {
		t.Fatal("missing labors dep")
	}
}

func TestGenerateTablesEmpty(t *testing.T) {
	reg := NewRegistry()
	var buf bytes.Buffer

	err := GenerateTables(&buf, reg)
	if err != nil {
		t.Fatal(err)
	}

	output := buf.String()
	if !strings.Contains(output, "export interface Tables") {
		t.Fatal("should still contain Tables interface")
	}
}
