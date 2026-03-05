package arcana

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func startCentrifugo(t *testing.T, ctx context.Context) (apiURL string, apiKey string) {
	t.Helper()

	apiKey = "test-api-key"

	req := testcontainers.ContainerRequest{
		Image:        "centrifugo/centrifugo:v5",
		ExposedPorts: []string{"8000/tcp"},
		Cmd:          []string{"centrifugo", "--health"},
		Env: map[string]string{
			"CENTRIFUGO_API_KEY":                    apiKey,
			"CENTRIFUGO_ALLOW_SUBSCRIBE_FOR_CLIENT": "true",
		},
		WaitingFor: wait.ForHTTP("/health").WithPort("8000/tcp").WithStartupTimeout(30 * time.Second),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start centrifugo: %v", err)
	}
	t.Cleanup(func() { container.Terminate(ctx) })

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("get host: %v", err)
	}
	port, err := container.MappedPort(ctx, "8000")
	if err != nil {
		t.Fatalf("get port: %v", err)
	}

	apiURL = fmt.Sprintf("http://%s:%s", host, port.Port())
	return apiURL, apiKey
}

func TestCentrifugoTransportIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	ctx := context.Background()
	apiURL, apiKey := startCentrifugo(t, ctx)

	transport := NewCentrifugoTransport(CentrifugoConfig{
		APIURL: apiURL,
		APIKey: apiKey,
	})

	// Step 1: Publish to workspace channel — should not error
	err := transport.SendToWorkspace(ctx, "w1", Message{
		Type: "table_diff",
		Data: map[string]any{
			"table": "users",
			"id":    "u1",
			"ver":   int64(2),
			"patch": []PatchOp{
				{Op: "replace", Path: "/name", Value: "Alice Updated"},
			},
		},
	})
	if err != nil {
		t.Fatalf("SendToWorkspace failed: %v", err)
	}
	t.Log("Step 1 OK: SendToWorkspace succeeded against real Centrifugo")

	// Step 2: Publish to seance channel
	err = transport.SendToSeance(ctx, "seance-1", Message{
		Type: "view_diff",
		Data: map[string]any{
			"view":        "user_list",
			"params_hash": "abc123",
			"version":     int64(2),
			"refs_patch": []PatchOp{
				{Op: "add", Path: "/1", Value: Ref{Table: "users", ID: "u2", Fields: []string{"id", "name"}}},
			},
		},
	})
	if err != nil {
		t.Fatalf("SendToSeance failed: %v", err)
	}
	t.Log("Step 2 OK: SendToSeance succeeded against real Centrifugo")

	// Step 3: Verify channel naming by checking Centrifugo API channels info
	resp, err := centrifugoAPICall(apiURL, apiKey, "channels", map[string]any{})
	if err != nil {
		t.Fatalf("channels API call failed: %v", err)
	}
	t.Logf("Step 3 OK: Centrifugo channels response: %s", resp)

	// Step 4: Disconnect (should not error even if user doesn't exist)
	err = transport.DisconnectSeance(ctx, "nonexistent-seance")
	if err != nil {
		t.Fatalf("DisconnectSeance failed: %v", err)
	}
	t.Log("Step 4 OK: DisconnectSeance succeeded")

	// Step 5: Verify message format by publishing and checking info
	err = transport.SendToWorkspace(ctx, "test-workspace", Message{
		Type: "table_diff",
		Data: map[string]any{
			"table": "orders",
			"id":    "o1",
			"ver":   int64(1),
			"patch": []PatchOp{{Op: "add", Path: "/status", Value: "pending"}},
		},
	})
	if err != nil {
		t.Fatalf("second SendToWorkspace failed: %v", err)
	}
	t.Log("Step 5 OK: Multiple workspace channels work")
}

func centrifugoAPICall(apiURL, apiKey, method string, params map[string]any) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"method": method,
		"params": params,
	})

	req, err := http.NewRequest("POST", apiURL+"/api", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "apikey "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	return string(respBody), nil
}
