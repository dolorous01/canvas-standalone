ALTER TABLE canvas_jobs ALTER COLUMN credential_id DROP NOT NULL;
ALTER TABLE canvas_jobs ADD COLUMN attempt_plan JSONB NOT NULL DEFAULT '[]'::JSONB CHECK (jsonb_typeof(attempt_plan) = 'array');
ALTER TABLE canvas_jobs ADD COLUMN attempt_position INTEGER NOT NULL DEFAULT 0 CHECK (attempt_position >= 0);

ALTER TABLE canvas_jobs ADD CONSTRAINT canvas_jobs_credential_required_for_live_check
    CHECK (legacy_imported OR credential_id IS NOT NULL);
