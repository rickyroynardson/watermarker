# Watermarker

Upload images, add a watermark, and download the results. Processing runs in
background workers while the web app shows batch progress.

This is a portfolio and learning project built with Go, Python and React. It
explores how an application handles background jobs, failures and recovery.

## What it does

- Process JPEG, PNG and WebP sources into PNG outputs with a watermark at the bottom right.
- Show batch history, live progress, previews and downloads.
- Sign in through an OIDC provider, or use an API key.
- Cancel batches and manually retry images after fixing processing failures.
- Enforce request rate limits and storage allowances, with demo plan upgrades.
- Schedule expired files for deletion and retry failed deletions.
- Collect logs, metrics and distributed traces.

Plan upgrades are simulated: there are no payments or subscriptions. Watermark
position, size and opacity are currently fixed.

## Run locally

Start OrbStack or Docker with Compose 2.24.4+. The development stack uses
LocalStack Pro and requires a `LOCALSTACK_AUTH_TOKEN`.

On a fresh checkout:

```sh
cp .env.example .env
# Set LOCALSTACK_AUTH_TOKEN in .env.
make app-up
```

If `.env` already exists, update it instead of replacing it. Migrations run
before the application starts. No host Go, Python or Node installation is needed
for this container setup.

Open [the web app](http://localhost:5173). To access your account, either configure
[OIDC sign-in](apps/api/README.md#browser-sign-in-and-ownership) or create the
[local demo account](docs/containers.md#local-account-without-google).

Try the bundled [source image](scripts/load/fixtures/source.jpg) and
[watermark](scripts/load/fixtures/watermark.png): select them in the UI, upload,
create a batch, then preview and download the result.

```sh
make app-logs   # Follow application logs.
make app-down   # Stop the stack and keep its volumes.
```

See the [container guide](docs/containers.md) for worker scaling, optional
monitoring, cleanup and troubleshooting. For running services on the host, use
the [API](apps/api/README.md), [worker](apps/worker/README.md) and
[web](apps/web/README.md) guides.

## How processing works

The browser uploads directly to S3 using signed requests from the API. The API
saves a batch and its jobs together in PostgreSQL. A Go consumer sends those jobs
to SQS; Python workers process the images and publish results. The consumer saves
results, and Redis notifications wake the API's live progress streams.

PostgreSQL holds the current state. S3 holds image files. SQS carries jobs and
results. Redis carries notifications and request-limit counters.

Read the [architecture walkthrough](docs/architecture.md) for the request flow,
service responsibilities and failure handling.

## Checks

From the repository root:

```sh
(cd apps/api && go test -race -count=1 -p 1 ./...)
(cd apps/worker && uv sync --locked && uv run --frozen --offline python -m unittest discover -s tests -v)
(cd apps/web && npm ci && npm run check)
```

These require a compatible Go toolchain and C compiler for race tests, Python
3.14 with uv, and Node 22.18+. Go integration tests use disposable Docker
containers; they do not use your development database or need a LocalStack Pro
token. See [CI](docs/ci.md) for the automated checks.

For a full container check after building the images, stop the development stack
and run `python3 scripts/test_container_stack.py`. It requires Python 3 and Node,
uses token-free community LocalStack, processes real fixtures, and removes its
disposable resources. Port 4566 must be free.

## Learning guides and deployment status

The [docs index](docs/README.md) covers observability, Terraform, query tuning,
connection pools, locking and backup/PITR exercises.

The application runs locally through Compose, with a separate LocalStack ECS
exercise. Terraform includes AWS infrastructure configuration and mocked tests;
it has not been verified by deploying this application to real AWS. The AWS root
does not provision PostgreSQL, Redis or a complete application deployment.

Backup exercises use temporary databases and synthetic data. They do not enable
scheduled production backups. GHCR image publishing is currently paused.
