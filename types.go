package arcana

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var uuidRegex = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// GraphDef defines a reactive data graph — a named SQL-backed query
// with dependencies and a factory function that produces normalized results.
type GraphDef struct {
	Key     string
	Params  ParamSchema
	Deps    []TableDep
	Factory FactoryFunc
}

// FactoryFunc executes SQL queries and returns normalized refs + rows.
type FactoryFunc func(ctx context.Context, q Querier, p Params) (*Result, error)

// TableDep declares a dependency on a database table and specific columns.
// When any of these columns change, graphs depending on this table are invalidated.
type TableDep struct {
	Table   string
	Columns []string
}

// ParamSchema defines the expected parameters for a graph subscription.
type ParamSchema map[string]ParamDef

// ParamDef describes a single parameter: its type, constraints, and default value.
type ParamDef struct {
	Type         ParamType
	Required     bool
	DefaultValue any
	AllowedVals  []string
}

// ParamType represents the type of a graph parameter.
type ParamType int

const (
	ParamTypeString ParamType = iota
	ParamTypeInt
	ParamTypeUUID
	ParamTypeBool
	ParamTypeFloat
)

// ParamUUID creates a UUID parameter definition.
func ParamUUID() ParamDefBuilder {
	return ParamDefBuilder{def: ParamDef{Type: ParamTypeUUID}}
}

// ParamInt creates an integer parameter definition.
func ParamInt() ParamDefBuilder {
	return ParamDefBuilder{def: ParamDef{Type: ParamTypeInt}}
}

// ParamString creates a string parameter definition.
func ParamString() ParamDefBuilder {
	return ParamDefBuilder{def: ParamDef{Type: ParamTypeString}}
}

// ParamBool creates a boolean parameter definition.
func ParamBool() ParamDefBuilder {
	return ParamDefBuilder{def: ParamDef{Type: ParamTypeBool}}
}

// ParamFloat creates a float parameter definition.
func ParamFloat() ParamDefBuilder {
	return ParamDefBuilder{def: ParamDef{Type: ParamTypeFloat}}
}

// ParamDefBuilder provides a fluent API for constructing ParamDef values.
type ParamDefBuilder struct {
	def ParamDef
}

// Required marks the parameter as required.
func (b ParamDefBuilder) Required() ParamDef {
	b.def.Required = true
	return b.def
}

// Default sets a default value and returns the completed definition.
func (b ParamDefBuilder) Default(v any) ParamDef {
	b.def.DefaultValue = v
	return b.def
}

// OneOf restricts allowed string values (only for ParamTypeString).
func (b ParamDefBuilder) OneOf(vals ...string) ParamDefBuilder {
	b.def.AllowedVals = vals
	return b
}

// Build returns the completed ParamDef without setting required or default.
func (b ParamDefBuilder) Build() ParamDef {
	return b.def
}

// Params provides typed access to resolved subscription parameters.
type Params struct {
	values map[string]any
}

// NewParams creates a Params instance from a raw value map.
func NewParams(values map[string]any) Params {
	return Params{values: values}
}

// UUID returns the parameter value as a string (UUID representation).
func (p Params) UUID(key string) string {
	v, ok := p.values[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// String returns the parameter value as a string.
func (p Params) String(key string) string {
	v, ok := p.values[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// Int returns the parameter value as an int.
func (p Params) Int(key string) int {
	v, ok := p.values[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case int64:
		return int(n)
	default:
		return 0
	}
}

// Bool returns the parameter value as a bool.
func (p Params) Bool(key string) bool {
	v, ok := p.values[key]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

// Float returns the parameter value as a float64.
func (p Params) Float(key string) float64 {
	v, ok := p.values[key]
	if !ok {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	default:
		return 0
	}
}

// Raw returns the underlying values map.
func (p Params) Raw() map[string]any {
	return p.values
}

// Ref represents a reference to a normalized row, defining view structure.
type Ref struct {
	Table  string         `json:"table"`
	ID     string         `json:"id"`
	Fields []string       `json:"fields"`
	Nested map[string]Ref `json:"nested,omitempty"`
}

// PatchOp represents a single JSON Patch (RFC 6902) operation.
type PatchOp struct {
	Op    string `json:"op"`
	Path  string `json:"path"`
	Value any    `json:"value,omitempty"`
}

// Change represents a data mutation event from the application or database.
type Change struct {
	Table   string   `json:"table"`
	RowID   string   `json:"id"`
	Op      string   `json:"op,omitempty"` // "INSERT", "UPDATE", "DELETE"
	Columns []string `json:"columns,omitempty"`
}

// Identity represents an authenticated user session.
type Identity struct {
	SeanceID    string
	UserID      string
	WorkspaceID string
	Role        string
	Permissions []string
}

// HasPermission checks whether the identity has the given permission.
func (id *Identity) HasPermission(perm string) bool {
	for _, p := range id.Permissions {
		if p == perm {
			return true
		}
	}
	return false
}

// Message is the envelope sent over the transport layer.
type Message struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

// Querier abstracts database query execution (compatible with pgx).
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
}

// Rows abstracts a result set from a database query.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Close()
	Err() error
}

// Row abstracts a single-row result from a database query.
type Row interface {
	Scan(dest ...any) error
}

// ValidateParams validates raw input against a ParamSchema,
// applying defaults and type checks. Returns resolved values or an error.
// When strict is true, any parameter key not defined in the schema causes an error.
func ValidateParams(schema ParamSchema, raw map[string]any, strict ...bool) (map[string]any, error) {
	if len(strict) > 0 && strict[0] {
		for key := range raw {
			if _, ok := schema[key]; !ok {
				return nil, fmt.Errorf("%w: unknown parameter %q", ErrInvalidParams, key)
			}
		}
	}

	resolved := make(map[string]any, len(schema))

	for name, def := range schema {
		val, provided := raw[name]
		if !provided || val == nil {
			if def.Required {
				return nil, fmt.Errorf("%w: missing required parameter %q", ErrInvalidParams, name)
			}
			if def.DefaultValue != nil {
				resolved[name] = def.DefaultValue
			}
			continue
		}

		switch def.Type {
		case ParamTypeString:
			s, ok := val.(string)
			if !ok {
				return nil, fmt.Errorf("%w: parameter %q must be a string", ErrInvalidParams, name)
			}
			if len(def.AllowedVals) > 0 {
				allowed := false
				for _, av := range def.AllowedVals {
					if s == av {
						allowed = true
						break
					}
				}
				if !allowed {
					return nil, fmt.Errorf("%w: parameter %q must be one of [%s]",
						ErrInvalidParams, name, strings.Join(def.AllowedVals, ", "))
				}
			}
			resolved[name] = s

		case ParamTypeInt:
			switch n := val.(type) {
			case int:
				resolved[name] = n
			case float64:
				resolved[name] = int(n)
			case int64:
				resolved[name] = int(n)
			default:
				return nil, fmt.Errorf("%w: parameter %q must be an integer", ErrInvalidParams, name)
			}

		case ParamTypeUUID:
			s, ok := val.(string)
			if !ok {
				return nil, fmt.Errorf("%w: parameter %q must be a UUID string", ErrInvalidParams, name)
			}
			if !uuidRegex.MatchString(s) {
				return nil, fmt.Errorf("%w: parameter %q must be a valid UUID", ErrInvalidParams, name)
			}
			resolved[name] = s

		case ParamTypeBool:
			b, ok := val.(bool)
			if !ok {
				return nil, fmt.Errorf("%w: parameter %q must be a boolean", ErrInvalidParams, name)
			}
			resolved[name] = b

		case ParamTypeFloat:
			switch n := val.(type) {
			case float64:
				resolved[name] = n
			case int:
				resolved[name] = float64(n)
			case int64:
				resolved[name] = float64(n)
			default:
				return nil, fmt.Errorf("%w: parameter %q must be a number", ErrInvalidParams, name)
			}
		}
	}

	return resolved, nil
}
