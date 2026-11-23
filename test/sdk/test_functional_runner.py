import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

import runner


def make_environment(settings):
    # Each nested Make invocation selects its own SDK. Do not inherit the
    # parent target's command-line assignments, flags or environment selector.
    # Supply a parent selector even when this suite is run directly, so the
    # real Make/default-all assertions retain this regression control.
    inherited = dict(os.environ, MAKEFLAGS="-- SDK_LANGUAGE=go",
                     MAKEOVERRIDES="SDK_LANGUAGE=go", SDK_LANGUAGE="go")
    return {key: value for key, value in dict(inherited, CATALOG_ENV_FILE=str(settings)).items()
            if key not in ("MAKEFLAGS", "MFLAGS", "GNUMAKEFLAGS", "MAKELEVEL", "MAKEOVERRIDES", "SDK_LANGUAGE")}


class RunnerFunctional(unittest.TestCase):
    def test_copied_launcher_preflight_and_healthy_receipt_transitions(self):
        # Run the unchanged launcher in a native interpreter. Only expensive
        # preflight/build producers are injected; real receipt invalidation,
        # selection, fail-fast and successful-publication order execute here.
        languages = ("go", "python", "typescript", "rust")
        with tempfile.TemporaryDirectory(prefix="oac-sdk-preflight-") as directory:
            root = Path(directory)
            scripts = root / "test/sdk"
            scripts.mkdir(parents=True)
            for name in ("runner.py", "go_archive.py", "rust_runner.py"):
                shutil.copyfile(runner.HERE / name, scripts / name)
            (scripts / "rust").mkdir()
            shutil.copyfile(runner.HERE / "rust/generation.py", scripts / "rust/generation.py")
            entry = scripts / "control.py"
            entry.write_text('''import json,sys
from pathlib import Path
from types import SimpleNamespace
import runner
selection,mode=sys.argv[1:]
languages=("go","python","typescript","rust") if selection=="all" else (selection,)
def preflight(language):
    assert language==languages[0]
    for selected in languages:
        for name in ("qualification.json","manifest.json"):
            assert not (runner.ROOT/"output/artifacts/sdk"/selected/name).exists()
    if mode=="refused":
        raise RuntimeError("synthetic native preflight refusal")
    return SimpleNamespace(qualify_sdk=qualify),"fresh-run",{"source":"controlled source"}
def build(action,language):
    target=runner.ROOT/"output/artifacts/sdk"/language
    (target/"manifest.json").write_text("fresh manifest "+language)
    if language=="rust":
        return {"rust":"fresh measurement"}
def qualify(root,language,run,source):
    target=root/"output/artifacts/sdk"/language
    assert (target/"manifest.json").read_text()=="fresh manifest "+language
    if language=="rust":
        assert not (root/"output/sdk/rust-coverage.json").exists()
        if mode=="source-changed":
            raise ValueError("SDK qualification source changed")
    (target/"qualification.json").write_text(json.dumps({"run":run,"source":source,"language":language}))
runner.qualification_context=preflight
runner.selected=build
sys.argv=["runner.py","check","--language",selection]
raise SystemExit(runner.main())
''')
            for selection in ("python", "all"):
                selected = languages if selection == "all" else (selection,)
                coverage = root / "output/sdk/rust-coverage.json"
                coverage.parent.mkdir(parents=True, exist_ok=True)
                for mode in (("healthy", "refused", "source-changed") if selection == "all" else ("healthy", "refused")):
                    with self.subTest(selection=selection, mode=mode):
                        coverage.write_text("previous measurement")
                        for language in languages:
                            target = root / "output/artifacts/sdk" / language
                            target.mkdir(parents=True, exist_ok=True)
                            for name in ("qualification.json", "manifest.json", "package.bin"):
                                (target / name).write_text("previous " + language)
                        result = subprocess.run([sys.executable, "-B", entry, selection, mode],
                                                cwd=root, capture_output=True, text=True, timeout=15, check=False)
                        self.assertEqual(result.returncode, 0 if mode == "healthy" else 1, result.stdout + result.stderr)
                        if selection == "python":
                            self.assertEqual(coverage.read_text(), "previous measurement")
                        elif mode == "healthy":
                            self.assertEqual(json.loads(coverage.read_text()), {"rust": "fresh measurement"})
                        else:
                            self.assertFalse(coverage.exists())
                        if mode == "source-changed":
                            self.assertIn("SDK qualification source changed", result.stderr)
                            self.assertFalse((root / "output/artifacts/sdk/rust/qualification.json").exists())
                            self.assertEqual(list(coverage.parent.iterdir()), [])
                        if mode == "refused":
                            self.assertIn("synthetic native preflight refusal", result.stderr)
                            self.assertNotIn("completed:", result.stdout)
                            self.assertNotIn("SDK check: rust", result.stdout)
                        for language in languages:
                            target = root / "output/artifacts/sdk" / language
                            self.assertEqual((target / "package.bin").read_text(), "previous " + language)
                            if mode == "source-changed" and language == "rust":
                                self.assertEqual((target / "manifest.json").read_text(), "fresh manifest rust")
                            elif language in selected:
                                if mode == "refused":
                                    self.assertFalse((target / "qualification.json").exists())
                                    self.assertFalse((target / "manifest.json").exists())
                                else:
                                    self.assertIn('"run": "fresh-run"', (target / "qualification.json").read_text())
                                    self.assertEqual((target / "manifest.json").read_text(), "fresh manifest " + language)
                            else:
                                for name in ("qualification.json", "manifest.json"):
                                    self.assertEqual((target / name).read_text(), "previous " + language)

    def test_native_go_sdk_completion_refuses_empty_skip_and_failure(self):
        go = str(runner.installed())
        with tempfile.TemporaryDirectory(prefix="oac-sdk-completion-") as directory:
            work = Path(directory)
            runner.run([go, "mod", "init", "fixture.example/sdk"], work, runner.environment(offline=True))
            script = work / "check.py"
            script.write_text("import importlib.util,os\nfrom pathlib import Path\n"
                              + "spec=importlib.util.spec_from_file_location('sdk_runner'," + repr(str(runner.HERE / "runner.py")) + ")\n"
                              + "runner=importlib.util.module_from_spec(spec);spec.loader.exec_module(runner)\n"
                              + "runner.required_go_tests(" + repr(go) + ",['-count=1','./...'],Path('.'),runner.environment(offline=True))\n")
            for label, source in (("healthy", 'func TestRequired(t *testing.T) {}'),
                                  ("skipped", 'func TestRequired(t *testing.T) { t.Skip("synthetic") }'),
                                  ("failed", 'func TestRequired(t *testing.T) { t.Fatal("synthetic assertion") }'),
                                  ("empty", 'func helper(t *testing.T) {}')):
                with self.subTest(label=label):
                    (work / "fixture_test.go").write_text('package sdk\nimport "testing"\n' + source + "\n")
                    result = subprocess.run([sys.executable, "-B", script], cwd=work, env=dict(os.environ, GOTOOLCHAIN="local"),
                                            capture_output=True, text=True, timeout=45, check=False)
                    self.assertEqual(result.returncode == 0, label == "healthy", result.stdout + result.stderr)

    def test_invalid_language_fails_before_acquisition(self):
        result = subprocess.run([sys.executable, str(runner.HERE / "runner.py"), "install", "--language", "not-a-language"],
                                capture_output=True, text=True, timeout=10, check=False)
        self.assertEqual(result.returncode, 2)
        self.assertIn("invalid choice", result.stderr)

    def test_rust_coverage_requires_both_go_workflows_to_finish(self):
        go = runner.installed()
        driver = """
import sys
from pathlib import Path
import runner
import rust_runner

root, destination, go = map(Path, sys.argv[1:])
runner.COMPAT = root
rust_runner.HERE = root
rust_runner.qualify_catalog(destination, go, root / "unused-consumer")
"""
        env = runner.environment(offline=True)
        for setting in ("CATALOG_RUST_REPORT", "CATALOG_SDK_RUST_EXAMPLE"):
            env.pop(setting, None)
        with tempfile.TemporaryDirectory(prefix="oac-rust-qualification-") as directory:
            root = Path(directory)
            module = root / "go"
            module.mkdir()
            for name in ("go.mod", "go.sum"):
                shutil.copyfile(runner.COMPAT / "go" / name, module / name)
            for case, transport, diagnostic in (
                ("healthy", "func TestRustSDKTransport(t *testing.T) {}", ""),
                ("skipped", 'func TestRustSDKTransport(t *testing.T) { t.Skip("controlled skip") }',
                 "Required Go test failed or skipped: TestRustSDKTransport"),
                ("missing", "func TestRenamedTransport(t *testing.T) {}",
                 "Required Go test missing or incomplete: TestRustSDKTransport"),
            ):
                with self.subTest(case=case):
                    (root / "rust_consumer_functional_test.go.txt").write_text(
                        'package fixture\nimport "testing"\nfunc TestRustSDKCatalogWorkflow(t *testing.T) {}\n'
                        + transport + "\n"
                    )
                    result = subprocess.run(
                        [sys.executable, "-B", "-c", driver, root, root / case, go],
                        cwd=runner.HERE, env=env, capture_output=True, text=True, timeout=120, check=False,
                    )
                    self.assertIn("--- PASS: TestRustSDKCatalogWorkflow", result.stdout, result.stderr)
                    if diagnostic:
                        self.assertNotEqual(result.returncode, 0, result.stdout)
                        self.assertIn(diagnostic, result.stderr)
                    else:
                        self.assertEqual(result.returncode, 0, result.stderr)
                        self.assertIn("--- PASS: TestRustSDKTransport", result.stdout)

    def test_interrupted_rust_check_and_failed_retry_keep_each_runs_evidence(self):
        driver = """
import signal, sys
from pathlib import Path
import runner, rust_runner

root, mode = Path(sys.argv[1]), sys.argv[2]
runner.ROOT = rust_runner.ROOT = root
rust_runner.installed = lambda: {}
rust_runner.source_files = lambda: []
runner.qualification_context = lambda language: (None, "fixture", {})
def generate(destination, work):
    (work / "attempt").write_text(mode)
    if mode == "interrupt":
        signal.raise_signal(signal.SIGINT)
    rust_runner.run([sys.executable, root / "child.py"], capture=True)
rust_runner.generate = generate
sys.argv = ["runner.py", "check", "--language", "rust"]
raise SystemExit(runner.main())
"""
        with tempfile.TemporaryDirectory(prefix="oac-rust-retry-") as directory:
            root = Path(directory)
            parent = root / "output/sdk"
            parent.mkdir(parents=True)
            coverage = parent / "rust-coverage.json"
            coverage.write_text("last successful run")
            unrelated = parent / "rust-do-not-clean"
            unrelated.mkdir()
            (unrelated / "notes").write_text("reader-owned")
            (root / "child.py").write_text(
                'import sys\nprint("native assertion details")\n'
                'print("native failure reason", file=sys.stderr)\nsys.exit(23)\n'
            )
            attempts = {}
            for mode in ("interrupt", "retry"):
                result = subprocess.run([sys.executable, "-B", "-c", driver, root, mode], cwd=runner.HERE,
                                        capture_output=True, text=True, timeout=15, check=False)
                self.assertNotEqual(result.returncode, 0)
                if mode == "interrupt":
                    self.assertIn("KeyboardInterrupt", result.stderr)
                else:
                    self.assertIn("native assertion details", result.stdout)
                    self.assertIn("native failure reason", result.stderr)
                    self.assertIn("exit status 23", result.stderr)
                self.assertNotIn("SDK check completed", result.stdout)
                self.assertFalse(coverage.exists())
                self.assertFalse((root / "output/artifacts/sdk/rust/qualification.json").exists())
                self.assertEqual((unrelated / "notes").read_text(), "reader-owned")
                retained = next(line.removeprefix("Failed Rust SDK output retained: ")
                                for line in result.stderr.splitlines() if line.startswith("Failed Rust SDK output retained: "))
                work = Path(retained)
                self.assertEqual(work.parent, parent)
                self.assertNotIn(work, attempts)
                attempts[work] = mode
                self.assertEqual(set(parent.iterdir()), {unrelated, *attempts})
                for attempt, expected in attempts.items():
                    self.assertEqual((attempt / "attempt").read_text(), expected)

    def test_make_without_installed_tools_fails_without_acquisition_or_stale_combined_report(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            scripts = root / "test/sdk"
            scripts.mkdir(parents=True)
            for name in ("runner.py", "go_archive.py", "python_runner.py", "typescript_runner.py", "rust_runner.py"):
                shutil.copyfile(runner.HERE / name, scripts / name)
            shutil.copytree(runner.HERE / "rust", scripts / "rust")
            for name in ("Makefile", "go.mod", "VERSION"):
                shutil.copyfile(runner.ROOT / name, root / name)
            poison = root / "settings.mk"
            poison.write_text("$(error SDK loaded deployment settings)\n")
            manifest = root / "output/artifacts/sdk/manifest.json"
            manifest.parent.mkdir(parents=True)
            coverage = root / "output/sdk/rust-coverage.json"
            coverage.parent.mkdir(parents=True)
            env = make_environment(poison)
            for language in ("go", "python", "typescript", "rust", "all"):
                for target in ("sdk-generate", "sdk-check", "sdk-package"):
                    with self.subTest(language=language, target=target):
                        manifest.write_text('{"stale":true}\n')
                        coverage.write_text("previous Rust coverage")
                        for prior_language in ("go", "python", "typescript", "rust"):
                            target_directory = manifest.parent / prior_language
                            target_directory.mkdir(exist_ok=True)
                            for name in ("qualification.json", "manifest.json", "package.bin"):
                                (target_directory / name).write_text("previous " + prior_language)
                        before = {p.relative_to(root): runner.digest(p) for p in root.rglob("*") if p.is_file()}
                        # Omit the selector to exercise the real default-all workflow.
                        args = [] if language == "all" else ["SDK_LANGUAGE=" + language]
                        result = subprocess.run(["make", "--no-print-directory", target, "PYTHON=" + sys.executable, *args],
                                                cwd=root, env=env, capture_output=True, text=True, timeout=15, check=False)
                        self.assertNotEqual(result.returncode, 0)
                        self.assertIn("sdk-tools-install", result.stderr)
                        self.assertNotIn("SDK loaded deployment settings", result.stderr)
                        selected = "go" if language == "all" else language
                        self.assertIn(f"SDK {target.removeprefix('sdk-')}: {selected}", result.stdout)
                        self.assertNotIn("completed:", result.stdout)
                        if language == "all":
                            self.assertNotIn("SDK " + target.removeprefix("sdk-") + ": python", result.stdout)
                            self.assertNotIn("SDK " + target.removeprefix("sdk-") + ": typescript", result.stdout)
                        if target == "sdk-package" and language == "all":
                            before.pop(manifest.relative_to(root))
                        if target == "sdk-check":
                            if language in ("rust", "all"):
                                before.pop(coverage.relative_to(root))
                            for invalidated in (("go", "python", "typescript", "rust") if language == "all" else (language,)):
                                for name in ("qualification.json", "manifest.json"):
                                    before.pop((manifest.parent / invalidated / name).relative_to(root))
                        after = {p.relative_to(root): runner.digest(p) for p in root.rglob("*") if p.is_file()}
                        self.assertEqual(after, before)

    def test_make_targets_bypass_env_and_reject_mixed_workflows(self):
        with tempfile.TemporaryDirectory() as directory:
            poison = Path(directory) / "settings.mk"
            poison.write_text("$(error SDK loaded deployment settings)\n")
            environment = make_environment(poison)
            for target, action, language in ((target, action, language) for language in ("go", "python", "typescript", "rust", "all") for target, action in (("sdk-tools-install", "install"), ("sdk-generate", "generate"), ("sdk-check", "check"), ("sdk-package", "package"))):
                with self.subTest(target=target):
                    result = subprocess.run(["make", "-n", target, "SDK_LANGUAGE=" + language], cwd=runner.ROOT,
                                            env=environment, capture_output=True, text=True, timeout=10, check=False)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn(f"runner.py {action} --language", result.stdout)
                    self.assertNotIn("ui-install", result.stdout)
            default = subprocess.run(["make", "-n", "sdk-package"], cwd=runner.ROOT, env=environment,
                                     capture_output=True, text=True, timeout=10, check=False)
            self.assertEqual(default.returncode, 0, default.stderr)
            self.assertIn('runner.py package --language "all"', default.stdout)
            mixed = subprocess.run(["make", "-n", "sdk-check", "build"], cwd=runner.ROOT,
                                   env=environment, capture_output=True, text=True, timeout=10, check=False)
            self.assertNotEqual(mixed.returncode, 0)
            self.assertIn("SDK targets must run alone", mixed.stderr)

    def test_rust_audit_actions_require_an_explicit_rust_selection(self):
        for action in ("audit", "audit-install"):
            for selection in ([], ["--language", "all"], ["--language", "go"]):
                with self.subTest(action=action, selection=selection):
                    result = subprocess.run([sys.executable, str(runner.HERE / "runner.py"), action, *selection],
                                            capture_output=True, text=True, timeout=10, check=False)
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn("requires --language rust", result.stderr)
                    self.assertNotIn("SDK " + action + ":", result.stdout)

    def test_rust_audit_make_targets_are_isolated(self):
        with tempfile.TemporaryDirectory() as directory:
            poison = Path(directory) / "settings.mk"
            poison.write_text("$(error audit loaded deployment settings)\n")
            for target, action in (("sdk-rust-audit-tools-install", "audit-install"), ("sdk-rust-audit", "audit")):
                with self.subTest(target=target):
                    result = subprocess.run(["make", "-n", target], cwd=runner.ROOT,
                                            env=make_environment(poison), capture_output=True, text=True, timeout=10, check=False)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertIn("runner.py " + action + " --language rust", result.stdout)
                    mixed = subprocess.run(["make", "-n", target, "build"], cwd=runner.ROOT,
                                           env=make_environment(poison), capture_output=True, text=True, timeout=10, check=False)
                    self.assertNotEqual(mixed.returncode, 0)
                    self.assertIn("SDK targets must run alone", mixed.stderr)

    def test_vscode_targets_bypass_deployment_settings_and_reject_mixed_goals(self):
        with tempfile.TemporaryDirectory() as directory:
            poison = Path(directory) / "settings.mk"
            poison.write_text("$(error extension loaded deployment settings)\n")
            for target, action in (("vscode-tools-install", "install"), ("vscode-check", "check"), ("vscode-package", "package"), ("test-vscode", "test")):
                with self.subTest(target=target):
                    result = subprocess.run(["make", "-n", target], cwd=runner.ROOT, env=make_environment(poison), capture_output=True, text=True, timeout=10, check=False)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    launcher = "extensions/vscode/tooling/runner.py" if action in ("check", "package") else "test/vscode/runner.py"
                    self.assertIn(launcher + " " + action, result.stdout)
                    mixed = subprocess.run(["make", "-n", target, "build"], cwd=runner.ROOT, env=make_environment(poison), capture_output=True, text=True, timeout=10, check=False)
                    self.assertNotEqual(mixed.returncode, 0)
                    self.assertIn("VS Code", mixed.stderr)


if __name__ == "__main__":
    unittest.main()
