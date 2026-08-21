package batch

import (
	"context"

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
