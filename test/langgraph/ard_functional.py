"""Actual ARD graph, HTTPS, installed entry point and owned Catalog publication."""

import asyncio
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import time

from ard_client import ARDClient, Service, Settings, SUBJECT, DEADLINE
from ard_agent import ARDTools, ScriptedModel, run_ard_agent


async def main():
    fixture = json.loads(sys.argv[1])
    os.environ.update(
        ARD_PRIMARY="reader",
        ARD_PEER="peer-reader",
        ARD_INVALID="invalid-token",
        # Ambient proxy/CA settings must not replace destination-specific inputs.
        HTTPS_PROXY=fixture["unapproved"],
        HTTP_PROXY=fixture["unapproved"],
        ALL_PROXY=fixture["unapproved"],
        NO_PROXY="",
        SSL_CERT_FILE=fixture["peerCA"],
    )
    primary = Service(
        search_url=fixture["primary"] + "/ard/v1/search",
        explore_url=fixture["primary"] + "/ard/v1/explore",
        token_env="ARD_PRIMARY",
        ca_file=fixture["primaryCA"],
    )
    peer = Service(
        search_url=fixture["peer"], token_env="ARD_PEER", ca_file=fixture["peerCA"]
    )
    expected = {row["identifier"]: row for row in fixture["entries"]}
    client = ARDClient(Settings(primary=primary))
    result = await client.search("web")
    assert result["status"] == "complete" and len(result["results"]) == 13
    assert len({row["identifier"] for row in result["results"]}) == 13
    for row in result["results"]:
        assert row["type"] == expected[row["identifier"]]["type"]
        assert row["version"] == "1.0.0"
        assert row["responder"] == primary.search_url
        assert row["source"] == fixture["primary"] + "/ard/v1/"
    assert client.requests == 2
    explored = await client.explore("web")
    assert explored["status"] == "complete"
    assert explored["facets"][SUBJECT]["buckets"] == [
        {"value": "developer tools", "count": 13}
    ], explored["facets"][SUBJECT]
    expected_types = {}
    for row in expected.values():
        expected_types[row["type"]] = expected_types.get(row["type"], 0) + 1
    assert {
        row["value"]: row["count"] for row in explored["facets"]["type"]["buckets"]
    } == expected_types
    assert client.requests == 3
    skill_type = 'text/markdown; profile="urn:air:agent-skills"'
    filtered = await client.search("web", [skill_type], ["developer tools"])
    assert filtered["status"] == "complete"
    assert {row["identifier"] for row in filtered["results"]} == {
        key for key, value in expected.items() if value["type"] == skill_type
    }
    alternatives = await client.search(
        "web",
        ["application/absent", skill_type],
        ["absent category", "developer tools"],
    )
    assert alternatives["status"] == "complete"
    assert len(alternatives["results"]) == 11 and {
        row["identifier"] for row in alternatives["results"]
    } == {row["identifier"] for row in filtered["results"]}
    filtered_facets = await client.explore("web", [skill_type], ["developer tools"])
    assert filtered_facets["facets"]["type"]["buckets"] == [
        {"value": skill_type, "count": 11}
    ]
    empty = await client.search("web", ["application/contradictory-type"])
    assert empty["status"] == "complete" and empty["results"] == []
    empty_facets = await client.explore("web", ["application/contradictory-type"])
    assert all(value["buckets"] == [] for value in empty_facets["facets"].values())
    missing_category = await client.search("web", categories=["absent category"])
    assert (
        missing_category["status"] == "complete" and missing_category["results"] == []
    )
    empty_graph = await run_ard_agent(
        ScriptedModel("web", ["application/contradictory-type"]),
        ARDTools(ARDClient(Settings(primary=primary))),
        "web",
    )
    assert (
        empty_graph["status"] == "complete"
        and empty_graph["recommendations"] == []
        and empty_graph["observed_results"] == 0
    )
    print(
        "Actual Catalog ARD: 13 exact entries, two pages, media types, versions, type/topic facets and contradictory-filter emptiness passed."
    )

    approved = Settings(primary=primary, referral=peer, disclose_to_referral=True)
    graph_client = ARDClient(approved)
    discovery = ARDTools(graph_client)
    graph = await run_ard_agent(ScriptedModel("web"), discovery, "web")
    assert graph["observed_results"] == 15 and graph_client.requests == 4
    assert graph["status"] == "partial" and graph["trust"] == "not_verified"
    assert all(value["identifier"] in expected for value in graph["recommendations"])
    remote = [
        value for value in discovery.records.values() if value["service"] == "referral"
    ]
    assert len(remote) == 2 and remote[0]["identifier"] == remote[1]["identifier"]
    assert "version" not in remote[0] and remote[1]["version"] == ""
    assert "type" not in remote[0]
    assert remote[0]["source"] == "https://claimed-source.test/registry"
    assert (
        remote[0]["responder"] == fixture["peer"]
        and remote[0]["trustStatus"] == "unverified"
    )
    assert any(
        value["reason"] == "not_approved"
        and value["url"].startswith(fixture["unapproved"])
        for observation in graph["observations"]
        for value in observation.get("omitted_referrals", [])
    )
    forwarded = await ARDClient(approved).search(
        "filtered", ["text/markdown"], ["developer tools"]
    )
    assert len(forwarded["results"]) == 2 and all(
        row["service"] == "referral" for row in forwarded["results"]
    )
    withheld = ARDClient(Settings(primary=primary, referral=peer))
    result = await withheld.search("web")
    assert (
        result["status"] == "complete"
        and len(result["results"]) == 13
        and withheld.requests == 2
    )
    recursive = await ARDClient(approved).search("recursive")
    assert any(
        value["reason"] == "hop_limit" for value in recursive["omitted_referrals"]
    )
    pages = ARDClient(approved)
    result = await pages.search("peer-pages")
    assert pages.requests == 3
    assert (
        len(result["results"]) == 2
        and "version" not in result["results"][0]
        and result["results"][1]["version"] == ""
    )
    print(
        "Actual LangGraph plus two HTTPS services: exact approved referral, separate credentials, sparse variants, opaque continuation and explicit omissions passed."
    )

    # A service without an explicit private CA must not inherit one from the
    # shell, even when that ambient bundle happens to trust the exact server.
    os.environ["SSL_CERT_FILE"] = fixture["primaryCA"]
    try:
        no_private_trust = primary.model_copy(update={"ca_file": None})
        denied = await ARDClient(Settings(primary=no_private_trust)).search("web")
        assert denied["status"] == "failed" and denied["results"] == [], (
            "Ambient CA unexpectedly authorized private TLS"
        )
    finally:
        os.environ["SSL_CERT_FILE"] = fixture["peerCA"]

    for selected in [
        primary.model_copy(update={"token_env": "ARD_INVALID"}),
        primary.model_copy(update={"ca_file": fixture["peerCA"]}),
    ]:
        denied = await ARDClient(Settings(primary=selected)).search("web")
        assert denied["status"] == "failed" and denied["results"] == []
    peer_only = Service(
        search_url=fixture["peer"],
        explore_url=fixture["peer"],
        ca_file=fixture["peerCA"],
        token_env="ARD_PEER",
    )
    for query, code in [
        ("redirect", "redirect_refused"),
        ("oversized", "response_too_large"),
        ("unavailable", "service_unavailable"),
    ]:
        result = await ARDClient(Settings(primary=peer_only)).search(query)
        assert result["status"] == "failed" and result["issues"][0]["code"] == code
    wrong_credential = peer_only.model_copy(update={"token_env": "ARD_PRIMARY"})
    result = await ARDClient(Settings(primary=wrong_credential)).search("web")
    assert (
        result["status"] == "failed"
        and result["issues"][0]["code"] == "unauthenticated"
    )
    result = await ARDClient(Settings(primary=peer_only)).search("page-failure")
    assert (
        result["status"] == "partial"
        and len(result["results"]) == 1
        and result["issues"][0]["code"] == "service_unavailable"
    )
    drip = ARDClient(Settings(primary=peer_only))
    drip.started = time.monotonic() - DEADLINE + 0.5
    result = await drip.search("drip")
    assert (
        result["status"] == "failed"
        and result["issues"][0]["code"] == "deadline_exceeded"
    )
    recovered = await ARDClient(Settings(primary=primary)).search("web")
    assert recovered["status"] == "complete" and len(recovered["results"]) == 13
    print(
        "Actual TLS/auth refusal, ambient proxy/CA isolation, no redirect following, byte limit, whole-call drip deadline, late-page failure and fresh explicit recovery passed."
    )

    root = Path(__file__).resolve().parents[2]
    with tempfile.TemporaryDirectory(prefix="catalog-ard-graph-") as directory:
        config = Path(directory) / "services.json"
        config.write_text(Settings(primary=primary).model_dump_json(exclude_none=True))
        env = dict(
            os.environ,
            LANGGRAPH_ARD_CONFIG=str(config),
            LANGGRAPH_ARD_QUERY="web",
            LANGSMITH_TRACING="true",
            LANGCHAIN_TRACING_V2="true",
            LANGSMITH_ENDPOINT=fixture["unapproved"],
            LANGSMITH_API_KEY="must-not-leave",
        )
        command = [
            "make",
            "--no-print-directory",
            "-s",
            "langgraph-ard-demo",
            "CATALOG_ENV_FILE=/dev/null",
        ]
        cli = [sys.executable, "-B", str(root / "examples/langgraph/ard_agent.py")]
        completed = subprocess.run(
            command,
            cwd=root,
            env=env,
            capture_output=True,
            text=True,
            timeout=30,
            check=False,
        )
        assert completed.returncode == 0, "Actual ARD Make/CLI failed"
        value = json.loads(completed.stdout)
        assert value["status"] == "complete" and value["observed_results"] == 13
        assert all(row["identifier"] in expected for row in value["recommendations"])
        assert (
            "Bearer reader" not in completed.stdout
            and "peer-reader" not in completed.stdout
            and "must-not-leave" not in completed.stdout
        )
        config.write_text(approved.model_dump_json(exclude_none=True))
        partial = subprocess.run(
            command,
            cwd=root,
            env=env,
            capture_output=True,
            text=True,
            timeout=30,
            check=False,
        )
        assert (
            partial.returncode == 2
            and json.loads(partial.stdout)["status"] == "partial"
        )
        config.write_text(
            Settings(
                primary=primary.model_copy(update={"token_env": "ARD_INVALID"})
            ).model_dump_json(exclude_none=True)
        )
        failed = subprocess.run(
            cli,
            cwd=root,
            env=env,
            capture_output=True,
            text=True,
            timeout=30,
            check=False,
        )
        assert failed.returncode == 1 and failed.stderr == ""
        value = json.loads(failed.stdout)
        assert (
            value["status"] == "failed"
            and value["observed_results"] == 0
            and value["recommendations"] == []
        )
        assert len(value["observations"]) == 2
        assert all(
            observation["issues"] == [{"service": "primary", "code": "unauthenticated"}]
            for observation in value["observations"]
        )
        make_failed = subprocess.run(
            command,
            cwd=root,
            env=env,
            capture_output=True,
            text=True,
            timeout=30,
            check=False,
        )
        assert (
            make_failed.returncode == 2
            and json.loads(make_failed.stdout)["status"] == "failed"
        )
        config.write_text(
            json.dumps(
                {
                    "primary": primary.model_dump(exclude_none=True),
                    "token": "MUST-NOT-PRINT-PRIVATE-CONFIG",
                }
            )
        )
        invalid = subprocess.run(
            cli,
            cwd=root,
            env=env,
            capture_output=True,
            text=True,
            timeout=30,
            check=False,
        )
        assert invalid.returncode == 1 and invalid.stdout == ""
        assert invalid.stderr == "ARD discovery failed: invalid_configuration\n"
        assert all(
            secret not in invalid.stderr
            for secret in [
                "MUST-NOT-PRINT-PRIVATE-CONFIG",
                "must-not-leave",
                str(config),
            ]
        )
    print(
        "Actual model-free Make entry point: complete/partial/failed exit codes, safe configuration diagnostics, fact binding and hosted tracing disabled passed; owned private input cleanup passed."
    )


asyncio.run(main())
