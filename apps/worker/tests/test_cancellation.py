import unittest
from io import BytesIO
from threading import Event
from unittest.mock import Mock, patch
from urllib.error import HTTPError, URLError

from test_worker import make_worker, message
from worker.cancellation import CancellationUnavailable, is_cancelled


class CancellationTests(unittest.TestCase):
    def test_dependency_errors_are_distinct_from_unknown_jobs(self):
        for code in (401, 403, 429, 500, 503, 404, 400):
            error = HTTPError("http://api", code, "test", {}, None)
            with (
                patch("worker.cancellation.urlopen", side_effect=error),
                self.assertRaises(
                    HTTPError if code in (400, 404) else CancellationUnavailable
                ),
            ):
                is_cancelled("http://api", "token", "batch")
        for body in (b"invalid", b"[]", b'{"cancelled": "false"}'):
            with (
                patch("worker.cancellation.urlopen", return_value=BytesIO(body)),
                self.assertRaises(CancellationUnavailable),
            ):
                is_cancelled("http://api", "token", "batch")
        with (
            patch("worker.cancellation.urlopen", side_effect=URLError("offline")),
            self.assertRaises(CancellationUnavailable),
        ):
            is_cancelled("http://api", "token", "batch")
        with patch(
            "worker.cancellation.urlopen", return_value=BytesIO(b'{"cancelled":false}')
        ):
            self.assertFalse(is_cancelled("http://api", "token", "batch"))

    def test_pause_preserves_receipt_heartbeat_and_resumes(self):
        worker = make_worker()
        worker.sqs.receive_message.return_value = {"Messages": [message()]}
        worker.is_cancelled = Mock(
            side_effect=[CancellationUnavailable(), False, False]
        )
        extended = Event()
        worker.sqs.change_message_visibility.side_effect = lambda **_: extended.set()

        def wait(_delay):
            self.assertTrue(
                extended.wait(2), "heartbeat must continue during API outage"
            )
            worker.sqs.receive_message.assert_called_once()
            worker.s3.head_object.assert_not_called()
            worker.sqs.delete_message.assert_not_called()
            return False

        with (
            patch.object(worker.stop, "wait", side_effect=wait),
            patch("worker.consumer.HEARTBEAT_SECONDS", 0.01),
            patch("worker.consumer.cancellation_blocked") as health,
        ):
            worker.poll()
        self.assertIn(unittest.mock.call(1), health.set.call_args_list)
        self.assertEqual(health.set.call_args_list[-1], unittest.mock.call(0))
        worker.sqs.receive_message.assert_called_once()
        worker.sqs.send_message.assert_called_once()
        worker.sqs.delete_message.assert_called_once()
        self.assertTrue(
            all(
                c.kwargs["VisibilityTimeout"] == 120
                for c in worker.sqs.change_message_visibility.call_args_list
            )
        )

    def test_second_check_waits_then_cancels_without_saving(self):
        worker = make_worker()
        worker.is_cancelled = Mock(side_effect=[False, CancellationUnavailable(), True])
        with patch.object(worker.stop, "wait", return_value=False):
            worker.process(message())
        worker.s3.put_object.assert_not_called()
        worker.sqs.send_message.assert_not_called()
        worker.sqs.delete_message.assert_called_once()

    def test_backoff_caps_and_unknown_job_clears_blocked_state(self):
        worker = make_worker()
        missing = HTTPError("http://api", 404, "missing", {}, None)
        worker.is_cancelled = Mock(
            side_effect=[CancellationUnavailable()] * 7 + [missing]
        )
        with (
            patch.object(worker.stop, "wait", return_value=False),
            patch("worker.consumer.randint", return_value=1) as jitter,
            patch("worker.consumer.cancellation_blocked") as health,
            self.assertRaises(HTTPError),
        ):
            worker.process(message())
        self.assertEqual(
            [c.args for c in jitter.call_args_list],
            [(1, 2), (2, 4), (4, 8), (8, 16), (15, 30), (15, 30), (15, 30)],
        )
        self.assertEqual(health.set.call_args_list[-1], unittest.mock.call(0))
        worker.sqs.delete_message.assert_not_called()

    def test_shutdown_interrupts_wait_without_ack_or_job_backoff(self):
        worker = make_worker()
        worker.sqs.receive_message.return_value = {"Messages": [message()]}
        worker.is_cancelled = Mock(side_effect=CancellationUnavailable())
        with patch.object(worker.stop, "wait", return_value=True):
            worker.poll()
        worker.sqs.delete_message.assert_not_called()
        worker.sqs.change_message_visibility.assert_not_called()
