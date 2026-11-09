"""Isolated ownership, command and evidence checks. No tool or service runs."""

import importlib.util
import copy
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

    def test_stop_deferral_restores_the_prior_mask_after_failure(self):
        previous = {campaign.signal.SIGINT}
        with mock.patch.object(campaign.relay.signal, "pthread_sigmask", return_value=previous) as mask:
            with self.assertRaisesRegex(ValueError, "owned startup failure"):
                with campaign.relay.defer_stop():
                    raise ValueError("owned startup failure")
        self.assertEqual(mask.call_args_list, [mock.call(campaign.signal.SIG_BLOCK, {campaign.signal.SIGINT, campaign.signal.SIGTERM}),
                                              mock.call(campaign.signal.SIG_SETMASK, previous)])

    def test_process_record_failure_reaps_each_owned_role(self):
        import subprocess
        (self.directory / "private").mkdir()
        original_write = campaign.write
        for role, timeout in (("relay", 8), ("receiver", 80)):
            campaign.write(self.directory / "report.json", self.state)
            child = mock.Mock(pid=1234)
            child.poll.return_value = None
            def write(path, value, *args):
                if value.get(role + "PID"):
                    raise OSError("owned PID record failed")
                original_write(path, value, *args)
            with self.subTest(role=role), mock.patch.object(subprocess, "Popen", return_value=child), \
                    mock.patch.object(campaign, "process_identity", return_value="owned-start"), \
                    mock.patch.object(campaign.relay.signal, "pthread_sigmask", return_value=set()), \
                    mock.patch.object(campaign, "write", side_effect=write), self.assertRaisesRegex(OSError, "owned PID record failed"):
                campaign.start_process(self.directory, role, ["owned-executable"])
            child.terminate.assert_called_once()
            child.wait.assert_called_once_with(timeout=timeout)

    def test_process_roles_cannot_select_another_log_or_spawn(self):
        import subprocess
        with mock.patch.object(subprocess, "Popen", side_effect=AssertionError("unit attempted a process")) as child:
            for role in ("", "other", "../outside"):
                with self.subTest(role=role), self.assertRaises(ValueError):
                    campaign.start_process(self.directory, role, ["unexecuted"])
            child.assert_not_called()
            with mock.patch.object(campaign.relay.threading, "active_count", return_value=2), self.assertRaisesRegex(ValueError, "single-threaded"):
                campaign.start_process(self.directory, "relay", ["unexecuted"])
            child.assert_not_called()

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

    def test_relay_targets_are_complete_distinct_fixed_service_addresses(self):
        values = {"api": {"address": "172.29.1.2", "port": 8080}, "mcp": {"address": "172.29.1.3", "port": 9443},
                  "postgres": {"address": "172.29.1.4", "port": 5432}}
        self.assertEqual(campaign.relay.targets("172.29.1.0/28", values),
                         {"api": ("172.29.1.2", 8080), "mcp": ("172.29.1.3", 9443), "postgres": ("172.29.1.4", 5432)})
        for subnet in ("0.0.0.0/0", "192.0.2.0/28", "172.29.1.0/27", "::1/128", None):
            with self.subTest(subnet=subnet), self.assertRaises(ValueError):
                campaign.relay.targets(subnet, values)
        for address in ("127.0.0.1", "0.0.0.0", "172.29.1.0", "172.29.1.1", "172.29.1.15", "172.29.2.2", "172.29.1.3", "peer.test", "::1", 42):
            changed = copy.deepcopy(values)
            changed["api"]["address"] = address
            with self.subTest(address=address), self.assertRaises(ValueError):
                campaign.relay.targets("172.29.1.0/28", changed)
        for changed in ({}, {**values, "other": values["api"]}, {**values, "api": {"address": "172.29.1.2", "port": 80}},
                        {**values, "api": {**values["api"], "proxy": "http://uncontacted.invalid"}}):
            with self.subTest(values=changed), self.assertRaises(ValueError):
                campaign.relay.targets("172.29.1.0/28", changed)

    def test_relay_readiness_requires_current_process_and_actual_loopback_ports(self):
        state = dict(self.state, relayPID=1234, relayStart="owned-start")
        campaign.write(self.directory / "report.json", state)
        (self.directory / "private").mkdir()
        ready = {"schemaVersion": 1, "pid": 1234, "listeners": {"api": "127.0.0.1:1111", "mcp": "127.0.0.1:2222", "postgres": "127.0.0.1:3333"}}
        def check(value, identity="owned-start"):
            campaign.write(self.directory / "private/relay-ready.json", value)
            with mock.patch.object(campaign, "process_identity", return_value=identity):
                return campaign.relay_addresses(self.directory)
        self.assertEqual(check(ready), ready["listeners"])
        for address in (":0", "127.0.0.1:0", "127.0.0.1:65536", "0.0.0.0:1111", "localhost:1111", "172.29.1.2:1111", "[::1]:1111", "127.0.0.1:2222", None):
            with self.subTest(address=address), self.assertRaises(ValueError):
                check({**ready, "listeners": {**ready["listeners"], "api": address}})
        for changed in ({**ready, "pid": 5678}, {**ready, "schemaVersion": True}, {**ready, "listeners": {}}, {**ready, "extra": True}):
            with self.subTest(ready=changed), self.assertRaises(ValueError):
                check(changed)
        with self.assertRaises(ValueError):
            check(ready, "reused-pid")

    def test_relay_target_selection_refuses_foreign_networks_services_and_publication(self):
        state = dict(self.state, subnet="172.29.1.0/28", gateway="172.29.1.1")
        campaign.write(self.directory / "report.json", state)
        name = state["project"] + "_owned"
        ids = {"api": "a" * 64, "mcp": "b" * 64, "postgres": "c" * 64}
        expected = {"api": {"address": "172.29.1.2", "port": 8080}, "mcp": {"address": "172.29.1.3", "port": 9443},
                    "postgres": {"address": "172.29.1.4", "port": 5432}}
        network = {"Id": "d" * 64, "Driver": "bridge", "Internal": True, "Labels": {"com.docker.compose.project": state["project"]},
                   "IPAM": {"Config": [{"Subnet": state["subnet"], "Gateway": state["gateway"]}]},
                   "Containers": {ids[service]: {"IPv4Address": target["address"] + "/28"} for service, target in expected.items()}}
        containers = {cid: {"Id": cid, "State": {"Running": True}, "Config": {"Labels": {"com.docker.compose.project": state["project"], "com.docker.compose.service": service}},
                            "HostConfig": {"PortBindings": {}}, "NetworkSettings": {"Ports": {"8080/tcp": None},
                                "Networks": {name: {"NetworkID": network["Id"], "IPAddress": expected[service]["address"]}}}}
                      for service, cid in ids.items()}
        def check(selected_network, selected_containers):
            def child(argv):
                if argv == ["docker", "network", "inspect", name]:
                    return json.dumps([selected_network]).encode()
                self.assertEqual(argv[:2], ["docker", "inspect"])
                return json.dumps([selected_containers[argv[2]]]).encode()
            with mock.patch.object(campaign, "compose", side_effect=lambda _directory, *args: ids[args[-1]].encode()), \
                    mock.patch.object(campaign, "call", side_effect=child):
                return campaign.service_targets(self.directory)
        self.assertEqual(check(network, containers), expected)
        for field, value in (("Id", "e" * 64), ("State", {"Running": False}),
                             ("Config", {"Labels": {"com.docker.compose.project": "foreign", "com.docker.compose.service": "api"}}),
                             ("HostConfig", {"PortBindings": {"8080/tcp": [{"HostIp": "127.0.0.1", "HostPort": "1111"}]}}),
                             ("NetworkSettings", {"Networks": {"foreign": {}}, "Ports": {}}),
                             ("NetworkSettings", {"Networks": {name: {"NetworkID": "wrong", "IPAddress": "172.29.1.2"}}, "Ports": {}}),
                             ("NetworkSettings", {"Networks": {name: {"NetworkID": network["Id"], "IPAddress": "172.29.1.2"}}, "Ports": {"8080/tcp": [{"HostIp": "0.0.0.0", "HostPort": "1111"}]}})):
            changed = copy.deepcopy(containers)
            changed[ids["api"]][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                check(network, changed)
        for field, value in (("Internal", False), ("Driver", "other"), ("Labels", {"com.docker.compose.project": "foreign"}),
                             ("IPAM", {"Config": [{"Subnet": "172.29.2.0/28", "Gateway": "172.29.2.1"}]}), ("Containers", {})):
            with self.subTest(field=field), self.assertRaises(ValueError):
                check({**network, field: value}, containers)

    def test_failure_at_startup_is_not_mislabeled_as_a_network_assertion(self):
        def cleanup(_directory):
            state = campaign.owner(self.directory)
            campaign.write(self.directory / "report.json", dict(state, cleanup=True))
            return True
        for stage, completed in (("start", []), ("test", [campaign.CASES[0]])):
            campaign.write(self.directory / "report.json", dict(self.state, completed=completed))
            with self.subTest(stage=stage), mock.patch.object(campaign.signal, "signal"), \
                    mock.patch.object(campaign.sys, "argv", ["campaign.py", stage, "--directory", str(self.directory)]), \
                    mock.patch.object(campaign, "start", side_effect=ValueError("synthetic startup failure")), \
                    mock.patch.object(campaign, "qualify", side_effect=AssertionError("synthetic assertion failure")), \
                    mock.patch.object(campaign, "stop", side_effect=cleanup):
                self.assertEqual(campaign.main(), 1)
            saved = campaign.owner(self.directory)
            self.assertEqual(saved["failureStage"], stage)
            if stage == "start":
                self.assertNotIn("failedCase", saved)
            else:
                self.assertEqual(saved["failedCase"], campaign.CASES[1])
            self.assertTrue(saved["cleanup"])

    def test_compose_uses_the_selected_project_and_private_env_file(self):
        with mock.patch.object(campaign, "call", return_value=b"ok") as child:
            self.assertEqual(campaign.compose(self.directory, "config", "--quiet"), b"ok")
        args = child.call_args.args[0]
        self.assertEqual(args[:2], ["docker", "compose"])
        self.assertEqual(args[args.index("--project-name") + 1], self.directory.name)
        self.assertEqual(args[args.index("--env-file") + 1], self.directory / "compose.env")
        self.assertEqual(args[-2:], ["config", "--quiet"])

    def test_sql_keeps_timeout_separate_from_database_creation(self):
        for statement in ("CREATE DATABASE owned_receiver", "SELECT 1; SELECT 2"):
            with self.subTest(statement=statement), mock.patch.object(campaign, "compose", return_value=b"result\n") as child:
                self.assertEqual(campaign.sql(self.directory, statement, "receiver"), "result")
            child.assert_called_once()
            args = child.call_args.args[1:]
            commands = [args[index + 1] for index, value in enumerate(args) if value in ("-c", "-Atc", "--command")]
            self.assertEqual(commands, ["SET statement_timeout='5s'", statement])
            self.assertEqual(args[args.index("-d") + 1], "receiver")
            self.assertIn("ON_ERROR_STOP=1", args)

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

    def test_relay_cleanup_failure_preserves_private_inputs_until_successful_stop(self):
        (self.directory / "private").mkdir()
        campaign.write(self.directory / "report.json", dict(self.state, retained=[]))
        with mock.patch.object(campaign, "call", return_value=b""), mock.patch.object(campaign, "stop_relays", side_effect=ValueError("owned relay refusal")):
            self.assertFalse(campaign.stop(self.directory))
        self.assertEqual(campaign.owner(self.directory)["cleanupFailures"], ["relay"])
        self.assertTrue((self.directory / "private").is_dir())
        with mock.patch.object(campaign, "call", return_value=b""):
            self.assertTrue(campaign.stop(self.directory))
        self.assertNotIn("cleanupFailures", campaign.owner(self.directory))
        self.assertFalse((self.directory / "private").exists())

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
