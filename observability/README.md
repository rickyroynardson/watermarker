# Logs

Go Zap / Python logging → OTLP over HTTP → OpenTelemetry Collector → Loki → Grafana.
Loki's [native OTLP ingestion](https://grafana.com/docs/loki/latest/send-data/otel/)
keeps messages, severity, and structured attributes. No application-specific Loki client.

## Run locally

1. Start Docker, then run `make observability-up` from the repository root.
   This stack is independent of PostgreSQL/LocalStack and needs no AWS token.
2. Add these lines to `apps/api/.env` (or export them for each process):

   ```dotenv
   OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
   OTEL_RESOURCE_ATTRIBUTES=deployment.environment.name=development
   ```

3. Restart the API, consumer, and worker using their existing run commands.
   The Go binaries load `.env` before creating their loggers; Python uses
   `uv run --env-file ../api/.env main.py` from `apps/worker`.
4. Open [Watermarker logs](http://localhost:3000/d/watermarker-logs).
   The provisioned dashboard refreshes every five seconds. Anonymous local access
   is read-only; Grafana's initial admin credentials are `admin` / `admin`.
5. `curl http://localhost:8080/ping` generates an API access log. Startup logs also
   appear for all three processes. Allow a few seconds for batching and refresh.

Run `python3 observability/smoke.py` to independently send a test log through the
collector and verify that Loki returns it. It requires the observability stack,
but no application, database, AWS credentials, or third-party Python packages.

Stop with `make observability-down`; Docker volumes retain logs and Grafana state.
Loki retains logs for seven days (compaction removes expired data asynchronously).

## Search

In Grafana Explore select Loki:

```logql
{service_name="watermarker-api"}
{service_name="watermarker-consumer"}
{service_name="watermarker-worker"} | image_id="<image-uuid>"
{service_name=~"watermarker-.+"} | severity_text=~"(?i)error|fatal.*"
```

Only service and environment are index labels. Image IDs, batch IDs, HTTP route,
status, and other fields stay structured metadata. Loki normalizes dots in field
names to underscores. Request logs include route templates, status, and duration;
headers, bodies, raw URLs, and query strings are not collected by the middleware.
Recovery logs omit panic values because they can contain request data.

## Configuration and limits

- No OTLP endpoint means console-only logging. Console output remains enabled
  when exporting; Go production console logs retain their existing sampling.
  OTLP logs follow the console level but are not sampled.
- Default service names are `watermarker-api`, `watermarker-consumer`, and
  `watermarker-worker`. `OTEL_SERVICE_NAME` overrides a process's name. Don't set
  one shared name in the shared `.env`; the dashboard selects `watermarker-*`.
- `OTEL_EXPORTER_OTLP_ENDPOINT` is the base URL. Alternatively,
  `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT` is the complete URL including `/v1/logs`.
  Export uses HTTP/protobuf; a gRPC endpoint will not work.
- Standard OTLP headers/certificate environment variables are handled by the SDKs.
  Runtime export failures go to stderr; normal application logging continues.
- Logs export in bounded background batches. Normal shutdown flushes them; Go
  panic/fatal logs flush before termination. Crashes, forced kills, long outages,
  and queue overflow can lose logs. This is not an audit log system.
- This local stack binds ports to loopback, uses anonymous Grafana viewing and
  unauthenticated Loki/OTLP, and stores data on local Docker volumes. For remote
  deployment, configure TLS/authentication, storage capacity, and backups or point
  the collector at a managed backend. Do not expose this Compose stack publicly.
- This implements logs only. Metrics, traces, distributed trace propagation, and
  alerts can be added when there is a concrete operational question to answer.

## Checks

```sh
(cd apps/api && go test -short ./...)
(cd apps/worker && uv run --frozen --offline python -m unittest discover -s tests)
docker compose -p watermarker-observability -f observability/compose.yml config --quiet
python3 observability/smoke.py
```

The exporter tests use loopback OTLP receivers to verify service identity,
structured fields, severity/exception data, level filtering, and shutdown flush.
