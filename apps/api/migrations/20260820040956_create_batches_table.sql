-- +goose Up
CREATE TABLE batches(
    id UUID PRIMARY KEY,
    api_key_id UUID NOT NULL REFERENCES api_keys(id),
    watermark_key TEXT NOT NULL,
    idempotency_key TEXT,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_batches_api_key_id_idempotency_key UNIQUE(api_key_id, idempotency_key)
);

CREATE INDEX idx_batches_api_key_id_created_at ON batches(api_key_id, created_at DESC, id DESC);

-- +goose Down
DROP TABLE batches;
