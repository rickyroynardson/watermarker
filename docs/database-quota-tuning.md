# Learning quota aggregation and cleanup joins

This exercise tunes the actual storage-usage SQL in
`apps/api/internal/quota/quota.go`. The application now joins each reservation
or stored output to its cleanup record directly, instead of hiding that lookup
inside a nested `NOT EXISTS` predicate. The accounting rules, user lock,
API response, and database schema are unchanged. No new indexes or stored
usage counter were added.

## Run it locally

With OrbStack running and selected as the active Docker context:

```sh
make quota-query-lab
# Equivalent:
python3 scripts/db/query_lab.py quota-usage
# Optional plan report:
make quota-query-lab > /tmp/watermarker-quota-plans.txt
```

The existing runner creates its own temporary PostgreSQL container, loads the
real migration Up sections, and removes the container afterward. It does not
use `.env`, your application database, S3, queues, or Redis. See
[the batch-history exercise](database-query-tuning.md) for requirements and
cleanup details. `make query-lab` still runs that earlier exercise by default.

The quota fixture includes 21 users, 42,000 batches, 84,000 images, 126,004
upload reservations, 84,000 stored outputs, and 105,006 cleanup records. One user
owns 2,000 batches; half their batch objects have confirmed deletion while the
references remain in the database as history. Other users' histories exercise
how the plan handles global tables. Extra abandoned uploads cover active,
pending deletion, persistent-only deleted, and fully deleted states.

The runner reads the current `const usage` SQL directly from the Go source;
there is no separate hand-maintained candidate query. The SQL file retains the
original query as a historical baseline. If that constant moves or stops being
a raw string, update the runner: it fails explicitly instead of silently using
an outdated query. It also explains the current account endpoint query shape.

The lab compares the original and revised queries without extra indexes, then
tries two reference indexes **only inside the disposable database**. It drops
those indexes before explaining the final account query to match the actual
application schema. It checks the usage total before and after the rewrite,
with and without the experimental indexes. There are no timing assertions.
The experiment has a 60-second per-statement timeout; resource-constrained
machines may need a smaller synthetic fixture.

## Where the query runs

- `GET /account/quota` reads plan allowance and current usage for the account UI.
- `quota.Reserve` locks the user's row with `FOR UPDATE`, calculates usage,
  checks the requested bytes against the allowance, and inserts the reservation
  in the same transaction.

The user lock serializes concurrent admissions, including requests handled by
different API instances. Faster calculation can shorten that lock's duration;
the lab measures SQL execution, not lock contention or end-to-end HTTP latency.
The lock remains necessary even after optimizing the query.

## Preserve the accounting rule first

For a reservation, define:

- `D`: persistent source deletion has been confirmed.
- `R`: a batch watermark or image still references the key.
- `S`: deletion of the matching temporary staging key has been confirmed.

The original query releases its charge only when:

```text
D AND (R OR S)
```

A referenced source can release its charge after confirmed persistent deletion.
An abandoned upload needs both the persistent and staging locations confirmed
deleted, because it might never have been promoted out of staging.

The equivalent rule for retaining its charge is:

```text
NOT D OR NOT (R OR S)
```

| Object state | Charged? |
| --- | --- |
| Active, no cleanup record | Yes |
| Scheduled deletion or failed deletion | Yes |
| Referenced source, persistent deletion confirmed | No |
| Abandoned upload, persistent deletion only confirmed | Yes |
| Abandoned upload, staging deletion only confirmed | Yes |
| Abandoned upload, both locations confirmed deleted | No |
| Stored output, deletion pending | Yes |
| Stored output, deletion confirmed | No |

A cleanup record existing does not mean the S3 object has been deleted.
`deleted_at` is the confirmation. A recreated late output resets that confirmation
and becomes charged again; existing result/cleanup tests verify this behavior.

## What the SQL changed

Previously, the reservation calculation had this structure:

```sql
FROM upload_reservations r
WHERE r.user_id = $1
  AND NOT EXISTS (
    SELECT 1 FROM cleanup_objects c
    WHERE c.key = r.key
      AND c.deleted_at IS NOT NULL
      AND (batch_reference OR image_reference OR staging_deleted)
  )
```

Now the cleanup lookup is a direct left join:

```sql
FROM upload_reservations r
LEFT JOIN cleanup_objects c ON c.key = r.key
WHERE r.user_id = $1
  AND (
    c.deleted_at IS NULL
    OR NOT (batch_reference OR image_reference OR staging_deleted)
  )
```

These are explanatory fragments; the actual code uses the corresponding
`EXISTS` predicates. The output sum uses the same direct join, retaining rows
where `c.deleted_at IS NULL`.

A **left join** retains a reservation even when there is no matching cleanup
record. In that case the cleanup columns are null. `c.deleted_at IS NULL`
therefore covers both “no cleanup record” and “deletion not confirmed.”

`cleanup_objects.key` is a primary key. Each reservation/output can match at
most one cleanup row, so the join cannot multiply rows and inflate the sum.
That uniqueness is part of the rewrite's correctness; the same transformation
would need more care with a non-unique cleanup table. `COALESCE(sum(...), 0)`
keeps accounts with no stored records at zero.

## What the database plans showed

In a local PostgreSQL 18 / OrbStack run on October 6, 2026:

| Query with application indexes only | Execution time | Shared buffer hits |
| --- | ---: | ---: |
| Original nested predicate | 1,452.423 ms | 93,659 |
| Revised cleanup joins | 134.235 ms | 7,144 |

These are instrumented measurements on synthetic data, in sequential runs.
They are not a promised production speedup. JIT compilation, temporary-file
spills, caches, and device load caused substantial timing variation between
runs. The row-access change was the more useful evidence.

The original plan scanned `upload_reservations_pkey`, filtered by user, and
removed **120,000 other-user rows** to retrieve the relevant 6,004 reservations.
Its nested predicate became a **hash anti join** with reference checks in the
join filter. An anti join keeps rows that have no qualifying match on the other
side. This is a valid implementation of `NOT EXISTS`, but the full plan was
expensive for this fixture.

The revised plan used `upload_reservations_owner` to retrieve those 6,004
reservations and a hash left join to apply cleanup status. It also retained the
existing `output_storage_owner` index for the output sum. Hash joins build a
lookup structure from one input and probe it with the other; the planner can
choose a different physical join orientation than the written SQL. A reported
hash right join can therefore implement our written left join.

The rewrite gave the optimizer a different expression of the same rule, leading
to a better plan here. SQL text order does not dictate execution order, and a
left join is not inherently faster than `NOT EXISTS`.

### Why we did not add the proposed indexes

The experiment tried `batches(watermark_key)` and `images(source_key)`. The
measured plans still used **hashed subplans**, reading those reference tables
once to build key sets, rather than using the proposed indexes for individual
lookups. Inspect `loops=1`: an `EXISTS` expression does not necessarily mean
one full table scan for every reservation.

The rewrite improved owner filtering without those indexes. Since the exercise
did not establish a need for the extra index storage and write maintenance,
the application schema was left unchanged. The lab retains the comparison to
show why looking at the chosen plan matters more than assuming every predicate
needs a new index.

### JIT was another contributor

The original plan's estimated cost was high enough to trigger JIT compilation,
including inlining and optimization. In the sample above, JIT took about
586 ms; some other runs took much longer. The revised plan did not trigger JIT
under the same default thresholds. No application-wide JIT configuration was
changed. PostgreSQL bases that decision on estimated cost, not the measured
runtime; check the `JIT` section instead of attributing every millisecond to
scanning or joins.

## Verify behavior

```sh
cd apps/api
go test ./... -count=1 -timeout=5m
# Focus on quota integration:
go test ./internal/httpapi -run '^TestBatchAPIIntegration/plan_quotas' -count=1 -timeout=5m
```

The new deletion-state cases verify an empty account, active reservations,
failed/pending cleanup, both kinds of partial abandoned-upload deletion,
fully deleted abandoned uploads, and watermark/image references. Existing tests
also verify concurrent admission limits across two database pools, actual
output bytes, delayed quota release, and late-output recharging. The integration
suite uses its own temporary services; it does not modify your development data.

The fixture independently expects **7,169,536 bytes**: 1,000 active batches ×
(three 1,024-byte sources + two 2,048-byte outputs), plus three still-charged
512-byte abandoned reservations. Both query versions must match that total.

## Remaining limit and exercises

This remains an aggregate query. It must read a user's reservations and outputs;
the measured hash joins/subplans also scan global cleanup/reference tables.
The rewrite does not make usage calculation constant-time or eliminate every
full-table scan. We have not established which plan your running app previously
used or benchmarked production contention.

- Locate the owner index condition and count the other-user rows rejected by the
  original plan.
- Explain why a primary key on cleanup keys prevents inflated sums after the join.
- Find the hash subplans and their loop counts; distinguish one global scan from
  thousands of repeated scans.
- Compare estimated costs, actual times, buffer accesses, temp reads/writes, and
  JIT overhead without treating cost units as milliseconds.
- Explain why removing the user lock would still allow concurrent uploads to
  exceed quota even with a fast query.
- Consider a transactional usage counter only if realistic workloads show the
  remaining aggregation is too expensive; every reservation, output update,
  confirmed deletion, and late recreation would then need consistent updates.

References: [PostgreSQL joins](https://www.postgresql.org/docs/18/queries-table-expressions.html),
[EXPLAIN and subplans](https://www.postgresql.org/docs/18/using-explain.html),
[JIT decisions](https://www.postgresql.org/docs/18/jit-decision.html),
[multicolumn index behavior](https://www.postgresql.org/docs/18/indexes-multicolumn.html).

Continue with [cleanup partial indexes and row locking](database-cleanup-tuning.md),
using `make cleanup-query-lab`.
