"""Independent result and immutable-reference assertions for native ADK qualification."""

import json
import re

TOOLS = frozenset("catalog_control_" + name for name in ("capabilities", "access", "search", "release", "explore", "indexed_record"))
CASES = frozenset(("inventory", "schemas", "capabilities-access", "known-search", "contradictory-filter", "exact-release",
                   "explore", "closed-arguments", "missing-token", "invalid-token", "foreign-tenant", "revoked-access", "untrusted-ca"))


def require(value, message):
    """Fail with a fixed assertion message rather than private native responses."""
    if not value:
        raise AssertionError(message)


def envelope(result):
    """Require matching structured/text Control data from a successful native call."""
    require(isinstance(result, dict) and not result.get("isError") and "error" not in result, "native call was refused")
    content = result.get("content")
    require(isinstance(content, list) and len(content) == 1 and content[0].get("type") == "text", "native content shape")
    value = json.loads(content[0]["text"])
    require(value == result.get("structuredContent"), "native structured/text mismatch")
    require(value["meta"]["requestId"] and value["data"] is not None, "native Control envelope")
    return value["data"]


def refusal(result, code):
    """An expected tool refusal must remain distinct from successful empty data."""
    require(isinstance(result, dict) and result.get("isError") is True, "native tool refusal was lost")
    require("structuredContent" not in result and "error" not in result, "native refusal disclosed partial data")
    content = result.get("content")
    require(isinstance(content, list) and len(content) == 1 and content[0].get("type") == "text", "native refusal content")
    require(content[0]["text"].split(":", 1)[0] == code, "native refusal classification")


def check_network(interfaces):
    """Require loopback as the sole UP interface, allowing disabled kernel tunnels."""
    require("lo" in interfaces and interfaces["lo"] & 1, "native loopback is unavailable")
    require(all(type(flags) is int and (name == "lo" or flags & 1 == 0) for name, flags in interfaces.items()), "native client has an active external interface")


def check_metadata(detail, record):
    """Compare every projected field with independently read immutable metadata."""
    for section in ("resource", "version", "versionRevision", "listing", "listingRevision", "release", "evidence"):
        require(detail[section]["id"] == record[section]["id"], "immutable reference changed")
        for field, value in detail[section].items():
            expected = record[section][field]
            if section == "evidence" and field in ("trust", "legalRights"):
                expected = {"state": expected["state"]}
            require(value == expected, "projected metadata changed")
    require(detail["versionRevision"]["contentSetDigest"] == record["versionRevision"]["contentSetDigest"], "content-set digest changed")


def check_search_item(item, record):
    """Preserve Search identity, display fields and local summary across adaptation."""
    require(item["type"] == "local" and item["kind"] == record["resource"]["kind"], "native Search origin/kind changed")
    require(item["identifier"] == record["resource"]["canonicalIdentifier"] and item["versionPresent"] is True and item["version"] == record["version"]["version"], "native Search identity changed")
    for field in ("displayName", "description", "tags"):
        require(item[field] == record["listingRevision"][field], "native Search display changed")
    check_metadata(item["local"], record)


def check_release(detail, record):
    """Bind native exact-read identities and reference bytes to independent API evidence."""
    check_metadata(detail, record)
    references = detail["representations"]
    retained = {r["id"]: r for r in record["versionRevision"]["representations"]}
    require(len(references) == len(retained) > 0 and {r["id"] for r in references} == set(retained), "representation membership changed")
    for ref in references:
        original = retained[ref["id"]]
        for field in ("id", "role", "mediaType", "profileId", "contentDigest", "size"):
            require(ref[field] == original[field], "representation reference changed")
        for field in ("artifactId", "url"):
            require(ref.get(field, "") == original.get(field, ""), "representation location changed")
        require(re.fullmatch(r"sha256:[a-f0-9]{64}", ref["contentDigest"]) and "data" not in ref, "native content disclosed")


def validate_result(result, secrets, expected_versions):
    """Accept a complete native result only after actual successful process completion."""
    require(result.code == 0, "native ADK client failed")
    require(not any(secret in result.stdout + result.stderr for secret in secrets), "native credential exposure")
    value = json.loads(result.stdout)
    require(set(value) == {"adk", "mcp", "cases", "closed", "sameToolsetRevocation", "phase", "schemas"}, "native result shape")
    require(value["adk"] == expected_versions["adk"] and value["mcp"] == expected_versions["mcp"], "native dependency version changed")
    require(set(value["cases"]) == CASES and all(type(v) is bool and v for v in value["cases"].values()), "native cases incomplete")
    require(value["phase"] == "complete" and value["closed"] is True and value["sameToolsetRevocation"] is True, "native lifecycle incomplete")
    require(set(value["schemas"]) == {"server", "adk"} and all(set(v) == TOOLS and all(isinstance(s, dict) and s for s in v.values()) for v in value["schemas"].values()), "native schema evidence incomplete")
    return value


def validate_traffic(value):
    """Require real negotiated Catalog calls and no sampling or resource traffic."""
    require(value["requests"] > 0 and value["unexpected"] == 0 and value["sampling"] is False, "native traffic boundary")
    require(value["negotiated"] and set(value["negotiated"]) == {"2025-11-25"}, "native negotiated protocol changed")
    require(value["offered"] and "other" not in value["offered"], "native offered protocol changed")
    require(value["statuses"].get("401", 0) >= 2, "native authentication controls missing")
    require(value["methods"].get("initialize", 0) >= 4 and value["methods"].get("tools/list", 0) >= 2, "native discovery missing")
    expected = {"catalog_control_capabilities": 1, "catalog_control_access": 1, "catalog_control_search": 5,
                "catalog_control_release": 4, "catalog_control_explore": 1}
    require(value["tools"] == expected and all(type(n) is int for n in value["tools"].values()), "native tool call counts")
