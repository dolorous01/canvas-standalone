package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

//go:embed sql/*.sql
var migrationFiles embed.FS

const migrationTable = `
CREATE TABLE IF NOT EXISTS canvas_schema_migrations (
    filename TEXT PRIMARY KEY,
    checksum CHAR(64) NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
)`

type Status struct {
	Applied       int    `json:"applied"`
	Latest        string `json:"latest"`
	SchemaVersion string `json:"schema_version"`
}

type migration struct {
	filename string
	payload  []byte
	checksum string
}

func Apply(ctx context.Context, db *sql.DB) (Status, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return Status{}, err
	}
	if _, err := db.ExecContext(ctx, migrationTable); err != nil {
		return Status{}, fmt.Errorf("create canvas migration table: %w", err)
	}
	for _, item := range migrations {
		if err := applyOne(ctx, db, item); err != nil {
			return Status{}, err
		}
	}
	return Verify(ctx, db)
}

func Verify(ctx context.Context, db *sql.DB) (Status, error) {
	migrations, err := loadMigrations()
	if err != nil {
		return Status{}, err
	}
	rows, err := db.QueryContext(ctx, `SELECT filename, checksum FROM canvas_schema_migrations ORDER BY filename`)
	if err != nil {
		return Status{}, fmt.Errorf("read canvas migration state: %w", err)
	}
	defer rows.Close()
	applied := make(map[string]string)
	for rows.Next() {
		var filename, checksum string
		if err := rows.Scan(&filename, &checksum); err != nil {
			return Status{}, fmt.Errorf("scan canvas migration state: %w", err)
		}
		applied[filename] = checksum
	}
	if err := rows.Err(); err != nil {
		return Status{}, fmt.Errorf("iterate canvas migration state: %w", err)
	}
	for _, item := range migrations {
		checksum, ok := applied[item.filename]
		if !ok {
			return Status{}, fmt.Errorf("canvas migration %s is not applied", item.filename)
		}
		if checksum != item.checksum {
			return Status{}, fmt.Errorf("canvas migration checksum drift: %s", item.filename)
		}
		delete(applied, item.filename)
	}
	if len(applied) != 0 {
		return Status{}, errors.New("database contains unknown canvas migrations")
	}
	latest := "none"
	if len(migrations) > 0 {
		latest = migrations[len(migrations)-1].filename
	}
	return Status{Applied: len(migrations), Latest: latest, SchemaVersion: schemaDigest(migrations)}, nil
}

func applyOne(ctx context.Context, db *sql.DB, item migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin canvas migration %s: %w", item.filename, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('canvas-standalone-migrations'))`); err != nil {
		return fmt.Errorf("lock canvas migrations: %w", err)
	}
	var checksum string
	err = tx.QueryRowContext(ctx, `SELECT checksum FROM canvas_schema_migrations WHERE filename = $1`, item.filename).Scan(&checksum)
	if err == nil {
		if checksum != item.checksum {
			return fmt.Errorf("canvas migration checksum drift: %s", item.filename)
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect canvas migration %s: %w", item.filename, err)
	}
	if _, err := tx.ExecContext(ctx, string(item.payload)); err != nil {
		return fmt.Errorf("apply canvas migration %s: %w", item.filename, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO canvas_schema_migrations (filename, checksum) VALUES ($1, $2)`, item.filename, item.checksum); err != nil {
		return fmt.Errorf("record canvas migration %s: %w", item.filename, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit canvas migration %s: %w", item.filename, err)
	}
	return nil
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "sql")
	if err != nil {
		return nil, fmt.Errorf("list embedded migrations: %w", err)
	}
	items := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		payload, err := migrationFiles.ReadFile("sql/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read embedded migration %s: %w", entry.Name(), err)
		}
		digest := sha256.Sum256(payload)
		items = append(items, migration{filename: entry.Name(), payload: payload, checksum: hex.EncodeToString(digest[:])})
	}
	sort.Slice(items, func(left, right int) bool { return items[left].filename < items[right].filename })
	for index, item := range items {
		prefix := fmt.Sprintf("%03d_", index+1)
		if !strings.HasPrefix(item.filename, prefix) {
			return nil, fmt.Errorf("migration order is not contiguous at %s", item.filename)
		}
	}
	return items, nil
}

func schemaDigest(migrations []migration) string {
	hash := sha256.New()
	for _, item := range migrations {
		hash.Write([]byte(item.filename))
		hash.Write([]byte{0})
		hash.Write([]byte(item.checksum))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
