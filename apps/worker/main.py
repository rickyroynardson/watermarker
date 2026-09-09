import argparse
import logging
import os
import signal
from threading import Event

import boto3
from botocore.config import Config
from worker.consumer import Worker

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
    args = parser.parse_args()
    logging.basicConfig(
        level=logging.INFO, format="%(asctime)s %(levelname)s %(message)s"
    )
    required = ("S3_BUCKET", "SQS_JOBS_QUEUE_URL", "SQS_RESULTS_QUEUE_URL")
    for name in required:
        if not os.environ.get(name):
            parser.error(f"{name} is required")
    config = Config(
        connect_timeout=5,
        read_timeout=30,
        retries={"mode": "standard", "total_max_attempts": 3},
    )
    session = boto3.Session(region_name=os.environ.get("AWS_REGION") or None)
    worker = Worker(
        session.client(
            "s3", config=config.merge(Config(s3={"addressing_style": "path"}))
        ),
        session.client("sqs", config=config),
        os.environ["S3_BUCKET"],
        os.environ["SQS_JOBS_QUEUE_URL"],
        os.environ["SQS_RESULTS_QUEUE_URL"],
    )
    stop = Event()
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, lambda *_: stop.set())
    log.info("worker started")
    if args.once:
        worker.poll()
    else:
        worker.run(stop)


if __name__ == "__main__":
    main()
