"""Isolated ContextForge supervisor tests. No process, server or SDK execution."""

import base64
import copy
from contextlib import redirect_stdout
import io
import json
import os
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch

from common import Result
import contextforge_runner as runner
import contextforge_tools as tools
from contextforge.checks import token_times, tool_pages
from contextforge import proxy


class Inputs(unittest.TestCase):
    def test_proxy_summary_requires_every_protocol_and_admission_control(self):
        protocol = dict.fromkeys(("inventory", "schemas", "sixTools", "pagesAndFilters", "emptyResults", "exactReferences", "indexed", "facetsAndAccess"), True)
        protocol.update(upstreamProtocols=["2026-07-28"], closedArguments="tool_error", argumentValidation="gateway_or_client")
        value = {"protocols": [dict(protocol, protocol=p) for p in ("2025-11-25", "2026-07-28")],
                 "isolation": dict(dict.fromkeys(("twoTenants", "wrongTeam", "upstreamContact", "revokedCollections", "sameClientRestored"), True), revokedExactRead="tool_error"),
                 "credentials": dict(dict.fromkeys(("missingAndInvalidInbound", "host", "origin", "privateTLS", "expiredAudienceAndScope", "explicitOverride", "configuredIdentity"), True), upstreamDenials={"expired": 401, "audience": 401, "scope": 403}),
                 "lifecycle": dict.fromkeys(("sameUserSubset", "excludedWithoutUpstreamCall", "acceptedCredentialReplacement", "replacementData", "restored"), True), "metadataOnly": True}
        runner.validate_proxy(value)
        broken_values = []
        for key in value:
            changed = copy.deepcopy(value)
            del changed[key]
            broken_values.append(changed)
        for group, key, replacement in (("isolation", "sameClientRestored", False), ("isolation", "revokedExactRead", "successful_empty"),
                                        ("credentials", "upstreamDenials", {"expired": 401, "audience": 401, "scope": 200}), ("lifecycle", "acceptedCredentialReplacement", False)):
            changed = copy.deepcopy(value)
            changed[group][key] = replacement
            broken_values.append(changed)
        for index in (0, 1):
            for key, replacement in (("protocol", "2026-07-28" if index == 0 else "2025-11-25"), ("schemas", False), ("upstreamProtocols", []),
                                     ("upstreamProtocols", ["unknown"]), ("closedArguments", "passed"), ("argumentValidation", "unknown")):
                changed = copy.deepcopy(value)
                changed["protocols"][index][key] = replacement
                broken_values.append(changed)
        for changed in broken_values:
            with self.subTest(changed=changed), self.assertRaises((AssertionError, KeyError)):
                runner.validate_proxy(changed)

    def test_native_summary_rejects_missing_controls_wrong_bytes_and_extra_inventory(self):
        value = {"catalog": {"tools": ["catalog_control_" + x for x in ("capabilities", "search", "release", "indexed_record", "explore", "access")],
                             "protocol": "2026-07-28", "capabilitiesAndAccess": True},
                 "everything": {"protocol": "2025-11-25", "version": "2.0.0", "echo": True, "resource": True, "prompt": True,
                                "resourceSha256": "36593c6d475378b29c6c43a3256fbfd2cad7b087dcbd3e940d53fa0876a70cd7"},
                 "contextforge": {"version": "1.1.0", "adminUI": True, "nativeTeamRolesAndServerScopedTokens": True, "scopedRuntimeReadAndWriteDenial": True,
                                  "nativeRegistration": {"a": {"tools": 6, "resources": 0, "prompts": 0}, "b": {"tools": 6, "resources": 0, "prompts": 0},
                                                         "everything": {"tools": 1, "resources": 1, "prompts": 1}}},
                 "refusals": dict.fromkeys(("missingCatalogToken", "missingEverythingKey", "missingContextForgeToken", "untrustedCA", "invalidContextForgeToken", "httpFailureDiagnostic"), True)}
        runner.validate_native(value)
        for group, key, replacement in (("catalog", "tools", []), ("catalog", "tools", value["catalog"]["tools"] + value["catalog"]["tools"][:1]),
                                        ("everything", "prompt", False), ("everything", "resourceSha256", "0" * 64),
                                        ("catalog", "protocol", "2025-11-25"), ("contextforge", "scopedRuntimeReadAndWriteDenial", False),
                                        ("contextforge", "nativeRegistration", {}), ("refusals", "untrustedCA", False)):
            broken = copy.deepcopy(value)
            broken[group][key] = replacement
            with self.subTest(group=group, key=key), self.assertRaises(AssertionError):
                runner.validate_native(broken)
        with self.assertRaises(AssertionError):
            runner.validate_native({})

    def test_linux_manifest_is_bound_to_the_reviewed_index(self):
        pin = {"manifest": "sha256:expected"}
        manifest = {"digest": "sha256:expected", "platform": {"os": "linux", "architecture": "amd64"}}
        tools.validate_index(pin, {"manifests": [manifest, {"digest": "arm64", "platform": {"os": "linux", "architecture": "arm64"}}]})
        for values in ([], [manifest, manifest], [dict(manifest, digest="sha256:changed")], [dict(manifest, platform={"os": "windows", "architecture": "amd64"})]):
            with self.subTest(values=values), self.assertRaises(AssertionError):
                tools.validate_index(pin, {"manifests": values})

    def test_latest_stable_must_match_the_reviewed_pin_without_an_age_wait(self):
        pin = {"tag": "v1.1.0"}
        tools.validate_release(pin, {"tag_name": "v1.1.0", "draft": False, "prerelease": False, "published_at": "2099-01-01"})
        for release in ({"tag_name": "v1.1.1", "draft": False, "prerelease": False},
                        {"tag_name": "v1.1.0", "draft": True, "prerelease": False},
                        {"tag_name": "v1.1.0", "draft": False, "prerelease": True}):
            with self.subTest(release=release), self.assertRaises(AssertionError):
                tools.validate_release(pin, release)

    def test_missing_install_fails_without_download_or_process(self):
        with tempfile.TemporaryDirectory() as temp, patch.object(tools, "STAMP", Path(temp) / "absent"), patch.object(tools, "run") as run, patch.object(tools, "docker") as docker:
            with self.assertRaisesRegex(RuntimeError, "contextforge-tools-install"):
                tools.installed()
            run.assert_not_called()
            docker.assert_not_called()

    def test_environment_excludes_workflow_secrets_and_compose_or_proxy_selectors(self):
        ambient = {"HOME": "/home/test", "PATH": "/bin", "COMPOSE_FILE": "retained.yaml", "COMPOSE_PROJECT_NAME": "retained",
                   "CATALOG_ENV_FILE": "/private/retained.env", "HTTPS_PROXY": "https://proxy.invalid", "SECRET": "secret"}
        with patch.dict(os.environ, ambient, clear=True):
            result = runner.environment()
        self.assertEqual(result, {"HOME": "/home/test", "PATH": "/bin", "CATALOG_ENV_FILE": "/dev/null", "DOCKER_CLI_HINTS": "false"})


class Ownership(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.output = Path(self.temporary.name)
        self.directory = self.output / "catalog-contextforge-012345abcdef"
        self.directory.mkdir()
        self.state = {"project": self.directory.name, "directory": str(self.directory.resolve()), "retained": ["retained-container"], "cleanup": False, "success": True}
        runner.json_file(self.directory / "owner.json", self.state)
        self.scope = patch.object(tools, "OUTPUT", self.output)
        self.scope.start()
        self.addCleanup(self.scope.stop)

    def test_compose_selects_only_the_exact_owned_project_and_private_inputs(self):
        command = runner.compose_args(self.directory, "up", "--pull", "never")
        self.assertEqual(command, ["docker", "compose", "--project-directory", str(self.directory / "private"), "--env-file", str(self.directory / "compose.env"),
                                   "--project-name", self.directory.name, "--file", str(tools.INPUTS / "compose.yaml"), "up", "--pull", "never"])

    def test_native_checkpoint_cannot_report_success_before_proxy_checks(self):
        self.state.update(phase="started", success=False, source="frozen", builds={})
        runner.json_file(self.directory / "owner.json", self.state)
        with patch.object(runner, "client_command", return_value={"native": True}), patch.object(runner, "validate_native"), patch.object(runner, "fingerprint", return_value="frozen"):
            runner.native(self.directory)
        report = json.loads((self.directory / "report.json").read_text())
        self.assertEqual(report["phase"], "native-controls-complete")
        self.assertEqual(report["cases"], {"native": True})
        self.assertFalse(report["success"], "the proxy and recovery checks have not run")

    def test_proxy_checkpoint_cannot_report_success_before_governed_handoff(self):
        self.state.update(phase="native-controls-complete", success=False, source="frozen")
        runner.json_file(self.directory / "owner.json", self.state)
        stages = [{"aLocal": 2, "bLocal": 1, "indexed": 1, "independentReview": True}, {"proxy": True}, {"forwardedRead": True},
                  {"boundedFailure": True, "translation": "tool_error", "upstream502": True}, {"forwardedRead": True}]
        with patch.object(runner, "client_command", side_effect=stages), patch.object(runner, "validate_proxy"), patch.object(runner, "compose"), patch.object(runner, "fingerprint", return_value="frozen"):
            runner.proxy(self.directory)
        report = json.loads((self.directory / "report.json").read_text())
        self.assertEqual(report["phase"], "catalog-proxy-complete")
        self.assertFalse(report["success"], "governed runtime handoff and restart controls have not run")

    def test_handoff_cli_reads_have_fixed_origin_private_trust_and_no_database_inputs(self):
        private = self.directory / "private"
        private.mkdir()
        runner.json_file(private / "secrets.json", {"tokens": {"reader": "catalog-token-sentinel"}})
        with patch.object(runner, "compose", return_value=Result(0, "{}", "")) as compose:
            runner.cli_result(self.directory, "record", "--id", "selected-release")
        arguments = compose.call_args.args[1:]
        self.assertEqual(arguments[:6], ("--progress", "quiet", "run", "--rm", "--no-deps", "cli"))
        self.assertEqual(arguments[6:], ("remote", "record", "--base-url", "https://federation-fixture:8443", "--ca-file", "/client/ca.pem", "--token-file", "/client/reader-token", "--timeout", "10s", "--id", "selected-release"))
        self.assertNotIn("catalog-token-sentinel", str(arguments))
        with patch.object(runner, "compose") as compose:
            with self.assertRaises(AssertionError):
                runner.cli_result(self.directory, "record", actor="../../private")
            compose.assert_not_called()

    def test_failed_diagnostics_preserve_original_failure_and_clear_success(self):
        for original in (ValueError("synthetic operation failure"), KeyboardInterrupt("synthetic interruption")):
            with self.subTest(original=type(original).__name__):
                runner.json_file(self.directory / "owner.json", self.state)
                with patch.object(runner.sys, "argv", ["contextforge_runner.py", "check"]), patch.object(runner, "prepare", return_value=self.directory), \
                     patch.object(runner, "start", side_effect=original), patch.object(runner, "compose", side_effect=TimeoutError("synthetic log failure")), \
                     patch.object(runner, "stop") as stop, redirect_stdout(io.StringIO()), self.assertRaises(type(original)) as caught:
                    runner.main()
                self.assertIs(caught.exception, original)
                stop.assert_called_once_with(self.directory)
                report = runner.owner(self.directory)
                self.assertFalse(report["success"])
                self.assertEqual(report["errorType"], type(original).__name__)
                self.assertEqual(report["diagnosticFailure"], "TimeoutError")

    def test_native_probe_keeps_credentials_on_stdin_and_preserves_failure(self):
        private = self.directory / "private"
        state = private / "client-state"
        state.mkdir(parents=True)
        token = diagnostic_token({"iat": 1700000000, "sub": "private-identity"})
        (state / "native-failure-token").write_text(token)
        runner.json_file(private / "secrets.json", {"tokens": {"reader": "catalog-token-sentinel"}})
        result = SimpleNamespace(returncode=0, stdout='{"verification":"passed","times":{"iat":1700000000},"subjectExists":true}')
        original = ValueError("synthetic operation failure")
        with patch.object(runner.sys, "argv", ["contextforge_runner.py", "check"]), patch.object(runner, "prepare", return_value=self.directory), \
             patch.object(runner, "start", side_effect=original), patch.object(runner, "subprocess") as subprocess, \
             patch.object(runner, "compose", return_value=Result(0, "catalog-token-sentinel", "")), patch.object(runner, "stop"), \
             redirect_stdout(io.StringIO()), self.assertRaises(ValueError) as caught:
            subprocess.run.return_value = result
            runner.main()
        self.assertIs(caught.exception, original)
        command = subprocess.run.call_args.args[0]
        self.assertNotIn(token, " ".join(command))
        self.assertEqual(subprocess.run.call_args.kwargs["input"], token)
        compile(command[-1], "<native-authentication-probe>", "exec")
        report = runner.owner(self.directory)
        self.assertFalse(report["success"])
        self.assertEqual(report["nativeAuthenticationProbe"]["verification"], "passed")
        self.assertNotIn("catalog-token-sentinel", (self.directory / "failure.log").read_text())

    def test_cleanup_rejects_unowned_paths_and_symlinks_before_execution(self):
        link = self.output / "catalog-contextforge-aaaaaaaaaaaa"
        link.symlink_to(self.directory, target_is_directory=True)
        with patch.object(runner, "compose") as compose, patch.object(runner, "docker") as docker:
            for directory in (self.output, self.output / "retained", self.output / ".." / self.directory.name, link):
                with self.subTest(directory=directory), self.assertRaises(ValueError):
                    runner.stop(directory)
            compose.assert_not_called()
            docker.assert_not_called()

    def test_cleanup_requires_empty_owned_resources_and_retained_containers(self):
        private = self.directory / "private"
        private.mkdir()
        (private / "synthetic-secret").write_text("secret")
        def docker(*arguments):
            if arguments == ("ps", "-q"):
                return Result(0, "retained-container\n", "")
            self.assertIn("label=com.docker.compose.project=" + self.directory.name, arguments)
            return Result(0, "", "")
        with patch.object(runner, "compose", return_value=Result(0, "", "")) as compose, patch.object(runner, "docker", side_effect=docker):
            runner.stop(self.directory)
            runner.stop(self.directory)
        self.assertFalse(private.exists())
        report = json.loads((self.directory / "report.json").read_text())
        self.assertTrue(report["success"] and report["cleanup"] and report["privateRemoved"])
        self.assertEqual(compose.call_args.args[1:], ("down", "--volumes", "--remove-orphans", "--timeout", "20"))
        self.assertEqual(compose.call_count, 1)

    def test_incomplete_teardown_never_reports_success(self):
        (self.directory / "private").mkdir()
        with patch.object(runner, "compose", return_value=Result(1, "", "")), patch.object(runner, "docker") as docker, self.assertRaisesRegex(AssertionError, "teardown"):
            runner.stop(self.directory)
        docker.assert_not_called()
        report = json.loads((self.directory / "report.json").read_text())
        self.assertFalse(report["success"] or report["cleanup"])
        self.assertTrue((self.directory / "private").exists(), "keep exact Compose inputs for a scoped teardown retry")

    def test_failed_cleanup_retry_discards_previous_success_flags(self):
        self.state.update(cleanup=True, resourcesRemoved=True, privateRemoved=True)
        runner.json_file(self.directory / "owner.json", self.state)
        private = self.directory / "private"
        private.mkdir()
        with patch.object(runner, "compose", return_value=Result(1, "", "")), patch.object(runner, "docker"), self.assertRaisesRegex(AssertionError, "teardown"):
            runner.stop(self.directory)
        report = json.loads((self.directory / "report.json").read_text())
        self.assertFalse(report["cleanup"] or report["success"] or report["resourcesRemoved"])
        self.assertTrue(private.exists(), "a stale success flag must not remove live-service inputs")

    def test_leftover_owned_resources_keep_private_inputs_for_recovery(self):
        self.state.update(cleanup=True, resourcesRemoved=True)
        runner.json_file(self.directory / "owner.json", self.state)
        private = self.directory / "private"
        private.mkdir()
        with patch.object(runner, "compose", return_value=Result(0, "", "")), patch.object(runner, "docker", return_value=Result(0, "leftover-container", "")), self.assertRaisesRegex(AssertionError, "container cleanup"):
            runner.stop(self.directory)
        report = json.loads((self.directory / "report.json").read_text())
        self.assertFalse(report["cleanup"] or report["success"] or report["resourcesRemoved"])
        self.assertTrue(private.exists())

    def test_client_output_rejects_known_credentials_and_peer_jwts_before_logging(self):
        private = self.directory / "private"
        private.mkdir()
        runner.json_file(private / "secrets.json", {"tokens": {"reader": "catalog-token-sentinel"}, "everythingKey": "everything-key-sentinel"})
        with patch.object(runner, "compose", return_value=Result(0, '{"native":true}', "")):
            self.assertEqual(runner.client_command(self.directory, "native"), {"native": True})
        for secret in ("catalog-token-sentinel", "everything-key-sentinel", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJzeW50aGV0aWMifQ.synthetic-signature"):
            with self.subTest(secret=secret), patch.object(runner, "compose", return_value=Result(1, "", "reflected " + secret)), self.assertRaisesRegex(AssertionError, "credential"):
                runner.client_command(self.directory, "native")
            self.assertFalse((self.directory / "client-error.log").exists())


def diagnostic_token(claims):
    payload = base64.urlsafe_b64encode(json.dumps(claims).encode()).decode().rstrip("=")
    return "header." + payload + ".signature"


class NativeDiagnostics(unittest.TestCase):
    def test_diagnostic_dates_exclude_identity_text_booleans_and_nonfinite_numbers(self):
        self.assertEqual(token_times(diagnostic_token({"iat": 1700000000, "exp": 1700003600.5, "sub": "private-identity", "scope": "private-scope"})),
                         {"iat": 1700000000, "exp": 1700003600.5})
        for claims in ({"iat": "private-sentinel", "exp": True, "nbf": {"secret": "private-sentinel"}},
                       {"iat": float("nan"), "exp": float("inf"), "nbf": float("-inf")}):
            self.assertEqual(token_times(diagnostic_token(claims)), {})

    def test_opaque_and_malformed_tokens_cannot_mask_the_original_http_failure(self):
        for token in (None, "", "opaque-token-sentinel", "too.many.parts.here", "h.%%%.s", "h._w.s",
                      diagnostic_token([]), diagnostic_token("private-sentinel"), "h." + "A" * 16384 + ".s"):
            with self.subTest(token_type=type(token).__name__):
                self.assertEqual(token_times(token), {})


class PagedTools(unittest.IsolatedAsyncioTestCase):
    @staticmethod
    def client(pages):
        class Client:
            def __init__(self):
                self.calls = []
                self.pages = iter(pages)

            async def list_tools(self, *, cursor):
                self.calls.append(cursor)
                return next(self.pages)
        return Client()

    @staticmethod
    def page(names, cursor=None):
        return SimpleNamespace(tools=[SimpleNamespace(name=name) for name in names], next_cursor=cursor)

    async def test_complete_tool_discovery_follows_each_cursor_without_reordering(self):
        client = self.client([self.page(["a", "b"], "page-2"), self.page(["c", "d"], "page-3"), self.page(["e", "f"])])
        self.assertEqual([tool.name for tool in await tool_pages(client)], ["a", "b", "c", "d", "e", "f"])
        self.assertEqual(client.calls, [None, "page-2", "page-3"])

    async def test_repeated_cursor_or_tool_fails_instead_of_reporting_partial_success(self):
        for pages, message in (([self.page(["a"], "repeat"), self.page(["b"], "repeat")], "cursor"),
                               ([self.page(["a"], "next"), self.page(["a"])], "unique tools")):
            with self.subTest(message=message), self.assertRaisesRegex(AssertionError, message):
                await tool_pages(self.client(pages))

    async def test_page_bound_stops_further_requests(self):
        client = self.client([self.page([str(i)], "page-" + str(i)) for i in range(9)])
        with self.assertRaisesRegex(AssertionError, "page bound"):
            await tool_pages(client)
        self.assertEqual(len(client.calls), 8)

    async def test_gateway_must_apply_the_configured_page_size(self):
        with self.assertRaisesRegex(AssertionError, "page size"):
            await tool_pages(self.client([self.page(["a", "b", "c"])]))

    async def test_failed_observations_preserve_the_original_proxy_failure(self):
        original = ValueError("synthetic proxy failure")
        context = SimpleNamespace(CASES={})
        observer = SimpleNamespace(observations=AsyncMock(side_effect=TimeoutError("synthetic observation failure")))
        with patch.object(proxy, "execute", new=AsyncMock(side_effect=original)), patch.object(proxy, "Proxy", return_value=observer), self.assertRaises(ValueError) as caught:
            await proxy.run(context, "proxy")
        self.assertIs(caught.exception, original)
        self.assertEqual(context.CASES, {"observationFailure": "TimeoutError"})


if __name__ == "__main__":
    unittest.main()
