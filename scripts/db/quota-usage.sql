-- Disposable database only. The runner injects the actual application's usage SQL.
SET statement_timeout = '60s';
\echo 'Seed: 21 users, 42,000 batches, 84,000 images; half the busy user history deleted.'
INSERT INTO users(id, name)
SELECT md5('quota-user-' || n)::uuid, 'Quota lab ' || n FROM generate_series(0, 20) n;
INSERT INTO batches(id, user_id, watermark_key, completed_at)
SELECT md5('quota-batch-' || n)::uuid, md5('quota-user-' || ((n-1)/2000))::uuid,
       'sources/' || md5('quota-user-' || ((n-1)/2000))::uuid || '/watermark-' || n, now()
FROM generate_series(1,42000) n;
INSERT INTO images(id,batch_id,source_key,status,output_key)
SELECT md5('quota-image-' || n || '-' || slot)::uuid, md5('quota-batch-' || n)::uuid,
       'sources/' || md5('quota-user-' || ((n-1)/2000))::uuid || '/image-' || n || '-' || slot,
       'done'::image_status,
       'processed/' || md5('quota-batch-' || n)::uuid || '/' || md5('quota-image-' || n || '-' || slot)::uuid || '.png'
FROM generate_series(1,42000) n CROSS JOIN generate_series(1,2) slot;
INSERT INTO upload_reservations(key,user_id,bytes)
SELECT watermark_key,user_id,1024 FROM batches
UNION ALL
SELECT i.source_key,b.user_id,1024 FROM images i JOIN batches b ON b.id=i.batch_id;
INSERT INTO output_storage(key,batch_id,user_id,bytes)
SELECT i.output_key,b.id,b.user_id,2048 FROM images i JOIN batches b ON b.id=i.batch_id;
-- Confirmed deletes for the first 1,000 batches of every user. References remain as history.
INSERT INTO cleanup_objects(key,deleted_at)
SELECT watermark_key,now() FROM batches WHERE substring(watermark_key from '/watermark-([0-9]+)$')::int % 2000 BETWEEN 1 AND 1000
UNION ALL
SELECT i.source_key,now() FROM images i JOIN batches b ON b.id=i.batch_id WHERE substring(b.watermark_key from '/watermark-([0-9]+)$')::int % 2000 BETWEEN 1 AND 1000
UNION ALL
SELECT i.output_key,now() FROM images i JOIN batches b ON b.id=i.batch_id WHERE substring(b.watermark_key from '/watermark-([0-9]+)$')::int % 2000 BETWEEN 1 AND 1000;
-- Four abandoned-upload states: active, scheduled, persistent-only deleted, both copies deleted.
INSERT INTO upload_reservations(key,user_id,bytes)
SELECT 'sources/' || md5('quota-user-0')::uuid || '/abandoned-' || n,md5('quota-user-0')::uuid,512
FROM generate_series(1,4) n;
INSERT INTO cleanup_objects(key,deleted_at)
SELECT key,CASE WHEN key LIKE '%-2' THEN NULL ELSE now() END FROM upload_reservations WHERE key LIKE '%/abandoned-%' AND key NOT LIKE '%-1';
INSERT INTO cleanup_objects(key,deleted_at)
SELECT replace(key,'sources/','uploads/'),now() FROM upload_reservations WHERE key LIKE '%/abandoned-4';
-- A staged-but-not-deleted source and output stay charged.
INSERT INTO cleanup_objects(key)
SELECT watermark_key FROM batches WHERE watermark_key LIKE '%/watermark-1500'
UNION ALL
SELECT output_key FROM images WHERE source_key LIKE '%/image-1500-1';
ANALYZE;
SELECT md5('quota-user-0')::uuid AS owner \gset
PREPARE baseline_quota(uuid) AS SELECT used FROM (SELECT (COALESCE(sum(r.bytes),0)+(SELECT COALESCE(sum(o.bytes),0) FROM output_storage o
 WHERE o.user_id=$1 AND NOT EXISTS(SELECT 1 FROM cleanup_objects c WHERE c.key=o.key AND c.deleted_at IS NOT NULL)))::bigint FROM upload_reservations r
 WHERE r.user_id=$1 AND NOT EXISTS(SELECT 1 FROM cleanup_objects c WHERE c.key=r.key AND c.deleted_at IS NOT NULL
 AND (EXISTS(SELECT 1 FROM batches b WHERE b.watermark_key=r.key) OR EXISTS(SELECT 1 FROM images i WHERE i.source_key=r.key) OR EXISTS(SELECT 1 FROM cleanup_objects staging WHERE staging.key=replace(r.key,'sources/','uploads/') AND staging.deleted_at IS NOT NULL)))) AS usage(used);
-- APP_QUOTA_QUERY
SET plan_cache_mode = force_generic_plan;

\echo '1. Original quota query without direct reference indexes'
BEGIN;
DROP INDEX IF EXISTS idx_batches_watermark_key;
DROP INDEX IF EXISTS idx_images_source_key;
EXPLAIN (ANALYZE, BUFFERS) EXECUTE baseline_quota(:'owner');
CREATE TEMP TABLE baseline_usage ON COMMIT PRESERVE ROWS AS EXECUTE baseline_quota(:'owner');
-- Save outside the rollback, using psql variables.
SELECT * FROM baseline_usage \gset baseline_
\echo '1b. Revised application query WITHOUT reference indexes'
EXPLAIN (ANALYZE, BUFFERS) EXECUTE quota_usage(:'owner');
CREATE TEMP TABLE revised_without_indexes AS EXECUTE quota_usage(:'owner');
SELECT used = 7169536 AND used = :'baseline_used'::bigint AS correct FROM revised_without_indexes \gset
\if :correct
  \echo 'PASS: query rewrite preserves usage without adding indexes.'
\else
  \echo 'FAIL: query rewrite changed usage.'
  \quit 1
\endif
ROLLBACK;

\echo '2. Original query with experimental reference indexes (lab only)'
CREATE INDEX IF NOT EXISTS idx_batches_watermark_key ON batches(watermark_key);
CREATE INDEX IF NOT EXISTS idx_images_source_key ON images(source_key);
ANALYZE batches;
ANALYZE images;
EXPLAIN (ANALYZE, BUFFERS) EXECUTE baseline_quota(:'owner');
CREATE TEMP TABLE indexed_usage AS EXECUTE baseline_quota(:'owner');
SELECT used = 7169536 AND used = :'baseline_used'::bigint AS correct FROM indexed_usage \gset
\if :correct
  \echo 'PASS: 7,169,536 bytes before/after; active, scheduled and persistent-only-deleted uploads remain charged.'
\else
  \echo 'FAIL: usage changed or deleted-object rules are incorrect.'
  \quit 1
\endif
\echo '3. Revised quota query with direct cleanup joins'
EXPLAIN (ANALYZE, BUFFERS) EXECUTE quota_usage(:'owner');
CREATE TEMP TABLE revised_usage AS EXECUTE quota_usage(:'owner');
SELECT used = 7169536 AND used = :'baseline_used'::bigint AS revised_correct FROM revised_usage \gset
\if :revised_correct
  \echo 'PASS: revised application query preserves exact usage.'
\else
  \echo 'FAIL: revised query changed usage.'
  \quit 1
\endif
\echo '4. Account endpoint shape WITHOUT experimental indexes (matches app schema)'
DROP INDEX idx_batches_watermark_key;
DROP INDEX idx_images_source_key;
-- APP_ACCOUNT_QUERY
EXPLAIN (ANALYZE, BUFFERS) EXECUTE account_quota(:'owner');
