package arcana

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestExplicitNotifier(t *testing.T) {
	n := NewExplicitNotifier(10)

	var mu sync.Mutex
	var received []Change

	n.Start(context.Background(), func(c Change) {
		mu.Lock()
		received = append(received, c)
		mu.Unlock()
	})

	n.Send(Change{Table: "users", RowID: "u1", Columns: []string{"name"}})
	n.Send(Change{Table: "orders", RowID: "o1", Op: "INSERT"})

	// Wait for processing
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	count := len(received)
	mu.Unlock()

	if count != 2 {
		t.Fatalf("expected 2 changes, got %d", count)
	}

	n.Stop()
}

func TestExplicitNotifierBufferFull(t *testing.T) {
	n := NewExplicitNotifier(1)

	// Don't start — so nothing drains the channel
	n.ch <- Change{Table: "t1"} // fills buffer

	// Should not block (drops the change)
	n.Send(Change{Table: "t2"})
}

func TestParsePGNotifyPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    Change
		wantErr bool
	}{
		{
			name:    "full update",
			payload: `{"table":"users","id":"u1","op":"UPDATE","columns":["name","email"]}`,
			want:    Change{Table: "users", RowID: "u1", Op: "UPDATE", Columns: []string{"name", "email"}},
		},
		{
			name:    "insert",
			payload: `{"table":"orders","id":"o1","op":"INSERT"}`,
			want:    Change{Table: "orders", RowID: "o1", Op: "INSERT"},
		},
		{
			name:    "delete",
			payload: `{"table":"users","id":"u1","op":"DELETE"}`,
			want:    Change{Table: "users", RowID: "u1", Op: "DELETE"},
		},
		{
			name:    "missing table",
			payload: `{"id":"u1","op":"UPDATE"}`,
			wantErr: true,
		},
		{
			name:    "invalid json",
			payload: `not json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePGNotifyPayload(tt.payload)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Table != tt.want.Table || got.RowID != tt.want.RowID || got.Op != tt.want.Op {
				t.Fatalf("mismatch: got %+v, want %+v", got, tt.want)
			}
			if len(got.Columns) != len(tt.want.Columns) {
				t.Fatalf("columns count mismatch")
			}
		})
	}
}

// mockPGConn implements PGNotifyConn for testing.
type mockPGConn struct {
	mu            sync.Mutex
	notifications []*Notification
	listenCalled  bool
	waitIdx       int
	waitCh        chan struct{}
}

func newMockPGConn() *mockPGConn {
	return &mockPGConn{
		waitCh: make(chan struct{}),
	}
}

func (c *mockPGConn) Exec(_ context.Context, sql string, _ ...any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sql == "LISTEN arcana_changes" {
		c.listenCalled = true
	}
	return nil
}

func (c *mockPGConn) WaitForNotification(ctx context.Context) (*Notification, error) {
	c.mu.Lock()
	if c.waitIdx < len(c.notifications) {
		n := c.notifications[c.waitIdx]
		c.waitIdx++
		c.mu.Unlock()
		return n, nil
	}
	c.mu.Unlock()

	// Block until context cancelled
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *mockPGConn) addNotification(payload string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.notifications = append(c.notifications, &Notification{
		Channel: "arcana_changes",
		Payload: payload,
	})
}

func TestPGNotifyListener(t *testing.T) {
	conn := newMockPGConn()
	conn.addNotification(`{"table":"users","id":"u1","op":"UPDATE","columns":["name"]}`)

	var mu sync.Mutex
	var received []Change

	listener := NewPGNotifyListener(PGNotifyConfig{Conn: conn})
	err := listener.Start(context.Background(), func(c Change) {
		mu.Lock()
		received = append(received, c)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	count := len(received)
	mu.Unlock()

	if count != 1 {
		t.Fatalf("expected 1 change, got %d", count)
	}

	mu.Lock()
	if received[0].Table != "users" || received[0].RowID != "u1" {
		t.Fatalf("unexpected change: %+v", received[0])
	}
	mu.Unlock()

	if !conn.listenCalled {
		t.Fatal("LISTEN should have been called")
	}

	listener.Stop()
}
