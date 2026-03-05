package arcana

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"crypto/rand"
	"encoding/hex"
)

// Manager orchestrates subscription lifecycle and connects
// Registry, DataStore, Invalidator, and Transport.
type Manager struct {
	mu        sync.RWMutex
	registry  *Registry
	store     *DataStore
	transport Transport
	pool      Querier
	config    *Config
	logger    *slog.Logger

	// Subscriptions indexed multiple ways
	subs       map[string]*Subscription          // subID → subscription
	bySeance   map[string]map[string]*Subscription // seanceID → subID → subscription
	byGraphKey map[string]map[string]*Subscription // graphKey → subID → subscription
	byHash     map[string]map[string]*Subscription // paramsHash → subID → subscription
}

// NewManager creates a Manager.
func NewManager(registry *Registry, store *DataStore, transport Transport, pool Querier, config *Config, logger *slog.Logger) *Manager {
	if logger == nil {
		logger = slog.Default()
	}
	return &Manager{
		registry:   registry,
		store:      store,
		transport:  transport,
		pool:       pool,
		config:     config,
		logger:     logger,
		subs:       make(map[string]*Subscription),
		bySeance:   make(map[string]map[string]*Subscription),
		byGraphKey: make(map[string]map[string]*Subscription),
		byHash:     make(map[string]map[string]*Subscription),
	}
}

// SubscribeRequest contains the parameters for a Subscribe call.
type SubscribeRequest struct {
	GraphKey    string
	Params      map[string]any
	SeanceID    string
	UserID      string
	WorkspaceID string
}

// SubscribeResponse contains the initial data returned to the client.
type SubscribeResponse struct {
	ParamsHash string                             `json:"params_hash"`
	Version    int64                              `json:"version"`
	Refs       []Ref                              `json:"refs"`
	Tables     map[string]map[string]map[string]any `json:"tables"`
}

// Subscribe creates a new subscription, executes the factory, and returns initial data.
func (m *Manager) Subscribe(ctx context.Context, req SubscribeRequest) (*SubscribeResponse, error) {
	def, ok := m.registry.Get(req.GraphKey)
	if !ok {
		return nil, fmt.Errorf("%w: graph %q", ErrNotFound, req.GraphKey)
	}

	// Validate params
	resolved, err := ValidateParams(def.Params, req.Params)
	if err != nil {
		return nil, err
	}

	// Check subscription limit
	m.mu.RLock()
	seanceSubs := m.bySeance[req.SeanceID]
	seanceCount := len(seanceSubs)
	m.mu.RUnlock()

	if seanceCount >= m.config.MaxSubscriptionsPerSeance {
		return nil, ErrTooManySubscriptions
	}

	paramsHash := ComputeParamsHash(req.GraphKey, resolved)

	// Check if already subscribed with same params
	m.mu.RLock()
	if hashSubs, ok := m.byHash[paramsHash]; ok {
		for _, existing := range hashSubs {
			if existing.SeanceID == req.SeanceID {
				m.mu.RUnlock()
				return nil, fmt.Errorf("%w: already subscribed to %q with same params", ErrInvalidParams, req.GraphKey)
			}
		}
	}
	m.mu.RUnlock()

	// Execute factory
	factoryCtx := WithIdentity(ctx, &Identity{
		SeanceID:    req.SeanceID,
		UserID:      req.UserID,
		WorkspaceID: req.WorkspaceID,
	})

	if def.Factory == nil {
		return nil, fmt.Errorf("%w: graph %q has no factory", ErrNotFound, req.GraphKey)
	}

	result, err := def.Factory(factoryCtx, m.pool, NewParams(resolved))
	if err != nil {
		return nil, err
	}

	refs := result.Refs()
	tables := result.Tables()

	// Store rows in DataStore and manage RefCount
	for table, rows := range tables {
		for rowID, fields := range rows {
			m.store.Upsert(req.WorkspaceID, table, rowID, fields)
			m.store.IncrRef(req.WorkspaceID, table, rowID)
		}
	}

	// Also increment refs for nested refs
	m.incrNestedRefs(req.WorkspaceID, refs)

	// Create subscription
	subID := generateID()
	sub := &Subscription{
		ID:          subID,
		SeanceID:    req.SeanceID,
		WorkspaceID: req.WorkspaceID,
		UserID:      req.UserID,
		GraphKey:    req.GraphKey,
		Params:      resolved,
		ParamsHash:  paramsHash,
		LastRefs:    refs,
		Version:     1,
		CreatedAt:   time.Now(),
	}

	m.mu.Lock()
	m.subs[subID] = sub

	if m.bySeance[req.SeanceID] == nil {
		m.bySeance[req.SeanceID] = make(map[string]*Subscription)
	}
	m.bySeance[req.SeanceID][subID] = sub

	if m.byGraphKey[req.GraphKey] == nil {
		m.byGraphKey[req.GraphKey] = make(map[string]*Subscription)
	}
	m.byGraphKey[req.GraphKey][subID] = sub

	if m.byHash[paramsHash] == nil {
		m.byHash[paramsHash] = make(map[string]*Subscription)
	}
	m.byHash[paramsHash][subID] = sub
	m.mu.Unlock()

	return &SubscribeResponse{
		ParamsHash: paramsHash,
		Version:    1,
		Refs:       refs,
		Tables:     tables,
	}, nil
}

// Unsubscribe removes a subscription identified by seanceID and paramsHash.
func (m *Manager) Unsubscribe(seanceID, paramsHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	hashSubs, ok := m.byHash[paramsHash]
	if !ok {
		return ErrNotFound
	}

	var target *Subscription
	for _, sub := range hashSubs {
		if sub.SeanceID == seanceID {
			target = sub
			break
		}
	}
	if target == nil {
		return ErrNotFound
	}

	m.removeSubscriptionLocked(target)
	return nil
}

// UnsubscribeAll removes all subscriptions for a seance (on disconnect).
func (m *Manager) UnsubscribeAll(seanceID string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	seanceSubs, ok := m.bySeance[seanceID]
	if !ok {
		return
	}

	for _, sub := range seanceSubs {
		m.removeSubscriptionLocked(sub)
	}
}

// removeSubscriptionLocked removes a subscription from all indices.
// Must be called with m.mu held.
func (m *Manager) removeSubscriptionLocked(sub *Subscription) {
	delete(m.subs, sub.ID)

	if seanceSubs, ok := m.bySeance[sub.SeanceID]; ok {
		delete(seanceSubs, sub.ID)
		if len(seanceSubs) == 0 {
			delete(m.bySeance, sub.SeanceID)
		}
	}

	if graphSubs, ok := m.byGraphKey[sub.GraphKey]; ok {
		delete(graphSubs, sub.ID)
		if len(graphSubs) == 0 {
			delete(m.byGraphKey, sub.GraphKey)
		}
	}

	if hashSubs, ok := m.byHash[sub.ParamsHash]; ok {
		delete(hashSubs, sub.ID)
		if len(hashSubs) == 0 {
			delete(m.byHash, sub.ParamsHash)
		}
	}

	// Decrement RefCount for all referenced rows
	m.decrRefsForSubscription(sub)
}

func (m *Manager) decrRefsForSubscription(sub *Subscription) {
	seen := make(map[string]struct{})
	for _, ref := range sub.LastRefs {
		m.decrRefRecursive(sub.WorkspaceID, ref, seen)
	}
}

func (m *Manager) decrRefRecursive(wsID string, ref Ref, seen map[string]struct{}) {
	key := ref.Table + ":" + ref.ID
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}

	m.store.DecrRef(wsID, ref.Table, ref.ID)
	for _, nested := range ref.Nested {
		m.decrRefRecursive(wsID, nested, seen)
	}
}

func (m *Manager) incrNestedRefs(wsID string, refs []Ref) {
	seen := make(map[string]struct{})
	for _, ref := range refs {
		m.incrRefRecursive(wsID, ref, seen)
	}
}

func (m *Manager) incrRefRecursive(wsID string, ref Ref, seen map[string]struct{}) {
	key := ref.Table + ":" + ref.ID
	if _, ok := seen[key]; ok {
		return
	}
	seen[key] = struct{}{}

	// Only increment for nested refs — top-level already handled in Subscribe
	for _, nested := range ref.Nested {
		m.store.IncrRef(wsID, nested.Table, nested.ID)
		m.incrRefRecursive(wsID, nested, seen)
	}
}

// GetByGraphKey implements SubscriptionStore for Invalidator.
func (m *Manager) GetByGraphKey(graphKey string) []*Subscription {
	m.mu.RLock()
	defer m.mu.RUnlock()

	graphSubs, ok := m.byGraphKey[graphKey]
	if !ok {
		return nil
	}

	out := make([]*Subscription, 0, len(graphSubs))
	for _, sub := range graphSubs {
		out = append(out, sub)
	}
	return out
}

// UpdateSubscription updates a subscription's LastRefs and Version,
// and records a version history entry for catch-up sync.
func (m *Manager) UpdateSubscription(subID string, refs []Ref, version int64, entry *VersionEntry) {
	m.mu.Lock()
	defer m.mu.Unlock()

	sub, ok := m.subs[subID]
	if !ok {
		return
	}
	sub.LastRefs = refs
	sub.Version = version

	if entry != nil {
		maxHistory := m.config.SnapshotThreshold
		sub.VersionHistory = append(sub.VersionHistory, *entry)
		if len(sub.VersionHistory) > maxHistory {
			sub.VersionHistory = sub.VersionHistory[len(sub.VersionHistory)-maxHistory:]
		}
	}
}

// SyncRequest contains the parameters for a Sync call.
type SyncRequest struct {
	SeanceID    string
	WorkspaceID string
	Views       []SyncViewRequest
}

// SyncViewRequest describes a single view to sync.
type SyncViewRequest struct {
	View       string `json:"view"`
	ParamsHash string `json:"params_hash"`
	Version    int64  `json:"version"`
}

// SyncResponse contains the sync result for the client.
type SyncResponse struct {
	Views []SyncViewResponse `json:"views"`
}

// SyncViewResponse contains the sync result for a single view.
type SyncViewResponse struct {
	View       string    `json:"view"`
	ParamsHash string    `json:"params_hash"`
	Mode       string    `json:"mode"` // "catch_up" or "snapshot"
	Patches    []VersionEntry `json:"patches,omitempty"`
	// Full snapshot fields (used when mode == "snapshot")
	Version int64                              `json:"version,omitempty"`
	Refs    []Ref                              `json:"refs,omitempty"`
	Tables  map[string]map[string]map[string]any `json:"tables,omitempty"`
}

// Sync processes a reconnect request, returning catch-up diffs or full snapshots.
func (m *Manager) Sync(ctx context.Context, req SyncRequest) (*SyncResponse, error) {
	resp := &SyncResponse{
		Views: make([]SyncViewResponse, 0, len(req.Views)),
	}

	m.mu.RLock()
	orig := m.bySeance[req.SeanceID]
	seanceSubs := make(map[string]*Subscription, len(orig))
	for k, v := range orig {
		seanceSubs[k] = v
	}
	m.mu.RUnlock()

	for _, viewReq := range req.Views {
		viewResp, err := m.syncView(ctx, seanceSubs, viewReq, req)
		if err != nil {
			return nil, err
		}
		resp.Views = append(resp.Views, *viewResp)
	}

	return resp, nil
}

func (m *Manager) syncView(ctx context.Context, seanceSubs map[string]*Subscription, viewReq SyncViewRequest, req SyncRequest) (*SyncViewResponse, error) {
	// Find the subscription
	m.mu.RLock()
	var sub *Subscription
	for _, s := range seanceSubs {
		if s.ParamsHash == viewReq.ParamsHash {
			sub = s
			break
		}
	}
	m.mu.RUnlock()

	if sub == nil {
		return nil, fmt.Errorf("%w: no active subscription for params_hash %q", ErrNotFound, viewReq.ParamsHash)
	}

	delta := sub.Version - viewReq.Version

	// If delta is within threshold and we have history, send catch-up patches
	if delta > 0 && delta <= int64(m.config.SnapshotThreshold) {
		m.mu.RLock()
		var patches []VersionEntry
		for _, entry := range sub.VersionHistory {
			if entry.Version > viewReq.Version {
				patches = append(patches, entry)
			}
		}
		m.mu.RUnlock()

		if len(patches) == int(delta) {
			return &SyncViewResponse{
				View:       viewReq.View,
				ParamsHash: viewReq.ParamsHash,
				Mode:       "catch_up",
				Patches:    patches,
			}, nil
		}
		// Fall through to snapshot if history is incomplete
	}

	// Full snapshot: re-run factory
	def, ok := m.registry.Get(sub.GraphKey)
	if !ok {
		return nil, fmt.Errorf("%w: graph %q", ErrNotFound, sub.GraphKey)
	}

	factoryCtx := WithIdentity(ctx, &Identity{
		SeanceID:    sub.SeanceID,
		UserID:      sub.UserID,
		WorkspaceID: sub.WorkspaceID,
	})

	result, err := def.Factory(factoryCtx, m.pool, NewParams(sub.Params))
	if err != nil {
		return nil, err
	}

	return &SyncViewResponse{
		View:       viewReq.View,
		ParamsHash: viewReq.ParamsHash,
		Mode:       "snapshot",
		Version:    sub.Version,
		Refs:       result.Refs(),
		Tables:     result.Tables(),
	}, nil
}

// GetActive returns all active subscriptions for a seance.
func (m *Manager) GetActive(seanceID string) []*Subscription {
	m.mu.RLock()
	defer m.mu.RUnlock()

	seanceSubs, ok := m.bySeance[seanceID]
	if !ok {
		return nil
	}

	out := make([]*Subscription, 0, len(seanceSubs))
	for _, sub := range seanceSubs {
		out = append(out, sub)
	}
	return out
}

// SubscriptionCount returns total active subscriptions.
func (m *Manager) SubscriptionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.subs)
}

// Stats returns subscription count and seance count.
func (m *Manager) Stats() (subs int, seances int) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.subs), len(m.bySeance)
}

func generateID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}
