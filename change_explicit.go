package arcana

import (
	"context"
	"log/slog"
)

// ExplicitNotifier is the default ChangeDetector that receives
// changes via a channel. Application code calls engine.Notify()
// which sends to this channel.
type ExplicitNotifier struct {
	ch      chan Change
	handler func(Change)
	done    chan struct{}
}

// NewExplicitNotifier creates a notifier with the given buffer size.
func NewExplicitNotifier(bufferSize int) *ExplicitNotifier {
	if bufferSize <= 0 {
		bufferSize = 1024
	}
	return &ExplicitNotifier{
		ch:   make(chan Change, bufferSize),
		done: make(chan struct{}),
	}
}

// Start begins processing changes from the channel.
func (n *ExplicitNotifier) Start(_ context.Context, handler func(Change)) error {
	n.handler = handler
	go n.loop()
	return nil
}

// Stop terminates the processing loop.
func (n *ExplicitNotifier) Stop() error {
	close(n.done)
	return nil
}

// Send enqueues a change for processing.
func (n *ExplicitNotifier) Send(change Change) {
	select {
	case n.ch <- change:
	default:
		slog.Warn("arcana: change dropped (buffer full)", "table", change.Table, "row_id", change.RowID)
	}
}

func (n *ExplicitNotifier) loop() {
	for {
		select {
		case change := <-n.ch:
			if n.handler != nil {
				n.handler(change)
			}
		case <-n.done:
			return
		}
	}
}
