import copy
import unittest
from unittest.mock import Mock, patch

import operations
import operators


class WorkerRecoveryTests(unittest.TestCase):
    def test_admitted_worker_build_identity_is_exact(self):
        value = self.outage_status("job_worker")
        value.update(version="0.1.0", sourceState="modified")
        operations.check_status(value, "commit", False, "0.1.0", "modified")
        for version, state in (("functional", "modified"), ("0.1.0", "unknown"), ("0.1.0", "clean")):
            with self.subTest(version=version, state=state), self.assertRaises(AssertionError):
                operations.check_status(value, "commit", False, version, state)

    def test_operator_commands_preserve_the_admitted_image_and_fixture_scope(self):
        environment = {"CATALOG_FUNCTIONAL_SUFFIX": "abcdef123456", "CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY": "/fixture",
                       "CATALOG_FUNCTIONAL_COMMIT": "a" * 40, "CATALOG_FUNCTIONAL_HTTP_PORT": "12345",
                       "CATALOG_FUNCTIONAL_SUBNET": "172.31.0.0/28", "CATALOG_FUNCTIONAL_BASE_URL": "http://127.0.0.1:12345",
                       "CATALOG_FUNCTIONAL_CANDIDATE_IMAGE": "sha256:" + "c" * 64,
                       "CATALOG_FUNCTIONAL_VERSION": "0.1.0", "CATALOG_FUNCTIONAL_SOURCE_STATE": "modified"}
        with patch.object(operators, "Client"):
            value = operators.Operators(environment)
        self.assertEqual(value.env["IMAGE"], environment["CATALOG_FUNCTIONAL_CANDIDATE_IMAGE"])
        self.assertEqual(value.env["VERSION"], "0.1.0")
        self.assertEqual(value.env["SOURCE_STATE"], "modified")
        self.assertEqual(value.project, "open-agentic-catalog-functional-abcdef123456")

    def test_readiness_alone_does_not_prove_component_recovery(self):
        value = {"degraded": True, "components": dict.fromkeys(operations.COMPONENTS, "running")}
        for component in operations.COMPONENTS - {"operations_server"}:
            with self.subTest(component=component):
                changed = copy.deepcopy(value)
                changed["components"][component] = "degraded"
                self.assertFalse(operations.phase_complete(changed, "recovered", set()))
        value["degraded"] = False
        self.assertTrue(operations.phase_complete(value, "recovered", set()))
        observed_degraded = set()
        self.assertFalse(operations.phase_complete(value, "degraded", observed_degraded))
        value["components"]["job_worker"] = "degraded"
        self.assertFalse(operations.phase_complete(value, "degraded", observed_degraded))
        value["components"]["outbox_processor"] = "degraded"
        self.assertTrue(operations.phase_complete(value, "degraded", observed_degraded))

    @staticmethod
    def outage_status(component):
        value = {"status": "not_ready", "role": "worker", "state": "running", "ready": False,
                 "degraded": True, "components": dict.fromkeys(operations.COMPONENTS, "running"),
                 "dependencies": {"postgresql": False}, "version": "functional", "commit": "commit",
                 "sourceState": "unknown", "instanceId": "00000000-0000-7000-8000-000000000001", "startedAt": "2026-09-01T12:00:00Z", "observedAt": "2026-09-01T11:00:00Z", "uptimeSeconds": 1, "housekeeping": {"interval": "1s", "terminalJobRetention": "1h",
                 "acknowledgedOutboxRetention": "1h", "batchLimit": 100}}
        value["components"][component] = "degraded"
        return value

    def test_process_information_rejects_malformed_values_but_allows_clock_corrections(self):
        value = self.outage_status("job_worker")
        operations.check_process_info(value)
        for changed in ({"instanceId": "private-url"}, {"instanceId": None},
                      {"uptimeSeconds": -1}, {"uptimeSeconds": True}, {"uptimeSeconds": 1.5},
                      {"startedAt": "invalid"}, {"observedAt": "2026-09-01T12:00:00+01:00"}):
            with self.subTest(changed=changed), self.assertRaises((AssertionError, ValueError)):
                operations.check_process_info({**value, **changed})

    def test_independent_backoffs_need_not_coincide_in_one_sample(self):
        api = Mock()
        version = {"status": "version", "role": "worker", "state": "running", "degraded": True,
                   "version": "functional", "commit": "commit", "sourceState": "unknown"}
        api.request.side_effect = [(self.outage_status("job_worker"), {"Cache-Control": "no-store"}),
                                   (self.outage_status("outbox_processor"), {"Cache-Control": "no-store"}),
                                   (version, {})]

        def observe(label, callback):
            self.assertEqual(label, "worker degraded component transition")
            self.assertFalse(callback())
            self.assertTrue(callback())

        with patch.object(operations, "Client", return_value=api), patch.object(operations, "wait_until", side_effect=observe):
            operations.run("http://owned-worker.invalid", "commit", False, "degraded")
        self.assertEqual(api.request.call_args_list[-1].args, ("GET", "/version"))

    def test_outage_observations_cannot_span_a_readiness_change(self):
        api = Mock()
        recovered = self.outage_status("outbox_processor")
        recovered.update(ready=True, status="degraded", dependencies={"postgresql": True})
        api.request.side_effect = [(self.outage_status("job_worker"), {"Cache-Control": "no-store"}),
                                   (recovered, {"Cache-Control": "no-store"})]

        def observe(_label, callback):
            self.assertFalse(callback())
            callback()

        with patch.object(operations, "Client", return_value=api), patch.object(operations, "wait_until", side_effect=observe), self.assertRaisesRegex(AssertionError, "dependency readiness mismatch"):
            operations.run("http://owned-worker.invalid", "commit", False, "degraded")
        self.assertEqual(api.request.call_count, 2)

    def test_timeout_retains_only_the_last_valid_status_and_accumulated_progress(self):
        api = Mock()
        first = self.outage_status("job_worker")
        second = self.outage_status("housekeeper")
        api.request.side_effect = [(first, {"Cache-Control": "no-store"}), (second, {"Cache-Control": "no-store"})]

        def expire(_label, callback):
            self.assertFalse(callback())
            self.assertFalse(callback())
            raise AssertionError("private-worker-timeout")

        with patch.object(operations, "Client", return_value=api), patch.object(operations, "wait_until", side_effect=expire) as wait, patch("time.monotonic", side_effect=[10, 55.125]):
            with self.assertRaisesRegex(AssertionError, "private-worker-timeout") as failure:
                operations.run("http://private-worker.invalid", "commit", False, "degraded")
        self.assertEqual(failure.exception.worker_status_failure, {
            "lastStatus": {key: second[key] for key in ("status", "ready", "degraded", "components")},
            "observedDegraded": ["job_worker"], "samples": 2, "elapsedMillis": 45125})
        self.assertEqual(len(wait.call_args.args), 2)
        self.assertFalse(wait.call_args.kwargs)  # Preserve the existing default deadline.
        self.assertEqual(api.request.call_count, 2)  # Diagnostics make no extra HTTP request.
        self.assertNotIn("private", str(failure.exception.worker_status_failure))

    def test_invalid_status_is_not_retained_as_the_last_valid_observation(self):
        api = Mock()
        invalid = self.outage_status("job_worker") | {"status": "private-response"}
        api.request.return_value = invalid, {"Cache-Control": "no-store"}
        with patch.object(operations, "Client", return_value=api), patch.object(operations, "wait_until", side_effect=lambda _label, callback: callback()), patch("time.monotonic", side_effect=[10, 11]):
            with self.assertRaises(AssertionError) as failure:
                operations.run("http://private-worker.invalid", "commit", False, "degraded")
        self.assertEqual(failure.exception.worker_status_failure, {
            "lastStatus": None, "observedDegraded": [], "samples": 0, "elapsedMillis": 1000})


    def test_process_replacement_cannot_count_as_recovery(self):
        before = [["api", 100, "first-start", 0], ["worker", 200, "first-start", 0]]
        operations.check_process_identity(before, copy.deepcopy(before))
        for process in range(2):
            for field, replacement in enumerate(["replacement", 300, "second-start", 1]):
                with self.subTest(process=process, field=field):
                    after = copy.deepcopy(before)
                    after[process][field] = replacement
                    with self.assertRaisesRegex(AssertionError, "restarted"):
                        operations.check_process_identity(before, after)
        with self.assertRaisesRegex(AssertionError, "missing"):
            operations.check_process_identity([], [])
        before[0][1] = 0
        with self.assertRaisesRegex(AssertionError, "invalid"):
            operations.check_process_identity(before, before)

    def test_fresh_recovery_job_must_finish_successfully(self):
        api = Mock()
        with patch.object(operations, "source_revision", return_value=3) as revision, patch.object(operations, "sync", return_value={"job": {"id": "fresh"}, "existing": False}) as submit, patch.object(operations, "wait_job") as wait:
            for completion in (
                {"id": "fresh", "state": "succeeded", "result": {"snapshotId": "snapshot"}},
                {"id": "fresh", "state": "failed"},
                {"id": "fresh", "state": "cancelled"},
                {"id": "previous", "state": "succeeded", "result": {"snapshotId": "snapshot"}},
                {"id": "fresh", "state": "succeeded", "result": {}},
            ):
                with self.subTest(completion=completion):
                    wait.return_value = completion
                    if completion == {"id": "fresh", "state": "succeeded", "result": {"snapshotId": "snapshot"}}:
                        operations.recover_work(api, "existing-source")
                    else:
                        with self.assertRaisesRegex(AssertionError, "did not succeed"):
                            operations.recover_work(api, "existing-source")
                    revision.assert_called_with(api, "existing-source")
                    submit.assert_called_with(api, "existing-source", 3, "database-recovery")
                    wait.assert_called_with(api, "fresh", {"succeeded", "failed", "cancelled"})

    def test_deduplication_must_be_followed_by_fresh_work(self):
        existing = {"job": {"id": "automatic"}, "existing": True}
        fresh = {"job": {"id": "fresh"}, "existing": False}
        for second in (fresh, existing):
            with self.subTest(second=second), patch.object(operations, "source_revision", return_value=3), patch.object(operations, "sync", side_effect=[existing, second]) as submit, patch.object(operations, "wait_job", side_effect=[{"state": "succeeded"}, {"id": "fresh", "state": "succeeded", "result": {"snapshotId": "snapshot"}}]) as wait:
                if second == fresh:
                    operations.recover_work(Mock(), "source")
                    self.assertEqual(wait.call_count, 2)
                else:
                    with self.assertRaisesRegex(AssertionError, "create fresh work"):
                        operations.recover_work(Mock(), "source")
                    self.assertEqual(wait.call_count, 1)
                self.assertEqual(submit.call_count, 2)
        with patch.object(operations, "source_revision", return_value=3), patch.object(operations, "sync", return_value=existing) as submit, patch.object(operations, "wait_job", return_value={"state": "failed"}):
            with self.assertRaisesRegex(AssertionError, "existing recovery Job"):
                operations.recover_work(Mock(), "source")
            self.assertEqual(submit.call_count, 1)

    def test_worker_diagnostics_reject_default_or_wrong_retry_policy(self):
        value = {"status": "ready", "role": "worker", "version": "functional", "commit": "commit", "schema": {"startupCompatible": True, "embeddedVersion": 33}, "profiles": {}, "operations": {}, "configuration": {"database": {"maxConnLifetime": "45m0s", "maxConnIdleTime": "3m0s", "healthCheckPeriod": "15s"}, "worker": {"dependencyRetryInitialDelay": "1.25s", "dependencyRetryCeiling": "2.5s"}}, "workLimits": {}}
        operations.diagnostics(value, "commit", "worker")
        for name, wrong in (("dependencyRetryInitialDelay", "1s"), ("dependencyRetryCeiling", "30s")):
            changed = copy.deepcopy(value)
            changed["configuration"]["worker"][name] = wrong
            with self.assertRaisesRegex(AssertionError, "not delivered"):
                operations.diagnostics(changed, "commit", "worker")

    def test_diagnostics_reject_default_or_missing_pool_policy(self):
        value = {"status": "ready", "role": "api", "version": "functional", "commit": "commit",
                 "schema": {"startupCompatible": True, "embeddedVersion": 1}, "profiles": {},
                 "operations": {}, "workLimits": {}, "configuration": {"database": {
                     "maxConnLifetime": "45m0s", "maxConnIdleTime": "3m0s", "healthCheckPeriod": "15s"}}}
        operations.diagnostics(value, "commit", "api")
        for name, default in (("maxConnLifetime", "30m0s"), ("maxConnIdleTime", "5m0s"), ("healthCheckPeriod", "30s")):
            changed = copy.deepcopy(value)
            changed["configuration"]["database"][name] = default
            with self.assertRaisesRegex(AssertionError, "pool policy not delivered"):
                operations.diagnostics(changed, "commit", "api")
