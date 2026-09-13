package asset

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dolorous01/canvas-standalone/backend/internal/objectstore"
	"github.com/dolorous01/canvas-standalone/backend/internal/publicid"
	_ "golang.org/x/image/webp"
)

const MaxUploadBytes = int64(32 << 20)

var (
	ErrNotFound    = errors.New("asset not found")
	ErrInvalidFile = errors.New("asset file is invalid")
)

type Asset struct {
	ID             int64     `json:"-"`
	PublicID       string    `json:"id"`
	ExternalUserID int64     `json:"-"`
	ProjectID      *int64    `json:"-"`
	SourceType     string    `json:"source_type"`
	MediaKind      string    `json:"media_kind"`
	ObjectKey      string    `json:"-"`
	ThumbnailKey   *string   `json:"-"`
	FileName       string    `json:"file_name,omitempty"`
	MIMEType       string    `json:"mime_type"`
	Width          int       `json:"width"`
	Height         int       `json:"height"`
	DurationMS     *int64    `json:"duration_ms,omitempty"`
	ByteSize       int64     `json:"byte_size"`
	SHA256         string    `json:"sha256"`
	CreatedAt      time.Time `json:"created_at"`
	URL            string    `json:"url"`
	ThumbnailURL   string    `json:"thumbnail_url,omitempty"`
}

type CreateInput struct {
	ProjectPublicID string
	SourceType      string
	FileName        string
	DeclaredMIME    string
	Width           int
	Height          int
	DurationMS      *int64
	ParentAssetIDs  []string
	Body            io.Reader
}

type ObjectStore interface {
	Put(context.Context, string, io.Reader, int64, string) error
	Open(context.Context, string) (io.ReadCloser, objectstore.Metadata, error)
	Delete(context.Context, string) error
}

type Service struct {
	db      *sql.DB
	objects ObjectStore
}

func NewService(db *sql.DB, objects ObjectStore) *Service {
	return &Service{db: db, objects: objects}
}

func (service *Service) Create(ctx context.Context, ownerID int64, input CreateInput) (Asset, error) {
	if ownerID <= 0 || input.Body == nil || (input.SourceType != "upload" && input.SourceType != "generated" && input.SourceType != "derived" && input.SourceType != "legacy") {
		return Asset{}, ErrInvalidFile
	}
	payload, err := io.ReadAll(io.LimitReader(input.Body, MaxUploadBytes+1))
	if err != nil || len(payload) == 0 || int64(len(payload)) > MaxUploadBytes {
		return Asset{}, ErrInvalidFile
	}
	mediaKind, detectedMIME, width, height, err := inspect(payload, input.DeclaredMIME, input.Width, input.Height, input.DurationMS)
	if err != nil {
		return Asset{}, err
	}
	digest := sha256.Sum256(payload)
	digestText := hex.EncodeToString(digest[:])
	identifier, err := publicid.New("asset")
	if err != nil {
		return Asset{}, err
	}
	objectKey := "users/" + strconv.FormatInt(ownerID, 10) + "/assets/" + identifier + "/original"
	if err := service.objects.Put(ctx, objectKey, bytes.NewReader(payload), int64(len(payload)), digestText); err != nil {
		return Asset{}, fmt.Errorf("store asset object: %w", err)
	}
	for index := range payload {
		payload[index] = 0
	}
	committed := false
	defer func() {
		if !committed {
			_ = service.objects.Delete(context.Background(), objectKey)
		}
	}()
	tx, err := service.db.BeginTx(ctx, nil)
	if err != nil {
		return Asset{}, fmt.Errorf("begin asset create: %w", err)
	}
	defer tx.Rollback()
	var projectID *int64
	if input.ProjectPublicID != "" {
		var value int64
		if err := tx.QueryRowContext(ctx, `
			SELECT id FROM canvas_projects
			WHERE external_user_id = $1 AND public_id = $2 AND deleted_at IS NULL`, ownerID, input.ProjectPublicID).Scan(&value); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Asset{}, ErrInvalidFile
			}
			return Asset{}, fmt.Errorf("resolve asset project: %w", err)
		}
		projectID = &value
	}
	item, err := scanAsset(tx.QueryRowContext(ctx, `
		INSERT INTO canvas_assets (
			public_id, external_user_id, project_id, source_type, media_kind, object_key,
			file_name, mime_type, width, height, duration_ms, byte_size, sha256
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id, public_id, external_user_id, project_id, source_type, media_kind, object_key,
			thumbnail_object_key, COALESCE(file_name, ''), mime_type, width, height, duration_ms,
			byte_size, sha256, created_at`,
		identifier, ownerID, projectID, input.SourceType, mediaKind, objectKey,
		safeFileName(input.FileName), detectedMIME, width, height, input.DurationMS, len(payload), digestText,
	))
	if err != nil {
		return Asset{}, err
	}
	for _, parentPublicID := range input.ParentAssetIDs {
		var parentID int64
		if err := tx.QueryRowContext(ctx, `
			SELECT id FROM canvas_assets
			WHERE external_user_id = $1 AND public_id = $2 AND deleted_at IS NULL`, ownerID, parentPublicID).Scan(&parentID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return Asset{}, ErrInvalidFile
			}
			return Asset{}, fmt.Errorf("resolve parent asset: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO canvas_asset_parents (asset_id, parent_asset_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, item.ID, parentID); err != nil {
			return Asset{}, fmt.Errorf("link parent asset: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return Asset{}, fmt.Errorf("commit asset create: %w", err)
	}
	committed = true
	return withURLs(item), nil
}

func (service *Service) Get(ctx context.Context, ownerID int64, publicID string) (Asset, error) {
	item, err := scanAsset(service.db.QueryRowContext(ctx, `
		SELECT id, public_id, external_user_id, project_id, source_type, media_kind, object_key,
			thumbnail_object_key, COALESCE(file_name, ''), mime_type, width, height, duration_ms,
			byte_size, sha256, created_at
		FROM canvas_assets
		WHERE external_user_id = $1 AND public_id = $2 AND deleted_at IS NULL`, ownerID, publicID))
	if err != nil {
		return Asset{}, err
	}
	return withURLs(item), nil
}

func (service *Service) Open(ctx context.Context, ownerID int64, publicID string, thumbnail bool) (Asset, io.ReadCloser, objectstore.Metadata, error) {
	item, err := service.Get(ctx, ownerID, publicID)
	if err != nil {
		return Asset{}, nil, objectstore.Metadata{}, err
	}
	key := item.ObjectKey
	if thumbnail && item.ThumbnailKey != nil {
		key = *item.ThumbnailKey
	}
	reader, metadata, err := service.objects.Open(ctx, key)
	if err != nil {
		return Asset{}, nil, objectstore.Metadata{}, fmt.Errorf("open asset object: %w", err)
	}
	return item, reader, metadata, nil
}

func inspect(payload []byte, declared string, suppliedWidth, suppliedHeight int, duration *int64) (string, string, int, int, error) {
	detected := canonicalMIME(http.DetectContentType(payload[:min(len(payload), 512)]), payload)
	declaredType, _, _ := mime.ParseMediaType(declared)
	declaredType = strings.ToLower(declaredType)
	if declaredType != "" && declaredType != "application/octet-stream" && declaredType != detected {
		return "", "", 0, 0, ErrInvalidFile
	}
	switch {
	case strings.HasPrefix(detected, "image/"):
		configuration, _, err := image.DecodeConfig(bytes.NewReader(payload))
		if err != nil || configuration.Width <= 0 || configuration.Height <= 0 || configuration.Width > 16384 || configuration.Height > 16384 {
			return "", "", 0, 0, ErrInvalidFile
		}
		return "image", detected, configuration.Width, configuration.Height, nil
	case strings.HasPrefix(detected, "video/"):
		if suppliedWidth <= 0 || suppliedHeight <= 0 || suppliedWidth > 16384 || suppliedHeight > 16384 || duration == nil || *duration <= 0 {
			return "", "", 0, 0, ErrInvalidFile
		}
		return "video", detected, suppliedWidth, suppliedHeight, nil
	case strings.HasPrefix(detected, "audio/"):
		if duration == nil || *duration <= 0 {
			return "", "", 0, 0, ErrInvalidFile
		}
		return "audio", detected, 0, 0, nil
	default:
		return "", "", 0, 0, ErrInvalidFile
	}
}

func canonicalMIME(detected string, payload []byte) string {
	if len(payload) >= 12 && string(payload[0:4]) == "RIFF" && string(payload[8:12]) == "WEBP" {
		return "image/webp"
	}
	if len(payload) >= 12 && string(payload[4:8]) == "ftyp" {
		return "video/mp4"
	}
	if len(payload) >= 12 && string(payload[0:4]) == "RIFF" && string(payload[8:12]) == "WAVE" {
		return "audio/wav"
	}
	if len(payload) >= 4 && string(payload[0:4]) == "OggS" {
		return "audio/ogg"
	}
	if len(payload) >= 3 && string(payload[0:3]) == "ID3" {
		return "audio/mpeg"
	}
	mediaType, _, err := mime.ParseMediaType(detected)
	if err != nil {
		return "application/octet-stream"
	}
	return strings.ToLower(mediaType)
}

func safeFileName(value string) string {
	value = strings.TrimSpace(filepath.Base(strings.ReplaceAll(value, "\\", "/")))
	if value == "." || value == "" {
		return "asset"
	}
	value = strings.Map(func(char rune) rune {
		if char < 0x20 || char == 0x7f {
			return -1
		}
		return char
	}, value)
	if len(value) > 240 {
		end := 240
		for end > 0 && !utf8.ValidString(value[:end]) {
			end--
		}
		value = value[:end]
	}
	return value
}

func withURLs(item Asset) Asset {
	item.URL = "/canvas-api/v1/assets/" + item.PublicID
	if item.ThumbnailKey != nil {
		item.ThumbnailURL = item.URL + "/thumbnail"
	}
	return item
}

type scanner interface {
	Scan(...any) error
}

func scanAsset(row scanner) (Asset, error) {
	var item Asset
	if err := row.Scan(
		&item.ID, &item.PublicID, &item.ExternalUserID, &item.ProjectID, &item.SourceType,
		&item.MediaKind, &item.ObjectKey, &item.ThumbnailKey, &item.FileName, &item.MIMEType,
		&item.Width, &item.Height, &item.DurationMS, &item.ByteSize, &item.SHA256, &item.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Asset{}, ErrNotFound
		}
		return Asset{}, fmt.Errorf("scan asset: %w", err)
	}
	return item, nil
}
