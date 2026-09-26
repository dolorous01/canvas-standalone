package authn

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/dolorous01/canvas-standalone/backend/internal/gateway"
)

func TestSessionCacheCannotBypassChangedBinding(t *testing.T) {
	headers := http.Header{}
	headers.Set("User-Agent", "browser-a")
	headers.Set("CF-Connecting-IP", "203.0.113.1")
	original := gateway.WithSessionHeaders(context.Background(), headers)
	calls := 0
	auth := New(profileClientFunc(func(ctx context.Context, token string) (gateway.Principal, error) {
		calls++
		if gateway.SessionFingerprint(ctx) != gateway.SessionFingerprint(original) {
			return gateway.Principal{}, errors.New("binding mismatch")
		}
		return gateway.Principal{ExternalUserID: 42}, nil
	}), 20*time.Second, 8)
	for range 2 {
		if _, err := auth.AuthenticateToken(original, "token", false); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("original session was not cached")
	}
	for _, name := range []string{"User-Agent", "CF-Connecting-IP"} {
		changed := headers.Clone()
		changed.Set(name, "changed")
		ctx := gateway.WithSessionHeaders(context.Background(), changed)
		if _, err := auth.AuthenticateToken(ctx, "token", false); err == nil {
			t.Fatal("changed binding bypassed official verification")
		}
	}
	auth.Invalidate("token")
	if _, err := auth.AuthenticateToken(original, "token", false); err != nil {
		t.Fatal(err)
	}
	if calls != 4 {
		t.Fatalf("expected all changed and invalidated bindings to be verified, calls=%d", calls)
	}
}
