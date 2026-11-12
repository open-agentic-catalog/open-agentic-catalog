import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import runner


class PerformanceOwnership(unittest.TestCase):
    def test_reservation_retries_conflict_and_binds_exact_policy(self):
        state = {"project": "owned", "nonce": "nonce", "monitoring": False}
        inspected = [{"Name": "owned_default", "Driver": "bridge", "Labels": {runner.OWNER_LABEL: "nonce", "com.docker.compose.project": "owned"}, "IPAM": {"Config": [{"Subnet": "172.31.0.16/28"}]}}]
        with patch.object(runner.secrets, "randbelow", side_effect=[0, 1]), patch.object(runner.local, "command", side_effect=[runner.Failure("child-failed"), b"id", json.dumps(inspected).encode()]) as command:
            subnets = runner.reserve_networks(state, {})
            self.assertEqual({"default": "172.31.0.16/28"}, subnets)
            self.assertEqual(3, command.call_count)
        policy = runner.fixture_policy(subnets)
        self.assertEqual([], policy["public"]["origins"])
        private = policy["privateProfiles"][0]
        self.assertEqual(["http://api:8080"], private["origins"])
        self.assertEqual(["172.31.0.16/28"], private["addressRanges"])
        self.assertEqual(["00000000-0000-7000-8000-000000000001"], private["assignedTenantIds"])
    def test_reservation_is_bounded_and_never_adopts_existing_networks(self):
        state = {"project": "owned", "nonce": "nonce", "monitoring": True}
        for failure, calls in [("child-failed", 8), ("deadline", 1), ("interrupted-or-deadline", 1)]:
            with patch.object(runner.local, "command", side_effect=runner.Failure(failure)) as command:
                with self.assertRaises(runner.Failure):
                    runner.reserve_networks(state, {})
                self.assertEqual(calls, command.call_count)
        with patch.object(runner.local, "command", side_effect=[b"id", b'[{"Name":"foreign"}]']) as command:
            with self.assertRaisesRegex(runner.Failure, "inspection-failed"):
                runner.reserve_networks(state, {})
            self.assertEqual(2, command.call_count)
    def test_only_current_network_ownership_model_is_accepted(self):
        state = {"version": 2, "project": "owned", "nonce": "nonce", "monitoring": True}
        expected = {name: {"external": True, "name": "owned_" + name} for name in ["default", "monitoring"]}
        self.assertEqual(expected, runner.state_model(state)["networks"])
        for version in [1, 3, True, "2", None]:
            with patch.object(runner.local, "command") as command:
                with self.assertRaisesRegex(runner.Failure, "invalid-ownership-record"):
                    runner.state_model(state | {"version": version})
                command.assert_not_called()
    def test_static_model_check_rejects_retained_ipam(self):
        def rendered(arguments, *_args):
            monitored = str(runner.ROOT / "compose.monitoring.yaml") in arguments
            networks = runner.model("check", monitored, "open-agentic-catalog-performance-check")["networks"]
            networks["default"]["ipam"] = {}
            return json.dumps({"networks": networks}).encode()
        with patch.object(runner.local, "command", side_effect=rendered) as command:
            runner.check_models()
            self.assertEqual(2, command.call_count)
            self.assertTrue(all(call.args[0][-3:] == ["config", "--format", "json"] for call in command.call_args_list))
        bad = {"networks": runner.model("check", False, "open-agentic-catalog-performance-check")["networks"]}
        bad["networks"]["default"]["ipam"] = {"config": [{"subnet": "172.30.255.0/24"}]}
        with patch.object(runner.local, "command", return_value=json.dumps(bad).encode()):
            with self.assertRaisesRegex(runner.Failure, "network-model-mismatch"):
                runner.check_models()
    def test_monitored_reservations_verify_both_networks(self):
        state = {"project": "owned", "nonce": "nonce", "monitoring": True}
        pools = {}
        def command(arguments, *_args):
            name = arguments[-1]
            if arguments[2] == "create":
                pools[name] = arguments[arguments.index("--subnet") + 1]
                return b"id"
            return json.dumps([{"Name": name, "Driver": "bridge", "Labels": {runner.OWNER_LABEL: "nonce", "com.docker.compose.project": "owned"},
                                "IPAM": {"Config": [{"Subnet": pools[name]}]}}]).encode()
        with patch.object(runner.secrets, "randbelow", side_effect=[0, 4095]), patch.object(runner.local, "command", side_effect=command):
            subnets = runner.reserve_networks(state, {})
        self.assertEqual({"default": "172.31.0.0/28", "monitoring": "172.31.255.240/28"}, subnets)
        self.assertEqual(list(subnets.values()), runner.fixture_policy(subnets)["privateProfiles"][0]["addressRanges"])
    def test_metric_duplicates_and_malformed_fixed_samples_fail(self):
        baseline = b"open_agentic_catalog_go_heap_bytes 1\nopen_agentic_catalog_go_goroutines 2\n"
        for extra in [b"open_agentic_catalog_go_heap_bytes 1\n", b"open_agentic_catalog_go_heap_bytes\n", b"open_agentic_catalog_go_heap_bytes 1 extra\n"]:
            with patch.object(runner, "get", return_value=baseline + extra):
                with self.assertRaisesRegex(runner.Failure, "invalid-metric-shape"):
                    runner.metrics_sample("http://127.0.0.1:1")
    def test_workload_settings_are_bounded_and_recordable(self):
        self.assertEqual({"seconds": 30, "warmupSeconds": 5, "workers": 2, "maxRequests": 50000}, runner.workload_settings("smoke", {}))
        self.assertEqual(120, runner.workload_settings("monitoring", {})["seconds"])
        self.assertEqual(120, runner.workload_settings("monitoring", {"PERF_DURATION_SECONDS": ""})["seconds"])
        selected = runner.workload_settings("monitoring", {"PERF_DURATION_SECONDS": "180", "PERF_WARMUP_SECONDS": "10", "PERF_WORKERS": "8"})
        self.assertEqual({"seconds": 180, "warmupSeconds": 10, "workers": 8, "maxRequests": 144000}, selected)
        self.assertEqual(0, runner.workload_settings("smoke", {"PERF_WARMUP_SECONDS": "0", "PERF_WORKERS": "1"})["warmupSeconds"])
        for name, values in [("PERF_DURATION_SECONDS", ["29", "181", "30s", "-1", "1e2", "30\n"]),
                             ("PERF_WORKERS", ["0", "9", "", "2.0", "2;true"]),
                             ("PERF_WARMUP_SECONDS", ["-1", "11", "", "NaN"])]:
            for value in values:
                with patch.dict(os.environ, {name: value}, clear=True), patch.object(runner.local, "private_directory") as allocate, patch.object(runner, "engine_identity") as engine:
                    with self.assertRaises(runner.Failure):
                        runner.run("smoke")
                    allocate.assert_not_called()
                    engine.assert_not_called()
        with self.assertRaises(runner.Failure):
            runner.workload_settings("monitoring", {"PERF_DURATION_SECONDS": "119"})
        self.assertEqual({"seconds": 3600, "warmupSeconds": 5, "workers": 2, "maxRequests": 720000}, runner.workload_settings("soak", {}))
        for key, value in [("PERF_WORKERS", "8"), ("PERF_DURATION_SECONDS", "180"), ("PERF_WARMUP_SECONDS", "0")]:
            with self.assertRaisesRegex(runner.Failure, "soak-profile-is-fixed"):
                runner.workload_settings("soak", {key: value})
    def test_loopback_only(self):
        self.assertEqual("http://127.0.0.1:8080", runner.address(b"127.0.0.1:8080\n"))
        for value in [b"0.0.0.0:8080", b"foreign:8080", b"127.0.0.1:65536", b"127.0.0.1:0", b"127.0.0.1:8080\nforeign"]:
            with self.assertRaises(runner.Failure):
                runner.address(value)
    def test_closed_ownership(self):
        identifier = "a" * 24
        state = {"version": 2, "runId": identifier, "project": "open-agentic-catalog-perf-" + identifier, "image": "open-agentic-catalog:perf-" + identifier, "nonce": "b" * 24, "engine": "engine", "monitoring": False}
        runner.validate_state(state, identifier)
        runner.validate_state(state | {"version": 2}, identifier)
        for changes in [{"project": "foreign"}, {"image": "foreign"}, {"nonce": "bad"}, {"nonce": None}, {"engine": ""}, {"version": True}, {"version": 0}, {"version": 3}, {"runId": "b" * 24}, {"monitoring": "false"}, {"unknown": True}]:
            with self.assertRaises(runner.Failure):
                runner.validate_state(state | changes, identifier)
    def test_conflicting_profile_is_rejected_before_allocation(self):
        for name in ["LOCAL_MONITORING", "PERF_PROFILE"]:
            with patch.dict(os.environ, {name: "on"}), patch.object(runner.local, "private_directory") as allocate:
                with self.assertRaisesRegex(runner.Failure, "selected-by-target"):
                    runner.run("smoke")
                allocate.assert_not_called()
    def test_cleanup_has_an_independent_total_deadline(self):
        with patch.object(runner, "engine_identity", return_value="engine"), patch.object(runner.time, "monotonic", side_effect=[0, 91]), patch.object(runner.local, "command") as command:
            with self.assertRaisesRegex(runner.Failure, "cleanup-deadline"):
                runner.cleanup({"engine": "engine", "project": "owned"}, Path("unused"), {})
            command.assert_not_called()
    def test_report_write_failure_does_not_skip_cleanup(self):
        original = runner.local.safety.write_document
        def write(path, value):
            if path.name == "report.json":
                raise OSError("simulated-full-disk")
            original(path, value)
        with tempfile.TemporaryDirectory() as temporary, patch.object(runner, "ROOT", Path(temporary).resolve()), patch.object(runner, "engine_identity", return_value="engine"), patch.object(runner, "source_identity", side_effect=runner.Failure("source-failure")), patch.object(runner, "cleanup") as cleanup, patch.object(runner.local.safety, "write_document", side_effect=write), patch.dict(os.environ, {}, clear=True):
            with self.assertRaises(OSError):
                runner.run("smoke")
            cleanup.assert_called_once()
    def test_foreign_resource_refused_before_delete(self):
        state = {"project": "owned", "nonce": "expected"}
        with patch.object(runner.local, "command", side_effect=[b"container-id\n", json.dumps([{"Config": {"Labels": {runner.OWNER_LABEL: "foreign"}}}]).encode()]) as command:
            with self.assertRaisesRegex(runner.Failure, "foreign-resource"):
                runner.owned_resources(state, {})
            self.assertEqual(2, command.call_count)
    def test_cleanup_refuses_changed_engine(self):
        with patch.object(runner, "engine_identity", return_value="other"), patch.object(runner.local, "command") as command:
            with self.assertRaisesRegex(runner.Failure, "engine-identity"):
                runner.cleanup({"engine": "expected"}, Path("unused"), {})
            command.assert_not_called()
    def test_cleanup_reaps_a_late_daemon_create(self):
        empty = {"container": [], "network": [], "volume": []}
        late = empty | {"container": ["late-owned-container"]}
        state = {"engine": "engine", "project": "owned", "nonce": "nonce"}
        with patch.object(runner, "engine_identity", return_value="engine"), patch.object(runner, "owned_resources", side_effect=[empty, late, empty, empty]), patch.object(runner.time, "sleep"), patch.object(runner.local, "command", return_value=b"") as command:
            runner.cleanup(state, Path("unused"), {})
            self.assertIn(["docker", "container", "rm", "--force", "late-owned-container"], [call.args[0] for call in command.call_args_list])
    def test_model_has_exact_bounds_and_ownership(self):
        plain = runner.model("nonce", False, "owned")
        enabled = runner.model("nonce", True, "owned")
        self.assertNotIn("grafana", plain["services"])
        self.assertEqual("256m", plain["services"]["api"]["mem_limit"])
        self.assertEqual("512m", plain["services"]["postgres"]["mem_limit"])
        self.assertEqual("256m", plain["services"]["postgres"]["shm_size"])
        for values in [enabled["services"], enabled["volumes"]]:
            for value in values.values():
                self.assertEqual({runner.OWNER_LABEL: "nonce"}, value["labels"])
    def test_metric_failure_is_not_zero(self):
        for raw in [b"# only an error comment\n", b"open_agentic_catalog_go_heap_bytes NaN\n", b"open_agentic_catalog_go_heap_bytes 1\nopen_agentic_catalog_go_goroutines 2\nopen_agentic_catalog_operational_snapshot_available 0\n"]:
            with patch.object(runner, "get", return_value=raw):
                with self.assertRaises(runner.Failure):
                    runner.metrics_sample("http://127.0.0.1:1")
    def test_worker_requires_available_complete_snapshot(self):
        runtime = b"open_agentic_catalog_go_heap_bytes 1\nopen_agentic_catalog_go_goroutines 2\n"
        fields = [b"open_agentic_catalog_operational_snapshot_available 1\n", b"open_agentic_catalog_job_oldest_runnable_age_seconds 0\n", b"open_agentic_catalog_outbox_oldest_pending_age_seconds 0\n"]
        with patch.object(runner, "get", return_value=runtime):
            self.assertEqual(2, len(runner.metrics_sample("http://127.0.0.1:1")))
        with patch.object(runner, "get", return_value=runtime + b"".join(fields)):
            self.assertEqual(5, len(runner.metrics_sample("http://127.0.0.1:1", worker=True)))
        for missing in range(len(fields)):
            with patch.object(runner, "get", return_value=runtime + b"".join(value for index, value in enumerate(fields) if index != missing)):
                with self.assertRaisesRegex(runner.Failure, "required-metrics-missing"):
                    runner.metrics_sample("http://127.0.0.1:1", worker=True)
        for value in [b"0", b"NaN", b"+Inf"]:
            with patch.object(runner, "get", return_value=runtime + b"".join(fields).replace(b"available 1", b"available " + value)):
                with self.assertRaises(runner.Failure):
                    runner.metrics_sample("http://127.0.0.1:1", worker=True)
    def test_sampler_retains_all_emitted_job_states_and_excludes_private_samples(self):
        runtime = b"open_agentic_catalog_go_heap_bytes 1\nopen_agentic_catalog_go_goroutines 2\n"
        states = ["queued", "running", "waiting", "succeeded", "failed", "cancelled", "dead_letter"]
        expected = {f'open_agentic_catalog_jobs{{state="{state}"}}': index for index, state in enumerate(states, 1)}
        emitted = "".join(f"{name} {value}\n" for name, value in expected.items()).encode()
        unrelated = b'open_agentic_catalog_jobs{state="canceled"} 99\nopen_agentic_catalog_jobs{state="private"} invalid\nopen_agentic_catalog_jobs{state="queued",tenant="secret"} invalid\nother_private_metric invalid\n'
        worker_snapshot = b"open_agentic_catalog_operational_snapshot_available 1\nopen_agentic_catalog_job_oldest_runnable_age_seconds 0\nopen_agentic_catalog_outbox_oldest_pending_age_seconds 0\n"
        for worker in (False, True):
            with patch.object(runner, "get", return_value=runtime + emitted + unrelated + (worker_snapshot if worker else b"")):
                observed = runner.metrics_sample("http://127.0.0.1:1", worker=worker)
                self.assertEqual(expected, {key: value for key, value in observed.items() if "jobs{" in key})
                self.assertNotIn("secret", json.dumps(observed))
        name = 'open_agentic_catalog_jobs{state="cancelled"}'
        for tail in [f"{name} 8\n", f"{name} 1 2\n"]:
            with patch.object(runner, "get", return_value=runtime + emitted + tail.encode()), self.assertRaises(runner.Failure):
                runner.metrics_sample("http://127.0.0.1:1")
        for value in ["NaN", "Inf", "-1", "invalid"]:
            with patch.object(runner, "get", return_value=runtime + f"{name} {value}\n".encode()), self.assertRaises((runner.Failure, ValueError)):
                runner.metrics_sample("http://127.0.0.1:1")
    def test_stored_series_require_finite_positive_samples(self):
        self.assertTrue(runner.positive_samples([{"value": [1, "0.5"]}], 1))
        for value in ["NaN", "+Inf", "-Inf", "0", "-1", "invalid", None]:
            self.assertFalse(runner.positive_samples([{"value": [1, value]}], 1))
        for rows in [None, {}, [], [{}], [{"value": []}], [{"value": None}], [{"value": [1, "1"]}] * 2]:
            self.assertFalse(runner.positive_samples(rows, 1))
