from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import runner
import runner_test_helpers as helpers


class RunnerTests(helpers.RunnerTestsFixture, unittest.TestCase):
    def test_subprocess_output_failure_and_timeout(self):
        log = self.root / "check.log"
        env = runner.environment(offline=True)
        # Owned Python children exercise actual stdout/stderr redirection and
        # exit handling, without invoking a compiler or downloading anything.
        runner.run([sys.executable, "-c", 'print("first child")'], self.root, env, log)
        with self.assertRaises(subprocess.CalledProcessError) as failure:
            runner.run([sys.executable, "-c", 'import sys; print("failed child", file=sys.stderr); sys.exit(7)'], self.root, env, log)
        self.assertEqual(failure.exception.returncode, 7)
        self.assertIn("first child", log.read_text())
        self.assertIn("failed child", log.read_text())
        self.assertIn("failed child", self.stderr.getvalue())
        runner.run([sys.executable, "-c", "pass"], self.root, env)
        for output in (None, log):
            with self.subTest(log=output), patch.object(runner.subprocess, "run", side_effect=subprocess.TimeoutExpired("child", 300)) as child:
                with self.assertRaises(subprocess.TimeoutExpired):
                    runner.run(["child"], self.root, env, output)
                self.assertEqual(child.call_args.kwargs["timeout"], 300)


class MakeBoundaryTests(unittest.TestCase):
    def test_no_environment_include_or_mixed_goals(self):
        with tempfile.TemporaryDirectory() as directory:
            env_file = Path(directory) / "deployment.env"
            env_file.write_text("$(error deployment environment must not be evaluated)\n")
            for target in ("client-go-tools-install", "client-go-generate", "client-go-check"):
                result = subprocess.run(["make", "-n", target, f"CATALOG_ENV_FILE={env_file}"], cwd=runner.ROOT, capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
            result = subprocess.run(["make", "-n", "client-go-check", "help", f"CATALOG_ENV_FILE={env_file}"], cwd=runner.ROOT, capture_output=True, text=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("alone", result.stderr)
            self.assertNotIn("must not be evaluated", result.stderr)
