"""Receipt and inventory contracts without running tests or a coverage tool."""

import importlib.metadata
import json
from pathlib import Path
from types import SimpleNamespace
import tempfile
import unittest
from unittest.mock import Mock, patch

import tooling_coverage as coverage
from sonarqube import selected


class ToolingCoverageTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.paths = ["test/alpha/helper.py", "test/alpha/test_unit_helper.py", "test/beta/unseen.py"]
        for name in self.paths:
            path = self.root / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("value = 1\n")
        (self.root / "pyproject.toml").write_text('[dependency-groups]\ndev = ["coverage==7.16.2"]\n')
        self.addCleanup(patch.stopall)
        patch.object(coverage, "ROOT", self.root).start()
        patch.object(coverage, "tracked_paths", side_effect=lambda: self.paths).start()
        self.native = Mock()
        self.native.measured_files.return_value = [str(self.root / self.paths[0])]
        self.native.has_arcs.return_value = True
        self.module = SimpleNamespace(__version__="7.16.2", CoverageData=Mock(return_value=self.native), Coverage=Mock())

    def tool(self):
        return patch.object(coverage, "coverage_tool", return_value=(self.module, "7.16.2"))

    def prepared(self):
        with self.tool():
            return coverage.prepare("output/run", ["alpha", "beta"])

    def completed(self):
        directory = self.prepared()
        for owner in ("alpha", "beta"):
            (directory / "data" / (".coverage." + owner + ".native")).write_bytes(b"native-data")
            with self.tool():
                coverage.record_owner("output/run", owner)
        return directory

    def test_inventory_binds_tests_and_unexecuted_modules_but_not_other_roots(self):
        self.paths += ["outside.py", "test/alpha/data.json", "test/deleted.py", self.paths[0]]
        sources = coverage.source_files()
        self.assertEqual(set(sources), {"test/alpha/helper.py", "test/alpha/test_unit_helper.py", "test/beta/unseen.py"})
        before = sources[self.paths[0]]
        (self.root / self.paths[0]).write_text("value = 2\n")
        self.assertNotEqual(coverage.source_files()[self.paths[0]], before)

    def test_inventory_rejects_escaping_paths_and_source_symlinks(self):
        for name in ("test/../outside.py", "test/back\\slash.py", "test/new\nline.py", "test/new\rline.py"):
            with self.subTest(name=name), patch.object(coverage, "tracked_paths", return_value=[name]), self.assertRaises(ValueError):
                coverage.source_files()
        target = self.root / self.paths[0]
        target.unlink()
        target.symlink_to(self.root / self.paths[1])
        with self.assertRaisesRegex(ValueError, "regular repository"):
            coverage.source_files()

    def test_inventory_selection_matches_sonar_test_python_snapshot(self):
        self.paths += ["test/.hidden.py", "test/alpha/.private/helper.py", "test/node_modules/dependency.py",
                       "test/nested/dist/generated.py", "test/coverage/copied.py", "test/output/generated.py",
                       "test/clientcompat/go/generated/client.py", "sdk/python/client.py"]
        self.assertEqual(set(coverage.source_files()), {name for name in self.paths if selected(name)})

    def test_exact_installed_tool_pin_and_distribution_must_agree(self):
        with patch.object(coverage.importlib.metadata, "version", return_value="7.16.2"), \
                patch.object(coverage.importlib, "import_module", return_value=self.module):
            self.assertEqual(coverage.coverage_tool(), (self.module, "7.16.2"))
        with patch.object(coverage.importlib.metadata, "version", return_value="7.10.6"), self.assertRaisesRegex(ValueError, "differs"):
            coverage.coverage_tool()
        with patch.object(coverage.importlib.metadata, "version", side_effect=importlib.metadata.PackageNotFoundError), self.assertRaisesRegex(ValueError, "install"):
            coverage.coverage_tool()
        with patch.object(coverage.importlib.metadata, "version", return_value="7.16.2"), \
                patch.object(coverage.importlib, "import_module", return_value=SimpleNamespace(__version__="wrong")), \
                self.assertRaisesRegex(ValueError, "disagree"):
            coverage.coverage_tool()
        (self.root / "pyproject.toml").write_text('[dependency-groups]\ndev = []\n')
        with self.assertRaisesRegex(ValueError, "exact coverage pin"):
            coverage.coverage_tool()

    def test_prepare_requires_fresh_run_unique_existing_owners_and_output_scope(self):
        for owners in ([], ["alpha", "alpha"], ["missing"], ["../alpha"]):
            with self.subTest(owners=owners), self.tool(), self.assertRaisesRegex(ValueError, "owners"):
                coverage.prepare("output/run", owners)
        for output in ("elsewhere/run", "output/../../elsewhere"):
            with self.subTest(output=output), self.tool(), self.assertRaisesRegex(ValueError, "repository output"):
                coverage.prepare(output, ["alpha"])
        directory = self.prepared()
        self.assertEqual(directory.stat().st_mode & 0o777, 0o700)
        config = (directory / "coverage.ini").read_text()
        self.assertIn("parallel = True", config)
        self.assertIn("data_file = ${COVERAGE_FILE?}", config)
        self.assertIn("*/test_*.py", config)
        self.assertIn(str(self.root / self.paths[2]), config)
        with self.tool(), self.assertRaises(FileExistsError):
            coverage.prepare("output/run", ["alpha"])

    def test_output_symlink_cannot_redirect_run(self):
        (self.root / "output").symlink_to(self.root / "test", target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "repository output"):
            coverage.output_path("output/run")

    def test_changed_config_and_published_run_cannot_be_reused(self):
        directory = self.prepared()
        original = (directory / "coverage.ini").read_bytes()
        (directory / "coverage.ini").write_text("changed")
        with self.tool(), self.assertRaisesRegex(ValueError, "configuration changed"):
            coverage.record_owner("output/run", "alpha")
        (directory / "coverage.ini").write_bytes(original)
        (directory / "receipt.json").write_text("{}")
        with self.tool(), self.assertRaisesRegex(ValueError, "already published"):
            coverage.record_owner("output/run", "alpha")

    def test_owner_completion_requires_native_data_and_cannot_be_duplicated(self):
        directory = self.prepared()
        with self.tool(), self.assertRaisesRegex(ValueError, "unexpected Python unit owner"):
            coverage.record_owner("output/run", "other")
        with self.tool(), self.assertRaisesRegex(ValueError, "no native coverage"):
            coverage.record_owner("output/run", "alpha")
        path = directory / "data/.coverage.alpha.native"
        path.write_bytes(b"native-data")
        with self.tool():
            coverage.record_owner("output/run", "alpha")
        marker = json.loads((directory / "owners/alpha.json").read_text())
        self.assertEqual(marker["data"], [{"path": str(path.relative_to(self.root)), "sha256": coverage.digest(path)}])
        with self.tool(), self.assertRaisesRegex(ValueError, "already recorded"):
            coverage.record_owner("output/run", "alpha")

    def test_native_data_rejects_wrong_sources_line_only_empty_and_symlink_files(self):
        directory = self.prepared()
        path = directory / "data/.coverage.alpha.native"
        path.write_bytes(b"native-data")
        sources = coverage.source_files()
        self.native.measured_files.return_value = ["/outside/source.py"]
        with self.tool(), self.assertRaisesRegex(ValueError, "unexpected source or mode"):
            coverage.read_native(path, sources)
        self.native.measured_files.return_value = [str(self.root / self.paths[0])]
        self.native.has_arcs.return_value = False
        with self.tool(), self.assertRaisesRegex(ValueError, "unexpected source or mode"):
            coverage.read_native(path, sources)
        path.write_bytes(b"")
        with self.assertRaisesRegex(ValueError, "missing or exceeds"):
            coverage.read_native(path, sources)
        path.unlink()
        path.symlink_to(self.root / self.paths[0])
        with self.assertRaisesRegex(ValueError, "missing or exceeds"):
            coverage.read_native(path, sources)

    def test_finish_refuses_missing_owner_and_changed_sources_before_reporting(self):
        directory = self.prepared()
        with self.tool(), self.assertRaisesRegex(ValueError, "incomplete"):
            coverage.finish("output/run")
        (self.root / self.paths[0]).write_text("changed = True\n")
        with self.tool(), self.assertRaisesRegex(ValueError, "sources changed"):
            coverage.finish("output/run")
        self.module.Coverage.assert_not_called()
        self.assertFalse((directory / "receipt.json").exists())

    def test_finish_refuses_changed_or_unrecorded_data(self):
        directory = self.completed()
        path = directory / "data/.coverage.alpha.native"
        path.write_bytes(b"changed-data")
        with self.tool(), self.assertRaisesRegex(ValueError, "changed after owner completion"):
            coverage.finish("output/run")
        path.write_bytes(b"native-data")
        (directory / "data/.coverage.unknown.native").write_bytes(b"other-data")
        with self.tool(), self.assertRaisesRegex(ValueError, "unrecorded"):
            coverage.finish("output/run")
        self.assertFalse((directory / "receipt.json").exists())

    def test_finish_passes_zero_hit_modules_to_native_reporter_and_binds_receipt(self):
        directory = self.completed()
        reporter = self.module.Coverage.return_value

        def report(**arguments):
            self.assertEqual(Path.cwd(), self.root)
            self.assertEqual(set(arguments["morfs"]), {str(self.root / self.paths[0]), str(self.root / self.paths[2])})
            Path(arguments["outfile"]).write_text("native XML fixture\n")

        reporter.xml_report.side_effect = report
        previous = Path.cwd()
        with self.tool():
            receipt_path = coverage.finish("output/run")
        self.assertEqual(Path.cwd(), previous)
        reporter.combine.assert_called_once_with(data_paths=[str(directory / "data/.coverage.alpha.native"), str(directory / "data/.coverage.beta.native")], strict=True, keep=True)
        receipt = json.loads(receipt_path.read_text())
        self.assertEqual(receipt["sourceRoots"], ["test"])
        self.assertEqual(receipt["sourceFiles"], coverage.source_files())
        self.assertEqual(receipt["tool"], {"name": "coverage", "version": "7.16.2"})
        self.assertEqual(receipt["reports"], [{"format": "cobertura", "path": "output/run/coverage.xml", "sha256": coverage.digest(directory / "coverage.xml")}])

    def test_report_failure_preserves_cwd_and_does_not_publish_receipt(self):
        directory = self.completed()
        self.module.Coverage.return_value.xml_report.side_effect = ValueError("native reporting failed")
        previous = Path.cwd()
        with self.tool(), self.assertRaisesRegex(ValueError, "native reporting failed"):
            coverage.finish("output/run")
        self.assertEqual(Path.cwd(), previous)
        self.assertFalse((directory / "receipt.json").exists())
        self.assertFalse((directory / "coverage.xml").exists())


if __name__ == "__main__":
    unittest.main()
