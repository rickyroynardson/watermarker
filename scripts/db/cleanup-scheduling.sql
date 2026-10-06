-- Disposable database only; actual selection SQL is injected from cleanup/apply.go.
SET statement_timeout = '60s';
\echo 'Seed: 200,000 batches, 300,000 cleanup records, a small eligible working set.'
INSERT INTO users(id,name) VALUES(md5('cleanup-lab-user')::uuid,'Cleanup lab');
INSERT INTO batches(id,user_id,watermark_key,completed_at,expired_at)
SELECT md5('cleanup-batch-' || n)::uuid,md5('cleanup-lab-user')::uuid,
       'sources/cleanup-lab/batch-' || n,
       CASE WHEN n <= 190000 OR n > 199980 THEN now()-interval '40 days'
            WHEN n > 199000 THEN now()-interval '1 day' END,
       CASE WHEN n <= 190000 THEN now()-interval '10 days' END
FROM generate_series(1,200000) n;
INSERT INTO images(id,batch_id,source_key,status,error,retryable)
SELECT md5('cleanup-image-' || n)::uuid,md5('cleanup-batch-' || n)::uuid,
       'sources/cleanup-lab/image-' || n,
       CASE WHEN n=199998 THEN 'pending' WHEN n=199999 THEN 'failed' ELSE 'cancelled' END::image_status,
       CASE WHEN n=199999 THEN 'retryable lab failure' END,n=199999
FROM generate_series(199981,200000) n;
INSERT INTO outbox_messages(image_id,payload)
VALUES(md5('cleanup-image-200000')::uuid,'{}');
INSERT INTO cleanup_objects(key,deleted_at,delete_after,next_attempt_at)
SELECT 'sources/cleanup-lab/object-' || lpad(n::text,6,'0'),
       CASE WHEN n<=294000 THEN now()-interval '2 days' END,
       CASE WHEN n>299000 THEN now()+interval '1 day' ELSE now()-interval '1 day' END,
       now()-interval '1 hour'
FROM generate_series(1,300000) n;
SELECT now()-interval '30 days' AS cutoff \gset
ANALYZE;
SET plan_cache_mode = force_generic_plan;
-- APP_EXPIRY_QUERY
-- APP_DELETION_QUERY

\echo '1. Expiry selection WITHOUT proposed partial index'
BEGIN;
DROP INDEX IF EXISTS idx_batches_cleanup_expiry;
EXPLAIN (ANALYZE, BUFFERS) EXECUTE expiry_candidates(:'cutoff',10);
CREATE TEMP TABLE expiry_baseline AS EXECUTE expiry_candidates(:'cutoff',10);
SELECT array_agg(id ORDER BY id)::text AS ids FROM expiry_baseline \gset baseline_
ROLLBACK;
\echo '2. Same expiry selection WITH proposed partial index'
CREATE INDEX IF NOT EXISTS idx_batches_cleanup_expiry
ON batches(completed_at,id) WHERE expired_at IS NULL AND completed_at IS NOT NULL;
ANALYZE batches;
BEGIN;
EXPLAIN (ANALYZE, BUFFERS) EXECUTE expiry_candidates(:'cutoff',10);
CREATE TEMP TABLE expiry_indexed AS EXECUTE expiry_candidates(:'cutoff',10);
SELECT count(*)=10 AND array_agg(id ORDER BY id)::text=:'baseline_ids' AS correct
FROM expiry_indexed \gset
\if :correct
  \echo 'PASS: expiry index preserves the same 10 ordered candidates.'
\else
  \echo 'FAIL: expiry candidates changed.'
  \quit 1
\endif
-- Exercise guards with a larger limit: 20 old unexpired batches, three blocked.
CREATE TEMP TABLE eligible_all AS EXECUTE expiry_candidates(:'cutoff',1000);
SELECT count(*)=17 AND NOT bool_or(id=ANY(ARRAY[
  md5('cleanup-batch-199998')::uuid,md5('cleanup-batch-199999')::uuid,md5('cleanup-batch-200000')::uuid
])) AS guards_correct FROM eligible_all \gset
\if :guards_correct
  \echo 'PASS: pending images, retryable failures and unsent outbox intents remain protected.'
\else
  \echo 'FAIL: unfinished batches became eligible.'
  \quit 1
\endif
ROLLBACK;

\echo '3. Deletion claim WITH previous single-column due index'
BEGIN;
DROP INDEX IF EXISTS cleanup_objects_due;
CREATE INDEX cleanup_objects_due ON cleanup_objects(next_attempt_at) WHERE deleted_at IS NULL;
EXPLAIN (ANALYZE, BUFFERS) EXECUTE deletion_candidate;
CREATE TEMP TABLE deletion_baseline AS EXECUTE deletion_candidate;
SELECT key AS key FROM deletion_baseline \gset baseline_
ROLLBACK;
\echo '4. Deletion claim WITH order-matching partial index'
DROP INDEX IF EXISTS cleanup_objects_due;
CREATE INDEX cleanup_objects_due ON cleanup_objects(next_attempt_at,key) WHERE deleted_at IS NULL;
ANALYZE cleanup_objects;
BEGIN;
EXPLAIN (ANALYZE, BUFFERS) EXECUTE deletion_candidate;
CREATE TEMP TABLE deletion_indexed AS EXECUTE deletion_candidate;
SELECT count(*)=1 AND min(key)=:'baseline_key' AND min(key)='sources/cleanup-lab/object-294001' AS claim_correct
FROM deletion_indexed \gset
\if :claim_correct
  \echo 'PASS: due/grace checks and next-attempt/key ordering return the same object.'
\else
  \echo 'FAIL: deletion claim changed.'
  \quit 1
\endif
ROLLBACK;

\echo '5. Verify the index migration Down restores the previous layout'
-- INDEX_MIGRATION_DOWN
SELECT to_regclass('idx_batches_cleanup_expiry') IS NULL
 AND pg_get_indexdef('cleanup_objects_due'::regclass) LIKE '%(next_attempt_at)%'
 AS rollback_correct \gset
\if :rollback_correct
  \echo 'PASS: rollback restores the single-column due index and removes the expiry index.'
\else
  \echo 'FAIL: index migration rollback did not restore the old layout.'
  \quit 1
\endif
-- INDEX_MIGRATION_UP
