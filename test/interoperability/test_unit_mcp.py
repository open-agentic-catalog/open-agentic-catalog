"""Pure validation and injected supervisor failure checks; no Docker/client execution."""

import base64
import copy
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from common import Result
import mcp_contract as c
import mcp_runner as r
import mcp_tools as tools


def observations():
    return {"requests": 22, "forwarded": 21, "denied": 1, "authorization": 0, "otherTools": 0,
            "http": {"POST": 17, "GET": 4}, "methods": {"tools/call": 2, "tools/list": 3},
            "initialize": [{"protocolVersion": "2025-11-25", "capabilities": {"roots": {}}} for _ in range(4)]}


def registry_metadata(package, expected):
    name, version = package.rsplit("@", 1)
    result = {"name": name, "version": version,
              "dist": {"integrity": expected["dist.integrity"], "tarball": expected["dist.tarball"]}}
    if expected["gitHead"] is not None:
        result["gitHead"] = expected["gitHead"]
    return result


class Contract(unittest.TestCase):
    def test_config_and_credentials_are_explicit(self):
        plain = c.client_config()["mcpServers"]["handoff"]
        keyed = c.client_config("test-key")["mcpServers"]["handoff"]
        self.assertEqual(plain["headers"], {})
        self.assertEqual(keyed["headers"], {"X-MCP-Test-Key": "test-key"})
        self.assertEqual(keyed["roots"], [])
        self.assertEqual(keyed["protocolEra"], "legacy")
        self.assertEqual(keyed["url"], "https://127.0.0.1:9443/mcp")

    def test_echo_requires_exact_native_content(self):
        data = {"content": [{"type": "text", "text": "Echo: " + c.MARKER}]}
        def result(item):
            return Result(0, json.dumps({"result": item}), "")
        self.assertEqual(c.validate_case("echo", result(data))["outcome"], "echoed")
        for change in ({"isError": True}, {"content": []}, {"content": [{"type": "text", "text": "wrong"}]}):
            with self.subTest(change=change), self.assertRaises(AssertionError):
                c.validate_case("echo", result(dict(data, **change)))
        with self.assertRaises(AssertionError):
            c.validate_case("echo", Result(5, json.dumps({"result": data}), ""))

    def test_initialize_checks_wire_and_advertised_identity(self):
        data = {"protocolVersion": "2025-11-25", "serverInfo": {"name": "mcp-servers/everything", "version": "2.0.0"}}
        def result(item):
            return Result(0, json.dumps({"result": item}), "")
        c.validate_case("initialize", result(data))
        for field in ("protocolVersion", "serverInfo"):
            broken = copy.deepcopy(data)
            broken[field] = "wrong" if field == "protocolVersion" else {"name": "fake", "version": "2026.8.31"}
            with self.assertRaises(AssertionError):
                c.validate_case("initialize", result(broken))

    def test_list_checks_unique_tools_and_echo_contract(self):
        echo = {"name": "echo", "inputSchema": {"properties": {"message": {"type": "string"}}, "required": ["message"]}}
        for listing in ([echo], [], [echo, echo], [dict(echo, inputSchema={"properties": {"message": {"type": "number"}}, "required": []})]):
            result = Result(0, json.dumps({"result": {"tools": listing}}), "")
            if listing == [echo]:
                c.validate_case("tools-list", result)
            else:
                with self.assertRaises(AssertionError):
                    c.validate_case("tools-list", result)

    def test_negative_controls_require_matching_native_evidence(self):
        error = {"result": {"isError": True, "content": [{"type": "text", "text": "MCP error -32602: Invalid arguments for tool echo: expected string, received number at message"}]}}
        valid = Result(5, json.dumps(error), json.dumps({"error": {"code": "tool_is_error"}}))
        c.validate_case("tool-error", valid)
        for broken in (Result(5, "{}", valid.stderr), Result(5, valid.stdout, ""), Result(4, valid.stdout, valid.stderr), Result(5, valid.stdout, '{"error":{"code":"unreachable"}}')):
            with self.assertRaises((AssertionError, KeyError)):
                c.validate_case("tool-error", broken)
        missing = Result(3, "", 'notice\n{"error":{"code":"auth_required"}}')
        c.validate_case("missing-key", missing)
        with self.assertRaises(AssertionError):
            c.validate_case("missing-key", Result(3, "unexpected result", missing.stderr))
        with self.assertRaises(AssertionError):
            c.validate_case("missing-key", Result(3, "", '{"error":{"code":"unreachable"}}'))

    def test_incomplete_or_unsafe_reports_cannot_pass(self):
        outcomes = ("connected", "listed", "echoed", "authentication-required", "tool-error")
        report = {"scope": "mcp-direct-control", "cases": {name: {"outcome": outcome, "exit": {"missing-key": 3, "tool-error": 5}.get(name, 0)} for name, outcome in zip(c.CASES, outcomes, strict=True)}, "observations": observations(), "cleanup": True}
        c.complete(report)
        broken = copy.deepcopy(report)
        del broken["cases"]["echo"]
        with self.assertRaises(AssertionError):
            c.complete(broken)
        broken = copy.deepcopy(report)
        broken["cleanup"] = False
        with self.assertRaises(AssertionError):
            c.complete(broken)
        for field in ("authorization", "otherTools"):
            broken = copy.deepcopy(report)
            broken["observations"][field] = 1
            with self.assertRaises(AssertionError):
                c.complete(broken)
        broken = copy.deepcopy(report)
        broken["observations"]["initialize"][0]["capabilities"]["sampling"] = {}
        with self.assertRaises(AssertionError):
            c.complete(broken)


class Tools(unittest.TestCase):
    def test_pending_interruption_cannot_hide_unconfirmed_creation(self):
        for interruption in (KeyboardInterrupt(), TimeoutError("scenario deadline")):
            with self.subTest(interruption=type(interruption).__name__), \
                 patch.object(tools, "safe_args", return_value=[]), \
                 patch.object(tools, "docker", side_effect=RuntimeError("creation deadline")), \
                 patch.object(tools.signal, "pthread_sigmask", side_effect=[set(), interruption]):
                with self.assertRaises(tools.CreationUnconfirmed):
                    tools.create("owned", "image")

    def test_creation_failure_is_unconfirmed_and_restores_signal_mask(self):
        for failure in (RuntimeError("subprocess deadline exceeded"), OSError("CLI unavailable")):
            with self.subTest(failure=str(failure)), patch.object(tools, "safe_args", return_value=[]), \
                 patch.object(tools, "docker", side_effect=failure), \
                 patch.object(tools.signal, "pthread_sigmask", return_value=set()) as mask:
                with self.assertRaisesRegex(tools.CreationUnconfirmed, "unconfirmed: owned"):
                    tools.create("owned", "image")
                self.assertEqual(mask.call_args.args, (tools.signal.SIG_SETMASK, set()))

    def test_checked_in_lock_matches_reviewed_provenance(self):
        self.assertEqual(len(tools.validate_lock()), 3)

    def test_npm_image_drift_is_refused_before_registry_acquisition(self):
        package = json.loads((tools.INPUTS / "package.json").read_text())
        recipe = (tools.INPUTS / "Containerfile").read_text()
        archive = json.loads((tools.ROOT / "test/ci/pins.json").read_text())["archives"]["npm"]
        for selected, image in ((dict(package, packageManager="npm@0.0.0"), recipe),
                                (package, recipe.replace(archive["sha256"], "0" * 64)),
                                (package, recipe.replace('= "' + archive["version"] + '"', '= "0.0.0"'))):
            with tempfile.TemporaryDirectory() as directory:
                inputs = Path(directory)
                (inputs / "package.json").write_text(json.dumps(selected))
                (inputs / "Containerfile").write_text(image)
                with patch.object(tools, "INPUTS", inputs), patch.object(tools, "local_daemon"), \
                        patch.object(tools.urllib.request, "urlopen") as acquire:
                    with self.assertRaisesRegex(ValueError, "npm distribution differs"):
                        tools.install()
                    acquire.assert_not_called()

    def test_absent_registry_revision_requires_a_separate_reviewed_source_pin(self):
        package = "@modelcontextprotocol/inspector@2.12.0"
        expected = tools.validate_lock()[package]
        self.assertIsNone(expected["gitHead"])
        self.assertEqual(tools.source_revision(package, expected), "0cac110a809e93753fa353810b9b4c91e89810fc")
        metadata = registry_metadata(package, expected)
        tools.validate_registry_identity(package, expected, metadata)
        for value in (None, "a" * 40):
            with self.subTest(value=value), self.assertRaisesRegex(RuntimeError, "registry identity drift"):
                tools.validate_registry_identity(package, expected, dict(metadata, gitHead=value))
        for revision in (None, "main", "a" * 39, "g" * 40):
            with self.subTest(revision=revision), self.assertRaisesRegex(ValueError, "immutable source revision"):
                tools.source_revision(package, dict(expected, sourceRevision=revision))
        missing_presence = dict(expected)
        del missing_presence["gitHead"]
        with self.assertRaisesRegex(ValueError, "presence is unrecorded"):
            tools.source_revision(package, missing_presence)

    def test_registry_revision_and_artifact_changes_still_fail_closed(self):
        package = "@modelcontextprotocol/sdk@1.32.2"
        expected = tools.validate_lock()[package]
        metadata = registry_metadata(package, expected)
        tools.validate_registry_identity(package, expected, metadata)
        for field, value in (("name", "different"), ("version", "0.0.0"), ("gitHead", "a" * 40), ("gitHead", None)):
            with self.subTest(field=field, value=value), self.assertRaisesRegex(RuntimeError, "registry identity drift"):
                tools.validate_registry_identity(package, expected, dict(metadata, **{field: value}))
        missing = dict(metadata)
        del missing["gitHead"]
        with self.assertRaisesRegex(RuntimeError, "registry identity drift"):
            tools.validate_registry_identity(package, expected, missing)
        for field in ("integrity", "tarball"):
            broken = copy.deepcopy(metadata)
            broken["dist"][field] = "changed"
            with self.subTest(field=field), self.assertRaisesRegex(RuntimeError, "registry identity drift"):
                tools.validate_registry_identity(package, expected, broken)
        with self.assertRaisesRegex(ValueError, "disagrees"):
            tools.source_revision(package, dict(expected, sourceRevision="a" * 40))

    def test_upstream_source_version_mismatch_is_rejected_before_container_build(self):
        pins = tools.validate_lock()
        repositories = {"modelcontextprotocol/servers/": "@modelcontextprotocol/server-everything",
                        "modelcontextprotocol/inspector/": "@modelcontextprotocol/inspector",
                        "modelcontextprotocol/typescript-sdk/": "@modelcontextprotocol/sdk"}
        for target in ("@modelcontextprotocol/inspector", "@modelcontextprotocol/sdk"):
            def source(args, **_):
                self.assertEqual(args[:2], ["gh", "api"])
                name = next(name for repository, name in repositories.items() if repository in args[2])
                package = next(package for package in pins if package.rsplit("@", 1)[0] == name)
                self.assertIn("ref=" + tools.source_revision(package, pins[package]), args[2])
                manifest = {"name": name, "version": "0.0.0" if name == target else pins[package]["version"]}
                return Result(0, json.dumps({"content": base64.b64encode(json.dumps(manifest).encode()).decode()}), "")
            responses = [io.BytesIO(json.dumps(registry_metadata(package, expected)).encode()) for package, expected in pins.items()]
            with self.subTest(target=target), tempfile.TemporaryDirectory() as directory, \
                 patch.object(tools, "OUTPUT", Path(directory)), patch.object(tools, "local_daemon"), \
                 patch.object(tools.urllib.request, "urlopen", side_effect=responses), patch.object(tools, "run", side_effect=source), \
                 patch.object(tools, "docker") as docker, self.assertRaisesRegex(RuntimeError, "source/package version mismatch"):
                tools.install()
            docker.assert_not_called()

    def test_runtime_options_do_not_inherit_secrets_or_publish_ports(self):
        with patch.object(tools.os, "getuid", return_value=1001), patch.object(tools.os, "getgid", return_value=1001):
            args = tools.safe_args("owned")
        self.assertIn("--read-only", args)
        self.assertIn("--cap-drop=ALL", args)
        self.assertIn("--pull=never", args)
        self.assertIn("1001:1001", args)
        self.assertIn("HTTPS_PROXY=", args)
        self.assertNotIn("--privileged", args)
        self.assertNotIn("--publish", args)
        with patch.object(tools.os, "getuid", return_value=0), self.assertRaises(RuntimeError):
            tools.safe_args("owned")

    def test_cleanup_attempts_every_owned_container_and_reports_failure(self):
        with patch.object(tools, "docker", side_effect=[RuntimeError("timeout"), Result(0, "", ""), Result(1, "", "No such container")]) as docker:
            with self.assertRaisesRegex(RuntimeError, "cleanup failed"):
                tools.remove(["one", "two", "three"])
            self.assertEqual([call.args[2] for call in docker.call_args_list], ["three", "two", "one"])

    def test_missing_installation_fails_without_downloading(self):
        with tempfile.TemporaryDirectory() as name, patch.object(tools, "STAMP", Path(name) / "missing"), patch.object(tools, "docker") as docker:
            with self.assertRaisesRegex(RuntimeError, "mcp-tools-install first"):
                tools.installed()
            docker.assert_not_called()


class Build(unittest.TestCase):
    def test_wrapper_and_commands_use_the_resolved_cached_compiler_offline(self):
        with tempfile.TemporaryDirectory() as name:
            directory = Path(name)
            for binary in ("fixture", "catalogd", "catalogctl"):
                (directory / binary).write_bytes(binary.encode())
            with patch.object(r, "cached_go", return_value="/cached/bin/go") as compiler, patch.object(r, "run", return_value=Result(0, "", "")) as run, patch.object(r, "build_binaries") as commands, patch.dict(r.os.environ, {
                "GOMODCACHE": "/cached/modules", "GOCACHE": "/cached/builds", "GOFLAGS": "-mod=mod", "GOROOT": "/ambient",
            }):
                result = r.build(directory, "amd64")
            compiler.assert_called_once_with()
            self.assertEqual(set(result), {"fixture", "catalogd", "catalogctl"})
            self.assertEqual(run.call_args.args[0][:2], ["/cached/bin/go", "build"])
            self.assertEqual(run.call_args.kwargs["env"]["GOTOOLCHAIN"], "local")
            self.assertEqual(run.call_args.kwargs["env"]["GOPROXY"], "off")
            self.assertEqual(run.call_args.kwargs["env"]["GOMODCACHE"], "/cached/modules")
            self.assertEqual(run.call_args.kwargs["env"]["GOCACHE"], "/cached/builds")
            self.assertEqual(run.call_args.kwargs["env"]["GOFLAGS"], "-mod=readonly")
            self.assertNotIn("GOROOT", run.call_args.kwargs["env"])
            commands.assert_called_once_with(directory, "/cached/bin/go", "amd64")


class Supervisor(unittest.TestCase):
    def setUp(self):
        # Supervisor units model a supported host without depending on the test runner's CPU/OS.
        for name, value in (("system", "Linux"), ("machine", "x86_64")):
            patcher = patch.object(r.platform, name, return_value=value)
            patcher.start()
            self.addCleanup(patcher.stop)
        fingerprint = patch.object(r, "fingerprint", return_value="fixture-source")
        fingerprint.start()
        self.addCleanup(fingerprint.stop)
        # Even cleanup-error paths must not hide accidental unit subprocesses.
        for name in ("run", "build_binaries"):
            guard = patch.object(r, name, side_effect=AssertionError("unit attempted a subprocess"))
            mocked = guard.start()
            self.addCleanup(guard.stop)
            self.addCleanup(mocked.assert_not_called)

    def test_failed_or_interrupted_setup_removes_private_files_and_owned_containers(self):
        for failure in (RuntimeError("fixture unavailable"), KeyboardInterrupt(), tools.CreationUnconfirmed("creation timed out")):
            with self.subTest(failure=type(failure).__name__), tempfile.TemporaryDirectory() as name:
                root = Path(name)
                def certificates(_binary, path):
                    (path / "ca.pem").write_text("test CA")
                with patch.object(r, "ROOT", root), patch.object(tools, "OUTPUT", root), \
                     patch.object(r, "local_daemon", return_value="amd64"), \
                     patch.object(tools, "installed", return_value={"image": "sha256:test"}), \
                     patch.object(r, "certificates", side_effect=certificates), patch.object(r, "build", return_value="build"), \
                     patch.object(tools, "create", side_effect=failure), patch.object(r, "docker"), patch.object(tools, "remove") as remove:
                    with self.assertRaises(type(failure)):
                        r.check()
                self.assertEqual(len(remove.call_args.args[0]), 1)
                directory = next(root.glob("run-*"))
                report = json.loads((directory / "report.json").read_text())
                self.assertFalse(report["success"])
                uncertain = isinstance(failure, tools.CreationUnconfirmed)
                self.assertEqual(report["cleanup"], not uncertain)
                self.assertEqual(report.get("creation_unconfirmed", False), uncertain)
                self.assertEqual(report["containers"], remove.call_args.args[0])
                self.assertEqual(list(directory.glob("private-*")), [])

    def test_cleanup_failure_cannot_be_reported_as_clean(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            with patch.object(r, "ROOT", root), patch.object(tools, "OUTPUT", root), \
                 patch.object(r, "local_daemon", return_value="amd64"), \
                 patch.object(tools, "installed", return_value={"image": "sha256:test"}), \
                 patch.object(r, "build", return_value={}), patch.object(r, "certificates", side_effect=RuntimeError("stop")), \
                 patch.object(tools, "remove", side_effect=RuntimeError("cleanup failed")):
                with self.assertRaisesRegex(RuntimeError, "cleanup failed"):
                    r.check()
            report = json.loads(next(root.glob("run-*/report.json")).read_text())
            self.assertFalse(report["cleanup"])
            self.assertFalse(report["success"])
