"""Local Make/process controls; no acquired reference tool or Docker required."""

import os
from pathlib import Path
import sys
import tempfile
import unittest

from common import run
from runner import ROOT, HERE


class LocalPluginProcesses(unittest.TestCase):
    def test_targets_ignore_deployment_environment_and_run_alone(self):
        with tempfile.TemporaryDirectory() as tmp:
            poison = Path(tmp) / "env"
            poison.write_text("$(error deployment env must not load)\n")
            for target, action in (
                ("plugin-tools-install", "install"),
                ("test-interoperability-plugins", "check"),
            ):
                result = run(
                    [
                        "make",
                        "-n",
                        "-C",
                        str(ROOT),
                        target,
                        "CATALOG_ENV_FILE=" + str(poison),
                        "PYTHON=selected-python",
                    ]
                )
                self.assertIn(
                    "selected-python -B test/interoperability/plugins_runner.py "
                    + action,
                    result.stdout,
                )
                result = run(
                    ["make", "-n", "-C", str(ROOT), target, "check"], check=False
                )
                self.assertNotEqual(result.code, 0)
                self.assertIn("must run alone", result.stderr)

    def test_optimized_python_cannot_disable_assertions(self):
        result = run(
            [sys.executable, "-O", "-B", str(HERE / "plugins_runner.py"), "check"],
            check=False,
        )
        self.assertEqual(result.code, 2)
        self.assertIn("requires Python assertions", result.stderr)

    def test_failed_interrupted_and_expired_runs_clean_owned_state(self):
        program = r"""
import json, os, signal, sys
from pathlib import Path
import plugins_runner as r
root = Path(sys.argv[1]); mode = sys.argv[2]
r.tools.OUTPUT = root
r.tools.installed = lambda: {'image': 'sha256:fixture', 'go': 'go'}
r.local_daemon = lambda: 'amd64'
r.fingerprint = lambda _: 'source'
if mode == 'early-interrupt':
    import tempfile
    original_tempfile = tempfile.mkdtemp
    r.tempfile.mkdtemp = lambda prefix: original_tempfile(prefix=prefix, dir=root)
    original_json = json.dumps
    first = [True]
    def interrupt_report(*args, **kwargs):
        if first[0]:
            first[0] = False
            os.kill(os.getpid(), signal.SIGINT)
        return original_json(*args, **kwargs)
    r.json.dumps = interrupt_report
def fail(*args):
    if mode in ('fail', 'cleanup-failure'): raise RuntimeError('synthetic failure')
    os.kill(os.getpid(), signal.SIGINT if mode == 'interrupt' else signal.SIGALRM)
r.build_binaries = fail
def remove(names):
    if mode == 'cleanup-failure': raise RuntimeError('synthetic cleanup failure')
r.tools.remove = remove
try: r.check()
except (RuntimeError, TimeoutError): pass
else: raise AssertionError('failure passed')
reports = list(root.glob('*/report.json')); assert len(reports) == 1
report = json.loads(reports[0].read_text())
assert report['cleanup'] == (mode != 'cleanup-failure') and not report['success']
assert not Path(report['private_directory']).exists()
"""
        for mode in (
            "fail",
            "interrupt",
            "expire",
            "early-interrupt",
            "cleanup-failure",
        ):
            with tempfile.TemporaryDirectory() as tmp:
                run(
                    [sys.executable, "-B", "-c", program, tmp, mode],
                    env=dict(os.environ, PYTHONPATH=str(HERE)),
                    timeout=10,
                )


if __name__ == "__main__":
    unittest.main()
