package arcana

import "context"

// Transport abstracts the message delivery layer (Centrifugo, WebSocket, SSE).
type Transport interface {
	// SendToSeance delivers a message to a specific client session.
	SendToSeance(ctx context.Context, seanceID string, msg Message) error

	// SendToWorkspace delivers a message to all sessions in a workspace.
	SendToWorkspace(ctx context.Context, workspaceID string, msg Message) error

	// DisconnectSeance forcibly disconnects a client session.
	DisconnectSeance(ctx context.Context, seanceID string) error
}
