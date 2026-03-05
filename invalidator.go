package arcana

import (
	"context"
	"log/slog"
)

// SubscriptionStore provides access to active subscriptions for invalidation.
type SubscriptionStore interface {
	// GetByGraphKey returns all active subscriptions for a graph key.
	GetByGraphKey(graphKey string) []*Subscription

	// UpdateSubscription updates a subscription's LastRefs, Version, and records
	// a version history entry for catch-up sync.
	UpdateSubscription(subID string, refs []Ref, version int64, entry *VersionEntry)
}

// Invalidator processes data changes and dispatches updates to affected subscriptions.
type Invalidator struct {
	registry  *Registry
	store     *DataStore
	transport Transport
	subs      SubscriptionStore
	pool      Querier
	logger    *slog.Logger
}

// NewInvalidator creates an Invalidator.
func NewInvalidator(registry *Registry, store *DataStore, transport Transport, subs SubscriptionStore, pool Querier, logger *slog.Logger) *Invalidator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Invalidator{
		registry:  registry,
		store:     store,
		transport: transport,
		subs:      subs,
		pool:      pool,
		logger:    logger,
	}
}

// Invalidate processes a Change event: finds affected graphs, filters by columns,
// and dispatches table_diff and view_diff to affected subscriptions.
func (inv *Invalidator) Invalidate(ctx context.Context, change Change) {
	defs := inv.registry.GetByTable(change.Table)
	if len(defs) == 0 {
		return
	}

	for _, def := range defs {
		if !inv.columnsOverlap(def, change) {
			continue
		}

		subs := inv.subs.GetByGraphKey(def.Key)
		if len(subs) == 0 {
			continue
		}

		for _, sub := range subs {
			inv.invalidateSubscription(ctx, def, sub, change)
		}
	}
}

// columnsOverlap checks if the changed columns intersect with the graph's deps.
// If change.Columns is empty, treat as all columns changed.
func (inv *Invalidator) columnsOverlap(def *GraphDef, change Change) bool {
	if len(change.Columns) == 0 {
		return true
	}

	for _, dep := range def.Deps {
		if dep.Table != change.Table {
			continue
		}
		for _, depCol := range dep.Columns {
			for _, changedCol := range change.Columns {
				if depCol == changedCol {
					return true
				}
			}
		}
	}

	return false
}

func (inv *Invalidator) invalidateSubscription(ctx context.Context, def *GraphDef, sub *Subscription, change Change) {
	if def.Factory == nil {
		return
	}

	subCtx := WithIdentity(ctx, &Identity{
		SeanceID:    sub.SeanceID,
		UserID:      sub.UserID,
		WorkspaceID: sub.WorkspaceID,
	})

	result, err := def.Factory(subCtx, inv.pool, NewParams(sub.Params))
	if err != nil {
		inv.logger.Error("factory execution failed",
			"graph", def.Key,
			"subscription", sub.ID,
			"error", err,
		)
		return
	}

	// Process table diffs — update DataStore and send table_diff messages
	tables := result.Tables()
	for table, rows := range tables {
		for rowID, fields := range rows {
			oldFields, diff, _ := inv.store.Upsert(sub.WorkspaceID, table, rowID, fields)
			if len(diff) == 0 && oldFields != nil {
				continue
			}

			ver := inv.store.RowVersion(sub.WorkspaceID, table, rowID)

			msg := Message{
				Type: "table_diff",
				Data: map[string]any{
					"table": table,
					"id":    rowID,
					"ver":   ver,
					"patch": diff,
				},
			}

			if err := inv.transport.SendToWorkspace(ctx, sub.WorkspaceID, msg); err != nil {
				inv.logger.Error("failed to send table_diff",
					"workspace", sub.WorkspaceID,
					"error", err,
				)
			}
		}
	}

	// Process view diffs — compare refs and send view_diff messages
	newRefs := result.Refs()
	refsPatch := DiffRefs(sub.LastRefs, newRefs)

	if len(refsPatch) > 0 {
		newVersion := sub.Version + 1

		// Collect new table data for added refs
		newTables := make(map[string]map[string]map[string]any)
		for _, op := range refsPatch {
			if op.Op == "add" {
				if ref, ok := op.Value.(Ref); ok {
					inv.collectRefTables(sub.WorkspaceID, ref, tables, newTables)
				}
			}
		}

		msg := Message{
			Type: "view_diff",
			Data: map[string]any{
				"view":        def.Key,
				"params_hash": sub.ParamsHash,
				"version":     newVersion,
				"refs_patch":  refsPatch,
				"tables":      newTables,
			},
		}

		if err := inv.transport.SendToSeance(ctx, sub.SeanceID, msg); err != nil {
			inv.logger.Error("failed to send view_diff",
				"seance", sub.SeanceID,
				"error", err,
			)
		}

		inv.subs.UpdateSubscription(sub.ID, newRefs, newVersion, &VersionEntry{
			Version:   newVersion,
			RefsPatch: refsPatch,
			Tables:    newTables,
		})
	}
}

// collectRefTables gathers table data for a ref and its nested refs.
func (inv *Invalidator) collectRefTables(wsID string, ref Ref, source map[string]map[string]map[string]any, dest map[string]map[string]map[string]any) {
	if _, ok := dest[ref.Table]; !ok {
		dest[ref.Table] = make(map[string]map[string]any)
	}
	if fields, ok := source[ref.Table][ref.ID]; ok {
		dest[ref.Table][ref.ID] = fields
	} else if fields := inv.store.GetFields(wsID, ref.Table, ref.ID); fields != nil {
		dest[ref.Table][ref.ID] = fields
	}

	for _, nested := range ref.Nested {
		inv.collectRefTables(wsID, nested, source, dest)
	}
}
