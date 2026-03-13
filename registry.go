package arcana

import (
	"fmt"
	"sort"
	"sync"
)

// Registry stores graph definitions, mutation definitions, and provides lookup indices.
type Registry struct {
	mu        sync.RWMutex
	graphs    map[string]*GraphDef           // key → definition
	mutations map[string]*MutationDef        // key → mutation definition
	byTable   map[string][]*GraphDef         // table → affected graphs
	repTable  map[string]map[string]struct{} // table → merged columns
}

// NewRegistry creates an empty Registry.
func NewRegistry() *Registry {
	return &Registry{
		graphs:    make(map[string]*GraphDef),
		mutations: make(map[string]*MutationDef),
		byTable:   make(map[string][]*GraphDef),
		repTable:  make(map[string]map[string]struct{}),
	}
}

// Register adds one or more graph definitions to the registry.
// Returns an error if a graph key is already registered.
func (r *Registry) Register(defs ...GraphDef) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := range defs {
		def := &defs[i]
		if def.Key == "" {
			return fmt.Errorf("arcana: graph definition must have a key")
		}
		if _, exists := r.graphs[def.Key]; exists {
			return fmt.Errorf("arcana: graph %q already registered", def.Key)
		}

		r.graphs[def.Key] = def

		for _, dep := range def.Deps {
			r.byTable[dep.Table] = append(r.byTable[dep.Table], def)
			if _, ok := r.repTable[dep.Table]; !ok {
				r.repTable[dep.Table] = make(map[string]struct{})
			}
			for _, col := range dep.Columns {
				r.repTable[dep.Table][col] = struct{}{}
			}
		}
	}

	return nil
}

// RegisterMutation adds one or more mutation definitions to the registry.
func (r *Registry) RegisterMutation(defs ...MutationDef) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := range defs {
		def := &defs[i]
		if def.Key == "" {
			return fmt.Errorf("arcana: mutation definition must have a key")
		}
		if _, exists := r.mutations[def.Key]; exists {
			return fmt.Errorf("arcana: mutation %q already registered", def.Key)
		}
		r.mutations[def.Key] = def
	}

	return nil
}

// GetMutation returns a mutation definition by key.
func (r *Registry) GetMutation(key string) (*MutationDef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.mutations[key]
	return def, ok
}

// MutationKeys returns all registered mutation keys.
func (r *Registry) MutationKeys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keys := make([]string, 0, len(r.mutations))
	for k := range r.mutations {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Get returns a graph definition by key.
func (r *Registry) Get(key string) (*GraphDef, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.graphs[key]
	return def, ok
}

// GetByTable returns all graph definitions that depend on the given table.
func (r *Registry) GetByTable(table string) []*GraphDef {
	r.mu.RLock()
	defer r.mu.RUnlock()

	defs := r.byTable[table]
	out := make([]*GraphDef, len(defs))
	copy(out, defs)
	return out
}

// RepTable returns the representation table: table → sorted column list.
// This is the union of all columns declared in graph Deps for each table.
func (r *Registry) RepTable() map[string][]string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string][]string, len(r.repTable))
	for table, colSet := range r.repTable {
		cols := make([]string, 0, len(colSet))
		for col := range colSet {
			cols = append(cols, col)
		}
		sort.Strings(cols)
		result[table] = cols
	}
	return result
}

// Keys returns all registered graph keys.
func (r *Registry) Keys() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	keys := make([]string, 0, len(r.graphs))
	for k := range r.graphs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// HasColumn checks if a table+column is tracked by any graph.
func (r *Registry) HasColumn(table, column string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	cols, ok := r.repTable[table]
	if !ok {
		return false
	}
	_, exists := cols[column]
	return exists
}

// GraphCount returns the number of registered graphs.
func (r *Registry) GraphCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.graphs)
}
