package policy

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalid         = errors.New("model policy is invalid")
	ErrVersionConflict = errors.New("model policy version conflict")
)

type Model struct {
	Model      string          `json:"model"`
	Enabled    bool            `json:"enabled"`
	Position   int             `json:"position"`
	Capability json.RawMessage `json:"capability"`
}

type Policy struct {
	Enabled bool    `json:"enabled"`
	Version int64   `json:"version"`
	Models  []Model `json:"models"`
}

type Audit struct {
	ID                     int64           `json:"id"`
	OperatorExternalUserID int64           `json:"operator_external_user_id"`
	RequestID              string          `json:"request_id"`
	OldVersion             int64           `json:"old_version"`
	NewVersion             int64           `json:"new_version"`
	Before                 json.RawMessage `json:"before"`
	After                  json.RawMessage `json:"after"`
	CreatedAt              time.Time       `json:"created_at"`
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (repository *Repository) Get(ctx context.Context) (Policy, error) {
	var result Policy
	if err := repository.db.QueryRowContext(ctx, `SELECT enabled, version FROM canvas_model_policies WHERE id = 1`).Scan(&result.Enabled, &result.Version); err != nil {
		return Policy{}, fmt.Errorf("read model policy: %w", err)
	}
	rows, err := repository.db.QueryContext(ctx, `
		SELECT model, enabled, position, capability
		FROM canvas_model_policy_items WHERE policy_id = 1 ORDER BY position, id`)
	if err != nil {
		return Policy{}, fmt.Errorf("read model policy items: %w", err)
	}
	defer rows.Close()
	result.Models = make([]Model, 0)
	for rows.Next() {
		var item Model
		if err := rows.Scan(&item.Model, &item.Enabled, &item.Position, &item.Capability); err != nil {
			return Policy{}, fmt.Errorf("scan model policy item: %w", err)
		}
		result.Models = append(result.Models, item)
	}
	if err := rows.Err(); err != nil {
		return Policy{}, fmt.Errorf("iterate model policy items: %w", err)
	}
	return result, nil
}

func (repository *Repository) Update(ctx context.Context, operatorID int64, requestID string, expectedVersion int64, enabled bool, models []Model) (Policy, error) {
	if operatorID <= 0 || expectedVersion <= 0 || strings.TrimSpace(requestID) == "" {
		return Policy{}, ErrInvalid
	}
	models, err := validateModels(models)
	if err != nil {
		return Policy{}, err
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return Policy{}, fmt.Errorf("begin model policy update: %w", err)
	}
	defer tx.Rollback()
	var current Policy
	if err := tx.QueryRowContext(ctx, `SELECT enabled, version FROM canvas_model_policies WHERE id = 1 FOR UPDATE`).Scan(&current.Enabled, &current.Version); err != nil {
		return Policy{}, fmt.Errorf("lock model policy: %w", err)
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT model, enabled, position, capability FROM canvas_model_policy_items
		WHERE policy_id = 1 ORDER BY position, id`)
	if err != nil {
		return Policy{}, fmt.Errorf("read current model policy: %w", err)
	}
	for rows.Next() {
		var item Model
		if err := rows.Scan(&item.Model, &item.Enabled, &item.Position, &item.Capability); err != nil {
			_ = rows.Close()
			return Policy{}, fmt.Errorf("scan current model policy: %w", err)
		}
		current.Models = append(current.Models, item)
	}
	if err := rows.Close(); err != nil {
		return Policy{}, fmt.Errorf("close current model policy: %w", err)
	}
	if current.Version != expectedVersion {
		return Policy{}, ErrVersionConflict
	}
	next := Policy{Enabled: enabled, Version: current.Version + 1, Models: models}
	beforeJSON, err := json.Marshal(current)
	if err != nil {
		return Policy{}, fmt.Errorf("encode current model policy: %w", err)
	}
	afterJSON, err := json.Marshal(next)
	if err != nil {
		return Policy{}, fmt.Errorf("encode next model policy: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE canvas_model_policies SET enabled = $1, version = $2, updated_at = NOW() WHERE id = 1`, enabled, next.Version); err != nil {
		return Policy{}, fmt.Errorf("update model policy: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM canvas_model_policy_items WHERE policy_id = 1`); err != nil {
		return Policy{}, fmt.Errorf("replace model policy items: %w", err)
	}
	for _, item := range models {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_model_policy_items (policy_id, model, enabled, position, capability)
			VALUES (1, $1, $2, $3, $4)`, item.Model, item.Enabled, item.Position, item.Capability); err != nil {
			return Policy{}, fmt.Errorf("insert model policy item: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO canvas_model_policy_audits (
			operator_external_user_id, request_id, old_version, new_version, before_value, after_value
		) VALUES ($1, $2, $3, $4, $5, $6)`, operatorID, requestID, current.Version, next.Version, beforeJSON, afterJSON); err != nil {
		return Policy{}, fmt.Errorf("audit model policy update: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO canvas_audit_events (event_type, external_user_id, request_id, detail)
		VALUES ('model_policy_updated', $1, $2, jsonb_build_object('old_version', $3::BIGINT, 'new_version', $4::BIGINT))`,
		operatorID, requestID, current.Version, next.Version); err != nil {
		return Policy{}, fmt.Errorf("audit model policy event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Policy{}, fmt.Errorf("commit model policy update: %w", err)
	}
	return next, nil
}

func (repository *Repository) ListAudits(ctx context.Context, limit int) ([]Audit, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	rows, err := repository.db.QueryContext(ctx, `
		SELECT id, operator_external_user_id, request_id, old_version, new_version,
			before_value, after_value, created_at
		FROM canvas_model_policy_audits ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list model policy audits: %w", err)
	}
	defer rows.Close()
	result := make([]Audit, 0)
	for rows.Next() {
		var item Audit
		if err := rows.Scan(&item.ID, &item.OperatorExternalUserID, &item.RequestID, &item.OldVersion, &item.NewVersion, &item.Before, &item.After, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan model policy audit: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func validateModels(models []Model) ([]Model, error) {
	if len(models) == 0 || len(models) > 100 {
		return nil, ErrInvalid
	}
	result := make([]Model, len(models))
	seen := make(map[string]struct{}, len(models))
	for index, item := range models {
		item.Model = strings.TrimSpace(item.Model)
		if item.Model == "" || len(item.Model) > 128 || strings.ContainsAny(item.Model, "\r\n") || item.Position != index {
			return nil, ErrInvalid
		}
		key := strings.ToLower(item.Model)
		if _, exists := seen[key]; exists {
			return nil, ErrInvalid
		}
		seen[key] = struct{}{}
		var capability struct {
			MediaKind      string `json:"media_kind"`
			Generation     bool   `json:"generation"`
			Edit           bool   `json:"edit"`
			MaxInputImages int    `json:"max_input_images"`
			MaxOutputs     int    `json:"max_outputs"`
		}
		if len(item.Capability) == 0 || len(item.Capability) > 64<<10 || json.Unmarshal(item.Capability, &capability) != nil ||
			capability.MediaKind != "image" || (!capability.Generation && !capability.Edit) || capability.MaxInputImages < 0 || capability.MaxInputImages > 32 || capability.MaxOutputs <= 0 || capability.MaxOutputs > 10 {
			return nil, ErrInvalid
		}
		var object map[string]any
		if json.Unmarshal(item.Capability, &object) != nil || object == nil {
			return nil, ErrInvalid
		}
		result[index] = item
	}
	return result, nil
}
