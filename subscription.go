package arcana

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Subscription represents an active client subscription to a graph view.
type Subscription struct {
	ID          string
	SeanceID    string
	WorkspaceID string
	UserID      string
	GraphKey    string
	Params      map[string]any
	ParamsHash  string
	LastRefs    []Ref
	Version     int64
	CreatedAt   time.Time

	// VersionHistory stores recent view_diff messages for catch-up sync.
	// Ring buffer of size SnapshotThreshold.
	VersionHistory []VersionEntry
}

// VersionEntry records a single version's diff for catch-up sync.
type VersionEntry struct {
	Version   int64                                `json:"version"`
	RefsPatch []PatchOp                            `json:"refs_patch"`
	Tables    map[string]map[string]map[string]any `json:"tables,omitempty"`
}

// ComputeParamsHash produces a deterministic hash of graphKey + sorted params.
func ComputeParamsHash(graphKey string, params map[string]any) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	h := sha256.New()
	h.Write([]byte(graphKey))
	h.Write([]byte{0})

	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		v, _ := json.Marshal(params[k])
		h.Write(v)
		h.Write([]byte{0})
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}
