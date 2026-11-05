import os
from pathlib import Path
import runpy
import subprocess
import sys
import unittest
from unittest.mock import patch
import runner
import runner_test_helpers as helpers


class RunnerTests(helpers.RunnerTestsFixture, unittest.TestCase):
    def test_missing_stale_and_modified_installation(self):
        with self.assertRaisesRegex(RuntimeError, "client-go-tools-install"):
            runner.installed()
        self.stamp()
        self.assertTrue(runner.installed().is_file())
        (self.here / ".tools/oapi-codegen").write_text("different binary")
        with self.assertRaisesRegex(RuntimeError, "stale"):
            runner.installed()
        self.stamp()
        (self.root / "go.sum").write_text("updated locks")
        with self.assertRaisesRegex(RuntimeError, "stale"):
            runner.installed()
    def test_environment_ignores_go_workspace_and_options(self):
        with patch.dict(os.environ, {"GOWORK": "other-workspace", "GOFLAGS": "-mod=mod", "GOROOT": "/other", "GOTOOLCHAIN": "auto", "GOOS": "linux", "GOARCH": "arm64", "CGO_ENABLED": "0", "GOPRIVATE": "*", "GONOPROXY": "*"}):
            online = runner.environment()
            offline = runner.environment(offline=True)
        self.assertEqual(online["GOTOOLCHAIN"], "go1.27.0")
        for key, value in {"GOWORK": "off", "GOFLAGS": "", "GOENV": "off", "GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GONOPROXY": "none"}.items():
            self.assertEqual(offline[key], value)
        for key in ("GOROOT", "GOOS", "GOARCH", "CGO_ENABLED"):
            self.assertNotIn(key, offline)
            self.assertNotIn(key, online)
    def test_malformed_stamp_has_actionable_error(self):
        self.stamp()
        for value in ("[]", "null", '{"inputs":null}', "not json"):
            with self.subTest(value=value):
                (self.here / ".tools/installed.json").write_text(value)
                with self.assertRaisesRegex(RuntimeError, "client-go-tools-install"):
                    runner.installed()
    def test_install_acquires_both_locks_and_records_built_tool(self):
        calls = []
        goroot = self.root / "native-go"
        (goroot / "bin").mkdir(parents=True)
        (goroot / "bin/go").write_text("native toolchain")
        def build(args, cwd, env, log=None):
            calls.append((args, cwd))
            self.assertEqual(env["GOWORK"], "off")
            self.assertNotIn("GOARCH", env)
            if "build" in args:
                Path(args[args.index("-o") + 1]).write_text("built generator")
        before = runner.inputs()
        with patch.object(runner.shutil, "which", return_value="/selected/go"), patch.object(runner.subprocess, "check_output", return_value=str(goroot) + "\n") as resolve, patch.object(runner, "run", side_effect=build):
            runner.install()
        self.assertEqual(runner.installed(), goroot / "bin/go")
        self.assertEqual(runner.inputs(), before)
        self.assertEqual([cwd for _, cwd in calls], [self.here / "tools", self.here / "go", self.here / "tools"])
        self.assertEqual([args[1:] for args, _ in calls[:2]], [["mod", "download"], ["mod", "download"]])
        self.assertIn("-mod=readonly", calls[2][0])
        self.assertEqual(resolve.call_args.args[0], ["/selected/go", "env", "GOROOT"])
    def test_failed_install_never_leaves_a_valid_stamp(self):
        for failure in ("missing-go", "download", "build", "lock-drift"):
            with self.subTest(failure=failure):
                self.stamp()
                def fail(args, cwd, env, log=None):
                    if failure == "download" and "download" in args or failure == "build" and "build" in args:
                        raise subprocess.CalledProcessError(1, args)
                    if failure == "lock-drift" and "build" in args:
                        (self.here / "tools/go.sum").write_text("changed during install")
                with patch.object(runner.shutil, "which", return_value=None if failure == "missing-go" else "/go"), patch.object(runner.subprocess, "check_output", return_value=str(self.root)), patch.object(runner, "run", side_effect=fail):
                    with self.assertRaises((RuntimeError, subprocess.CalledProcessError)):
                        runner.install()
                self.assertFalse((self.here / ".tools/installed.json").exists())
    def test_generate_uses_exact_contract_and_config_offline(self):
        self.stamp()
        with patch.object(runner, "run") as execute:
            runner.check("generate")
        args, cwd, env = execute.call_args.args
        self.assertEqual(args, [self.here / ".tools/oapi-codegen", "-config", self.here / "generate.yaml", "-o", self.here / "go/generated/client.gen.go", self.root / "api/openapi/control-v1.yaml"])
        self.assertEqual(cwd, self.root)
        self.assertEqual(env["GOPROXY"], "off")
        self.assertTrue((self.here / "go/generated").is_dir())
    def test_source_drift_fails_both_generation_and_check(self):
        for action in ("generate", "check"):
            with self.subTest(action=action):
                self.stamp()
                watched = self.root / "api/openapi/control-v1.yaml"
                watched.write_text("contract\n")
                def mutate(path, env):
                    self.fake_generate(path, env)
                    watched.write_text("unexpected contract rewrite")
                with patch.object(runner, "generate", side_effect=mutate), patch.object(runner, "run", side_effect=self.fake_run):
                    with self.assertRaisesRegex(RuntimeError, "inputs changed"):
                        runner.check(action)
    def test_unexpected_module_replacement_stops_before_generation(self):
        for replacement in ("", f"replace {runner.MODULE} => ../../..\n" * 2):
            with self.subTest(replacement=replacement):
                (self.here / "go/go.mod").write_text("module fixture\ngo 1.27.0\n" + replacement)
                self.stamp()
                with patch.object(runner, "generate") as generate:
                    with self.assertRaisesRegex(RuntimeError, "one local Catalog module replacement"):
                        runner.check("check")
                generate.assert_not_called()
    def test_main_dispatch_and_actionable_errors(self):
        for action in ("install", "generate", "check"):
            with self.subTest(action=action), patch.object(sys, "argv", ["runner.py", action]), patch.object(runner, "install") as install, patch.object(runner, "check") as check:
                self.assertEqual(runner.main(), 0)
                if action == "install":
                    install.assert_called_once_with()
                    check.assert_not_called()
                else:
                    install.assert_not_called()
                    check.assert_called_once_with(action)
        with patch.object(sys, "argv", ["runner.py", "check"]):
            self.assertEqual(runner.main(), 1)
        self.assertIn("client-go-tools-install", self.stderr.getvalue())
    def test_script_help_requires_no_installation(self):
        with patch.object(sys, "argv", ["runner.py", "--help"]), patch.object(subprocess, "run", side_effect=AssertionError("help launched a process")), patch.object(subprocess, "check_output", side_effect=AssertionError("help resolved a toolchain")):
            with self.assertRaises(SystemExit) as status:
                runpy.run_path(runner.__file__, run_name="__main__")
        self.assertEqual(status.exception.code, 0)
        self.assertIn("{install,generate,check}", self.stdout.getvalue())
    def test_repeated_checks_ignore_existing_generated_output(self):
        self.stamp()
        stale = self.here / "go/generated/stale.go"
        before = runner.inputs()
        with patch.object(runner, "generate", side_effect=self.fake_generate) as generate, patch.object(runner, "run", side_effect=self.fake_run) as execute:
            runner.check("check")
            self.assertEqual(generate.call_count, 2)
            calls = [call.args[0] for call in execute.call_args_list]
            self.assertEqual([args[1] for args in calls], ["test", "test", "vet", "-m"])
            self.assertNotIn("-tags=functional", calls[0])
            self.assertIn("-tags=functional", calls[1])
            self.assertIn("-tags=functional", calls[2])
            self.assertEqual(calls[3][-1], "test_*.py")
            for call in execute.call_args_list[:2]:
                self.assertIn("-race", call.args[0])
                self.assertIn("-shuffle=on", call.args[0])
            generate.reset_mock()
            execute.reset_mock()
            self.fake_generate(self.here / "go/generated/client.gen.go", {})
            stale.write_text("invalid old generated output")
            runner.check("check")
            self.assertEqual(generate.call_count, 2)
            calls = [call.args[0] for call in execute.call_args_list]
            self.assertEqual([args[1] for args in calls], ["test", "test", "vet", "-m"])
            self.assertNotIn("-tags=functional", calls[0])
            self.assertIn("-tags=functional", calls[1])
            self.assertIn("-tags=functional", calls[2])
            self.assertEqual(calls[3][-1], "test_*.py")
            for call in execute.call_args_list[:2]:
                self.assertIn("-race", call.args[0])
                self.assertIn("-shuffle=on", call.args[0])
            generate.reset_mock()
            execute.reset_mock()
        self.assertEqual(runner.inputs(), before)
        self.assertTrue(stale.exists())
        self.assertEqual(list((self.root / "output/clientcompat").iterdir()), [])
    def test_temporary_lock_drift_is_not_hidden(self):
        self.stamp()
        def mutate(args, cwd, env, log=None):
            self.fake_run(args, cwd, env, log)
            if cwd.name == "consumer":
                (cwd / "go.sum").write_text("unexpected dependency resolution")
        before = runner.inputs()
        with patch.object(runner, "generate", side_effect=self.fake_generate), patch.object(runner, "run", side_effect=mutate):
            with self.assertRaisesRegex(RuntimeError, "temporary module locks"):
                runner.check("check")
        self.assertEqual(runner.inputs(), before)
    def test_failed_checks_retain_only_owned_diagnostics(self):
        self.stamp()
        def fail(args, cwd, env, log=None):
            self.fake_run(args, cwd, env, log)
            raise subprocess.CalledProcessError(1, args)
        with patch.object(runner, "generate", side_effect=self.fake_generate), patch.object(runner, "run", side_effect=fail):
            with self.assertRaises(subprocess.CalledProcessError):
                runner.check("check")
        runs = list((self.root / "output/clientcompat").iterdir())
        self.assertEqual(len(runs), 1)
        self.assertEqual((runs[0] / "check.log").read_text(), "test diagnostics\n")
        self.assertTrue((runs[0] / "consumer/generated/client.gen.go").is_file())
    def test_nondeterministic_generation_fails(self):
        self.stamp()
        def generate(path, env):
            self.fake_generate(path, env)
            if path.name == "repeat.txt":
                path.write_text("changed generation")
        with patch.object(runner, "generate", side_effect=generate):
            with self.assertRaisesRegex(RuntimeError, "not deterministic"):
                runner.check("check")
