package arcana

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// CentrifugoTransport publishes messages via Centrifugo HTTP API.
type CentrifugoTransport struct {
	apiURL  string
	apiKey  string
	client  *http.Client
	retries int
}

// CentrifugoConfig holds configuration for CentrifugoTransport.
type CentrifugoConfig struct {
	APIURL     string
	APIKey     string
	HTTPClient *http.Client
	Retries    int
}

// BatchMessage represents a single message in a batch publish.
type BatchMessage struct {
	Channel string
	Msg     Message
}

// NewCentrifugoTransport creates a transport that publishes via Centrifugo HTTP API.
func NewCentrifugoTransport(cfg CentrifugoConfig) *CentrifugoTransport {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	retries := cfg.Retries
	if retries == 0 {
		retries = 3
	}
	return &CentrifugoTransport{
		apiURL:  cfg.APIURL,
		apiKey:  cfg.APIKey,
		client:  client,
		retries: retries,
	}
}

// SendToSeance publishes a message to the seance-specific channel.
func (t *CentrifugoTransport) SendToSeance(ctx context.Context, seanceID string, msg Message) error {
	channel := "views:" + seanceID
	return t.publish(ctx, channel, msg)
}

// SendToWorkspace publishes a message to the workspace channel.
func (t *CentrifugoTransport) SendToWorkspace(ctx context.Context, workspaceID string, msg Message) error {
	channel := "workspace:" + workspaceID
	return t.publish(ctx, channel, msg)
}

// DisconnectSeance disconnects a client by seance ID.
func (t *CentrifugoTransport) DisconnectSeance(ctx context.Context, seanceID string) error {
	return t.apiCall(ctx, "disconnect", map[string]any{
		"user": seanceID,
	})
}

func (t *CentrifugoTransport) publish(ctx context.Context, channel string, msg Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("arcana: marshal message: %w", err)
	}

	return t.apiCall(ctx, "publish", map[string]any{
		"channel": channel,
		"data":    json.RawMessage(data),
	})
}

// SendBatch publishes multiple messages in a single HTTP call.
func (t *CentrifugoTransport) SendBatch(ctx context.Context, msgs []BatchMessage) error {
	if len(msgs) == 0 {
		return nil
	}
	if len(msgs) == 1 {
		return t.publish(ctx, msgs[0].Channel, msgs[0].Msg)
	}

	var buf bytes.Buffer
	for i, m := range msgs {
		data, err := json.Marshal(m.Msg)
		if err != nil {
			return fmt.Errorf("arcana: marshal batch message %d: %w", i, err)
		}
		line, err := json.Marshal(map[string]any{
			"method": "publish",
			"params": map[string]any{
				"channel": m.Channel,
				"data":    json.RawMessage(data),
			},
		})
		if err != nil {
			return fmt.Errorf("arcana: marshal batch command %d: %w", i, err)
		}
		if i > 0 {
			buf.WriteByte('\n')
		}
		buf.Write(line)
	}

	return t.doRequest(ctx, t.apiURL+"/api/batch", buf.Bytes())
}

func (t *CentrifugoTransport) apiCall(ctx context.Context, method string, params map[string]any) error {
	body, err := json.Marshal(map[string]any{
		"method": method,
		"params": params,
	})
	if err != nil {
		return fmt.Errorf("arcana: marshal api call: %w", err)
	}

	return t.doRequest(ctx, t.apiURL+"/api", body)
}

func (t *CentrifugoTransport) doRequest(ctx context.Context, url string, body []byte) error {
	backoff := 100 * time.Millisecond
	var lastErr error

	for attempt := 0; attempt < t.retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 5
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("arcana: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "apikey "+t.apiKey)

		resp, err := t.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("arcana: centrifugo api call: %w", err)
			continue
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			resp.Body.Close()
			return nil
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		lastErr = fmt.Errorf("arcana: centrifugo returned %d: %s", resp.StatusCode, string(respBody))

		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			return lastErr
		}
	}

	return lastErr
}
