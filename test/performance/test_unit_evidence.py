import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import runner
from evidence_test_helpers import profile, report


class WorkloadEvidenceTests(unittest.TestCase):
    def test_cursor_failure_classes_remain_failed_and_allowlisted(self):
        for classification in ("400", "400-stale-generation", "400-invalid-live-cursor"):
            with self.subTest(classification=classification):
                data = report()
                data["verdict"] = "failed"
                data["measured"].update(succeeded=2, failed=1)
                data["measured"]["outcomes"]["status"] = 1
                data["measured"]["httpFailureStatuses"] = {classification: 1}
                runner.validate_workload(data, profile())
                with self.assertRaises(runner.Failure):
                    runner.validate_workload(data | {"verdict": "passed"}, profile())
                data["measured"]["httpFailureStatuses"] = {classification + "-private": 1}
                with self.assertRaises(runner.Failure):
                    runner.validate_workload(data, profile())

    def test_report_reconciles_exact_schema_counters_windows_and_profile(self):
        runner.validate_workload(report(), profile())
        for key, value in [("schemaVersion", True), ("verdict", "anything"), ("profile", {}),
                           ("baselineResults", True), ("responseDigest", "private-text"), ("bucketUpperSeconds", [1])]:
            with self.subTest(key=key):
                data = report()
                data[key] = value
                with self.assertRaises(runner.Failure):
                    runner.validate_workload(data, profile())
        for key, value in [("successfulResponseBytes", 0), ("successfulResponseBytes", -1), ("successfulResponseBytes", True), ("successfulResponseBytes", 4 << 20), ("count", True), ("succeeded", 2), ("failed", -1), ("latencySecondsTotal", float("nan")),
                           ("complete", 1), ("latencyBuckets", [0] * 12), ("latencyOverflow", 1),
                           ("outcomes", {}), ("httpFailureStatuses", {"private": 1}), ("httpFailureStatuses", {"503": 1}), ("httpFailureStatuses", None), ("finishedAtUnixMillis", 1001), ("issuanceEndedAtUnixMillis", 5000)]:
            with self.subTest(key=key):
                data = report()
                data["measured"][key] = value
                with self.assertRaises(runner.Failure):
                    runner.validate_workload(data, profile())
        data = report()
        data["private"] = "secret"
        with self.assertRaises(runner.Failure):
            runner.validate_workload(data, profile())
    def test_wall_clock_step_is_explicit_without_corrupting_monotonic_duration(self):
        data = report()
        data["measured"]["clockAdjustmentMillis"] = -3000
        self.assertEqual(-3000, runner.validate_workload(data, profile())["measured"]["clockAdjustmentMillis"])
    def test_final_verdict_requires_complete_and_consistent_work(self):
        incomplete = report()
        incomplete["verdict"] = "incomplete"
        incomplete["measured"]["complete"] = False
        runner.validate_workload(incomplete, profile())
        for verdict in ("passed", "failed"):
            with self.subTest(verdict=verdict), self.assertRaises(runner.Failure):
                runner.validate_workload(incomplete | {"verdict": verdict}, profile())
        failed = report()
        failed["verdict"] = "failed"
        with self.assertRaises(runner.Failure):
            runner.validate_workload(failed, profile())
        failed["measured"].update(succeeded=2, failed=1)
        failed["measured"]["outcomes"]["status"] = 1
        failed["measured"]["httpFailureStatuses"] = {"503": 1}
        runner.validate_workload(failed, profile())
        with self.assertRaises(runner.Failure):
            runner.validate_workload(failed | {"verdict": "passed"}, profile())
    def test_warmup_and_measurement_must_not_overlap_after_clock_adjustment(self):
        for adjustment in (-500, 0, 500):
            with self.subTest(adjustment=adjustment):
                selected = profile() | {"warmupSeconds": 1}
                data = report()
                data["profile"] = selected
                data["warmup"] = copy.deepcopy(data["measured"])
                data["warmup"]["clockAdjustmentMillis"] = adjustment
                start = data["warmup"]["finishedAtUnixMillis"] + adjustment
                data["measured"].update(startedAtUnixMillis=start,
                                        issuanceEndedAtUnixMillis=start + 1000,
                                        finishedAtUnixMillis=start + 1000)
                runner.validate_workload(data, selected)
                for key in ("startedAtUnixMillis", "issuanceEndedAtUnixMillis", "finishedAtUnixMillis"):
                    data["measured"][key] -= 2
                with self.assertRaises(runner.Failure):
                    runner.validate_workload(data, selected)
    def test_missing_unsafe_or_corrupt_report_never_passes(self):
        for mode in ["missing", "invalid", "public", "symlink", "oversized"]:
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                path = root / "workload.json"
                def execute(*_args):
                    if mode == "missing":
                        raise runner.Failure("deadline")
                    if mode == "symlink":
                        path.symlink_to(root / "foreign")
                    else:
                        path.write_text("{}" if mode == "invalid" else " " * 16385 if mode == "oversized" else json.dumps(report()))
                        path.chmod(0o644 if mode == "public" else 0o600)
                with patch.object(runner.local, "command", side_effect=execute), self.assertRaises(runner.Failure):
                    runner.workload_result(root / "child", root, profile(), {})
