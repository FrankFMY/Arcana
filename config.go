package arcana

import "time"

// Config holds the configuration for an Arcana engine instance.
type Config struct {
	// Pool is the PostgreSQL connection pool (required).
	Pool Querier

	// Transport delivers messages to clients (required).
	Transport Transport

	// AuthFunc extracts identity from an incoming HTTP request.
	AuthFunc AuthFunc

	// ChangeDetector determines how data changes are detected.
	// Defaults to an internal explicit notifier if nil.
	ChangeDetector ChangeDetector

	// InvalidationDebounce batches changes within this window. Default: 50ms.
	InvalidationDebounce time.Duration

	// MaxSubscriptionsPerSeance limits subscriptions per client session. Default: 100.
	MaxSubscriptionsPerSeance int

	// SnapshotThreshold sends a full snapshot instead of diffs when version
	// difference exceeds this value. Default: 50.
	SnapshotThreshold int

	// GCInterval is how often the store garbage-collects unreferenced rows. Default: 1min.
	GCInterval time.Duration
}

func (c *Config) withDefaults() {
	if c.InvalidationDebounce == 0 {
		c.InvalidationDebounce = 50 * time.Millisecond
	}
	if c.MaxSubscriptionsPerSeance == 0 {
		c.MaxSubscriptionsPerSeance = 100
	}
	if c.SnapshotThreshold == 0 {
		c.SnapshotThreshold = 50
	}
	if c.GCInterval == 0 {
		c.GCInterval = time.Minute
	}
}
