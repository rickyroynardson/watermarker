"""Run against make observability-up; verify Collector -> Loki ingestion."""

import json
import time
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
