"""Make and supervisor lifecycle controls without acquiring or executing A2A tools."""

import json
import os
from pathlib import Path
import sys
import tempfile
import unittest

from common import run
from runner import ROOT, HERE


class Dispatch(unittest.TestCase):
    def test_targets_are_explicit_and_ignore_environment(self):
        with tempfile.TemporaryDirectory() as name:
            poison = Path(name) / "env"
            poison.write_text("$(error deployment env must not load)\n")
            for target, action in (("a2a-tools-install", "install"), ("test-interoperability-a2a", "check")):
                result = run(["make", "-n", "-C", str(ROOT), target, "CATALOG_ENV_FILE=" + str(poison), "PYTHON=selected-python"])
                self.assertIn("selected-python -B test/interoperability/a2a_runner.py " + action, result.stdout)
                result = run(["make", "-n", "-C", str(ROOT), target, "check"], check=False)
                self.assertNotEqual(result.code, 0)
                self.assertIn("must run alone", result.stderr)

    def test_optimized_python_is_rejected_before_tools(self):
        result = run([sys.executable, "-O", "-B", str(HERE / "a2a_runner.py"), "check"], check=False)
        self.assertEqual(result.code, 2)
        self.assertIn("requires Python assertions", result.stderr)

    def test_creation_defers_signals_and_preserves_daemon_uncertainty(self):
        # Exercise actual signal masking in a child, not a mocked signal handler.
        program = r'''
import json, os, signal, sys
import a2a_tools as tools
from common import Result
events = []
def docker(*args, **kwargs):
    events.append('creating')
    os.kill(os.getpid(), signal.SIGTERM)
    events.append('created')
    if sys.argv[1] == 'unknown':
        raise RuntimeError('daemon reply lost')
    return Result(0, 'owned-container', '')
tools.docker = docker
tools.safe_args = lambda name: ['--name', name]
signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
try:
    tools.create('owned', '--network=none', 'image')
except tools.CreationUnconfirmed:
    events.append('unknown')
except KeyboardInterrupt:
    events.append('interrupted')
assert events == ['creating', 'created', sys.argv[1]], events
print(json.dumps(events))
'''
        for outcome in ("interrupted", "unknown"):
            with self.subTest(outcome=outcome):
                result = run([sys.executable, "-B", "-c", program, outcome],
                             env=dict(os.environ, PYTHONPATH=str(HERE)), timeout=10)
                self.assertEqual(json.loads(result.stdout), ["creating", "created", outcome])

    def test_failed_interrupted_and_expired_launches_clean_private_state(self):
        # Exercise the real supervisor in a child process, with its Docker boundary inert.
        program = r'''
import json, os, signal, sys
from pathlib import Path
import a2a_runner as runner
from common import Result
root = Path(sys.argv[1])
mode = sys.argv[2]
runner.tools.OUTPUT = root
runner.local_daemon = lambda: 'amd64'
runner.tools.installed = lambda: {'image': 'sha256:fixture'}
runner.build_binaries = lambda directory, go, arch: [(directory / name).write_bytes(b'fixture') for name in ('catalogd', 'catalogctl')]
runner.certificates = lambda private: (private / 'key.pem').write_text('private-marker')
owned = []
private_dirs = []
def create(name, *args):
    owned.append(name)
    private_dirs.append(Path(next(a for a in args if a.startswith('type=bind,src=')).split('src=')[1].split(',')[0]).parent)
    if mode == 'interrupt':
        os.kill(os.getpid(), signal.SIGTERM)
    if mode == 'deadline':
        os.kill(os.getpid(), signal.SIGALRM)
    if mode == 'unconfirmed':
        raise runner.tools.CreationUnconfirmed('daemon creation unconfirmed')
    raise RuntimeError('controlled launcher failure')
runner.tools.create = create
runner.tools.remove = lambda names: [owned.remove(n) for n in names]
runner.docker = lambda *a, **kw: Result(1, '', 'controlled absent fixture')
signal.signal(signal.SIGTERM, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
try:
    runner.check()
except (RuntimeError, KeyboardInterrupt, TimeoutError):
    pass
else:
    raise AssertionError('failed launch passed')
assert not owned and private_dirs and all(not p.exists() for p in private_dirs)
report = json.loads(next(root.glob('*/report.json')).read_text())
assert report['cleanup'] is (mode != 'unconfirmed') and not report['success'] and not report['cases']
assert bool(report.get('creation_unconfirmed')) is (mode == 'unconfirmed')
print(json.dumps({'cleanup': report['cleanup'], 'error': report['error']}))
'''
        with tempfile.TemporaryDirectory() as temporary:
            for mode in ("failure", "interrupt", "deadline", "unconfirmed"):
                with self.subTest(mode=mode):
                    directory = Path(temporary) / mode
                    # Set only the module search path; no peer SDK is imported here.
                    result = run([sys.executable, "-B", "-c", program, str(directory), mode],
                                 env=dict(os.environ, PYTHONPATH=str(HERE)), timeout=10)
                    summary = json.loads(result.stdout.splitlines()[-1])
                    self.assertEqual(summary["cleanup"], mode != "unconfirmed")
                    self.assertIn({"failure": "controlled launcher failure", "interrupt": "KeyboardInterrupt", "deadline": "600 seconds", "unconfirmed": "daemon creation unconfirmed"}[mode], summary["error"])
