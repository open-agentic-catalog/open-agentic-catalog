"""No listeners, subprocesses, DNS or real network in this selection."""
import asyncio
import json
from pathlib import Path
import re
import unittest
from unittest.mock import AsyncMock, Mock, patch
from uuid import UUID

import httpx2 as httpx
from pydantic import ValidationError

from catalog_sdk import Client, Config, SDKError, generated, response_limit
from catalog_sdk.generated.api.service_api import ServiceApi
from catalog_sdk.generated.api.identity_api import IdentityApi
from catalog_sdk.generated.api.discovery_api import DiscoveryApi
from catalog_sdk.generated.api.catalog_discovery_api import CatalogDiscoveryApi
from catalog_sdk.generated.api.personal_workspace_api import PersonalWorkspaceApi
from catalog_sdk.generated.models.catalog_search import CatalogSearch
from catalog_sdk.generated.models.trust_assessment_request import TrustAssessmentRequest
from catalog_sdk.transport import HTTPBoundary, Response


class Stream(httpx.AsyncByteStream):
    def __init__(self, chunks):
        self.chunks = chunks
        self.closed = False

    async def __aiter__(self):
        for value in self.chunks:
            yield value

    async def aclose(self):
        self.closed = True


def injected(boundary, handler):
    return patch.object(boundary, "_client", lambda: httpx.AsyncClient(
        transport=httpx.MockTransport(handler), trust_env=False, follow_redirects=False))


class ReadmeExampleUnits(unittest.IsolatedAsyncioTestCase):
    """Execute the documented recipes with an in-memory HTTP transport only."""

    @classmethod
    def setUpClass(cls):
        readme = Path(__file__).resolve().parents[4] / "sdk/python/README.md"
        cls.examples = {"__name__": "readme_examples"}
        helpers = ("require_status", "search_local_pages", "show_local_results", "read_local_record", "create_draft_resource", "read_job_state")
        blocks = re.findall(r"^```python\n(.*?)^```", readme.read_text(), re.MULTILINE | re.DOTALL)
        for block in blocks:
            compiled = compile(block, str(readme), "exec")
            if any(re.search(rf"^(?:async )?def {name}\(", block, re.MULTILINE) for name in helpers):
                exec(compiled, cls.examples)
        for name in helpers:
            if not callable(cls.examples.get(name)):
                raise AssertionError(f"Missing Python README helper: {name}")

    def setUp(self):
        self.client = Client(Config(origin="https://catalog.example.com", token="selected"))
        self.uid = UUID("00000000-0000-7000-8000-000000000001")

    async def test_search_keeps_filters_empty_page_cursor_partial_and_page_budget(self):
        calls = []
        def handler(request):
            self.assertEqual(request.method, "POST")
            self.assertEqual(request.url.path, "/api/v1/catalog/search")
            self.assertEqual(request.headers["Authorization"], "Bearer selected")
            calls.append(json.loads(request.content))
            page = {"items":[], "durableTotal":0, "liveCount":0, "profile":"core-lexical-v1", "generation":"sha256:" + "a" * 64, "tier":"core", "partial":len(calls) == 1, "diagnostics":[]}
            if len(calls) == 1:
                page["nextCursor"] = "opaque=continuation"
            return httpx.Response(200, headers={"Content-Type":"application/json"}, stream=Stream([json.dumps(page).encode()]))
        search = self.examples["search_local_pages"]
        with injected(self.client.rest_client, handler):
            pages = [page async for page in search(self.client, "weather", namespace_id=self.uid)]
        self.assertEqual(len(pages), 2)
        self.assertTrue(pages[0].partial)
        self.assertFalse(pages[1].partial)
        self.assertIsNone(pages[1].next_cursor)
        base = {"text":"weather", "origins":["local"], "namespaceId":str(self.uid), "limit":20}
        self.assertEqual(calls, [base, dict(base, cursor="opaque=continuation")])
        calls.clear()
        with injected(self.client.rest_client, handler):
            pages = [page async for page in search(self.client, "weather", namespace_id=self.uid, cursor="retained=cursor", max_pages=1)]
        self.assertEqual(calls, [dict(base, cursor="retained=cursor")])
        self.assertEqual(pages[0].next_cursor, "opaque=continuation")
        calls.clear()
        with injected(self.client.rest_client, handler), patch("builtins.print") as display:
            cursor = await self.examples["show_local_results"](self.client)
        self.assertIsNone(cursor)
        self.assertEqual(len(calls), 2)
        display.assert_called_once_with("Search results are incomplete")
        forbidden = Mock(side_effect=AssertionError("must not send"))
        with injected(self.client.rest_client, forbidden), self.assertRaises(ValueError):
            _ = [page async for page in search(self.client, "weather", max_pages=0)]
        forbidden.assert_not_called()

    async def test_draft_recipe_serializes_each_kind_and_retains_key_and_etag(self):
        create = self.examples["create_draft_resource"]
        for kind in ("agent", "mcp_server", "skill", "plugin"):
            identifier = "urn:air:example.com:examples:" + kind
            key = "retained-draft-" + kind
            expected = {"namespaceId":str(self.uid), "canonicalIdentifier":identifier, "kind":kind}
            response = {"id":str(self.uid), "tenantId":str(self.uid), **expected, "createdBy":"selected", "revision":1, "createdAt":"2026-10-14T12:00:00Z", "updatedAt":"2026-10-14T12:00:00Z"}
            calls = []
            def handler(request):
                calls.append(request)
                self.assertEqual(request.method, "POST")
                self.assertEqual(request.url.path, "/api/v1/resources")
                self.assertEqual(request.headers["Idempotency-Key"], key)
                self.assertEqual(json.loads(request.content), expected)
                return httpx.Response(201, headers={"ETag":'"rev-1"', "Content-Type":"application/json"}, stream=Stream([json.dumps(response).encode()]))
            with self.subTest(kind=kind), injected(self.client.rest_client, handler):
                result = await create(self.client, self.uid, identifier, kind, key)
                self.assertEqual(len(calls), 1)
                self.assertEqual(result.data.id, self.uid)
                self.assertEqual(result.data.kind.value, kind)
                self.assertEqual(result.headers["ETag"], '"rev-1"')

    async def test_record_recipe_refuses_external_origins_and_reports_safe_http_failure(self):
        read = self.examples["read_local_record"]
        forbidden = Mock(side_effect=AssertionError("must not send"))
        with injected(self.client.rest_client, forbidden):
            for origin in ("indexed", "live"):
                with self.subTest(origin=origin), self.assertRaises(ValueError):
                    await read(self.client, {"type":origin})
        forbidden.assert_not_called()
        calls = []
        def handler(request):
            calls.append(request)
            self.assertEqual(request.method, "GET")
            self.assertEqual(request.url.path, f"/api/v1/catalog/resources/{self.uid}")
            problem = {"type":"about:blank", "title":"Not found", "status":404, "code":"NOT_FOUND", "detail":"PRIVATE_BODY", "requestId":"correlation"}
            return httpx.Response(404, headers={"Content-Type":"application/problem+json", "X-Request-ID":"correlation"}, stream=Stream([json.dumps(problem).encode()]))
        local = {"type":"local", "id":"local:not-a-record-uuid", "identifier":"urn:air:example.com:examples:item", "local":{"release":{"id":str(self.uid)}}}
        with injected(self.client.rest_client, handler), self.assertRaises(RuntimeError) as caught:
            await read(self.client, local)
        self.assertEqual(str(caught.exception), "Catalog returned HTTP 404; code=NOT_FOUND")
        self.assertNotIn("PRIVATE_BODY", str(caught.exception))
        self.assertEqual(len(calls), 1)

    async def test_job_recipe_reads_one_current_state_and_does_not_poll(self):
        calls = []
        job = {"id":str(self.uid), "tenantId":str(self.uid), "kind":"source_sync", "state":"waiting", "priority":0, "progress":0, "notBefore":"2026-10-14T12:00:00Z", "attempt":1, "attemptLimit":3, "cancellationRequested":False, "createdAt":"2026-10-14T12:00:00Z", "updatedAt":"2026-10-14T12:00:00Z"}
        def handler(request):
            calls.append(request)
            self.assertEqual(request.method, "GET")
            self.assertEqual(request.url.path, f"/api/v1/jobs/{self.uid}")
            return httpx.Response(200, headers={"Content-Type":"application/json"}, stream=Stream([json.dumps(job).encode()]))
        with injected(self.client.rest_client, handler):
            state = await self.examples["read_job_state"](self.client, self.uid)
        self.assertEqual(state, "waiting")
        self.assertEqual(len(calls), 1)


class ConfigurationUnits(unittest.TestCase):
    def test_configuration_and_secret_safe_repr(self):
        for origin in ("http://localhost", "http://example.com", "ftp://example.com", "https://user:secret@example.com", "https://example.com/path", "https://example.com?x=secret", "https://example.com#secret", "https://example.com?", "https://example.com#", "https://example.com:0", "https://example.com:65536", "https://example.com\\evil", "https://example.com\n"):
            with self.subTest(origin=origin):
                with self.assertRaises(SDKError) as caught:
                    Config(origin=origin, allow_loopback_http=True)
                self.assertEqual(caught.exception.code, "invalid_configuration")
        for override in ({"timeout": 0}, {"timeout": float("inf")}, {"timeout": True}, {"phase_timeout": -1}, {"request_bytes": 0}, {"response_bytes": 1.2}, {"header_bytes": True}, {"token": "secret", "token_provider": lambda: "secret"}):
            with self.subTest(override=override), self.assertRaises(SDKError):
                Config(origin="https://catalog.example.com", **override)
        for origin in ("http://127.0.0.1:8080", "http://[::1]:8080"):
            self.assertEqual(Config(origin=origin, allow_loopback_http=True).origin, origin)
            for flag in (False, "false", "true", 1):
                with self.subTest(origin=origin, allow_loopback_http=flag):
                    with self.assertRaises(SDKError) as caught:
                        Config(origin=origin, allow_loopback_http=flag)
                    self.assertEqual(caught.exception.code, "invalid_configuration")
        self.assertNotIn("secret", repr(Config(origin="https://catalog.example.com", token="secret")))
        with self.assertRaises(SDKError) as caught:
            HTTPBoundary(Config(origin="https://catalog.example.com", ca_file="/missing/secret"))
        self.assertEqual(caught.exception.code, "invalid_tls_configuration")
        self.assertNotIn("secret", str(caught.exception))

    def test_sync_bridge_has_no_background_loop(self):
        client = Client(Config(origin="https://catalog.example.com"))
        async def run():
            with self.assertRaises(SDKError) as caught:
                client.raw_sync("GET", "/")
            self.assertEqual(caught.exception.code, "use_async_method")
        asyncio.run(run())

    def test_managed_construction_content_type_and_safe_serialization(self):
        from catalog_sdk.generated.api_client import ApiClient
        with self.assertRaises(SDKError) as caught:
            ApiClient()
        self.assertEqual(caught.exception.code, "managed_client_required")
        client = Client(Config(origin="https://catalog.example.com"))
        self.assertEqual(client.select_header_content_type(["application/merge-patch+json", "application/json"]), "application/json")
        self.assertEqual(client.select_header_content_type(["application/octet-stream"]), "application/octet-stream")
        with patch.object(ApiClient, "param_serialize", side_effect=ValueError("secret")), self.assertRaises(SDKError) as caught:
            client.param_serialize()
        self.assertEqual(str(caught.exception), "Catalog SDK: invalid_request")


class TransportUnits(unittest.IsolatedAsyncioTestCase):
    async def test_personal_label_query_preserves_literal_value_and_omission(self):
        client = Client(Config(origin="https://catalog.example.com", token="selected"))
        calls = []
        def handler(request):
            calls.append(request)
            self.assertEqual(request.method, "GET")
            self.assertEqual(request.url.path, "/api/v1/personal/items")
            return httpx.Response(200, headers={"Content-Type":"application/json"}, stream=Stream([b'{"items":[]}']))
        with injected(client.rest_client, handler):
            for label in (None, " Case é%_ "):
                result = await PersonalWorkspaceApi(client).list_personal_items(label=label)
                self.assertEqual(result.items, [])
                self.assertEqual("label" in calls[-1].url.params, label is not None)
                if label is not None:
                    self.assertEqual(calls[-1].url.params["label"], label)
        self.assertEqual(len(calls), 2)

    def setUp(self):
        self.client = Client(Config(origin="https://catalog.example.com", token="selected", response_bytes=32, request_bytes=256))
        self.boundary = self.client.rest_client

    async def test_generated_calls_report_package_version(self):
        for version in ("1.2.3", "1.2.3rc2"):
            with self.subTest(version=version), patch.object(generated, "__version__", version):
                client = Client(Config(origin="https://catalog.example.com"))
                transport = Mock(return_value=httpx.Response(200, headers={"Content-Type": "text/plain"}, stream=Stream([b"metric 1\n"])))
                with injected(client.rest_client, transport):
                    result = await ServiceApi(client).get_metrics_without_preload_content()
                self.assertEqual(result.data, b"metric 1\n")
                transport.assert_called_once()
                self.assertEqual(transport.call_args.args[0].headers["User-Agent"], "open-agentic-catalog-python/" + version)

    async def test_origin_guards_precede_credentials(self):
        provider = unittest.mock.Mock(return_value="secret")
        boundary = HTTPBoundary(Config(origin="https://catalog.example.com", token_provider=provider))
        handler = Mock(side_effect=AssertionError("must not send"))
        with injected(boundary, handler):
            for target in ("https://elsewhere.example/", "http://catalog.example.com/", "https://catalog.example.com:4433/", "https://u:p@catalog.example.com/", "https://catalog.example.com/#fragment"):
                with self.subTest(target=target), self.assertRaises(SDKError) as caught:
                    await boundary.request("POST", target, body=b"secret")
                self.assertEqual(caught.exception.code, "origin_mismatch")
        handler.assert_not_called()
        provider.assert_not_called()
        with injected(self.boundary, handler):
            for path in ("//elsewhere.example/", "https://elsewhere.example", "\\evil"):
                with self.assertRaises(SDKError) as caught:
                    await self.client.raw("GET", path)
                self.assertEqual(caught.exception.code, "invalid_path")
        handler.assert_not_called()
        with injected(self.boundary, handler), self.assertRaises(SDKError) as caught:
            await self.client.raw("POST", "/", body={"secret":"not raw bytes"})
        self.assertEqual(caught.exception.code, "invalid_raw_body")
        handler.assert_not_called()

    async def test_exact_raw_input_single_call_and_header_binding(self):
        raw = b'{"n":9007199254740993,"d":0.1234567890123456789,"x":[null,false,""]}'
        calls = []
        def handler(request):
            calls.append(request)
            self.assertEqual(request.content, raw)
            self.assertEqual(request.headers["Authorization"], "Bearer selected")
            self.assertNotIn("cookie", request.headers)
            self.assertNotIn("proxy-authorization", request.headers)
            self.assertEqual(request.headers["Accept-Encoding"], "identity")
            self.assertEqual(request.headers["Host"], "catalog.example.com")
            self.assertEqual(request.headers["Idempotency-Key"], "unchanged")
            self.assertEqual(request.headers["If-Match"], '"revision"')
            return httpx.Response(503, headers={"Retry-After": "5", "X-Request-ID": "safe"}, stream=Stream([b"SECRET_BODY"]))
        headers = {"authorization": "wrong", "Cookie": "secret", "Proxy-Authorization": "secret", "Host": "other", "Accept-Encoding": "gzip", "Idempotency-Key": "unchanged", "If-Match": '"revision"'}
        with injected(self.boundary, handler):
            with response_limit(128):
                result = await self.client.raw("POST", "/api/v1/resources", body=raw, headers=headers)
        self.assertEqual(len(calls), 1)
        self.assertEqual(result.status, 503)
        self.assertEqual(result.data, b"SECRET_BODY")
        self.assertEqual(result.headers["Retry-After"], "5")
        self.assertNotIn("SECRET_BODY", repr(result))

    async def test_exact_body_limits_stream_closure_and_overrides(self):
        for size, code in ((32, None), (33, "response_too_large")):
            stream = Stream([b"x" * 16, b"x" * (size - 16)])
            with injected(self.boundary, lambda _: httpx.Response(200, headers={"X-Request-ID": "req"}, stream=stream)):
                if code:
                    with self.assertRaises(SDKError) as caught:
                        await self.client.raw("GET", "/")
                    self.assertEqual(caught.exception.code, code)
                    self.assertEqual(caught.exception.status, 200)
                    self.assertEqual(caught.exception.request_id, "req")
                else:
                    self.assertEqual(len((await self.client.raw("GET", "/")).data), size)
            self.assertTrue(stream.closed)
        stream = Stream([b"x" * 33])
        with injected(self.boundary, lambda _: httpx.Response(200, stream=stream)), response_limit(33):
            self.assertEqual(len((await self.client.raw("GET", "/")).data), 33)
        with self.assertRaises(SDKError):
            with response_limit(0):
                pass
        handler = Mock(side_effect=AssertionError("must not send"))
        with injected(self.boundary, handler):
            for body, code in ((b"x" * 257, "request_too_large"), (float("nan"), "request_failed")):
                with self.assertRaises(SDKError) as caught:
                    await self.boundary.request("POST", "https://catalog.example.com", body=body)
                self.assertEqual(caught.exception.code, code)
        handler.assert_not_called()

    async def test_encoding_headers_timeout_and_safe_transport_failure(self):
        for headers, code in (({"Content-Encoding": "gzip"}, "encoded_response"), ({"X-Large": "x" * 40000}, "headers_too_large")):
            stream = Stream([b"secret"])
            with injected(self.boundary, lambda _: httpx.Response(200, headers=headers, stream=stream)), self.assertRaises(SDKError) as caught:
                await self.client.raw("GET", "/")
            self.assertEqual(caught.exception.code, code)
            self.assertTrue(stream.closed)
            self.assertNotIn("secret", repr(caught.exception))
        with injected(self.boundary, unittest.mock.Mock(side_effect=RuntimeError("secret dependency error"))), self.assertRaises(SDKError) as caught:
            await self.client.raw("POST", "/", body=b"command")
        self.assertEqual(caught.exception.code, "transport_failed")
        self.assertNotIn("secret", str(caught.exception))
        handler = Mock(side_effect=AssertionError("must not send"))
        with injected(self.boundary, handler):
            for timeout in (0, True, float("nan"), (1, 2)):
                with self.assertRaises(SDKError) as caught:
                    await self.client.raw("GET", "/", timeout=timeout)
                self.assertEqual(caught.exception.code, "invalid_timeout")
        handler.assert_not_called()

    async def test_provider_time_counts_toward_capped_request_deadline(self):
        provider = Mock(return_value="current")
        boundary = HTTPBoundary(Config(origin="https://catalog.example.com", token_provider=provider, timeout=1.0))
        exchange = AsyncMock(return_value=Response(200, httpx.Headers(), b"ok"))
        for override, elapsed, expected in ((None, 0.25, 0.75), (2.0, 0.25, 0.75), (0.5, 0.25, 0.25), (0.5, 0.5, None)):
            provider.reset_mock()
            exchange.reset_mock()
            clock = Mock()
            clock.time.side_effect = [100.0, 100.0 + elapsed]
            with self.subTest(override=override, elapsed=elapsed), patch("catalog_sdk.transport.asyncio.get_running_loop", return_value=clock), patch.object(boundary, "_exchange", exchange):
                if expected is None:
                    with self.assertRaises(SDKError) as caught:
                        await boundary.request("GET", "https://catalog.example.com/", _request_timeout=override)
                    self.assertEqual(caught.exception.code, "deadline_exceeded")
                    exchange.assert_not_awaited()
                else:
                    result = await boundary.request("GET", "https://catalog.example.com/", _request_timeout=override)
                    self.assertEqual(result.data, b"ok")
                    exchange.assert_awaited_once()
                    self.assertEqual(exchange.await_args.args[4], expected)
                    self.assertEqual(exchange.await_args.args[2]["Authorization"], "Bearer current")
            provider.assert_called_once_with()

    async def test_provider_failures_are_safe_and_send_nothing(self):
        handler = Mock(side_effect=AssertionError("must not send"))
        for error, code in ((TimeoutError("SECRET_PROVIDER"), "deadline_exceeded"), (RuntimeError("SECRET_PROVIDER"), "request_failed")):
            provider = Mock(side_effect=error)
            boundary = HTTPBoundary(Config(origin="https://catalog.example.com", token_provider=provider))
            with injected(boundary, handler), self.assertRaises(SDKError) as caught:
                await boundary.request("GET", "https://catalog.example.com/")
            self.assertEqual(caught.exception.code, code)
            self.assertNotIn("SECRET_PROVIDER", str(caught.exception))
            provider.assert_called_once_with()
        handler.assert_not_called()

    async def test_task_cancellation_closes_stream(self):
        entered = asyncio.Event()
        closed = asyncio.Event()
        class Waiting(httpx.AsyncByteStream):
            async def __aiter__(self):
                entered.set()
                await asyncio.Event().wait()
                yield b"unreachable"
            async def aclose(self):
                closed.set()
        with injected(self.boundary, lambda _: httpx.Response(200, stream=Waiting())):
            task = asyncio.create_task(self.client.raw("GET", "/"))
            await entered.wait()
            task.cancel()
            with self.assertRaises(asyncio.CancelledError):
                await task
        self.assertTrue(closed.is_set())

    async def test_concurrent_tokens_and_context_budgets(self):
        credential = __import__("contextvars").ContextVar("credential")
        boundary = HTTPBoundary(Config(origin="https://catalog.example.com", token_provider=credential.get))
        async def handler(request):
            await asyncio.sleep(0)
            return httpx.Response(200, stream=Stream([request.headers["Authorization"].encode()]))
        async def call(token):
            credential.set(token)
            with response_limit(8 if token == "a" else 9):
                return (await boundary.request("GET", "https://catalog.example.com/")).data
        with injected(boundary, handler):
            self.assertEqual(await asyncio.gather(call("a"), call("bb")), [b"Bearer a", b"Bearer bb"])
        for token in ("", "bad\ntoken", "nonascii\u00e9", 5):
            b = HTTPBoundary(Config(origin="https://catalog.example.com", token_provider=lambda: token))
            handler = Mock(side_effect=AssertionError("must not send"))
            with injected(b, handler), self.assertRaises(SDKError) as caught:
                await b.request("GET", "https://catalog.example.com/")
            self.assertEqual(caught.exception.code, "invalid_token")
            handler.assert_not_called()

    async def test_generated_raw_response_and_client_context_close_streams(self):
        client = Client(Config(origin="https://catalog.example.com"))
        stream = Stream([b"metric 1\n"])
        async with client:
            with injected(client.rest_client, lambda _: httpx.Response(200, headers={"Content-Type":"text/plain"}, stream=stream)):
                result = await ServiceApi(client).get_metrics_without_preload_content()
            self.assertIsInstance(result, Response)
            self.assertEqual(await result.read(), b"metric 1\n")
            self.assertTrue(stream.closed)
        stream = Stream([])
        def unchanged(request):
            self.assertEqual(request.headers["If-None-Match"], '"contract"')
            return httpx.Response(304, headers={"ETag":'"contract"'}, stream=stream)
        with injected(client.rest_client, unchanged):
            result = await ServiceApi(client).get_control_open_api_with_http_info(if_none_match='"contract"')
        self.assertEqual(result.status_code, 304)
        self.assertIsNone(result.data)
        self.assertEqual(result.raw_data, b"")
        self.assertEqual(result.headers["ETag"], '"contract"')
        self.assertTrue(stream.closed)

    async def test_generated_errors_bodyless_and_safe_decoding(self):
        client = Client(Config(origin="https://catalog.example.com"))
        for status in (204, 304):
            result = client.response_deserialize(Response(status, httpx.Headers({"ETag": '"same"'}), b""), {str(status): None})
            self.assertIsNone(result.data)
            self.assertEqual(result.status_code, status)
        for status in (429, 503):
            body = json.dumps({"type":"about:blank","title":"Unavailable","status":status,"code":"DEPENDENCY_UNAVAILABLE","detail":"private","requestId":"safe"}).encode()
            with injected(client.rest_client, lambda _: httpx.Response(status, headers={"Content-Type":"application/problem+json", "Retry-After":"2"}, stream=Stream([body]))):
                result = await ServiceApi(client).get_capabilities_with_http_info()
            self.assertEqual(result.data.code, "DEPENDENCY_UNAVAILABLE")
            self.assertEqual(result.status_code, status)
            self.assertEqual(result.headers["Retry-After"], "2")
        for body in (b"", b"null", b" \n null \t", b"private not JSON", b'{"version":"secret","commit":false}'):
            with injected(client.rest_client, lambda _: httpx.Response(200, headers={"Content-Type":"application/json","X-Request-ID":"corr"}, stream=Stream([body]))), self.assertRaises(SDKError) as caught:
                await ServiceApi(client).get_version_with_http_info()
            self.assertEqual(caught.exception.code, "invalid_response")
            self.assertEqual(caught.exception.request_id, "corr")
            self.assertNotIn("secret", repr(caught.exception))
        # Generator validation does not echo supplied invalid values.
        handler = Mock(side_effect=AssertionError("must not send"))
        with injected(client.rest_client, handler), self.assertRaises(ValidationError) as caught:
            await IdentityApi(client).get_namespace("secret-invalid-uuid")
        self.assertNotIn("secret-invalid-uuid", str(caught.exception))
        handler.assert_not_called()

    async def test_conditional_object_keeps_all_fields(self):
        value = {"sourceRevision": 1, "identifier": "urn:air:example.com:ns:item", "versionPresent": False, "delivery":"data", "exactEntryDigest":"sha256:" + "a" * 64, "text":"exact", "reason":"check", "mediaType":"application/json", "extension": [None, False, "", 9007199254740993]}
        model = TrustAssessmentRequest.model_validate(value)
        self.assertEqual(self.client.sanitize_for_serialization(model), value)
        self.assertEqual(TrustAssessmentRequest.from_dict(value).to_dict(), value)

    async def test_binary_ard_errors_and_partial_search_are_not_followed(self):
        client = Client(Config(origin="https://catalog.example.com"))
        binary = b"\x00\xff\x89PNG\r\n"
        result = client.response_deserialize(Response(200, httpx.Headers({"Content-Type":"image/png"}), binary), {"200":"file"})
        self.assertEqual(result.data, binary)
        self.assertEqual(result.raw_data, binary)
        error = {"errorCode":"INVALID_ARGUMENT", "message":"Invalid cursor"}
        with injected(client.rest_client, lambda _: httpx.Response(400, headers={"Content-Type":"application/json"}, stream=Stream([json.dumps(error).encode()]))):
            result = await DiscoveryApi(client).list_ard_registry_entries_with_http_info()
        self.assertEqual(result.data.error_code, "INVALID_ARGUMENT")
        self.assertEqual(result.data.message, "Invalid cursor")
        partial = {"items":[],"nextCursor":"opaque=cursor","durableTotal":0,"liveCount":0,"profile":"core-lexical-v1","generation":"sha256:" + "a" * 64,"tier":"core","partial":True,"diagnostics":[]}
        calls = []
        def handler(request):
            calls.append(request)
            return httpx.Response(200, headers={"Content-Type":"application/json", "X-Catalog-Partial":"true"}, stream=Stream([json.dumps(partial).encode()]))
        with injected(client.rest_client, handler):
            result = await CatalogDiscoveryApi(client).search_catalog_with_http_info(catalog_search=CatalogSearch(limit=1, tags=["exact"]))
        self.assertTrue(result.data.partial)
        self.assertEqual(result.data.next_cursor, "opaque=cursor")
        self.assertEqual(result.headers["X-Catalog-Partial"], "true")
        self.assertEqual(len(calls), 1)
        self.assertEqual(json.loads(calls[0].content), {"limit":1,"tags":["exact"]})
