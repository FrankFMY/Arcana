package arcana

import (
	"context"
	"testing"
	"time"
)

func TestChangeBatcherCoalescesByTableAndRow(t *testing.T) {
	var handled []Change
	batcher := newChangeBatcher(context.Background(), time.Hour, func(change Change) {
		handled = append(handled, change)
	})

	batcher.Handle(Change{Table: "users", RowID: "u1", Op: "UPDATE", Columns: []string{"name"}})
	batcher.Handle(Change{Table: "users", RowID: "u1", Op: "UPDATE", Columns: []string{"email", "name"}})
	batcher.Handle(Change{Table: "users", RowID: "u2", Op: "UPDATE", Columns: []string{"name"}})
	batcher.Flush()

	if len(handled) != 2 {
		t.Fatalf("expected 2 coalesced changes, got %d", len(handled))
	}

	var u1 Change
	for _, change := range handled {
		if change.RowID == "u1" {
			u1 = change
			break
		}
	}
	if u1.RowID == "" {
		t.Fatal("expected merged u1 change")
	}

	wantCols := map[string]bool{"name": true, "email": true}
	if len(u1.Columns) != len(wantCols) {
		t.Fatalf("expected columns %v, got %v", wantCols, u1.Columns)
	}
	for _, col := range u1.Columns {
		if !wantCols[col] {
			t.Fatalf("unexpected column %q in merged change", col)
		}
	}
}

func TestChangeBatcherTreatsEmptyColumnsAsAllColumns(t *testing.T) {
	var handled []Change
	batcher := newChangeBatcher(context.Background(), time.Hour, func(change Change) {
		handled = append(handled, change)
	})

	batcher.Handle(Change{Table: "users", RowID: "u1", Columns: []string{"name"}})
	batcher.Handle(Change{Table: "users", RowID: "u1"})
	batcher.Flush()

	if len(handled) != 1 {
		t.Fatalf("expected 1 coalesced change, got %d", len(handled))
	}
	if len(handled[0].Columns) != 0 {
		t.Fatalf("expected empty columns to mean all columns, got %v", handled[0].Columns)
	}
}

func TestChangeBatcherCanBypassDebounce(t *testing.T) {
	var handled []Change
	batcher := newChangeBatcher(context.Background(), 0, func(change Change) {
		handled = append(handled, change)
	})

	batcher.Handle(Change{Table: "users", RowID: "u1"})

	if len(handled) != 1 {
		t.Fatalf("expected immediate handling, got %d changes", len(handled))
	}
}
