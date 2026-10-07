"""Check actual database activity panel expressions with synthetic Prometheus data."""
import json
import subprocess
import tempfile
from pathlib import Path

dashboard = json.loads((Path(__file__).parent / 'grafana/dashboards/metrics.json').read_text())
panels = {p['id']: p for p in dashboard['panels'] if p['id'] in (15, 16, 17)}
if len(panels) != 3:
    raise RuntimeError('Database activity panels not found')
metrics = [
    (15, 'watermarker_db_lock_sessions', ',state="blocked"', 2),
    (16, 'watermarker_db_transaction_idle', '', 1),
    (17, 'watermarker_db_transaction_oldest_age_seconds', '', 30),
]


def inputs(success=1, stale=False, zero=False):
    labels = 'service_name="watermarker-monitor",deployment_environment_name="development",instance="test",source="database"'
    result = [
        {'series': 'watermarker_backlog_observation_success{' + labels + '}', 'values': f'{success}+0x40'},
        {'series': 'watermarker_backlog_observation_time_seconds{' + labels + '}', 'values': '0+0x40' if stale else '0+15x40'},
    ]
    for _, name, state, value in metrics:
        result.append({'series': name + '{' + labels + state + '}', 'values': f'{0 if zero else value}+0x40'})
    return result


tests = []
for name, series, valid, zero in [
    ('fresh populated observations', inputs(), True, False),
    ('fresh healthy zeroes', inputs(zero=True), True, True),
    ('failed read hides previous values', inputs(success=0), False, False),
    ('stopped monitor hides stale values', inputs(stale=True), False, False),
    ('missing monitor stays empty', [], False, False),
]:
    expressions = []
    for id, _, state, value in metrics:
        expected = []
        if valid:
            expected = [{'labels': '{deployment_environment_name="development"' + state + '}',
                         'value': 0 if zero else value}]
        expressions.append({'expr': panels[id]['targets'][0]['expr'],
                            'eval_time': '3m', 'exp_samples': expected})
    tests.append({'name': name, 'interval': '15s', 'input_series': series,
                  'promql_expr_test': expressions})

with tempfile.TemporaryDirectory(prefix='watermarker-db-panels-') as temp:
    path = Path(temp)
    (path / 'tests.json').write_text(json.dumps({'evaluation_interval': '15s', 'tests': tests}))
    subprocess.run(['docker', 'run', '--rm', '--network', 'none', '--entrypoint', '/bin/promtool',
                    '-v', f'{path}:/tests:ro', 'prom/prometheus:latest',
                    'test', 'rules', '/tests/tests.json'], check=True)
