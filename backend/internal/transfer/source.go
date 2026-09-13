package transfer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var requiredSourceMigrations = []string{
	"159_image_jobs.sql",
	"160_image_canvas.sql",
	"161_canvas_media.sql",
	"162_canvas_media_tasks.sql",
	"163_hybrid_image_editor.sql",
	"164_image_canvas_library.sql",
}

type ExportOptions struct {
	ExporterVersion string
	ResolutionPath  string
}

type sourceSnapshot struct {
	GeneratedAt      time.Time
	Migrations       []SourceMigration
	Content          dataset
	StatusCounts     map[string]int
	Unresolved       []UnresolvedItem
	Warnings         []string
	ReferencedSource map[string]struct{}
}

type legacyAsset struct {
	ID                 int64
	Asset              Asset
	SourceObjectKey    string
	SourceThumbnailKey string
	ParentValues       json.RawMessage
}

func VerifySourceReadOnlyRole(ctx context.Context, db *sql.DB) error {
	var canWrite bool
	err := db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM unnest(ARRAY[
				'image_canvas_projects','image_assets','image_canvas_asset_references',
				'image_canvas_library_items','image_editor_documents','image_editor_asset_references',
				'image_editor_revisions','image_jobs','image_job_inputs','image_job_results',
				'canvas_media_tasks','image_model_policies','image_model_policy_items',
				'image_model_policy_audits','settings'
			]::TEXT[]) AS table_name
			WHERE has_table_privilege(current_user, table_name, 'INSERT')
				OR has_table_privilege(current_user, table_name, 'UPDATE')
				OR has_table_privilege(current_user, table_name, 'DELETE')
		)`).Scan(&canWrite)
	if err != nil {
		return fmt.Errorf("verify source database role: %w", err)
	}
	if canWrite {
		return errors.New("source database role has write privileges; use a dedicated read-only migration role")
	}
	return nil
}

func AuditSource(ctx context.Context, db *sql.DB, objectRoot string) (AuditReport, error) {
	snapshot, err := loadSourceSnapshot(ctx, db, objectRoot)
	if err != nil {
		return AuditReport{}, err
	}
	decisions := make([]ResolutionDecision, 0, len(snapshot.Unresolved))
	for _, item := range snapshot.Unresolved {
		decisions = append(decisions, ResolutionDecision{
			Kind: item.Kind, PublicID: item.PublicID,
			Action: ResolutionImportAsIndeterminate, Reason: "audit-only validation",
		})
	}
	objectDigest, err := digestJSONLines(snapshot.Content.Objects)
	if err != nil {
		return AuditReport{}, err
	}
	validationManifest := Manifest{
		ContentSHA256: strings.Repeat("0", 64), Counts: datasetCounts(snapshot.Content),
		StatusCounts: snapshot.StatusCounts, Unresolved: snapshot.Unresolved, Resolutions: decisions,
		Files:    map[string]FileSummary{objectsFile: {SHA256: objectDigest}},
		Objects:  objectSummary(snapshot.Content.Objects, objectDigest),
		Warnings: snapshot.Warnings,
	}
	if err := validateDataset(validationManifest, snapshot.Content); err != nil {
		return AuditReport{}, fmt.Errorf("validate source snapshot: %w", err)
	}
	return AuditReport{
		Format: AuditFormat, GeneratedAt: snapshot.GeneratedAt, SourceMigrations: snapshot.Migrations,
		Counts: datasetCounts(snapshot.Content), StatusCounts: snapshot.StatusCounts,
		Unresolved: snapshot.Unresolved, Objects: validationManifest.Objects, Warnings: snapshot.Warnings,
	}, nil
}

func Export(ctx context.Context, db *sql.DB, objectRoot, output string, options ExportOptions) (Manifest, error) {
	snapshot, err := loadSourceSnapshot(ctx, db, objectRoot)
	if err != nil {
		return Manifest{}, err
	}
	decisions, err := loadResolutions(options.ResolutionPath, snapshot.Unresolved)
	if err != nil {
		return Manifest{}, err
	}
	exporterVersion := strings.TrimSpace(options.ExporterVersion)
	if exporterVersion == "" || len(exporterVersion) > 128 || containsSecret(exporterVersion) {
		return Manifest{}, errors.New("exporter version is required and must be non-secret")
	}
	manifest := Manifest{
		Format: Format, ExporterVersion: exporterVersion, GeneratedAt: snapshot.GeneratedAt,
		SourceMigrations: snapshot.Migrations, StatusCounts: snapshot.StatusCounts,
		Unresolved: snapshot.Unresolved, Resolutions: decisions, Warnings: snapshot.Warnings,
	}
	return writeExport(output, manifest, snapshot.Content)
}

func loadSourceSnapshot(ctx context.Context, db *sql.DB, objectRoot string) (sourceSnapshot, error) {
	objectRoot, err := validateObjectRoot(objectRoot, false, "source object root")
	if err != nil {
		return sourceSnapshot{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return sourceSnapshot{}, fmt.Errorf("begin read-only source snapshot: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SET LOCAL statement_timeout = '90s'`); err != nil {
		return sourceSnapshot{}, fmt.Errorf("bound source statements: %w", err)
	}
	var snapshot sourceSnapshot
	snapshot.ReferencedSource = make(map[string]struct{})
	if err := tx.QueryRowContext(ctx, `SELECT transaction_timestamp()`).Scan(&snapshot.GeneratedAt); err != nil {
		return sourceSnapshot{}, fmt.Errorf("read source snapshot time: %w", err)
	}
	if snapshot.Migrations, err = readSourceMigrations(ctx, tx); err != nil {
		return sourceSnapshot{}, err
	}
	projects, err := readSourceProjects(ctx, tx)
	if err != nil {
		return sourceSnapshot{}, err
	}
	snapshot.Content.Projects = projects
	assets, legacyAssets, err := readSourceAssets(ctx, tx)
	if err != nil {
		return sourceSnapshot{}, err
	}
	snapshot.Content.Assets = assets
	if err := resolveAssetParents(snapshot.Content.Assets, legacyAssets); err != nil {
		return sourceSnapshot{}, err
	}
	if snapshot.Content.ProjectAssetRefs, err = readSourceProjectRefs(ctx, tx); err != nil {
		return sourceSnapshot{}, err
	}
	if snapshot.Content.LibraryItems, err = readSourceLibrary(ctx, tx); err != nil {
		return sourceSnapshot{}, err
	}
	if snapshot.Content.EditorDocuments, err = readSourceEditorDocuments(ctx, tx); err != nil {
		return sourceSnapshot{}, err
	}
	if snapshot.Content.EditorAssetRefs, err = readSourceEditorRefs(ctx, tx); err != nil {
		return sourceSnapshot{}, err
	}
	if snapshot.Content.EditorRevisions, err = readSourceEditorRevisions(ctx, tx); err != nil {
		return sourceSnapshot{}, err
	}
	if snapshot.Content.ModelPolicies, err = readSourcePolicies(ctx, tx, &snapshot.Warnings); err != nil {
		return sourceSnapshot{}, err
	}
	if snapshot.Content.ModelPolicyItems, err = readSourcePolicyItems(ctx, tx, &snapshot.Warnings); err != nil {
		return sourceSnapshot{}, err
	}
	if len(snapshot.Content.ModelPolicyItems) == 0 && len(snapshot.Content.ModelPolicies) == 1 && snapshot.Content.ModelPolicies[0].Enabled {
		snapshot.Content.ModelPolicies[0].Enabled = false
		snapshot.Warnings = append(snapshot.Warnings, "source policy was disabled because no supported standalone image model remained")
	}
	if snapshot.Content.PolicyAudits, err = readSourcePolicyAudits(ctx, tx); err != nil {
		return sourceSnapshot{}, err
	}
	if snapshot.Content.RuntimeSettings, err = readSourceRuntimeSettings(ctx, tx); err != nil {
		return sourceSnapshot{}, err
	}
	if snapshot.Content.Jobs, snapshot.Unresolved, err = readSourceJobs(ctx, tx, snapshot.GeneratedAt, snapshot.Content.ModelPolicies, &snapshot.Warnings); err != nil {
		return sourceSnapshot{}, err
	}
	if snapshot.Content.JobInputs, err = readSourceJobInputs(ctx, tx); err != nil {
		return sourceSnapshot{}, err
	}
	if snapshot.Content.JobResults, err = readSourceJobResults(ctx, tx); err != nil {
		return sourceSnapshot{}, err
	}
	var mediaUnresolved []UnresolvedItem
	if snapshot.Content.LegacyMediaTasks, mediaUnresolved, err = readSourceMediaTasks(ctx, tx, snapshot.GeneratedAt); err != nil {
		return sourceSnapshot{}, err
	}
	snapshot.Unresolved = append(snapshot.Unresolved, mediaUnresolved...)
	if snapshot.Content.Objects, err = readSourceObjects(objectRoot, legacyAssets, snapshot.ReferencedSource); err != nil {
		return sourceSnapshot{}, err
	}
	orphans, walkErr := countUnreferencedFiles(objectRoot, snapshot.ReferencedSource)
	if walkErr != nil {
		snapshot.Warnings = append(snapshot.Warnings, "unreferenced source object scan was incomplete")
	} else if orphans > 0 {
		snapshot.Warnings = append(snapshot.Warnings, fmt.Sprintf("%d unreferenced source object files were intentionally not exported", orphans))
	}
	snapshot.GeneratedAt = snapshot.GeneratedAt.UTC()
	if err := normalizeDataset(&snapshot.Content); err != nil {
		return sourceSnapshot{}, fmt.Errorf("normalize source snapshot: %w", err)
	}
	snapshot.StatusCounts = datasetStatusCounts(snapshot.Content)
	sort.Slice(snapshot.Unresolved, func(left, right int) bool {
		if snapshot.Unresolved[left].Kind != snapshot.Unresolved[right].Kind {
			return snapshot.Unresolved[left].Kind < snapshot.Unresolved[right].Kind
		}
		return snapshot.Unresolved[left].PublicID < snapshot.Unresolved[right].PublicID
	})
	sort.Strings(snapshot.Warnings)
	if err := tx.Commit(); err != nil {
		return sourceSnapshot{}, fmt.Errorf("finish read-only source snapshot: %w", err)
	}
	return snapshot, nil
}

func readSourceMigrations(ctx context.Context, tx *sql.Tx) ([]SourceMigration, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT filename, checksum FROM schema_migrations
		WHERE filename = ANY($1::TEXT[]) ORDER BY filename`, requiredSourceMigrations)
	if err != nil {
		return nil, fmt.Errorf("read source Canvas migrations: %w", err)
	}
	defer rows.Close()
	result := make([]SourceMigration, 0, len(requiredSourceMigrations))
	for rows.Next() {
		var item SourceMigration
		if err := rows.Scan(&item.Filename, &item.Checksum); err != nil {
			return nil, err
		}
		item.Checksum = strings.ToLower(strings.TrimSpace(item.Checksum))
		if !validSHA256(item.Checksum) {
			return nil, fmt.Errorf("invalid source migration checksum for %s", item.Filename)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) != len(requiredSourceMigrations) {
		return nil, errors.New("source database does not contain the complete Canvas migration set 159-164")
	}
	for index, name := range requiredSourceMigrations {
		if result[index].Filename != name {
			return nil, fmt.Errorf("source Canvas migration is missing: %s", name)
		}
	}
	return result, nil
}

func readSourceProjects(ctx context.Context, tx *sql.Tx) ([]Project, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT project.public_id, project.user_id, project.name, project.document, project.version,
			COALESCE(thumbnail.public_id, ''), project.created_at, project.updated_at, project.deleted_at
		FROM image_canvas_projects project
		LEFT JOIN image_assets thumbnail ON thumbnail.id = project.thumbnail_asset_id
		ORDER BY project.public_id`)
	if err != nil {
		return nil, fmt.Errorf("read source projects: %w", err)
	}
	defer rows.Close()
	result := make([]Project, 0)
	for rows.Next() {
		var item Project
		var raw []byte
		if err := rows.Scan(&item.PublicID, &item.ExternalUserID, &item.Name, &raw, &item.Version, &item.ThumbnailAssetPublicID, &item.CreatedAt, &item.UpdatedAt, &item.DeletedAt); err != nil {
			return nil, err
		}
		item.Document, err = normalizeJSON(raw, "object")
		if err != nil {
			return nil, fmt.Errorf("sanitize source project %s: %w", item.PublicID, err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func readSourceAssets(ctx context.Context, tx *sql.Tx) ([]Asset, []legacyAsset, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT asset.id, asset.public_id, asset.owner_user_id, COALESCE(project.public_id, ''),
			asset.source_type, asset.media_kind, asset.object_key, COALESCE(asset.thumbnail_object_key, ''),
			asset.file_name, asset.mime_type, asset.width, asset.height, asset.duration_ms,
			asset.byte_size, asset.sha256, asset.parent_asset_ids, asset.created_at, asset.deleted_at
		FROM image_assets asset
		LEFT JOIN image_canvas_projects project ON project.id = asset.project_id
		ORDER BY asset.public_id`)
	if err != nil {
		return nil, nil, fmt.Errorf("read source assets: %w", err)
	}
	defer rows.Close()
	result := make([]Asset, 0)
	legacy := make([]legacyAsset, 0)
	for rows.Next() {
		var source legacyAsset
		var duration sql.NullInt64
		var parentValues []byte
		if err := rows.Scan(&source.ID, &source.Asset.PublicID, &source.Asset.ExternalUserID, &source.Asset.ProjectPublicID,
			&source.Asset.SourceType, &source.Asset.MediaKind, &source.SourceObjectKey, &source.SourceThumbnailKey,
			&source.Asset.FileName, &source.Asset.MIMEType, &source.Asset.Width, &source.Asset.Height, &duration,
			&source.Asset.ByteSize, &source.Asset.SHA256, &parentValues, &source.Asset.CreatedAt, &source.Asset.DeletedAt); err != nil {
			return nil, nil, err
		}
		if duration.Valid && duration.Int64 > 0 {
			value := duration.Int64
			source.Asset.DurationMS = &value
		}
		source.Asset.SHA256 = strings.ToLower(strings.TrimSpace(source.Asset.SHA256))
		base := "users/" + strconv.FormatInt(source.Asset.ExternalUserID, 10) + "/assets/" + source.Asset.PublicID
		source.Asset.ObjectKey = base + "/original"
		if source.SourceThumbnailKey != "" {
			source.Asset.ThumbnailKey = base + "/thumbnail"
		}
		source.ParentValues = append(json.RawMessage(nil), parentValues...)
		result = append(result, source.Asset)
		legacy = append(legacy, source)
	}
	return result, legacy, rows.Err()
}

func resolveAssetParents(assets []Asset, legacy []legacyAsset) error {
	byNumericID := make(map[int64]string, len(legacy))
	byPublicID := make(map[string]struct{}, len(legacy))
	for _, item := range legacy {
		byNumericID[item.ID] = item.Asset.PublicID
		byPublicID[item.Asset.PublicID] = struct{}{}
	}
	for index := range legacy {
		decoder := json.NewDecoder(strings.NewReader(string(legacy[index].ParentValues)))
		decoder.UseNumber()
		var raw []any
		if err := decoder.Decode(&raw); err != nil {
			return fmt.Errorf("decode parent assets for %s: %w", legacy[index].Asset.PublicID, err)
		}
		parents := make([]string, 0, len(raw))
		for _, value := range raw {
			var publicID string
			switch typed := value.(type) {
			case json.Number:
				numericID, err := typed.Int64()
				if err != nil {
					return fmt.Errorf("invalid parent asset ID for %s", legacy[index].Asset.PublicID)
				}
				publicID = byNumericID[numericID]
			case string:
				if _, ok := byPublicID[typed]; ok {
					publicID = typed
				}
			}
			if publicID == "" {
				return fmt.Errorf("asset %s refers to an unknown parent", legacy[index].Asset.PublicID)
			}
			parents = append(parents, publicID)
		}
		parents, err := sortedUnique(parents)
		if err != nil {
			return fmt.Errorf("asset %s parent list: %w", legacy[index].Asset.PublicID, err)
		}
		assets[index].ParentPublicIDs = parents
	}
	return nil
}

func readSourceProjectRefs(ctx context.Context, tx *sql.Tx) ([]ProjectAssetRef, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT project.public_id, asset.public_id, reference.node_id, reference.created_at
		FROM image_canvas_asset_references reference
		JOIN image_canvas_projects project ON project.id = reference.project_id
		JOIN image_assets asset ON asset.id = reference.asset_id
		ORDER BY project.public_id, asset.public_id, reference.node_id`)
	if err != nil {
		return nil, fmt.Errorf("read source project references: %w", err)
	}
	defer rows.Close()
	result := make([]ProjectAssetRef, 0)
	for rows.Next() {
		var item ProjectAssetRef
		if err := rows.Scan(&item.ProjectPublicID, &item.AssetPublicID, &item.NodeID, &item.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func readSourceLibrary(ctx context.Context, tx *sql.Tx) ([]LibraryItem, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT item.public_id, item.client_id, item.user_id, item.kind, COALESCE(asset.public_id, ''),
			item.title, item.content, item.tags, item.source, item.note, item.metadata, item.version,
			item.created_at, item.updated_at, item.deleted_at
		FROM image_canvas_library_items item LEFT JOIN image_assets asset ON asset.id = item.asset_id
		ORDER BY item.public_id`)
	if err != nil {
		return nil, fmt.Errorf("read source library: %w", err)
	}
	defer rows.Close()
	result := make([]LibraryItem, 0)
	for rows.Next() {
		var item LibraryItem
		var tags, metadata []byte
		if err := rows.Scan(&item.PublicID, &item.ClientID, &item.ExternalUserID, &item.Kind, &item.AssetPublicID,
			&item.Title, &item.Content, &tags, &item.Source, &item.Note, &metadata, &item.Version,
			&item.CreatedAt, &item.UpdatedAt, &item.DeletedAt); err != nil {
			return nil, err
		}
		item.Tags, err = normalizeJSON(tags, "array")
		if err != nil {
			return nil, fmt.Errorf("sanitize library tags %s: %w", item.PublicID, err)
		}
		item.Metadata, err = normalizeJSON(metadata, "object")
		if err != nil {
			return nil, fmt.Errorf("sanitize library metadata %s: %w", item.PublicID, err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func readSourceEditorDocuments(ctx context.Context, tx *sql.Tx) ([]EditorDocument, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT document.public_id, project.public_id, document.node_id, base.public_id, current.public_id,
			document.document, document.version, document.created_at, document.updated_at, document.deleted_at
		FROM image_editor_documents document
		JOIN image_canvas_projects project ON project.id = document.project_id
		JOIN image_assets base ON base.id = document.base_asset_id
		JOIN image_assets current ON current.id = document.current_asset_id
		ORDER BY document.public_id`)
	if err != nil {
		return nil, fmt.Errorf("read source editor documents: %w", err)
	}
	defer rows.Close()
	result := make([]EditorDocument, 0)
	for rows.Next() {
		var item EditorDocument
		var raw []byte
		if err := rows.Scan(&item.PublicID, &item.ProjectPublicID, &item.NodeID, &item.BaseAssetPublicID, &item.CurrentAssetPublicID,
			&raw, &item.Version, &item.CreatedAt, &item.UpdatedAt, &item.DeletedAt); err != nil {
			return nil, err
		}
		item.Document, err = normalizeJSON(raw, "object")
		if err != nil {
			return nil, fmt.Errorf("sanitize editor document %s: %w", item.PublicID, err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func readSourceEditorRefs(ctx context.Context, tx *sql.Tx) ([]EditorAssetRef, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT document.public_id, asset.public_id, reference.role, reference.element_id, reference.created_at
		FROM image_editor_asset_references reference
		JOIN image_editor_documents document ON document.id = reference.document_id
		JOIN image_assets asset ON asset.id = reference.asset_id
		ORDER BY document.public_id, asset.public_id, reference.role, reference.element_id`)
	if err != nil {
		return nil, fmt.Errorf("read source editor references: %w", err)
	}
	defer rows.Close()
	result := make([]EditorAssetRef, 0)
	for rows.Next() {
		var item EditorAssetRef
		if err := rows.Scan(&item.DocumentPublicID, &item.AssetPublicID, &item.Role, &item.ElementID, &item.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func readSourceEditorRevisions(ctx context.Context, tx *sql.Tx) ([]EditorRevision, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT revision.public_id, document.public_id, revision.version, asset.public_id,
			revision.operation, revision.parameters, revision.created_at
		FROM image_editor_revisions revision
		JOIN image_editor_documents document ON document.id = revision.document_id
		JOIN image_assets asset ON asset.id = revision.asset_id
		ORDER BY revision.public_id`)
	if err != nil {
		return nil, fmt.Errorf("read source editor revisions: %w", err)
	}
	defer rows.Close()
	result := make([]EditorRevision, 0)
	for rows.Next() {
		var item EditorRevision
		var raw []byte
		if err := rows.Scan(&item.PublicID, &item.DocumentPublicID, &item.Version, &item.AssetPublicID, &item.Operation, &raw, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.Parameters, err = normalizeJSON(raw, "object")
		if err != nil {
			return nil, fmt.Errorf("sanitize editor revision %s: %w", item.PublicID, err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func readSourcePolicies(ctx context.Context, tx *sql.Tx, warnings *[]string) ([]ModelPolicy, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT version, enabled, created_at, updated_at
		FROM image_model_policies WHERE id = 1`)
	if err != nil {
		return nil, fmt.Errorf("read source model policy: %w", err)
	}
	defer rows.Close()
	result := make([]ModelPolicy, 0, 1)
	for rows.Next() {
		var item ModelPolicy
		if err := rows.Scan(&item.Version, &item.Enabled, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if item.Version <= 0 {
			item.Version = 1
			*warnings = append(*warnings, "source model policy version zero was mapped to version one")
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func readSourcePolicyItems(ctx context.Context, tx *sql.Tx, warnings *[]string) ([]ModelPolicyItem, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT model, enabled, position, created_at, updated_at
		FROM image_model_policy_items WHERE policy_id = 1 ORDER BY position, model`)
	if err != nil {
		return nil, fmt.Errorf("read source model policy items: %w", err)
	}
	defer rows.Close()
	result := make([]ModelPolicyItem, 0)
	for rows.Next() {
		var model string
		var enabled bool
		var sourcePosition int
		var createdAt, updatedAt time.Time
		if err := rows.Scan(&model, &enabled, &sourcePosition, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		capability, ok := legacyImageCapability(model)
		if !ok {
			*warnings = append(*warnings, fmt.Sprintf("source policy model %s is not supported by the standalone image worker and was not activated", model))
			continue
		}
		result = append(result, ModelPolicyItem{
			Model: model, Enabled: enabled, Position: len(result), Capability: capability,
			CreatedAt: createdAt, UpdatedAt: updatedAt,
		})
	}
	return result, rows.Err()
}

func readSourcePolicyAudits(ctx context.Context, tx *sql.Tx) ([]PolicyAudit, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, operator_user_id, old_version, new_version, before_value, after_value, created_at
		FROM image_model_policy_audits ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("read source policy audits: %w", err)
	}
	defer rows.Close()
	result := make([]PolicyAudit, 0)
	for rows.Next() {
		var item PolicyAudit
		var before, after []byte
		if err := rows.Scan(&item.LegacyID, &item.OperatorExternalUserID, &item.OldVersion, &item.NewVersion, &before, &after, &item.CreatedAt); err != nil {
			return nil, err
		}
		item.RequestID = "legacy-policy-audit-" + strconv.FormatInt(item.LegacyID, 10)
		item.Before, err = normalizeJSON(before, "object")
		if err != nil {
			return nil, fmt.Errorf("sanitize policy audit %d before value: %w", item.LegacyID, err)
		}
		item.After, err = normalizeJSON(after, "object")
		if err != nil {
			return nil, fmt.Errorf("sanitize policy audit %d after value: %w", item.LegacyID, err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func readSourceRuntimeSettings(ctx context.Context, tx *sql.Tx) ([]RuntimeSetting, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT key, value, updated_at FROM settings WHERE key = 'image_job_runtime_settings' ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("read source runtime settings: %w", err)
	}
	defer rows.Close()
	result := make([]RuntimeSetting, 0, 1)
	for rows.Next() {
		var item RuntimeSetting
		var raw string
		if err := rows.Scan(&item.Name, &raw, &item.UpdatedAt); err != nil {
			return nil, err
		}
		item.Version = 1
		item.Value, err = normalizeJSON([]byte(raw), "object")
		if err != nil {
			return nil, fmt.Errorf("sanitize runtime setting %s: %w", item.Name, err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func readSourceJobs(ctx context.Context, tx *sql.Tx, snapshotTime time.Time, policies []ModelPolicy, warnings *[]string) ([]Job, []UnresolvedItem, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT job.public_id, job.user_id, COALESCE(project.public_id, ''), COALESCE(job.client_node_id, ''),
			job.endpoint, job.mode, job.operation, job.requested_model, COALESCE(job.selected_model, ''),
			COALESCE(job.successful_model, ''), COALESCE(job.policy_version, 0), job.status, job.execution_phase,
			job.requested_count, job.completed_count, job.request_digest, COALESCE(job.idempotency_key_hash, ''),
			job.attempt_plan, job.cancel_requested_at, COALESCE(job.error_type, ''), COALESCE(job.error_code, ''),
			COALESCE(job.error_message, ''), job.error_retryable, job.created_at, job.updated_at, job.finished_at
		FROM image_jobs job LEFT JOIN image_canvas_projects project ON project.id = job.project_id
		ORDER BY job.public_id`)
	if err != nil {
		return nil, nil, fmt.Errorf("read source image jobs: %w", err)
	}
	defer rows.Close()
	result := make([]Job, 0)
	unresolved := make([]UnresolvedItem, 0)
	defaultPolicyVersion := int64(1)
	if len(policies) == 1 {
		defaultPolicyVersion = policies[0].Version
	}
	for rows.Next() {
		var item Job
		var endpoint, mode, requestedModel string
		var attemptPlan []byte
		var originalStatus, originalPhase string
		if err := rows.Scan(&item.PublicID, &item.ExternalUserID, &item.ProjectPublicID, &item.ClientNodeID,
			&endpoint, &mode, &item.Operation, &requestedModel, &item.SelectedModel, &item.SuccessfulModel,
			&item.PolicyVersion, &originalStatus, &originalPhase, &item.RequestedCount, &item.CompletedCount,
			&item.RequestDigest, &item.IdempotencyHash, &attemptPlan, &item.CancelRequestedAt,
			&item.ErrorType, &item.ErrorCode, &item.ErrorMessage, &item.ErrorRetryable,
			&item.CreatedAt, &item.UpdatedAt, &item.FinishedAt); err != nil {
			return nil, nil, err
		}
		if item.ClientNodeID == "" {
			item.ClientNodeID = truncateUTF8("legacy-"+item.PublicID, 128)
			*warnings = append(*warnings, fmt.Sprintf("image job %s had no client node ID; a deterministic legacy value was used", item.PublicID))
		}
		if item.SelectedModel == "" {
			item.SelectedModel = requestedModel
		}
		if item.PolicyVersion <= 0 {
			item.PolicyVersion = defaultPolicyVersion
		}
		item.RequestDigest = strings.ToLower(strings.TrimSpace(item.RequestDigest))
		if item.IdempotencyHash == "" {
			digest := sha256.Sum256([]byte("legacy-image-job\x00" + item.PublicID))
			item.IdempotencyHash = hex.EncodeToString(digest[:])
			*warnings = append(*warnings, fmt.Sprintf("image job %s had no idempotency hash; a deterministic history-only hash was generated", item.PublicID))
		} else {
			item.IdempotencyHash = strings.ToLower(strings.TrimSpace(item.IdempotencyHash))
		}
		item.AttemptPlan, err = normalizeStringArray(attemptPlan)
		if err != nil {
			return nil, nil, fmt.Errorf("sanitize attempt plan for image job %s: %w", item.PublicID, err)
		}
		item.Request, err = sanitizedLegacyRequest("image", endpoint, mode, requestedModel)
		if err != nil {
			return nil, nil, err
		}
		item.ErrorType = truncateUTF8(sanitizeText(item.ErrorType), 64)
		item.ErrorCode = truncateUTF8(sanitizeText(item.ErrorCode), 64)
		item.ErrorMessage = truncateUTF8(sanitizeText(item.ErrorMessage), 512)
		item.Phase = mapJobPhase(originalPhase)
		item.Status = originalStatus
		if isUnresolvedStatus(originalStatus) {
			unresolved = append(unresolved, UnresolvedItem{Kind: "image_job", PublicID: item.PublicID, Status: originalStatus, Phase: originalPhase})
			item.Status = "indeterminate"
			if item.FinishedAt == nil {
				finished := snapshotTime
				item.FinishedAt = &finished
			}
		}
		result = append(result, item)
	}
	return result, unresolved, rows.Err()
}

func readSourceJobInputs(ctx context.Context, tx *sql.Tx) ([]JobInput, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT job.public_id, input.index, input.kind, COALESCE(asset.public_id, ''), input.sha256, input.created_at
		FROM image_job_inputs input
		JOIN image_jobs job ON job.id = input.job_id
		LEFT JOIN image_assets asset ON asset.object_key = input.object_key
		ORDER BY job.public_id, input.kind, input.index`)
	if err != nil {
		return nil, fmt.Errorf("read source image job inputs: %w", err)
	}
	defer rows.Close()
	result := make([]JobInput, 0)
	for rows.Next() {
		var item JobInput
		var sourceKind string
		if err := rows.Scan(&item.JobPublicID, &item.Position, &sourceKind, &item.AssetPublicID, &item.SHA256, &item.CreatedAt); err != nil {
			return nil, err
		}
		if item.AssetPublicID == "" {
			return nil, fmt.Errorf("image job %s input %d has no matching durable asset and cannot be imported safely", item.JobPublicID, item.Position)
		}
		item.Kind = "image"
		if strings.Contains(strings.ToLower(sourceKind), "mask") {
			item.Kind = "mask"
		}
		item.SHA256 = strings.ToLower(strings.TrimSpace(item.SHA256))
		result = append(result, item)
	}
	return result, rows.Err()
}

func readSourceJobResults(ctx context.Context, tx *sql.Tx) ([]JobResult, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT job.public_id, result.index, result.status, COALESCE(asset.public_id, ''),
			COALESCE(result.mime_type, ''), COALESCE(result.size_tier, ''), result.created_at
		FROM image_job_results result
		JOIN image_jobs job ON job.id = result.job_id
		LEFT JOIN image_assets asset ON asset.id = result.asset_id
		ORDER BY job.public_id, result.index`)
	if err != nil {
		return nil, fmt.Errorf("read source image job results: %w", err)
	}
	defer rows.Close()
	result := make([]JobResult, 0)
	for rows.Next() {
		var item JobResult
		if err := rows.Scan(&item.JobPublicID, &item.Position, &item.Status, &item.AssetPublicID, &item.MIMEType, &item.Size, &item.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func readSourceMediaTasks(ctx context.Context, tx *sql.Tx, snapshotTime time.Time) ([]LegacyMediaTask, []UnresolvedItem, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT task.public_id, task.kind, task.status, task.phase, task.user_id, project.public_id,
			task.client_node_id, task.selected_model, COALESCE(task.successful_model, ''), task.request_hash,
			COALESCE(asset.public_id, ''), task.error IS NOT NULL, task.created_at, task.updated_at, task.completed_at
		FROM canvas_media_tasks task
		JOIN image_canvas_projects project ON project.id = task.project_id
		LEFT JOIN image_assets asset ON asset.id = task.result_asset_id
		ORDER BY task.public_id`)
	if err != nil {
		return nil, nil, fmt.Errorf("read source media tasks: %w", err)
	}
	defer rows.Close()
	result := make([]LegacyMediaTask, 0)
	unresolved := make([]UnresolvedItem, 0)
	for rows.Next() {
		var item LegacyMediaTask
		var hadError bool
		var originalStatus string
		if err := rows.Scan(&item.PublicID, &item.MediaKind, &originalStatus, &item.Phase, &item.ExternalUserID,
			&item.ProjectPublicID, &item.ClientNodeID, &item.SelectedModel, &item.SuccessfulModel,
			&item.RequestDigest, &item.ResultAssetPublicID, &hadError, &item.CreatedAt, &item.UpdatedAt, &item.FinishedAt); err != nil {
			return nil, nil, err
		}
		item.RequestDigest = strings.ToLower(strings.TrimSpace(item.RequestDigest))
		item.Request, err = sanitizedLegacyRequest(item.MediaKind, "", "", item.SelectedModel)
		if err != nil {
			return nil, nil, err
		}
		if hadError {
			item.Error = json.RawMessage(`{"legacy_import":true,"present":true}`)
		}
		item.Status = originalStatus
		if isUnresolvedStatus(originalStatus) {
			unresolved = append(unresolved, UnresolvedItem{Kind: "legacy_media_task", PublicID: item.PublicID, Status: originalStatus, Phase: item.Phase})
			item.Status = "indeterminate"
			if item.FinishedAt == nil {
				finished := snapshotTime
				item.FinishedAt = &finished
			}
		}
		result = append(result, item)
	}
	return result, unresolved, rows.Err()
}

func readSourceObjects(root string, assets []legacyAsset, referenced map[string]struct{}) ([]Object, error) {
	result := make([]Object, 0, len(assets)*2)
	for _, source := range assets {
		metadata, err := inspectObject(root, source.SourceObjectKey)
		if err != nil {
			return nil, fmt.Errorf("inspect source object for asset %s: %w", source.Asset.PublicID, err)
		}
		if metadata.Size != source.Asset.ByteSize || metadata.SHA256 != source.Asset.SHA256 {
			return nil, fmt.Errorf("source object metadata mismatch for asset %s", source.Asset.PublicID)
		}
		result = append(result, Object{AssetPublicID: source.Asset.PublicID, Kind: "original", SourceKey: source.SourceObjectKey,
			TargetKey: source.Asset.ObjectKey, MIMEType: source.Asset.MIMEType, Size: metadata.Size, SHA256: metadata.SHA256})
		referenced[source.SourceObjectKey] = struct{}{}
		if source.SourceThumbnailKey != "" {
			thumbnail, err := inspectObject(root, source.SourceThumbnailKey)
			if err != nil {
				return nil, fmt.Errorf("inspect source thumbnail for asset %s: %w", source.Asset.PublicID, err)
			}
			result = append(result, Object{AssetPublicID: source.Asset.PublicID, Kind: "thumbnail", SourceKey: source.SourceThumbnailKey,
				TargetKey: source.Asset.ThumbnailKey, MIMEType: thumbnail.ContentType, Size: thumbnail.Size, SHA256: thumbnail.SHA256})
			referenced[source.SourceThumbnailKey] = struct{}{}
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].TargetKey < result[right].TargetKey })
	return result, nil
}

func countUnreferencedFiles(root string, referenced map[string]struct{}) (int, error) {
	count := 0
	err := filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return errors.New("source object root contains a symlink")
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("source object root contains a non-regular file")
		}
		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(relative)
		if _, ok := referenced[key]; !ok {
			count++
		}
		return nil
	})
	return count, err
}

func loadResolutions(path string, unresolved []UnresolvedItem) ([]ResolutionDecision, error) {
	if len(unresolved) == 0 && strings.TrimSpace(path) == "" {
		return []ResolutionDecision{}, nil
	}
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("source has unresolved jobs; --resolution-file is required")
	}
	path, err := cleanAbsolutePath(path, "resolution file")
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect resolution file: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("resolution file must be a private regular file")
	}
	var file ResolutionFile
	if err := readJSONFile(path, &file); err != nil {
		return nil, fmt.Errorf("read resolution file: %w", err)
	}
	if file.Format != ResolutionFormat {
		return nil, fmt.Errorf("unsupported resolution format %q", file.Format)
	}
	sort.Slice(file.Decisions, func(left, right int) bool {
		if file.Decisions[left].Kind != file.Decisions[right].Kind {
			return file.Decisions[left].Kind < file.Decisions[right].Kind
		}
		return file.Decisions[left].PublicID < file.Decisions[right].PublicID
	})
	if err := validateResolutions(unresolved, file.Decisions); err != nil {
		return nil, err
	}
	return file.Decisions, nil
}

func sanitizedLegacyRequest(kind, endpoint, mode, model string) (json.RawMessage, error) {
	value := map[string]any{"legacy_import": true, "kind": kind}
	if endpoint != "" {
		value["endpoint"] = endpoint
	}
	if mode != "" {
		value["mode"] = mode
	}
	if model != "" {
		value["model"] = model
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return normalizeJSON(payload, "object")
}

func normalizeStringArray(raw []byte) (json.RawMessage, error) {
	var items []string
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	if err := decoder.Decode(&items); err != nil {
		return nil, err
	}
	if items == nil {
		items = []string{}
	}
	for _, item := range items {
		if strings.TrimSpace(item) == "" || len(item) > 128 || containsSecret(item) {
			return nil, errors.New("attempt plan contains an invalid model")
		}
	}
	payload, err := json.Marshal(items)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(payload), nil
}

func mapJobPhase(value string) string {
	switch value {
	case "preflight":
		return "preflight"
	case "upstream", "falling_back":
		return "upstream"
	case "saving":
		return "saving"
	default:
		return "preflight"
	}
}

func isUnresolvedStatus(value string) bool {
	return value == "queued" || value == "running" || value == "indeterminate"
}

func sanitizeText(value string) string {
	if containsSecret(value) {
		return "[redacted legacy value]"
	}
	return strings.TrimSpace(value)
}

func truncateUTF8(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	end := maximum
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func digestJSONLines(objects []Object) (string, error) {
	hasher := sha256.New()
	encoder := json.NewEncoder(hasher)
	encoder.SetEscapeHTML(false)
	for _, object := range objects {
		if err := encoder.Encode(object); err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func legacyImageCapability(model string) (json.RawMessage, bool) {
	normalized := strings.ToLower(strings.TrimSpace(model))
	var raw string
	switch {
	case normalized == "gpt-image-2":
		raw = `{"media_kind":"image","provider":"openai","dimension_mode":"size","generation":true,"edit":true,"multi_image":true,"mask":true,"max_input_images":16,"max_outputs":4,"sizes":["auto","1024x1024","1536x1024","1024x1536","2048x2048","2048x1152","1152x2048","3840x2160","2160x3840"],"qualities":["auto","low","medium","high"],"output_formats":["png","jpeg","webp"],"backgrounds":["auto","opaque","transparent"],"output_compression":true,"defaults":{"size":"1024x1024","quality":"high","output_format":"png","background":"auto"}}`
	case strings.HasPrefix(normalized, "gpt-image-"):
		raw = `{"media_kind":"image","provider":"openai","dimension_mode":"size","generation":true,"edit":true,"multi_image":true,"mask":true,"max_input_images":10,"max_outputs":4,"sizes":["auto","1024x1024","1536x1024","1024x1536"],"qualities":["auto","low","medium","high"],"output_formats":["png","jpeg","webp"],"backgrounds":["auto","opaque","transparent"],"output_compression":true,"defaults":{"size":"auto","quality":"auto","output_format":"png","background":"auto"}}`
	case normalized == "grok-imagine" || normalized == "grok-imagine-image" || normalized == "grok-imagine-image-quality" || normalized == "grok-imagine-edit":
		generation := normalized != "grok-imagine-edit"
		raw = fmt.Sprintf(`{"media_kind":"image","provider":"grok","dimension_mode":"aspect_ratio_resolution","generation":%t,"edit":true,"multi_image":true,"mask":false,"max_input_images":3,"max_outputs":4,"aspect_ratios":["auto","1:1","16:9","9:16","4:3","3:4","3:2","2:3"],"resolutions":["1k","2k"],"defaults":{"aspect_ratio":"auto","resolution":"2k"}}`, generation)
	default:
		return nil, false
	}
	value, err := normalizeJSON([]byte(raw), "object")
	return value, err == nil
}
