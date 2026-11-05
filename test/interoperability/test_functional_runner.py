from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import json
import urllib.error
import urllib.request
import client_controls as controls
from client_contract import NATIVE_CASES
from common import run
import qualification
import runner as r
import runner_test_helpers as helpers


class Processes(unittest.TestCase):
    def test_success_failure_and_binary(self):
        result = run([sys.executable, "-c", "import sys; print('ok'); print('err',file=sys.stderr)"])
        self.assertEqual((result.code, result.stdout, result.stderr), (0, "ok\n", "err\n"))
        self.assertEqual(run([sys.executable, "-c", "import sys; sys.stdout.buffer.write(b'\\xff')"], binary=True).stdout, b"\xff")
        result = run([sys.executable, "-c", "raise SystemExit(3)"], check=False)
        self.assertEqual(result.code, 3)
        with self.assertRaisesRegex(RuntimeError, "failed \\(3\\)"):
            run([sys.executable, "-c", "raise SystemExit(3)"])
    def test_deadline_and_output_bounds(self):
        with self.assertRaisesRegex(RuntimeError, "deadline"):
            run([sys.executable, "-c", "import time; time.sleep(30)"], timeout=0.05)
        with self.assertRaisesRegex(RuntimeError, "deadline"):
            run([sys.executable, "-c", "import os,time; os.close(1); os.close(2); time.sleep(30)"], timeout=0.05)
        with self.assertRaisesRegex(RuntimeError, "output limit"):
            run([sys.executable, "-c", "print('x'*10000)"], limit=100)


class NativeChildReports(unittest.TestCase):
    @staticmethod
    def child_result(raw, code):
        """Exercise actual stdout/exit transport without importing independent tools."""
        script = f"print({raw!r}); raise SystemExit({code})"
        def child(_argv, **kwargs):
            return run([sys.executable, "-B", "-c", script], **kwargs)
        with patch.object(qualification, "run", side_effect=child):
            return qualification.child_results("native_search.py", NATIVE_CASES)

    def test_complete_child_reports_preserve_success_and_individual_failure(self):
        for failed in (False, True):
            cases = [helpers.passing_case(name) for name in NATIVE_CASES]
            if failed:
                cases[-1].update(status="fail", detail="wrong HTTP rejection")
            with self.subTest(failed=failed):
                self.assertEqual(self.child_result(json.dumps({"cases": cases}), int(failed)), cases)

    def test_real_child_cannot_pass_incomplete_invalid_or_contradictory_output(self):
        cases = [helpers.passing_case(name) for name in NATIVE_CASES]
        complete = json.dumps({"cases": cases})
        for raw, code in ((complete, 1), (complete, 2), ("not-json", 0), ("null", 0),
                          (json.dumps({"cases": cases[:-1]}), 0)):
            with self.subTest(raw=raw, code=code):
                actual = self.child_result(raw, code)
                self.assertEqual([case["name"] for case in actual], list(NATIVE_CASES))
                self.assertTrue(all(case["status"] == "fail" for case in actual))


class Supervisor(helpers.SupervisorFixture, unittest.TestCase):
    def test_make_isolation(self):
        """An external Make env file cannot select a target or inject execution."""
        with tempfile.TemporaryDirectory() as name:
            env = Path(name) / "env"
            env.write_text("$(error must not be loaded)\n")
            for target in ("test-interoperability", "test-interoperability-hf", "test-interoperability-agntcy", "interoperability-tools-install", "gateway-tools-install", "test-interoperability-gateway", "ans-tools-install", "test-interoperability-ans", "capture-ans-trust-fixtures", "test-trust-verification"):
                result = subprocess.run(["make", "-n", "-C", str(r.ROOT), target, "CATALOG_ENV_FILE=" + str(env)], capture_output=True, text=True, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
            result = subprocess.run(["make", "-n", "-C", str(r.ROOT), "test-interoperability", "check"], capture_output=True, text=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)

    def test_peer_python_selection_and_assertions_required(self):
        result = run(["make", "-n", "-C", str(r.ROOT), "test-interoperability-hf", "PYTHON=selected-python"])
        self.assertIn("selected-python -B test/interoperability/runner.py peer", result.stdout)
        result = run(["make", "-n", "-C", str(r.ROOT), "test-interoperability-hf", "check"], check=False)
        self.assertNotEqual(result.code, 0)
        self.assertIn("must run alone", result.stderr)
        result = run([sys.executable, "-O", "-B", str(r.HERE / "runner.py"), "peer"], check=False)
        self.assertEqual(result.code, 2)
        self.assertIn("requires Python assertions", result.stderr)


class LocalHTTPControls(unittest.TestCase):
    def test_real_exchange_and_cleanup_on_assertion_failure(self):
        with self.assertRaisesRegex(AssertionError, "injected"):
            with controls.local_http({("POST", "/search"): {"results": [controls.FULL]}}) as server:
                request = urllib.request.Request(server.origin + "/search",
                    json.dumps({"query": {"text": "full"}, "federation": "none", "pageSize": 1}).encode(),
                    {"Content-Type": "application/json"})
                opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
                with opener.open(request, timeout=3) as response:
                    controls.validate_full(json.load(response))
                controls.validate_exchange(server, "full")
                raise AssertionError("injected")
        self.assertEqual(server.fileno(), -1)

    def test_unselected_routes_body_bounds_and_token_evidence(self):
        with controls.local_http({("POST", "/search"): {"results": [controls.FULL]}}) as server:
            opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
            for path, body, headers, status in [("/other", None, {}, 404),
                                               ("/search", b"{}", {"Content-Length": "8193"}, 400),
                                               ("/search", b"bad-json", {}, 400)]:
                with self.subTest(path=path, status=status), self.assertRaises(urllib.error.HTTPError) as caught:
                    opener.open(urllib.request.Request(server.origin + path, body, headers), timeout=3)
                self.assertEqual(caught.exception.code, status)
                caught.exception.close()
            self.assertNotIn(controls.TOKEN, json.dumps(server.seen))
        self.assertEqual(server.fileno(), -1)
