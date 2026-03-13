package arcana

import "context"

// MutationDef defines a named write operation with parameter validation
// and a handler function. Register mutations with Engine.RegisterMutation.
type MutationDef struct {
	Key     string
	Params  ParamSchema
	Handler MutationFunc
}

// MutationFunc executes a mutation. Identity is available via IdentityFromCtx(ctx).
// Returns a result containing response data and a list of changes for reactive invalidation.
type MutationFunc func(ctx context.Context, q Querier, p Params) (*MutationResult, error)

// MutationResult holds the outcome of a mutation.
type MutationResult struct {
	// Data is returned to the client in the response.
	Data any `json:"data,omitempty"`

	// Changes lists the tables/rows affected by this mutation.
	// Arcana auto-notifies these after the handler returns,
	// triggering reactive invalidation of affected views.
	Changes []Change `json:"-"`
}

// MutateRequest is the input for a mutation execution.
type MutateRequest struct {
	Action      string         `json:"action"`
	Params      map[string]any `json:"params"`
	SeanceID    string         `json:"-"`
	UserID      string         `json:"-"`
	WorkspaceID string         `json:"-"`
}

// MutateResponse is returned after a successful mutation.
type MutateResponse struct {
	OK   bool `json:"ok"`
	Data any  `json:"data,omitempty"`
}

// executeMutation validates params, runs the handler, and auto-notifies changes.
func executeMutation(
	ctx context.Context,
	def *MutationDef,
	pool Querier,
	params map[string]any,
	notifyFn func(Change),
) (*MutationResult, error) {
	resolved, err := ValidateParams(def.Params, params, true)
	if err != nil {
		return nil, err
	}

	result, err := def.Handler(ctx, pool, NewParams(resolved))
	if err != nil {
		return nil, err
	}

	if result == nil {
		result = &MutationResult{}
	}

	for _, ch := range result.Changes {
		notifyFn(ch)
	}

	return result, nil
}
