package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dolorous01/canvas-standalone/backend/internal/authn"
	"github.com/dolorous01/canvas-standalone/backend/internal/credential"
	"github.com/dolorous01/canvas-standalone/backend/internal/gateway"
	"github.com/dolorous01/canvas-standalone/backend/internal/policy"
)

type discoveryOfficial struct {
	candidateOfficial
	role   string
	owner  int64
	status string
	calls  int
}

func (o *discoveryOfficial) Profile(context.Context, string) (gateway.Principal, error) {
	return gateway.Principal{ExternalUserID: 42, Role: o.role, Status: "active"}, nil
}
func (o *discoveryOfficial) GetKey(context.Context, string, int64) (gateway.APIKeySecret, error) {
	return gateway.APIKeySecret{UserID: o.owner, Summary: gateway.APIKeySummary{ID: 7, Status: o.status}, Key: []byte("private-discovery-key")}, nil
}
func (o *discoveryOfficial) ListModels(_ context.Context, key []byte) ([]string, error) {
	o.calls++
	return []string{"gpt-5", "gpt-image-2.5-flare", "gpt-image-2", "custom-alias"}, nil
}

type discoveryCredentials struct {
	candidateCredentials
	record  credential.Record
	missing bool
}

func (r discoveryCredentials) GetByExternalKey(_ context.Context, owner, id int64) (credential.Record, error) {
	if owner != 42 || id != 7 || r.missing {
		return credential.Record{}, credential.ErrNotFound
	}
	return r.record, nil
}

type discoveryPolicy struct{ PolicyRepository }

func (discoveryPolicy) Get(context.Context) (policy.Policy, error) {
	return policy.Policy{Models: []policy.Model{{Model: "gpt-image-2", Capability: json.RawMessage(`{"media_kind":"image","edit":true}`)}}}, nil
}
func TestDiscoveryEnforcesBoundKeyOwnershipAndRole(t *testing.T) {
	for _, tc := range []struct {
		name, role, status string
		owner, boundOwner  int64
		missing, stale     bool
		want               int
	}{
		{"admin", "admin", "active", 42, 42, false, false, 200},
		{"ordinary user", "user", "active", 42, 42, false, false, 403},
		{"other owner", "admin", "active", 99, 42, false, false, 403},
		{"other binding", "admin", "active", 42, 99, false, false, 403},
		{"disabled", "admin", "disabled", 42, 42, false, false, 409},
		{"unbound", "admin", "active", 42, 42, true, false, 404},
		{"rotated key", "admin", "active", 42, 42, false, true, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ring, _ := credential.NewKeyringForTest(1, map[int][]byte{1: bytes.Repeat([]byte{1}, 32)})
			key := "private-discovery-key"
			if tc.stale {
				key = "stale-key"
			}
			encrypted, _ := ring.Encrypt(tc.boundOwner, 7, []byte(key))
			official := &discoveryOfficial{role: tc.role, owner: tc.owner, status: tc.status}
			s := &server{dependencies: Dependencies{Official: official, Authenticator: authn.New(official, 0, 1),
				Credentials: discoveryCredentials{record: credential.Record{ExternalUserID: tc.boundOwner, ExternalAPIKeyID: 7, Status: "active", Encrypted: encrypted}, missing: tc.missing},
				Policies:    discoveryPolicy{}, Keyring: ring, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}}
			r := httptest.NewRequest("GET", "/canvas-api/v1/admin/model-discovery?api_key_id=7", nil)
			r = r.WithContext(authn.WithSession(r.Context(), authn.Session{Principal: gateway.Principal{ExternalUserID: 42, Role: "admin"}, Bearer: "session-token"}))
			w := httptest.NewRecorder()
			s.discoverModels(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "private-discovery-key") || strings.Contains(w.Body.String(), "session-token") {
				t.Fatal("secret leak")
			}
			if tc.want != 200 && official.calls != 0 {
				t.Fatal("probed unauthorized key")
			}
			if tc.want == 200 {
				if official.calls != 1 || !strings.Contains(w.Body.String(), `"generation_verified":false`) || !strings.Contains(w.Body.String(), "gpt-image-2.5-flare") || strings.Contains(w.Body.String(), "gpt-5") || strings.Contains(w.Body.String(), "custom-alias") {
					t.Fatalf("unexpected discovery: %s", w.Body.String())
				}
			}
		})
	}
}
func TestRecognizeImageModelPreservesExistingParameters(t *testing.T) {
	configured := []policy.Model{{Model: "custom-alias", Capability: json.RawMessage(`{"edit":true,"custom_field":"keep"}`)}}
	got, ok := recognizeImageModel("custom-alias", configured)
	if !ok || !got.Configured || string(got.Capability) != string(configured[0].Capability) {
		t.Fatal("existing parameters changed")
	}
	for _, id := range []string{"gpt-image-2.5-flare", "gpt-image-2.5-sunburst", "grok-imagine-image-quality", "dall-e-3"} {
		got, ok := recognizeImageModel(id, nil)
		if !ok || got.ParameterSource != "basic" || !bytes.Contains(got.Capability, []byte(`"edit":false`)) {
			t.Fatalf("recognition: %s", id)
		}
	}
	for _, id := range []string{"gpt-5", "gemini-image", "image-analysis", "unknown"} {
		if _, ok := recognizeImageModel(id, nil); ok {
			t.Fatalf("unsafe recognition: %s", id)
		}
	}
}
