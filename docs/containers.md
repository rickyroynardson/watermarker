# Run the full application in containers

The base `docker-compose.yml` still runs infrastructure for host-based development.
The `docker-compose.app.yml` overlay adds API, consumer, worker, web and a one-shot
migration service. No application code changes are needed.

From the repository root:

```sh
make app-up
make app-logs
```

Keep the existing root `.env` with your `LOCALSTACK_AUTH_TOKEN`. Stop host processes
using ports 8080 and 5173 before starting the application containers. The web app
is at **http://localhost:5173** and the API is at **http://localhost:8080**.

Migrations run before the API starts. API startup waits for PostgreSQL, Redis and
LocalStack; the consumer, worker and web wait for the API health check. The
`/ping` check confirms the HTTP process is running; it is not a continuous check
of all dependencies. `make app-down` stops the application and infrastructure
without deleting the database or LocalStack volumes.

The API alone reads the optional `apps/api/.env` at container startup, preserving
your OIDC provider configuration. For Google login keep
`APP_ORIGIN=http://localhost:5173` and the callback
`http://localhost:5173/api/auth/callback`. Container service addresses override
host database/Redis/AWS settings. No `.env` files are copied into images.
Host `AWS_PROFILE`/`AWS_DEFAULT_PROFILE` and temporary session tokens are cleared
because the container uses explicit LocalStack credentials. The binaries read
process environment variables; a `.env` file is optional in both development
and production. `APP_ENV` selects logging style, not whether environment variables
are available.
The overlay uses local-only AWS credentials and a default worker API token;
set `WORKER_API_TOKEN` in the root `.env` to replace the development token.

LocalStack has the Docker network alias `localhost.localstack.cloud`. Containers
resolve that hostname to LocalStack; the browser resolves it to the host loopback
address. This keeps signed S3 upload/download URLs accessible from both places.
Port 4566 must remain published, and your host must resolve
`localhost.localstack.cloud` to `127.0.0.1`. See
[LocalStack endpoint networking](https://docs.localstack.cloud/aws/customization/networking/accessing-endpoint-url/).

Nginx serves the built React assets and proxies `/api/` to the Go API, stripping
the prefix. Proxy buffering is disabled so SSE snapshots arrive immediately.
All three images run as non-root users. The Go image contains API, consumer,
monitor, cleanup and Goose; Compose chooses the command for each service.
The worker installs dependencies from `uv.lock` during the build and runs Python
directly, without installing packages at startup. See
[uv's Docker guide](https://docs.astral.sh/uv/guides/integration/docker/).

## Local account without Google

After `make app-up`, you can create a demo account without configuring OIDC.
Run this from the repository root against the local Compose database only:

```sh
docker compose -f docker-compose.yml -f docker-compose.app.yml exec -T postgres \
  psql -U watermarker -d watermarker -v ON_ERROR_STOP=1 <<'SQL'
BEGIN;
INSERT INTO users(id,name)
VALUES ('11111111-1111-4111-8111-111111111111','Local demo')
ON CONFLICT (id) DO NOTHING;
INSERT INTO api_keys(id,user_id,name,key_hash)
VALUES ('22222222-2222-4222-8222-222222222222',
        '11111111-1111-4111-8111-111111111111','Local demo',
        '2a0fb1074a14249741dbe138b4e0552ab770bf3ffb3eb76800b45dd8b2c7cbc7')
ON CONFLICT (id) DO NOTHING;
COMMIT;
SQL
```

Open http://localhost:5173 and enter `watermarker-local-demo-key` as the API key.
This is a public development credential; use it only for this local demo account.
The database stores its SHA-256 hash. Repeating the command keeps the same account
and does not reactivate a revoked key.

Use `scripts/load/fixtures/source.jpg` and `scripts/load/fixtures/watermark.png`
for a first batch. Set `QUOTA_DEMO_ENABLED=true` in the root `.env` and rerun
`make app-up` if you want to try the simulated plan upgrades. API-key access does
not enable sign-in/out or key management; those require an OIDC browser session.

## Scale and optional tools

```sh
docker compose -f docker-compose.yml -f docker-compose.app.yml up -d --scale worker=3
```

Workers have no published ports or fixed container names, so Compose can start
multiple instances. Set `WORKER_CONCURRENCY=2` in the root `.env` and recreate
workers to allow two simultaneous jobs per instance (default 1). Three instances
at concurrency 2 can hold up to six jobs. Start small and watch memory; image
size limits are per job rather than a total process memory cap.
Consumer and worker stop grace periods allow in-flight work
and SQS long polls to finish; exceeding the grace period still causes a forced
stop, with unacknowledged messages left for retry.

For observability, start `make observability-up`, then set the following in the
root `.env` and recreate the application containers with `make app-up`:

```dotenv
CONTAINER_OTEL_ENDPOINT=http://host.docker.internal:4318
```

Start the separate backlog monitor explicitly:

```sh
docker compose -f docker-compose.yml -f docker-compose.app.yml --profile monitor up -d monitor
```

Cleanup remains manual and defaults to dry-run:

```sh
docker compose -f docker-compose.yml -f docker-compose.app.yml run --rm cleanup
docker compose -f docker-compose.yml -f docker-compose.app.yml run --rm cleanup --apply --retention-days 30
```

## Verify the images

After building with `make app-up` or `docker compose -f docker-compose.yml -f
docker-compose.app.yml build api worker web`, run:

```sh
python3 scripts/test_container_stack.py
```

The smoke check needs Docker Compose 2.24.4+, Node 22.18+ and free host port 4566
(stop the development stack first). It uses a separate project with disposable
volumes, a token-free LocalStack community image and OIDC disabled. Its test
bucket enables versioning to avoid the community image's known conditional-copy
bug, matching the existing Go integration fixture. It verifies
migrations, the web proxy, signed uploads, real worker processing, SSE and signed
downloads, then removes its own resources. Existing development volumes are not
used. This local Compose overlay is not an AWS production deployment; production
needs its own endpoints, credentials, HTTPS and secret injection.

For an ECS orchestration learning alternative, see the
[LocalStack ECS guide](infrastructure-ecs-local.md). Use one app deployment at a time.
