"""Make/process dispatch checks without Docker, npm, or MCP executables."""

from pathlib import Path
import sys
import subprocess
import signal
import select
import tempfile
import unittest

from common import run
from runner import ROOT, HERE


class Dispatch(unittest.TestCase):
    def test_make_targets_bypass_environment_and_reject_mixed_goals(self):
        with tempfile.TemporaryDirectory() as name:
            poison = Path(name) / "env"
            poison.write_text("$(error deployment env must not load)\n")
            for target, action in (("mcp-tools-install", "install"), ("mcp-catalog-tools-install", "catalog-install"), ("test-interoperability-mcp", "check"), ("test-interoperability-mcp-catalog", "catalog")):
                result = run(["make", "-n", "-C", str(ROOT), target, "CATALOG_ENV_FILE=" + str(poison), "PYTHON=selected-python"])
                self.assertIn("selected-python -B test/interoperability/mcp_runner.py " + action, result.stdout)
                result = run(["make", "-n", "-C", str(ROOT), target, "check"], check=False)
                self.assertNotEqual(result.code, 0)
                self.assertIn("must run alone", result.stderr)

    def test_optimized_python_is_rejected_before_external_tools(self):
        for action in ("check", "catalog", "catalog-install"):
            with self.subTest(action=action):
                result = run([sys.executable, "-O", "-B", str(HERE / "mcp_runner.py"), action], check=False)
                self.assertEqual(result.code, 2)
                self.assertIn("requires Python assertions", result.stderr)

    def test_interruption_waits_until_container_creation_is_registered(self):
        program = r"""
import signal, time
import mcp_tools as tools
from common import Result
def create(*args, **kwargs):
    print("creating", flush=True)
    time.sleep(.2)
    print("created", flush=True)
    return Result(0, "owned-id", "")
def interrupt(*args):
    raise KeyboardInterrupt()
tools.docker = create
tools.safe_args = lambda name: ["--name", name]
signal.signal(signal.SIGTERM, interrupt)
try:
    tools.create("owned", "--network=none", "image")
except KeyboardInterrupt:
    print("interrupted", flush=True)
"""
        child = subprocess.Popen([sys.executable, "-B", "-c", program], cwd=HERE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            self.assertTrue(select.select([child.stdout], [], [], 5)[0], "creation did not start within its bound")
            self.assertEqual(child.stdout.readline().strip(), "creating")
            child.send_signal(signal.SIGTERM)
            stdout, stderr = child.communicate(timeout=5)
            self.assertEqual(child.returncode, 0, stderr)
            self.assertEqual(stdout.splitlines(), ["created", "interrupted"])
        finally:
            if child.poll() is None:
                child.kill()
                child.wait(timeout=5)
            child.stdout.close()
            child.stderr.close()
