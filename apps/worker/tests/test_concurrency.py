import unittest
from threading import Barrier, Event, Lock, Thread
from unittest.mock import patch

from test_worker import make_worker, message
from worker.cancellation import CancellationUnavailable
from worker.messages import Job


class ConcurrencyTests(unittest.TestCase):
    def test_bounded_polling_heartbeats_shutdown_and_retry(self):
        worker = make_worker()
        worker.concurrency = 3
        entered = Barrier(4)
        release = Event()
        heartbeats = Event()
        lock = Lock()
        receipts = set()
        received = []

        def receive(**kwargs):
            self.assertEqual(kwargs["MaxNumberOfMessages"], 1)
            with lock:
                msg = message()
                msg["ReceiptHandle"] = str(len(received))
                received.append(msg)
            return {"Messages": [msg]}

        def process(msg):
            entered.wait(timeout=5)
            if not release.wait(5):
                raise TimeoutError("processing was not released")
            if msg["ReceiptHandle"] == "0":
                raise RuntimeError("transient failure")
            worker.sqs.send_message(QueueUrl="results", MessageBody="result")
            worker.sqs.delete_message(QueueUrl="jobs", ReceiptHandle=msg["ReceiptHandle"])

        def visibility(**kwargs):
            if kwargs["VisibilityTimeout"] == 120:
                with lock:
                    receipts.add(kwargs["ReceiptHandle"])
                    if len(receipts) == 3:
                        heartbeats.set()

        worker.sqs.receive_message.side_effect = receive
        worker.sqs.change_message_visibility.side_effect = visibility
        runner = Thread(target=worker.run, args=(worker.stop,), daemon=True)
        with patch.object(worker, "process", side_effect=process), patch("worker.consumer.HEARTBEAT_SECONDS", 0.01), patch("worker.consumer.randint", return_value=60):
            try:
                runner.start()
                entered.wait(timeout=5)
                self.assertTrue(heartbeats.wait(5), "all concurrent receipts need heartbeats")
                self.assertEqual(len(received), 3, "busy slots must not prefetch jobs")
            finally:
                worker.stop.set()
                release.set()
                runner.join(5)
        self.assertFalse(runner.is_alive(), "shutdown must drain the polling threads")
        self.assertEqual(len(received), 3, "shutdown must not receive additional work")
        self.assertEqual(worker.sqs.send_message.call_count, 2)
        self.assertEqual({c.kwargs["ReceiptHandle"] for c in worker.sqs.delete_message.call_args_list}, {"1", "2"})
        failed = [c.kwargs for c in worker.sqs.change_message_visibility.call_args_list if c.kwargs["ReceiptHandle"] == "0"]
        self.assertTrue(failed)
        self.assertEqual(failed[-1]["VisibilityTimeout"], 60)

    def test_one_recovered_slot_does_not_clear_another_blocked_slot(self):
        worker = make_worker()
        jobs = [Job.parse(message()["Body"]) for _ in range(2)]
        waiting = [Event(), Event()]
        release = [Event(), Event()]
        calls = [0, 0]

        def cancellation(batch_id):
            slot = next(n for n, job in enumerate(jobs) if job.batch_id == batch_id)
            calls[slot] += 1
            if calls[slot] == 1:
                raise CancellationUnavailable()
            waiting[slot].set()
            if not release[slot].wait(5):
                raise TimeoutError("cancellation check was not released")
            return False

        worker.is_cancelled = cancellation
        threads = [Thread(target=worker.skip_cancelled, args=(job, {}), daemon=True) for job in jobs]
        with patch.object(worker.stop, "wait", return_value=False), patch("worker.consumer.cancellation_blocked") as health:
            try:
                for thread in threads:
                    thread.start()
                self.assertTrue(all(event.wait(5) for event in waiting))
                release[0].set()
                threads[0].join(5)
                self.assertFalse(threads[0].is_alive())
                self.assertEqual(health.set.call_args.args, (1,))
            finally:
                for event in release:
                    event.set()
                for thread in threads:
                    thread.join(5)
            self.assertEqual(health.set.call_args.args, (0,))
