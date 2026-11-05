"""Profile reconciliation never hides uncovered files or averages percentages."""

from contextlib import redirect_stderr
from io import StringIO
from pathlib import Path
import tempfile
import unittest

import application_coverage as coverage

from application_coverage import encode, merge, reconcile, totals
from unit_coverage import parse_profile


FILES = (
    "app/cmd/catalogctl/main.go", "app/cmd/catalogd/main.go",
    "app/internal/platform/runtime/runtime.go", "app/catalog-ui/cmd/catalog-ui/main.go",
    "app/internal/platform/database/store.go",
)


def profile(files=FILES, hit=False):
    return {key: value for file in files for key, value in (
        ((file, "1.1,2.1"), (8, hit)), ((file, "3.1,4.1"), (2, False)),
    )}


class ApplicationCoverageTests(unittest.TestCase):
    def test_scoped_checks_share_exact_arithmetic_and_require_critical_inventory(self):
        blocks = parse_profile("mode: atomic\napp/a/main.go:1.1,2.1 30350 1\napp/a/main.go:3.1,4.1 2041 0\n")
        for scope in ("local", "core", "postgres"):
            options = ("app/a", "93.69") if scope == "core" else ()
            with self.subTest(scope=scope):
                self.assertIn("30350/32391", coverage.check_profile(blocks, "93.69", scope, *options))
                with self.assertRaisesRegex(ValueError, "below the 93.7%"):
                    coverage.check_profile(blocks, "93.7", scope, *options)
        with self.assertRaisesRegex(ValueError, "Critical local package app/a.*below"):
            coverage.check_profile(blocks, "0", "core", "app/a", "93.7")
        for selected, minimum in ((None, None), ("", "0"), ("app/missing", "0"), ("app/a", "NaN")):
            with self.subTest(selected=selected), self.assertRaises(ValueError):
                coverage.check_profile(blocks, "0", "core", selected, minimum)
        with self.assertRaisesRegex(ValueError, "require the core profile"):
            coverage.check_profile(blocks, "0", "local", "app/a", "90")
        with self.assertRaisesRegex(ValueError, "no executable statements"):
            coverage.check_profile({}, "0", "postgres")

    def test_combined_total_floor_is_additional_to_the_per_file_floor(self):
        inputs = (profile(), profile(hit=True), profile(FILES[-1:], True), profile(FILES[:3], True))
        for minimum in ("79.999", "80"):
            _, report = reconcile(*inputs, minimum)
            self.assertIn("Combined application floor: 40/50", report)
        for minimum in ("80.001", "95"):
            with self.subTest(minimum=minimum), self.assertRaisesRegex(ValueError, "Combined application floor.*below"):
                reconcile(*inputs, minimum)
        for minimum in ("", "-1", "101", "nan", "inf", "1e1"):
            with self.subTest(minimum=minimum), self.assertRaisesRegex(ValueError, "FULL_COVERAGE_MIN"):
                reconcile(*inputs, minimum)

    def test_union_is_weighted_idempotent_and_keeps_zero_hit_source(self):
        base = profile()
        extra = profile(FILES[:1], True)
        result = merge(base, [extra, extra])
        self.assertEqual(totals(result)[1:], (8, 50))
        self.assertEqual(result.keys(), base.keys())
        self.assertEqual(parse_profile(encode(result)), result)
        self.assertFalse(any(hit for _, hit in base.values()))

    def test_changed_partial_unknown_and_empty_profiles_are_rejected(self):
        base = profile()
        extra = profile(FILES[:1], True)
        key = next(iter(extra))
        for contribution in ({}, {key: (8, True)}, {**extra, key: (9, True)},
                             {(key[0], "1.2,2.2"): (8, True)}, profile(["app/new.go"])):
            with self.subTest(contribution=contribution), self.assertRaises(ValueError):
                merge(base, [contribution])
        with self.assertRaises(ValueError):
            merge({}, [extra])

    def test_full_report_preserves_application_inventory_and_separate_totals(self):
        unit = profile()
        local = profile(hit=True) | profile(["app/test/fixture/main.go"], True)
        postgres = profile(FILES[-1:], True)
        processes = profile(FILES[:3], True)
        result, report = reconcile(unit, local, postgres, processes, "80")
        self.assertEqual(totals(result)[1:], (40, 50))
        self.assertEqual(result.keys(), unit.keys())
        self.assertIn("Unit application: 0/50", report)
        self.assertIn("Local application: 40/50", report)
        self.assertIn("PostgreSQL package: 8/10", report)
        self.assertIn("PostgreSQL processes: 24/30", report)
        self.assertIn("Combined application: 40/50", report)
        self.assertIn("Application files: 5", report)

    def test_full_report_rejects_incomplete_inputs_and_unrounded_failures(self):
        unit, local = profile(), profile(hit=True)
        postgres, processes = profile(FILES[-1:], True), profile(FILES[:3], True)
        for inputs in ((unit, profile(FILES[:3], True), postgres, processes),
                       (unit, local, {}, processes), (unit, local, postgres, profile(FILES[:1], True)),
                       (unit, profile(), postgres, processes),
                       (unit, local | profile(["app/unknown/code.go"]), postgres, processes)):
            with self.subTest(inputs=inputs), self.assertRaises(ValueError):
                reconcile(*inputs, "0")
        # 79.999% rounds to 80.00%, but still fails the exact threshold.
        for target in (unit, local):
            target[(FILES[3], "1.1,2.1")] = (79999, target is local)
            target[(FILES[3], "3.1,4.1")] = (20001, False)
        with self.assertRaisesRegex(ValueError, "below 80%"):
            reconcile(unit, local, postgres, processes, "80")

    def test_bad_merge_preserves_last_successful_output(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            base, extra, output = (root / name for name in ("base", "extra", "output"))
            base.write_text("mode: atomic\napp/main.go:1.1,2.1 1 0\n")
            extra.write_text("mode: atomic\napp/main.go:1.1,2.1 2 1\n")
            output.write_text("retained")
            with redirect_stderr(StringIO()):
                self.assertEqual(coverage.main(["merge", str(base), str(extra), str(output)]), 1)
            self.assertEqual(output.read_text(), "retained")
