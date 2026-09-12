"""Console logging and optional OTLP/HTTP logs and metrics export."""

import logging
import os
from contextlib import contextmanager
from uuid import uuid4

from opentelemetry import metrics
from opentelemetry.exporter.otlp.proto.http._log_exporter import OTLPLogExporter
from opentelemetry.exporter.otlp.proto.http.metric_exporter import OTLPMetricExporter
from opentelemetry.instrumentation.logging.handler import LoggingHandler
from opentelemetry.sdk._logs import LoggerProvider
from opentelemetry.sdk._logs.export import BatchLogRecordProcessor
from opentelemetry.sdk.metrics import MeterProvider
from opentelemetry.sdk.metrics.export import PeriodicExportingMetricReader
from opentelemetry.sdk.resources import Resource


@contextmanager
def configure_logging():
    logging.basicConfig(
        level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s"
    )
    provider = handler = None
    root = logging.getLogger()
    if os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT") or os.getenv(
        "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"
    ):
        provider = LoggerProvider(
            resource=Resource.create(
                {
                    "service.name": os.getenv("OTEL_SERVICE_NAME")
                    or "watermarker-worker",
                }
            )
        )
        endpoint = os.getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT") or (
            os.environ["OTEL_EXPORTER_OTLP_ENDPOINT"].rstrip("/") + "/v1/logs"
        )
        provider.add_log_record_processor(
            BatchLogRecordProcessor(OTLPLogExporter(endpoint=endpoint, timeout=5))
        )
        handler = LoggingHandler(level=logging.INFO, logger_provider=provider)
        # Exporter diagnostics must stay on the console to avoid recursive export failures.
        handler.addFilter(lambda record: not record.name.startswith("opentelemetry."))
        root.addHandler(handler)
    try:
        yield
    except Exception:
        logging.getLogger(__name__).exception("worker terminated")
        raise
    finally:
        if handler is not None:
            root.removeHandler(handler)
        if provider is not None:
            provider.shutdown()


@contextmanager
def configure_metrics():
    provider = None
    if os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT") or os.getenv(
        "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT"
    ):
        provider = MeterProvider(
            resource=Resource.create(
                {
                    "service.name": os.getenv("OTEL_SERVICE_NAME")
                    or "watermarker-worker",
                    "service.instance.id": str(uuid4()),
                }
            ),
            metric_readers=[
                PeriodicExportingMetricReader(OTLPMetricExporter(timeout=5))
            ],
        )
        metrics.set_meter_provider(provider)
    try:
        yield
    finally:
        if provider is not None:
            provider.shutdown(timeout_millis=5000)
