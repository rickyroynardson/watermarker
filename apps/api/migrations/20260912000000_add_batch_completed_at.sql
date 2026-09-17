-- +goose Up
ALTER TABLE batches ADD COLUMN completed_at timestamptz;
-- Preserve historical completion times without emitting historical metrics.
UPDATE batches b SET completed_at = (
    SELECT max(updated_at) FROM images WHERE batch_id = b.id
)
WHERE EXISTS (SELECT 1 FROM images WHERE batch_id = b.id)
  AND NOT EXISTS (SELECT 1 FROM images WHERE batch_id = b.id AND status = 'pending');

-- +goose Down
ALTER TABLE batches DROP COLUMN completed_at;
