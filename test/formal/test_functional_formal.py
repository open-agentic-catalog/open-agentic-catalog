"""Real local process/Make fixtures; no Java or verifier dependency in ordinary gates."""

import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time
import sys
import unittest
from unittest.mock import patch

import run as formal


class FormalBoundaryTests(unittest.TestCase):
    def test_make_targets_are_explicit_isolated_and_do_not_load_workflow_env(self):
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory).resolve() / "loaded"
            envfile = Path(directory).resolve() / "workflow.env"
            envfile.write_text(f"SIDE_EFFECT := $(shell touch {marker})\n")
            for goals, success in ((["test-formal"], True), (["formal-tools-install"], True),
                                   (["test-formal", "check"], False), (["formal-tools-install", "test-formal"], False)):
                result = subprocess.run(["make", "--no-print-directory", "-n", "CATALOG_ENV_FILE=" + str(envfile), *goals],
                                        cwd=formal.ROOT, capture_output=True, text=True, timeout=10, check=False)
                self.assertEqual(result.returncode == 0, success, result.stdout + result.stderr)
                self.assertFalse(marker.exists())
                if goals == ["test-formal"]:
                    self.assertIn("test/formal/run.py check", result.stdout)
                    self.assertNotIn("run.py install", result.stdout)

    def fixture(self, root):
        area = root / formal.AREA
        area.mkdir(parents=True)
        for name in ("contract.txt", "time.gobra", "tool.json"):
            (area / name).write_bytes((formal.ROOT / formal.AREA / name).read_bytes())
        source = root / formal.SOURCE
        source.parent.mkdir(parents=True)
        source.write_bytes((formal.ROOT / formal.SOURCE).read_bytes())
        pin = json.loads((area / "tool.json").read_text())
        java = root / "java"
        java.write_text('''#!/usr/bin/env python3
import os, sys, time
if sys.argv[1:] == ['-version']:
 print('openjdk version "21.0.12"'); sys.exit()
mode = os.environ.get('FORMAL_FIXTURE', 'pass')
if mode == 'timeout':
 if 'FORMAL_STARTED' in os.environ:
  with open(os.environ['FORMAL_STARTED'], 'w') as f: f.write(str(os.getpid()))
 time.sleep(10)
if mode == 'drift':
 with open(os.environ['FORMAL_SOURCE'], 'a') as f: f.write('\\n')
print('Gobra (COMMIT@HEAD)')
print('Verifying package .. - jobs')
if mode == 'empty': sys.exit()
name = sys.argv[sys.argv.index('-i')+1]
if any(name.endswith(x) for x in ('/valid.go', '/calibration-missing-contract.go', '/calibration-vacuous.go')):
 print('Gobra found 0 errors.'); sys.exit()
message = 'Termination measure might not decrease' if 'nondecreasing' in name else 'Postcondition might not hold'
if 'calibration-unsupported' in name: message = "no viable alternative at input '['"
if 'calibration-overflow' in name: message = 'Expression may cause integer overflow'
if mode == 'wrong': message = 'unexpected parser failure'
print('Error at: fixture '+message)
print('Gobra found 1 error.')
sys.exit(1)
'''.replace("COMMIT", pin["gobraCommit"]))
        java.chmod(0o700)
        contents = {"server/server.jar": b"fixture", "z3/bin/z3": b'#!/bin/sh\necho "Z3 version 4.16.0 - 64 bit"\n', "z3/LICENSE.txt": b"fixture license"}
        for name, raw in contents.items():
            path = root / formal.TOOLS / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(raw)
        (root / formal.TOOLS / "z3/bin/z3").chmod(0o700)
        pin["files"] = {name: formal.digest(raw) for name, raw in contents.items()}
        (area / "tool.json").write_text(json.dumps(pin))
        return source

    def test_cli_interruption_kills_owned_child_and_retains_incomplete_report(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            self.fixture(root)
            (root / formal.AREA / "run.py").write_bytes((formal.ROOT / formal.AREA / "run.py").read_bytes())
            common = root / "test/interoperability/common.py"
            common.parent.mkdir(parents=True)
            common.write_bytes((formal.ROOT / "test/interoperability/common.py").read_bytes())
            marker = root / "started"
            env = dict(os.environ, PATH=str(root) + os.pathsep + os.environ["PATH"],
                       FORMAL_FIXTURE="timeout", FORMAL_STARTED=str(marker))
            with subprocess.Popen([sys.executable, "-B", str(root / formal.AREA / "run.py"), "check"],
                                  env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE) as child:
                try:
                    deadline = time.monotonic() + 5
                    while not marker.exists() and time.monotonic() < deadline and child.poll() is None:
                        time.sleep(.01)
                    self.assertTrue(marker.exists(), "fixture verifier did not start")
                    child.terminate()
                    _stdout, stderr = child.communicate(timeout=5)
                    self.assertEqual(child.returncode, 1, stderr)
                    self.assertIn(b"interrupted", stderr)
                    with self.assertRaises(ProcessLookupError):
                        os.kill(int(marker.read_text()), 0)
                finally:
                    if child.poll() is None:
                        child.kill()
                        child.wait(timeout=5)
            reports = list((root / "output/formal").glob("*/report.json"))
            self.assertEqual(len(reports), 1)
            self.assertEqual(json.loads(reports[0].read_text())["status"], "incomplete")

    def test_installed_execution_and_failed_evidence(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            source = self.fixture(root)
            env = dict(PATH=str(root) + os.pathsep + os.environ["PATH"], FORMAL_SOURCE=str(source))
            with patch.dict(os.environ, env), contextlib.redirect_stdout(io.StringIO()):
                report = formal.execute(root)
                self.assertEqual(report["status"], "passed")
                self.assertEqual(len(report["cases"]), 8)
                for mode in ("empty", "wrong", "drift"):
                    with self.subTest(mode=mode), patch.dict(os.environ, FORMAL_FIXTURE=mode), self.assertRaises(ValueError):
                        formal.execute(root)
                original = formal.run
                def bounded(*args, **kwargs):
                    kwargs["timeout"] = .15
                    return original(*args, **kwargs)
                with patch.dict(os.environ, FORMAL_FIXTURE="timeout"), patch.object(formal, "run", bounded), self.assertRaisesRegex(RuntimeError, "deadline"):
                    formal.execute(root)
                with patch.object(formal, "validate", side_effect=InterruptedError("fixture interruption")), self.assertRaises(InterruptedError):
                    formal.execute(root)
            reports = [json.loads(p.read_text()) for p in (root / "output/formal").glob("*/report.json")]
            self.assertEqual(sum(p["status"] == "passed" for p in reports), 1)
            self.assertEqual(sum(p["status"] == "incomplete" for p in reports), 5)
            self.assertTrue(all(p["flags"] == formal.FLAGS for p in reports))
            (root / formal.TOOLS / "server/server.jar").unlink()
            with self.assertRaisesRegex(ValueError, "missing"):
                formal.execute(root)


if __name__ == "__main__":
    unittest.main()
