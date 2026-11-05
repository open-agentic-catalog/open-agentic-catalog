"""Isolated topology decisions for the password-based local setup."""

import copy
import hashlib
import json
import os
from pathlib import Path
import tempfile
from unittest import TestCase, mock
import sys

from tutorial_test_helpers import load_example

with mock.patch.dict(sys.modules, {"compose": load_example("compose"), "prepare": load_example("prepare")}):
    local_auth = load_example("local_auth")


class LocalAuthenticationTopologyTests(TestCase):
    def topology(self):
        services = {name: {} for name in (*sorted(local_auth.SERVICES), "api-b", "worker-b", "postgres-b", "owner-b", "ui-b")}
        services["frontend"] = {
            "ports": [{"target": port} for port in (8443, 8445, 9443, 9445)],
            "volumes": [{"target": "/tls/" + name + ".pem"} for name in ("catalog-a", "catalog-b", "browse-a", "browse-b")],
            "networks": {"tutorial": {"aliases": ["catalog-a.test", "catalog-b.test"]}},
        }
        return {"services": services, "volumes": {"postgres-a": {}, "postgres-b": {}, "identity": {}}}

    def test_single_topology_excludes_second_catalog_and_preserves_original(self):
        original = self.topology()
        before = copy.deepcopy(original)
        selected = local_auth.single_configuration(original)
        self.assertEqual(original, before)
        self.assertEqual(set(selected["services"]), local_auth.SERVICES)
        self.assertEqual(set(selected["volumes"]), {"postgres-a", "identity"})
        frontend = selected["services"]["frontend"]
        self.assertEqual(frontend["ports"], [{"target": 8443}, {"target": 9443}])
        self.assertEqual(frontend["volumes"], [{"target": "/tls/catalog-a.pem"}, {"target": "/tls/browse-a.pem"}])
        self.assertEqual(frontend["environment"], {"CATALOG_TUTORIAL_SINGLE": "1"})
        self.assertEqual(frontend["networks"]["tutorial"]["aliases"], ["catalog-a.test", "browse-a.test"])
        self.assertEqual(selected["services"]["api-a"]["stop_grace_period"], "40s")
        self.assertEqual(selected["services"]["worker-a"]["stop_grace_period"], "40s")

    def test_missing_required_service_fails_closed(self):
        original = self.topology()
        del original["services"]["identity"]
        with self.assertRaisesRegex(ValueError, "incomplete"):
            local_auth.single_configuration(original)

    def startup_commands(self, count):
        calls = []

        def execute(command, environment):
            calls.append(command)
            return count if "psql" in command else b""

        with mock.patch.object(local_auth, "preflight"), mock.patch.object(local_auth, "run", side_effect=execute), \
                mock.patch.object(local_auth, "wait_for"), mock.patch.object(local_auth, "private_json", return_value={"catalog-admin": "11111111-1111-4111-8111-111111111111", "catalog-reader": "22222222-2222-4222-8222-222222222222"}), \
                mock.patch("builtins.print"):
            local_auth.start(local_auth.Path("/not-read"), ["compose"], {})
        return calls

    def test_existing_membership_never_reprovisions_or_restores_grants(self):
        calls = self.startup_commands(b"1")
        self.assertFalse(any("provision" in command for command in calls))
        stop_api = calls.index(["compose", "stop", "api-a"])
        stop_worker = calls.index(["compose", "stop", "worker-a"])
        migration = next(index for index, command in enumerate(calls) if "migrate" in command)
        self.assertLess(stop_api, stop_worker)
        self.assertLess(stop_worker, migration)
        self.assertTrue(any("m.actor_type='human'" in argument and "r.issuer='https://identity.test:8447/realms/catalog'" in argument
                            for command in calls for argument in command))

    def test_absent_membership_bootstraps_only_administrator(self):
        calls = self.startup_commands(b"0")
        provisioning = [command for command in calls if "provision" in command]
        self.assertEqual(len(provisioning), 1)
        self.assertIn("tenant_admin", provisioning[0])
        self.assertNotIn("reader", provisioning[0])
        self.assertNotIn("--password", provisioning[0])

    def test_ambiguous_membership_stops_startup(self):
        with self.assertRaisesRegex(ValueError, "ambiguous"):
            self.startup_commands(b"2")

    def test_remote_docker_is_rejected_before_commands(self):
        with mock.patch.object(local_auth, "run") as execute:
            with self.assertRaisesRegex(ValueError, "local Docker"):
                local_auth.local_engine({"DOCKER_HOST": "ssh://example.invalid"})
            execute.assert_not_called()

    def test_explicit_context_takes_precedence_over_docker_host(self):
        with mock.patch.object(local_auth, "run", return_value=b'[{"Endpoints":{"docker":{"Host":"unix:///local.sock"}}}]') as execute:
            environment = {"DOCKER_CONTEXT": "local", "DOCKER_HOST": "ssh://example.invalid"}
            local_auth.local_engine(environment)
            execute.assert_called_once_with(["docker", "context", "inspect", "local"], environment)


    def test_missing_image_reports_service_before_startup(self):
        configuration = {"services": {name: {"image": "unused"} for name in local_auth.SERVICES}}
        with mock.patch.object(local_auth, "private_json", return_value=configuration), \
                mock.patch.object(local_auth, "prepared_accounts"), \
                mock.patch.object(local_auth, "run", side_effect=[b"local-engine", ValueError("command failed")]) as execute:
            with self.assertRaisesRegex(ValueError, "local image for api-a is missing or unreadable"):
                local_auth.preflight(local_auth.Path("/not-read"), ["compose"], {})
            self.assertEqual(execute.call_count, 2)
            self.assertEqual(execute.call_args.args[0], ["docker", "image", "inspect", "unused"])

    def test_occupied_port_reports_port_before_startup(self):
        configuration = {"services": {name: {"image": "unused"} for name in local_auth.SERVICES}}
        with mock.patch.object(local_auth, "private_json", return_value=configuration), \
                mock.patch.object(local_auth, "prepared_accounts"), mock.patch.object(local_auth, "run", return_value=b"") as execute, \
                mock.patch.object(local_auth.socket, "socket") as socket:
            socket.return_value.__enter__.return_value.bind.side_effect = OSError("address in use")
            with self.assertRaisesRegex(ValueError, "Local port 8447 is unavailable"):
                local_auth.preflight(local_auth.Path("/not-read"), ["compose"], {})
            socket.return_value.__enter__.return_value.bind.assert_called_once_with(("127.0.0.1", 8447))
            self.assertFalse(any("up" in call.args[0] for call in execute.call_args_list))


class LocalAuthenticationInputTests(TestCase):
    def test_private_json_rejects_wrong_mode_shape_symlink_and_oversize(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "input.json"
            path.write_text('{"valid":true}')
            path.chmod(0o600)
            self.assertEqual(local_auth.private_json(path), {"valid": True})
            path.chmod(0o644)
            with self.assertRaisesRegex(ValueError, "0600"):
                local_auth.private_json(path)
            path.chmod(0o600)
            for invalid in ("[]", "null", "{", " " * 1048577):
                path.write_text(invalid)
                with self.assertRaises(ValueError):
                    local_auth.private_json(path)
            link = Path(temporary) / "linked.json"
            link.symlink_to(path)
            with self.assertRaisesRegex(ValueError, "0600"):
                local_auth.private_json(link)

    def test_selection_uses_prepared_project_despite_ambient_compose_inputs(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            configuration = directory / "compose.single.json"
            configuration.write_text('{"services":{}}')
            configuration.chmod(0o600)
            marker = directory / "single.json"
            marker.write_text(json.dumps({"format": 1, "ready": True,
                                          "sha256": hashlib.sha256(configuration.read_bytes()).hexdigest()}))
            marker.chmod(0o600)
            prepared = {"COMPOSE_PROJECT_NAME": "catalog-docs-12345678", "CATALOG_TUTORIAL_DIRECTORY": temporary}
            with mock.patch.object(local_auth.compose, "prepared_inputs", return_value=(directory, prepared)), \
                    mock.patch.dict(os.environ, {"COMPOSE_PROJECT_NAME": "unrelated", "COMPOSE_FILE": "/unrelated",
                                                 "BUILDKIT_HOST": "ssh://unrelated", "BUILDX_BUILDER": "unrelated"}):
                root, command, environment = local_auth.selection(directory)
            self.assertEqual(root, directory)
            self.assertEqual(command, ["docker", "compose", "--project-name", "catalog-docs-12345678", "--env-file",
                                       str(directory / "compose.env"), "--file", str(configuration)])
            self.assertEqual(environment["COMPOSE_PROJECT_NAME"], prepared["COMPOSE_PROJECT_NAME"])
            self.assertEqual(environment["BUILDX_BUILDER"], "default")
            self.assertNotIn("BUILDKIT_HOST", environment)
            configuration.write_text('{"services":{"changed":{}}}')
            with mock.patch.object(local_auth.compose, "prepared_inputs", return_value=(directory, prepared)):
                with self.assertRaisesRegex(ValueError, "changed"):
                    local_auth.selection(directory)

    def test_duplicate_account_subjects_are_rejected(self):
        subject = "11111111-1111-4111-8111-111111111111"
        with mock.patch.object(local_auth, "private_json", return_value={"catalog-admin": subject, "catalog-reader": subject}):
            with self.assertRaisesRegex(ValueError, "different subjects"):
                local_auth.prepared_accounts(Path("/not-read"))

    def test_wait_is_bounded_and_does_not_hide_permanent_errors(self):
        action = mock.Mock(side_effect=OSError("unavailable"))
        with mock.patch.object(local_auth.time, "monotonic", side_effect=[0, 181]), mock.patch.object(local_auth.time, "sleep") as sleep:
            with self.assertRaisesRegex(ValueError, "180 seconds"):
                local_auth.wait_for(action)
            action.assert_called_once()
            sleep.assert_not_called()
        with self.assertRaisesRegex(RuntimeError, "permanent"):
            local_auth.wait_for(mock.Mock(side_effect=RuntimeError("permanent")))
