-- +goose Up
ALTER TABLE batches ADD COLUMN expired_at timestamptz;
CREATE TABLE cleanup_objects (
 key text PRIMARY KEY CHECK (key LIKE 'sources/%' OR key LIKE 'processed/%'),
 created_at timestamptz NOT NULL DEFAULT now(),
 delete_after timestamptz NOT NULL DEFAULT (now() + interval '24 hours'),
 next_attempt_at timestamptz NOT NULL DEFAULT now(),
 attempts integer NOT NULL DEFAULT 0,
 last_error text,
 deleted_at timestamptz
);
CREATE INDEX cleanup_objects_due ON cleanup_objects(next_attempt_at) WHERE deleted_at IS NULL;
-- +goose Down
-- Deleted S3 data cannot be restored by rolling back this migration.
DROP TABLE cleanup_objects;
ALTER TABLE batches DROP COLUMN expired_at;
