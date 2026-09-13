package authn

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dolorous01/canvas-standalone/backend/internal/gateway"
)

type profileClientFunc func(context.Context, string) (gateway.Principal, error)

func (function profileClientFunc) Profile(ctx context.Context, token string) (gateway.Principal, error) {
	return function(ctx, token)
}

func TestAuthenticatorCachesOnlyPositiveHashedSessions(t *testing.T) {
	var calls atomic.Int32
	client := profileClientFunc(func(_ context.Context, token string) (gateway.Principal, error) {
		calls.Add(1)
		if token == "revoked" {
			return gateway.Principal{}, errors.New("revoked")
		}
		return gateway.Principal{ExternalUserID: 42, Role: "user", Status: "active"}, nil
	})
	auth := New(client, 20*time.Second, 2)
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	auth.now = func() time.Time { return now }

	for range 2 {
		principal, err := auth.AuthenticateToken(context.Background(), "opaque-token", false)
		if err != nil || principal.ExternalUserID != 42 {
			t.Fatalf("AuthenticateToken() = %+v, %v", principal, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("profile calls = %d, want 1", calls.Load())
	}
	if len(auth.cache) != 1 {
		t.Fatalf("cache entries = %d", len(auth.cache))
	}
	for key := range auth.cache {
		if string(key[:]) == "opaque-token" {
			t.Fatal("cache key contains the raw token")
		}
	}

	now = now.Add(21 * time.Second)
	if _, err := auth.AuthenticateToken(context.Background(), "opaque-token", false); err != nil {
		t.Fatal(err)
	}
	if _, err := auth.AuthenticateToken(context.Background(), "opaque-token", true); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Fatalf("profile calls after expiry/fresh = %d, want 3", calls.Load())
	}

	for range 2 {
		if _, err := auth.AuthenticateToken(context.Background(), "revoked", false); err == nil {
			t.Fatal("revoked token was accepted")
		}
	}
	if calls.Load() != 5 {
		t.Fatalf("negative result was cached; calls = %d", calls.Load())
	}
}

func TestExtractBearerIsStrict(t *testing.T) {
	value, err := ExtractBearer("Bearer opaque-token")
	if err != nil || value != "opaque-token" {
		t.Fatalf("ExtractBearer() = %q, %v", value, err)
	}
	for _, header := range []string{"", "Basic value", "Bearer", "Bearer one two", "Bearer value\nInjected: yes"} {
		if _, err := ExtractBearer(header); err == nil {
			t.Fatalf("invalid header accepted: %q", header)
		}
	}
}
