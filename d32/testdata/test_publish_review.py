import importlib.util
import json
from pathlib import Path
import subprocess
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location("publisher", Path(__file__).resolve().parents[1] / "scripts" / "publish_review.py")
publisher = importlib.util.module_from_spec(spec)
spec.loader.exec_module(publisher)


class PublishReviewTest(unittest.TestCase):
    def test_decisions_are_reviews_pinned_to_the_analyzed_commit(self):
        for decision, event in publisher.EVENTS.items():
            with self.subTest(decision=decision), patch.object(publisher.subprocess, "run") as run:
                publisher.publish({"decision": decision, "body": "## Баги\nТекст"}, "owner/repo", "42", "a" * 40)
                args, kwargs = run.call_args
                self.assertEqual(args[0], ["gh", "api", "--method", "POST", "repos/owner/repo/pulls/42/reviews", "--input", "-"])
                self.assertEqual(json.loads(kwargs["input"]), {"event": event, "body": "## Баги\nТекст", "commit_id": "a" * 40})
                self.assertTrue(kwargs["check"])

    def test_invalid_result_does_not_contact_github(self):
        for result in (None, {}, {"decision": "merge", "body": "OK"}, {"decision": "approve", "body": " "}, {"decision": [], "body": "OK"}):
            with self.subTest(result=result), patch.object(publisher.subprocess, "run") as run:
                with self.assertRaises(ValueError):
                    publisher.publish(result, "owner/repo", "42", "a" * 40)
                run.assert_not_called()

    def test_api_failure_is_not_reported_as_success(self):
        with patch.object(publisher.subprocess, "run", side_effect=subprocess.CalledProcessError(1, "gh")):
            with self.assertRaises(subprocess.CalledProcessError):
                publisher.publish({"decision": "approve", "body": "OK"}, "owner/repo", "42", "a" * 40)


if __name__ == "__main__":
    unittest.main()
