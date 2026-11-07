"""Execute the workflow's collector with real Git selection in owned fixtures."""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import textwrap
import unittest

import fuzz_corpus as f

CORPUS = b'go test fuzz v1\nstring("synthetic counterexample")\n'


class FuzzCorpusFunctionalTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="catalog fuzz corpus ")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.env = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
        self.env["REPORT_DIRECTORY"] = "output/ci/fixture/assurance/go-fuzz"
        self.output = self.root / self.env["REPORT_DIRECTORY"] / "fuzz-corpus"
        subprocess.run(["git", "init", "-q", str(self.root)], env=self.env, check=True, capture_output=True, timeout=20)
        (self.root / "test/ci").mkdir(parents=True)
        shutil.copyfile(f.ROOT / "test/ci/fuzz_corpus.py", self.root / "test/ci/fuzz_corpus.py")
        (self.root / ".gitignore").write_text("/output/\n**/testdata/fuzz/FuzzIgnored/\n")
        self.tracked = self.fixture("internal/artwork/testdata/fuzz/FuzzTracked/0123456789abcdef")
        subprocess.run(["git", "add", "."], cwd=self.root, env=self.env, check=True, capture_output=True, timeout=20)
        workflow = (f.ROOT / ".github/workflows/assurance.yml").read_text()
        step = workflow.split("      - name: Preserve new synthetic fuzz counterexamples after a failure\n", 1)[1].split("      - name:", 1)[0]
        self.assertIn("!cancelled() && inputs.suite == 'go-fuzz'", step)
        self.assertIn("steps.measurement.outcome == 'failure'", step)
        measurement = workflow.split("      - name: Run the selected assertion boundary\n", 1)[1].split("      - name:", 1)[0]
        self.assertIn("id: measurement", measurement)
        self.assertNotIn("continue-on-error", measurement + step)
        self.command = textwrap.dedent(step.split("        run: |\n", 1)[1])

    def fixture(self, name, data=CORPUS):
        path = self.root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(data)
        return name

    def run_collector(self):
        """Run the exact maintained shell step without uploads or a hosted runner."""
        return subprocess.run(["bash", "-e", "-o", "pipefail", "-c", self.command],
                              cwd=self.root, env=self.env, capture_output=True, text=True, timeout=20, check=False)

    def test_workflow_collects_missing_packages_and_keeps_git_selection(self):
        names = [self.fixture(package + "/testdata/fuzz/FuzzSynthetic/0123456789abcdef")
                 for package in ("internal/draftfile", "internal/platform/authn", "internal/artwork")]
        ignored = self.fixture("internal/artwork/testdata/fuzz/FuzzIgnored/0123456789abcdef")
        unrelated = self.fixture("private/operator-input", b"private fixture\n")
        result = self.run_collector()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Retained 3 synthetic fuzz counterexamples", result.stdout)
        self.assertEqual({path.relative_to(self.output).as_posix() for path in self.output.rglob("*") if path.is_file()}, set(names))
        for name in names:
            self.assertEqual((self.output / name).read_bytes(), CORPUS)
            self.assertEqual((self.root / name).read_bytes(), CORPUS)
        for name in (self.tracked, ignored, unrelated):
            self.assertFalse((self.output / name).exists())

    def test_malformed_selected_corpus_fails_without_copying_private_data(self):
        name = self.fixture("internal/platform/authn/testdata/fuzz/FuzzSynthetic/0123456789abcdef", b"private fixture\n")
        result = self.run_collector()
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.output.exists())
        self.assertEqual((self.root / name).read_bytes(), b"private fixture\n")
        self.assertNotIn("private fixture", result.stdout + result.stderr)

    def test_source_symlink_refuses_copy_and_existing_report_is_preserved(self):
        name = self.fixture("internal/draftfile/testdata/fuzz/FuzzSynthetic/0123456789abcdef")
        self.assertEqual(self.run_collector().returncode, 0)
        retained = self.output / name
        retained.write_bytes(b"existing report")
        repeat = self.run_collector()
        self.assertNotEqual(repeat.returncode, 0)
        self.assertEqual(retained.read_bytes(), b"existing report")
        source = self.root / name
        source.unlink()
        source.symlink_to(self.root / self.fixture("private/operator-input"))
        redirected = self.run_collector()
        self.assertNotEqual(redirected.returncode, 0)
        self.assertEqual(retained.read_bytes(), b"existing report")

    def test_no_new_corpus_is_a_successful_noop(self):
        result = self.run_collector()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Retained 0 synthetic fuzz counterexamples", result.stdout)
        self.assertFalse(self.output.exists())
