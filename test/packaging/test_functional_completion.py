"""Actual native runner/Make controls with disposable synthetic tests only."""
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest

import completion

ROOT = Path(__file__).resolve().parents[2]


class CompletionFunctionalTests(unittest.TestCase):
    def test_python_native_required_files_and_skips_stop_completion(self):
        for label, body in (("healthy", "    def test_value(self): self.assertEqual(2 + 2, 4)\n"),
                            ("skipped", "    @unittest.skip('synthetic')\n    def test_value(self): pass\n"),
                            ("failed", "    def test_value(self): self.fail('synthetic assertion')\n"),
                            ("expected failure", "    @unittest.expectedFailure\n    def test_value(self): self.fail('synthetic expected failure')\n"),
                            ("unexpected success", "    @unittest.expectedFailure\n    def test_value(self): pass\n"),
                            ("empty", "    pass\n")):
            with self.subTest(label=label), tempfile.TemporaryDirectory(prefix="oac-required-python-") as directory:
                fixture = Path(directory)
                (fixture / "test_unit_fixture.py").write_text("import unittest\nclass Case(unittest.TestCase):\n" + body)
                (fixture / "test_unit_sibling.py").write_text("import unittest\nclass Sibling(unittest.TestCase):\n    def test_ok(self): pass\n")
                result = subprocess.run([sys.executable, "-B", completion.__file__, "python", "--directory", fixture],
                                        capture_output=True, text=True, timeout=15, check=False)
                self.assertEqual(result.returncode == 0, label == "healthy", result.stdout + result.stderr)
                if label == "empty":
                    self.assertIn("required test file has no cases", result.stderr)

    def test_actual_architecture_make_requires_each_named_owner(self):
        required = ("TestArchitectureContracts", "TestArchitectureAnalyzer", "TestArchitectureDomainOwnership")
        env = {key: value for key, value in os.environ.items() if key not in ("MAKEFLAGS", "MFLAGS", "MAKEOVERRIDES", "GOWORK", "GOFLAGS")}
        env.update(GOTOOLCHAIN="local", GOTELEMETRY="off", GOPROXY="off", GOSUMDB="off")
        with tempfile.TemporaryDirectory(prefix="oac-required-go-") as directory:
            fixture = Path(directory)
            (fixture / "test/packaging").mkdir(parents=True)
            (fixture / "internal/platform/runtime").mkdir(parents=True)
            shutil.copyfile(ROOT / "Makefile", fixture / "Makefile")
            shutil.copyfile(Path(completion.__file__), fixture / "test/packaging/completion.py")
            (fixture / "go.mod").write_text("module fixture.example/required\n\ngo 1.27.3\n")
            controls = [("healthy", None, "")]
            controls += [(mode, name, "t.Skip(\"synthetic\")" if mode == "skipped" else "")
                         for name in required for mode in ("missing", "skipped")]
            controls.append(("failed", required[0], 't.Fatal("synthetic assertion")'))
            for mode, selected, action in controls:
                with self.subTest(mode=mode, selected=selected):
                    source = 'package runtime\nimport "testing"\n'
                    for name in required:
                        actual = name + "Renamed" if mode == "missing" and name == selected else name
                        source += "func " + actual + "(t *testing.T) {" + (action if name == selected else "") + "}\n"
                    (fixture / "internal/platform/runtime/fixture_test.go").write_text(source)
                    result = subprocess.run(["make", "--no-print-directory", "architecture-check", "CATALOG_ENV_FILE=/dev/null", "PYTHON=" + sys.executable],
                                            cwd=fixture, env=env, capture_output=True, text=True, timeout=45, check=False)
                    self.assertEqual(result.returncode == 0, mode == "healthy", result.stdout + result.stderr)
                    if mode == "missing":
                        self.assertIn("missing or incomplete: " + selected, result.stderr)
                    if mode == "skipped":
                        self.assertIn("failed or skipped", result.stderr)
