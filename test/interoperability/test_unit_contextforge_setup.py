"""Isolated setup command, credential-file and interruption-signal checks."""

from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from common import Result
import contextforge_runner as runner
import contextforge_tools as tools
from contextforge.checks import proxy_progress


class Setup(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.output = Path(temporary.name)
        self.directory = self.output / "catalog-contextforge-012345abcdef"
        self.directory.mkdir()
        self.private = self.directory / "private"
        (self.private / "client-state").mkdir(parents=True)
        self.state = {"project": self.directory.name, "directory": str(self.directory), "retained": [], "phase": "prepared", "source": "frozen", "success": False, "cleanup": False}
        runner.json_file(self.directory / "owner.json", self.state)
        runner.json_file(self.private / "secrets.json", {"tokens": {}, "cfPassword": "password-sentinel"})
        connections = {"admin": "private-admin-token", "connections": {"a": {"server": "a" * 32, "token": "private-reader-token"}}}
        runner.json_file(self.private / "client-state/contextforge-connections.json", connections)
        for name, value in (("OUTPUT", self.output),):
            setting = patch.object(tools, name, value)
            setting.start()
            self.addCleanup(setting.stop)

    def command(self, *arguments):
        return patch.object(runner.sys, "argv", ["contextforge_runner.py", *arguments])

    def test_prepare_returns_only_the_owned_directory_and_keeps_it(self):
        out = io.StringIO()
        with self.command("prepare"), patch.object(runner, "prepare", return_value=self.directory), patch.object(runner, "stop") as stop, redirect_stdout(out):
            runner.main()
        self.assertEqual(out.getvalue(), str(self.directory) + "\n")
        stop.assert_not_called()

    def test_successful_manual_stage_keeps_the_fixture_for_the_next_step(self):
        out = io.StringIO()
        with self.command("start", str(self.directory)), patch.object(runner, "fingerprint", return_value="frozen"), \
             patch.object(runner, "start") as start, patch.object(runner, "stop") as stop, redirect_stdout(out):
            runner.main()
        start.assert_called_once_with(self.directory)
        stop.assert_not_called()
        self.assertEqual(json.loads(out.getvalue())["stage"], "start")

    def test_wrong_order_and_source_drift_refuse_before_mutation_or_teardown(self):
        for action, source in (("register", "frozen"), ("proxy", "frozen"), ("handoff", "frozen"), ("start", "changed")):
            with self.subTest(action=action, source=source), self.command(action, str(self.directory)), \
                 patch.object(runner, "fingerprint", return_value=source), patch.object(runner, "compose") as compose, patch.object(runner, "stop") as stop, self.assertRaises(AssertionError):
                runner.main()
            compose.assert_not_called()
            stop.assert_not_called()
            self.assertTrue(self.private.exists())

    def test_manual_failure_preserves_the_original_error_and_cleans_owned_state(self):
        error = RuntimeError("synthetic stage failure")
        with self.command("start", str(self.directory)), patch.object(runner, "fingerprint", return_value="frozen"), \
             patch.object(runner, "start", side_effect=error), patch.object(runner, "compose", return_value=Result(0, "", "")), \
             patch.object(runner, "stop") as stop, self.assertRaises(RuntimeError) as caught:
            runner.main()
        self.assertIs(caught.exception, error)
        stop.assert_called_once_with(self.directory)
        self.assertFalse(runner.owner(self.directory)["success"])

    def test_unknown_or_incomplete_commands_never_prepare_or_mutate(self):
        for args in (("publish",), ("start",), ("prepare", "extra"), ("check", "extra"), ("stop",)):
            with self.subTest(args=args), self.command(*args), patch.object(runner, "prepare") as prepare, patch.object(runner, "compose") as compose, self.assertRaises(ValueError):
                runner.main()
            prepare.assert_not_called()
            compose.assert_not_called()

    def test_registration_writes_complete_headers_without_printing_credentials(self):
        out = io.StringIO()
        with patch.object(runner, "native") as native, redirect_stdout(out):
            runner.register(self.directory)
        native.assert_called_once_with(self.directory)
        for name, token in (("admin", "private-admin-token"), ("a", "private-reader-token")):
            path = self.private / "headers" / (name + ".header")
            self.assertEqual(path.read_text(), "Authorization: Bearer " + token + "\n")
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        self.assertEqual((self.private / "headers").stat().st_mode & 0o777, 0o700)
        self.assertEqual(out.getvalue(), "")

    def test_connection_info_contains_only_owned_urls_and_paths(self):
        with patch.object(runner, "compose", return_value=Result(0, "127.0.0.1:12345\n", "")) as compose:
            info = runner.connection_info(self.directory)
        self.assertEqual(info["adminUrl"], "https://127.0.0.1:12345/v1/admin/")
        self.assertEqual(info["servers"]["a"]["url"], "https://contextforge:4444/servers/" + "a" * 32 + "/mcp")
        self.assertEqual(info["servers"]["a"]["headerFile"], str(self.private / "headers/a.header"))
        self.assertEqual(compose.call_args.args[1:], ("port", "federation-fixture", "8443"))
        for secret in ("private-admin-token", "private-reader-token", "password-sentinel"):
            self.assertNotIn(secret, json.dumps(info))

    def test_connection_info_refuses_nonloopback_or_invalid_published_ports(self):
        for value in ("0.0.0.0:12345", "[::]:12345", "127.0.0.1:0", "127.0.0.1:65536", "127.0.0.1:12345\n0.0.0.0:12345"):
            with self.subTest(value=value), patch.object(runner, "compose", return_value=Result(0, value, "")), self.assertRaises(AssertionError):
                runner.connection_info(self.directory)

    def test_stopped_fixture_refuses_inspection_without_restarting_it(self):
        self.state["cleanup"] = True
        runner.json_file(self.directory / "owner.json", self.state)
        with self.command("info", str(self.directory)), patch.object(runner, "compose") as compose, self.assertRaises(AssertionError):
            runner.main()
        compose.assert_not_called()

    def test_browser_refuses_a_different_node_before_loading_private_inputs(self):
        with patch.object(runner, "run", return_value=Result(0, "v0.0.0", "")) as run, patch.object(runner, "connection_info") as info, self.assertRaises(AssertionError):
            runner.browser(self.directory)
        info.assert_not_called()
        self.assertEqual(run.call_count, 1)
        self.assertEqual(run.call_args.args[0], ["node", "--version"])

    def test_documented_example_is_executed_unchanged_in_the_owned_client(self):
        guide = self.output / "guide.md"
        code = 'print("Echo: Hello")\n'
        guide.write_text("Before\n```sh\nprintf example\n```\n```python\n" + code + "```\nAfter\n")
        out = io.StringIO()
        with patch.object(runner, "GUIDE", guide), patch.object(runner, "compose", return_value=Result(0, "Echo: Hello\n", "")) as compose, redirect_stdout(out):
            self.assertEqual(runner.guide_echo(self.directory), {"echo": True})
        script = self.private / "client-state/guide-example.py"
        self.assertEqual(script.read_text(), code)
        self.assertEqual(script.stat().st_mode & 0o777, 0o600)
        self.assertEqual(compose.call_args.args, (self.directory, "exec", "--no-TTY", "client", "python", "-B", "/state/guide-example.py"))
        self.assertEqual(compose.call_args.kwargs["timeout"], 30)
        self.assertEqual(out.getvalue(), "")

    def test_failed_wrong_or_secret_reflecting_example_never_qualifies_handoff(self):
        guide = self.output / "guide.md"
        guide.write_text('```python\nprint("Echo: Hello")\n```\n')
        for result in (Result(1, "Echo: Hello\n", ""), Result(0, "another result\n", ""),
                       Result(0, "Echo: Hello\n", "password-sentinel"),
                       Result(0, "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJzZW50aW5lbCJ9.synthetic-signature", "")):
            with self.subTest(code=result.code), patch.object(runner, "GUIDE", guide), \
                 patch.object(runner, "compose", return_value=result), self.assertRaises(AssertionError):
                runner.guide_echo(self.directory)


class GuideSource(unittest.TestCase):
    def test_only_one_explicit_python_block_is_selected(self):
        code = "value = 1\n"
        self.assertEqual(runner.guide_source("Outside\n```sh\nprintf shell\n```\n```python\n" + code + "```\nAfter\n"), code)

    def test_missing_multiple_and_invalid_examples_refuse_before_execution(self):
        for text in ("No example", "```python\nx=1\n```\n```python\ny=2\n```\n", "```python\nif broken syntax\n```\n"):
            with self.subTest(text=text), self.assertRaises((AssertionError, SyntaxError)):
                runner.guide_source(text)


class InterruptionSignal(unittest.TestCase):
    def test_only_a_successful_proxied_tool_response_emits_a_fixed_signal(self):
        body = json.dumps({"method": "tools/call", "params": {"private": "argument-sentinel"}}).encode()
        self.assertEqual(proxy_progress("proxy-2025-11-25", "POST", body, 200), {"phase": "proxy-2025-11-25", "method": "tools/call", "httpStatus": 200})
        self.assertNotIn("argument-sentinel", str(proxy_progress("proxy-2026-07-28", "POST", body, 200)))
        for phase, method, content, status in (("native", "POST", body, 200), ("proxy-unknown", "POST", body, 200), ("proxy-2026-07-28", "GET", body, 200),
                                               ("proxy-2026-07-28", "POST", body, 401), ("proxy-2026-07-28", "POST", b'{"method":"initialize"}', 200),
                                               ("proxy-2026-07-28", "POST", b"[]", 200), ("proxy-2026-07-28", "POST", b"malformed", 200)):
            with self.subTest(phase=phase, method=method, status=status):
                self.assertIsNone(proxy_progress(phase, method, content, status))


if __name__ == "__main__":
    unittest.main()
