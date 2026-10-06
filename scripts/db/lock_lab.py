"""Demonstrate the real quota row lock with independent psql sessions."""
import re
import subprocess
import time


def run_lock_lab(psql, root):
    source = (root / 'apps/api/internal/quota/quota.go').read_text()
    match = re.search(r'tx.QueryRow\(ctx, `([^`]+FOR UPDATE OF u)`', source)
    if match is None:
        raise RuntimeError('Could not locate the application quota lock SQL')
    first = '00000000-0000-0000-0000-000000000001'
    second = '00000000-0000-0000-0000-000000000002'
    prepare = 'PREPARE quota_lock(uuid) AS ' + match[1] + ';\n'

    def run(sql):
        return subprocess.run(psql + ['-Atq'], input=sql, text=True,
                              capture_output=True, check=True, timeout=10).stdout.strip()

    run(f"INSERT INTO users(id,name) VALUES ('{first}','lock lab A'),('{second}','lock lab B');")
    holder = subprocess.Popen(psql + ['-Atq'], stdin=subprocess.PIPE,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    waiter = None
    try:
        holder.stdin.write("SET application_name='quota-lab-holder';\n"
                           "SET statement_timeout='5s';\n"
                           "SET idle_in_transaction_session_timeout='20s';\n" + prepare +
                           f"BEGIN; EXECUTE quota_lock('{first}');\n\\echo LOCKED\n")
        holder.stdin.flush()
        for line in holder.stdout:
            if line.strip() == 'LOCKED':
                break
        else:
            raise RuntimeError('Holder failed to acquire the quota lock')

        # A normal MVCC read and another account's lock must work while A is held.
        run("SET statement_timeout='1s'; SELECT count(*) FROM users;\n" + prepare +
            f"BEGIN; EXECUTE quota_lock('{second}'); ROLLBACK;")
        print('PASS: ordinary reads and another user proceed while user A is locked', flush=True)

        waiter = subprocess.Popen(psql + ['-Atq', '-v', 'VERBOSITY=verbose'],
                                  stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                                  stderr=subprocess.PIPE, text=True)
        waiter.stdin.write("SET application_name='quota-lab-waiter';\n"
                           "SET statement_timeout='6s'; SET lock_timeout='4s';\n" + prepare +
                           f"BEGIN; EXECUTE quota_lock('{first}'); COMMIT;\n")
        waiter.stdin.flush()
        inspection = """
SELECT w.wait_event_type || ':' || w.wait_event || ' blocked by ' || h.application_name
FROM pg_stat_activity w JOIN pg_stat_activity h ON h.pid=ANY(pg_blocking_pids(w.pid))
WHERE w.application_name='quota-lab-waiter' AND h.application_name='quota-lab-holder'
  AND w.wait_event_type='Lock';
"""
        deadline = time.monotonic() + 3
        while time.monotonic() < deadline:
            blocking = run(inspection)
            if blocking:
                print('Observed: ' + blocking, flush=True)
                break
            time.sleep(0.05)
        else:
            raise RuntimeError('Did not observe the same-user lock wait')
        _, error = waiter.communicate(timeout=8)
        if waiter.returncode == 0 or '55P03' not in error:
            raise RuntimeError('Expected lock timeout SQLSTATE 55P03: ' + error)
        print('PASS: same-user reservation lock times out with SQLSTATE 55P03', flush=True)

        holder.communicate('ROLLBACK;\n\\q\n', timeout=5)
        if holder.returncode != 0:
            raise RuntimeError('Holder failed to roll back')
        run("SET statement_timeout='1s';\n" + prepare +
            f"BEGIN; EXECUTE quota_lock('{first}'); ROLLBACK;")
        print('PASS: rolling back the holder releases the lock for user A', flush=True)
    finally:
        # The outer runner also removes the container, including any server sessions.
        for process in (waiter, holder):
            if process is not None and process.poll() is None:
                process.kill()
                process.communicate(timeout=5)
