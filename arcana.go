package arcana

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Engine is the main entry point for the Arcana reactive sync library.
type Engine struct {
	config      Config
	registry    *Registry
	store       *DataStore
	manager     *Manager
	invalidator *Invalidator
	notifier    *ExplicitNotifier
	detector    ChangeDetector
	logger      *slog.Logger

	mu      sync.Mutex
	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

// New creates a new Arcana engine with the given configuration.
func New(config Config) *Engine {
	config.withDefaults()
	return &Engine{
		config:   config,
		registry: NewRegistry(),
		store:    NewDataStore(),
		logger:   slog.Default(),
	}
}

// Register adds graph definitions to the engine.
// Must be called before Start.
func (e *Engine) Register(defs ...GraphDef) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.started {
		return ErrAlreadyStarted
	}
	return e.registry.Register(defs...)
}

// Start initializes all internal components and begins processing.
func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.started {
		return ErrAlreadyStarted
	}

	engineCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel

	// Create manager
	e.manager = NewManager(e.registry, e.store, e.config.Transport, e.config.Pool, &e.config, e.logger)

	// Create invalidator
	e.invalidator = NewInvalidator(e.registry, e.store, e.config.Transport, e.manager, e.config.Pool, e.logger)

	// Setup change detection
	e.notifier = NewExplicitNotifier(4096)

	if e.config.ChangeDetector != nil {
		e.detector = e.config.ChangeDetector
	} else {
		e.detector = e.notifier
	}

	err := e.detector.Start(engineCtx, func(change Change) {
		e.invalidator.Invalidate(engineCtx, change)
	})
	if err != nil {
		cancel()
		return err
	}

	// If using external detector, also start notifier for explicit notify
	if e.config.ChangeDetector != nil {
		err = e.notifier.Start(engineCtx, func(change Change) {
			e.invalidator.Invalidate(engineCtx, change)
		})
		if err != nil {
			cancel()
			return err
		}
	}

	// Start GC goroutine
	e.wg.Add(1)
	go e.gcLoop(engineCtx)

	e.started = true
	return nil
}

// Stop gracefully shuts down the engine.
func (e *Engine) Stop() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if !e.started {
		return ErrNotStarted
	}

	// Stop change detectors before cancelling context
	if e.detector != nil {
		e.detector.Stop()
	}
	if e.config.ChangeDetector != nil && e.notifier != nil {
		e.notifier.Stop()
	}

	e.cancel()
	e.wg.Wait()
	e.started = false
	return nil
}

// Notify sends a data change event to the engine for processing.
func (e *Engine) Notify(ctx context.Context, change Change) {
	if e.notifier != nil {
		e.notifier.Send(change)
	}
}

// NotifyTable is a convenience method for notifying about a table change.
func (e *Engine) NotifyTable(ctx context.Context, table, rowID string, columns []string) {
	e.Notify(ctx, Change{
		Table:   table,
		RowID:   rowID,
		Columns: columns,
	})
}

// Handler returns an http.Handler for the Arcana endpoints.
// Mount this on your router: mux.Mount("/arcana", engine.Handler())
func (e *Engine) Handler() http.Handler {
	return newHandler(e.manager, e.registry, e.config.AuthFunc)
}

// Registry returns the engine's graph registry (for codegen and inspection).
func (e *Engine) Registry() *Registry {
	return e.registry
}

// EngineStats holds runtime statistics about the engine.
type EngineStats struct {
	Running             bool `json:"running"`
	RegisteredGraphs    int  `json:"registered_graphs"`
	ActiveSubscriptions int  `json:"active_subscriptions"`
	SeancesWithSubs     int  `json:"seances_with_subs"`
	DataStoreRows       int  `json:"data_store_rows"`
}

// Stats returns current engine statistics for monitoring/admin endpoints.
func (e *Engine) Stats() EngineStats {
	e.mu.Lock()
	running := e.started
	e.mu.Unlock()

	stats := EngineStats{
		Running:          running,
		RegisteredGraphs: e.registry.GraphCount(),
	}

	if e.manager != nil {
		subCount, seanceCount := e.manager.Stats()
		stats.ActiveSubscriptions = subCount
		stats.SeancesWithSubs = seanceCount
	}

	if e.store != nil {
		stats.DataStoreRows = e.store.RowCount()
	}

	return stats
}

func (e *Engine) gcLoop(ctx context.Context) {
	defer e.wg.Done()

	ticker := time.NewTicker(e.config.GCInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			e.store.GCAll()
		case <-ctx.Done():
			return
		}
	}
}
