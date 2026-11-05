"""Finite observation, failure reporting and the exact owned port boundary."""
import copy
import json
import os
from pathlib import Path
import signal
import tempfile
import threading
import unittest
from unittest.mock import patch

import runner


class QualificationTests(unittest.TestCase):
    def test_database_failure_evidence_is_fixed_and_redacted(self):
        cases = {
            "sharedMemoryExhausted": b'could not resize shared memory segment "/PostgreSQL.secret" to 1 bytes: No space left on device',
            "memoryExhausted": b"out of memory",
            "statementTimeout": b"canceling statement due to statement timeout",
            "serializationFailure": b"could not serialize access due to concurrent update",
            "deadlock": b"deadlock detected",
        }
        for selected, message in cases.items():
            with self.subTest(selected=selected), patch.object(runner.local, "command", return_value=message + b" private SQL secret") as command:
                evidence = runner.database_failure_evidence(["owned-compose"], {})
                self.assertEqual({"status": "observed"} | {key: key == selected for key in cases}, evidence)
                self.assertEqual(["owned-compose", "logs", "--no-color", "--tail", "80", "postgres"], command.call_args.args[0])
                self.assertNotIn("secret", json.dumps(evidence))
        with patch.object(runner.local, "command", return_value=b"unrelated private SQL"):
            self.assertEqual({"status": "observed"} | dict.fromkeys(cases, False), runner.database_failure_evidence([], {}))
        with patch.object(runner.local, "command", side_effect=runner.Failure("deadline")):
            self.assertEqual({"status": "unavailable"}, runner.database_failure_evidence([], {}))

    def test_activation_guard_classification_never_retains_raw_logs(self):
        for raw, expected in [(b"private statement: active publication snapshot activation time is inconsistent", True), (b"private unrelated error", False)]:
            with patch.object(runner.local, "command", return_value=raw):
                self.assertIs(expected, runner.publication_clock_guard(["owned-compose"], {}))
        with patch.object(runner.local, "command", side_effect=runner.Failure("deadline")):
            self.assertIsNone(runner.publication_clock_guard(["owned-compose"], {}))

    def test_failed_background_work_interrupts_only_an_active_owned_trial(self):
        stop = threading.Event()
        with patch.object(runner.os, "kill") as kill:
            runner.abort_trial(stop)
            kill.assert_called_once_with(os.getpid(), signal.SIGALRM)
            stop.set()
            runner.abort_trial(stop)
            self.assertEqual(1, kill.call_count)

    def test_whole_run_alarm_retains_partial_report_and_cleans_independently(self):
        handlers = {}
        def install(number, callback):
            previous = handlers.get(number, signal.SIG_DFL)
            handlers[number] = callback
            return previous
        def expired(_environment):
            handlers[signal.SIGALRM](signal.SIGALRM, None)
        for mode, seconds in [("smoke", 900), ("load", 1800), ("soak", 4800)]:
            with tempfile.TemporaryDirectory() as temporary, patch.object(runner, "ROOT", Path(temporary)), patch.object(runner, "engine_identity", return_value="engine"), patch.object(runner, "source_identity", side_effect=expired), patch.object(runner, "cleanup") as cleanup, patch.object(runner.signal, "signal", side_effect=install), patch.object(runner.signal, "alarm") as alarm, patch.dict(os.environ, {}, clear=True):
                with self.assertRaisesRegex(runner.Failure, "incomplete-see-safe-report"):
                    runner.run(mode)
                cleanup.assert_called_once()
                self.assertEqual([seconds, 0], [call.args[0] for call in alarm.call_args_list])
                report = json.loads(next(Path(temporary).rglob("report.json")).read_text())
                self.assertEqual("incomplete", report["status"])
                self.assertEqual("passed", report["cleanup"])
                self.assertEqual("interrupted-or-deadline", report["failure"])
                self.assertNotIn("workload", report)

    def test_worker_drain_requires_actual_complete_zero_and_is_bounded(self):
        idle = {'open_agentic_catalog_jobs{state="' + state + '"}': 0 for state in ("queued", "running", "waiting")}
        idle['open_agentic_catalog_outbox_events{state="pending"}'] = 0
        with patch.object(runner, "metrics_sample", return_value=idle):
            self.assertEqual(0, runner.drain_worker("owned")["nonterminalJobs"])
        with patch.object(runner, "metrics_sample", return_value={}):
            with self.assertRaisesRegex(runner.Failure, "signals-missing"):
                runner.drain_worker("owned")
        with patch.object(runner, "metrics_sample", return_value=dict.fromkeys(idle, 1)), patch.object(runner.time, "monotonic", side_effect=[0, 1, 31]), patch.object(runner.time, "sleep"):
            with self.assertRaisesRegex(runner.Failure, "drain-incomplete"):
                runner.drain_worker("owned")

    def test_soak_requires_cycles_complete_samples_and_reports_growth_not_flatness(self):
        classes = ("idempotency", "source_preview", "source_inspection", "source_coordination", "terminal_job", "webhook_delivery", "acknowledged_outbox")
        worker = {'open_agentic_catalog_housekeeping_batches_total{class="' + name + '",outcome="' + outcome + '"}': 0 for name in classes for outcome in ("succeeded", "failed")}
        worker.update(open_agentic_catalog_go_heap_bytes=100, open_agentic_catalog_go_goroutines=10)
        sample = {"api": dict(worker), "worker": worker, "resources": {role: {"workingSetBytes": 100} for role in ("api", "worker")}, "database": {"databaseBytes": 100}}
        samples = [copy.deepcopy(sample) | {"startedElapsedSeconds": index * 60, "elapsed": index * 60} for index in range(60)]
        final = copy.deepcopy(sample)
        for name in classes:
            final["worker"]['open_agentic_catalog_housekeeping_batches_total{class="' + name + '",outcome="succeeded"}'] = 12
        for index, value in enumerate(samples):
            value["database"]["databaseBytes"] += index * 10
        evidence = runner.soak_evidence(samples, final, {"startedElapsedSeconds": 0, "finishedElapsedSeconds": 3600})
        self.assertEqual(600, evidence["databaseBytesPerHour"])
        self.assertEqual(dict.fromkeys(classes, 12), evidence["successfulHousekeepingChecks"])
        # Setup/drain outliers do not become a measured growth trend.
        outlier = copy.deepcopy(samples[0])
        outlier["database"]["databaseBytes"] = 100000000
        outside = [outlier | {"startedElapsedSeconds": -60, "elapsed": 0}, *samples,
                   outlier | {"startedElapsedSeconds": 3599, "elapsed": 3601}]
        self.assertEqual(evidence, runner.soak_evidence(outside, final, {"startedElapsedSeconds": 0, "finishedElapsedSeconds": 3600}))
        for bad_samples, bad_final in [(samples[:49], final), (samples, sample), (samples, {})]:
            with self.assertRaises(runner.Failure):
                runner.soak_evidence(bad_samples, bad_final, {"startedElapsedSeconds": 0, "finishedElapsedSeconds": 3600})
        final["worker"]['open_agentic_catalog_housekeeping_batches_total{class="idempotency",outcome="failed"}'] = 1
        with self.assertRaisesRegex(runner.Failure, "maintenance-incomplete"):
            runner.soak_evidence(samples, final, {"startedElapsedSeconds": 0, "finishedElapsedSeconds": 3600})

    def test_sample_windows_reject_leading_trailing_stale_or_out_of_order_evidence(self):
        samples = [{"startedElapsedSeconds": 0, "elapsed": 1}, {"startedElapsedSeconds": 11, "elapsed": 12}]
        final = {"startedElapsedSeconds": 20, "elapsed": 21}
        runner.validate_observation_window(samples, final, 0, 45)
        for selected, last in [(samples[:1], final),
                               ([{"startedElapsedSeconds": 46, "elapsed": 47}, samples[1]], final),
                               ([samples[0], {"startedElapsedSeconds": 11, "elapsed": 57}], final),
                               (list(reversed(samples)), final),
                               (samples, {"startedElapsedSeconds": 58, "elapsed": 59}),
                               (samples, {"startedElapsedSeconds": 20, "elapsed": 19})]:
            with self.subTest(samples=selected, final=last), self.assertRaisesRegex(runner.Failure, "required-sample-gap"):
                runner.validate_observation_window(selected, last, 0, 45)

    def test_real_bind_failure_not_an_arbitrary_failed_container_is_required(self):
        state = {"project": "owned", "nonce": "nonce"}
        for error, expected in [("bind: address already in use", True), ("port is already allocated", True), ("unrelated failure", False)]:
            value = [{"State": {"Running": False, "Error": error}, "Config": {"Labels": {runner.OWNER_LABEL: "nonce"}}}]
            with patch.object(runner.local, "command", side_effect=[b"a" * 64, runner.Failure("child-failed"), json.dumps(value).encode()]) as command:
                if expected:
                    self.assertTrue(runner.occupied_port_check(state, {}, 12345)["occupiedPortRefused"])
                else:
                    with self.assertRaisesRegex(runner.Failure, "bind-failure-unproven"):
                        runner.occupied_port_check(state, {}, 12345)
                self.assertIn("/prometheus:size=16m,mode=1777", command.call_args_list[0].args[0])
                self.assertFalse(any("rm" in call.args[0] or "down" in call.args[0] for call in command.call_args_list))

    def test_query_tool_mounts_synthetic_input_without_writable_collectors(self):
        with patch.object(runner.local, "command") as command:
            runner.check_query_fixture({"project": "owned", "nonce": "nonce"}, {}, Path("/owned/tests.json"))
            args = command.call_args.args[0]
            self.assertIn("--read-only", args)
            self.assertIn("none", args)
            self.assertIn("type=bind,src=/owned/tests.json,dst=/tests.json,readonly", args)
            self.assertIn("/prometheus:size=16m,mode=1777", args)
            self.assertEqual(["test", "rules", "/tests.json"], args[-3:])


if __name__ == "__main__":
    unittest.main()
