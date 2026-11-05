"""Exercise Make and Go process boundaries without downloading license tools."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parents[2]


class LicenseGateTests(unittest.TestCase):
    def test_make_scans_test_and_release_graphs_and_propagates_policy_failures(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            log = base / "calls.jsonl"
            fake = base / "go"
            fake.write_text('''#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
case = os.environ.get("LICENSE_CASE", "pass")
with open(os.environ["LICENSE_CALLS"], "a") as log:
    log.write(json.dumps({"args": args, "flags": os.environ.get("GOFLAGS"),
                         "work": os.environ.get("GOWORK"), "arch": os.environ.get("GOARCH"),
                         "toolchain": os.environ.get("GOTOOLCHAIN")}) + "\\n")
if case == "tool-failure":
    sys.exit(7)
if args[0] == "list":
    tests = "-test" in args
    if case == "empty-discovery":
        sys.exit(0)
    print(json.dumps({"ImportPath": "example.com/owned", "Module": {"Path": "example.com/owned", "Main": True}}))
    release_only = case == "arm64-runtime" and os.environ.get("GOARCH") == "arm64"
    if (tests and case != "arm64-runtime") or case == "runtime" or release_only:
        module = {"Path": "pgregory.net/rapid", "Version": "v1.3.1" if case == "version" else "v1.3.0"}
        if case == "replacement":
            module["Replace"] = {"Path": "../rapid"}
        print(json.dumps({"ImportPath": "pgregory.net/rapid", "Module": module}))
else:
    print("pgregory.net/rapid,https://example.com/LICENSE,MPL-2.0")
    if case == "transitive":
        print("example.com/transitive,https://example.com/LICENSE,MPL-2.0")
    if case == "unknown":
        print("example.com/transitive,Unknown,Unknown")
''')
            fake.chmod(0o755)
            command = ["make", "--no-print-directory", "license-check", "CATALOG_ENV_FILE=/dev/null",
                       "GO=" + str(fake)]
            environment = dict(os.environ, LICENSE_CALLS=str(log), GOFLAGS="-run=NoTests", GOWORK="/missing")
            passed = subprocess.run(command, cwd=ROOT, env=environment, capture_output=True,
                                    text=True, timeout=15, check=False)
            self.assertEqual(passed.returncode, 0, passed.stdout + passed.stderr)
            calls = [json.loads(line) for line in log.read_text().splitlines()]
            self.assertEqual(len(calls), 5)
            self.assertIn("-test", calls[0]["args"])
            self.assertNotIn("-test", calls[1]["args"])
            self.assertEqual([call["arch"] for call in calls[2:4]], ["amd64", "arm64"])
            for call in calls[2:4]:
                self.assertIn("-tags=netgo,osusergo", call["args"])
                for executable in ("./cmd/catalogd", "./cmd/catalogctl", "./catalog-ui/cmd/catalog-ui"):
                    self.assertIn(executable, call["args"])
            report = calls[-1]
            self.assertIn("github.com/google/go-licenses@v1.6.0", report["args"])
            self.assertIn("--include_tests", report["args"])
            self.assertIn("report", report["args"])
            self.assertIn("-tags=functional,integration,helm,minikube", report["flags"])
            for call in calls:
                self.assertEqual(call["work"], "off")
                self.assertEqual(call["toolchain"], "go1.27.3")
                self.assertNotIn("NoTests", call["flags"])
            for case in ("runtime", "arm64-runtime", "version", "replacement", "transitive",
                         "unknown", "empty-discovery", "tool-failure"):
                with self.subTest(case=case):
                    result = subprocess.run(command, cwd=ROOT, env=dict(environment, LICENSE_CASE=case),
                                            capture_output=True, text=True, timeout=15, check=False)
                    self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
                    self.assertIn("Go license check failed:", result.stderr)


if __name__ == "__main__":
    unittest.main()
