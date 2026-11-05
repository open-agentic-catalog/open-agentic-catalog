"""Scripted runtime outcomes; no MCP SDK, network, processes or databases."""

from contextlib import asynccontextmanager
from types import SimpleNamespace
import unittest
from unittest.mock import patch

from contextforge import handoff
from contextforge.handoff_checks import classify_prompt_failure


class MCPError(Exception):
    pass


class ConnectError(Exception):
    pass


def require(condition, message):
    if not condition:
        raise AssertionError(message)


def runtime(*, is_error=True, error=None, http_status=200, upstream_status=502, upstream_method="tools/call"):
    events = []
    suite = object.__new__(handoff.Handoff)
    suite.c = SimpleNamespace(require=require)
    suite.private = {"connections": {"a": {"token": "other-team-token"}}}

    async def observations():
        return list(events)

    async def call_tool(*_args, **_kwargs):
        if upstream_status is not None:
            events.append({"path": "everything", "status": upstream_status, "method": upstream_method})
        if error is not None:
            raise error
        return SimpleNamespace(is_error=is_error, content=[SimpleNamespace(text="wrong successful result")])

    @asynccontextmanager
    async def connect(*, headers=None, responses=None):
        if responses is not None and http_status is not None:
            responses.append(http_status)
        yield SimpleNamespace(call_tool=call_tool)

    suite.observations, suite.connect = observations, connect
    return suite


class RuntimeControls(unittest.IsolatedAsyncioTestCase):
    async def test_negative_tool_result_and_real_upstream_error_qualify_outage(self):
        with patch.object(handoff, "saved", return_value={"echo": "native-echo"}):
            result = await runtime().down("everything")
        self.assertEqual(result, {"boundedFailure": True, "service": "everything", "upstream502": True})

    async def test_successful_but_wrong_result_cannot_qualify_an_outage(self):
        # A separate failed health request must not hide a successful tool result.
        with patch.object(handoff, "saved", return_value={"echo": "native-echo"}), self.assertRaises(AssertionError):
            await runtime(is_error=False).down("everything")

    async def test_authentication_refusal_cannot_qualify_gateway_outage(self):
        with patch.object(handoff, "saved", return_value={"echo": "native-echo"}), self.assertRaises(AssertionError):
            await runtime(error=MCPError("authentication refused"), http_status=401, upstream_status=None).down("contextforge")

    async def test_client_refusals_cannot_qualify_upstream_outage(self):
        with patch.object(handoff, "saved", return_value={"echo": "native-echo"}):
            for status in (400, 401, 403, 404, 405, 429):
                with self.subTest(status=status), self.assertRaises(AssertionError):
                    await runtime(error=MCPError("client refused"), http_status=status).down("everything")

    async def test_background_error_cannot_qualify_forwarded_call_outage(self):
        with patch.object(handoff, "saved", return_value={"echo": "native-echo"}), self.assertRaises(AssertionError):
            await runtime(upstream_method="").down("everything")

    async def test_transport_failure_without_http_response_qualifies_gateway_outage(self):
        with patch.object(handoff, "saved", return_value={"echo": "native-echo"}):
            result = await runtime(error=ConnectError("connection refused"), http_status=None, upstream_status=None).down("contextforge")
        self.assertEqual(result, {"boundedFailure": True, "service": "contextforge", "upstream502": False})

    async def test_refusals_require_classified_errors_after_the_expected_status(self):
        def suite(error_type):
            result = runtime(upstream_status=None)

            @asynccontextmanager
            async def connect(*, headers, responses):
                responses.append(403 if headers else 401)
                async def call_tool(*_args, **_kwargs):
                    raise error_type("synthetic refusal")
                yield SimpleNamespace(call_tool=call_tool)

            result.connect = connect
            return result

        with patch.object(handoff, "saved", return_value={"echo": "native-echo"}):
            self.assertEqual(await suite(MCPError).refusals(), {"missing": 401, "wrongTeam": 403, "noInvocation": True})
            for error_type in (ValueError, AssertionError, TimeoutError):
                with self.subTest(error_type=error_type.__name__), self.assertRaises(AssertionError):
                    await suite(error_type).refusals()


class PromptCause(unittest.TestCase):
    def test_native_prompt_certificate_error_is_classified(self):
        prompt = {"status": "peer_limited", "cause": "unclassified"}
        logs = "Error getting prompt 'native-simple-prompt': [SSL: CERTIFICATE_VERIFY_FAILED] certificate verify failed"
        self.assertEqual(classify_prompt_failure(prompt, "native-simple-prompt", logs)["cause"], "private_ca_rejected")
        self.assertEqual(prompt["cause"], "unclassified")

    def test_unrelated_certificate_error_cannot_explain_a_prompt_failure(self):
        prompt = {"status": "peer_limited", "cause": "unclassified"}
        for logs in ("TLS handshake: certificate verify failed", "Error getting prompt 'another-prompt': CERTIFICATE_VERIFY_FAILED",
                     "Error getting prompt 'other-native-simple-prompt': CERTIFICATE_VERIFY_FAILED",
                     "native-simple-prompt: access denied\nOther connection: CERTIFICATE_VERIFY_FAILED"):
            with self.subTest(logs=logs), self.assertRaises(AssertionError):
                classify_prompt_failure(prompt, "native-simple-prompt", logs)

    def test_known_first_failure_and_prompt_success_need_no_tls_explanation(self):
        for prompt in ({"status": "passed"}, {"status": "peer_limited", "cause": "upstream_unauthorized"}):
            self.assertEqual(classify_prompt_failure(prompt, "native-simple-prompt", ""), prompt)


if __name__ == "__main__":
    unittest.main()
