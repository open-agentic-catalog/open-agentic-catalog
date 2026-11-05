"""One governed MCP selection and its narrow, non-executing Inspector mapping."""

import copy
import hashlib
import json

from common import Result
from catalog_control import CATALOG_DENIALS, catalog_denial, validate_search_record
import mcp_contract as control
from mcp_contract import client_config

CARD = "application/mcp-server-card+json"
REGISTRY = "application/vnd.mcp.server+json"
CARD_PROFILE = "mcp-server-card-v1-experimental-526201b"
REGISTRY_PROFILE = "mcp-registry-server-1.8.1-f52dc85"
ENDPOINT = "https://127.0.0.1:9443/mcp"
IDENTIFIER = "urn:air:catalog.example.com:mcp:everything"
TAG = "mcp-handoff-qualification"
CASES = ("governance", "cli-discovery", "public-projection", "revision-binding", "metadata-mapping")
MCP_FAILURES = ("wrong-key", "catalog-key", "bearer-forwarding", "untrusted-ca", "unavailable")


def documents():
    """Synthetic deployment descriptors; identities match the unchanged runtime."""
    remote = {"type": "streamable-http", "url": ENDPOINT,
              "headers": [{"name": "X-MCP-Test-Key", "description": "Supplied separately by the local fixture.",
                           "isRequired": True, "isSecret": True}]}
    registry = {"name": "mcp-servers/everything", "version": "2.0.0",
                "description": "Owned local Everything deployment for explicit echo qualification.",
                "remotes": [remote], "_meta": {"io.example/qualification": {"marker": TAG}}}
    card = copy.deepcopy(registry)
    card["$schema"] = "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json"
    card["remotes"][0]["supportedProtocolVersions"] = ["2025-11-25"]
    return card, registry


def representations(card, registry):
    return [{"role": "primary", "mediaType": CARD, "data": card},
            {"role": "distribution", "mediaType": REGISTRY, "data": registry}]


def canonical(document):
    """Independent digest oracle for these ASCII-only, integer-free JSON fixtures."""
    raw = json.dumps(document, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    assert raw.isascii() and not any(c in raw for c in "<>&"), "fixture exceeds the reviewed digest oracle"
    return raw.encode()


def digest(document):
    return "sha256:" + hashlib.sha256(canonical(document)).hexdigest()


def validate_record(record, expected):
    """Bind every immutable reference to setup and exact CLI-selected evidence."""
    for key in ("resource", "version", "versionRevision", "listingRevision", "release"):
        assert record[key]["id"] == expected[key], "changed " + key + " identity"
    assert record["resource"]["canonicalIdentifier"] == IDENTIFIER
    assert record["resource"]["kind"] == "mcp_server"
    assert record["version"]["resourceId"] == record["resource"]["id"]
    assert record["version"]["version"] == "2.0.0"
    assert record["version"]["sealedRevisionId"] == expected["versionRevision"]
    assert record["versionRevision"]["resourceVersionId"] == record["version"]["id"]
    assert record["versionRevision"]["id"] == record["listingRevision"]["versionRevisionId"] == record["release"]["versionRevisionId"]
    assert record["listingRevision"]["id"] == record["release"]["listingRevisionId"]
    assert record["listingRevision"]["listingId"] == record["release"]["listingId"] == expected["listing"]
    assert record["release"]["state"] == "published"
    assert record["listing"]["id"] == expected["listing"] and record["listing"]["currentReleaseId"] == expected["release"]
    assert record["versionRevision"]["contentSetDigest"] == expected["contentSetDigest"] == record["evidence"]["subjectDigest"]
    assert record["evidence"]["state"] == "complete"
    assert record["versionRevision"]["evidenceSetId"] == record["evidence"]["id"]
    assert "MCP_SERVER_CARD_EXPERIMENTAL" in {f["code"] for f in record["evidence"]["findings"]}
    actual = record["versionRevision"]["representations"]
    assert len(actual) == 2
    for data, media, profile, role in zip(documents(), (CARD, REGISTRY), (CARD_PROFILE, REGISTRY_PROFILE), ("primary", "distribution"), strict=True):
        matching = [r for r in actual if r["role"] == role and r["mediaType"] == media]
        assert len(matching) == 1
        representation = matching[0]
        assert representation["profileId"] == profile and representation["validationState"] == "valid"
        assert representation["data"] == data, "retained metadata changed"
        assert representation["contentDigest"] == digest(data) and representation["size"] == len(canonical(data))
    return next(r for r in actual if r["role"] == "primary")


def select_record(search, read, expected):
    """The caller must read the release actually selected by the real CLI search."""
    page = search["data"]
    assert page["partial"] is False and not page.get("nextCursor")
    assert len(page["items"]) == 1, "ambiguous MCP search"
    item = page["items"][0]
    assert item["type"] == "local" and item["kind"] == "mcp_server" and item["versionPresent"] is True
    assert item["version"] == "2.0.0"
    selected = item["local"]["release"]["id"]
    assert selected == expected["release"]
    assert selected != item["local"]["resource"]["id"] and selected != item["id"]
    record = read(selected)["data"]
    validate_record(record, expected)
    validate_search_record(item, record)
    return record


def projection(manifest, record):
    entries = manifest["entries"]
    assert len(entries) == 2, "one release and the Registry self-entry are required"
    matches = [e for e in entries if e["identifier"] == IDENTIFIER]
    assert len(matches) == 1
    entry = matches[0]
    assert entry["metadata"]["catalogReleaseId"] == record["release"]["id"]
    assert entry["type"] == CARD and entry["version"] == "2.0.0"
    assert entry["data"] == documents()[0] and not entry.get("url")
    assert entry["displayName"] == record["listingRevision"]["displayName"]
    assert entry["description"] == record["listingRevision"]["description"]
    assert entry["tags"] == record["listingRevision"]["tags"]
    other = next(e for e in entries if e["identifier"] != IDENTIFIER)
    assert other["type"] == "application/ai-registry+json" and other["metadata"]["catalogReleaseId"] == "registry:self"


def map_client(record, key, *, approved_endpoint=ENDPOINT):
    """Map this remote-only pilot; unknown descriptive metadata stays inert."""
    selected = [r for r in record["versionRevision"]["representations"] if r.get("role") == "primary"]
    assert len(selected) == 1, "exactly one primary representation required"
    representation = selected[0]
    assert (representation["mediaType"], representation["profileId"], representation["validationState"]) == (CARD, CARD_PROFILE, "valid")
    card = representation["data"]
    assert (card["name"], card["version"]) == ("mcp-servers/everything", "2.0.0")
    remotes = card.get("remotes", [])
    assert len(remotes) == 1, "one literal remote required"
    remote = remotes[0]
    assert remote["type"] == "streamable-http", "unsupported handoff transport"
    assert remote["url"] == approved_endpoint and approved_endpoint == ENDPOINT, "unapproved endpoint"
    assert not remote.get("variables") and "{" not in remote["url"], "unresolved URL template"
    assert remote.get("supportedProtocolVersions") == ["2025-11-25"], "unsupported handoff protocol"
    headers = remote.get("headers", [])
    assert len(headers) == 1, "unsupported header inputs"
    header = headers[0]
    assert header["name"] == "X-MCP-Test-Key" and header.get("isRequired") is True and header.get("isSecret") is True
    assert not any(field in header for field in ("value", "default", "variables", "choices")), "preset/template credential input"
    assert header.get("format", "string") == "string"
    assert isinstance(key, str) and len(key) >= 32 and key.isascii() and key.isalnum()
    config = client_config()
    config["mcpServers"]["handoff"].update(url=remote["url"], headers={header["name"]: key})
    return config


def check_cases(cases):
    assert set(cases) == set(CASES), "incomplete governed handoff"
    for value in cases.values():
        assert value is True, "governed assertion failed"


def validate_traffic(report):
    before, after = report["direct_observations"], report["handoff_observations"]
    assert report["metadata_observations"] == before, "metadata traffic reached MCP"
    assert after["authorization"] == after["otherTools"] == 0
    assert after["denied"] == before["denied"]
    assert len(after["initialize"]) == 7 and after["http"]["GET"] == 7
    assert after["methods"]["tools/call"] == 3
    assert after["methods"]["tools/list"] > before["methods"]["tools/list"]
    for item in after["initialize"]:
        assert item["protocolVersion"] == "2025-11-25"
        assert "sampling" not in item["capabilities"] and "elicitation" not in item["capabilities"]
        assert "roots" in item["capabilities"]



def failure_result(case, result):
    assert case in MCP_FAILURES
    assert not result.stdout.strip(), "failed MCP connection returned a result"
    lines = result.stderr.strip().splitlines()
    assert lines, "missing native error envelope"
    error = json.loads(lines[-1])["error"]
    if case in MCP_FAILURES[:3]:
        assert result.code == 3 and error["code"] == "auth_required"
        return {"exit": result.code, "outcome": "authentication-required"}
    assert result.code == 4 and error["code"] == "unreachable", "expected native connection failure"
    cause = error.get("cause", "")
    if case == "untrusted-ca":
        assert cause == "unable to verify the first certificate", "missing certificate trust failure"
    else:
        assert "ECONNREFUSED 127.0.0.1:9443" in cause, "outage did not reach the owned MCP listener"
    return {"exit": result.code, "outcome": "tls-rejected" if case == "untrusted-ca" else "unavailable"}


def failure_traffic(case, before, after):
    if case == "untrusted-ca":
        assert after == before, "untrusted TLS reached HTTP"
        return
    assert case in MCP_FAILURES[:3]
    for field in ("forwarded", "http", "methods", "initialize", "otherTools", "emptyRoots"):
        assert after[field] == before[field], "denied credentials reached MCP"
    assert after["requests"] > before["requests"] and after["denied"] > before["denied"]
    if case == "bearer-forwarding":
        assert after["authorization"] > before["authorization"], "bearer negative never sent its header"
    else:
        assert after["authorization"] == before["authorization"], "key-only negative sent bearer authorization"


def complete(report):
    assert report["scope"] == "mcp-governed-handoff"
    assert set(report["cases"]) == set(control.CASES) | {"handoff-initialize", "handoff-tools-list", "handoff-echo"} | set(MCP_FAILURES)
    control.complete({"scope": "mcp-direct-control", "cases": {key: report["cases"][key] for key in control.CASES},
                      "observations": report["direct_observations"], "cleanup": report["cleanup"]})
    for case, outcome in (("initialize", "connected"), ("tools-list", "listed"), ("echo", "echoed")):
        assert report["cases"]["handoff-" + case]["outcome"] == outcome
        assert report["cases"]["handoff-" + case]["exit"] == 0
    check_cases(report["governed_cases"])
    validate_traffic(report)
    assert set(report["binaries"]) == {"fixture", "catalogd", "catalogctl"}
    for value in report["binaries"].values():
        assert len(value) == 64 and int(value, 16) >= 0
    assert report["connection"]["url"] == ENDPOINT and report["connection"]["type"] == "http"
    assert report["selection"]["versionRevision"] != report["selection"]["draftRevision"]
    assert report["selection"]["cardDigest"] == digest(documents()[0])
    assert report["selection"]["registryDigest"] == digest(documents()[1])
    assert report["selection"]["profiles"] == {CARD: CARD_PROFILE, REGISTRY: REGISTRY_PROFILE}
    assert set(report["catalog_denials"]) == set(CATALOG_DENIALS)
    for case, (_, status, code) in CATALOG_DENIALS.items():
        result = report["catalog_denials"][case]
        assert (result["api_status"], result["api_code"], result["cli_exit"], result["outcome"]) == (status, code, 1, "denied")
        catalog_denial(case, Result(result["cli_exit"], "", result["cli_error"]))
        assert result["handoff_created"] is False
        assert result["observations"] == report["metadata_observations"]
    before = report["handoff_observations"]
    for case in MCP_FAILURES:
        result = report["cases"][case]
        expected = (3, "authentication-required") if case in MCP_FAILURES[:3] else (4, "tls-rejected" if case == "untrusted-ca" else "unavailable")
        assert (result["exit"], result["outcome"]) == expected
        if case != "unavailable":
            failure_traffic(case, before, result["observations"])
            before = result["observations"]
        else:
            assert result["observations"] is None, "stopped wrapper cannot supply fresh observations"
    assert report["observations"] == before and report["wrapper_stopped"] is True
    assert report["post_failure_integrity"] is True
