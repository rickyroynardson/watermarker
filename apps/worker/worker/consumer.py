"""SQS job processing with retry-safe S3 output and result-before-ack ordering."""

import json
import logging
from contextlib import contextmanager
from functools import lru_cache
from threading import Event, Thread

from botocore.exceptions import ClientError

from .messages import Job
from .watermark import MAX_BYTES, InvalidImage, composite

log = logging.getLogger(__name__)
VISIBILITY_SECONDS = 120
HEARTBEAT_SECONDS = 40


class Worker:
    def __init__(self, s3, sqs, bucket: str, jobs_url: str, results_url: str):
        self.s3, self.sqs = s3, sqs
        self.bucket, self.jobs_url, self.results_url = bucket, jobs_url, results_url
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
        job = Job.parse(message["Body"])
        result = job.result()
        if not self.output_exists(job.output_key):
            try:
                output = composite(self.download(job.source_key), self.watermark(job.watermark_key))
            except InvalidImage as exc:
                result = job.result(str(exc))
            else:
                try:
                    self.s3.put_object(
                        Bucket=self.bucket, Key=job.output_key, Body=output,
                        ContentType="image/png", IfNoneMatch="*",
                    )
                except ClientError as exc:
                    # Another delivery finished first. Other failures remain retryable.
                    if exc.response["Error"]["Code"] not in ("PreconditionFailed", "412"):
                        raise
        self.sqs.send_message(QueueUrl=self.results_url, MessageBody=json.dumps(result))
        self.sqs.delete_message(QueueUrl=self.jobs_url, ReceiptHandle=message["ReceiptHandle"])
        log.info("image processed", extra={
            "image_id": job.image_id, "batch_id": job.batch_id, "status": result["status"],
        })

    @contextmanager
    def visibility_heartbeat(self, receipt: str):
        finished = Event()

        def extend():
            while not finished.wait(HEARTBEAT_SECONDS):
                try:
                    self.sqs.change_message_visibility(
                        QueueUrl=self.jobs_url, ReceiptHandle=receipt,
                        VisibilityTimeout=VISIBILITY_SECONDS,
                    )
                except Exception:
                    log.exception("could not extend job visibility; duplicate delivery is possible")

        thread = Thread(target=extend, daemon=True)
        thread.start()
        try:
            yield
        finally:
            finished.set()
            thread.join()

    def poll(self) -> None:
        response = self.sqs.receive_message(
            QueueUrl=self.jobs_url, MaxNumberOfMessages=1,
            WaitTimeSeconds=20, VisibilityTimeout=VISIBILITY_SECONDS,
        )
        for message in response.get("Messages", []):
            with self.visibility_heartbeat(message["ReceiptHandle"]):
                self.process(message)

    def run(self, stop: Event) -> None:
        while not stop.is_set():
            try:
                self.poll()
            except Exception:
                # Malformed jobs and transient AWS failures stay unacked for retry/DLQ.
                log.exception("job attempt failed")
                stop.wait(1)
