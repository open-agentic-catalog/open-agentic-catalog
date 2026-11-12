"""Real Make and process fixtures for the mutation boundary; no tool download."""

import json
import os
import signal
import time
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import mutation as m
from make_test_helpers import ROOT


class MutationBoundaryTests(unittest.TestCase):
    def test_make_bypasses_env_before_inclusion_and_rejects_mixed_goals(self):
        with tempfile.TemporaryDirectory() as directory:
            fixture = Path(directory).resolve()
            marker = fixture / "loaded"
            envfile = fixture / "workflow.env"
            envfile.write_text(f"FAIL := $(shell touch {marker})\n")
            for goals, success in [(["-n", "test-mutation", "MUTATION_PACKAGE=./internal/jsondoc"], True),
                                   (["-n", "test-mutation", "MUTATION_PACKAGE=./internal/platform/fetcher"], True),
                                   (["-n", "test-mutation", "MUTATION_PACKAGE=./internal/platform/authn"], True),
                                   (["-n", "test-mutation", "MUTATION_PACKAGE=./internal/catalog"], True),
                                   (["-n", "test-mutation", "MUTATION_PACKAGE=./internal/platform/httpserver"], True),
                                   (["-n", "test-mutation", "MUTATION_PACKAGE=./internal/audit"], True),
                                   (["-n", "test-mutation", "MUTATION_PACKAGE=./internal/adapter"], True),
                                   (["-n", "test-mutation", "MUTATION_PACKAGE=./catalog-ui/internal/web"], True),
                                   (["-n", "test-mutation", "MUTATION_PACKAGE=./internal/webhook"], True),
                                   (["-n", "test-mutation", "MUTATION_PACKAGE=./internal/publication"], True),
                                   (["-n", "test-mutation", "MUTATION_PACKAGE=./internal/platform/config"], True),
                                   (["test-mutation", "MUTATION_PACKAGE=./internal/jobs"], False),
                                   (["-n", "test-mutation", "test-unit"], False),
                                   (["test-mutation", "MUTATION_PACKAGE=./..."], False),
                                   (["test-mutation"], False)]:
                result = subprocess.run(["make", "--no-print-directory", "CATALOG_ENV_FILE=" + str(envfile), *goals],
                                        cwd=ROOT, capture_output=True, timeout=30)
                self.assertEqual(result.returncode == 0, success, result.stderr.decode())
                self.assertFalse(marker.exists())
            self.assertIn("test-mutation", subprocess.check_output(["make", "help", "CATALOG_ENV_FILE=/dev/null"], cwd=ROOT, text=True, timeout=30))

    def test_hook_records_only_executed_assertion_failures(self):
        with tempfile.TemporaryDirectory(prefix="mutation fixture ") as directory:
            root = Path(directory).resolve()
            (root / "policy.go").write_text("original\n")
            temp = root / "tmp"
            temp.mkdir()
            changed = temp / "policy.go"
            changed.write_text("mutated\n")
            executable = root / "fake-go"
            executable.write_text("#!/usr/bin/env python3\nimport json\n" +
                "events=[{'Action':'start'},{'Action':'run','Test':'TestPolicy'},{'Action':'fail','Test':'TestPolicy'},{'Action':'fail'}]\n" +
                "for e in events: print(json.dumps(dict(Package='example.com/policy',**e)))\nraise SystemExit(1)\n")
            executable.chmod(0o700)
            evidence = root / "evidence"
            evidence.mkdir()
            config = root / "config.json"
            m.write_json(config, dict(mode="execute", root=str(root), evidence=str(evidence), go=str(executable),
                        temporary=str(temp), package="example.com/policy", sources={"policy.go": m.sha(b"original\n")}))
            env = m.environment(os.environ, "local")
            env.update(CATALOG_MUTATION_HOOK=str(config), MUTATE_ORIGINAL=str(root / "policy.go"),
                       MUTATE_CHANGED=str(changed), MUTATE_PACKAGE="example.com/policy")
            args = [sys.executable, "-I", "-B", str(ROOT / "test/packaging/mutation_exec.py")]
            m.run(args, env, root)
            records = list(evidence.glob("*.result.json"))
            self.assertEqual(len(records), 1)
            self.assertEqual(m.decode(m.read(records[0]))["status"], "caught")
            # Repeating a hook cannot replace an earlier result or appear complete.
            with self.assertRaises(m.Failure):
                m.run(args, env, root)
            self.assertEqual((root / "policy.go").read_text(), "original\n")

    def test_make_fixes_exported_selection_and_assertion_settings(self):
        with tempfile.TemporaryDirectory() as directory:
            stub = Path(directory).resolve() / "python"
            stub.write_text('#!/usr/bin/env python3\nimport json,os,sys\nprint(json.dumps({k:os.environ.get(k) for k in ["GOFLAGS","GOWORK","PYTHONOPTIMIZE","MUTATION_PACKAGE"]}))\n')
            stub.chmod(0o700)
            result = subprocess.check_output(["make", "--no-print-directory", "test-mutation", "PYTHON=" + str(stub),
                "GOFLAGS=-run=none -tags=integration", "GOWORK=elsewhere", "PYTHONOPTIMIZE=2",
                "MUTATION_PACKAGE=./internal/identity"], cwd=ROOT, text=True, timeout=30)
            data = json.loads(result.splitlines()[-1])
            self.assertEqual(data, dict(GOFLAGS="-mod=readonly", GOWORK="off", PYTHONOPTIMIZE="0", MUTATION_PACKAGE="./internal/identity"))

    @unittest.skipUnless(sys.platform == "linux", "qualified process group boundary uses Linux /proc")
    def test_outer_deadline_and_cancellation_reap_hook_grandchildren(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            for cancel in (False, True):
                case = root / str(cancel)
                case.mkdir()
                marker = case / "child.json"
                child_code = "import os,time,json; from pathlib import Path; Path(" + repr(str(marker)) + ").write_text(json.dumps([os.getpid(),os.getpgrp()])); time.sleep(30)"
                fake = case / "go"
                fake.write_text("#!/usr/bin/env python3\nimport subprocess,sys,time\nsubprocess.Popen([sys.executable,'-c'," + repr(child_code) + "])\ntime.sleep(30)\n")
                fake.chmod(0o700)
                config = case / "config.json"
                m.write_json(config, dict(mode="baseline", root=str(root), evidence=str(case), go=str(fake), package="example.com/policy"))
                env = m.environment(os.environ, "local")
                env["CATALOG_MUTATION_HOOK"] = str(config)
                args = [sys.executable, "-I", "-B", str(ROOT / "test/packaging/mutation_exec.py")]
                parent = None
                try:
                    if cancel:
                        code = "import sys,signal; sys.path.insert(0," + repr(str(ROOT / 'test/packaging')) + "); import mutation as m; signal.signal(signal.SIGTERM,m.interrupt); m.run(" + repr(args) + "," + repr(env) + "," + repr(str(root)) + ",10)"
                        parent = subprocess.Popen([sys.executable, "-c", code], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
                        deadline = time.monotonic() + 3
                        while not marker.exists() and time.monotonic() < deadline:
                            time.sleep(.02)
                        self.assertTrue(marker.exists())
                        parent.send_signal(signal.SIGTERM)
                        self.assertNotEqual(parent.wait(timeout=5), 0)
                    else:
                        with self.assertRaises(m.Failure):
                            m.run(args, env, root, 1)
                    self.assertTrue(marker.exists())
                    pid, group = json.loads(marker.read_text())
                    deadline = time.monotonic() + 2
                    while time.monotonic() < deadline:
                        stat = Path(f"/proc/{pid}/stat")
                        if not stat.exists() or stat.read_text().split(") ", 1)[1].startswith("Z"):
                            break
                        time.sleep(.02)
                    else:
                        self.fail("owned grandchild still running")
                finally:
                    if marker.exists():
                        _, group = json.loads(marker.read_text())
                        try:
                            os.killpg(group, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                    if parent is not None and parent.poll() is None:
                        parent.kill()
                        parent.wait(timeout=5)

    def test_bounds_and_symlink_refusal(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory).resolve()
            source = root / "source"
            source.write_text("hello")
            link = root / "link"
            link.symlink_to(source)
            self.assertRaises(ValueError, m.read, link)
            with patch.object(m, "LIMIT", 4):
                self.assertRaises(ValueError, m.read, source)
                self.assertRaises(ValueError, m.capture_child, [sys.executable, "-c", "print('too much')"], os.environ)
            with self.assertRaisesRegex(ValueError, "deadline"):
                m.capture_child([sys.executable, "-c", "import time; time.sleep(2)"], os.environ, .05)
