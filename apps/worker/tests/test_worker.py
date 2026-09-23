import json
import unittest
from io import BytesIO
from threading import Event
from unittest.mock import Mock, patch
from uuid import uuid4

from botocore.exceptions import ClientError
from PIL import Image, ImageCms
from worker.consumer import Worker
from worker.messages import Job
from worker.watermark import InvalidImage, composite, decode


def encoded(color="white", size=(100, 100), mode="RGBA", format="PNG", **kwargs):
    with Image.new(mode, size, color) as image:
        output = BytesIO()
        image.save(output, format=format, **kwargs)
        return output.getvalue()


def message():
    owner = str(uuid4())
    return {
        "Body": json.dumps(
            {
                "version": 1,
                "job_type": "composite",
                "batch_id": str(uuid4()),
                "image_id": str(uuid4()),
                "source_key": f"sources/{owner}/{uuid4()}",
                "watermark_key": f"sources/{owner}/{uuid4()}",
            }
        ),
        "ReceiptHandle": "receipt",
    }


def make_worker():
    s3, sqs = Mock(), Mock()
    objects = {}

    def head(**kwargs):
        if kwargs["Key"] not in objects:
            raise ClientError({"Error": {"Code": "404"}}, "HeadObject")
        return {}

    def download(**kwargs):
        data = encoded()
        return {"ContentLength": len(data), "Body": BytesIO(data)}

    def put(**kwargs):
        objects[kwargs["Key"]] = kwargs["Body"]

    s3.head_object.side_effect = head
    s3.get_object.side_effect = download
    s3.put_object.side_effect = put
    return Worker(s3, sqs, "bucket", "jobs", "results", is_cancelled=lambda _: False)


class WorkerTests(unittest.TestCase):
    def test_cancelled_jobs_skip_storage_and_results(self):
        worker = make_worker()
        worker.is_cancelled = Mock(return_value=True)
        worker.process(message())
        worker.s3.head_object.assert_not_called()
        worker.sqs.send_message.assert_not_called()
        worker.sqs.delete_message.assert_called_once()
        worker = make_worker()
        worker.is_cancelled = Mock(side_effect=[False, True])
        worker.process(message())
        worker.s3.put_object.assert_not_called()
        worker.sqs.send_message.assert_not_called()
        worker.sqs.delete_message.assert_called_once()
        worker = make_worker()
        worker.is_cancelled = Mock(side_effect=RuntimeError("API unavailable"))
        with self.assertRaisesRegex(RuntimeError, "API unavailable"):
            worker.process(message())
        worker.s3.head_object.assert_not_called()
        worker.sqs.delete_message.assert_not_called()

    def test_poll_backoff_and_failed_visibility_update(self):
        for received, bounds in (
            (1, (60, 120)),
            (2, (120, 240)),
            (3, (240, 480)),
            (99, (450, 900)),
        ):
            worker = make_worker()
            msg = message()
            msg["Attributes"] = {"ApproximateReceiveCount": str(received)}
            worker.sqs.receive_message.return_value = {"Messages": [msg]}
            failure = RuntimeError("S3 unavailable")
            with (
                patch.object(worker, "process", side_effect=failure),
                patch("worker.consumer.randint", return_value=bounds[0]) as jitter,
            ):
                with self.assertRaisesRegex(RuntimeError, "S3 unavailable"):
                    worker.poll()
                jitter.assert_called_once_with(*bounds)
            worker.sqs.change_message_visibility.assert_called_once_with(
                QueueUrl="jobs", ReceiptHandle="receipt", VisibilityTimeout=bounds[0]
            )
            worker.sqs.delete_message.assert_not_called()
            self.assertEqual(
                worker.sqs.receive_message.call_args.kwargs[
                    "MessageSystemAttributeNames"
                ],
                ["ApproximateReceiveCount"],
            )
        worker.sqs.change_message_visibility.side_effect = RuntimeError(
            "SQS unavailable"
        )
        with (
            patch.object(worker, "process", side_effect=failure),
            self.assertRaisesRegex(RuntimeError, "S3 unavailable"),
        ):
            worker.poll()
        worker.sqs.delete_message.assert_not_called()
        worker = make_worker()
        worker.sqs.receive_message.return_value = {"Messages": [message()]}
        worker.poll()
        worker.sqs.delete_message.assert_called_once()
        worker.sqs.change_message_visibility.assert_not_called()

    def test_retry_attempt_roundtrip_and_validation(self):
        data = json.loads(message()["Body"])
        for attempt in (0, 1, 8):
            data["attempt"] = attempt
            job = Job.parse(json.dumps(data))
            self.assertEqual(job.result().get("attempt", 0), attempt)
            self.assertEqual(job.result("invalid image").get("attempt", 0), attempt)
        for attempt in (-1, True, "1", None, 2147483648):
            data["attempt"] = attempt
            with self.assertRaises(ValueError):
                Job.parse(json.dumps(data))

    def test_composite_preserves_alpha_and_places_watermark(self):
        output = composite(encoded(), encoded((255, 0, 0, 128), (4, 4)))
        with Image.open(BytesIO(output)) as image:
            self.assertEqual(image.format, "PNG")
            self.assertEqual(image.size, (100, 100))
            self.assertEqual(image.getpixel((0, 0)), (255, 255, 255, 255))
            self.assertEqual(image.getpixel((95, 95)), (255, 178, 178, 255))
        with decode(composite(encoded((0, 0, 0, 0)), encoded("red", (4, 4)))) as image:
            self.assertEqual(image.getpixel((0, 0))[3], 0)

    def test_formats_orientation_and_color_profile(self):
        exif = Image.Exif()
        exif[274] = 6
        with decode(
            encoded(size=(40, 20), mode="RGB", format="JPEG", exif=exif)
        ) as image:
            self.assertEqual(image.size, (20, 40))
        profile = ImageCms.ImageCmsProfile(ImageCms.createProfile("sRGB")).tobytes()
        for mode, format, options in [
            ("CMYK", "JPEG", {}),
            ("RGB", "WEBP", {}),
            ("RGB", "PNG", {"icc_profile": profile}),
            ("L", "PNG", {}),
        ]:
            with (
                self.subTest(mode=mode, format=format),
                decode(encoded(mode=mode, format=format, **options)) as image,
            ):
                self.assertEqual(image.mode, "RGBA")

    def test_invalid_images_and_limits(self):
        for data in (b"not an image", encoded(format="GIF"), encoded()[:40]):
            with self.subTest(data=data[:10]), self.assertRaises(InvalidImage):
                decode(data)
        with patch("worker.watermark.MAX_PIXELS", 1), self.assertRaises(InvalidImage):
            decode(encoded())
        with patch("worker.watermark.MAX_BYTES", 1), self.assertRaises(InvalidImage):
            decode(encoded())
        animated = BytesIO()
        with (
            Image.new("RGBA", (10, 10), "red") as first,
            Image.new("RGBA", (10, 10), "blue") as second,
        ):
            first.save(
                animated,
                format="WEBP",
                save_all=True,
                append_images=[second],
                duration=100,
            )
        with self.assertRaises(InvalidImage):
            decode(animated.getvalue())

    def test_contract_validation(self):
        valid = json.loads(message()["Body"])
        self.assertEqual(Job.parse(json.dumps(valid)).image_id, valid["image_id"])
        for field, value in (
            ("version", True),
            ("version", 2),
            ("job_type", "unknown"),
            ("image_id", "bad"),
            ("batch_id", None),
            ("source_key", "sources/../secret"),
            ("watermark_key", f"sources/{uuid4()}/{uuid4()}"),
        ):
            with self.subTest(field=field, value=value), self.assertRaises(ValueError):
                Job.parse(json.dumps(valid | {field: value}))

    def test_result_publish_failure_and_redelivery(self):
        worker, msg = make_worker(), message()
        worker.sqs.send_message.side_effect = RuntimeError("results queue unavailable")
        with self.assertRaises(RuntimeError):
            worker.process(msg)
        worker.sqs.delete_message.assert_not_called()
        worker.sqs.send_message.side_effect = None
        worker.process(msg)
        worker.s3.put_object.assert_called_once()
        self.assertEqual(
            worker.s3.get_object.call_count, 2, "redelivery reuses the saved output"
        )
        self.assertEqual(worker.s3.put_object.call_args.kwargs["IfNoneMatch"], "*")
        result = json.loads(worker.sqs.send_message.call_args.kwargs["MessageBody"])
        self.assertEqual(result, Job.parse(msg["Body"]).result())
        self.assertEqual(
            [c[0] for c in worker.sqs.mock_calls],
            ["send_message", "send_message", "delete_message"],
        )

    def test_permanent_and_transient_failures(self):
        worker = make_worker()
        worker.s3.get_object.side_effect = lambda **_: {
            "Body": BytesIO(b"bad"),
            "ContentLength": 3,
        }
        worker.process(message())
        result = json.loads(worker.sqs.send_message.call_args.kwargs["MessageBody"])
        self.assertEqual(result["status"], "failed")
        self.assertNotIn("output_key", result)
        worker.s3.put_object.assert_not_called()
        worker.sqs.delete_message.assert_called_once()
        for operation in ("head_object", "get_object", "put_object"):
            with self.subTest(operation=operation):
                worker = make_worker()
                getattr(worker.s3, operation).side_effect = ClientError(
                    {"Error": {"Code": "SlowDown"}}, operation
                )
                with self.assertRaises(ClientError):
                    worker.process(message())
                worker.sqs.send_message.assert_not_called()
                worker.sqs.delete_message.assert_not_called()

    def test_conditional_write_loser_still_publishes_success(self):
        worker = make_worker()
        worker.s3.put_object.side_effect = ClientError(
            {"Error": {"Code": "PreconditionFailed"}}, "PutObject"
        )
        worker.process(message())
        self.assertEqual(
            json.loads(worker.sqs.send_message.call_args.kwargs["MessageBody"])[
                "status"
            ],
            "done",
        )
        worker.sqs.delete_message.assert_called_once()

    def test_watermark_cache_and_visibility_heartbeat(self):
        worker, msg = make_worker(), message()
        worker.process(msg)
        data = json.loads(msg["Body"])
        data["image_id"] = str(uuid4())
        worker.process(msg | {"Body": json.dumps(data)})
        self.assertEqual(
            worker.s3.get_object.call_count,
            3,
            "watermark downloaded once for two images",
        )
        extended = Event()
        worker.sqs.change_message_visibility.side_effect = lambda **_: extended.set()
        with (
            patch("worker.consumer.HEARTBEAT_SECONDS", 0.01),
            worker.visibility_heartbeat("receipt"),
        ):
            self.assertTrue(extended.wait(1))
        self.assertEqual(
            worker.sqs.change_message_visibility.call_args.kwargs["VisibilityTimeout"],
            120,
        )


if __name__ == "__main__":
    unittest.main()
