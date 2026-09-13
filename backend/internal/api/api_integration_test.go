package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dolorous01/canvas-standalone/backend/internal/asset"
	"github.com/dolorous01/canvas-standalone/backend/internal/authn"
	"github.com/dolorous01/canvas-standalone/backend/internal/credential"
	"github.com/dolorous01/canvas-standalone/backend/internal/editor"
	"github.com/dolorous01/canvas-standalone/backend/internal/gateway"
	"github.com/dolorous01/canvas-standalone/backend/internal/job"
	"github.com/dolorous01/canvas-standalone/backend/internal/library"
	"github.com/dolorous01/canvas-standalone/backend/internal/logging"
	"github.com/dolorous01/canvas-standalone/backend/internal/migrate"
	"github.com/dolorous01/canvas-standalone/backend/internal/objectstore"
	"github.com/dolorous01/canvas-standalone/backend/internal/policy"
	"github.com/dolorous01/canvas-standalone/backend/internal/project"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestAPIAuthCredentialAndProjectIsolation(t *testing.T) {
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
	if _, err := migrate.Apply(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE canvas_audit_events, canvas_credentials, canvas_projects CASCADE`); err != nil {
		t.Fatal(err)
	}

	const fullKey = "sk-" + "standalone-integration-secret"
	imageBytes, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	imageBase64 := base64.StdEncoding.EncodeToString(imageBytes)
	var imageCalls atomic.Int32
	officialServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		token := strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer ")
		if request.URL.Path == "/v1/images/generations" {
			if token != fullKey {
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(writer, `{"error":{"message":"unauthorized"}}`)
				return
			}
			imageCalls.Add(1)
			_, _ = io.WriteString(writer, `{"created":1,"data":[{"b64_json":"`+imageBase64+`"}]}`)
			return
		}
		userID := int64(42)
		if token == "user-two-token" {
			userID = 84
		} else if token != "user-one-token" {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(writer, `{"code":401,"message":"unauthorized"}`)
			return
		}
		switch request.URL.Path {
		case "/api/v1/user/profile":
			_, _ = io.WriteString(writer, `{"code":0,"message":"success","data":{"id":`+jsonNumber(userID)+`,"role":"user","status":"active"}}`)
		case "/api/v1/keys":
			_, _ = io.WriteString(writer, `{"code":0,"message":"success","data":{"items":[{"id":7,"user_id":`+jsonNumber(userID)+`,"key":"must-be-stripped","name":"Studio","status":"active","quota":10,"quota_used":1}],"total":1}}`)
		case "/api/v1/keys/7":
			_, _ = io.WriteString(writer, `{"code":0,"message":"success","data":{"id":7,"user_id":`+jsonNumber(userID)+`,"key":"`+fullKey+`","name":"Studio","status":"active","quota":10,"quota_used":1}}`)
		default:
			writer.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(writer, `{"code":404,"message":"not found"}`)
		}
	}))
	defer officialServer.Close()
	official, err := gateway.NewClient(officialServer.URL, &http.Client{Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	keyring, err := credential.NewKeyringForTest(1, map[int][]byte{1: bytes.Repeat([]byte{7}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	defer keyring.Close()
	objects, err := objectstore.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	assetService := asset.NewService(db, objects)
	credentialRepository := credential.NewRepository(db)
	projectRepository := project.NewRepository(db)
	policyRepository := policy.NewRepository(db)
	jobService := job.NewService(db, credentialRepository, projectRepository, policyRepository, assetService)
	dependencies := Dependencies{
		Authenticator: authn.New(official, 20*time.Second, 32),
		Official:      official,
		Credentials:   credentialRepository,
		Projects:      projectRepository,
		Assets:        assetService,
		Libraries:     library.NewRepository(db),
		Editors:       editor.NewService(db, assetService),
		Jobs:          jobService,
		Policies:      policyRepository,
		Keyring:       keyring,
		WritesEnabled: true,
		Release:       "integration",
		Logger:        logging.New(&logs, slog.LevelDebug),
	}
	handler, err := New(dependencies)
	if err != nil {
		t.Fatal(err)
	}

	response := performRequest(t, handler, http.MethodGet, "/canvas-api/v1/session", "", "")
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d, body = %s", response.Code, response.Body.String())
	}

	readOnlyDependencies := dependencies
	readOnlyDependencies.WritesEnabled = false
	readOnlyHandler, err := New(readOnlyDependencies)
	if err != nil {
		t.Fatal(err)
	}
	response = performRequest(t, readOnlyHandler, http.MethodPost, "/canvas-api/v1/projects", "user-one-token", `{"name":"Blocked","document":{"schema_version":1,"nodes":[]}}`)
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "writes_disabled") {
		t.Fatalf("read-only response = %d, %s", response.Code, response.Body.String())
	}

	response = performRequest(t, handler, http.MethodPost, "/canvas-api/v1/credentials", "user-one-token", `{"external_api_key_id":7}`)
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), fullKey) {
		if strings.Contains(logs.String(), fullKey) {
			t.Fatal("API logs leaked the full key")
		}
		t.Fatalf("bind response = %d, %s; logs=%s", response.Code, response.Body.String(), logs.String())
	}
	var ciphertext []byte
	if err := db.QueryRowContext(ctx, `SELECT ciphertext FROM canvas_credentials WHERE external_user_id = 42 AND external_api_key_id = 7`).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(ciphertext, []byte(fullKey)) {
		t.Fatal("database ciphertext contains the full API key")
	}

	document := `{"schema_version":1,"nodes":[],"edges":[]}`
	response = performRequest(t, handler, http.MethodPost, "/canvas-api/v1/projects", "user-one-token", `{"name":"First project","document":`+document+`}`)
	if response.Code != http.StatusCreated {
		t.Fatalf("create response = %d, %s", response.Code, response.Body.String())
	}
	var created envelope
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(created.Data)
	if err != nil {
		t.Fatal(err)
	}
	var item project.Project
	if err := json.Unmarshal(data, &item); err != nil {
		t.Fatal(err)
	}
	if item.PublicID == "" || item.Version != 1 {
		t.Fatalf("created project = %+v", item)
	}

	response = performRequest(t, handler, http.MethodGet, "/canvas-api/v1/projects/"+item.PublicID, "user-two-token", "")
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-user read = %d, %s", response.Code, response.Body.String())
	}
	response = performRequest(t, handler, http.MethodPatch, "/canvas-api/v1/projects/"+item.PublicID, "user-one-token", `{"name":"Changed","version":1,"document":`+document+`}`)
	if response.Code != http.StatusOK {
		t.Fatalf("update response = %d, %s", response.Code, response.Body.String())
	}
	response = performRequest(t, handler, http.MethodPatch, "/canvas-api/v1/projects/"+item.PublicID, "user-one-token", `{"name":"Stale","version":1,"document":`+document+`}`)
	if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), "project_version_conflict") {
		t.Fatalf("stale update response = %d, %s", response.Code, response.Body.String())
	}

	jobBody := `{"project_id":"` + item.PublicID + `","client_node_id":"node-generation","operation":"generation","api_key_id":7,"selected_model":"gpt-image-2","prompt":"a gray square","input_asset_ids":[],"parameters":{"n":1}}`
	response = performRequestWithHeaders(t, handler, http.MethodPost, "/canvas-api/v1/jobs", "user-one-token", jobBody, map[string]string{"Idempotency-Key": "integration-job-1"})
	if response.Code != http.StatusAccepted {
		t.Fatalf("job create response = %d, %s", response.Code, response.Body.String())
	}
	var jobEnvelope struct {
		Data job.Job `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &jobEnvelope); err != nil || jobEnvelope.Data.PublicID == "" {
		t.Fatalf("decode job response: %+v, %v", jobEnvelope, err)
	}
	processor := job.NewProcessor(jobService, credentialRepository, keyring, official, assetService, "worker_integration")
	processed, err := processor.RunOnce(ctx)
	if err != nil || !processed {
		t.Fatalf("RunOnce() = %v, %v", processed, err)
	}
	response = performRequest(t, handler, http.MethodGet, "/canvas-api/v1/jobs/"+jobEnvelope.Data.PublicID, "user-one-token", "")
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"status":"completed"`) || !strings.Contains(response.Body.String(), `"asset_id":"asset_`) {
		t.Fatalf("completed job response = %d, %s", response.Code, response.Body.String())
	}
	if imageCalls.Load() != 1 {
		t.Fatalf("official image calls = %d, want 1", imageCalls.Load())
	}
	response = performRequestWithHeaders(t, handler, http.MethodPost, "/canvas-api/v1/jobs", "user-one-token", jobBody, map[string]string{"Idempotency-Key": "integration-job-1"})
	if response.Code != http.StatusOK || imageCalls.Load() != 1 {
		t.Fatalf("idempotent create response = %d, calls = %d, body = %s", response.Code, imageCalls.Load(), response.Body.String())
	}
}

func performRequest(t *testing.T, handler http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	return performRequestWithHeaders(t, handler, method, path, token, body, nil)
}

func performRequestWithHeaders(t *testing.T, handler http.Handler, method, path, token, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	for name, value := range headers {
		request.Header.Set(name, value)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func jsonNumber(value int64) string {
	return strconv.FormatInt(value, 10)
}
