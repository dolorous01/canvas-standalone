INSERT INTO canvas_model_policy_items (policy_id, model, enabled, position, capability)
VALUES (
    1,
    'gpt-image-2',
    TRUE,
    0,
    '{
      "media_kind":"image",
      "provider":"openai",
      "dimension_mode":"size",
      "generation":true,
      "edit":true,
      "multi_image":true,
      "mask":true,
      "max_input_images":10,
      "max_outputs":4,
      "sizes":["1024x1024","1536x1024","1024x1536"],
      "qualities":["low","medium","high"],
      "output_formats":["png","jpeg","webp"],
      "backgrounds":["auto","transparent","opaque"],
      "output_compression":true,
      "defaults":{"size":"1024x1024","quality":"medium","output_format":"png","background":"auto"}
    }'::JSONB
)
ON CONFLICT (policy_id, model) DO NOTHING;

CREATE TABLE canvas_audit_events (
    id BIGSERIAL PRIMARY KEY,
    event_type VARCHAR(64) NOT NULL,
    external_user_id BIGINT NOT NULL CHECK (external_user_id > 0),
    subject_public_id VARCHAR(128),
    request_id VARCHAR(128) NOT NULL,
    detail JSONB NOT NULL DEFAULT '{}'::JSONB CHECK (jsonb_typeof(detail) = 'object'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_canvas_audit_events_user_created
    ON canvas_audit_events(external_user_id, created_at DESC, id DESC);
