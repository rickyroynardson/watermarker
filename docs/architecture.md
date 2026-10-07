# How Watermarker works

The application separates HTTP requests from image processing. A batch request
returns after its database transaction commits; workers process its images later.

## Services

| Component | Responsibility |
| --- | --- |
| React web app | Sign-in, uploads, batch history, progress and downloads |
| Go API | Authentication, ownership, signed S3 requests, batches, quotas and live updates |
| Go consumer | Dispatch pending jobs, save results and record exhausted jobs from the jobs DLQ |
| Python worker | Read input files, composite the watermark, store output and publish a result |
| Go monitor | Observe queue depths, pending jobs, cleanup and database activity |
| Go cleanup command | Schedule expired/orphaned files and perform resumable deletion |

Monitor and cleanup are optional processes; the default application stack does
not start them. PostgreSQL stores accounts, batches, images, quota records and
pending work. S3 stores the bytes. SQS has separate jobs/results queues and DLQs.

## Follow one batch

1. The browser authenticates with a session cookie or API key. The API checks
   ownership and reserves storage before issuing signed uploads.
2. The browser sends the watermark and sources directly to S3's `uploads/`
   prefix. File bytes do not pass through the API.
3. The browser submits their keys with an idempotency key. The API copies inputs
   into `sources/`, then saves the batch, images and pending job messages in one
   PostgreSQL transaction.
4. The consumer dispatches pending messages to the jobs queue. Those database
   records are the outbox: a committed batch keeps its job intent even if SQS is
   temporarily unavailable.
5. A worker receives a job and checks whether its batch was cancelled or expired.
   It reads S3 inputs, processes the image, checks cancellation again, stores the
   output in `processed/`, and publishes a result to the results queue.
6. The consumer applies the result to PostgreSQL and publishes a batch-change
   notification through Redis. The API reads the current batch and sends it to
   connected browsers over SSE, including signed output links.

S3 copies and uploads are outside PostgreSQL transactions. Orphan cleanup handles
files left behind when a request or processing attempt fails.

## When something fails

| Failure | Behavior |
| --- | --- |
| Batch response is lost | Retry the same request with the same idempotency key |
| Job dispatch fails | The outbox retains the intent for another dispatch attempt |
| Worker dies during processing | Unacknowledged work becomes visible for redelivery |
| Transient processing error | Retry with visibility backoff; exhausted deliveries reach the jobs DLQ |
| Invalid image | Publish a terminal failure result |
| Cancellation check is unavailable | Pause that worker slot while keeping the message heartbeat alive |
| Result is delivered twice | The consumer guards committed image transitions against duplicates |
| Redis notification is missed | Reconnect/stream rotation reads a fresh database snapshot |
| File deletion fails | Keep its cleanup record and retry later |

SQS delivery is at least once. Processing can happen again after a crash; this is
not an exactly-once system. Cancellation is cooperative and cannot instantly
interrupt image computation. Redis Pub/Sub carries wake-up signals, not a durable
history of events. Each API process shares one Redis subscriber and notifies only
viewers of the affected batch.

## Authentication and limits

An OIDC provider verifies the user's identity. The API validates its ID token,
then creates a local account/session. Browser sessions and API keys are stored
hashed in PostgreSQL and share account ownership.

Redis applies per-user request limits across API instances. PostgreSQL account
locks protect storage reservations from concurrent requests. Storage allowance is
separate from request rate limits. Demo upgrades change the allowance without
charging money.

## Observe and recover

OpenTelemetry exports logs, metrics and traces to the local observability stack.
Grafana dashboards and alert rules are provisioned from files. Jaeger uses
OpenSearch for trace storage; Grafana also queries traces in Tempo. Alert rules
currently have no external notification destinations.

Database recovery needs coordination with S3 and SQS: restoring PostgreSQL does
not restore deleted image bytes or roll back delivered messages. The
[backup guide](backup-recovery.md) explains those boundaries, and the
[PITR lab](postgresql-pitr.md) demonstrates physical backup plus archived WAL.

For implementation details, read the [API](../apps/api/README.md),
[worker](../apps/worker/README.md), [web](../apps/web/README.md), and
[observability](../observability/README.md) guides.
