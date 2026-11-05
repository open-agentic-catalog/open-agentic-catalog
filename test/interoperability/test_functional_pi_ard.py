"""Local subprocess/Make guards; never load the independently acquired Pi host."""

import os
from pathlib import Path
import subprocess
import tempfile
import unittest

import pi_ard as pi


class PiFunctional(unittest.TestCase):
    def test_native_script_budget_units_without_peer_dependencies(self):
        node = pi.NODE / "bin/node"
        if not node.is_file():
            self.skipTest("install the pinned repository Node toolchain")
        reply = subprocess.run(
            [str(node), "--test", str(pi.INPUTS / "script.unit.test.mjs")],
            capture_output=True,
            text=True,
            timeout=20,
            check=False,
        )
        self.assertEqual(reply.returncode, 0, reply.stdout + reply.stderr)
        self.assertIn("# pass 4", reply.stdout)

    def test_new_targets_ignore_workflow_environment_and_refuse_mixed_goals(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "Makefile").write_bytes((pi.ROOT / "Makefile").read_bytes())
            (root / ".env").write_text("$(error workflow environment must not load)\n")
            env = {
                k: v for k, v in os.environ.items() if k not in ("MAKEFLAGS", "MFLAGS", "MAKELEVEL", "MAKEOVERRIDES")
            }
            for target, action in (
                ("pi-ard-tools-install", "install"),
                ("test-interoperability-pi-ard-prepare", "prepare"),
                ("test-interoperability-pi-ard", "check"),
            ):
                result = subprocess.run(
                    ["make", "-n", "PYTHON=python3", target],
                    cwd=root,
                    env=env,
                    capture_output=True,
                    text=True,
                    timeout=15,
                    check=False,
                )
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("pi_ard.py " + action, result.stdout)
                result = subprocess.run(
                    ["make", "-n", "PYTHON=python3", target, "build"],
                    cwd=root,
                    env=env,
                    capture_output=True,
                    text=True,
                    timeout=15,
                    check=False,
                )
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("Interoperability targets must run alone", result.stderr)
