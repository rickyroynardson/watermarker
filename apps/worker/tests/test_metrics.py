import unittest
from concurrent.futures import ThreadPoolExecutor
from json import JSONDecodeError
from threading import Barrier, Event
from unittest.mock import patch

from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import Histogram, InMemoryMetricReader
from test_worker import message
from worker.consumer import Worker
from worker.telemetry import cpu_usage, peak_memory


class MetricsTest(unittest.TestCase):
    def test_slot_metrics_persist_and_reset_after_success_failure_and_shutdown(self):
        reader = InMemoryMetricReader()
        provider = MeterProvider(metric_readers=[reader])
        entered, release = Barrier(3), Event()

        def values():
            return {
                metric.name: metric.data.data_points[0].value
                for resource in reader.get_metrics_data().resource_metrics
                for scope in resource.scope_metrics
                for metric in scope.metrics
            }

        def process(msg):
            entered.wait(timeout=5)
            release.wait(5)
            if msg["ReceiptHandle"] == "failure":
                raise RuntimeError("transient failure")

        try:
            with patch(
                "worker.consumer.metrics.get_meter",
                return_value=provider.get_meter("test"),
            ):
                worker = Worker(
                    None,
                    unittest.mock.Mock(),
                    "bucket",
                    "jobs",
                    "results",
                    concurrency=2,
                    is_cancelled=lambda _: False,
                )
            idle = {
                "watermarker.worker.jobs.active": 0,
                "watermarker.worker.capacity": 2,
            }
            self.assertEqual(values(), idle)
            self.assertEqual(values(), idle, "idle gauges must persist across exports")
            worker.sqs.receive_message.side_effect = [
                {"Messages": [message()]},
                {"Messages": [message() | {"ReceiptHandle": "failure"}]},
            ]
            with (
                patch.object(worker, "process", side_effect=process),
                ThreadPoolExecutor(max_workers=2) as pool,
            ):
                futures = [pool.submit(worker.poll) for _ in range(2)]
                try:
                    entered.wait(timeout=5)
                    self.assertEqual(
                        values(),
                        {
                            "watermarker.worker.jobs.active": 2,
                            "watermarker.worker.capacity": 2,
                        },
                    )
                finally:
                    release.set()
                failures = 0
                for future in futures:
                    try:
                        future.result()
                    except RuntimeError:
                        failures += 1
                self.assertEqual(failures, 1)
            self.assertEqual(values(), idle)
            worker.stop.set()
            worker.sqs.receive_message.side_effect = None
            worker.sqs.receive_message.return_value = {"Messages": [message()]}
            worker.poll()
            self.assertEqual(
                values(), idle, "interrupted jobs must release the metric slot"
            )
        finally:
            release.set()
            provider.shutdown()

    def test_process_resource_observations(self):
        self.assertGreaterEqual(cpu_usage(None)[0].value, 0)
        self.assertGreater(peak_memory(None)[0].value, 0)

    def test_invalid_job_records_error_and_reraises(self):
        reader = InMemoryMetricReader()
        provider = MeterProvider(metric_readers=[reader])
        histogram = provider.get_meter("test").create_histogram("duration", unit="s")
        try:
            with (
                patch("worker.consumer.job_duration", histogram),
                self.assertRaises(JSONDecodeError),
            ):
                Worker(
                    None,
                    None,
                    "bucket",
                    "jobs",
                    "results",
                    is_cancelled=lambda _: False,
                ).process({"Body": "invalid"})
            data = reader.get_metrics_data()
            assert data is not None
            measurement = data.resource_metrics[0].scope_metrics[0].metrics[0].data
            assert isinstance(measurement, Histogram)
            point = measurement.data_points[0]
            self.assertEqual(point.count, 1)
            self.assertEqual(point.attributes, {"outcome": "error"})
            self.assertGreaterEqual(point.sum, 0)
        finally:
            provider.shutdown()
