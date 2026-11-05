import fcntl
import json
import unittest
from unittest.mock import patch
import runner
import recovery_test_helpers as helpers


class SupervisorRecovery(helpers.SupervisorRecoveryFixture, unittest.TestCase):
    def test_reaper_retains_failed_report_and_removes_only_generated_artifacts(self):
        directory = self.record()
        for name in ["grafana-password", "load-client", "keep-me"]:
            (directory / name).write_text("test-only")
        with patch.object(runner, "cleanup") as cleanup:
            runner.clean(self.run_id)
            cleanup.assert_called_once()
            self.assertEqual((self.state, directory), cleanup.call_args.args[:2])
        self.assertEqual({"status": "incomplete", "cleanup": "passed"}, runner.local.safety.read_document(directory / "report.json"))
        for name in ["grafana-password", "load-client"]:
            self.assertFalse((directory / name).exists())
        for name in ["owner.lock", "ownership.json", "owned-compose.json", "keep-me"]:
            self.assertTrue((directory / name).is_file())
    def test_reaper_supports_reserved_network_record(self):
        self.state["version"] = 2
        self.record()
        with patch.object(runner, "cleanup") as cleanup:
            runner.clean(self.run_id)
            cleanup.assert_called_once()
    def test_reaper_refuses_live_owner_and_tampered_model_before_cleanup(self):
        directory = self.record()
        with (directory / "owner.lock").open("r+") as lock, patch.object(runner, "cleanup") as cleanup:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            with self.assertRaisesRegex(runner.Failure, "run-still-active"):
                runner.clean(self.run_id)
            cleanup.assert_not_called()
        runner.local.safety.write_document(directory / "owned-compose.json", {})
        with patch.object(runner, "cleanup") as cleanup:
            with self.assertRaisesRegex(runner.Failure, "owned-model-changed"):
                runner.clean(self.run_id)
            cleanup.assert_not_called()
    def test_reaper_refuses_missing_and_symlinked_records(self):
        with patch.object(runner, "cleanup") as cleanup:
            for value in ["", "../foreign", "F" * 24]:
                with self.assertRaisesRegex(runner.Failure, "invalid-run-id"):
                    runner.clean(value)
            with self.assertRaisesRegex(runner.Failure, "missing-run-record"):
                runner.clean(self.run_id)
            directory = self.record()
            target = directory / "ownership.json"
            original = target.read_bytes()
            target.unlink()
            with self.assertRaisesRegex(runner.Failure, "unsafe-run-record"):
                runner.clean(self.run_id)
            foreign = self.root / "foreign.json"
            foreign.write_bytes(original)
            target.symlink_to(foreign)
            with self.assertRaisesRegex(runner.Failure, "unsafe-run-record"):
                runner.clean(self.run_id)
            self.assertEqual(original, foreign.read_bytes())
            cleanup.assert_not_called()
    def test_reaper_never_unlinks_artifact_symlink_target(self):
        directory = self.record()
        foreign = self.root / "foreign"
        foreign.write_text("preserve")
        (directory / "load-client").symlink_to(foreign)
        with patch.object(runner, "cleanup"):
            with self.assertRaisesRegex(runner.Failure, "unsafe-artifact"):
                runner.clean(self.run_id)
        self.assertEqual("preserve", foreign.read_text())
    def test_benchmark_keeps_incomplete_evidence_on_failure_or_source_drift(self):
        sample = b"BenchmarkRuntimeCollection-2 10 100 ns/op 1 B/op 1 allocs/op\n"
        complete = sample * 10 + sample.replace(b"Collection", b"Exposition") * 10
        for name, raw, identities in [("success", complete, ["source", "source"]), ("missing-samples", sample * 11, ["source"]), ("unbalanced", sample * 12, ["source"]), ("drift", complete, ["source", "changed"])]:
            with self.subTest(name=name), patch.object(runner, "source_identity", side_effect=identities), patch.object(runner.local, "command", return_value=raw), patch.object(runner.benchmarks, "metadata", return_value={"owned": True}):
                before = set((self.root / "coverage/performance").glob("bench-*/report.json"))
                if name == "success":
                    runner.benchmark()
                else:
                    with self.assertRaisesRegex(runner.Failure, "samples-incomplete-or-source-changed"):
                        runner.benchmark()
                after = set((self.root / "coverage/performance").glob("bench-*/report.json"))
                self.assertEqual(1, len(after - before))
                report = runner.local.safety.read_document((after - before).pop())
                self.assertEqual("passed-development-diagnostic" if name == "success" else "incomplete", report["status"])
    def test_cleanup_deletes_only_inspected_owned_resources_and_image(self):
        empty = {"container": [], "volume": [], "network": []}
        full = {"container": ["owned-container"], "volume": ["owned-volume"], "network": ["owned-network"]}
        def command(args, environment, seconds=15, capture=False):
            if args[1:3] == ["image", "ls"]:
                return b"owned-image\n"
            if args[1:3] == ["image", "inspect"]:
                return json.dumps([{"Config": {"Labels": {runner.OWNER_LABEL: self.state["nonce"]}}}]).encode()
            return b""
        with patch.object(runner, "engine_identity", return_value="engine"), patch.object(runner, "owned_resources", side_effect=[full, empty, empty]), patch.object(runner.time, "sleep"), patch.object(runner.local, "command", side_effect=command) as execute:
            runner.cleanup(self.state, self.root, {})
            deletions = [call.args[0] for call in execute.call_args_list if "rm" in call.args[0]]
            self.assertEqual([
                ["docker", "container", "rm", "--force", "owned-container"],
                ["docker", "network", "rm", "owned-network"],
                ["docker", "volume", "rm", "owned-volume"],
                ["docker", "image", "rm", self.state["image"]],
            ], deletions)
    def test_foreign_image_is_never_removed(self):
        empty = {"container": [], "volume": [], "network": []}
        with patch.object(runner, "engine_identity", return_value="engine"), patch.object(runner, "owned_resources", return_value=empty), patch.object(runner.time, "sleep"), patch.object(runner.local, "command", side_effect=[b"image\n", b'[{"Config":{"Labels":{}}}]']) as command:
            with self.assertRaisesRegex(runner.Failure, "foreign-image"):
                runner.cleanup(self.state, self.root, {})
            self.assertTrue(all("rm" not in call.args[0] for call in command.call_args_list))
    def test_owned_inventory_checks_every_resource_class_and_bounds_count(self):
        outputs = []
        for kind in ["container", "volume", "network"]:
            labels = {runner.OWNER_LABEL: self.state["nonce"]}
            info = {"Config": {"Labels": labels}} if kind == "container" else {"Labels": labels}
            outputs.extend([kind.encode() + b"\n", json.dumps([info]).encode()])
        with patch.object(runner.local, "command", side_effect=outputs):
            self.assertEqual({kind: [kind] for kind in ["container", "volume", "network"]}, runner.owned_resources(self.state, {}))
        with patch.object(runner.local, "command", return_value=b"id\n" * 21) as command:
            with self.assertRaisesRegex(runner.Failure, "resource-count-limit"):
                runner.owned_resources(self.state, {})
            self.assertEqual(1, command.call_count)
    def test_http_readiness_and_metric_bounds(self):
        with patch.object(runner.http, "request", return_value=(200, {}, b"ready")) as request:
            self.assertEqual(b"ready", runner.get("http://127.0.0.1:1", "test-only-password"))
            self.assertTrue(request.call_args.args[0].get_header("Authorization").startswith("Basic "))
        with patch.object(runner.http, "request", return_value=(503, {}, b"private detail")):
            with self.assertRaisesRegex(runner.Failure, "unexpected-http-status"):
                runner.get("http://127.0.0.1:1")
        with patch.object(runner, "get", side_effect=[runner.Failure("starting"), b"ready"]), patch.object(runner.time, "sleep"):
            self.assertEqual(b"ready", runner.wait_http("http://127.0.0.1:1"))
        with patch.object(runner, "get") as get, patch.object(runner.time, "monotonic", side_effect=[0, 61]):
            with self.assertRaisesRegex(runner.Failure, "readiness-deadline"):
                runner.wait_http("http://127.0.0.1:1")
            get.assert_not_called()
        with patch.object(runner, "get", return_value=b"x" * ((1 << 20) + 1)):
            with self.assertRaisesRegex(runner.Failure, "metrics-limit"):
                runner.metrics_sample("http://127.0.0.1:1")
    def test_source_identity_is_order_independent_and_detects_edits(self):
        (self.root / "first").write_text("first")
        (self.root / "second").write_text("second")
        with patch.object(runner.local, "command", side_effect=[b"commit\n", b"second\0first\0first\0"]):
            original = runner.source_identity({})
        with patch.object(runner.local, "command", side_effect=[b"commit\n", b"first\0second\0"]):
            self.assertEqual(original, runner.source_identity({}))
        (self.root / "second").write_text("changed")
        with patch.object(runner.local, "command", side_effect=[b"commit\n", b"first\0second\0"]):
            changed = runner.source_identity({})
        self.assertEqual(original["commit"], changed["commit"])
        self.assertNotEqual(original["sourceDigest"], changed["sourceDigest"])
        (self.root / "link").symlink_to(self.root / "first")
        for name in [b"link\0"]:
            with patch.object(runner.local, "command", side_effect=[b"commit\n", name]):
                with self.assertRaisesRegex(runner.Failure, "unsafe-source-input"):
                    runner.source_identity({})
    def test_source_identity_records_deletions_and_detects_reappearance(self):
        with patch.object(runner.local, "command", side_effect=[b"commit\n", b"deleted\0"]):
            deleted = runner.source_identity({})
        (self.root / "deleted").write_text("restored")
        with patch.object(runner.local, "command", side_effect=[b"commit\n", b"deleted\0"]):
            restored = runner.source_identity({})
        self.assertNotEqual(deleted["sourceDigest"], restored["sourceDigest"])
    def test_engine_identity_must_be_present_and_bounded(self):
        for raw in [b"", b"x" * 257]:
            with patch.object(runner.local, "command", return_value=raw), patch.object(runner.benchmarks, "metadata", return_value={"owned": True}):
                with self.assertRaisesRegex(runner.Failure, "missing-engine-identity"):
                    runner.engine_identity({})
        with patch.object(runner.local, "command", return_value=b"owned-engine\n"):
            self.assertEqual("owned-engine", runner.engine_identity({}))
