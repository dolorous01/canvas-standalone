package project

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/dolorous01/canvas-standalone/backend/internal/publicid"
)

var (
	ErrNotFound        = errors.New("project not found")
	ErrVersionConflict = errors.New("project version conflict")
	ErrInvalidDocument = errors.New("project document is invalid")
)

const (
	MaxDocumentBytes = 2 << 20
	MaxReferences    = 1000
	maxDepth         = 64
	maxValues        = 100000
)

type Project struct {
	ID             int64           `json:"-"`
	PublicID       string          `json:"id"`
	ExternalUserID int64           `json:"-"`
	Name           string          `json:"name"`
	Document       json.RawMessage `json:"document"`
	Version        int64           `json:"version"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type AssetReference struct {
	PublicAssetID string
	NodeID        string
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (repository *Repository) List(ctx context.Context, ownerID int64, limit, offset int) ([]Project, error) {
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := repository.db.QueryContext(ctx, `
		SELECT id, public_id, external_user_id, name, document, version, created_at, updated_at
		FROM canvas_projects
		WHERE external_user_id = $1 AND deleted_at IS NULL
		ORDER BY updated_at DESC, id DESC LIMIT $2 OFFSET $3`, ownerID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()
	projects := make([]Project, 0)
	for rows.Next() {
		item, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects: %w", err)
	}
	return projects, nil
}

func (repository *Repository) Get(ctx context.Context, ownerID int64, id string) (Project, error) {
	return scanProject(repository.db.QueryRowContext(ctx, `
		SELECT id, public_id, external_user_id, name, document, version, created_at, updated_at
		FROM canvas_projects
		WHERE external_user_id = $1 AND public_id = $2 AND deleted_at IS NULL`, ownerID, id))
}

func (repository *Repository) Create(ctx context.Context, ownerID int64, requestedID, name string, document json.RawMessage, references []AssetReference) (Project, error) {
	identifier := strings.TrimSpace(requestedID)
	if identifier == "" {
		var err error
		identifier, err = publicid.New("project")
		if err != nil {
			return Project{}, err
		}
	} else if !publicid.Valid(identifier) {
		return Project{}, ErrInvalidDocument
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, fmt.Errorf("begin project create: %w", err)
	}
	defer tx.Rollback()
	item, err := scanProject(tx.QueryRowContext(ctx, `
		INSERT INTO canvas_projects (public_id, external_user_id, name, document)
		VALUES ($1, $2, $3, $4)
		RETURNING id, public_id, external_user_id, name, document, version, created_at, updated_at`,
		identifier, ownerID, strings.TrimSpace(name), document))
	if err != nil {
		return Project{}, err
	}
	if err := replaceReferences(ctx, tx, item.ID, ownerID, references); err != nil {
		return Project{}, err
	}
	if err := tx.Commit(); err != nil {
		return Project{}, fmt.Errorf("commit project create: %w", err)
	}
	return item, nil
}

func (repository *Repository) Update(ctx context.Context, ownerID int64, id string, expectedVersion int64, name string, document json.RawMessage, references []AssetReference) (Project, error) {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, fmt.Errorf("begin project update: %w", err)
	}
	defer tx.Rollback()
	item, err := scanProject(tx.QueryRowContext(ctx, `
		UPDATE canvas_projects
		SET name = $4, document = $5, version = version + 1, updated_at = NOW()
		WHERE external_user_id = $1 AND public_id = $2 AND version = $3 AND deleted_at IS NULL
		RETURNING id, public_id, external_user_id, name, document, version, created_at, updated_at`,
		ownerID, id, expectedVersion, strings.TrimSpace(name), document))
	if errors.Is(err, ErrNotFound) {
		var exists bool
		lookupErr := tx.QueryRowContext(ctx, `
			SELECT EXISTS(SELECT 1 FROM canvas_projects WHERE external_user_id = $1 AND public_id = $2 AND deleted_at IS NULL)`,
			ownerID, id).Scan(&exists)
		if lookupErr != nil {
			return Project{}, fmt.Errorf("resolve project conflict: %w", lookupErr)
		}
		if exists {
			return Project{}, ErrVersionConflict
		}
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, err
	}
	if err := replaceReferences(ctx, tx, item.ID, ownerID, references); err != nil {
		return Project{}, err
	}
	if err := tx.Commit(); err != nil {
		return Project{}, fmt.Errorf("commit project update: %w", err)
	}
	return item, nil
}

func (repository *Repository) Delete(ctx context.Context, ownerID int64, id string) error {
	result, err := repository.db.ExecContext(ctx, `
		UPDATE canvas_projects SET deleted_at = NOW(), updated_at = NOW()
		WHERE external_user_id = $1 AND public_id = $2 AND deleted_at IS NULL`, ownerID, id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read project delete result: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func ValidateDocument(payload json.RawMessage) ([]AssetReference, error) {
	if len(payload) == 0 || len(payload) > MaxDocumentBytes {
		return nil, ErrInvalidDocument
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		return nil, ErrInvalidDocument
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidDocument
	}
	schema, ok := root["schema_version"].(json.Number)
	if !ok || (schema.String() != "1" && schema.String() != "2") {
		return nil, ErrInvalidDocument
	}
	nodes, ok := root["nodes"].([]any)
	if !ok {
		return nil, ErrInvalidDocument
	}
	count := 0
	if err := validateValue(root, 0, &count); err != nil {
		return nil, err
	}
	references := make([]AssetReference, 0)
	seen := make(map[string]struct{})
	for _, rawNode := range nodes {
		node, ok := rawNode.(map[string]any)
		if !ok {
			return nil, ErrInvalidDocument
		}
		nodeID, ok := node["id"].(string)
		if !ok || strings.TrimSpace(nodeID) == "" || len(nodeID) > 128 {
			return nil, ErrInvalidDocument
		}
		assetIDs := make([]string, 0)
		collectAssetIDs(node, &assetIDs)
		for _, assetID := range assetIDs {
			if !publicid.Valid(assetID) {
				return nil, ErrInvalidDocument
			}
			key := nodeID + "\x00" + assetID
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			references = append(references, AssetReference{PublicAssetID: assetID, NodeID: nodeID})
			if len(references) > MaxReferences {
				return nil, ErrInvalidDocument
			}
		}
	}
	return references, nil
}

func validateValue(value any, depth int, count *int) error {
	if depth > maxDepth {
		return ErrInvalidDocument
	}
	*count++
	if *count > maxValues {
		return ErrInvalidDocument
	}
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
			switch normalized {
			case "api_key", "authorization", "bearer_token", "access_token", "refresh_token", "provider_url", "object_key", "signed_url", "database_id", "password", "secret":
				return ErrInvalidDocument
			}
			if strings.HasPrefix(normalized, "on") && len(normalized) > 2 {
				return ErrInvalidDocument
			}
			if err := validateValue(child, depth+1, count); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range typed {
			if err := validateValue(child, depth+1, count); err != nil {
				return err
			}
		}
	case string:
		if len(typed) > 256<<10 {
			return ErrInvalidDocument
		}
		lower := strings.ToLower(strings.TrimSpace(typed))
		if strings.HasPrefix(lower, "data:image/") || strings.HasPrefix(lower, "javascript:") || strings.Contains(lower, "<script") || strings.HasPrefix(lower, "bearer ") || strings.HasPrefix(lower, "sk-") {
			return ErrInvalidDocument
		}
	case nil, bool, json.Number:
	default:
		return ErrInvalidDocument
	}
	return nil
}

func collectAssetIDs(value any, target *[]string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "asset_id" {
				if assetID, ok := child.(string); ok && assetID != "" {
					*target = append(*target, assetID)
				}
				continue
			}
			collectAssetIDs(child, target)
		}
	case []any:
		for _, child := range typed {
			collectAssetIDs(child, target)
		}
	}
}

func replaceReferences(ctx context.Context, tx *sql.Tx, projectID, ownerID int64, references []AssetReference) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM canvas_project_asset_refs WHERE project_id = $1`, projectID); err != nil {
		return fmt.Errorf("delete project asset references: %w", err)
	}
	for _, reference := range references {
		var assetID int64
		err := tx.QueryRowContext(ctx, `
			SELECT id FROM canvas_assets
			WHERE external_user_id = $1 AND public_id = $2 AND deleted_at IS NULL`, ownerID, reference.PublicAssetID).Scan(&assetID)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrInvalidDocument
		}
		if err != nil {
			return fmt.Errorf("resolve project asset reference: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_project_asset_refs (project_id, asset_id, node_id)
			VALUES ($1, $2, $3)`, projectID, assetID, reference.NodeID); err != nil {
			return fmt.Errorf("insert project asset reference: %w", err)
		}
	}
	return nil
}

type scanner interface {
	Scan(...any) error
}

func scanProject(row scanner) (Project, error) {
	var item Project
	if err := row.Scan(&item.ID, &item.PublicID, &item.ExternalUserID, &item.Name, &item.Document, &item.Version, &item.CreatedAt, &item.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Project{}, ErrNotFound
		}
		return Project{}, fmt.Errorf("scan project: %w", err)
	}
	return item, nil
}
