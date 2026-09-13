package transfer

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dolorous01/canvas-standalone/backend/internal/migrate"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestResolutionValidation(t *testing.T) {
	unresolved := []UnresolvedItem{{Kind: "image_job", PublicID: "job_one", Status: "running", Phase: "upstream"}}
	valid := []ResolutionDecision{{Kind: "image_job", PublicID: "job_one", Action: ResolutionImportAsIndeterminate, Reason: "upstream result cannot be proven"}}
	if err := validateResolutions(unresolved, valid); err != nil {
		t.Fatal(err)
	}
	if err := validateResolutions(unresolved, nil); err == nil {
		t.Fatal("missing resolution was accepted")
	}
	valid[0].Reason = "Bearer should-not-be-here"
	if err := validateResolutions(unresolved, valid); err == nil {
		t.Fatal("secret-like resolution was accepted")
	}
}

func TestRelativeObjectKeyValidation(t *testing.T) {
	valid := []string{"users/42/assets/asset_one/original", "legacy/file.png"}
	for _, value := range valid {
		if !validRelativeKey(value) {
			t.Fatalf("valid key rejected: %q", value)
		}
	}
	invalid := []string{"", "/absolute", "../escape", "path/../escape", "path\\file", "path\nfile"}
	for _, value := range invalid {
		if validRelativeKey(value) {
			t.Fatalf("invalid key accepted: %q", value)
		}
	}
}

func TestWriteReadAndCopyExport(t *testing.T) {
	sourceRoot := filepath.Join(t.TempDir(), "source")
	targetRoot := filepath.Join(t.TempDir(), "target")
	if err := os.Mkdir(sourceRoot, 0700); err != nil {
		t.Fatal(err)
	}
	payload := []byte("object-payload")
	digest := sha256.Sum256(payload)
	sha := hex.EncodeToString(digest[:])
	sourceKey := "legacy/asset-one"
	if err := os.MkdirAll(filepath.Join(sourceRoot, "legacy"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, sourceKey), payload, 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 12, 1, 2, 3, 0, time.UTC)
	content := dataset{
		Projects: []Project{{PublicID: "project_one", ExternalUserID: 42, Name: "One", Document: json.RawMessage(`{"schema_version":1,"nodes":[]}`), Version: 1, CreatedAt: now, UpdatedAt: now}},
		Assets:   []Asset{{PublicID: "asset_one", ExternalUserID: 42, ProjectPublicID: "project_one", SourceType: "upload", MediaKind: "image", ObjectKey: "users/42/assets/asset_one/original", FileName: "one.png", MIMEType: "image/png", Width: 1, Height: 1, ByteSize: int64(len(payload)), SHA256: sha, CreatedAt: now}},
		Objects:  []Object{{AssetPublicID: "asset_one", Kind: "original", SourceKey: sourceKey, TargetKey: "users/42/assets/asset_one/original", MIMEType: "image/png", Size: int64(len(payload)), SHA256: sha}},
	}
	output := filepath.Join(t.TempDir(), "export")
	manifest, err := writeExport(output, Manifest{Format: Format, ExporterVersion: "test@revision", GeneratedAt: now, StatusCounts: map[string]int{}, Resolutions: []ResolutionDecision{}, Unresolved: []UnresolvedItem{}, Warnings: []string{}}, content)
	if err != nil {
		t.Fatal(err)
	}
	loadedManifest, loaded, err := readExport(output)
	if err != nil {
		t.Fatal(err)
	}
	if loadedManifest.ContentSHA256 != manifest.ContentSHA256 || len(loaded.Assets) != 1 {
		t.Fatalf("unexpected loaded export: %+v", loadedManifest)
	}
	if _, err := CopyObjects(context.Background(), output, sourceRoot, targetRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := CopyObjects(context.Background(), output, sourceRoot, targetRoot); err != nil {
		t.Fatalf("repeat copy failed: %v", err)
	}
	copied, err := os.ReadFile(filepath.Join(targetRoot, content.Objects[0].TargetKey))
	if err != nil || string(copied) != string(payload) {
		t.Fatalf("copied payload = %q, %v", copied, err)
	}
	if err := os.WriteFile(filepath.Join(targetRoot, content.Objects[0].TargetKey), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifyObjects(context.Background(), targetRoot, content.Objects); err == nil {
		t.Fatal("corrupt target object was accepted")
	}
	if err := os.WriteFile(filepath.Join(output, projectsFile), []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readExport(output); err == nil {
		t.Fatal("tampered export was accepted")
	}
}

func TestPostgresLegacyTransferEndToEnd(t *testing.T) {
	databaseURL := os.Getenv("CANVAS_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("CANVAS_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	admin, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	suffix := strings.ToLower(strings.ReplaceAll(t.Name(), "/", "_")) + "_" + time.Now().Format("150405000000")
	sourceSchema := "source_" + suffix
	targetSchema := "target_" + suffix
	for _, schema := range []string{sourceSchema, targetSchema} {
		if _, err := admin.ExecContext(ctx, `CREATE SCHEMA `+schema); err != nil {
			t.Fatal(err)
		}
		defer admin.ExecContext(context.Background(), `DROP SCHEMA `+schema+` CASCADE`)
	}
	sourceDB := openSchemaDatabase(t, databaseURL, sourceSchema)
	defer sourceDB.Close()
	targetDB := openSchemaDatabase(t, databaseURL, targetSchema)
	defer targetDB.Close()
	if err := createLegacyFixture(ctx, sourceDB); err != nil {
		t.Fatal(err)
	}
	if _, err := migrate.Apply(ctx, targetDB); err != nil {
		t.Fatal(err)
	}

	sourceRoot := filepath.Join(t.TempDir(), "source-objects")
	targetRoot := filepath.Join(t.TempDir(), "target-objects")
	if err := os.Mkdir(sourceRoot, 0700); err != nil {
		t.Fatal(err)
	}
	image := mustDecodeFixture(t)
	objectKey := "canvas-assets/42/asset_fixture/original.png"
	if err := os.MkdirAll(filepath.Dir(filepath.Join(sourceRoot, objectKey)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, objectKey), image, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(image)
	if _, err := sourceDB.ExecContext(ctx, `UPDATE image_assets SET byte_size=$1, sha256=$2 WHERE public_id='asset_fixture'`, len(image), hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}

	audit, err := AuditSource(ctx, sourceDB, sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(audit.Unresolved) != 1 || audit.Unresolved[0].PublicID != "media_fixture" {
		t.Fatalf("unexpected unresolved audit: %+v", audit.Unresolved)
	}
	resolutionPath := filepath.Join(t.TempDir(), "resolution.json")
	resolution := ResolutionFile{Format: ResolutionFormat, Decisions: []ResolutionDecision{{Kind: "legacy_media_task", PublicID: "media_fixture", Action: ResolutionImportAsIndeterminate, Reason: "fixture has an unknown upstream result"}}}
	resolutionBytes, err := json.Marshal(resolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolutionPath, resolutionBytes, 0600); err != nil {
		t.Fatal(err)
	}
	exportPath := filepath.Join(t.TempDir(), "export")
	manifest, err := Export(ctx, sourceDB, sourceRoot, exportPath, ExportOptions{ExporterVersion: "integration@test", ResolutionPath: resolutionPath})
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Counts["projects_active"] != 1 || manifest.Counts["assets_active"] != 1 {
		t.Fatalf("unexpected manifest counts: %+v", manifest.Counts)
	}
	if _, err := Import(ctx, targetDB, exportPath); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(ctx, targetDB, exportPath); err != nil {
		t.Fatalf("repeat import failed: %v", err)
	}
	if _, err := CopyObjects(ctx, exportPath, sourceRoot, targetRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyTransfer(ctx, targetDB, exportPath, targetRoot); err != nil {
		t.Fatal(err)
	}
	var importedJobs int
	if err := targetDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM canvas_legacy_media_tasks WHERE status='indeterminate'`).Scan(&importedJobs); err != nil || importedJobs != 1 {
		t.Fatalf("legacy history count=%d, err=%v", importedJobs, err)
	}
	if _, err := targetDB.ExecContext(ctx, `UPDATE canvas_projects SET name='tampered' WHERE public_id='project_fixture'`); err != nil {
		t.Fatal(err)
	}
	if _, err := VerifyTransfer(ctx, targetDB, exportPath, targetRoot); err == nil {
		t.Fatal("tampered target database was accepted")
	}
}

func openSchemaDatabase(t *testing.T, databaseURL, schema string) *sql.DB {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	return db
}

func mustDecodeFixture(t *testing.T) []byte {
	t.Helper()
	const encoded = "89504e470d0a1a0a0000000d4948445200000001000000010804000000b51c0c020000000b4944415478da6364f80f00010501012718e3660000000049454e44ae426082"
	payload, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func createLegacyFixture(ctx context.Context, db *sql.DB) error {
	statements := []string{
		`CREATE TABLE schema_migrations (filename TEXT PRIMARY KEY, checksum CHAR(64) NOT NULL)`,
		`CREATE TABLE image_canvas_projects (id BIGSERIAL PRIMARY KEY, public_id VARCHAR(64) UNIQUE NOT NULL, user_id BIGINT NOT NULL, name VARCHAR(160) NOT NULL, document JSONB NOT NULL, version BIGINT NOT NULL, thumbnail_asset_id BIGINT, created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE image_assets (id BIGSERIAL PRIMARY KEY, public_id VARCHAR(64) UNIQUE NOT NULL, owner_user_id BIGINT NOT NULL, project_id BIGINT, source_type VARCHAR(20) NOT NULL, object_key TEXT UNIQUE NOT NULL, thumbnail_object_key TEXT, mime_type VARCHAR(100) NOT NULL, width INTEGER NOT NULL, height INTEGER NOT NULL, byte_size BIGINT NOT NULL, sha256 CHAR(64) NOT NULL, parent_asset_ids JSONB NOT NULL, created_at TIMESTAMPTZ NOT NULL, deleted_at TIMESTAMPTZ, media_kind VARCHAR(16) NOT NULL, duration_ms BIGINT NOT NULL, file_name VARCHAR(255) NOT NULL)`,
		`CREATE TABLE image_canvas_asset_references (project_id BIGINT NOT NULL, asset_id BIGINT NOT NULL, node_id VARCHAR(128) NOT NULL, created_at TIMESTAMPTZ NOT NULL)`,
		`CREATE TABLE image_canvas_library_items (id BIGSERIAL PRIMARY KEY, public_id VARCHAR(64) NOT NULL, client_id VARCHAR(128) NOT NULL, user_id BIGINT NOT NULL, kind VARCHAR(16) NOT NULL, asset_id BIGINT, title VARCHAR(240) NOT NULL, content TEXT NOT NULL, tags JSONB NOT NULL, source VARCHAR(240) NOT NULL, note TEXT NOT NULL, metadata JSONB NOT NULL, version BIGINT NOT NULL, created_at TIMESTAMPTZ NOT NULL, updated_at TIMESTAMPTZ NOT NULL, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE image_editor_documents (id BIGSERIAL PRIMARY KEY, public_id VARCHAR(64), project_id BIGINT, node_id VARCHAR(128), base_asset_id BIGINT, current_asset_id BIGINT, document JSONB, version BIGINT, created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ, deleted_at TIMESTAMPTZ)`,
		`CREATE TABLE image_editor_asset_references (document_id BIGINT, asset_id BIGINT, role VARCHAR(16), element_id VARCHAR(128), created_at TIMESTAMPTZ)`,
		`CREATE TABLE image_editor_revisions (id BIGSERIAL PRIMARY KEY, public_id VARCHAR(64), document_id BIGINT, version BIGINT, asset_id BIGINT, operation VARCHAR(32), parameters JSONB, created_at TIMESTAMPTZ)`,
		`CREATE TABLE image_model_policies (id SMALLINT PRIMARY KEY, version BIGINT, enabled BOOLEAN, created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ)`,
		`CREATE TABLE image_model_policy_items (id BIGSERIAL PRIMARY KEY, policy_id SMALLINT, model VARCHAR(128), enabled BOOLEAN, position INTEGER, created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ)`,
		`CREATE TABLE image_model_policy_audits (id BIGSERIAL PRIMARY KEY, operator_user_id BIGINT, old_version BIGINT, new_version BIGINT, before_value JSONB, after_value JSONB, created_at TIMESTAMPTZ)`,
		`CREATE TABLE settings (id BIGSERIAL PRIMARY KEY, key VARCHAR(128), value TEXT, updated_at TIMESTAMPTZ)`,
		`CREATE TABLE image_jobs (id BIGSERIAL PRIMARY KEY, public_id VARCHAR(64), user_id BIGINT, project_id BIGINT, client_node_id VARCHAR(128), endpoint VARCHAR(64), mode VARCHAR(32), operation VARCHAR(32), requested_model VARCHAR(128), selected_model VARCHAR(128), successful_model VARCHAR(128), policy_version BIGINT, status VARCHAR(32), execution_phase VARCHAR(20), requested_count INTEGER, completed_count INTEGER, request_digest VARCHAR(64), idempotency_key_hash VARCHAR(64), attempt_plan JSONB, cancel_requested_at TIMESTAMPTZ, error_type VARCHAR(64), error_code VARCHAR(64), error_message TEXT, error_retryable BOOLEAN, created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ, finished_at TIMESTAMPTZ)`,
		`CREATE TABLE image_job_inputs (id BIGSERIAL PRIMARY KEY, job_id BIGINT, index INTEGER, kind VARCHAR(32), object_key TEXT, sha256 VARCHAR(64), created_at TIMESTAMPTZ)`,
		`CREATE TABLE image_job_results (id BIGSERIAL PRIMARY KEY, job_id BIGINT, index INTEGER, status VARCHAR(32), mime_type VARCHAR(128), size_tier VARCHAR(16), asset_id BIGINT, created_at TIMESTAMPTZ)`,
		`CREATE TABLE canvas_media_tasks (id BIGSERIAL PRIMARY KEY, public_id VARCHAR(64), kind VARCHAR(16), status VARCHAR(20), phase VARCHAR(32), user_id BIGINT, project_id BIGINT, client_node_id VARCHAR(128), selected_model VARCHAR(128), successful_model VARCHAR(128), request_hash CHAR(64), result_asset_id BIGINT, error JSONB, created_at TIMESTAMPTZ, updated_at TIMESTAMPTZ, completed_at TIMESTAMPTZ)`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	for _, migrationName := range requiredSourceMigrations {
		if _, err := db.ExecContext(ctx, `INSERT INTO schema_migrations(filename,checksum) VALUES($1,$2)`, migrationName, strings.Repeat("0", 64)); err != nil {
			return err
		}
	}
	now := time.Date(2026, 9, 12, 1, 2, 3, 0, time.UTC)
	projectDocument := `{"schema_version":1,"nodes":[{"id":"node_one","asset_id":"asset_fixture"}]}`
	if _, err := db.ExecContext(ctx, `INSERT INTO image_canvas_projects(public_id,user_id,name,document,version,created_at,updated_at) VALUES('project_fixture',42,'Fixture',$1,3,$2,$2)`, projectDocument, now); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO image_assets(public_id,owner_user_id,project_id,source_type,object_key,mime_type,width,height,byte_size,sha256,parent_asset_ids,created_at,media_kind,duration_ms,file_name) SELECT 'asset_fixture',42,id,'upload','canvas-assets/42/asset_fixture/original.png','image/png',1,1,1,$1,'[]',$2,'image',0,'fixture.png' FROM image_canvas_projects WHERE public_id='project_fixture'`, strings.Repeat("0", 64), now); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO image_canvas_asset_references(project_id,asset_id,node_id,created_at) SELECT project.id,asset.id,'node_one',$1 FROM image_canvas_projects project,image_assets asset`, now); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO image_canvas_library_items(public_id,client_id,user_id,kind,asset_id,title,content,tags,source,note,metadata,version,created_at,updated_at) SELECT 'library_fixture','client_fixture',42,'image',id,'Fixture','', '[]','legacy','','{}',2,$1,$1 FROM image_assets`, now); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO image_model_policies(id,version,enabled,created_at,updated_at) VALUES(1,4,TRUE,$1,$1)`, now); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO image_model_policy_items(policy_id,model,enabled,position,created_at,updated_at) VALUES(1,'gpt-image-2',TRUE,0,$1,$1)`, now); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO canvas_media_tasks(public_id,kind,status,phase,user_id,project_id,client_node_id,selected_model,request_hash,error,created_at,updated_at) SELECT 'media_fixture','video','indeterminate','failed',42,id,'node_video','grok-imagine-video-1.5',$1,'{"code":"unknown"}',$2,$2 FROM image_canvas_projects`, strings.Repeat("a", 64), now); err != nil {
		return err
	}
	return nil
}
