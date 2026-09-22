# Worker performance comparison

Requires Node 22.18+, valid PNG/JPEG/WebP source and watermark fixtures, and a
running API, consumer, monitor and worker. Runs create real batches and S3 objects;
use a local/test API key. No new dependencies or schema changes.

Included fixtures: `fixtures/source.jpg` is a deterministic textured 2400×1600
RGB image; `fixtures/watermark.png` is a 480×160 transparent watermark.
Both pass the worker decoder and compositing checks. This synthetic workload
provides a repeatable baseline; use representative photos for production estimates.

Restart the updated consumer and worker to enable the new metrics. Set
`OTEL_METRIC_EXPORT_INTERVAL=15000` for both, and keep the existing OTLP endpoint
and `deployment.environment.name=development` resource settings.
Start each worker from `apps/worker`:

```sh
OTEL_METRIC_EXPORT_INTERVAL=15000 uv run --env-file ../api/.env main.py
```

Wait at least 60 seconds for telemetry baseline samples. Ensure previous jobs and
outbox entries are drained and no other workloads are running.
From the repository root, export your test key as `WATERMARKER_API_KEY`, then:

```sh
node scripts/load/run.mjs --source scripts/load/fixtures/source.jpg --watermark scripts/load/fixtures/watermark.png --images 50 --workers 1 --runs 3 --output /tmp/one-worker.json
```

Stop the first worker gracefully. Start two fresh worker processes with the same
command in separate terminals, wait for baseline telemetry, then repeat:

```sh
node scripts/load/run.mjs --source scripts/load/fixtures/source.jpg --watermark scripts/load/fixtures/watermark.png --images 50 --workers 2 --runs 3 --output /tmp/two-workers.json
node scripts/load/compare.mjs /tmp/one-worker.json /tmp/two-workers.json
```

`--workers` records the intended configuration; it does not launch or verify
workers. Check the dashboard's fresh-worker count and your processes. Both
configurations use the same fixture bytes, number of images and sequential runs.
Fresh uploads/IDs avoid reusing completed outputs. Each run uses one batch,
and repeats the source image to keep the workload identical.
Keep machine load and fixture sizes fixed; filesystem caches can still affect results.

The report contains fixture SHA-256 hashes, timestamps, batch IDs, exact database
batch duration, successful images per second, and a Grafana time-range link.
The comparison prints median duration, median throughput and speedup relative
to the first file. Upload time is excluded; API promotion time is also outside
database batch duration. Failed or timed-out runs are saved and stop the script.
A timeout leaves jobs running: inspect the saved batch before another run.
An ambiguous creation error may have created a batch; inspect using the saved
idempotency key before starting again. The script never automatically retries a
failed batch or deletes its data. Output files overwrite the selected path.

Open http://localhost:3000/d/watermarker-performance and select each run window
(or follow report links in separate tabs). Wait for export after the run finishes.
Rate and histogram panels are estimates and need multiple samples: tiny batches
may finish too quickly for meaningful graphs. Increase image count or image size.
Use report values for exact throughput comparison.

Metrics:
- Committed successful image transitions exclude duplicate result deliveries.
  Export is best effort; a process crash can lose observations.
- Processing p50/p95 includes S3 and publishing time, excludes queue wait.
- Redeliveries count SQS receives after the first, including duplicates.
- CPU is CPU seconds per wall second (cores used) per worker.
- Memory is lifetime **peak RSS**, not current memory; restart for comparable peaks.
- Batch IDs, image IDs and run labels are only in reports/logs/traces, not metric labels.

Checks: `node --test scripts/load/run.test.mjs`, worker unittests, and Go tests.
