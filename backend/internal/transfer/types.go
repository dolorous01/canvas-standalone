package transfer

import (
	"encoding/json"
	"time"
)

const (
	Format           = "canvas-standalone-export/v1"
	ResolutionFormat = "canvas-standalone-resolution/v1"
	AuditFormat      = "canvas-standalone-source-audit/v1"
	ReportFormat     = "canvas-standalone-transfer-report/v1"

	ResolutionImportAsIndeterminate = "import_as_indeterminate_history"
)

const (
	projectsFile         = "projects.ndjson"
	assetsFile           = "assets.ndjson"
	projectRefsFile      = "project-asset-refs.ndjson"
	libraryItemsFile     = "library-items.ndjson"
	editorDocumentsFile  = "editor-documents.ndjson"
	editorRefsFile       = "editor-asset-refs.ndjson"
	editorRevisionsFile  = "editor-revisions.ndjson"
	modelPoliciesFile    = "model-policies.ndjson"
	modelPolicyItemsFile = "model-policy-items.ndjson"
	policyAuditsFile     = "model-policy-audits.ndjson"
	runtimeSettingsFile  = "runtime-settings.ndjson"
	jobsFile             = "terminal-jobs.ndjson"
	jobInputsFile        = "terminal-job-inputs.ndjson"
	jobResultsFile       = "terminal-job-results.ndjson"
	legacyMediaTasksFile = "terminal-media-tasks.ndjson"
	objectsFile          = "objects.ndjson"
	objectsChecksumFile  = "objects.sha256"
	manifestFile         = "manifest.json"
)

var dataFiles = []string{
	projectsFile,
	assetsFile,
	projectRefsFile,
	libraryItemsFile,
	editorDocumentsFile,
	editorRefsFile,
	editorRevisionsFile,
	modelPoliciesFile,
	modelPolicyItemsFile,
	policyAuditsFile,
	runtimeSettingsFile,
	jobsFile,
	jobInputsFile,
	jobResultsFile,
	legacyMediaTasksFile,
	objectsFile,
	objectsChecksumFile,
}

type Manifest struct {
	Format           string                 `json:"format"`
	ExporterVersion  string                 `json:"exporter_version"`
	GeneratedAt      time.Time              `json:"generated_at"`
	SourceMigrations []SourceMigration      `json:"source_migrations"`
	Files            map[string]FileSummary `json:"files"`
	Counts           map[string]int         `json:"counts"`
	StatusCounts     map[string]int         `json:"status_counts"`
	Unresolved       []UnresolvedItem       `json:"unresolved"`
	Resolutions      []ResolutionDecision   `json:"resolutions"`
	Objects          ObjectSummary          `json:"objects"`
	ContentSHA256    string                 `json:"content_sha256"`
	Warnings         []string               `json:"warnings"`
}

type FileSummary struct {
	Rows   int    `json:"rows"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type SourceMigration struct {
	Filename string `json:"filename"`
	Checksum string `json:"checksum"`
}

type ObjectSummary struct {
	Count  int    `json:"count"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

type UnresolvedItem struct {
	Kind     string `json:"kind"`
	PublicID string `json:"public_id"`
	Status   string `json:"status"`
	Phase    string `json:"phase"`
}

type ResolutionFile struct {
	Format    string               `json:"format"`
	Decisions []ResolutionDecision `json:"decisions"`
}

type ResolutionDecision struct {
	Kind     string `json:"kind"`
	PublicID string `json:"public_id"`
	Action   string `json:"action"`
	Reason   string `json:"reason"`
}

type Project struct {
	PublicID               string          `json:"public_id"`
	ExternalUserID         int64           `json:"external_user_id"`
	Name                   string          `json:"name"`
	Document               json.RawMessage `json:"document"`
	Version                int64           `json:"version"`
	ThumbnailAssetPublicID string          `json:"thumbnail_asset_public_id,omitempty"`
	CreatedAt              time.Time       `json:"created_at"`
	UpdatedAt              time.Time       `json:"updated_at"`
	DeletedAt              *time.Time      `json:"deleted_at,omitempty"`
}

type Asset struct {
	PublicID        string     `json:"public_id"`
	ExternalUserID  int64      `json:"external_user_id"`
	ProjectPublicID string     `json:"project_public_id,omitempty"`
	SourceType      string     `json:"source_type"`
	MediaKind       string     `json:"media_kind"`
	ObjectKey       string     `json:"object_key"`
	ThumbnailKey    string     `json:"thumbnail_object_key,omitempty"`
	FileName        string     `json:"file_name,omitempty"`
	MIMEType        string     `json:"mime_type"`
	Width           int        `json:"width"`
	Height          int        `json:"height"`
	DurationMS      *int64     `json:"duration_ms,omitempty"`
	ByteSize        int64      `json:"byte_size"`
	SHA256          string     `json:"sha256"`
	ParentPublicIDs []string   `json:"parent_public_ids,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
}

type ProjectAssetRef struct {
	ProjectPublicID string    `json:"project_public_id"`
	AssetPublicID   string    `json:"asset_public_id"`
	NodeID          string    `json:"node_id"`
	CreatedAt       time.Time `json:"created_at"`
}

type LibraryItem struct {
	PublicID       string          `json:"public_id"`
	ClientID       string          `json:"client_id"`
	ExternalUserID int64           `json:"external_user_id"`
	Kind           string          `json:"kind"`
	AssetPublicID  string          `json:"asset_public_id,omitempty"`
	Title          string          `json:"title"`
	Content        string          `json:"content"`
	Tags           json.RawMessage `json:"tags"`
	Source         string          `json:"source"`
	Note           string          `json:"note"`
	Metadata       json.RawMessage `json:"metadata"`
	Version        int64           `json:"version"`
	CreatedAt      time.Time       `json:"created_at"`
	UpdatedAt      time.Time       `json:"updated_at"`
	DeletedAt      *time.Time      `json:"deleted_at,omitempty"`
}

type EditorDocument struct {
	PublicID             string          `json:"public_id"`
	ProjectPublicID      string          `json:"project_public_id"`
	NodeID               string          `json:"node_id"`
	BaseAssetPublicID    string          `json:"base_asset_public_id"`
	CurrentAssetPublicID string          `json:"current_asset_public_id"`
	Document             json.RawMessage `json:"document"`
	Version              int64           `json:"version"`
	CreatedAt            time.Time       `json:"created_at"`
	UpdatedAt            time.Time       `json:"updated_at"`
	DeletedAt            *time.Time      `json:"deleted_at,omitempty"`
}

type EditorAssetRef struct {
	DocumentPublicID string    `json:"document_public_id"`
	AssetPublicID    string    `json:"asset_public_id"`
	Role             string    `json:"role"`
	ElementID        string    `json:"element_id"`
	CreatedAt        time.Time `json:"created_at"`
}

type EditorRevision struct {
	PublicID         string          `json:"public_id"`
	DocumentPublicID string          `json:"document_public_id"`
	Version          int64           `json:"version"`
	AssetPublicID    string          `json:"asset_public_id"`
	Operation        string          `json:"operation"`
	Parameters       json.RawMessage `json:"parameters"`
	CreatedAt        time.Time       `json:"created_at"`
}

type ModelPolicy struct {
	Version   int64     `json:"version"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type ModelPolicyItem struct {
	Model      string          `json:"model"`
	Enabled    bool            `json:"enabled"`
	Position   int             `json:"position"`
	Capability json.RawMessage `json:"capability"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
}

type PolicyAudit struct {
	LegacyID               int64           `json:"legacy_id"`
	OperatorExternalUserID int64           `json:"operator_external_user_id"`
	RequestID              string          `json:"request_id"`
	OldVersion             int64           `json:"old_version"`
	NewVersion             int64           `json:"new_version"`
	Before                 json.RawMessage `json:"before"`
	After                  json.RawMessage `json:"after"`
	CreatedAt              time.Time       `json:"created_at"`
}

type RuntimeSetting struct {
	Name                    string          `json:"name"`
	Value                   json.RawMessage `json:"value"`
	Version                 int64           `json:"version"`
	UpdatedByExternalUserID *int64          `json:"updated_by_external_user_id,omitempty"`
	UpdatedAt               time.Time       `json:"updated_at"`
}

type Job struct {
	PublicID          string          `json:"public_id"`
	ExternalUserID    int64           `json:"external_user_id"`
	ProjectPublicID   string          `json:"project_public_id"`
	ClientNodeID      string          `json:"client_node_id"`
	Operation         string          `json:"operation"`
	SelectedModel     string          `json:"selected_model"`
	SuccessfulModel   string          `json:"successful_model,omitempty"`
	PolicyVersion     int64           `json:"policy_version"`
	Status            string          `json:"status"`
	Phase             string          `json:"phase"`
	RequestedCount    int             `json:"requested_count"`
	CompletedCount    int             `json:"completed_count"`
	Request           json.RawMessage `json:"request"`
	RequestDigest     string          `json:"request_digest"`
	IdempotencyHash   string          `json:"idempotency_key_hash"`
	AttemptPlan       json.RawMessage `json:"attempt_plan"`
	AttemptPosition   int             `json:"attempt_position"`
	CancelRequestedAt *time.Time      `json:"cancel_requested_at,omitempty"`
	ErrorType         string          `json:"error_type,omitempty"`
	ErrorCode         string          `json:"error_code,omitempty"`
	ErrorMessage      string          `json:"error_message,omitempty"`
	ErrorRetryable    bool            `json:"error_retryable"`
	CreatedAt         time.Time       `json:"created_at"`
	UpdatedAt         time.Time       `json:"updated_at"`
	FinishedAt        *time.Time      `json:"finished_at,omitempty"`
}

type JobInput struct {
	JobPublicID   string    `json:"job_public_id"`
	Position      int       `json:"position"`
	Kind          string    `json:"kind"`
	AssetPublicID string    `json:"asset_public_id"`
	SHA256        string    `json:"sha256"`
	CreatedAt     time.Time `json:"created_at"`
}

type JobResult struct {
	JobPublicID   string    `json:"job_public_id"`
	Position      int       `json:"position"`
	Status        string    `json:"status"`
	AssetPublicID string    `json:"asset_public_id,omitempty"`
	MIMEType      string    `json:"mime_type,omitempty"`
	Size          string    `json:"size,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type LegacyMediaTask struct {
	PublicID            string          `json:"public_id"`
	MediaKind           string          `json:"media_kind"`
	Status              string          `json:"status"`
	Phase               string          `json:"phase"`
	ExternalUserID      int64           `json:"external_user_id"`
	ProjectPublicID     string          `json:"project_public_id"`
	ClientNodeID        string          `json:"client_node_id"`
	SelectedModel       string          `json:"selected_model"`
	SuccessfulModel     string          `json:"successful_model,omitempty"`
	Request             json.RawMessage `json:"request"`
	RequestDigest       string          `json:"request_digest"`
	ResultAssetPublicID string          `json:"result_asset_public_id,omitempty"`
	Error               json.RawMessage `json:"error,omitempty"`
	CreatedAt           time.Time       `json:"created_at"`
	UpdatedAt           time.Time       `json:"updated_at"`
	FinishedAt          *time.Time      `json:"finished_at,omitempty"`
}

type Object struct {
	AssetPublicID string `json:"asset_public_id"`
	Kind          string `json:"kind"`
	SourceKey     string `json:"source_key"`
	TargetKey     string `json:"target_key"`
	MIMEType      string `json:"mime_type"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
}

type AuditReport struct {
	Format           string            `json:"format"`
	GeneratedAt      time.Time         `json:"generated_at"`
	SourceMigrations []SourceMigration `json:"source_migrations"`
	Counts           map[string]int    `json:"counts"`
	StatusCounts     map[string]int    `json:"status_counts"`
	Unresolved       []UnresolvedItem  `json:"unresolved"`
	Objects          ObjectSummary     `json:"objects"`
	Warnings         []string          `json:"warnings"`
}

type Report struct {
	Format        string         `json:"format"`
	Operation     string         `json:"operation"`
	Verified      bool           `json:"verified"`
	ContentSHA256 string         `json:"content_sha256"`
	Counts        map[string]int `json:"counts"`
	ObjectCount   int            `json:"object_count"`
	ObjectBytes   int64          `json:"object_bytes"`
	CompletedAt   time.Time      `json:"completed_at"`
}

type dataset struct {
	Projects         []Project
	Assets           []Asset
	ProjectAssetRefs []ProjectAssetRef
	LibraryItems     []LibraryItem
	EditorDocuments  []EditorDocument
	EditorAssetRefs  []EditorAssetRef
	EditorRevisions  []EditorRevision
	ModelPolicies    []ModelPolicy
	ModelPolicyItems []ModelPolicyItem
	PolicyAudits     []PolicyAudit
	RuntimeSettings  []RuntimeSetting
	Jobs             []Job
	JobInputs        []JobInput
	JobResults       []JobResult
	LegacyMediaTasks []LegacyMediaTask
	Objects          []Object
}
