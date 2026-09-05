package batch

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type BatchRepository struct {
	dbpool *pgxpool.Pool
}

func NewRepository(db *pgxpool.Pool) *BatchRepository {
	return &BatchRepository{
		dbpool: db,
	}
}

func (r *BatchRepository) ListBatches(ctx context.Context, apiKeyID uuid.UUID, before, beforeID any, limit int) ([]ListBatchItem, error) {
	const q = `
		SELECT id, watermark_key, created_at
		FROM batches
		WHERE api_key_id = $1
			AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3::uuid))
		ORDER BY created_at DESC, id DESC
		LIMIT $4
	`

	rows, err := r.dbpool.Query(ctx, q, apiKeyID, before, beforeID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return pgx.CollectRows(rows, pgx.RowToStructByName[ListBatchItem])
}

func (r *BatchRepository) CreateBatch(ctx context.Context, b Batch) (Batch, bool, error) {
	tx, err := r.dbpool.Begin(ctx)
	if err != nil {
		return Batch{}, false, err
	}
	defer tx.Rollback(ctx)

	const insertBatch = `
		INSERT INTO batches(id, api_key_id, watermark_key, idempotency_key)
		VALUES ($1, $2, $3, NULLIF($4, ''))
		ON CONFLICT (api_key_id, idempotency_key) DO NOTHING
		RETURNING id, watermark_key;
	`

	err = tx.QueryRow(ctx, insertBatch, b.ID, b.APIKeyID, b.WatermarkKey, b.IdempotencyKey).Scan(&b.ID, &b.WatermarkKey)
	if errors.Is(err, pgx.ErrNoRows) {
		var existing Batch

		err := tx.QueryRow(ctx, `
			SELECT id, watermark_key
			FROM batches
			WHERE api_key_id = $1 AND idempotency_key = NULLIF($2, '');
		`, b.APIKeyID, b.IdempotencyKey).Scan(&existing.ID, &existing.WatermarkKey)
		if err != nil {
			return Batch{}, false, err
		}

		rows, err := tx.Query(ctx, `
			SELECT id, source_key
			FROM images
			WHERE batch_id = $1
			ORDER BY source_key;	
		`, existing.ID)
		if err != nil {
			return Batch{}, false, err
		}
		defer rows.Close()

		existing.Images, err = pgx.CollectRows(rows, pgx.RowToStructByName[Image])
		if err != nil {
			return Batch{}, false, err
		}

		if err := tx.Commit(ctx); err != nil {
			return Batch{}, false, err
		}

		return existing, false, nil
	}
	if err != nil {
		return Batch{}, false, err
	}

	const insertImage = `
		INSERT INTO images(id, batch_id, source_key)
		VALUES ($1, $2, $3);
	`
	for _, img := range b.Images {
		if _, err := tx.Exec(ctx, insertImage, img.ID, b.ID, img.SourceKey); err != nil {
			return Batch{}, false, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Batch{}, false, err
	}

	return b, true, nil
}
