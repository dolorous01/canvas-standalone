package library

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
	"unicode/utf8"

	"github.com/dolorous01/canvas-standalone/backend/internal/publicid"
)

var (
	ErrNotFound        = errors.New("library item not found")
	ErrVersionConflict = errors.New("library item version conflict")
	ErrClientConflict  = errors.New("library client ID conflict")
	ErrInvalid         = errors.New("library item is invalid")
)

type Item struct {
	ID             int64           `json:"-"`
	PublicID       string          `json:"id"`
	ClientID       string          `json:"client_id"`
	ExternalUserID int64           `json:"-"`
	Kind           string          `json:"kind"`
	AssetID        *int64          `json:"-"`
	AssetPublicID  string          `json:"asset_id,omitempty"`
	AssetURL       string          `json:"asset_url,omitempty"`
	ThumbnailURL   string          `json:"thumbnail_url,omitempty"`
	Title          string          `json:"title"`
	Content        string          `json:"content,omitempty"`
	Tags           []string        `json:"tags"`
	Source         string          `json:"source,omitempty"`
	Note           string          `json:"note,omitempty"`
	Metadata       json.RawMessage `json:"metadata"`
	Version        int64           `json:"version"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
}

type Write struct {
	ClientID      string          `json:"client_id"`
	Version       int64           `json:"version"`
	Kind          string          `json:"kind"`
	AssetPublicID string          `json:"asset_id"`
	Title         string          `json:"title"`
	Content       string          `json:"content"`
	Tags          []string        `json:"tags"`
	Source        string          `json:"source"`
	Note          string          `json:"note"`
	Metadata      json.RawMessage `json:"metadata"`
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (repository *Repository) List(ctx context.Context, ownerID int64) ([]Item, error) {
	rows, err := repository.db.QueryContext(ctx, `
		SELECT item.id, item.public_id, item.client_id, item.external_user_id, item.kind, item.asset_id,
			COALESCE(asset.public_id, ''), item.title, item.content, item.tags, item.source,
			item.note, item.metadata, item.version, item.created_at, item.updated_at
		FROM canvas_library_items item
		LEFT JOIN canvas_assets asset ON asset.id = item.asset_id
		WHERE item.external_user_id = $1 AND item.deleted_at IS NULL
		ORDER BY item.updated_at DESC, item.id DESC`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list library items: %w", err)
	}
	defer rows.Close()
	items := make([]Item, 0)
	for rows.Next() {
		item, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate library items: %w", err)
	}
	return items, nil
}

func (repository *Repository) Create(ctx context.Context, ownerID int64, input Write) (Item, bool, error) {
	input, err := normalize(input, true)
	if err != nil {
		return Item{}, false, err
	}
	identifier, err := publicid.New("library")
	if err != nil {
		return Item{}, false, err
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return Item{}, false, fmt.Errorf("begin library create: %w", err)
	}
	defer tx.Rollback()
	assetID, err := resolveAsset(ctx, tx, ownerID, input.Kind, input.AssetPublicID)
	if err != nil {
		return Item{}, false, err
	}
	tags, _ := json.Marshal(input.Tags)
	item, err := scanItem(tx.QueryRowContext(ctx, `
		WITH inserted AS (
			INSERT INTO canvas_library_items (
				public_id, client_id, external_user_id, kind, asset_id, title, content, tags, source, note, metadata
			) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			ON CONFLICT (external_user_id, client_id) DO NOTHING
			RETURNING *
		)
		SELECT item.id, item.public_id, item.client_id, item.external_user_id, item.kind, item.asset_id,
			COALESCE(asset.public_id, ''), item.title, item.content, item.tags, item.source,
			item.note, item.metadata, item.version, item.created_at, item.updated_at
		FROM inserted item LEFT JOIN canvas_assets asset ON asset.id = item.asset_id`,
		identifier, input.ClientID, ownerID, input.Kind, assetID, input.Title, input.Content,
		tags, input.Source, input.Note, input.Metadata,
	))
	created := true
	if errors.Is(err, ErrNotFound) {
		created = false
		item, err = scanItem(tx.QueryRowContext(ctx, `
			SELECT item.id, item.public_id, item.client_id, item.external_user_id, item.kind, item.asset_id,
				COALESCE(asset.public_id, ''), item.title, item.content, item.tags, item.source,
				item.note, item.metadata, item.version, item.created_at, item.updated_at
			FROM canvas_library_items item LEFT JOIN canvas_assets asset ON asset.id = item.asset_id
			WHERE item.external_user_id = $1 AND item.client_id = $2 AND item.deleted_at IS NULL`, ownerID, input.ClientID))
		if errors.Is(err, ErrNotFound) {
			return Item{}, false, ErrClientConflict
		}
	}
	if err != nil {
		return Item{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return Item{}, false, fmt.Errorf("commit library create: %w", err)
	}
	return withURLs(item), created, nil
}

func (repository *Repository) Update(ctx context.Context, ownerID int64, publicID string, input Write) (Item, error) {
	input, err := normalize(input, false)
	if err != nil || input.Version <= 0 || !publicid.Valid(publicID) {
		return Item{}, ErrInvalid
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return Item{}, fmt.Errorf("begin library update: %w", err)
	}
	defer tx.Rollback()
	assetID, err := resolveAsset(ctx, tx, ownerID, input.Kind, input.AssetPublicID)
	if err != nil {
		return Item{}, err
	}
	tags, _ := json.Marshal(input.Tags)
	item, err := scanItem(tx.QueryRowContext(ctx, `
		WITH updated AS (
			UPDATE canvas_library_items SET kind = $4, asset_id = $5, title = $6,
				content = $7, tags = $8, source = $9, note = $10, metadata = $11,
				version = version + 1, updated_at = NOW()
			WHERE public_id = $1 AND external_user_id = $2 AND version = $3 AND deleted_at IS NULL
			RETURNING *
		)
		SELECT item.id, item.public_id, item.client_id, item.external_user_id, item.kind, item.asset_id,
			COALESCE(asset.public_id, ''), item.title, item.content, item.tags, item.source,
			item.note, item.metadata, item.version, item.created_at, item.updated_at
		FROM updated item LEFT JOIN canvas_assets asset ON asset.id = item.asset_id`,
		publicID, ownerID, input.Version, input.Kind, assetID, input.Title, input.Content,
		tags, input.Source, input.Note, input.Metadata,
	))
	if errors.Is(err, ErrNotFound) {
		var exists bool
		if lookupErr := tx.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM canvas_library_items WHERE public_id = $1 AND external_user_id = $2 AND deleted_at IS NULL
		)`, publicID, ownerID).Scan(&exists); lookupErr != nil {
			return Item{}, fmt.Errorf("resolve library conflict: %w", lookupErr)
		}
		if exists {
			return Item{}, ErrVersionConflict
		}
		return Item{}, ErrNotFound
	}
	if err != nil {
		return Item{}, err
	}
	if err := tx.Commit(); err != nil {
		return Item{}, fmt.Errorf("commit library update: %w", err)
	}
	return withURLs(item), nil
}

func (repository *Repository) Delete(ctx context.Context, ownerID int64, publicID string) error {
	result, err := repository.db.ExecContext(ctx, `
		UPDATE canvas_library_items SET deleted_at = NOW(), updated_at = NOW()
		WHERE public_id = $1 AND external_user_id = $2 AND deleted_at IS NULL`, publicID, ownerID)
	if err != nil {
		return fmt.Errorf("delete library item: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read library delete result: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func normalize(input Write, requireClientID bool) (Write, error) {
	input.ClientID = strings.TrimSpace(input.ClientID)
	input.Kind = strings.ToLower(strings.TrimSpace(input.Kind))
	input.AssetPublicID = strings.TrimSpace(input.AssetPublicID)
	input.Title = strings.TrimSpace(input.Title)
	input.Source = strings.TrimSpace(input.Source)
	input.Note = strings.TrimSpace(input.Note)
	if (requireClientID && input.ClientID == "") || len(input.ClientID) > 128 || !utf8.ValidString(input.ClientID) {
		return Write{}, ErrInvalid
	}
	if input.Kind != "text" && input.Kind != "image" && input.Kind != "video" && input.Kind != "audio" {
		return Write{}, ErrInvalid
	}
	if input.Title == "" || utf8.RuneCountInString(input.Title) > 240 || len(input.Content) > 1<<20 || !utf8.ValidString(input.Title+input.Content) {
		return Write{}, ErrInvalid
	}
	if input.Kind == "text" {
		if strings.TrimSpace(input.Content) == "" || input.AssetPublicID != "" {
			return Write{}, ErrInvalid
		}
	} else {
		input.Content = ""
		if !publicid.Valid(input.AssetPublicID) {
			return Write{}, ErrInvalid
		}
	}
	if utf8.RuneCountInString(input.Source) > 240 || len(input.Note) > 16<<10 || !utf8.ValidString(input.Source+input.Note) {
		return Write{}, ErrInvalid
	}
	if len(input.Tags) > 50 {
		return Write{}, ErrInvalid
	}
	normalizedTags := make([]string, 0, len(input.Tags))
	seen := make(map[string]struct{})
	for _, value := range input.Tags {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 64 {
			return Write{}, ErrInvalid
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; !exists {
			seen[key] = struct{}{}
			normalizedTags = append(normalizedTags, value)
		}
	}
	input.Tags = normalizedTags
	if len(input.Metadata) == 0 {
		input.Metadata = json.RawMessage(`{}`)
	}
	if len(input.Metadata) > 64<<10 {
		return Write{}, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(input.Metadata))
	var metadata map[string]any
	if err := decoder.Decode(&metadata); err != nil || metadata == nil {
		return Write{}, ErrInvalid
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Write{}, ErrInvalid
	}
	return input, nil
}

func resolveAsset(ctx context.Context, tx *sql.Tx, ownerID int64, kind, publicID string) (*int64, error) {
	if kind == "text" {
		return nil, nil
	}
	var id int64
	var mediaKind string
	if err := tx.QueryRowContext(ctx, `
		SELECT id, media_kind FROM canvas_assets
		WHERE external_user_id = $1 AND public_id = $2 AND deleted_at IS NULL`, ownerID, publicID).Scan(&id, &mediaKind); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrInvalid
		}
		return nil, fmt.Errorf("resolve library asset: %w", err)
	}
	if mediaKind != kind {
		return nil, ErrInvalid
	}
	return &id, nil
}

func withURLs(item Item) Item {
	if item.AssetPublicID != "" {
		item.AssetURL = "/canvas-api/v1/assets/" + item.AssetPublicID
		if item.Kind == "image" {
			item.ThumbnailURL = item.AssetURL + "/thumbnail"
		}
	}
	if item.Tags == nil {
		item.Tags = []string{}
	}
	return item
}

type scanner interface {
	Scan(...any) error
}

func scanItem(row scanner) (Item, error) {
	var item Item
	var tags []byte
	if err := row.Scan(
		&item.ID, &item.PublicID, &item.ClientID, &item.ExternalUserID, &item.Kind,
		&item.AssetID, &item.AssetPublicID, &item.Title, &item.Content, &tags,
		&item.Source, &item.Note, &item.Metadata, &item.Version, &item.CreatedAt, &item.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Item{}, ErrNotFound
		}
		return Item{}, fmt.Errorf("scan library item: %w", err)
	}
	if err := json.Unmarshal(tags, &item.Tags); err != nil {
		return Item{}, fmt.Errorf("decode library tags: %w", err)
	}
	return item, nil
}
