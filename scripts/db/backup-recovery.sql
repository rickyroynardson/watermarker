-- Synthetic application data only: no S3 objects, AWS calls or real credentials.
INSERT INTO users(id,name,issuer,subject,quota_plan,addon_bytes) VALUES
 ('00000000-0000-0000-0000-000000000001','Recovery demo A','https://issuer.example.invalid','demo-a','pro',104857600),
 ('00000000-0000-0000-0000-000000000002','Recovery demo B',NULL,NULL,'free',0);
INSERT INTO api_keys(id,name,key_hash,user_id) VALUES
 ('00000000-0000-0000-0000-000000000010','Demo key',encode(sha256('synthetic-api-key'::bytea),'hex'),'00000000-0000-0000-0000-000000000001');
INSERT INTO auth_sessions(token_hash,user_id,expires_at) VALUES
 (encode(sha256('synthetic-session'::bytea),'hex'),'00000000-0000-0000-0000-000000000001',now()+interval '1 day');
INSERT INTO auth_logins(state_hash,verifier,nonce,expires_at) VALUES
 (encode(sha256('synthetic-state'::bytea),'hex'),'synthetic-verifier','synthetic-nonce',now()+interval '5 minutes');
INSERT INTO quota_addons(user_id,request_id) VALUES
 ('00000000-0000-0000-0000-000000000001','00000000-0000-0000-0000-000000000020');

INSERT INTO batches(id,user_id,watermark_key,idempotency_key,completed_at,cancelled_at) VALUES
 ('00000000-0000-0000-0000-000000000101','00000000-0000-0000-0000-000000000001','sources/demo-a/watermark','recovery-pending',NULL,NULL),
 ('00000000-0000-0000-0000-000000000102','00000000-0000-0000-0000-000000000001','sources/demo-a/watermark','recovery-done',now(),NULL),
 ('00000000-0000-0000-0000-000000000103','00000000-0000-0000-0000-000000000002','sources/demo-b/watermark','recovery-cancelled',now(),now());
INSERT INTO images(id,batch_id,status,source_key,output_key,error,attempt,retryable) VALUES
 ('00000000-0000-0000-0000-000000000201','00000000-0000-0000-0000-000000000101','pending','sources/demo-a/pending',NULL,NULL,0,false),
 ('00000000-0000-0000-0000-000000000202','00000000-0000-0000-0000-000000000101','failed','sources/demo-a/retryable',NULL,'Synthetic storage failure',2,true),
 ('00000000-0000-0000-0000-000000000203','00000000-0000-0000-0000-000000000102','done','sources/demo-a/done','processed/demo-a/done.png',NULL,1,false),
 ('00000000-0000-0000-0000-000000000204','00000000-0000-0000-0000-000000000103','cancelled','sources/demo-b/cancelled',NULL,NULL,0,false);
INSERT INTO outbox_messages(image_id,payload) VALUES
 ('00000000-0000-0000-0000-000000000201',jsonb_build_object('version',1,'job_type','demo','image_id','00000000-0000-0000-0000-000000000201','batch_id','00000000-0000-0000-0000-000000000101'));
INSERT INTO upload_reservations(key,user_id,bytes)
SELECT watermark_key,user_id,1024 FROM batches
UNION
SELECT i.source_key,b.user_id,1024 FROM images i JOIN batches b ON b.id=i.batch_id;
INSERT INTO output_storage(key,batch_id,user_id,bytes)
SELECT i.output_key,b.id,b.user_id,2048 FROM images i JOIN batches b ON b.id=i.batch_id WHERE i.status='done';
INSERT INTO cleanup_objects(key,attempts,last_error,deleted_at) VALUES
 ('uploads/demo-a/orphan',0,NULL,NULL),
 ('sources/demo-a/deleted',1,NULL,now()),
 ('processed/demo-a/delete-retry.png',2,'Synthetic delete failure',NULL);
