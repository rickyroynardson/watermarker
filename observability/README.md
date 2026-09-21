# Logs, metrics, and traces

Go Zap / Python logging → OTLP over HTTP → OpenTelemetry Collector → Loki → Grafana.
Loki's [native OTLP ingestion](https://grafana.com/docs/loki/latest/send-data/otel/)
keeps messages, severity, and structured attributes. No application-specific Loki client.

## Run locally

1. Start Docker, then run `make observability-up` from the repository root.
   This stack is independent of PostgreSQL/LocalStack and needs no AWS token.
2. Add these lines to `apps/api/.env` (or export them for each process):

   ```dotenv
   OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
   OTEL_METRIC_EXPORT_INTERVAL=15000
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
collector and verify that Loki returns it, then verify a metric in Prometheus and the same trace in Tempo and Jaeger. It requires the observability stack,
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
- Queue/outbox monitoring uses the separate monitor process described below. Alerts are not configured.

## Checks

```sh
(cd apps/api && go test -short ./...)
(cd apps/worker && uv run --frozen --offline python -m unittest discover -s tests)
docker compose -p watermarker-observability -f observability/docker-compose.yml config --quiet
python3 observability/smoke.py
```

The exporter tests use loopback OTLP receivers to verify service identity,
structured fields, severity/exception data, level filtering, and shutdown flush.

## Metrics

Services → OTLP/HTTP → Collector → Prometheus → Grafana.
Prometheus uses its [native OTLP receiver](https://prometheus.io/docs/guides/opentelemetry/).
Run `make observability-up`, restart all three services, and open
[Watermarker metrics](http://localhost:3000/d/watermarker-metrics).
Use the same OTLP endpoint as logs. Export defaults to 60 seconds; the example
above sets 15 seconds. Rate charts need at least two exports and some traffic.
Prometheus is available at http://localhost:9090 and retains data for seven days.

Histograms provide counts, rates, and p95 duration without separate counters:

- `watermarker_http_request_duration_seconds`: API requests by method, route template,
  and status (including 4xx/5xx). Unknown methods and unmatched routes use fixed labels.
- `watermarker_message_duration_seconds`: consumer `publish_job` SQS send calls and
  `process_result` handler calls, by `success`/`error`. Result handling excludes SQS
  acknowledgment; publishing excludes the outbox transaction. Poll failures remain logs.
- `watermarker_job_duration_seconds`: worker attempts including result publishing and
  acknowledgment, by `done`, `failed` (invalid image), or `error` (exception, including
  malformed jobs and acknowledgment failures). Duplicate deliveries count as attempts.

Each metric has `_count`, `_sum`, and `_bucket` series. Service identity and a unique
process instance distinguish replicas. Image/batch IDs and error text are never labels.
No endpoint means metrics are disabled. Normal shutdown flushes metrics; crashes can
lose measurements since the last export. This is operational telemetry, not accounting.

If every metrics panel shows no data, run `python3 observability/smoke.py`.
The Prometheus service must keep `--web.enable-otlp-receiver`; without it the
Collector receives HTTP 404 and drops metrics. After changing Compose commands,
run `make observability-up` to recreate the container (a restart alone is insufficient).
Grafana's Prometheus data source uses a 60-second interval to match the default
SDK export interval, giving rate queries enough samples. Idle services have no
latency observations until they handle work.

Batch completion is recorded by the result consumer after every image reaches
`done` or `failed`. `watermarker_batch_duration_seconds` measures wall-clock time
from batch database creation (after upload promotion) to the terminal update,
including outbox delay and queue waiting. Grafana shows p50/p95 by `done`/`failed`.
It excludes uploads and does not sum concurrent image durations.

Apply `make migrate-up` before restarting the updated API and consumer. Batch list
and detail responses include nullable `completed_at` and `duration_seconds`; the
results page shows the completed duration. Existing terminal batches are backfilled
from their last image update without replaying historical metrics. Concurrent and
repeated results preserve the first completion timestamp. The database timestamp is
durable; the histogram is best effort and can miss a completion if the consumer
crashes after committing but before exporting it.

Loki can return `Ingester is shutting down` when WAL disk usage exceeds its
90% safety threshold, even while `/ready` returns success. Check
`loki_ingester_wal_disk_usage_percent` at http://localhost:3100/metrics and free
space on the Docker host. Restarting alone does not fix disk pressure; keep the
threshold enabled. Logs already dropped after retry exhaustion cannot be recovered
from the Collector.

Latency percentiles require observations between exports inside the recent rate
window. Idle panels show no observations rather than NaN or a fabricated zero.
The worker/batch totals show cumulative observations from active process instances
and reset on restart; exact batch duration remains available in the application.

## Distributed tracing

Run `make observability-up` and restart all three processes with the existing
`OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318`. No database migration is needed.
In Grafana Explore select **Tempo**, search for service `watermarker-api`, and
open a `POST /batches` trace. Traces retain seven days in a local Docker volume.
The Tempo configuration targets 3.x (verified with 3.0.0): retention is set under
`overrides.defaults.compaction.block_retention`; the old top-level `compactor`
section is no longer supported.

The span chain is HTTP request → `publish_job` (Go consumer) → `process_job`
(Python worker, with `watermark` and `publish_result` children) → `process_result`
(Go consumer, covering the database commit and acknowledgment).
W3C `traceparent`/`tracestate` travel in optional JSON `trace_context` fields,
persisted atomically in the outbox. Delayed dispatch and retries retain the
original trace; each delivery creates a new span. Old jobs without context start
new traces. Invalid optional context is ignored. No baggage is propagated.
Worker spans include image and batch IDs; headers, message bodies, and raw error
text are not recorded on spans. HTTP and worker logs carry active OTLP trace IDs.
Browser uploads via presigned S3 URLs and subsequent polling are separate requests;
individual SQL/S3 calls and heartbeat threads do not have their own spans.

Export is batched and optional; without an endpoint the tracing SDK is not enabled.
`OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` can instead specify the full `/v1/traces` URL.
Both SDKs accept `OTEL_TRACES_SAMPLER=parentbased_traceidratio` and
`OTEL_TRACES_SAMPLER_ARG=0.1` to sample 10% of root traces (default: all).
Normal shutdown flushes pending spans; crashes and exporter outages can lose them.
The local Tempo service is unauthenticated and intended for development.

References: [OpenTelemetry propagation](https://pkg.go.dev/go.opentelemetry.io/otel/propagation)
and [Tempo Collector setup](https://grafana.com/docs/tempo/latest/set-up-for-tracing/instrument-send/set-up-collector/otel-collector/).

### Jaeger trace view

The same Collector trace pipeline exports to both Tempo and Jaeger. Application
OTLP endpoints and instrumentation stay unchanged; newly received traces have the
same trace IDs in both backends. Existing Tempo history is not copied to Jaeger.

Run `make observability-up`, then reload the Collector configuration:

```sh
docker compose -p watermarker-observability -f observability/docker-compose.yml restart otel-collector
```

Open [Jaeger](http://localhost:16686), select `watermarker-api`, and click
**Find Traces**, or paste a trace ID from Tempo. Generate a new batch to inspect
the complete job flow. Jaeger's OTLP port stays inside the Compose network, so it
does not conflict with the Collector's host port 4318.

Jaeger writes and queries traces through [OpenSearch](https://www.jaegertracing.io/docs/2.21/storage/opensearch/),
configured in `jaeger-config.yaml`. The `opensearch_data` Docker volume survives
container restarts and `make observability-down`; `make observability-nuke`
deletes it. Previously in-memory Jaeger history is not migrated.

OpenSearch runs as one node with a 512 MiB JVM heap (total memory use is higher),
one shard per index, and no replicas. Jaeger waits for its health check before
starting. OpenSearch has no published host ports; security is disabled for this
local development stack. Enable authentication/TLS for a remote deployment.
Unlike Tempo's seven-day retention, OpenSearch indices currently have no automatic
expiry; configure an OpenSearch ISM retention policy when you need bounded history.

After changing the Jaeger config, run:

```sh
docker compose -p watermarker-observability -f observability/docker-compose.yml up -d opensearch jaeger
docker compose -p watermarker-observability -f observability/docker-compose.yml restart jaeger
python3 observability/smoke.py
```

To verify persistence, copy the smoke trace's Jaeger URL, restart OpenSearch and
Jaeger, and open that same URL again. Traces already indexed remain available.

## Queue and outbox dashboard

Open [Watermarker queues and outbox](http://localhost:3000/d/watermarker-queues).
Run the monitor independently of the API, worker, and consumer so it continues
observing when either processing service stops:

```sh
cd apps/api
go run ./cmd/monitor
```

The monitor loads `apps/api/.env`, using the existing database, AWS credentials,
endpoint, jobs/results URLs, and OTLP endpoint. Add `SQS_JOBS_DLQ_QUEUE_URL` and
`SQS_RESULTS_DLQ_QUEUE_URL` with the actual dead-letter queue URLs. For our
LocalStack setup these are the existing queue URLs with `-dlq` appended.
All four queue URLs are required; the monitor needs `sqs:GetQueueAttributes`
on those queues and SELECT access to `outbox_messages` and `images`.

It checks each source every 15 seconds with a three-second timeout. SQS reads
only attributes: it never receives or deletes messages. Counts are approximate
and can lag. Visible, in-flight, and delayed messages are displayed separately;
DLQ totals include all three. Outbox age comes from the oldest pending image's
creation time, including intents waiting for retry. No schema migration is needed.

Each source exports check success and check time. Failed checks and observations
older than 90 seconds are excluded from backlog panels, which show no fresh data
instead of a misleading zero. The monitor defaults to a 15-second metric export interval; an explicit
`OTEL_METRIC_EXPORT_INTERVAL` overrides it. Keep that interval below 90 seconds. Replica observations use max rather than sum.
Run one monitor per environment; this dashboard assumes a single environment.

Repeat the outage exercises: stop the worker to see jobs accumulate; stop the
consumer after dispatch to see results accumulate. New batches submitted while
the consumer is stopped remain in the outbox and its oldest age increases.
Keep the monitor running throughout. No alert notifications are configured.

Queue depths are snapshots, not counts of jobs processed. A job that arrives and
finishes between 15-second polls can leave the backlog graph at zero throughout.
The dashboard also shows successful publish/result-handler operation totals from
the consumer, which capture fast work. Those totals reset on process restart and
include retries/duplicates. For a sustained-backlog test, stop the worker before
submitting a batch and leave it stopped for at least a minute.

The top processing-activity line graph uses cumulative successful consumer operations
per instance. It captures fast jobs even when queue snapshots remain zero. The
first exported value may already be nonzero; subsequent operations create steps.
Consumer export still defaults to 60 seconds, independently of the monitor.
