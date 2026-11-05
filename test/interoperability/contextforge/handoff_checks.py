"""Pure checks for one owned ContextForge connection. No runtime or SDK imports."""

import hashlib
import json
import re

CARD = "application/mcp-server-card+json"
PROFILE = "mcp-server-card-v1-experimental-526201b"
IDENTIFIER = "urn:air:catalog.example.com:mcp:contextforge"
NAME = "catalog.example.com/contextforge-everything"
VERSION = "1.0.0"
TAG = "contextforge-handoff"
RESOURCE_SHA256 = "36593c6d475378b29c6c43a3256fbfd2cad7b087dcbd3e940d53fa0876a70cd7"


def endpoint(server):
    assert isinstance(server, str) and re.fullmatch(r"[a-f0-9]{32}", server), "owned native virtual-server ID"
    return "https://contextforge:4444/servers/" + server + "/mcp"


def document(server):
    return {"$schema": "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json", "name": NAME, "version": VERSION,
            "description": "Owned ContextForge virtual server for deterministic MCP checks.",
            "remotes": [{"type": "streamable-http", "url": endpoint(server), "supportedProtocolVersions": ["2026-07-28"],
                         "headers": [{"name": "Authorization", "description": "Supply the separate ContextForge runtime bearer credential.", "isRequired": True, "isSecret": True}]}],
            "_meta": {"io.example/qualification": {"serverId": server, "note": "Catalog metadata does not grant runtime access."}}}


def canonical(value):
    raw = json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    assert raw.isascii() and not any(c in raw for c in "<>&"), "reviewed ASCII digest oracle"
    return raw.encode()


def digest(value):
    return "sha256:" + hashlib.sha256(canonical(value)).hexdigest()


def validate_record(record, expected):
    for section in ("resource", "version", "versionRevision", "listing", "listingRevision", "release"):
        assert record[section]["id"] == expected[section], "changed immutable handoff identity: " + section
    assert record["resource"]["kind"] == "mcp_server" and record["resource"]["canonicalIdentifier"] == IDENTIFIER
    assert record["version"]["version"] == VERSION and record["version"]["resourceId"] == expected["resource"]
    assert record["version"]["sealedRevisionId"] == expected["versionRevision"]
    assert record["versionRevision"]["resourceVersionId"] == expected["version"]
    assert record["listingRevision"]["versionRevisionId"] == record["release"]["versionRevisionId"] == expected["versionRevision"]
    assert record["listingRevision"]["listingId"] == record["release"]["listingId"] == expected["listing"]
    assert record["release"]["listingRevisionId"] == expected["listingRevision"]
    assert record["release"]["state"] == "published" and record["listing"]["currentReleaseId"] == expected["release"]
    assert record["versionRevision"]["contentSetDigest"] == record["evidence"]["subjectDigest"] == expected["contentSetDigest"]
    assert record["versionRevision"]["evidenceSetId"] == record["evidence"]["id"] and record["evidence"]["state"] == "complete"
    assert "MCP_SERVER_CARD_EXPERIMENTAL" in {f["code"] for f in record["evidence"]["findings"]}
    representations = record["versionRevision"]["representations"]
    assert len(representations) == 1, "one retained handoff representation"
    representation = representations[0]
    card = document(expected["server"])
    assert (representation["role"], representation["mediaType"], representation["profileId"], representation["validationState"]) == ("primary", CARD, PROFILE, "valid")
    assert representation["data"] == card and representation["contentDigest"] == digest(card)
    assert representation["size"] == len(canonical(card))
    if "representation" in expected:
        assert representation["id"] == expected["representation"]
    if "evidence" in expected:
        assert record["evidence"]["id"] == expected["evidence"]
    return representation


def select_record(search, read, expected):
    page = search["data"]
    assert page["partial"] is False and not page.get("nextCursor") and len(page["items"]) == 1, "one complete CLI selection"
    item = page["items"][0]
    assert item["type"] == "local" and item["kind"] == "mcp_server" and item["versionPresent"] is True and item["version"] == VERSION
    selected = item["local"]["release"]["id"]
    assert selected == expected["release"], "CLI selects the governed release"
    record = read(selected)["data"]
    validate_record(record, expected)
    return item, record


def map_client(record, expected, token):
    validate_record(record, expected)
    assert isinstance(token, str) and 16 <= len(token) <= 16384 and token.isascii() and not any(c.isspace() for c in token), "separate runtime token"
    remote = record["versionRevision"]["representations"][0]["data"]["remotes"][0]
    return {"url": remote["url"], "headers": {"Authorization": "Bearer " + token}}


def validate_runtime(result):
    assert result["echo"] is True and result["resourceSha256"] == RESOURCE_SHA256 and result["credentialsSeparated"] is True
    assert result["promptFirst"] is True, "fresh prompt-first control"
    assert result["protocol"] == "2026-07-28" and result["upstreamProtocols"] == ["2025-11-25"]
    assert result["serverInfo"]["name"] and result["serverInfo"]["version"] == "1.1.0"
    assert result["nativeNames"]["echo"] and result["nativeNames"]["prompt"] and result["nativeNames"]["resource"]
    prompt = result["prompt"]
    assert prompt["status"] == "passed" or prompt["status"] == "peer_limited" and prompt["cause"] in ("private_ca_rejected", "upstream_unauthorized"), "classified native prompt outcome"


def classify_prompt_failure(prompt, name, logs):
    result = dict(prompt)
    if result["status"] == "peer_limited" and result["cause"] == "unclassified":
        assert name and any("Error getting prompt '" + name + "':" in line and ("certificate_verify_failed" in line.lower() or "certificate verify failed" in line.lower())
                            for line in logs.splitlines()), "classify the exact native prompt failure from its service log entry"
        result["cause"] = "private_ca_rejected"
    return result
