"""Job-scoped evidence assertions for the packaged federation scenario."""

import copy
import json
import os
from pathlib import Path
import tempfile
import runner
import unittest
from unittest import mock

from federation import Client, check_job_completion, check_retry_wait, wait_job, wait_until


class FederationEvidenceTests(unittest.TestCase):
    def test_client_rejects_public_cursor_failure_with_only_a_fixed_diagnostic(self):
        for message, category in (("ARD hybrid pageToken is stale; restart the search", "PUBLIC_CURSOR_STALE"),
                                  ("ARD hybrid pageToken is stale; restart the search private-secret", "unclassified")):
            payload = json.dumps({"errorCode": "INVALID_ARGUMENT", "message": message, "private": "private-secret"}).encode()
            with self.subTest(category=category), mock.patch("federation.coverage.enabled", return_value=False), \
                    mock.patch("federation.bounded_request", return_value=(400, {"Date": "Thu, 22 Oct 2026 12:00:00 GMT"}, payload)), \
                    mock.patch("federation.validate_response") as validate:
                with self.assertRaises(AssertionError) as failure:
                    Client("http://127.0.0.1:1").request("POST", "/ard/v1/search", {"query": {"text": "peer"}})
                self.assertEqual(str(failure.exception), f"functional federation request returned HTTP 400 ({category})")
                self.assertNotIn("private-secret", str(failure.exception))
                self.assertEqual(failure.exception.response_date, 1792670400)
                validate.assert_not_called()

    def test_wait_deadline_rejects_late_success_and_bounds_sleep(self):
        for elapsed in (45, 46):
            now = [0.0]
            def callback():
                now[0] = elapsed
                return "late"
            with self.subTest(elapsed=elapsed), mock.patch("federation.time.monotonic", side_effect=lambda: now[0]), \
                    mock.patch("federation.time.sleep") as sleep:
                with self.assertRaisesRegex(AssertionError, "timed out"):
                    wait_until("owned condition", callback)
                sleep.assert_not_called()
        with mock.patch("federation.time.monotonic", side_effect=[0, 0, 44.9, 45]), \
                mock.patch("federation.time.sleep") as sleep:
            with self.assertRaises(AssertionError):
                wait_until("owned condition", lambda: None)
            self.assertAlmostEqual(sleep.call_args.args[0], 0.1)
        with mock.patch("federation.time.monotonic", return_value=0):
            self.assertEqual(wait_until("owned condition", lambda: "on time"), "on time")

    def test_job_wait_rejects_unwanted_terminal_outcomes_without_polling_again(self):
        for state in ("succeeded", "failed", "cancelled", "dead_letter"):
            job = {"state": state, "attempt": 1}
            api = mock.Mock()
            api.request.return_value = (job, {})
            with self.subTest(state=state), mock.patch("federation.record_job_wait_failure") as record, \
                    mock.patch("federation.time.sleep") as sleep:
                self.assertEqual(wait_job(api, "private-id", {state}), job)
                with self.assertRaisesRegex(AssertionError, "unexpected terminal"):
                    wait_job(api, "private-id", {"waiting"})
                sleep.assert_not_called()
                record.assert_called_once()
                self.assertEqual(record.call_args.args[0], job)
                self.assertEqual(api.request.call_count, 2)

    def test_job_wait_retains_last_observation_and_original_request_failure(self):
        job = {"state": "running"}
        original = ValueError("original request error")
        api = mock.Mock()
        api.request.side_effect = [(job, {}), original]
        with mock.patch("federation.time.sleep"), mock.patch("federation.record_job_wait_failure") as record:
            with self.assertRaises(ValueError) as caught:
                wait_job(api, "private-id", {"succeeded"})
            self.assertIs(caught.exception, original)
            self.assertEqual(record.call_args.args[0], job)

    def test_job_wait_observes_transitions_without_recording_success_as_failure(self):
        sequence = [{"state": state, "attempt": 1} for state in ("waiting", "running", "succeeded")]
        api = mock.Mock()
        api.request.side_effect = [(job, {}) for job in sequence]
        now = [0.0]
        def advance(seconds):
            now[0] += seconds
        with mock.patch("federation.time.monotonic", side_effect=lambda: now[0]), \
                mock.patch("federation.time.sleep", side_effect=advance) as sleep, \
                mock.patch("federation.record_job_wait_failure") as record:
            self.assertIs(wait_job(api, "owned", {"succeeded"}), sequence[-1])
            self.assertEqual(api.request.call_args_list, [mock.call("GET", "/api/v1/jobs/owned")] * 3)
            self.assertEqual(sleep.call_args_list, [mock.call(0.25)] * 2)
            record.assert_not_called()

    def test_job_wait_timeout_checkpoints_the_latest_observation(self):
        now, reads = [0.0], [0]
        def observe(*_args):
            reads[0] += 1
            now[0] = 1.0 if reads[0] == 1 else 45.0
            return {"id": "private-id", "state": "waiting" if reads[0] == 1 else "running",
                    "attempt": reads[0], "attemptLimit": 7, "cancellationRequested": False,
                    "result": {"private": "payload"}, "safeError": "private-error"}, {}
        api = mock.Mock()
        api.request.side_effect = observe
        with tempfile.TemporaryDirectory() as directory, \
                mock.patch.dict(os.environ, {"CATALOG_FUNCTIONAL_STATE_DIRECTORY": directory}), \
                mock.patch("federation.time.monotonic", side_effect=lambda: now[0]), \
                mock.patch("federation.time.sleep"):
            with self.assertRaisesRegex(AssertionError, "timed out waiting for requested Job state"):
                wait_job(api, "private-id", {"succeeded"})
            self.assertEqual(reads[0], 2)
            self.assertEqual(runner.read_job_wait_failure(Path(directory)), {
                "state": "running", "failureClass": "", "attempt": 2, "attemptLimit": 7,
                "cancellationRequested": False, "elapsedMillis": 45000})

    def test_retry_window_uses_persisted_bounds_and_classification(self):
        job = {"state": "waiting", "attemptLimit": 7, "failureClass": "rate_limited",
               "updatedAt": "2026-09-26T12:00:00Z", "notBefore": "2026-09-26T12:00:02Z"}
        check_retry_wait(job, "rate_limited")
        check_retry_wait(dict(job, notBefore="2026-09-26T12:00:07.500000Z"), "rate_limited")
        for change in ({"notBefore": "2026-09-26T12:00:01.9Z"}, {"notBefore": "2026-09-26T12:00:07.6Z"},
                       {"state": "running"}, {"attemptLimit": 5}, {"failureClass": "permanent_upstream"}):
            with self.subTest(change=change), mock.patch("federation.record_job_wait_failure") as record:
                failed = dict(job, **change)
                with self.assertRaises(AssertionError):
                    check_retry_wait(failed, "rate_limited")
                record.assert_called_once_with(failed, 0)

    def test_independent_refreshes_and_earlier_attempts_are_not_duplicates(self):
        job = {"id": "selected", "attempt": 2, "result": {"snapshotId": "final"}}
        events = [
            {"targetId": "automatic", "details": {"facts": {"jobId": "background", "jobAttempt": 1}}},
            {"targetId": "earlier", "details": {"facts": {"jobId": "selected", "jobAttempt": 1}}},
            {"targetId": "final", "details": {"facts": {"jobId": "selected", "jobAttempt": 2}}},
        ]
        check_job_completion(events, job)
        for invalid in (events[:-1], events + [copy.deepcopy(events[-1])]):
            with self.assertRaisesRegex(AssertionError, "one matching completion"):
                check_job_completion(invalid, job)
        wrong = copy.deepcopy(events)
        wrong[-1]["targetId"] = "another-snapshot"
        with self.assertRaisesRegex(AssertionError, "one matching completion"):
            check_job_completion(wrong, job)


if __name__ == "__main__":
    unittest.main()
