-- Run only through query_lab.py, which creates a disposable database.
\echo 'Seed: 200,000 batches for one busy user; 100 other users with 1,000 each.'
INSERT INTO users(id, name)
SELECT md5('user-' || n)::uuid, 'Query lab ' || n FROM generate_series(0, 100) n;
INSERT INTO batches(id, user_id, watermark_key, created_at, completed_at)
SELECT md5('batch-' || n)::uuid,
       md5('user-' || CASE WHEN n <= 200000 THEN 0 ELSE 1 + (n - 200001) / 1000 END)::uuid,
       'sources/query-lab/' || n,
       timestamptz '2026-01-01 00:00:00+00' + (n / 10) * interval '1 second',
       timestamptz '2026-01-01 00:00:00+00' + (n / 10) * interval '1 second' + interval '12 seconds'
FROM generate_series(1, 300000) n;
ANALYZE batches;
SELECT user_id AS owner, created_at AS before, id AS before_id FROM batches
WHERE user_id = md5('user-0')::uuid ORDER BY created_at DESC, id DESC
OFFSET 179999 LIMIT 1 \gset

-- Baseline: original application query, including its nullable cursor predicate.
PREPARE nullable_cursor(uuid, timestamptz, uuid, int) AS
SELECT id, watermark_key, created_at, completed_at, expired_at,
       EXTRACT(EPOCH FROM (completed_at - created_at))::double precision AS duration_seconds
FROM batches WHERE user_id = $1
AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3::uuid))
ORDER BY created_at DESC, id DESC LIMIT $4;

-- Candidate: a distinct query shape for cursor pages; same projection and order.
PREPARE cursor_page(uuid, timestamptz, uuid, int) AS
SELECT id, watermark_key, created_at, completed_at, expired_at,
       EXTRACT(EPOCH FROM (completed_at - created_at))::double precision AS duration_seconds
FROM batches WHERE user_id = $1 AND (created_at, id) < ($2, $3)
ORDER BY created_at DESC, id DESC LIMIT $4;
PREPARE first_page(uuid, int) AS
SELECT id, watermark_key, created_at, completed_at, expired_at,
       EXTRACT(EPOCH FROM (completed_at - created_at))::double precision AS duration_seconds
FROM batches WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2;

\echo '1. Original cursor query, forced CUSTOM plan'
SET plan_cache_mode = force_custom_plan;
EXPLAIN (ANALYZE, BUFFERS) EXECUTE nullable_cursor(:'owner', :'before', :'before_id', 21);
\echo '2. Original cursor query, forced GENERIC plan'
SET plan_cache_mode = force_generic_plan;
EXPLAIN (ANALYZE, BUFFERS) EXECUTE nullable_cursor(:'owner', :'before', :'before_id', 21);
\echo '3. Direct cursor query, forced GENERIC plan'
EXPLAIN (ANALYZE, BUFFERS) EXECUTE cursor_page(:'owner', :'before', :'before_id', 21);
-- Correctness is checked separately from timing; no flaky millisecond thresholds.
CREATE TEMP TABLE original_page AS EXECUTE nullable_cursor(:'owner', :'before', :'before_id', 21);
CREATE TEMP TABLE direct_page AS EXECUTE cursor_page(:'owner', :'before', :'before_id', 21);
DO $$ BEGIN
    IF (SELECT count(*) FROM direct_page) <> 21 OR EXISTS (
        (SELECT * FROM original_page EXCEPT ALL SELECT * FROM direct_page)
        UNION ALL
        (SELECT * FROM direct_page EXCEPT ALL SELECT * FROM original_page)
    ) THEN
        RAISE EXCEPTION 'Cursor query changed the returned rows';
    END IF;
END $$;
\echo 'PASS: original and direct cursor queries return the same 21 rows.'

\echo '4. First page, existing index'
EXPLAIN (ANALYZE, BUFFERS) EXECUTE first_page(:'owner', 21);
\echo '5. Offset pagination, existing index, same deep page'
EXPLAIN (ANALYZE, BUFFERS)
SELECT id, watermark_key, created_at, completed_at, expired_at,
       EXTRACT(EPOCH FROM (completed_at - created_at))::double precision AS duration_seconds
FROM batches WHERE user_id = :'owner'
ORDER BY created_at DESC, id DESC OFFSET 180000 LIMIT 21;
\echo '6. First page WITHOUT the history index (rolled back afterward)'
BEGIN;
DROP INDEX idx_batches_user_id_created_at;
EXPLAIN (ANALYZE, BUFFERS) EXECUTE first_page(:'owner', 21);
ROLLBACK;
