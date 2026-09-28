"""End-to-end smoke check using disposable Compose resources (needs free port 4566)."""

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
    project = f"watermarker-smoke-{os.getpid()}"
    env = {
        **os.environ,
        "LOCALSTACK_AUTH_TOKEN": "smoke-test",
        "CONTAINER_OTEL_ENDPOINT": "",
    }
    with tempfile.TemporaryDirectory(prefix="watermarker-smoke-") as directory:
        profile = Path(directory) / "host.env"
        profile.write_text(
            "AWS_PROFILE=host-only\nAWS_DEFAULT_PROFILE=host-only\nAWS_SESSION_TOKEN=host-session\n"
        )
        override = Path(directory) / "compose.yml"
        override.write_text(f"""services:
  postgres:
    ports: !reset []
  redis:
    ports: !reset []
  localstack:
    image: localstack/localstack:4.14.0
    ports: !override ['127.0.0.1:4566:4566']
    environment:
      LOCALSTACK_AUTH_TOKEN: ''
      PERSISTENCE: '0'
  api:
    ports: !reset []
    env_file: !override ['{profile}']
    environment:
      APP_ORIGIN: ''
      OIDC_ISSUER_URL: ''
      OIDC_CLIENT_ID: ''
      OIDC_CLIENT_SECRET: ''
  web:
    ports: !override ['127.0.0.1::8080']
""")
        compose = [
            "docker",
            "compose",
            "-p",
            project,
            "-f",
            str(ROOT / "docker-compose.yml"),
            "-f",
            str(ROOT / "docker-compose.app.yml"),
            "-f",
            str(override),
        ]

        def command(*args, **kwargs):
            return subprocess.run(
                compose + list(args), cwd=ROOT, env=env, check=True, **kwargs
            )

        try:
            command("up", "-d", "--no-build", "--wait", "--wait-timeout", "180")
            # Match the existing integration fixture: community LocalStack's conditional
            # CopyObject requires versioning to avoid its copy-before-precondition bug.
            command(
                "exec",
                "-T",
                "localstack",
                "awslocal",
                "s3api",
                "put-bucket-versioning",
                "--bucket",
                "watermarker",
                "--versioning-configuration",
                "Status=Enabled",
                capture_output=True,
            )
            address = command(
                "port", "web", "8080", capture_output=True, text=True
            ).stdout.strip()
            origin = "http://" + address
            with urllib.request.urlopen(origin + "/api/ping", timeout=10) as response:
                assert json.load(response)["message"] == "pong", "API proxy failed"
            with urllib.request.urlopen(
                origin + "/batches/example", timeout=10
            ) as response:
                assert b'<div id="root">' in response.read(), "SPA routing failed"
            token, owner, key_id = uuid.uuid4().hex, uuid.uuid4(), uuid.uuid4()
            digest = hashlib.sha256(token.encode()).hexdigest()
            sql = (
                f"INSERT INTO users(id,name) VALUES('{owner}','Container smoke');"
                f"INSERT INTO api_keys(id,user_id,name,key_hash) "
                f"VALUES('{key_id}','{owner}','Container smoke','{digest}');"
            )
            command(
                "exec",
                "-T",
                "postgres",
                "psql",
                "-U",
                "watermarker",
                "-d",
                "watermarker",
                "-v",
                "ON_ERROR_STOP=1",
                input=sql,
                text=True,
                capture_output=True,
            )
            output = Path(directory) / "result.json"
            subprocess.run(
                [
                    "node",
                    "scripts/load/run.mjs",
                    "--api",
                    origin + "/api",
                    "--source",
                    "scripts/load/fixtures/source.jpg",
                    "--watermark",
                    "scripts/load/fixtures/watermark.png",
                    "--images",
                    "1",
                    "--workers",
                    "1",
                    "--runs",
                    "1",
                    "--timeout",
                    "120",
                    "--output",
                    str(output),
                ],
                cwd=ROOT,
                env={**env, "WATERMARKER_API_KEY": token},
                check=True,
                capture_output=True,
            )
            result = json.loads(output.read_text())["runs"][0]
            assert result["done"] == 1 and result["failed"] == 0
            request = urllib.request.Request(
                origin + "/api/batches/" + result["batch_id"] + "/events",
                headers={"Authorization": "Bearer " + token},
            )
            with urllib.request.urlopen(request, timeout=10) as response:
                assert response.headers["Content-Type"].startswith("text/event-stream")
                while True:
                    line = response.readline()
                    assert line, "SSE closed before initial snapshot"
                    if line.startswith(b"data: "):
                        snapshot = json.loads(line[6:])
                        break
            assert snapshot["status"] == "done"
            with urllib.request.urlopen(
                snapshot["images"][0]["download_url"], timeout=10
            ) as response:
                assert response.read(8) == b"\x89PNG\r\n\x1a\n", (
                    "Signed download failed"
                )
            print(
                "Container smoke passed: migrations, proxy, upload, worker, result, SSE, signed download."
            )
        except Exception:
            command(
                "logs",
                "--no-color",
                "--tail",
                "30",
                "migrate",
                "api",
                "consumer",
                "worker",
                "web",
            )
            raise
        finally:
            command("down", "-v", "--remove-orphans")


if __name__ == "__main__":
    main()
