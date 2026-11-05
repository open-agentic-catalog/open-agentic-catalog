import os
import unittest
from unittest.mock import patch
import runner
import recovery_test_helpers as helpers


class SupervisorRecovery(helpers.SupervisorRecoveryFixture, unittest.TestCase):
    def test_build_failure_preserves_report_even_when_cleanup_fails(self):
        for failed_cleanup in [False, True]:
            with self.subTest(failed_cleanup=failed_cleanup), patch.dict(os.environ, {}, clear=True), patch.object(runner, "source_identity", return_value={"commit": "a" * 40, "sourceDigest": "sha256:" + "b" * 64}), patch.object(runner, "engine_identity", return_value="engine"), patch.object(runner.local, "command", side_effect=runner.Failure("build-failed")), patch.object(runner, "cleanup", side_effect=runner.Failure("engine-unavailable") if failed_cleanup else None) as cleanup:
                before = set((self.root / "coverage/performance").glob("*/report.json"))
                with self.assertRaisesRegex(runner.Failure, "trial-incomplete"):
                    runner.run("smoke")
                cleanup.assert_called_once()
                after = set((self.root / "coverage/performance").glob("*/report.json"))
                self.assertEqual(1, len(after - before))
                report = runner.local.safety.read_document((after - before).pop())
                self.assertEqual("incomplete", report["status"])
                self.assertEqual("failed" if failed_cleanup else "passed", report["cleanup"])
                self.assertEqual("build-failed", report["failure"])
