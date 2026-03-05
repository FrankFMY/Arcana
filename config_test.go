package arcana

import (
	"testing"
	"time"
)

func TestConfigDefaults(t *testing.T) {
	c := Config{}
	c.withDefaults()

	if c.InvalidationDebounce != 50*time.Millisecond {
		t.Fatalf("expected 50ms, got %v", c.InvalidationDebounce)
	}
	if c.MaxSubscriptionsPerSeance != 100 {
		t.Fatalf("expected 100, got %d", c.MaxSubscriptionsPerSeance)
	}
	if c.SnapshotThreshold != 50 {
		t.Fatalf("expected 50, got %d", c.SnapshotThreshold)
	}
	if c.GCInterval != time.Minute {
		t.Fatalf("expected 1m, got %v", c.GCInterval)
	}
}

func TestConfigCustomValues(t *testing.T) {
	c := Config{
		InvalidationDebounce:      100 * time.Millisecond,
		MaxSubscriptionsPerSeance: 200,
	}
	c.withDefaults()

	if c.InvalidationDebounce != 100*time.Millisecond {
		t.Fatalf("custom debounce should be preserved, got %v", c.InvalidationDebounce)
	}
	if c.MaxSubscriptionsPerSeance != 200 {
		t.Fatalf("custom max subs should be preserved, got %d", c.MaxSubscriptionsPerSeance)
	}
	// Others should get defaults
	if c.GCInterval != time.Minute {
		t.Fatalf("expected default 1m, got %v", c.GCInterval)
	}
}
