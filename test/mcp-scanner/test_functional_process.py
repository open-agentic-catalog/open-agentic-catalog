"""Owned subprocess doubles. These tests never acquire or run MCP Scanner."""

import contextlib
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

from fixtures import base, core, document, issue, native, runner


FAKE_DOCKER = r'''
import json, os, pathlib, sys, time
state_path = pathlib.Path(os.environ["FAKE_STATE"])
state = json.loads(state_path.read_text())
args = sys.argv[1:]
def save():
 staged = state_path.with_suffix(".writing")
 staged.write_text(json.dumps(state))
 staged.replace(state_path)
if args[2:4] == ["container", "ls"]:
 if state.get("query_fail"): sys.exit(2)
 if state.get("exists"): print("a" * 64)
elif args[2:4] == ["container", "inspect"]:
 print(json.dumps([{"Id":"a"*64, "Name":"/"+state["name"], "Config":{"Labels":{
  "io.open-agentic-catalog.mcp-scanner": "wrong" if state.get("foreign") else state["marker"]}}}]))
elif args[2] == "rm":
 if state.get("remove_fail"): sys.exit(2)
 state["exists"] = False
 save()
elif args[2] == "create":
 state.update(exists=True, name=args[args.index("--name")+1], marker=args[args.index("--label")+1].split("=",1)[1],
              args=args, environment=dict(os.environ))
 if "--mount" in args:
  input_path = pathlib.Path(args[args.index("--mount")+1].split(",dst=",1)[0].removeprefix("type=bind,src="))
  state["input_names"] = sorted(p.name for p in input_path.iterdir())
 save()
 if state.get("mode") == "uncertain_create": sys.exit(2)
 print("a" * 64)
elif args[2] == "start":
 state["started"] = True
 save()
 mode = state.get("mode", "valid")
 if mode == "timeout": time.sleep(60)
 if mode == "closed_pipes":
  os.close(1)
  os.close(2)
  time.sleep(60)
 if mode in ("stdout_limit", "stderr_limit"):
  stream = sys.stdout if mode == "stdout_limit" else sys.stderr
  for unused in range(1000):
   stream.write("x" * 65536)
   stream.flush()
 elif mode == "malformed": print('{"results":')
 else: print(json.dumps(state["report"]))
 if mode == "nonzero": sys.exit(7)
else: sys.exit(9)
'''


class ProcessTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory(prefix="catalog-scanner-process-")
        self.addCleanup(self.temporary.cleanup)
        self.directory = Path(self.temporary.name)
        self.executable = self.directory / "docker"
        self.executable.write_text("#!" + sys.executable + "\n" + FAKE_DOCKER)
        self.executable.chmod(0o700)
        self.state_path = self.directory / "state.json"
        self.state_path.write_text(json.dumps({"report": native()}))
        self.env = {"PATH": str(self.directory), "HOME": str(self.directory), "FAKE_STATE": str(self.state_path)}
        (self.directory / "input").mkdir()

    def state(self, **updates):
        current = json.loads(self.state_path.read_text())
        if updates:
            current.update(updates)
            self.state_path.write_text(json.dumps(current))
        return current

    def run_scan(self):
        capture = self.directory / "capture.json"
        capture.write_bytes(core.encoded(document()))
        report = self.directory / "report.json"
        identity = {"version": "4.8.7", "imageId": "sha256:" + "a" * 64, "rulesSha256": "b" * 64}
        with mock.patch.object(runner, "environment", return_value=self.env), mock.patch.object(runner, "require_installed", return_value=identity):
            code = runner.scan(capture, report, "test capture", "2026-10-28T00:00:00Z")
        return code, json.loads(report.read_bytes())

    def test_owned_process_uses_network_denial_and_only_the_owned_input_mount(self):
        result = runner.execute("local-image", self.directory, self.env)
        self.assertEqual((result[1], result[2], result[3]), (0, None, False))
        actual = self.state()
        args = actual["args"]
        for flag in ("--network=none", "--read-only", "--pull=never", "--cap-drop=ALL", "--security-opt=no-new-privileges"):
            self.assertIn(flag, args)
        self.assertEqual(args.count("--mount"), 1)
        self.assertIn("dst=/input,readonly", args[args.index("--mount") + 1])
        self.assertEqual(args[args.index("--user") + 1], str(os.getuid()) + ":" + str(os.getgid()))
        self.assertNotIn("-e", args)
        self.assertFalse(actual["exists"])

    def test_installation_identity_uses_confirmed_creation_and_owned_cleanup(self):
        self.state(report={"version": "4.8.7", "packages": {}, "rulesSha256": "b" * 64})
        identity = runner.image_identity("local-image", self.env)
        self.assertEqual(identity["version"], "4.8.7")
        self.assertIn("--identity", self.state()["args"])
        self.assertFalse(self.state()["exists"])

    def test_timeout_and_closed_pipes_terminate_the_process_and_remove_owned_container(self):
        for mode in ("timeout", "closed_pipes"):
            self.state(mode=mode)
            raw, code, failure, cleanup_failed = runner.execute("local-image", self.directory, self.env, seconds=0.2)
            self.assertEqual(failure, "scanner_timeout")
            self.assertFalse(cleanup_failed)
            self.assertNotEqual(code, 0)
            self.assertEqual(raw, b'')
            self.assertFalse(self.state()["exists"])

    def test_uncertain_creation_is_never_reported_as_confirmed_cleanup(self):
        self.state(mode="uncertain_create")
        _raw, _code, failure, cleanup_failed = runner.execute("local-image", self.directory, self.env)
        self.assertEqual(failure, "scanner_creation_failed")
        self.assertTrue(cleanup_failed)
        self.assertFalse(self.state()["exists"])
        self.assertFalse(self.state().get("started", False))

    def test_both_output_streams_are_bounded(self):
        for mode in ("stdout_limit", "stderr_limit"):
            self.state(mode=mode)
            raw, _code, failure, cleanup_failed = runner.execute("local-image", self.directory, self.env)
            self.assertEqual(failure, "scanner_output_limit")
            self.assertLessEqual(len(raw), core.REPORT_LIMIT)
            self.assertFalse(cleanup_failed)
            self.assertFalse(self.state()["exists"])

    def test_uncertain_engine_and_foreign_containers_are_not_cleaned_or_reported_clean(self):
        self.state(query_fail=True)
        self.assertTrue(runner.cleanup_container("owned", "marker", self.env))
        with self.assertRaises(ValueError):
            runner.execute("local-image", self.directory, self.env)
        self.state(query_fail=False, exists=True, name="owned", marker="marker", foreign=True)
        self.assertTrue(runner.cleanup_container("owned", "marker", self.env))
        self.assertTrue(self.state()["exists"])
        self.state(foreign=False, remove_fail=True)
        self.assertTrue(runner.cleanup_container("owned", "marker", self.env))

    def test_valid_report_and_failed_exit_preserve_findings_without_claiming_success(self):
        report = native()
        report["results"][0]["findings"] = [issue()]
        self.state(mode="nonzero", report=report)
        code, result = self.run_scan()
        self.assertEqual((code, result["status"], result["error"], result["cleanup"]), (2, "failed", "scanner_process_failed", "passed"))
        self.assertEqual(result["results"][0]["findings"], [issue()])
        self.assertEqual(self.state()["input_names"], ["capture.json", "context.json"])
        self.assertEqual(stat.S_IMODE((self.directory / "report.json").stat().st_mode), 0o600)

    def test_malformed_output_is_a_failed_report(self):
        self.state(mode="malformed")
        code, result = self.run_scan()
        self.assertEqual((code, result["status"], result["error"]), (2, "failed", "invalid_scanner_report"))
        self.assertEqual(result["completedTools"], 0)

    def test_cleanup_failure_preserves_findings_and_fails_the_report(self):
        report = native()
        report["results"][0]["findings"] = [issue()]
        with mock.patch.object(runner, "execute", return_value=(core.encoded(report), 0, None, True)):
            code, result = self.run_scan()
        self.assertEqual((code, result["status"], result["error"], result["cleanup"]), (2, "failed", "scanner_cleanup_failed", "failed"))
        self.assertEqual(result["results"][0]["findings"], [issue()])

    def test_cleanup_failure_does_not_hide_the_original_scan_failure(self):
        with mock.patch.object(runner, "execute", return_value=(b"", 2, "scanner_timeout", True)):
            code, result = self.run_scan()
        self.assertEqual((code, result["status"], result["error"], result["cleanup"]), (2, "failed", "scanner_timeout", "failed"))

    def test_final_report_metadata_cannot_exceed_the_report_limit(self):
        report = native()
        finding = issue()
        finding["details"]["padding"] = ""
        report["results"][0]["findings"] = [finding]
        normalized = core.normalize(core.encoded(report), document(), base())
        finding["details"]["padding"] = "x" * (core.REPORT_LIMIT - len(core.encoded(normalized)) - 1)
        raw = core.encoded(report)
        self.assertLess(len(raw), core.REPORT_LIMIT)
        with mock.patch.object(runner, "execute", return_value=(raw, 0, None, False)):
            code, result = self.run_scan()
        self.assertEqual((code, result["status"], result["error"], result["cleanup"]), (2, "failed", "scanner_report_limit", "passed"))
        self.assertEqual(result["completedTools"], 1)
        self.assertEqual(result["results"], [])
        self.assertLess((self.directory / "report.json").stat().st_size, core.REPORT_LIMIT)

    def test_root_scan_is_refused_before_any_docker_call(self):
        with mock.patch.object(runner.os, "getuid", return_value=0), \
                mock.patch.object(runner, "docker", side_effect=AssertionError("root scan contacted Docker")), \
                self.assertRaisesRegex(ValueError, "non-root"):
            runner.execute("local-image", self.directory, self.env)

    def test_malformed_cleanup_inspection_is_incomplete_and_never_removes_a_container(self):
        malformed = (None, {}, [None], [{"Id": "a" * 64, "Name": "/owned", "Config": None}],
                     [{"Id": "a" * 64, "Name": "/owned", "Config": {"Labels": [1]}}],
                     [{"Name": "/owned", "Config": {"Labels": []}}],
                     [{"Name": "/owned", "Config": {"Labels": {runner.LABEL: "marker"}}}])
        for data in malformed:
            inspected = subprocess.CompletedProcess([], 0, json.dumps(data), "")
            with self.subTest(data=data), mock.patch.object(runner, "named_containers", return_value=["a" * 64]), \
                    mock.patch.object(runner, "docker", return_value=inspected) as operation:
                self.assertTrue(runner.cleanup_container("owned", "marker", self.env))
                self.assertEqual(operation.call_count, 1)

    def test_sigterm_writes_an_interrupted_report_and_confirms_cleanup(self):
        self.state(mode="timeout")
        capture = self.directory / "capture.json"
        capture.write_bytes(core.encoded(document()))
        output = self.directory / "interrupted.json"
        script = '''import importlib.util, json, signal, sys
from pathlib import Path
spec=importlib.util.spec_from_file_location("runner", sys.argv[1])
runner=importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)
runner.environment=lambda unused: json.loads(sys.argv[2])
runner.require_installed=lambda unused: {"version":"4.8.7","imageId":"sha256:"+"a"*64,"rulesSha256":"b"*64}
signal.signal(signal.SIGTERM, runner.interrupted)
raise SystemExit(runner.scan(Path(sys.argv[3]),Path(sys.argv[4]),"test capture","2026-10-28T00:00:00Z"))
'''
        process = subprocess.Popen([sys.executable, "-B", "-c", script, str(runner.HERE / "runner.py"), json.dumps(self.env), str(capture), str(output)],
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            deadline = time.monotonic() + 5
            while not self.state().get("started") and time.monotonic() < deadline:
                time.sleep(0.02)
            self.assertTrue(self.state().get("started"), "owned process did not start")
            process.terminate()
            process.communicate(timeout=10)
            self.assertEqual(process.returncode, 2)
            result = json.loads(output.read_bytes())
            self.assertEqual((result["status"], result["error"], result["cleanup"]), ("failed", "scanner_interrupted", "passed"))
            self.assertFalse(self.state()["exists"])
        finally:
            if process.poll() is None:
                process.kill()
                process.communicate(timeout=5)

    def test_empty_work_requires_installation_and_does_not_launch_scanner(self):
        capture = self.directory / "empty.json"
        capture.write_text('{"tools":[]}')
        output = self.directory / "empty-report.json"
        with mock.patch.object(runner, "require_installed", return_value={"version": "4.8.7", "imageId": "image", "rulesSha256": "b" * 64}), \
                mock.patch.object(runner, "execute", side_effect=AssertionError("empty capture launched scanner")):
            self.assertEqual(runner.scan(capture, output, "empty fixture", "2026-10-28T00:00:00Z"), 2)
        self.assertEqual(json.loads(output.read_bytes())["status"], "skipped")
        with mock.patch.object(runner, "require_installed", side_effect=ValueError("missing tools")), self.assertRaises(ValueError):
            runner.scan(capture, self.directory / "absent.json", "empty fixture", "2026-10-28T00:00:00Z")
        self.assertFalse((self.directory / "absent.json").exists())

    def test_report_write_does_not_overwrite_existing_files_or_links(self):
        output = self.directory / "report.json"
        runner.write_report(output, base())
        before = output.read_bytes()
        with self.assertRaises(FileExistsError):
            runner.write_report(output, {"replacement": True})
        self.assertEqual(output.read_bytes(), before)
        link = self.directory / "link.json"
        link.symlink_to(output)
        with self.assertRaises(ValueError):
            runner.write_report(link, {}, replace=True)
        self.assertEqual(output.read_bytes(), before)
        with self.assertRaises(OSError):
            runner.read_file(link, core.INPUT_LIMIT)
        with self.assertRaises(ValueError):
            runner.read_file(self.directory, core.INPUT_LIMIT)

    def test_environment_excludes_provider_proxy_dotenv_and_docker_overrides(self):
        poison = {"OPENAI_API_KEY": "never-forward", "ANTHROPIC_API_KEY": "never-forward", "HTTP_PROXY": "never-forward",
                  "PYTHONPATH": "never-forward", "DOCKER_HOST": "tcp://never-forward", "COMPOSE_FILE": "never-forward"}
        with mock.patch.dict(os.environ, poison):
            actual = runner.environment(self.directory)
        for key in poison:
            if key == "DOCKER_HOST":
                self.assertEqual(actual[key], "unix:///var/run/docker.sock")
            else:
                self.assertNotIn(key, actual)

    def test_missing_receipt_fails_without_installation(self):
        with mock.patch.object(runner, "OWNED", self.directory / "missing"), self.assertRaises(ValueError), \
                mock.patch.object(runner, "docker", side_effect=AssertionError("missing tools contacted Docker")):
            runner.require_installed(self.env)

    def test_identity_command_only_returns_the_current_scanner_binding(self):
        installed = {"version": "4.8.7", "imageId": "sha256:" + "a" * 64,
                     "rulesSha256": "b" * 64, "packages": {"private_inventory": {"version": "1"}}}
        output = io.BytesIO()
        with mock.patch.object(sys, "argv", ["runner.py", "identity"]), \
                mock.patch.object(runner, "require_installed", return_value=installed), \
                mock.patch.object(sys, "stdout", mock.Mock(buffer=output)):
            self.assertEqual(runner.main(), 0)
        value = json.loads(output.getvalue())
        self.assertEqual(set(value), {"version", "imageId", "rulesSha256", "configurationSha256"})
        self.assertEqual(value["configurationSha256"], core.digest(core.encoded(core.configuration("b" * 64))))

    def test_stale_receipt_fails_before_docker(self):
        owned = self.directory / "tools"
        owned.mkdir()
        (owned / "installed.json").write_text('{"version":"4.8.7","fingerprint":"old"}')
        with mock.patch.object(runner, "OWNED", owned), mock.patch.object(runner, "fingerprint", return_value="current"), \
                mock.patch.object(runner, "docker", side_effect=AssertionError("stale tools contacted Docker")), self.assertRaises(ValueError):
            runner.require_installed(self.env)

    def test_image_and_rule_drift_are_refused(self):
        owned = self.directory / "tools"
        owned.mkdir()
        receipt = {"version": runner.PINS["scanner"], "fingerprint": "current", "imageId": "sha256:" + "a" * 64,
                   "rulesSha256": runner.PINS["rulesSha256"]}
        for image, rules in (({"Id": "sha256:" + "b" * 64, "Config": {"User": "65532:65532"}}, receipt["rulesSha256"]),
                             ({"Id": receipt["imageId"], "Config": {"User": "0:0"}}, receipt["rulesSha256"]),
                             ({"Id": receipt["imageId"], "Config": {"User": "65532:65532"}}, "b" * 64)):
            (owned / "installed.json").write_text(json.dumps({**receipt, "rulesSha256": rules}))
            with mock.patch.object(runner, "OWNED", owned), mock.patch.object(runner, "fingerprint", return_value="current"), \
                    mock.patch.object(runner, "image_inspection", return_value=image), self.assertRaises(ValueError):
                runner.require_installed(self.env)

    def test_pending_scanner_exception_prevents_setup_before_downloads(self):
        clock = mock.Mock(wraps=runner.datetime)
        clock.now.return_value = runner.datetime(2026, 10, 28, tzinfo=runner.timezone.utc)
        with mock.patch.object(runner, "PINS", {**runner.PINS, "scannerAgeExceptionApproved": False}), \
                mock.patch.object(runner, "datetime", clock), mock.patch.object(runner, "docker", side_effect=AssertionError("unapproved tool download")), \
                mock.patch.object(runner.os, "getuid", return_value=1000), mock.patch.object(runner.platform, "system", return_value="Linux"), \
                mock.patch.object(runner.platform, "machine", return_value="x86_64"), self.assertRaisesRegex(ValueError, "age exception"):
            runner.install()

    def test_cli_failure_does_not_disclose_input_or_environment(self):
        capture = self.directory / "invalid.json"
        capture.write_text('private-scanned-content')
        with mock.patch.object(sys, "argv", ["runner.py", "scan", "--input", str(capture), "--output", str(self.directory / "out.json"),
                                             "--source", "private-origin", "--captured-at", "2026-10-28T00:00:00Z"]), \
                contextlib.redirect_stderr(io.StringIO()) as stderr:
            self.assertEqual(runner.main(), 2)
        self.assertNotIn("private", stderr.getvalue())


if __name__ == "__main__":
    unittest.main()
