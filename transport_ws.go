package arcana

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// WSTransportConfig configures the built-in WebSocket transport.
type WSTransportConfig struct {
	// AuthFunc authenticates the HTTP upgrade request (cookies, headers).
	// If nil, the engine's AuthFunc is used automatically.
	AuthFunc AuthFunc

	// TokenAuthFunc authenticates a token sent in the first WS message.
	// Used for SPA/mobile clients where cookies are unavailable.
	// If both AuthFunc and TokenAuthFunc are nil, connections are rejected.
	TokenAuthFunc func(token string) (*Identity, error)

	// WriteBufferSize is the number of messages buffered per connection
	// before drops occur. Default: 256.
	WriteBufferSize int

	// PingInterval is how often to send WebSocket pings. Default: 30s.
	PingInterval time.Duration

	// PingTimeout is how long to wait for a pong before disconnecting. Default: 10s.
	PingTimeout time.Duration

	// AcceptOptions are passed to websocket.Accept for the upgrade.
	AcceptOptions *websocket.AcceptOptions
}

func (c *WSTransportConfig) withDefaults() {
	if c.WriteBufferSize == 0 {
		c.WriteBufferSize = 256
	}
	if c.PingInterval == 0 {
		c.PingInterval = 30 * time.Second
	}
	if c.PingTimeout == 0 {
		c.PingTimeout = 10 * time.Second
	}
}

type ctxHolder struct {
	ctx context.Context
}

// WSTransport is a built-in WebSocket transport implementing Transport and http.Handler.
type WSTransport struct {
	config    WSTransportConfig
	manager   atomic.Pointer[Manager]
	engineCtx atomic.Pointer[ctxHolder]
	registry  *Registry
	pool      Querier
	notifyFn  func(Change)
	logger    *slog.Logger

	mu          sync.RWMutex
	bySeance    map[string]*wsConn
	byWorkspace map[string]map[string]*wsConn
}

type wsConn struct {
	seanceID    string
	workspaceID string
	conn        *websocket.Conn
	send        chan []byte
	closed      atomic.Bool
}

// NewWSTransport creates a built-in WebSocket transport.
func NewWSTransport(cfg WSTransportConfig) *WSTransport {
	cfg.withDefaults()
	return &WSTransport{
		config:      cfg,
		logger:      slog.Default(),
		bySeance:    make(map[string]*wsConn),
		byWorkspace: make(map[string]map[string]*wsConn),
	}
}

// wire is called by Engine.Start to inject dependencies.
func (t *WSTransport) wire(ctx context.Context, m *Manager, r *Registry, pool Querier, notifyFn func(Change)) {
	t.manager.Store(m)
	t.engineCtx.Store(&ctxHolder{ctx: ctx})
	t.registry = r
	t.pool = pool
	t.notifyFn = notifyFn
}

// ServeHTTP handles WebSocket upgrade requests.
func (t *WSTransport) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Phase 1: try HTTP-level auth (cookies/headers)
	var identity *Identity
	if t.config.AuthFunc != nil {
		id, err := t.config.AuthFunc(r)
		if err == nil && id != nil {
			identity = id
		}
	}

	opts := t.config.AcceptOptions
	if opts == nil {
		opts = &websocket.AcceptOptions{}
	}

	conn, err := websocket.Accept(w, r, opts)
	if err != nil {
		return
	}

	holder := t.engineCtx.Load()
	if holder == nil {
		conn.Close(websocket.StatusInternalError, "transport not ready")
		return
	}

	connCtx, cancel := context.WithCancel(holder.ctx)
	defer cancel()

	// If no HTTP-level auth, wait for token auth message
	if identity == nil {
		if t.config.TokenAuthFunc == nil {
			conn.Close(websocket.StatusPolicyViolation, "unauthorized")
			return
		}
		id, reqID, err := t.waitForAuth(connCtx, conn)
		if err != nil {
			t.directWrite(conn, connCtx, wsOutMsg{
				ReplyTo: reqID, Type: "error",
				Code: "unauthorized", Message: err.Error(),
			})
			conn.Close(websocket.StatusPolicyViolation, "unauthorized")
			return
		}
		identity = id
		t.directWrite(conn, connCtx, wsOutMsg{
			ReplyTo:  reqID,
			Type:     "auth_ok",
			SeanceID: identity.SeanceID,
		})
	}

	c := &wsConn{
		seanceID:    identity.SeanceID,
		workspaceID: identity.WorkspaceID,
		conn:        conn,
		send:        make(chan []byte, t.config.WriteBufferSize),
	}

	t.register(c)

	go t.writeLoop(connCtx, c)
	go t.pingLoop(connCtx, c)

	t.readLoop(connCtx, c, identity)

	// Connection closed — cleanup
	m := t.manager.Load()
	if m != nil {
		m.UnsubscribeAll(c.seanceID)
	}
	t.unregister(c)
}

func (t *WSTransport) waitForAuth(ctx context.Context, conn *websocket.Conn) (*Identity, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	_, data, err := conn.Read(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("read auth message: %w", err)
	}

	var msg wsInMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, "", fmt.Errorf("invalid auth message: %w", err)
	}

	if msg.Type != "auth" {
		return nil, msg.ID, fmt.Errorf("expected auth message, got %q", msg.Type)
	}

	identity, err := t.config.TokenAuthFunc(msg.Token)
	if err != nil {
		return nil, msg.ID, err
	}
	if identity == nil {
		return nil, msg.ID, fmt.Errorf("authentication failed")
	}

	return identity, msg.ID, nil
}

// SendToSeance delivers a message to a specific client session.
func (t *WSTransport) SendToSeance(ctx context.Context, seanceID string, msg Message) error {
	t.mu.RLock()
	c, ok := t.bySeance[seanceID]
	t.mu.RUnlock()

	if !ok {
		return nil
	}

	t.sendJSON(c, msg)
	return nil
}

// SendToWorkspace delivers a message to all sessions in a workspace.
func (t *WSTransport) SendToWorkspace(ctx context.Context, workspaceID string, msg Message) error {
	t.mu.RLock()
	conns := t.byWorkspace[workspaceID]
	if len(conns) == 0 {
		t.mu.RUnlock()
		return nil
	}
	// Copy slice under read lock to avoid holding it during send
	targets := make([]*wsConn, 0, len(conns))
	for _, c := range conns {
		targets = append(targets, c)
	}
	t.mu.RUnlock()

	for _, c := range targets {
		t.sendJSON(c, msg)
	}
	return nil
}

// DisconnectSeance forcibly disconnects a client session.
func (t *WSTransport) DisconnectSeance(ctx context.Context, seanceID string) error {
	t.mu.RLock()
	c, ok := t.bySeance[seanceID]
	t.mu.RUnlock()

	if !ok {
		return nil
	}

	t.sendJSON(c, wsOutMsg{Type: "disconnect", Reason: "session_expired"})
	c.conn.Close(websocket.StatusNormalClosure, "session_expired")
	return nil
}

func (t *WSTransport) register(c *wsConn) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.bySeance[c.seanceID] = c

	if t.byWorkspace[c.workspaceID] == nil {
		t.byWorkspace[c.workspaceID] = make(map[string]*wsConn)
	}
	t.byWorkspace[c.workspaceID][c.seanceID] = c
}

func (t *WSTransport) unregister(c *wsConn) {
	c.closed.Store(true)

	t.mu.Lock()
	defer t.mu.Unlock()

	delete(t.bySeance, c.seanceID)

	if ws, ok := t.byWorkspace[c.workspaceID]; ok {
		delete(ws, c.seanceID)
		if len(ws) == 0 {
			delete(t.byWorkspace, c.workspaceID)
		}
	}
}

func (t *WSTransport) readLoop(ctx context.Context, c *wsConn, identity *Identity) {
	for {
		_, data, err := c.conn.Read(ctx)
		if err != nil {
			return
		}

		if err := t.handleClientMessage(ctx, c, identity, data); err != nil {
			t.logger.Warn("ws message handling failed",
				"seance", c.seanceID,
				"error", err,
			)
		}
	}
}

func (t *WSTransport) writeLoop(ctx context.Context, c *wsConn) {
	for {
		select {
		case data := <-c.send:
			if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
				return
			}
		case <-ctx.Done():
			c.conn.Close(websocket.StatusGoingAway, "server shutdown")
			return
		}
	}
}

func (t *WSTransport) pingLoop(ctx context.Context, c *wsConn) {
	ticker := time.NewTicker(t.config.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			pingCtx, cancel := context.WithTimeout(ctx, t.config.PingTimeout)
			err := c.conn.Ping(pingCtx)
			cancel()
			if err != nil {
				c.conn.Close(websocket.StatusGoingAway, "ping timeout")
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// WS protocol messages

type wsInMsg struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"`
	Token      string            `json:"token,omitempty"`
	View       string            `json:"view,omitempty"`
	Action     string            `json:"action,omitempty"`
	Params     map[string]any    `json:"params,omitempty"`
	ParamsHash string            `json:"params_hash,omitempty"`
	Views      []SyncViewRequest `json:"views,omitempty"`
}

type wsOutMsg struct {
	ReplyTo    string                               `json:"reply_to,omitempty"`
	Type       string                               `json:"type"`
	SeanceID   string                               `json:"seance_id,omitempty"`
	ParamsHash string                               `json:"params_hash,omitempty"`
	Version    int64                                `json:"version,omitempty"`
	Refs       []Ref                                `json:"refs,omitempty"`
	Tables     map[string]map[string]map[string]any `json:"tables,omitempty"`
	Total      int                                  `json:"total,omitempty"`
	Code       string                               `json:"code,omitempty"`
	Message    string                               `json:"message,omitempty"`
	Reason     string                               `json:"reason,omitempty"`
	Data       any                                  `json:"data,omitempty"`
	Views      []SyncViewResponse                   `json:"views,omitempty"`
}

func (t *WSTransport) handleClientMessage(ctx context.Context, c *wsConn, identity *Identity, raw []byte) error {
	var msg wsInMsg
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.sendError(c, "", "invalid_message", "malformed JSON")
		return fmt.Errorf("unmarshal: %w", err)
	}

	switch msg.Type {
	case "subscribe":
		t.handleWSSubscribe(ctx, c, identity, msg)
	case "unsubscribe":
		t.handleWSUnsubscribe(c, msg)
	case "sync":
		t.handleWSSync(ctx, c, identity, msg)
	case "mutate":
		t.handleWSMutate(ctx, c, identity, msg)
	case "ping":
		t.sendReply(c, wsOutMsg{ReplyTo: msg.ID, Type: "pong"})
	default:
		t.sendError(c, msg.ID, "unknown_type", fmt.Sprintf("unknown message type %q", msg.Type))
	}

	return nil
}

func (t *WSTransport) handleWSSubscribe(ctx context.Context, c *wsConn, identity *Identity, msg wsInMsg) {
	m := t.manager.Load()
	if m == nil {
		t.sendError(c, msg.ID, "not_ready", ErrTransportNotReady.Error())
		return
	}

	subCtx := WithIdentity(ctx, identity)
	resp, err := m.Subscribe(subCtx, SubscribeRequest{
		GraphKey:    msg.View,
		Params:      msg.Params,
		SeanceID:    c.seanceID,
		UserID:      identity.UserID,
		WorkspaceID: c.workspaceID,
	})
	if err != nil {
		code := "error"
		switch {
		case isErr(err, ErrNotFound):
			code = "not_found"
		case isErr(err, ErrInvalidParams):
			code = "invalid_params"
		case isErr(err, ErrTooManySubscriptions):
			code = "too_many_subscriptions"
		case isErr(err, ErrForbidden):
			code = "forbidden"
		}
		t.sendError(c, msg.ID, code, err.Error())
		return
	}

	t.sendReply(c, wsOutMsg{
		ReplyTo:    msg.ID,
		Type:       "snapshot",
		SeanceID:   c.seanceID,
		ParamsHash: resp.ParamsHash,
		Version:    resp.Version,
		Refs:       resp.Refs,
		Tables:     resp.Tables,
		Total:      resp.Total,
	})
}

func (t *WSTransport) handleWSUnsubscribe(c *wsConn, msg wsInMsg) {
	m := t.manager.Load()
	if m == nil {
		t.sendError(c, msg.ID, "not_ready", ErrTransportNotReady.Error())
		return
	}

	err := m.Unsubscribe(c.seanceID, msg.ParamsHash)
	if err != nil {
		code := "error"
		if isErr(err, ErrNotFound) {
			code = "not_found"
		}
		t.sendError(c, msg.ID, code, err.Error())
		return
	}

	t.sendReply(c, wsOutMsg{ReplyTo: msg.ID, Type: "ok"})
}

func (t *WSTransport) handleWSSync(ctx context.Context, c *wsConn, identity *Identity, msg wsInMsg) {
	m := t.manager.Load()
	if m == nil {
		t.sendError(c, msg.ID, "not_ready", ErrTransportNotReady.Error())
		return
	}

	subCtx := WithIdentity(ctx, identity)
	resp, err := m.Sync(subCtx, SyncRequest{
		SeanceID:    c.seanceID,
		WorkspaceID: c.workspaceID,
		Views:       msg.Views,
	})
	if err != nil {
		t.sendError(c, msg.ID, "error", err.Error())
		return
	}

	t.sendReply(c, wsOutMsg{
		ReplyTo: msg.ID,
		Type:    "sync_result",
		Views:   resp.Views,
	})
}

func (t *WSTransport) handleWSMutate(ctx context.Context, c *wsConn, identity *Identity, msg wsInMsg) {
	if t.registry == nil || t.pool == nil {
		t.sendError(c, msg.ID, "not_ready", ErrTransportNotReady.Error())
		return
	}

	def, ok := t.registry.GetMutation(msg.Action)
	if !ok {
		t.sendError(c, msg.ID, "not_found", fmt.Sprintf("mutation %q not found", msg.Action))
		return
	}

	mutCtx := WithIdentity(ctx, identity)
	result, err := executeMutation(mutCtx, def, t.pool, msg.Params, t.notifyFn)
	if err != nil {
		code := "error"
		switch {
		case isErr(err, ErrInvalidParams):
			code = "invalid_params"
		case isErr(err, ErrForbidden):
			code = "forbidden"
		}
		t.sendError(c, msg.ID, code, err.Error())
		return
	}

	t.sendReply(c, wsOutMsg{
		ReplyTo: msg.ID,
		Type:    "mutate_ok",
		Data:    result.Data,
	})
}

func (t *WSTransport) sendJSON(c *wsConn, v any) bool {
	if c.closed.Load() {
		return false
	}

	data, err := json.Marshal(v)
	if err != nil {
		t.logger.Error("ws marshal failed", "error", err)
		return false
	}

	select {
	case c.send <- data:
		return true
	default:
		t.logger.Warn("ws write buffer full, dropping message",
			"seance", c.seanceID,
		)
		return false
	}
}

// directWrite writes to the WebSocket conn directly. Only safe before registration
// when no concurrent writers exist (writeLoop not yet started).
func (t *WSTransport) directWrite(conn *websocket.Conn, ctx context.Context, msg wsOutMsg) {
	data, err := json.Marshal(msg)
	if err != nil {
		return
	}
	conn.Write(ctx, websocket.MessageText, data)
}

// sendReply routes a reply through the connection's send channel (thread-safe).
func (t *WSTransport) sendReply(c *wsConn, msg wsOutMsg) {
	t.sendJSON(c, msg)
}

// sendError sends an error reply through the connection's send channel (thread-safe).
func (t *WSTransport) sendError(c *wsConn, replyTo, code, message string) {
	t.sendJSON(c, wsOutMsg{
		ReplyTo: replyTo,
		Type:    "error",
		Code:    code,
		Message: message,
	})
}

func isErr(err, target error) bool {
	return errors.Is(err, target)
}
