"""Actual existing graph with bounded SDK observations and standard callbacks.

Imported only by the explicitly installed LangGraph environment, never by the
model-free benchmark units. Judgments enter only the post-execution scorer.
"""

import json
import asyncio
from pathlib import Path
import sys
import time

import httpx
from catalog_sdk import Client, Config
from catalog_sdk.generated.api.catalog_discovery_api import CatalogDiscoveryApi
from langchain_core.callbacks import AsyncCallbackHandler
from langchain_ollama import ChatOllama

sys.path.insert(0, str(Path(__file__).resolve().parents[3] / "examples/langgraph"))
from agent import DemoError, DiscoveryTools, check_local_model, run_agent

from . import protocol


class Observer(AsyncCallbackHandler):
    """Record counts/usage and safe failures; never retain prompts or reasoning."""

    raise_error = True
    run_inline = True

    def __init__(self):
        self.calls = []
        self.refusals = []
        self.model_calls = 0
        self.inference_http_attempts = 0
        self.usage_calls = 0
        self.tool_calls = 0
        self.token_usage = None
        self.boundary_failure = False
        self.model_boundary_failure = False
        self.model_timeout = False

    async def on_tool_start(self, serialized, input_str, **kwargs):
        self.tool_calls += 1
        if self.tool_calls > 128:
            self.boundary_failure = True
            raise ValueError("benchmark observation budget exhausted")

    async def on_tool_error(self, error, **kwargs):
        self.refusals.append(str(error) if isinstance(error, DemoError) else "invalid_tool_call")

    async def on_chat_model_start(self, serialized, messages, **kwargs):
        self.model_calls += 1

    async def on_llm_error(self, error, **kwargs):
        if isinstance(error, (asyncio.CancelledError, TimeoutError, httpx.ReadTimeout)):
            self.model_timeout = True
        else:
            self.model_boundary_failure = True

    async def on_inference_request(self, request):
        if request.url.path == "/api/chat":
            self.inference_http_attempts += 1

    async def on_inference_response(self, response):
        if response.request.url.path == "/api/chat" and response.status_code >= 400:
            self.model_boundary_failure = True

    async def on_llm_end(self, response, **kwargs):
        for group in response.generations:
            for generation in group:
                usage = getattr(getattr(generation, "message", None), "usage_metadata", None)
                message = getattr(generation, "message", None)
                for call in getattr(message, "tool_calls", ()):
                    if call["name"] not in {"search_catalog", "read_catalog_record"}:
                        self.refusals.append("unknown_tool")
                if usage:
                    self.usage_calls += 1
                    if self.token_usage is None:
                        self.token_usage = {"input_tokens": 0, "output_tokens": 0, "total_tokens": 0}
                    for key in self.token_usage:
                        self.token_usage[key] += usage[key]


class ObservedAPI:
    def __init__(self, api, fixture, observer):
        self.api, self.fixture, self.observer = api, fixture, observer

    async def search_catalog_with_http_info(self, **kwargs):
        body = json.loads(kwargs["catalog_search"].to_json())
        return await self.call("search", self.api.search_catalog_with_http_info, body, kwargs)

    async def get_catalog_record_with_http_info(self, **kwargs):
        return await self.call("read", self.api.get_catalog_record_with_http_info, {"id": str(kwargs["catalog_record_id"])}, kwargs)

    async def call(self, operation, function, inputs, kwargs):
        self.fixture.guard()
        started = time.perf_counter()
        row = {"operation": operation, "inputs": inputs}
        try:
            response = await function(**kwargs)
            row["status"] = response.status_code
            if response.status_code != 200:
                self.observer.boundary_failure = True
            elif operation == "search":
                page = json.loads(response.data.to_json())
                # The model may browse with blank text. Unknown AIR-looking
                # text is classified by the service; do not duplicate its parser.
                text = inputs.get("text") or ""
                model_free = not text.strip() or (text.startswith("urn:air:") and page["tier"] == "core")
                self.fixture.observe_page(page, model_free=model_free, kinds=inputs.get("kinds") or ())
                row["response"] = page
            else:
                row["release_id"] = str(response.data.release.id)
                if row["release_id"] not in self.fixture.by_release:
                    raise ValueError("unmapped exact read")
            return response
        except Exception:
            self.observer.boundary_failure = True
            row["error"] = "catalog_or_observation_failure"
            raise
        finally:
            row["seconds"] = time.perf_counter()-started
            self.observer.calls.append(row)


class ObservedDiscovery(DiscoveryTools):
    def tools(self):
        tools = super().tools()
        for tool in tools:
            tool.callbacks = [self.observer]
        return tools


async def run_episode(fixture, task, pin, seconds, scripted=None):
    """Run normal tools/graph first; inspect private judgments only afterward."""
    from uuid import UUID
    observer = Observer()
    transport = httpx.AsyncHTTPTransport(trust_env=False, retries=0) if scripted is None else None
    model = scripted or ChatOllama(base_url=fixture.reasoner_origin, model=pin["model"], temperature=0, reasoning=False,
                                   num_predict=1024, seed=27062, callbacks=[observer],
                                   client_kwargs={"timeout": 90, "trust_env": False, "follow_redirects": False},
                                   async_client_kwargs={"transport": transport, "event_hooks": {"request": [observer.on_inference_request], "response": [observer.on_inference_response]}})
    api = CatalogDiscoveryApi(Client(Config(origin=fixture.origin, allow_loopback_http=True, token="benchmark-reader")))
    discovery = ObservedDiscovery(ObservedAPI(api, fixture, observer), UUID(fixture.inventory["namespaceId"]))
    discovery.observer = observer
    started = time.perf_counter()
    result, error = None, None
    try:
        result = await run_agent(model, discovery, task["text"], timeout=seconds)
    except DemoError as exc:
        error = str(exc)
    finally:
        if transport is not None:
            await transport.aclose()
    if observer.boundary_failure:
        raise ValueError("real agent encountered a Catalog/observation boundary failure")
    if observer.model_boundary_failure:
        raise ValueError("owned reasoning endpoint encountered an HTTP/transport/protocol failure")
    if error == "agent_failed" and observer.model_timeout:
        error = "inference_deadline_exceeded"
    mapping = fixture.by_release
    evidence = {"searched": discovery.searched, "incomplete": discovery.incomplete,
                "found": sorted(mapping[i] for i in discovery.seen), "read": sorted(mapping[i] for i in discovery.records),
                "catalog_requests": discovery.requests, "tool_calls": observer.tool_calls, "tool_refusals": observer.refusals,
                "model_calls": observer.model_calls, "inference_http_attempts": observer.inference_http_attempts,
                "tokens": observer.token_usage if observer.usage_calls == observer.model_calls else None, "seconds": time.perf_counter()-started,
                "sdk_calls": observer.calls}
    evidence["shortened_descriptions"] = sorted({mapping[item["local"]["release"]["id"]]
                                                for call in observer.calls if call["operation"] == "search" and "response" in call
                                                for item in call["response"]["items"] if len(item.get("description") or "") > 500})
    recommendations = [] if result is None else [{"release_id": mapping[r["release_id"]], "reason": r["reason"]} for r in result["recommendations"]]
    row = protocol.episode(task, evidence, recommendations, error)
    row["recommendation_reasons"] = recommendations
    return row


def verify_model(fixture, pin):
    check_local_model(fixture.reasoner_origin, pin["model"], pin["digest"])


class ScriptedModel:
    """Deterministic observation control, never reported as reasoning quality."""

    def bind_tools(self, tools):
        return self

    async def ainvoke(self, messages, **kwargs):
        from langchain_core.messages import AIMessage
        tools = [m for m in messages if m.type == "tool"]
        if not tools:
            return AIMessage(content="", tool_calls=[{"name": "search_catalog", "args": {"query": "Invoice reconciliation", "kinds": []}, "id": "search", "type": "tool_call"}])
        if len(tools) == 1:
            page = json.loads(tools[-1].content)
            if not page["items"]:
                return AIMessage(content=json.dumps({"summary": "No records returned", "recommendations": []}))
            return AIMessage(content="", tool_calls=[{"name": "read_catalog_record", "args": {"release_id": page["items"][0]["release_id"]}, "id": "read", "type": "tool_call"}])
        record = json.loads(tools[-1].content)
        return AIMessage(content=json.dumps({"summary": "Scripted instrumentation control", "recommendations": [{"release_id": record["release_id"], "reason": "Inspected actual Catalog metadata"}]}))
