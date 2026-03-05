package arcana

import (
	"context"
	"testing"
)

func TestContextIdentity(t *testing.T) {
	id := &Identity{
		SeanceID:    "s1",
		UserID:      "u1",
		WorkspaceID: "w1",
	}

	ctx := WithIdentity(context.Background(), id)

	got := IdentityFromCtx(ctx)
	if got == nil {
		t.Fatal("expected identity")
	}
	if got.UserID != "u1" {
		t.Fatalf("expected u1, got %s", got.UserID)
	}

	if WorkspaceID(ctx) != "w1" {
		t.Fatalf("expected w1, got %s", WorkspaceID(ctx))
	}

	if User(ctx).SeanceID != "s1" {
		t.Fatal("User() mismatch")
	}
}

func TestContextIdentityNil(t *testing.T) {
	ctx := context.Background()

	if IdentityFromCtx(ctx) != nil {
		t.Fatal("expected nil identity")
	}
	if WorkspaceID(ctx) != "" {
		t.Fatal("expected empty workspace")
	}
}
