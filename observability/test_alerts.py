"""Check provisioned PromQL/thresholds with synthetic time series; requires Docker."""
import json
import subprocess
import tempfile
from pathlib import Path

rules = json.loads((Path(__file__).parent / 'grafana/provisioning/alerting/backlog.json').read_text())['groups'][0]['rules']
prom_rules = [{'alert': r['uid'], 'expr': '(' + r['data'][0]['model']['expr'] + ') > ' + str(r['data'][1]['model']['conditions'][0]['evaluator']['params'][0]), 'for': r['for']} for r in rules]
sources = ['jobs', 'results', 'jobs_dlq', 'results_dlq', 'outbox']

def series(source, metric, values, extra=''):
    return {'series': metric + '{service_name="watermarker-monitor",deployment_environment_name="development",instance="test",source="' + source + '"' + extra + '}', 'values': values}

def inputs(missing=(), failed=(), stale=False, backlog=None, age=0):
    result = []
    for source in sources:
        if source in missing:
            continue
        result += [series(source, 'watermarker_backlog_observation_success', '0+0x40' if source in failed else '1+0x40'), series(source, 'watermarker_backlog_observation_time_seconds', '0+0x40' if stale else '0+15x40')]
        if source != 'outbox':
            result.append(series(source, 'watermarker_queue_depth', (backlog or {}).get(source, '0+0x40'), ',state="visible"'))
        else:
            result.append(series(source, 'watermarker_outbox_oldest_age_seconds', f'{age}+0x40'))
    return result

def check(name, at, labels=None):
    return {'eval_time': at, 'alertname': name, 'exp_alerts': [] if labels is None else [{'exp_labels': label} for label in labels]}

tests = [
    {'name': 'healthy empty sources', 'input_series': inputs(), 'alert_rule_test': [check(r['uid'], '6m') for r in rules]},
    {'name': 'DLQ waits then fires', 'input_series': inputs(backlog={'jobs_dlq': '1+0x40'}), 'alert_rule_test': [check('watermarker-dlq', '1m'), check('watermarker-dlq', '3m', [{'source': 'jobs_dlq'}])]},
    {'name': 'queue backlog waits five minutes', 'input_series': inputs(backlog={'jobs': '2+0x40'}), 'alert_rule_test': [check('watermarker-backlog', '4m'), check('watermarker-backlog', '6m', [{'source': 'jobs'}])]},
    {'name': 'outbox old enough', 'input_series': inputs(age=121), 'alert_rule_test': [check('watermarker-outbox', '1m'), check('watermarker-outbox', '3m', [{}])]},
    {'name': 'all telemetry absent', 'input_series': [], 'alert_rule_test': [check('watermarker-monitor', '3m', [{}])]},
    {'name': 'one source missing', 'input_series': inputs(missing=['results_dlq']), 'alert_rule_test': [check('watermarker-monitor', '3m', [{}])]},
    {'name': 'read failed', 'input_series': inputs(failed=['jobs']), 'alert_rule_test': [check('watermarker-monitor', '3m', [{}])]},
    {'name': 'stale data is not healthy', 'input_series': inputs(stale=True, backlog={'jobs_dlq': '2+0x40'}), 'alert_rule_test': [check('watermarker-monitor', '4m', [{}]), check('watermarker-dlq', '4m')]},
    {'name': 'DLQ resolves after draining', 'input_series': inputs(backlog={'jobs_dlq': '1+0x12 0+0x28'}), 'alert_rule_test': [check('watermarker-dlq', '3m', [{'source': 'jobs_dlq'}]), check('watermarker-dlq', '4m')]},
]
with tempfile.TemporaryDirectory(prefix='watermarker-alerts-') as temp:
    root = Path(temp)
    (root / 'rules.json').write_text(json.dumps({'groups': [{'name': 'test', 'rules': prom_rules}]}))
    (root / 'tests.json').write_text(json.dumps({'rule_files': ['/tests/rules.json'], 'evaluation_interval': '20s', 'tests': [dict(t, interval='15s') for t in tests]}))
    subprocess.run(['docker', 'run', '--rm', '--entrypoint', '/bin/promtool', '-v', f'{root}:/tests:ro', 'prom/prometheus:latest', 'test', 'rules', '/tests/tests.json'], check=True)
