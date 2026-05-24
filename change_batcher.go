package arcana

import (
	"context"
	"sync"
	"time"
)

type changeBatcher struct {
	ctx     context.Context
	delay   time.Duration
	handler func(Change)

	mu      sync.Mutex
	runMu   sync.Mutex
	timer   *time.Timer
	pending map[string]Change
}

func newChangeBatcher(ctx context.Context, delay time.Duration, handler func(Change)) *changeBatcher {
	return &changeBatcher{
		ctx:     ctx,
		delay:   delay,
		handler: handler,
		pending: make(map[string]Change),
	}
}

func (b *changeBatcher) Handle(change Change) {
	if b.delay <= 0 {
		b.runMu.Lock()
		defer b.runMu.Unlock()
		b.handler(change)
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	key := change.Table + "\x00" + change.RowID
	if existing, ok := b.pending[key]; ok {
		b.pending[key] = mergeChanges(existing, change)
	} else {
		b.pending[key] = change
	}

	if b.timer == nil {
		b.timer = time.AfterFunc(b.delay, b.Flush)
	}
}

func (b *changeBatcher) Flush() {
	b.mu.Lock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	batch := make([]Change, 0, len(b.pending))
	for _, change := range b.pending {
		batch = append(batch, change)
	}
	b.pending = make(map[string]Change)
	b.mu.Unlock()

	b.runMu.Lock()
	defer b.runMu.Unlock()
	for _, change := range batch {
		select {
		case <-b.ctx.Done():
			return
		default:
			b.handler(change)
		}
	}
}

func mergeChanges(a, b Change) Change {
	merged := a
	merged.Op = mergeOp(a.Op, b.Op)

	if len(a.Columns) == 0 || len(b.Columns) == 0 {
		merged.Columns = nil
		return merged
	}

	seen := make(map[string]bool, len(a.Columns)+len(b.Columns))
	cols := make([]string, 0, len(a.Columns)+len(b.Columns))
	for _, col := range a.Columns {
		if !seen[col] {
			seen[col] = true
			cols = append(cols, col)
		}
	}
	for _, col := range b.Columns {
		if !seen[col] {
			seen[col] = true
			cols = append(cols, col)
		}
	}
	merged.Columns = cols
	return merged
}

func mergeOp(a, b string) string {
	if a == "DELETE" || b == "DELETE" {
		return "DELETE"
	}
	if a == "INSERT" || b == "INSERT" {
		return "INSERT"
	}
	if b != "" {
		return b
	}
	return a
}
