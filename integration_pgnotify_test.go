package arcana

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestPGNotifyTriggerIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	pool := startPostgres(t, ctx)

	_, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS trigger_test (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT NOT NULL,
			status TEXT DEFAULT 'active'
		)
	`)
	if err != nil {
		t.Fatalf("create table: %v", err)
	}

	tables := map[string][]string{
		"trigger_test": {"name", "email", "status"},
	}
	err = EnsureTriggers(ctx, pgxExec{pool}, "arcana_changes", tables)
	if err != nil {
		t.Fatalf("ensure triggers: %v", err)
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	defer conn.Release()

	_, err = conn.Exec(ctx, "LISTEN arcana_changes")
	if err != nil {
		t.Fatalf("LISTEN: %v", err)
	}

	// INSERT
	_, err = pool.Exec(ctx, `INSERT INTO trigger_test (id, name, email) VALUES ('t1', 'Alice', 'alice@test.com')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	notification := waitNotification(t, ctx, conn.Conn(), 5*time.Second)
	var payload struct {
		Table   string   `json:"table"`
		ID      string   `json:"id"`
		Op      string   `json:"op"`
		Columns []string `json:"columns"`
	}
	if err := json.Unmarshal([]byte(notification.Payload), &payload); err != nil {
		t.Fatalf("unmarshal INSERT payload: %v (raw: %s)", err, notification.Payload)
	}
	if payload.Table != "trigger_test" || payload.ID != "t1" || payload.Op != "INSERT" {
		t.Fatalf("unexpected INSERT payload: %+v", payload)
	}

	// UPDATE single column
	_, err = pool.Exec(ctx, `UPDATE trigger_test SET name = 'Alice Updated' WHERE id = 't1'`)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	notification = waitNotification(t, ctx, conn.Conn(), 5*time.Second)
	if err := json.Unmarshal([]byte(notification.Payload), &payload); err != nil {
		t.Fatalf("unmarshal UPDATE payload: %v (raw: %s)", err, notification.Payload)
	}
	if payload.Op != "UPDATE" {
		t.Fatalf("expected UPDATE, got %s", payload.Op)
	}
	if len(payload.Columns) != 1 || payload.Columns[0] != "name" {
		t.Fatalf("expected columns [name], got %v", payload.Columns)
	}

	// UPDATE non-tracked column should not notify (but we have status tracked, so update a non-existent tracked scenario)
	// Instead, update without changing any tracked column value
	_, err = pool.Exec(ctx, `UPDATE trigger_test SET name = 'Alice Updated' WHERE id = 't1'`)
	if err != nil {
		t.Fatalf("noop update: %v", err)
	}

	// Should NOT receive notification for no-change update
	noNotification := waitNotificationTimeout(ctx, conn.Conn(), 500*time.Millisecond)
	if noNotification != nil {
		t.Fatalf("expected no notification for noop update, got: %s", noNotification.Payload)
	}

	// DELETE
	_, err = pool.Exec(ctx, `DELETE FROM trigger_test WHERE id = 't1'`)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	notification = waitNotification(t, ctx, conn.Conn(), 5*time.Second)
	if err := json.Unmarshal([]byte(notification.Payload), &payload); err != nil {
		t.Fatalf("unmarshal DELETE payload: %v (raw: %s)", err, notification.Payload)
	}
	if payload.Op != "DELETE" || payload.ID != "t1" {
		t.Fatalf("unexpected DELETE payload: %+v", payload)
	}
}

type pgxExec struct {
	pool *pgxpool.Pool
}

func (p pgxExec) Exec(ctx context.Context, sql string, args ...any) (any, error) {
	return p.pool.Exec(ctx, sql, args...)
}

func waitNotification(t *testing.T, ctx context.Context, conn *pgx.Conn, timeout time.Duration) *pgconn.Notification {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	n, err := conn.WaitForNotification(ctx)
	if err != nil {
		t.Fatalf("wait for notification: %v", err)
	}
	return n
}

func waitNotificationTimeout(ctx context.Context, conn *pgx.Conn, timeout time.Duration) *pgconn.Notification {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	n, err := conn.WaitForNotification(ctx)
	if err != nil {
		return nil
	}
	return n
}
