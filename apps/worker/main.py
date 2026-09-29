import argparse
import logging
import os
import signal
from functools import partial
from threading import Event

import boto3
from botocore.config import Config
from worker.cancellation import is_cancelled
from worker.consumer import Worker
from worker.telemetry import configure_logging, configure_metrics, configure_tracing

log = logging.getLogger(__name__)


def main():
    parser = argparse.ArgumentParser(
        description="Process image watermark jobs from SQS"
    )
    parser.add_argument(
        "--once",
        action="store_true",
        help="poll once, then exit (failures exit nonzero)",
    )
    parser.add_argument(
        "--concurrency", type=int,
        default=os.environ.get("WORKER_CONCURRENCY", "1"),
        help="maximum simultaneous jobs (default: WORKER_CONCURRENCY or 1)",
    )
    args = parser.parse_args()
    if args.concurrency < 1:
        parser.error("concurrency must be at least 1")
    required = (
        "S3_BUCKET",
        "SQS_JOBS_QUEUE_URL",
        "SQS_RESULTS_QUEUE_URL",
        "WORKER_API_URL",
        "WORKER_API_TOKEN",
    )
    for name in required:
        if not os.environ.get(name):
            parser.error(f"{name} is required")
    config = Config(
        max_pool_connections=max(10, args.concurrency),
        connect_timeout=5,
        read_timeout=30,
        retries={"mode": "standard", "total_max_attempts": 3},
    )
    session = boto3.Session(region_name=os.environ.get("AWS_REGION") or None)
    stop = Event()
    worker = Worker(
        session.client(
            "s3", config=config.merge(Config(s3={"addressing_style": "path"}))
        ),
        session.client("sqs", config=config),
        os.environ["S3_BUCKET"],
        os.environ["SQS_JOBS_QUEUE_URL"],
        os.environ["SQS_RESULTS_QUEUE_URL"],
        stop=stop,
        concurrency=args.concurrency,
        is_cancelled=partial(
            is_cancelled, os.environ["WORKER_API_URL"], os.environ["WORKER_API_TOKEN"]
        ),
    )
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, lambda *_: stop.set())
    log.info("worker started", extra={"concurrency": args.concurrency})
    if args.once:
        worker.poll()
    else:
        worker.run(stop)


if __name__ == "__main__":
    with configure_logging(), configure_metrics(), configure_tracing():
        main()
