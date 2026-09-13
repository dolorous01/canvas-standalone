package transfer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/dolorous01/canvas-standalone/backend/internal/editor"
	"github.com/dolorous01/canvas-standalone/backend/internal/project"
)

var persistedIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func validateDataset(manifest Manifest, content dataset) error {
	if !validSHA256(manifest.ContentSHA256) {
		return errors.New("manifest content digest is invalid")
	}
	counts := datasetCounts(content)
	if !reflect.DeepEqual(counts, manifest.Counts) {
		return errors.New("manifest entity counts do not match export files")
	}
	if manifest.Objects != objectSummary(content.Objects, manifest.Files[objectsFile].SHA256) {
		return errors.New("manifest object summary does not match export files")
	}
	if err := validateResolutions(manifest.Unresolved, manifest.Resolutions); err != nil {
		return err
	}
	for _, warning := range manifest.Warnings {
		if containsSecret(warning) {
			return errors.New("manifest warning contains forbidden secret-like data")
		}
	}

	projects := make(map[string]Project, len(content.Projects))
	for _, item := range content.Projects {
		if !validPersistedID(item.PublicID) || item.ExternalUserID <= 0 || strings.TrimSpace(item.Name) == "" || len(item.Name) > 640 || item.Version <= 0 || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
			return fmt.Errorf("invalid project %q", item.PublicID)
		}
		if _, exists := projects[item.PublicID]; exists {
			return fmt.Errorf("duplicate project public ID %q", item.PublicID)
		}
		if _, err := project.ValidateDocument(item.Document); err != nil {
			return fmt.Errorf("invalid project document %q: %w", item.PublicID, err)
		}
		projects[item.PublicID] = item
	}

	assets := make(map[string]Asset, len(content.Assets))
	objectKeys := make(map[string]struct{}, len(content.Assets)*2)
	for _, item := range content.Assets {
		if !validPersistedID(item.PublicID) || item.ExternalUserID <= 0 || !oneOf(item.SourceType, "upload", "generated", "derived", "legacy") ||
			!oneOf(item.MediaKind, "image", "video", "audio") || !validRelativeKey(item.ObjectKey) || !validSHA256(item.SHA256) ||
			item.ByteSize <= 0 || item.Width < 0 || item.Height < 0 || item.CreatedAt.IsZero() || strings.TrimSpace(item.MIMEType) == "" {
			return fmt.Errorf("invalid asset %q", item.PublicID)
		}
		if item.ThumbnailKey != "" && !validRelativeKey(item.ThumbnailKey) {
			return fmt.Errorf("invalid thumbnail key for asset %q", item.PublicID)
		}
		if item.MediaKind == "audio" && (item.Width != 0 || item.Height != 0) {
			return fmt.Errorf("invalid audio dimensions for asset %q", item.PublicID)
		}
		if item.MediaKind != "audio" && (item.Width <= 0 || item.Height <= 0) {
			return fmt.Errorf("invalid visual dimensions for asset %q", item.PublicID)
		}
		if _, exists := assets[item.PublicID]; exists {
			return fmt.Errorf("duplicate asset public ID %q", item.PublicID)
		}
		if _, exists := objectKeys[item.ObjectKey]; exists {
			return fmt.Errorf("duplicate target object key %q", item.ObjectKey)
		}
		objectKeys[item.ObjectKey] = struct{}{}
		if item.ThumbnailKey != "" {
			if _, exists := objectKeys[item.ThumbnailKey]; exists {
				return fmt.Errorf("duplicate target thumbnail key %q", item.ThumbnailKey)
			}
			objectKeys[item.ThumbnailKey] = struct{}{}
		}
		if item.ProjectPublicID != "" {
			owner, ok := projects[item.ProjectPublicID]
			if !ok || owner.ExternalUserID != item.ExternalUserID {
				return fmt.Errorf("asset %q refers to a missing or cross-owner project", item.PublicID)
			}
		}
		assets[item.PublicID] = item
	}
	for _, item := range content.Projects {
		if item.ThumbnailAssetPublicID == "" {
			continue
		}
		thumbnail, ok := assets[item.ThumbnailAssetPublicID]
		if !ok || thumbnail.ExternalUserID != item.ExternalUserID {
			return fmt.Errorf("project %q has a missing or cross-owner thumbnail", item.PublicID)
		}
	}
	for _, item := range content.Assets {
		for _, parentID := range item.ParentPublicIDs {
			parent, ok := assets[parentID]
			if !ok || parent.ExternalUserID != item.ExternalUserID || parentID == item.PublicID {
				return fmt.Errorf("asset %q has an invalid parent %q", item.PublicID, parentID)
			}
		}
	}

	projectRefs := make(map[string]struct{}, len(content.ProjectAssetRefs))
	for _, item := range content.ProjectAssetRefs {
		owner, projectOK := projects[item.ProjectPublicID]
		assetItem, assetOK := assets[item.AssetPublicID]
		key := item.ProjectPublicID + "\x00" + item.AssetPublicID + "\x00" + item.NodeID
		if !projectOK || !assetOK || owner.ExternalUserID != assetItem.ExternalUserID || strings.TrimSpace(item.NodeID) == "" || len(item.NodeID) > 512 || item.CreatedAt.IsZero() {
			return fmt.Errorf("invalid project asset reference %q", key)
		}
		if _, exists := projectRefs[key]; exists {
			return fmt.Errorf("duplicate project asset reference %q", key)
		}
		projectRefs[key] = struct{}{}
	}

	libraryIDs := make(map[string]struct{}, len(content.LibraryItems))
	libraryClients := make(map[string]struct{}, len(content.LibraryItems))
	for _, item := range content.LibraryItems {
		if !validPersistedID(item.PublicID) || item.ExternalUserID <= 0 || strings.TrimSpace(item.ClientID) == "" || strings.TrimSpace(item.Title) == "" ||
			!oneOf(item.Kind, "text", "image", "video", "audio") || item.Version <= 0 || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
			return fmt.Errorf("invalid library item %q", item.PublicID)
		}
		if (item.Kind == "text") != (item.AssetPublicID == "") {
			return fmt.Errorf("invalid library asset relation %q", item.PublicID)
		}
		if item.AssetPublicID != "" {
			assetItem, ok := assets[item.AssetPublicID]
			if !ok || assetItem.ExternalUserID != item.ExternalUserID || assetItem.MediaKind != item.Kind {
				return fmt.Errorf("library item %q has a missing or cross-owner asset", item.PublicID)
			}
		}
		if err := validateSafeJSON(item.Tags, "array"); err != nil {
			return fmt.Errorf("invalid library tags %q: %w", item.PublicID, err)
		}
		if err := validateSafeJSON(item.Metadata, "object"); err != nil {
			return fmt.Errorf("invalid library metadata %q: %w", item.PublicID, err)
		}
		if containsSecret(item.Content) || containsSecret(item.Note) || containsSecret(item.Source) {
			return fmt.Errorf("library item %q contains secret-like data", item.PublicID)
		}
		if _, exists := libraryIDs[item.PublicID]; exists {
			return fmt.Errorf("duplicate library public ID %q", item.PublicID)
		}
		libraryIDs[item.PublicID] = struct{}{}
		clientKey := fmt.Sprintf("%d\x00%s", item.ExternalUserID, item.ClientID)
		if _, exists := libraryClients[clientKey]; exists {
			return fmt.Errorf("duplicate library client ID %q", clientKey)
		}
		libraryClients[clientKey] = struct{}{}
	}

	documents := make(map[string]EditorDocument, len(content.EditorDocuments))
	activeProjectNodes := make(map[string]struct{})
	for _, item := range content.EditorDocuments {
		owner, projectOK := projects[item.ProjectPublicID]
		base, baseOK := assets[item.BaseAssetPublicID]
		current, currentOK := assets[item.CurrentAssetPublicID]
		if !validPersistedID(item.PublicID) || !projectOK || !baseOK || !currentOK || base.MediaKind != "image" || current.MediaKind != "image" ||
			owner.ExternalUserID != base.ExternalUserID || owner.ExternalUserID != current.ExternalUserID || strings.TrimSpace(item.NodeID) == "" || len(item.NodeID) > 512 ||
			item.Version <= 0 || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
			return fmt.Errorf("invalid editor document %q", item.PublicID)
		}
		if err := editor.ValidateDocument(item.Document); err != nil {
			return fmt.Errorf("invalid editor document JSON %q: %w", item.PublicID, err)
		}
		if _, exists := documents[item.PublicID]; exists {
			return fmt.Errorf("duplicate editor document public ID %q", item.PublicID)
		}
		documents[item.PublicID] = item
		if item.DeletedAt == nil {
			key := item.ProjectPublicID + "\x00" + item.NodeID
			if _, exists := activeProjectNodes[key]; exists {
				return fmt.Errorf("duplicate active editor project/node %q", key)
			}
			activeProjectNodes[key] = struct{}{}
		}
	}
	editorRefs := make(map[string]struct{}, len(content.EditorAssetRefs))
	for _, item := range content.EditorAssetRefs {
		document, documentOK := documents[item.DocumentPublicID]
		assetItem, assetOK := assets[item.AssetPublicID]
		projectItem := projects[document.ProjectPublicID]
		key := item.DocumentPublicID + "\x00" + item.AssetPublicID + "\x00" + item.Role + "\x00" + item.ElementID
		if !documentOK || !assetOK || projectItem.ExternalUserID != assetItem.ExternalUserID || !oneOf(item.Role, "source", "layer", "mask", "result") || strings.TrimSpace(item.ElementID) == "" || item.CreatedAt.IsZero() {
			return fmt.Errorf("invalid editor asset reference %q", key)
		}
		if _, exists := editorRefs[key]; exists {
			return fmt.Errorf("duplicate editor asset reference %q", key)
		}
		editorRefs[key] = struct{}{}
	}
	revisions := make(map[string]struct{}, len(content.EditorRevisions))
	revisionVersions := make(map[string]struct{}, len(content.EditorRevisions))
	for _, item := range content.EditorRevisions {
		document, documentOK := documents[item.DocumentPublicID]
		assetItem, assetOK := assets[item.AssetPublicID]
		projectItem := projects[document.ProjectPublicID]
		if !validPersistedID(item.PublicID) || !documentOK || !assetOK || projectItem.ExternalUserID != assetItem.ExternalUserID || item.Version <= 0 || strings.TrimSpace(item.Operation) == "" || item.CreatedAt.IsZero() {
			return fmt.Errorf("invalid editor revision %q", item.PublicID)
		}
		if _, err := editor.ValidateParameters(item.Parameters); err != nil {
			return fmt.Errorf("invalid editor revision parameters %q: %w", item.PublicID, err)
		}
		if _, exists := revisions[item.PublicID]; exists {
			return fmt.Errorf("duplicate editor revision public ID %q", item.PublicID)
		}
		revisions[item.PublicID] = struct{}{}
		versionKey := fmt.Sprintf("%s\x00%d", item.DocumentPublicID, item.Version)
		if _, exists := revisionVersions[versionKey]; exists {
			return fmt.Errorf("duplicate editor revision version %q", versionKey)
		}
		revisionVersions[versionKey] = struct{}{}
	}

	if len(content.ModelPolicies) > 1 {
		return errors.New("export contains more than one model policy")
	}
	for _, item := range content.ModelPolicies {
		if item.Version <= 0 || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
			return errors.New("invalid model policy")
		}
	}
	policyModels := make(map[string]struct{}, len(content.ModelPolicyItems))
	for index, item := range content.ModelPolicyItems {
		if strings.TrimSpace(item.Model) == "" || item.Position != index || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
			return fmt.Errorf("invalid model policy item %q", item.Model)
		}
		if err := validateCapability(item.Capability); err != nil {
			return fmt.Errorf("invalid capability for %q: %w", item.Model, err)
		}
		key := strings.ToLower(item.Model)
		if _, exists := policyModels[key]; exists {
			return fmt.Errorf("duplicate model policy item %q", item.Model)
		}
		policyModels[key] = struct{}{}
	}
	policyAuditIDs := make(map[int64]struct{}, len(content.PolicyAudits))
	for _, item := range content.PolicyAudits {
		if item.LegacyID <= 0 || item.OperatorExternalUserID <= 0 || strings.TrimSpace(item.RequestID) == "" || item.OldVersion < 0 || item.NewVersion <= item.OldVersion || item.CreatedAt.IsZero() {
			return fmt.Errorf("invalid policy audit %d", item.LegacyID)
		}
		if err := validateSafeJSON(item.Before, "object"); err != nil {
			return fmt.Errorf("invalid policy audit before %d: %w", item.LegacyID, err)
		}
		if err := validateSafeJSON(item.After, "object"); err != nil {
			return fmt.Errorf("invalid policy audit after %d: %w", item.LegacyID, err)
		}
		if _, exists := policyAuditIDs[item.LegacyID]; exists {
			return fmt.Errorf("duplicate policy audit %d", item.LegacyID)
		}
		policyAuditIDs[item.LegacyID] = struct{}{}
	}
	settings := make(map[string]struct{}, len(content.RuntimeSettings))
	for _, item := range content.RuntimeSettings {
		if item.Name != "image_job_runtime_settings" || item.Version <= 0 || item.UpdatedAt.IsZero() {
			return fmt.Errorf("invalid runtime setting %q", item.Name)
		}
		if err := validateSafeJSON(item.Value, "object"); err != nil {
			return fmt.Errorf("invalid runtime setting %q: %w", item.Name, err)
		}
		if _, exists := settings[item.Name]; exists {
			return fmt.Errorf("duplicate runtime setting %q", item.Name)
		}
		settings[item.Name] = struct{}{}
	}

	jobs := make(map[string]Job, len(content.Jobs))
	idempotency := make(map[string]struct{}, len(content.Jobs))
	for _, item := range content.Jobs {
		projectItem, projectOK := projects[item.ProjectPublicID]
		if !validPersistedID(item.PublicID) || !projectOK || projectItem.ExternalUserID != item.ExternalUserID || strings.TrimSpace(item.ClientNodeID) == "" ||
			!oneOf(item.Operation, "generation", "edit") || strings.TrimSpace(item.SelectedModel) == "" || item.PolicyVersion <= 0 ||
			!oneOf(item.Status, "completed", "partial", "failed", "canceled", "indeterminate", "expired") || !oneOf(item.Phase, "preflight", "leased", "validating", "upstream", "saving") ||
			item.RequestedCount <= 0 || item.RequestedCount > 10 || item.CompletedCount < 0 || item.CompletedCount > item.RequestedCount ||
			!validSHA256(item.RequestDigest) || !validSHA256(item.IdempotencyHash) || item.AttemptPosition < 0 || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
			return fmt.Errorf("invalid terminal job %q", item.PublicID)
		}
		if err := validateSafeJSON(item.Request, "object"); err != nil {
			return fmt.Errorf("invalid sanitized job request %q: %w", item.PublicID, err)
		}
		if err := validateSafeJSON(item.AttemptPlan, "array"); err != nil {
			return fmt.Errorf("invalid job attempt plan %q: %w", item.PublicID, err)
		}
		if containsSecret(item.ErrorType) || containsSecret(item.ErrorCode) || containsSecret(item.ErrorMessage) {
			return fmt.Errorf("job %q contains secret-like error data", item.PublicID)
		}
		if _, exists := jobs[item.PublicID]; exists {
			return fmt.Errorf("duplicate job public ID %q", item.PublicID)
		}
		jobs[item.PublicID] = item
		key := fmt.Sprintf("%d\x00%s", item.ExternalUserID, item.IdempotencyHash)
		if _, exists := idempotency[key]; exists {
			return fmt.Errorf("duplicate job idempotency hash for owner %d", item.ExternalUserID)
		}
		idempotency[key] = struct{}{}
	}
	jobInputs := make(map[string]struct{}, len(content.JobInputs))
	for _, item := range content.JobInputs {
		jobItem, jobOK := jobs[item.JobPublicID]
		assetItem, assetOK := assets[item.AssetPublicID]
		key := fmt.Sprintf("%s\x00%s\x00%d", item.JobPublicID, item.Kind, item.Position)
		if !jobOK || !assetOK || jobItem.ExternalUserID != assetItem.ExternalUserID || item.Position < 0 || !oneOf(item.Kind, "image", "mask") || !validSHA256(item.SHA256) || !strings.EqualFold(item.SHA256, assetItem.SHA256) || item.CreatedAt.IsZero() {
			return fmt.Errorf("invalid job input %q", key)
		}
		if _, exists := jobInputs[key]; exists {
			return fmt.Errorf("duplicate job input %q", key)
		}
		jobInputs[key] = struct{}{}
	}
	jobResults := make(map[string]struct{}, len(content.JobResults))
	for _, item := range content.JobResults {
		jobItem, jobOK := jobs[item.JobPublicID]
		if !jobOK || item.Position < 0 || strings.TrimSpace(item.Status) == "" || item.CreatedAt.IsZero() {
			return fmt.Errorf("invalid job result for %q", item.JobPublicID)
		}
		if item.AssetPublicID != "" {
			assetItem, ok := assets[item.AssetPublicID]
			if !ok || jobItem.ExternalUserID != assetItem.ExternalUserID {
				return fmt.Errorf("job result for %q has a missing or cross-owner asset", item.JobPublicID)
			}
		}
		key := fmt.Sprintf("%s\x00%d", item.JobPublicID, item.Position)
		if _, exists := jobResults[key]; exists {
			return fmt.Errorf("duplicate job result %q", key)
		}
		jobResults[key] = struct{}{}
	}
	legacyTasks := make(map[string]struct{}, len(content.LegacyMediaTasks))
	for _, item := range content.LegacyMediaTasks {
		projectItem, projectOK := projects[item.ProjectPublicID]
		if !validPersistedID(item.PublicID) || !projectOK || projectItem.ExternalUserID != item.ExternalUserID || !oneOf(item.MediaKind, "video", "audio") ||
			!oneOf(item.Status, "completed", "partial", "failed", "canceled", "indeterminate", "expired") || strings.TrimSpace(item.Phase) == "" ||
			strings.TrimSpace(item.ClientNodeID) == "" || strings.TrimSpace(item.SelectedModel) == "" || !validSHA256(item.RequestDigest) || item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
			return fmt.Errorf("invalid legacy media task %q", item.PublicID)
		}
		if err := validateSafeJSON(item.Request, "object"); err != nil {
			return fmt.Errorf("invalid sanitized media request %q: %w", item.PublicID, err)
		}
		if len(item.Error) > 0 {
			if err := validateSafeJSON(item.Error, "object"); err != nil {
				return fmt.Errorf("invalid media error %q: %w", item.PublicID, err)
			}
		}
		if item.ResultAssetPublicID != "" {
			assetItem, ok := assets[item.ResultAssetPublicID]
			if !ok || assetItem.ExternalUserID != item.ExternalUserID {
				return fmt.Errorf("legacy media task %q has a missing or cross-owner result", item.PublicID)
			}
		}
		if _, exists := legacyTasks[item.PublicID]; exists {
			return fmt.Errorf("duplicate legacy media task %q", item.PublicID)
		}
		legacyTasks[item.PublicID] = struct{}{}
	}

	objects := make(map[string]Object, len(content.Objects))
	originals := make(map[string]int, len(content.Assets))
	thumbnails := make(map[string]int, len(content.Assets))
	for _, item := range content.Objects {
		assetItem, ok := assets[item.AssetPublicID]
		if !ok || !oneOf(item.Kind, "original", "thumbnail") || !validRelativeKey(item.SourceKey) || !validRelativeKey(item.TargetKey) || item.Size <= 0 || !validSHA256(item.SHA256) || strings.TrimSpace(item.MIMEType) == "" {
			return fmt.Errorf("invalid object %q", item.TargetKey)
		}
		if _, exists := objects[item.TargetKey]; exists {
			return fmt.Errorf("duplicate object target key %q", item.TargetKey)
		}
		objects[item.TargetKey] = item
		if item.Kind == "original" {
			originals[item.AssetPublicID]++
			if item.TargetKey != assetItem.ObjectKey || item.Size != assetItem.ByteSize || !strings.EqualFold(item.SHA256, assetItem.SHA256) || item.MIMEType != assetItem.MIMEType {
				return fmt.Errorf("asset metadata and original object differ for %q", item.AssetPublicID)
			}
		} else {
			thumbnails[item.AssetPublicID]++
			if item.TargetKey != assetItem.ThumbnailKey {
				return fmt.Errorf("asset metadata and thumbnail object differ for %q", item.AssetPublicID)
			}
		}
	}
	for _, item := range content.Assets {
		if originals[item.PublicID] != 1 {
			return fmt.Errorf("asset %q does not have exactly one original object", item.PublicID)
		}
		wantThumbnail := 0
		if item.ThumbnailKey != "" {
			wantThumbnail = 1
		}
		if thumbnails[item.PublicID] != wantThumbnail {
			return fmt.Errorf("asset %q thumbnail object count mismatch", item.PublicID)
		}
	}

	statusCounts := datasetStatusCounts(content)
	if !reflect.DeepEqual(statusCounts, manifest.StatusCounts) {
		return errors.New("manifest status counts do not match export files")
	}
	return nil
}

func validateResolutions(unresolved []UnresolvedItem, decisions []ResolutionDecision) error {
	needed := make(map[string]UnresolvedItem, len(unresolved))
	for _, item := range unresolved {
		if !oneOf(item.Kind, "image_job", "legacy_media_task") || !validPersistedID(item.PublicID) || strings.TrimSpace(item.Status) == "" || strings.TrimSpace(item.Phase) == "" {
			return fmt.Errorf("invalid unresolved item %q", item.PublicID)
		}
		key := item.Kind + "\x00" + item.PublicID
		if _, exists := needed[key]; exists {
			return fmt.Errorf("duplicate unresolved item %q", item.PublicID)
		}
		needed[key] = item
	}
	seen := make(map[string]struct{}, len(decisions))
	for _, decision := range decisions {
		key := decision.Kind + "\x00" + decision.PublicID
		if _, ok := needed[key]; !ok {
			return fmt.Errorf("resolution has no matching unresolved item: %s/%s", decision.Kind, decision.PublicID)
		}
		if decision.Action != ResolutionImportAsIndeterminate || strings.TrimSpace(decision.Reason) == "" || containsSecret(decision.Reason) {
			return fmt.Errorf("invalid resolution for %s/%s", decision.Kind, decision.PublicID)
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate resolution for %s/%s", decision.Kind, decision.PublicID)
		}
		seen[key] = struct{}{}
	}
	if len(seen) != len(needed) {
		return errors.New("every unresolved source item requires an explicit resolution")
	}
	return nil
}

func validateCapability(raw json.RawMessage) error {
	if err := validateSafeJSON(raw, "object"); err != nil {
		return err
	}
	var capability struct {
		MediaKind      string `json:"media_kind"`
		Generation     bool   `json:"generation"`
		Edit           bool   `json:"edit"`
		MaxInputImages int    `json:"max_input_images"`
		MaxOutputs     int    `json:"max_outputs"`
	}
	if err := json.Unmarshal(raw, &capability); err != nil || capability.MediaKind != "image" || (!capability.Generation && !capability.Edit) || capability.MaxInputImages < 0 || capability.MaxInputImages > 32 || capability.MaxOutputs <= 0 || capability.MaxOutputs > 10 {
		return errors.New("unsupported image capability")
	}
	return nil
}

func validateSafeJSON(raw json.RawMessage, expected string) error {
	if len(raw) == 0 || len(raw) > maxNDJSONLineBytes {
		return errors.New("JSON size is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("multiple JSON values")
	}
	if expected == "object" {
		if _, ok := value.(map[string]any); !ok {
			return errors.New("JSON must be an object")
		}
	}
	if expected == "array" {
		if _, ok := value.([]any); !ok {
			return errors.New("JSON must be an array")
		}
	}
	var walk func(any, int) error
	walk = func(current any, depth int) error {
		if depth > 64 {
			return errors.New("JSON nesting is too deep")
		}
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
				if strings.Contains(normalized, "token") || strings.Contains(normalized, "api_key") || normalized == "authorization" || normalized == "object_key" || normalized == "provider_url" || normalized == "signed_url" || normalized == "password" || normalized == "secret" {
					return fmt.Errorf("forbidden JSON key %q", key)
				}
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		case []any:
			for _, child := range typed {
				if err := walk(child, depth+1); err != nil {
					return err
				}
			}
		case string:
			if containsSecret(typed) {
				return errors.New("forbidden secret-like JSON value")
			}
		}
		return nil
	}
	return walk(value, 0)
}

func containsSecret(value string) bool {
	normalized := strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(normalized, "bearer ") || strings.HasPrefix(normalized, "sk-") ||
		strings.HasPrefix(normalized, "postgres://") || strings.HasPrefix(normalized, "postgresql://") ||
		strings.HasPrefix(normalized, "data:") || strings.HasPrefix(normalized, "javascript:") ||
		strings.Contains(normalized, "private key-----") || strings.Contains(normalized, "<script")
}

func normalizeJSON(raw []byte, expected string) (json.RawMessage, error) {
	if err := validateSafeJSON(raw, expected); err != nil {
		return nil, err
	}
	buffer := bytes.NewBuffer(nil)
	if err := json.Compact(buffer, raw); err != nil {
		return nil, err
	}
	return json.RawMessage(append([]byte(nil), buffer.Bytes()...)), nil
}

func validRelativeKey(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\\\r\n\t\x00") || strings.HasPrefix(value, "/") {
		return false
	}
	cleaned := path.Clean(value)
	return cleaned == value && cleaned != "." && cleaned != ".." && !strings.HasPrefix(cleaned, "../")
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && value == strings.ToLower(value)
}

func validPersistedID(value string) bool {
	return persistedIDPattern.MatchString(value)
}

func oneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}

func datasetStatusCounts(content dataset) map[string]int {
	result := make(map[string]int)
	for _, item := range content.Jobs {
		result["image_job:"+item.Status]++
	}
	for _, item := range content.LegacyMediaTasks {
		result["legacy_media_task:"+item.Status]++
	}
	return result
}

func sortedUnique(values []string) ([]string, error) {
	result := append([]string(nil), values...)
	sort.Strings(result)
	for index, value := range result {
		if index > 0 && value == result[index-1] {
			return nil, fmt.Errorf("duplicate value %q", value)
		}
	}
	return result, nil
}
