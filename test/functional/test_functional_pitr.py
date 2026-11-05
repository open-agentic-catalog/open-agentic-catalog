"""Real Make parsing controls; no Docker daemon or database is required."""

from pathlib import Path
import os
import subprocess
import tempfile
import unittest


class PITRMakeTests(unittest.TestCase):
    def test_preparation_ignores_a_selected_workflow_env_file(self):
        with tempfile.TemporaryDirectory() as directory:
            poison = Path(directory) / "private.env"
            poison.write_text("$(error PITR loaded a private environment file)\n", encoding="ascii")
            for target, command in (("test-postgres-pitr-prepare", b"run-pitr-prepare"), ("test-postgres-pitr", b"run-pitr-recovery")):
                with self.subTest(target=target):
                    result = subprocess.run(["make", "--no-print-directory", "-n", target, "CATALOG_ENV_FILE=" + str(poison)],
                        cwd=Path(__file__).resolve().parents[2], env=dict(os.environ), capture_output=True, timeout=10, check=False)
                    self.assertEqual(result.returncode, 0, result.stderr.decode())
                    self.assertIn(b"runner.py " + command, result.stdout)
                    self.assertNotIn(b"test-functional-workflow", result.stdout)
                    self.assertNotIn(b"Functional failed", result.stdout)
                    self.assertNotIn(b"$(MAKE_COMMAND)", result.stdout)

    def test_preparation_refuses_combined_goals(self):
        result = subprocess.run(["make", "--no-print-directory", "-n", "test-postgres-pitr-prepare", "test-backup-restore"],
            cwd=Path(__file__).resolve().parents[2], capture_output=True, timeout=10, check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b"PostgreSQL PITR targets must run alone", result.stderr)
