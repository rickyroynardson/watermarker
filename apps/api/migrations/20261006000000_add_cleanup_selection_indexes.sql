-- +goose Up
-- Match expiry filtering and ordering; exclude retained tombstones and unfinished batches.
CREATE INDEX idx_batches_cleanup_expiry ON batches(completed_at, id)
WHERE expired_at IS NULL AND completed_at IS NOT NULL;

-- Claim one pending object in retry/key order without sorting a timestamp tie group.
DROP INDEX cleanup_objects_due;
CREATE INDEX cleanup_objects_due ON cleanup_objects(next_attempt_at, key)
WHERE deleted_at IS NULL;

-- +goose Down
DROP INDEX idx_batches_cleanup_expiry;
DROP INDEX cleanup_objects_due;
CREATE INDEX cleanup_objects_due ON cleanup_objects(next_attempt_at)
WHERE deleted_at IS NULL;
