"""Fixed client case inventory and synthetic fixture inputs; no tool execution."""

PUBLIC = "http://127.0.0.1:8080"
PRIVATE = "http://localhost:8080"
MCP = "application/mcp-server-card+json"
TOKENS = ("local-admin", "local-reader", "local-publisher", "other-tenant-admin", "invalid-token")
SEARCH_CASES = ("resource-1", "resource-2", "resource-3", "all-results", "explicit-search-endpoint",
                "no-matches", "limit-one", "mcp-filter", "private-reader", "private-missing",
                "private-invalid", "private-other-tenant", "human-output")
NAVIGATION_CASES = ("navigate-origin", "navigate-current", "navigate-predecessor", "navigate-mcp",
                    "navigate-skill-kind", "navigate-empty", "navigate-private-missing",
                    "navigate-private-reader", "navigate-private-current")
CONTROL_CASES = ("decode-search-full", "decode-search-sparse", "decode-navigate-full",
                 "decode-navigate-sparse", "navigate-credential-source")
NATIVE_FILTERS = {
    "listing": {"metadata.license": "Apache-2.0", "http://purl.org/dc/terms/subject": "developer tools"},
    "a2a": {"data.skills.id": "resolve-order-exceptions", "data.defaultInputModes": "application/json",
            "data.defaultOutputModes": "application/json", "data.supportedInterfaces.protocolBinding": "JSONRPC",
            "data.supportedInterfaces.protocolVersion": "1.0"},
    "mcp": {"data.remotes.type": "streamable-http", "data.remotes.supportedProtocolVersions": "2025-11-25"},
}
NATIVE_CASES = tuple(f"native-{family}-{kind}" for family in NATIVE_FILTERS for kind in ("match", "contradictions")) + (
    "native-pagination", "native-changed-filter-token")
DISPOSITIONS = dict.fromkeys((*SEARCH_CASES, *NAVIGATION_CASES, *CONTROL_CASES, *NATIVE_CASES), "supported")
DISPOSITIONS.update({name: "profile-gap" for name in CONTROL_CASES if name.endswith("-sparse")})
DISPOSITIONS.update({name: "client-limitation" for name in NAVIGATION_CASES if "private" in name})
DISPOSITIONS.update({"navigate-skill-kind": "filter-distinction", "navigate-credential-source": "client-limitation"})


def redact(value):
    """Exclude disposable bearer values from retained diagnostics too."""
    for token in TOKENS:
        value = value.replace(token, "[test-token]")
    return value


def outcome(name, **fields):
    """Keep assertion status independent of the fixed capability disposition."""
    return {"name": name, "status": "pass", "disposition": DISPOSITIONS[name], **fields}


def navigation_evidence(name):
    """Exact discovery diagnostics and counts for the fixed local publications."""
    private = "private" in name
    current = name in ("navigate-current", "navigate-private-current")
    manifest = (PRIVATE if private else PUBLIC) + ("/.well-known/ard.json" if current else "/.well-known/ai-catalog.json")
    discovered = [dict(kind="catalog", url=manifest, status="error" if private else "ok",
                       detail="HTTP 401" if private else "")]
    if name == "navigate-current":
        discovered.append(dict(kind="registry", url=PUBLIC + "/ard/v1/search", status="ok", detail=""))
    count = 0 if private or name in ("navigate-empty", "navigate-skill-kind") else 1 if name == "navigate-mcp" else 3
    return dict(exit_code=0, result_count=count, discovered=discovered)


def check_cases(cases, expected):
    """Reject incomplete inventories and child-invented compatibility claims."""
    if (not isinstance(cases, list) or len(cases) != len(expected)
            or any(not isinstance(case, dict) or not isinstance(case.get("name"), str) for case in cases)
            or {case["name"] for case in cases} != set(expected)):
        raise RuntimeError("incomplete client qualification report")
    for case in cases:
        if case.get("status") not in ("pass", "fail") or case.get("disposition") != DISPOSITIONS[case["name"]]:
            raise RuntimeError("invalid qualification status or disposition")
        if case["name"] in NAVIGATION_CASES and case.get("http_status") is not None:
            raise RuntimeError("navigation status must come from discovery diagnostics")
        if case["status"] == "pass":
            check_evidence(case)
    return sum(case["status"] == "pass" for case in cases)


def check_evidence(case):
    """A reproduced limitation must not be relabeled as a successful capability."""
    name = case["name"]
    if name in NATIVE_CASES:
        check_native_evidence(case)
    if name in NAVIGATION_CASES:
        if (type(case.get("exit_code")) is not int or type(case.get("result_count")) is not int
                or any(case.get(field) != value for field, value in navigation_evidence(name).items())):
            raise RuntimeError("contradictory navigation evidence")
    if name.startswith("decode-"):
        full = name.endswith("-full")
        error = None if full else "BadParameter" if name == "decode-search-sparse" else "ValidationError"
        if (case.get("accepted") is not full or case.get("decoder_error") != error
                or case.get("http_status") != 200 or type(case.get("http_requests")) is not int
                or case["http_requests"] != 1):
            raise RuntimeError("contradictory decoder evidence")
    if name == "navigate-credential-source":
        if (case.get("manifest_authorized") is not False
                or case.get("other_owned_origin_registry_authorized") is not True
                or case.get("source_replaced_with_queried_endpoint") is not True
                or type(case.get("http_requests")) is not int or case["http_requests"] != 2):
            raise RuntimeError("contradictory credential/source evidence")


def check_native_evidence(case):
    """Require exact native call counts, page completion and the real ARD refusal."""
    name = case["name"]
    if name in ("native-pagination", "native-changed-filter-token"):
        changed = name == "native-changed-filter-token"
        expected = dict(native_calls=2 if changed else 3, result_counts=[1] if changed else [1, 1, 1])
        expected.update(dict(http_status=400, error_code="INVALID_ARGUMENT", message="page token is invalid for this request")
                        if changed else dict(unique_results=3, continuation_tokens=2, terminal=True))
    else:
        _, family, kind = name.split("-")
        count = len(NATIVE_FILTERS[family]) if kind == "contradictions" else 1
        expected = dict(native_calls=count, result_counts=[0 if kind == "contradictions" else 3 if family == "listing" else 1] * count)
    if any(type(case.get(key)) is not type(value) or case[key] != value for key, value in expected.items()):
        raise RuntimeError("contradictory native Search evidence")
    if any(type(count) is not int for count in case["result_counts"]):
        raise RuntimeError("invalid native result count")
