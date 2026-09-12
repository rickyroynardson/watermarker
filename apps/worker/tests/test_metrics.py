import unittest
from json import JSONDecodeError
from unittest.mock import patch

from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import Histogram, InMemoryMetricReader
from worker.consumer import Worker


class MetricsTest(unittest.TestCase):
    def test_invalid_job_records_error_and_reraises(self):
        reader = InMemoryMetricReader()
        provider = MeterProvider(metric_readers=[reader])
        histogram = provider.get_meter("test").create_histogram("duration", unit="s")
        try:
            with (
                patch("worker.consumer.job_duration", histogram),
                self.assertRaises(JSONDecodeError),
            ):
                Worker(None, None, "bucket", "jobs", "results").process(
                    {"Body": "invalid"}
                )
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
