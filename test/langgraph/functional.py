"""Real Catalog HTTP/SDK with the actual graph; scripted and local models are distinct."""

import asyncio
import json
import os
import sys
import subprocess
from pathlib import Path
from uuid import UUID

from catalog_sdk import Client, Config
from catalog_sdk.generated.api.catalog_discovery_api import CatalogDiscoveryApi
from langchain_core.messages import AIMessage
from agent import (
    DiscoveryTools,
    DemoError,
    run_agent,
    check_local_model,
)


class ScriptedModel:
    def bind_tools(self, tools):
        self.names = [t.name for t in tools]
        return self

    async def ainvoke(self, messages, **kwargs):
        tool_messages = [m for m in messages if m.type == "tool"]
        if not tool_messages:
            return AIMessage(
                content="",
                tool_calls=[
                    {
                        "name": "search_catalog",
                        "args": {"query": "web", "kinds": ["skill"]},
                        "id": "search",
                        "type": "tool_call",
                    }
                ],
            )
        if len(tool_messages) == 1:
            page = json.loads(tool_messages[-1].content)
            identifier = page["items"][0]["release_id"]
            return AIMessage(
                content="",
                tool_calls=[
                    {
                        "name": "read_catalog_record",
                        "args": {"release_id": identifier},
                        "id": "read",
                        "type": "tool_call",
                    }
                ],
            )
        record = json.loads(tool_messages[-1].content)
        return AIMessage(
            content=json.dumps(
                {
                    "summary": "A governed web testing resource.",
                    "recommendations": [
                        {
                            "release_id": record["release_id"],
                            "reason": "Matches the web testing task.",
                        }
                    ],
                }
            )
        )


async def main():
    origin, expected = sys.argv[1], json.loads(sys.argv[2])
    token = ["reader"]
    client = Client(
        Config(origin=origin, allow_loopback_http=True, token_provider=lambda: token[0])
    )
    api = CatalogDiscoveryApi(client)
    discovery = DiscoveryTools(api, UUID(expected["namespaceId"]))
    result = await run_agent(ScriptedModel(), discovery, "Find a Skill for web testing")
    assert result["recommendations"][0]["release_id"] in expected["releaseIds"]
    assert result["recommendations"][0]["kind"] == "skill"
    assert (
        discovery.requests == 3
    )  # Two unchanged-query Search pages, then exact release read.
    assert len(discovery.seen) == 11
    # Current authentication is checked again; cached selection confers no authority.
    token[0] = "other"
    try:
        await discovery.tools()[1].ainvoke(
            {"release_id": result["recommendations"][0]["release_id"]}
        )
    except DemoError as error:
        assert str(error) == "catalog_record_unavailable"
    else:
        raise AssertionError("cross-tenant read unexpectedly succeeded")
    assert discovery.requests == 4
    denied = DiscoveryTools(
        CatalogDiscoveryApi(
            Client(Config(origin=origin, allow_loopback_http=True, token="bad-token"))
        )
    )
    try:
        await run_agent(ScriptedModel(), denied, "Find web testing")
    except DemoError as error:
        assert str(error) == "catalog_unauthenticated"
    else:
        raise AssertionError("unauthenticated graph unexpectedly succeeded")
    assert denied.requests == 1
    print(
        "Scripted graph + real Catalog SDK HTTP: passed; 13 releases, 11 Skills, two pages, exact read, current-identity and authentication refusals."
    )
    # Validate the real HTTP/SDK fixtures independently before a stochastic
    # model can obscure a malformed fixture as an inference failure.
    for case in expected["modelCases"]:
        control = DiscoveryTools(
            CatalogDiscoveryApi(Client(Config(origin=case["origin"], allow_loopback_http=True, token="reader"))),
            UUID(case["namespaceId"]),
        )
        page = await control.tools()[0].ainvoke({"query": "web", "kinds": []})
        assert {item["release_id"] for item in page["items"]} == set(case["releaseIds"])
        if case["name"] not in ("empty", "withdraw-before-read"):
            record = await control.tools()[1].ainvoke({"release_id": case["releaseIds"][0]})
            assert record["release_id"] == case["releaseIds"][0]
    print("Four owned model-case real Catalog/SDK HTTP fixture controls: passed.")
    model_origin = os.environ.get("CATALOG_LANGGRAPH_MODEL_ORIGIN")
    if model_origin:
        pin = json.loads(Path(__file__).with_name("model.json").read_text())
        assert os.environ["CATALOG_LANGGRAPH_MODEL"] == pin["model"]
        await asyncio.to_thread(
            check_local_model, model_origin, pin["model"], pin["digest"]
        )
        env = dict(os.environ)
        env.update(
            CATALOG_SDK_ORIGIN=origin,
            CATALOG_SDK_TOKEN="reader",
            LANGGRAPH_MODEL_ORIGIN=model_origin,
            LANGGRAPH_MODEL=pin["model"],
            LANGGRAPH_ALLOW_LOOPBACK_HTTP="1",
            LANGGRAPH_NAMESPACE_ID=expected["namespaceId"],
            LANGGRAPH_QUESTION="Find one Skill for web testing. Search the Catalog, inspect one selected record, then recommend it.",
        )
        root = Path(__file__).resolve().parents[2]
        completed = subprocess.run(
            [
                "make",
                "--no-print-directory",
                "-s",
                "langgraph-demo",
                "CATALOG_ENV_FILE=/dev/null",
            ],
            cwd=root,
            env=env,
            capture_output=True,
            text=True,
            timeout=330,
            check=False,
        )
        assert completed.returncode == 0, (
            "Actual example CLI failed: " + completed.stderr
        )
        result = json.loads(completed.stdout)
        assert result["recommendations"] and all(
            r["release_id"] in expected["releaseIds"] and r["kind"] == "skill"
            for r in result["recommendations"]
        )
        assert "reader" not in completed.stdout and "reader" not in completed.stderr
        (root / "output/langgraph").mkdir(parents=True, exist_ok=True)
        (root / "output/langgraph/model-result.json").write_text(
            json.dumps(result, indent=2) + "\n"
        )
        print(
            "Real local-model + actual make langgraph-demo + real Catalog SDK HTTP: passed; recommendations bind inspected governed releases."
        )
        from model_evaluation import evaluate

        await evaluate(expected["modelCases"], model_origin, pin, root / "output/langgraph/model-characterization.json")


if __name__ == "__main__":
    asyncio.run(main())
