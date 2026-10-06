#!/usr/bin/env python3
"""Run query learning labs in disposable PostgreSQL using Docker's active context."""
import argparse
import pathlib
import re
import subprocess
import time
import uuid

from lock_lab import run_lock_lab

ROOT = pathlib.Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('scenario', choices=['batch-history', 'quota-usage', 'cleanup-scheduling', 'quota-locks'], nargs='?', default='batch-history')
    scenario = parser.parse_args().scenario
    name = 'watermarker-query-lab-' + uuid.uuid4().hex[:12]
    # No host ports, existing database URL, app services, or persistent volume.
    docker = ['docker']
    subprocess.run(docker + ['info'], check=True, stdout=subprocess.DEVNULL)
    try:
        subprocess.run(docker + [
            'run', '-d', '--name', name, '--memory', '512m',
            '--tmpfs', '/var/lib/postgresql',
            '-e', 'POSTGRES_USER=querylab', '-e', 'POSTGRES_PASSWORD=querylab',
            '-e', 'POSTGRES_DB=querylab', 'postgres:18-alpine',
        ], check=True, stdout=subprocess.DEVNULL)
        for _ in range(60):
            ready = subprocess.run(docker + [
                'exec', name, 'pg_isready', '-h', '127.0.0.1', '-U', 'querylab',
            ], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            if ready.returncode == 0:
                break
            time.sleep(1)
        else:
            raise RuntimeError('Disposable PostgreSQL did not become ready')
        # Same Up-only migration loading used by the API integration test.
        migrations = sorted((ROOT / 'apps/api/migrations').glob('*.sql'))
        if not migrations:
            raise RuntimeError('No application migrations found')
        sql = '\n'.join(p.read_text().split('-- +goose Down', 1)[0] for p in migrations)
        if scenario == 'quota-locks':
            psql = docker + ['exec', '-i', name, 'psql', '-X', '-h', '127.0.0.1',
                             '-U', 'querylab', '-d', 'querylab', '-v', 'ON_ERROR_STOP=1']
            subprocess.run(psql, input=sql, text=True, check=True, stdout=subprocess.DEVNULL)
            run_lock_lab(psql, ROOT)
            return
        experiment = (ROOT / f'scripts/db/{scenario}.sql').read_text()
        if scenario == 'quota-usage':
            source = (ROOT / 'apps/api/internal/quota/quota.go').read_text()
            query = re.search(r'const usage = `([^`]+)`', source)
            if query is None:
                raise RuntimeError('Could not locate the application quota SQL')
            experiment = experiment.replace('-- APP_QUOTA_QUERY', 'PREPARE quota_usage(uuid) AS SELECT used FROM (' + query[1] + ') AS usage(used);')
            experiment = experiment.replace('-- APP_ACCOUNT_QUERY', 'PREPARE account_quota(uuid) AS SELECT u.quota_plan,p.included_bytes,u.addon_bytes,(' + query[1] + ') FROM users u JOIN quota_plans p ON p.name=u.quota_plan WHERE u.id=$1;')
        if scenario == 'cleanup-scheduling':
            source = (ROOT / 'apps/api/internal/cleanup/apply.go').read_text()
            expiry = re.search(r'rows, err := tx.Query\(ctx, `([^`]+)`', source)
            deletion = re.search(r'err = tx.QueryRow\(ctx, `([^`]+)`', source)
            if expiry is None or deletion is None:
                raise RuntimeError('Could not locate the application cleanup SQL')
            experiment = experiment.replace('-- APP_EXPIRY_QUERY', 'PREPARE expiry_candidates(timestamptz, int) AS ' + expiry[1] + ';')
            experiment = experiment.replace('-- APP_DELETION_QUERY', 'PREPARE deletion_candidate AS ' + deletion[1] + ';')
            index_migration = (ROOT / 'apps/api/migrations/20261006000000_add_cleanup_selection_indexes.sql').read_text()
            up, down = index_migration.split('-- +goose Down', 1)
            experiment = experiment.replace('-- INDEX_MIGRATION_DOWN', down)
            experiment = experiment.replace('-- INDEX_MIGRATION_UP', up)
        sql += '\n' + experiment
        subprocess.run(docker + [
            'exec', '-i', name, 'psql', '-X', '-h', '127.0.0.1',
            '-U', 'querylab', '-d', 'querylab', '-v', 'ON_ERROR_STOP=1',
        ], input=sql, text=True, check=True)
    finally:
        subprocess.run(docker + ['rm', '-f', '-v', name],
                       stdout=subprocess.DEVNULL, check=False)


if __name__ == '__main__':
    main()
