package arcana

import "sync"

// Result accumulates the output of a graph Factory execution.
// It holds both normalized table rows and the ref structure that defines the view.
type Result struct {
	mu     sync.Mutex
	refs   []Ref
	tables map[string]map[string]map[string]any // table → rowID → fields
	total  int                                   // total count for paginated results
}

// NewResult creates an empty Result.
func NewResult() *Result {
	return &Result{
		tables: make(map[string]map[string]map[string]any),
	}
}

// AddRow adds a normalized row to the result's table data.
// If the row already exists, fields are merged (new values overwrite).
func (r *Result) AddRow(table, id string, fields map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()

	tbl, ok := r.tables[table]
	if !ok {
		tbl = make(map[string]map[string]any)
		r.tables[table] = tbl
	}

	existing, ok := tbl[id]
	if !ok {
		row := make(map[string]any, len(fields))
		for k, v := range fields {
			row[k] = v
		}
		tbl[id] = row
		return
	}

	for k, v := range fields {
		existing[k] = v
	}
}

// AddRef appends a reference to the result's ref list.
func (r *Result) AddRef(ref Ref) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refs = append(r.refs, ref)
}

// Refs returns the accumulated references.
func (r *Result) Refs() []Ref {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Ref, len(r.refs))
	copy(out, r.refs)
	return out
}

// Tables returns the accumulated table data.
// Returns: table_name → row_id → fields.
func (r *Result) Tables() map[string]map[string]map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make(map[string]map[string]map[string]any, len(r.tables))
	for tbl, rows := range r.tables {
		outRows := make(map[string]map[string]any, len(rows))
		for id, fields := range rows {
			outFields := make(map[string]any, len(fields))
			for k, v := range fields {
				outFields[k] = v
			}
			outRows[id] = outFields
		}
		out[tbl] = outRows
	}
	return out
}

// SetTotal stores the total row count for paginated graph results.
// This is typically populated from a COUNT(*) OVER() window function.
func (r *Result) SetTotal(n int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.total = n
}

// Total returns the total row count set by SetTotal.
// Returns 0 if SetTotal was never called.
func (r *Result) Total() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

// RowCount returns the total number of rows across all tables.
func (r *Result) RowCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	count := 0
	for _, rows := range r.tables {
		count += len(rows)
	}
	return count
}
