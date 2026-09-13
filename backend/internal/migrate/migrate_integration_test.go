package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestPostgresFreshRepeatAndChecksumDrift(t *testing.T) {
	databaseURL := os.Getenv("CANVAS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("CANVAS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	fresh := schemaFingerprint(t, ctx, db)
	if _, err := Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if repeated := schemaFingerprint(t, ctx, db); repeated != fresh {
		t.Fatalf("repeat migration changed schema: fresh=%s repeated=%s", fresh, repeated)
	}

	var filename, checksum string
	if err := db.QueryRowContext(ctx, `SELECT filename, checksum FROM canvas_schema_migrations ORDER BY filename LIMIT 1`).Scan(&filename, &checksum); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE canvas_schema_migrations SET checksum = $1 WHERE filename = $2`, strings.Repeat("0", 64), filename); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(ctx, db); err == nil || !strings.Contains(err.Error(), "checksum drift") {
		t.Fatalf("Verify() after drift = %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE canvas_schema_migrations SET checksum = $1 WHERE filename = $2`, checksum, filename); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(ctx, db); err != nil {
		t.Fatal(err)
	}
}

func schemaFingerprint(t *testing.T, ctx context.Context, db *sql.DB) string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `
		SELECT table_name, column_name, data_type, is_nullable, COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = 'public' AND (table_name LIKE 'canvas_%')
		ORDER BY table_name, ordinal_position`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	hash := sha256.New()
	count := 0
	for rows.Next() {
		var table, column, dataType, nullable, defaultValue string
		if err := rows.Scan(&table, &column, &dataType, &nullable, &defaultValue); err != nil {
			t.Fatal(err)
		}
		count++
		hash.Write([]byte(strings.Join([]string{table, column, dataType, nullable, defaultValue}, "\x00")))
		hash.Write([]byte{'\n'})
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if count < 50 {
		t.Fatalf("unexpectedly small Canvas schema: %d columns", count)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
