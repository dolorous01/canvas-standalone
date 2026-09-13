CREATE TABLE canvas_legacy_media_tasks (
    id BIGSERIAL PRIMARY KEY,
    public_id VARCHAR(64) NOT NULL UNIQUE,
    media_kind VARCHAR(16) NOT NULL CHECK (media_kind IN ('video', 'audio')),
    status VARCHAR(24) NOT NULL,
    phase VARCHAR(32) NOT NULL,
    external_user_id BIGINT NOT NULL CHECK (external_user_id > 0),
    project_id BIGINT NOT NULL REFERENCES canvas_projects(id) ON DELETE RESTRICT,
    client_node_id VARCHAR(128) NOT NULL,
    selected_model VARCHAR(128) NOT NULL,
    successful_model VARCHAR(128),
    request JSONB NOT NULL CHECK (jsonb_typeof(request) = 'object'),
    request_digest CHAR(64) NOT NULL,
    result_asset_id BIGINT REFERENCES canvas_assets(id) ON DELETE SET NULL,
    error JSONB,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ
);

CREATE INDEX idx_canvas_legacy_media_owner_project
    ON canvas_legacy_media_tasks(external_user_id, project_id, created_at DESC);

ALTER TABLE canvas_job_inputs
    ADD COLUMN created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

CREATE TABLE canvas_transfer_imports (
    content_sha256 CHAR(64) PRIMARY KEY,
    export_format VARCHAR(64) NOT NULL,
    counts JSONB NOT NULL CHECK (jsonb_typeof(counts) = 'object'),
    imported_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
