"""Restore drill for the existing disposable database runner; never accepts a real DB URL."""
import hashlib
import json
import pathlib
import subprocess
import tempfile
import time


def capture_snapshot(sql, database):
    # ponytail: tiny fixture tables fit in memory; stream fingerprints for large restore drills.
    tables = sql(database, "SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename;").splitlines()
    data, counts = {}, {}
    for table in tables:
        identifier = '"' + table.replace('"', '""') + '"'
        rows = sql(database, 'SELECT COALESCE(jsonb_agg(to_jsonb(t) ORDER BY to_jsonb(t)::text),\'[]\'::jsonb)::text FROM public.' + identifier + ' t;')
        data[table] = hashlib.sha256(rows.encode()).hexdigest()
        counts[table] = len(json.loads(rows))
    # Compare column definitions, checks/FKs/uniqueness, indexes and enum labels.
    # Ownership/ACLs are deliberately omitted from this portable local restore.
    schema = sql(database, """
SELECT jsonb_build_object(
 'columns', (SELECT jsonb_agg(to_jsonb(c) ORDER BY table_name,ordinal_position)
   FROM (SELECT table_name,column_name,ordinal_position,udt_schema,udt_name,is_nullable,column_default
     FROM information_schema.columns WHERE table_schema='public') c),
 'constraints', (SELECT jsonb_agg(to_jsonb(c) ORDER BY table_name,name)
   FROM (SELECT r.relname AS table_name,c.conname AS name,c.convalidated,
     pg_get_constraintdef(c.oid) AS definition FROM pg_constraint c
     JOIN pg_class r ON r.oid=c.conrelid JOIN pg_namespace n ON n.oid=r.relnamespace
     WHERE n.nspname='public') c),
 'indexes', (SELECT jsonb_agg(to_jsonb(i) ORDER BY tablename,indexname)
   FROM (SELECT tablename,indexname,indexdef FROM pg_indexes WHERE schemaname='public') i),
 'enums', (SELECT jsonb_agg(to_jsonb(e) ORDER BY name,enumsortorder)
   FROM (SELECT t.typname AS name,e.enumsortorder,e.enumlabel FROM pg_enum e
     JOIN pg_type t ON t.oid=e.enumtypid JOIN pg_namespace n ON n.oid=t.typnamespace
     WHERE n.nspname='public') e)
)::text;
""")
    return data, counts, hashlib.sha256(schema.encode()).hexdigest()


def run_backup_lab(docker, name, root):
    connection = ['-h', '127.0.0.1', '-U', 'querylab']

    def sql(database, statement):
        return subprocess.run(docker + ['exec', '-i', name, 'psql', '-X', '-Atq'] +
                              connection + ['-d', database, '-v', 'ON_ERROR_STOP=1'],
                              input=statement, text=True, stdout=subprocess.PIPE,
                              check=True, timeout=30).stdout.strip()

    sql('querylab', (root / 'scripts/db/backup-recovery.sql').read_text())
    expected = capture_snapshot(sql, 'querylab')
    with tempfile.TemporaryDirectory(prefix='watermarker-backup-lab-') as directory:
        archive = pathlib.Path(directory) / 'backup.dump'
        started = time.monotonic()
        with archive.open('wb') as output:
            subprocess.run(docker + ['exec', name, 'pg_dump'] + connection +
                           ['-d', 'querylab', '--format=custom', '--no-privileges'],
                           stdout=output, check=True, timeout=60)
        backup_seconds = time.monotonic() - started
        digest = hashlib.sha256(archive.read_bytes()).hexdigest()
        with archive.open('rb') as source:
            subprocess.run(docker + ['exec', '-i', name, 'pg_restore', '--list'],
                           stdin=source, stdout=subprocess.DEVNULL, check=True, timeout=30)
        print('PASS: custom archive created and its table of contents is readable', flush=True)

        # A committed change after the dump demonstrates the recovery point boundary.
        sql('querylab', "INSERT INTO users(id,name) VALUES ('00000000-0000-0000-0000-000000000003','After backup');")
        if int(sql('querylab', 'SELECT count(*) FROM users;')) != expected[1]['users'] + 1:
            raise RuntimeError('Post-backup write was not committed')
        subprocess.run(docker + ['exec', name, 'dropdb'] + connection + ['querylab'], check=True, timeout=30)
        print('Source database dropped inside disposable container only', flush=True)

        recovery_started = time.monotonic()
        subprocess.run(docker + ['exec', name, 'createdb'] + connection +
                       ['--template=template0', 'recovered'], check=True, timeout=30)
        if hashlib.sha256(archive.read_bytes()).hexdigest() != digest:
            raise RuntimeError('Archive checksum changed before restore')
        with archive.open('rb') as source:
            subprocess.run(docker + ['exec', '-i', name, 'pg_restore'] + connection +
                           ['-d', 'recovered', '--single-transaction', '--exit-on-error',
                            '--no-owner', '--no-privileges'], stdin=source, check=True, timeout=60)
        sql('recovered', 'ANALYZE;')
        actual = capture_snapshot(sql, 'recovered')
        if actual != expected:
            raise RuntimeError('Restored row contents, counts, or schema differ from the backup snapshot')
        if sql('recovered', "SELECT count(*) FROM users WHERE id='00000000-0000-0000-0000-000000000003';") != '0':
            raise RuntimeError('Post-backup write unexpectedly survived')
        recovery_seconds = time.monotonic() - recovery_started
        print('PASS: all public table contents, counts, and checked schema definitions restored', flush=True)
        print('PASS: committed post-backup write is absent, as expected', flush=True)
        print(json.dumps({'backup_bytes': archive.stat().st_size, 'archive_sha256': digest,
                          'backup_seconds': round(backup_seconds, 3),
                          'restore_and_verify_seconds': round(recovery_seconds, 3),
                          'restored_table_rows': actual[1]}, indent=2))
    # The host archive and both databases are disposable; the outer runner removes the container.
