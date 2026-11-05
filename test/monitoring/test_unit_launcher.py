import os
import io
import json
from pathlib import Path
import tempfile
import traceback
import unittest
from unittest.mock import patch
import launcher


class MonitoringSafety(unittest.TestCase):
    def test_embedding_lifecycle_includes_existing_service_without_cache_on_down(self):
        with patch.dict(os.environ, {}, clear=True), patch.object(launcher, "command", return_value=b"embeddings\n") as command, patch.object(launcher, "embedding_inputs", side_effect=lambda environment, verify: environment) as inputs:
            launcher.development("down", "catalog", "off")
            self.assertFalse(inputs.call_args.kwargs["verify"])
            self.assertIn(str(launcher.ROOT / "compose.embeddings.yaml"), command.call_args.args[0])
            self.assertNotIn("--volumes", command.call_args.args[0])
    def test_embedding_activation_requires_verified_cache(self):
        with patch.dict(os.environ, {"LOCAL_EMBEDDINGS": "on"}, clear=True), patch.object(launcher, "command", return_value=b""), patch.object(launcher, "embedding_inputs", side_effect=ValueError("cache mismatch")) as inputs:
            with self.assertRaisesRegex(ValueError, "cache mismatch"):
                launcher.development("up", "catalog", "off")
            self.assertTrue(inputs.call_args.kwargs["verify"])
    def test_existing_embedding_is_not_silently_orphaned(self):
        with patch.dict(os.environ, {}, clear=True), patch.object(launcher, "command", return_value=b"embeddings\n") as command:
            with self.assertRaisesRegex(launcher.Failure, "embeddings-already-enabled"):
                launcher.development("up", "catalog", "off")
            self.assertEqual(1, command.call_count)
    def test_local_webhooks_default_and_explicit_overrides(self):
        for settings, expected in [({}, "true"), ({"LOCAL_WEBHOOKS": "off"}, "false"),
                                   ({"CATALOG_WEBHOOKS_ENABLED": "false"}, "false"),
                                   ({"LOCAL_WEBHOOKS": "off", "CATALOG_WEBHOOKS_ENABLED": "true"}, "true")]:
            with self.subTest(settings=settings), patch.dict(os.environ, settings, clear=True), patch.object(launcher, "command", return_value=b"") as command:
                launcher.development("up", "catalog", "off")
                environment = command.call_args.args[1]
                self.assertEqual(expected, environment["CATALOG_WEBHOOKS_ENABLED"])
                self.assertNotIn("CATALOG_WEBHOOK_ENCRYPTION_KEY", environment)
                self.assertNotIn("CATALOG_WEBHOOKS_POLICY_FILE", environment)
    def test_local_webhooks_reject_invalid_or_production_default_before_tools(self):
        for settings in [{"LOCAL_WEBHOOKS": "yes"}, {"CATALOG_ENVIRONMENT": "production"}]:
            with self.subTest(settings=settings), patch.dict(os.environ, settings, clear=True), patch.object(launcher, "command") as command:
                with self.assertRaises(launcher.Failure):
                    launcher.development("up", "catalog", "off")
                command.assert_not_called()
    def test_recreation_drains_api_then_worker_before_replacing_roles(self):
        with patch.dict(os.environ, {}, clear=True), patch.object(launcher, "command", return_value=b"api\nworker\n") as command:
            launcher.development("up", "catalog", "off")
            calls = [call.args[0] for call in command.call_args_list]
            self.assertEqual("build", calls[-6][-1])
            self.assertEqual(["--role", "api"], calls[-5][-2:])
            self.assertEqual(["--role", "worker"], calls[-4][-2:])
            self.assertEqual(["stop", "api"], calls[-3][-2:])
            self.assertEqual(["stop", "worker"], calls[-2][-2:])
            self.assertEqual(["up", "--no-build", "--force-recreate", "--wait", "--wait-timeout", "180"], calls[-1][-6:])
            self.assertTrue(all("--volumes" not in call for call in calls))
    def test_failed_build_leaves_existing_services_running(self):
        def run(arguments, environment, **options):
            if arguments[-1] == "build":
                raise launcher.Failure("build-failed")
            return b"api\nworker\n"
        with patch.dict(os.environ, {}, clear=True), patch.object(launcher, "command", side_effect=run) as command:
            with self.assertRaisesRegex(launcher.Failure, "build-failed"):
                launcher.development("up", "catalog", "off")
            self.assertFalse(any("stop" in call.args[0] or "up" in call.args[0] for call in command.call_args_list))

    def test_role_validation_failure_preserves_running_services(self):
        def run(arguments, *_args, **_kwargs):
            if "validate" in arguments:
                raise launcher.Failure("configuration-invalid")
            return b"api\nworker\n"
        with patch.dict(os.environ, {}, clear=True), patch.object(launcher, "command", side_effect=run) as command:
            with self.assertRaisesRegex(launcher.Failure, "configuration-invalid"):
                launcher.development("up", "catalog", "off")
            self.assertFalse(any("stop" in call.args[0] or "up" in call.args[0] for call in command.call_args_list))

    def test_drain_and_readiness_failure_never_roll_back_or_retry_startup(self):
        for stage in ("stop", "up"):
            with self.subTest(stage=stage):
                def run(arguments, *_args, **_kwargs):
                    if stage in arguments:
                        raise launcher.Failure("selected-stage-failed")
                    return b"api\nworker\n"
                with patch.dict(os.environ, {}, clear=True), patch.object(launcher, "command", side_effect=run) as command:
                    with self.assertRaisesRegex(launcher.Failure, "selected-stage-failed"):
                        launcher.development("up", "catalog", "off")
                    starts = [call.args[0] for call in command.call_args_list if "up" in call.args[0]]
                    self.assertEqual(0 if stage == "stop" else 1, len(starts))
    def test_ui_inputs_are_private_retained_and_scoped(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "config").mkdir()
            (root / "config/postgres.json").write_text(json.dumps({"auth": {"developmentTokens": {"test-reader": {"subject": "Reader"}}}}))
            with patch.object(launcher, "ROOT", root):
                env = launcher.ui_inputs("catalog", {"CATALOG_UI_PORT": "14180"}, True)
                inputs = Path(env["CATALOG_UI_INPUT_DIRECTORY"])
                users = json.loads((inputs / "users.json").read_text())
                self.assertEqual([{"alias": "test-reader", "label": "Reader", "token": "test-reader"}], users)
                original = (inputs / "keys.json").read_bytes()
                self.assertEqual("", json.loads((inputs / "config.json").read_text())["documentationUrl"])
                launcher.ui_inputs("catalog", {**env, "CATALOG_UI_DOCUMENTATION_URL": "https://docs.example.test/manual/"}, True)
                self.assertEqual("https://docs.example.test/manual/", json.loads((inputs / "config.json").read_text())["documentationUrl"])
                launcher.ui_inputs("catalog", env, True)
                self.assertEqual(original, (inputs / "keys.json").read_bytes())
                self.assertEqual(0o700, inputs.parent.stat().st_mode & 0o777)
                self.assertEqual(0o755, inputs.stat().st_mode & 0o777)
                self.assertEqual(0o444, (inputs / "keys.json").stat().st_mode & 0o777)
                config = json.loads((inputs / "config.json").read_text())
                self.assertEqual("http://api:8080", config["upstream"])
                self.assertEqual("development", config["authMode"])
                self.assertEqual("info", config["logLevel"])
                self.assertEqual("text", config["logFormat"])
                profile = {"auth": {"developmentTokens": {"exact": {"subject": "issuer-subject", "label": "Readable choice", "description": "Exact-template explanation", "profileGroup": "template", "elevated": False, "separateTenant": True}}}}
                (root / "config/postgres.json").write_text(json.dumps(profile))
                launcher.ui_inputs("catalog", {**env, "CATALOG_UI_LOG_LEVEL": "warn", "CATALOG_UI_LOG_FORMAT": "json"}, True)
                self.assertEqual([{"alias": "exact", "label": "Readable choice", "description": "Exact-template explanation", "profileGroup": "template", "elevated": False, "separateTenant": True, "token": "exact"}], json.loads((inputs / "users.json").read_text()))
                changed = json.loads((inputs / "config.json").read_text())
                self.assertEqual("warn", changed["logLevel"])
                self.assertEqual("json", changed["logFormat"])
                profile["auth"]["developmentTokens"]["exact"]["label"] = ""
                (root / "config/postgres.json").write_text(json.dumps(profile))
                launcher.ui_inputs("catalog", env, True)
                self.assertEqual("issuer-subject", json.loads((inputs / "users.json").read_text())[0]["label"])
                self.assertEqual("14180", launcher.ui_inputs("catalog", {}, False)["CATALOG_UI_PORT"])
                (inputs / "keys.json").unlink()
                (inputs / "keys.json").symlink_to(root / "unrelated")
                with self.assertRaisesRegex(launcher.Failure, "unsafe-ui-input"):
                    launcher.ui_inputs("catalog", env, True)
    def test_ui_selection_validates_before_tools_and_off_requires_no_inputs(self):
        for env in [{"LOCAL_UI": "yes"}, {"LOCAL_UI": "on", "CATALOG_UI_PORT": "0"},
                    {"LOCAL_UI": "on", "CATALOG_AUTH_MODE": "oidc"}]:
            with patch.dict(os.environ, env, clear=True), patch.object(launcher, "command") as command:
                with self.assertRaises(launcher.Failure):
                    launcher.development("up", "catalog", "off")
                command.assert_not_called()
        with patch.dict(os.environ, {}, clear=True), patch.object(launcher, "command", return_value=b"") as command, patch.object(launcher, "ui_inputs") as inputs:
            launcher.development("up", "catalog", "off")
            inputs.assert_not_called()
            self.assertNotIn(str(launcher.ROOT / "compose.ui.yaml"), command.call_args.args[0])
    def test_existing_ui_is_included_for_stop_and_not_silently_orphaned(self):
        with patch.dict(os.environ, {}, clear=True), patch.object(launcher, "command", return_value=b"ui\n") as command, patch.object(launcher, "ui_inputs", side_effect=lambda project, env, create: env):
            with self.assertRaisesRegex(launcher.Failure, "ui-already-enabled"):
                launcher.development("up", "catalog", "off")
            launcher.development("stop", "catalog", "off")
            self.assertIn(str(launcher.ROOT / "compose.ui.yaml"), command.call_args.args[0])
    def test_monitoring_settings_defaults_and_bounds(self):
        default = launcher.monitoring_environment({}, "/dev/null")
        self.assertEqual("6", default["CATALOG_MONITORING_RETENTION_HOURS"])
        self.assertEqual("512", default["CATALOG_MONITORING_RETENTION_MB"])
        for hours, size in [("1", "128"), ("24", "2048")]:
            env = {"CATALOG_MONITORING_RETENTION_HOURS": hours, "CATALOG_MONITORING_RETENTION_MB": size}
            result = launcher.monitoring_environment(env, "/dev/null", "0", "0")
            self.assertEqual(hours, result["CATALOG_MONITORING_RETENTION_HOURS"])
            self.assertEqual(size, result["CATALOG_MONITORING_RETENTION_MB"])
        for name, invalid in [("CATALOG_MONITORING_RETENTION_HOURS", ["0", "25", "6h", "", "1;true", "1\n", "-1", "1.5"]),
                              ("CATALOG_MONITORING_RETENTION_MB", ["127", "2049", "512MB", "NaN"])]:
            for value in invalid:
                with patch.dict(os.environ, {name: value}, clear=True), patch.object(launcher, "command") as command, patch.object(launcher, "secret_file") as secret:
                    with self.assertRaises(launcher.Failure):
                        launcher.development("up", "catalog", "on")
                    command.assert_not_called()
                    secret.assert_not_called()
        with self.assertRaisesRegex(launcher.Failure, "conflicting-monitoring-ports"):
            launcher.monitoring_environment({}, "/dev/null", "19090", "19090")
    def test_static_check_uses_selected_settings(self):
        env = {"CATALOG_MONITORING_GRAFANA_PORT": "23000", "CATALOG_MONITORING_PROMETHEUS_PORT": "29090", "CATALOG_MONITORING_RETENTION_HOURS": "12"}
        with patch.dict(os.environ, env, clear=True), patch.object(launcher, "command") as command:
            launcher.development("check", "catalog", "on")
            for key, value in env.items():
                self.assertEqual(value, command.call_args.args[1][key])
    def test_closed_action_mode_and_commands_fail_before_tools(self):
        for action, mode in [("unknown", "off"), ("up", "true")]:
            with patch.object(launcher, "command") as command:
                with self.assertRaises(launcher.Failure):
                    launcher.development(action, "catalog", mode)
                command.assert_not_called()
        for variable in ["CATALOG_LOCAL_COMPOSE", "CATALOG_LOCAL_ENGINE"]:
            with patch.dict(os.environ, {variable: ""}), patch.object(launcher, "command") as command:
                with self.assertRaisesRegex(launcher.Failure, "missing-container-command"):
                    launcher.development("up", "catalog", "off")
                command.assert_not_called()
    def test_image_pin_inventory_is_closed_and_digest_bound(self):
        valid = launcher.image_pins()
        for pins in [{}, valid | {"extra": valid["grafana"]}, valid | {"grafana": 1}, valid | {"grafana": "grafana/grafana:latest"}, valid | {"prometheus": valid["grafana"]}]:
            with patch.object(launcher.safety, "read_document", return_value=pins):
                with self.assertRaisesRegex(launcher.Failure, "invalid-image-pin"):
                    launcher.image_pins()
    def test_invalid_or_writable_secret_is_never_replaced(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            path = root / "grafana-password"
            for raw, mode in [(b"invalid\n", 0o444), (b"a" * 64 + b"\ntrailing", 0o444), (b"a" * 64 + b"\n", 0o666)]:
                if path.exists():
                    path.chmod(0o600)
                path.write_bytes(raw)
                path.chmod(mode)
                with self.assertRaisesRegex(launcher.Failure, "unsafe-secret-file" if mode == 0o666 else "invalid-secret-file") as caught:
                    launcher.secret_file(root)
                self.assertEqual(raw, path.read_bytes())
                diagnostic = "".join(traceback.format_exception(caught.exception))
                self.assertNotIn("FileExistsError", diagnostic)
                self.assertNotIn(str(path), diagnostic)
    def test_default_off_start_needs_no_monitoring_secret(self):
        with patch.object(launcher, "command", return_value=b"") as command, patch.object(launcher, "secret_file") as secret:
            launcher.development("up", "catalog", "off")
            secret.assert_not_called()
            args = command.call_args.args[0]
            self.assertNotIn(str(launcher.ROOT / "compose.monitoring.yaml"), args)
            self.assertEqual(["up", "--no-build", "--force-recreate", "--wait", "--wait-timeout", "180"], args[-6:])
    def test_retained_monitoring_is_included_in_each_lifecycle_action(self):
        for action in ["stop", "down", "ps", "logs"]:
            with patch.object(launcher, "command", side_effect=[b"api\ngrafana\n", b"health output\n"]) as command, patch.object(launcher, "secret_file", return_value=Path("/dev/null")), patch.object(launcher.os, "execvpe") as execute, patch.object(launcher.sys, "stdout") as stdout:
                launcher.development(action, "catalog", "off")
                args = execute.call_args.args[1] if action == "logs" else command.call_args.args[0]
                self.assertIn(str(launcher.ROOT / "compose.monitoring.yaml"), args)
                if action == "ps":
                    stdout.buffer.write.assert_called_once_with(b"health output\n")
                if action == "logs":
                    self.assertEqual(["logs", "--follow", "--tail", "100"], args[-4:])
    def test_enabled_start_reports_only_secret_location(self):
        with patch.object(launcher, "command", return_value=b"") as command, patch.object(launcher, "secret_file", return_value=Path("/dev/null")), patch.object(launcher.sys, "stdout", new_callable=io.StringIO) as output:
            launcher.development("up", "catalog", "on")
            self.assertIn(str(launcher.ROOT / "compose.monitoring.yaml"), command.call_args.args[0])
            self.assertIn("password file:", output.getvalue())
            self.assertIn("username: catalog", output.getvalue())
    def test_command_overrides_are_argument_lists_not_shells(self):
        env = {"CATALOG_LOCAL_COMPOSE": '"/path with space/compose" --compatibility', "CATALOG_LOCAL_ENGINE": '"/path with space/docker"'}
        with patch.dict(os.environ, env), patch.object(launcher, "command", return_value=b"") as command:
            launcher.development("stop", "catalog", "off")
            self.assertEqual("/path with space/docker", command.call_args_list[0].args[0][0])
            self.assertEqual(["/path with space/compose", "--compatibility"], command.call_args.args[0][:2])
    def test_closed_project_and_port_inputs(self):
        for value in ["", "../foreign", "a/b", "UPPER", "x;true", "a" * 65]:
            with self.assertRaises(launcher.Failure):
                launcher.validate_project(value)
        self.assertEqual("catalog-1", launcher.validate_project("catalog-1"))
        for value in ["-1", "65536", "1;true", "abc"]:
            with self.assertRaises(launcher.Failure):
                launcher.monitoring_environment({}, "/dev/null", value)
    def test_pins_and_private_secret_reuse(self):
        self.assertEqual({"prometheus", "grafana"}, set(launcher.image_pins()))
        with tempfile.TemporaryDirectory() as temporary:
            path = launcher.secret_file(Path(temporary) / "private")
            first = path.read_bytes()
            self.assertEqual(first, launcher.secret_file(path.parent).read_bytes())
            self.assertEqual(65, len(first))
            self.assertEqual(0, path.parent.stat().st_mode & 0o077)
            self.assertEqual(0, path.stat().st_mode & 0o022)
    def test_state_and_secret_links_refused(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "link").symlink_to(root, target_is_directory=True)
            with self.assertRaises(launcher.Failure):
                launcher.private_directory(root / "link" / "state")
            (root / "grafana-password").symlink_to(root / "foreign")
            with self.assertRaises(launcher.Failure):
                launcher.secret_file(root)
    def test_isolated_environment_discards_foreign_runtime_inputs(self):
        with patch.dict(os.environ, {"CATALOG_DATABASE_DSN": "foreign", "COMPOSE_PROJECT": "foreign", "DOCKER_HOST": "tcp://foreign:2375", "DOCKER_CONTEXT": "foreign", "LOCAL_MONITORING": "on", "HTTP_PROXY": "foreign", "CATALOG_MONITORING_RETENTION_HOURS": "24", "CATALOG_MONITORING_GRAFANA_PORT": "23000"}):
            env = launcher.isolated_environment()
            self.assertEqual("default", env["DOCKER_CONTEXT"])
            for name in ["CATALOG_DATABASE_DSN", "COMPOSE_PROJECT", "DOCKER_HOST", "LOCAL_MONITORING", "HTTP_PROXY", "CATALOG_MONITORING_RETENTION_HOURS", "CATALOG_MONITORING_GRAFANA_PORT"]:
                self.assertNotIn(name, env)
    def test_existing_monitoring_cannot_be_accidentally_removed(self):
        with patch.object(launcher, "command", return_value=b"api\nprometheus\ngrafana\n") as command:
            with self.assertRaisesRegex(launcher.Failure, "already-enabled"):
                launcher.development("up", "catalog", "off")
            self.assertEqual(1, command.call_count)
    def test_static_check_never_starts_or_pulls(self):
        with patch.object(launcher, "command", return_value=b"") as command:
            launcher.development("check", "catalog", "on")
            args = command.call_args.args[0]
            self.assertEqual(["config", "--quiet"], args[-2:])
            self.assertNotIn("up", args)
            self.assertNotIn("pull", args)
    def test_full_reset_after_down_includes_retained_monitoring(self):
        with patch.object(launcher, "command", side_effect=[b"", b"catalog_monitoring-prometheus\n", b"catalog_monitoring-grafana\n", b""]) as command, patch.object(launcher, "secret_file", return_value=Path("/dev/null")):
            launcher.development("reset", "catalog", "off")
            args = command.call_args.args[0]
            self.assertIn(str(launcher.ROOT / "compose.monitoring.yaml"), args)
            self.assertEqual(["down", "--volumes"], args[-2:])
    def test_foreign_volume_refused_before_any_removal(self):
        for action in ["reset", "reset-monitoring"]:
            with patch.object(launcher, "command", side_effect=[b"grafana\n", b"foreign_volume\n"]) as command:
                with self.assertRaisesRegex(launcher.Failure, "foreign-monitoring-volume"):
                    launcher.development(action, "catalog", "off")
                self.assertTrue(all("rm" not in call.args[0] and "down" not in call.args[0] for call in command.call_args_list))
    def test_monitoring_reset_never_selects_postgres(self):
        with patch.object(launcher, "command", side_effect=[b"api\npostgres\ngrafana\n", b"catalog_monitoring-prometheus\n", b"catalog_monitoring-grafana\n", b"", b"", b""]) as command, patch.object(launcher, "secret_file", return_value=Path("/dev/null")):
            launcher.development("reset-monitoring", "catalog", "off")
            mutations = [call.args[0] for call in command.call_args_list if "rm" in call.args[0]]
            self.assertEqual(3, len(mutations))
            self.assertTrue(all("postgres" not in args and "catalog_catalog-postgres-18" not in args for args in mutations))
    def test_failed_recreation_never_rolls_back_retained_data(self):
        for code in ["child-failed", "deadline"]:
            with patch.object(launcher, "command", side_effect=[b"api\nprometheus\ngrafana\n", launcher.Failure(code)]) as command, patch.object(launcher, "secret_file", return_value=Path("/dev/null")):
                with self.assertRaisesRegex(launcher.Failure, code):
                    launcher.development("up", "catalog", "on")
                self.assertEqual(2, command.call_count)
                self.assertTrue(all("down" not in call.args[0] and "rm" not in call.args[0] for call in command.call_args_list))


class DemoArchiveLifecycle(unittest.TestCase):
    def test_explicit_overlay_and_retained_stop_without_prepared_files(self):
        with patch.dict(os.environ, {}, clear=True), patch.object(launcher, "command", return_value=b"demo-archives\napi\nworker\n") as command:
            launcher.development("stop", "catalog", "off")
            self.assertIn(str(launcher.ROOT / "compose.demo.yaml"), command.call_args.args[0])
            self.assertEqual("stop", command.call_args.args[0][-1])
            self.assertNotIn("--volumes", command.call_args.args[0])
            with self.assertRaisesRegex(launcher.Failure, "demo-archives-already-enabled"):
                launcher.development("up", "catalog", "off")

    def test_invalid_selection_and_unprepared_start_never_call_tools(self):
        for settings in ({"LOCAL_DEMO_ARCHIVES": "yes"}, {"LOCAL_DEMO_ARCHIVES": "on", "DEMO_ARCHIVE_DIR": "/missing/demo"},
                         {"LOCAL_DEMO_ARCHIVES": "on", "CATALOG_ENVIRONMENT": "production"},
                         {"LOCAL_DEMO_ARCHIVES": "on", "CATALOG_AUTH_MODE": "oidc"},
                         {"LOCAL_DEMO_ARCHIVES": "on", "DEMO_ARCHIVE_IP": "127.0.0.1"},
                         {"LOCAL_DEMO_ARCHIVES": "on", "DEMO_ARCHIVE_IP": "172.30.254.41"},
                         {"LOCAL_DEMO_ARCHIVES": "on", "DEMO_ARCHIVE_IP": "0.0.0.41", "CATALOG_COMPOSE_SUBNET": "0.0.0.0/24"}):
            with self.subTest(settings=settings), patch.dict(os.environ, settings, clear=True), patch.object(launcher, "command") as command:
                with self.assertRaises(launcher.Failure):
                    launcher.development("up", "catalog", "off")
                command.assert_not_called()

    def test_prepared_files_admitted_without_rotation_or_seed(self):
        with tempfile.TemporaryDirectory() as temporary:
            directory = Path(temporary)
            for name, mode in (("cert.pem", 0o644), ("key.pem", 0o600), ("demo-archives", 0o700), ("allowlist.json", 0o644)):
                path = directory / name
                path.write_text("inert unit fixture")
                path.chmod(mode)
            settings = {"LOCAL_DEMO_ARCHIVES": "on", "DEMO_ARCHIVE_DIR": temporary}
            with patch.dict(os.environ, settings, clear=True), patch.object(launcher, "command", return_value=b"") as command:
                launcher.development("up", "catalog", "off")
                calls = [call.args[0] for call in command.call_args_list]
                self.assertIn(str(launcher.ROOT / "compose.demo.yaml"), calls[-1])
                self.assertFalse(any("seed-demo" in call for call in calls))
                self.assertEqual(str(os.getuid()), command.call_args.args[1]["DEMO_ARCHIVE_UID"])
                self.assertEqual("inert unit fixture", (directory / "key.pem").read_text())
            (directory / "key.pem").chmod(0o644)
            with self.assertRaisesRegex(launcher.Failure, "unsafe-demo-archive-files"):
                launcher.demo_inputs(settings, verify=True)
            (directory / "key.pem").unlink()
            (directory / "key.pem").symlink_to(directory / "cert.pem")
            with self.assertRaisesRegex(launcher.Failure, "prepare-files-first"):
                launcher.demo_inputs(settings, verify=True)


class DemoPeerLifecycle(unittest.TestCase):
    def test_selector_and_retained_peer_handling(self):
        with patch.dict(os.environ, {'LOCAL_DEMO_RUNTIMES': 'yes'}, clear=True), patch.object(launcher, 'command') as command:
            with self.assertRaisesRegex(launcher.Failure, 'invalid-demo-runtimes'):
                launcher.development('up', 'catalog', 'off')
            command.assert_not_called()
        with patch.dict(os.environ, {}, clear=True), patch.object(launcher, 'command', return_value=b'demo-agent\n') as command:
            with self.assertRaisesRegex(launcher.Failure, 'demo-runtimes-already-enabled'):
                launcher.development('up', 'catalog', 'off')
            self.assertEqual(1, command.call_count)
        with patch.dict(os.environ, {}, clear=True), patch.object(launcher, 'command', return_value=b'demo-mcp\n') as command, patch.object(launcher.runtimes, 'inputs', side_effect=lambda env, **kw: env) as inputs:
            launcher.development('down', 'catalog', 'off')
            self.assertFalse(inputs.call_args.kwargs['verify'])
            self.assertIn(str(launcher.ROOT / 'compose.demo-runtimes.yaml'), command.call_args.args[0])
            self.assertNotIn('--volumes', command.call_args.args[0])

    def test_enabled_peers_qualify_only_after_startup(self):
        with patch.dict(os.environ, {'LOCAL_DEMO_RUNTIMES': 'on'}, clear=True), patch.object(launcher, 'command', return_value=b''), patch.object(launcher.runtimes, 'inputs', side_effect=lambda env, **kw: dict(env, DEMO_A2A_PORT='19443', DEMO_MCP_PORT='19444', DEMO_RUNTIME_DIR='private-inputs')) as inputs, patch.object(launcher.runtimes, 'ready') as ready:
            launcher.development('up', 'catalog', 'off')
            self.assertTrue(inputs.call_args.kwargs['verify'])
            self.assertIn(str(launcher.ROOT / 'compose.demo-runtimes.yaml'), ready.call_args.args[1])
