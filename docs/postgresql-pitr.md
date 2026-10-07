# PostgreSQL physical backup and WAL recovery lab

This continues the [backup/recovery topic](backup-recovery.md). Python orchestrates
native PostgreSQL commands; PostgreSQL performs the backup, archiving and recovery.
It does not enable backups on the application's database.

## Run locally

Start OrbStack and select its Docker context, then run from the repository root:

```sh
make pitr-recovery-lab
```

No AWS credentials, LocalStack, `.env`, application services or host ports are
needed. The runner creates a PostgreSQL 18 container, a private temporary backup
volume and two recovery containers. It prints their names and removes them when
finished, including on ordinary failures. If the process is forcibly killed,
remove only the printed containers and repository volume manually.

## What happens

1. Enable WAL archiving before taking the backup. Load the real application
   migrations and synthetic fixtures into an isolated database.
2. Run `pg_basebackup` and validate its manifest with `pg_verifybackup`.
3. Commit a new user **after** the base backup. Capture the expected table contents
   and checked schema, then create the named restore point
   `watermarker_before_delete`.
4. Delete a completed batch (including cascading records), and commit another
   user **after** the target. Switch the WAL segment and wait for it to be archived.
5. Copy the base into two separate recovery directories. Stop the primary.
6. Attempt recovery without archive retrieval. It must fail because the named
   target lies beyond the WAL included in the base backup.
7. Recover the other copy using archived WAL, stop at the named point and promote.
   Compare all public table fingerprints and checked schema with the target state.

Both recovery containers have networking disabled. The successful restore must
keep the first new user, restore the deleted batch, and exclude the later user.
This proves that recovery replayed WAL beyond the base backup and respected the
chosen boundary. Matching row counts alone would not prove this.

## Read the implementation

- [query_lab.py](../scripts/db/query_lab.py): disposable resource lifecycle,
  PostgreSQL settings and migration loading.
- [pitr_lab.py](../scripts/db/pitr_lab.py): native backup commands, target creation,
  failure simulation and both recovery attempts.
- [archive-wal.sh](../scripts/db/archive-wal.sh): archive a completed WAL file.
- [backup_lab.py](../scripts/db/backup_lab.py): shared data/schema fingerprints.

The lab starts the primary with:

```conf
wal_level = replica
archive_mode = on
archive_command = 'sh /backup/archive-wal.sh "%p" "%f"'
```

PostgreSQL invokes the archive command automatically for completed WAL segments.
`%p` is the source path and `%f` the archive filename. The script accepts an
identical existing file, rejects conflicting contents, and uses a staged copy,
filesystem sync and rename before reporting success. Its assumptions are one
archiver and a private repository. This is a learning script, not a production
backup manager. See PostgreSQL's [archiving requirements](https://www.postgresql.org/docs/18/continuous-archiving.html#BACKUP-ARCHIVING-WAL).

The physical backup command inside the container is:

```sh
pg_basebackup -h 127.0.0.1 -U querylab \
  --pgdata=/backup/base --format=plain --wal-method=stream --checkpoint=fast
pg_verifybackup /backup/base
```

Streaming WAL here makes the base backup recoverable through the backup window.
Continuous archiving supplies subsequent changes. See
[pg_basebackup](https://www.postgresql.org/docs/18/app-pgbasebackup.html).

The recovery copy contains `recovery.signal` and starts with:

```conf
archive_mode = off
restore_command = 'cp /backup/wal/%f %p'
recovery_target_name = 'watermarker_before_delete'
recovery_target_timeline = 'current'
recovery_target_action = 'promote'
```

`restore_command` retrieves archived segments for replay. The named target gives
this drill a deterministic boundary. For an incident at a known time, use
`recovery_target_time` with an explicit timezone instead of the name; do not set
both targets. The report's `target_observed_at` is an observation timestamp, not
an exact timestamp target. See [recovery targets](https://www.postgresql.org/docs/18/runtime-config-wal.html#RUNTIME-CONFIG-WAL-RECOVERY-TARGET).

## Verified result and limits

The local run restored all 12 public tables, including three users, three batches
and four images. Archiving reported five successes and zero failures. Base backup
plus verification took 1.060 seconds; successful recovery plus verification took
1.923 seconds. These tiny fixture timings are not production RTO estimates.

A physical backup contains the whole PostgreSQL cluster. Both source and recovery
use the same PostgreSQL 18 image. The checker verifies public table contents,
columns, constraints, indexes and enums; it does not independently verify roles,
permissions, sequences, functions, triggers or other schemas. Configuration
changes after the base backup are not recovered by WAL. Database recovery also
does not restore S3 files or reconcile SQS messages; review the cross-system
recovery discussion in the earlier guide.

The repository lives in the same Docker VM as the primary and is deleted after
the drill. It demonstrates separation from PGDATA, not protection against loss
of the host. There is no retained backup schedule, remote repository or retention
policy. The explicit `pg_switch_wal()` makes this short drill deterministic; real
archiving also needs a policy for low-traffic periods and monitoring.

## Next infrastructure exercise

For retained self-managed backups, explore
[pgBackRest](https://pgbackrest.org/user-guide.html): a dedicated backup identity,
full/differential/incremental backup schedules, continuous WAL archiving, protected
remote storage, retention that preserves recoverable WAL chains, and periodic
isolated restores. Monitor archive failures, repository capacity and backup age.
Do not independently delete WAL files by age; a retained base backup may need them.

Exercises: explain why the post-base-backup user proves replay; explain why
`pg_dump` cannot be the base for WAL recovery; identify the last recoverable point
if archiving stops; and distinguish manifest verification from a successful restore.
