package arcana

import (
	"context"
	"strings"
	"testing"
)

func TestGenerateTriggerSQL(t *testing.T) {
	tables := map[string][]string{
		"users":  {"name", "email", "role"},
		"orders": {"status", "total"},
	}

	stmts := GenerateTriggerSQL("arcana_changes", tables)

	// 3 statements per table: function + drop trigger + create trigger
	if len(stmts) != 6 {
		t.Fatalf("expected 6 statements, got %d", len(stmts))
	}

	joined := strings.Join(stmts, "\n")

	// Function should contain column tracking for users
	if !strings.Contains(joined, "NEW.name IS DISTINCT FROM OLD.name") {
		t.Fatal("expected column tracking for name")
	}
	if !strings.Contains(joined, "NEW.email IS DISTINCT FROM OLD.email") {
		t.Fatal("expected column tracking for email")
	}

	// Function should reference the channel
	if !strings.Contains(joined, "arcana_changes") {
		t.Fatal("expected channel name in SQL")
	}

	// Should have CREATE OR REPLACE FUNCTION
	if !strings.Contains(joined, "CREATE OR REPLACE FUNCTION arcana_notify_users()") {
		t.Fatal("expected CREATE OR REPLACE FUNCTION for users")
	}
	if !strings.Contains(joined, "CREATE OR REPLACE FUNCTION arcana_notify_orders()") {
		t.Fatal("expected CREATE OR REPLACE FUNCTION for orders")
	}
}

func TestGenerateTriggerSQLInsertDelete(t *testing.T) {
	tables := map[string][]string{
		"items": {"name"},
	}

	stmts := GenerateTriggerSQL("ch", tables)
	joined := strings.Join(stmts, "\n")

	if !strings.Contains(joined, "TG_OP = 'INSERT'") {
		t.Fatal("expected INSERT handling")
	}
	if !strings.Contains(joined, "TG_OP = 'DELETE'") {
		t.Fatal("expected DELETE handling")
	}
	if !strings.Contains(joined, "TG_OP = 'UPDATE'") {
		t.Fatal("expected UPDATE handling")
	}
	if !strings.Contains(joined, "AFTER INSERT OR UPDATE OR DELETE") {
		t.Fatal("expected trigger to cover INSERT OR UPDATE OR DELETE")
	}
}

func TestGenerateTriggerSQLEmptyColumns(t *testing.T) {
	tables := map[string][]string{
		"events": {},
	}

	stmts := GenerateTriggerSQL("arcana_changes", tables)

	// Still generates valid SQL: 3 per table
	if len(stmts) != 3 {
		t.Fatalf("expected 3 statements, got %d", len(stmts))
	}

	joined := strings.Join(stmts, "\n")

	// Should still have INSERT/DELETE handling
	if !strings.Contains(joined, "TG_OP = 'INSERT'") {
		t.Fatal("expected INSERT handling even with no columns")
	}

	// UPDATE with no tracked columns should still notify (no column diff check)
	if !strings.Contains(joined, "CREATE OR REPLACE FUNCTION arcana_notify_events()") {
		t.Fatal("expected function for events")
	}
}

func TestGenerateTriggerSQLPayloadSafety(t *testing.T) {
	// Generate a table with many long column names to test payload size safety
	cols := make([]string, 200)
	for i := range cols {
		cols[i] = strings.Repeat("x", 30) + "_" + string(rune('a'+i%26)) + string(rune('0'+i/26))
	}
	tables := map[string][]string{
		"big_table": cols,
	}

	stmts := GenerateTriggerSQL("arcana_changes", tables)
	joined := strings.Join(stmts, "\n")

	// Should contain the 7500 byte safety check
	if !strings.Contains(joined, "7500") {
		t.Fatal("expected payload length safety check at 7500 bytes")
	}
}

func TestEnsureTriggers(t *testing.T) {
	tables := map[string][]string{
		"users": {"name", "email"},
	}

	var executed []string
	exec := &mockTriggerExec{
		execFn: func(_ context.Context, sql string, _ ...any) (any, error) {
			executed = append(executed, sql)
			return nil, nil
		},
	}

	err := EnsureTriggers(context.Background(), exec, "arcana_changes", tables)
	if err != nil {
		t.Fatalf("EnsureTriggers failed: %v", err)
	}

	if len(executed) != 3 {
		t.Fatalf("expected 3 executed statements, got %d", len(executed))
	}
}

type mockTriggerExec struct {
	execFn func(ctx context.Context, sql string, args ...any) (any, error)
}

func (m *mockTriggerExec) Exec(ctx context.Context, sql string, args ...any) (any, error) {
	return m.execFn(ctx, sql, args...)
}
