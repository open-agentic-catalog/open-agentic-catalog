"""Fixed peer qualification obligations and offline response validation."""

import json
import re

TENANT = "00000000-0000-7000-8000-000000000001"
LEGACY = "ai-catalog-1.0-b062278"
CURRENT = "ard-0.91-aa3e598"
MCP = "application/mcp-server-card+json"
A2A = "application/a2a-agent-card+json"
REGISTRY = "application/ai-registry+json"
DISPOSITIONS = {
    "wire-challenge": "supported",
    "wire-primary": "backend-unavailable",
    "wire-filters": "supported",
    "wire-context": "peer-limitation",
    "wire-referrals": "peer-limitation",
    "wire-optional": "optional-unavailable",
    "wire-bases": "peer-limitation",
    "indexed-challenge": "supported",
    "indexed-primary": "descriptors-only",
    "selected-search": "supported",
    "source-admission": "supported",
    "trust-admission": "supported",
    "live-admission": "peer-limitation",
    "live-routes": "supported",
    "public-partial": "peer-limitation",
    "public-routes": "supported",
    "outage": "supported",
    "recovery": "supported",
    "navigation-one": "client-bound",
    "navigation-five": "client-bound",
    **{f"checker-{peer}-{mode}": "backend-unavailable" if peer == "primary" and mode == "registry" else "supported"
       for peer in ("challenge", "primary") for mode in ("manifest", "publisher", "registry")},
}


SUMMARY = {
    "wire-challenge": {"entries": 8, "current_status": 404, "search_status": 200},
    "wire-primary": {"entries": 2, "current_status": 404, "search_status": 500},
    "wire-filters": {"page_size_one": 1},
    "wire-context": {"challenge": 422, "primary": 422, "paired_controls": 200, "field": "query.@context"},
    "wire-optional": {"list": 404, "explore": 501, "peers": 2},
    "wire-bases": {"challenge_route_bases": 4, "primary_operational_bases": 2},
    "selected-search": {"primary_status": 400, "list": False},
    "source-admission": {"missing_list_rejections": 3, "manifest_live_rejections": 2},
    "trust-admission": {"untrusted": 503, "wrong_tenant": 403, "anonymous": 403},
    "live-admission": {"results": 0, "partial": True},
    "public-partial": {"local_results": 3, "partial_signal": 'federation;desc="partial"', "private_diagnostics": False},
    "outage": {"preserved_members": 10},
    "recovery": {"members": 10, "added": 0, "updated": 0, "withdrawn": 0},
}

PHASES = {
    "trust": set(),
    "bootstrap": {"indexed-challenge", "indexed-primary", "selected-search", "source-admission", "trust-admission"},
    "live": {"live-admission"},
    "public": {"public-partial"},
    "outage": {"outage"},
    "recovery": {"recovery"},
}


def outcome(name, evidence):
    """Passing characterization does not imply a supported peer capability."""
    return dict(name=name, status="pass", disposition=DISPOSITIONS[name], evidence=evidence)


def check_cases(cases, expected_names=None):
    """Require all fixed obligations, distinct from the existing client report."""
    expected_names = set(DISPOSITIONS) if expected_names is None else expected_names
    if not isinstance(cases, list) or len(cases) != len(expected_names):
        raise ValueError("incomplete peer case inventory")
    seen = set()
    for case in cases:
        if not isinstance(case, dict):
            raise ValueError("invalid peer case")
        name = case.get("name")
        if not isinstance(name, str) or name not in expected_names or name in seen or case.get("disposition") != DISPOSITIONS[name]:
            raise ValueError("duplicate, unknown or contradictory peer case")
        seen.add(name)
        if case.get("status") not in ("pass", "fail") or not isinstance(case.get("evidence"), dict) or not case["evidence"]:
            raise ValueError("missing peer assertion evidence")
        evidence = case["evidence"]
        if case["status"] == "pass":
            for key, expected in SUMMARY.get(name, {}).items():
                if type(evidence.get(key)) is not type(expected) or evidence[key] != expected:
                    raise ValueError("contradictory peer summary: " + name + "/" + key)
            if name.startswith("checker-"):
                check_checker(name.split("-")[1], name.split("-")[2], evidence)
            elif name.startswith("indexed-"):
                expected = 10 if name == "indexed-challenge" else 2
                if (evidence.get("count") != expected or evidence.get("supported") != (3 if expected == 10 else 0)
                        or evidence.get("profile") != (CURRENT if name == "indexed-challenge" else LEGACY) or evidence.get("enumerable") is not True
                        or evidence.get("search") != "" or evidence.get("type") != "hybrid"):
                    raise ValueError("incomplete indexed peer evidence")
            elif name.startswith("navigation-"):
                expected = (1, 6) if name.endswith("one") else (5, 13)
                if (evidence.get("registries"), evidence.get("results")) != expected:
                    raise ValueError("incomplete navigation peer evidence")
            elif name in ("live-routes", "public-routes"):
                validate_routes(evidence)
    return sum(case["status"] == "pass" for case in cases)


def check_phase(label, payload, code):
    """Validate a child before its rows can enter the supervisor's retained state."""
    if (not isinstance(payload, dict) or not isinstance(payload.get("state"), dict)
            or not isinstance(payload.get("cases"), list) or type(code) is not int or code not in (0, 1)):
        raise ValueError("malformed peer phase report")
    names = set()
    for row in payload["cases"]:
        if not isinstance(row, dict) or not isinstance(row.get("name"), str) or row["name"] not in PHASES[label]:
            raise ValueError("unexpected peer phase case")
        names.add(row["name"])
    passed = check_cases(payload["cases"], PHASES[label] if code == 0 else names)
    if code == 0:
        if "error" in payload or passed != len(PHASES[label]):
            raise ValueError("contradictory successful peer phase")
    elif not isinstance(payload.get("error"), str) or not payload["error"]:
        raise ValueError("failed peer phase lacks its error")


def check_checker(peer, mode, evidence):
    """A nonzero checker exit is accepted only for the measured backend failure."""
    expected_code = 1 if (peer, mode) == ("primary", "registry") else 0
    if type(evidence.get("exit_code")) is not int or evidence["exit_code"] != expected_code:
        raise ValueError("unexpected native conformance status")
    text = evidence.get("output", "")
    stderr = evidence.get("stderr", "")
    if not isinstance(text, str) or not text.strip() or not isinstance(stderr, str) or stderr.strip():
        raise ValueError("missing native conformance output")
    clean = re.sub(r"\x1b\[[0-9;]*m", "", text)
    if expected_code:
        if ("✗ POST /search request failed: 500 Internal Server Error" not in clean
                or clean.count("✗") != 1 or "Found 1 critical specification errors." not in clean or "CONFORMANCE STATUS: FAIL" not in clean):
            raise ValueError("failure is not the measured primary Search backend failure")
    elif mode == "registry":
        if ("CONFORMANCE STATUS: PASS" not in clean
                or "POST /search responded successfully with 200 OK." not in clean
                or "POST /search returned valid results list (1 items)." not in clean
                or "Validated with 0 critical specification errors and 0 warnings." not in clean):
            raise ValueError("missing successful Registry protocol evidence")
    else:
        count = 8 if peer == "challenge" else 2
        if (clean.count("✓ Manifest validates against ArdManifest.") != 1
                or clean.count(f"✓ All {count} entries validate against ArdEntry.") != 1
                or "skipping" in clean.lower()):
            raise ValueError("missing complete schema evidence")
        warnings = [line.strip() for line in clean.splitlines() if line.strip().startswith("⚠")]
        expected = (9 if peer == "challenge" else 2) + int(mode == "publisher")
        if (len(warnings) != expected + int(mode == "publisher") or evidence.get("warnings") != warnings
                or evidence.get("summary_warnings") != expected
                or f"Validated with 0 critical specification errors and {expected} warnings." not in clean):
            raise ValueError("unexpected conformance warnings")


def search_validator(schema):
    """Resolve pinned EntryFields locally; never retrieve a schema over the network."""
    from jsonschema import Draft202012Validator, FormatChecker
    from referencing import Registry, Resource
    uri = schema["$id"]
    registry = Registry().with_resource(uri, Resource.from_contents(schema))
    return Draft202012Validator({"$ref": uri + "#/$defs/EntryFields"},
                                registry=registry, format_checker=FormatChecker())


def validate_search(document, validator):
    """Add the reviewed OpenAPI obligations not owned by the entry JSON schema."""
    assert isinstance(document, dict) and set(document) <= {"results", "referrals", "pageToken"}, "invalid Search envelope"
    assert isinstance(document.get("results"), list), "missing Search results"
    if "pageToken" in document:
        assert isinstance(document["pageToken"], str), "invalid continuation"
    seen = set()
    for entry in document["results"]:
        assert not list(validator.iter_errors(entry)), "entry schema validation failed"
        identifier = entry.get("identifier")
        assert isinstance(identifier, str) and identifier.startswith("urn:air:") and identifier not in seen, "invalid/duplicate identifier"
        seen.add(identifier)
        assert type(entry.get("score")) in (int, float) and 0 <= entry["score"] <= 100 and entry["score"] % 1 == 0, "invalid score"
        assert isinstance(entry.get("source"), str) and re.match(r"^[A-Za-z][A-Za-z0-9+.-]*:", entry["source"]) and validator.format_checker.conforms(entry["source"], "uri"), "invalid source URI"
    assert isinstance(document.get("referrals", []), list), "invalid referrals"
    for referral in document.get("referrals", []):
        assert isinstance(referral, dict), "invalid referral"
        assert all(isinstance(referral.get(key), str) and referral[key] for key in ("identifier", "displayName", "type", "url")), "missing referral fields"
        assert validator.format_checker.conforms(referral["url"], "uri") and referral["type"] in ("application/ai-registry", REGISTRY), "invalid referral destination/type"
    return document


def parse_routes(output):
    """Retain only fixed synthetic Uvicorn method/path/status counts, never headers."""
    counts = {}
    for method, path, status in re.findall(r'"(GET|POST) (/[^ ?"]*)(?:\?[^ "]*)? HTTP/1\.[01]" (\d{3})', output):
        key = f"{method} {path} {status}"
        counts[key] = counts.get(key, 0) + 1
    return counts


def route_delta(before, after):
    """Reject a reset/truncated log rather than treating missing calls as zero."""
    if any(after.get(key, 0) < value for key, value in before.items()):
        raise ValueError("peer access evidence was truncated")
    return {key: value - before.get(key, 0) for key, value in after.items() if value != before.get(key, 0)}


def validate_routes(counts):
    """Exactly the root and approved child; no unapproved or beyond-hop calls."""
    if counts != {"POST /search 200": 1, "POST /registries/nested/search 200": 1}:
        raise ValueError("unexpected live referral contacts: " + json.dumps(counts, sort_keys=True))
