package editor

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dolorous01/canvas-standalone/backend/internal/asset"
	"github.com/dolorous01/canvas-standalone/backend/internal/publicid"
)

var (
	ErrNotFound        = errors.New("editor document not found")
	ErrVersionConflict = errors.New("editor document version conflict")
	ErrInvalid         = errors.New("editor document is invalid")
)

type AssetReference struct {
	AssetPublicID string `json:"asset_id"`
	Role          string `json:"role"`
	ElementID     string `json:"element_id"`
}

type Revision struct {
	PublicID   string          `json:"id"`
	Version    int64           `json:"version"`
	Asset      asset.Asset     `json:"asset"`
	Operation  string          `json:"operation"`
	Parameters json.RawMessage `json:"parameters"`
	CreatedAt  time.Time       `json:"created_at"`
}

type Document struct {
	ID              int64            `json:"-"`
	PublicID        string           `json:"id"`
	ProjectID       int64            `json:"-"`
	ProjectPublicID string           `json:"project_id"`
	ExternalUserID  int64            `json:"-"`
	NodeID          string           `json:"node_id"`
	BaseAsset       asset.Asset      `json:"base_asset"`
	CurrentAsset    asset.Asset      `json:"current_asset"`
	RawDocument     json.RawMessage  `json:"document"`
	Version         int64            `json:"version"`
	AssetReferences []AssetReference `json:"asset_references"`
	Revisions       []Revision       `json:"revisions"`
	CreatedAt       time.Time        `json:"created_at"`
	UpdatedAt       time.Time        `json:"updated_at"`
}

type CreateInput struct {
	ProjectPublicID   string           `json:"project_id"`
	NodeID            string           `json:"node_id"`
	BaseAssetPublicID string           `json:"base_asset_id"`
	Document          json.RawMessage  `json:"document"`
	AssetReferences   []AssetReference `json:"asset_references,omitempty"`
}

type UpdateInput struct {
	Version              int64            `json:"version"`
	Document             json.RawMessage  `json:"document"`
	CurrentAssetPublicID string           `json:"current_asset_id,omitempty"`
	Operation            string           `json:"operation,omitempty"`
	Parameters           json.RawMessage  `json:"parameters,omitempty"`
	AssetReferences      []AssetReference `json:"asset_references,omitempty"`
}

type Service struct {
	db     *sql.DB
	assets *asset.Service
}

func NewService(db *sql.DB, assets *asset.Service) *Service {
	return &Service{db: db, assets: assets}
}

func (service *Service) Create(ctx context.Context, ownerID int64, input CreateInput) (Document, bool, error) {
	input.ProjectPublicID = strings.TrimSpace(input.ProjectPublicID)
	input.NodeID = strings.TrimSpace(input.NodeID)
	input.BaseAssetPublicID = strings.TrimSpace(input.BaseAssetPublicID)
	if !publicid.Valid(input.ProjectPublicID) || !publicid.Valid(input.BaseAssetPublicID) || input.NodeID == "" || utf8.RuneCountInString(input.NodeID) > 128 {
		return Document{}, false, ErrInvalid
	}
	if err := validateDocument(input.Document); err != nil {
		return Document{}, false, err
	}
	references, err := normalizeReferences(input.AssetReferences)
	if err != nil {
		return Document{}, false, err
	}
	identifier, err := publicid.New("editor")
	if err != nil {
		return Document{}, false, err
	}
	tx, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return Document{}, false, fmt.Errorf("begin editor create: %w", err)
	}
	defer tx.Rollback()
	var projectID, baseAssetID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT project.id, asset.id
		FROM canvas_projects project
		JOIN canvas_assets asset ON asset.public_id = $3 AND asset.external_user_id = $1 AND asset.deleted_at IS NULL
		WHERE project.external_user_id = $1 AND project.public_id = $2 AND project.deleted_at IS NULL
			AND asset.media_kind = 'image'`, ownerID, input.ProjectPublicID, input.BaseAssetPublicID).Scan(&projectID, &baseAssetID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Document{}, false, ErrInvalid
		}
		return Document{}, false, fmt.Errorf("resolve editor resources: %w", err)
	}
	var documentID int64
	created := true
	err = tx.QueryRowContext(ctx, `
		INSERT INTO canvas_editor_documents (
			public_id, project_id, node_id, base_asset_id, current_asset_id, document
		) VALUES ($1, $2, $3, $4, $4, $5)
		ON CONFLICT (project_id, node_id) WHERE deleted_at IS NULL DO NOTHING
		RETURNING id`, identifier, projectID, input.NodeID, baseAssetID, input.Document).Scan(&documentID)
	if errors.Is(err, sql.ErrNoRows) {
		created = false
		if err := tx.QueryRowContext(ctx, `
			SELECT id FROM canvas_editor_documents
			WHERE project_id = $1 AND node_id = $2 AND deleted_at IS NULL`, projectID, input.NodeID).Scan(&documentID); err != nil {
			return Document{}, false, fmt.Errorf("read existing editor document: %w", err)
		}
	} else if err != nil {
		return Document{}, false, fmt.Errorf("create editor document: %w", err)
	}
	if created {
		if err := replaceReferences(ctx, tx, documentID, ownerID, references); err != nil {
			return Document{}, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Document{}, false, fmt.Errorf("commit editor create: %w", err)
	}
	document, err := service.Get(ctx, ownerID, identifier)
	if !created {
		document, err = service.getByProjectNode(ctx, ownerID, input.ProjectPublicID, input.NodeID)
	}
	return document, created, err
}

func (service *Service) Get(ctx context.Context, ownerID int64, publicID string) (Document, error) {
	return service.load(ctx, ownerID, `document.public_id = $2`, publicID, "")
}

func (service *Service) getByProjectNode(ctx context.Context, ownerID int64, projectPublicID, nodeID string) (Document, error) {
	return service.load(ctx, ownerID, `project.public_id = $2 AND document.node_id = $3`, projectPublicID, nodeID)
}

func (service *Service) load(ctx context.Context, ownerID int64, condition string, arguments ...string) (Document, error) {
	query := `
		SELECT document.id, document.public_id, document.project_id, project.public_id,
			project.external_user_id, document.node_id, base.public_id, current.public_id,
			document.document, document.version, document.created_at, document.updated_at
		FROM canvas_editor_documents document
		JOIN canvas_projects project ON project.id = document.project_id
		JOIN canvas_assets base ON base.id = document.base_asset_id
		JOIN canvas_assets current ON current.id = document.current_asset_id
		WHERE project.external_user_id = $1 AND document.deleted_at IS NULL AND project.deleted_at IS NULL AND ` + condition
	values := make([]any, 0, len(arguments)+1)
	values = append(values, ownerID)
	for _, argument := range arguments {
		values = append(values, argument)
	}
	var document Document
	var basePublicID, currentPublicID string
	if err := service.db.QueryRowContext(ctx, query, values...).Scan(
		&document.ID, &document.PublicID, &document.ProjectID, &document.ProjectPublicID,
		&document.ExternalUserID, &document.NodeID, &basePublicID, &currentPublicID,
		&document.RawDocument, &document.Version, &document.CreatedAt, &document.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Document{}, ErrNotFound
		}
		return Document{}, fmt.Errorf("read editor document: %w", err)
	}
	var err error
	document.BaseAsset, err = service.assets.Get(ctx, ownerID, basePublicID)
	if err != nil {
		return Document{}, err
	}
	document.CurrentAsset, err = service.assets.Get(ctx, ownerID, currentPublicID)
	if err != nil {
		return Document{}, err
	}
	if err := service.loadRelations(ctx, ownerID, &document); err != nil {
		return Document{}, err
	}
	return document, nil
}

func (service *Service) Update(ctx context.Context, ownerID int64, publicID string, input UpdateInput) (Document, error) {
	if !publicid.Valid(publicID) || input.Version <= 0 {
		return Document{}, ErrInvalid
	}
	if err := validateDocument(input.Document); err != nil {
		return Document{}, err
	}
	references, err := normalizeReferences(input.AssetReferences)
	if err != nil {
		return Document{}, err
	}
	parameters, err := validateParameters(input.Parameters)
	if err != nil {
		return Document{}, err
	}
	input.CurrentAssetPublicID = strings.TrimSpace(input.CurrentAssetPublicID)
	input.Operation = strings.TrimSpace(input.Operation)
	if (input.CurrentAssetPublicID == "") != (input.Operation == "") {
		return Document{}, ErrInvalid
	}
	if input.Operation != "" && !validOperation(input.Operation) {
		return Document{}, ErrInvalid
	}
	tx, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return Document{}, fmt.Errorf("begin editor update: %w", err)
	}
	defer tx.Rollback()
	var documentID, currentAssetID int64
	if input.CurrentAssetPublicID == "" {
		if err := tx.QueryRowContext(ctx, `
			SELECT document.id, document.current_asset_id
			FROM canvas_editor_documents document
			JOIN canvas_projects project ON project.id = document.project_id
			WHERE project.external_user_id = $1 AND document.public_id = $2 AND document.deleted_at IS NULL`, ownerID, publicID).Scan(&documentID, &currentAssetID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Document{}, ErrNotFound
			}
			return Document{}, fmt.Errorf("resolve editor update: %w", err)
		}
	} else {
		if err := tx.QueryRowContext(ctx, `
			SELECT document.id, asset.id
			FROM canvas_editor_documents document
			JOIN canvas_projects project ON project.id = document.project_id
			JOIN canvas_assets asset ON asset.external_user_id = $1 AND asset.public_id = $3
				AND asset.media_kind = 'image' AND asset.deleted_at IS NULL
			WHERE project.external_user_id = $1 AND document.public_id = $2 AND document.deleted_at IS NULL`, ownerID, publicID, input.CurrentAssetPublicID).Scan(&documentID, &currentAssetID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Document{}, ErrInvalid
			}
			return Document{}, fmt.Errorf("resolve editor current asset: %w", err)
		}
	}
	var nextVersion int64
	err = tx.QueryRowContext(ctx, `
		UPDATE canvas_editor_documents SET document = $4, current_asset_id = $5,
			version = version + 1, updated_at = NOW()
		WHERE id = $1 AND version = $2 AND deleted_at IS NULL
			AND EXISTS (SELECT 1 FROM canvas_projects WHERE id = canvas_editor_documents.project_id AND external_user_id = $3 AND deleted_at IS NULL)
		RETURNING version`, documentID, input.Version, ownerID, input.Document, currentAssetID).Scan(&nextVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return Document{}, ErrVersionConflict
	}
	if err != nil {
		return Document{}, fmt.Errorf("update editor document: %w", err)
	}
	if err := replaceReferences(ctx, tx, documentID, ownerID, references); err != nil {
		return Document{}, err
	}
	if input.Operation != "" {
		revisionID, err := publicid.New("revision")
		if err != nil {
			return Document{}, err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_editor_revisions (public_id, document_id, version, asset_id, operation, parameters)
			VALUES ($1, $2, $3, $4, $5, $6)`, revisionID, documentID, nextVersion, currentAssetID, input.Operation, parameters); err != nil {
			return Document{}, fmt.Errorf("append editor revision: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Document{}, fmt.Errorf("commit editor update: %w", err)
	}
	return service.Get(ctx, ownerID, publicID)
}

func (service *Service) CreateDerivedAsset(ctx context.Context, ownerID int64, documentPublicID, parentPublicID string, input asset.CreateInput) (asset.Asset, error) {
	document, err := service.Get(ctx, ownerID, documentPublicID)
	if err != nil {
		return asset.Asset{}, err
	}
	allowed := document.BaseAsset.PublicID == parentPublicID || document.CurrentAsset.PublicID == parentPublicID
	for _, reference := range document.AssetReferences {
		allowed = allowed || reference.AssetPublicID == parentPublicID
	}
	for _, revision := range document.Revisions {
		allowed = allowed || revision.Asset.PublicID == parentPublicID
	}
	if !allowed {
		return asset.Asset{}, ErrInvalid
	}
	input.ProjectPublicID = document.ProjectPublicID
	input.SourceType = "derived"
	input.ParentAssetIDs = []string{parentPublicID}
	return service.assets.Create(ctx, ownerID, input)
}

func (service *Service) loadRelations(ctx context.Context, ownerID int64, document *Document) error {
	rows, err := service.db.QueryContext(ctx, `
		SELECT asset.public_id, reference.role, reference.element_id
		FROM canvas_editor_asset_refs reference
		JOIN canvas_assets asset ON asset.id = reference.asset_id AND asset.external_user_id = $2
		WHERE reference.document_id = $1 ORDER BY reference.role, reference.element_id, asset.public_id`, document.ID, ownerID)
	if err != nil {
		return fmt.Errorf("read editor references: %w", err)
	}
	document.AssetReferences = make([]AssetReference, 0)
	for rows.Next() {
		var reference AssetReference
		if err := rows.Scan(&reference.AssetPublicID, &reference.Role, &reference.ElementID); err != nil {
			_ = rows.Close()
			return fmt.Errorf("scan editor reference: %w", err)
		}
		document.AssetReferences = append(document.AssetReferences, reference)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close editor references: %w", err)
	}
	rows, err = service.db.QueryContext(ctx, `
		SELECT revision.public_id, revision.version, asset.public_id,
			revision.operation, revision.parameters, revision.created_at
		FROM canvas_editor_revisions revision
		JOIN canvas_assets asset ON asset.id = revision.asset_id AND asset.external_user_id = $2
		WHERE revision.document_id = $1 ORDER BY revision.version, revision.id`, document.ID, ownerID)
	if err != nil {
		return fmt.Errorf("read editor revisions: %w", err)
	}
	defer rows.Close()
	document.Revisions = make([]Revision, 0)
	for rows.Next() {
		var revision Revision
		var assetPublicID string
		if err := rows.Scan(&revision.PublicID, &revision.Version, &assetPublicID, &revision.Operation, &revision.Parameters, &revision.CreatedAt); err != nil {
			return fmt.Errorf("scan editor revision: %w", err)
		}
		revision.Asset, err = service.assets.Get(ctx, ownerID, assetPublicID)
		if err != nil {
			return err
		}
		document.Revisions = append(document.Revisions, revision)
	}
	return rows.Err()
}

func replaceReferences(ctx context.Context, tx *sql.Tx, documentID, ownerID int64, references []AssetReference) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM canvas_editor_asset_refs WHERE document_id = $1`, documentID); err != nil {
		return fmt.Errorf("delete editor references: %w", err)
	}
	for _, reference := range references {
		var assetID int64
		if err := tx.QueryRowContext(ctx, `
			SELECT id FROM canvas_assets WHERE external_user_id = $1 AND public_id = $2
				AND media_kind = 'image' AND deleted_at IS NULL`, ownerID, reference.AssetPublicID).Scan(&assetID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrInvalid
			}
			return fmt.Errorf("resolve editor reference: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO canvas_editor_asset_refs (document_id, asset_id, role, element_id)
			VALUES ($1, $2, $3, $4)`, documentID, assetID, reference.Role, reference.ElementID); err != nil {
			return fmt.Errorf("insert editor reference: %w", err)
		}
	}
	return nil
}

func ValidateDocument(raw json.RawMessage) error {
	if len(raw) == 0 || len(raw) > 128<<10 {
		return ErrInvalid
	}
	var document struct {
		SchemaVersion int `json:"schema_version"`
		Viewport      struct {
			Zoom float64 `json:"zoom"`
			X    float64 `json:"x"`
			Y    float64 `json:"y"`
		} `json:"viewport"`
		Canvas struct {
			Width      int    `json:"width"`
			Height     int    `json:"height"`
			Background string `json:"background"`
		} `json:"canvas"`
		SelectedRevisionID string `json:"selected_revision_id,omitempty"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return ErrInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	if document.SchemaVersion != 1 || !finite(document.Viewport.Zoom) || !finite(document.Viewport.X) || !finite(document.Viewport.Y) ||
		document.Viewport.Zoom <= 0 || document.Viewport.Zoom > 100 || math.Abs(document.Viewport.X) > 1e9 || math.Abs(document.Viewport.Y) > 1e9 ||
		document.Canvas.Width <= 0 || document.Canvas.Height <= 0 || document.Canvas.Width > 16384 || document.Canvas.Height > 16384 ||
		int64(document.Canvas.Width)*int64(document.Canvas.Height) > 100_000_000 {
		return ErrInvalid
	}
	if document.Canvas.Background != "transparent" && document.Canvas.Background != "white" && document.Canvas.Background != "black" {
		return ErrInvalid
	}
	if document.SelectedRevisionID != "" && !publicid.Valid(document.SelectedRevisionID) {
		return ErrInvalid
	}
	return validateSafeJSON(raw)
}

func validateDocument(raw json.RawMessage) error {
	return ValidateDocument(raw)
}

func ValidateParameters(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return json.RawMessage(`{}`), nil
	}
	if len(raw) > 64<<10 {
		return nil, ErrInvalid
	}
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, ErrInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, ErrInvalid
	}
	if err := validateSafeJSON(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func validateParameters(raw json.RawMessage) (json.RawMessage, error) {
	return ValidateParameters(raw)
}

func validateSafeJSON(raw json.RawMessage) error {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return ErrInvalid
	}
	var walk func(any, int) error
	walk = func(current any, depth int) error {
		if depth > 32 {
			return ErrInvalid
		}
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
				if strings.Contains(normalized, "token") || strings.Contains(normalized, "api_key") || normalized == "authorization" || normalized == "object_key" || normalized == "provider_url" || normalized == "password" || normalized == "secret" {
					return ErrInvalid
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
			lower := strings.ToLower(strings.TrimSpace(typed))
			if strings.HasPrefix(lower, "data:image/") || strings.HasPrefix(lower, "javascript:") || strings.HasPrefix(lower, "bearer ") || strings.HasPrefix(lower, "sk-") || strings.Contains(lower, "<script") {
				return ErrInvalid
			}
		}
		return nil
	}
	return walk(value, 0)
}

func normalizeReferences(input []AssetReference) ([]AssetReference, error) {
	if len(input) > 500 {
		return nil, ErrInvalid
	}
	result := make([]AssetReference, 0, len(input))
	seen := make(map[string]struct{})
	for _, reference := range input {
		reference.AssetPublicID = strings.TrimSpace(reference.AssetPublicID)
		reference.Role = strings.ToLower(strings.TrimSpace(reference.Role))
		reference.ElementID = strings.TrimSpace(reference.ElementID)
		if !publicid.Valid(reference.AssetPublicID) || !validRole(reference.Role) || reference.ElementID == "" || utf8.RuneCountInString(reference.ElementID) > 128 {
			return nil, ErrInvalid
		}
		key := reference.AssetPublicID + "\x00" + reference.Role + "\x00" + reference.ElementID
		if _, ok := seen[key]; !ok {
			seen[key] = struct{}{}
			result = append(result, reference)
		}
	}
	return result, nil
}

func validRole(value string) bool {
	return value == "source" || value == "layer" || value == "mask" || value == "result"
}

func validOperation(value string) bool {
	return value == "crop" || value == "mask_edit" || value == "background_replace" || value == "outpaint" || value == "revision_select"
}

func finite(value float64) bool {
	return !math.IsInf(value, 0) && !math.IsNaN(value)
}
