import copy
import hashlib
import json
from pathlib import Path
import unittest
from unittest import mock
import failover
import runner
from failover_test_helpers import evidence


class FailoverTests(unittest.TestCase):
    def test_evidence_requires_all_assertions_and_closed_safe_fields(self):
        for profile in runner.FAILOVER_PROFILES:
            value = evidence(profile)
            self.assertTrue(failover.valid_evidence(value, profile))
            for field in value:
                changed = copy.deepcopy(value)
                del changed[field]
                self.assertFalse(failover.valid_evidence(changed, profile), field)
            for field, bad in (("writerChanged", False), ("completedReplay", 1), ("baselineLost", 1),
                               ("replayedBarrier", "0/FE"), ("barrier", "invalid"),
                               ("barrier", "100000000/0"), ("replayedBarrier", "0/100000000"),
                               ("postgresVersions", [180003, 180004]), ("postgresVersions", [170009, 170009]),
                               ("recoverySeconds", float("nan")), ("recoverySeconds", 181),
                               ("processDigests", {"api": "private-secret"}),
                               ("pendingRepair", not value["pendingRepair"]), ("jobRecovered", not value["jobRecovered"])):
                changed = copy.deepcopy(value)
                changed[field] = bad
                self.assertFalse(failover.valid_evidence(changed, profile), field)
            self.assertFalse(failover.valid_evidence(dict(value, secret="private-secret"), profile))
            self.assertFalse(failover.valid_evidence(value, "unknown"))
    def test_polling_does_not_swallow_unexpected_failure(self):
        with mock.patch.object(failover.time, "sleep"):
            callback = mock.Mock(side_effect=[False, "ready"])
            self.assertEqual(failover.wait_for(callback), "ready")
        with self.assertRaisesRegex(runner.Failure, "unexpected"):
            failover.wait_for(mock.Mock(side_effect=runner.Failure("unexpected")))
        with mock.patch.object(failover.time, "monotonic", side_effect=[0, 2]):
            with self.assertRaisesRegex(runner.Failure, "phase-timeout"):
                failover.wait_for(lambda: False, 1)
    def test_success_must_finish_before_deadline_and_preserves_value(self):
        for finished in (44.9, 45, 46):
            with self.subTest(finished=finished):
                now = [0]
                value = object()
                def observe():
                    now[0] = finished
                    return value
                with mock.patch.object(failover.time, "monotonic", side_effect=lambda: now[0]), mock.patch.object(failover.time, "sleep") as sleep:
                    if finished < 45:
                        self.assertIs(failover.wait_for(observe), value)
                    else:
                        with self.assertRaisesRegex(runner.Failure, "^failover-phase-timeout$"):
                            failover.wait_for(observe)
                    sleep.assert_not_called()
    def test_false_observation_crossing_deadline_does_not_sleep_or_retry(self):
        now = [0]
        def observe():
            now[0] = 46
            return False
        callback = mock.Mock(side_effect=observe)
        with mock.patch.object(failover.time, "monotonic", side_effect=lambda: now[0]), mock.patch.object(failover.time, "sleep") as sleep:
            with self.assertRaisesRegex(runner.Failure, "^failover-phase-timeout$"):
                failover.wait_for(callback)
        callback.assert_called_once()
        sleep.assert_not_called()
    def test_sleep_is_capped_to_remaining_time(self):
        now = [0]
        def observe():
            now[0] = 44.95
            return False
        def advance(seconds):
            now[0] += seconds
        callback = mock.Mock(side_effect=observe)
        with mock.patch.object(failover.time, "monotonic", side_effect=lambda: now[0]), mock.patch.object(failover.time, "sleep", side_effect=advance) as sleep:
            with self.assertRaisesRegex(runner.Failure, "^failover-phase-timeout$"):
                failover.wait_for(callback)
        callback.assert_called_once()
        sleep.assert_called_once()
        self.assertAlmostEqual(sleep.call_args.args[0], 0.05)
    def test_sql_readiness_waits_for_streaming_replication(self):
        scenario = object.__new__(failover.Failover)
        scenario.phase = mock.Mock()
        scenario.env = {"CATALOG_FUNCTIONAL_REPLICATION_PASSWORD": "a" * 48}
        scenario.evidence = {}
        scenario.compose = mock.Mock(return_value=b"127.0.0.1:12345")
        scenario.identity = mock.Mock(return_value="sha256:" + "a" * 64)
        streaming = mock.Mock(side_effect=["0", "1"])
        def sql(statement, _server=None):
            if "pg_stat_replication" in statement:
                return streaming()
            return {"SELECT pg_is_in_recovery()": "t", "SHOW synchronous_standby_names": "",
                    "SHOW server_version_num": "180006"}.get(statement, "")
        scenario.sql = mock.Mock(side_effect=sql)
        with mock.patch.object(failover.time, "sleep"):
            scenario.setup()
        self.assertEqual(streaming.call_count, 2)
        self.assertEqual(scenario.evidence["postgresVersions"], [180006, 180006])
    def test_incomplete_promotion_cannot_reach_recovery(self):
        scenario = object.__new__(failover.Failover)
        scenario.profile = "failover-planned"
        scenario.phase = mock.Mock()
        scenario.compose = mock.Mock(return_value=b"")
        scenario.health = mock.Mock(return_value=True)
        scenario.sql = mock.Mock(return_value="f")
        with self.assertRaises(AssertionError):
            scenario.promote()
        self.assertNotIn(mock.call("recovery"), scenario.phase.call_args_list)
    def test_supervisor_refuses_partial_evidence_and_cleanup_failure(self):
        for profile in runner.FAILOVER_PROFILES:
            report = {"profile": profile, "images": {"api": "image"}, "servedVersion": {"version": "functional"},
                      "smoke": {"checkCount": 1}, "failover": evidence(profile)}
            steps = list(runner.selected_steps(profile))
            self.assertTrue(runner.finalize(copy.deepcopy(report), steps, "", True))
            self.assertFalse(runner.finalize(copy.deepcopy(report), steps[:-1], "", True))
            self.assertFalse(runner.finalize(copy.deepcopy(report), steps, "", False))
            self.assertFalse(runner.finalize(copy.deepcopy(report), steps, "interrupted", True))
            report.pop("failover")
            self.assertFalse(runner.finalize(report, steps, "", True))


class RetainedStateTests(unittest.TestCase):
    """Exercise the actual baseline/verification decisions with inert exchanges."""

    def setUp(self):
        self.namespace = {"id": "namespace", "tenantId": "tenant", "slug": "complete-owned",
                          "displayName": "Writer recovery baseline", "revision": 1}
        self.created = json.dumps(self.namespace).encode()
        self.public = b'{"entries":[]}\n'
        self.responses = {
            "created": (self.created, {"ETag": '"rev-1"'}),
            "replay": (self.created, {"ETag": '"rev-1"', "Idempotency-Replayed": "true"}),
            "read": (self.created, {"ETag": '"rev-1"'}),
            "public": (self.public, {"ETag": '"' + hashlib.sha256(self.public).hexdigest() + '"'}),
        }
        self.scenario = object.__new__(failover.Failover)
        self.scenario.phase = mock.Mock()
        self.scenario.suffix = "owned"
        self.scenario.profile = "failover-planned"
        self.scenario.state = Path("unused-in-memory-state")
        self.scenario.evidence = evidence(self.scenario.profile)
        self.scenario.processes = dict(self.scenario.evidence["processDigests"])
        self.scenario.identity = mock.Mock(side_effect=self.scenario.processes.get)
        self.scenario.sql = mock.Mock(side_effect=lambda statement: "2" if "count(DISTINCT usename)" in statement
                                      else "0" if "published_at IS NULL" in statement else '[{"id":"event","data":{}}]')
        self.scenario.api = mock.Mock(base_url="http://unused.invalid")
        self.scenario.api.request.side_effect = self.exchange
        public_client = mock.Mock()
        public_client.request.side_effect = self.exchange
        self.enterContext(mock.patch.object(failover, "Client", return_value=public_client))
        self.enterContext(mock.patch.object(failover, "audit_once"))
        self.enterContext(mock.patch.object(failover.authorization_recovery, "prepare", return_value={"owned": True}))
        self.policy_verify = self.enterContext(mock.patch.object(failover.authorization_recovery, "verify"))
        self.report = self.enterContext(mock.patch.object(failover, "write_document"))
        self.creating = True

    def exchange(self, method, path, _body=None, expected=(200,), _headers=None, *, decode_body=True):
        if expected == (404,):
            return {"code": "NOT_FOUND"}, {}
        key = "public" if path == "/.well-known/ard.json" else "read" if method == "GET" else "created" if self.creating else "replay"
        raw, headers = self.responses[key]
        return (json.loads(raw) if decode_body else raw), dict(headers)

    def baseline(self):
        self.scenario.baseline()
        self.creating = False

    def test_unchanged_state_completes_and_writes_safe_evidence(self):
        self.baseline()
        self.scenario.verify()
        self.report.assert_called_once()
        self.assertTrue(failover.valid_evidence(self.report.call_args.args[1], self.scenario.profile))
        self.scenario.phase.assert_called_with("complete")
        self.policy_verify.assert_called_once_with(self.scenario.api, {"owned": True})

    def test_baseline_requires_expected_etags(self):
        for key in ("created", "public"):
            raw, valid_headers = self.responses[key]
            for invalid in ({}, {"ETag": '"wrong"'}):
                with self.subTest(key=key, headers=invalid):
                    self.responses[key] = raw, invalid
                    with self.assertRaises(AssertionError):
                        self.scenario.baseline()
            self.responses[key] = raw, valid_headers

    def test_recovery_rejects_changed_bytes_even_when_json_is_equal(self):
        self.baseline()
        for key in ("replay", "public"):
            raw, headers = self.responses[key]
            with self.subTest(key=key):
                self.responses[key] = b" " + raw, headers
                with self.assertRaises(AssertionError):
                    self.scenario.verify()
                self.report.assert_not_called()
            self.responses[key] = raw, headers

    def test_recovery_rejects_missing_or_wrong_etags(self):
        self.baseline()
        for key in ("replay", "read", "public"):
            raw, headers = self.responses[key]
            for invalid in ({}, {"ETag": '"wrong"'}):
                with self.subTest(key=key, headers=invalid):
                    self.responses[key] = raw, {**{k: v for k, v in headers.items() if k != "ETag"}, **invalid}
                    with self.assertRaises(AssertionError):
                        self.scenario.verify()
                    self.report.assert_not_called()
            self.responses[key] = raw, headers

    def test_changed_process_cannot_be_reported_as_recovery(self):
        self.baseline()
        self.scenario.identity.side_effect = lambda name: "sha256:" + "b" * 64 if name == "api" else self.scenario.processes[name]
        with self.assertRaises(AssertionError):
            self.scenario.verify()
        self.report.assert_not_called()

    def prepare_repair(self):
        self.scenario.pending_body = {name: self.namespace[name] for name in ("slug", "displayName")}
        self.scenario.pending_headers = {"Idempotency-Key": "original-pending-key"}
        self.scenario.reservation = "original-reservation"
        self.scenario.job_id = "original-job"
        self.scenario.repair = mock.Mock(side_effect=[
            {"state": "interrupted", "eligible": True, "active": False, "reason": "owned", "planDigest": "digest"},
            {"applied": True},
        ])
        def exchange(method, path, body=None, expected=(200,), headers=None, **kwargs):
            if expected == (503,):
                return {"code": "DEPENDENCY_UNAVAILABLE"}, {}
            if path == "/api/v1/jobs/original-job":
                return {"id": "original-job", "state": "succeeded", "attempt": 2}, {}
            result = self.exchange(method, path, body, expected, headers, **kwargs)
            if method == "POST":
                self.creating = False
            return result
        self.scenario.api.request.side_effect = exchange
        return mock.Mock(result=mock.Mock(return_value=({"code": "DEPENDENCY_UNAVAILABLE"}, {})))

    def test_repaired_command_preserves_original_key_and_reads_retained_state(self):
        pending = self.prepare_repair()
        self.scenario.recover_command(pending)
        pending.result.assert_called_once_with(timeout=70)
        posts = [call for call in self.scenario.api.request.call_args_list if call.args[0] == "POST"]
        self.assertEqual(len(posts), 3)
        self.assertTrue(all(call.args[4] == {"Idempotency-Key": "original-pending-key"} for call in posts))
        self.scenario.api.request.assert_any_call("GET", "/api/v1/namespaces/namespace")
        self.scenario.api.request.assert_any_call("GET", "/api/v1/jobs/original-job")
        self.assertEqual([call.args[0] for call in self.scenario.repair.call_args_list], ["plan", "apply"])

    def test_repaired_command_rejects_changed_replay_bytes(self):
        pending = self.prepare_repair()
        raw, headers = self.responses["replay"]
        self.responses["replay"] = b" " + raw, headers
        with self.assertRaises(AssertionError):
            self.scenario.recover_command(pending)
        self.assertNotIn(mock.call("GET", "/api/v1/jobs/original-job"), self.scenario.api.request.call_args_list)

    def test_repaired_command_requires_creation_replay_and_readback_etags(self):
        for key in ("created", "replay", "read"):
            raw, headers = self.responses[key]
            for invalid in ({}, {"ETag": '"wrong"'}):
                with self.subTest(key=key, headers=invalid):
                    self.creating = True
                    pending = self.prepare_repair()
                    self.responses[key] = raw, {**{k: v for k, v in headers.items() if k != "ETag"}, **invalid}
                    with self.assertRaises(AssertionError):
                        self.scenario.recover_command(pending)
            self.responses[key] = raw, headers
