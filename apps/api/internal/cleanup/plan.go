package cleanup

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Candidate struct {
	Key      string      `json:"key"`
	BatchIDs []uuid.UUID `json:"batch_ids"`
}

// Plan is a single SELECT snapshot, not a deletion authorization.
// ponytail: scan all references for this dry-run; paginate if inventory size warrants it.
func Plan(ctx context.Context, db *pgxpool.Pool, before time.Time) ([]Candidate, error) {
	rows, err := db.Query(ctx, `
 WITH eligible AS (
   SELECT b.id FROM batches b
   WHERE b.completed_at < $1
     AND NOT EXISTS (SELECT 1 FROM images i WHERE i.batch_id=b.id AND (i.status='pending' OR i.retryable))
     AND NOT EXISTS (SELECT 1 FROM images i JOIN outbox_messages o ON o.image_id=i.id WHERE i.batch_id=b.id)
 ), refs AS (
   SELECT id AS batch_id, watermark_key AS key FROM batches
   UNION
   SELECT batch_id, source_key FROM images
   UNION
   SELECT batch_id, output_key FROM images WHERE output_key IS NOT NULL
 )
 SELECT r.key, array_agg(DISTINCT r.batch_id ORDER BY r.batch_id) AS batch_ids
 FROM refs r
 WHERE (r.key LIKE 'sources/%' OR r.key LIKE 'processed/%')
 GROUP BY r.key
 HAVING bool_and(r.batch_id IN (SELECT id FROM eligible))
 ORDER BY r.key
 `, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates, err := pgx.CollectRows(rows, pgx.RowToStructByName[Candidate])
	if candidates == nil {
		candidates = []Candidate{}
	}
	return candidates, err
}
