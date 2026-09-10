"""Console logging with optional batched OTLP/HTTP export."""

import logging
import os
from contextlib import contextmanager

from opentelemetry.exporter.otlp.proto.http._log_exporter import OTLPLogExporter
from opentelemetry.instrumentation.logging.handler import LoggingHandler
from opentelemetry.sdk._logs import LoggerProvider
from opentelemetry.sdk._logs.export import BatchLogRecordProcessor
from opentelemetry.sdk.resources import Resource


@contextmanager
def configure_logging():
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s")
    provider = handler = None
    root = logging.getLogger()
    if os.getenv("OTEL_EXPORTER_OTLP_ENDPOINT") or os.getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"):
        provider = LoggerProvider(resource=Resource.create({
            "service.name": os.getenv("OTEL_SERVICE_NAME") or "watermarker-worker",
        }))
        endpoint = os.getenv("OTEL_EXPORTER_OTLP_LOGS_ENDPOINT") or (
            os.environ["OTEL_EXPORTER_OTLP_ENDPOINT"].rstrip("/") + "/v1/logs"
        )
        provider.add_log_record_processor(BatchLogRecordProcessor(OTLPLogExporter(endpoint=endpoint, timeout=5)))
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
            provider.shutdown()
