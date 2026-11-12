"""Supervisor wiring with inert process, transport, clock and thread boundaries."""
from contextlib import ExitStack
import copy
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

import runner


class ContinuityTests(unittest.TestCase):
    def test_load_and_soak_check_baseline_each_sample_and_finalization(self):
        for mode in ("load", "soak"):
            for failure_at in (None, 1, 2, 4):
                with self.subTest(mode=mode, failure_at=failure_at):
                    self.run_supervisor(mode, failure_at)

    def test_scale_establishes_baseline_after_deliberate_worker_restart(self):
        self.run_supervisor("scale", 1)

    def test_scale_preserves_safe_plan_failure_and_always_cleans(self):
        for error in (runner.Failure("scale-plan-node-limit"), ValueError("private report body")):
            self.run_supervisor("scale", None, plans_error=error)

    def test_publisher_selection_qualifies_the_actual_runner_and_cursor_binding(self):
        self.run_supervisor("scale", None, selected_workload="publisher-sort")

    def test_seed_failure_does_not_blame_the_previous_sync_command(self):
        self.run_supervisor("scale", None, seed_error=True)

    def test_mixed_failure_preserves_its_actual_failed_command(self):
        self.run_supervisor("scale", None, mixed_error=True)

    def test_post_seed_checks_report_their_actual_stage(self):
        for operation in ("prepare", "publisher_access", "overview"):
            with self.subTest(operation=operation):
                self.run_supervisor("scale", None, post_seed_error=operation)

    def run_supervisor(self, mode, failure_at, plans_error=None, selected_workload="standard", seed_error=False, mixed_error=False, post_seed_error=None):
        identifiers = {role: character * 64 for role, character in (("api", "a"), ("worker", "b"), ("postgres", "c"))}
        rows = [{"Id": identifier, "RestartCount": 0, "State": {"Running": True, "OOMKilled": False, "StartedAt": "original"}} for identifier in identifiers.values()]
        inspections = []
        worker_changes = []
        def command(args, *_args):
            if args[-1] == "worker" and any(action in args for action in ("stop", "start")):
                worker_changes.append("stop" if "stop" in args else "start")
            if args[:2] == ["docker", "inspect"]:
                if len(args) == 5:
                    self.assertEqual(list(identifiers.values()), args[2:])
                    inspections.append(args)
                    if mode == "scale":
                        self.assertEqual(["stop", "start"], worker_changes)
                    value = copy.deepcopy(rows)
                    if len(inspections) == failure_at:
                        if failure_at == 1:
                            value[1]["RestartCount"] = 1
                        else:
                            value[1]["State"]["StartedAt"] = "restarted"
                    return json.dumps(value).encode()
                role = next(role for role, identifier in identifiers.items() if args[-1] == identifier)
                return json.dumps([{"HostConfig": {"NanoCpus": 500000000 if role == "worker" else 1000000000,
                                                  "Memory": (512 if role == "postgres" else 256) << 20, "ShmSize": 256 << 20}}]).encode()
            if args[-3:-1] == ["ps", "-q"]:
                return identifiers[args[-1]].encode()
            if "port" in args:
                return b"127.0.0.1:12345"
            return b""
        def thread(*, target, daemon):
            self.assertTrue(daemon)
            return Mock(start=target, is_alive=Mock(return_value=False))
        def event():
            return Mock(is_set=Mock(return_value=False), wait=Mock(side_effect=[False, True]))
        journey = Mock(stats={"committedPublications": 0}, failure_clock=None, last_command="initial-sync")
        def mixed(_stop, _duration, _warmup, result):
            result.update(status="passed", committedPublications=60 if mode == "soak" else 2)
            if mixed_error:
                result.update(status="failed", failedCommand="publish")
        journey.mixed.side_effect = mixed
        def seed(*args):
            args[5].update(seedResult={"status": "incomplete" if seed_error else "passed", "stage": "listing" if seed_error else "inventory",
                                      "inventory": {"sparseNamespace": "sparse", "fixtureVersion": 2}})
            if seed_error:
                raise runner.Failure("child-failed")
        workload = {"verdict": "passed", "warmup": {"clockAdjustmentMillis": 0},
                    "measured": {"clockAdjustmentMillis": 0, "count": 10, "failed": 0, "elapsedSeconds": 60}}
        with tempfile.TemporaryDirectory() as temporary, ExitStack() as stack:
            root = Path(temporary).resolve()
            (root / "config").mkdir()
            (root / "config/postgres.json").write_text("{}")
            stack.enter_context(patch.dict(os.environ, ({"PERF_SQL_PLANS": "on"} if plans_error else {}) | ({"PERF_SCALE_WORKLOAD": selected_workload} if selected_workload != "standard" else {}), clear=True))
            patches = [(runner, "ROOT", root), (runner, "engine_identity", Mock(return_value="engine")),
                       (runner, "source_identity", Mock(return_value={"commit": "a" * 40})),
                       (runner, "reserve_networks", Mock(return_value={"default": "172.31.0.16/28"})),
                       (runner, "wait_http", Mock()), (runner, "get", Mock(return_value=b"{}")),
                       (runner, "owned_resources", Mock(return_value={})), (runner, "cleanup", Mock()),
                       (runner, "diagnostic_sample", Mock(side_effect=lambda *_: {})), (runner, "resource_sample", Mock(return_value={})),
                       (runner, "database_sample", Mock(return_value={"databaseStatsReset": None, "walStatsReset": None})),
                       (runner, "drain_worker", Mock(return_value={})), (runner, "soak_evidence", Mock(return_value={})),
                       (runner, "workload_result", Mock(side_effect=lambda *_: (copy.deepcopy(workload), False))),
                       (runner, "abort_trial", Mock()), (runner.local, "command", Mock(side_effect=command)),
                       (runner.scale, "preflight", Mock(return_value={})),
                       (runner.scale, "seed", Mock(side_effect=seed)),
                       (runner.scale, "overview", Mock(return_value={"status": "passed"})),
                       (runner.scale, "publisher_access", Mock(return_value={"orders": 3})),
                       (runner.scale, "collect_plans", Mock(side_effect=plans_error)),
                       (runner.scale, "prepare", Mock(return_value={})),
                       (runner.scale, "later_cursor", Mock(return_value="cursor")),
                       (runner.scale, "request", Mock(return_value={"durableTotal": 1002})),
                       (runner.scenarios, "configuration", Mock(return_value={})), (runner.scenarios, "Journey", Mock(return_value=journey)),
                       (runner.socket, "socket", Mock()), (runner.threading, "Thread", Mock(side_effect=thread)),
                       (runner.threading, "Event", Mock(side_effect=event)), (runner.signal, "signal", Mock()), (runner.signal, "alarm", Mock())]
            for owner, name, value in patches:
                stack.enter_context(patch.object(owner, name, value))
            if post_seed_error:
                getattr(runner.scale, post_seed_error).side_effect = runner.Failure("post-seed-check-failed")
            # Socket reservation is inert too: these units never bind a port.
            runner.socket.socket.return_value.__enter__ = Mock(return_value=Mock(getsockname=Mock(return_value=("127.0.0.1", 12345))))
            runner.socket.socket.return_value.__exit__ = Mock(return_value=False)
            if failure_at is None and plans_error is None and not seed_error and not mixed_error and post_seed_error is None:
                runner.run(mode)
            else:
                with self.assertRaisesRegex(runner.Failure, "trial-incomplete"):
                    runner.run(mode)
            report = json.loads(next(root.rglob("report.json")).read_text())
            self.assertEqual("passed", report["cleanup"])
            runner.cleanup.assert_called_once()
            if post_seed_error:
                self.assertEqual({"prepare": "scale-profile", "publisher_access": "scale-scoped-access", "overview": "scale-overview"}[post_seed_error], report["failureStage"])
                self.assertEqual("passed", report["seedResult"]["status"])
                self.assertEqual("inventory", report["seedResult"]["stage"])
                self.assertNotIn("failureCommand", report)
                self.assertEqual([], inspections)
                return
            if seed_error:
                self.assertEqual("scale-seed", report["failureStage"])
                self.assertEqual("listing", report["seedResult"]["stage"])
                self.assertEqual("child-failed", report["failure"])
                self.assertNotIn("failureCommand", report)
                self.assertEqual([], inspections)
                return
            if mixed_error:
                self.assertEqual("publish", report["failureCommand"])
                self.assertEqual("mixed-workload-incomplete", report["failure"])
                return
            self.assertEqual(failure_at or 4, len(inspections))
            self.assertEqual("passed-development-diagnostic" if failure_at is None and plans_error is None else "incomplete", report["status"])
            if selected_workload == "publisher-sort":
                self.assertEqual({"local", "later", *runner.scale.ORDER_CASES}, set(report["readScenarios"]))
                self.assertEqual("publisher-sort", report["scaleWorkload"])
                timed = [call.args[2] for call in runner.workload_result.call_args_list
                         if call.args[2].get("scale", {}).get("case") in runner.scale.ORDER_CASES and call.args[2]["seconds"] == 30]
                self.assertEqual(4, len(timed))
                for selected in timed:
                    self.assertEqual(selected["scale"]["case"].endswith("-third"), "cursor" in selected["scale"])
            if plans_error:
                runner.scale.collect_plans.assert_called_once()
                self.assertEqual({"status": "failed", "failure": runner.scale.plan_failure(plans_error)}, report["sqlPlans"])
                self.assertEqual("scale-plans-incomplete", report["failure"])
                self.assertNotIn("private", json.dumps(report))
            elif failure_at == 2:
                self.assertTrue(report["sampleFailure"])
                self.assertEqual("container-restarted-or-stopped", report["sampleFailureCode"])
                runner.abort_trial.assert_called_once()
            elif failure_at is not None:
                self.assertEqual("container-restarted-or-stopped", report["failure"])
            else:
                self.assertEqual(2, len(report["samples"]))
                for observation in [*report["samples"], report["final"]]:
                    self.assertEqual(report["containerBaseline"], observation["containers"])
