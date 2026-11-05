"""Isolated controls for original-process failure, fresh acceptance and restart."""

import copy
import math
import re
import unittest
from unittest import mock

import runner
import tracing_recovery as recovery


class RecoveryFixture:
    def __init__(self, fault=""):
        self.fault = fault
        self.reject = False
        self.restarted = False
        self.failure_observed = False
        self.parents = []
        self.phases = []
        self.readiness = []
        self.instance = "00000000-0000-7000-8000-000000000001"
        self.next_instance = "00000000-0000-7000-8000-000000000002"
        self.root = {"name": "GET /api/v1/namespaces", "service": "open-agentic-catalog-api",
                     "installation": "compose-tracing", "instance": self.instance,
                     "traceId": "a" * 32, "parentId": "b" * 16}
        self.api = mock.Mock()
        self.api.request.side_effect = self.request
        self.receiver = mock.Mock()
        self.receiver.request.side_effect = self.control

    def identity(self):
        changed = self.restarted or self.fault == "outage-restart" and self.reject or self.fault == "recovery-restart" and self.parents
        if self.fault == "missing-restart":
            changed = False
        container = "d" if self.fault == "replaced-container" and self.restarted else "c"
        return (container * 64, "2" if changed else "1", "2026-10-02T12:00:01Z" if changed else "2026-10-02T12:00:00Z", "1" if changed else "0")

    def metrics(self):
        failures, submitted = 2, 10
        if self.reject:
            if self.fault != "missing-failure":
                failures += 1
                submitted += 10
                self.failure_observed = True
            if self.fault == "reset-counter":
                submitted = 0
            if self.fault == "missing-submission":
                submitted = 10
            if self.fault == "missing-metric":
                return {}
        return {recovery.FAILURES: failures, recovery.SUBMITTED: submitted}

    def control(self, method, path, body, **kwargs):
        assert method == "POST" and path == "/tracing/control" and kwargs == {"expected": (204,)}
        if body == {"reject": False}:
            assert self.failure_observed, "receiver recovered before failure evidence"
        self.reject = body["reject"]

    def request(self, method, path, **kwargs):
        assert method == "GET" and path == "/api/v1/namespaces?limit=2"
        if "headers" in kwargs:
            assert not self.reject and self.failure_observed
            self.parents.append(kwargs["headers"]["Traceparent"])
        elif not self.reject:
            raise AssertionError("outage read skipped rejection")

    def ready(self):
        self.readiness.append(self.reject)
        if self.fault == "unready" and self.reject:
            raise runner.Failure("product-not-ready")

    def spans(self):
        span = dict(self.root)
        # Include stale ingress even during recovery: it must never satisfy a new parent.
        values = [span]
        if self.fault == "stale-capture":
            return values
        parent = self.parents[-1]
        fresh = dict(span, traceId=parent[3:35], parentId=parent[36:52],
                     instance=self.next_instance if self.restarted else self.instance)
        if self.fault == "wrong-parent":
            fresh["parentId"] = "f" * 16
        if self.fault == "wrong-service":
            fresh["service"] = "open-agentic-catalog-worker"
        if self.fault == "wrong-installation":
            fresh["installation"] = "another"
        if self.fault == "wrong-operation":
            fresh["name"] = "job process"
        if self.fault == "wrong-recovery-instance" and not self.restarted:
            fresh["instance"] = self.next_instance
        if self.fault == "old-restart-instance" and self.restarted:
            fresh["instance"] = self.instance
        return values + [fresh]

    def restart(self):
        assert self.failure_observed and len(self.parents) == 1 and not self.reject
        self.restarted = True

    @staticmethod
    def wait(label, callback):
        for _ in range(3):
            result = callback()
            if result:
                return result
        raise AssertionError("missing " + label)

    def run(self):
        with mock.patch.object(recovery, "wait_until", self.wait):
            return recovery.qualify(self.api, self.receiver, spans=self.spans, metrics=self.metrics,
                                    identity=self.identity, ready=self.ready, restart=self.restart,
                                    phase=self.phases.append, root=self.root)


class TracingRecoveryTests(unittest.TestCase):
    def test_failure_precedes_recovery_and_restart_is_separate(self):
        fixture = RecoveryFixture()
        result = fixture.run()
        self.assertTrue(recovery.valid_evidence(result))
        self.assertEqual(fixture.phases, ["outage", "recover-original", "restart"])
        self.assertEqual(fixture.readiness, [True, True, False, False])
        self.assertEqual(fixture.api.request.call_count, 12)
        self.assertEqual(result["failedBefore"], 2)
        self.assertEqual(result["failedAfter"], 3)
        self.assertNotEqual(fixture.parents[0][3:35], fixture.parents[1][3:35])

    def test_false_pass_controls(self):
        cases = {
            "missing-failure": (AssertionError, "missing original trace export failure"),
            "missing-submission": (AssertionError, "missing original trace export failure"),
            "reset-counter": (runner.Failure, "trace-outage-counters-reset"),
            "missing-metric": (runner.Failure, "invalid-trace-counter"),
            "outage-restart": (runner.Failure, "trace-outage-process-changed"),
            "recovery-restart": (runner.Failure, "trace-recovery-process-changed"),
            "missing-restart": (runner.Failure, "trace-restart-process-unchanged"),
            "replaced-container": (runner.Failure, "trace-restart-process-unchanged"),
            "stale-capture": (AssertionError, "missing original process fresh export"),
            "wrong-parent": (AssertionError, "missing original process fresh export"),
            "wrong-service": (AssertionError, "missing original process fresh export"),
            "wrong-installation": (AssertionError, "missing original process fresh export"),
            "wrong-operation": (AssertionError, "missing original process fresh export"),
            "wrong-recovery-instance": (runner.Failure, "trace-recovery-process-changed"),
            "old-restart-instance": (runner.Failure, "invalid-trace-recovery-evidence"),
            "unready": (runner.Failure, "product-not-ready"),
        }
        for fault, (exception, message) in cases.items():
            with self.subTest(fault=fault), self.assertRaisesRegex(exception, "^" + re.escape(message) + "$"):
                RecoveryFixture(fault).run()

    def test_counters_reject_absence_invalid_values_and_accept_zero(self):
        for value in (None, True, -1, 0.5, math.inf, math.nan, "1"):
            with self.subTest(value=value), self.assertRaises(runner.Failure):
                recovery.counter({recovery.FAILURES: value}, recovery.FAILURES)
        self.assertEqual(recovery.counter({recovery.FAILURES: 0.0}, recovery.FAILURES), 0)

    def test_process_observation_is_selected_and_requires_running(self):
        raw = ("c" * 64 + " 42 2026-10-02T12:00:00.123456789Z 0 true").encode()
        self.assertEqual(recovery.process_identity(raw)[1], "42")
        for invalid in (b"", raw + b" private", raw.replace(b" true", b" false"),
                        raw.replace(b" 42 ", b" 0 "), raw.replace(b" 0 true", b" -1 true"),
                        raw.replace(b"2026-10-02T12:00:00.123456789Z", b"unknown")):
            with self.subTest(invalid=invalid), self.assertRaises(runner.Failure):
                recovery.process_identity(invalid)

    def test_report_requires_completed_recovery_evidence(self):
        valid = RecoveryFixture().run()
        for change in ({"failedAfter": 2}, {"failedAfter": True}, {"submittedAfter": 10},
                       {"recoveredInstance": valid["restartedInstance"]},
                       {"restartedInstance": valid["originalInstance"]}, {"checks": recovery.CHECKS[:-1]},
                       {"restartParent": valid["recoveryParent"]}, {"recoveryParent": "bad"},
                       {"originalInstance": "private"}, {"private": "unreviewed"}):
            with self.subTest(change=change):
                self.assertFalse(recovery.valid_evidence(dict(valid, **change)))
        for value in (None, {}, [], valid):
            report = {"profile": "tracing", "images": {"api": "owned"}, "servedVersion": "owned", "smoke": {"passed": True}, "tracingRecovery": copy.deepcopy(value)}
            self.assertEqual(runner.finalize(report, list(runner.selected_steps("tracing")), "", True), value == valid)


if __name__ == "__main__":
    unittest.main()
