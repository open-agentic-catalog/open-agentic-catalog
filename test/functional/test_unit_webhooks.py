"""Deterministic endpoint-management and retention-fixture checks."""

import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import runner
import webhooks


class RetryWindowTests(unittest.TestCase):
    def test_persisted_retry_window_accepts_both_inclusive_bounds(self):
        for attempt in (1, 2):
            for due in ("03.500001", "04.100251", "06.000001"):
                with self.subTest(attempt=attempt, due=due):
                    webhooks.check_retry_window({
                        "state": "waiting", "attempt": attempt, "responseStatus": 429,
                        "updatedAt": "2026-09-26T12:00:01.000001Z",
                        "nextAttemptAt": "2026-09-26T12:00:" + due + "Z"})

    def test_persisted_retry_window_rejects_invalid_state_or_schedule(self):
        valid = {"state": "waiting", "attempt": 2, "responseStatus": 429,
                 "updatedAt": "2026-09-26T12:00:01.000001Z", "nextAttemptAt": "2026-09-26T12:00:04.000001Z"}
        for change in ({"state": "succeeded"}, {"attempt": 0}, {"attempt": 3}, {"responseStatus": 204},
                       {"nextAttemptAt": "2026-09-26T12:00:00.000001Z"},
                       {"nextAttemptAt": "2026-09-26T12:00:03.500000Z"},
                       {"nextAttemptAt": "2026-09-26T12:00:06.000002Z"}):
            with self.subTest(change=change), self.assertRaisesRegex(runner.Failure, "persisted-webhook-retry-window-invalid"):
                webhooks.check_retry_window(dict(valid, **change))


class ManagementTests(unittest.TestCase):
    def test_key_backup_cleans_archive_before_repair_and_on_each_failed_command(self):
        for failed in ("sh", "createdb", "pg_restore", "run"):
            with self.subTest(failed=failed), tempfile.TemporaryDirectory() as root:
                journey = object.__new__(webhooks.Journey)
                journey.fixture = Path(root)
                keys = journey.fixture / "webhooks"
                keys.mkdir()
                (keys / "encryption.key").write_bytes(b"synthetic retained key")
                calls = []

                def compose(*args, **_kwargs):
                    calls.append(args)
                    if failed in args:
                        raise runner.Failure("selected-command-failed")
                    return b"/tmp/catalog-archive.0123456789\n"

                journey.compose = compose
                with self.assertRaisesRegex(runner.Failure, "selected-command-failed"):
                    journey.replace_key({})
                cleanup = ("exec", "-T", "postgres", "rm", "-r", "--", "/tmp/catalog-archive.0123456789")
                self.assertEqual(calls[-2] if failed == "run" else calls[-1], cleanup)
                self.assertEqual(sum(call[0] == "run" for call in calls), int(failed == "run"))
                if failed in ("pg_restore", "run"):
                    restored = next(call for call in calls if "pg_restore" in call)
                    self.assertEqual(restored[-1], "/tmp/catalog-archive.0123456789/archive.dump")
                self.assertEqual((keys / "encryption.key").read_bytes(), b"synthetic retained key")

    def test_creation_readback_rejects_changed_metadata_or_stored_timestamp(self):
        endpoint = {"id": "owned-endpoint", "name": "Owned receiver", "createdAt": "2026-09-10T01:02:03.123456789Z"}
        for change in ({"name": "unexpected"}, {"createdAt": "2026-09-10T01:02:03.123457Z"}):
            with self.subTest(change=change):
                journey = object.__new__(webhooks.Journey)
                journey.api = mock.Mock()
                journey.api.request.side_effect = [({"items": [endpoint]}, {}), (dict(endpoint, **change), {})]
                with self.assertRaisesRegex(AssertionError, "differs from creation"):
                    journey.management({"endpoint": endpoint})
                self.assertEqual(journey.api.request.call_count, 2)

    def test_metadata_update_requires_retained_state_and_preserves_one_time_secret(self):
        endpoint = {"id": "owned-endpoint", "name": "Owned receiver", "revision": 1, "secretVersion": 1,
                    "createdAt": "2026-09-10T01:02:03.123456789Z", "updatedAt": "2026-09-10T01:02:03.123456789Z"}
        stored = dict(endpoint, createdAt="2026-09-10T01:02:03.123456Z", updatedAt="2026-09-10T01:02:03.123456Z")
        changed = dict(stored, name="Updated owned receiver", revision=2)
        for invalid in (None, {"revision": 1}, {"secretVersion": 2}, {"secret": "must-not-return"}):
            with self.subTest(invalid=invalid):
                journey = object.__new__(webhooks.Journey)
                journey.api = mock.Mock()
                retained = dict(changed, **(invalid or {}))
                journey.api.request.side_effect = [({"items": [stored]}, {}), (stored, {"ETag": '"rev-1"'}),
                                                   (retained, {}), (retained, {}), (retained, {})]
                original = {"endpoint": endpoint, "secret": "one-time-fixture-value"}
                if invalid:
                    with self.assertRaises(AssertionError):
                        journey.management(original)
                else:
                    result = journey.management(original)
                    self.assertEqual(result, {"endpoint": changed, "secret": original["secret"]})
                    self.assertEqual(original["endpoint"]["revision"], 1)
                    calls = journey.api.request.call_args_list
                    self.assertEqual(calls[2], calls[3])
                    self.assertEqual(calls[2].kwargs["headers"]["If-Match"], '"rev-1"')
                    self.assertEqual(calls[2].args, ("PATCH", "/api/v1/webhooks/owned-endpoint", {"name": changed["name"]}))


class RetentionTests(unittest.TestCase):
    def journey(self):
        journey = object.__new__(webhooks.Journey)
        journey.env = {"CATALOG_FUNCTIONAL_WEBHOOK_HOUSEKEEPING_INTERVAL": "1h"}
        calls = mock.Mock()
        for name in ("worker_body", "test", "mutation", "compose", "sql", "ready", "record"):
            setattr(journey, name, getattr(calls, name))
        journey.worker_body.side_effect = [
            b'open_agentic_catalog_housekeeping_cycles_total{outcome="succeeded"} 0\n',
            b'open_agentic_catalog_housekeeping_cycles_total{outcome="succeeded"} 1\n']
        journey.test.return_value = {"id": "00000000-0000-7000-8000-000000000001"}
        journey.mutation.return_value = ({}, {"id": "00000000-0000-7000-8000-000000000002"})
        journey.sql.side_effect = ["0", "1", "2", "0", "2", "2", "1", "0", "2"]

        def compose(*args):
            expected = "1m" if args[0] == "up" else "1h"
            self.assertEqual(journey.env["CATALOG_FUNCTIONAL_WEBHOOK_HOUSEKEEPING_INTERVAL"], expected)

        journey.compose.side_effect = compose

        def wait(label, callback, **options):
            self.assertEqual(options, {"seconds": 125} if label == "delivery-cleanup" else {})
            # Each asynchronous boundary must observe completion, not sleep
            # once or accept the first incomplete observation.
            for _ in range(3):
                if callback():
                    return
            self.fail("fixture did not observe its required barrier")

        journey.wait = wait
        return journey, calls

    def test_setup_waits_for_housekeeping_and_database_expiry(self):
        journey, calls = self.journey()
        journey.qualify_retention("endpoint")
        names = [call[0] for call in calls.mock_calls]
        self.assertEqual(names, ["worker_body", "worker_body", "test", "mutation", "sql", "sql", "compose",
                                 "sql", "sql", "sql", "compose", "ready", "sql", "sql", "sql", "sql", "record"])
        self.assertEqual(journey.compose.call_args_list, [mock.call("stop", "--timeout", "20", "worker"), mock.call("up", "--detach", "--no-deps", "--force-recreate", "worker")])
        self.assertEqual(journey.env["CATALOG_FUNCTIONAL_WEBHOOK_HOUSEKEEPING_INTERVAL"], "1h")
        queries = [call.args[0] for call in journey.sql.call_args_list]
        self.assertIn("e.tenant_id=d.tenant_id AND e.id=d.event_id AND e.event_type=d.event_type", queries[0])
        self.assertIn("e.published_at IS NOT NULL AND e.lease_owner IS NULL AND e.lease_expires_at IS NULL", queries[0])
        self.assertIn("cleanup_after IS NULL RETURNING id", queries[2])
        self.assertIn("cleanup_after<=clock_timestamp()", queries[3])
        self.assertTrue(all("tenant_id='" + webhooks.TENANT + "'" in query for query in queries))
        journey.record.assert_called_once_with("prospective-delivery-retention")
        overlay = Path(__file__).with_name("webhooks.compose.yaml").read_text()
        self.assertIn("CATALOG_WORKER_HOUSEKEEPING_INTERVAL: ${CATALOG_FUNCTIONAL_WEBHOOK_HOUSEKEEPING_INTERVAL:-1h}", overlay)
        self.assertLess(runner.RUN_SECONDS, 3600)

    def test_recreation_failure_restores_setup_interval(self):
        journey, _ = self.journey()
        journey.compose.side_effect = [None, runner.Failure("owned-recreation-failed")]
        with self.assertRaisesRegex(runner.Failure, "owned-recreation-failed"):
            journey.qualify_retention("endpoint")
        self.assertEqual(journey.env["CATALOG_FUNCTIONAL_WEBHOOK_HOUSEKEEPING_INTERVAL"], "1h")
        journey.record.assert_not_called()

    def test_preexisting_markers_fail_immediately_without_restart_or_success(self):
        for marked in ("0", "1"):
            journey, _ = self.journey()
            journey.sql.side_effect = ["1", marked]
            with self.assertRaisesRegex(runner.Failure, "delivery-cleanup-fixture-not-marked"):
                journey.qualify_retention("endpoint")
            journey.compose.assert_called_once_with("stop", "--timeout", "20", "worker")
            journey.record.assert_not_called()

    def test_cleanup_must_preserve_both_audit_records(self):
        journey, _ = self.journey()
        journey.sql.side_effect = ["1", "2", "0", "2", "1", "0", "1"]
        with self.assertRaisesRegex(runner.Failure, "delivery-cleanup-lost-audit"):
            journey.qualify_retention("endpoint")
        journey.record.assert_not_called()

    def test_retention_rejects_duplicate_or_malformed_ids_before_sql(self):
        for identifier in ("00000000-0000-7000-8000-000000000001", "not-an-id'", ""):
            journey, _ = self.journey()
            journey.mutation.return_value = ({}, {"id": identifier})
            with self.assertRaisesRegex(runner.Failure, "invalid-delivery-identity"):
                journey.qualify_retention("endpoint")
            journey.sql.assert_not_called()
            journey.compose.assert_not_called()
            journey.record.assert_not_called()

    def test_parent_acknowledgement_failure_does_not_stop_worker_or_mark_rows(self):
        journey, _ = self.journey()
        journey.sql.side_effect = runner.Failure("parent-observation-failed")
        with self.assertRaisesRegex(runner.Failure, "parent-observation-failed"):
            journey.qualify_retention("endpoint")
        journey.sql.assert_called_once()
        self.assertTrue(journey.sql.call_args.args[0].startswith("SELECT"))
        journey.compose.assert_not_called()
        journey.record.assert_not_called()


class DispatcherRecoveryTests(unittest.TestCase):
    def test_phase_requires_actual_worker_recovery_but_preserves_delivery_loss(self):
        status = {"role": "worker", "state": "running", "ready": True, "dependencies": {"postgresql": True},
                  "degraded": True, "components": dict.fromkeys(webhooks.COMPONENTS, "running") | {"webhook_delivery": "degraded"}}
        self.assertTrue(webhooks.worker_restart_phase(status, True))
        for component in webhooks.COMPONENTS:
            changed = copy.deepcopy(status)
            changed["components"][component] = "degraded"
            self.assertFalse(webhooks.worker_restart_phase(changed, True))
        for name, value in (("ready", False), ("role", "api"), ("state", "draining"), ("dependencies", {"postgresql": False})):
            self.assertFalse(webhooks.worker_restart_phase(status | {name: value}, True))
        self.assertFalse(webhooks.worker_restart_phase(status, False))
        status.update(ready=False, dependencies={"postgresql": False})
        self.assertFalse(webhooks.worker_restart_phase(status, False))
        status["components"]["job_worker"] = "degraded"
        self.assertFalse(webhooks.worker_restart_phase(status, False))
        status["components"]["outbox_processor"] = "degraded"
        self.assertTrue(webhooks.worker_restart_phase(status, False))
        status["components"].pop("housekeeper")
        self.assertFalse(webhooks.worker_restart_phase(status, False))

    def test_process_capture_never_reads_full_inspect_or_accepts_missing_container(self):
        journey = object.__new__(webhooks.Journey)
        journey.env = {}
        journey.compose = mock.Mock(side_effect=[b"a" * 64, b"b" * 64])
        expected = [["a" * 64, 100, "start", 0], ["b" * 64, 200, "start", 0]]
        with mock.patch.object(webhooks, "command", return_value=b"\n".join(json.dumps(value).encode() for value in expected)) as command:
            self.assertEqual(journey.process_identities(), expected)
            args = command.call_args.args[0]
            self.assertEqual(args[:3], ["docker", "inspect", "--format"])
            self.assertEqual(args[3], "[{{json .Id}},{{.State.Pid}},{{json .State.StartedAt}},{{.RestartCount}}]")
            self.assertEqual(args[4:], ["a" * 64, "b" * 64])
            journey.compose.side_effect = [b"", b"b" * 64]
            with self.assertRaisesRegex(runner.Failure, "missing-recovery-process"):
                journey.process_identities()
            self.assertEqual(command.call_count, 1)

    def test_recovery_cannot_pass_on_failed_fresh_delivery_or_replaced_process(self):
        for problem in (None, "delivery", "process"):
            with self.subTest(problem=problem):
                journey = object.__new__(webhooks.Journey)
                journey.env = {"CATALOG_FUNCTIONAL_COMMIT": "commit"}
                before = [["api", 100, "start", 0], ["worker", 200, "start", 0]]
                after = copy.deepcopy(before)
                if problem == "process":
                    after[1][2] = "restarted"
                journey.process_identities = mock.Mock(side_effect=[before, after])
                journey.compose = mock.Mock(return_value=b"{}")
                journey.wait = mock.Mock()
                journey.worker_body = mock.Mock()
                journey.ready = mock.Mock()
                journey.api = mock.Mock()
                journey.api.request.return_value = ({"id": "fresh-namespace"}, {})
                journey.mutation = mock.Mock(return_value=({"id": "fresh-namespace"}, {"state": "dead_letter" if problem == "delivery" else "succeeded"}))
                journey.record = mock.Mock()
                with mock.patch.object(webhooks, "diagnostics"):
                    if problem:
                        with self.assertRaises((runner.Failure, AssertionError)):
                            journey.database_recovery({})
                        journey.record.assert_not_called()
                    else:
                        journey.database_recovery({})
                        journey.record.assert_called_once_with("database-outage-and-dispatcher-recovery")
                journey.mutation.assert_called_once_with({})
                journey.ready.assert_called_once_with(restarting=True)
                journey.api.request.assert_any_call("GET", "/readyz", expected=(503,))
                journey.api.request.assert_any_call("GET", "/livez")
