package batch

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyroynardson/watermarker/apps/api/internal/tracing"
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
		SELECT id, watermark_key, created_at, completed_at,
 EXTRACT(EPOCH FROM (completed_at - created_at))::double precision AS duration_seconds
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

	// The job intent commits atomically with the batch and its images.
	_, err = tx.Exec(ctx, `
		INSERT INTO outbox_messages (image_id, payload)
		SELECT id, jsonb_build_object(
			'version', 1, 'job_type', 'composite', 'batch_id', batch_id,
			'image_id', id, 'source_key', source_key, 'watermark_key', $2::text,
			'trace_context', $3::jsonb
		)
		FROM images WHERE batch_id = $1;
	`, b.ID, b.WatermarkKey, tracing.Carrier(ctx))
	if err != nil {
		return Batch{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Batch{}, err
	}

	return b, nil
}

func (r *BatchRepository) GetBatch(ctx context.Context, owner, id uuid.UUID) (BatchDetails, error) {
	var b BatchDetails
	err := r.dbpool.QueryRow(ctx, `
		SELECT id, watermark_key, created_at, completed_at,
 EXTRACT(EPOCH FROM (completed_at - created_at))::double precision AS duration_seconds, cancelled_at FROM batches
		WHERE id = $1 AND api_key_id = $2
	`, id, owner).Scan(&b.ID, &b.WatermarkKey, &b.CreatedAt, &b.CompletedAt, &b.DurationSeconds, &b.CancelledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrBatchNotFound
	}
	if err != nil {
		return b, err
	}
	rows, err := r.dbpool.Query(ctx, `
		SELECT id, source_key, status, COALESCE(output_key, ''),
			COALESCE(error, ''), updated_at, attempt, retryable FROM images
		WHERE batch_id = $1 ORDER BY created_at, id
	`, id)
	if err != nil {
		return b, err
	}
	defer rows.Close()
	b.Images, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (ImageDetails, error) {
		var image ImageDetails
		err := row.Scan(&image.ID, &image.SourceKey, &image.Status, &image.OutputKey, &image.Error, &image.UpdatedAt, &image.Attempt, &image.Retryable)
		return image, err
	})
	return b, err
}

// RetryImage serializes with results and fences duplicate requests by their observed attempt.
func (r *BatchRepository) RetryImage(ctx context.Context, owner, batchID, imageID uuid.UUID, attempt int) error {
	tx, err := r.dbpool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var watermark string
	var cancelled bool
	err = tx.QueryRow(ctx, "SELECT watermark_key, cancelled_at IS NOT NULL FROM batches WHERE id=$1 AND api_key_id=$2 FOR UPDATE", batchID, owner).Scan(&watermark, &cancelled)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBatchNotFound
	}
	if err != nil {
		return err
	}
	if cancelled {
		return ErrRetryConflict
	}
	var current int
	var retryable bool
	err = tx.QueryRow(ctx, "SELECT attempt, retryable FROM images WHERE id=$1 AND batch_id=$2", imageID, batchID).Scan(&current, &retryable)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBatchNotFound
	}
	if err != nil {
		return err
	}
	// A repeated request succeeds without creating another job.
	if current == attempt+1 {
		return tx.Commit(ctx)
	}
	if current != attempt || !retryable {
		return ErrRetryConflict
	}
	_, err = tx.Exec(ctx, `
 UPDATE images SET status='pending', error=NULL, output_key=NULL, retryable=false,
 attempt=attempt+1, updated_at=clock_timestamp() WHERE id=$1;
 `, imageID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
 INSERT INTO outbox_messages(image_id,payload)
 SELECT id,jsonb_build_object('version',1,'job_type','composite','batch_id',batch_id,
 'image_id',id,'source_key',source_key,'watermark_key',$2::text,'attempt',attempt,'trace_context',$3::jsonb)
 FROM images WHERE id=$1
 ON CONFLICT (image_id) DO UPDATE SET payload=EXCLUDED.payload, next_attempt_at=now()
 `, imageID, watermark, tracing.Carrier(ctx))
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE batches SET completed_at=NULL WHERE id=$1", batchID)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Cancel uses the same batch lock as result handling and retry.
func (r *BatchRepository) Cancel(ctx context.Context, owner, id uuid.UUID) error {
	tx, err := r.dbpool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var cancelled bool
	err = tx.QueryRow(ctx, "SELECT cancelled_at IS NOT NULL FROM batches WHERE id=$1 AND api_key_id=$2 FOR UPDATE", id, owner).Scan(&cancelled)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrBatchNotFound
	}
	if err != nil {
		return err
	}
	if cancelled {
		return tx.Commit(ctx)
	}
	tag, err := tx.Exec(ctx, "UPDATE images SET status='cancelled', error=NULL, output_key=NULL, retryable=false, updated_at=clock_timestamp() WHERE batch_id=$1 AND (status='pending' OR retryable)", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrCancelConflict
	}
	_, err = tx.Exec(ctx, "DELETE FROM outbox_messages WHERE image_id IN (SELECT id FROM images WHERE batch_id=$1)", id)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE batches SET cancelled_at=clock_timestamp(), completed_at=clock_timestamp() WHERE id=$1", id)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
