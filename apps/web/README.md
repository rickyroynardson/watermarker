# Web

Minimal React + TypeScript UI for every current API endpoint: ping, presign/upload,
create batch, cursor-paginated batch history, and batch results. Vite, Tailwind CSS, Oxlint, and
Oxfmt; no router, component library, or state/query library.

## Run

Use Node 22.18+ (or Node 24). Start the API using [its instructions](../api/README.md), then:

```sh
cd apps/web
npm ci
npm run dev
```

Open http://localhost:5173 and enter an existing API key. The key stays in memory.
`/api` is proxied to `http://localhost:8080`; to change the backend, run
`API_TARGET=http://localhost:8081 npm run dev`. Do not put API keys in Vite env vars.
The API has no key-creation endpoint; provision keys through your existing database workflow
(`api_keys.key_hash` stores the hex SHA-256 of the bearer key).

Select one watermark and one or more sources, then upload and create a batch.
The API consumer and Python worker must also be running for processing.
Uploads are sequential and successful uploads are reused on retry. The same
idempotency key is retained until you change files or credentials or reload.
After an uncertain submission, retry before changing files or reloading.
New batches open their results automatically. Use **View results** in history to
reopen a batch. Results show per-image pending/done/failed status, worker errors,
previews, and individual downloads. Pending batches poll every 3 seconds; terminal
batches refresh signed links every 10 minutes. Refresh manually after a network
error or if a link has expired. Switching batches or credentials cancels polling.
Pending means queued or processing; the worker does not emit a separate running event.

## Browser uploads

New LocalStack stacks configure bucket CORS automatically. For an existing stack,
apply the configuration without deleting data:

```sh
docker compose exec localstack awslocal s3api put-bucket-cors \
  --bucket watermarker \
  --cors-configuration file:///etc/localstack/init/ready.d/cors.json
```

Run that from the repo root. The configuration allows POST from localhost and
127.0.0.1 on port 5173. `AWS_ENDPOINT_URL` must be reachable from the browser
(e.g. `http://localhost:4566`, not a Docker-only hostname).
For AWS, set the bucket's CORS policy to allow POST from your actual web origin.

## Checks and build

```sh
npm run check  # Oxlint, Oxfmt check, Node tests, TypeScript, Vite build
npm run fmt   # format with Oxfmt
```

The small Node test checks upload form fields/order, auth isolation, file limits,
API errors, and safe retry after losing a batch-creation response.

`npm run build` writes `dist/`. For deployment, serve it behind a reverse proxy
that forwards `/api/*` to the Go API with `/api` stripped. `npm run preview` only
previews static assets; it does not configure the production API proxy.
