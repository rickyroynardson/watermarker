# Python worker

To run the complete application in containers, see [the container guide](../../docs/containers.md).

Consumes image jobs from SQS with bounded concurrency, downloads the source and watermark,
composites them with Pillow, writes a PNG to S3, and publishes the result for the
Go consumer. Uses Python 3.14, `boto3`, `Pillow`, and OpenTelemetry logging.

## Run

From `apps/worker`:

```sh
uv sync --locked
uv run --env-file ../api/.env main.py
```

When opening the repository root in VS Code, select `apps/worker/.venv/bin/python`
using **Python: Select Interpreter**. Dependencies live in this uv environment,
not the system Python. Workspace settings provide the default interpreter and
local import path; an already selected interpreter may need changing manually.

The existing API `.env` can supply local settings. In production, inject environment
variables and run `uv run --frozen main.py`; no `.env` file is required.

Required: `S3_BUCKET`, `SQS_JOBS_QUEUE_URL`, `SQS_RESULTS_QUEUE_URL`, and AWS
credentials/region through the normal SDK configuration. `AWS_REGION`,
`AWS_DEFAULT_REGION`, `AWS_PROFILE`, and `AWS_ENDPOINT_URL` are supported. Use the
same bucket, queues, region, and LocalStack endpoint as the Go process. The worker
does not use `DATABASE_URL`.

Keep the API and `go run ./cmd/consumer` running separately. The Go process dispatches
the database outbox and records Python results in Postgres. `--once` processes at
most one message and exits, which is useful for debugging or integration tests.
SIGINT/SIGTERM stops new polling and waits for active slots. Cancellation waits
and jobs reaching a cancellation check after shutdown stay unacknowledged for
redelivery; jobs past the final check can finish. An idle long poll can take up
to 20 seconds to return.

Set `WORKER_CONCURRENCY=2` or pass `--concurrency 2` to process up to two jobs
in one process. The default is 1; the CLI flag overrides the environment variable.
`--once` still processes at most one message regardless of concurrency.
Each fixed polling thread receives one message and finishes before polling again,
so busy slots leave excess work in SQS instead of prefetching into an unbounded
local queue. Each active message keeps its own visibility heartbeat and retry
handling. AWS clients and the immutable watermark cache are shared; Pillow images
remain private to each job. The blocked-worker metric remains 1 while any slot
is waiting for the cancellation API.

Start with 2 and watch memory: the 20-million-pixel limit applies per image, not
to the process total. More slots can overlap network I/O, but don't guarantee
proportional CPU throughput. Multiple worker processes can still be used; two
processes with concurrency 2 have up to four simultaneous jobs.

With OTLP metrics enabled, the performance dashboard shows active jobs, configured
capacity and slot utilization per worker instance. The gauges
`watermarker_worker_jobs_active` and `watermarker_worker_capacity` are sampled at
export time, including while idle. Active jobs include cancellation waits and
retry handling, but exclude empty long polls. Utilization is active / capacity,
not CPU usage. Short jobs between exports may not appear; use
`OTEL_METRIC_EXPORT_INTERVAL=15000` for 15-second exports. The dashboard hides
samples older than 90 seconds rather than displaying stopped workers as idle.

## Processing and delivery

- Accepts version 1 `composite` jobs from the Go API. UUIDs and persistent source
  keys are validated before accessing S3; source and watermark must share an owner.
- Decodes JPEG, PNG, and WebP by their actual bytes. Inputs are limited to 10 MiB
  and 20 million pixels each. Animated images, corrupt inputs, and invalid color
  profiles produce a failed result. EXIF orientation is applied; embedded ICC
  profiles are converted to sRGB, and source metadata is stripped.
- Fits the watermark within 20% of the source width and height without upscaling.
  Places it bottom-right with a 2% margin (of the shorter side), multiplying its
  existing alpha by 60%. Output is PNG, preserving transparency.
- Saves to `processed/{batch_id}/{image_id}.png`. Existing output is reused on
  redelivery. A [conditional S3 write](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutObject.html)
  ensures simultaneous deliveries cannot overwrite the first completed output.
- Publishes the result **before** deleting the job message. A failed publish or
  acknowledgement leaves the job retryable. Duplicate results are harmless to the
  Go consumer. Concurrent deliveries may still repeat computation.
- Transient AWS errors leave the job unacknowledged. Invalid job envelopes also
  remain for retry and eventual jobs DLQ redrive; they cannot safely identify a
  result to update. Permanent image errors and missing input objects publish
  `status: failed`. Monitor the jobs DLQ for exhausted retries.
- Refreshes the 120-second visibility timeout every 40 seconds during processing.
  Caches up to four immutable watermark downloads (at most 40 MiB of encoded data).
  Scale with bounded concurrency or additional worker processes when needed.

AWS permissions: `s3:GetObject` on `sources/*` and `processed/*`, `s3:PutObject` on
`processed/*`, and `s3:ListBucket` so missing output checks return 404. On the jobs
queue grant `sqs:ReceiveMessage`, `sqs:ChangeMessageVisibility`, and
`sqs:DeleteMessage`; on results grant `sqs:SendMessage`. Use an IAM role in AWS.

## Structure

```text
main.py               configuration, AWS clients, shutdown
worker/messages.py    job validation and result contract
worker/watermark.py   pure Pillow processing
worker/consumer.py    S3, SQS, retry ordering, visibility heartbeat
tests/test_worker.py  standard-library unittest checks
```

## Tests

From `apps/worker`, run the offline unit checks after syncing dependencies:

```sh
uv run --frozen --offline python -m unittest discover -s tests -v
```

For the complete pipeline, run from `apps/api` with Docker available:

```sh
WATERMARKER_WORKER_TEST=1 go test -race -count=1 ./internal/httpapi
```

The existing integration fixture creates disposable PostgreSQL/LocalStack services.
It uploads actual PNGs, creates a batch, dispatches the outbox, runs Python through
`uv`, consumes its result in Go, and verifies the pixels and database status. It
also checks duplicate delivery preserves the S3 object version and database
timestamp, and corrupt image bytes produce a failed result. Without the environment
flag, Go tests do not require Python or `uv`.

## Observability

See [the logging setup](../../observability/README.md) for OpenTelemetry export,
local Grafana/Loki, queries, and the pipeline smoke check.

### Processing retry backoff

Failed deliveries use SQS visibility changes with exponential backoff and
jitter: 60–120 seconds after the first receive, 120–240 after the second,
240–480 after the third, capped at 450–900 for subsequent receives.
The existing redrive policy still limits receives (three locally).
DLQ arrival therefore varies; for quick failures expect roughly 7–14 minutes
plus polling. The heartbeat still protects active processing for 120 seconds,
and stops before the retry delay is set. If setting the delay fails, the message
remains unacknowledged under its existing visibility timeout.
Invalid images still produce terminal results, without retrying.
SQS receive count controls backoff; the application's manual attempt is unchanged.

## Cancellation

Also required: `WORKER_API_URL` and `WORKER_API_TOKEN`. The token must match the
API's setting; sharing `apps/api/.env` keeps local values consistent. Apply the
cancellation migration and restart the API before starting updated workers.

Each received job checks the API before S3 work and again before writing newly
processed output. Cancelled jobs are acknowledged without publishing results.
API/network/authentication failures pause the affected polling slot and retry the
same check with exponential jitter (1–2 seconds initially, capped at 15–30 seconds).
The current message stays unacknowledged and its visibility heartbeat continues.
Recovery resumes at the failed check, including before saving output; it does not
rerun already completed image computation. SIGINT/SIGTERM interrupts the wait
(after an in-flight HTTP timeout of up to 5 seconds), leaving the job unacknowledged.
Unknown/malformed batch requests (HTTP 400/404) still follow normal job retry/DLQ
handling. Checks are not cached.

This reduces receive-count churn, not a guarantee against DLQ delivery: process
restarts, failed heartbeat updates, other workers and SQS's visibility limit can
still cause redelivery. Keep outages shorter than the queue's retention and
visibility limits. Processing duration metrics include time spent waiting on checks.
The gauge `watermarker_worker_cancellation_blocked` is 1 while any slot is waiting, otherwise
0; it measures blocked workers, not proactive health checks during idle periods.
Cancellation cannot interrupt an ongoing Pillow operation; late results remain
blocked by the database state even if cancellation races the final worker check.
