# Python worker

Consumes one image job at a time from SQS, downloads the source and watermark,
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
SIGINT/SIGTERM stops polling after finishing the current job; an idle long poll
can take up to 20 seconds to return.

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
  Scale by running more worker processes when needed.

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
