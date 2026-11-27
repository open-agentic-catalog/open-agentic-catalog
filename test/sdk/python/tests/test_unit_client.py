"""No listeners, subprocesses, DNS or real network in this selection."""
import asyncio
from contextvars import ContextVar
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
from catalog_sdk.generated.api.catalog_mcp_api import CatalogMCPApi
from catalog_sdk.generated.api.catalog_lifecycle_api import CatalogLifecycleApi
from catalog_sdk.generated.api.personal_workspace_api import PersonalWorkspaceApi
from catalog_sdk.generated.api.shared_collections_api import SharedCollectionsApi
from catalog_sdk.generated.api.feedback_api import FeedbackApi
from catalog_sdk.generated.api.federation_api import FederationApi
from catalog_sdk.generated.models.catalog_search import CatalogSearch
from catalog_sdk.generated.models.create_collection import CreateCollection
from catalog_sdk.generated.models.trust_assessment_request import TrustAssessmentRequest
from catalog_sdk.generated.models.update_collection import UpdateCollection
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

    async def test_search_resumes_from_last_decoded_page_after_invalid_continuation(self):
        search = self.examples["search_local_pages"]
        cursor = "opaque+page/2=="
        page = {"items": [], "durableTotal": 0, "liveCount": 0, "partial": False,
                "tier": "core", "profile": "core-lexical-v1", "generation": "sha256:" + "a" * 64}
        # The second response advertises a new cursor but has an invalid result type.
        bodies = [dict(page, nextCursor=cursor), dict(page, items="private-result", nextCursor="unconfirmed"), page]
        transport = Mock(side_effect=[
            httpx.Response(200, headers={"Content-Type": "application/json", "X-Request-ID": f"page-{number}"},
                           stream=Stream([json.dumps(body).encode()]))
            for number, body in enumerate(bodies, 1)
        ])
        with injected(self.client.rest_client, transport):
            pages = search(self.client, "weather", namespace_id=self.uid)
            first = await anext(pages)
            self.assertEqual(first.next_cursor, cursor)
            self.assertEqual(transport.call_count, 1)
            with self.assertRaises(SDKError) as caught:
                await anext(pages)
            failure = caught.exception
            self.assertEqual((failure.code, failure.status, failure.request_id), ("invalid_response", 200, "page-2"))
            self.assertNotIn("private-result", str(failure))
            self.assertEqual(transport.call_count, 2)
            resumed = [p async for p in search(self.client, "weather", namespace_id=self.uid, cursor=first.next_cursor)]

        self.assertEqual([p.next_cursor for p in resumed], [None])
        base = {"text": "weather", "origins": ["local"], "namespaceId": str(self.uid), "limit": 20}
        requests = [call.args[0] for call in transport.call_args_list]
        self.assertEqual([json.loads(r.content) for r in requests], [base, dict(base, cursor=cursor), dict(base, cursor=cursor)])
        self.assertTrue(all(r.headers.get("Authorization") == "Bearer selected" for r in requests))

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
    def test_sync_collection_patch_preserves_omitted_fields_and_empty_description(self):
        collection_id = UUID("00000000-0000-7000-8000-000000000101")
        client = Client(Config(origin="https://catalog.example.com"))
        api = SharedCollectionsApi(client)
        receipt = json.dumps({"id": str(collection_id), "revision": 3}).encode()
        for title, update, expected in (
            ("rename only", UpdateCollection(name="Renamed"), {"name": "Renamed"}),
            ("clear description only", UpdateCollection(description=""), {"description": ""}),
        ):
            with self.subTest(title):
                transport = Mock(return_value=httpx.Response(
                    200, headers={"Content-Type": "application/json"}, stream=Stream([receipt]),
                ))
                with injected(client.rest_client, transport):
                    result = api.update_collection_sync_with_http_info(
                        collection_id=collection_id, idempotency_key="collection-edit",
                        if_match='"rev-2"', update_collection=update,
                    )
                transport.assert_called_once()
                request = transport.call_args.args[0]
                self.assertEqual((request.method, request.url.path), ("PATCH", f"/api/v1/collections/{collection_id}"))
                self.assertEqual(request.headers["If-Match"], '"rev-2"')
                self.assertEqual(request.headers["Idempotency-Key"], "collection-edit")
                self.assertEqual(json.loads(request.content), expected)
                self.assertEqual((result.status_code, result.data.id, result.data.revision), (200, collection_id, 3))

    async def test_withdrawal_preview_is_one_explicit_read_with_bound_references(self):
        release = "00000000-0000-7000-8000-000000000101"
        client = Client(Config(origin="https://catalog.example.com", token="selected"))
        calls = []
        def handler(request):
            calls.append(request)
            self.assertEqual(request.method, "GET")
            self.assertEqual(request.url.path, "/api/v1/listing-releases/" + release + "/withdrawal-preview")
            self.assertEqual(dict(request.url.params), {"transition": "retired"})
            self.assertNotIn("Idempotency-Key", request.headers)
            self.assertNotIn("If-Match", request.headers)
            wire = {"releaseId": release, "releaseRevision": 3, "transition": "retired", "observedAt": "2026-11-15T12:00:00Z", "snapshots": [{"snapshotId": "00000000-0000-7000-8000-000000000201", "host": "catalog.example.com", "audience": "anonymous", "generation": 2, "etag": "feed-two"}]}
            return httpx.Response(200, headers={"Content-Type": "application/json"}, stream=Stream([json.dumps(wire).encode()]))
        with injected(client.rest_client, handler):
            value = await CatalogLifecycleApi(client).preview_listing_release_withdrawal(transition="retired", release_id=UUID(release))
        self.assertEqual(len(calls), 1)
        self.assertEqual(value.release_revision, 3)
        self.assertEqual(value.snapshots[0].host, "catalog.example.com")
        self.assertEqual(value.snapshots[0].audience, "anonymous")

    async def test_source_sync_outcome_retains_exact_filters_and_explicit_cursor(self):
        client = Client(Config(origin="https://catalog.example.com", token="selected"))
        calls = []
        def handler(request):
            self.assertEqual(request.method, "GET")
            self.assertEqual(request.url.path, "/api/v1/federation/sources")
            self.assertNotIn("Idempotency-Key", request.headers)
            calls.append(dict(request.url.params))
            wire = {"items": [], **({"nextCursor": "opaque+/= café"} if len(calls) == 1 else {})}
            return httpx.Response(200, headers={"Content-Type": "application/json"}, stream=Stream([json.dumps(wire).encode()]))
        with injected(client.rest_client, handler):
            api = FederationApi(client)
            first = await api.list_federation_sources(display_name="Exact Source", sync_state="failed", status="active", limit=1)
            self.assertEqual(len(calls), 1)
            self.assertEqual(first.next_cursor, "opaque+/= café")
            last = await api.list_federation_sources(display_name="Exact Source", sync_state="failed", status="active", limit=1, cursor=first.next_cursor)
            self.assertIsNone(last.next_cursor)
        expected = {"displayName": "Exact Source", "syncState": "failed", "status": "active", "limit": "1"}
        self.assertEqual(calls, [expected, dict(expected, cursor="opaque+/= café")])

    async def test_recent_own_feedback_retains_order_state_and_explicit_cursor(self):
        client = Client(Config(origin="https://catalog.example.com", token="selected"))
        calls = []
        def handler(request):
            self.assertEqual(request.method, "GET")
            self.assertEqual(request.url.path, "/api/v1/feedback")
            self.assertNotIn("Idempotency-Key", request.headers)
            calls.append(dict(request.url.params))
            wire = {"items": [], **({"nextCursor": "opaque+/= café"} if len(calls) == 1 else {})}
            return httpx.Response(200, headers={"Content-Type": "application/json"}, stream=Stream([json.dumps(wire).encode()]))
        with injected(client.rest_client, handler):
            api = FeedbackApi(client)
            first = await api.list_own_feedback(order="updated_desc", state="addressed", limit=1)
            self.assertEqual(len(calls), 1)
            self.assertEqual(first.next_cursor, "opaque+/= café")
            last = await api.list_own_feedback(order="updated_desc", state="addressed", limit=1, cursor=first.next_cursor)
            self.assertIsNone(last.next_cursor)
        expected = {"order": "updated_desc", "state": "addressed", "limit": "1"}
        self.assertEqual(calls, [expected, dict(expected, cursor="opaque+/= café")])

    async def test_review_queue_retains_namespace_intersection_and_cursor_without_retry(self):
        client = Client(Config(origin="https://catalog.example.com", token="selected"))
        uid = UUID("aaaaaaaa-0000-7000-8000-000000000010")
        calls = []
        def handler(request):
            self.assertEqual(request.method, "GET")
            self.assertEqual(request.url.path, "/api/v1/review-submissions")
            self.assertNotIn("Idempotency-Key", request.headers)
            calls.append(dict(request.url.params))
            wire = {"items": [], **({"nextCursor": "opaque+/= café"} if len(calls) == 1 else {})}
            return httpx.Response(200, headers={"Content-Type": "application/json"}, stream=Stream([json.dumps(wire).encode()]))
        with injected(client.rest_client, handler):
            api = CatalogLifecycleApi(client)
            first = await api.list_review_submissions(namespace_id=uid, listing_id=uid, state="approved", unreleased=True, limit=1)
            self.assertEqual(len(calls), 1)
            self.assertEqual(first.next_cursor, "opaque+/= café")
            last = await api.list_review_submissions(namespace_id=uid, listing_id=uid, state="approved", unreleased=True, limit=1, cursor=first.next_cursor)
            self.assertIsNone(last.next_cursor)
        expected = {"namespaceId": str(uid), "listingId": str(uid), "state": "approved", "unreleased": "true", "limit": "1"}
        self.assertEqual(calls, [expected, dict(expected, cursor="opaque+/= café")])

    async def test_generated_wire_semantics_use_one_origin_and_preserve_open_metadata(self):
        from catalog_sdk.generated.models.create_listing_revision_extensions import CreateListingRevisionExtensions
        words = ("", "é", "日本語", "😀", "%+&=", "line\nvalue", 'quoted"value', "two words")
        client = Client(Config(origin="https://catalog.example.com", token="selected"))
        calls = []
        def handler(request):
            calls.append(request)
            self.assertEqual(request.url.scheme, "https")
            self.assertEqual(request.url.host, "catalog.example.com")
            self.assertEqual(request.method, "GET")
            self.assertEqual(request.url.path, "/api/v1/personal/items")
            return httpx.Response(200, headers={"Content-Type":"application/json"}, stream=Stream([b'{"items":[]}']))
        with injected(client.rest_client, handler):
            for seed in range(32):
                label = f"case-{seed}-{words[seed % len(words)]}"
                selected = None if seed % 3 == 0 else "" if seed % 3 == 1 else label
                before = len(calls)
                if selected == "":
                    # Python's generated parameter enforces minLength locally;
                    # the other SDKs deliberately leave it to the API.
                    with self.assertRaises(ValidationError):
                        await PersonalWorkspaceApi(client).list_personal_items(label=selected)
                    self.assertEqual(len(calls), before)
                else:
                    await PersonalWorkspaceApi(client).list_personal_items(label=selected)
                    self.assertEqual(len(calls), before + 1)
                    self.assertEqual(list(calls[-1].url.params.multi_items()), [] if selected is None else [("label", label)])
                after = len(calls)
                expected = {"additional_properties":{"literal":words[seed % len(words)]}, "model_extra":None,
                            "values":[None, False, "", label], "count":seed * 65537}
                self.assertEqual(CreateListingRevisionExtensions(**expected).to_dict(), expected)
                self.assertEqual(len(calls), after, "codec work must not contact a destination")

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

    async def test_exact_body_limits_and_stream_closure(self):
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

    def test_metadata_head_returns_bodyless_errors_while_get_still_checks_the_body(self):
        api = CatalogMCPApi(self.client)
        path = "/.well-known/oauth-protected-resource/mcp"
        metadata = {"resource": "https://catalog.example.com/mcp", "authorization_servers": ["https://issuer.example"],
                    "scopes_supported": ["catalog.read"], "bearer_methods_supported": ["header"]}
        wire = json.dumps(metadata).encode()
        for method, status, chunks, limit, failure in (
            ("HEAD", 200, [], 1, None),
            ("HEAD", 404, [], 1, None),
            ("GET", 404, [], 1, "invalid_response"),
            ("GET", 200, [wire], len(wire), None),
            ("GET", 200, [wire], len(wire) - 1, "response_too_large"),
        ):
            with self.subTest(method=method, status=status, limit=limit):
                headers = {"Content-Type": "application/json", "X-Request-ID": "metadata-request"}
                if method == "HEAD" or chunks:
                    headers["Content-Length"] = str(len(wire))
                stream = Stream(chunks)
                handler = Mock(return_value=httpx.Response(status, headers=headers, stream=stream))
                with injected(self.boundary, handler), response_limit(limit):
                    if method == "HEAD":
                        result = api.head_catalog_mcpo_auth_metadata_sync_with_http_info()
                        self.assertEqual((result.status_code, result.data, result.raw_data), (status, None, b""))
                        self.assertEqual(result.headers["X-Request-ID"], "metadata-request")
                        self.assertEqual(result.headers["Content-Length"], str(len(wire)))
                    elif failure is None:
                        result = api.get_catalog_mcpo_auth_metadata_sync_with_http_info()
                        self.assertEqual((result.status_code, result.data.to_dict(), result.raw_data), (200, metadata, wire))
                    else:
                        with self.assertRaises(SDKError) as caught:
                            api.get_catalog_mcpo_auth_metadata_sync_with_http_info()
                        self.assertEqual((caught.exception.code, caught.exception.status, caught.exception.request_id),
                                         (failure, status, "metadata-request"))
                handler.assert_called_once()
                request = handler.call_args.args[0]
                self.assertEqual((request.method, request.url.path), (method, path))
                self.assertTrue(stream.closed)

    async def test_interrupted_command_response_keeps_status_and_request_id(self):
        namespace_id = "00000000-0000-7000-8000-000000000101"
        collection_id = UUID("00000000-0000-7000-8000-000000000102")
        def receipt(complete):
            yield b'{"id":'
            if not complete:
                raise httpx.RemoteProtocolError("response ended before completion")
            yield f'"{collection_id}","revision":1}}'.encode()

        for complete in (True, False):
            with self.subTest(complete=complete):
                stream = Stream(receipt(complete))
                handler = Mock(return_value=httpx.Response(
                    201, headers={"Content-Type": "application/json", "X-Request-ID": "command-created"}, stream=stream
                ))
                with injected(self.boundary, handler), response_limit(256):
                    command = SharedCollectionsApi(self.client).create_collection_with_http_info(
                        create_collection=CreateCollection(namespace_id=UUID(namespace_id), name="Weather"),
                        idempotency_key="single-command",
                    )
                    if complete:
                        result = await command
                        self.assertEqual((result.status_code, result.data.id, result.data.revision), (201, collection_id, 1))
                    else:
                        with self.assertRaises(SDKError) as caught:
                            await command
                        self.assertEqual(caught.exception.code, "transport_failed")
                        self.assertEqual(caught.exception.status, 201)
                        self.assertEqual(caught.exception.request_id, "command-created")
                handler.assert_called_once()
                request = handler.call_args.args[0]
                self.assertEqual((request.method, str(request.url)), ("POST", "https://catalog.example.com/api/v1/collections"))
                self.assertEqual(request.headers["Idempotency-Key"], "single-command")
                self.assertEqual(json.loads(request.content), {"namespaceId": namespace_id, "name": "Weather"})
                self.assertTrue(stream.closed)

    async def test_response_limit_restores_outer_budget_after_error_and_default_on_exit(self):
        payload = b"x" * 33  # One byte over the configured default.
        def handler(_):
            return httpx.Response(200, stream=Stream([payload]))

        with injected(self.boundary, handler):
            with response_limit(33):
                with self.assertRaises(SDKError) as inner, response_limit(32):
                    await self.client.raw("GET", "/")
                self.assertEqual(inner.exception.code, "response_too_large")
                self.assertEqual((await self.client.raw("GET", "/")).data, payload)
            with self.assertRaises(SDKError) as outside:
                await self.client.raw("GET", "/")
            self.assertEqual(outside.exception.code, "response_too_large")

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
        credential = ContextVar("credential")
        client = Client(Config(origin="https://catalog.example.com", token_provider=credential.get))
        both_scopes = asyncio.Barrier(2)
        received = []
        payload = b"123456789"

        def handler(request):
            received.append((request.url.path, request.headers.get("Authorization")))
            return httpx.Response(200, headers={"X-Request-ID": request.url.path[1:]}, stream=Stream([payload]))

        async def call(token, limit):
            credential.set(token)
            with response_limit(limit):
                # Keep both overrides active until both responses have been checked.
                await both_scopes.wait()
                try:
                    return await client.raw("GET", "/" + token)
                finally:
                    await both_scopes.wait()

        with injected(client.rest_client, handler):
            async with asyncio.timeout(5):
                smaller, exact = await asyncio.gather(call("small", 8), call("exact", 9), return_exceptions=True)
        self.assertCountEqual(received, [("/small", "Bearer small"), ("/exact", "Bearer exact")])
        self.assertIsInstance(smaller, SDKError)
        self.assertEqual((smaller.code, smaller.status, smaller.request_id), ("response_too_large", 200, "small"))
        self.assertIsInstance(exact, Response)
        self.assertEqual((exact.data, exact.headers["X-Request-ID"]), (payload, "exact"))

    async def test_invalid_provider_tokens_do_not_reach_transport(self):
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
            self.assertEqual((request.method, request.url.path), ("GET", "/openapi/control-v1.yaml"))
            self.assertEqual(request.headers["If-None-Match"], '"contract"')
            return httpx.Response(304, headers={"ETag":'"contract"', "Content-Length": "2"}, stream=stream)
        with injected(client.rest_client, unchanged), response_limit(1):
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

    async def test_ard_list_continuation_preserves_criteria_and_returns_the_peer_refusal(self):
        client = Client(Config(origin="https://catalog.example.com"))
        api = DiscoveryApi(client)
        entry = {"identifier": "urn:air:example.com:examples:weather", "displayName": "Weather agent",
                 "type": "application/a2a-agent-card+json", "data": {"name": "Weather agent"}}
        cursor = "next+/= café"
        error = {"errorCode": "INVALID_ARGUMENT", "message": "Invalid cursor"}
        responses = Mock(side_effect=[
            httpx.Response(200, headers={"Content-Type": "application/json"}, stream=Stream([
                json.dumps({"items": [entry], "total": 2, "pageToken": cursor}).encode()
            ])),
            httpx.Response(400, headers={"Content-Type": "application/json", "X-Request-ID": "list-refused"},
                           stream=Stream([json.dumps(error).encode()])),
        ])
        criteria = {"filter": "displayName = 'Weather agent'", "order_by": "updatedAt DESC", "page_size": 1}
        with injected(client.rest_client, responses):
            first = await api.list_ard_registry_entries_with_http_info(**criteria)
            self.assertEqual(responses.call_count, 1)
            self.assertEqual((first.status_code, first.data.total, first.data.page_token), (200, 2, cursor))
            self.assertEqual([item.to_dict() for item in first.data.items], [entry])
            refused = await api.list_ard_registry_entries_with_http_info(**criteria, page_token=first.data.page_token)

        self.assertEqual((refused.status_code, refused.data.error_code, refused.data.message),
                         (400, "INVALID_ARGUMENT", "Invalid cursor"))
        self.assertEqual(refused.headers["X-Request-ID"], "list-refused")
        query = {"filter": "displayName = 'Weather agent'", "orderBy": "updatedAt DESC", "pageSize": "1"}
        requests = [call.args[0] for call in responses.call_args_list]
        self.assertEqual([sorted(r.url.params.multi_items()) for r in requests],
                         [sorted(query.items()), sorted({**query, "pageToken": cursor}.items())])
        for request in requests:
            self.assertEqual((request.method, request.url.host, request.url.path), ("GET", "catalog.example.com", "/ard/v1/agents"))
            self.assertNotIn("Authorization", request.headers)

    async def test_binary_and_partial_search_responses(self):
        client = Client(Config(origin="https://catalog.example.com"))
        binary = b"\x00\xff\x89PNG\r\n"
        result = client.response_deserialize(Response(200, httpx.Headers({"Content-Type":"image/png"}), binary), {"200":"file"})
        self.assertEqual(result.data, binary)
        self.assertEqual(result.raw_data, binary)
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
