"""Make and subprocess property fixtures; actual Rapid qualification is explicit."""

import contextlib
import io
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import property as p


class PropertyBoundaryTests(unittest.TestCase):
    def test_make_isolation_and_normal_suite_settings(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            marker, envfile = root / "loaded", root / "workflow.env"
            envfile.write_text(f"SIDE_EFFECT := $(shell touch {marker})\n")
            for goals, valid in ((["test-property"], True), (["test-property", "test-unit"], False)):
                result = subprocess.run(["make", "--no-print-directory", "-n", "CATALOG_ENV_FILE=" + str(envfile), *goals],
                                        cwd=p.ROOT, capture_output=True, text=True, check=False, timeout=10)
                self.assertEqual(result.returncode == 0, valid, result.stdout + result.stderr)
                self.assertFalse(marker.exists())
            probe = root / "environment.py"
            probe.write_text("import os,json\nprint(json.dumps({k:v for k,v in os.environ.items() if k.startswith(('RAPID_', 'PROPERTY_'))}))\n")
            result = subprocess.run(["make", "--no-print-directory", "test-property", "PYTHON=python3 " + str(probe),
                                     "PROPERTY_CHECKS=321", "PROPERTY_SEED=123", "RAPID_CHECKS=0", "RAPID_FAILFILE=missing"],
                                    cwd=p.ROOT, env=dict(os.environ, MAKELEVEL="1", RAPID_DEBUG="true", RAPID_FUTURE="bad"),
                                    capture_output=True, text=True, timeout=10, check=True)
            values = json.loads(result.stdout.splitlines()[-1])
            self.assertEqual(values["PROPERTY_CHECKS"], "321")
            self.assertEqual(values["PROPERTY_SEED"], "123")
            self.assertEqual(values["RAPID_CHECKS"], "100")
            self.assertEqual(values["RAPID_SEED"], "1")
            self.assertEqual(values["RAPID_FAILFILE"], "")
            self.assertEqual(values["RAPID_DEBUG"], "false")
            self.assertNotIn("RAPID_FUTURE", values)
            for value in ("0", "-1", "no", "10001"):
                result = subprocess.run(["make", "--no-print-directory", "test-property", "PROPERTY_CHECKS=" + value],
                                        cwd=p.ROOT, capture_output=True, text=True, timeout=10, check=False)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("invalid property checks", result.stderr)

    def test_launcher_retains_success_and_rejects_incomplete_or_failed_execution(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "go.mod").write_text("module example.com/property\n")
            (root / "go.sum").write_text("fixture\n")
            (root / "sample").mkdir()
            (root / "sample/property_test.go").write_text("package sample\n")
            go = root / "go"
            go.write_text('''#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
case = os.environ.get("PROPERTY_FIXTURE", "pass")
if args[0] == "env":
    print("go1.27.0")
elif "-m" in args:
    print(json.dumps({"Version": "v1.3.0", "Sum": "h1:fixture"}))
elif "-f" in args:
    print("example.com/property/sample")
elif "-list=^TestProperty" in args:
    if case != "missing":
        print("TestPropertySample")
else:
    count = 1 if case == "partial" else int(os.environ["RAPID_CHECKS"])
    name, package = "TestPropertySample", "example.com/property/sample"
    status = "fail" if case == "failure" else "skip" if case == "skip" else "pass"
    output = "[rapid] OK, passed %d tests (1ms)\\n" % count
    if case == "replay":
        output = "[rapid] ignoring fail file: broken\\n" + output
    if case == "failure":
        output = "[rapid] failed after 1 tests: wrong result\\n"
    for row in ({"Action":"start"}, {"Action":"run","Test":name},
                {"Action":"output","Test":name,"Output":output},
                {"Action":status,"Test":name}, {"Action":"fail" if status == "fail" else "pass"}):
        print(json.dumps(dict(Package=package, **row)))
    sys.exit(1 if case == "failure" else 0)
''')
            go.chmod(0o700)
            with contextlib.redirect_stdout(io.StringIO()):
                report = p.execute(str(go), "go1.27.0", p.settings(), root, ("./sample",))
            self.assertTrue(report["complete"])
            self.assertEqual(report["packages"][0]["checksPerProperty"], 1000)
            for case in ("partial", "missing", "skip", "replay", "failure"):
                with self.subTest(case=case), patch.dict(os.environ, PROPERTY_FIXTURE=case), \
                        contextlib.redirect_stdout(io.StringIO()), self.assertRaises(ValueError):
                    p.execute(str(go), "go1.27.0", p.settings(), root, ("./sample",))
            self.assertEqual(len(list((root / "output/property").glob("*/report.json"))), 1)
            self.assertEqual(len(list((root / "output/property").glob("*/failure.json"))), 5)


if __name__ == "__main__":
    unittest.main()
