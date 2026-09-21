-- +goose Up
ALTER TABLE images ADD COLUMN attempt integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
 ADD COLUMN retryable boolean NOT NULL DEFAULT false,
 ADD CONSTRAINT retryable_failed CHECK (NOT retryable OR status = 'failed');

-- +goose Down
ALTER TABLE images DROP COLUMN retryable, DROP COLUMN attempt;
