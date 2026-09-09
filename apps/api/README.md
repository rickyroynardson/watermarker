# API

Run from `apps/api` with `go run ./cmd/api`. The API loads `.env` from its
working directory; add the AWS settings from the root `.env.example` to
`apps/api/.env` alongside `DATABASE_URL`. Start infrastructure with `make up`
and apply migrations with `make migrate-up` from the repository root.

For AWS, set `S3_BUCKET` and `AWS_REGION`, remove the LocalStack endpoint and
test credentials, and use the SDK's standard credential chain. The signing
principal needs `s3:GetObject` and `s3:PutObject` on `uploads/*` and `sources/*`, and
`s3:DeleteObject` on `uploads/*` (batch creation copies staging objects to a persistent
prefix, then deletes the originals). Also grant `s3:ListBucket` on the bucket so
missing objects produce a 404 rather than an ambiguous 403 during
[HEAD checks](https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadObject.html).
The API needs no SQS permissions. Run `go run ./cmd/consumer` from `apps/api`
in a second terminal for outbox delivery and result consumption. It loads the same
`.env` and needs `DATABASE_URL`, AWS credentials/region, `SQS_JOBS_QUEUE_URL`, and
`SQS_RESULTS_QUEUE_URL`. Grant it `sqs:SendMessage` on jobs and `sqs:ReceiveMessage`
and `sqs:DeleteMessage` on results. Both clients honor `AWS_ENDPOINT_URL`.

Get the LocalStack queue URLs with:

```sh
docker compose exec localstack awslocal sqs get-queue-url --queue-name watermarker-jobs --query QueueUrl --output text
docker compose exec localstack awslocal sqs get-queue-url --queue-name watermarker-results --query QueueUrl --output text
```

The initialization script configures a separate DLQ for each queue after three
failed deliveries. Existing results queues need their redrive policy updated too;
use the same policy in AWS. The consumer retains invalid messages and failed
handler attempts for retry,
so monitor and redrive the results DLQ after fixing the cause.

## Presign uploads

`POST /uploads/presign`, authenticated with `Authorization: Bearer <api-key>`:

```json
{
  "content_type": "image/png"
}
```

Accepts one file per request: JPEG, PNG, or WebP. Each policy
allows 1 byte to 10 MiB and expires after 900 seconds (or sooner if the signing
credentials expire). Keys are generated as `uploads/<api-key-id>/<uuid>`.

The response is `{ "data": <upload> }`. The upload contains `key`, `url`, `fields`, and
`expires_in` (seconds). POST multipart form data to `url`, copying **all** returned
`fields` unchanged and appending the file last:

```js
async function uploadFile(upload, file) {
  const form = new FormData();
  for (const [key, value] of Object.entries(upload.fields)) form.append(key, value);
  form.append("file", file);
  const response = await fetch(upload.url, { method: "POST", body: form });
  if (!response.ok) throw new Error(`Upload failed: ${response.status}`);
  return upload.key;
}
```

Browser clients need bucket CORS configured for their origin and POST. The
endpoint in `AWS_ENDPOINT_URL` must be reachable by the uploading client.
Request a presign and upload each file independently, including the watermark.
After uploads succeed, use the returned keys as `watermark_key` and `source_keys`
in `POST /batches`. The API copies each object from
`uploads/<api-key-id>/<uuid>` to `sources/<api-key-id>/<uuid>`, and stores the
persistent keys. A [conditional copy](https://docs.aws.amazon.com/AmazonS3/latest/API/API_CopyObject.html)
preserves the first promoted version, even if the upload URL is reused or batches
are submitted concurrently. Request a new upload key to change an image.

Send an `Idempotency-Key` header to safely retry batch creation. Matching retries
return the existing batch without contacting S3; changed requests return 409.
Source order is ignored. Staging objects are deleted only after a successful
database commit; cleanup failures are logged without failing an accepted batch.
Batch creation writes one `outbox_messages` row per image in the same transaction as
its batch and image rows. It returns 201 after commit without contacting SQS.
The background dispatcher sends each stored JSON message to the jobs queue:

```json
{
  "version": 1,
  "job_type": "composite",
  "batch_id": "<batch-uuid>",
  "image_id": "<image-uuid>",
  "source_key": "sources/<api-key-id>/<upload-uuid>",
  "watermark_key": "sources/<api-key-id>/<watermark-uuid>"
}
```

Apply all migrations before starting either binary. The outbox migration creates
the table and its index; new batch creation populates it.

The dispatcher claims one due row with `FOR UPDATE SKIP LOCKED`, sends it with a
10-second timeout, and deletes it only after SQS accepts it. Failed sends remain
in Postgres and become eligible again after five seconds. Idle dispatchers poll
once per second. Multiple consumer processes can dispatch different rows safely;
a restart resumes from the persisted outbox without a client retry.

A crash after SQS accepts a message but before the outbox deletion commits can
still resend that message. This is an at-least-once
[transactional outbox](https://docs.aws.amazon.com/prescriptive-guidance/latest/cloud-design-patterns/transactional-outbox.html),
so processing must tolerate repeated image IDs.

Partial promotions survive failures so a retry can reuse them. Expiration of
unused staging uploads and cleanup of unreferenced persistent objects remain
unimplemented. Presigning creates no database rows. The Python worker validates
image bytes before processing them.

## Result messages

The Go consumer long-polls the results queue and accepts version 1 `composite`
messages. A successful result is:

```json
{
  "version": 1,
  "job_type": "composite",
  "batch_id": "<batch-uuid>",
  "image_id": "<image-uuid>",
  "status": "done",
  "output_key": "processed/<batch-uuid>/<image-uuid>.png"
}
```

For failures, use `"status": "failed"` and `"error": "reason"`, omitting
`output_key`. Errors must be nonblank and at most 4096 bytes. Successful keys must
match the batch/image IDs and end in `.png`, `.jpg`, `.jpeg`, or `.webp`.

Results update only images with `status = 'pending'`; the first terminal result
wins. Duplicate or later conflicting terminal results leave status, output, and
`updated_at` unchanged. Messages are deleted from SQS only after the database
update succeeds (or an existing terminal image is verified). Failed updates,
unknown IDs, and malformed messages are left for retry/DLQ. Result handling has a
10-second timeout within a 60-second message visibility timeout. Shutdown cancels
polling and dispatch before closing the database pool.

The [Python worker](../worker/README.md) writes to the deterministic output key
above, publishes its result before acknowledging the job, and reuses existing
output on redelivery. Conditional S3 writes prevent duplicate output overwrites;
the Go consumer's conditional updates protect database state. Concurrent deliveries
can still repeat image computation.

After `uv sync --locked` in `apps/worker`, run it with
`uv run --env-file ../api/.env main.py` from that directory alongside the Go API
and consumer. Set `WATERMARKER_WORKER_TEST=1` when running the integration suite
to include real Python processing against the disposable test services.

## Tests

Run from `apps/api` with Docker running:

```sh
go test -race -count=1 ./...
```

[Testcontainers](https://golang.testcontainers.org/) starts disposable PostgreSQL
and LocalStack containers on random ports and removes them after the tests.
No `make up`, database URL, AWS account, or manually applied migrations are needed.
The first run downloads the container images. Tests pin LocalStack to the
4.14.0 community image, which does not require the development stack's auth token.
When `DOCKER_HOST` is unset, the integration suite uses the endpoint from the
active Docker CLI context, including OrbStack and other non-default sockets.

`TestBatchAPIIntegration` in `internal/httpapi/batch_integration_test.go` sends
HTTP requests through the same router used by production: authentication, handlers,
services, repositories, and storage all run together. It applies the real migrations,
uploads files to LocalStack, then verifies HTTP responses, PostgreSQL rows, S3
contents, and SQS job payloads. Pipeline scenarios cover atomic outbox writes,
failed sends, restart recovery, concurrent dispatch, crashes after send, and
idempotent results. Unit checks verify result validation and acknowledgement order.

Focused unit tests cover validation and failures that are difficult to trigger
reliably through containers, such as S3 outages and failed cleanup. Separate
handler/repository/storage integration suites do not duplicate the same flows.

LocalStack copies bytes before checking destination preconditions (observed in
4.14.0 and the August 2026 image), corrupting unversioned objects even when it
returns 412. The test bucket enables versioning to avoid this emulator bug.
Unversioned overwrite protection still needs verification against AWS; production
bucket configuration is unchanged.

For a fast run without Docker:

```sh
go test -short ./...
```
