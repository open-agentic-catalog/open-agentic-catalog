"""Qualify the unchanged navigation CLI against the existing Catalog publications."""

import json

from client_contract import MCP, PRIVATE, PUBLIC, outcome, redact
from common import run


def cases(entries):
    """Select only owned URLs and the nine reviewed navigation behaviors."""
    ids = {entry["identifier"] for entry in entries}
    mcp = {entry["identifier"] for entry in entries if entry["type"] == MCP}
    assert len(ids) == 3 and len(mcp) == 1, "navigation fixture inventory changed"
    return [
        dict(name="navigate-origin", url=PUBLIC, expected=ids),
        dict(name="navigate-current", url=PUBLIC + "/.well-known/ard.json", expected=ids, registry=True),
        dict(name="navigate-predecessor", url=PUBLIC + "/.well-known/ai-catalog.json", expected=ids),
        dict(name="navigate-mcp", url=PUBLIC, expected=mcp, kind="mcp"),
        dict(name="navigate-skill-kind", url=PUBLIC, expected=set(), kind="skill"),
        dict(name="navigate-empty", url=PUBLIC, expected=set(), query="zqxunmatchedfixture"),
        dict(name="navigate-private-missing", url=PRIVATE, expected=set(), private=True),
        dict(name="navigate-private-reader", url=PRIVATE, expected=set(), private=True, token="local-reader"),
        dict(name="navigate-private-current", url=PRIVATE + "/.well-known/ard.json", expected=set(),
             private=True, token="local-reader"),
    ]


def validate(case, result, entries, registry, private_entries):
    """Static fallback and a zero process status cannot conceal failed discovery."""
    assert result.code == 0, "navigation command failed"
    payload = json.loads(result.stdout)
    manifest = case["url"] if case["url"].endswith(".json") else case["url"] + "/.well-known/ai-catalog.json"
    discovery = [dict(kind="catalog", url=manifest, status="ok", detail="")]
    if case.get("registry"):
        discovery.append(dict(kind="registry", url=PUBLIC + "/ard/v1/search", status="ok", detail=""))
    if case.get("private"):
        discovery[0].update(status="error", detail="HTTP 401")
        for entry in private_entries:
            for field in ("identifier", "displayName"):
                assert entry[field] not in result.stdout + result.stderr, "private metadata leaked"
    assert payload["discovered"] == discovery, "unexpected discovery endpoints or diagnostics"
    actual = payload["results"]
    assert isinstance(actual, list), "navigation results must be an array"
    found = [entry["identifier"] for entry in actual]
    assert len(found) == len(set(found)) and set(found) == case["expected"], "unexpected navigation identities"
    assert not payload.get("referrals") and not payload.get("pageToken"), "unexpected navigation continuation"
    by_id = {entry["identifier"]: entry for entry in entries}
    scores = {entry["identifier"]: entry["score"] for entry in registry["results"]}
    if case.get("registry"):
        assert set(scores) == case["expected"] and all(type(score) is int and 50 < score <= 100 for score in scores.values()), "Registry control does not beat static results"
    for entry in actual:
        original = by_id[entry["identifier"]]
        for field in ("displayName", "type", "data", "url"):
            assert entry.get(field) == original.get(field), f"navigation changed {field}"
        assert ("url" in entry) != ("data" in entry), "ambiguous navigation representation"
        assert type(entry["score"]) is int, "invalid navigation score"
        assert entry["score"] == (scores[entry["identifier"]] if case.get("registry") else 50), "navigation score changed"
        assert entry["source"] == (PUBLIC + "/ard/v1/search" if case.get("registry") else manifest), "navigation source changed"
    return {"discovered": discovery, "result_count": len(actual)}


def qualify(current, predecessor, private, registry):
    """Continue after case failures, retaining only bounded synthetic diagnostics."""
    assert {entry["identifier"] for entry in current} == {entry["identifier"] for entry in predecessor}, "publication profile inventories differ"
    outcomes = []
    for case in cases(current):
        item = outcome(case["name"])
        argv = ["hf-discover", "navigate", case["url"], case.get("query", "order"), "--json",
                "--kind", case.get("kind", "all"), "--timeout", "2", "--limit", "10",
                "--max-depth", "1", "--max-registries", "3", "--max-per-source", "10", "--no-follow-referrals"]
        if case.get("token"):
            argv.extend(["--token", case["token"]])
        try:
            result = run(argv, timeout=30, limit=1024 * 1024, check=False)
            item.update(exit_code=result.code, stderr=redact(result.stderr)[:6000])
            item.update(validate(case, result, current if case.get("registry") else predecessor, registry, private))
        except (AssertionError, RuntimeError, OSError, ValueError, KeyError, TypeError) as error:
            item.update(status="fail", detail=redact(str(error))[:6000])
        outcomes.append(item)
    return outcomes
