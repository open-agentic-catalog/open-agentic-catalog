"""Interpreter admission uses a real process, without running independent tools."""

import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


class ExplorerProcessTests(unittest.TestCase):
    def test_optimized_interpreter_refuses_before_tool_or_fixture_startup(self):
        with tempfile.TemporaryDirectory() as directory:
            result = subprocess.run([sys.executable, '-O', '-B', str(Path(__file__).with_name('ard_explorer.py')), 'check'],
                                    cwd=directory, env={'PATH': '', 'HOME': directory, 'LANG': 'C.UTF-8'},
                                    capture_output=True, text=True, timeout=5, check=False)
            self.assertEqual(result.returncode, 1)
            self.assertEqual(result.stdout, '')
            self.assertEqual(result.stderr.strip(), 'Explorer: Explorer qualification requires Python assertions')
            self.assertEqual(os.listdir(directory), [])


if __name__ == '__main__':
    unittest.main()
