package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dolorous01/canvas-standalone/backend/internal/asset"
	"github.com/dolorous01/canvas-standalone/backend/internal/authn"
	"github.com/dolorous01/canvas-standalone/backend/internal/credential"
	"github.com/dolorous01/canvas-standalone/backend/internal/editor"
	"github.com/dolorous01/canvas-standalone/backend/internal/gateway"
	"github.com/dolorous01/canvas-standalone/backend/internal/job"
	"github.com/dolorous01/canvas-standalone/backend/internal/library"
	"github.com/dolorous01/canvas-standalone/backend/internal/logging"
	"github.com/dolorous01/canvas-standalone/backend/internal/policy"
	"github.com/dolorous01/canvas-standalone/backend/internal/project"
	"github.com/dolorous01/canvas-standalone/backend/internal/publicid"
	"github.com/dolorous01/canvas-standalone/backend/internal/secretfile"
)

const maxJSONRequest = int64(2 << 20)

type OfficialClient interface {
	Profile(context.Context, string) (gateway.Principal, error)
	ListKeys(context.Context, string) ([]gateway.APIKeySummary, error)
	GetKey(context.Context, string, int64) (gateway.APIKeySecret, error)
}

type CredentialRepository interface {
	Upsert(context.Context, int64, int64, string, string, string, credential.Ciphertext) (credential.Record, error)
	List(context.Context, int64) ([]credential.Record, error)
	GetByExternalKey(context.Context, int64, int64) (credential.Record, error)
	Delete(context.Context, int64, string, string) error
}

type ProjectRepository interface {
	List(context.Context, int64, int, int) ([]project.Project, error)
	Get(context.Context, int64, string) (project.Project, error)
	Create(context.Context, int64, string, string, json.RawMessage, []project.AssetReference) (project.Project, error)
	Update(context.Context, int64, string, int64, string, json.RawMessage, []project.AssetReference) (project.Project, error)
	Delete(context.Context, int64, string) error
}

type PolicyRepository interface {
	Get(context.Context) (policy.Policy, error)
	Update(context.Context, int64, string, int64, bool, []policy.Model) (policy.Policy, error)
	ListAudits(context.Context, int) ([]policy.Audit, error)
}

type Dependencies struct {
	Authenticator          *authn.Authenticator
	Official               OfficialClient
	Credentials            CredentialRepository
	Projects               ProjectRepository
	Assets                 *asset.Service
	Libraries              *library.Repository
	Editors                *editor.Service
	Jobs                   *job.Service
	Policies               PolicyRepository
	Keyring                *credential.Keyring
	WritesEnabled          bool
	Release                string
	Logger                 *slog.Logger
	AllowedExternalUserIDs []int64
}

type server struct {
	dependencies Dependencies
	allowedUsers map[int64]struct{}
}

type envelope struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Reason  string `json:"reason,omitempty"`
	Data    any    `json:"data,omitempty"`
}

func New(dependencies Dependencies) (http.Handler, error) {
	if dependencies.Authenticator == nil || dependencies.Official == nil || dependencies.Credentials == nil ||
		dependencies.Projects == nil || dependencies.Assets == nil || dependencies.Libraries == nil || dependencies.Editors == nil || dependencies.Jobs == nil || dependencies.Policies == nil || dependencies.Logger == nil {
		return nil, errors.New("canvas API dependencies are incomplete")
	}
	if dependencies.WritesEnabled && dependencies.Keyring == nil {
		return nil, errors.New("canvas writes require a credential keyring")
	}
	handler := &server{dependencies: dependencies, allowedUsers: make(map[int64]struct{}, len(dependencies.AllowedExternalUserIDs))}
	for _, identifier := range dependencies.AllowedExternalUserIDs {
		if identifier > 0 {
			handler.allowedUsers[identifier] = struct{}{}
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /canvas-api/v1/session", handler.session)
	mux.HandleFunc("GET /canvas-api/v1/credentials/candidates", handler.credentialCandidates)
	mux.HandleFunc("POST /canvas-api/v1/credentials", handler.bindCredential)
	mux.HandleFunc("DELETE /canvas-api/v1/credentials/{credential_id}", handler.deleteCredential)
	mux.HandleFunc("GET /canvas-api/v1/config", handler.canvasConfig)
	mux.HandleFunc("GET /canvas-api/v1/projects", handler.listProjects)
	mux.HandleFunc("POST /canvas-api/v1/projects", handler.createProject)
	mux.HandleFunc("GET /canvas-api/v1/projects/{project_id}", handler.getProject)
	mux.HandleFunc("PATCH /canvas-api/v1/projects/{project_id}", handler.updateProject)
	mux.HandleFunc("DELETE /canvas-api/v1/projects/{project_id}", handler.deleteProject)
	mux.HandleFunc("POST /canvas-api/v1/assets", handler.uploadAsset)
	mux.HandleFunc("GET /canvas-api/v1/assets/{asset_id}", handler.downloadAsset)
	mux.HandleFunc("GET /canvas-api/v1/assets/{asset_id}/thumbnail", handler.downloadAssetThumbnail)
	mux.HandleFunc("GET /canvas-api/v1/library-items", handler.listLibraryItems)
	mux.HandleFunc("POST /canvas-api/v1/library-items", handler.createLibraryItem)
	mux.HandleFunc("PATCH /canvas-api/v1/library-items/{item_id}", handler.updateLibraryItem)
	mux.HandleFunc("DELETE /canvas-api/v1/library-items/{item_id}", handler.deleteLibraryItem)
	mux.HandleFunc("POST /canvas-api/v1/editor-documents", handler.createEditorDocument)
	mux.HandleFunc("GET /canvas-api/v1/editor-documents/{document_id}", handler.getEditorDocument)
	mux.HandleFunc("PATCH /canvas-api/v1/editor-documents/{document_id}", handler.updateEditorDocument)
	mux.HandleFunc("POST /canvas-api/v1/editor-documents/{document_id}/assets", handler.uploadEditorAsset)
	mux.HandleFunc("POST /canvas-api/v1/jobs", handler.createJob)
	mux.HandleFunc("GET /canvas-api/v1/jobs/{job_id}", handler.getJob)
	mux.HandleFunc("DELETE /canvas-api/v1/jobs/{job_id}", handler.cancelJob)
	mux.HandleFunc("GET /canvas-api/v1/jobs/{job_id}/events", handler.streamJobEvents)
	mux.HandleFunc("GET /canvas-api/v1/admin/model-policy", handler.getModelPolicy)
	mux.HandleFunc("PUT /canvas-api/v1/admin/model-policy", handler.updateModelPolicy)
	mux.HandleFunc("GET /canvas-api/v1/admin/model-policy/audit", handler.listModelPolicyAudits)
	protected := http.Handler(mux)
	if len(handler.allowedUsers) > 0 {
		protected = handler.requireAllowedUser(protected)
	}
	authenticated := authn.Middleware(dependencies.Authenticator, handler.writeError, protected)
	return handler.requestMiddleware(authenticated), nil
}

func (server *server) requireAllowedUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		session, ok := authn.FromContext(request.Context())
		if !ok {
			server.writeError(writer, request, errForbidden)
			return
		}
		if _, allowed := server.allowedUsers[session.Principal.ExternalUserID]; !allowed {
			server.writeError(writer, request, errForbidden)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func (server *server) session(writer http.ResponseWriter, request *http.Request) {
	session, _ := authn.FromContext(request.Context())
	server.writeSuccess(writer, http.StatusOK, map[string]any{
		"external_user_id": session.Principal.ExternalUserID,
		"role":             session.Principal.Role,
		"status":           session.Principal.Status,
	})
}

func (server *server) credentialCandidates(writer http.ResponseWriter, request *http.Request) {
	session, _ := authn.FromContext(request.Context())
	keys, err := server.dependencies.Official.ListKeys(request.Context(), session.Bearer)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	bindings, err := server.dependencies.Credentials.List(request.Context(), session.Principal.ExternalUserID)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	byExternalID := make(map[int64]credential.Record, len(bindings))
	for _, item := range bindings {
		byExternalID[item.ExternalAPIKeyID] = item
	}
	officialIDs := make(map[int64]struct{}, len(keys))
	type candidate struct {
		ID           int64   `json:"id"`
		Name         string  `json:"name"`
		GroupID      *int64  `json:"group_id,omitempty"`
		Status       string  `json:"status"`
		Quota        float64 `json:"quota"`
		QuotaUsed    float64 `json:"quota_used"`
		ExpiresAt    *string `json:"expires_at,omitempty"`
		Bound        bool    `json:"bound"`
		CredentialID string  `json:"credential_id,omitempty"`
		KeyHint      string  `json:"key_hint,omitempty"`
	}
	items := make([]candidate, 0, len(keys))
	for _, key := range keys {
		officialIDs[key.ID] = struct{}{}
		binding, bound := byExternalID[key.ID]
		items = append(items, candidate{
			ID: key.ID, Name: key.Name, GroupID: key.GroupID, Status: key.Status,
			Quota: key.Quota, QuotaUsed: key.QuotaUsed, ExpiresAt: key.ExpiresAt,
			Bound: bound && binding.Status == "active", CredentialID: binding.PublicID, KeyHint: binding.KeyHint,
		})
	}
	type missingBinding struct {
		ExternalAPIKeyID int64  `json:"external_api_key_id"`
		BindingStatus    string `json:"binding_status"`
	}
	missingBindings := make([]missingBinding, 0)
	for _, binding := range bindings {
		if _, exists := officialIDs[binding.ExternalAPIKeyID]; !exists {
			missingBindings = append(missingBindings, missingBinding{
				ExternalAPIKeyID: binding.ExternalAPIKeyID,
				BindingStatus:    binding.Status,
			})
		}
	}
	sort.Slice(missingBindings, func(left, right int) bool {
		return missingBindings[left].ExternalAPIKeyID < missingBindings[right].ExternalAPIKeyID
	})
	server.writeSuccess(writer, http.StatusOK, map[string]any{
		"items":            items,
		"missing_bindings": missingBindings,
	})
}

func (server *server) bindCredential(writer http.ResponseWriter, request *http.Request) {
	if !server.requireWrites(writer, request) {
		return
	}
	var input struct {
		ExternalAPIKeyID int64 `json:"external_api_key_id"`
	}
	if err := decodeJSON(writer, request, &input); err != nil {
		server.writeError(writer, request, err)
		return
	}
	if input.ExternalAPIKeyID <= 0 {
		server.writeError(writer, request, errInvalidRequest)
		return
	}
	session, _ := authn.FromContext(request.Context())
	principal, err := server.dependencies.Authenticator.AuthenticateToken(request.Context(), session.Bearer, true)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	if principal.ExternalUserID != session.Principal.ExternalUserID {
		server.dependencies.Authenticator.Invalidate(session.Bearer)
		server.writeError(writer, request, errForbidden)
		return
	}
	secret, err := server.dependencies.Official.GetKey(request.Context(), session.Bearer, input.ExternalAPIKeyID)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	defer secretfile.Zero(secret.Key)
	if secret.UserID != principal.ExternalUserID {
		server.writeError(writer, request, errForbidden)
		return
	}
	if secret.Summary.Status != "active" {
		server.writeError(writer, request, errKeyUnavailable)
		return
	}
	encrypted, err := server.dependencies.Keyring.Encrypt(principal.ExternalUserID, secret.Summary.ID, secret.Key)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	record, err := server.dependencies.Credentials.Upsert(
		request.Context(), principal.ExternalUserID, secret.Summary.ID, secret.Summary.Name,
		keyHint(secret.Key), logging.RequestID(request.Context()), encrypted,
	)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusCreated, credentialDTO(record))
}

func (server *server) deleteCredential(writer http.ResponseWriter, request *http.Request) {
	if !server.requireWrites(writer, request) {
		return
	}
	identifier := request.PathValue("credential_id")
	if !publicid.Valid(identifier) {
		server.writeError(writer, request, errNotFound)
		return
	}
	session, _ := authn.FromContext(request.Context())
	if err := server.dependencies.Credentials.Delete(request.Context(), session.Principal.ExternalUserID, identifier, logging.RequestID(request.Context())); err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusOK, map[string]bool{"deleted": true})
}

func (server *server) canvasConfig(writer http.ResponseWriter, request *http.Request) {
	session, _ := authn.FromContext(request.Context())
	keys, err := server.dependencies.Official.ListKeys(request.Context(), session.Bearer)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	bindings, err := server.dependencies.Credentials.List(request.Context(), session.Principal.ExternalUserID)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	modelPolicy, err := server.dependencies.Policies.Get(request.Context())
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	bound := make(map[int64]credential.Record, len(bindings))
	for _, item := range bindings {
		bound[item.ExternalAPIKeyID] = item
	}
	type keyDTO struct {
		ID                int64  `json:"id"`
		Name              string `json:"name"`
		GroupID           int64  `json:"group_id"`
		GroupName         string `json:"group_name"`
		Available         bool   `json:"available"`
		UnavailableReason string `json:"unavailable_reason,omitempty"`
	}
	apiKeys := make([]keyDTO, 0, len(keys))
	var selected *int64
	requestedID, _ := strconv.ParseInt(request.URL.Query().Get("api_key_id"), 10, 64)
	for _, key := range keys {
		binding, isBound := bound[key.ID]
		available := key.Status == "active" && isBound && binding.Status == "active"
		reason := ""
		if key.Status != "active" {
			reason = "key_disabled"
		} else if !isBound {
			reason = "credential_not_bound"
		}
		groupID := int64(0)
		if key.GroupID != nil {
			groupID = *key.GroupID
		}
		apiKeys = append(apiKeys, keyDTO{ID: key.ID, Name: key.Name, GroupID: groupID, Available: available, UnavailableReason: reason})
		if available && (selected == nil || requestedID == key.ID) {
			value := key.ID
			selected = &value
		}
	}
	models := make([]policy.Model, 0, len(modelPolicy.Models))
	for _, model := range modelPolicy.Models {
		if model.Enabled {
			models = append(models, model)
		}
	}
	server.writeSuccess(writer, http.StatusOK, map[string]any{
		"enabled":             modelPolicy.Enabled,
		"api_keys":            apiKeys,
		"selected_api_key_id": selected,
		"policy_version":      modelPolicy.Version,
		"models":              models,
	})
}

func (server *server) listProjects(writer http.ResponseWriter, request *http.Request) {
	session, _ := authn.FromContext(request.Context())
	limit := parseBoundedInt(request.URL.Query().Get("limit"), 100, 1, 200)
	offset := parseBoundedInt(request.URL.Query().Get("offset"), 0, 0, 1000000)
	items, err := server.dependencies.Projects.List(request.Context(), session.Principal.ExternalUserID, limit, offset)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusOK, map[string]any{"items": items})
}

func (server *server) getProject(writer http.ResponseWriter, request *http.Request) {
	identifier := request.PathValue("project_id")
	if !publicid.Valid(identifier) {
		server.writeError(writer, request, errNotFound)
		return
	}
	session, _ := authn.FromContext(request.Context())
	item, err := server.dependencies.Projects.Get(request.Context(), session.Principal.ExternalUserID, identifier)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusOK, item)
}

type projectWrite struct {
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Document json.RawMessage `json:"document"`
	Version  int64           `json:"version"`
}

func (server *server) createProject(writer http.ResponseWriter, request *http.Request) {
	if !server.requireWrites(writer, request) {
		return
	}
	var input projectWrite
	if err := decodeJSON(writer, request, &input); err != nil {
		server.writeError(writer, request, err)
		return
	}
	if !validProjectName(input.Name) {
		server.writeError(writer, request, errInvalidRequest)
		return
	}
	references, err := project.ValidateDocument(input.Document)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	session, _ := authn.FromContext(request.Context())
	item, err := server.dependencies.Projects.Create(request.Context(), session.Principal.ExternalUserID, input.ID, input.Name, input.Document, references)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusCreated, item)
}

func (server *server) updateProject(writer http.ResponseWriter, request *http.Request) {
	if !server.requireWrites(writer, request) {
		return
	}
	identifier := request.PathValue("project_id")
	if !publicid.Valid(identifier) {
		server.writeError(writer, request, errNotFound)
		return
	}
	var input projectWrite
	if err := decodeJSON(writer, request, &input); err != nil {
		server.writeError(writer, request, err)
		return
	}
	if input.Version <= 0 || !validProjectName(input.Name) {
		server.writeError(writer, request, errInvalidRequest)
		return
	}
	references, err := project.ValidateDocument(input.Document)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	session, _ := authn.FromContext(request.Context())
	item, err := server.dependencies.Projects.Update(request.Context(), session.Principal.ExternalUserID, identifier, input.Version, input.Name, input.Document, references)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusOK, item)
}

func (server *server) deleteProject(writer http.ResponseWriter, request *http.Request) {
	if !server.requireWrites(writer, request) {
		return
	}
	identifier := request.PathValue("project_id")
	if !publicid.Valid(identifier) {
		server.writeError(writer, request, errNotFound)
		return
	}
	session, _ := authn.FromContext(request.Context())
	if err := server.dependencies.Projects.Delete(request.Context(), session.Principal.ExternalUserID, identifier); err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusOK, map[string]bool{"deleted": true})
}

func (server *server) uploadAsset(writer http.ResponseWriter, request *http.Request) {
	if !server.requireWrites(writer, request) {
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, asset.MaxUploadBytes+(1<<20))
	if err := request.ParseMultipartForm(4 << 20); err != nil {
		server.writeError(writer, request, errInvalidRequest)
		return
	}
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}
	file, header, err := request.FormFile("file")
	if err != nil {
		server.writeError(writer, request, errInvalidRequest)
		return
	}
	defer file.Close()
	width := formInteger(request.FormValue("width"), 0)
	height := formInteger(request.FormValue("height"), 0)
	var durationMS *int64
	if value := formInteger64(request.FormValue("duration_ms"), 0); value > 0 {
		durationMS = &value
	}
	session, _ := authn.FromContext(request.Context())
	item, err := server.dependencies.Assets.Create(request.Context(), session.Principal.ExternalUserID, asset.CreateInput{
		ProjectPublicID: request.FormValue("project_id"),
		SourceType:      "upload",
		FileName:        header.Filename,
		DeclaredMIME:    header.Header.Get("Content-Type"),
		Width:           width,
		Height:          height,
		DurationMS:      durationMS,
		Body:            file,
	})
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusCreated, item)
}

func (server *server) downloadAsset(writer http.ResponseWriter, request *http.Request) {
	server.serveAsset(writer, request, request.URL.Query().Get("thumbnail") == "true")
}

func (server *server) downloadAssetThumbnail(writer http.ResponseWriter, request *http.Request) {
	server.serveAsset(writer, request, true)
}

func (server *server) serveAsset(writer http.ResponseWriter, request *http.Request, thumbnail bool) {
	identifier := request.PathValue("asset_id")
	if !publicid.Valid(identifier) {
		server.writeError(writer, request, errNotFound)
		return
	}
	session, _ := authn.FromContext(request.Context())
	item, reader, metadata, err := server.dependencies.Assets.Open(request.Context(), session.Principal.ExternalUserID, identifier, thumbnail)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	defer reader.Close()
	writer.Header().Set("Content-Type", item.MIMEType)
	writer.Header().Set("Cache-Control", "private, max-age=300")
	writer.Header().Set("Content-Disposition", mime.FormatMediaType("inline", map[string]string{"filename": item.FileName}))
	if seeker, ok := reader.(io.ReadSeeker); ok {
		http.ServeContent(writer, request, item.FileName, item.CreatedAt, seeker)
		return
	}
	writer.Header().Set("Content-Length", strconv.FormatInt(metadata.Size, 10))
	writer.WriteHeader(http.StatusOK)
	_, _ = io.Copy(writer, reader)
}

func (server *server) listLibraryItems(writer http.ResponseWriter, request *http.Request) {
	session, _ := authn.FromContext(request.Context())
	items, err := server.dependencies.Libraries.List(request.Context(), session.Principal.ExternalUserID)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusOK, map[string]any{"items": items})
}

func (server *server) createLibraryItem(writer http.ResponseWriter, request *http.Request) {
	if !server.requireWrites(writer, request) {
		return
	}
	var input library.Write
	if err := decodeJSON(writer, request, &input); err != nil {
		server.writeError(writer, request, err)
		return
	}
	session, _ := authn.FromContext(request.Context())
	item, created, err := server.dependencies.Libraries.Create(request.Context(), session.Principal.ExternalUserID, input)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	server.writeSuccess(writer, status, item)
}

func (server *server) updateLibraryItem(writer http.ResponseWriter, request *http.Request) {
	if !server.requireWrites(writer, request) {
		return
	}
	identifier := request.PathValue("item_id")
	if !publicid.Valid(identifier) {
		server.writeError(writer, request, errNotFound)
		return
	}
	var input library.Write
	if err := decodeJSON(writer, request, &input); err != nil {
		server.writeError(writer, request, err)
		return
	}
	session, _ := authn.FromContext(request.Context())
	item, err := server.dependencies.Libraries.Update(request.Context(), session.Principal.ExternalUserID, identifier, input)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusOK, item)
}

func (server *server) deleteLibraryItem(writer http.ResponseWriter, request *http.Request) {
	if !server.requireWrites(writer, request) {
		return
	}
	identifier := request.PathValue("item_id")
	if !publicid.Valid(identifier) {
		server.writeError(writer, request, errNotFound)
		return
	}
	session, _ := authn.FromContext(request.Context())
	if err := server.dependencies.Libraries.Delete(request.Context(), session.Principal.ExternalUserID, identifier); err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusOK, map[string]bool{"deleted": true})
}

func (server *server) createEditorDocument(writer http.ResponseWriter, request *http.Request) {
	if !server.requireWrites(writer, request) {
		return
	}
	var input editor.CreateInput
	if err := decodeJSON(writer, request, &input); err != nil {
		server.writeError(writer, request, err)
		return
	}
	session, _ := authn.FromContext(request.Context())
	document, created, err := server.dependencies.Editors.Create(request.Context(), session.Principal.ExternalUserID, input)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	server.writeSuccess(writer, status, document)
}

func (server *server) getEditorDocument(writer http.ResponseWriter, request *http.Request) {
	identifier := request.PathValue("document_id")
	if !publicid.Valid(identifier) {
		server.writeError(writer, request, errNotFound)
		return
	}
	session, _ := authn.FromContext(request.Context())
	document, err := server.dependencies.Editors.Get(request.Context(), session.Principal.ExternalUserID, identifier)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusOK, document)
}

func (server *server) updateEditorDocument(writer http.ResponseWriter, request *http.Request) {
	if !server.requireWrites(writer, request) {
		return
	}
	identifier := request.PathValue("document_id")
	if !publicid.Valid(identifier) {
		server.writeError(writer, request, errNotFound)
		return
	}
	var input editor.UpdateInput
	if err := decodeJSON(writer, request, &input); err != nil {
		server.writeError(writer, request, err)
		return
	}
	session, _ := authn.FromContext(request.Context())
	document, err := server.dependencies.Editors.Update(request.Context(), session.Principal.ExternalUserID, identifier, input)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusOK, document)
}

func (server *server) uploadEditorAsset(writer http.ResponseWriter, request *http.Request) {
	if !server.requireWrites(writer, request) {
		return
	}
	identifier := request.PathValue("document_id")
	if !publicid.Valid(identifier) {
		server.writeError(writer, request, errNotFound)
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, asset.MaxUploadBytes+(1<<20))
	if err := request.ParseMultipartForm(4 << 20); err != nil {
		server.writeError(writer, request, errInvalidRequest)
		return
	}
	if request.MultipartForm != nil {
		defer request.MultipartForm.RemoveAll()
	}
	file, header, err := request.FormFile("file")
	if err != nil {
		server.writeError(writer, request, errInvalidRequest)
		return
	}
	defer file.Close()
	parentID := strings.TrimSpace(request.FormValue("parent_asset_id"))
	if !publicid.Valid(parentID) {
		server.writeError(writer, request, errInvalidRequest)
		return
	}
	session, _ := authn.FromContext(request.Context())
	item, err := server.dependencies.Editors.CreateDerivedAsset(
		request.Context(), session.Principal.ExternalUserID, identifier, parentID,
		asset.CreateInput{FileName: header.Filename, DeclaredMIME: header.Header.Get("Content-Type"), Body: file},
	)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusCreated, item)
}

func (server *server) createJob(writer http.ResponseWriter, request *http.Request) {
	if !server.requireWrites(writer, request) {
		return
	}
	var input job.CreateInput
	if err := decodeJSON(writer, request, &input); err != nil {
		server.writeError(writer, request, err)
		return
	}
	session, _ := authn.FromContext(request.Context())
	item, created, err := server.dependencies.Jobs.Create(
		request.Context(), session.Principal.ExternalUserID, input, request.Header.Get("Idempotency-Key"),
	)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	server.writeSuccess(writer, status, item)
}

func (server *server) getJob(writer http.ResponseWriter, request *http.Request) {
	identifier := request.PathValue("job_id")
	if !publicid.Valid(identifier) {
		server.writeError(writer, request, errNotFound)
		return
	}
	session, _ := authn.FromContext(request.Context())
	item, err := server.dependencies.Jobs.Get(request.Context(), session.Principal.ExternalUserID, identifier)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusOK, item)
}

func (server *server) cancelJob(writer http.ResponseWriter, request *http.Request) {
	if !server.requireWrites(writer, request) {
		return
	}
	identifier := request.PathValue("job_id")
	if !publicid.Valid(identifier) {
		server.writeError(writer, request, errNotFound)
		return
	}
	session, _ := authn.FromContext(request.Context())
	item, err := server.dependencies.Jobs.Cancel(request.Context(), session.Principal.ExternalUserID, identifier)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusOK, item)
}

func (server *server) streamJobEvents(writer http.ResponseWriter, request *http.Request) {
	identifier := request.PathValue("job_id")
	if !publicid.Valid(identifier) {
		server.writeError(writer, request, errNotFound)
		return
	}
	session, _ := authn.FromContext(request.Context())
	initial, err := server.dependencies.Jobs.Get(request.Context(), session.Principal.ExternalUserID, identifier)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Accel-Buffering", "no")
	writer.WriteHeader(http.StatusOK)
	flusher, _ := writer.(http.Flusher)
	writeEvent := func(item job.Job) bool {
		payload, err := json.Marshal(item)
		if err != nil {
			return false
		}
		event := "progress"
		if terminalJobStatus(item.Status) {
			event = item.Status
		}
		if _, err := fmt.Fprintf(writer, "event: %s\ndata: %s\n\n", event, payload); err != nil {
			return false
		}
		if flusher != nil {
			flusher.Flush()
		}
		return true
	}
	if !writeEvent(initial) || terminalJobStatus(initial.Status) {
		return
	}
	poll := time.NewTicker(time.Second)
	keepalive := time.NewTicker(15 * time.Second)
	defer poll.Stop()
	defer keepalive.Stop()
	lastUpdated := initial.UpdatedAt
	for {
		select {
		case <-request.Context().Done():
			return
		case <-keepalive.C:
			if _, err := io.WriteString(writer, ": keepalive\n\n"); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		case <-poll.C:
			item, err := server.dependencies.Jobs.Get(request.Context(), session.Principal.ExternalUserID, identifier)
			if err != nil {
				return
			}
			if item.UpdatedAt != lastUpdated {
				lastUpdated = item.UpdatedAt
				if !writeEvent(item) {
					return
				}
			}
			if terminalJobStatus(item.Status) {
				return
			}
		}
	}
}

func (server *server) getModelPolicy(writer http.ResponseWriter, request *http.Request) {
	if _, ok := server.requireAdmin(writer, request, false); !ok {
		return
	}
	item, err := server.dependencies.Policies.Get(request.Context())
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusOK, item)
}

func (server *server) updateModelPolicy(writer http.ResponseWriter, request *http.Request) {
	if !server.requireWrites(writer, request) {
		return
	}
	session, ok := server.requireAdmin(writer, request, true)
	if !ok {
		return
	}
	var input struct {
		Version int64          `json:"version"`
		Enabled bool           `json:"enabled"`
		Models  []policy.Model `json:"models"`
	}
	if err := decodeJSON(writer, request, &input); err != nil {
		server.writeError(writer, request, err)
		return
	}
	item, err := server.dependencies.Policies.Update(
		request.Context(), session.Principal.ExternalUserID, logging.RequestID(request.Context()),
		input.Version, input.Enabled, input.Models,
	)
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusOK, item)
}

func (server *server) listModelPolicyAudits(writer http.ResponseWriter, request *http.Request) {
	if _, ok := server.requireAdmin(writer, request, false); !ok {
		return
	}
	items, err := server.dependencies.Policies.ListAudits(request.Context(), parseBoundedInt(request.URL.Query().Get("limit"), 100, 1, 200))
	if err != nil {
		server.writeError(writer, request, err)
		return
	}
	server.writeSuccess(writer, http.StatusOK, map[string]any{"items": items})
}

func (server *server) requireAdmin(writer http.ResponseWriter, request *http.Request, fresh bool) (authn.Session, bool) {
	session, ok := authn.FromContext(request.Context())
	if !ok {
		server.writeError(writer, request, authn.ErrMissingBearer)
		return authn.Session{}, false
	}
	if fresh {
		principal, err := server.dependencies.Authenticator.AuthenticateToken(request.Context(), session.Bearer, true)
		if err != nil {
			server.writeError(writer, request, err)
			return authn.Session{}, false
		}
		session.Principal = principal
	}
	if session.Principal.Role != "admin" {
		server.writeError(writer, request, errForbidden)
		return authn.Session{}, false
	}
	return session, true
}

func (server *server) requireWrites(writer http.ResponseWriter, request *http.Request) bool {
	if server.dependencies.WritesEnabled {
		return true
	}
	server.writeError(writer, request, errWritesDisabled)
	return false
}

func (server *server) requestMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		started := time.Now()
		requestID := strings.TrimSpace(request.Header.Get("X-Request-ID"))
		if requestID == "" || len(requestID) > 128 || strings.ContainsAny(requestID, "\r\n") {
			requestID, _ = publicid.New("req")
		}
		writer.Header().Set("X-Request-ID", requestID)
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("Referrer-Policy", "same-origin")
		recorder := &statusRecorder{ResponseWriter: writer, status: http.StatusOK}
		next.ServeHTTP(recorder, request.WithContext(logging.WithRequestID(request.Context(), requestID)))
		server.dependencies.Logger.Info("canvas request",
			"request_id", requestID,
			"method", request.Method,
			"path", request.URL.Path,
			"status", recorder.status,
			"duration_ms", time.Since(started).Milliseconds(),
			"release", server.dependencies.Release,
		)
	})
}

func (server *server) writeSuccess(writer http.ResponseWriter, status int, data any) {
	writeJSON(writer, status, envelope{Code: 0, Message: "success", Data: data})
}

func (server *server) writeError(writer http.ResponseWriter, request *http.Request, err error) {
	status, reason, message := classifyError(err)
	if errors.Is(err, errWritesDisabled) {
		server.dependencies.Logger.Info("canvas write rejected while read-only",
			"request_id", logging.RequestID(request.Context()), "reason", reason)
	} else if status >= 500 {
		server.dependencies.Logger.Error("canvas request failed",
			"request_id", logging.RequestID(request.Context()), "reason", reason, "error", err)
	}
	writeJSON(writer, status, envelope{Code: status, Message: message, Reason: reason})
}

func classifyError(err error) (int, string, string) {
	switch {
	case errors.Is(err, errInvalidRequest), errors.Is(err, project.ErrInvalidDocument), errors.Is(err, asset.ErrInvalidFile):
		return http.StatusBadRequest, "invalid_request", "The request is invalid."
	case errors.Is(err, errForbidden):
		return http.StatusForbidden, "forbidden", "Access is forbidden."
	case errors.Is(err, errNotFound), errors.Is(err, project.ErrNotFound), errors.Is(err, credential.ErrNotFound), errors.Is(err, asset.ErrNotFound):
		return http.StatusNotFound, "not_found", "The resource was not found."
	case errors.Is(err, project.ErrVersionConflict):
		return http.StatusConflict, "project_version_conflict", "The project changed on the server."
	case errors.Is(err, library.ErrVersionConflict):
		return http.StatusConflict, "library_version_conflict", "The library item changed on the server."
	case errors.Is(err, editor.ErrVersionConflict):
		return http.StatusConflict, "editor_version_conflict", "The editor document changed on the server."
	case errors.Is(err, job.ErrIdempotencyConflict):
		return http.StatusConflict, "idempotency_conflict", "The idempotency key was already used for another request."
	case errors.Is(err, policy.ErrVersionConflict):
		return http.StatusConflict, "policy_version_conflict", "The model policy changed on the server."
	case errors.Is(err, library.ErrClientConflict):
		return http.StatusConflict, "library_client_conflict", "The library client ID is already retired."
	case errors.Is(err, errWritesDisabled):
		return http.StatusServiceUnavailable, "writes_disabled", "Canvas is temporarily read-only."
	case errors.Is(err, errKeyUnavailable):
		return http.StatusConflict, "credential_unavailable", "The API key is not active."
	case errors.Is(err, library.ErrInvalid):
		return http.StatusBadRequest, "invalid_request", "The library item is invalid."
	case errors.Is(err, editor.ErrInvalid):
		return http.StatusBadRequest, "invalid_request", "The editor document is invalid."
	case errors.Is(err, job.ErrInvalid):
		return http.StatusBadRequest, "invalid_request", "The image job is invalid."
	case errors.Is(err, policy.ErrInvalid):
		return http.StatusBadRequest, "invalid_request", "The model policy is invalid."
	case errors.Is(err, library.ErrNotFound):
		return http.StatusNotFound, "not_found", "The resource was not found."
	case errors.Is(err, editor.ErrNotFound):
		return http.StatusNotFound, "not_found", "The resource was not found."
	case errors.Is(err, job.ErrNotFound):
		return http.StatusNotFound, "not_found", "The resource was not found."
	case errors.Is(err, authn.ErrMissingBearer):
		return http.StatusUnauthorized, "unauthenticated", "Sign in is required."
	}
	var contractError *gateway.ContractError
	if !errors.As(err, &contractError) {
		return http.StatusInternalServerError, "internal_error", "An internal error occurred."
	}
	switch contractError.Kind {
	case gateway.ErrorUnauthenticated:
		return http.StatusUnauthorized, "unauthenticated", "The official session is invalid."
	case gateway.ErrorForbidden:
		return http.StatusForbidden, "forbidden", "The official account cannot use Canvas."
	case gateway.ErrorNotFound:
		return http.StatusNotFound, "not_found", "The official resource was not found."
	case gateway.ErrorRateLimited:
		return http.StatusTooManyRequests, "rate_limited", "The official service rate limit was reached."
	case gateway.ErrorInvalidRequest:
		return http.StatusBadRequest, "invalid_request", "The official service rejected the request."
	case gateway.ErrorUpstreamUnavailable, gateway.ErrorInvalidResponse, gateway.ErrorIndeterminate:
		return http.StatusServiceUnavailable, "official_unavailable", "The official service is unavailable."
	default:
		return http.StatusInternalServerError, "internal_error", "An internal error occurred."
	}
}

func decodeJSON(writer http.ResponseWriter, request *http.Request, target any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maxJSONRequest)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errInvalidRequest
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errInvalidRequest
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func credentialDTO(record credential.Record) map[string]any {
	return map[string]any{
		"id": record.PublicID, "external_api_key_id": record.ExternalAPIKeyID,
		"name": record.DisplayName, "key_hint": record.KeyHint, "status": record.Status,
		"last_verified_at": record.LastVerifiedAt, "created_at": record.CreatedAt, "updated_at": record.UpdatedAt,
	}
}

func keyHint(secret []byte) string {
	if len(secret) <= 4 {
		return "****"
	}
	return "..." + string(secret[len(secret)-4:])
}

func validProjectName(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 160 || !utf8.ValidString(value) {
		return false
	}
	for _, char := range value {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func parseBoundedInt(value string, fallback, minimum, maximum int) int {
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < minimum || parsed > maximum {
		return fallback
	}
	return parsed
}

func formInteger(value string, fallback int) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return fallback
	}
	return parsed
}

func formInteger64(value string, fallback int64) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (writer *statusRecorder) WriteHeader(status int) {
	writer.status = status
	writer.ResponseWriter.WriteHeader(status)
}

func (writer *statusRecorder) Flush() {
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (writer *statusRecorder) Unwrap() http.ResponseWriter {
	return writer.ResponseWriter
}

func terminalJobStatus(status string) bool {
	return status == "completed" || status == "partial" || status == "failed" || status == "canceled" || status == "indeterminate" || status == "expired"
}

var (
	errInvalidRequest = errors.New("invalid request")
	errForbidden      = errors.New("forbidden")
	errNotFound       = errors.New("not found")
	errWritesDisabled = errors.New("writes disabled")
	errKeyUnavailable = errors.New("API key unavailable")
)
