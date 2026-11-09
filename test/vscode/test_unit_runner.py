"""Extension package and installed-input guards; no real process or network."""
import base64
import hashlib
import importlib.util
import json
from pathlib import Path
import re
import tempfile
import unittest
from unittest.mock import patch
import zipfile

spec = importlib.util.spec_from_file_location("vscode_runner", Path(__file__).with_name("runner.py"))
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)
core = runner.core


class VSCodeRunnerUnits(unittest.TestCase):
    def test_remote_server_directory_uses_the_selected_archive_commit(self):
        servers = [url for name, (url, _) in runner.REMOTE_TOOLS.items() if name.startswith("server-linux-x64-")]
        self.assertEqual(len(servers), 1)
        selected = re.search(r"/commit:([0-9a-f]{40})/server-linux-x64/stable$", servers[0])
        installed = re.search(r'\bcommit\s*=\s*"([0-9a-f]{40})"', (runner.HERE / "remote.mjs").read_text())
        self.assertIsNotNone(selected, "Selected server URL must bind an immutable commit")
        self.assertIsNotNone(installed, "RemoteSSH fixture must name its server installation commit")
        self.assertEqual(installed[1], selected[1], "RemoteSSH must install the selected server under its matching commit")

    def test_screenshot_capture_is_separate_source_bound_and_never_copies_assets(self):
        for failure in (None, "desktop", "source", "artifact", "prerequisite"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                artifact = root / "output/artifacts/vscode/guide.vsix"
                artifact.parent.mkdir(parents=True)
                artifact.write_bytes(b"package")
                manifest = {"sources": {"source": "current"}, "inputs": {}, "sha256": runner.sdk.digest(artifact)}
                (artifact.parent / "manifest.json").write_text(json.dumps(manifest))
                assets = root / "extensions/vscode/media"
                assets.mkdir(parents=True)
                (assets / "catalog-preview.png").write_bytes(b"maintained image")
                workspaces = []
                with patch.object(runner, "ROOT", root), patch.object(runner, "installed", return_value=({"node": "node", "go": "go"}, {"host": "host", "oras": "oras", "uvx": "uvx"})) as installed, patch.object(runner, "package", return_value=artifact), patch.object(runner, "assert_sources") as sources, patch.object(runner.sdk, "qualify_catalog") as capture, patch.object(core, "coverage") as coverage:
                    def qualify(*args, **kwargs):
                        self.assertEqual(kwargs["pattern"], "^TestVSCodeGuideScreenshots$")
                        self.assertEqual(kwargs["workflow"].name, "screenshots.mjs")
                        workspace = Path(kwargs["extra_env"]["CATALOG_VSCODE_WORKSPACE"])
                        self.assertEqual(workspace.parent, Path(kwargs["extra_env"]["XDG_RUNTIME_DIR"]))
                        self.assertFalse(workspace.is_relative_to(root))
                        workspace.mkdir()
                        (workspace / "installed-package").write_text("owned fixture data")
                        workspaces.append(workspace)
                        if failure == "desktop":
                            raise RuntimeError("capture refused")
                        output = root / "output/vscode/guide-screenshots"
                        for name in ("catalog-preview.png", "agent-conversation.png", "mcp-selection-preview.png", "my-tools-installed.png"):
                            (output / name).write_bytes(b"owned image")
                        if failure == "source":
                            sources.side_effect = RuntimeError("source drift")
                        if failure == "artifact":
                            artifact.write_bytes(b"replacement")
                    capture.side_effect = qualify
                    if failure == "prerequisite":
                        installed.side_effect = RuntimeError("missing tools")
                    if failure:
                        with self.assertRaises(RuntimeError):
                            runner.screenshots()
                    else:
                        runner.screenshots()
                    report = json.loads((root / "output/vscode/guide-screenshots/manifest.json").read_text())
                    self.assertEqual(report["status"], "failed" if failure else "passed")
                    self.assertEqual(report["cleanup"], "not_confirmed" if failure in ("desktop", "prerequisite") else "passed")
                    self.assertEqual((assets / "catalog-preview.png").read_bytes(), b"maintained image")
                    self.assertEqual(list((root / "output/vscode/guide-screenshots").glob("capture-*")), [])
                    self.assertTrue(all(not workspace.exists() for workspace in workspaces))
                    coverage.assert_not_called()

    def test_failed_prerequisites_replace_old_success_and_create_fresh_report(self):
        for previous in (False, True):
            with self.subTest(previous=previous), tempfile.TemporaryDirectory() as directory, patch.object(runner, "ROOT", Path(directory)), patch.object(runner.sys, "argv", ["runner.py", "test"]), patch.object(runner, "installed", side_effect=RuntimeError("Run make vscode-tools-install")), patch.object(runner, "run") as run:
                report = Path(directory) / "output/vscode/qualification.json"
                if previous:
                    report.parent.mkdir(parents=True)
                    report.write_text('{"status":"passed"}')
                with self.assertRaisesRegex(RuntimeError, "vscode-tools-install"):
                    runner.main()
                actual = json.loads(report.read_text())
                self.assertEqual(actual["status"], "failed")
                self.assertEqual(actual["phases"], {"toolingFunctional": "not_run", "localFunctional": "not_run", "packagedDesktop": "not_run", "linuxRemoteAndBrowser": "not_run", "linuxMcpRunners": "not_run", "cleanup": "not_confirmed"})
                run.assert_not_called()

    def test_failed_packaging_replaces_old_success_report(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(runner, "ROOT", Path(directory)), patch.object(runner, "installed", return_value=({"go":"go"}, {"host": "host", "oras": "oras", "uvx": "uvx"})), patch.object(runner, "build"), patch.object(core, "coverage"), patch.object(runner, "package", side_effect=RuntimeError("package refused")), patch.object(runner.sdk, "environment", return_value={}):
            report = Path(directory) / "output/vscode/qualification.json"
            report.parent.mkdir(parents=True)
            report.write_text('{"status":"passed"}')
            with self.assertRaisesRegex(RuntimeError, "package refused"):
                runner.functional()
            actual = json.loads(report.read_text())
            self.assertEqual(actual["status"], "failed")
            self.assertEqual(actual["phases"], {"toolingFunctional": "not_run", "localFunctional": "not_run", "packagedDesktop": "not_run", "linuxRemoteAndBrowser": "not_run", "linuxMcpRunners": "not_run", "cleanup": "not_confirmed"})

    def test_runtime_creation_failure_replaces_old_success_before_build(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(runner, "ROOT", Path(directory)), patch.object(runner, "installed", return_value=({}, {})), patch.object(runner, "build") as build:
            report = Path(directory) / "output/vscode/qualification.json"
            report.parent.mkdir(parents=True)
            report.write_text('{"status":"passed"}')
            with patch.object(runner.tempfile, "TemporaryDirectory", side_effect=OSError("runtime directory unavailable")):
                with self.assertRaisesRegex(OSError, "runtime directory unavailable"):
                    runner.functional()
            actual = json.loads(report.read_text())
            self.assertEqual(actual["status"], "failed")
            self.assertEqual(actual["phases"]["cleanup"], "not_confirmed")
            self.assertTrue(all(value in ("not_run", "not_confirmed") for value in actual["phases"].values()))
            build.assert_not_called()

    def test_qualification_records_failed_phases_and_refuses_late_source_or_vsix_drift(self):
        for scope, failure in ((scope, failure) for scope in ("all", "runtime") for failure in (None, "local", "desktop", "remote", "runners", "remote-cleanup", "runners-stale", "source", "vsix")):
            with self.subTest(scope=scope, failure=failure), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                artifacts = root / "output/artifacts/vscode"
                artifacts.mkdir(parents=True)
                artifact = artifacts / "test.vsix"
                artifact.write_bytes(b"reviewed package")
                manifest = {"sources": {"source": "reviewed"}, "inputs": {"sdk": "reviewed"}, "sha256": runner.sdk.digest(artifact)}
                (artifacts / "manifest.json").write_text(json.dumps(manifest))
                full_report = root / "output/vscode/qualification.json"
                full_report.parent.mkdir(parents=True)
                full_report.write_bytes(b"prior full-scope evidence")
                with patch.object(runner, "ROOT", root), patch.object(runner, "installed", return_value=({"node": "node", "go": "go"}, {"host": "host", "oras": "oras", "uvx": "uvx"})), patch.object(runner, "run") as tooling, patch.object(runner, "build"), patch.object(runner, "package", return_value=artifact), patch.object(runner.sdk, "environment", return_value={}), patch.object(core, "coverage") as run, patch.object(core, "source_hashes", return_value=manifest["sources"]) as sources, patch.object(core, "inputs", return_value=manifest["inputs"]), patch.object(runner.sdk, "qualify_catalog") as desktop:
                    runtime_paths = []
                    def check_runtime(env):
                        runtime = Path(env["XDG_RUNTIME_DIR"])
                        self.assertTrue(runtime.is_dir())
                        self.assertEqual(runtime.stat().st_mode & 0o777, 0o700)
                        self.assertLess(len(str(runtime / "vscode-12345678-1.14-main.sock").encode()), 108)
                        runtime_paths.append(runtime)
                    def qualify(*args, **kwargs):
                        check_runtime(kwargs["extra_env"])
                        if str(kwargs["workflow"]).endswith("remote.mjs"):
                            self.assertEqual(kwargs["pattern"], "^TestVSCodeRemoteCatalogHost$")
                            if failure == "remote":
                                raise RuntimeError("remote refused")
                            result = {"status":"passed", "passed":True, "cleanup":"failed" if failure == "remote-cleanup" else "passed", "packageSHA256":manifest["sha256"]}
                            (root / "output/vscode/remote-qualification.json").write_text(json.dumps(result))
                            return
                        if failure == "desktop":
                            raise RuntimeError("desktop refused")
                        if failure == "source":
                            sources.return_value = {"source": "changed"}
                        if failure == "vsix":
                            artifact.write_bytes(b"replaced package")
                    desktop.side_effect = qualify
                    def phase(args, **kwargs):
                        script = str(args[-1])
                        runners = script.endswith("runners.mjs")
                        if not runners:
                            return
                        check_runtime(kwargs["env"])
                        if failure == "runners":
                            raise RuntimeError("runners refused")
                        result = {"status":"passed", "cleanup":"passed", "packageSHA256":"old" if failure == "runners-stale" else manifest["sha256"]}
                        (root / "output/vscode/runner-qualification.json").write_text(json.dumps(result))
                    tooling.side_effect = phase
                    def local_coverage(*args, **kwargs):
                        check_runtime(kwargs["env"])
                        if failure == "local":
                            raise RuntimeError("local refused")
                    run.side_effect = local_coverage
                    if failure:
                        with self.assertRaisesRegex(RuntimeError, {"local": "local refused", "desktop": "desktop refused", "source": "inputs changed", "vsix": "VSIX changed", "remote": "remote refused", "runners": "runners refused", "remote-cleanup": "Remote/browser result", "runners-stale": "Native MCP runner result"}[failure]):
                            runner.functional(scope=scope)
                    else:
                        runner.functional(scope=scope)
                    actual = json.loads((root / "output/vscode" / ("qualification.json" if scope == "all" else "runtime-qualification.json")).read_text())
                    self.assertTrue(runtime_paths)
                    self.assertEqual(len(set(runtime_paths)), 1)
                    self.assertTrue(all(not path.exists() for path in runtime_paths))
                    self.assertEqual(actual["scope"], scope)
                    self.assertEqual(tooling.call_count, (1 if scope == "all" else 0) + (0 if failure in ("local", "desktop", "remote", "remote-cleanup") else 1))
                    if scope == "runtime":
                        self.assertEqual(full_report.read_bytes(), b"prior full-scope evidence")
                    self.assertEqual(actual["status"], "failed" if failure else "passed")
                    self.assertEqual(actual["phases"], {
                        "toolingFunctional": "passed" if scope == "all" else "not_selected",
                        "localFunctional": "failed" if failure == "local" else "passed",
                        "packagedDesktop": "not_run" if failure == "local" else "failed" if failure == "desktop" else "passed",
                        "linuxRemoteAndBrowser": "not_run" if failure in ("local", "desktop") else "failed" if failure in ("remote", "remote-cleanup") else "passed",
                        "linuxMcpRunners": "not_run" if failure in ("local", "desktop", "remote", "remote-cleanup") else "failed" if failure in ("runners", "runners-stale") else "passed",
                        "cleanup": "not_confirmed" if failure in ("local", "desktop", "remote", "remote-cleanup", "runners", "runners-stale") else "passed",
                    })
                    self.assertEqual(list((root / "output/vscode").glob("vscode-catalog-*")), [])
                    self.assertEqual(list((root / "output/vscode").glob("vscode-remote-catalog-*")), [])
                    if failure == "local":
                        desktop.assert_not_called()

    def test_runtime_scope_cli_and_failed_prerequisites_keep_full_report_separate(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(runner, "ROOT", Path(directory)), patch.object(runner.sys, "argv", ["runner.py", "test", "--scope", "runtime"]), patch.object(runner, "installed", side_effect=RuntimeError("missing test prerequisites")), patch.object(runner, "run") as run:
            full = Path(directory) / "output/vscode/qualification.json"
            full.parent.mkdir(parents=True)
            full.write_bytes(b"prior full-scope evidence")
            with self.assertRaisesRegex(RuntimeError, "missing test prerequisites"):
                runner.main()
            actual = json.loads(full.with_name("runtime-qualification.json").read_text())
            self.assertEqual(actual["scope"], "runtime")
            self.assertEqual(actual["status"], "failed")
            self.assertEqual(actual["phases"], {"toolingFunctional": "not_selected", "localFunctional": "not_run", "packagedDesktop": "not_run", "linuxRemoteAndBrowser": "not_run", "linuxMcpRunners": "not_run", "cleanup": "not_confirmed"})
            self.assertEqual(full.read_bytes(), b"prior full-scope evidence")
            run.assert_not_called()
        with patch.object(runner.sys, "argv", ["runner.py", "package", "--scope", "runtime"]), patch.object(runner, "package") as package, patch("sys.stderr"):
            with self.assertRaises(SystemExit) as refused:
                runner.main()
            self.assertEqual(refused.exception.code, 2)
            package.assert_not_called()

    def test_verification_rejects_changed_sources_or_package_inputs(self):
        with patch.object(core, "source_hashes", return_value={"source": "reviewed"}), patch.object(core, "inputs", return_value={"sdk": "reviewed"}):
            runner.assert_sources({"source": "reviewed"}, {"sdk": "reviewed"})
            for sources, inputs in (({"source": "old"}, {"sdk": "reviewed"}), ({"source": "reviewed"}, {"sdk": "old"}), ({}, {"sdk": "reviewed"})):
                with self.assertRaisesRegex(RuntimeError, "inputs changed"):
                    runner.assert_sources(sources, inputs)

    def test_missing_or_stale_installation_never_acquires_tools(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(runner, "TOOLS", Path(directory)), patch.object(runner.core, "installed", return_value={}), patch.object(runner.sdk, "installed", return_value={}), patch.object(runner, "inputs", return_value={}), patch.object(runner, "run") as run:
            for value in (None, "invalid", "{}", '{"inputs":{"old":"hash"}}'):
                if value:
                    (Path(directory) / "installed.json").write_text(value)
                with self.assertRaisesRegex(RuntimeError, "test-tools-install"):
                    runner.installed()
            run.assert_not_called()

    def test_drift_in_source_host_or_tools_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            executable = root / "host"
            executable.write_bytes(b"qualified")
            stamp = {"uvx": str(executable), "uvx_hash": runner.sdk.digest(executable), "oras": str(executable), "oras_hash": runner.sdk.digest(executable), "inputs": {}, "host": str(executable), "host_hash": runner.sdk.digest(executable), "tool_hashes": {"tool": "good"}, "node_hash": runner.sdk.digest(executable)}
            (root / "installed.json").write_text(json.dumps(stamp))
            (root / "package-lock.json").write_text('{"packages":{}}')
            with patch.object(runner, "TOOLS", root), patch.object(runner, "EXTENSION", root), patch.object(runner.core, "installed", return_value={"node": str(executable)}), patch.object(runner.sdk, "installed", return_value={"go": "owned-go", "go_hash": "owned-hash"}), patch.object(runner, "inputs", return_value={}) as inputs, patch.object(runner, "A2A_SAMPLE", executable), patch.object(runner, "A2A_SAMPLE_HASH", runner.sdk.digest(executable)), patch.object(runner, "tool_hashes", return_value={"tool": "good"}) as hashes, patch.object(runner, "run") as run:
                self.assertEqual(runner.installed()[1], stamp)
                for source, tools in (({"source": "changed"}, {"tool": "good"}), ({}, {"tool": "changed"})):
                    inputs.return_value, hashes.return_value = source, tools
                    with self.assertRaisesRegex(RuntimeError, "test-tools-install"):
                        runner.installed()
                inputs.return_value, hashes.return_value = {}, {"tool": "good"}
                executable.write_bytes(b"changed")
                with self.assertRaisesRegex(RuntimeError, "test-tools-install"):
                    runner.installed()
                run.assert_not_called()

    def test_sdk_manifest_binds_source_bytes_archive_and_reviewed_lock(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            ext = root / "extension"
            ext.mkdir()
            (root / "VERSION").write_text("1.2.3\n")
            source = root / "client.ts"
            source.write_bytes(b"client")
            directory = root / "output/artifacts/sdk/typescript"
            directory.mkdir(parents=True)
            archive = directory / "open-agentic-catalog-client-1.2.3.tgz"
            archive.write_bytes(b"packaged")
            manifest = {"releaseBase": "1.2.3", "sources": {"client.ts": runner.sdk.digest(source)}, "artifacts": {archive.name: runner.sdk.digest(archive)}}
            manifest_file = directory / "manifest.json"
            manifest_file.write_text(json.dumps(manifest))
            integrity = "sha512-" + base64.b64encode(hashlib.sha512(archive.read_bytes()).digest()).decode("ascii")
            lock = {"packages": {"node_modules/@open-agentic-catalog/client": {"integrity": integrity}}}
            (ext / "package-lock.json").write_text(json.dumps(lock))
            with patch.object(core, "ROOT", root), patch.object(core, "EXTENSION", ext), patch.object(core, "sdk_sources", return_value=[source]):
                self.assertEqual(runner.sdk_archive(), manifest["sources"] | {str(archive.relative_to(root)): runner.sdk.digest(archive)})
                source.write_bytes(b"changed")
                with self.assertRaisesRegex(RuntimeError, "current SDK"):
                    runner.sdk_archive()
                source.write_bytes(b"client")
                archive.write_bytes(b"tampered")
                with self.assertRaisesRegex(RuntimeError, "extension SDK lock"):
                    runner.sdk_archive()

    def test_unchanged_a2a_sample_is_hash_checked_and_only_explicitly_acquired(self):
        with tempfile.TemporaryDirectory() as directory:
            sample = Path(directory) / "sample.ts"
            expected = b"unchanged upstream sample"
            digest = hashlib.sha256(expected).hexdigest()
            with patch.object(runner, "A2A_SAMPLE", sample), patch.object(runner, "A2A_SAMPLE_HASH", digest), patch.object(runner, "run", return_value=base64.b64encode(expected).decode()) as run:
                runner.install_a2a_sample()
                self.assertEqual(sample.read_bytes(), expected)
                self.assertIn("11cbb295a7933ecb459d4e2af0740bf4570e71b2", run.call_args.args[0][2])
                run.reset_mock()
                runner.install_a2a_sample()
                run.assert_not_called()
                sample.write_bytes(b"drift")
                run.return_value = base64.b64encode(b"wrong upstream bytes").decode()
                with self.assertRaisesRegex(RuntimeError, "source checksum"):
                    runner.install_a2a_sample()
                self.assertEqual(sample.read_bytes(), b"drift")

    def test_vsix_requires_runtime_notices_and_excludes_development_payload(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(core, "ROOT", Path(directory)):
            root = Path(directory)
            (root / "VERSION").write_text("1.2.3\n")
            metadata = {"version": "1.2.3", "main": "./dist/extension.cjs", "name": "catalog", "publisher": "local", "icon": "media/catalog-icon.png"}
            payload = {name: (json.dumps(metadata) if relative == "package.json" else "exact " + relative).encode() for name, relative in core.PAYLOAD.items()}
            for name, relative in core.PAYLOAD.items():
                path = root / relative
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(payload[name])
            xml = b'<PackageManifest><Metadata><Identity Id="catalog" Publisher="local" Version="1.2.3"/></Metadata><Assets><Asset Path="extension/package.json"/></Assets></PackageManifest>'
            payload.update({"extension.vsixmanifest": xml, "[Content_Types].xml": b"<Types/>"})
            archive = root / "test.vsix"
            changes = ({}, {"extension/test/secret.json": b"synthetic"}, {"extension/../canary": b"x"}, {"extension/LICENSE.txt": None}, {"extension/media/catalog-preview.png": b"changed"}, {"extension/readme.md": b"stale guide"}, {"extension.vsixmanifest": xml.replace(b'Id="catalog"', b'Id="other"')})
            for change in changes:
                with zipfile.ZipFile(archive, "w") as bundle:
                    for name, value in (payload | change).items():
                        if value is not None:
                            bundle.writestr(name, value)
                if change:
                    with self.assertRaises(RuntimeError):
                        core.inspect_package(archive, root)
                else:
                    core.inspect_package(archive, root)
            metadata["main"] = "./missing.cjs"
            (root / "package.json").write_text(json.dumps(metadata))
            with zipfile.ZipFile(archive, "w") as bundle:
                for name, value in (payload | {"extension/package.json": (root / "package.json").read_bytes()}).items():
                    bundle.writestr(name, value)
            with self.assertRaisesRegex(RuntimeError, "entrypoint"):
                core.inspect_package(archive, root)


if __name__ == "__main__":
    unittest.main()
