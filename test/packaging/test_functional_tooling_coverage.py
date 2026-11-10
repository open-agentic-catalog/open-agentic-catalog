"""Native coverage collection across isolated interpreter processes, offline."""

import json
import os
from pathlib import Path
import shlex
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import xml.etree.ElementTree as ET

import tooling_coverage as coverage


class ToolingCoverageFunctionalTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.repository = coverage.ROOT
        self.paths = []
        for owner, value in (("alpha", 7), ("beta", 9)):
            self.write("test/" + owner + "/helper.py", "def answer(flag):\n    if flag:\n        return " + str(value) + "\n    return 0\n")
            self.write("test/" + owner + "/test_unit_helper.py", "import unittest\nimport helper\n\nclass Case(unittest.TestCase):\n    def test_answer(self):\n        self.assertEqual(helper.answer(True), " + str(value) + ")\n")
        self.write("test/namespace/unseen.py", "def untouched():\n    return 42\n")
        (self.root / "pyproject.toml").write_bytes((coverage.ROOT / "pyproject.toml").read_bytes())
        self.addCleanup(patch.stopall)
        patch.object(coverage, "ROOT", self.root).start()
        patch.object(coverage, "tracked_paths", side_effect=lambda: self.paths).start()
        self.directory = coverage.prepare("output/run", ["alpha", "beta"])

    def write(self, name, text):
        target = self.root / name
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_text(text)
        if name not in self.paths:
            self.paths.append(name)

    def owner(self, name, startup=True):
        environment = {key: value for key, value in os.environ.items()
                       if not key.startswith(("COVERAGE_", "COV_CORE_")) and key not in ("PYTHONPATH", "PYTHONHOME")}
        environment["COVERAGE_FILE"] = str(self.directory / "data" / (".coverage." + name))
        if startup:
            environment["COVERAGE_PROCESS_START"] = str(self.directory / "coverage.ini")
        return subprocess.run([sys.executable, "-B", Path(__file__).with_name("completion.py"), "python"], cwd=self.root / "test" / name,
                              env=environment, capture_output=True, text=True, timeout=30, check=False)

    def complete(self):
        for owner in ("alpha", "beta"):
            result = self.owner(owner)
            self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
            coverage.record_owner("output/run", owner)

    def make_fixture(self):
        for name in ("Makefile", "go.mod", "test/packaging/tooling_coverage.py", "test/packaging/completion.py"):
            self.write(name, (self.repository / name).read_text())
        (self.root / ".env").write_text("$(error hostile workflow file loaded)\n")
        result = subprocess.run(["git", "init", "--quiet"], cwd=self.root, env=self.make_environment(),
                                capture_output=True, text=True, timeout=10, check=False)
        self.assertEqual(result.returncode, 0, result.stderr)

    def make_environment(self):
        return {key: value for key, value in os.environ.items()
                if not key.startswith(("GIT_", "COVERAGE_", "COV_CORE_"))
                and key not in ("MAKEFLAGS", "MFLAGS", "MAKEOVERRIDES", "MAKELEVEL", "PYTHONPATH", "PYTHONHOME",
                                "TOOLING_PYTHON_COVERAGE_DIR", "CATALOG_ENV_FILE")}

    def make(self, target, environment=None):
        selected = self.make_environment()
        selected.update(environment or {})
        return subprocess.run(["make", "--no-print-directory", target,
                               "PYTHON=" + shlex.quote(sys.executable), "PYTHON_UNIT_DIRS=alpha beta"],
                              cwd=self.root, env=selected, capture_output=True, text=True, timeout=30, check=False)

    def test_native_report_binds_owners_and_tests_and_includes_zero_hit_namespace(self):
        self.complete()
        receipt = json.loads(coverage.finish("output/run").read_text())
        self.assertEqual(set(receipt["sourceFiles"]), set(self.paths))
        self.assertEqual(receipt["tool"], {"name": "coverage", "version": "7.17.0"})
        self.assertEqual(receipt["reports"][0]["sha256"], coverage.digest(self.directory / "coverage.xml"))
        document = ET.parse(self.directory / "coverage.xml")
        classes = {item.attrib["filename"]: item for item in document.findall(".//class")}
        self.assertEqual(set(classes), {"test/alpha/helper.py", "test/beta/helper.py", "test/namespace/unseen.py"})
        for name in ("test/alpha/helper.py", "test/beta/helper.py"):
            hits = [int(line.attrib["hits"]) for line in classes[name].findall("./lines/line")]
            self.assertTrue(any(hits), name)
            self.assertIn(0, hits, name)
        untouched = classes["test/namespace/unseen.py"].findall("./lines/line")
        self.assertTrue(untouched)
        self.assertTrue(all(line.attrib["hits"] == "0" for line in untouched))
        self.assertEqual({path.name for path in (self.directory / "owners").iterdir()}, {"alpha.json", "beta.json"})

    def test_failed_or_empty_owner_cannot_publish_a_receipt(self):
        for index, (body, expected) in enumerate((("        self.fail('synthetic failure')\n", "synthetic failure"),
                                                 ("", "No unit tests discovered"))):
            with self.subTest(expected=expected):
                test = self.root / "test/alpha/test_unit_helper.py"
                test.write_text("import unittest\n\nclass Case(unittest.TestCase):\n    def test_answer(self):\n" + body
                                if body else "import unittest\n")
                output = "output/failure" + str(index)
                self.directory = coverage.prepare(output, ["alpha", "beta"])
                beta = self.owner("beta")
                self.assertEqual(beta.returncode, 0, beta.stderr)
                coverage.record_owner(output, "beta")
                result = self.owner("alpha")
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(expected, result.stderr)
                # Match the Make contract: no record-owner after nonzero exit.
                with self.assertRaisesRegex(ValueError, "incomplete"):
                    coverage.finish(output)
                self.assertFalse((self.directory / "receipt.json").exists())

    def test_source_edit_after_successful_native_collection_refuses_publication(self):
        self.complete()
        (self.root / "test/namespace/unseen.py").write_text("def untouched():\n    return 43\n")
        with self.assertRaisesRegex(ValueError, "sources changed"):
            coverage.finish("output/run")
        self.assertFalse((self.directory / "receipt.json").exists())

    def test_missing_native_startup_cannot_record_successful_unit_process(self):
        result = self.owner("alpha", startup=False)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        with self.assertRaisesRegex(ValueError, "no native coverage data"):
            coverage.record_owner("output/run", "alpha")
        with self.assertRaisesRegex(ValueError, "incomplete"):
            coverage.finish("output/run")
        self.assertFalse((self.directory / "owners/alpha.json").exists())
        self.assertFalse((self.directory / "receipt.json").exists())

    def test_make_coverage_target_runs_recursive_owners_and_bypasses_hostile_env(self):
        self.make_fixture()
        result = self.make("coverage-tooling-python")
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertNotIn("hostile workflow file loaded", result.stderr)
        self.assertEqual(result.stderr.count("Ran 1 test"), 2, result.stderr)
        receipts = list((self.root / "output/coverage/tooling-python").glob("run-*/profile/receipt.json"))
        self.assertEqual(len(receipts), 1)
        receipt = json.loads(receipts[0].read_text())
        self.assertEqual(receipt["status"], "passed")
        self.assertEqual(receipt["profile"], "python-unit")
        self.assertEqual(receipt["tool"], {"name": "coverage", "version": "7.17.0"})
        self.assertEqual(set(receipt["sourceFiles"]), {name for name in self.paths if name.startswith("test/") and name.endswith(".py")})
        profile = receipts[0].parent
        self.assertEqual({path.name for path in (profile / "owners").iterdir()}, {"alpha.json", "beta.json"})
        data_paths = []
        for owner in ("alpha", "beta"):
            record = json.loads((profile / "owners" / (owner + ".json")).read_text())
            self.assertEqual(record["status"], "passed")
            self.assertEqual(len(record["data"]), 1)
            data_paths.append(record["data"][0]["path"])
            self.assertTrue(Path(data_paths[-1]).name.startswith(".coverage." + owner + "."))
        self.assertNotEqual(data_paths[0], data_paths[1])
        report = self.root / receipt["reports"][0]["path"]
        self.assertEqual(receipt["reports"][0]["sha256"], coverage.digest(report))
        filenames = {item.attrib["filename"] for item in ET.parse(report).findall(".//class")}
        self.assertEqual(filenames, {"test/alpha/helper.py", "test/beta/helper.py", "test/namespace/unseen.py",
                                     "test/packaging/tooling_coverage.py", "test/packaging/completion.py"})

    def test_make_failure_and_zero_tests_stop_owner_loop_and_do_not_finish(self):
        self.make_fixture()
        for body, expected in (("class Case(unittest.TestCase):\n    def test_failure(self):\n        self.fail('synthetic Make failure')\n", "synthetic Make failure"),
                               ("class Case(unittest.TestCase):\n    @unittest.skip('synthetic Make skip')\n    def test_required(self): pass\n", "failed, skipped or did not complete"),
                               ("", "No unit tests discovered")):
            with self.subTest(expected=expected):
                (self.root / "test/alpha/test_unit_helper.py").write_text("import unittest\n" + body)
                before = set((self.root / "output").glob("coverage/tooling-python/run-*/profile"))
                result = self.make("coverage-tooling-python")
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(expected, result.stderr)
                after = set((self.root / "output").glob("coverage/tooling-python/run-*/profile"))
                self.assertEqual(len(after - before), 1)
                profile = (after - before).pop()
                self.assertFalse(list((profile / "owners").iterdir()))
                self.assertFalse(list((profile / "data").glob(".coverage.beta.*")))
                self.assertFalse((profile / "coverage.xml").exists())
                self.assertFalse(list((self.root / "output").rglob("receipt.json")))

    def test_ordinary_make_units_ignore_ambient_coverage_without_opt_in(self):
        self.make_fixture()
        for owner, value in (("alpha", 7), ("beta", 9)):
            (self.root / "test" / owner / "test_unit_helper.py").write_text(
                "import os\nimport unittest\nimport helper\n\nclass Case(unittest.TestCase):\n"
                "    def test_uninstrumented(self):\n"
                "        self.assertNotIn('COVERAGE_PROCESS_START', os.environ)\n"
                "        self.assertNotIn('COVERAGE_PROCESS_CONFIG', os.environ)\n"
                "        self.assertNotIn('COVERAGE_FILE', os.environ)\n"
                "        self.assertEqual(helper.answer(True), " + str(value) + ")\n")
        result = self.make("test-tooling-unit", {"COVERAGE_PROCESS_START": str(self.root / "missing.ini"),
                                                "COVERAGE_PROCESS_CONFIG": "invalid synthetic native configuration",
                                                "COVERAGE_FILE": str(self.root / "ambient/.coverage")})
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(result.stderr.count("Ran 1 test"), 2, result.stderr)
        self.assertFalse((self.root / "ambient").exists())
        self.assertFalse(list(self.root.rglob(".coverage*")))
        self.assertFalse(list((self.root / "output").rglob("receipt.json")))


if __name__ == "__main__":
    unittest.main()
