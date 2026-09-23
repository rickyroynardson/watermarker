-- +goose Up
ALTER TYPE image_status ADD VALUE IF NOT EXISTS 'cancelled';
ALTER TABLE batches ADD COLUMN cancelled_at timestamptz;
ALTER TABLE images DROP CONSTRAINT ck_images_status_fields;
ALTER TABLE images ADD CONSTRAINT ck_images_status_fields CHECK (
 (status::text IN ('pending', 'cancelled') AND output_key IS NULL AND error IS NULL)
 OR (status::text = 'done' AND output_key IS NOT NULL AND error IS NULL)
 OR (status::text = 'failed' AND error IS NOT NULL)
);

-- +goose Down
UPDATE images SET status='failed', error='Cancelled' WHERE status::text='cancelled';
ALTER TABLE batches DROP COLUMN cancelled_at;
ALTER TABLE images DROP CONSTRAINT ck_images_status_fields;
ALTER TABLE images ADD CONSTRAINT ck_images_status_fields CHECK (
 (status = 'pending' AND output_key IS NULL AND error IS NULL)
 OR (status = 'done' AND output_key IS NOT NULL AND error IS NULL)
 OR (status = 'failed' AND error IS NOT NULL)
);
-- PostgreSQL keeps the unused enum label on rollback.
