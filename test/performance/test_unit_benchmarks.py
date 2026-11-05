"""Comparison validity is checked without invoking Go or benchstat."""
import copy
import unittest
from unittest.mock import patch
from pathlib import Path
import benchmarks as bench


def report():
    return {"version": 2, "status": "passed-development-diagnostic", "source": {"commit": "a" * 40, "sourceDigest": "sha256:" + "b" * 64},
            "environment": {"go": dict(GOVERSION="go1.26.7", GOOS="linux", GOARCH="amd64", GOAMD64="v1", CGO_ENABLED="1", GOEXPERIMENT=""), "system": "Linux owned", "machine": "amd64", "cpu": "Owned CPU", "affinity": [0, 1], "cpuLimit": "max 100000", "memoryLimit": "max", "args": bench.ARGS},
            "output": "".join(("BenchmarkRuntime" + name + "-2 1000 100 ns/op 4 B/op 1 allocs/op\n") * 10 for name in ["Collection", "Exposition"]) + "PASS\n"}


class BenchmarkTests(unittest.TestCase):
    def test_matching_reports_allow_a_new_revision_but_not_different_environments(self):
        before, after = report(), report()
        after["source"]["commit"] = "c" * 40
        bench.compatible(before, after)
        for field in bench.FIELDS:
            changed = copy.deepcopy(after)
            changed["environment"][field] = "different"
            with self.subTest(field=field), self.assertRaises(ValueError):
                bench.compatible(before, changed)

    def test_different_gomaxprocs_are_not_a_matched_comparison(self):
        before, after = report(), report()
        after["output"] = after["output"].replace("-2 1000", "-4 1000")
        bench.validate(after)  # Each report is individually complete.
        with self.assertRaisesRegex(ValueError, "environments-differ"):
            bench.compatible(before, after)

    def test_single_cpu_output_has_no_suffix_and_compares_only_with_matching_gomaxprocs(self):
        single = report()
        single["output"] = single["output"].replace("-2 1000", " 1000")
        self.assertEqual(bench.samples(single["output"]), 1)
        bench.compatible(single, copy.deepcopy(single))
        with self.assertRaisesRegex(ValueError, "environments-differ"):
            bench.compatible(single, report())

    def test_incomplete_historical_nonfinite_and_insufficient_samples_fail(self):
        valid = report()
        for field, value in [("version", 1), ("status", "incomplete"), ("source", {}), ("environment", {}), ("output", ""),
                             ("output", valid["output"].replace("100 ns/op", "NaN ns/op")),
                             ("output", valid["output"].replace("100 ns/op", "-1 ns/op")),
                             ("output", valid["output"].replace("PASS", "FAIL")),
                             ("output", valid["output"].replace("-2 1000", "-0 1000")),
                             ("output", valid["output"].split("\n", 1)[1])]:
            with self.subTest(field=field, value=str(value)[:30]), self.assertRaises(ValueError):
                bench.validate({**valid, field: value})

    def test_installed_tool_requires_both_command_and_exact_module_identity(self):
        good = ("\tpath\t" + bench.MODULE + "/cmd/benchstat\n\tmod\t" + bench.MODULE + "\t" + bench.PIN + "\th1:owned\n").encode()
        with patch.object(Path, "is_file", return_value=True), patch.object(Path, "is_symlink", return_value=False):
            bench.installed(lambda *_args: good, {})
            for bad in (b"", good.replace(b"/cmd/benchstat", b"/cmd/benchsave"), good.replace(bench.PIN.encode(), b"other")):
                with self.assertRaisesRegex(ValueError, "version-mismatch"):
                    bench.installed(lambda *_args: bad, {})
