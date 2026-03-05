package arcana

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCentrifugoTransportSendBatch(t *testing.T) {
	var capturedBody string
	var capturedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		capturedBody = string(body)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := NewCentrifugoTransport(CentrifugoConfig{
		APIURL:  server.URL,
		APIKey:  "test-key",
		Retries: 1,
	})

	err := transport.SendBatch(context.Background(), []BatchMessage{
		{Channel: "workspace:w1", Msg: Message{Type: "table_diff", Data: map[string]any{"table": "users"}}},
		{Channel: "views:s1", Msg: Message{Type: "view_diff", Data: map[string]any{"view": "list"}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if capturedPath != "/api/batch" {
		t.Fatalf("expected /api/batch, got %s", capturedPath)
	}

	lines := strings.Split(capturedBody, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %q", len(lines), capturedBody)
	}

	for i, line := range lines {
		var cmd map[string]any
		if err := json.Unmarshal([]byte(line), &cmd); err != nil {
			t.Fatalf("line %d is not valid JSON: %v", i, err)
		}
		if cmd["method"] != "publish" {
			t.Fatalf("line %d: expected method publish, got %v", i, cmd["method"])
		}
		params, ok := cmd["params"].(map[string]any)
		if !ok {
			t.Fatalf("line %d: params is not an object", i)
		}
		if _, ok := params["channel"]; !ok {
			t.Fatalf("line %d: missing channel", i)
		}
		if _, ok := params["data"]; !ok {
			t.Fatalf("line %d: missing data", i)
		}
	}

	var cmd0 map[string]any
	json.Unmarshal([]byte(lines[0]), &cmd0)
	params0 := cmd0["params"].(map[string]any)
	if params0["channel"] != "workspace:w1" {
		t.Fatalf("expected workspace:w1, got %v", params0["channel"])
	}

	var cmd1 map[string]any
	json.Unmarshal([]byte(lines[1]), &cmd1)
	params1 := cmd1["params"].(map[string]any)
	if params1["channel"] != "views:s1" {
		t.Fatalf("expected views:s1, got %v", params1["channel"])
	}
}

func TestCentrifugoTransportSendBatchEmpty(t *testing.T) {
	transport := NewCentrifugoTransport(CentrifugoConfig{
		APIURL:  "http://localhost:9999",
		APIKey:  "test-key",
		Retries: 1,
	})

	err := transport.SendBatch(context.Background(), nil)
	if err != nil {
		t.Fatalf("expected nil for empty batch, got %v", err)
	}

	err = transport.SendBatch(context.Background(), []BatchMessage{})
	if err != nil {
		t.Fatalf("expected nil for empty slice, got %v", err)
	}
}

func TestCentrifugoTransportSendBatchSingle(t *testing.T) {
	var capturedPath string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := NewCentrifugoTransport(CentrifugoConfig{
		APIURL:  server.URL,
		APIKey:  "test-key",
		Retries: 1,
	})

	err := transport.SendBatch(context.Background(), []BatchMessage{
		{Channel: "workspace:w1", Msg: Message{Type: "test"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if capturedPath != "/api" {
		t.Fatalf("single message should use regular publish (/api), got %s", capturedPath)
	}
}
