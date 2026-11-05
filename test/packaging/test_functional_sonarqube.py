"""Make isolation and real loopback API handling; no Sonar or Docker dependency."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import unittest

import sonarqube as sonar


class SonarFunctionalTests(unittest.TestCase):
    def test_make_targets_bypass_workflow_files_and_never_join_default_gates(self):
        targets = ["sonarqube-tools-install", "sonarqube-up", "sonarqube-scan", "sonarqube-status", "sonarqube-credentials", "sonarqube-down"]
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "Makefile").write_text((sonar.ROOT / "Makefile").read_text())
            (root / "go.mod").write_text((sonar.ROOT / "go.mod").read_text())
            (root / ".env").write_text("$(error workflow file loaded)\n")
            environment = {key: value for key, value in os.environ.items() if key not in ("MAKEFLAGS", "MFLAGS")}
            for target in targets:
                result = subprocess.run(["make", "-n", target], cwd=root, env=environment, capture_output=True, text=True, timeout=10, check=False)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertIn("test/packaging/sonarqube.py", result.stdout)
                result = subprocess.run(["make", "-n", target, "check"], cwd=root, env=environment, capture_output=True, text=True, timeout=10, check=False)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("must run alone", result.stderr)

    def test_credentials_target_reads_saved_state_without_docker_or_a_server(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ("Makefile", "go.mod", "test/sonarqube/images.json", "test/packaging/sonarqube.py",
                         "test/packaging/application_coverage.py", "test/packaging/unit_coverage.py"):
                target = root / name
                target.parent.mkdir(parents=True, exist_ok=True)
                target.write_bytes((sonar.ROOT / name).read_bytes())
            (root / ".env").write_text("$(error workflow file loaded)\n")
            state = root / "output/sonarqube"
            state.mkdir(parents=True)
            sonar.write_json(state / "credentials.json", {"initialized": True, "password": "synthetic-secret", "port": 19001})
            binaries = root / "bin"
            binaries.mkdir()
            docker = binaries / "docker"
            docker.write_text("#!/bin/sh\nprintf 'Docker must not run' >&2\nexit 99\n")
            docker.chmod(0o700)
            environment = {key: value for key, value in os.environ.items() if key not in ("MAKEFLAGS", "MFLAGS", "SONAR_PORT")}
            environment["PATH"] = str(binaries) + os.pathsep + environment["PATH"]
            # The Go wrapper inherits a recursive Make level under make verify.
            for level in ("0", "1"):
                with self.subTest(make_level=level):
                    result = subprocess.run(["make", "--no-print-directory", "sonarqube-credentials"], cwd=root,
                                            env={**environment, "MAKELEVEL": level},
                                            capture_output=True, text=True, timeout=10, check=False)
                    self.assertEqual(result.returncode, 0, result.stderr)
                    self.assertEqual(result.stdout, "Dashboard: http://127.0.0.1:19001/dashboard?id=open-agentic-catalog\nUsername: admin\nPassword: synthetic-secret\n")
                    self.assertFalse((state / "workflow.lock").exists())

    def test_local_api_does_not_follow_redirects_or_forward_credentials(self):
        requests = []
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_GET(self):
                requests.append(self.path)
                if self.path == "/redirect":
                    self.send_response(302)
                    self.send_header("Location", "/unexpected")
                    self.end_headers()
                else:
                    self.send_response(200)
                    self.end_headers()
                    self.wfile.write(json.dumps({"status": "UP"}).encode())

        with ThreadingHTTPServer(("127.0.0.1", 0), Handler) as server:
            worker = threading.Thread(target=server.serve_forever)
            worker.start()
            try:
                local = object.__new__(sonar.Local)
                local.base = "http://127.0.0.1:" + str(server.server_port)
                self.assertEqual(local.api("/status"), {"status": "UP"})
                with self.assertRaisesRegex(sonar.Failure, "redirect"):
                    local.api("/redirect", password="synthetic-secret")
                self.assertEqual(requests, ["/status", "/redirect"])
            finally:
                server.shutdown()
                worker.join(timeout=5)
