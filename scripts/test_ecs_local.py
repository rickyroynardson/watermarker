"""Run one small batch through the running local ECS web/API/worker pipeline."""
import hashlib
import json
import os
import subprocess
import tempfile
import urllib.request
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]


def main():
    with urllib.request.urlopen("http://localhost:5180/api/ping", timeout=10) as response:
        assert json.load(response)["message"] == "pong", "Web-to-API proxy failed"
    # Validate injected worker auth without reading values into host logs or argv.
    containers = subprocess.check_output(["docker", "ps", "--format", "{{json .}}"], text=True)
    workers = [json.loads(line)["ID"] for line in containers.splitlines()
               if json.loads(line)["Image"].startswith("watermarker-worker:ecs-")]
    assert len(workers) == 1, "Expected one running local ECS worker"
    subprocess.run(["docker", "exec", workers[0], "python", "-c", """
import os, urllib.request, urllib.error
assert os.environ.get('WORKER_API_TOKEN'), 'Missing injected worker token'
assert 'DATABASE_URL' not in os.environ and 'REDIS_URL' not in os.environ, 'Worker must not receive database or Redis credentials'
request = urllib.request.Request(os.environ['WORKER_API_URL'] + '/internal/batches/invalid/cancellation',
    headers={'Authorization': 'Bearer ' + os.environ['WORKER_API_TOKEN']})
try:
    urllib.request.urlopen(request, timeout=10)
    raise AssertionError('Invalid batch ID unexpectedly accepted')
except urllib.error.HTTPError as error:
    assert error.code == 400, 'Injected worker token was rejected'
"""], check=True, cwd=ROOT)
    state = (ROOT / "infra/ecs-local/terraform.tfstate").read_text()
    assert "local-ecs-worker-token" not in state, "Token value leaked into current Terraform state"
    assert "postgres://watermarker:watermarker@" not in state, "Database credentials leaked into current Terraform state"
    token, owner, key_id = uuid.uuid4().hex, uuid.uuid4(), uuid.uuid4()
    digest = hashlib.sha256(token.encode()).hexdigest()
    # Generated UUIDs/digest only; no user input is interpolated into SQL.
    sql = (
        f"INSERT INTO users(id,name,quota_plan) VALUES('{owner}','Local ECS test','pro');"
        f"INSERT INTO api_keys(id,user_id,name,key_hash) "
        f"VALUES('{key_id}','{owner}','Local ECS test','{digest}');"
    )
    subprocess.run(
        ["docker", "compose", "exec", "-T", "postgres", "psql", "-U", "watermarker",
         "-d", "watermarker", "-v", "ON_ERROR_STOP=1"],
        input=sql, text=True, check=True, cwd=ROOT, capture_output=True,
    )
    try:
        with tempfile.TemporaryDirectory(prefix="watermarker-ecs-test-") as directory:
            output = Path(directory) / "result.json"
            subprocess.run(
                ["node", "scripts/load/run.mjs", "--api", "http://localhost:5180/api",
                 "--source", "scripts/load/fixtures/source.jpg",
                 "--watermark", "scripts/load/fixtures/watermark.png",
                 "--images", "2", "--workers", "1", "--runs", "1", "--timeout", "120",
                 "--output", str(output)],
                env={**os.environ, "WATERMARKER_API_KEY": token}, cwd=ROOT,
                check=True,
            )
            run = json.loads(output.read_text())["runs"][0]
            assert run["done"] == 2 and run["failed"] == 0, run
            print(f"Local ECS pipeline passed: batch {run['batch_id']}, 2 images done.")
    finally:
        subprocess.run(
            ["docker", "compose", "exec", "-T", "postgres", "psql", "-U", "watermarker",
             "-d", "watermarker", "-v", "ON_ERROR_STOP=1"],
            input=f"UPDATE api_keys SET revoked_at=now() WHERE id='{key_id}';",
            text=True, check=True, cwd=ROOT, capture_output=True,
        )


if __name__ == "__main__":
    main()
