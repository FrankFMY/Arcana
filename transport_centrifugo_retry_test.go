package arcana

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCentrifugoTransportRetryOn5xx(t *testing.T) {
	var attempts int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt64(&attempts, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("unavailable"))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := NewCentrifugoTransport(CentrifugoConfig{
		APIURL:  server.URL,
		APIKey:  "test-key",
		Retries: 3,
	})

	err := transport.SendToWorkspace(context.Background(), "w1", Message{Type: "test"})
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}

	got := atomic.LoadInt64(&attempts)
	if got != 3 {
		t.Fatalf("expected 3 attempts, got %d", got)
	}
}

func TestCentrifugoTransportNoRetryOn4xx(t *testing.T) {
	var attempts int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad request"))
	}))
	defer server.Close()

	transport := NewCentrifugoTransport(CentrifugoConfig{
		APIURL:  server.URL,
		APIKey:  "test-key",
		Retries: 3,
	})

	err := transport.SendToWorkspace(context.Background(), "w1", Message{Type: "test"})
	if err == nil {
		t.Fatal("expected error for 400 response")
	}

	got := atomic.LoadInt64(&attempts)
	if got != 1 {
		t.Fatalf("expected 1 attempt (no retry on 4xx), got %d", got)
	}
}

func TestCentrifugoTransportRetryExhausted(t *testing.T) {
	var attempts int64

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&attempts, 1)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("server error"))
	}))
	defer server.Close()

	transport := NewCentrifugoTransport(CentrifugoConfig{
		APIURL:  server.URL,
		APIKey:  "test-key",
		Retries: 2,
	})

	err := transport.SendToWorkspace(context.Background(), "w1", Message{Type: "test"})
	if err == nil {
		t.Fatal("expected error after exhausted retries")
	}

	got := atomic.LoadInt64(&attempts)
	if got != 2 {
		t.Fatalf("expected 2 attempts, got %d", got)
	}
}
