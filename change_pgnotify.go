package arcana

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// PGNotifyListener listens to PostgreSQL LISTEN/NOTIFY for change detection.
type PGNotifyListener struct {
	channel        string
	conn           PGNotifyConn
	handler        func(Change)
	logger         *slog.Logger
	cancel         context.CancelFunc
	maxRetries     int
	initialBackoff time.Duration
}

// PGNotifyConn abstracts a PostgreSQL connection capable of LISTEN/NOTIFY.
// Compatible with pgx.Conn.
type PGNotifyConn interface {
	Exec(ctx context.Context, sql string, args ...any) error
	WaitForNotification(ctx context.Context) (*Notification, error)
}

// Notification represents a PostgreSQL notification.
type Notification struct {
	Channel string
	Payload string
}

// PGNotifyConfig configures the PGNotifyListener.
type PGNotifyConfig struct {
	Conn       PGNotifyConn
	Channel    string // default: "arcana_changes"
	Logger     *slog.Logger
	MaxRetries int // 0 = unlimited retries
}

// NewPGNotifyListener creates a listener for PostgreSQL notifications.
func NewPGNotifyListener(cfg PGNotifyConfig) *PGNotifyListener {
	channel := cfg.Channel
	if channel == "" {
		channel = "arcana_changes"
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &PGNotifyListener{
		channel:        channel,
		conn:           cfg.Conn,
		logger:         logger,
		maxRetries:     cfg.MaxRetries,
		initialBackoff: 1 * time.Second,
	}
}

// Start begins listening on the PostgreSQL notification channel.
func (l *PGNotifyListener) Start(ctx context.Context, handler func(Change)) error {
	l.handler = handler

	err := l.conn.Exec(ctx, fmt.Sprintf("LISTEN %s", l.channel))
	if err != nil {
		return fmt.Errorf("arcana: LISTEN %s: %w", l.channel, err)
	}

	listenCtx, cancel := context.WithCancel(ctx)
	l.cancel = cancel

	go l.loop(listenCtx)
	return nil
}

// Stop terminates the listener.
func (l *PGNotifyListener) Stop() error {
	if l.cancel != nil {
		l.cancel()
	}
	return nil
}

func (l *PGNotifyListener) loop(ctx context.Context) {
	const maxBackoff = 30 * time.Second

	failures := 0
	backoff := l.initialBackoff

	for {
		notification, err := l.conn.WaitForNotification(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			failures++

			if l.maxRetries > 0 && failures > l.maxRetries {
				l.logger.Error("pg_notify max retries exceeded, stopping listener",
					"failures", failures, "max_retries", l.maxRetries)
				return
			}

			l.logger.Warn("pg_notify wait error, retrying",
				"error", err, "attempt", failures, "backoff", backoff)

			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}

			backoff = min(backoff*2, maxBackoff)

			_ = l.conn.Exec(ctx, fmt.Sprintf("LISTEN %s", l.channel))
			continue
		}

		failures = 0
		backoff = l.initialBackoff

		change, err := ParsePGNotifyPayload(notification.Payload)
		if err != nil {
			l.logger.Error("pg_notify parse error", "payload", notification.Payload, "error", err)
			continue
		}

		if l.handler != nil {
			l.handler(change)
		}
	}
}

// ParsePGNotifyPayload parses a JSON payload from pg_notify into a Change.
// Expected format: {"table":"users","id":"uuid","op":"UPDATE","columns":["name"]}
func ParsePGNotifyPayload(payload string) (Change, error) {
	var raw struct {
		Table   string   `json:"table"`
		ID      string   `json:"id"`
		Op      string   `json:"op"`
		Columns []string `json:"columns"`
	}

	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return Change{}, fmt.Errorf("arcana: parse pg_notify payload: %w", err)
	}

	if raw.Table == "" {
		return Change{}, fmt.Errorf("arcana: pg_notify payload missing table")
	}

	return Change{
		Table:   raw.Table,
		RowID:   raw.ID,
		Op:      raw.Op,
		Columns: raw.Columns,
	}, nil
}
