"""Polling and final-report regressions without containers or real sleeps."""

import contextlib
import io
import json
import os
from pathlib import Path
import signal
import tempfile
import unittest
from unittest import mock

from http_client import ClientFailure
import runner
import webhooks


class WaitTests(unittest.TestCase):
    def setUp(self):
        self.journey = object.__new__(webhooks.Journey)
        self.journey.completed = []
        self.journey.api = mock.Mock()
        self.now = 0
        self.monotonic = mock.patch.object(webhooks.time, "monotonic", side_effect=lambda: self.now)
        self.sleep = mock.patch.object(webhooks.time, "sleep", side_effect=self.advance)
        self.monotonic.start()
        self.sleeper = self.sleep.start()
        self.addCleanup(self.monotonic.stop)
        self.addCleanup(self.sleep.stop)

    def advance(self, seconds):
        self.now += seconds

    def page(self, state=None, attempt=1):
        return ({"items": [] if state is None else [
            {"eventId": "private-event", "state": state, "attempt": attempt}]}, {})

    def delivery(self, state="succeeded"):
        return self.journey.delivery({"endpoint": {"id": "private-endpoint"}}, "private-event", state)

    def test_missing_pending_waiting_then_success(self):
        other = ({"items": [{"eventId": "unrelated", "state": "dead_letter", "attempt": 3}]}, {})
        self.journey.api.request.side_effect = [other, self.page("pending", 0), self.page("waiting"), self.page("succeeded", 2)]
        self.assertEqual(self.delivery()["attempt"], 2)
        self.assertEqual(self.journey.api.request.call_count, 4)
        self.assertEqual(self.sleeper.call_count, 3)

    def test_retry_observation_waits_for_the_requested_attempt(self):
        endpoint = {"endpoint": {"id": "private-endpoint"}}
        self.journey.api.request.side_effect = [self.page("waiting", 1), self.page("waiting", 2)]
        observed = self.journey.delivery(endpoint, "private-event", "waiting", label="throttle-waiting", attempt=2)
        self.assertEqual(observed["attempt"], 2)
        self.assertEqual(self.journey.api.request.call_count, 2)
        self.sleeper.assert_called_once()
        self.journey.api.request.side_effect = None
        self.journey.api.request.return_value = self.page("dead_letter", 3)
        with self.assertRaises(webhooks.WaitFailure) as caught:
            self.journey.delivery(endpoint, "private-event", "waiting", label="throttle-waiting", attempt=2)
        self.assertEqual(caught.exception.observation["category"], "unexpected-terminal")

    def test_persisted_retry_metric_wait_accepts_delayed_post_commit_observation(self):
        callback = mock.Mock(side_effect=[False, True])
        self.assertTrue(self.journey.wait("persisted-retry-metric", callback))
        self.assertEqual(callback.call_count, 2)
        self.sleeper.assert_called_once()

    def test_database_diagnostic_is_scoped_read_only_and_closed(self):
        endpoint = {"endpoint": {"tenantId": "00000000-0000-7000-8000-000000000001",
                                 "id": "00000000-0000-7000-8000-000000000002"}}
        event = "00000000-0000-7000-8000-000000000003"
        evidence = {field: None for field in runner.WEBHOOK_DATABASE_FIELDS}
        evidence.update(eventExists=True, endpointExists=True, eventPredatesEndpoint=True,
                        eventAcknowledged=True, eventLeased=False, deliveryExists=False)
        self.journey.sql = mock.Mock(return_value="BEGIN\nSET\n" + json.dumps(evidence) + "\nCOMMIT")
        self.assertEqual(self.journey.delivery_diagnostic(endpoint, event), evidence)
        query = self.journey.sql.call_args.args[0]
        self.assertIn("BEGIN READ ONLY", query)
        self.assertIn("statement_timeout='2s'", query)
        self.assertEqual(query.count("tenant_id='" + endpoint["endpoint"]["tenantId"] + "'"), 3)
        for raw in ("private-secret", "x" * 4097, json.dumps(dict(evidence, private="secret")),
                    json.dumps(dict(evidence, eventExists=1)), json.dumps(evidence) + "\n" + json.dumps(evidence)):
            self.journey.sql.return_value = raw
            self.assertEqual(self.journey.delivery_diagnostic(endpoint, event), "unavailable")
        self.journey.sql.side_effect = RuntimeError("private-secret")
        self.assertEqual(self.journey.delivery_diagnostic(endpoint, event), "unavailable")
        self.journey.sql.reset_mock()
        self.assertIsNone(self.journey.delivery_diagnostic(endpoint, "'; private-secret"))
        self.journey.sql.assert_not_called()

    def test_missing_delivery_retains_database_evidence_without_replacing_timeout(self):
        self.journey.api.request.return_value = self.page()
        evidence = {field: None for field in runner.WEBHOOK_DATABASE_FIELDS}
        evidence.update(eventPredatesEndpoint=True, deliveryExists=False)
        for result in (evidence, "unavailable"):
            with mock.patch.object(self.journey, "delivery_diagnostic", return_value=result) as diagnostic:
                with self.assertRaises(webhooks.WaitFailure) as caught:
                    self.delivery()
                diagnostic.assert_called_once()
            failure = caught.exception.observation
            self.assertEqual(failure["category"], "timeout")
            self.assertEqual(failure["observedState"], "missing")
            self.assertEqual(failure["database"], result)
            self.assertTrue(runner.valid_webhook_failure(failure))
            with tempfile.TemporaryDirectory() as directory:
                runner.write_document(Path(directory) / "webhooks.json", {"completed": [], "failure": failure})
                self.assertEqual(runner.webhook_evidence(Path(directory))["failure"], failure)
        for invalid in ({"private": "secret"}, dict(evidence, eventExists=1), "private-secret"):
            self.assertFalse(runner.valid_webhook_failure(dict(failure, database=invalid)))

    def test_terminal_states_fail_immediately_unless_expected(self):
        for observed, expected in (("dead_letter", "succeeded"), ("succeeded", "waiting"), ("succeeded", "dead_letter")):
            with self.subTest(observed=observed, expected=expected):
                self.journey.api.request.return_value = self.page(observed, 3)
                self.journey.api.request.reset_mock()
                with self.assertRaises(webhooks.WaitFailure) as caught:
                    self.delivery(expected)
                value = caught.exception.observation
                self.assertEqual((value["category"], value["expectedState"], value["observedState"], value["attempt"]),
                                 ("unexpected-terminal", expected, observed, 3))
                self.journey.api.request.assert_called_once()
        self.sleeper.assert_not_called()
        for state in runner.WEBHOOK_STATES:
            self.journey.api.request.return_value = self.page(state)
            self.assertEqual(self.delivery(state)["state"], state)

    def test_observation_failures_are_not_retried_or_disclosed(self):
        errors = [
            (AssertionError("functional federation request returned HTTP 403 (unclassified)"), "http", 403),
            (AssertionError("functional federation request returned HTTP 503 (DEPENDENCY_UNAVAILABLE)"), "http", 503),
            (AssertionError("private-secret"), "assertion", None),
            (AssertionError("functional federation request returned HTTP 403 (unclassified) private-secret"), "assertion", None),
            (ClientFailure("functional response contract mismatch"), "contract", None),
            (ClientFailure("functional response validator failed"), "contract", None),
            (ClientFailure("functional HTTP transport failed"), "transport", None),
            (ClientFailure("functional HTTP deadline"), "transport", None),
            (ClientFailure("functional HTTP invalid framing"), "invalid-response", None),
            (ClientFailure("functional JSON response invalid"), "invalid-response", None),
            (ValueError("private-secret"), "observation", None),
        ]
        for error, category, status in errors:
            with self.subTest(category=category, status=status):
                self.journey.api.request.side_effect = error
                self.journey.api.request.reset_mock()
                with self.assertRaises(webhooks.WaitFailure) as caught:
                    self.delivery()
                value = caught.exception.observation
                self.assertEqual((value["category"], value["status"]), (category, status))
                self.assertEqual(value["observedState"], "unobserved")
                self.assertNotIn("private-", json.dumps(value))
                self.journey.api.request.assert_called_once()
        self.sleeper.assert_not_called()

    def test_only_readiness_retries_known_unavailability(self):
        callback = mock.Mock(side_effect=[ClientFailure("functional HTTP transport failed"),
                             AssertionError("functional federation request returned HTTP 503 (unclassified)"), True])
        self.assertTrue(self.journey.wait("api-ready", callback))
        self.assertEqual(callback.call_count, 3)
        for error in (AssertionError("private-secret"), ClientFailure("functional JSON response invalid"),
                      AssertionError("functional federation request returned HTTP 401 (unclassified)")):
            callback = mock.Mock(side_effect=error)
            with self.assertRaises(webhooks.WaitFailure):
                self.journey.wait("api-ready", callback)
            callback.assert_called_once()

    def test_readiness_retry_requires_explicit_api_restart(self):
        self.journey.compose = mock.Mock()
        error = AssertionError("functional federation request returned HTTP 503 (unclassified)")
        self.journey.api.request.side_effect = error
        with self.assertRaises(webhooks.WaitFailure) as caught:
            self.journey.ready()
        self.assertEqual(caught.exception.observation["wait"], "api-health")
        self.journey.api.request.assert_called_once()
        self.journey.compose.assert_not_called()
        self.journey.api.request.side_effect = [error, ({"ready": True}, {})]
        self.journey.ready(restarting=True)
        self.assertEqual(self.journey.compose.call_count, 2)

    def test_deadline_caps_sleep_retains_last_error_and_rejects_late_success(self):
        callback = mock.Mock(side_effect=ClientFailure("functional HTTP transport failed"))
        with self.assertRaises(webhooks.WaitFailure) as caught:
            self.journey.wait("api-ready", callback, seconds=0.15)
        self.assertEqual(caught.exception.observation["category"], "transport")
        self.assertAlmostEqual(self.now, 0.15)
        self.assertEqual(callback.call_count, 2)
        self.assertAlmostEqual(self.sleeper.call_args.args[0], 0.05)
        for elapsed, result in ((1, True), (2, False), (2, True)):
            def slow():
                self.advance(elapsed)
                return result
            with self.assertRaises(webhooks.WaitFailure) as caught:
                self.journey.wait("delivery-cleanup", slow, seconds=1)
            self.assertEqual(caught.exception.observation["category"], "timeout")

    def test_expired_budget_starts_no_work_and_just_in_time_success_passes(self):
        callback = mock.Mock(return_value=True)
        with self.assertRaises(webhooks.WaitFailure) as caught:
            self.journey.wait("delivery-cleanup", callback, seconds=0)
        self.assertEqual(caught.exception.observation["category"], "timeout")
        callback.assert_not_called()
        self.sleeper.assert_not_called()
        def just_in_time():
            self.advance(0.99)
            return "ready"
        self.assertEqual(self.journey.wait("delivery-cleanup", just_in_time, seconds=1), "ready")
        self.sleeper.assert_not_called()

    def test_latest_observation_replaces_stale_error_or_delivery_state(self):
        calls = 0
        def readiness():
            nonlocal calls
            calls += 1
            if calls == 1:
                raise AssertionError("functional federation request returned HTTP 503 (unclassified)")
            self.advance(1)
            return False
        with self.assertRaises(webhooks.WaitFailure) as caught:
            self.journey.wait("api-ready", readiness, seconds=1)
        self.assertEqual(calls, 2)
        self.assertEqual((caught.exception.observation["category"], caught.exception.observation["status"]), ("timeout", None))
        self.journey.api.request.side_effect = [self.page("waiting", 2), ClientFailure("functional response contract mismatch")]
        with self.assertRaises(webhooks.WaitFailure) as caught:
            self.delivery()
        self.assertEqual((caught.exception.observation["category"], caught.exception.observation["observedState"],
                          caught.exception.observation["attempt"]), ("contract", "unobserved", None))
        self.assertEqual(self.journey.api.request.call_count, 2)

    def test_timeout_retains_latest_valid_state_not_an_earlier_one(self):
        self.journey.api.request.side_effect = [self.page("waiting", 1), self.page(), self.page("pending", 0)]
        # Advance beyond the deadline during the final observation.
        original = self.journey.api.request.side_effect
        def observe(*_args):
            value = next(original)
            if value[0]["items"] and value[0]["items"][0]["state"] == "pending":
                self.advance(46)
            return value
        self.journey.api.request.side_effect = observe
        with self.assertRaises(webhooks.WaitFailure) as caught:
            self.delivery()
        self.assertEqual((caught.exception.observation["observedState"], caught.exception.observation["attempt"]), ("pending", 0))

    def test_invalid_delivery_values_do_not_enter_diagnostics(self):
        for state, attempt in (("private-secret", 1), ("waiting", True), ("waiting", -1), ("waiting", 10001)):
            self.journey.api.request.return_value = self.page(state, attempt)
            with self.assertRaises(webhooks.WaitFailure) as caught:
                self.delivery()
            self.assertEqual(caught.exception.observation["category"], "invalid-response")
            self.assertEqual(caught.exception.observation["observedState"], "unobserved")
            self.assertNotIn("private-", json.dumps(caught.exception.observation))

    def test_interrupt_is_not_retried_or_reclassified(self):
        callback = mock.Mock(side_effect=KeyboardInterrupt)
        with self.assertRaises(KeyboardInterrupt):
            self.journey.wait("api-ready", callback)
        callback.assert_called_once()
        self.sleeper.assert_not_called()


class ReportTests(unittest.TestCase):
    def test_documented_failure_example_passes_report_validation(self):
        manual = Path(__file__).with_name("README.md").read_text()
        raw = manual.split("```json\n", 1)[1].split("\n```", 1)[0]
        failure = runner.decode(raw.encode())
        self.assertTrue(runner.valid_webhook_failure(failure))
        completed = runner.WEBHOOK_CASES[:runner.WEBHOOK_CASES.index(failure["case"])]
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            runner.write_document(path / "webhooks.json", {"completed": completed, "failure": failure})
            self.assertEqual(runner.webhook_evidence(path)["failure"], failure)

    def test_closed_report_rejects_invalid_fields_and_case_attribution(self):
        journey = object.__new__(webhooks.Journey)
        journey.completed = []
        valid = journey.observation("delivery", "succeeded")
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            invalid = [{}, [], None, dict(valid, extra="private-secret")]
            invalid += [dict(valid, **{key: value}) for key, value in (
                ("case", "private-secret"), ("case", runner.WEBHOOK_CASES[1]), ("wait", "private-secret"),
                ("expectedState", []), ("observedState", {}), ("category", "private-secret"),
                ("attempt", True), ("attempt", -1), ("attempt", 10001), ("attempt", "1"),
                ("status", True), ("status", 99), ("status", 600), ("status", "200"))]
            for failure in invalid:
                runner.write_document(path / "webhooks.json", {"completed": [], "failure": failure})
                with self.assertRaisesRegex(runner.Failure, "invalid-webhook-progress"):
                    runner.webhook_evidence(path)
            runner.write_document(path / "webhooks.json", {"completed": [], "failure": valid})
            self.assertEqual(runner.webhook_evidence(path)["failure"], valid)
            runner.write_document(path / "webhooks.json", {"completed": runner.WEBHOOK_CASES, "failure": valid})
            with self.assertRaises(runner.Failure):
                runner.webhook_evidence(path)

    def test_real_main_to_report_path_preserves_failure_and_cleanup(self):
        for diagnostic in ("valid", "write-failed", "bounded-write-failed", "invalid", "cleanup-failed", "child-zero"):
            with self.subTest(diagnostic=diagnostic), tempfile.TemporaryDirectory() as directory, \
                    mock.patch.object(Path, "cwd", return_value=Path(directory)):
                instance = runner.Runner({}, "webhooks")
                instance.state = Path(directory)
                journey = object.__new__(webhooks.Journey)
                journey.state = instance.state
                journey.completed = runner.WEBHOOK_CASES[:2]
                journey.crash_evidence = None
                journey.report_evidence = None
                # Use the real client and error encoder; fake only wire I/O.
                journey.api = webhooks.Client("http://127.0.0.1:18080", "private-secret")
                journey.run = lambda: journey.delivery({"endpoint": {"id": "private-endpoint"}},
                                                       "private-event", label="retry-recovery")
                def execute():
                    with mock.patch.object(webhooks, "Journey", return_value=journey):
                        self.assertEqual(webhooks.main(), 1)
                    if diagnostic == "invalid":
                        runner.write_document(instance.state / "webhooks.json", {"completed": [], "failure": "private-secret"})
                    if diagnostic != "child-zero":
                        raise runner.Failure("child-failed")
                output = io.StringIO()
                with mock.patch.object(runner, "Runner", return_value=instance), \
                        mock.patch.object(instance, "execute", side_effect=execute), \
                        mock.patch.object(instance, "cleanup", return_value=diagnostic != "cleanup-failed") as cleanup, \
                        mock.patch("federation.bounded_request", return_value=(403, {"X-Private": "private-header"},
                                   b'{"code":"private-code","detail":"private-body"}')) as wire, \
                        mock.patch.object(runner, "progress", return_value=[]), \
                        mock.patch.object(signal, "signal"), \
                        mock.patch.dict(os.environ, {"CATALOG_FUNCTIONAL_STATE_DIRECTORY": directory}), \
                        contextlib.redirect_stdout(output), contextlib.redirect_stderr(output), \
                        contextlib.ExitStack() as stack:
                    if diagnostic in ("write-failed", "bounded-write-failed"):
                        error = OSError("private-secret") if diagnostic == "write-failed" else runner.Failure("document-limit")
                        stack.enter_context(mock.patch.object(webhooks, "write_document", side_effect=error))
                    self.assertEqual(runner.run("webhooks"), 1)
                    cleanup.assert_called_once()
                    wire.assert_called_once()
                report = runner.read_document(instance.report_path)
                self.assertEqual(report["failureCode"], "webhook-wait-failed" if diagnostic == "child-zero" else "child-failed")
                self.assertEqual(report["cleanup"], "failed" if diagnostic == "cleanup-failed" else "passed")
                if diagnostic in ("valid", "cleanup-failed", "child-zero"):
                    self.assertEqual(report["webhookFailure"], dict(journey.observation("retry-recovery", "succeeded"), category="http", status=403))
                    self.assertEqual(report["webhookCases"], runner.WEBHOOK_CASES[:2])
                else:
                    self.assertNotIn("webhookFailure", report)
                self.assertNotIn("private-", json.dumps(report) + output.getvalue())
