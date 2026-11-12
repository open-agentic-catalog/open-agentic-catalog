"""Actual graph/callback observations with injected SDK calls; no services."""

import asyncio
import json
from pathlib import Path
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock, patch
from uuid import UUID

from langchain_core.messages import AIMessage
import httpx
import test_unit_agent as data

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "search-relevance"))
from benchmark import agent_benchmark as subject
from benchmark.runtime import CatalogFixture


class ObservationUnits(unittest.TestCase):
    def fixture(self):
        fixture = object.__new__(CatalogFixture)
        fixture.arm, fixture.context = "lexical", None
        fixture.by_release = {data.ID: "fixture-skill"}
        fixture.kind_by_release = {data.ID: "skill"}
        fixture.inventory = {"namespaceId": data.OTHER}
        fixture.origin, fixture.reasoner_origin = "http://127.0.0.1:12345", "http://127.0.0.1:12346"
        fixture.guard = lambda: None
        page = data.page()
        raw = {"items": [{"id": "local:" + data.ID, **item.to_dict()} for item in page.data.items],
               "tier": "core", "profile": "core-lexical-v1", "generation": "sha256:" + "a" * 64, "partial": False, "liveCount": 0}
        page.data.to_json = lambda: json.dumps(raw)
        record = data.record()
        record.data.release = SimpleNamespace(id=UUID(data.ID))
        api = SimpleNamespace(search_catalog_with_http_info=AsyncMock(return_value=page), get_catalog_record_with_http_info=AsyncMock(return_value=record))
        task = {"id": "t", "intentFamily": "web", "text": "Find a Skill for web testing", "relevant": {"fixture-skill": 2}}
        return fixture, api, task

    def test_actual_graph_records_serializable_sdk_uuid_inputs_without_extra_prompt_evidence(self):
        fixture, api, task = self.fixture()
        model = data.Model(data.call("search_catalog", {"query": "web", "kinds": ["skill"]}),
                           data.call("read_catalog_record", {"release_id": data.ID}), data.answer())
        with patch.object(subject, "CatalogDiscoveryApi", return_value=api):
            row = asyncio.run(subject.run_episode(fixture, task, {"model": "unused"}, 30, scripted=model))
        self.assertEqual(row["outcome"], "successful_discovery")
        self.assertEqual((row["tool_calls"], row["catalog_requests"], row["model_calls"]), (2, 2, 0))
        self.assertEqual(row["sdk_calls"][0]["inputs"]["namespaceId"], data.OTHER)
        self.assertEqual(row["found"], ["fixture-skill"])
        self.assertEqual(row["read"], ["fixture-skill"])
        self.assertIsNone(row["tokens"])
        json.dumps(row)  # UUIDs in the generated SDK must not corrupt reports.
        self.assertFalse(any("relevant" in str(message.content) for call in model.calls for message in call))

    def test_refused_tool_attempt_is_observed_before_any_sdk_call_then_graph_can_recover(self):
        fixture, api, task = self.fixture()
        model = data.Model(data.call("read_catalog_record", {"release_id": data.ID}, "refused"),
                           data.call("search_catalog", {"query": "web"}, "search"),
                           data.call("read_catalog_record", {"release_id": data.ID}, "read"), data.answer())
        with patch.object(subject, "CatalogDiscoveryApi", return_value=api):
            row = asyncio.run(subject.run_episode(fixture, task, {"model": "unused"}, 30, scripted=model))
        self.assertEqual(row["outcome"], "successful_discovery")
        self.assertEqual(row["tool_refusals"], ["select_a_search_result"])
        self.assertEqual((row["tool_calls"], row["catalog_requests"]), (3, 2))
        self.assertEqual(api.get_catalog_record_with_http_info.await_count, 1)

    def test_catalog_failure_invalidates_campaign_instead_of_becoming_quality_success(self):
        fixture, api, task = self.fixture()
        api.search_catalog_with_http_info.return_value = SimpleNamespace(status_code=503)
        model = data.Model(data.call("search_catalog", {"query": "web"}))
        with patch.object(subject, "CatalogDiscoveryApi", return_value=api), self.assertRaisesRegex(ValueError, "boundary failure"):
            asyncio.run(subject.run_episode(fixture, task, {"model": "unused"}, 30, scripted=model))
        self.assertEqual(api.get_catalog_record_with_http_info.await_count, 0)

    def test_model_free_browsing_and_invalid_air_looking_text_are_classified_by_actual_search_path(self):
        for text, tier in (("", "core"), ("urn:air:bad:scope:entry", "hybrid")):
            with self.subTest(text=text):
                fixture, api, task = self.fixture()
                fixture.arm = "bge-small-v1"
                fixture.context = {"tier": "hybrid", "profile": "sha256:" + "c" * 64, "generation": "sha256:" + "a" * 64}
                raw = json.loads(api.search_catalog_with_http_info.return_value.data.to_json())
                raw.update(tier=tier, profile="core-lexical-v1" if tier == "core" else "sha256:" + "c" * 64)
                api.search_catalog_with_http_info.return_value.data.to_json = lambda: json.dumps(raw)
                model = data.Model(data.call("search_catalog", {"query": text}), data.call("read_catalog_record", {"release_id": data.ID}), data.answer())
                with patch.object(subject, "CatalogDiscoveryApi", return_value=api):
                    row = asyncio.run(subject.run_episode(fixture, task, {"model": "unused"}, 30, scripted=model))
                self.assertEqual(row["outcome"], "successful_discovery")
                self.assertEqual(row["sdk_calls"][0]["response"]["tier"], tier)
                self.assertEqual(fixture.context["tier"], "hybrid")

    def test_reasoner_transport_errors_are_execution_failures_and_deadlines_are_budget_outcomes(self):
        for error, boundary, budget in ((httpx.ConnectError("PRIVATE"), True, False),
                                         (httpx.ReadTimeout("PRIVATE"), False, True),
                                         (asyncio.CancelledError(), False, True)):
            with self.subTest(error=type(error).__name__):
                observer = subject.Observer()
                asyncio.run(observer.on_llm_error(error))
                self.assertEqual(observer.model_boundary_failure, boundary)
                self.assertEqual(observer.model_timeout, budget)
                self.assertNotIn("PRIVATE", json.dumps(vars(observer)))

    def test_http_hooks_count_attempts_and_refuse_provider_http_errors(self):
        observer = subject.Observer()
        request = httpx.Request("POST", "http://127.0.0.1:12346/api/chat")
        asyncio.run(observer.on_inference_request(request))
        self.assertEqual(observer.inference_http_attempts, 1)
        asyncio.run(observer.on_inference_response(httpx.Response(200, request=request)))
        self.assertFalse(observer.model_boundary_failure)
        asyncio.run(observer.on_inference_response(httpx.Response(503, request=request)))
        self.assertTrue(observer.model_boundary_failure)

    def test_callbacks_retain_usage_and_safe_unknown_tool_count_without_model_text(self):
        observer = subject.Observer()
        message = AIMessage(content="PRIVATE_HIDDEN_REASONING", usage_metadata={"input_tokens": 2, "output_tokens": 3, "total_tokens": 5},
                            tool_calls=[{"name": "unknown_exec", "args": {}, "id": "1", "type": "tool_call"}])
        asyncio.run(observer.on_chat_model_start({}, []))
        asyncio.run(observer.on_llm_end(SimpleNamespace(generations=[[SimpleNamespace(message=message)]])))
        self.assertEqual(observer.token_usage, {"input_tokens": 2, "output_tokens": 3, "total_tokens": 5})
        self.assertEqual(observer.refusals, ["unknown_tool"])
        self.assertEqual(observer.model_calls, 1)
        self.assertNotIn("PRIVATE_HIDDEN_REASONING", json.dumps(vars(observer)))
