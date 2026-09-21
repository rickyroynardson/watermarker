import json
import unittest
from unittest.mock import patch

from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.sdk.trace.export.in_memory_span_exporter import InMemorySpanExporter
from opentelemetry.trace import StatusCode
from test_worker import make_worker, message
from worker import consumer


class TracingTests(unittest.TestCase):
    def test_worker_propagates_parent_to_result_and_retries(self):
        exporter = InMemorySpanExporter()
        provider = TracerProvider()
        provider.add_span_processor(SimpleSpanProcessor(exporter))
        parent = "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01"
        msg = message()
        data = json.loads(msg["Body"])
        data["trace_context"] = {"traceparent": parent}
        msg["Body"] = json.dumps(data)
        worker = make_worker()
        with patch.object(consumer, "tracer", provider.get_tracer("test")):
            worker.process(msg)
            worker.process(msg)
            worker.sqs.send_message.side_effect = RuntimeError("unavailable")
            with self.assertRaises(RuntimeError):
                worker.process(msg)
        spans = exporter.get_finished_spans()
        attempts = [s for s in spans if s.name == "process_job"]
        self.assertEqual(len(attempts), 3)
        self.assertEqual(len({s.context.span_id for s in attempts}), 3)
        for attempt in attempts:
            self.assertEqual(attempt.context.trace_id, int(parent.split("-")[1], 16))
            self.assertEqual(attempt.parent.span_id, int(parent.split("-")[2], 16))
        self.assertEqual(attempts[-1].status.status_code, StatusCode.ERROR)
        result = json.loads(worker.sqs.send_message.call_args.kwargs["MessageBody"])
        published = [s for s in spans if s.name == "publish_result"][-1]
        self.assertEqual(
            result["trace_context"]["traceparent"].split("-")[2],
            f"{published.context.span_id:016x}",
        )
        self.assertEqual(worker.sqs.delete_message.call_count, 2)
        provider.shutdown()
