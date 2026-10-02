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

const candidateQuery = `
 WITH eligible AS (
   SELECT b.id FROM batches b
   WHERE (b.completed_at < $1 OR b.expired_at IS NOT NULL)
     AND (NOT $2::boolean OR b.expired_at IS NOT NULL)
     AND NOT EXISTS (SELECT 1 FROM images i WHERE i.batch_id=b.id AND (i.status='pending' OR i.retryable))
     AND NOT EXISTS (SELECT 1 FROM images i JOIN outbox_messages o ON o.image_id=i.id WHERE i.batch_id=b.id)
 ), refs AS (
   SELECT id AS batch_id, watermark_key AS key FROM batches
   UNION
   SELECT batch_id, source_key FROM images
   UNION
   SELECT batch_id, output_key FROM images WHERE output_key IS NOT NULL
   UNION
   SELECT batch_id, key FROM output_storage
 )
 , abandoned AS (
 SELECT r.key FROM upload_reservations r WHERE r.created_at<now()-interval '24 hours'
 AND NOT EXISTS(SELECT 1 FROM refs WHERE refs.key=r.key)
 ), candidates AS (
 SELECT r.key, array_agg(DISTINCT r.batch_id ORDER BY r.batch_id) AS batch_ids
 FROM refs r
 WHERE (r.key LIKE 'sources/%' OR r.key LIKE 'processed/%')
 AND NOT EXISTS (SELECT 1 FROM cleanup_objects c WHERE c.key=r.key)
 GROUP BY r.key
 HAVING bool_and(r.batch_id IN (SELECT id FROM eligible))
 UNION ALL
 SELECT a.key, ARRAY[]::uuid[] FROM abandoned a WHERE NOT EXISTS(SELECT 1 FROM cleanup_objects c WHERE c.key=a.key)
 UNION ALL
 SELECT replace(a.key,'sources/','uploads/'), ARRAY[]::uuid[] FROM abandoned a
 WHERE NOT EXISTS(SELECT 1 FROM cleanup_objects c WHERE c.key=replace(a.key,'sources/','uploads/'))
 ) SELECT * FROM candidates ORDER BY key LIMIT $3`

// Plan lists keys not yet scheduled. Existing cleanup records remain resumable.
func Plan(ctx context.Context, db *pgxpool.Pool, before time.Time) ([]Candidate, error) {
	rows, err := db.Query(ctx, candidateQuery, before, false, nil)
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
