package arcana

import (
	"sync"
	"sync/atomic"
)

// DataStore is the in-memory normalized data store.
// Structure: workspace_id → table_name → row_id → StoreRow.
type DataStore struct {
	mu         sync.RWMutex
	workspaces map[string]*WorkspaceStore
}

// NewDataStore creates an empty DataStore.
func NewDataStore() *DataStore {
	return &DataStore{
		workspaces: make(map[string]*WorkspaceStore),
	}
}

// WorkspaceStore holds all table data for a single workspace.
type WorkspaceStore struct {
	mu     sync.RWMutex
	tables map[string]*TableStore
}

// TableStore holds all rows for a single table within a workspace.
type TableStore struct {
	mu   sync.RWMutex
	rows map[string]*StoreRow
}

// StoreRow is a single row in the normalized store.
type StoreRow struct {
	mu       sync.RWMutex
	Fields   map[string]any
	Version  int64
	RefCount int32 // managed atomically
}

func (s *DataStore) getOrCreateWorkspace(wsID string) *WorkspaceStore {
	s.mu.RLock()
	ws, ok := s.workspaces[wsID]
	s.mu.RUnlock()
	if ok {
		return ws
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	ws, ok = s.workspaces[wsID]
	if ok {
		return ws
	}
	ws = &WorkspaceStore{tables: make(map[string]*TableStore)}
	s.workspaces[wsID] = ws
	return ws
}

func (ws *WorkspaceStore) getOrCreateTable(table string) *TableStore {
	ws.mu.RLock()
	ts, ok := ws.tables[table]
	ws.mu.RUnlock()
	if ok {
		return ts
	}

	ws.mu.Lock()
	defer ws.mu.Unlock()
	ts, ok = ws.tables[table]
	if ok {
		return ts
	}
	ts = &TableStore{rows: make(map[string]*StoreRow)}
	ws.tables[table] = ts
	return ts
}

// Get retrieves a row from the store. Returns nil, false if not found.
func (s *DataStore) Get(wsID, table, rowID string) (*StoreRow, bool) {
	s.mu.RLock()
	ws, ok := s.workspaces[wsID]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}

	ws.mu.RLock()
	ts, ok := ws.tables[table]
	ws.mu.RUnlock()
	if !ok {
		return nil, false
	}

	ts.mu.RLock()
	row, ok := ts.rows[rowID]
	ts.mu.RUnlock()
	return row, ok
}

// GetFields returns a copy of the row's fields. Returns nil if not found.
func (s *DataStore) GetFields(wsID, table, rowID string) map[string]any {
	row, ok := s.Get(wsID, table, rowID)
	if !ok {
		return nil
	}

	row.mu.RLock()
	defer row.mu.RUnlock()
	cp := make(map[string]any, len(row.Fields))
	for k, v := range row.Fields {
		cp[k] = v
	}
	return cp
}

// Upsert inserts or updates a row. Returns the diff (patch operations),
// whether the row was newly created, and the old fields snapshot.
func (s *DataStore) Upsert(wsID, table, rowID string, fields map[string]any) (oldFields map[string]any, diff []PatchOp, isNew bool) {
	ws := s.getOrCreateWorkspace(wsID)
	ts := ws.getOrCreateTable(table)

	ts.mu.Lock()
	row, exists := ts.rows[rowID]
	if !exists {
		newFields := make(map[string]any, len(fields))
		for k, v := range fields {
			newFields[k] = v
		}
		ts.rows[rowID] = &StoreRow{
			Fields:  newFields,
			Version: 1,
		}
		ts.mu.Unlock()

		patch := make([]PatchOp, 0, len(fields))
		for k, v := range fields {
			patch = append(patch, PatchOp{Op: "add", Path: "/" + k, Value: v})
		}
		return nil, patch, true
	}
	ts.mu.Unlock()

	row.mu.Lock()
	defer row.mu.Unlock()

	oldFields = make(map[string]any, len(row.Fields))
	for k, v := range row.Fields {
		oldFields[k] = v
	}

	diff = DiffFields(row.Fields, fields)
	if len(diff) == 0 {
		return oldFields, nil, false
	}

	for k, v := range fields {
		row.Fields[k] = v
	}
	// Remove fields not in the new set
	for k := range row.Fields {
		if _, ok := fields[k]; !ok {
			delete(row.Fields, k)
		}
	}
	row.Version++
	return oldFields, diff, false
}

// Delete removes a row from the store. Returns the deleted row and true,
// or nil and false if the row didn't exist.
func (s *DataStore) Delete(wsID, table, rowID string) (*StoreRow, bool) {
	s.mu.RLock()
	ws, ok := s.workspaces[wsID]
	s.mu.RUnlock()
	if !ok {
		return nil, false
	}

	ws.mu.RLock()
	ts, ok := ws.tables[table]
	ws.mu.RUnlock()
	if !ok {
		return nil, false
	}

	ts.mu.Lock()
	row, ok := ts.rows[rowID]
	if ok {
		delete(ts.rows, rowID)
	}
	ts.mu.Unlock()
	return row, ok
}

// IncrRef atomically increments the reference count of a row.
func (s *DataStore) IncrRef(wsID, table, rowID string) {
	row, ok := s.Get(wsID, table, rowID)
	if !ok {
		return
	}
	atomic.AddInt32(&row.RefCount, 1)
}

// DecrRef atomically decrements the reference count. Returns true if
// the count reached zero (candidate for GC).
func (s *DataStore) DecrRef(wsID, table, rowID string) bool {
	row, ok := s.Get(wsID, table, rowID)
	if !ok {
		return false
	}
	newVal := atomic.AddInt32(&row.RefCount, -1)
	return newVal <= 0
}

// GC removes a row if its RefCount is zero or negative.
func (s *DataStore) GC(wsID, table, rowID string) {
	row, ok := s.Get(wsID, table, rowID)
	if !ok {
		return
	}
	if atomic.LoadInt32(&row.RefCount) > 0 {
		return
	}
	s.Delete(wsID, table, rowID)
}

// GCAll iterates all rows and removes those with RefCount <= 0.
func (s *DataStore) GCAll() {
	s.mu.RLock()
	wsIDs := make([]string, 0, len(s.workspaces))
	for wsID := range s.workspaces {
		wsIDs = append(wsIDs, wsID)
	}
	s.mu.RUnlock()

	for _, wsID := range wsIDs {
		s.mu.RLock()
		ws, ok := s.workspaces[wsID]
		s.mu.RUnlock()
		if !ok {
			continue
		}

		ws.mu.RLock()
		tableNames := make([]string, 0, len(ws.tables))
		for name := range ws.tables {
			tableNames = append(tableNames, name)
		}
		ws.mu.RUnlock()

		for _, table := range tableNames {
			ws.mu.RLock()
			ts, ok := ws.tables[table]
			ws.mu.RUnlock()
			if !ok {
				continue
			}

			ts.mu.RLock()
			var candidates []string
			for id, row := range ts.rows {
				if atomic.LoadInt32(&row.RefCount) <= 0 {
					candidates = append(candidates, id)
				}
			}
			ts.mu.RUnlock()

			for _, id := range candidates {
				s.GC(wsID, table, id)
			}
		}
	}
}

// RowCount returns the total number of rows across all workspaces and tables.
func (s *DataStore) RowCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for _, ws := range s.workspaces {
		ws.mu.RLock()
		for _, ts := range ws.tables {
			ts.mu.RLock()
			count += len(ts.rows)
			ts.mu.RUnlock()
		}
		ws.mu.RUnlock()
	}
	return count
}

// RowVersion returns the version of a row, or 0 if not found.
func (s *DataStore) RowVersion(wsID, table, rowID string) int64 {
	row, ok := s.Get(wsID, table, rowID)
	if !ok {
		return 0
	}
	row.mu.RLock()
	defer row.mu.RUnlock()
	return row.Version
}
