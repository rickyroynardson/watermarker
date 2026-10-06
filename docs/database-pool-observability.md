# Learning database connection pool pressure

The API, result consumer, and backlog monitor now export their own pgx pool
statistics through the existing OpenTelemetry metrics pipeline. Open
[Watermarker metrics](http://localhost:3000/d/watermarker-metrics) and scroll to
the four database pool panels. No additional monitoring process is needed.

## Run locally

Start the app dependencies using your usual commands, then:

```sh
make observability-up
```

Restart the API, consumer, and monitor with their existing database and OTLP
environment settings. For a local collector, the metrics settings are:

```sh
export OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
export OTEL_METRIC_EXPORT_INTERVAL=15000
```

Each process must receive those settings. Without an OTLP endpoint, our metrics
provider remains disabled. Gauges appear after an export; rates require at least
two exports. An idle process still reports its pool snapshots. The provisioned
dashboard JSON is in `observability/grafana/dashboards/metrics.json`.

## How collection works

After opening its database pool, each instrumented binary calls
`metrics.DatabasePool(db)` and defers the returned unregister function before
closing the pool. An OpenTelemetry callback reads `db.Stat()` during collection.
It reads in-memory counters, never runs SQL, and does not borrow a connection.
This matters when all connections are already occupied.

The monitor can observe shared database backlog through SQL, but its local pool
statistics describe only the monitor. It cannot inspect the API's connection
pool. API and consumer statistics must come from those processes themselves.
The short-lived cleanup CLI does not initialize metrics and is not instrumented
by this change.

Each process already has a service name and a unique service instance resource.
Our Prometheus configuration promotes `service.name` to `service_name`;
OTLP ingestion also assigns `instance` from `service.instance.id`. Panels group
by both, keeping replicas separate. See
[Prometheus OTLP ingestion](https://prometheus.io/docs/guides/opentelemetry/).
No connection URL, SQL, user ID, or request ID is added as a metric label.

## What the metrics mean

| Prometheus metric | Type | Meaning |
| --- | --- | --- |
| `watermarker_db_pool_connections{state="acquired"}` | Gauge | Connections currently checked out |
| `watermarker_db_pool_connections{state="idle"}` | Gauge | Connections available for acquisition |
| `watermarker_db_pool_connections{state="constructing"}` | Gauge | Connections being constructed |
| `watermarker_db_pool_limit` | Gauge | Configured maximum connections for that pool |
| `watermarker_db_pool_acquires_total` | Counter | Successful acquisitions since pool creation |
| `watermarker_db_pool_waits_total` | Counter | Successful acquisitions that waited for release or construction |
| `watermarker_db_pool_canceled_total` | Counter | Acquisition attempts canceled by their context |
| `watermarker_db_pool_wait_duration_seconds_total` | Counter | Accumulated wait time for successful waiting acquisitions |

The meanings come from the installed pgx version's statistics API; see
[pgxpool Stat](https://pkg.go.dev/github.com/jackc/pgx/v5/pgxpool#Stat).
Construction can contribute to waits even when the pool is not at its maximum.
A canceled acquire can also receive a context that was already canceled, so that
counter alone does not prove saturation. Canceled wait time is not included in
the successful-wait duration or count. Neither counter is a live waiting-request
count. Process restarts reset counters; use `rate()` rather than subtracting raw
values across restarts.

The duration instrument has unit `s`. Our configured Prometheus translation adds
the seconds and counter suffixes shown above; Go instrument names use dots.

## Read the panels together

- **Utilization:** acquired / maximum, per process. Sustained high utilization
  plus growing waits is evidence of pressure. A checked-out connection may be
  executing SQL, waiting on a lock, or held by application code without executing.
- **Connections:** shows acquired, idle, and constructing state snapshots.
  These states add up to total connections, which can be below the maximum.
- **Waits and cancellations / second:** separates successful acquisitions that
  waited from attempts that were canceled. Startup construction waits are normal.
- **Mean wait:** rate of accumulated wait seconds / rate of successful waiting
  acquisitions. It excludes cancellations and is not a percentile or SQL latency.
  With no successful waits in the window, it intentionally shows no value.

Gauges are sampled at export time: short utilization spikes can disappear between
samples. Wait counters retain successful wait observations until export. If a
request is still waiting, its successful-wait observation has not been recorded
yet. No data can also mean an export outage; it does not automatically mean idle.
After a stopped process's series becomes stale, it disappears from current queries.

## Why more connections are not always the answer

Our shared `database.ConnectPgx` currently sets MaxConns to 25 and MinConns to 5.
The API, consumer, and monitor each own a separate pool. Replicas multiply the
potential connections, and other clients need capacity too. Compare the total
budget with PostgreSQL's connection limit before increasing pool sizes.

A request waiting for an available connection has not submitted its next SQL.
Once it acquires a connection, it can still wait on the quota account row lock.
A slow query or long-held transaction can occupy connections and cause more
requests to queue in the application. Use these metrics together with the
[lock contention lab](database-lock-contention.md) and database blocker query.

The runtime change is observation only: pool size, transaction logic, quota
checks, request deadlines, and retry behavior remain unchanged.

## Verify without running the app stack

```sh
cd apps/api
go test ./internal/metrics -run '^TestDatabasePoolMetrics$' -count=1 -timeout=3m
# Include the race detector:
go test -race ./internal/metrics -run '^TestDatabasePoolMetrics$' -count=1 -timeout=3m
```

The test uses disposable PostgreSQL through OrbStack, a one-connection pool,
and a local OpenTelemetry manual reader. It holds the only connection, verifies
another acquire times out, collects metrics while that connection is held,
checks acquired/canceled counts, releases it, and checks the idle state.
Unregistering the callback stops observations. No collector or app database is
needed. The test does not impose its one-connection limit on the application.

## Exercises

- Explain how a long transaction can cause both database lock waits and pool waits.
- Explain why a construction wait is not necessarily saturation.
- Explain why the mean wait panel can be empty while cancellations increase.
- Explain why averaging all API replicas' utilization could hide one busy replica.
- Compare query optimization, shortening transactions, and pool resizing before
  deciding how to respond to observed pressure.
