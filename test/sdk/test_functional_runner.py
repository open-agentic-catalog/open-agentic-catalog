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
    def test_invalid_language_fails_before_acquisition(self):
        result = subprocess.run([sys.executable, str(runner.HERE / "runner.py"), "install", "--language", "not-a-language"],
                                capture_output=True, text=True, timeout=10, check=False)
        self.assertEqual(result.returncode, 2)
        self.assertIn("invalid choice", result.stderr)

    def test_make_without_installed_tools_fails_without_acquisition_or_stale_combined_report(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            scripts = root / "test/sdk"
            scripts.mkdir(parents=True)
            for name in ("runner.py", "python_runner.py", "typescript_runner.py", "rust_runner.py"):
                shutil.copyfile(runner.HERE / name, scripts / name)
            shutil.copytree(runner.HERE / "rust", scripts / "rust")
            for name in ("Makefile", "go.mod", "VERSION"):
                shutil.copyfile(runner.ROOT / name, root / name)
            poison = root / "settings.mk"
            poison.write_text("$(error SDK loaded deployment settings)\n")
            manifest = root / "output/artifacts/sdk/manifest.json"
            manifest.parent.mkdir(parents=True)
            env = make_environment(poison)
            for language in ("go", "python", "typescript", "rust", "all"):
                for target in ("sdk-generate", "sdk-check", "sdk-package"):
                    with self.subTest(language=language, target=target):
                        manifest.write_text('{"stale":true}\n')
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
