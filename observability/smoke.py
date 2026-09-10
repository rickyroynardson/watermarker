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
