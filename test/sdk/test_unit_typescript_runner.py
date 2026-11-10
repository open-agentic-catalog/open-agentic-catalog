"""SDK launcher units with injected tools, never real processes or network."""
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import typescript_runner as runner


class TypeScriptRunnerUnits(unittest.TestCase):
    def test_npm_pack_result_binds_one_selected_identity_and_local_archive(self):
        manifest = {"name": "@owned/client", "version": "1.0.0"}
        record = dict(manifest, filename="owned-client-1.0.0.tgz")
        self.assertEqual(runner.packed_filename({manifest["name"]: record}, manifest), record["filename"])
        for packed in ([], [record], {}, {"other": record},
                       {manifest["name"]: record, "other": record},
                       {manifest["name"]: None}):
            with self.subTest(packed=packed), self.assertRaises(RuntimeError):
                runner.packed_filename(packed, manifest)
        for change in ({"name": "other"}, {"version": "2.0.0"}, {"filename": None},
                       {"filename": "../outside.tgz"}, {"filename": "..\\outside.tgz"},
                       {"filename": "/outside.tgz"}, {"filename": ".tgz"}, {"filename": "archive.zip"}):
            with self.subTest(change=change), self.assertRaises(RuntimeError):
                runner.packed_filename({manifest["name"]: dict(record, **change)}, manifest)

    def test_runtime_qualification_binds_each_actual_node_and_artifact_identity(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            target = root / "output/artifacts/sdk/typescript/manifest.json"
            target.parent.mkdir(parents=True)
            stamp = {"node":"/primary/node", "node_hash":"primary-hash",
                     "runtimes":{"22.23.4":{"node":"/node22","hash":"node22-hash"},
                                 "24.20.0":{"node":"/node24","hash":"node24-hash"}}}
            def qualify(action, runtime):
                value = {"tools":runtime, "artifacts":{"client.tgz":runtime["qualifiedRuntime"] + "-artifact"}}
                target.write_text(json.dumps(value))
                return value
            with patch.object(runner, "ROOT", root), patch.object(runner, "installed", return_value=stamp), \
                 patch.object(runner, "check_runtime", side_effect=qualify) as check:
                runner.check("check")
            self.assertEqual([call.args[1]["node_hash"] for call in check.call_args_list], ["node22-hash","node24-hash"])
            result = json.loads(target.read_text())
            self.assertEqual(result["tools"]["qualifiedRuntimeBuilds"], {
                "22.23.4":{"nodeSha256":"node22-hash","artifacts":{"client.tgz":"22.23.4-artifact"}},
                "24.20.0":{"nodeSha256":"node24-hash","artifacts":{"client.tgz":"24.20.0-artifact"}}})

    def test_environment_removes_ambient_npm_node_and_browser_overrides(self):
        with patch.dict(os.environ, {"npm_config_registry": "https://private", "NODE_OPTIONS": "secret", "NODE_TLS_REJECT_UNAUTHORIZED": "0", "PLAYWRIGHT_BROWSERS_PATH": "/outside"}):
            env = runner.environment()
        self.assertNotIn("NODE_OPTIONS", env)
        self.assertNotIn("NODE_TLS_REJECT_UNAUTHORIZED", env)
        self.assertNotIn("npm_config_registry", env)
        self.assertEqual(env["NPM_CONFIG_IGNORE_SCRIPTS"], "true")
        self.assertEqual(env["PLAYWRIGHT_BROWSERS_PATH"], str(runner.TOOLS / "chromium"))
        self.assertNotEqual(env["NPM_CONFIG_USERCONFIG"], env["NPM_CONFIG_GLOBALCONFIG"])

    def test_primary_toolchain_is_exact(self):
        runner.validate_runtime("v24.22.1\n", "12.2.1\n")
        for node, npm in (("v22.23.4", "12.2.1"), ("v24.20.0", "12.2.1"), ("v24.22.1", "10.9.9"), ("v24.22.1", "11.19.1")):
            with self.assertRaisesRegex(RuntimeError, "Pinned Node"):
                runner.validate_runtime(node, npm)

    def test_missing_malformed_and_stale_installations_fail_before_commands(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(runner, "TOOLS", Path(directory)), patch.object(runner, "inputs", return_value={"lock": "current"}), patch.object(runner, "run") as run:
            for contents in (None, "malformed", "{}", json.dumps({"inputs": {"lock": "old"}})):
                if contents is not None:
                    (Path(directory) / "installed.json").write_text(contents)
                with self.assertRaisesRegex(RuntimeError, "sdk-tools-install"):
                    runner.installed()
            run.assert_not_called()

    def test_complete_dependency_inventory_checks_versions_and_integrity(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(runner, "SDK", Path(directory)):
            root = Path(directory)
            (root / "node_modules/example").mkdir(parents=True)
            package = root / "node_modules/example/package.json"
            package.write_text(json.dumps({"version": "1.0.0", "license": "MIT"}))
            lock = {"packages": {"": {}, "node_modules/example": {"version": "1.0.0", "integrity": "sha512-locked"}}}
            (root / "package-lock.json").write_text(json.dumps(lock))
            self.assertEqual(runner.tool_inventory()["node_modules/example"], {"version": "1.0.0", "license": "MIT"})
            package.write_text(json.dumps({"version": "2.0.0"}))
            with self.assertRaisesRegex(RuntimeError, "drift"):
                runner.tool_inventory()
            package.write_text(json.dumps({"version": "1.0.0"}))
            lock["packages"]["node_modules/example"].pop("integrity")
            (root / "package-lock.json").write_text(json.dumps(lock))
            with self.assertRaisesRegex(RuntimeError, "drift"):
                runner.tool_inventory()


    def test_executable_and_tool_drift_are_rejected_without_acquisition(self):
        with tempfile.TemporaryDirectory() as directory:
            tools = Path(directory)
            executable = tools / "tool"
            executable.write_bytes(b"qualified")
            versions = json.loads((runner.HERE / "node-runtimes.json").read_text())
            stamp = {"inputs": {}, "inventory": {}, "tool_hashes": {"generator": "current"}, "runtimes": {version: {"node": str(executable), "hash": runner.digest(executable)} for version in versions}}
            for name in ("node", "npm", "go"):
                stamp[name], stamp[name + "_hash"] = str(executable), runner.digest(executable)
            (tools / "installed.json").write_text(json.dumps(stamp))
            with patch.object(runner, "TOOLS", tools), patch.object(runner, "inputs", return_value={}), patch.object(runner, "tool_inventory", return_value={}), patch.object(runner, "tool_hashes", return_value={"generator": "current"}) as hashes, patch.object(runner, "runtime_licenses") as licenses, patch.object(runner, "run") as run:
                self.assertEqual(runner.installed(), stamp)
                licenses.assert_called_once()
                incomplete = dict(stamp, runtimes={})
                (tools / "installed.json").write_text(json.dumps(incomplete))
                with self.assertRaisesRegex(RuntimeError, "sdk-tools-install"):
                    runner.installed()
                (tools / "installed.json").write_text(json.dumps(stamp))
                hashes.return_value = {"generator": "changed"}
                with self.assertRaisesRegex(RuntimeError, "sdk-tools-install"):
                    runner.installed()
                hashes.return_value = stamp["tool_hashes"]
                executable.write_bytes(b"changed")
                with self.assertRaisesRegex(RuntimeError, "sdk-tools-install"):
                    runner.installed()
                run.assert_not_called()

    def test_catalog_qualification_uses_recorded_go_offline(self):
        import runner as go_runner
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            fixture = work / "fixtures/go"
            fixture.mkdir(parents=True)
            (fixture / "go.mod").write_text("replace " + go_runner.MODULE + " => ../../..\n")
            (fixture / "fixture.go").write_text("package test\n")
            with patch.dict(os.environ, {"NODE_OPTIONS": "--require=/outside", "NODE_TLS_REJECT_UNAUTHORIZED": "0", "npm_config_registry": "https://private", "PLAYWRIGHT_BROWSERS_PATH": "/outside"}, clear=True), patch.object(go_runner, "COMPAT", fixture.parent), patch.object(runner, "run") as run:
                runner.qualify_catalog(work, {"node": "/qualified/node", "go": "/qualified/go"})
            self.assertEqual(run.call_count, 2)
            self.assertEqual(str(run.call_args_list[0].args[0][0]), "/qualified/go")
            completion = run.call_args_list[1].args[0]
            self.assertIn("TestTypeScriptSDKCatalogWorkflow", completion)
            self.assertIn("-json", completion)
            self.assertIn("/qualified/go", [str(arg) for arg in completion])
            for call in run.call_args_list:
                self.assertEqual(call.kwargs["env"]["GOTOOLCHAIN"], "local")
                self.assertEqual(call.kwargs["env"]["GOPROXY"], "off")
                self.assertEqual(call.kwargs["env"]["GOSUMDB"], "off")
                self.assertNotIn("NODE_OPTIONS", call.kwargs["env"])
                self.assertNotIn("NODE_TLS_REJECT_UNAUTHORIZED", call.kwargs["env"])
                self.assertNotIn("npm_config_registry", call.kwargs["env"])
                self.assertEqual(call.kwargs["env"]["PLAYWRIGHT_BROWSERS_PATH"], str(runner.TOOLS / "chromium"))

    def test_ard_example_inventory_contains_sources_and_executed_tests(self):
        sources = runner.ard_example_sources()
        for name in ("index.mts", "package.json", "README.md", "LICENSE", ".gitignore"):
            self.assertIn(runner.ARD_EXAMPLE / name, sources)
        self.assertIn(runner.HERE / "typescript_ard_functional_test.go.txt", sources)
        self.assertTrue(any(path.name.endswith(".unit.test.mjs") for path in sources))
        self.assertFalse(any("node_modules" in path.parts or path.name == "package-lock.json" for path in sources))

    def test_ard_example_refuses_missing_unit_selection_before_commands(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(runner, "PROJECT", Path(directory)), patch.object(runner, "run") as run:
            with self.assertRaisesRegex(RuntimeError, "unit tests are missing"):
                runner.qualify_ard_example(Path(directory), {"node": "/qualified/node"}, Path(directory))
            run.assert_not_called()

    def test_ard_example_stages_only_declared_sources(self):
        with tempfile.TemporaryDirectory() as directory:
            work = Path(directory)
            source = work / "source"
            source.mkdir()
            files = {"index.mts", "package.json", "README.md", "LICENSE", ".gitignore"}
            for name in files:
                (source / name).write_text("source")
            (source / "LICENSE").write_bytes((runner.ROOT / "LICENSE").read_bytes())
            for name in ("catalog-token", ".env", "private/server-key.pem", "node_modules/local/cache", "package-lock.json"):
                path = source / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("private-input-sentinel")
            consumer = work / "consumer"
            consumer.mkdir()
            with patch.object(runner, "ARD_EXAMPLE", source), patch.object(runner, "run") as run, patch.object(runner, "qualify_catalog") as qualify:
                runner.qualify_ard_example(work, {"node": "/qualified/node"}, consumer)
            staged = consumer / "ard-example"
            self.assertEqual({p.name for p in staged.iterdir()}, files)
            self.assertEqual((staged / "LICENSE").read_bytes(), (runner.ROOT / "LICENSE").read_bytes())
            self.assertEqual(run.call_count, 2)
            qualify.assert_called_once()


if __name__ == "__main__":
    unittest.main()
