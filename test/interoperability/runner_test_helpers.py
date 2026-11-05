import contextlib
import io
import json
from pathlib import Path
import tempfile
from unittest.mock import patch
from common import Result
from client_contract import NATIVE_CASES, NATIVE_FILTERS, NAVIGATION_CASES, navigation_evidence, outcome
import runner as r

def passing_case(name):
    """Minimal complete report fixtures for supervisor fault injection."""
    value = outcome(name)
    if name in NAVIGATION_CASES:
        value.update(navigation_evidence(name))
    if name.startswith("decode-"):
        full = name.endswith("-full")
        value.update(accepted=full, decoder_error=None if full else "BadParameter" if name == "decode-search-sparse" else "ValidationError",
                     http_status=200, http_requests=1)
    if name == "navigate-credential-source":
        value.update(manifest_authorized=False, other_owned_origin_registry_authorized=True,
                     source_replaced_with_queried_endpoint=True, http_requests=2)
    if name in NATIVE_CASES:
        if name == "native-pagination":
            value.update(native_calls=3, result_counts=[1, 1, 1], unique_results=3, continuation_tokens=2, terminal=True)
        elif name == "native-changed-filter-token":
            value.update(native_calls=2, result_counts=[1], http_status=400, error_code="INVALID_ARGUMENT",
                         message="page token is invalid for this request")
        else:
            _, family, kind = name.split("-")
            count = len(NATIVE_FILTERS[family]) if kind == "contradictions" else 1
            value.update(native_calls=count, result_counts=[0 if kind == "contradictions" else 3 if family == "listing" else 1] * count)
    return value
class SupervisorFixture:
    def exercise(self, failure=None):
        """Run the actual supervisor with a fake engine and return its retained report."""
        calls = []

        def docker(*args, **kwargs):
            calls.append(args)
            if failure == "build" and args[0] == "build":
                raise RuntimeError("build failed")
            if failure == "interrupt" and args[:2] == ("run", "-d"):
                raise KeyboardInterrupt()
            if failure == "cleanup" and args[0] == "rm":
                return Result(1, "", "permission denied")
            if args[0] == "run" and "--entrypoint=python" in args:
                if failure == "traversal-interrupt":
                    phase = next(r.OUTPUT.glob("run-*/progress/phase.json"))
                    phase.write_text(json.dumps({"phase": "native-pagination-page-2"}))
                    raise KeyboardInterrupt()
                report = {"cases": [outcome(name, status="fail") if failure == "case" else passing_case(name) for name in sorted(r.EXPECTED_CASES)],
                          "passed": 0 if failure == "case" else len(r.EXPECTED_CASES), "total": len(r.EXPECTED_CASES), "client_version": "hf-discover 1.3.7"}
                return Result(1 if failure == "case" else 0, json.dumps(report), "")
            return Result(0, "{}", "")

        fingerprints = iter(("identity", "identity", "changed") if failure == "catalog-drift" else
                            ("identity", "identity", "identity", "changed") if failure == "harness-drift" else ())
        with tempfile.TemporaryDirectory() as name, patch.object(r, "OUTPUT", Path(name)), patch.object(r, "local_daemon", return_value="amd64"), patch.object(r, "installed_image", return_value={"go": "go", "image": "sha256:test"}), patch.object(r, "navigation_preflight"), patch.object(r, "fingerprint", side_effect=lambda _paths: next(fingerprints, "identity")), patch.object(r, "run", return_value=Result(0, "commit", "")), patch.object(r, "docker", side_effect=docker), contextlib.redirect_stdout(io.StringIO()):
            status = r.check()
            directory = next(Path(name).iterdir())
            report = json.loads((directory / "result.json").read_text())
            self.assertFalse((directory / "build").exists())
            self.assertEqual((directory / "progress").stat().st_mode & 0o777, 0o700)
            if failure == "traversal-interrupt":
                self.assertEqual(json.loads((directory / "progress/phase.json").read_text())["phase"], "native-pagination-page-2")
        cleanup = [args for args in calls if args[0] in ("rm", "network", "image") and "create" not in args]
        self.assertEqual(len(cleanup), 4)
        for args in cleanup:
            self.assertTrue(args[-1].startswith("open-agentic-catalog-hf-"))
        for args in calls:
            self.assertNotIn("--network=host", args)
            self.assertNotIn("-p", args)
            if args[0] == "run" and "--entrypoint=python" in args:
                mounts = [arg for arg in args if arg.startswith("type=bind")]
                self.assertEqual(len(mounts), 2)
                self.assertTrue(mounts[0].endswith("target=/harness,readonly"))
                self.assertTrue(mounts[1].endswith("/progress/phase.json,target=/phase.json"))
        return status, report
