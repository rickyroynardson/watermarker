import logging
import os
import unittest
from http.server import BaseHTTPRequestHandler, HTTPServer
from threading import Thread
from unittest.mock import patch

from opentelemetry.proto.collector.logs.v1.logs_service_pb2 import (
    ExportLogsServiceRequest,
)
from worker.telemetry import configure_logging


class TelemetryTest(unittest.TestCase):
    def test_export_flushes_structured_logs_and_exceptions(self):
        requests = []

        class Receiver(BaseHTTPRequestHandler):
            def do_POST(self):
                request = ExportLogsServiceRequest()
                request.ParseFromString(
                    self.rfile.read(int(self.headers["Content-Length"]))
                )
                requests.append((self.path, request))
                self.send_response(200)
                self.send_header("Content-Type", "application/x-protobuf")
                self.end_headers()

            def log_message(self, *_):
                pass

        with HTTPServer(("127.0.0.1", 0), Receiver) as server:
            thread = Thread(target=server.serve_forever)
            thread.start()
            try:
                with (
                    patch.dict(
                        os.environ,
                        {
                            "OTEL_EXPORTER_OTLP_ENDPOINT": f"http://127.0.0.1:{server.server_port}",
                            "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": "",
                            "OTEL_EXPORTER_OTLP_LOGS_COMPRESSION": "none",
                            "OTEL_SERVICE_NAME": "test-worker",
                            "OTEL_RESOURCE_ATTRIBUTES": "deployment.environment.name=test",
                        },
                    ),
                    self.assertRaisesRegex(ValueError, "test failure"),
                    configure_logging(),
                ):
                    logging.getLogger("worker.consumer").warning(
                        "image processed", extra={"image_id": "image-123"}
                    )
                    raise ValueError("test failure")
            finally:
                server.shutdown()
                thread.join()
        self.assertEqual(len(requests), 1)
        self.assertEqual(requests[0][0], "/v1/logs")
        resource = requests[0][1].resource_logs[0]
        attrs = {a.key: a.value.string_value for a in resource.resource.attributes}
        self.assertEqual(attrs["service.name"], "test-worker")
        self.assertEqual(attrs["deployment.environment.name"], "test")
        records = [r for scope in resource.scope_logs for r in scope.log_records]
        self.assertEqual(len(records), 2)
        self.assertEqual(records[0].body.string_value, "image processed")
        self.assertIn("image_id", [a.key for a in records[0].attributes])
        self.assertIn("exception.stacktrace", [a.key for a in records[1].attributes])

    def test_disabled_does_not_install_handler(self):
        with (
            patch.dict(
                os.environ,
                {
                    "OTEL_EXPORTER_OTLP_ENDPOINT": "",
                    "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": "",
                },
            ),
            configure_logging(),
        ):
            self.assertFalse(
                any(
                    type(h).__name__ == "LoggingHandler"
                    for h in logging.getLogger().handlers
                )
            )
