# API

Run from `apps/api` with `go run ./cmd/api`. The API loads `.env` from its
working directory; add the storage settings from the root `.env.example` to
`apps/api/.env` alongside `DATABASE_URL`. Start infrastructure with `make up`
and apply migrations with `make migrate-up` from the repository root.

For AWS, set `S3_BUCKET` and `AWS_REGION`, remove the LocalStack endpoint and
test credentials, and use the SDK's standard credential chain. The signing
principal needs `s3:PutObject` permission on the bucket's `uploads/*` prefix.

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
in `POST /batches`. Presigning creates no database rows. File-byte validation
and checking uploaded objects during batch creation are still pending.

Run checks with `go test ./...`; presign tests use dummy credentials and require
no running AWS, LocalStack, or database services.
