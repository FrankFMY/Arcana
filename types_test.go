package arcana

import (
	"errors"
	"testing"
)

func TestParamBuilders(t *testing.T) {
	t.Run("UUID required", func(t *testing.T) {
		def := ParamUUID().Required()
		if def.Type != ParamTypeUUID {
			t.Fatalf("expected ParamTypeUUID, got %d", def.Type)
		}
		if !def.Required {
			t.Fatal("expected required")
		}
	})

	t.Run("Int with default", func(t *testing.T) {
		def := ParamInt().Default(50)
		if def.Type != ParamTypeInt {
			t.Fatalf("expected ParamTypeInt, got %d", def.Type)
		}
		if def.DefaultValue != 50 {
			t.Fatalf("expected default 50, got %v", def.DefaultValue)
		}
	})

	t.Run("String with OneOf", func(t *testing.T) {
		def := ParamString().OneOf("ASC", "DESC").Default("DESC")
		if def.Type != ParamTypeString {
			t.Fatalf("expected ParamTypeString, got %d", def.Type)
		}
		if len(def.AllowedVals) != 2 {
			t.Fatalf("expected 2 allowed vals, got %d", len(def.AllowedVals))
		}
		if def.DefaultValue != "DESC" {
			t.Fatalf("expected default DESC, got %v", def.DefaultValue)
		}
	})

	t.Run("Bool build", func(t *testing.T) {
		def := ParamBool().Build()
		if def.Type != ParamTypeBool {
			t.Fatalf("expected ParamTypeBool, got %d", def.Type)
		}
	})

	t.Run("Float build", func(t *testing.T) {
		def := ParamFloat().Default(3.14)
		if def.Type != ParamTypeFloat {
			t.Fatalf("expected ParamTypeFloat, got %d", def.Type)
		}
	})
}

func TestParams(t *testing.T) {
	p := NewParams(map[string]any{
		"id":     "550e8400-e29b-41d4-a716-446655440000",
		"limit":  float64(10), // JSON numbers decode as float64
		"sort":   "ASC",
		"active": true,
		"rate":   3.14,
	})

	if got := p.UUID("id"); got != "550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("UUID mismatch: %s", got)
	}
	if got := p.Int("limit"); got != 10 {
		t.Fatalf("Int mismatch: %d", got)
	}
	if got := p.String("sort"); got != "ASC" {
		t.Fatalf("String mismatch: %s", got)
	}
	if got := p.Bool("active"); !got {
		t.Fatal("Bool mismatch")
	}
	if got := p.Float("rate"); got != 3.14 {
		t.Fatalf("Float mismatch: %f", got)
	}

	// Missing keys return zero values
	if got := p.UUID("missing"); got != "" {
		t.Fatalf("expected empty, got %s", got)
	}
	if got := p.Int("missing"); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
}

func TestValidateParams(t *testing.T) {
	schema := ParamSchema{
		"org_id": ParamUUID().Required(),
		"limit":  ParamInt().Default(50),
		"offset": ParamInt().Default(0),
		"sort":   ParamString().OneOf("ASC", "DESC").Default("DESC"),
		"active": ParamBool().Build(),
	}

	t.Run("valid with defaults", func(t *testing.T) {
		resolved, err := ValidateParams(schema, map[string]any{
			"org_id": "550e8400-e29b-41d4-a716-446655440000",
		})
		if err != nil {
			t.Fatal(err)
		}
		if resolved["org_id"] != "550e8400-e29b-41d4-a716-446655440000" {
			t.Fatal("org_id mismatch")
		}
		if resolved["limit"] != 50 {
			t.Fatalf("expected default limit 50, got %v", resolved["limit"])
		}
		if resolved["sort"] != "DESC" {
			t.Fatalf("expected default sort DESC, got %v", resolved["sort"])
		}
	})

	t.Run("valid with overrides", func(t *testing.T) {
		resolved, err := ValidateParams(schema, map[string]any{
			"org_id": "550e8400-e29b-41d4-a716-446655440000",
			"limit":  float64(10),
			"sort":   "ASC",
		})
		if err != nil {
			t.Fatal(err)
		}
		if resolved["limit"] != 10 {
			t.Fatalf("expected limit 10, got %v", resolved["limit"])
		}
		if resolved["sort"] != "ASC" {
			t.Fatalf("expected sort ASC, got %v", resolved["sort"])
		}
	})

	t.Run("missing required", func(t *testing.T) {
		_, err := ValidateParams(schema, map[string]any{})
		if !errors.Is(err, ErrInvalidParams) {
			t.Fatalf("expected ErrInvalidParams, got %v", err)
		}
	})

	t.Run("invalid type", func(t *testing.T) {
		_, err := ValidateParams(schema, map[string]any{
			"org_id": 123, // should be string
		})
		if !errors.Is(err, ErrInvalidParams) {
			t.Fatalf("expected ErrInvalidParams, got %v", err)
		}
	})

	t.Run("invalid OneOf", func(t *testing.T) {
		_, err := ValidateParams(schema, map[string]any{
			"org_id": "550e8400-e29b-41d4-a716-446655440000",
			"sort":   "RANDOM",
		})
		if !errors.Is(err, ErrInvalidParams) {
			t.Fatalf("expected ErrInvalidParams, got %v", err)
		}
	})

	t.Run("bool type check", func(t *testing.T) {
		_, err := ValidateParams(schema, map[string]any{
			"org_id": "550e8400-e29b-41d4-a716-446655440000",
			"active": "not-a-bool",
		})
		if !errors.Is(err, ErrInvalidParams) {
			t.Fatalf("expected ErrInvalidParams, got %v", err)
		}
	})

	t.Run("valid UUID passes", func(t *testing.T) {
		resolved, err := ValidateParams(schema, map[string]any{
			"org_id": "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11",
		})
		if err != nil {
			t.Fatal(err)
		}
		if resolved["org_id"] != "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11" {
			t.Fatal("org_id mismatch")
		}
	})

	t.Run("invalid UUID format", func(t *testing.T) {
		_, err := ValidateParams(schema, map[string]any{
			"org_id": "not-a-uuid",
		})
		if !errors.Is(err, ErrInvalidParams) {
			t.Fatalf("expected ErrInvalidParams, got %v", err)
		}
	})

	t.Run("empty string UUID fails if required", func(t *testing.T) {
		_, err := ValidateParams(schema, map[string]any{
			"org_id": "",
		})
		if !errors.Is(err, ErrInvalidParams) {
			t.Fatalf("expected ErrInvalidParams, got %v", err)
		}
	})

	t.Run("missing required UUID", func(t *testing.T) {
		uuidOnlySchema := ParamSchema{
			"id": ParamUUID().Required(),
		}
		_, err := ValidateParams(uuidOnlySchema, map[string]any{})
		if !errors.Is(err, ErrInvalidParams) {
			t.Fatalf("expected ErrInvalidParams, got %v", err)
		}
	})
}

func TestValidateParamsStrict(t *testing.T) {
	schema := ParamSchema{
		"org_id": ParamUUID().Required(),
		"limit":  ParamInt().Default(50),
	}

	t.Run("non-strict ignores extra params", func(t *testing.T) {
		resolved, err := ValidateParams(schema, map[string]any{
			"org_id":  "550e8400-e29b-41d4-a716-446655440000",
			"unknown": "value",
		})
		if err != nil {
			t.Fatal(err)
		}
		if resolved["org_id"] != "550e8400-e29b-41d4-a716-446655440000" {
			t.Fatal("org_id mismatch")
		}
	})

	t.Run("strict rejects extra params", func(t *testing.T) {
		_, err := ValidateParams(schema, map[string]any{
			"org_id":  "550e8400-e29b-41d4-a716-446655440000",
			"unknown": "value",
		}, true)
		if !errors.Is(err, ErrInvalidParams) {
			t.Fatalf("expected ErrInvalidParams, got %v", err)
		}
	})

	t.Run("strict passes with valid params only", func(t *testing.T) {
		resolved, err := ValidateParams(schema, map[string]any{
			"org_id": "550e8400-e29b-41d4-a716-446655440000",
			"limit":  float64(25),
		}, true)
		if err != nil {
			t.Fatal(err)
		}
		if resolved["limit"] != 25 {
			t.Fatalf("expected limit 25, got %v", resolved["limit"])
		}
	})
}

func TestIdentity(t *testing.T) {
	id := &Identity{
		SeanceID:    "s1",
		UserID:      "u1",
		WorkspaceID: "w1",
		Role:        "admin",
		Permissions: []string{"hr.view", "hr.edit"},
	}

	if !id.HasPermission("hr.view") {
		t.Fatal("should have hr.view")
	}
	if id.HasPermission("finance.view") {
		t.Fatal("should not have finance.view")
	}
}
