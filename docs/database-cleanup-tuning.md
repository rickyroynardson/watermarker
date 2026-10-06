# Learning cleanup partial indexes and row locks

This exercise changes two indexes, not the cleanup rules or locking algorithm.
The index migration is `20261006000000_add_cleanup_selection_indexes.sql`.
The existing expiry and deletion queries in `internal/cleanup/apply.go` are
unchanged. The batch-history and quota exercises remain available alongside it.

## Run the isolated exercise

With OrbStack selected and running:

```sh
make cleanup-query-lab
# Equivalent:
python3 scripts/db/query_lab.py cleanup-scheduling
# Optional report:
make cleanup-query-lab > /tmp/watermarker-cleanup-plans.txt
```

The shared runner creates a disposable PostgreSQL container with no host ports
or persistent data volume, loads the real migrations, reads the actual expiry
and deletion selection SQL from the Go source, and prepares those statements
in PostgreSQL. It does not use your application database, `.env`, or S3.
The runner fails explicitly if it cannot find the source queries. Keep its SQL
extraction in sync if the functions move or stop using raw string literals.

The fixture contains:

- 200,000 batches: 190,000 already expired, 9,000 incomplete, 980 recently
  completed, and 20 old unexpired batches. Three old batches have pending images,
  retryable failures, or an unsent outbox intent and must remain protected.
- 300,000 cleanup records: 294,000 already deleted, 5,000 due for deletion,
  and 1,000 still inside their grace period. The pending records share a retry
  timestamp to exercise ordering ties.

Each selection is explained with `EXPLAIN (ANALYZE, BUFFERS)` using a forced
generic plan. Because these SELECTs include `FOR UPDATE`, they acquire locks;
the lab wraps them in transactions and rolls back afterward. It never updates
expiry/deletion markers or invokes object deletion. Candidate sets are checked
before and after index changes. The experiment also runs the migration's real
Down SQL, checks that it restores the old layout, and reapplies its Up SQL.
The container is removed at the end. See the [first exercise](database-query-tuning.md)
for runner requirements and recovery after a forcibly killed process.

## Step one: select batches to expire

The existing selection has this structure:

```sql
SELECT b.id FROM batches b
WHERE b.completed_at < $1
  AND b.expired_at IS NULL
  AND NOT EXISTS (... pending or retryable images ...)
  AND NOT EXISTS (... unsent outbox intents ...)
ORDER BY b.completed_at, b.id
LIMIT $2
FOR UPDATE OF b;
```

A small LIMIT does not guarantee little work. Previously, the measured plan
scanned the batches table, rejected 199,980 rows, checked the remaining candidates,
and sorted the eligible rows before selecting ten.

The new index is:

```sql
CREATE INDEX idx_batches_cleanup_expiry
ON batches(completed_at, id)
WHERE expired_at IS NULL AND completed_at IS NOT NULL;
```

A **partial index** stores only rows satisfying its predicate. Already expired
history and incomplete batches are absent from this index. Its key order matches
the query's completion-time/ID order, so PostgreSQL can navigate to the old
completion-time range and stop after enough safe candidates pass the remaining
checks. The comparison `completed_at < $1` excludes null completion times and
lets the planner use the partial predicate; the explicit `expired_at IS NULL`
condition matches its other requirement.

The date cutoff belongs in the query, not in a partial predicate containing
`now()`. Index predicates need immutable expressions: their membership cannot
silently change as the clock advances. The stored null/non-null state is suitable
for a partial predicate.

Completing a batch adds an index entry. Expiring it removes the entry. Retrying
and resetting its completion time to null also removes it until completion.
Index maintenance has a write/storage cost, so this is justified by the measured
small active set within large retained history.

`Reserve` still acquires its global advisory transaction lock, locks the selected
batches, and rechecks eligibility before setting `expired_at`. That protects
source promotion, retries, and concurrent scheduling. We did not add `SKIP LOCKED`
to expiry selection or remove the advisory lock.

## Step two: claim one object for deletion

`DeleteOne` uses:

```sql
SELECT key FROM cleanup_objects
WHERE deleted_at IS NULL
  AND delete_after <= now()
  AND next_attempt_at <= now()
ORDER BY next_attempt_at, key
LIMIT 1
FOR UPDATE SKIP LOCKED;
```

The previous index was already partial:

```sql
ON cleanup_objects(next_attempt_at)
WHERE deleted_at IS NULL
```

It excluded deleted history and provided the retry-time order. However, records
sharing that timestamp still needed sorting by `key`. In the fixture, PostgreSQL
visited 6,000 pending entries, rejected 1,000 grace-period records, and performed
an incremental sort before claiming one object.

The migration replaces it with:

```sql
ON cleanup_objects(next_attempt_at, key)
WHERE deleted_at IS NULL
```

Now the index matches both ORDER BY columns. In the measured fixture it visited
one eligible entry and avoided the sort. `delete_after` remains a filter: the
change does not eliminate grace checks. If many early index entries have future
grace deadlines or are locked, the query can still visit and reject/skip many
entries before finding one it can claim. It is not universally constant-time.

The `LockRows` plan node performs row locking. These queries still need heap
access to lock rows; this is not an index-only scan improvement.

## Read the measurements

One local PostgreSQL 18 / OrbStack run on October 6, 2026, before the migration
was added to the runner's initial schema:

| Selection | Before | After | Main plan difference |
| --- | ---: | ---: | --- |
| Ten expiry candidates | 21.580 ms | 0.106 ms | Table scan/sort becomes partial index scan |
| One deletion claim | 1.697 ms | 0.054 ms | Timestamp tie-group sort disappears |

Expiry buffer accesses changed from 2,961 hits to 14 hits plus two reads;
deletion accesses changed from 76 hits plus six reads to two hits plus two reads.
These are instrumented, sequential measurements on synthetic data, not production
latency promises. The first statement and later statements have different cache
conditions. Use rows visited, index conditions, and sort nodes to understand the
improvement; timings vary with hardware and concurrent activity.

The lab verifies the same ten expiry IDs, all 17 safe old batches when using a
larger limit, and the same first due object. No elapsed-time pass/fail thresholds
are used.

## How SKIP LOCKED coordinates concurrent deletion

The behavior already existed; this change adds a direct integration check.

1. Cleanup A begins a transaction and locks object A's cleanup row.
2. It keeps that row locked while the bounded S3 deletion callback runs.
3. Cleanup B executes the same selection on another connection. `SKIP LOCKED`
   skips object A rather than waiting, allowing B to claim object B.
4. B records its completed deletion and commits. A becomes available only after
   its own transaction commits or rolls back.

The test blocks A's fake deletion callback with a channel, invokes B through
another database pool, and verifies B completes while A is still blocked. A
third claim returns no work because the remaining due row is locked; grace-period,
future-retry, and already-deleted rows are all ineligible. Then A is released and
both claimed rows have one recorded successful attempt. This proves concurrent
claims do not select the same locked row in this scenario.

`SKIP LOCKED` gives a view that omits locked rows. "No work" can therefore mean
"no eligible unlocked work," not "the queue is permanently empty." Poll again
later. It is appropriate for this queue-like workload, not a consistent listing
of every pending object. It does not prevent table-level locks or guarantee
fairness when rows stay locked for a long time.

This is not exactly-once S3 deletion. If S3 accepts a delete and the process
fails before committing the database marker, the cleanup row can be retried.
Deletion must remain idempotent. The existing crash/retry integration test covers
this recovery path. No Redis lock or new concurrency library is needed.

## Verify and apply

```sh
cd apps/api
go test ./... -count=1 -timeout=5m
# Focused concurrent claim test with the race detector:
go test -race ./internal/httpapi -run '^TestBatchAPIIntegration$/^pipeline$/^cleanup_claims' -count=1 -timeout=5m
```

These tests use disposable containers. The new migration has not been applied
to your development database. To use the indexes there, start your development
PostgreSQL and run the existing migration command against its configured URL:

```sh
make migrate-up
```

This runs all outstanding migrations, so check the migration status and your
configured database first. The migration is transactional; its Down section
removes the expiry index and restores the old single-column due index. It does
not delete application data or reverse object deletion.

Ordinary CREATE/DROP INDEX can block writes on a live table. That is appropriate
for this local learning migration. A large live deployment would need a reviewed
concurrent-index rollout using `CREATE INDEX CONCURRENTLY` outside a transaction,
then an intentional old-index removal; do not add CONCURRENTLY to this Goose
transactional migration blindly.

## Boundaries and exercises

This lab profiles the expiry selector and single-object claim. It does not
benchmark the entire cleanup run or optimize `candidateQuery` in `plan.go`.
That query still resolves shared source/output references across history so
objects referenced by retained batches are preserved. Existing integration
coverage checks those safety rules. The global scheduling lock is unchanged;
multiple deletion callers can cooperate, but scheduling is still serialized.

- Find the partial predicate, range condition, and ORDER BY match in each plan.
- Compare a large deleted history with a large pending queue: partial indexes
  help the former, but do not make the latter disappear.
- Move grace-period records ahead of eligible keys and observe residual filtering.
- Explain why `LIMIT 1` can still require scanning or sorting thousands of entries.
- Explain why retries are safe after a process crashes while holding a row lock.
- Inspect how index membership changes when completion/expiry/deletion markers
  change, and why the time cutoff remains a query parameter.

References: [PostgreSQL partial indexes](https://www.postgresql.org/docs/18/indexes-partial.html),
[index ordering](https://www.postgresql.org/docs/18/indexes-ordering.html),
[locking and SKIP LOCKED](https://www.postgresql.org/docs/18/sql-select.html),
[concurrent index creation](https://www.postgresql.org/docs/18/sql-createindex.html).

Next exercise: [quota lock contention](database-lock-contention.md) reproduces
same-user blocking, identifies the blocker, and demonstrates transaction rollback.
