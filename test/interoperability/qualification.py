"""Exercise the installed, unmodified HF executable against an owned Catalog only."""

import json
from pathlib import Path
import re
import sys
import urllib.error
import urllib.request

from common import run
from client_contract import CONTROL_CASES, MCP, NATIVE_CASES, NAVIGATION_CASES, PRIVATE, PUBLIC, check_cases, outcome, redact
import navigation_qualification as navigation


def request(origin, path, *, token=None, body=None, key=None, etag=None):
    """Read setup evidence or publish the second host without ambient proxies/redirects."""
    if origin not in (PUBLIC, PRIVATE) or not path.startswith("/") or path.startswith("//"):
        raise ValueError("fixture origin/path rejected")

    class NoRedirect(urllib.request.HTTPRedirectHandler):
        def redirect_request(self, req, fp, code, msg, headers, newurl):
            return None

    headers = {"Accept": "application/json"}
    for name, value in (("Authorization", "Bearer " + token if token else None),
                        ("Idempotency-Key", key), ("If-Match", etag)):
        if value:
            headers[name] = value
    payload = None if body is None else json.dumps(body).encode()
    if payload is not None:
        headers["Content-Type"] = "application/json"
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    with opener.open(urllib.request.Request(origin + path, payload, headers), timeout=10) as response:
        raw = response.read(2 * 1024 * 1024 + 1)
        if len(raw) > 2 * 1024 * 1024:
            raise RuntimeError("fixture response exceeded bound")
        return json.loads(raw)


def prepare():
    """Publish the existing reviewed records to the authenticated localhost binding."""
    snapshot = request(PUBLIC, "/api/v1/publication/snapshots", token="local-publisher",
                       key="hf-private-snapshot", body={"host": "localhost"})
    request(PUBLIC, "/api/v1/publication/snapshots/" + snapshot["id"] + ":activate",
            token="local-publisher", key="hf-private-activate", etag=snapshot["etag"],
            body={"host": "localhost", "reason": "Qualify private HF client reads"})


def cases(entries):
    """Define the fixed supported matrix; limits and filters have exact expected sets."""
    ids = {entry["identifier"] for entry in entries}
    mcp_ids = {entry["identifier"] for entry in entries if entry["type"] == MCP}
    if len(ids) != 3 or len(mcp_ids) != 1:
        raise RuntimeError("expected the three maintained smoke resource kinds")
    result = []
    for index, entry in enumerate(entries):
        result.append(dict(name=f"resource-{index + 1}", query=entry["identifier"], expected={entry["identifier"]}))
    result.extend([
        dict(name="all-results", query="order", expected=ids),
        dict(name="explicit-search-endpoint", query="order", expected=ids, endpoint=True),
        dict(name="no-matches", query="zqxunmatchedfixture", expected=set()),
        dict(name="limit-one", query="order", expected=ids, limit=1),
        dict(name="mcp-filter", query="order", expected=mcp_ids, kind="mcp"),
        dict(name="private-reader", query="order", expected=ids, private=True, token="local-reader"),
        dict(name="private-missing", query="order", private=True, status=401),
        dict(name="private-invalid", query="order", private=True, token="invalid-token", status=401),
        dict(name="private-other-tenant", query="order", private=True, token="other-tenant-admin", status=403),
        dict(name="human-output", query=entries[0]["identifier"], expected={entries[0]["identifier"]}, human=True),
    ])
    return result


def validate(case, result, entries):
    """Compare independently decoded client output with immutable published evidence."""
    if case.get("status"):
        assert result.code != 0, "private search unexpectedly succeeded"
        assert f"HTTP {case['status']}" in result.stderr, "expected HTTP rejection absent"
        assert not result.stdout.strip(), "failed command emitted success output"
        assert not any(entry["identifier"] in result.stderr for entry in entries), "private identifier leaked"
        assert not any(entry["displayName"] in result.stderr for entry in entries), "private name leaked"
        return
    assert result.code == 0, "HF CLI failed to decode/search"
    if case.get("human"):
        selected = next(entry for entry in entries if entry["identifier"] in case["expected"])
        # Rich may wrap names; check a stable, nonempty name word, not table spacing.
        assert selected["displayName"].split()[0] in result.stdout, "human output missing name"
        return
    payload = json.loads(result.stdout)
    actual = payload["results"]
    found = [entry["identifier"] for entry in actual]
    assert len(found) == len(set(found)), "duplicate results"
    if case.get("limit"):
        assert len(found) == case["limit"] and set(found) <= case["expected"], "limit not honored"
        assert isinstance(payload.get("pageToken"), str) and payload["pageToken"], "missing continuation token"
    else:
        assert set(found) == case["expected"], "unexpected result identifiers"
    assert not payload.get("referrals"), "federation=none returned referrals"
    by_id = {entry["identifier"]: entry for entry in entries}
    for entry in actual:
        original = by_id[entry["identifier"]]
        for field in ("displayName", "type", "data", "url"):
            assert entry.get(field) == original.get(field), f"changed {field}"
        assert type(entry["score"]) is int and 0 <= entry["score"] <= 100, "invalid score"
        origin = PRIVATE if case.get("private") else PUBLIC
        assert entry["source"] == origin + "/ard/v1/", "unexpected source attribution"
        assert ("url" in entry) != ("data" in entry), "ambiguous representation"


def qualify():
    """Continue through all cases and return nonzero when any supported exchange fails."""
    prepare()
    manifest = request(PUBLIC, "/.well-known/ard.json")
    entries = [entry for entry in manifest["entries"] if ":readiness:" in entry["identifier"]]
    private_manifest = request(PRIVATE, "/.well-known/ard.json", token="local-reader")
    private_entries = [entry for entry in private_manifest["entries"] if ":readiness:" in entry["identifier"]]
    if {entry["identifier"] for entry in entries} != {entry["identifier"] for entry in private_entries}:
        raise RuntimeError("public/private fixture sets differ")
    outcomes = []
    for case in cases(entries):
        origin = PRIVATE if case.get("private") else PUBLIC
        url = origin + "/ard/v1" + ("/search" if case.get("endpoint") else "")
        argv = ["hf-discover", "search", case["query"], "--registry-url", url,
                "--federation", "none", "--kind", case.get("kind", "all")]
        if not case.get("human"):
            argv.append("--json")
        if case.get("limit"):
            argv.extend(["--limit", str(case["limit"])])
        if case.get("token"):
            argv.extend(["--token", case["token"]])
        item = outcome(case["name"])
        try:
            result = run(argv, timeout=40, limit=1024 * 1024, check=False)
            item["exit_code"] = result.code
            match = re.search(r"HTTP (\d{3})", result.stderr)
            item["http_status"] = int(match[1]) if match else (200 if result.code == 0 else None)
            item["stderr"] = redact(result.stderr)[:6000]
            validate(case, result, private_entries if case.get("private") else entries)
        except (AssertionError, RuntimeError, OSError, ValueError, KeyError, TypeError) as error:
            item.update(status="fail", detail=redact(str(error))[:6000])
        outcomes.append(item)
    outcomes.extend(navigation_results(entries, private_entries))
    outcomes.extend(control_results())
    outcomes.extend(child_results("native_search.py", NATIVE_CASES))
    report = {"cases": outcomes, "passed": sum(item["status"] == "pass" for item in outcomes),
              "total": len(outcomes), "client_version": run(["hf-discover", "--version"]).stdout.strip()}
    print(json.dumps(report, indent=2))
    return int(report["passed"] != report["total"])


def navigation_results(entries, private_entries):
    """Reuse published evidence, with each profile supplying its own expected metadata."""
    try:
        predecessor = request(PUBLIC, "/.well-known/ai-catalog.json")
        predecessor_entries = [entry for entry in predecessor["entries"] if ":readiness:" in entry["identifier"]]
        registry = request(PUBLIC, "/ard/v1/search", body={"query": {"text": "order"}, "federation": "none", "pageSize": 10})
        return navigation.qualify(entries, predecessor_entries, private_entries, registry)
    except (AssertionError, RuntimeError, OSError, ValueError, KeyError, TypeError) as error:
        return [outcome(name, status="fail", detail=redact(str(error))[:6000]) for name in NAVIGATION_CASES]


def control_results():
    """Run upstream imports and local HTTP in a separately bounded child process."""
    return child_results("client_controls.py", CONTROL_CASES)


def child_results(filename, expected):
    """Require complete, consistent evidence from a bounded native-tool child."""
    try:
        result = run([sys.executable, "-B", str(Path(__file__).with_name(filename))],
                     timeout=180 if expected == NATIVE_CASES else 40, limit=1024 * 1024, check=False)
        cases = json.loads(result.stdout)["cases"]
        passed = check_cases(cases, expected)
        if result.code not in (0, 1) or (result.code == 0) != (passed == len(expected)):
            raise RuntimeError("contradictory native process outcome")
        return cases
    except (RuntimeError, OSError, ValueError, KeyError, TypeError) as error:
        return [outcome(name, status="fail", detail=redact(str(error))[:6000]) for name in expected]


if __name__ == "__main__":
    try:
        sys.exit(qualify())
    except (OSError, RuntimeError, ValueError, KeyError) as error:
        print(json.dumps({"setup_error": redact(str(error))}))
        sys.exit(2)
