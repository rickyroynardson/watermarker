-- +goose Up
CREATE TABLE api_keys(
    id UUID PRIMARY KEY,
    name TEXT NOT NULL,
    key_hash TEXT NOT NULL,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    revoked_at TIMESTAMP WITH TIME ZONE,
    CONSTRAINT uq_api_keys_key_hash UNIQUE(key_hash)
);

-- +goose Down
DROP TABLE api_keys;
