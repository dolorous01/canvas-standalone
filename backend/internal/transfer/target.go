package transfer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

func Import(ctx context.Context, db *sql.DB, input string) (Report, error) {
	manifest, content, err := readExport(input)
	if err != nil {
		return Report{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Report{}, fmt.Errorf("begin target import: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('canvas-standalone-transfer'))`); err != nil {
		return Report{}, fmt.Errorf("lock target import: %w", err)
	}
	if err := requireTargetSchema(ctx, tx); err != nil {
		return Report{}, err
	}
	if err := prepareTargetPolicy(ctx, tx, content); err != nil {
		return Report{}, err
	}
	if err := insertTargetRows(ctx, tx, content); err != nil {
		return Report{}, err
	}
	if err := verifyTargetDatabase(ctx, tx, content); err != nil {
		return Report{}, err
	}
	counts, err := json.Marshal(manifest.Counts)
	if err != nil {
		return Report{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO canvas_transfer_imports (content_sha256, export_format, counts)
		VALUES ($1, $2, $3) ON CONFLICT (content_sha256) DO NOTHING`, manifest.ContentSHA256, manifest.Format, counts); err != nil {
		return Report{}, fmt.Errorf("record target import: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Report{}, fmt.Errorf("commit target import: %w", err)
	}
	return newReport("import", manifest, content, true), nil
}

func VerifyTransfer(ctx context.Context, db *sql.DB, input, targetObjectRoot string) (Report, error) {
	manifest, content, err := readExport(input)
	if err != nil {
		return Report{}, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Report{}, fmt.Errorf("begin target verification: %w", err)
	}
	defer tx.Rollback()
	if err := requireTargetSchema(ctx, tx); err != nil {
		return Report{}, err
	}
	if err := verifyTargetDatabase(ctx, tx, content); err != nil {
		return Report{}, err
	}
	var recorded bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM canvas_transfer_imports WHERE content_sha256 = $1)`, manifest.ContentSHA256).Scan(&recorded); err != nil {
		return Report{}, fmt.Errorf("read target import record: %w", err)
	}
	if !recorded {
		return Report{}, errors.New("target database has no successful record for this export digest")
	}
	if err := tx.Commit(); err != nil {
		return Report{}, fmt.Errorf("finish target verification snapshot: %w", err)
	}
	if err := verifyObjects(ctx, targetObjectRoot, content.Objects); err != nil {
		return Report{}, err
	}
	return newReport("verify-transfer", manifest, content, true), nil
}

func requireTargetSchema(ctx context.Context, query rowQueryer) error {
	var applied int
	if err := query.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM canvas_schema_migrations
		WHERE filename IN ('001_core.sql','002_default_policy.sql','003_job_compatibility.sql','004_legacy_media_history.sql')`).Scan(&applied); err != nil {
		return fmt.Errorf("read target Canvas schema: %w", err)
	}
	if applied != 4 {
		return errors.New("target Canvas schema migrations 001-004 are not all applied")
	}
	return nil
}

func prepareTargetPolicy(ctx context.Context, tx *sql.Tx, content dataset) error {
	if len(content.ModelPolicies) == 0 {
		return nil
	}
	currentPolicies, currentItems, err := readTargetPolicy(ctx, tx)
	if err != nil {
		return err
	}
	if policyDataEqual(currentPolicies, currentItems, content.ModelPolicies, content.ModelPolicyItems) {
		return nil
	}
	var businessRows, audits int
	if err := tx.QueryRowContext(ctx, `
		SELECT
			(SELECT COUNT(*) FROM canvas_projects) +
			(SELECT COUNT(*) FROM canvas_assets) +
			(SELECT COUNT(*) FROM canvas_library_items) +
			(SELECT COUNT(*) FROM canvas_editor_documents) +
			(SELECT COUNT(*) FROM canvas_jobs) +
			(SELECT COUNT(*) FROM canvas_legacy_media_tasks),
			(SELECT COUNT(*) FROM canvas_model_policy_audits)`).Scan(&businessRows, &audits); err != nil {
		return fmt.Errorf("inspect target policy state: %w", err)
	}
	pristine := businessRows == 0 && audits == 0 && len(currentPolicies) == 1 && currentPolicies[0].Version == 1 && len(currentItems) == 1 && currentItems[0].Model == "gpt-image-2"
	if !pristine {
		return errors.New("target model policy differs from the export and is not a pristine default")
	}
	return nil
}

func insertTargetRows(ctx context.Context, tx *sql.Tx, content dataset) error {
	for _, item := range content.Projects {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_projects (public_id, external_user_id, name, document, version, created_at, updated_at, deleted_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (public_id) DO NOTHING`,
			item.PublicID, item.ExternalUserID, item.Name, item.Document, item.Version, item.CreatedAt, item.UpdatedAt, item.DeletedAt); err != nil {
			return rowError("project", item.PublicID, err)
		}
	}
	for _, item := range content.Assets {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_assets (
				public_id, external_user_id, project_id, source_type, media_kind, object_key,
				thumbnail_object_key, file_name, mime_type, width, height, duration_ms,
				byte_size, sha256, created_at, deleted_at
			) VALUES ($1,$2,(SELECT id FROM canvas_projects WHERE public_id = NULLIF($3,'')),$4,$5,$6,NULLIF($7,''),$8,$9,$10,$11,$12,$13,$14,$15,$16)
			ON CONFLICT (public_id) DO NOTHING`, item.PublicID, item.ExternalUserID, item.ProjectPublicID,
			item.SourceType, item.MediaKind, item.ObjectKey, item.ThumbnailKey, item.FileName, item.MIMEType,
			item.Width, item.Height, item.DurationMS, item.ByteSize, item.SHA256, item.CreatedAt, item.DeletedAt); err != nil {
			return rowError("asset", item.PublicID, err)
		}
	}
	for _, item := range content.Assets {
		for _, parentID := range item.ParentPublicIDs {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO canvas_asset_parents (asset_id, parent_asset_id)
				SELECT asset.id, parent.id FROM canvas_assets asset, canvas_assets parent
				WHERE asset.public_id=$1 AND parent.public_id=$2 ON CONFLICT DO NOTHING`, item.PublicID, parentID); err != nil {
				return rowError("asset parent", item.PublicID, err)
			}
		}
	}
	for _, item := range content.Projects {
		if item.ThumbnailAssetPublicID == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE canvas_projects SET thumbnail_asset_id=(SELECT id FROM canvas_assets WHERE public_id=$2) WHERE public_id=$1`, item.PublicID, item.ThumbnailAssetPublicID); err != nil {
			return rowError("project thumbnail", item.PublicID, err)
		}
	}
	for _, item := range content.ProjectAssetRefs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_project_asset_refs (project_id, asset_id, node_id, created_at)
			SELECT project.id, asset.id, $3, $4 FROM canvas_projects project, canvas_assets asset
			WHERE project.public_id=$1 AND asset.public_id=$2 ON CONFLICT DO NOTHING`, item.ProjectPublicID, item.AssetPublicID, item.NodeID, item.CreatedAt); err != nil {
			return rowError("project asset reference", item.ProjectPublicID, err)
		}
	}
	for _, item := range content.LibraryItems {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_library_items (public_id, client_id, external_user_id, kind, asset_id, title, content, tags, source, note, metadata, version, created_at, updated_at, deleted_at)
			VALUES ($1,$2,$3,$4,(SELECT id FROM canvas_assets WHERE public_id=NULLIF($5,'')),$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
			ON CONFLICT (public_id) DO NOTHING`, item.PublicID, item.ClientID, item.ExternalUserID, item.Kind, item.AssetPublicID,
			item.Title, item.Content, item.Tags, item.Source, item.Note, item.Metadata, item.Version, item.CreatedAt, item.UpdatedAt, item.DeletedAt); err != nil {
			return rowError("library item", item.PublicID, err)
		}
	}
	for _, item := range content.EditorDocuments {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_editor_documents (public_id, project_id, node_id, base_asset_id, current_asset_id, document, version, created_at, updated_at, deleted_at)
			SELECT $1, project.id, $3, base.id, current.id, $6, $7, $8, $9, $10
			FROM canvas_projects project, canvas_assets base, canvas_assets current
			WHERE project.public_id=$2 AND base.public_id=$4 AND current.public_id=$5
			ON CONFLICT (public_id) DO NOTHING`, item.PublicID, item.ProjectPublicID, item.NodeID,
			item.BaseAssetPublicID, item.CurrentAssetPublicID, item.Document, item.Version,
			item.CreatedAt, item.UpdatedAt, item.DeletedAt); err != nil {
			return rowError("editor document", item.PublicID, err)
		}
	}
	for _, item := range content.EditorAssetRefs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_editor_asset_refs (document_id, asset_id, role, element_id, created_at)
			SELECT document.id, asset.id, $3, $4, $5 FROM canvas_editor_documents document, canvas_assets asset
			WHERE document.public_id=$1 AND asset.public_id=$2 ON CONFLICT DO NOTHING`, item.DocumentPublicID, item.AssetPublicID, item.Role, item.ElementID, item.CreatedAt); err != nil {
			return rowError("editor asset reference", item.DocumentPublicID, err)
		}
	}
	for _, item := range content.EditorRevisions {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_editor_revisions (public_id, document_id, version, asset_id, operation, parameters, created_at)
			SELECT $1, document.id, $3, asset.id, $5, $6, $7 FROM canvas_editor_documents document, canvas_assets asset
			WHERE document.public_id=$2 AND asset.public_id=$4 ON CONFLICT (public_id) DO NOTHING`, item.PublicID, item.DocumentPublicID, item.Version, item.AssetPublicID, item.Operation, item.Parameters, item.CreatedAt); err != nil {
			return rowError("editor revision", item.PublicID, err)
		}
	}
	if len(content.ModelPolicies) == 1 {
		policy := content.ModelPolicies[0]
		if _, err := tx.ExecContext(ctx, `UPDATE canvas_model_policies SET version=$1, enabled=$2, created_at=$3, updated_at=$4 WHERE id=1`, policy.Version, policy.Enabled, policy.CreatedAt, policy.UpdatedAt); err != nil {
			return fmt.Errorf("update model policy: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM canvas_model_policy_items WHERE policy_id=1`); err != nil {
			return fmt.Errorf("replace model policy items: %w", err)
		}
		for _, item := range content.ModelPolicyItems {
			if _, err := tx.ExecContext(ctx, `INSERT INTO canvas_model_policy_items (policy_id, model, enabled, position, capability, created_at, updated_at) VALUES (1,$1,$2,$3,$4,$5,$6)`, item.Model, item.Enabled, item.Position, item.Capability, item.CreatedAt, item.UpdatedAt); err != nil {
				return rowError("model policy item", item.Model, err)
			}
		}
	}
	for _, item := range content.PolicyAudits {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_model_policy_audits (operator_external_user_id, request_id, old_version, new_version, before_value, after_value, created_at)
			SELECT $1,$2::VARCHAR(128),$3,$4,$5,$6,$7 WHERE NOT EXISTS (SELECT 1 FROM canvas_model_policy_audits WHERE request_id=$2::VARCHAR(128))`,
			item.OperatorExternalUserID, item.RequestID, item.OldVersion, item.NewVersion, item.Before, item.After, item.CreatedAt); err != nil {
			return rowError("policy audit", item.RequestID, err)
		}
	}
	for _, item := range content.RuntimeSettings {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_runtime_settings (name, value, version, updated_by_external_user_id, updated_at)
			VALUES ($1,$2,$3,$4,$5) ON CONFLICT (name) DO NOTHING`, item.Name, item.Value, item.Version, item.UpdatedByExternalUserID, item.UpdatedAt); err != nil {
			return rowError("runtime setting", item.Name, err)
		}
	}
	for _, item := range content.Jobs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_jobs (
				public_id, external_user_id, project_id, credential_id, client_node_id, operation,
				selected_model, successful_model, policy_version, status, phase, requested_count,
				completed_count, request, request_digest, idempotency_key_hash, attempt_plan,
				attempt_position, cancel_requested_at, error_type, error_code, error_message,
				error_retryable, legacy_imported, created_at, updated_at, finished_at
			) SELECT $1,$2,project.id,NULL,$4,$5,$6,NULLIF($7,''),$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,NULLIF($19,''),NULLIF($20,''),NULLIF($21,''),$22,TRUE,$23,$24,$25
			FROM canvas_projects project WHERE project.public_id=$3 ON CONFLICT (public_id) DO NOTHING`,
			item.PublicID, item.ExternalUserID, item.ProjectPublicID, item.ClientNodeID, item.Operation,
			item.SelectedModel, item.SuccessfulModel, item.PolicyVersion, item.Status, item.Phase,
			item.RequestedCount, item.CompletedCount, item.Request, item.RequestDigest, item.IdempotencyHash,
			item.AttemptPlan, item.AttemptPosition, item.CancelRequestedAt, item.ErrorType, item.ErrorCode,
			item.ErrorMessage, item.ErrorRetryable, item.CreatedAt, item.UpdatedAt, item.FinishedAt); err != nil {
			return rowError("terminal job", item.PublicID, err)
		}
	}
	for _, item := range content.JobInputs {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_job_inputs (job_id, position, asset_id, kind, sha256, created_at)
			SELECT job.id,$2,asset.id,$4,$5,$6 FROM canvas_jobs job,canvas_assets asset
			WHERE job.public_id=$1 AND asset.public_id=$3 ON CONFLICT DO NOTHING`, item.JobPublicID, item.Position, item.AssetPublicID, item.Kind, item.SHA256, item.CreatedAt); err != nil {
			return rowError("job input", item.JobPublicID, err)
		}
	}
	for _, item := range content.JobResults {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_job_results (job_id, position, status, asset_id, mime_type, size, created_at)
			SELECT job.id,$2,$3,asset.id,NULLIF($5,''),NULLIF($6,''),$7 FROM canvas_jobs job
			LEFT JOIN canvas_assets asset ON asset.public_id=NULLIF($4,'') WHERE job.public_id=$1
			ON CONFLICT DO NOTHING`, item.JobPublicID, item.Position, item.Status, item.AssetPublicID, item.MIMEType, item.Size, item.CreatedAt); err != nil {
			return rowError("job result", item.JobPublicID, err)
		}
	}
	for _, item := range content.LegacyMediaTasks {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_legacy_media_tasks (
				public_id, media_kind, status, phase, external_user_id, project_id, client_node_id,
				selected_model, successful_model, request, request_digest, result_asset_id, error,
				created_at, updated_at, finished_at
			) SELECT $1,$2,$3,$4,$5,project.id,$7,$8,NULLIF($9,''),$10,$11,asset.id,$13,$14,$15,$16
			FROM canvas_projects project LEFT JOIN canvas_assets asset ON asset.public_id=NULLIF($12,'')
			WHERE project.public_id=$6 ON CONFLICT (public_id) DO NOTHING`, item.PublicID, item.MediaKind,
			item.Status, item.Phase, item.ExternalUserID, item.ProjectPublicID, item.ClientNodeID,
			item.SelectedModel, item.SuccessfulModel, item.Request, item.RequestDigest,
			item.ResultAssetPublicID, nullableJSON(item.Error), item.CreatedAt, item.UpdatedAt, item.FinishedAt); err != nil {
			return rowError("legacy media task", item.PublicID, err)
		}
	}
	return nil
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func rowError(kind, identifier string, err error) error {
	return fmt.Errorf("import %s %s: %w", kind, identifier, err)
}

type rowQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type rowsQueryer interface {
	rowQueryer
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readTargetPolicy(ctx context.Context, query rowsQueryer) ([]ModelPolicy, []ModelPolicyItem, error) {
	rows, err := query.QueryContext(ctx, `SELECT version, enabled, created_at, updated_at FROM canvas_model_policies WHERE id=1`)
	if err != nil {
		return nil, nil, fmt.Errorf("read target model policy: %w", err)
	}
	policies := make([]ModelPolicy, 0, 1)
	for rows.Next() {
		var item ModelPolicy
		if err := rows.Scan(&item.Version, &item.Enabled, &item.CreatedAt, &item.UpdatedAt); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		policies = append(policies, item)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	rows, err = query.QueryContext(ctx, `SELECT model, enabled, position, capability, created_at, updated_at FROM canvas_model_policy_items WHERE policy_id=1 ORDER BY position, model`)
	if err != nil {
		return nil, nil, fmt.Errorf("read target model policy items: %w", err)
	}
	defer rows.Close()
	items := make([]ModelPolicyItem, 0)
	for rows.Next() {
		var item ModelPolicyItem
		if err := rows.Scan(&item.Model, &item.Enabled, &item.Position, &item.Capability, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, nil, err
		}
		items = append(items, item)
	}
	return policies, items, rows.Err()
}

func policyDataEqual(leftPolicies []ModelPolicy, leftItems []ModelPolicyItem, rightPolicies []ModelPolicy, rightItems []ModelPolicyItem) bool {
	left := dataset{ModelPolicies: leftPolicies, ModelPolicyItems: leftItems}
	right := dataset{ModelPolicies: rightPolicies, ModelPolicyItems: rightItems}
	if normalizeDataset(&left) != nil || normalizeDataset(&right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

func verifyTargetDatabase(ctx context.Context, query rowsQueryer, expected dataset) error {
	actual, err := readTargetDataset(ctx, query)
	if err != nil {
		return err
	}
	if err := normalizeDataset(&expected); err != nil {
		return fmt.Errorf("normalize expected transfer data: %w", err)
	}
	if err := normalizeDataset(&actual); err != nil {
		return fmt.Errorf("normalize target transfer data: %w", err)
	}
	expected.Objects = nil
	actual.Objects = nil
	comparisons := []struct {
		name     string
		expected any
		actual   any
	}{
		{"projects", expected.Projects, actual.Projects}, {"assets", expected.Assets, actual.Assets},
		{"project asset references", expected.ProjectAssetRefs, actual.ProjectAssetRefs},
		{"library items", expected.LibraryItems, actual.LibraryItems},
		{"editor documents", expected.EditorDocuments, actual.EditorDocuments},
		{"editor asset references", expected.EditorAssetRefs, actual.EditorAssetRefs},
		{"editor revisions", expected.EditorRevisions, actual.EditorRevisions},
		{"model policies", expected.ModelPolicies, actual.ModelPolicies},
		{"model policy items", expected.ModelPolicyItems, actual.ModelPolicyItems},
		{"model policy audits", expected.PolicyAudits, actual.PolicyAudits},
		{"runtime settings", expected.RuntimeSettings, actual.RuntimeSettings},
		{"terminal jobs", expected.Jobs, actual.Jobs}, {"job inputs", expected.JobInputs, actual.JobInputs},
		{"job results", expected.JobResults, actual.JobResults},
		{"legacy media tasks", expected.LegacyMediaTasks, actual.LegacyMediaTasks},
	}
	for _, comparison := range comparisons {
		left, err := json.Marshal(comparison.expected)
		if err != nil {
			return err
		}
		right, err := json.Marshal(comparison.actual)
		if err != nil {
			return err
		}
		if !bytes.Equal(left, right) {
			return fmt.Errorf("target %s do not match the export", comparison.name)
		}
	}
	var claimable, credentialed int
	if err := query.QueryRowContext(ctx, `
		SELECT COUNT(*) FILTER (WHERE status IN ('queued','running')),
			COUNT(*) FILTER (WHERE credential_id IS NOT NULL)
		FROM canvas_jobs WHERE legacy_imported=TRUE`).Scan(&claimable, &credentialed); err != nil {
		return fmt.Errorf("inspect imported job safety: %w", err)
	}
	if claimable != 0 || credentialed != 0 {
		return errors.New("imported jobs are claimable or retain a credential binding")
	}
	return nil
}

func readTargetDataset(ctx context.Context, query rowsQueryer) (dataset, error) {
	var result dataset
	var err error
	result.Projects, err = queryJSONRows[Project](ctx, query, `
		SELECT jsonb_build_object(
			'public_id',project.public_id,'external_user_id',project.external_user_id,'name',project.name,
			'document',project.document,'version',project.version,
			'thumbnail_asset_public_id',COALESCE(thumbnail.public_id,''),'created_at',project.created_at,
			'updated_at',project.updated_at,'deleted_at',project.deleted_at)
		FROM canvas_projects project LEFT JOIN canvas_assets thumbnail ON thumbnail.id=project.thumbnail_asset_id
		ORDER BY project.public_id`)
	if err != nil {
		return dataset{}, targetReadError("projects", err)
	}
	result.Assets, err = queryJSONRows[Asset](ctx, query, `
		SELECT jsonb_build_object(
			'public_id',asset.public_id,'external_user_id',asset.external_user_id,
			'project_public_id',COALESCE(project.public_id,''),'source_type',asset.source_type,
			'media_kind',asset.media_kind,'object_key',asset.object_key,
			'thumbnail_object_key',COALESCE(asset.thumbnail_object_key,''),'file_name',COALESCE(asset.file_name,''),
			'mime_type',asset.mime_type,'width',asset.width,'height',asset.height,'duration_ms',asset.duration_ms,
			'byte_size',asset.byte_size,'sha256',asset.sha256,
			'parent_public_ids',COALESCE((SELECT jsonb_agg(parent.public_id ORDER BY parent.public_id)
				FROM canvas_asset_parents relation JOIN canvas_assets parent ON parent.id=relation.parent_asset_id
				WHERE relation.asset_id=asset.id),'[]'::jsonb),
			'created_at',asset.created_at,'deleted_at',asset.deleted_at)
		FROM canvas_assets asset LEFT JOIN canvas_projects project ON project.id=asset.project_id
		ORDER BY asset.public_id`)
	if err != nil {
		return dataset{}, targetReadError("assets", err)
	}
	result.ProjectAssetRefs, err = queryJSONRows[ProjectAssetRef](ctx, query, `
		SELECT jsonb_build_object('project_public_id',project.public_id,'asset_public_id',asset.public_id,
			'node_id',reference.node_id,'created_at',reference.created_at)
		FROM canvas_project_asset_refs reference JOIN canvas_projects project ON project.id=reference.project_id
		JOIN canvas_assets asset ON asset.id=reference.asset_id
		ORDER BY project.public_id,asset.public_id,reference.node_id`)
	if err != nil {
		return dataset{}, targetReadError("project asset references", err)
	}
	result.LibraryItems, err = queryJSONRows[LibraryItem](ctx, query, `
		SELECT jsonb_build_object('public_id',item.public_id,'client_id',item.client_id,
			'external_user_id',item.external_user_id,'kind',item.kind,'asset_public_id',COALESCE(asset.public_id,''),
			'title',item.title,'content',item.content,'tags',item.tags,'source',item.source,'note',item.note,
			'metadata',item.metadata,'version',item.version,'created_at',item.created_at,
			'updated_at',item.updated_at,'deleted_at',item.deleted_at)
		FROM canvas_library_items item LEFT JOIN canvas_assets asset ON asset.id=item.asset_id ORDER BY item.public_id`)
	if err != nil {
		return dataset{}, targetReadError("library items", err)
	}
	result.EditorDocuments, err = queryJSONRows[EditorDocument](ctx, query, `
		SELECT jsonb_build_object('public_id',document.public_id,'project_public_id',project.public_id,
			'node_id',document.node_id,'base_asset_public_id',base.public_id,'current_asset_public_id',current.public_id,
			'document',document.document,'version',document.version,'created_at',document.created_at,
			'updated_at',document.updated_at,'deleted_at',document.deleted_at)
		FROM canvas_editor_documents document JOIN canvas_projects project ON project.id=document.project_id
		JOIN canvas_assets base ON base.id=document.base_asset_id JOIN canvas_assets current ON current.id=document.current_asset_id
		ORDER BY document.public_id`)
	if err != nil {
		return dataset{}, targetReadError("editor documents", err)
	}
	result.EditorAssetRefs, err = queryJSONRows[EditorAssetRef](ctx, query, `
		SELECT jsonb_build_object('document_public_id',document.public_id,'asset_public_id',asset.public_id,
			'role',reference.role,'element_id',reference.element_id,'created_at',reference.created_at)
		FROM canvas_editor_asset_refs reference JOIN canvas_editor_documents document ON document.id=reference.document_id
		JOIN canvas_assets asset ON asset.id=reference.asset_id
		ORDER BY document.public_id,asset.public_id,reference.role,reference.element_id`)
	if err != nil {
		return dataset{}, targetReadError("editor asset references", err)
	}
	result.EditorRevisions, err = queryJSONRows[EditorRevision](ctx, query, `
		SELECT jsonb_build_object('public_id',revision.public_id,'document_public_id',document.public_id,
			'version',revision.version,'asset_public_id',asset.public_id,'operation',revision.operation,
			'parameters',revision.parameters,'created_at',revision.created_at)
		FROM canvas_editor_revisions revision JOIN canvas_editor_documents document ON document.id=revision.document_id
		JOIN canvas_assets asset ON asset.id=revision.asset_id ORDER BY revision.public_id`)
	if err != nil {
		return dataset{}, targetReadError("editor revisions", err)
	}
	result.ModelPolicies, result.ModelPolicyItems, err = readTargetPolicy(ctx, query)
	if err != nil {
		return dataset{}, err
	}
	result.PolicyAudits, err = queryJSONRows[PolicyAudit](ctx, query, `
		SELECT jsonb_build_object(
			'legacy_id',CASE WHEN audit.request_id ~ '^legacy-policy-audit-[0-9]+$'
				THEN substring(audit.request_id from '[0-9]+$')::bigint ELSE 0 END,
			'operator_external_user_id',audit.operator_external_user_id,'request_id',audit.request_id,
			'old_version',audit.old_version,'new_version',audit.new_version,'before',audit.before_value,
			'after',audit.after_value,'created_at',audit.created_at)
		FROM canvas_model_policy_audits audit ORDER BY audit.id`)
	if err != nil {
		return dataset{}, targetReadError("model policy audits", err)
	}
	result.RuntimeSettings, err = queryJSONRows[RuntimeSetting](ctx, query, `
		SELECT jsonb_build_object('name',setting.name,'value',setting.value,'version',setting.version,
			'updated_by_external_user_id',setting.updated_by_external_user_id,'updated_at',setting.updated_at)
		FROM canvas_runtime_settings setting ORDER BY setting.name`)
	if err != nil {
		return dataset{}, targetReadError("runtime settings", err)
	}
	result.Jobs, err = queryJSONRows[Job](ctx, query, `
		SELECT jsonb_build_object('public_id',job.public_id,'external_user_id',job.external_user_id,
			'project_public_id',project.public_id,'client_node_id',job.client_node_id,'operation',job.operation,
			'selected_model',job.selected_model,'successful_model',COALESCE(job.successful_model,''),
			'policy_version',job.policy_version,'status',job.status,'phase',job.phase,
			'requested_count',job.requested_count,'completed_count',job.completed_count,'request',job.request,
			'request_digest',job.request_digest,'idempotency_key_hash',job.idempotency_key_hash,
			'attempt_plan',job.attempt_plan,'attempt_position',job.attempt_position,
			'cancel_requested_at',job.cancel_requested_at,'error_type',COALESCE(job.error_type,''),
			'error_code',COALESCE(job.error_code,''),'error_message',COALESCE(job.error_message,''),
			'error_retryable',job.error_retryable,'created_at',job.created_at,'updated_at',job.updated_at,
			'finished_at',job.finished_at)
		FROM canvas_jobs job JOIN canvas_projects project ON project.id=job.project_id
		ORDER BY job.public_id`)
	if err != nil {
		return dataset{}, targetReadError("terminal jobs", err)
	}
	result.JobInputs, err = queryJSONRows[JobInput](ctx, query, `
		SELECT jsonb_build_object('job_public_id',job.public_id,'position',input.position,'kind',input.kind,
			'asset_public_id',asset.public_id,'sha256',input.sha256,'created_at',input.created_at)
		FROM canvas_job_inputs input JOIN canvas_jobs job ON job.id=input.job_id
		JOIN canvas_assets asset ON asset.id=input.asset_id ORDER BY job.public_id,input.kind,input.position`)
	if err != nil {
		return dataset{}, targetReadError("job inputs", err)
	}
	result.JobResults, err = queryJSONRows[JobResult](ctx, query, `
		SELECT jsonb_build_object('job_public_id',job.public_id,'position',result.position,'status',result.status,
			'asset_public_id',COALESCE(asset.public_id,''),'mime_type',COALESCE(result.mime_type,''),
			'size',COALESCE(result.size,''),'created_at',result.created_at)
		FROM canvas_job_results result JOIN canvas_jobs job ON job.id=result.job_id
		LEFT JOIN canvas_assets asset ON asset.id=result.asset_id ORDER BY job.public_id,result.position`)
	if err != nil {
		return dataset{}, targetReadError("job results", err)
	}
	result.LegacyMediaTasks, err = queryJSONRows[LegacyMediaTask](ctx, query, `
		SELECT jsonb_build_object('public_id',task.public_id,'media_kind',task.media_kind,'status',task.status,
			'phase',task.phase,'external_user_id',task.external_user_id,'project_public_id',project.public_id,
			'client_node_id',task.client_node_id,'selected_model',task.selected_model,
			'successful_model',COALESCE(task.successful_model,''),'request',task.request,
			'request_digest',task.request_digest,'result_asset_public_id',COALESCE(asset.public_id,''),
			'error',task.error,'created_at',task.created_at,'updated_at',task.updated_at,'finished_at',task.finished_at)
		FROM canvas_legacy_media_tasks task JOIN canvas_projects project ON project.id=task.project_id
		LEFT JOIN canvas_assets asset ON asset.id=task.result_asset_id ORDER BY task.public_id`)
	if err != nil {
		return dataset{}, targetReadError("legacy media tasks", err)
	}
	return result, nil
}

func queryJSONRows[T any](ctx context.Context, query rowsQueryer, statement string, arguments ...any) ([]T, error) {
	rows, err := query.QueryContext(ctx, statement, arguments...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]T, 0)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var item T
		decoder := json.NewDecoder(bytes.NewReader(payload))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&item); err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func targetReadError(entity string, err error) error {
	return fmt.Errorf("read target %s: %w", entity, err)
}

func normalizeDataset(content *dataset) error {
	if content.Projects == nil {
		content.Projects = []Project{}
	}
	if content.Assets == nil {
		content.Assets = []Asset{}
	}
	if content.ProjectAssetRefs == nil {
		content.ProjectAssetRefs = []ProjectAssetRef{}
	}
	if content.LibraryItems == nil {
		content.LibraryItems = []LibraryItem{}
	}
	if content.EditorDocuments == nil {
		content.EditorDocuments = []EditorDocument{}
	}
	if content.EditorAssetRefs == nil {
		content.EditorAssetRefs = []EditorAssetRef{}
	}
	if content.EditorRevisions == nil {
		content.EditorRevisions = []EditorRevision{}
	}
	if content.ModelPolicies == nil {
		content.ModelPolicies = []ModelPolicy{}
	}
	if content.ModelPolicyItems == nil {
		content.ModelPolicyItems = []ModelPolicyItem{}
	}
	if content.PolicyAudits == nil {
		content.PolicyAudits = []PolicyAudit{}
	}
	if content.RuntimeSettings == nil {
		content.RuntimeSettings = []RuntimeSetting{}
	}
	if content.Jobs == nil {
		content.Jobs = []Job{}
	}
	if content.JobInputs == nil {
		content.JobInputs = []JobInput{}
	}
	if content.JobResults == nil {
		content.JobResults = []JobResult{}
	}
	if content.LegacyMediaTasks == nil {
		content.LegacyMediaTasks = []LegacyMediaTask{}
	}
	if content.Objects == nil {
		content.Objects = []Object{}
	}
	for index := range content.Projects {
		content.Projects[index].CreatedAt = content.Projects[index].CreatedAt.UTC()
		content.Projects[index].UpdatedAt = content.Projects[index].UpdatedAt.UTC()
		normalizeOptionalTime(&content.Projects[index].DeletedAt)
		if err := canonicalRaw(&content.Projects[index].Document); err != nil {
			return err
		}
	}
	for index := range content.Assets {
		content.Assets[index].CreatedAt = content.Assets[index].CreatedAt.UTC()
		normalizeOptionalTime(&content.Assets[index].DeletedAt)
		content.Assets[index].SHA256 = strings.ToLower(strings.TrimSpace(content.Assets[index].SHA256))
		sortStrings(content.Assets[index].ParentPublicIDs)
	}
	for index := range content.ProjectAssetRefs {
		content.ProjectAssetRefs[index].CreatedAt = content.ProjectAssetRefs[index].CreatedAt.UTC()
	}
	for index := range content.LibraryItems {
		item := &content.LibraryItems[index]
		item.CreatedAt = item.CreatedAt.UTC()
		item.UpdatedAt = item.UpdatedAt.UTC()
		normalizeOptionalTime(&item.DeletedAt)
		if err := canonicalRaw(&item.Tags); err != nil {
			return err
		}
		if err := canonicalRaw(&item.Metadata); err != nil {
			return err
		}
	}
	for index := range content.EditorDocuments {
		item := &content.EditorDocuments[index]
		item.CreatedAt = item.CreatedAt.UTC()
		item.UpdatedAt = item.UpdatedAt.UTC()
		normalizeOptionalTime(&item.DeletedAt)
		if err := canonicalRaw(&item.Document); err != nil {
			return err
		}
	}
	for index := range content.EditorAssetRefs {
		content.EditorAssetRefs[index].CreatedAt = content.EditorAssetRefs[index].CreatedAt.UTC()
	}
	for index := range content.EditorRevisions {
		item := &content.EditorRevisions[index]
		item.CreatedAt = item.CreatedAt.UTC()
		if err := canonicalRaw(&item.Parameters); err != nil {
			return err
		}
	}
	for index := range content.ModelPolicies {
		content.ModelPolicies[index].CreatedAt = content.ModelPolicies[index].CreatedAt.UTC()
		content.ModelPolicies[index].UpdatedAt = content.ModelPolicies[index].UpdatedAt.UTC()
	}
	for index := range content.ModelPolicyItems {
		item := &content.ModelPolicyItems[index]
		item.CreatedAt = item.CreatedAt.UTC()
		item.UpdatedAt = item.UpdatedAt.UTC()
		if err := canonicalRaw(&item.Capability); err != nil {
			return err
		}
	}
	for index := range content.PolicyAudits {
		item := &content.PolicyAudits[index]
		item.CreatedAt = item.CreatedAt.UTC()
		if err := canonicalRaw(&item.Before); err != nil {
			return err
		}
		if err := canonicalRaw(&item.After); err != nil {
			return err
		}
	}
	for index := range content.RuntimeSettings {
		item := &content.RuntimeSettings[index]
		item.UpdatedAt = item.UpdatedAt.UTC()
		if err := canonicalRaw(&item.Value); err != nil {
			return err
		}
	}
	for index := range content.Jobs {
		item := &content.Jobs[index]
		item.CreatedAt = item.CreatedAt.UTC()
		item.UpdatedAt = item.UpdatedAt.UTC()
		normalizeOptionalTime(&item.CancelRequestedAt)
		normalizeOptionalTime(&item.FinishedAt)
		if err := canonicalRaw(&item.Request); err != nil {
			return err
		}
		if err := canonicalRaw(&item.AttemptPlan); err != nil {
			return err
		}
	}
	for index := range content.JobInputs {
		content.JobInputs[index].CreatedAt = content.JobInputs[index].CreatedAt.UTC()
		content.JobInputs[index].SHA256 = strings.ToLower(strings.TrimSpace(content.JobInputs[index].SHA256))
	}
	for index := range content.JobResults {
		content.JobResults[index].CreatedAt = content.JobResults[index].CreatedAt.UTC()
	}
	for index := range content.LegacyMediaTasks {
		item := &content.LegacyMediaTasks[index]
		item.CreatedAt = item.CreatedAt.UTC()
		item.UpdatedAt = item.UpdatedAt.UTC()
		normalizeOptionalTime(&item.FinishedAt)
		if err := canonicalRaw(&item.Request); err != nil {
			return err
		}
		if len(item.Error) > 0 {
			if err := canonicalRaw(&item.Error); err != nil {
				return err
			}
		}
	}
	return nil
}

func canonicalRaw(target *json.RawMessage) error {
	if len(*target) == 0 {
		return nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(*target))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	*target = json.RawMessage(payload)
	return nil
}

func normalizeOptionalTime(value **time.Time) {
	if *value != nil {
		normalized := (*value).UTC()
		*value = &normalized
	}
}

func sortStrings(values []string) { sort.Strings(values) }

func parseLegacyAuditID(requestID string) int64 {
	const prefix = "legacy-policy-audit-"
	if !strings.HasPrefix(requestID, prefix) {
		return 0
	}
	value, _ := strconv.ParseInt(strings.TrimPrefix(requestID, prefix), 10, 64)
	return value
}
