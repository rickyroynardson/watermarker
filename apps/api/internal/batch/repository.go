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

func (r *BatchRepository) FindByIdempotencyKey(ctx context.Context, apiKeyID uuid.UUID, key string) (Batch, bool, error) {
	var b Batch
	err := r.dbpool.QueryRow(ctx, `
		SELECT id, watermark_key
		FROM batches
		WHERE api_key_id = $1 AND idempotency_key = NULLIF($2, '');
	`, apiKeyID, key).Scan(&b.ID, &b.WatermarkKey)
	if errors.Is(err, pgx.ErrNoRows) {
		return Batch{}, false, nil
	}
	if err != nil {
		return Batch{}, false, err
	}

	rows, err := r.dbpool.Query(ctx, `
		SELECT id, source_key
		FROM images
		WHERE batch_id = $1
		ORDER BY source_key;
	`, b.ID)
	if err != nil {
		return Batch{}, false, err
	}
	defer rows.Close()
	b.Images, err = pgx.CollectRows(rows, pgx.RowToStructByName[Image])
	return b, err == nil, err
}

func (r *BatchRepository) CreateBatch(ctx context.Context, b Batch) (Batch, error) {
	tx, err := r.dbpool.Begin(ctx)
	if err != nil {
		return Batch{}, err
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
		// Release the connection before looking up the committed winner.
		if err := tx.Rollback(ctx); err != nil {
			return Batch{}, err
		}
		existing, found, err := r.FindByIdempotencyKey(ctx, b.APIKeyID, b.IdempotencyKey)
		if err == nil && !found {
			err = pgx.ErrNoRows
		}
		return existing, err
	}
	if err != nil {
		return Batch{}, err
	}

	// Insert images in a single batch using the CopyFrom API.
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"images"}, []string{"id", "batch_id", "source_key"},
		pgx.CopyFromSlice(len(b.Images), func(i int) ([]any, error) {
			img := b.Images[i]
			return []any{img.ID, b.ID, img.SourceKey}, nil
		}))
	if err != nil {
		return Batch{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Batch{}, err
	}

	return b, nil
}
