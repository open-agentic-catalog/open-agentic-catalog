"""Run the public lifecycle entry point against missing and ambiguous local state."""

from pathlib import Path
import json
import os
import shutil
import subprocess
import sys
import tempfile
import unittest


class LabOwnershipTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.lab = self.root / "output/argocd-lab"

    def command(self, action):
        script = Path(__file__).parent / "fixtures/argocd/lab.py"
        return subprocess.run(
            [sys.executable, "-B", str(script.resolve()), action],
            cwd=self.root,
            timeout=5,
            env={**os.environ, "KUBECONFIG": "/unusable/ambient-context"},
            check=False,
            capture_output=True,
            text=True,
        )

    def test_missing_lab_status_and_down_leave_ambient_state_alone(self):
        for action in ("status", "down"):
            outcome = self.command(action)
            self.assertEqual(outcome.returncode, 0, outcome.stderr)
            self.assertIn("No retained", outcome.stdout)
            self.assertFalse(self.lab.exists())

    def test_reuse_fingerprint_covers_built_fixture_inputs_and_excludes_lab_output(self):
        source = Path(__file__).resolve().parent
        versioning = self.root / "test/packaging/versioning.py"
        versioning.parent.mkdir(parents=True)
        shutil.copyfile(source.parent / "packaging/versioning.py", versioning)
        (self.root / "VERSION").write_text("0.1.0\n")
        fixture = self.root / "test/functional/fixture/main.go"
        fixture.parent.mkdir(parents=True)
        fixture.write_text("package main\n")

        def fingerprint():
            return subprocess.check_output(
                [
                    sys.executable,
                    "-B",
                    "-c",
                    "import sys; sys.path.insert(0,sys.argv[1]); import lab; print(lab.fingerprint())",
                    str(source / "fixtures/argocd"),
                ],
                cwd=self.root,
                timeout=5,
                text=True,
            )

        before = fingerprint()
        self.lab.parent.mkdir()
        (self.lab.parent / "run.log").write_text("previous run")
        self.assertEqual(fingerprint(), before)
        fixture.write_text("package main\nfunc main() {}\n")
        self.assertNotEqual(fingerprint(), before)

    def test_down_requires_the_finished_profile_to_match_the_retained_profile(self):
        self.lab.mkdir(parents=True, mode=0o700)
        (self.lab / "owner.json").write_text(json.dumps({"pid": 41}))
        (self.lab / "ready.json").write_text(json.dumps({"profile": "open-agentic-catalog-argocd-aaaaaaaaaaaa"}))
        (self.lab / "finished.json").write_text(
            json.dumps({"cleanup": True, "pid": 41, "profile": "open-agentic-catalog-argocd-bbbbbbbbbbbb"})
        )
        result = self.command("down")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("scoped cleanup failed", result.stderr)
        self.assertTrue((self.lab / "ready.json").exists())

    def test_dangling_inventory_is_ambiguous_for_status_and_teardown(self):
        self.lab.parent.mkdir()
        self.lab.symlink_to(self.root / "missing", target_is_directory=True)
        for action in ("status", "down"):
            outcome = self.command(action)
            self.assertNotEqual(outcome.returncode, 0)
            self.assertIn("owned private directory", outcome.stderr)
            self.assertTrue(self.lab.is_symlink())

    def test_unrecognized_directory_and_symlink_refused_before_teardown(self):
        neighbor = self.root / "neighbor"
        neighbor.mkdir(mode=0o700)
        sentinel = neighbor / "retained"
        sentinel.write_text("unrelated")
        self.lab.parent.mkdir()
        self.lab.symlink_to(neighbor, target_is_directory=True)
        outcome = self.command("down")
        self.assertNotEqual(outcome.returncode, 0)
        self.assertIn("private directory", outcome.stderr)
        self.lab.unlink()
        self.lab.mkdir(mode=0o700)
        (self.lab / "not-owned").write_text("preserve")
        outcome = self.command("down")
        self.assertIn("unrecognized entry", outcome.stderr)
        self.assertTrue((self.lab / "not-owned").exists())
        self.assertEqual(sentinel.read_text(), "unrelated")
