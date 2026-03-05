package arcana

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestCentrifugoPublish(t *testing.T) {
	var mu sync.Mutex
	var calls []map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "apikey test-key" {
			t.Error("missing or wrong API key")
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Error("wrong content type")
		}

		body, _ := io.ReadAll(r.Body)
		var call map[string]any
		json.Unmarshal(body, &call)

		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()

		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := NewCentrifugoTransport(CentrifugoConfig{
		APIURL: server.URL,
		APIKey: "test-key",
	})

	ctx := context.Background()

	// Test SendToWorkspace
	err := transport.SendToWorkspace(ctx, "w1", Message{
		Type: "table_diff",
		Data: map[string]any{"table": "users"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Test SendToSeance
	err = transport.SendToSeance(ctx, "s1", Message{
		Type: "view_diff",
		Data: map[string]any{"view": "user_list"},
	})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()

	if len(calls) != 2 {
		t.Fatalf("expected 2 api calls, got %d", len(calls))
	}

	// Verify first call is publish to workspace channel
	if calls[0]["method"] != "publish" {
		t.Fatalf("expected publish, got %v", calls[0]["method"])
	}
	params0 := calls[0]["params"].(map[string]any)
	if params0["channel"] != "workspace:w1" {
		t.Fatalf("expected workspace:w1, got %v", params0["channel"])
	}

	// Verify second call is publish to seance channel
	params1 := calls[1]["params"].(map[string]any)
	if params1["channel"] != "views:s1" {
		t.Fatalf("expected views:s1, got %v", params1["channel"])
	}
}

func TestCentrifugoDisconnect(t *testing.T) {
	var receivedMethod string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var call map[string]any
		json.Unmarshal(body, &call)
		receivedMethod = call["method"].(string)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := NewCentrifugoTransport(CentrifugoConfig{
		APIURL: server.URL,
		APIKey: "test-key",
	})

	err := transport.DisconnectSeance(context.Background(), "s1")
	if err != nil {
		t.Fatal(err)
	}

	if receivedMethod != "disconnect" {
		t.Fatalf("expected disconnect, got %s", receivedMethod)
	}
}

func TestCentrifugoErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer server.Close()

	transport := NewCentrifugoTransport(CentrifugoConfig{
		APIURL: server.URL,
		APIKey: "test-key",
	})

	err := transport.SendToWorkspace(context.Background(), "w1", Message{Type: "test"})
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}
