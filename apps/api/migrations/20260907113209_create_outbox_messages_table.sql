-- +goose Up
CREATE TABLE outbox_messages(
    image_id UUID PRIMARY KEY REFERENCES images(id) ON DELETE CASCADE,
    payload JSONB NOT NULL,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_outbox_messages_next_attempt_at ON outbox_messages(next_attempt_at, image_id);

-- +goose Down
DROP TABLE outbox_messages;
