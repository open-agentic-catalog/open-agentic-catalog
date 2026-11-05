"""Isolated ownership, command and evidence checks. No tool or service runs."""

import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("mcp_scanner_campaign", Path(__file__).with_name("campaign.py"))
campaign = importlib.util.module_from_spec(spec)
spec.loader.exec_module(campaign)


class CampaignUnits(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="catalog-scanner-unit-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        patcher = mock.patch.object(campaign, "OUTPUT", self.root)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.directory = self.root / "catalog-mcp-scan-012345abcdef"
        self.directory.mkdir()
        self.state = {"project": self.directory.name, "directory": str(self.directory), "api": "http://127.0.0.1:8080",
                      "receiverAddress": "172.29.0.1:9444", "completed": [], "cleanup": False}
        campaign.write(self.directory / "report.json", self.state)
        guard = mock.patch.object(campaign, "call", side_effect=AssertionError("unit attempted a process"))
        guard.start()
        self.addCleanup(guard.stop)

    def test_environment_excludes_ambient_secrets_and_remote_docker(self):
        with mock.patch.dict(os.environ, {"OPENAI_API_KEY": "secret", "CATALOG_DATABASE_DSN": "secret", "HTTP_PROXY": "secret",
                                         "DOCKER_CONTEXT": "remote", "DOCKER_HOST": "ssh://external", "CATALOG_ENV_FILE": "secret"}):
            env = campaign.environment()
        self.assertEqual(env["DOCKER_HOST"], "unix:///var/run/docker.sock")
        self.assertEqual(env["CATALOG_ENV_FILE"], "/dev/null")
        for name in ("OPENAI_API_KEY", "CATALOG_DATABASE_DSN", "HTTP_PROXY", "DOCKER_CONTEXT"):
            self.assertNotIn(name, env)

    def test_owner_rejects_arbitrary_names_paths_symlinks_and_changed_markers(self):
        self.assertEqual(campaign.owner(self.directory), self.state)
        for selected in (self.root, self.root / "other", self.root / "catalog-mcp-scan-abc"):
            with self.assertRaises(ValueError):
                campaign.owner(selected)
        linked = self.root / "catalog-mcp-scan-aaaaaaaaaaaa"
        linked.symlink_to(self.directory, target_is_directory=True)
        with self.assertRaises(ValueError):
            campaign.owner(linked)
        changed = dict(self.state, project="unrelated")
        campaign.write(self.directory / "report.json", changed)
        with self.assertRaises(ValueError):
            campaign.owner(self.directory)

    def test_commands_bind_private_inputs_without_credentials_in_arguments(self):
        args = campaign.receiver_args(self.directory, "serve")
        self.assertIn("--allow-loopback-http", args)
        self.assertEqual(args[args.index("--origin") + 1], self.state["api"])
        self.assertEqual(args[args.index("--listen") + 1], self.state["receiverAddress"])
        self.assertEqual(args[args.index("--scanner-runner") + 1], str(campaign.HERE / "runner.py"))
        for value in ("signer.json", "catalog-token", "receiver.dsn", "mapping.json"):
            self.assertIn(str(self.directory / "private/receiver" / value), args)
        self.assertNotIn("--context", args)

    def test_compose_uses_the_selected_project_and_private_env_file(self):
        with mock.patch.object(campaign, "call", return_value=b"ok") as child:
            self.assertEqual(campaign.compose(self.directory, "config", "--quiet"), b"ok")
        args = child.call_args.args[0]
        self.assertEqual(args[:2], ["docker", "compose"])
        self.assertEqual(args[args.index("--project-name") + 1], self.directory.name)
        self.assertEqual(args[args.index("--env-file") + 1], self.directory / "compose.env")
        self.assertEqual(args[-2:], ["config", "--quiet"])

    def test_case_completion_is_closed_and_unique(self):
        campaign.mark(self.directory, campaign.CASES[0])
        self.assertEqual(campaign.owner(self.directory)["completed"], [campaign.CASES[0]])
        for name in (campaign.CASES[0], "invented"):
            with self.assertRaises(ValueError):
                campaign.mark(self.directory, name)

    def test_stopping_a_cleaned_run_is_idempotent_and_runs_no_child(self):
        campaign.write(self.directory / "report.json", dict(self.state, cleanup=True))
        self.assertTrue(campaign.stop(self.directory))

    def test_interrupted_preparation_records_failure_and_cleans_owned_inputs(self):
        installed = {"version": "control", "imageId": "control", "rulesSha256": "control"}
        with mock.patch.object(campaign.scanner, "require_host"), mock.patch.object(campaign.scanner, "require_installed", return_value=installed), \
                mock.patch.object(campaign, "source_digest", return_value="control"), mock.patch.object(campaign.secrets, "token_hex", return_value="aaaaaaaaaaaa"), \
                mock.patch.object(campaign, "call", return_value=b""), mock.patch.object(campaign, "go_executable", side_effect=KeyboardInterrupt):
            with self.assertRaises(KeyboardInterrupt):
                campaign.prepare()
        directory = self.root / "catalog-mcp-scan-aaaaaaaaaaaa"
        state = campaign.owner(directory)
        self.assertEqual(state["phase"], "interrupted")
        self.assertFalse(state["success"])
        self.assertTrue(state["cleanup"])
        self.assertFalse((directory / "private").exists())

    def test_cleanup_refuses_an_image_outside_its_project(self):
        private = self.directory / "private"
        private.mkdir()
        campaign.write(self.directory / "report.json", dict(self.state, image="unrelated:keep", retained=[]))
        with mock.patch.object(campaign, "call", return_value=b"") as child:
            self.assertFalse(campaign.stop(self.directory))
        self.assertTrue(private.is_dir())
        self.assertEqual(campaign.owner(self.directory)["cleanupFailures"], ["image"])
        self.assertFalse(any("rm" in args.args[0] for args in child.call_args_list))

    def test_cleanup_retains_the_final_report_after_receiver_shutdown(self):
        resource = "00000000-0000-4000-8000-000000000002"
        reports = self.directory / "private/reports"
        reports.mkdir(parents=True)
        metadata = reports / (resource + ".json")
        campaign.write(metadata, {"resourceId": resource, "state": "pending"})
        campaign.write(self.directory / "report.json", dict(self.state, resource=resource, retained=[]))
        def finish_receiver(_directory):
            campaign.write(metadata, {"resourceId": resource, "state": "historical"})
        with mock.patch.object(campaign, "stop_receiver", side_effect=finish_receiver), mock.patch.object(campaign, "call", return_value=b""):
            self.assertTrue(campaign.stop(self.directory))
        retained = json.loads((self.directory / "evidence/last" / metadata.name).read_bytes())
        self.assertEqual(retained["state"], "historical")

    def test_cleanup_records_an_unavailable_container_inventory(self):
        private = self.directory / "private"
        private.mkdir()
        campaign.write(self.directory / "report.json", dict(self.state, retained=[]))
        with mock.patch.object(campaign, "call", side_effect=OSError("synthetic unavailable daemon")):
            self.assertFalse(campaign.stop(self.directory))
        self.assertTrue(private.is_dir())
        self.assertFalse(campaign.owner(self.directory)["cleanup"])

    def test_successful_cleanup_removes_resolved_cleanup_failures(self):
        campaign.write(self.directory / "report.json", dict(self.state, retained=[], cleanupFailures=["image"]))
        with mock.patch.object(campaign, "call", return_value=b""):
            self.assertTrue(campaign.stop(self.directory))
        self.assertNotIn("cleanupFailures", campaign.owner(self.directory))

    def test_private_writes_preserve_exact_bytes(self):
        path = self.directory / "evidence.json"
        original = b'{"exact":9007199254740993}\n'
        campaign.write(path, original)
        self.assertEqual(path.read_bytes(), original)
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        campaign.write(path, {"safe": True})
        self.assertEqual(json.loads(path.read_bytes()), {"safe": True})

    def test_failed_atomic_replacement_preserves_existing_evidence(self):
        path = self.directory / "evidence.json"
        campaign.write(path, b'{"original":true}\n', 0o640)
        with mock.patch.object(campaign.os, "replace", side_effect=OSError("synthetic replacement failure")):
            with self.assertRaises(OSError):
                campaign.write(path, b'{"replacement":true}\n')
        self.assertEqual(path.read_bytes(), b'{"original":true}\n')
        self.assertEqual(path.stat().st_mode & 0o777, 0o640)
        self.assertEqual(list(self.directory.glob(".mcp-scanner-*")), [])

    def test_private_input_cleanup_failure_remains_retryable(self):
        private = self.directory / "private"
        private.mkdir()
        campaign.write(self.directory / "report.json", dict(self.state, retained=[]))
        with mock.patch.object(campaign, "call", return_value=b""), mock.patch.object(campaign.shutil, "rmtree", side_effect=OSError("synthetic busy directory")):
            self.assertFalse(campaign.stop(self.directory))
        self.assertTrue(private.is_dir())
        self.assertEqual(campaign.owner(self.directory)["cleanupFailures"], ["private-inputs"])
        with mock.patch.object(campaign, "call", return_value=b""):
            self.assertTrue(campaign.stop(self.directory))
        self.assertFalse(private.exists())
        self.assertNotIn("cleanupFailures", campaign.owner(self.directory))


if __name__ == "__main__":
    unittest.main()
