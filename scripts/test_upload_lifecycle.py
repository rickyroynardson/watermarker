"""Safety checks for the staging-only lifecycle policy. Run with Python stdlib."""
import json
import unittest
from pathlib import Path


class UploadLifecycleTest(unittest.TestCase):
    def test_retention_cannot_delete_job_inputs_or_outputs(self):
        policy = json.loads(
            (Path(__file__).parent / "localstack/uploads-lifecycle.json").read_text()
        )
        rules = policy["Rules"]
        self.assertTrue(rules)
        self.assertEqual(len({r["ID"] for r in rules}), len(rules))
        for rule in rules:
            # A bucket-wide or broader filter risks live job inputs and results.
            self.assertEqual(rule["Filter"], {"Prefix": "uploads/"})
            self.assertEqual(rule["Status"], "Enabled")
            prefix = rule["Filter"]["Prefix"]
            self.assertTrue("uploads/owner/abandoned".startswith(prefix))
            for key in ("sources/owner/shared-watermark", "processed/batch/image.png", "uploads-backup/file"):
                self.assertFalse(key.startswith(prefix))
        expiry = [r["Expiration"]["Days"] for r in rules if "Days" in r["Expiration"]]
        self.assertEqual(expiry, [7], "keep staging uploads for a full week")
        self.assertTrue(any(r.get("NoncurrentVersionExpiration", {}).get("NoncurrentDays") == 7 for r in rules))
        self.assertTrue(any(r["Expiration"].get("ExpiredObjectDeleteMarker") for r in rules))


if __name__ == "__main__":
    unittest.main()
