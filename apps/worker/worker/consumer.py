"""SQS job processing with retry-safe S3 output and result-before-ack ordering."""

import json
import logging
from contextlib import contextmanager
from functools import lru_cache
from random import randint
from threading import Event, Thread
from time import perf_counter

from botocore.exceptions import ClientError
from opentelemetry import metrics, trace
from opentelemetry.context import Context
from opentelemetry.trace.propagation.tracecontext import TraceContextTextMapPropagator

from .cancellation import CancellationUnavailable
from .messages import Job
from .watermark import MAX_BYTES, InvalidImage, composite

log = logging.getLogger(__name__)
tracer = trace.get_tracer("watermarker")
propagator = TraceContextTextMapPropagator()
VISIBILITY_SECONDS = 120
HEARTBEAT_SECONDS = 40

cancellation_blocked = metrics.get_meter("watermarker").create_gauge("watermarker.worker.cancellation.blocked")

redeliveries = metrics.get_meter("watermarker").create_counter("watermarker.job.redeliveries")

job_duration = metrics.get_meter("watermarker").create_histogram(
    "watermarker.job.duration",
    unit="s",
    explicit_bucket_boundaries_advisory=(0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60, 120),
)


class Worker:
    def __init__(self, s3, sqs, bucket: str, jobs_url: str, results_url: str, *, is_cancelled, stop=None):
        self.s3, self.sqs = s3, sqs
        self.bucket, self.jobs_url, self.results_url = bucket, jobs_url, results_url
        self.is_cancelled = is_cancelled
        self.stop = stop if stop is not None else Event()
        cancellation_blocked.set(0)
        # Cache immutable encoded watermark bytes, not mutable Pillow images (<=40 MiB).
        self.watermark = lru_cache(maxsize=4)(self.download)

    def download(self, key: str) -> bytes:
        try:
            response = self.s3.get_object(Bucket=self.bucket, Key=key)
        except ClientError as exc:
            if exc.response["Error"]["Code"] in ("NoSuchKey", "NotFound", "404"):
                raise InvalidImage("source or watermark object was not found") from exc
            raise
        with response["Body"] as stream:
            if response["ContentLength"] > MAX_BYTES:
                raise InvalidImage("image exceeds 10 MiB")
            data = stream.read(MAX_BYTES + 1)
        if len(data) > MAX_BYTES:
            raise InvalidImage("image exceeds 10 MiB")
        return data

    def output_exists(self, key: str) -> bool:
        try:
            self.s3.head_object(Bucket=self.bucket, Key=key)
            return True
        except ClientError as exc:
            if exc.response["Error"]["Code"] in ("NoSuchKey", "NotFound", "404"):
                return False
            raise

    def process(self, message: dict) -> None:
        # Optional telemetry never changes job validation or retry behavior.
        try:
            carrier = json.loads(message["Body"]).get("trace_context", {})
        except (ValueError, AttributeError):
            carrier = {}
        if not isinstance(carrier, dict) or not all(
            isinstance(v, str) for v in carrier.values()
        ):
            carrier = {}
        with tracer.start_as_current_span(
            "process_job",
            context=propagator.extract(carrier, context=Context()),
            kind=trace.SpanKind.CONSUMER,
            record_exception=False,
            set_status_on_exception=False,
        ) as span:
            try:
                self._process(message)
            except InterruptedError:
                raise
            except Exception:
                span.set_status(trace.StatusCode.ERROR, "job attempt failed")
                log.exception("job attempt failed")
                raise

    def _process(self, message: dict) -> None:
        start = perf_counter()
        outcome = "error"
        try:
            job = Job.parse(message["Body"])
            span = trace.get_current_span()
            span.set_attributes({"image.id": job.image_id, "batch.id": job.batch_id})
            if self.skip_cancelled(job, message):
                outcome = "cancelled"
                return
            result = job.result()
            if not self.output_exists(job.output_key):
                try:
                    with tracer.start_as_current_span(
                        "watermark",
                        record_exception=False,
                        set_status_on_exception=False,
                    ):
                        output = composite(
                            self.download(job.source_key),
                            self.watermark(job.watermark_key),
                        )
                except InvalidImage as exc:
                    result = job.result(str(exc))
                    span.set_status(trace.StatusCode.ERROR, "invalid image")
                else:
                    if self.skip_cancelled(job, message):
                        outcome = "cancelled"
                        return
                    try:
                        self.s3.put_object(
                            Bucket=self.bucket,
                            Key=job.output_key,
                            Body=output,
                            ContentType="image/png",
                            IfNoneMatch="*",
                        )
                    except ClientError as exc:
                        # Another delivery finished first. Other failures remain retryable.
                        if exc.response["Error"]["Code"] not in (
                            "PreconditionFailed",
                            "412",
                        ):
                            raise
            with tracer.start_as_current_span(
                "publish_result",
                kind=trace.SpanKind.PRODUCER,
                record_exception=False,
                set_status_on_exception=False,
            ):
                carrier = {}
                propagator.inject(carrier)
                if carrier:
                    result["trace_context"] = carrier
                self.sqs.send_message(
                    QueueUrl=self.results_url, MessageBody=json.dumps(result)
                )
            self.sqs.delete_message(
                QueueUrl=self.jobs_url, ReceiptHandle=message["ReceiptHandle"]
            )
            outcome = result["status"]
            log.info(
                "image processed",
                extra={
                    "image_id": job.image_id,
                    "batch_id": job.batch_id,
                    "status": result["status"],
                },
            )
        finally:
            job_duration.record(perf_counter() - start, {"outcome": outcome})

    def skip_cancelled(self, job, message):
        retries = 0
        while not self.stop.is_set():
            try:
                cancelled = self.is_cancelled(job.batch_id)
                cancellation_blocked.set(0)
                break
            except CancellationUnavailable:
                cancellation_blocked.set(1)
                ceiling = min(30, 2 ** min(retries + 1, 5))
                delay = randint(max(1, ceiling // 2), ceiling)
                retries += 1
                log.warning("cancellation API unavailable; queue polling paused", extra={"retry_delay_seconds": delay})
                if self.stop.wait(delay):
                    raise InterruptedError("worker stopping during cancellation check")
            except Exception:
                cancellation_blocked.set(0)
                raise
        else:
            raise InterruptedError("worker stopping during cancellation check")
        if not cancelled:
            return False
        self.sqs.delete_message(QueueUrl=self.jobs_url, ReceiptHandle=message["ReceiptHandle"])
        log.info("cancelled job skipped", extra={"batch_id": job.batch_id, "image_id": job.image_id})
        return True

    @contextmanager
    def visibility_heartbeat(self, receipt: str):
        finished = Event()

        def extend():
            while not finished.wait(HEARTBEAT_SECONDS):
                try:
                    self.sqs.change_message_visibility(
                        QueueUrl=self.jobs_url,
                        ReceiptHandle=receipt,
                        VisibilityTimeout=VISIBILITY_SECONDS,
                    )
                except Exception:
                    log.exception(
                        "could not extend job visibility; duplicate delivery is possible"
                    )

        thread = Thread(target=extend, daemon=True)
        thread.start()
        try:
            yield
        finally:
            finished.set()
            thread.join()

    def poll(self) -> None:
        response = self.sqs.receive_message(
            QueueUrl=self.jobs_url,
            MaxNumberOfMessages=1,
            WaitTimeSeconds=20,
            VisibilityTimeout=VISIBILITY_SECONDS,
            MessageSystemAttributeNames=["ApproximateReceiveCount"],
        )
        for message in response.get("Messages", []):
            if int(message.get("Attributes", {}).get("ApproximateReceiveCount", "1")) > 1:
                redeliveries.add(1)
            try:
                with self.visibility_heartbeat(message["ReceiptHandle"]):
                    self.process(message)
            except InterruptedError:
                return  # Shutdown leaves the message unacknowledged.
            except Exception:
                # Stop the heartbeat before setting the retry delay so it cannot overwrite it.
                try:
                    received = max(
                        1,
                        int(
                            message.get("Attributes", {}).get(
                                "ApproximateReceiveCount", "1"
                            )
                        ),
                    )
                    ceiling = min(900, 120 * 2 ** min(received - 1, 3))
                    delay = randint(ceiling // 2, ceiling)
                    self.sqs.change_message_visibility(
                        QueueUrl=self.jobs_url,
                        ReceiptHandle=message["ReceiptHandle"],
                        VisibilityTimeout=delay,
                    )
                    log.warning(
                        "job retry delayed",
                        extra={"receive_count": received, "retry_delay_seconds": delay},
                    )
                except Exception:
                    # Leave the message unacknowledged under its existing visibility timeout.
                    log.exception("could not set job retry delay")
                raise

    def run(self, stop: Event) -> None:
        self.stop = stop
        while not stop.is_set():
            try:
                self.poll()
            except Exception:
                # Malformed jobs and transient AWS failures stay unacked for retry/DLQ.
                log.exception("job attempt failed")
                stop.wait(1)
