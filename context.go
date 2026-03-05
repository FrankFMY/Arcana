package arcana

import (
	"context"
	"net/http"
)

type contextKey int

const (
	ctxKeyIdentity contextKey = iota
)

// AuthFunc extracts an Identity from an HTTP request.
type AuthFunc func(r *http.Request) (*Identity, error)

// WithIdentity stores an Identity in the context.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, ctxKeyIdentity, id)
}

// IdentityFromCtx retrieves the Identity from the context.
func IdentityFromCtx(ctx context.Context) *Identity {
	id, _ := ctx.Value(ctxKeyIdentity).(*Identity)
	return id
}

// WorkspaceID is a shorthand to get workspace ID from the context identity.
func WorkspaceID(ctx context.Context) string {
	id := IdentityFromCtx(ctx)
	if id == nil {
		return ""
	}
	return id.WorkspaceID
}

// User returns the Identity from context (alias for IdentityFromCtx).
func User(ctx context.Context) *Identity {
	return IdentityFromCtx(ctx)
}
