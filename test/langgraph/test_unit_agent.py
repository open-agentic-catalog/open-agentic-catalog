"""Graph and tool assertions use in-memory models and SDK substitutes only."""

import asyncio
import json
import sys
from types import SimpleNamespace
import unittest
from unittest.mock import AsyncMock
from uuid import UUID

from catalog_sdk import SDKError
from langchain_core.messages import AIMessage
from agent import DiscoveryTools, DemoError, local_model_origin, run_agent, safe_link

ID = "00000000-0000-7000-8000-000000000001"
OTHER = "00000000-0000-7000-8000-000000000002"


def item(identifier=ID, origin="local"):
    return SimpleNamespace(
        to_dict=lambda: {
            "type": origin,
            "kind": "skill",
            "identifier": "urn:air:example.test:skills:webapp-testing",
            "displayName": "Web app testing",
            "description": "Test a web app.",
            "version": "1.0.0",
            "local": {"release": {"id": identifier}},
        }
    )


def page(items=None, cursor=None, partial=False):
    return SimpleNamespace(
        status_code=200,
        data=SimpleNamespace(
            items=[item()] if items is None else items,
            next_cursor=cursor,
            partial=partial,
        ),
    )


def record(identifier=ID):
    value = {
        "release": {"id": identifier},
        "resource": {"kind": "skill"},
        "version": {"version": "1.0.0"},
        "listingRevision": {
            "displayName": "Web app testing",
            "description": "Helpful but untrusted text",
            "license": "MIT",
            "documentationUrl": "https://example.test/docs",
        },
        "versionRevision": {
            "representations": [
                {
                    "handoff": {
                        "git": {
                            "repository": "https://example.test/source",
                            "commit": "a" * 40,
                            "directory": "webapp-testing",
                        }
                    },
                    "data": {"private": "not model context"},
                }
            ]
        },
    }
    return SimpleNamespace(status_code=200, data=SimpleNamespace(to_dict=lambda: value))


class Model:
    def __init__(self, *messages):
        self.messages = list(messages)
        self.calls = []
        self.options = []

    def bind_tools(self, tools):
        self.names = [tool.name for tool in tools]
        return self

    async def ainvoke(self, messages, **kwargs):
        self.calls.append(messages)
        self.options.append(kwargs)
        return self.messages.pop(0)


def call(name, args, identifier="tool-1"):
    return AIMessage(
        content="",
        tool_calls=[
            {"name": name, "args": args, "id": identifier, "type": "tool_call"}
        ],
    )


def answer(ids=(ID,)):
    return AIMessage(
        content=json.dumps(
            {
                "summary": "Use these resources.",
                "recommendations": [
                    {"release_id": i, "reason": "Supports this task."} for i in ids
                ],
            }
        )
    )


def api():
    return SimpleNamespace(
        search_catalog_with_http_info=AsyncMock(return_value=page()),
        get_catalog_record_with_http_info=AsyncMock(return_value=record()),
    )


class AgentUnits(unittest.IsolatedAsyncioTestCase):
    async def test_real_graph_uses_only_search_and_exact_record_tools(self):
        client = api()
        discovery = DiscoveryTools(client)
        model = Model(
            call("search_catalog", {"query": "web", "kinds": ["skill"]}),
            call("read_catalog_record", {"release_id": ID}, "tool-2"),
            answer(),
        )
        result = await run_agent(model, discovery, "Help test a web app")
        self.assertEqual(model.names, ["search_catalog", "read_catalog_record"])
        self.assertEqual(result["recommendations"][0]["name"], "Web app testing")
        self.assertEqual(
            result["recommendations"][0]["git_handoffs"][0]["commit"], "a" * 40
        )
        self.assertNotIn("not model context", str(model.calls))
        request = client.search_catalog_with_http_info.call_args.kwargs[
            "catalog_search"
        ]
        self.assertEqual(request.origins, ["local"])
        self.assertEqual(request.kinds, ["skill"])
        self.assertEqual(request.limit, 10)
        self.assertIsNone(request.cursor)
        client.get_catalog_record_with_http_info.assert_awaited_once_with(
            catalog_record_id=UUID(ID)
        )
        self.assertFalse(result["search_incomplete"])
        self.assertEqual(model.options[0], {})
        self.assertEqual(
            model.options[-1]["format"]["required"], ["summary", "recommendations"]
        )

    async def test_cursor_and_namespace_are_fixed_with_explicit_incomplete_results(
        self,
    ):
        client = api()
        client.search_catalog_with_http_info.side_effect = [
            page([], "first"),
            page([item()], "more", True),
        ]
        discovery = DiscoveryTools(client, UUID(OTHER))
        result = await discovery.tools()[0].ainvoke({"query": "web"})
        self.assertTrue(result["partial"])
        self.assertTrue(result["more_available"])
        requests = [
            c.kwargs["catalog_search"]
            for c in client.search_catalog_with_http_info.call_args_list
        ]
        self.assertEqual([q.cursor for q in requests], [None, "first"])
        for q in requests:
            self.assertEqual(q.namespace_id, UUID(OTHER))
            self.assertEqual(q.origins, ["local"])
            self.assertEqual(q.limit, 10)

    def test_kind_schema_is_a_simple_optional_array_for_local_models(self):
        schema = DiscoveryTools(api()).tools()[0].args_schema.model_json_schema()
        self.assertEqual(schema["properties"]["kinds"]["type"], "array")
        self.assertEqual(
            schema["properties"]["kinds"]["items"]["enum"],
            ["agent", "mcp_server", "skill", "plugin"],
        )
        self.assertEqual(schema["required"], ["query"])

    async def test_each_kind_maps_to_the_sdk_without_model_control_of_authority(self):
        for kind in ("agent", "mcp_server", "skill", "plugin"):
            client = api()
            await (
                DiscoveryTools(client, UUID(OTHER))
                .tools()[0]
                .ainvoke({"query": "web", "kinds": [kind]})
            )
            query = client.search_catalog_with_http_info.call_args.kwargs[
                "catalog_search"
            ]
            self.assertEqual(query.kinds, [kind])
            self.assertEqual(query.namespace_id, UUID(OTHER))
            self.assertEqual(query.origins, ["local"])

    async def test_empty_results_are_successful_without_a_record_lookup(self):
        client = api()
        client.search_catalog_with_http_info.return_value = page([])
        model = Model(call("search_catalog", {"query": "absent"}), answer(()))
        result = await run_agent(
            model, DiscoveryTools(client), "Find missing resources"
        )
        self.assertEqual(result["recommendations"], [])
        client.get_catalog_record_with_http_info.assert_not_awaited()

    async def test_unselected_or_fabricated_recommendations_do_not_trigger_reads(self):
        client = api()
        discovery = DiscoveryTools(client)
        with self.assertRaisesRegex(DemoError, "select_a_search_result"):
            await discovery.tools()[1].ainvoke({"release_id": OTHER})
        client.get_catalog_record_with_http_info.assert_not_awaited()
        model = Model(call("search_catalog", {"query": "web"}), answer((ID,)))
        with self.assertRaisesRegex(DemoError, "model_recommended_unread_resource"):
            await run_agent(model, discovery, "Find resources")

    async def test_auth_refusal_stops_followup_requests_and_hides_dependency_details(
        self,
    ):
        for status, code in [
            (401, "catalog_unauthenticated"),
            (403, "catalog_forbidden"),
            (503, "catalog_unavailable"),
        ]:
            with self.subTest(status=status):
                client = api()
                client.search_catalog_with_http_info.return_value = SimpleNamespace(
                    status_code=status, data="PRIVATE_BODY"
                )
                discovery = DiscoveryTools(client)
                model = Model(call("search_catalog", {"query": "web"}), answer(()))
                with self.assertRaisesRegex(DemoError, code):
                    await run_agent(model, discovery, "Find tools")
                self.assertEqual(len(model.calls), 1)
                with self.assertRaises(DemoError):
                    await discovery.tools()[0].ainvoke({"query": "again"})
                self.assertEqual(client.search_catalog_with_http_info.await_count, 1)

    async def test_sdk_transport_errors_and_withdrawn_records_are_terminal(self):
        client = api()
        client.search_catalog_with_http_info.side_effect = SDKError("transport_failed")
        discovery = DiscoveryTools(client)
        with self.assertRaisesRegex(DemoError, "catalog_transport_failed"):
            await discovery.tools()[0].ainvoke({"query": "web"})
        client = api()
        discovery = DiscoveryTools(client)
        discovery.seen.add(ID)
        client.get_catalog_record_with_http_info.return_value = SimpleNamespace(
            status_code=404
        )
        with self.assertRaisesRegex(DemoError, "catalog_record_unavailable"):
            await discovery.tools()[1].ainvoke({"release_id": ID})

    async def test_unexpected_origin_record_mismatch_and_repeated_cursor_fail(self):
        client = api()
        client.search_catalog_with_http_info.return_value = page(
            [item(origin="indexed")]
        )
        with self.assertRaisesRegex(DemoError, "unexpected_catalog_origin"):
            await DiscoveryTools(client).tools()[0].ainvoke({"query": "web"})
        client = api()
        client.search_catalog_with_http_info.return_value = page([], "repeat")
        with self.assertRaisesRegex(DemoError, "invalid_continuation"):
            await DiscoveryTools(client).tools()[0].ainvoke({"query": "web"})
        client = api()
        client.get_catalog_record_with_http_info.return_value = record(OTHER)
        discovery = DiscoveryTools(client)
        discovery.seen.add(ID)
        with self.assertRaisesRegex(DemoError, "catalog_record_mismatch"):
            await discovery.tools()[1].ainvoke({"release_id": ID})

    async def test_query_and_request_limits_reject_before_network(self):
        client = api()
        discovery = DiscoveryTools(client)
        for query in ("x" * 501, "\0", "\ud800"):
            with self.assertRaisesRegex(DemoError, "invalid_search"):
                await discovery.tools()[0].ainvoke({"query": query})
        client.search_catalog_with_http_info.assert_not_awaited()
        discovery.requests = 16
        with self.assertRaisesRegex(DemoError, "request_budget_exhausted"):
            await discovery.tools()[0].ainvoke({"query": "valid"})
        client.search_catalog_with_http_info.assert_not_awaited()

    async def test_model_validation_deadline_and_cancellation(self):
        with self.assertRaisesRegex(DemoError, "model_did_not_search"):
            await run_agent(Model(answer(())), DiscoveryTools(api()), "Find tools")
        for question, timeout in [
            ("", 120),
            ("x" * 2001, 120),
            ("ok", 0),
            ("ok", float("nan")),
            ("\ud800", 120),
        ]:
            with self.assertRaisesRegex(DemoError, "invalid_input"):
                await run_agent(Model(), DiscoveryTools(api()), question, timeout)

        class Waiting(Model):
            async def ainvoke(self, messages, **kwargs):
                await asyncio.Future()

        with self.assertRaisesRegex(DemoError, "deadline_exceeded"):
            await run_agent(Waiting(), DiscoveryTools(api()), "Find tools", 0.001)
        task = asyncio.create_task(
            run_agent(Waiting(), DiscoveryTools(api()), "Find tools")
        )
        await asyncio.sleep(0)
        task.cancel()
        with self.assertRaises(asyncio.CancelledError):
            await task

    def test_model_origin_and_display_links_are_safe(self):
        self.assertEqual(
            local_model_origin("http://127.0.0.1:11434/"), "http://127.0.0.1:11434"
        )
        for url in (
            "https://public.test",
            "http://user@127.0.0.1",
            "http://127.0.0.1/?",
            "http://127.0.0.1/#",
            "http://127.0.0.1/mcp",
        ):
            with self.assertRaises(DemoError):
                local_model_origin(url)
        self.assertEqual(
            safe_link("https://example.test/docs"), "https://example.test/docs"
        )
        for url in (
            None,
            "https://user:secret@example.test",
            "javascript:alert(1)",
            "https://[invalid",
            "https://example.test/\n",
            "x" * 2049,
        ):
            self.assertIsNone(safe_link(url))
        for budget in (0, 4, True):
            with self.assertRaises(DemoError):
                DiscoveryTools(api(), max_pages=budget)


class ModelBoundaryUnits(unittest.IsolatedAsyncioTestCase):
    async def test_final_answer_rejects_wrong_shape_and_duplicate_recommendations(self):
        for message, code in [
            (AIMessage(content="not JSON"), "agent_failed"),
            (
                AIMessage(content='{"summary":"ok","recommendations":[],"extra":true}'),
                "agent_failed",
            ),
            (answer((ID, ID)), "model_recommended_unread_resource"),
        ]:
            client = api()
            model = Model(
                call("search_catalog", {"query": "web"}),
                call("read_catalog_record", {"release_id": ID}, "read"),
                message,
            )
            with self.assertRaisesRegex(DemoError, code):
                await run_agent(model, DiscoveryTools(client), "Find web testing")

    async def test_fenced_model_json_is_valid_but_truncation_is_not_repaired(self):
        for content, valid in [
            ("Chosen resource:\n```json\n" + answer().content + "\n```", True),
            ('```json\n{"summary":"broken","recommendations":[', False),
        ]:
            model = Model(
                call("search_catalog", {"query": "web"}),
                call("read_catalog_record", {"release_id": ID}, "read"),
                AIMessage(content=content),
            )
            if valid:
                result = await run_agent(
                    model, DiscoveryTools(api()), "Find web testing"
                )
                self.assertEqual(result["recommendations"][0]["release_id"], ID)
            else:
                with self.assertRaisesRegex(DemoError, "agent_failed"):
                    await run_agent(model, DiscoveryTools(api()), "Find web testing")

    async def test_unending_model_reaches_graph_limit_with_bounded_requests(self):
        class Repeating(Model):
            async def ainvoke(self, messages, **kwargs):
                return call("search_catalog", {"query": "web"}, str(len(messages)))

        discovery = DiscoveryTools(api())
        with self.assertRaisesRegex(DemoError, "model_turn_limit_exceeded"):
            await run_agent(Repeating(), discovery, "Find web testing")
        self.assertLessEqual(discovery.requests, 7)

    def test_model_origin_rejects_invalid_ports_and_hidden_delimiters(self):
        for value in (
            "http://127.0.0.1:0",
            "http://127.0.0.1:65536",
            "http://127.0.0.1:invalid",
            " http://127.0.0.1:11434",
            "http://127.0.0.1:11434\n",
            "http://@127.0.0.1",
            "http://127.0.0.1\\evil",
            "http://[invalid",
        ):
            with self.subTest(value=value), self.assertRaises(DemoError):
                local_model_origin(value)
        self.assertEqual(
            local_model_origin("http://[::1]:11434/"), "http://[::1]:11434"
        )

    def test_local_preflight_bounds_invalid_responses_and_transport_errors(self):
        from unittest.mock import MagicMock, patch
        from agent import check_local_model

        for body in (b"{", b"x" * (1024 * 1024 + 1), OSError("PRIVATE_DEPENDENCY")):
            connection = MagicMock()
            response = connection.getresponse.return_value
            response.status = 200
            response.read.side_effect = body if isinstance(body, Exception) else [body]
            with (
                patch("agent.http.client.HTTPConnection", return_value=connection),
                self.assertRaisesRegex(DemoError, "local_model_unavailable"),
            ):
                check_local_model("http://127.0.0.1:11434", "qwen2.5:7b")
            connection.close.assert_called_once()

    def test_local_preflight_refuses_remote_missing_or_unpinned_models_and_closes(self):
        from unittest.mock import MagicMock, patch
        from agent import check_local_model

        for value, status, expected, digest in [
            ({"capabilities": ["tools"]}, 200, None, None),
            (
                {"remote_host": "https://remote.test", "capabilities": ["tools"]},
                200,
                "local_model_required",
                None,
            ),
            (
                {"capabilities": ["completion"]},
                200,
                "tool_calling_model_required",
                None,
            ),
            ({}, 404, "local_model_unavailable", None),
            ({"capabilities": ["tools"]}, 200, "local_model_identity_changed", "wrong"),
        ]:
            connection = MagicMock()
            response = connection.getresponse.return_value
            response.status = status
            response.read.side_effect = [json.dumps(value).encode(), b'{"models":[]}']
            with (
                self.subTest(expected=expected),
                patch("agent.http.client.HTTPConnection", return_value=connection),
            ):
                if expected:
                    with self.assertRaisesRegex(DemoError, expected):
                        check_local_model(
                            "http://127.0.0.1:11434", "qwen2.5:7b", digest
                        )
                else:
                    check_local_model("http://127.0.0.1:11434", "qwen2.5:7b")
            connection.close.assert_called_once()


class CLIUnits(unittest.IsolatedAsyncioTestCase):
    async def test_cli_uses_explicit_settings_and_prints_sdk_bound_facts_without_credentials(
        self,
    ):
        import io
        import os
        from unittest.mock import patch
        import agent

        client = api()
        model = Model(
            call("search_catalog", {"query": "web"}),
            call("read_catalog_record", {"release_id": ID}, "read"),
            answer(),
        )
        env = {
            "CATALOG_SDK_ORIGIN": "http://127.0.0.1:8080",
            "CATALOG_SDK_TOKEN": "PRIVATE_TOKEN",
            "LANGGRAPH_ALLOW_LOOPBACK_HTTP": "1",
            "LANGGRAPH_QUESTION": "Find web testing",
            "LANGGRAPH_MODEL_ORIGIN": "http://127.0.0.1:11434",
            "LANGGRAPH_MODEL": "qwen2.5:7b",
            "LANGGRAPH_NAMESPACE_ID": OTHER,
        }
        output = io.StringIO()
        with (
            patch.dict(os.environ, env, clear=True),
            patch.object(sys, "argv", ["agent.py"]),
            patch("agent.CatalogDiscoveryApi", return_value=client),
            patch("agent.check_local_model"),
            patch("langchain_ollama.ChatOllama", return_value=model) as constructor,
            patch("sys.stdout", output),
        ):
            await agent.main()
        value = json.loads(output.getvalue())
        self.assertEqual(value["recommendations"][0]["release_id"], ID)
        self.assertNotIn("PRIVATE_TOKEN", output.getvalue())
        self.assertEqual(
            constructor.call_args.kwargs["base_url"], "http://127.0.0.1:11434"
        )
        self.assertEqual(
            constructor.call_args.kwargs["client_kwargs"]["trust_env"], False
        )
        request = client.search_catalog_with_http_info.call_args.kwargs[
            "catalog_search"
        ]
        self.assertEqual(request.namespace_id, UUID(OTHER))

    async def test_cli_refuses_invalid_configuration_before_any_model_constructor(self):
        import os
        import sys
        from unittest.mock import patch
        import agent

        for env, code in [
            ({"LANGGRAPH_ALLOW_LOOPBACK_HTTP": "yes"}, "invalid_configuration"),
            (
                {"LANGGRAPH_MODEL": "remote:cloud", "LANGGRAPH_QUESTION": "Find"},
                "local_model_required",
            ),
            ({}, "question_required"),
        ]:
            with (
                self.subTest(code=code),
                patch.dict(os.environ, env, clear=True),
                patch.object(sys, "argv", ["agent.py"]),
                patch("langchain_ollama.ChatOllama") as constructor,
            ):
                with self.assertRaisesRegex(DemoError, code):
                    await agent.main()
                constructor.assert_not_called()


class EntryUnits(unittest.TestCase):
    def test_exit_classification_omits_raw_error_text(self):
        import io
        from unittest.mock import patch
        from agent import entry

        for failure, code, expected in [
            (None, 0, ""),
            (DemoError("deadline_exceeded"), 1, "deadline_exceeded"),
            (SDKError("invalid_token"), 1, "invalid_token"),
            (KeyboardInterrupt(), 130, "interrupted"),
            (ValueError("PRIVATE_SETTING"), 1, "invalid_configuration"),
            (RuntimeError("PRIVATE_DEPENDENCY"), 1, "agent_failed"),
        ]:

            async def outcome():
                if failure is not None:
                    raise failure

            output = io.StringIO()
            with (
                self.subTest(code=code, expected=expected),
                patch("agent.main", outcome),
                patch("sys.stderr", output),
            ):
                self.assertEqual(entry(), code)
                self.assertIn(expected, output.getvalue())
                self.assertNotIn("PRIVATE_", output.getvalue())


if __name__ == "__main__":
    unittest.main()
