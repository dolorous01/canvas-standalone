package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/dolorous01/canvas-standalone/backend/internal/policy"
	"github.com/dolorous01/canvas-standalone/backend/internal/secretfile"
)

type discoveredModel struct {
	Model           string          `json:"model"`
	Capability      json.RawMessage `json:"capability"`
	Configured      bool            `json:"configured"`
	ParameterSource string          `json:"parameter_source"`
}

func (server *server) discoverModels(writer http.ResponseWriter, request *http.Request) {
	session, ok := server.requireAdmin(writer, request, true)
	if !ok {
		return
	}
	keyID, err := strconv.ParseInt(request.URL.Query().Get("api_key_id"), 10, 64)
	if err != nil || keyID <= 0 {
		server.writeError(writer, request, errInvalidRequest)
		return
	}
	owner := session.Principal.ExternalUserID
	binding, err := server.dependencies.Credentials.GetByExternalKey(request.Context(), owner, keyID)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	if binding.ExternalUserID != owner || binding.ExternalAPIKeyID != keyID {
		server.writeError(writer, request, errForbidden)
		return
	}
	if binding.Status != "active" || server.dependencies.Keyring == nil {
		server.writeError(writer, request, errKeyUnavailable)
		return
	}
	secret, err := server.dependencies.Official.GetKey(request.Context(), session.Bearer, keyID)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	defer secretfile.Zero(secret.Key)
	if secret.UserID != owner || secret.Summary.ID != keyID {
		server.writeError(writer, request, errForbidden)
		return
	}
	if secret.Summary.Status != "active" || (secret.Summary.Quota > 0 && secret.Summary.QuotaUsed >= secret.Summary.Quota) {
		server.writeError(writer, request, errKeyUnavailable)
		return
	}
	if secret.Summary.ExpiresAt != nil {
		expires, parseErr := time.Parse(time.RFC3339, *secret.Summary.ExpiresAt)
		if parseErr != nil || !expires.After(time.Now()) {
			server.writeError(writer, request, errKeyUnavailable)
			return
		}
	}
	key, err := server.dependencies.Keyring.Decrypt(owner, keyID, binding.Encrypted)
	if err != nil {
		server.writeError(writer, request, errKeyUnavailable)
		return
	}
	defer secretfile.Zero(key)
	if !bytes.Equal(key, secret.Key) {
		server.writeError(writer, request, errKeyUnavailable)
		return
	}
	ids, err := server.dependencies.Official.ListModels(request.Context(), key)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	current, err := server.dependencies.Policies.Get(request.Context())
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	models := make([]discoveredModel, 0)
	for _, id := range ids {
		if model, recognized := recognizeImageModel(id, current.Models); recognized {
			models = append(models, model)
		}
	}
	writer.Header().Set("Cache-Control", "no-store")
	server.writeSuccess(writer, http.StatusOK, map[string]any{
		"api_key_id": keyID, "models": models, "total": len(ids),
		"unrecognized": len(ids) - len(models), "source": "/v1/models", "generation_verified": false,
	})
}

// Only recognize image families supported by the current /images transport.
func recognizeImageModel(id string, configured []policy.Model) (discoveredModel, bool) {
	for _, model := range configured {
		if model.Model == id {
			return discoveredModel{id, model.Capability, true, "configured"}, true
		}
	}
	lower := strings.ToLower(id)
	provider := "openai"
	if strings.HasPrefix(lower, "grok-imagine-image") {
		provider = "grok"
	} else if !strings.HasPrefix(lower, "gpt-image-") && lower != "dall-e-2" && lower != "dall-e-3" {
		return discoveredModel{}, false
	}
	// /models supplies no parameter schema: do not inherit advanced capabilities.
	capability, _ := json.Marshal(map[string]any{
		"media_kind": "image", "provider": provider, "dimension_mode": "size",
		"generation": true, "edit": false, "multi_image": false, "mask": false,
		"max_input_images": 0, "max_outputs": 1, "sizes": []string{"1024x1024"},
		"defaults": map[string]string{"size": "1024x1024"},
	})
	return discoveredModel{id, capability, false, "basic"}, true
}
