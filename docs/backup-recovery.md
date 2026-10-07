# Learning PostgreSQL backup and recovery

This is a new topic after database performance: recovery from data loss.
The exercise uses PostgreSQL's native `pg_dump` and `pg_restore`, the project's
real migration SQL, and synthetic records. It changes no application behavior.

## Run the drill

With OrbStack active:

```sh
make backup-recovery-lab
# Equivalent:
PYTHONDONTWRITEBYTECODE=1 python3 scripts/db/query_lab.py backup-recovery
```

No running app, LocalStack, AWS credentials, local PostgreSQL installation, or
DATABASE_URL is needed. The existing runner creates a uniquely named PostgreSQL
18 container with no host ports and a temporary data filesystem. It accepts no
target database URL. All drop/create operations use fixed database names inside
that newly created container. Your development database and `.env` are untouched.

The dump is written to a host temporary directory so it survives dropping the
source database. Both the archive and container are removed when the exercise
finishes or fails. This is a restore drill, not a retained backup of your app.
If the process is forcibly killed, a temporary file or container may remain;
use the runner's printed `watermarker-query-lab-*` container name to identify
this run's container before removing it. Do not remove other containers.

## Follow the recovery flow

1. Load the real migrations' Up SQL through the existing learning runner.
2. Seed two users, API-key/session/login demo records, quota plans/add-ons,
   upload reservations, pending/completed/cancelled batches, all four image
   statuses, a retryable failure, an outbox intent, output accounting, and
   pending/deleted/failed cleanup records.
3. Capture counts and content fingerprints for every public table, plus checked
   column, constraint, index, and enum definitions.
4. Run `pg_dump --format=custom --no-privileges`. Save the archive checksum and
   confirm `pg_restore --list` can read its table of contents.
5. Commit a new user *after* the backup. Then drop the source database.
6. Create an empty `recovered` database from `template0` and restore the archive
   with `--single-transaction --exit-on-error --no-owner --no-privileges`.
7. Run `ANALYZE`, compare all captured counts/content/schema fingerprints, and
   confirm the post-backup user is absent.

The sample job payload and object keys are deliberately synthetic; no worker
consumes them, and no object is created or deleted. Migration loading uses Up SQL
directly, as in the earlier labs; it does not create a Goose version ledger.
A full dump of a real application database would include its existing Goose
ledger, and recovery should verify that ledger before running newer migrations.

## What the tools do

`pg_dump` makes a logical backup of one database using a consistent snapshot.
The custom format can be inspected and restored using `pg_restore`; it is not
a copy of the PostgreSQL data volume. See
[PostgreSQL pg_dump](https://www.postgresql.org/docs/18/app-pgdump.html).

The restore creates tables, loads data, and restores indexes and constraints.
`--single-transaction` makes the restoration all-or-nothing. `--no-owner` assigns
objects to the restoring role, and `--no-privileges` omits grants/revokes in this
portable lab. A real deployment must provision and verify its intended roles,
ownership, and grants separately. The new database starts empty; the drill does
not restore over an existing application database. See
[PostgreSQL pg_restore](https://www.postgresql.org/docs/18/app-pgrestore.html).

Reading an archive's table of contents is only an early check. Actually restoring
and comparing contents is the recovery proof here. SHA-256 identifies the archive
and detects a byte change before this restore; a checksum stored alongside a
backup is not independently authenticated provenance.

The row fingerprints sort full JSON representations, so a matching count alone
is insufficient: changes to payloads, ownership IDs, retry attempts, byte counts,
timestamps, or deletion markers change the fingerprint. Schema comparison covers
the definitions listed above. It does not verify roles/ACLs, database-wide
configuration, functions, triggers, sequence state, extensions, or objects outside
the public schema. The current migration set has UUID IDs; adding sequence-backed
IDs or other object types requires extending the recovery checks.

## Read the report

One verified local run on October 6, 2026:

```text
PASS: custom archive created and its table of contents is readable
Source database dropped inside disposable container only
PASS: all public table contents, counts, and checked schema definitions restored
PASS: committed post-backup write is absent, as expected
```

The report included a 24,061-byte archive, about 0.128 seconds for the dump,
and 1.411 seconds to create, restore, analyze, and verify the new database.
Archive size and timings vary; these tiny fixtures are not production capacity
measurements. Table counts were:

| Table | Restored rows |
| --- | ---: |
| users | 2 |
| api_keys | 1 |
| auth_logins | 1 |
| auth_sessions | 1 |
| batches | 3 |
| images | 4 |
| outbox_messages | 1 |
| upload_reservations | 6 |
| output_storage | 1 |
| quota_plans | 2 |
| quota_addons | 1 |
| cleanup_objects | 3 |

## Recovery point and recovery time

**Recovery point objective (RPO)** is the tolerated data-loss window.
**Recovery time objective (RTO)** is the tolerated time to restore service.
See [AWS recovery planning](https://docs.aws.amazon.com/wellarchitected/latest/reliability-pillar/plan-for-disaster-recovery-dr.html).

Our post-backup user demonstrates the recovery point: a dump cannot recover
writes committed after its snapshot. With daily logical backups, the possible
loss can approach a day, and failed backup runs can make the window longer.
Backup completion time is not automatically the snapshot's recovery point.

`restore_and_verify_seconds` measures only this local create/restore/analyze/check
phase. It excludes failure detection, fetching a backup, provisioning a host,
fixing credentials, restoring files, reconciling jobs, restarting services, and
validating the application. It is not a production RTO guarantee.

For finer recovery points, learn base backups plus continuous WAL archiving and
point-in-time recovery. A logical dump alone does not provide WAL-based PITR.
See [PostgreSQL continuous archiving](https://www.postgresql.org/docs/18/continuous-archiving.html).

## Recovery in this distributed application

PostgreSQL contains user ownership, image/batch state, quota accounting, outbox
intents, and cleanup progress. It contains object *keys*, not S3 image bytes.
Restoring an old row cannot recreate a file that cleanup already deleted.
S3 object protection and restoration need their own recovery design. Do not
blindly enable bucket versioning here: our cleanup tool currently requires an
unversioned bucket and would need deliberate changes to handle object versions.

SQS state is also outside a PostgreSQL dump. A restored unsent outbox intent
may have been published after the snapshot and can publish again after recovery.
Conversely, existing queue messages may reference a batch created after that
snapshot, whose row is now absent. Existing idempotent result handling helps
with duplicate delivery but does not eliminate cross-system recovery work.

In a real recovery, pause application writes, workers, consumers, and destructive
cleanup while choosing the database/files recovery point. Restore to a separate
target, validate it, reconcile outstanding messages and objects, then resume
services intentionally. Review recovered sessions, API-key revocations, and
cleanup markers too: database restoration rolls those records back in time.
This drill does not start services or simulate a complete app recovery.

## Infrastructure learning steps for later

- Keep retained backups outside the primary database volume and failure domain.
  A Docker volume preserves restarts but is not an independent backup.
- Add a deliberate retention policy and protected storage for real dumps; database
  archives contain user data and authentication records. This drill stores only
  fake data temporarily and configures no scheduled backup or durable storage.
- Test restoring into a separate PostgreSQL instance and then a complete app stack,
  including file availability and queue reconciliation. This first exercise uses
  two database names on the same disposable PostgreSQL server, not host-loss recovery.
- When using real AWS, explore managed database snapshots/PITR and isolated restore
  targets. These are future learning steps, not AWS resources configured here.

## Exercises

- Explain why matching row counts does not prove matching data.
- Explain why the post-backup write is absent and how backup cadence affects loss.
- Trace what a restored outbox intent does when the original job was already sent.
- Explain why a database dump cannot recover deleted S3 bytes.
- Identify what is missing from the measured restore time before calling it an RTO.
- Explain which checks to add when introducing sequences, triggers, or extensions.

The runnable drill is its own check: subprocess failures, snapshot differences,
or an unexpected surviving post-backup write produce a nonzero exit status.

For the next exercise in this same topic, run `make pitr-recovery-lab`; see
the [physical backup and archived WAL guide](postgresql-pitr.md).
