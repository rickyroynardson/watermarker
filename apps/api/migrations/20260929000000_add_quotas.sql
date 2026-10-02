-- +goose Up
ALTER TABLE cleanup_objects DROP CONSTRAINT cleanup_objects_key_check;
ALTER TABLE cleanup_objects ADD CONSTRAINT cleanup_objects_key_check CHECK (key LIKE 'sources/%' OR key LIKE 'processed/%' OR key LIKE 'uploads/%');
CREATE TABLE quota_plans (
    name text PRIMARY KEY,
    included_bytes bigint NOT NULL CHECK (included_bytes > 0)
);
INSERT INTO quota_plans VALUES ('free', 104857600), ('pro', 1073741824);
ALTER TABLE users ADD COLUMN quota_plan text NOT NULL DEFAULT 'free' REFERENCES quota_plans(name);
ALTER TABLE users ADD COLUMN addon_bytes bigint NOT NULL DEFAULT 0 CHECK (addon_bytes BETWEEN 0 AND 10737418240);
CREATE TABLE upload_reservations (
    key text PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id),
    bytes bigint NOT NULL CHECK (bytes BETWEEN 1 AND 10485760),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX upload_reservations_owner ON upload_reservations(user_id);
-- Legacy objects have no size metadata: conservatively charge the old 10 MiB upload ceiling.
INSERT INTO upload_reservations(key,user_id,bytes)
SELECT watermark_key,user_id,10485760 FROM batches
UNION
SELECT i.source_key,b.user_id,10485760 FROM images i JOIN batches b ON b.id=i.batch_id
ON CONFLICT(key) DO NOTHING;
CREATE TABLE quota_addons (
    user_id uuid NOT NULL REFERENCES users(id),
    request_id uuid NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY(user_id, request_id)
);
-- +goose Down
DROP TABLE quota_addons;
DROP TABLE upload_reservations;
ALTER TABLE users DROP COLUMN addon_bytes, DROP COLUMN quota_plan;
DROP TABLE quota_plans;
