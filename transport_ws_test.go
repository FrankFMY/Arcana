package arcana

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func testIdentity() *Identity {
	return &Identity{
		SeanceID:    "test-seance-1",
		UserID:      "test-user-1",
		WorkspaceID: "test-ws-1",
		Role:        "admin",
	}
}

func setupTestEngine(t *testing.T) (*Engine, *httptest.Server) {
	t.Helper()

	identity := testIdentity()

	engine := New(Config{
		Pool: &mockQuerier{},
		AuthFunc: func(r *http.Request) (*Identity, error) {
			return identity, nil
		},
	})

	engine.Register(GraphDef{
		Key:    "test_view",
		Params: ParamSchema{},
		Deps:   []TableDep{{Table: "users", Columns: []string{"name"}}},
		Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
			r := NewResult()
			r.AddRow("users", "u1", map[string]any{"id": "u1", "name": "Alice"})
			r.AddRef(Ref{Table: "users", ID: "u1", Fields: []string{"id", "name"}})
			return r, nil
		},
	})

	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/ws", engine.WSHandler())
	mux.Handle("/", engine.Handler())

	server := httptest.NewServer(mux)
	t.Cleanup(func() {
		engine.Stop()
		server.Close()
	})

	return engine, server
}

func dialWS(t *testing.T, server *httptest.Server) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("websocket.Dial: %v", err)
	}
	return conn
}

func readMsg(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("conn.Read: %v", err)
	}
	var msg map[string]any
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return msg
}

func writeMsg(t *testing.T, conn *websocket.Conn, msg any) {
	t.Helper()
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		t.Fatalf("conn.Write: %v", err)
	}
}

func drainNonReply(t *testing.T, conn *websocket.Conn) {
	t.Helper()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		_, _, err := conn.Read(ctx)
		cancel()
		if err != nil {
			return // timeout or closed — done draining
		}
	}
}

func TestWSTransportAutoCreation(t *testing.T) {
	engine := New(Config{
		Pool: &mockQuerier{},
	})
	engine.Start(context.Background())
	defer engine.Stop()

	handler := engine.WSHandler()
	if handler == nil {
		t.Fatal("WSHandler() should return non-nil when Transport is nil")
	}
}

func TestWSTransportNilWhenCentrifugo(t *testing.T) {
	engine := New(Config{
		Pool:      &mockQuerier{},
		Transport: NewCentrifugoTransport(CentrifugoConfig{APIURL: "http://localhost:8000"}),
	})
	engine.Start(context.Background())
	defer engine.Stop()

	handler := engine.WSHandler()
	if handler != nil {
		t.Fatal("WSHandler() should return nil when using CentrifugoTransport")
	}
}

func TestWSTransportSubscribeOverWS(t *testing.T) {
	_, server := setupTestEngine(t)
	conn := dialWS(t, server)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	writeMsg(t, conn, map[string]any{
		"id":   "req-1",
		"type": "subscribe",
		"view": "test_view",
	})

	msg := readMsg(t, conn)

	if msg["reply_to"] != "req-1" {
		t.Errorf("expected reply_to=req-1, got %v", msg["reply_to"])
	}
	if msg["type"] != "snapshot" {
		t.Errorf("expected type=snapshot, got %v", msg["type"])
	}
	if msg["params_hash"] == nil || msg["params_hash"] == "" {
		t.Error("expected params_hash in snapshot response")
	}
	if msg["seance_id"] != "test-seance-1" {
		t.Errorf("expected seance_id=test-seance-1, got %v", msg["seance_id"])
	}
	refs, ok := msg["refs"].([]any)
	if !ok || len(refs) == 0 {
		t.Error("expected non-empty refs in snapshot")
	}
}

func TestWSTransportUnsubscribeOverWS(t *testing.T) {
	_, server := setupTestEngine(t)
	conn := dialWS(t, server)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// Subscribe first
	writeMsg(t, conn, map[string]any{
		"id":   "req-1",
		"type": "subscribe",
		"view": "test_view",
	})
	snapshot := readMsg(t, conn)
	paramsHash := snapshot["params_hash"].(string)

	// Unsubscribe
	writeMsg(t, conn, map[string]any{
		"id":          "req-2",
		"type":        "unsubscribe",
		"params_hash": paramsHash,
	})

	msg := readMsg(t, conn)
	if msg["reply_to"] != "req-2" {
		t.Errorf("expected reply_to=req-2, got %v", msg["reply_to"])
	}
	if msg["type"] != "ok" {
		t.Errorf("expected type=ok, got %v", msg["type"])
	}
}

func TestWSTransportSubscribeError(t *testing.T) {
	_, server := setupTestEngine(t)
	conn := dialWS(t, server)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	writeMsg(t, conn, map[string]any{
		"id":   "req-1",
		"type": "subscribe",
		"view": "nonexistent_view",
	})

	msg := readMsg(t, conn)
	if msg["reply_to"] != "req-1" {
		t.Errorf("expected reply_to=req-1, got %v", msg["reply_to"])
	}
	if msg["type"] != "error" {
		t.Errorf("expected type=error, got %v", msg["type"])
	}
	if msg["code"] != "not_found" {
		t.Errorf("expected code=not_found, got %v", msg["code"])
	}
}

func TestWSTransportSendToSeance(t *testing.T) {
	engine, server := setupTestEngine(t)
	conn := dialWS(t, server)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// Subscribe to register the connection
	writeMsg(t, conn, map[string]any{
		"id":   "req-1",
		"type": "subscribe",
		"view": "test_view",
	})
	readMsg(t, conn) // consume snapshot

	// Send message to seance via transport interface
	time.Sleep(50 * time.Millisecond) // let registration complete
	err := engine.wsTransport.SendToSeance(context.Background(), "test-seance-1", Message{
		Type: "view_diff",
		Data: map[string]any{"test": true},
	})
	if err != nil {
		t.Fatalf("SendToSeance: %v", err)
	}

	msg := readMsg(t, conn)
	if msg["type"] != "view_diff" {
		t.Errorf("expected type=view_diff, got %v", msg["type"])
	}
}

func TestWSTransportSendToWorkspace(t *testing.T) {
	engine, server := setupTestEngine(t)
	conn := dialWS(t, server)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// Subscribe to register the connection
	writeMsg(t, conn, map[string]any{
		"id":   "req-1",
		"type": "subscribe",
		"view": "test_view",
	})
	readMsg(t, conn) // consume snapshot

	time.Sleep(50 * time.Millisecond)
	err := engine.wsTransport.SendToWorkspace(context.Background(), "test-ws-1", Message{
		Type: "table_diff",
		Data: map[string]any{"table": "users", "id": "u1"},
	})
	if err != nil {
		t.Fatalf("SendToWorkspace: %v", err)
	}

	msg := readMsg(t, conn)
	if msg["type"] != "table_diff" {
		t.Errorf("expected type=table_diff, got %v", msg["type"])
	}
}

func TestWSTransportMultipleClientsInWorkspace(t *testing.T) {
	identity1 := &Identity{SeanceID: "seance-a", UserID: "user-1", WorkspaceID: "ws-shared"}
	identity2 := &Identity{SeanceID: "seance-b", UserID: "user-2", WorkspaceID: "ws-shared"}

	engine := New(Config{
		Pool: &mockQuerier{},
		AuthFunc: func(r *http.Request) (*Identity, error) {
			if r.Header.Get("X-Seance") == "b" {
				return identity2, nil
			}
			return identity1, nil
		},
	})

	if err := engine.Start(context.Background()); err != nil {
		t.Fatalf("engine.Start: %v", err)
	}
	defer engine.Stop()

	mux := http.NewServeMux()
	mux.Handle("/ws", engine.WSHandler())
	server := httptest.NewServer(mux)
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	ctx := context.Background()
	conn1, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial conn1: %v", err)
	}
	defer conn1.Close(websocket.StatusNormalClosure, "done")

	conn2, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{
		HTTPHeader: http.Header{"X-Seance": {"b"}},
	})
	if err != nil {
		t.Fatalf("dial conn2: %v", err)
	}
	defer conn2.Close(websocket.StatusNormalClosure, "done")

	time.Sleep(100 * time.Millisecond) // registration

	// Send workspace broadcast
	engine.wsTransport.SendToWorkspace(ctx, "ws-shared", Message{
		Type: "table_diff",
		Data: map[string]any{"table": "test"},
	})

	// Both should receive
	msg1 := readMsg(t, conn1)
	msg2 := readMsg(t, conn2)

	if msg1["type"] != "table_diff" {
		t.Errorf("conn1: expected table_diff, got %v", msg1["type"])
	}
	if msg2["type"] != "table_diff" {
		t.Errorf("conn2: expected table_diff, got %v", msg2["type"])
	}
}

func TestWSTransportDisconnectCleansUp(t *testing.T) {
	engine, server := setupTestEngine(t)
	conn := dialWS(t, server)

	// Subscribe
	writeMsg(t, conn, map[string]any{
		"id":   "req-1",
		"type": "subscribe",
		"view": "test_view",
	})
	readMsg(t, conn) // snapshot

	time.Sleep(50 * time.Millisecond)

	// Verify subscription exists
	subs := engine.manager.GetActive("test-seance-1")
	if len(subs) == 0 {
		t.Fatal("expected active subscription before disconnect")
	}

	// Close connection
	conn.Close(websocket.StatusNormalClosure, "bye")
	time.Sleep(200 * time.Millisecond)

	// Verify subscription was cleaned up
	subs = engine.manager.GetActive("test-seance-1")
	if len(subs) != 0 {
		t.Errorf("expected 0 subscriptions after disconnect, got %d", len(subs))
	}
}

func TestWSTransportBufferOverflowDrops(t *testing.T) {
	engine := New(Config{
		Pool: &mockQuerier{},
		AuthFunc: func(r *http.Request) (*Identity, error) {
			return testIdentity(), nil
		},
		WSConfig: &WSTransportConfig{
			WriteBufferSize: 2, // tiny buffer
		},
	})
	engine.Start(context.Background())
	defer engine.Stop()

	mux := http.NewServeMux()
	mux.Handle("/ws", engine.WSHandler())
	server := httptest.NewServer(mux)
	defer server.Close()

	conn := dialWS(t, &httptest.Server{URL: server.URL})
	defer conn.Close(websocket.StatusNormalClosure, "done")

	time.Sleep(50 * time.Millisecond)

	// Send more messages than the buffer can hold
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			engine.wsTransport.SendToSeance(context.Background(), "test-seance-1", Message{
				Type: "table_diff",
				Data: map[string]any{"i": i},
			})
		}(i)
	}
	wg.Wait()

	// Should not panic or block — some messages will be dropped
}

func TestWSTransportTokenAuth(t *testing.T) {
	engine := New(Config{
		Pool: &mockQuerier{},
		WSConfig: &WSTransportConfig{
			TokenAuthFunc: func(token string) (*Identity, error) {
				if token == "valid-token" {
					return testIdentity(), nil
				}
				return nil, ErrForbidden
			},
		},
	})
	engine.Register(GraphDef{
		Key:    "test_view",
		Params: ParamSchema{},
		Deps:   []TableDep{{Table: "users"}},
		Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
			r := NewResult()
			r.AddRow("users", "u1", map[string]any{"id": "u1"})
			r.AddRef(Ref{Table: "users", ID: "u1", Fields: []string{"id"}})
			return r, nil
		},
	})
	engine.Start(context.Background())
	defer engine.Stop()

	mux := http.NewServeMux()
	mux.Handle("/ws", engine.WSHandler())
	server := httptest.NewServer(mux)
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("websocket.Dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// Send valid auth token
	writeMsg(t, conn, map[string]any{
		"id":    "auth-1",
		"type":  "auth",
		"token": "valid-token",
	})

	msg := readMsg(t, conn)
	if msg["type"] != "auth_ok" {
		t.Errorf("expected auth_ok, got %v", msg["type"])
	}
	if msg["seance_id"] != "test-seance-1" {
		t.Errorf("expected seance_id, got %v", msg["seance_id"])
	}

	// Now subscribe should work
	writeMsg(t, conn, map[string]any{
		"id":   "req-1",
		"type": "subscribe",
		"view": "test_view",
	})

	msg = readMsg(t, conn)
	if msg["type"] != "snapshot" {
		t.Errorf("expected snapshot, got %v", msg["type"])
	}
}

func TestWSTransportPingPong(t *testing.T) {
	_, server := setupTestEngine(t)
	conn := dialWS(t, server)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	writeMsg(t, conn, map[string]any{
		"id":   "p1",
		"type": "ping",
	})

	msg := readMsg(t, conn)
	if msg["type"] != "pong" {
		t.Errorf("expected pong, got %v", msg["type"])
	}
	if msg["reply_to"] != "p1" {
		t.Errorf("expected reply_to=p1, got %v", msg["reply_to"])
	}
}

func TestWSTransport5ClientsBroadcastAndDisconnect(t *testing.T) {
	const numClients = 5

	var mu sync.Mutex
	seanceIdx := 0
	engine := New(Config{
		Pool: &mockQuerier{},
		AuthFunc: func(r *http.Request) (*Identity, error) {
			mu.Lock()
			seanceIdx++
			id := seanceIdx
			mu.Unlock()
			return &Identity{
				SeanceID:    fmt.Sprintf("seance-%d", id),
				UserID:      "user-1",
				WorkspaceID: "ws-shared",
			}, nil
		},
	})
	engine.Register(GraphDef{
		Key:    "test_view",
		Params: ParamSchema{},
		Deps:   []TableDep{{Table: "users", Columns: []string{"name"}}},
		Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
			r := NewResult()
			r.AddRow("users", "u1", map[string]any{"id": "u1", "name": "Alice"})
			r.AddRef(Ref{Table: "users", ID: "u1", Fields: []string{"id", "name"}})
			return r, nil
		},
	})
	engine.Start(context.Background())
	defer engine.Stop()

	mux := http.NewServeMux()
	mux.Handle("/ws", engine.WSHandler())
	server := httptest.NewServer(mux)
	defer server.Close()

	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	// a) 5 clients connect simultaneously
	conns := make([]*websocket.Conn, numClients)
	for i := 0; i < numClients; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c, _, err := websocket.Dial(ctx, url, nil)
		cancel()
		if err != nil {
			t.Fatalf("dial client %d: %v", i, err)
		}
		conns[i] = c
	}
	defer func() {
		for _, c := range conns {
			if c != nil {
				c.Close(websocket.StatusNormalClosure, "done")
			}
		}
	}()

	// b) All subscribe to one view
	for i, c := range conns {
		writeMsg(t, c, map[string]any{
			"id":   fmt.Sprintf("sub-%d", i),
			"type": "subscribe",
			"view": "test_view",
		})
		msg := readMsg(t, c) // consume snapshot
		if msg["type"] != "snapshot" {
			t.Fatalf("client %d: expected snapshot, got %v", i, msg["type"])
		}
	}

	time.Sleep(100 * time.Millisecond) // registration

	// c) Server sends table_diff
	engine.wsTransport.SendToWorkspace(context.Background(), "ws-shared", Message{
		Type: "table_diff",
		Data: map[string]any{"table": "users", "id": "u1"},
	})

	// d) All 5 receive the diff
	for i, c := range conns {
		msg := readMsg(t, c)
		if msg["type"] != "table_diff" {
			t.Errorf("client %d: expected table_diff, got %v", i, msg["type"])
		}
	}

	// e) Client 4 disconnects
	conns[4].Close(websocket.StatusNormalClosure, "bye")
	conns[4] = nil
	time.Sleep(200 * time.Millisecond)

	// f) Server sends another diff
	engine.wsTransport.SendToWorkspace(context.Background(), "ws-shared", Message{
		Type: "table_diff",
		Data: map[string]any{"table": "users", "id": "u1", "round": 2},
	})

	// g) 4 receive, 5th doesn't, its goroutine completed
	for i := 0; i < 4; i++ {
		msg := readMsg(t, conns[i])
		if msg["type"] != "table_diff" {
			t.Errorf("client %d: expected table_diff round 2, got %v", i, msg["type"])
		}
	}

	// Verify 5th client's subscription was cleaned
	subs := engine.manager.GetActive("seance-5")
	if len(subs) != 0 {
		t.Errorf("expected 0 subs for disconnected seance-5, got %d", len(subs))
	}
}

func TestWSTransportMutationEndToEnd(t *testing.T) {
	var counter int64

	engine := New(Config{
		Pool: &mockQuerier{},
		AuthFunc: func(r *http.Request) (*Identity, error) {
			return testIdentity(), nil
		},
	})
	engine.Register(GraphDef{
		Key:    "counter",
		Params: ParamSchema{},
		Deps:   []TableDep{{Table: "counters", Columns: []string{"value"}}},
		Factory: func(ctx context.Context, q Querier, p Params) (*Result, error) {
			r := NewResult()
			r.AddRow("counters", "c1", map[string]any{"id": "c1", "value": atomic.LoadInt64(&counter)})
			r.AddRef(Ref{Table: "counters", ID: "c1", Fields: []string{"id", "value"}})
			return r, nil
		},
	})
	engine.RegisterMutation(MutationDef{
		Key: "increment",
		Handler: func(ctx context.Context, q Querier, p Params) (*MutationResult, error) {
			newVal := atomic.AddInt64(&counter, 1)
			return &MutationResult{
				Data: map[string]any{"value": newVal},
				Changes: []Change{
					{Table: "counters", RowID: "c1", Columns: []string{"value"}},
				},
			}, nil
		},
	})
	engine.RegisterMutation(MutationDef{
		Key: "fail_mutation",
		Handler: func(ctx context.Context, q Querier, p Params) (*MutationResult, error) {
			return nil, fmt.Errorf("%w: mutation intentionally failed", ErrInvalidParams)
		},
	})
	engine.Start(context.Background())
	defer engine.Stop()

	mux := http.NewServeMux()
	mux.Handle("/ws", engine.WSHandler())
	server := httptest.NewServer(mux)
	defer server.Close()

	conn := dialWS(t, server)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// b) Subscribe to counter view
	writeMsg(t, conn, map[string]any{
		"id":   "sub-1",
		"type": "subscribe",
		"view": "counter",
	})
	snapshot := readMsg(t, conn)
	if snapshot["type"] != "snapshot" {
		t.Fatalf("expected snapshot, got %v", snapshot["type"])
	}

	// c) Execute mutation
	writeMsg(t, conn, map[string]any{
		"id":     "mut-1",
		"type":   "mutate",
		"action": "increment",
	})
	mutReply := readMsg(t, conn)
	if mutReply["type"] != "mutate_ok" {
		t.Fatalf("expected mutate_ok, got %v: %v", mutReply["type"], mutReply["message"])
	}

	// d) Verify mutation returned new value
	data, ok := mutReply["data"].(map[string]any)
	if !ok {
		t.Fatalf("expected data map, got %T", mutReply["data"])
	}
	if data["value"] != float64(1) {
		t.Fatalf("expected value=1, got %v", data["value"])
	}

	// e) Execute mutation that errors
	time.Sleep(200 * time.Millisecond) // let invalidation cycle complete
	writeMsg(t, conn, map[string]any{
		"id":     "mut-2",
		"type":   "mutate",
		"action": "fail_mutation",
	})
	// Read messages until we get the reply to mut-2
	for {
		errReply := readMsg(t, conn)
		replyTo, _ := errReply["reply_to"].(string)
		if replyTo == "mut-2" {
			if errReply["type"] != "error" {
				t.Fatalf("expected error for failed mutation, got %v", errReply["type"])
			}
			if errReply["code"] != "invalid_params" {
				t.Fatalf("expected code=invalid_params, got %v", errReply["code"])
			}
			break
		}
		// Skip invalidation diffs
	}
	// Counter should still be 1 (failed mutation didn't increment)
	if atomic.LoadInt64(&counter) != 1 {
		t.Fatalf("expected counter=1 after failed mutation, got %d", counter)
	}
}
