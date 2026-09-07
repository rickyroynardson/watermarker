# API

Run from `apps/api` with `go run ./cmd/api`. The API loads `.env` from its
working directory; add the storage settings from the root `.env.example` to
`apps/api/.env` alongside `DATABASE_URL`. Start infrastructure with `make up`
and apply migrations with `make migrate-up` from the repository root.

For AWS, set `S3_BUCKET` and `AWS_REGION`, remove the LocalStack endpoint and
test credentials, and use the SDK's standard credential chain. The signing
principal needs `s3:GetObject` and `s3:PutObject` on `uploads/*` and `sources/*`, and
`s3:DeleteObject` on `uploads/*` (batch creation copies staging objects to a persistent
prefix, then deletes the originals). Also grant `s3:ListBucket` on the bucket so
missing objects produce a 404 rather than an ambiguous 403 during
[HEAD checks](https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadObject.html).

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
Partial promotions survive failures so a retry can reuse them. Expiration of
unused staging uploads and cleanup of unreferenced persistent objects remain
unimplemented. Presigning creates no database rows. File-byte validation is
planned for the worker, which is currently a placeholder.

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
uploads files to LocalStack, then verifies HTTP responses, PostgreSQL rows, and S3
contents. Scenarios cover creation, retries, conflicts, ownership, invalid input,
concurrent requests, and rollback after a database constraint failure.

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
