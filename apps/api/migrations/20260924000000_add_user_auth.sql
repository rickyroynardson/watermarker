-- +goose Up
CREATE TABLE users (
    id uuid PRIMARY KEY,
    issuer text,
    subject text,
    name text NOT NULL DEFAULT 'User',
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (issuer, subject),
    CHECK ((issuer IS NULL) = (subject IS NULL) AND (issuer IS NULL OR (issuer <> '' AND subject <> '')))
);
-- Preserve existing storage prefixes and ownership without moving S3 objects.
INSERT INTO users(id, name) SELECT id, name FROM api_keys;
ALTER TABLE api_keys ADD COLUMN user_id uuid REFERENCES users(id);
UPDATE api_keys SET user_id=id;
ALTER TABLE api_keys ALTER COLUMN user_id SET NOT NULL;
CREATE INDEX api_keys_user_id ON api_keys(user_id);
ALTER TABLE batches DROP CONSTRAINT batches_api_key_id_fkey;
ALTER TABLE batches RENAME COLUMN api_key_id TO user_id;
ALTER TABLE batches ADD CONSTRAINT batches_user_id_fkey FOREIGN KEY(user_id) REFERENCES users(id);
ALTER TABLE batches RENAME CONSTRAINT uq_batches_api_key_id_idempotency_key TO uq_batches_user_id_idempotency_key;
ALTER INDEX idx_batches_api_key_id_created_at RENAME TO idx_batches_user_id_created_at;
CREATE TABLE auth_logins (
    state_hash text PRIMARY KEY,
    verifier text NOT NULL,
    nonce text NOT NULL,
    expires_at timestamptz NOT NULL
);
CREATE TABLE auth_sessions (
    token_hash text PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id),
    expires_at timestamptz NOT NULL
);
CREATE INDEX auth_sessions_expires ON auth_sessions(expires_at);
CREATE INDEX auth_logins_expires ON auth_logins(expires_at);
-- +goose Down
-- User-owned batches cannot safely be mapped back to one credential after key rotation.
-- Restore a pre-migration backup instead of silently changing ownership.
-- +goose StatementBegin
DO $$ BEGIN RAISE EXCEPTION 'User ownership migration requires backup restore to roll back'; END $$;
-- +goose StatementEnd
