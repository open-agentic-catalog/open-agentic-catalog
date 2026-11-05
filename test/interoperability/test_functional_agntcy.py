"""Owned loopback/process checks for the runner; never execute Directory or a cluster."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import ssl
import subprocess
import sys
import tempfile
import threading
import unittest
from unittest.mock import patch

import agntcy_lab as lab
import agntcy_runner as r
from common import run


class FixtureHTTP(unittest.TestCase):
    def test_request_confinement_credentials_redirects_and_body_bounds(self):
        seen = []

        class Handler(BaseHTTPRequestHandler):
            def do_GET(self):
                seen.append((self.path, self.headers.get("Authorization")))
                self.send_response(302 if self.path == "/redirect" else 200)
                self.send_header("Location", "/must-not-follow")
                self.end_headers()
                self.wfile.write(b"x" * 64 if self.path == "/large" else b'{}')

            def log_message(self, *_args):
                pass

        with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as server:
            thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.01})
            thread.start()
            try:
                origin = f"http://127.0.0.1:{server.server_port}"
                # HTTP transport controls need no certificate generator or external CA lookup.
                with patch.object(lab.ssl, "create_default_context", return_value=ssl.SSLContext(ssl.PROTOCOL_TLS_CLIENT)):
                    client = lab.LocalHTTP({"catalog": origin, "peer": origin}, "/unused-ca", token="owned-oidc-token")
                self.assertEqual(client.document("peer", "/peer")[0], {})
                self.assertEqual(client.document("catalog", "/catalog")[0], {})
                with self.assertRaisesRegex(AssertionError, "302"):
                    client.request("peer", "/redirect")
                with patch.object(lab, "LIMIT", 32), self.assertRaisesRegex(ValueError, "size bound"):
                    client.request("peer", "/large")
                self.assertEqual(seen, [("/peer", None), ("/catalog", "Bearer owned-oidc-token"), ("/redirect", None), ("/large", None)])
                with self.assertRaises(ValueError):
                    client.request("peer", "//another-origin")
                self.assertEqual(len(seen), 4)
            finally:
                server.shutdown()
                thread.join(timeout=3)
                self.assertFalse(thread.is_alive())


class Commands(unittest.TestCase):
    def test_make_isolation_selected_python_and_disabled_assertions(self):
        command = ["make", "-n", "-C", str(r.shared.ROOT), "test-interoperability-agntcy"]
        result = run([*command, "PYTHON=selected-python", "AGNTCY_LAB_DIR=/explicit/lab"])
        self.assertIn("selected-python -B test/interoperability/agntcy_runner.py", result.stdout)
        self.assertNotEqual(run([*command, "check"], check=False).code, 0)
        result = run([sys.executable, "-O", "-B", str(r.shared.HERE / "agntcy_runner.py")], check=False)
        self.assertEqual(result.code, 2)
        self.assertIn("requires Python assertions", result.stderr)

    def test_missing_lab_fails_before_build_or_server_and_removes_private_state(self):
        with tempfile.TemporaryDirectory() as directory:
            code = ("import os,sys; from pathlib import Path; import agntcy_runner as r; "
                    "r.shared.OUTPUT=Path(sys.argv[1]); raise SystemExit(r.main())")
            env = dict(os.environ, AGNTCY_LAB_DIR="", PYTHONPATH=str(r.shared.HERE))
            result = run([sys.executable, "-B", "-c", code, directory], env=env, check=False)
            self.assertEqual(result.code, 1)
            report = json.loads(next(Path(directory).glob("*/result.json")).read_text())
            self.assertIn("AGNTCY_LAB_DIR is required", report["error"])
            self.assertIsNone(report["owned"]["catalog_pid"])
            self.assertEqual(report["cleanup"]["status"], "pass")
            self.assertTrue(report["cleanup"]["private_removed"])
            self.assertTrue(all(case["status"] == "unrun" for case in report["cases"]))

    def test_shutdown_terminates_only_the_owned_process(self):
        with tempfile.TemporaryDirectory() as directory:
            ready = Path(directory) / "ready"
            code = ("import signal,time; from pathlib import Path; "
                    "signal.signal(signal.SIGTERM, lambda *_: exit(0)); "
                    "Path(__import__('sys').argv[1]).touch(); time.sleep(30)")
            process = subprocess.Popen([sys.executable, "-B", "-c", code, str(ready)], start_new_session=True)
            try:
                r.wait_for(lambda: True if ready.exists() else None, "test child", timeout=5)
                r.stop_catalog(process)
                self.assertEqual(process.returncode, 0)
            finally:
                if process.poll() is None:
                    process.kill()
                    process.wait(timeout=5)
