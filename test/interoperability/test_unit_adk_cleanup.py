"""Isolated cleanup-controller decisions; no native SDK, Docker or subprocesses."""

import copy
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import Mock, patch

import adk_cleanup as cleanup
from adk_contract import CASES
from common import Result


def child_report(mode):
    """Construct expected public lifecycle evidence independently of the runner."""
    value = {"source": "source", "installation": {"image": "image"}, "control": "none" if mode == "valid" else mode,
             "containers": ["catalog-adk-0123456789ab-" + suffix for suffix in ("catalog", "tls", "client")],
             "cleanup": True, "success": mode == "valid", "phase": "complete" if mode == "valid" else "native-adapter",
             "caseOutcomes": dict.fromkeys(CASES, "passed")}
    if mode == "metadata-mismatch":
        value.update(error="AssertionError", nativePhase="known-search",
                     caseOutcomes={**dict.fromkeys(CASES, "unrun"), "inventory": "passed", "schemas": "passed", "capabilities-access": "passed", "known-search": "failed"},
                     traffic={"unexpected": 0, "tools": {"catalog_control_capabilities": 1, "catalog_control_access": 1, "catalog_control_search": 1}})
    if mode == "interrupt":
        value.update(error="KeyboardInterrupt", nativePhase="revoked-access")
        value["caseOutcomes"]["revoked-access"] = "incomplete"
    return value


class CleanupContractTests(unittest.TestCase):
    """Prevent unrelated errors or incomplete evidence from passing a calibration."""

    def test_exact_control_outcomes_and_identity_are_required(self):
        """Each valid control must succeed before isolating its rejected changes."""
        for mode in cleanup.MODES:
            value = child_report(mode)
            code, sent = (0 if mode == "valid" else 2), mode == "interrupt"
            cleanup.validate(mode, code, value, "source", "image", sent)
            mutations = [lambda v: v.update(source="different"), lambda v: v.update(installation={"image": "different"}),
                         lambda v: v.update(cleanup=False), lambda v: v.update(control="other"),
                         lambda v: v.update(success=not v["success"]), lambda v: v.update(containers=[]),
                         lambda v: v["caseOutcomes"].pop("schemas")]
            if mode != "valid":
                mutations += [lambda v: v.update(error="RuntimeError"), lambda v: v.update(nativePhase="setup")]
            for mutate in mutations:
                invalid = copy.deepcopy(value)
                mutate(invalid)
                with self.subTest(mode=mode, invalid=invalid), self.assertRaises(AssertionError):
                    cleanup.validate(mode, code, invalid, "source", "image", sent)
            with self.assertRaises(AssertionError):
                cleanup.validate(mode, 2 if code == 0 else 0, value, "source", "image", sent)
            with self.assertRaises(AssertionError):
                cleanup.validate(mode, code, value, "source", "image", not sent)

    def test_fault_and_interruption_cannot_discard_native_progress(self):
        """A nonzero exit alone is insufficient evidence for the selected failure."""
        mismatch = child_report("metadata-mismatch")
        for update in ({"unexpected": 1}, {"tools": {}}):
            changed = copy.deepcopy(mismatch)
            changed["traffic"].update(update)
            with self.assertRaises(AssertionError):
                cleanup.validate("metadata-mismatch", 2, changed, "source", "image", False)
        for state in ("passed", "failed", "unrun"):
            interrupted = child_report("interrupt")
            interrupted["caseOutcomes"]["revoked-access"] = state
            with self.assertRaises(AssertionError):
                cleanup.validate("interrupt", 2, interrupted, "source", "image", True)

    def test_native_failure_requires_the_intended_metadata_assertion(self):
        """Authentication or another assertion at Search must fail the calibration."""
        value = {"failure": "AssertionError", "failureCheck": "search-display", "phase": "known-search", "closed": True}
        cleanup.validate_native_failure({"exit": 1, "stdout": json.dumps(value)})
        for update in ({"failureCheck": "unrelated"}, {"failureCheck": None}, {"failure": "RuntimeError"}, {"phase": "schemas"}, {"closed": False}):
            with self.subTest(update=update), self.assertRaises(AssertionError):
                cleanup.validate_native_failure({"exit": 1, "stdout": json.dumps({**value, **update})})
        with self.assertRaises(AssertionError):
            cleanup.validate_native_failure({"exit": 0, "stdout": json.dumps(value)})

    def test_child_report_location_is_bounded_and_unambiguous(self):
        """Never discover or act on a different concurrent invocation's report."""
        with tempfile.TemporaryDirectory(dir=cleanup.native.ROOT / "output") as temporary:
            root = Path(temporary)
            owned = root / "children"
            advertised = (owned / "run-abc_123").relative_to(cleanup.native.ROOT)
            log = root / "child.log"
            with patch.object(cleanup.native.tools, "OUTPUT", owned):
                log.write_text("startup\n")
                self.assertIsNone(cleanup.report_from_log(log))
                log.write_text("ADK report: " + str(advertised) + "\n")
                self.assertEqual(cleanup.report_from_log(log), owned / "run-abc_123/report.json")
                for text in ("ADK report: ../unrelated\n", log.read_text()*2, "x"*16385):
                    log.write_text(text)
                    with self.assertRaises(AssertionError):
                        cleanup.report_from_log(log)

    def test_cleanup_requires_exact_absence_and_preserves_unrelated_names(self):
        """Only the registered three-name family is inspected; residue is a failure."""
        with tempfile.TemporaryDirectory() as temporary:
            report = Path(temporary) / "report.json"
            value = child_report("valid")
            with patch.object(cleanup.native, "docker", return_value=Result(0, "", "")) as docker:
                cleanup.confirm_cleanup(value, report)
                self.assertEqual(docker.call_count, 3)
                for call, name in zip(docker.call_args_list, value["containers"], strict=True):
                    self.assertIn("name=^/" + name + "$", call.args)
            with patch.object(cleanup.native, "docker", return_value=Result(0, "remaining", "")), self.assertRaises(AssertionError):
                cleanup.confirm_cleanup(value, report)
            value["containers"] = ["unrelated"]
            with patch.object(cleanup.native, "docker") as docker, self.assertRaises(AssertionError):
                cleanup.confirm_cleanup(value, report)
            docker.assert_not_called()

    def test_forced_stop_cannot_claim_confirmed_cleanup(self):
        """A stuck owned child is bounded and produces an explicit failure."""
        child = Mock()
        child.poll.return_value = None
        child.wait.side_effect = [subprocess.TimeoutExpired("owned", 90), 2]
        with self.assertRaisesRegex(RuntimeError, "cleanup unconfirmed"):
            cleanup.stop(child)
        child.terminate.assert_called_once_with()
        child.kill.assert_called_once_with()

    def test_interruption_holds_revocation_until_the_supervisor_is_signalled(self):
        """The readiness checkpoint cannot accidentally revoke or resume the client."""
        with tempfile.TemporaryDirectory() as temporary:
            progress = Path(temporary)
            (progress / "revoke").write_text("revoke-reader\n")
            responses = [Result(0, "", ""), Result(0, json.dumps([{"State": {"Running": True}}]), ""),
                         Result(0, json.dumps([{"State": {"Running": False, "ExitCode": 2}}]), ""), Result(0, "", "")]
            with patch.object(cleanup.native, "docker", side_effect=responses), patch.object(cleanup.native, "revoke_reader") as revoke, patch.object(cleanup.native.time, "sleep"):
                result, revoked = cleanup.native.run_client("owned", progress, Mock(), interrupt=True)
            self.assertEqual(result.code, 2)
            self.assertFalse(revoked)
            revoke.assert_not_called()
            self.assertEqual((progress / "interrupt-ready").read_bytes(), b"interrupt-ready\n")
            self.assertFalse((progress / "continue").exists())


if __name__ == "__main__":
    unittest.main()
