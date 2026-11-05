"""Local Git/Make/process controls; no acquired reference tool or Docker required."""

import os
from pathlib import Path
import sys
import tempfile
import unittest

from common import run
from runner import ROOT, HERE
import skills_git as package
from test_unit_skills import representation


class LocalSkillProcesses(unittest.TestCase):
    def test_targets_ignore_deployment_environment_and_run_alone(self):
        with tempfile.TemporaryDirectory() as tmp:
            poison = Path(tmp) / "env"
            poison.write_text("$(error deployment env must not load)\n")
            for target, action in (("skill-tools-install", "install"), ("test-interoperability-skills", "check")):
                result = run(["make", "-n", "-C", str(ROOT), target, "CATALOG_ENV_FILE=" + str(poison), "PYTHON=selected-python"])
                self.assertIn("selected-python -B test/interoperability/skills_runner.py " + action, result.stdout)
                result = run(["make", "-n", "-C", str(ROOT), target, "check"], check=False)
                self.assertNotEqual(result.code, 0)
                self.assertIn("must run alone", result.stderr)

    def test_optimized_python_cannot_disable_assertions(self):
        result = run([sys.executable, "-O", "-B", str(HERE / "skills_runner.py"), "check"], check=False)
        self.assertEqual(result.code, 2)
        self.assertIn("requires Python assertions", result.stderr)

    def test_actual_git_immutable_files_and_missing_objects(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            repo = root / "repo"
            locator = package.seed(repo)
            package.advance(repo)
            destination = root / package.NAME
            package.materialize(repo, locator, destination)
            rep, _, zipped = representation()
            package.verify_inventory(package.read_directory(destination), rep, zipped)
            for changed in (dict(locator, commit="f"*40), dict(locator, directory="absent/" + package.NAME)):
                with self.assertRaises(RuntimeError):
                    package.materialize(repo, changed, root / "bad")
            self.assertFalse((root / "bad").exists())
            blob = package.git(repo, "rev-parse", locator["commit"] + ":" + package.DIRECTORY + "/SKILL.md").decode().strip()
            with self.assertRaises(ValueError):
                package.materialize(repo, dict(locator, commit=blob), root / "bad")

    def test_failed_interrupted_and_expired_runs_clean_owned_state(self):
        program = r'''
import json, os, signal, sys
from pathlib import Path
import skills_runner as r
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
    if mode == 'fail': raise RuntimeError('synthetic failure')
    os.kill(os.getpid(), signal.SIGINT if mode == 'interrupt' else signal.SIGALRM)
r.build_binaries = fail
r.tools.remove = lambda names: None
try: r.check()
except (RuntimeError, TimeoutError): pass
else: raise AssertionError('failure passed')
reports = list(root.glob('*/report.json')); assert len(reports) == 1
report = json.loads(reports[0].read_text())
assert report['cleanup'] and not report['success']
assert not Path(report['private_directory']).exists()
'''
        for mode in ("fail", "interrupt", "expire", "early-interrupt"):
            with tempfile.TemporaryDirectory() as tmp:
                run([sys.executable, "-B", "-c", program, tmp, mode], env=dict(os.environ, PYTHONPATH=str(HERE)), timeout=10)


if __name__ == "__main__":
    unittest.main()
