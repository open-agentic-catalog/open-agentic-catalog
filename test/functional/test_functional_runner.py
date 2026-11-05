import contextlib
import io
import json
import os
from pathlib import Path
import signal
import shutil
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock
import runner


class RunnerTests(unittest.TestCase):
    def test_worker_timeout_evidence_survives_child_exit_and_owned_cleanup(self):
        script = """
import federation, runpy, sys
from test_unit_operations import WorkerRecoveryTests
class Client:
    def __init__(self, *_args):
        self.samples = iter(('job_worker', 'housekeeper'))
    def request(self, *_args):
        return WorkerRecoveryTests.outage_status(next(self.samples)), {'Cache-Control': 'no-store'}
def expire(_label, callback):
    assert callback() is False
    assert callback() is False
    raise AssertionError('private-worker-response')
federation.Client, federation.wait_until = Client, expire
sys.argv = ['operations.py', '--commit', 'commit', '--url', 'http://private-worker.invalid',
            '--ready', 'no', '--phase', 'degraded']
runpy.run_module('operations', run_name='__main__')
"""
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(Path, "cwd", return_value=Path(directory)):
            instance = runner.Runner({})
            instance.state = Path(directory) / "state"
            instance.state.mkdir()
            environment = dict(os.environ, PYTHONPATH=str(Path(runner.__file__).parent),
                               CATALOG_FUNCTIONAL_STATE_DIRECTORY=str(instance.state))
            def execute():
                runner.command([sys.executable, "-B", "-c", script], environment, 5)
            def cleanup():
                shutil.rmtree(instance.state)
                return True
            terminal = io.StringIO()
            with mock.patch.object(runner, "Runner", return_value=instance), \
                    mock.patch.object(instance, "execute", side_effect=execute), \
                    mock.patch.object(instance, "cleanup", side_effect=cleanup), \
                    mock.patch.object(signal, "signal"), \
                    contextlib.redirect_stdout(terminal), contextlib.redirect_stderr(terminal):
                self.assertEqual(runner.run(), 1)
            report = runner.read_document(instance.report_path)
            self.assertEqual((report["status"], report["failureCode"], report["cleanup"]), ("failed", "child-failed", "passed"))
            self.assertFalse(instance.state.exists())
            location = report["failureLocation"]
            self.assertEqual((location["scenario"], location["phase"]), ("operations", "degraded"))
            evidence = location["workerStatus"]
            self.assertEqual(evidence["observedDegraded"], ["job_worker"])
            self.assertEqual(evidence["samples"], 2)
            self.assertEqual(evidence["lastStatus"]["components"]["housekeeper"], "degraded")
            self.assertTrue(type(evidence["elapsedMillis"]) is int and 0 <= evidence["elapsedMillis"] < 5000)
            self.assertNotIn("private-worker", json.dumps(report) + terminal.getvalue())

    def test_failed_child_job_evidence_survives_cleanup_without_leaking_private_fields(self):
        script = """
import federation, runner
class Client:
    def request(self, *_args):
        return {"id":"private-id", "state":"failed", "attempt":2, "attemptLimit":7,
                "cancellationRequested":False, "failureClass":"internal",
                "safeError":"private-error", "result":{"private":"private-job-payload"}}, {}
print("private-child-output")
try:
    federation.wait_job(Client(), "private-id", {"succeeded"})
except AssertionError as error:
    runner.record_scenario_failure("federation", error)
    raise
"""
        for block_checkpoint in (False, True):
            with self.subTest(block_checkpoint=block_checkpoint), tempfile.TemporaryDirectory() as directory, \
                    mock.patch.object(Path, "cwd", return_value=Path(directory)):
                instance = runner.Runner({})
                instance.state = Path(directory) / "state"
                instance.state.mkdir()
                if block_checkpoint:
                    # A directory collision forces a real write/read error,
                    # independent of whether the test account can bypass modes.
                    (instance.state / "job-wait-failure.json").mkdir()
                environment = dict(os.environ, PYTHONPATH=str(Path(runner.__file__).parent),
                                   CATALOG_FUNCTIONAL_STATE_DIRECTORY=str(instance.state))
                def execute():
                    runner.command([sys.executable, "-B", "-c", script], environment, 5)
                def cleanup():
                    self.assertTrue(instance.state.exists())
                    shutil.rmtree(instance.state)
                    return True
                terminal = io.StringIO()
                with mock.patch.object(runner, "Runner", return_value=instance), \
                        mock.patch.object(instance, "execute", side_effect=execute), \
                        mock.patch.object(instance, "cleanup", side_effect=cleanup) as cleaned, \
                        mock.patch.object(signal, "signal"), \
                        contextlib.redirect_stdout(terminal), contextlib.redirect_stderr(terminal):
                    self.assertEqual(runner.run(), 1)
                    cleaned.assert_called_once()
                report = runner.read_document(instance.report_path)
                self.assertEqual(report["status"], "failed")
                self.assertEqual(report["failureCode"], "child-failed")
                self.assertEqual(report["cleanup"], "passed")
                self.assertEqual(report["failureLocation"]["scenario"], "federation")
                self.assertFalse(instance.state.exists())
                for private in ("private-id", "private-error", "private-job-payload", "private-child-output"):
                    self.assertNotIn(private, json.dumps(report) + terminal.getvalue())
                if block_checkpoint:
                    self.assertNotIn("jobWaitFailure", report)
                else:
                    observed = report["jobWaitFailure"]
                    elapsed = observed.pop("elapsedMillis")
                    self.assertTrue(type(elapsed) is int and 0 <= elapsed < 5000)
                    self.assertEqual(observed, {"state":"failed", "attempt":2, "attemptLimit":7,
                                              "failureClass":"internal", "cancellationRequested":False})

    def test_failed_readiness_and_operations_children_retain_only_safe_evidence(self):
        message = "Functional worker read-only readiness timed out waiting for worker HTTP 503."
        script = "import sys; print('private-secret', file=sys.stderr); print(" + repr(message) + ", file=sys.stderr)"
        with self.assertRaises(runner.Failure) as failure:
            runner.command([sys.executable, "-c", script + "; sys.exit(1)"], os.environ, 5)
        self.assertEqual(str(failure.exception), "worker-read-only-readiness-timeout")
        self.assertIsNone(failure.exception.http_failure)
        self.assertEqual(runner.command([sys.executable, "-c", script], os.environ, 5), b"")
        with tempfile.TemporaryDirectory() as directory:
            environment = dict(os.environ, PYTHONPATH=str(Path(runner.__file__).parent),
                               CATALOG_FUNCTIONAL_STATE_DIRECTORY=directory)
            # Exercise the actual operations entry point. A private stdin parse
            # failure must leave a location, never the input or a traceback.
            script = """
import io, runpy, sys
sys.argv = ['operations.py', '--commit', 'owned', '--diagnostics', 'worker']
sys.stdin = io.TextIOWrapper(io.BytesIO(b'private-invalid-json'))
runpy.run_module('operations', run_name='__main__')
"""
            with self.assertRaises(runner.Failure) as failure:
                runner.command([sys.executable, "-B", "-c", script], environment, 5)
            self.assertEqual(str(failure.exception), "child-failed")
            location = runner.read_scenario_failure(Path(directory), "default-off")
            self.assertEqual(location["scenario"], "operations")
            self.assertTrue(location["lines"])
            self.assertNotIn("private", json.dumps(location))
            self.assertNotIn("phase", location)
            # The real entry point must retain the selected outage/recovery
            # phase even when the poll raises, without exposing its cause.
            for phase in ("degraded", "recovered"):
                script = """
import federation, runpy, sys
def fail(*args, **kwargs):
    raise AssertionError('private-worker-response')
federation.wait_until = fail
phase = sys.argv[1]
sys.argv = ['operations.py', '--commit', 'owned', '--url', 'http://127.0.0.1:1',
            '--ready', 'yes' if phase == 'recovered' else 'no', '--phase', phase]
runpy.run_module('operations', run_name='__main__')
"""
                with self.subTest(phase=phase), self.assertRaises(runner.Failure) as failure:
                    runner.command([sys.executable, "-B", "-c", script, phase], environment, 5)
                self.assertEqual(str(failure.exception), "child-failed")
                location = runner.read_scenario_failure(Path(directory), "default-off")
                self.assertEqual(location["scenario"], "operations")
                self.assertEqual(location["phase"], phase)
                self.assertTrue(location["lines"])
                self.assertNotIn("private", json.dumps(location))

    def test_only_failed_children_retain_safe_http_diagnostic(self):
        message = "functional federation request returned HTTP 503 (DEPENDENCY_UNAVAILABLE)"
        with self.assertRaises(runner.Failure) as failure:
            runner.command([sys.executable, "-c", "print('private-secret'); raise AssertionError(" + repr(message) + ")"], os.environ, 5)
        self.assertEqual(str(failure.exception), "child-failed")
        self.assertEqual(failure.exception.http_failure, {"status": 503, "category": "DEPENDENCY_UNAVAILABLE"})
        self.assertEqual(runner.command([sys.executable, "-c", "print(" + repr("AssertionError: " + message) + ")"], os.environ, 5), b"")
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(Path, "cwd", return_value=Path(directory)):
            instance = runner.Runner({})
            with mock.patch.object(runner, "Runner", return_value=instance), \
                    mock.patch.object(instance, "execute", side_effect=failure.exception), \
                    mock.patch.object(instance, "cleanup", return_value=True), \
                    mock.patch.object(signal, "signal"), \
                    contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(runner.run(), 1)
            report = runner.read_document(instance.report_path)
            self.assertEqual(report["status"], "failed")
            self.assertEqual(report["failureCode"], "child-failed")
            self.assertEqual(report["httpFailure"], failure.exception.http_failure)
            self.assertNotIn("private-secret", json.dumps(report))
    def test_actual_peer_child_preserves_cursor_category_and_page_checkpoint(self):
        with tempfile.TemporaryDirectory() as directory:
            environment = dict(os.environ, CATALOG_FUNCTIONAL_STATE_DIRECTORY=directory)
            script = "\n".join([
                "from unittest.mock import patch",
                "from federation import Client",
                "from peer_pagination import request_page",
                "import peers",
                "payload = b'{\"errorCode\":\"INVALID_ARGUMENT\",\"message\":\"page token is invalid\",\"private\":\"private-secret\"}'",
                "with patch('federation.bounded_request', return_value=(400, {}, payload)), patch('federation.coverage.enabled', return_value=False):",
                " try: request_page(Client('http://127.0.0.1:1'), '/ard/v1/search', {'query': {'text': 'private-query'}}, 1)",
                " except Exception as error: peers.report_failure(error)",
                "raise SystemExit(1)",
            ])
            with self.assertRaises(runner.Failure) as caught:
                runner.command([sys.executable, "-B", "-c", script], environment, 5)
            self.assertEqual(caught.exception.http_failure, {"status": 400, "category": "PUBLIC_CURSOR_INVALID"})
            value = runner.read_document(Path(directory) / "search-page-failure.json")
            self.assertEqual(value, {"surface": "public", "page": 1, "completedPages": 0,
                                     "continuation": False, "httpFailure": caught.exception.http_failure})
            self.assertNotIn("private", json.dumps(value))

    def test_optimized_python_cannot_skip_assertions(self):
        environment = runner.compose_environment({"CATALOG_FUNCTIONAL_COMMIT": "a" * 40, "PYTHONOPTIMIZE": "2"}, "0123456789ab", Path("/owned"))
        self.assertEqual(environment["PYTHONOPTIMIZE"], "0")
        result = subprocess.run([sys.executable, "-B", "-O", "-c", "import runner"], capture_output=True, timeout=5, check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(b"functional assertions must be enabled", result.stderr)
    def test_child_output_is_bounded_and_secret_safe(self):
        for script, limit, code in [("print('secret-value'); raise SystemExit(1)", 1024, "child-failed"),
                                    ("print('secret-value' * 1000)", 64, "child-output-limit"),
                                    ("import sys; print('secret-value' * 1000, file=sys.stderr)", 64, "child-output-limit"),
                                    ("import os; os.write(1, b'x' * 40); os.write(2, b'y' * 40)", 64, "child-output-limit")]:
            terminal = io.StringIO()
            with contextlib.redirect_stdout(terminal), contextlib.redirect_stderr(terminal):
                with self.assertRaisesRegex(runner.Failure, code) as error:
                    runner.command([sys.executable, "-c", script], os.environ, 5, limit=limit)
            self.assertNotIn("secret-value", str(error.exception) + terminal.getvalue())
        self.assertEqual(runner.command([sys.executable, "-c", "print('safe')"], os.environ, 5, capture=True), b"safe\n")

    def test_failed_stdout_sink_does_not_suppress_failure_or_capture_stderr(self):
        captured = []
        script = "import sys; print('assertion evidence'); print('private stderr', file=sys.stderr); sys.exit(1)"
        with self.assertRaises(runner.Failure) as raised:
            runner.command([sys.executable, "-c", script], os.environ, 5, capture=True, failed_stdout=captured.append)
        self.assertEqual(str(raised.exception), "child-failed")
        self.assertEqual(captured, [b"assertion evidence\n"])
        captured.clear()
        with self.assertRaises(runner.Failure):
            runner.command([sys.executable, "-c", script], os.environ, 5, failed_stdout=captured.append)
        self.assertEqual(captured, [])

    def test_capture_preserves_stdout_json_and_drains_private_stderr(self):
        # Operational diagnostics can be text or JSON, and can exceed a pipe's
        # capacity. Neither belongs in the command's machine-readable result.
        for diagnostic in ('level=INFO msg="private-diagnostic"', '{"level":"INFO","msg":"private-diagnostic"}'):
            with self.subTest(diagnostic=diagnostic):
                script = "import sys; sys.stderr.write(" + repr(diagnostic + "\n") + " * 8192); print('{\"status\":\"ready\"}')"
                terminal = io.StringIO()
                with contextlib.redirect_stdout(terminal), contextlib.redirect_stderr(terminal):
                    result = runner.command([sys.executable, "-c", script], os.environ, 5, limit=1 << 20, capture=True)
                self.assertEqual(result, b'{"status":"ready"}\n')
                self.assertEqual(json.loads(result), {"status": "ready"})
                self.assertEqual(terminal.getvalue(), "")

    def test_deadline_kills_child_and_inherited_pipe(self):
        for script in ("import time; time.sleep(30)",
                       "import os,time; os.close(1); os.close(2); time.sleep(30)",
                       "import subprocess,sys; subprocess.Popen([sys.executable,'-c','import time; time.sleep(30)'])"):
            started = time.monotonic()
            with self.assertRaisesRegex(runner.Failure, "deadline"):
                runner.command([sys.executable, "-c", script], os.environ, 0.2)
            self.assertLess(time.monotonic() - started, 3)
        with self.assertRaisesRegex(runner.Failure, "deadline"):
            runner.command(["must-not-start"], os.environ, 0)
    def test_cleanup_is_exact_and_failure_is_not_hidden(self):
        with tempfile.TemporaryDirectory() as directory, mock.patch.object(Path, "cwd", return_value=Path(directory)):
            instance = runner.Runner({})
            instance.environment = runner.compose_environment({"CATALOG_FUNCTIONAL_COMMIT": "a" * 40}, instance.suffix, Path(directory))
            instance.compose += ["-p", instance.project]
            calls = []
            def fake(arguments, _environment, seconds, **_kwargs):
                self.assertGreater(seconds, 0)
                calls.append(arguments)
                if "down" in arguments:
                    raise runner.Failure("deadline")
                return b"sha256:exists" if "ls" in arguments else b""
            with mock.patch.object(runner, "command", side_effect=fake):
                self.assertFalse(instance.cleanup())
            self.assertIn(instance.project, calls[0])
            removed = [args[-1] for args in calls if "rm" in args]
            self.assertEqual(removed, ["open-agentic-catalog:functional-" + instance.suffix,
                                       "open-agentic-catalog:functional-" + instance.suffix + "-fixture"])
            self.assertEqual(runner.read_document(instance.report_path)["status"], "incomplete")
            calls.clear()
            instance.environment["CATALOG_FUNCTIONAL_CANDIDATE_IMAGE"] = instance.environment["IMAGE"]
            with mock.patch.object(runner, "command", side_effect=fake):
                self.assertFalse(instance.cleanup())
            self.assertEqual([args[-1] for args in calls if "rm" in args],
                             [instance.environment["CATALOG_FUNCTIONAL_FIXTURE_IMAGE"]])
