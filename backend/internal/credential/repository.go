package credential

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dolorous01/canvas-standalone/backend/internal/publicid"
)

var ErrNotFound = errors.New("credential not found")

type Record struct {
	ID               int64
	PublicID         string
	ExternalUserID   int64
	ExternalAPIKeyID int64
	DisplayName      string
	KeyHint          string
	Encrypted        Ciphertext
	Status           string
	LastVerifiedAt   time.Time
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *Repository {
	return &Repository{db: db}
}

func (repository *Repository) Upsert(ctx context.Context, ownerID, externalKeyID int64, name, hint, requestID string, encrypted Ciphertext) (Record, error) {
	publicID, err := publicid.New("cred")
	if err != nil {
		return Record{}, err
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("begin credential binding: %w", err)
	}
	defer tx.Rollback()
	record, err := scanRecord(tx.QueryRowContext(ctx, `
		INSERT INTO canvas_credentials (
			public_id, external_user_id, external_api_key_id, display_name, key_hint,
			ciphertext, nonce, key_version, status, last_verified_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'active', NOW())
		ON CONFLICT (external_user_id, external_api_key_id) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			key_hint = EXCLUDED.key_hint,
			ciphertext = EXCLUDED.ciphertext,
			nonce = EXCLUDED.nonce,
			key_version = EXCLUDED.key_version,
			status = 'active',
			last_verified_at = NOW(),
			updated_at = NOW(),
			deleted_at = NULL
		RETURNING id, public_id, external_user_id, external_api_key_id, display_name, key_hint,
			ciphertext, nonce, key_version, status, last_verified_at, created_at, updated_at`,
		publicID, ownerID, externalKeyID, strings.TrimSpace(name), hint,
		encrypted.Payload, encrypted.Nonce, encrypted.KeyVersion,
	))
	if err != nil {
		return Record{}, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO canvas_audit_events (event_type, external_user_id, subject_public_id, request_id, detail)
		VALUES ('credential_bound', $1, $2, $3, jsonb_build_object('external_api_key_id', $4::BIGINT, 'key_version', $5::INTEGER))`,
		ownerID, record.PublicID, requestID, externalKeyID, encrypted.KeyVersion); err != nil {
		return Record{}, fmt.Errorf("audit credential binding: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("commit credential binding: %w", err)
	}
	return record, nil
}

func (repository *Repository) List(ctx context.Context, ownerID int64) ([]Record, error) {
	rows, err := repository.db.QueryContext(ctx, `
		SELECT id, public_id, external_user_id, external_api_key_id, display_name, key_hint,
			ciphertext, nonce, key_version, status, last_verified_at, created_at, updated_at
		FROM canvas_credentials
		WHERE external_user_id = $1 AND deleted_at IS NULL
		ORDER BY updated_at DESC, id DESC`, ownerID)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	defer rows.Close()
	result := make([]Record, 0)
	for rows.Next() {
		item, err := scanRecord(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate credentials: %w", err)
	}
	return result, nil
}

func (repository *Repository) GetByExternalKey(ctx context.Context, ownerID, externalKeyID int64) (Record, error) {
	return scanRecord(repository.db.QueryRowContext(ctx, `
		SELECT id, public_id, external_user_id, external_api_key_id, display_name, key_hint,
			ciphertext, nonce, key_version, status, last_verified_at, created_at, updated_at
		FROM canvas_credentials
		WHERE external_user_id = $1 AND external_api_key_id = $2 AND deleted_at IS NULL`, ownerID, externalKeyID))
}

func (repository *Repository) GetByID(ctx context.Context, id int64) (Record, error) {
	return scanRecord(repository.db.QueryRowContext(ctx, `
		SELECT id, public_id, external_user_id, external_api_key_id, display_name, key_hint,
			ciphertext, nonce, key_version, status, last_verified_at, created_at, updated_at
		FROM canvas_credentials WHERE id = $1 AND deleted_at IS NULL`, id))
}

func (repository *Repository) Disable(ctx context.Context, id int64) error {
	result, err := repository.db.ExecContext(ctx, `
		UPDATE canvas_credentials SET status = 'disabled', updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("disable credential: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read credential disable result: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	return nil
}

func (repository *Repository) Delete(ctx context.Context, ownerID int64, publicID, requestID string) error {
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin credential deletion: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
		UPDATE canvas_credentials SET status = 'deleted', deleted_at = NOW(), updated_at = NOW()
		WHERE external_user_id = $1 AND public_id = $2 AND deleted_at IS NULL`, ownerID, publicID)
	if err != nil {
		return fmt.Errorf("delete credential: %w", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read credential delete result: %w", err)
	}
	if count == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO canvas_audit_events (event_type, external_user_id, subject_public_id, request_id)
		VALUES ('credential_unbound', $1, $2, $3)`, ownerID, publicID, requestID); err != nil {
		return fmt.Errorf("audit credential deletion: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit credential deletion: %w", err)
	}
	return nil
}

type scanner interface {
	Scan(...any) error
}

func scanRecord(row scanner) (Record, error) {
	var record Record
	err := row.Scan(
		&record.ID, &record.PublicID, &record.ExternalUserID, &record.ExternalAPIKeyID,
		&record.DisplayName, &record.KeyHint, &record.Encrypted.Payload, &record.Encrypted.Nonce,
		&record.Encrypted.KeyVersion, &record.Status, &record.LastVerifiedAt, &record.CreatedAt, &record.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("scan credential: %w", err)
	}
	return record, nil
}
