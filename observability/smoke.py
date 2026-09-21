"""Run against make observability-up; verify Collector -> Loki ingestion."""

import json
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

message = f"otel-smoke-{uuid.uuid4()}"
payload = {"resourceLogs": [{
    "resource": {"attributes": [{"key": "service.name", "value": {"stringValue": "watermarker-smoke"}}]},
    "scopeLogs": [{"scope": {"name": "smoke"}, "logRecords": [{
        "timeUnixNano": str(time.time_ns()), "severityNumber": 9,
        "severityText": "INFO", "body": {"stringValue": message},
    }]}],
}]}
request = urllib.request.Request("http://localhost:4318/v1/logs",
    data=json.dumps(payload).encode(), headers={"Content-Type": "application/json"})
with urllib.request.urlopen(request, timeout=5) as response:
    result = json.load(response)
    assert not result.get("partialSuccess", {}).get("rejectedLogRecords"), result
query = urllib.parse.urlencode({"query": '{service_name="watermarker-smoke"} |= "' + message + '"'})
for _ in range(30):
    with urllib.request.urlopen("http://localhost:3100/loki/api/v1/query_range?" + query, timeout=5) as response:
        result = json.load(response)
    if any(message == value[1] for stream in result["data"]["result"] for value in stream["values"]):
        print("PASS: Collector -> Loki; view Watermarker logs at http://localhost:3000/d/watermarker-logs")
        break
    time.sleep(1)
else:
    raise SystemExit("FAIL: log not found in Loki within 30 seconds")

instance = str(uuid.uuid4())
now = time.time_ns()
payload = {"resourceMetrics": [{
    "resource": {"attributes": [
        {"key": "service.name", "value": {"stringValue": "watermarker-smoke"}},
        {"key": "service.instance.id", "value": {"stringValue": instance}},
    ]},
    "scopeMetrics": [{"scope": {"name": "smoke"}, "metrics": [{
        "name": "watermarker.smoke.duration", "unit": "s",
        "histogram": {"aggregationTemporality": 2, "dataPoints": [{
            "startTimeUnixNano": str(now - 1000000000), "timeUnixNano": str(now),
            "count": "1", "sum": 0.5, "explicitBounds": [1], "bucketCounts": ["1", "0"],
        }]},
    }]}],
}]}
request = urllib.request.Request("http://localhost:4318/v1/metrics",
    data=json.dumps(payload).encode(), headers={"Content-Type": "application/json"})
with urllib.request.urlopen(request, timeout=5) as response:
    result = json.load(response)
    assert not result.get("partialSuccess", {}).get("rejectedDataPoints"), result
query = urllib.parse.urlencode({"query": 'watermarker_smoke_duration_seconds_count{instance="' + instance + '"}'})
for _ in range(30):
    with urllib.request.urlopen("http://localhost:9090/api/v1/query?" + query, timeout=5) as response:
        result = json.load(response)
    if result["data"]["result"]:
        assert float(result["data"]["result"][0]["value"][1]) == 1, result
        print("PASS: Collector -> Prometheus; view http://localhost:3000/d/watermarker-metrics")
        break
    time.sleep(1)
else:
    raise SystemExit("FAIL: metric not found in Prometheus within 30 seconds")

# A known trace ID lets us verify ingestion without waiting for search indexing.
trace_id, span_id = uuid.uuid4().hex, uuid.uuid4().hex[:16]
now = time.time_ns()
payload = {"resourceSpans": [{
    "resource": {"attributes": [
        {"key": "service.name", "value": {"stringValue": "watermarker-smoke"}},
    ]},
    "scopeSpans": [{"scope": {"name": "smoke"}, "spans": [{
        "traceId": trace_id, "spanId": span_id, "name": "smoke", "kind": 1,
        "startTimeUnixNano": str(now - 1000000), "endTimeUnixNano": str(now),
    }]}],
}]}
request = urllib.request.Request("http://localhost:4318/v1/traces",
    data=json.dumps(payload).encode(), headers={"Content-Type": "application/json"})
with urllib.request.urlopen(request, timeout=5) as response:
    result = json.load(response)
    assert not result.get("partialSuccess", {}).get("rejectedSpans"), result
for _ in range(30):
    try:
        with urllib.request.urlopen("http://localhost:3200/api/traces/" + trace_id, timeout=5) as response:
            result = json.load(response)
        assert any(scope.get("spans") for batch in result.get("batches", []) for scope in batch.get("scopeSpans", [])), result
        print(f"PASS: Collector -> Tempo; trace {trace_id} in Grafana Explore")
        break
    except urllib.error.HTTPError as exc:
        if exc.code != 404:
            raise
    time.sleep(1)
else:
    raise SystemExit("FAIL: trace not found in Tempo within 30 seconds")

for _ in range(30):
    try:
        with urllib.request.urlopen("http://localhost:16686/api/traces/" + trace_id, timeout=5) as response:
            result = json.load(response)
        if any(trace.get("traceID") == trace_id and trace.get("spans") for trace in result.get("data", [])):
            print(f"PASS: same trace in Jaeger at http://localhost:16686/trace/{trace_id}")
            break
    except urllib.error.HTTPError as exc:
        if exc.code != 404:
            raise
    time.sleep(1)
else:
    raise SystemExit("FAIL: trace not found in Jaeger within 30 seconds")
