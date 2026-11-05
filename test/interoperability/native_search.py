"""Native HF Search qualification; upstream imports occur only in the functional entry point."""

import json
from pathlib import Path
import sys
import urllib.error

from client_contract import MCP, NATIVE_CASES, NATIVE_FILTERS, PUBLIC, outcome, redact

AGENT = "application/a2a-agent-card+json"
NEGATIVE = "hf-qualification-no-match"
ERROR_MESSAGE = "page token is invalid for this request"


def validate(payload, expected, entries, *, token=False):
    """Compare native model output with independent immutable publication evidence."""
    results = payload["results"]
    assert isinstance(results, list), "missing results array"
    identifiers = [entry["identifier"] for entry in results]
    assert len(identifiers) == len(set(identifiers)), "duplicate results"
    assert set(identifiers) == set(expected), "unexpected result identifiers"
    assert not payload.get("referrals"), "federation=none returned referrals"
    if not token:
        assert not payload.get("pageToken"), "unexpected continuation token"
    originals = {entry["identifier"]: entry for entry in entries}
    for entry in results:
        original = originals[entry["identifier"]]
        for field, value in original.items():
            assert entry.get(field) == value, "changed " + field
        assert (entry.get("url") is not None) != (entry.get("data") is not None), "ambiguous representation"
        assert type(entry["score"]) is int and 0 <= entry["score"] <= 100, "invalid score"
        assert entry["source"] == PUBLIC + "/ard/v1/", "unexpected source attribution"
    return results


def pagination(search, entries, baseline, item, progress):
    """Follow each returned token once, checking stable ordering and exact scores."""
    wanted = [entry["identifier"] for entry in baseline["results"]]
    token, tokens, found = None, set(), []
    for index, identifier in enumerate(wanted):
        progress("native-pagination-page-" + str(index + 1))
        item["native_calls"] += 1
        payload = search({}, 1, token)
        results = validate(payload, [identifier], entries, token=True)
        item["result_counts"].append(len(results))
        assert results[0]["score"] == baseline["results"][index]["score"], "changed page score"
        found.append(results[0]["identifier"])
        token = payload.get("pageToken")
        if index < len(wanted) - 1:
            assert isinstance(token, str) and token and token not in tokens, "missing/repeated continuation token"
            tokens.add(token)
        else:
            assert token is None, "terminal page returned a token"
    assert found == wanted and len(set(found)) == 3, "incomplete or duplicate page walk"
    item.update(unique_results=len(set(found)), continuation_tokens=len(tokens), terminal=token is None)


def changed_filter(search, entries, baseline, item):
    """A native HTTP rejection must carry the selected ARD error, not a decoder failure."""
    item["native_calls"] += 1
    first = search({}, 1, None)
    results = validate(first, [baseline["results"][0]["identifier"]], entries, token=True)
    assert results[0]["score"] == baseline["results"][0]["score"], "changed first-page score"
    item["result_counts"].append(len(results))
    token = first.get("pageToken")
    assert isinstance(token, str) and token, "missing continuation token"
    item["native_calls"] += 1
    try:
        search({"metadata.license": "MIT"}, 1, token)
    except urllib.error.HTTPError as error:
        with error:
            raw = error.read(8193)
            assert len(raw) <= 8192, "over-limit ARD problem"
            problem = json.loads(raw)
            assert error.code == 400, "wrong HTTP rejection"
            assert error.headers.get_content_type() == "application/json", "wrong ARD problem media type"
            assert problem == {"errorCode": "INVALID_ARGUMENT", "message": ERROR_MESSAGE}, "wrong ARD problem"
            item.update(http_status=error.code, error_code=problem["errorCode"], message=problem["message"])
    else:
        raise AssertionError("changed-filter token unexpectedly accepted")


def qualify(entries, baseline, search, progress=lambda _phase: None):
    """Run all eight outcomes; each contradictory request changes exactly one conjunct."""
    identifiers = [entry["identifier"] for entry in entries]
    assert len(identifiers) == len(set(identifiers)) == 3, "expected three published smoke entries"
    validate(baseline, identifiers, entries)
    selected = {family: [entry["identifier"] for entry in entries if family == "listing" or entry["type"] == (AGENT if family == "a2a" else MCP)]
                for family in NATIVE_FILTERS}
    assert len(selected["a2a"]) == len(selected["mcp"]) == 1, "missing native smoke kinds"
    outcomes = []
    for name in NATIVE_CASES:
        item = outcome(name, native_calls=0, result_counts=[])
        try:
            if name == "native-pagination":
                pagination(search, entries, baseline, item, progress)
            elif name == "native-changed-filter-token":
                changed_filter(search, entries, baseline, item)
            else:
                _, family, kind = name.split("-")
                filters = NATIVE_FILTERS[family]
                queries = [dict(filters, **{field: NEGATIVE}) for field in filters] if kind == "contradictions" else [dict(filters)]
                for query in queries:
                    item["native_calls"] += 1
                    results = validate(search(query, 10, None), [] if kind == "contradictions" else selected[family], entries)
                    item["result_counts"].append(len(results))
        except (AssertionError, RuntimeError, OSError, ValueError, KeyError, TypeError) as error:
            if isinstance(error, urllib.error.HTTPError):
                error.close()
            item.update(status="fail", detail=redact(str(error))[:6000])
        outcomes.append(item)
    return outcomes


def progress(phase):
    """Write only a fixed diagnostic label to the invocation-owned progress file."""
    path = Path("/phase.json")
    path.write_text(json.dumps({"phase": phase}) + "\n")


def main():
    # Keep independent tools outside host imports and ordinary unit execution.
    from discover.models import SearchQuery, SearchRequest
    from discover.navigation import search_registry
    from qualification import request

    def search(filters, size, token):
        query = SearchRequest(query=SearchQuery(text="order", filter=filters), federation="none", pageSize=size, pageToken=token)
        return search_registry(PUBLIC + "/ard/v1/", query, timeout=10).model_dump(exclude_none=True)

    try:
        entries = [entry for entry in request(PUBLIC, "/.well-known/ard.json")["entries"] if ":readiness:" in entry["identifier"]]
        baseline = request(PUBLIC, "/ard/v1/search", body={"query": {"text": "order"}, "federation": "none", "pageSize": 10})
        cases = qualify(entries, baseline, search, progress)
    except (AssertionError, RuntimeError, OSError, ValueError, KeyError, TypeError) as error:
        cases = [outcome(name, status="fail", detail=redact(str(error))[:6000]) for name in NATIVE_CASES]
    print(json.dumps({"cases": cases}))
    return int(any(case["status"] != "pass" for case in cases))


if __name__ == "__main__":
    sys.exit(main())
