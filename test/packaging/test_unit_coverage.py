"""Unit profile accounting must retain zero hits and reject incomplete evidence."""

from contextlib import redirect_stderr, redirect_stdout
from io import StringIO
from pathlib import Path
import tempfile
import unittest

import unit_coverage


class UnitCoverageTests(unittest.TestCase):
    def test_thresholds_compare_exact_counts_not_rounded_percentages(self):
        check = unit_coverage.check_minimum
        self.assertIn("30350/32391", check(30350, 32391, "93.69", "FLOOR", "Coverage"))
        with self.assertRaisesRegex(ValueError, "below the 93.7%"):
            check(30350, 32391, "93.7", "FLOOR", "Coverage")
        for minimum in ("89.999", "90"):
            check(9, 10, minimum, "FLOOR", "Coverage")
        # More decimal places than Decimal's default precision must stay exact.
        with self.assertRaisesRegex(ValueError, "below"):
            check(9, 10, "90.000000000000000000000000000001", "FLOOR", "Coverage")
        for minimum in ("", "-1", "101", "NaN", "inf", "1e1", " 90", "90 ", "90.", ".9", "９０"):
            with self.subTest(minimum=minimum), self.assertRaisesRegex(ValueError, "FLOOR"):
                check(1, 1, minimum, "FLOOR", "Coverage")
        check(0, 1, "0", "FLOOR", "Coverage")
        check(1, 1, "100", "FLOOR", "Coverage")
        with self.assertRaisesRegex(ValueError, "no executable statements"):
            check(0, 0, "0", "FLOOR", "Coverage")

    def test_each_critical_package_must_pass_independently_of_high_aggregate(self):
        packages = {"app/good": [990, 1000], "app/critical": [8, 10], "app/empty": [0, 0]}
        for minimum in ("79.999", "80"):
            output = unit_coverage.report({}, packages, "98", ["app/critical"], minimum)
            self.assertIn("Critical unit package app/critical: 8/10", output)
        with self.assertRaisesRegex(ValueError, "app/critical.*below"):
            unit_coverage.report({}, packages, "98", ["app/good", "app/critical"], "80.001")
        for selected in ([], [""], ["app/missing"], ["app/empty"]):
            with self.subTest(selected=selected), self.assertRaises(ValueError):
                unit_coverage.report({}, packages, "98", selected, "0")
        with self.assertRaisesRegex(ValueError, "UNIT_CRITICAL_PACKAGE_COVERAGE_MIN"):
            unit_coverage.report({}, packages, "98", ["app/critical"], "nan")

    def test_weighted_unique_blocks_zero_hits_and_sorted_output(self):
        files, packages = unit_coverage.summarize(
            "mode: atomic\napp/z/main.go:1.1,2.1 9 0\napp/a/code.go:1.1,2.1 1 2\n"
            "app/a/code.go:1.1,2.1 1 0\napp/a/code.go:3.1,4.1 2 0\n"
            "app/a/code.go:3.1,4.1 2 1\n", "app/z\napp/a\napp/embed\n", ["app/embed"])
        self.assertEqual(files, {"app/z/main.go": [0, 9], "app/a/code.go": [3, 3]})
        self.assertEqual(packages["app/embed"], [0, 0])
        output = unit_coverage.report(files, packages, "25", ["app/a"], "90")
        self.assertIn("3/12 statements (25.00%)", output)
        self.assertIn("n/a (no executable statements)", output)
        self.assertLess(output.index("app/a\n"), output.index("app/z\n"))
        with self.assertRaisesRegex(ValueError, "below"):
            unit_coverage.report(files, packages, "25.001", ["app/a"], "90")
        for minimum in ("", "-1", "101", "nan", "inf", "1e1"):
            with self.subTest(minimum=minimum), self.assertRaisesRegex(ValueError, "UNIT_COVERAGE_MIN"):
                unit_coverage.report(files, packages, minimum, ["app/a"], "90")

    def test_incomplete_or_inconsistent_profiles_fail_closed(self):
        valid = "mode: atomic\napp/a/code.go:1.1,2.1 1 1\n"
        for profile, expected, zero in (
            (valid, "", []),
            (valid, "app/a\n\n", []),
            (valid.replace("atomic", "set"), "app/a", []),
            (valid + "bad row\n", "app/a", []),
            (valid + "app/a/code.go:1.1,2.1 2 0\n", "app/a", []),
            (valid, "app/b", []),
            (valid, "app/a\napp/missing", []),
            (valid, "app/a", ["app/a"]),
            ("mode: atomic\n", "app/embed", ["app/embed"]),
        ):
            with self.subTest(profile=profile, expected=expected), self.assertRaises(ValueError):
                unit_coverage.summarize(profile, expected, zero)

    def test_cli_reports_errors_without_publishing_or_mutating_inputs(self):
        with tempfile.TemporaryDirectory() as directory:
            profile, packages = Path(directory) / "profile", Path(directory) / "packages"
            original = "mode: atomic\napp/a/code.go:1.1,2.1 1 0\n"
            profile.write_text(original)
            packages.write_text("app/a\n")
            args = [str(profile), str(packages), "--critical-packages", "app/a", "--critical-minimum", "0", "--minimum", "0"]
            with redirect_stdout(StringIO()) as output:
                self.assertEqual(unit_coverage.main(args), 0)
            self.assertIn("0/1 statements", output.getvalue())
            with redirect_stderr(StringIO()) as error, redirect_stdout(StringIO()) as output:
                self.assertEqual(unit_coverage.main(args[:-1] + ["1"]), 1)
                self.assertEqual(unit_coverage.main([str(profile) + "-missing", *args[1:]]), 1)
            self.assertEqual(output.getvalue(), "")
            self.assertIn("Unit coverage failed:", error.getvalue())
            self.assertEqual(profile.read_text(), original)
