# Learning PostgreSQL query plans with batch history

The first tuning exercise is `GET /batches`. It already had cursor pagination
and the composite index it needs. The measured improvement is a query change,
not another index: first-page and cursor-page requests now produce different
prepared statement shapes, so a generic plan can seek directly to the cursor.
There is no migration, API contract change, or production planner setting change.

## Run the isolated lab

Start OrbStack and select its Docker context if needed:

```sh
docker context use orbstack
make query-lab
# Or, from anywhere:
python3 /path/to/watermarker/scripts/db/query_lab.py
```

Requirements: Python 3, Docker CLI, and a running Docker engine. The lab uses
the active Docker context; it does not start Docker Desktop or other engines.
The PostgreSQL image may be downloaded on the first run.

The script creates a uniquely named `postgres:18-alpine` container, exposes no
host port, and uses a temporary in-memory data directory with a 512 MiB container
limit. It loads the real application migrations' Up sections into its own
`querylab` database, just as the API integration test does. It seeds 300,000
batches: 200,000 for one busy user and 1,000 each for 100 other users. Groups of
rows share timestamps to exercise the need for an ID tie-breaker. These are
synthetic rows, not image jobs: nothing is sent to S3, Redis, or SQS.

`finally` removes the container and any associated temporary volumes on normal
completion or an error. No `.env`, application database URL, existing database,
or Compose service is used. If the process is forcibly killed, remove only its
leftover container, identifiable by the `watermarker-query-lab-` prefix.

To retain the plans for comparison:

```sh
make query-lab > /tmp/watermarker-query-plans.txt
```

The lab compares six cases:

1. Original nullable-cursor query with a forced custom plan.
2. Original nullable-cursor query with a forced generic plan.
3. Direct cursor predicate with a forced generic plan.
4. First page with the existing history index.
5. A deep `OFFSET` page using the existing index.
6. First page after temporarily dropping the history index in a transaction.

Case six rolls the index drop back. The lab also asserts that the original and
direct cursor queries return the same 21 rows. Timings are observations, never
pass/fail thresholds. The API regression test separately checks real repository
execution and HTTP pagination semantics.

## Follow the application query

`apps/api/internal/batch/handler.go` validates a limit of 1–100 and accepts an
optional cursor. `utils/pagination.go` decodes that cursor into a timestamp and
UUID. The service asks the repository for `limit + 1` rows: the extra row tells
it whether another page exists, without counting the whole history. It returns
only `limit` rows and derives the next cursor from the last returned row.

The repository always filters by the authenticated user:

```sql
SELECT id, watermark_key, created_at, completed_at, expired_at,
       EXTRACT(EPOCH FROM (completed_at - created_at))::double precision AS duration_seconds
FROM batches
WHERE user_id = $1
ORDER BY created_at DESC, id DESC
LIMIT $2;
```

For cursor pages, it adds this predicate before the ordering:

```sql
AND (created_at, id) < ($3::timestamptz, $4::uuid)
```

Only fixed SQL fragments are concatenated; user IDs, limits, and cursor values
remain bound parameters. The first page binds two parameters, cursor pages four.
The laboratory SQL uses equivalent statements with a different parameter order
for easier side-by-side comparison. Its original query is deliberately retained
as a baseline; keep the candidate projection/order aligned with the repository
if batch history changes later.

A timestamp alone is insufficient: several batches can have the same timestamp.
Ordering by `(created_at DESC, id DESC)` and comparing the same pair makes the
boundary deterministic. `<` means older/lower rows, excluding the boundary row.
A new row inserted above the cursor does not shift subsequent pages. This is
not a snapshot of the entire history: deletes or changes to ordering keys can
still affect traversal. The application treats creation timestamps and IDs as
stable; updates to other batch fields do not change that order.

## Why the existing index fits

The initial batch migration created this B-tree index, and the auth migration
renamed its owner column and index:

```sql
CREATE INDEX idx_batches_user_id_created_at
ON batches(user_id, created_at DESC, id DESC);
```

The equality condition on `user_id` locates one user's range. The remaining
columns match the requested order and cursor boundary. PostgreSQL can walk
that range in order and stop after 21 rows instead of sorting the user's entire
history. The primary key index on `id` cannot provide that user/time ordering.
The unique `(user_id, idempotency_key)` index can filter by user, but cannot
provide the history's time ordering either.

An index is not free: inserts/updates maintain it and it consumes storage.
Adding the same index again would add cost without fixing the measured issue.
A covering index with the projected fields was unnecessary for this exercise.

## The subtle issue: nullable OR and generic plans

The previous query used one shape for both pages:

```sql
AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3::uuid))
```

A **custom plan** knows the actual cursor value. With a non-null cursor it can
simplify that expression and use the boundary as an index condition.
A **generic plan** must also handle the first-page case where the cursor is
null. In this lab it used the index to find the user, then tested the optional
boundary as a filter for every row it visited.

Prepared statements can use either kind of plan. With the default
`plan_cache_mode=auto`, PostgreSQL chooses based on estimated costs; the first
five parameterized executions use custom plans before it considers a generic
one. The lab forces both modes to demonstrate the risk reliably. We have not
proved that the original query was choosing a generic plan in your running app.
The application does not force either mode: two SQL shapes allow efficient
cursor seeking even when a generic plan is selected.

## Read the measured plans

Observed locally on October 6, 2026, using PostgreSQL 18 on OrbStack:

| Case | Execution time | Shared buffer hits | Work observed |
| --- | ---: | ---: | --- |
| Original cursor, custom | 2.212 ms | 5 | Cursor included in index condition |
| Original cursor, generic | 23.345 ms | 12,737 | 180,000 rows removed by filter |
| Direct cursor, generic | 0.045 ms | 5 | Cursor included in index condition |
| Indexed first page | 0.026 ms | 4 | Stops after 21 rows |
| Offset 180,000 | 69.776 ms | 12,737 | Reads 180,021 rows to return 21 |
| First page without history index | 128.243 ms | 2,729 | Reads 200,000 rows, then sorts |

These are one run's instrumented, sequential measurements on synthetic data,
with cache/startup effects; they are not throughput guarantees or a universal
speedup ratio. Focus on rows visited and where the cursor condition appears.
Buffer hits count page accesses, not necessarily distinct pages.

The original generic plan showed:

```text
Index Cond: (user_id = $1)
Filter: (($2 IS NULL) OR (ROW(created_at, id) < ROW($2, $3)))
Rows Removed by Filter: 180000
```

The direct cursor plan showed:

```text
Index Cond: ((user_id = $1) AND (ROW(created_at, id) < ROW($2, $3)))
```

Read these fields in `EXPLAIN (ANALYZE, BUFFERS)`:

- **Index Cond:** the condition PostgreSQL can use to locate the index range.
- **Filter / Rows Removed by Filter:** rows visited and rejected afterward.
- **Actual rows / loops:** rows produced per loop; multiply where loops exceed one.
- **Sort:** explicit sorting work; the matching history index avoids it here.
- **Buffers:** cache hits and storage reads, useful for understanding work beyond time.
- **Execution Time:** measured server time with instrumentation; it excludes HTTP,
  connection acquisition, and most application work. Estimated `cost` is not milliseconds.

The no-history-index case still used the idempotency index to find the user,
then a bitmap heap scan and top-N sort. An index appearing in a plan does not
by itself mean the query does little work. A sequential scan can also be the
correct choice when a query needs much of a table.

`ANALYZE batches` updates planner statistics after seeding. `EXPLAIN ANALYZE`
actually executes its statement; this lab explains SELECTs only. Do not casually
run it on writes in a real database.

## Regression checks

```sh
cd apps/api
go test -short ./internal/batch ./internal/utils
go test ./internal/httpapi -run '^TestBatchAPIIntegration/history_cursor' -count=1 -timeout=5m
```

The integration case runs with a forced generic plan in its isolated one-connection
pool. It walks seven rows across four pages, including equal timestamps, another
user's row, and a new row inserted between requests. It verifies each original
row appears once in the right order and the final cursor is absent. The mode is
reset afterward. The test uses the existing suite's temporary PostgreSQL,
Redis, and LocalStack containers; the learning lab requires PostgreSQL alone.

## Exercises and next steps

- Find the `Filter` versus `Index Cond` difference in your own lab output.
- Change the lab's offset and observe how offset work grows with page depth.
- Explain why ID belongs in both the order and the cursor, including timestamp ties.
- Compare a lightly populated user with the busy user; estimates and plan choices
  depend on data distribution, not just total table size.
- Later apply the same process to quota aggregation or cleanup scheduling:
  inspect the actual query and existing indexes, gather representative plans,
  make one justified change, then verify behavior and document the evidence.

References: [PostgreSQL EXPLAIN](https://www.postgresql.org/docs/18/using-explain.html),
[prepared/custom/generic plans](https://www.postgresql.org/docs/18/sql-prepare.html),
[multicolumn indexes](https://www.postgresql.org/docs/18/indexes-multicolumn.html),
[indexes and ORDER BY](https://www.postgresql.org/docs/18/indexes-ordering.html).

The next exercise is now available: [quota aggregation and cleanup joins](database-quota-tuning.md).
Run it with `make quota-query-lab`; it reuses the disposable runner and reads
the application's actual quota SQL.
