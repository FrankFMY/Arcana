package arcana

import "context"

// ChangeDetector listens for data changes and dispatches them to the engine.
type ChangeDetector interface {
	// Start begins listening for changes. The handler is called for each detected change.
	Start(ctx context.Context, handler func(Change)) error

	// Stop terminates the change detection loop.
	Stop() error
}
