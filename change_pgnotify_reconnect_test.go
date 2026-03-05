package arcana

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type mockReconnectConn struct {
	mu            sync.Mutex
	calls         int
	failUntil     int
	notifications chan *Notification
}

func (c *mockReconnectConn) Exec(ctx context.Context, sql string, args ...any) error {
	return nil
}

func (c *mockReconnectConn) WaitForNotification(ctx context.Context) (*Notification, error) {
	c.mu.Lock()
	c.calls++
	n := c.calls
	c.mu.Unlock()

	if n <= c.failUntil {
		return nil, errors.New("connection lost")
	}

	select {
	case notif := <-c.notifications:
		return notif, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *mockReconnectConn) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func TestPGNotifyListenerReconnectBackoff(t *testing.T) {
	conn := &mockReconnectConn{
		failUntil:     3,
		notifications: make(chan *Notification, 1),
	}

	var received atomic.Int32

	listener := NewPGNotifyListener(PGNotifyConfig{
		Conn:    conn,
		Channel: "test_channel",
	})
	listener.initialBackoff = 10 * time.Millisecond

	ctx := context.Background()
	err := listener.Start(ctx, func(c Change) {
		received.Add(1)
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	conn.notifications <- &Notification{
		Channel: "test_channel",
		Payload: `{"table":"users","id":"1","op":"INSERT"}`,
	}

	deadline := time.After(5 * time.Second)
	for received.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for notification after retries")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	if got := conn.callCount(); got < 4 {
		t.Fatalf("expected at least 4 WaitForNotification calls (3 failures + 1 success), got %d", got)
	}

	_ = listener.Stop()
}

func TestPGNotifyListenerMaxRetries(t *testing.T) {
	conn := &mockReconnectConn{
		failUntil:     100,
		notifications: make(chan *Notification),
	}

	listener := NewPGNotifyListener(PGNotifyConfig{
		Conn:       conn,
		Channel:    "test_channel",
		MaxRetries: 3,
	})
	listener.initialBackoff = 5 * time.Millisecond

	ctx := context.Background()
	err := listener.Start(ctx, func(c Change) {})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for listener to stop after max retries")
		default:
			if conn.callCount() >= 4 {
				time.Sleep(50 * time.Millisecond)
				got := conn.callCount()
				if got != 4 {
					t.Fatalf("expected exactly 4 calls (3 retries + 1 exceeding), got %d", got)
				}
				_ = listener.Stop()
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestPGNotifyListenerResetBackoffOnSuccess(t *testing.T) {
	conn := &mockResetConn{
		sequence:      []connAction{fail, fail, succeed, fail, fail, succeed},
		notifications: make(chan *Notification, 2),
	}
	conn.notifications <- &Notification{
		Channel: "test_channel",
		Payload: `{"table":"users","id":"1","op":"INSERT"}`,
	}
	conn.notifications <- &Notification{
		Channel: "test_channel",
		Payload: `{"table":"users","id":"2","op":"UPDATE"}`,
	}

	var received atomic.Int32

	listener := NewPGNotifyListener(PGNotifyConfig{
		Conn:    conn,
		Channel: "test_channel",
	})
	listener.initialBackoff = 10 * time.Millisecond

	ctx := context.Background()
	err := listener.Start(ctx, func(c Change) {
		received.Add(1)
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	deadline := time.After(5 * time.Second)
	for received.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for both notifications")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	_ = listener.Stop()

	backoffs := conn.getBackoffs()
	if len(backoffs) < 4 {
		t.Fatalf("expected at least 4 backoff waits, got %d", len(backoffs))
	}

	// First group: fail, fail -> backoffs should be ~10ms, ~20ms
	// After success, second group: fail, fail -> backoffs should reset to ~10ms, ~20ms
	// The third backoff (first of second group) should be close to the first backoff,
	// proving the reset happened.
	if backoffs[2] > backoffs[1] {
		t.Fatalf("backoff was not reset after success: third backoff %v > second backoff %v",
			backoffs[2], backoffs[1])
	}
}

type connAction int

const (
	fail    connAction = 0
	succeed connAction = 1
)

type mockResetConn struct {
	mu            sync.Mutex
	calls         int
	sequence      []connAction
	notifications chan *Notification
	backoffs      []time.Duration
	lastCallTime  time.Time
}

func (c *mockResetConn) Exec(ctx context.Context, sql string, args ...any) error {
	return nil
}

func (c *mockResetConn) WaitForNotification(ctx context.Context) (*Notification, error) {
	c.mu.Lock()
	now := time.Now()
	if !c.lastCallTime.IsZero() {
		c.backoffs = append(c.backoffs, now.Sub(c.lastCallTime))
	}
	c.lastCallTime = now

	idx := c.calls
	c.calls++
	c.mu.Unlock()

	if idx < len(c.sequence) && c.sequence[idx] == fail {
		return nil, errors.New("connection lost")
	}

	select {
	case notif := <-c.notifications:
		return notif, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *mockResetConn) getBackoffs() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]time.Duration, len(c.backoffs))
	copy(result, c.backoffs)
	return result
}
