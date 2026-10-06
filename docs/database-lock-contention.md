# Learning quota lock contention

Run with OrbStack active:

```sh
make quota-lock-lab
# Equivalent:
PYTHONDONTWRITEBYTECODE=1 python3 scripts/db/query_lab.py quota-locks
```

This extends the existing disposable PostgreSQL runner. It loads the real
migrations, creates two demo users, and reads the account-lock query from
`apps/api/internal/quota/quota.go`. Independent psql sessions reproduce the
database locking step; this is not an HTTP load test or a benchmark of the
complete reservation flow. No application database, `.env`, S3, or running app
services are used. The container is removed afterward, including on failure.

## What the exercise checks

1. Session A begins a transaction and runs the real quota SELECT with
   `FOR UPDATE OF u` for user A. It keeps the transaction open.
2. An ordinary read succeeds. Another session locks user B successfully.
3. Session B tries the same locking SELECT for user A and waits.
4. An observer checks `pg_stat_activity` and `pg_blocking_pids()` and prints the
   waiting session, its wait event, and the named blocker.
5. B reaches its session-local four-second `lock_timeout`. The runner requires
   SQLSTATE `55P03`; another error or unexpected success fails the exercise.
6. A rolls back. A new transaction can now acquire user A's lock.

Expected output from the verified local run:

```text
PASS: ordinary reads and another user proceed while user A is locked
Observed: Lock:transactionid blocked by quota-lab-holder
PASS: same-user reservation lock times out with SQLSTATE 55P03
PASS: rolling back the holder releases the lock for user A
```

The timeout is deliberate test behavior, not an application error. The lab sets
timeouts only in its own sessions and rolls back its locking transactions.
It adds no production timeout or reservation retry policy.

## Why our quota path locks the account

`quota.Reserve` begins a transaction, locks the user row, reads current usage,
validates capacity, inserts the upload reservation, and commits. Suppose a user
has room for one more 10 MiB upload. Without coordination, two requests could
both read that remaining capacity and both insert reservations.

Our account row acts as the admission guard. All reservation callers for the
same user, including different API instances, must pass that guard before
reading usage. Under the default Read Committed isolation, the next usage
statement sees reservations committed before that statement starts. The
existing concurrent quota integration test checks the complete admission
behavior; this lab explains the lock step that makes it possible.

Other users have different account rows. They can proceed independently in
this exercise. Increasing worker count or API replica count does not remove
contention on one user's row. The earlier quota-query improvement matters here:
less work calculating usage also shortens the time spent holding this lock.

PostgreSQL row locks block conflicting writers and locking reads, while normal
reads can still use MVCC snapshots. Locks are released at transaction end.
See [PostgreSQL explicit locking](https://www.postgresql.org/docs/18/explicit-locking.html).

## Diagnose a wait

On a database you administer, this read-only query shows blocked sessions:

```sql
SELECT w.pid AS waiting_pid, w.application_name AS waiting_app,
       w.wait_event_type, w.wait_event,
       h.pid AS blocking_pid, h.application_name AS blocking_app,
       h.state AS blocking_state, h.xact_start AS blocking_since
FROM pg_stat_activity w
JOIN pg_stat_activity h ON h.pid = ANY(pg_blocking_pids(w.pid))
WHERE w.wait_event_type = 'Lock';
```

Permissions can restrict session details. The lab uses its own database owner.
An empty result means no matching wait was observed at that instant.
`pg_blocking_pids()` identifies blockers directly; see
[system information functions](https://www.postgresql.org/docs/18/functions-info.html).
Row lock waits commonly appear as `transactionid` waits, because the waiter
needs the holding transaction to finish. See
[pg_locks](https://www.postgresql.org/docs/18/view-pg-locks.html).

## Lock wait versus slow execution versus pool wait

| Situation | Where work waits | Useful evidence |
| --- | --- | --- |
| Slow SQL | Database executing the statement | EXPLAIN, rows visited, buffers |
| Lock contention | Database waiting for a conflicting transaction | Lock wait event, blocker PID, transaction age |
| Pool saturation | Application waiting for an available connection | pgx pool acquisition and connection statistics |

A request waiting for a pool connection has not submitted that SQL yet, so it
does not appear as that SQL's lock wait in PostgreSQL. This lab uses independent
psql connections and does not test pgx pool saturation. Our API pool currently
sets a maximum of 25 connections per pool in `internal/database/postgres.go`;
more connections are not a fix for serialization on one account row.

`lock_timeout` bounds each lock acquisition wait; `statement_timeout` bounds
the whole statement. Neither replaces an application request deadline, and a
statement timeout does not cover time spent waiting for a pool connection.
After an error in an explicit transaction, roll it back before reuse. See
[PostgreSQL session timeouts](https://www.postgresql.org/docs/18/runtime-config-client.html).

## Exercises

- Explain why removing the account lock could allow quota oversubscription.
- Explain why user B succeeds while user A is held.
- Explain why a faster usage query helps concurrent requests for one user.
- Compare this admission guard with cleanup's `SKIP LOCKED`: cleanup can choose
  another object, but quota must validate the requested account.
- Find the deferred rollback and commit in `quota.Reserve` and trace every error
  path to ensure it releases the guard.

Keep transactions short. The reservation path does not upload to S3 while
holding the account lock. Adding global retries, lowering application timeouts,
or resizing the pool needs workload evidence and a separate behavior review.

Continue with [database pool observability](database-pool-observability.md) to
compare application connection waits with database lock waits in Grafana.
