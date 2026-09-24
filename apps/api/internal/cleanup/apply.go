package cleanup

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Reserve rechecks live state and commits permanent key tombstones before S3 deletion.
// ponytail: one global cleanup lock; creators share it, partition by owner if contention grows.
func Reserve(ctx context.Context, db *pgxpool.Pool, before time.Time, limit int) (int, error) {
	if limit < 1 || limit > 1000 {
		return 0, errors.New("limit must be 1-1000")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(823091)"); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, `
 SELECT b.id FROM batches b WHERE b.completed_at < $1 AND b.expired_at IS NULL
 AND NOT EXISTS (SELECT 1 FROM images i WHERE i.batch_id=b.id AND (i.status='pending' OR i.retryable))
 AND NOT EXISTS (SELECT 1 FROM images i JOIN outbox_messages o ON o.image_id=i.id WHERE i.batch_id=b.id)
 ORDER BY b.completed_at, b.id LIMIT $2 FOR UPDATE OF b
 `, before, limit)
	if err != nil {
		return 0, err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return 0, err
	}
	// Recheck after acquiring the batch locks (a retry may have won the race).
	_, err = tx.Exec(ctx, `
 UPDATE batches b SET expired_at=clock_timestamp() WHERE id=ANY($1)
 AND b.completed_at < $2
 AND NOT EXISTS (SELECT 1 FROM images i WHERE i.batch_id=b.id AND (i.status='pending' OR i.retryable))
 AND NOT EXISTS (SELECT 1 FROM images i JOIN outbox_messages o ON o.image_id=i.id WHERE i.batch_id=b.id)
 `, ids, before)
	if err != nil {
		return 0, err
	}
	rows, err = tx.Query(ctx, candidateQuery, before, true, limit)
	if err != nil {
		return 0, err
	}
	candidates, err := pgx.CollectRows(rows, pgx.RowToStructByName[Candidate])
	if err != nil {
		return 0, err
	}
	for _, c := range candidates {
		// Grace exceeds output-link lifetime and normal SQS visibility leases.
		_, err = tx.Exec(ctx, "INSERT INTO cleanup_objects(key) VALUES($1)", c.Key)
		if err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(candidates), nil
}

// DeleteOne holds only the cleanup row while making a bounded, idempotent S3 call.
func DeleteOne(ctx context.Context, db *pgxpool.Pool, remove func(context.Context, string) error) (bool, error) {
	tx, err := db.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var key string
	err = tx.QueryRow(ctx, `SELECT key FROM cleanup_objects
 WHERE deleted_at IS NULL AND delete_after<=now() AND next_attempt_at<=now()
 ORDER BY next_attempt_at,key LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	deleteErr := remove(callCtx, key)
	cancel()
	if deleteErr != nil {
		_, err = tx.Exec(ctx, "UPDATE cleanup_objects SET attempts=attempts+1,last_error=$2,next_attempt_at=now()+interval '5 minutes' WHERE key=$1", key, deleteErr.Error())
	} else {
		_, err = tx.Exec(ctx, "UPDATE cleanup_objects SET attempts=attempts+1,last_error=NULL,deleted_at=clock_timestamp() WHERE key=$1", key)
	}
	if err != nil {
		return true, err
	}
	if err = tx.Commit(ctx); err != nil {
		return true, err
	}
	return true, deleteErr
}

func Counts(ctx context.Context, db *pgxpool.Pool) (counts [3]int64, err error) {
	err = db.QueryRow(ctx, `SELECT count(*) FILTER(WHERE deleted_at IS NULL),
 count(*) FILTER(WHERE deleted_at IS NULL AND last_error IS NOT NULL),
 count(*) FILTER(WHERE deleted_at IS NOT NULL) FROM cleanup_objects`).Scan(&counts[0], &counts[1], &counts[2])
	return
}
