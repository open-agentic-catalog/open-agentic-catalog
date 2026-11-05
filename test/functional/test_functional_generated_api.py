"""Make selection remains offline and bypasses arbitrary workflow includes."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]


class MakeTests(unittest.TestCase):
    def test_owned_workflow_passes_generated_overlay_to_actual_compose(self):
        result = subprocess.run(["make", "--no-print-directory", "-n", "CATALOG_ENV_FILE=/dev/null", "test-functional-workflow"],
                                cwd=ROOT, capture_output=True, timeout=10, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(b"generated_overlay='-f test/functional/generated-api.compose.yaml'", result.stdout)
        self.assertIn(b'$security_overlay $generated_overlay -p "$project"', result.stdout)

    def test_targets_are_separate_and_do_not_load_workflow_files(self):
        with tempfile.TemporaryDirectory() as directory:
            poisoned = Path(directory) / "poison.env"
            poisoned.write_text('$(error workflow file must not load)\n')
            env = {k: v for k, v in os.environ.items() if k not in ("MAKEFLAGS", "MFLAGS")}
            for target in ("schemathesis-tools-install", "test-api-generated", "zap-tools-install", "test-security-passive", "test-security-active", "ui-test-property", "ui-test-mutation", "benchstat-tools-install", "bench-compare"):
                result = subprocess.run(["make", "--no-print-directory", "-n", f"CATALOG_ENV_FILE={poisoned}", target], cwd=ROOT, env=env, capture_output=True, timeout=10, check=False)
                self.assertEqual(result.returncode, 0, result.stderr)
                if target == "test-api-generated":
                    self.assertIn(b"generated_api.py check", result.stdout)
                    self.assertIn(b"run-generated-api", result.stdout)
                    self.assertNotIn(b"pip", result.stdout)
                if target.startswith("test-security-"):
                    self.assertIn(b"security_scan.py check", result.stdout)
                    self.assertNotIn(b"pull", result.stdout)
                    self.assertNotIn(b"pip", result.stdout)
                mixed = subprocess.run(["make", "--no-print-directory", "-n", target, "help"], cwd=ROOT, env=env, capture_output=True, timeout=10, check=False)
                self.assertNotEqual(mixed.returncode, 0)
                self.assertIn(b"must run alone", mixed.stderr)
