"""Pure report validation for native Pi and real Catalog interoperability."""

import json
import hashlib
from collections import Counter

MCP = "application/mcp-server-card+json"
CASES = {
    "catalog",
    "search-first",
    "search-next",
    "changed-query",
    "private-read",
    "unconfigured",
    "unsafe-url",
    "untrusted-catalog",
    "synthetic",
    "forbidden",
    "bad-json",
    "missing-results",
    "timeout",
    "declined",
    "unknown",
    "canonical-skill",
    "synthetic-no-ui",
    "no-ui",
}


ERROR_CASES = {
    "changed-query",
    "private-read",
    "unconfigured",
    "unsafe-url",
    "untrusted-catalog",
    "forbidden",
    "bad-json",
    "missing-results",
    "timeout",
    "unknown",
    "no-ui",
}


def validate_campaign(value):
    """Require independent membership, raw HTTP, continuation and native refusal evidence."""
    isolation = {
        "readOnlyRoot": True,
        "network": "loopback-only",
        "externalConnectErrno": 101,
        "memoryMax": 2147483648,
        "cpuCores": 2,
        "tasksMax": 256,
        "swapMax": 0,
        "fileLimit": 8388608,
        "openFiles": 256,
    }
    if set(value["isolation"]) != set(isolation) or any(
        type(value["isolation"][key]) is not type(expected) or value["isolation"][key] != expected
        for key, expected in isolation.items()
    ):
        raise ValueError("incomplete campaign isolation evidence")
    native = value["native"]
    state = value["installState"]
    if set(state) != {"agent", "no-ui"} or any(
        set(paths) != {"skills", "mcp.json", "pi-ard/lock.json"}
        or any(present is not False for present in paths.values())
        for paths in state.values()
    ):
        raise ValueError("installation state was created or omitted")
    cases = native["cases"]
    if {item["case"] for item in cases} != CASES or len(cases) != len(CASES):
        raise ValueError("incomplete native Pi cases")
    for item in cases:
        tool = (
            "ard_install"
            if item["case"] in {"declined", "unknown", "canonical-skill", "no-ui"}
            else ("ard_catalog" if item["case"] in {"catalog", "private-read", "untrusted-catalog"} else "ard_search")
        )
        if (
            type(item["isError"]) is not bool
            or item["isError"] != (item["case"] in ERROR_CASES)
            or item["tool"] != tool
            or type(item["text"]) is not str
            or not item["text"]
        ):
            raise ValueError("native outcome differs from the selected control")
        if item["case"] == "timeout" and (
            type(item["durationMs"]) not in (int, float) or not 14000 <= item["durationMs"] < 25000
        ):
            raise ValueError("native timeout did not exercise its request deadline")
    if (
        type(native["toolCalls"]) is not int
        or native["toolCalls"] != 18
        or native["offline"] is not True
        or native["fetchUnchanged"] is not True
        or native["activeTools"] != ["ard_catalog", "ard_install", "ard_search"]
        or native["dispatcherVersion"] != "8.12.3"
        or native["httpTimeoutMs"] != 15000
        or native["httpBytes"] != 1000000
        or native["artifactFiles"] != 0
        or type(native["mcpRegistrations"]) is not int
        or native["mcpRegistrations"] != 0
    ):
        raise ValueError("native Pi limits or transport differ")
    if len(native["confirms"]) != 1 or "UNTRUSTED" not in native["confirms"][0]["title"]:
        raise ValueError("native decline confirmation missing")
    if value["snapshotStable"] is not True or not value["snapshotETag"] or len(value["snapshotDigest"]) != 64:
        raise ValueError("publication stability evidence missing")
    members = value["manifest"]["entries"]
    expected = {e["identifier"] for e in members if e["type"] == MCP}
    if len(expected) != 2 or len(value["resources"]) != 6 or len(members) != 7:
        raise ValueError("owned publication membership differs")
    if {r["identifier"] for r in value["resources"]} != {
        e["identifier"] for e in members if e["type"] != "application/ai-registry+json"
    }:
        raise ValueError("publication differs from the governed resource identities")
    real = value["real"]
    if len(real) != 5 or any(item["authorization"] is not False for item in real):
        raise ValueError("unexpected real Catalog traffic")
    manifest = [e for e in real if e["path"] == "/.well-known/ard.json"]
    if len(manifest) != 1 or json.loads(manifest[0]["response"]) != value["manifest"]:
        raise ValueError("native manifest differs from owned publication")
    if hashlib.sha256(manifest[0]["response"].encode()).hexdigest() != value["snapshotDigest"]:
        raise ValueError("native manifest bytes differ from the retained snapshot digest")
    searches = [e for e in real if e["path"] == "/ard/v1/search"]
    if len(searches) != 3 or [e["status"] for e in searches] != [200, 200, 400]:
        raise ValueError("real continuation/refusal missing")
    first, second, changed = [e["request"] for e in searches]
    query = {"text": "order", "filter": {"type": [MCP, "application/mcp-server+json"]}}
    if first != {"query": query, "federation": "referrals", "pageSize": 1}:
        raise ValueError("native type aliases or endpoint request differ")
    pages = [json.loads(e["response"]) for e in searches[:2]]
    token = pages[0].get("pageToken")
    if not token or second != dict(first, pageToken=token):
        raise ValueError("native continuation changed the actual token or query")
    if changed != dict(second, query=dict(query, text="unrelated")):
        raise ValueError("changed-query control did not retain the current token")
    results = [e for page in pages for e in page["results"]]
    if len(results) != 2 or {e["identifier"] for e in results} != expected:
        raise ValueError("native pages lost or duplicated publication members")
    if any(e["type"] != MCP or e.get("source") != "http://127.0.0.1:8080/ard/v1/" for e in results):
        raise ValueError("canonical type or response provenance missing")
    private = [e for e in real if e["path"].startswith("/api/v1/catalog/resources/")]
    if len(private) != 1 or private[0]["status"] != 401:
        raise ValueError("actual private authentication refusal missing")
    expected_counts = Counter(
        (
            e["method"],
            "/api/v1/catalog/resources/{catalogRecordId}" if e is private[0] else e["path"],
            str(e["status"] // 100) + "xx",
        )
        for e in real
    )
    if value["catalogCounters"] != [list(key) + [count] for key, count in sorted(expected_counts.items())]:
        raise ValueError("independent Catalog operation counts differ")
    synthetic = value["synthetic"]
    if (
        len(synthetic) != 6
        or sorted(e["path"] for e in synthetic)
        != sorted(["/synthetic", "/synthetic", "/forbidden", "/bad-json", "/missing-results", "/timeout"])
        or any(e["authorization"] is not False for e in synthetic)
    ):
        raise ValueError("unexpected artifact, referral, trust or fixture contact")
    return value
