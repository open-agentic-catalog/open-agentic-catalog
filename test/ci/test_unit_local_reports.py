"""Report admission and copies use owned files only; no processes or services."""

import hashlib
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from types import SimpleNamespace

import local_reports as reports
import local_workflows as local


RUN_ID = "123456789012345678"


class ReportTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory(prefix="catalog-report-unit-")
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.source = self.root / "source"
        self.source.mkdir()
        self.destination = reports.create_destination(self.root, RUN_ID)
        guard = patch.object(local.subprocess, "run", side_effect=AssertionError("unit started a process"))
        guard.start()
        self.addCleanup(guard.stop)

    def write(self, relative, content=b"safe report"):
        path = self.source / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
        return path

    def test_fixed_summary_and_coverage_copy_excludes_raw_state_and_credentials(self):
        summary = f"output/ci/{RUN_ID}-1/quality/isolated-unit/summary.md"
        self.write(summary)
        self.write("coverage/unit.out", b"mode: atomic\n")
        self.write(f"output/ci/{RUN_ID}-1/quality/isolated-unit/private.env", b"PRIVATE-SENTINEL")
        self.write(".tools/token", b"PRIVATE-SENTINEL")
        files, missing = reports.collect(self.source, self.destination, "unit", "backend", RUN_ID)
        self.assertEqual(missing, [])
        self.assertEqual({item["path"] for item in files}, {"reports/" + summary, "reports/coverage/unit.out"})
        for item in files:
            contents = (self.destination / item["path"]).read_bytes()
            self.assertEqual(item["sha256"], hashlib.sha256(contents).hexdigest())
            self.assertNotIn(b"PRIVATE-SENTINEL", contents)
            self.assertEqual((self.destination / item["path"]).stat().st_mode & 0o777, 0o600)

    def test_missing_expected_reports_are_explicit(self):
        files, missing = reports.collect(self.source, self.destination, "docs", "backend", RUN_ID)
        self.assertEqual(files, [])
        self.assertEqual(missing, [f"output/ci/{RUN_ID}-1/quality/documentation/summary.md"])

    def test_both_distinct_chart_packages_are_required(self):
        base = f"output/ci/{RUN_ID}-1/charts/"
        self.write(base + "reports/summary.txt")
        self.write(base + "open-agentic-catalog-0.1.0.tgz")
        _, missing = reports.collect(self.source, self.destination, "charts", "backend", RUN_ID)
        self.assertEqual(missing, [base + "<packages>"])
        second = reports.create_destination(self.root, "223456789012345678")
        self.write(base + "open-agentic-catalog-monitoring-0.1.0.tgz")
        files, missing = reports.collect(self.source, second, "charts", "backend", RUN_ID)
        self.assertEqual(missing, [])
        self.assertEqual(len(files), 3)

    def test_extra_matching_packages_fail_instead_of_copying_an_entire_directory(self):
        base = f"output/ci/{RUN_ID}-1/charts/"
        self.write(base + "open-agentic-catalog-0.1.0.tgz")
        self.write(base + "open-agentic-catalog-0.2.0.tgz")
        with self.assertRaisesRegex(ValueError, "number of workflow packages"):
            reports.collect(self.source, self.destination, "charts", "backend", RUN_ID)

    def test_symlinked_file_and_ancestor_are_refused_without_disclosing_the_target(self):
        summary = f"output/ci/{RUN_ID}-1/quality/documentation/summary.md"
        outside = self.root / "outside"
        outside.write_bytes(b"PRIVATE-SENTINEL")
        path = self.write(summary)
        path.unlink()
        path.symlink_to(outside)
        with self.assertRaisesRegex(ValueError, "unsafe workflow report"):
            reports.collect(self.source, self.destination, "docs", "backend", RUN_ID)
        path.unlink()
        path.parent.rmdir()
        path.parent.symlink_to(self.root, target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "unsafe workflow report"):
            reports.collect(self.source, self.destination, "docs", "backend", RUN_ID)
        self.assertFalse((self.destination / "reports").exists())
        self.assertEqual(outside.read_bytes(), b"PRIVATE-SENTINEL")

    def test_special_file_is_refused_without_opening_it(self):
        summary = f"output/ci/{RUN_ID}-1/quality/documentation/summary.md"
        path = self.write(summary)
        path.unlink()
        os.mkfifo(path)
        with self.assertRaisesRegex(ValueError, "unsafe workflow report"):
            reports.collect(self.source, self.destination, "docs", "backend", RUN_ID)

    def test_selected_file_and_total_byte_limits_are_enforced(self):
        self.write(f"output/ci/{RUN_ID}-1/quality/isolated-unit/summary.md", b"123456")
        self.write("coverage/unit.out", b"123456")
        with patch.object(reports, "FILE_LIMIT", 5), self.assertRaisesRegex(ValueError, "file limit"):
            reports.collect(self.source, self.destination, "unit", "backend", RUN_ID)
        with patch.object(reports, "FILE_LIMIT", 8), patch.object(reports, "TOTAL_LIMIT", 10):
            with self.assertRaisesRegex(ValueError, "byte budget"):
                reports.collect(self.source, self.destination, "unit", "backend", RUN_ID)

    def test_native_go_profiles_have_a_finite_larger_budget_without_widening_summaries(self):
        summary = f"output/ci/{RUN_ID}-1/quality/isolated-unit/summary.md"
        self.write(summary, b"ok")
        self.write("coverage/unit.out", b"12345678")
        with patch.object(reports, "FILE_LIMIT", 4), patch.object(reports, "COVERAGE_LIMIT", 8), patch.object(reports, "TOTAL_LIMIT", 10):
            files, missing = reports.collect(self.source, self.destination, "unit", "backend", RUN_ID)
        self.assertEqual(missing, [])
        self.assertEqual([item["bytes"] for item in files], [2, 8])
        second = reports.create_destination(self.root, "223456789012345678")
        self.write("coverage/unit.out", b"123456789")
        with patch.object(reports, "COVERAGE_LIMIT", 8), self.assertRaisesRegex(ValueError, "file limit"):
            reports.collect(self.source, second, "unit", "backend", RUN_ID)
        self.write(summary, b"large")
        with patch.object(reports, "FILE_LIMIT", 4), self.assertRaisesRegex(ValueError, "file limit"):
            reports.collect(self.source, self.destination, "unit", "backend", RUN_ID)

    def test_later_rejection_retains_the_complete_report_inventory(self):
        summary = f"output/ci/{RUN_ID}-1/quality/isolated-unit/summary.md"
        self.write(summary, b"ok")
        self.write("coverage/unit.out", b"oversized")
        with patch.object(reports, "COVERAGE_LIMIT", 4), self.assertRaisesRegex(ValueError, "file limit") as caught:
            reports.collect(self.source, self.destination, "unit", "backend", RUN_ID)
        expected = [{"path": "reports/" + summary, "bytes": 2, "sha256": hashlib.sha256(b"ok").hexdigest()}]
        self.assertEqual(getattr(caught.exception, "files", None), expected)
        self.assertEqual(getattr(caught.exception, "missing", None), [])
        self.assertEqual((self.destination / expected[0]["path"]).read_bytes(), b"ok")
        self.assertFalse((self.destination / "reports/coverage/unit.out").exists())
        (self.source / summary).unlink()
        second = reports.create_destination(self.root, "223456789012345678")
        with patch.object(reports, "COVERAGE_LIMIT", 4), self.assertRaisesRegex(ValueError, "file limit") as caught:
            reports.collect(self.source, second, "unit", "backend", RUN_ID)
        self.assertEqual(caught.exception.files, [])
        self.assertEqual(caught.exception.missing, [summary])

    def test_failed_copy_removes_the_incomplete_file_and_keeps_prior_metadata(self):
        summary = f"output/ci/{RUN_ID}-1/quality/isolated-unit/summary.md"
        self.write(summary, b"ok")
        self.write("coverage/unit.out", b"complete profile")
        original = Path.open
        target = self.destination / "reports/coverage/unit.out"

        class FailingWriter:
            def __enter__(self):
                self.stream = original(target, "xb")
                return self

            def write(self, content):
                self.stream.write(content[:3])
                raise OSError("controlled write failure")

            def __exit__(self, *_args):
                self.stream.close()

        def open_file(path, *args, **kwargs):
            return FailingWriter() if path == target and args == ("xb",) else original(path, *args, **kwargs)

        with patch.object(Path, "open", open_file), self.assertRaises((OSError, ValueError)) as caught:
            reports.collect(self.source, self.destination, "unit", "backend", RUN_ID)
        self.assertFalse(target.exists(), "failed copy left an unlisted partial file")
        self.assertEqual(getattr(caught.exception, "files", None), [{
            "path": "reports/" + summary, "bytes": 2, "sha256": hashlib.sha256(b"ok").hexdigest(),
        }])
        second = reports.create_destination(self.root, "223456789012345678")
        protected = second / "reports/coverage/unit.out"
        protected.parent.mkdir(parents=True)
        protected.write_bytes(b"pre-existing report")
        with self.assertRaises(ValueError) as caught:
            reports.collect(self.source, second, "unit", "backend", RUN_ID)
        self.assertEqual(protected.read_bytes(), b"pre-existing report")
        self.assertEqual(len(caught.exception.files), 1)

    def test_destination_never_reuses_previous_reports_or_traverses_redirected_parents(self):
        with self.assertRaises(FileExistsError):
            reports.create_destination(self.root, RUN_ID)
        redirected = self.root / "redirected"
        redirected.mkdir()
        (redirected / "output").symlink_to(self.root / "output", target_is_directory=True)
        with self.assertRaisesRegex(ValueError, "redirected"):
            reports.create_destination(redirected, "223456789012345678")

    def test_paths_run_ids_and_selections_have_closed_admission(self):
        for value in ("../private", "/private", "123", "1" * 19, "abc123456789012345"):
            with self.subTest(value=value), self.assertRaises(ValueError):
                reports.create_destination(self.root, value)
        for relative in ("../private", "/private", ""):
            with self.subTest(path=relative), self.assertRaises(ValueError):
                reports.checked_path(self.source, relative)
        with self.assertRaises(ValueError):
            reports.plan("all", "backend", RUN_ID)

    def test_final_metadata_is_atomic_and_separate_from_report_payloads(self):
        result = {"execution": {"status": "failed", "exitCode": 7}, "cleanup": "passed", "reportStatus": "partial"}
        reports.write_result(self.destination, result)
        self.assertEqual(json.loads((self.destination / "run.json").read_text()), result)
        self.assertFalse((self.destination / "run.json.tmp").exists())


class TypedInputTests(unittest.TestCase):
    def test_scan_and_release_inputs_have_explicit_true_and_false_strings(self):
        for value in (True, False):
            inputs = local.selection("image", scan=value, release_candidate=value)[2]
            self.assertEqual(inputs["scan"], str(value).lower())
            self.assertEqual(inputs["release_candidate"], str(value).lower())
            self.assertEqual(inputs["upload_artifacts"], "false")
            self.assertEqual(local.boolean(str(value).lower()), value)
        self.assertEqual(local.selection("charts", release_candidate=True)[2]["release_candidate"], "true")

    def test_untyped_values_and_inapplicable_options_are_rejected(self):
        for value in ("TRUE", "1", "yes", "false; upload", ""):
            with self.subTest(value=value), self.assertRaises(ValueError):
                local.boolean(value)
        for kwargs in ({"scan": "false"}, {"release_candidate": 1}):
            with self.assertRaises(ValueError):
                local.selection("image", **kwargs)
        with self.assertRaises(ValueError):
            local.selection("docs", release_candidate=True)


class SupervisorTests(unittest.TestCase):
    def test_late_zero_exit_is_not_accepted_as_success(self):
        process = SimpleNamespace(wait=lambda **_kwargs: 0, poll=lambda: 0)
        with (
            patch.object(local.subprocess, "Popen", return_value=process),
            patch.object(local.time, "monotonic", side_effect=[0, 0, 2]),
            patch.object(local.signal, "signal"),
        ):
            self.assertEqual(local.execute(["inert"], {}, Path("/owned"), 1), {"status": "timed_out", "exitCode": 0})

    def test_observation_failure_reports_failed_execution_instead_of_setup_failure(self):
        process = SimpleNamespace(wait=lambda **_kwargs: 7, poll=lambda: 7)
        resources = SimpleNamespace(deadline=None, observe=lambda: (_ for _ in ()).throw(ValueError("incomplete observation")))
        with patch.object(local.subprocess, "Popen", return_value=process), patch.object(local.signal, "signal"):
            result = local.execute(["inert"], {}, Path("/owned"), 10, resources)
        self.assertEqual(result, {"status": "failed", "exitCode": None, "failureStage": "supervision"})
