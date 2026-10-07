"""Physical backup and archived-WAL recovery, using only disposable lab resources."""
import json
import subprocess
import time

from backup_lab import capture_snapshot


def prepare_pitr_repository(docker, name, volume, root):
    # Install the archive command before PostgreSQL starts; no missing-script retries.
    subprocess.run(docker + ['run', '--rm', '-i', '--name', name + '-init',
                             '--network', 'none', '--mount', 'type=volume,source=' + volume + ',target=/backup',
                             '--entrypoint', 'sh', 'postgres:18-alpine', '-ec',
                             'mkdir -p /backup/wal; cat > /backup/archive-wal.sh; '
                             'chown -R postgres:postgres /backup; chmod 700 /backup /backup/wal; '
                             'chmod 600 /backup/archive-wal.sh'],
                   input=(root / 'scripts/db/archive-wal.sh').read_text(), text=True,
                   check=True, timeout=30)


def run_pitr_lab(docker, primary, volume, root):
    recovered, missing = primary + '-recovered', primary + '-missing-wal'
    connection = ['-h', '127.0.0.1', '-U', 'querylab']
    target = 'watermarker_before_delete'

    def run(args, **options):
        return subprocess.run(docker + args, check=True, timeout=60, **options)

    def sql(container, database, statement):
        return run(['exec', '-i', container, 'psql', '-X', '-Atq'] + connection +
                   ['-d', database, '-v', 'ON_ERROR_STOP=1'], input=statement,
                   text=True, stdout=subprocess.PIPE).stdout.strip()

    def snapshot(container):
        return capture_snapshot(lambda database, statement: sql(container, database, statement), 'querylab')

    def start_recovery(container, directory, restore_command):
        print('Recovery container: ' + container, flush=True)
        run(['run', '-d', '--name', container, '--network', 'none', '--memory', '512m',
             '--mount', 'type=volume,source=' + volume + ',target=/backup',
             '--user', 'postgres', '--entrypoint', 'postgres', 'postgres:18-alpine',
             '-D', directory, '-c', 'archive_mode=off', '-c', 'restore_command=' + restore_command,
             '-c', 'recovery_target_name=' + target, '-c', 'recovery_target_timeline=current',
             '-c', 'recovery_target_action=promote'], stdout=subprocess.DEVNULL)

    try:
        sql(primary, 'querylab', (root / 'scripts/db/backup-recovery.sql').read_text())
        base_state = snapshot(primary)
        started = time.monotonic()
        run(['exec', '-u', 'postgres', '-e', 'PGPASSWORD=querylab', primary, 'pg_basebackup'] +
            connection + ['--pgdata=/backup/base', '--format=plain', '--wal-method=stream', '--checkpoint=fast'])
        run(['exec', '-u', 'postgres', primary, 'pg_verifybackup', '/backup/base'])
        backup_seconds = time.monotonic() - started
        print('PASS: physical base backup and manifest verified', flush=True)

        # This committed row must come from archived WAL, not the earlier base backup.
        sql(primary, 'querylab', "INSERT INTO users(id,name) VALUES ('00000000-0000-0000-0000-000000000003','After base backup, before target');")
        expected = snapshot(primary)
        if expected == base_state:
            raise RuntimeError('Expected a committed change after the base backup')
        point = json.loads(sql(primary, 'querylab', "SELECT json_build_object('lsn',pg_create_restore_point('" + target + "'),'observed_at',clock_timestamp());"))
        print('Named recovery target recorded: ' + target + ' at ' + point['lsn'], flush=True)

        sql(primary, 'querylab', """
BEGIN;
DELETE FROM batches WHERE id='00000000-0000-0000-0000-000000000102';
INSERT INTO users(id,name) VALUES ('00000000-0000-0000-0000-000000000004','After recovery target');
COMMIT;
""")
        if sql(primary, 'querylab', "SELECT count(*) FROM batches WHERE id='00000000-0000-0000-0000-000000000102';") != '0':
            raise RuntimeError('Synthetic deletion was not committed')
        segment = sql(primary, 'querylab', 'SELECT pg_walfile_name(pg_current_wal_insert_lsn());')
        sql(primary, 'querylab', 'SELECT pg_switch_wal();')
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            archived = subprocess.run(docker + ['exec', primary, 'test', '-s', '/backup/wal/' + segment],
                                      stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=5)
            if archived.returncode == 0:
                break
            time.sleep(0.2)
        else:
            raise RuntimeError('Required post-backup WAL segment was not archived')
        archive_stats = json.loads(sql(primary, 'querylab', "SELECT row_to_json(a) FROM (SELECT archived_count,failed_count,last_archived_wal FROM pg_stat_archiver) a;"))
        print('PASS: WAL containing the target and later deletion archived', flush=True)

        # Make two independent copies; never run PostgreSQL on the immutable base.
        run(['exec', '-u', 'postgres', primary, 'sh', '-ec',
             'cp -a /backup/base /backup/recovered; touch /backup/recovered/recovery.signal; '
             'cp -a /backup/base /backup/missing-wal; touch /backup/missing-wal/recovery.signal'])
        run(['stop', '--time', '10', primary], stdout=subprocess.DEVNULL)
        print('Primary stopped; both restores have networking disabled', flush=True)

        # Negative control: the base alone must not silently satisfy a later target.
        start_recovery(missing, '/backup/missing-wal', 'false')
        exit_code = run(['wait', missing], text=True, stdout=subprocess.PIPE).stdout.strip()
        logs = run(['logs', missing], text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT).stdout
        if exit_code == '0' or 'recovery ended before configured recovery target was reached' not in logs:
            raise RuntimeError('Missing archive did not fail at the unreached recovery target:\n' + logs)
        print('PASS: recovery without archived WAL fails instead of accepting an earlier state', flush=True)

        recovery_started = time.monotonic()
        start_recovery(recovered, '/backup/recovered', 'cp /backup/wal/%f %p')
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            state = subprocess.run(docker + ['exec', recovered, 'psql', '-X', '-Atq'] +
                                   connection + ['-d', 'querylab', '-c', 'SELECT NOT pg_is_in_recovery();'],
                                   text=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=5)
            if state.returncode == 0 and state.stdout.strip() == 't':
                break
            if run(['inspect', '--format', '{{.State.Running}}', recovered], text=True,
                   stdout=subprocess.PIPE).stdout.strip() != 'true':
                logs = run(['logs', recovered], text=True, stdout=subprocess.PIPE, stderr=subprocess.STDOUT).stdout
                raise RuntimeError('Recovery server exited:\n' + logs)
            time.sleep(0.2)
        else:
            raise RuntimeError('Recovery did not reach its target and promote within 30 seconds')
        actual = snapshot(recovered)
        if actual != expected:
            raise RuntimeError('Recovered table contents or checked schema differ from the target state')
        if sql(recovered, 'querylab', "SELECT count(*) FROM users WHERE id='00000000-0000-0000-0000-000000000003';") != '1':
            raise RuntimeError('Post-base-backup committed data was not recovered from WAL')
        if sql(recovered, 'querylab', "SELECT count(*) FROM users WHERE id='00000000-0000-0000-0000-000000000004';") != '0':
            raise RuntimeError('A write after the recovery target survived')
        recovery_seconds = time.monotonic() - recovery_started
        print('PASS: post-base-backup data recovered, later deletion undone, and later write excluded', flush=True)
        print(json.dumps({'target_name': target, 'target_lsn': point['lsn'],
                          'target_observed_at': point['observed_at'], 'archive_segment': segment,
                          'archive_stats': archive_stats, 'base_backup_and_verify_seconds': round(backup_seconds, 3),
                          'restore_and_verify_seconds': round(recovery_seconds, 3),
                          'restored_table_rows': actual[1]}, indent=2))
    finally:
        for container in (missing, recovered):
            subprocess.run(docker + ['rm', '-f', '-v', container], check=False,
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
