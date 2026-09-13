CREATE TABLE canvas_credentials (
    id BIGSERIAL PRIMARY KEY,
    public_id VARCHAR(64) NOT NULL UNIQUE,
    external_user_id BIGINT NOT NULL CHECK (external_user_id > 0),
    external_api_key_id BIGINT NOT NULL CHECK (external_api_key_id > 0),
    display_name VARCHAR(256) NOT NULL,
    key_hint VARCHAR(32) NOT NULL,
    ciphertext BYTEA NOT NULL,
    nonce BYTEA NOT NULL CHECK (octet_length(nonce) = 12),
    key_version INTEGER NOT NULL CHECK (key_version > 0),
    status VARCHAR(24) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled', 'deleted')),
    last_verified_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    UNIQUE (external_user_id, external_api_key_id)
);

CREATE TABLE canvas_model_policies (
    id SMALLINT PRIMARY KEY CHECK (id = 1),
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO canvas_model_policies (id) VALUES (1) ON CONFLICT (id) DO NOTHING;

CREATE TABLE canvas_model_policy_items (
    id BIGSERIAL PRIMARY KEY,
    policy_id SMALLINT NOT NULL REFERENCES canvas_model_policies(id) ON DELETE CASCADE,
    model VARCHAR(128) NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    position INTEGER NOT NULL CHECK (position >= 0),
    capability JSONB NOT NULL CHECK (jsonb_typeof(capability) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (policy_id, model),
    UNIQUE (policy_id, position)
);

CREATE TABLE canvas_model_policy_audits (
    id BIGSERIAL PRIMARY KEY,
    operator_external_user_id BIGINT NOT NULL CHECK (operator_external_user_id > 0),
    request_id VARCHAR(128) NOT NULL,
    old_version BIGINT NOT NULL,
    new_version BIGINT NOT NULL,
    before_value JSONB NOT NULL,
    after_value JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE canvas_projects (
    id BIGSERIAL PRIMARY KEY,
    public_id VARCHAR(64) NOT NULL UNIQUE,
    external_user_id BIGINT NOT NULL CHECK (external_user_id > 0),
    name VARCHAR(160) NOT NULL CHECK (BTRIM(name) <> ''),
    document JSONB NOT NULL CHECK (jsonb_typeof(document) = 'object'),
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    thumbnail_asset_id BIGINT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX idx_canvas_projects_owner_updated ON canvas_projects(external_user_id, updated_at DESC, id DESC) WHERE deleted_at IS NULL;

CREATE TABLE canvas_assets (
    id BIGSERIAL PRIMARY KEY,
    public_id VARCHAR(64) NOT NULL UNIQUE,
    external_user_id BIGINT NOT NULL CHECK (external_user_id > 0),
    project_id BIGINT REFERENCES canvas_projects(id) ON DELETE SET NULL,
    source_type VARCHAR(20) NOT NULL CHECK (source_type IN ('upload', 'generated', 'derived', 'legacy')),
    media_kind VARCHAR(16) NOT NULL CHECK (media_kind IN ('image', 'video', 'audio')),
    object_key TEXT NOT NULL UNIQUE,
    thumbnail_object_key TEXT,
    file_name VARCHAR(512),
    mime_type VARCHAR(100) NOT NULL,
    width INTEGER NOT NULL DEFAULT 0 CHECK (width >= 0),
    height INTEGER NOT NULL DEFAULT 0 CHECK (height >= 0),
    duration_ms BIGINT CHECK (duration_ms IS NULL OR duration_ms >= 0),
    byte_size BIGINT NOT NULL CHECK (byte_size > 0),
    sha256 CHAR(64) NOT NULL,
    delete_after TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

ALTER TABLE canvas_projects ADD CONSTRAINT fk_canvas_project_thumbnail FOREIGN KEY (thumbnail_asset_id) REFERENCES canvas_assets(id) ON DELETE SET NULL;
CREATE INDEX idx_canvas_assets_owner_project ON canvas_assets(external_user_id, project_id, created_at DESC) WHERE deleted_at IS NULL;
CREATE INDEX idx_canvas_assets_delete_after ON canvas_assets(delete_after) WHERE deleted_at IS NOT NULL;

CREATE TABLE canvas_asset_parents (
    asset_id BIGINT NOT NULL REFERENCES canvas_assets(id) ON DELETE CASCADE,
    parent_asset_id BIGINT NOT NULL REFERENCES canvas_assets(id) ON DELETE RESTRICT,
    PRIMARY KEY (asset_id, parent_asset_id),
    CHECK (asset_id <> parent_asset_id)
);

CREATE TABLE canvas_project_asset_refs (
    project_id BIGINT NOT NULL REFERENCES canvas_projects(id) ON DELETE CASCADE,
    asset_id BIGINT NOT NULL REFERENCES canvas_assets(id) ON DELETE RESTRICT,
    node_id VARCHAR(128) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_id, asset_id, node_id)
);
CREATE INDEX idx_canvas_project_asset_refs_asset ON canvas_project_asset_refs(asset_id);

CREATE TABLE canvas_library_items (
    id BIGSERIAL PRIMARY KEY,
    public_id VARCHAR(64) NOT NULL UNIQUE,
    client_id VARCHAR(128) NOT NULL CHECK (BTRIM(client_id) <> ''),
    external_user_id BIGINT NOT NULL CHECK (external_user_id > 0),
    kind VARCHAR(16) NOT NULL CHECK (kind IN ('text', 'image', 'video', 'audio')),
    asset_id BIGINT REFERENCES canvas_assets(id) ON DELETE RESTRICT,
    title VARCHAR(240) NOT NULL CHECK (BTRIM(title) <> ''),
    content TEXT NOT NULL DEFAULT '',
    tags JSONB NOT NULL DEFAULT '[]'::JSONB CHECK (jsonb_typeof(tags) = 'array'),
    source VARCHAR(240) NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    metadata JSONB NOT NULL DEFAULT '{}'::JSONB CHECK (jsonb_typeof(metadata) = 'object'),
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    UNIQUE (external_user_id, client_id),
    CHECK ((kind = 'text' AND asset_id IS NULL) OR (kind IN ('image', 'video', 'audio') AND asset_id IS NOT NULL))
);
CREATE INDEX idx_canvas_library_owner_updated ON canvas_library_items(external_user_id, updated_at DESC, id DESC) WHERE deleted_at IS NULL;

CREATE TABLE canvas_editor_documents (
    id BIGSERIAL PRIMARY KEY,
    public_id VARCHAR(64) NOT NULL UNIQUE,
    project_id BIGINT NOT NULL REFERENCES canvas_projects(id) ON DELETE CASCADE,
    node_id VARCHAR(128) NOT NULL CHECK (BTRIM(node_id) <> ''),
    base_asset_id BIGINT NOT NULL REFERENCES canvas_assets(id) ON DELETE RESTRICT,
    current_asset_id BIGINT NOT NULL REFERENCES canvas_assets(id) ON DELETE RESTRICT,
    document JSONB NOT NULL CHECK (jsonb_typeof(document) = 'object'),
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);
CREATE UNIQUE INDEX idx_canvas_editor_project_node_active ON canvas_editor_documents(project_id, node_id) WHERE deleted_at IS NULL;

CREATE TABLE canvas_editor_asset_refs (
    document_id BIGINT NOT NULL REFERENCES canvas_editor_documents(id) ON DELETE CASCADE,
    asset_id BIGINT NOT NULL REFERENCES canvas_assets(id) ON DELETE RESTRICT,
    role VARCHAR(16) NOT NULL CHECK (role IN ('source', 'layer', 'mask', 'result')),
    element_id VARCHAR(128) NOT NULL CHECK (BTRIM(element_id) <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (document_id, asset_id, role, element_id)
);

CREATE TABLE canvas_editor_revisions (
    id BIGSERIAL PRIMARY KEY,
    public_id VARCHAR(64) NOT NULL UNIQUE,
    document_id BIGINT NOT NULL REFERENCES canvas_editor_documents(id) ON DELETE CASCADE,
    version BIGINT NOT NULL CHECK (version > 0),
    asset_id BIGINT NOT NULL REFERENCES canvas_assets(id) ON DELETE RESTRICT,
    operation VARCHAR(32) NOT NULL CHECK (BTRIM(operation) <> ''),
    parameters JSONB NOT NULL DEFAULT '{}'::JSONB CHECK (jsonb_typeof(parameters) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (document_id, version)
);

CREATE TABLE canvas_jobs (
    id BIGSERIAL PRIMARY KEY,
    public_id VARCHAR(64) NOT NULL UNIQUE,
    external_user_id BIGINT NOT NULL CHECK (external_user_id > 0),
    project_id BIGINT NOT NULL REFERENCES canvas_projects(id) ON DELETE RESTRICT,
    credential_id BIGINT NOT NULL REFERENCES canvas_credentials(id) ON DELETE RESTRICT,
    client_node_id VARCHAR(128) NOT NULL,
    operation VARCHAR(16) NOT NULL CHECK (operation IN ('generation', 'edit')),
    selected_model VARCHAR(128) NOT NULL,
    successful_model VARCHAR(128),
    policy_version BIGINT NOT NULL,
    status VARCHAR(24) NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','partial','completed','failed','canceled','indeterminate','expired')),
    phase VARCHAR(24) NOT NULL DEFAULT 'preflight' CHECK (phase IN ('preflight','leased','validating','upstream','saving')),
    requested_count INTEGER NOT NULL CHECK (requested_count BETWEEN 1 AND 10),
    completed_count INTEGER NOT NULL DEFAULT 0 CHECK (completed_count >= 0),
    request JSONB NOT NULL CHECK (jsonb_typeof(request) = 'object'),
    request_digest CHAR(64) NOT NULL,
    idempotency_key_hash CHAR(64) NOT NULL,
    lease_owner VARCHAR(128),
    lease_expires_at TIMESTAMPTZ,
    upstream_started_at TIMESTAMPTZ,
    official_request_id VARCHAR(128),
    cancel_requested_at TIMESTAMPTZ,
    error_type VARCHAR(64),
    error_code VARCHAR(64),
    error_message VARCHAR(512),
    error_retryable BOOLEAN NOT NULL DEFAULT FALSE,
    legacy_imported BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    UNIQUE (external_user_id, idempotency_key_hash)
);
CREATE INDEX idx_canvas_jobs_claim ON canvas_jobs(status, created_at, id) WHERE status IN ('queued','running') AND legacy_imported = FALSE;
CREATE INDEX idx_canvas_jobs_project_created ON canvas_jobs(project_id, created_at DESC);

CREATE TABLE canvas_job_inputs (
    job_id BIGINT NOT NULL REFERENCES canvas_jobs(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    asset_id BIGINT NOT NULL REFERENCES canvas_assets(id) ON DELETE RESTRICT,
    kind VARCHAR(16) NOT NULL CHECK (kind IN ('image', 'mask')),
    sha256 CHAR(64) NOT NULL,
    PRIMARY KEY (job_id, kind, position)
);

CREATE TABLE canvas_job_results (
    job_id BIGINT NOT NULL REFERENCES canvas_jobs(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    status VARCHAR(24) NOT NULL,
    asset_id BIGINT REFERENCES canvas_assets(id) ON DELETE SET NULL,
    mime_type VARCHAR(100),
    size VARCHAR(64),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (job_id, position)
);

CREATE TABLE canvas_job_attempts (
    id BIGSERIAL PRIMARY KEY,
    job_id BIGINT NOT NULL REFERENCES canvas_jobs(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    model VARCHAR(128) NOT NULL,
    outcome VARCHAR(32) NOT NULL,
    official_request_id VARCHAR(128),
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ,
    UNIQUE (job_id, position)
);

CREATE TABLE canvas_runtime_settings (
    name VARCHAR(128) PRIMARY KEY,
    value JSONB NOT NULL,
    version BIGINT NOT NULL DEFAULT 1 CHECK (version > 0),
    updated_by_external_user_id BIGINT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
