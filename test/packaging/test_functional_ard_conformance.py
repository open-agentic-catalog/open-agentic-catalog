"""Conformance launcher failures use a disposable CLI and controlled prerequisite."""

from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import ard_conformance


class ConformanceLauncherTests(unittest.TestCase):
    def test_missing_dependency_fails_before_accessing_target(self):
        result = subprocess.run(
            [sys.executable, '-S', str(Path(ard_conformance.__file__)), 'manifest', '/not/read'],
            capture_output=True, text=True, timeout=10, check=False,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Install shared tools with make dev-tools-install', result.stderr)
        self.assertNotIn('Validating Manifest', result.stdout)

    def test_exit_status_and_schema_evidence_are_both_required(self):
        good = '  ✓ Manifest validates against ArdManifest.\n  ✓ All 1 entries validate against ArdEntry.\n'
        with tempfile.TemporaryDirectory() as directory:
            cli = Path(directory) / 'cli'
            for mode, output, status, expected in (
                ('manifest', good, 0, 0), ('publisher', good, 0, 0),
                ('manifest', 'CONFORMANCE STATUS: PASS\n', 0, 1),
                ('manifest', good, 7, 1), ('registry', 'protocol pass\n', 0, 0),
                ('registry', 'protocol failure\n', 1, 1),
            ):
                cli.write_text('import sys\nprint(' + repr(output) + ')\nsys.exit(' + str(status) + ')\n')
                with self.subTest(mode=mode, status=status, output=output), \
                     patch.object(ard_conformance, 'CLI', cli), \
                     patch.object(ard_conformance, 'prerequisite'), \
                     patch.object(sys, 'argv', ['ard_conformance.py', mode, 'owned-input']):
                    self.assertEqual(ard_conformance.main(), expected)


if __name__ == '__main__':
    unittest.main()
