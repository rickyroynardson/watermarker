-- +goose Up
CREATE TYPE image_status AS ENUM ('pending', 'done', 'failed');

CREATE TABLE images(
    id UUID PRIMARY KEY,
    batch_id UUID NOT NULL REFERENCES batches(id) ON DELETE CASCADE,
    status image_status NOT NULL DEFAULT 'pending',
    source_key TEXT NOT NULL,
    output_key TEXT,
    error TEXT,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_images_batch_id_source_key UNIQUE(batch_id, source_key),
    CONSTRAINT ck_images_status_fields CHECK(
        (status = 'pending' AND output_key IS NULL AND error IS NULL) OR
        (status = 'done' AND output_key IS NOT NULL AND error IS NULL) OR
        (status = 'failed' AND error IS NOT NULL)
    )
);

-- +goose Down
DROP TABLE images;

DROP TYPE image_status;
