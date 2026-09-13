package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dolorous01/canvas-standalone/backend/internal/authn"
	"github.com/dolorous01/canvas-standalone/backend/internal/credential"
	"github.com/dolorous01/canvas-standalone/backend/internal/gateway"
)

type candidateOfficial struct {
	keys []gateway.APIKeySummary
}

func (official candidateOfficial) Profile(context.Context, string) (gateway.Principal, error) {
	return gateway.Principal{}, errors.New("unexpected Profile call")
}

func (official candidateOfficial) ListKeys(context.Context, string) ([]gateway.APIKeySummary, error) {
	return official.keys, nil
}

func (official candidateOfficial) GetKey(context.Context, string, int64) (gateway.APIKeySecret, error) {
	return gateway.APIKeySecret{}, errors.New("unexpected GetKey call")
}

type candidateCredentials struct {
	records []credential.Record
}

func (repository candidateCredentials) Upsert(context.Context, int64, int64, string, string, string, credential.Ciphertext) (credential.Record, error) {
	return credential.Record{}, errors.New("unexpected Upsert call")
}

func (repository candidateCredentials) List(context.Context, int64) ([]credential.Record, error) {
	return repository.records, nil
}

func (repository candidateCredentials) GetByExternalKey(context.Context, int64, int64) (credential.Record, error) {
	return credential.Record{}, errors.New("unexpected GetByExternalKey call")
}

func (repository candidateCredentials) Delete(context.Context, int64, string, string) error {
	return errors.New("unexpected Delete call")
}

func TestCredentialCandidatesReportsMissingBindingsWithoutSecrets(t *testing.T) {
	server := &server{dependencies: Dependencies{
		Official: candidateOfficial{keys: []gateway.APIKeySummary{
			{ID: 20, Name: "Disabled", Status: "disabled", Quota: 20, QuotaUsed: 2},
			{ID: 10, Name: "Active", Status: "active", Quota: 10, QuotaUsed: 1},
		}},
		Credentials: candidateCredentials{records: []credential.Record{
			{ExternalAPIKeyID: 30, PublicID: "cred_missing_30", Status: "disabled", KeyHint: "...0030"},
			{ExternalAPIKeyID: 20, PublicID: "cred_bound_20", Status: "active", KeyHint: "...0020"},
			{ExternalAPIKeyID: 25, PublicID: "cred_missing_25", Status: "active", KeyHint: "...0025"},
		}},
	}}
	request := httptest.NewRequest(http.MethodGet, "/canvas-api/v1/credentials/candidates", nil)
	request = request.WithContext(authn.WithSession(request.Context(), authn.Session{
		Principal: gateway.Principal{ExternalUserID: 42, Role: "user", Status: "active"},
		Bearer:    "opaque-token",
	}))
	response := httptest.NewRecorder()

	server.credentialCandidates(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload struct {
		Data struct {
			Items []struct {
				ID    int64 `json:"id"`
				Bound bool  `json:"bound"`
			} `json:"items"`
			MissingBindings []struct {
				ExternalAPIKeyID int64  `json:"external_api_key_id"`
				BindingStatus    string `json:"binding_status"`
			} `json:"missing_bindings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Data.Items) != 2 || payload.Data.Items[0].ID != 20 || !payload.Data.Items[0].Bound || payload.Data.Items[1].ID != 10 || payload.Data.Items[1].Bound {
		t.Fatalf("items = %+v", payload.Data.Items)
	}
	missing := payload.Data.MissingBindings
	if len(missing) != 2 || missing[0].ExternalAPIKeyID != 25 || missing[0].BindingStatus != "active" || missing[1].ExternalAPIKeyID != 30 || missing[1].BindingStatus != "disabled" {
		t.Fatalf("missing_bindings = %+v", missing)
	}
	var raw map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	data := raw["data"].(map[string]any)
	for _, item := range data["missing_bindings"].([]any) {
		fields := item.(map[string]any)
		for _, forbidden := range []string{"credential_id", "key_hint", "ciphertext", "nonce", "key"} {
			if _, exists := fields[forbidden]; exists {
				t.Fatalf("missing binding leaks %q: %+v", forbidden, fields)
			}
		}
	}
}
