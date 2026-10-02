-- +goose Up
CREATE TABLE output_storage (
    key text PRIMARY KEY CHECK (key LIKE 'processed/%'),
    batch_id uuid NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
    user_id uuid NOT NULL REFERENCES users(id),
    bytes bigint NOT NULL CHECK (bytes BETWEEN 1 AND 134217728)
);
CREATE INDEX output_storage_owner ON output_storage(user_id);
-- Old workers did not report sizes. Conservatively estimate 80 MiB per output;
-- a later result with an actual size replaces the estimate.
INSERT INTO output_storage(key,batch_id,user_id,bytes)
SELECT i.output_key,b.id,b.user_id,83886080
FROM images i JOIN batches b ON b.id=i.batch_id WHERE i.output_key IS NOT NULL
ON CONFLICT(key) DO NOTHING;
-- +goose Down
DROP TABLE output_storage;
