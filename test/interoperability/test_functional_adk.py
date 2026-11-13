"""Real CLI and Make selection controls without Docker, ADK or model execution."""

import os
from pathlib import Path
import json
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import adk_cleanup

ROOT = Path(__file__).resolve().parents[2]


class ADKCommandTests(unittest.TestCase):
    """Keep the explicit native campaign separate from ordinary helper tests."""

    def test_help_does_not_require_installed_native_adk(self):
        """A contributor can inspect the supported actions before acquisition."""
        result = subprocess.run([sys.executable, "-B", str(ROOT / "test/interoperability/adk_runner.py"), "--help"], capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("{install,check}", result.stdout)

    def test_cleanup_help_and_optimized_refusal_require_no_native_environment(self):
        """The lifecycle controller can be inspected before installing its SDK image."""
        script = str(ROOT / "test/interoperability/adk_cleanup.py")
        help_result = subprocess.run([sys.executable, "-B", script, "--help"], capture_output=True, text=True, timeout=10)
        self.assertEqual(help_result.returncode, 0, help_result.stderr)
        self.assertIn("SIGTERM", help_result.stdout)
        optimized = subprocess.run([sys.executable, "-O", "-B", script], capture_output=True, text=True, timeout=10)
        self.assertEqual(optimized.returncode, 2)
        self.assertNotIn("ADK cleanup report:", optimized.stdout)

    def test_cleanup_controls_cannot_be_applied_to_tool_acquisition(self):
        """Invalid action combinations fail before downloads or Docker inspection."""
        for control in ("metadata-mismatch", "interrupt"):
            result = subprocess.run([sys.executable, "-B", str(ROOT / "test/interoperability/adk_runner.py"), "install", "--control", control], capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 2)
            self.assertIn("--control is only supported with check", result.stderr)

    def test_optimized_python_refuses_before_tools_or_environment(self):
        """Assertion removal must not turn a qualification into an unchecked pass."""
        result = subprocess.run([sys.executable, "-O", "-B", str(ROOT / "test/interoperability/adk_runner.py"), "check"], capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 2)
        self.assertIn("ADK qualification requires Python assertions", result.stderr)
        self.assertNotIn("ADK report:", result.stdout)

    def test_make_targets_are_explicit_separate_and_skip_local_env_files(self):
        """Dry-run the actual Make admission without starting a native process."""
        env = dict(os.environ, CATALOG_ENV_FILE="/does-not-exist/ADK_PRIVATE_SENTINEL")
        for target, invocation in (("adk-tools-install", "adk_runner.py install"), ("test-interoperability-adk", "adk_runner.py check"),
                                   ("test-interoperability-adk-cleanup", "adk_cleanup.py")):
            result = subprocess.run(["make", "--no-print-directory", "-n", target], cwd=ROOT, env=env, capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(invocation, result.stdout)
            self.assertIn("PYTHONOPTIMIZE=0", result.stdout)
            self.assertNotIn("ADK_PRIVATE_SENTINEL", result.stdout + result.stderr)
            combined = subprocess.run(["make", "--no-print-directory", "-n", target, "test-unit"], cwd=ROOT, env=env, capture_output=True, text=True, timeout=10)
            self.assertNotEqual(combined.returncode, 0)
            self.assertIn("run alone", combined.stderr)

    def test_cleanup_monitor_signals_only_its_real_child_at_readiness(self):
        """Use a local Python process to qualify PID ownership and the readiness handshake."""
        with tempfile.TemporaryDirectory(prefix="adk-cleanup-local-", dir=ROOT / "output") as temporary:
            root = Path(temporary)
            children = root / "children"
            directory = children / "run-local"
            directory.mkdir(parents=True)
            sentinel = root / "unrelated"
            sentinel.write_text("retained")
            script = root / "child.py"
            script.write_text('''import json,signal,sys,time,shutil
from pathlib import Path
directory=Path(sys.argv[1]);private=directory/'private'
progress=private/'progress';progress.mkdir(parents=True)
report=directory/'report.json'
report.write_text(json.dumps({'phase':'running','cleanup':False}))
def finish(*_):
 shutil.rmtree(private)
 report.write_text(json.dumps({'phase':'interrupted','cleanup':True}))
 sys.exit(2)
signal.signal(signal.SIGTERM,finish)
signal.alarm(10)
print('ADK report: '+str(directory.relative_to(Path.cwd())),flush=True)
(progress/'interrupt-ready').write_text('interrupt-ready\\n')
while True:time.sleep(.01)
''')
            log = root / "child.log"
            with log.open("w") as stream, patch.object(adk_cleanup.native.tools, "OUTPUT", children):
                child = subprocess.Popen([sys.executable, "-B", str(script), str(directory)], cwd=ROOT, stdout=stream, stderr=subprocess.STDOUT, start_new_session=True)
                try:
                    report, sent = adk_cleanup.monitor(child, log, "interrupt")
                finally:
                    adk_cleanup.stop(child)
            self.assertTrue(sent)
            self.assertEqual(child.returncode, 2)
            self.assertEqual(json.loads(report.read_text()), {"phase": "interrupted", "cleanup": True})
            self.assertEqual(sentinel.read_text(), "retained")


if __name__ == "__main__":
    unittest.main()
