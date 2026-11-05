"""Exercise bounded launcher processes and real Make wiring with local stubs."""

from argparse import Namespace
from contextlib import redirect_stderr, redirect_stdout
from io import StringIO
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

import dead_code
from make_test_helpers import ROOT, MakeScopeTestsFixture


class DeadCodeLauncherTests(unittest.TestCase):
    def test_launcher_uses_host_tool_and_fixed_complete_target_selections(self):
        with tempfile.TemporaryDirectory(prefix="deadcode fixture ") as directory:
            root = Path(directory)
            go = root / "go stub"
            log = root / "calls.jsonl"
            analyzer = """#!/usr/bin/env python3
import json, os, sys
with open(os.environ['DEADCODE_CALLS'], 'a') as log:
    env = {k: os.environ.get(k) for k in ['GOOS','GOARCH','GOFLAGS','GOWORK','GOPACKAGESDRIVER','GOTOOLCHAIN','CGO_ENABLED']}
    log.write(json.dumps([['analyze', *sys.argv[1:]], env]) + chr(10))
mode = os.environ.get('DEADCODE_FAILURE', '')
if mode == 'analyzer': sys.exit(3)
print('{' if mode == 'malformed' else '' if mode == 'missing' else 'null')
"""
            # The stub simulates the Go launcher, not application behavior.
            go.write_text('''#!/usr/bin/env python3
import json, os, pathlib, sys
args = sys.argv[1:]
with open(os.environ['DEADCODE_CALLS'], 'a') as log:
    log.write(json.dumps([args, {k: os.environ.get(k) for k in ['GOOS','GOARCH','GOFLAGS','GOWORK','GOPACKAGESDRIVER','GOTOOLCHAIN','CGO_ENABLED']}]) + '\\n')
mode = os.environ.get('DEADCODE_FAILURE', '')
if mode == args[0]:
    print('fixture tool failure', file=sys.stderr)
    sys.exit(2)
if args[:2] == ['env', 'GOHOSTOS']:
    print('linux\\namd64')
elif args[:2] == ['env', 'GOVERSION']:
    print('go1.27.0')
elif args[0] == 'install':
    binary = pathlib.Path(os.environ['GOBIN']) / 'deadcode'
    binary.write_text(os.environ['DEADCODE_ANALYZER_STUB'])
    binary.chmod(0o700)
    print('go: downloading fixture (simulated)', file=sys.stderr)
elif args[0] == 'version':
    version = 'v0.49.0' if mode == 'version-mismatch' else 'v0.50.0'
    print(args[-1] + ': go1.27.0\\n\\tpath\\tgolang.org/x/tools/cmd/deadcode\\n\\tmod\\tgolang.org/x/tools\\t' + version + '\\tchecksum')
elif args[0] == 'list':
    if mode == 'empty': sys.exit(0)
    if mode == 'unmatched': print('warning: matched no packages', file=sys.stderr)
    for main in ['cmd/catalogd','cmd/catalogctl','catalog-ui/cmd/catalog-ui']:
        print('example.com/catalog/' + main + '\\tmain\\texample.com/catalog')
    print('example.com/catalog/orphan\\torphan\\texample.com/catalog')
else: sys.exit(4)
''')
            go.chmod(0o700)
            options = Namespace(go=str(go), toolchain="go1.27.0", version="v0.50.0", module="example.com/catalog", application_root=["./cmd/...", "./internal/...", "./catalog-ui/cmd/..."], source_root=["./cmd/...", "./test/..."])
            with patch.dict(os.environ, {"DEADCODE_ANALYZER_STUB": analyzer, "DEADCODE_FAILURE": "", "DEADCODE_CALLS": str(log), "GOFLAGS": "-tags=wrong", "GOWORK": "wrong", "GOARCH": "arm64", "GOPACKAGESDRIVER": "untrusted"}):
                reports, descriptions = dead_code.analyze(options, root)
                self.assertEqual(reports, {scope[0]: {} for scope in dead_code.SCOPES})
                self.assertEqual(len(descriptions), 6)
                calls = [json.loads(line) for line in log.read_text().splitlines()]
                install = next(env for args, env in calls if args[0] == "install")
                self.assertEqual((install["GOOS"], install["GOARCH"]), ("linux", "amd64"))
                lists = [(args, env) for args, env in calls if args[0] == "list"]
                self.assertEqual(len(lists), 5)
                self.assertEqual([env["GOARCH"] for _, env in lists], ["amd64", "arm64", "amd64", "amd64", "amd64"])
                self.assertIn("./internal/...", lists[0][0])
                self.assertIn("./test/...", lists[2][0])
                analyses = [(args, env) for args, env in calls if args[0] == "analyze"]
                self.assertEqual(len(analyses), 5)
                self.assertEqual([env["GOARCH"] for _, env in analyses], ["amd64", "arm64", "amd64", "amd64", "amd64"])
                self.assertEqual(["-test" in args for args, _ in analyses], [False, False, True, True, True])
                for args, env in analyses:
                    self.assertIn("example.com/catalog/orphan", args)
                    self.assertEqual(env["CGO_ENABLED"], "0")
                for index, (_, _, tags, _) in enumerate(dead_code.SCOPES):
                    self.assertIn("-tags=" + tags, analyses[index][0])
                for _, env in calls:
                    self.assertEqual(env["GOFLAGS"], "-mod=readonly")
                    self.assertEqual(env["GOWORK"], "off")
                    self.assertEqual(env["GOPACKAGESDRIVER"], "off")
                for failure in ["env", "install", "version-mismatch", "list", "empty", "unmatched", "analyzer", "malformed", "missing"]:
                    with self.subTest(failure=failure), patch.dict(os.environ, {"DEADCODE_FAILURE": failure}), self.assertRaises(ValueError):
                        dead_code.analyze(options, root)
                self.assertEqual(list((root / "output").iterdir()), [])

    def test_process_timeout_missing_tool_and_cli_exit_policy(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            with self.assertRaises(OSError):
                dead_code.run([str(root / "missing"), "run"], root, os.environ)
            with self.assertRaises(subprocess.TimeoutExpired):
                dead_code.run(["python3", "-c", "import time; time.sleep(60)"], root, os.environ, timeout=0.05)
        args = ["report", "--go=go", "--toolchain=go1.27.0", "--version=v0.50.0", "--module=example.com/catalog", "--application-root=./cmd/...", "--source-root=./test/..."]
        reports = {scope[0]: {} for scope in dead_code.SCOPES}
        reports["production-amd64"] = {("example.com/catalog/lib", "Unused"): "lib/code.go:1:1"}
        with patch.object(dead_code, "analyze", return_value=(reports, ["fixture configuration"])), patch.object(dead_code, "parse_exceptions", return_value={}), redirect_stdout(StringIO()) as output, redirect_stderr(StringIO()) as error:
            self.assertEqual(dead_code.main(args), 0)
            self.assertEqual(dead_code.main(["check", *args[1:]]), 1)
            self.assertIn("Unused", output.getvalue())
            self.assertIn("1 unreviewed", error.getvalue())
        with patch.object(dead_code, "analyze", side_effect=ValueError("load failed")), patch.object(dead_code, "parse_exceptions", return_value={}), redirect_stderr(StringIO()) as error:
            self.assertEqual(dead_code.main(args), 1)
            self.assertIn("load failed", error.getvalue())


class DeadCodeMakeTests(MakeScopeTestsFixture, unittest.TestCase):
    def test_public_targets_reuse_roots_and_fixed_environment(self):
        for target, mode in [("dead-code-report", "report"), ("dead-code-check", "check")]:
            output = self.run_make("-n", target)
            self.assertIn("dead_code.py " + mode, output)
            self.assertIn("--version \"v0.51.0\"", output)
            self.assertIn("--application-root ./catalog-ui/internal/...", output)
            self.assertIn("--source-root ./examples/...", output)
            self.assertNotIn("--application-root ./test/", output)
            self.assertNotIn("./specs/", output)
            output = self.run_make("-s", "-f", "Makefile", "-f", "-", target, "GOFLAGS=-tags=wrong", "GOWORK=wrong", input=target + ":\n\t@printf '%s\\n' \"$$GOFLAGS\" \"$$GOWORK\" \"$$PYTHONOPTIMIZE\"\n")
            self.assertEqual(output.splitlines(), ["-mod=readonly", "off", "0"])
        self.assertNotIn("dead_code.py", self.run_make("-n", "test-unit"))

    def test_failures_propagate_through_lint_and_aggregate_targets(self):
        # Override sibling recipes in a disposable Makefile; run no other gates.
        for target in ["lint", "check", "test-all", "verify"]:
            with tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / "Makefile"
                path.write_text((ROOT / "Makefile").read_text() + "\n.PHONY: force-deadcode-fixture\nforce-deadcode-fixture:\n\t@false\ndead-code-check: force-deadcode-fixture\n")
                result = subprocess.run(["make", "-sf", str(path), "-o", "fmt-check", "-o", "module-check", "-o", "license-check", "-o", "test-integration-compile", "-o", "test-deployment-compile", "-o", "architecture-check", "-o", "shell-check", "CATALOG_ENV_FILE=/dev/null", target], cwd=ROOT, capture_output=True, text=True, timeout=20)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("force-deadcode-fixture", result.stderr)
