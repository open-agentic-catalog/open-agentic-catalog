"""Local Sonar source, coverage and ownership decisions without Docker or HTTP."""

import contextlib
import copy
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock
import urllib.parse

import sonarqube as sonar


class SonarTests(unittest.TestCase):
    def test_quality_profiles_inherit_and_associate_only_the_catalog_project(self):
        local = object.__new__(sonar.Local)
        profiles, writes = {}, []

        def api(path, fields=None, password=None):
            self.assertEqual(password, "synthetic-password")
            if fields is None:
                selector = urllib.parse.parse_qs(urllib.parse.urlsplit(path).query)
                language = selector["language"][0]
                parent = {"name": "Sonar way", "key": language + "-builtin", "isBuiltIn": True}
                profile = profiles.get(language)
                return {"profiles": [parent, profile] if profile else [parent]}
            writes.append((path, fields))
            language = fields["language"]
            if path.endswith("/create"):
                profiles[language] = {"key": language + "-catalog", "name": "Open Agentic Catalog", "isBuiltIn": False}
                return {"profile": profiles[language]}
            if path.endswith("/change_parent"):
                self.assertEqual(fields["parentQualityProfile"], "Sonar way")
                profiles[language]["parentKey"] = language + "-builtin"
            else:
                self.assertEqual(path, "/api/qualityprofiles/add_project")
                self.assertEqual(fields["project"], "open-agentic-catalog")
            return {}

        local.api = mock.Mock(side_effect=api)
        local.quality_rule = mock.Mock()
        first = local.quality_profiles("synthetic-password")
        expected = {
            "go": {"go:S3776": {"threshold": "40"}, "go:S107": {"Max": "15"}},
            "py": {"python:S3776": {"threshold": "40"}, "python:S107": {"max": "15"}},
            "js": {"javascript:S3776": {"threshold": "40"}, "javascript:S107": {"maximumFunctionParameters": "15"}},
            "ts": {"typescript:S3776": {"threshold": "40"}, "typescript:S107": {"maximumFunctionParameters": "15"}},
        }
        self.assertEqual({language: entry["overrides"] for language, entry in first.items()}, expected)
        local.quality_rule.assert_has_calls([
            mock.call(language + "-catalog", rule, parameters, "synthetic-password")
            for language, rules in expected.items() for rule, parameters in rules.items()
        ])
        self.assertEqual(local.quality_rule.call_count, 8)
        self.assertEqual(len(writes), 12)  # Create, inherit and associate each language.
        writes.clear()
        self.assertEqual(local.quality_profiles("synthetic-password"), first)
        self.assertEqual([path for path, _ in writes], ["/api/qualityprofiles/add_project"] * 4)
        for conflict in ({"isBuiltIn": True}, {"parentKey": "unrelated-profile"}):
            with self.subTest(conflict=conflict):
                original = profiles["go"].copy()
                profiles["go"].update(conflict)
                writes.clear()
                with self.assertRaisesRegex(sonar.Failure, "unexpected parent"):
                    local.quality_profiles("synthetic-password")
                self.assertEqual(writes, [])
                profiles["go"] = original
        local.api = mock.Mock(return_value={"profiles": []})
        with self.assertRaisesRegex(sonar.Failure, "missing Sonar way"):
            local.quality_profiles("synthetic-password")
        local.api.assert_called_once()

    def test_quality_rule_preserves_other_parameters_and_verifies_readback(self):
        before = {
            "rule": {"type": "CODE_SMELL", "impacts": [{"softwareQuality": "MAINTAINABILITY"}],
                     "params": [{"key": "threshold"}, {"key": "other"}]},
            "actives": [{"qProfile": "catalog", "params": [{"key": "threshold", "value": "15"}, {"key": "other", "value": "keep"}]}],
        }
        after = copy.deepcopy(before)
        after["actives"][0]["params"][0]["value"] = "40"
        local = object.__new__(sonar.Local)
        local.api = mock.Mock(side_effect=[before, {}, after])
        local.quality_rule("catalog", "go:S3776", {"threshold": "40"}, "synthetic")
        self.assertEqual(local.api.call_args_list[1], mock.call("/api/qualityprofiles/activate_rule", {
            "key": "catalog", "rule": "go:S3776", "params": "threshold=40;other=keep",
        }, password="synthetic"))  # No severity, impacts, reset or deactivation request.
        local.api = mock.Mock(return_value=after)
        local.quality_rule("catalog", "go:S3776", {"threshold": "40"}, "synthetic")
        local.api.assert_called_once()
        local.api = mock.Mock(side_effect=[before, {}, before])
        with self.assertRaisesRegex(sonar.Failure, "not applied"):
            local.quality_rule("catalog", "go:S3776", {"threshold": "40"}, "synthetic")
        for invalid in ("security", "type", "parameter", "inactive"):
            with self.subTest(invalid=invalid):
                detail = copy.deepcopy(before)
                if invalid == "security":
                    detail["rule"]["impacts"].append({"softwareQuality": "SECURITY"})
                elif invalid == "type":
                    detail["rule"]["type"] = "BUG"
                elif invalid == "parameter":
                    detail["rule"]["params"] = []
                else:
                    detail["actives"] = []
                local.api = mock.Mock(return_value=detail)
                with self.assertRaises(sonar.Failure):
                    local.quality_rule("catalog", "go:S3776", {"threshold": "40"}, "synthetic")
                local.api.assert_called_once()  # Failure precedes any mutation.

    def test_snapshot_excludes_private_vendor_generated_and_external_inputs(self):
        for path in (".env", "output/private.go", "specs/ard/main.go", "schemas/upstream/profile/a.json",
                     "test/clientcompat/go/generated/main.go", "catalog-ui/node_modules/.cache/a.js", "catalog-ui/node_modules/a.js",
                     "documentation/dist/app.js", "catalog-ui/coverage/report.json",
                     "/tmp/main.go", "internal/../private.go", "documentation/.astro/types.d.ts"):
            self.assertFalse(sonar.selected(path), path)
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            (root / "internal").mkdir()
            (root / "internal/main.go").write_text("package main\n")
            (root / ".env").write_text("private-secret")
            destination = root / "snapshot"
            destination.mkdir()
            with mock.patch.object(sonar, "ROOT", root), mock.patch.object(sonar, "command", side_effect=[
                b"internal/main.go\0internal/deleted.go\0.env\0", b"a" * 40]):
                report = sonar.snapshot(destination)
            self.assertEqual(report["files"], 1)
            self.assertFalse((destination / ".env").exists())
            (root / "internal/link.go").symlink_to(root / ".env")
            with mock.patch.object(sonar, "ROOT", root), mock.patch.object(sonar, "command", return_value=b"internal/link.go\0"):
                with self.assertRaisesRegex(sonar.Failure, "regular repository file"):
                    sonar.snapshot(destination)

    def test_coverage_paths_are_normalized_and_unknown_sources_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            for name in ("internal/a.go", "catalog-ui/src/a.ts"):
                path = root / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("source\n")
            raw = ("mode: atomic\n" + sonar.MODULE + "internal/a.go:1.1,1.5 1 2\n"
                   + sonar.MODULE + "internal/a.go:1.1,1.5 1 0\n").encode()
            self.assertEqual(sonar.coverage(raw, "go", root), "mode: atomic\ninternal/a.go:1.1,1.5 1 1\n")
            lcov = b"TN:\nSF:src/a.ts\nDA:1,1\nend_of_record\n"
            self.assertIn("SF:catalog-ui/src/a.ts", sonar.coverage(lcov, "js", root))
            for raw, kind in ((b"mode: set\n", "go"), (b"mode: set\n../../secret:1.1,1.5 1 1\n", "go"),
                              (b"mode: set\nmissing.go:1.1,1.5 1 1\n", "go"),
                              (b"SF:/private/secret\nend_of_record\n", "js"), (b"SF:src/a.ts", "js"),
                              (b"SF:src/a.ts\nDA:1,-1\nend_of_record\n", "js"),
                              (b"SF:src/a.ts\nSF:src/a.ts\nend_of_record\n", "js"),
                              (b"mode: atomic\ninternal/a.go:1.1,1.5 1 1\ninternal/a.go:1.1,1.5 2 0\n", "go")):
                with self.subTest(kind=kind, raw=raw), self.assertRaises(sonar.Failure):
                    sonar.coverage(raw, kind, root)

    def test_unowned_resources_and_public_bindings_are_rejected(self):
        local = object.__new__(sonar.Local)
        local.name, local.owner = "owned", "expected"
        local.inspect = mock.Mock(return_value={"Config": {"Labels": {sonar.LABEL: "other"}}})
        with self.assertRaisesRegex(sonar.Failure, "unowned"):
            local.ownership()
        local.port, local.images = 19000, {"server": "pinned"}
        local.inspect.return_value = {"State": {"Running": True}, "Config": {"Image": "pinned"},
                                      "HostConfig": {"PortBindings": {"9000/tcp": [{"HostIp": "0.0.0.0", "HostPort": "19000"}]}}}
        local.api = mock.Mock()
        with self.assertRaises(sonar.Failure):
            local.ready()
        local.api.assert_not_called()

    def test_private_state_rejects_symlinks_and_is_not_world_readable(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory).resolve() / "state.json"
            sonar.write_json(path, {"password": "synthetic"})
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            alias = Path(directory).resolve() / "alias"
            alias.symlink_to(path)
            with self.assertRaises(sonar.Failure):
                sonar.write_json(alias, {})
            self.assertEqual(json.loads(path.read_text())["password"], "synthetic")

    def test_credentials_display_requires_initialized_state_and_makes_no_api_calls(self):
        local = object.__new__(sonar.Local)
        local.base = "http://127.0.0.1:19000"
        local.api = mock.Mock()
        for state in (None, {"initialized": False, "password": "pending"}, {"initialized": True}):
            local.state = state
            output = io.StringIO()
            with contextlib.redirect_stdout(output), self.assertRaisesRegex(sonar.Failure, "sonarqube-up"):
                local.show_credentials()
            self.assertEqual(output.getvalue(), "")
        local.state = {"initialized": True, "password": "synthetic-secret"}
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            local.show_credentials()
        self.assertIn("Username: admin\nPassword: synthetic-secret\n", output.getvalue())
        local.api.assert_not_called()

    def test_restart_waits_through_connection_reset_and_retains_credentials(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            local = object.__new__(sonar.Local)
            local.state = {"initialized": True, "password": "retained-secret", "port": 19000}
            local.credentials = root / "credentials.json"
            local.port, local.base = 19000, "http://127.0.0.1:19000"
            local.compose = mock.Mock()
            local.ready = mock.Mock(side_effect=[ConnectionResetError(), {"status": "UP"}])
            local.api = mock.Mock(return_value={"valid": True})
            with mock.patch.object(sonar, "ROOT", root), mock.patch.object(sonar.secrets, "token_urlsafe") as generate, \
                    mock.patch.object(sonar.time, "sleep"), \
                    mock.patch.object(sonar.time, "monotonic", side_effect=[0, 1]), contextlib.redirect_stdout(io.StringIO()):
                local.up()
            generate.assert_not_called()
            self.assertEqual(local.ready.call_count, 2)
            self.assertEqual(json.loads(local.credentials.read_text())["password"], "retained-secret")
            local.api.assert_called_once_with("/api/authentication/validate", password="retained-secret")

    def test_first_start_guarantees_password_classes_before_initialization(self):
        for random_part in ("a" * 43, "A" * 43, "0" * 43, "-" * 43):
            with self.subTest(random_class=random_part[0]), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                local = object.__new__(sonar.Local)
                local.state, local.name = None, "owned"
                local.credentials = root / "credentials.json"
                local.port, local.base = 19000, "http://127.0.0.1:19000"
                local.inspect = mock.Mock(return_value=None)
                local.ready, local.compose = mock.Mock(), mock.Mock()
                local.api = mock.Mock(side_effect=[{"valid": False}, {}, {"valid": True}])
                output = io.StringIO()
                with mock.patch.object(sonar, "ROOT", root), \
                        mock.patch.object(sonar.secrets, "token_urlsafe", return_value=random_part) as generate, \
                        contextlib.redirect_stdout(output):
                    local.up()
                generate.assert_called_once_with(32)
                password = local.state["password"]
                for alphabet in ("abcdefghijklmnopqrstuvwxyz", "ABCDEFGHIJKLMNOPQRSTUVWXYZ", "0123456789", "!"):
                    self.assertTrue(any(char in alphabet for char in password), "required password class missing")
                self.assertTrue(password.startswith(random_part), "random entropy was discarded")
                self.assertNotIn(password, output.getvalue())
                self.assertEqual(local.credentials.stat().st_mode & 0o777, 0o600)
                self.assertTrue(json.loads(local.credentials.read_text())["initialized"])
                local.inspect.assert_called_once_with("volume", "owned_data")
                local.api.assert_has_calls([
                    mock.call("/api/authentication/validate", password=password),
                    mock.call("/api/users/change_password", {
                        "login": "admin", "previousPassword": "admin", "password": password,
                    }, password="admin"),
                    mock.call("/api/authentication/validate", password=password),
                ])

    def test_first_start_refusal_retains_pending_password_for_retry(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            local = object.__new__(sonar.Local)
            local.state, local.name = None, "owned"
            local.credentials = root / "credentials.json"
            local.port, local.base = 19000, "http://127.0.0.1:19000"
            local.inspect = mock.Mock(return_value=None)
            local.ready, local.compose = mock.Mock(), mock.Mock()
            local.api = mock.Mock(side_effect=[{"valid": False}, sonar.Failure("first-start refusal")])
            output = io.StringIO()
            with mock.patch.object(sonar, "ROOT", root), \
                    mock.patch.object(sonar.secrets, "token_urlsafe", return_value="a" * 43) as generate, \
                    contextlib.redirect_stdout(output):
                with self.assertRaisesRegex(sonar.Failure, "first-start refusal"):
                    local.up()
                pending = json.loads(local.credentials.read_text())
                self.assertFalse(pending["initialized"])
                self.assertEqual(local.state, pending)
                local.api = mock.Mock(side_effect=[{"valid": False}, {}, {"valid": True}])
                local.up()
                generate.assert_called_once_with(32)
            self.assertEqual(local.state["password"], pending["password"])
            self.assertTrue(json.loads(local.credentials.read_text())["initialized"])
            self.assertNotIn(pending["password"], output.getvalue())

    def test_missing_credentials_never_reinitialize_retained_data(self):
        local = object.__new__(sonar.Local)
        local.state, local.name = None, "owned"
        local.inspect = mock.Mock(return_value={"Name": "owned_data"})
        local.compose, local.api = mock.Mock(), mock.Mock()
        with mock.patch.object(sonar.secrets, "token_urlsafe") as generate:
            with self.assertRaisesRegex(sonar.Failure, "original credentials"):
                local.up()
        generate.assert_not_called()
        local.compose.assert_not_called()
        local.api.assert_not_called()

    def test_scanner_failure_revokes_token_redacts_log_and_preserves_unowned_container(self):
        for cleanup_case in ("absent", "unowned", "docker-unavailable"):
            with self.subTest(cleanup_case=cleanup_case), tempfile.TemporaryDirectory() as directory:
                root = Path(directory).resolve()
                (root / "catalog-ui/node_modules").mkdir(parents=True)
                state = root / "output"
                state.mkdir()
                local = object.__new__(sonar.Local)
                local.ready = mock.Mock()
                local.state = {"initialized": True, "password": "synthetic-password"}
                local.owner, local.name = "owner", "container"
                local.images = {"scanner": "pinned", "platform": "linux/amd64"}
                local.api = mock.Mock(side_effect=[{}, {"token": "synthetic-token"}, {}])
                local.quality_profiles = mock.Mock(return_value={"go": {"name": "Open Agentic Catalog"}})
                failure = sonar.Failure("scan failed")
                failure.output = b"failure synthetic-token"
                local.docker = mock.Mock(side_effect=failure)
                local.inspect = mock.Mock(return_value={"Config": {"Labels": {sonar.LABEL: "other"}}} if cleanup_case == "unowned" else None)
                if cleanup_case == "docker-unavailable":
                    local.inspect.side_effect = OSError("Docker executable unavailable")
                def snapshot(path):
                    (path / "catalog-ui").mkdir()
                    return {"digest": "sha256:synthetic"}
                with mock.patch.object(sonar, "ROOT", root), mock.patch.object(sonar, "STATE", state), \
                        mock.patch.object(sonar, "snapshot", side_effect=snapshot), \
                        mock.patch.dict(sonar.os.environ, {}, clear=True), contextlib.redirect_stdout(io.StringIO()):
                    with self.assertRaisesRegex(sonar.Failure, "scan failed"):
                        local.scan()
                local.docker.assert_called_once()  # Never remove the conflicting foreign name.
                self.assertIn(b"SCANNER_WORKDIR_PATH=/tmp/sonar-work", local.docker.call_args.kwargs["data"])
                self.assertNotIn("synthetic-token", " ".join(local.docker.call_args.args))
                self.assertEqual(local.api.call_args.args[0], "/api/user_tokens/revoke")
                run = next(state.glob("scan-*"))
                report = json.loads((run / "report.json").read_text())
                self.assertEqual(report["status"], "incomplete")
                local.quality_profiles.assert_called_once_with("synthetic-password")
                self.assertEqual(report["qualityProfiles"], {"go": {"name": "Open Agentic Catalog"}})
                self.assertEqual(report["cleanup"], "passed" if cleanup_case == "absent" else "failed")
                self.assertNotIn("synthetic-token", (run / "scanner.log").read_text())
                self.assertFalse((run / "source").exists())
                self.assertFalse((run / "work").exists())
