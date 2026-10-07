# Learning live transaction and lock monitoring

This continues the database performance topic on the same branch. Pool metrics
describe the application's connection usage; this change adds a database-side
view of lock waits and open transactions. No schema migration is needed.

## What changed

The existing monitor calls `database.ObserveActivity` with a three-second context
in its polling loop, alongside queue, outbox, and cleanup reads. The query reads
`pg_stat_activity` for client sessions in its current database and excludes its
own backend. It uses `pg_blocking_pids()` to identify blockers. The results are
recorded through the existing OpenTelemetry pipeline.

| Metric | Meaning |
| --- | --- |
| `watermarker_db_lock_sessions{state="blocked"}` | Client sessions waiting on a PostgreSQL Lock event |
| `watermarker_db_lock_sessions{state="blocking"}` | Distinct positive blocker PIDs for those sessions |
| `watermarker_db_transaction_idle` | Sessions idle inside an open transaction, including aborted transactions |
| `watermarker_db_transaction_oldest_age_seconds` | Age of the oldest open client transaction |

Blocking PIDs are deduplicated: one holder blocking five requests counts as one
blocker and five blocked sessions. Prepared-transaction blockers can have PID
zero; the positive-PID blocker count excludes that placeholder, while their
waiting client sessions can still contribute to the blocked count.

Age includes active and idle transactions. It is elapsed time since transaction
start, not a histogram of completed transaction durations. The oldest transaction
does not necessarily block anything. An idle pool connection with no transaction
is different from a connection idle *inside* a transaction.

These are client-session snapshots, not PostgreSQL-wide statistics: background
workers and other databases are excluded. Short waits between polling cycles
can be missed. Polling can take longer than 15 seconds when other monitor reads
are slow. Nothing here terminates sessions, retries transactions, or changes locks.
No SQL text, PIDs, user identifiers, or database connection strings are exported.

## Grafana and failed observations

Start the existing observability stack and restart the monitor with its existing
DATABASE_URL and OTLP settings. Open
[Watermarker metrics](http://localhost:3000/d/watermarker-metrics) and scroll below
the pool panels. The three new panels show blocked/blocking sessions, idle open
transactions, and oldest transaction age.

Every poll records success and time under `source="database"`, using the existing
observation metrics. Dashboard expressions require a successful observation
less than 90 seconds old. A failed query or stale monitor hides prior values;
it does not turn an unknown observation into a healthy zero. A fresh zero means
the query succeeded and found none. Multiple monitors observing the same database
use maximum readings rather than adding duplicate counts. The panels assume one
shared application database per deployment environment.

The existing observation health panels list the new source automatically.
The existing backlog health alert still checks its original queue/outbox/cleanup
sources; this change adds no new lock-duration alert or threshold.

## Monitoring permissions

PostgreSQL limits session details visible to ordinary roles. If another client
session's state is hidden, this observer returns an error instead of claiming
there are zero locks or transactions. The monitor logs that failure and records
an unsuccessful observation. When separate database roles are used, a database
administrator can grant the monitoring role the intended statistics visibility:

```sql
GRANT pg_read_all_stats TO your_monitor_role;
```

This code does not grant privileges automatically. The role exposes broader
statistics, so choose the intended monitoring identity. See
[PostgreSQL activity statistics and visibility](https://www.postgresql.org/docs/18/monitoring-stats.html).
Keep `track_activities` enabled; a client session with disabled tracking also
causes an unsuccessful observation rather than a misleading zero.

## See a controlled wait in Grafana

Use an expendable local database with the app monitor running. In terminal A,
choose an existing user ID and hold its row without changing data:

```sql
BEGIN;
SELECT id FROM users WHERE id = '<existing-user-uuid>' FOR UPDATE;
-- Leave this transaction open while watching Grafana.
```

In terminal B, use the same user ID:

```sql
BEGIN;
SET LOCAL lock_timeout = '60s';
SELECT id FROM users WHERE id = '<same-user-uuid>' FOR UPDATE;
```

B waits for A. Allow a monitor poll and metrics export: blocked/blocking counts
should rise, A contributes an idle open transaction, and oldest age grows.
Then run `ROLLBACK;` in A. B can finish its SELECT; run `ROLLBACK;` there too.
The next successful poll should clear the counts and age when no other open
transactions remain. If B times out first, it remains in an aborted transaction
until you roll it back. This holds a real account lock, so do it in a local
learning environment where delaying that user's requests is acceptable.

An idle transaction consumes an acquired pool connection. If enough requests
queue behind its lock, more pool connections may become occupied and later
requests may wait for pool acquisition. Compare the activity and pool panels;
neither alone explains every slow request. To inspect the actual blocker PID,
use the read-only diagnostic query in the
[lock contention guide](database-lock-contention.md).

## Automated verification

```sh
cd apps/api
go test ./internal/database ./internal/metrics -count=1 -timeout=3m
go test -race ./internal/database -run '^TestObserveActivity$' -count=1 -timeout=3m
# From the repository root:
python3 observability/test_database_panels.py
```

The integration test uses disposable OrbStack PostgreSQL. It holds a row on one
connection, blocks another, observes one waiter and one idle blocker, rolls back,
and checks the cleared snapshot. It also switches the observer to a restricted
role to verify hidden activity fails, then grants statistics visibility inside
that disposable database and verifies recovery. It also verifies disabled activity
tracking fails and re-enabling it restores observations. No development roles are changed.

Metric checks verify recorded values. Promtool checks the actual dashboard
expressions against fresh populated data, fresh zeroes, failures, stale data, and
an absent monitor. No app services or existing database are needed for the tests.

Continue reading: [pool pressure](database-pool-observability.md),
[row locking](database-lock-contention.md), and
[PostgreSQL blocker functions](https://www.postgresql.org/docs/18/functions-info.html).
