package job

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dolorous01/canvas-standalone/backend/internal/asset"
	"github.com/dolorous01/canvas-standalone/backend/internal/credential"
	"github.com/dolorous01/canvas-standalone/backend/internal/gateway"
	"github.com/dolorous01/canvas-standalone/backend/internal/policy"
	"github.com/dolorous01/canvas-standalone/backend/internal/project"
	"github.com/dolorous01/canvas-standalone/backend/internal/publicid"
	"github.com/dolorous01/canvas-standalone/backend/internal/secretfile"
)

var (
	ErrNotFound            = errors.New("job not found")
	ErrInvalid             = errors.New("job request is invalid")
	ErrIdempotencyConflict = errors.New("idempotency key was reused for another request")
	ErrCanceled            = errors.New("job was canceled before dispatch")
)

type Parameters struct {
	Size              string `json:"size,omitempty"`
	AspectRatio       string `json:"aspect_ratio,omitempty"`
	Resolution        string `json:"resolution,omitempty"`
	N                 int    `json:"n"`
	Quality           string `json:"quality,omitempty"`
	OutputFormat      string `json:"output_format,omitempty"`
	Background        string `json:"background,omitempty"`
	OutputCompression *int   `json:"output_compression,omitempty"`
}

type CreateInput struct {
	ProjectPublicID string     `json:"project_id"`
	ClientNodeID    string     `json:"client_node_id"`
	Operation       string     `json:"operation"`
	ExternalKeyID   int64      `json:"api_key_id"`
	SelectedModel   string     `json:"selected_model"`
	Prompt          string     `json:"prompt"`
	InputAssetIDs   []string   `json:"input_asset_ids"`
	MaskAssetID     string     `json:"mask_asset_id,omitempty"`
	Parameters      Parameters `json:"parameters"`
}

type Result struct {
	Index    int    `json:"index"`
	Status   string `json:"status"`
	AssetID  string `json:"asset_id,omitempty"`
	URL      string `json:"url,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Size     string `json:"size,omitempty"`
}

type PublicError struct {
	Type      string `json:"type"`
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type Job struct {
	ID              int64        `json:"-"`
	PublicID        string       `json:"id"`
	ExternalUserID  int64        `json:"-"`
	ProjectID       int64        `json:"-"`
	CredentialID    int64        `json:"-"`
	Status          string       `json:"status"`
	Phase           string       `json:"phase,omitempty"`
	Operation       string       `json:"operation"`
	ClientNodeID    string       `json:"client_node_id,omitempty"`
	SelectedModel   string       `json:"selected_model,omitempty"`
	SuccessfulModel string       `json:"successful_model,omitempty"`
	PolicyVersion   int64        `json:"policy_version,omitempty"`
	AttemptPlan     []string     `json:"attempt_plan"`
	AttemptPosition int          `json:"attempt_position,omitempty"`
	RequestedCount  int          `json:"requested_count"`
	CompletedCount  int          `json:"completed_count"`
	Results         []Result     `json:"results"`
	Error           *PublicError `json:"error,omitempty"`
	CreatedAt       int64        `json:"created_at"`
	UpdatedAt       int64        `json:"updated_at"`
}

type storedRequest struct {
	ProjectPublicID string     `json:"project_id"`
	Operation       string     `json:"operation"`
	Model           string     `json:"model"`
	Prompt          string     `json:"prompt"`
	InputAssetIDs   []string   `json:"input_asset_ids"`
	MaskAssetID     string     `json:"mask_asset_id,omitempty"`
	Parameters      Parameters `json:"parameters"`
}

type Service struct {
	db          *sql.DB
	credentials *credential.Repository
	projects    *project.Repository
	policies    *policy.Repository
	assets      *asset.Service
}

func NewService(db *sql.DB, credentials *credential.Repository, projects *project.Repository, policies *policy.Repository, assets *asset.Service) *Service {
	return &Service{db: db, credentials: credentials, projects: projects, policies: policies, assets: assets}
}

func (service *Service) Create(ctx context.Context, ownerID int64, input CreateInput, idempotencyKey string) (Job, bool, error) {
	input.ProjectPublicID = strings.TrimSpace(input.ProjectPublicID)
	input.ClientNodeID = strings.TrimSpace(input.ClientNodeID)
	input.Operation = strings.ToLower(strings.TrimSpace(input.Operation))
	input.SelectedModel = strings.TrimSpace(input.SelectedModel)
	input.Prompt = strings.TrimSpace(input.Prompt)
	input.MaskAssetID = strings.TrimSpace(input.MaskAssetID)
	idempotencyKey = strings.TrimSpace(idempotencyKey)
	if ownerID <= 0 || !publicid.Valid(input.ProjectPublicID) || input.ExternalKeyID <= 0 ||
		input.ClientNodeID == "" || len(input.ClientNodeID) > 128 || !utf8.ValidString(input.ClientNodeID) ||
		(input.Operation != "generation" && input.Operation != "edit") || input.SelectedModel == "" || len(input.SelectedModel) > 128 ||
		input.Prompt == "" || len(input.Prompt) > 32<<10 || !utf8.ValidString(input.Prompt) ||
		idempotencyKey == "" || len(idempotencyKey) > 128 || strings.ContainsAny(idempotencyKey, "\r\n") {
		return Job{}, false, ErrInvalid
	}
	projectRecord, err := service.projects.Get(ctx, ownerID, input.ProjectPublicID)
	if err != nil {
		return Job{}, false, err
	}
	credentialRecord, err := service.credentials.GetByExternalKey(ctx, ownerID, input.ExternalKeyID)
	if err != nil || credentialRecord.Status != "active" {
		return Job{}, false, ErrInvalid
	}
	modelPolicy, err := service.policies.Get(ctx)
	if err != nil {
		return Job{}, false, err
	}
	capability, err := selectCapability(modelPolicy, input.SelectedModel, input.Operation)
	if err != nil {
		return Job{}, false, err
	}
	input.Parameters = applyDefaults(input.Parameters, capability)
	if err := validateParameters(input, capability); err != nil {
		return Job{}, false, err
	}
	inputAssets := make([]asset.Asset, 0, len(input.InputAssetIDs)+1)
	seen := make(map[string]struct{})
	for _, publicID := range input.InputAssetIDs {
		publicID = strings.TrimSpace(publicID)
		if _, duplicate := seen[publicID]; duplicate || !publicid.Valid(publicID) {
			return Job{}, false, ErrInvalid
		}
		seen[publicID] = struct{}{}
		item, err := service.assets.Get(ctx, ownerID, publicID)
		if err != nil || item.MediaKind != "image" {
			return Job{}, false, ErrInvalid
		}
		inputAssets = append(inputAssets, item)
	}
	var mask *asset.Asset
	if input.MaskAssetID != "" {
		item, err := service.assets.Get(ctx, ownerID, input.MaskAssetID)
		if err != nil || item.MediaKind != "image" {
			return Job{}, false, ErrInvalid
		}
		mask = &item
	}
	requestValue := storedRequest{
		ProjectPublicID: projectRecord.PublicID, Operation: input.Operation, Model: input.SelectedModel,
		Prompt: input.Prompt, InputAssetIDs: append([]string(nil), input.InputAssetIDs...),
		MaskAssetID: input.MaskAssetID, Parameters: input.Parameters,
	}
	requestJSON, err := json.Marshal(requestValue)
	if err != nil {
		return Job{}, false, ErrInvalid
	}
	requestHash := sha256.Sum256(requestJSON)
	idempotencyHash := sha256.Sum256([]byte(idempotencyKey))
	identifier, err := publicid.New("job")
	if err != nil {
		return Job{}, false, err
	}
	attemptPlanJSON, err := json.Marshal([]string{input.SelectedModel})
	if err != nil {
		return Job{}, false, ErrInvalid
	}
	tx, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, false, fmt.Errorf("begin job create: %w", err)
	}
	defer tx.Rollback()
	var jobID int64
	created := true
	err = tx.QueryRowContext(ctx, `
		INSERT INTO canvas_jobs (
			public_id, external_user_id, project_id, credential_id, client_node_id, operation,
			selected_model, policy_version, requested_count, request, request_digest,
			idempotency_key_hash, attempt_plan
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		ON CONFLICT (external_user_id, idempotency_key_hash) DO NOTHING
		RETURNING id`, identifier, ownerID, projectRecord.ID, credentialRecord.ID, input.ClientNodeID,
		input.Operation, input.SelectedModel, modelPolicy.Version, input.Parameters.N, requestJSON,
		hex.EncodeToString(requestHash[:]), hex.EncodeToString(idempotencyHash[:]), attemptPlanJSON,
	).Scan(&jobID)
	if errors.Is(err, sql.ErrNoRows) {
		created = false
		var existingDigest string
		if err := tx.QueryRowContext(ctx, `
			SELECT id, request_digest FROM canvas_jobs
			WHERE external_user_id = $1 AND idempotency_key_hash = $2`, ownerID, hex.EncodeToString(idempotencyHash[:])).Scan(&jobID, &existingDigest); err != nil {
			return Job{}, false, fmt.Errorf("read idempotent job: %w", err)
		}
		if existingDigest != hex.EncodeToString(requestHash[:]) {
			return Job{}, false, ErrIdempotencyConflict
		}
	} else if err != nil {
		return Job{}, false, fmt.Errorf("create job: %w", err)
	}
	if created {
		for index, item := range inputAssets {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO canvas_job_inputs (job_id, position, asset_id, kind, sha256)
				VALUES ($1, $2, $3, 'image', $4)`, jobID, index, item.ID, item.SHA256); err != nil {
				return Job{}, false, fmt.Errorf("insert job input: %w", err)
			}
		}
		if mask != nil {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO canvas_job_inputs (job_id, position, asset_id, kind, sha256)
				VALUES ($1, 0, $2, 'mask', $3)`, jobID, mask.ID, mask.SHA256); err != nil {
				return Job{}, false, fmt.Errorf("insert job mask: %w", err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return Job{}, false, fmt.Errorf("commit job create: %w", err)
	}
	result, err := service.getByInternalID(ctx, ownerID, jobID)
	return result, created, err
}

func (service *Service) Get(ctx context.Context, ownerID int64, publicID string) (Job, error) {
	return service.scanAndLoad(ctx, service.db.QueryRowContext(ctx, jobSelect+` WHERE job.external_user_id = $1 AND job.public_id = $2`, ownerID, publicID))
}

func (service *Service) getByInternalID(ctx context.Context, ownerID, id int64) (Job, error) {
	return service.scanAndLoad(ctx, service.db.QueryRowContext(ctx, jobSelect+` WHERE job.external_user_id = $1 AND job.id = $2`, ownerID, id))
}

func (service *Service) Cancel(ctx context.Context, ownerID int64, publicID string) (Job, error) {
	result, err := service.db.ExecContext(ctx, `
		UPDATE canvas_jobs SET
			status = CASE WHEN status = 'queued' THEN 'canceled' ELSE status END,
			cancel_requested_at = NOW(),
			finished_at = CASE WHEN status = 'queued' THEN NOW() ELSE finished_at END,
			updated_at = NOW()
		WHERE external_user_id = $1 AND public_id = $2
			AND status IN ('queued', 'running')`, ownerID, publicID)
	if err != nil {
		return Job{}, fmt.Errorf("cancel job: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return Job{}, fmt.Errorf("read job cancel result: %w", err)
	}
	if count == 0 {
		job, getErr := service.Get(ctx, ownerID, publicID)
		if getErr != nil {
			return Job{}, getErr
		}
		return job, nil
	}
	return service.Get(ctx, ownerID, publicID)
}

func (service *Service) scanAndLoad(ctx context.Context, row scanner) (Job, error) {
	item, err := scanJob(row)
	if err != nil {
		return Job{}, err
	}
	rows, err := service.db.QueryContext(ctx, `
		SELECT result.position, result.status, COALESCE(asset.public_id, ''),
			COALESCE(asset.mime_type, ''), COALESCE(result.size, '')
		FROM canvas_job_results result
		LEFT JOIN canvas_assets asset ON asset.id = result.asset_id
		WHERE result.job_id = $1 ORDER BY result.position`, item.ID)
	if err != nil {
		return Job{}, fmt.Errorf("read job results: %w", err)
	}
	defer rows.Close()
	item.Results = make([]Result, 0)
	for rows.Next() {
		var result Result
		if err := rows.Scan(&result.Index, &result.Status, &result.AssetID, &result.MIMEType, &result.Size); err != nil {
			return Job{}, fmt.Errorf("scan job result: %w", err)
		}
		if result.AssetID != "" {
			result.URL = "/canvas-api/v1/assets/" + result.AssetID
		}
		item.Results = append(item.Results, result)
	}
	return item, rows.Err()
}

const jobSelect = `
	SELECT job.id, job.public_id, job.external_user_id, job.project_id, COALESCE(job.credential_id, 0),
		job.status, job.phase, job.operation, job.client_node_id, job.selected_model,
		COALESCE(job.successful_model, ''), job.policy_version, job.attempt_plan,
		job.attempt_position, job.requested_count, job.completed_count,
		job.error_type, job.error_code, job.error_message, job.error_retryable,
		(EXTRACT(EPOCH FROM job.created_at) * 1000)::BIGINT,
		(EXTRACT(EPOCH FROM job.updated_at) * 1000)::BIGINT
	FROM canvas_jobs job`

type scanner interface {
	Scan(...any) error
}

func scanJob(row scanner) (Job, error) {
	var item Job
	var attemptPlan []byte
	var errorType, errorCode, errorMessage sql.NullString
	var errorRetryable bool
	if err := row.Scan(
		&item.ID, &item.PublicID, &item.ExternalUserID, &item.ProjectID, &item.CredentialID,
		&item.Status, &item.Phase, &item.Operation, &item.ClientNodeID, &item.SelectedModel,
		&item.SuccessfulModel, &item.PolicyVersion, &attemptPlan, &item.AttemptPosition,
		&item.RequestedCount, &item.CompletedCount, &errorType, &errorCode, &errorMessage,
		&errorRetryable, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Job{}, ErrNotFound
		}
		return Job{}, fmt.Errorf("scan job: %w", err)
	}
	if err := json.Unmarshal(attemptPlan, &item.AttemptPlan); err != nil {
		return Job{}, fmt.Errorf("decode job attempt plan: %w", err)
	}
	if item.AttemptPlan == nil {
		item.AttemptPlan = []string{}
	}
	if errorCode.Valid || errorMessage.Valid || errorType.Valid {
		item.Error = &PublicError{Type: errorType.String, Code: errorCode.String, Message: errorMessage.String, Retryable: errorRetryable}
	}
	return item, nil
}

type capability struct {
	Generation        bool       `json:"generation"`
	Edit              bool       `json:"edit"`
	MultiImage        bool       `json:"multi_image"`
	Mask              bool       `json:"mask"`
	MaxInputImages    int        `json:"max_input_images"`
	MaxOutputs        int        `json:"max_outputs"`
	Sizes             []string   `json:"sizes"`
	AspectRatios      []string   `json:"aspect_ratios"`
	Resolutions       []string   `json:"resolutions"`
	Qualities         []string   `json:"qualities"`
	OutputFormats     []string   `json:"output_formats"`
	Backgrounds       []string   `json:"backgrounds"`
	OutputCompression bool       `json:"output_compression"`
	Defaults          Parameters `json:"defaults"`
}

func selectCapability(modelPolicy policy.Policy, model, operation string) (capability, error) {
	if !modelPolicy.Enabled {
		return capability{}, ErrInvalid
	}
	for _, item := range modelPolicy.Models {
		if item.Enabled && item.Model == model {
			var result capability
			if err := json.Unmarshal(item.Capability, &result); err != nil {
				return capability{}, fmt.Errorf("decode model capability: %w", err)
			}
			if (operation == "generation" && !result.Generation) || (operation == "edit" && !result.Edit) {
				return capability{}, ErrInvalid
			}
			return result, nil
		}
	}
	return capability{}, ErrInvalid
}

func applyDefaults(parameters Parameters, capability capability) Parameters {
	if parameters.N <= 0 {
		parameters.N = 1
	}
	if parameters.Size == "" {
		parameters.Size = capability.Defaults.Size
	}
	if parameters.AspectRatio == "" {
		parameters.AspectRatio = capability.Defaults.AspectRatio
	}
	if parameters.Resolution == "" {
		parameters.Resolution = capability.Defaults.Resolution
	}
	if parameters.Quality == "" {
		parameters.Quality = capability.Defaults.Quality
	}
	if parameters.OutputFormat == "" {
		parameters.OutputFormat = capability.Defaults.OutputFormat
	}
	if parameters.Background == "" {
		parameters.Background = capability.Defaults.Background
	}
	return parameters
}

func validateParameters(input CreateInput, capability capability) error {
	parameters := input.Parameters
	if parameters.N <= 0 || parameters.N > 10 || (capability.MaxOutputs > 0 && parameters.N > capability.MaxOutputs) {
		return ErrInvalid
	}
	if !allowed(parameters.Size, capability.Sizes) || !allowed(parameters.AspectRatio, capability.AspectRatios) ||
		!allowed(parameters.Resolution, capability.Resolutions) || !allowed(parameters.Quality, capability.Qualities) ||
		!allowed(parameters.OutputFormat, capability.OutputFormats) || !allowed(parameters.Background, capability.Backgrounds) {
		return ErrInvalid
	}
	if parameters.Background == "transparent" && parameters.OutputFormat == "jpeg" {
		return ErrInvalid
	}
	if parameters.OutputCompression != nil && (!capability.OutputCompression || *parameters.OutputCompression < 0 || *parameters.OutputCompression > 100 || (parameters.OutputFormat != "jpeg" && parameters.OutputFormat != "webp")) {
		return ErrInvalid
	}
	if input.Operation == "generation" {
		if len(input.InputAssetIDs) != 0 || input.MaskAssetID != "" {
			return ErrInvalid
		}
		return nil
	}
	if len(input.InputAssetIDs) == 0 || (!capability.MultiImage && len(input.InputAssetIDs) > 1) || (capability.MaxInputImages > 0 && len(input.InputAssetIDs) > capability.MaxInputImages) || (input.MaskAssetID != "" && !capability.Mask) {
		return ErrInvalid
	}
	return nil
}

func allowed(value string, options []string) bool {
	if value == "" {
		return true
	}
	for _, option := range options {
		if strings.EqualFold(strings.TrimSpace(value), strings.TrimSpace(option)) {
			return true
		}
	}
	return false
}

type Claimed struct {
	Job             Job
	ProjectPublicID string
	Request         storedRequest
}

func (service *Service) Claim(ctx context.Context, workerID string, lease time.Duration) (Claimed, bool, error) {
	if workerID == "" || lease < 10*time.Second {
		return Claimed{}, false, ErrInvalid
	}
	tx, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return Claimed{}, false, fmt.Errorf("begin job claim: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE canvas_jobs SET status = 'indeterminate', error_type = 'worker', error_code = 'lease_lost_after_dispatch',
			error_message = 'The worker lost the result after dispatch.', error_retryable = FALSE,
			finished_at = NOW(), updated_at = NOW(), lease_owner = NULL, lease_expires_at = NULL
		WHERE status = 'running' AND lease_expires_at < NOW() AND upstream_started_at IS NOT NULL`); err != nil {
		return Claimed{}, false, fmt.Errorf("resolve dispatched expired jobs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE canvas_jobs SET status = 'queued', phase = 'preflight', lease_owner = NULL, lease_expires_at = NULL, updated_at = NOW()
		WHERE status = 'running' AND lease_expires_at < NOW() AND upstream_started_at IS NULL AND cancel_requested_at IS NULL`); err != nil {
		return Claimed{}, false, fmt.Errorf("recover pre-dispatch jobs: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE canvas_jobs SET status = 'canceled', finished_at = NOW(), updated_at = NOW(), lease_owner = NULL, lease_expires_at = NULL
		WHERE status = 'running' AND lease_expires_at < NOW() AND upstream_started_at IS NULL AND cancel_requested_at IS NOT NULL`); err != nil {
		return Claimed{}, false, fmt.Errorf("cancel expired jobs: %w", err)
	}
	var claimed Claimed
	var requestJSON []byte
	var errorType, errorCode, errorMessage sql.NullString
	var errorRetryable bool
	err = tx.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT id FROM canvas_jobs
			WHERE status = 'queued' AND legacy_imported = FALSE AND cancel_requested_at IS NULL
			ORDER BY created_at, id FOR UPDATE SKIP LOCKED LIMIT 1
		), claimed AS (
			UPDATE canvas_jobs job SET status = 'running', phase = 'validating', lease_owner = $1,
				lease_expires_at = NOW() + $2::INTERVAL, updated_at = NOW()
			FROM candidate WHERE job.id = candidate.id RETURNING job.*
		)
		SELECT claimed.id, claimed.public_id, claimed.external_user_id, claimed.project_id,
			COALESCE(claimed.credential_id, 0), claimed.status, claimed.phase, claimed.operation,
			claimed.client_node_id, claimed.selected_model, COALESCE(claimed.successful_model, ''),
			claimed.policy_version, claimed.attempt_plan, claimed.attempt_position,
			claimed.requested_count, claimed.completed_count, claimed.error_type, claimed.error_code,
			claimed.error_message, claimed.error_retryable,
			(EXTRACT(EPOCH FROM claimed.created_at) * 1000)::BIGINT,
			(EXTRACT(EPOCH FROM claimed.updated_at) * 1000)::BIGINT,
			project.public_id, claimed.request
		FROM claimed JOIN canvas_projects project ON project.id = claimed.project_id`, workerID, intervalLiteral(lease)).Scan(
		&claimed.Job.ID, &claimed.Job.PublicID, &claimed.Job.ExternalUserID, &claimed.Job.ProjectID,
		&claimed.Job.CredentialID, &claimed.Job.Status, &claimed.Job.Phase, &claimed.Job.Operation,
		&claimed.Job.ClientNodeID, &claimed.Job.SelectedModel, &claimed.Job.SuccessfulModel,
		&claimed.Job.PolicyVersion, &rawAttemptPlan{target: &claimed.Job.AttemptPlan}, &claimed.Job.AttemptPosition,
		&claimed.Job.RequestedCount, &claimed.Job.CompletedCount, &errorType, &errorCode,
		&errorMessage, &errorRetryable, &claimed.Job.CreatedAt, &claimed.Job.UpdatedAt,
		&claimed.ProjectPublicID, &requestJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return Claimed{}, false, err
		}
		return Claimed{}, false, nil
	}
	if err != nil {
		return Claimed{}, false, fmt.Errorf("claim job: %w", err)
	}
	if err := json.Unmarshal(requestJSON, &claimed.Request); err != nil {
		return Claimed{}, false, fmt.Errorf("decode claimed job request: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Claimed{}, false, fmt.Errorf("commit job claim: %w", err)
	}
	return claimed, true, nil
}

func (service *Service) BeginUpstream(ctx context.Context, id int64, workerID string, lease time.Duration) error {
	result, err := service.db.ExecContext(ctx, `
		UPDATE canvas_jobs SET phase = 'upstream', upstream_started_at = NOW(),
			lease_expires_at = NOW() + $3::INTERVAL, updated_at = NOW()
		WHERE id = $1 AND status = 'running' AND lease_owner = $2 AND cancel_requested_at IS NULL`, id, workerID, intervalLiteral(lease))
	if err != nil {
		return fmt.Errorf("mark job dispatch: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrCanceled
	}
	return nil
}

func (service *Service) MarkSaving(ctx context.Context, id int64, workerID, officialRequestID string, lease time.Duration) error {
	result, err := service.db.ExecContext(ctx, `
		UPDATE canvas_jobs SET phase = 'saving', official_request_id = $3,
			lease_expires_at = NOW() + $4::INTERVAL, updated_at = NOW()
		WHERE id = $1 AND status = 'running' AND lease_owner = $2`, id, workerID, officialRequestID, intervalLiteral(lease))
	if err != nil {
		return fmt.Errorf("mark job saving: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (service *Service) Complete(ctx context.Context, claimed Claimed, workerID string, assets []asset.Asset) error {
	tx, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin job completion: %w", err)
	}
	defer tx.Rollback()
	for index, item := range assets {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_job_results (job_id, position, status, asset_id, mime_type, size)
			VALUES ($1, $2, 'completed', $3, $4, $5)
			ON CONFLICT (job_id, position) DO UPDATE SET
				status = EXCLUDED.status, asset_id = EXCLUDED.asset_id,
				mime_type = EXCLUDED.mime_type, size = EXCLUDED.size`,
			claimed.Job.ID, index, item.ID, item.MIMEType, claimed.Request.Parameters.Size); err != nil {
			return fmt.Errorf("save job result: %w", err)
		}
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE canvas_jobs SET status = 'completed', phase = 'saving', successful_model = selected_model,
			completed_count = $3, finished_at = NOW(), updated_at = NOW(), lease_owner = NULL, lease_expires_at = NULL
		WHERE id = $1 AND status = 'running' AND lease_owner = $2`, claimed.Job.ID, workerID, len(assets))
	if err != nil {
		return fmt.Errorf("complete job: %w", err)
	}
	count, _ := result.RowsAffected()
	if count == 0 {
		return ErrNotFound
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit job completion: %w", err)
	}
	return nil
}

func (service *Service) Fail(ctx context.Context, id int64, workerID, status, code, message string, retryable bool) error {
	if status != "failed" && status != "indeterminate" && status != "canceled" {
		status = "failed"
	}
	if len(message) > 512 {
		message = message[:512]
	}
	result, err := service.db.ExecContext(ctx, `
		UPDATE canvas_jobs SET status = $3, error_type = 'worker', error_code = $4,
			error_message = $5, error_retryable = $6, finished_at = NOW(), updated_at = NOW(),
			lease_owner = NULL, lease_expires_at = NULL
		WHERE id = $1 AND status = 'running' AND lease_owner = $2`, id, workerID, status, code, message, retryable)
	if err != nil {
		return fmt.Errorf("fail job: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read failed job update result: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

type rawAttemptPlan struct {
	target *[]string
}

func (value *rawAttemptPlan) Scan(source any) error {
	payload, ok := source.([]byte)
	if !ok {
		if text, stringOK := source.(string); stringOK {
			payload = []byte(text)
		} else {
			return errors.New("invalid attempt plan")
		}
	}
	return json.Unmarshal(payload, value.target)
}

func intervalLiteral(duration time.Duration) string {
	return strconv.FormatInt(int64(duration/time.Millisecond), 10) + " milliseconds"
}

type Processor struct {
	jobs        *Service
	credentials *credential.Repository
	keyring     *credential.Keyring
	official    *gateway.Client
	assets      *asset.Service
	workerID    string
	lease       time.Duration
}

func NewProcessor(jobs *Service, credentials *credential.Repository, keyring *credential.Keyring, official *gateway.Client, assets *asset.Service, workerID string) *Processor {
	return &Processor{jobs: jobs, credentials: credentials, keyring: keyring, official: official, assets: assets, workerID: workerID, lease: 15 * time.Minute}
}

func (processor *Processor) RunOnce(ctx context.Context) (bool, error) {
	claimed, ok, err := processor.jobs.Claim(ctx, processor.workerID, processor.lease)
	if err != nil || !ok {
		return ok, err
	}
	credentialRecord, err := processor.credentials.GetByID(ctx, claimed.Job.CredentialID)
	if err != nil || credentialRecord.Status != "active" {
		return true, processor.jobs.Fail(ctx, claimed.Job.ID, processor.workerID, "failed", "credential_unavailable", "The bound API key is unavailable.", false)
	}
	apiKey, err := processor.keyring.Decrypt(credentialRecord.ExternalUserID, credentialRecord.ExternalAPIKeyID, credentialRecord.Encrypted)
	if err != nil {
		return true, processor.jobs.Fail(ctx, claimed.Job.ID, processor.workerID, "failed", "credential_decrypt_failed", "The bound API key could not be decrypted.", false)
	}
	defer secretfile.Zero(apiKey)
	body, contentType, endpoint, err := processor.buildRequest(ctx, claimed)
	if err != nil {
		return true, processor.jobs.Fail(ctx, claimed.Job.ID, processor.workerID, "failed", "request_build_failed", "The image request could not be prepared.", false)
	}
	if err := processor.jobs.BeginUpstream(ctx, claimed.Job.ID, processor.workerID, processor.lease); err != nil {
		status := "failed"
		if errors.Is(err, ErrCanceled) {
			status = "canceled"
		}
		return true, processor.jobs.Fail(ctx, claimed.Job.ID, processor.workerID, status, "canceled_before_dispatch", "The job was canceled before dispatch.", false)
	}
	response, err := processor.official.DoImageRequest(ctx, endpoint, apiKey, contentType, bytes.NewReader(body), claimed.Job.PublicID)
	for index := range body {
		body[index] = 0
	}
	if err != nil {
		kind := gateway.ErrorKindOf(err)
		if kind == gateway.ErrorUnauthenticated || kind == gateway.ErrorForbidden {
			_ = processor.credentials.Disable(context.WithoutCancel(ctx), credentialRecord.ID)
		}
		status := "failed"
		code := string(kind)
		message := "The official image request failed."
		if kind == gateway.ErrorIndeterminate || kind == gateway.ErrorInvalidResponse {
			status, code, message = "indeterminate", "official_outcome_unknown", "The official request outcome is unknown and was not retried."
		}
		return true, processor.jobs.Fail(context.WithoutCancel(ctx), claimed.Job.ID, processor.workerID, status, code, message, kind == gateway.ErrorRateLimited || kind == gateway.ErrorUpstreamUnavailable)
	}
	if err := processor.jobs.MarkSaving(ctx, claimed.Job.ID, processor.workerID, response.RequestID, processor.lease); err != nil {
		return true, err
	}
	outputs, err := parseOutputs(response.Body, claimed.Request.Parameters.OutputFormat, claimed.Job.RequestedCount)
	for index := range response.Body {
		response.Body[index] = 0
	}
	if err != nil {
		return true, processor.jobs.Fail(context.WithoutCancel(ctx), claimed.Job.ID, processor.workerID, "indeterminate", "invalid_official_result", "The official result could not be stored and was not retried.", false)
	}
	created := make([]asset.Asset, 0, len(outputs))
	for index, output := range outputs {
		item, createErr := processor.assets.Create(ctx, claimed.Job.ExternalUserID, asset.CreateInput{
			ProjectPublicID: claimed.ProjectPublicID,
			SourceType:      "generated",
			FileName:        fmt.Sprintf("%s-%d.%s", claimed.Job.PublicID, index, output.extension),
			DeclaredMIME:    output.mimeType,
			Body:            bytes.NewReader(output.payload),
		})
		for payloadIndex := range output.payload {
			output.payload[payloadIndex] = 0
		}
		if createErr != nil {
			return true, processor.jobs.Fail(context.WithoutCancel(ctx), claimed.Job.ID, processor.workerID, "indeterminate", "result_storage_failed", "The official result could not be stored and was not retried.", false)
		}
		created = append(created, item)
	}
	return true, processor.jobs.Complete(context.WithoutCancel(ctx), claimed, processor.workerID, created)
}

func (processor *Processor) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		processed, err := processor.RunOnce(ctx)
		if err != nil {
			return err
		}
		if processed {
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (processor *Processor) buildRequest(ctx context.Context, claimed Claimed) ([]byte, string, string, error) {
	request := claimed.Request
	if request.Operation == "generation" {
		payload := map[string]any{
			"model": request.Model, "prompt": request.Prompt, "n": request.Parameters.N, "response_format": "b64_json",
		}
		addOptionalParameters(payload, request.Parameters)
		body, err := json.Marshal(payload)
		return body, "application/json", "/v1/images/generations", err
	}
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	fields := map[string]string{
		"model": request.Model, "prompt": request.Prompt, "n": strconv.Itoa(request.Parameters.N), "response_format": "b64_json",
		"size": request.Parameters.Size, "quality": request.Parameters.Quality, "output_format": request.Parameters.OutputFormat,
		"background": request.Parameters.Background,
	}
	if request.Parameters.OutputCompression != nil {
		fields["output_compression"] = strconv.Itoa(*request.Parameters.OutputCompression)
	}
	for name, value := range fields {
		if value != "" {
			if err := writer.WriteField(name, value); err != nil {
				return nil, "", "", err
			}
		}
	}
	for index, publicID := range request.InputAssetIDs {
		field := "image"
		if len(request.InputAssetIDs) > 1 {
			field = "image[]"
		}
		if err := processor.writeAssetPart(ctx, writer, claimed.Job.ExternalUserID, publicID, field, index); err != nil {
			return nil, "", "", err
		}
	}
	if request.MaskAssetID != "" {
		if err := processor.writeAssetPart(ctx, writer, claimed.Job.ExternalUserID, request.MaskAssetID, "mask", 0); err != nil {
			return nil, "", "", err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, "", "", err
	}
	if buffer.Len() > 64<<20 {
		return nil, "", "", ErrInvalid
	}
	return buffer.Bytes(), writer.FormDataContentType(), "/v1/images/edits", nil
}

func (processor *Processor) writeAssetPart(ctx context.Context, writer *multipart.Writer, ownerID int64, publicID, field string, index int) error {
	item, reader, metadata, err := processor.assets.Open(ctx, ownerID, publicID, false)
	if err != nil {
		return err
	}
	defer reader.Close()
	if metadata.Size <= 0 || metadata.Size > asset.MaxUploadBytes {
		return ErrInvalid
	}
	disposition := mime.FormatMediaType("form-data", map[string]string{
		"name": field, "filename": fmt.Sprintf("input-%d%s", index, extensionForMIME(item.MIMEType)),
	})
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", disposition)
	header.Set("Content-Type", item.MIMEType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(part, hash), io.LimitReader(reader, metadata.Size+1))
	if err != nil || written != metadata.Size || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), item.SHA256) {
		return ErrInvalid
	}
	return nil
}

func addOptionalParameters(payload map[string]any, parameters Parameters) {
	values := map[string]string{
		"size": parameters.Size, "aspect_ratio": parameters.AspectRatio, "resolution": parameters.Resolution,
		"quality": parameters.Quality, "output_format": parameters.OutputFormat, "background": parameters.Background,
	}
	for key, value := range values {
		if value != "" {
			payload[key] = value
		}
	}
	if parameters.OutputCompression != nil {
		payload["output_compression"] = *parameters.OutputCompression
	}
}

type output struct {
	payload   []byte
	mimeType  string
	extension string
}

func parseOutputs(body []byte, requestedFormat string, maxOutputs int) ([]output, error) {
	var response struct {
		Data []struct {
			Base64 string `json:"b64_json"`
			URL    string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil || len(response.Data) == 0 || len(response.Data) > maxOutputs {
		return nil, ErrInvalid
	}
	result := make([]output, 0, len(response.Data))
	for _, item := range response.Data {
		if item.Base64 == "" || item.URL != "" || base64.StdEncoding.DecodedLen(len(item.Base64)) > int(asset.MaxUploadBytes) {
			return nil, ErrInvalid
		}
		payload, err := base64.StdEncoding.Strict().DecodeString(item.Base64)
		if err != nil || len(payload) == 0 || int64(len(payload)) > asset.MaxUploadBytes {
			return nil, ErrInvalid
		}
		mimeType, extension := outputMedia(requestedFormat)
		result = append(result, output{payload: payload, mimeType: mimeType, extension: extension})
	}
	return result, nil
}

func outputMedia(format string) (string, string) {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "jpeg", "jpg":
		return "image/jpeg", "jpg"
	case "webp":
		return "image/webp", "webp"
	default:
		return "image/png", "png"
	}
}

func extensionForMIME(value string) string {
	switch value {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	default:
		return ".png"
	}
}
