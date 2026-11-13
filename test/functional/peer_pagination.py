"""Final client-pagination qualification inside the owned two-Catalog journey."""

import base64
import json

from federation import key, observations
from http_client import ClientFailure, decode, response_date
from journeys import header
from runner import Failure, command, http_failure_details, validate_search_page_failure, write_document


def cursor_clock(token, previous_headers, failed_date):
    """Compare opaque cursor timing only after failure, without retaining its bytes.

    This is diagnostic evidence, not token authentication or client authority.
    It never decides whether to accept, retry or restart a request.
    """
    if not isinstance(token, str) or len(token) > 4096 or type(failed_date) is not int:
        return None
    previous_date = response_date(previous_headers or {})
    if previous_date is None:
        return None
    parts = token.split(".")
    if len(parts) == 3 and parts[0] in ("lc1", "h1"):
        encoded = parts[1]
    elif len(parts) == 2:
        encoded = parts[0]
    else:
        return None
    try:
        raw = base64.b64decode(encoded + "=" * (-len(encoded) % 4), altchars=b"-_", validate=True)
        if base64.urlsafe_b64encode(raw).rstrip(b"=").decode("ascii") != encoded:
            return None
        expiry = decode(raw).get("e")
        if type(expiry) is not int:
            return None
        values = {"responseDeltaSeconds": failed_date - previous_date,
                  "cursorExpiryMinusResponseDateSeconds": expiry - failed_date}
        return values if all(-86400 <= value <= 86400 for value in values.values()) else None
    except (AttributeError, UnicodeError, ValueError, ClientFailure):
        return None


def request_page(client, path, body, page_number, *, previous_headers=None):
    """Attach bounded evidence to a failed page without retrying its request."""
    try:
        return client.request("POST", path, body)
    except Exception as error:
        if path in ("/api/v1/catalog/search", "/ard/v1/search"):
            try:
                value = {
                    "surface": "public" if path == "/ard/v1/search" else "control",
                    "page": page_number, "completedPages": page_number - 1,
                    "continuation": bool(body.get("pageToken" if path == "/ard/v1/search" else "cursor")),
                    "httpFailure": http_failure_details(error),
                }
                timing = cursor_clock(body.get("pageToken" if path == "/ard/v1/search" else "cursor"),
                                      previous_headers, getattr(error, "response_date", None))
                if timing is not None:
                    value["clock"] = timing
                error.search_page_failure = validate_search_page_failure(value)
            except (AttributeError, TypeError, ValueError, Failure):
                pass
        raise


def valid_evidence(value):
    """Require both retrieval modes and actual CLI/browser completion."""
    return (isinstance(value, dict) and set(value) == {"core", "hybrid", "liveOnly", "cli", "browser"}
            and all(value[name] is True for name in ("core", "hybrid", "liveOnly", "cli"))
            and value["browser"] == [{"mode": mode, "livePages": 2, "combinedPages": 2,
                                     "sameTokenRetry": True, "sourceComparison": True, "cleanup": "passed"} for mode in ("core", "hybrid")]
            and all(type(row["livePages"]) is int and type(row["combinedPages"]) is int
                    and row["sameTokenRetry"] is True and row["sourceComparison"] is True for row in value["browser"]))


def walk(scenario, client, path, body, expected, *, public=False, tier=None):
    """Traverse exact signed positions; reject repeated, missing or oversized pages."""
    field, token_field = ("results", "pageToken") if public else ("items", "nextCursor")
    cursor_field = "pageToken" if public else "cursor"
    query = dict(body)
    seen, tokens, pages = [], set(), []
    durable_only = False
    previous_headers = None
    limit = body["pageSize" if public else "limit"]
    for page_number in range(1, 65):
        before = scenario.stats()
        page, headers = request_page(client, path, query, page_number, previous_headers=previous_headers)
        cursor = query.get(cursor_field, "")
        after = scenario.stats()
        prefix = "peer-a.test:8443 POST /ard/v1/search none "
        contacts = sum(count for name, count in after.items() if name.startswith(prefix)) - sum(count for name, count in before.items() if name.startswith(prefix))
        assert contacts == 1 if not cursor else contacts in (0, 1), "client exceeded its peer replay budget"
        assert not durable_only or contacts == 0, "client resumed live work after exhausting its window"
        durable_only = contacts == 0
        scenario.assert_contacts(before, "peer-a.test", contacts)
        rows = page[field]
        assert 0 < len(rows) <= limit, "client page empty or oversized"
        if tier:
            assert (header(headers, "Catalog-Search-Tier") if public else page["tier"]) == tier, "client retrieval tier changed"
        seen.extend(row["identifier"] for row in rows)
        pages.append(page)
        token = page.get(token_field, "")
        if not token:
            assert set(seen) == set(expected) and len(seen) == len(expected), "client traversal lost or repeated identities"
            return pages
        assert isinstance(token, str) and len(token) <= 4096 and token not in tokens, "client token invalid or repeated"
        tokens.add(token)
        query[cursor_field] = token
        previous_headers = headers
    raise AssertionError("client pagination did not terminate")


def retry_control(scenario, client, path, body, token_field):
    """A real peer outage returns no page; the identical cursor recovers."""
    first = client.request("POST", path, body)[0]
    assert first.get(token_field, "").startswith("lc1."), "missing composite continuation"
    cursor_field = "pageToken" if token_field == "pageToken" else "cursor"
    continuation = {**body, cursor_field: first[token_field]}
    healthy = client.request("POST", path, continuation)[0]
    try:
        scenario.continuation_fault(True, initial=True)
        problem = client.request("POST", path, continuation, expected=(503,))[0]
        assert (problem.get("status") == 503 if token_field == "nextCursor" else problem.get("errorCode") == "DEPENDENCY_UNAVAILABLE") and not any(name in problem for name in ("items", "results", token_field)), "failed replay disclosed or advanced a page"
    finally:
        scenario.continuation_fault(False)
    recovered = client.request("POST", path, continuation)[0]
    assert recovered == healthy, "same-token recovery changed the healthy page"
    before = scenario.stats()
    changed = {"query": {"text": "changed"}} if token_field == "pageToken" else {"text": "changed"}
    client.request("POST", path, {**continuation, **changed}, expected=(400,))
    scenario.assert_contacts(before, "peer-a.test", 0)
    return first


def browser(scenario, directory, mode, expected, live_ids, source):
    retained, _ = scenario.b.request("GET", "/api/v1/federation/sources/" + source["id"])
    snapshot_id = retained["lastCompleteSnapshotId"]
    snapshot, _ = scenario.b.request("GET", "/api/v1/federation/snapshots/" + snapshot_id)
    identifiers = sorted(row["identifier"] for row in observations(scenario.b, source["id"]))
    assert snapshot["state"] == "complete" and snapshot["memberCount"] == len(identifiers) and len(set(identifiers)) == len(identifiers), "owned comparison requires unique complete membership"
    comparison = {"sourceId": source["id"], "snapshotId": snapshot_id, "sourceRevision": snapshot["sourceRevision"], "identifiers": identifiers}
    write_document(directory / "scenario.json", {"mode": mode, "expected": sorted(expected), "live": sorted(live_ids), "comparison": comparison})
    result = json.loads(command(["node", "catalog-ui/scripts/peer-journeys.mjs", str(directory), scenario.b.base_url,
                                scenario.public.base_url, str(scenario.fixture / "trust/functional-ca.pem")], scenario.env, 120, capture=True))
    assert result == {"mode": mode, "livePages": 2, "combinedPages": 2, "sameTokenRetry": True, "sourceComparison": True, "cleanup": "passed"}, "browser peer qualification incomplete"
    return result


def semantic(scenario, enabled):
    """Only this invocation's B runtimes change; owner-only SQL stays explicit."""
    scenario.compose("stop", "--timeout", "10", "api", "worker")
    if enabled:
        scenario.compose("exec", "-T", "postgres", "psql", "-U", "catalog", "-d", "catalog", "-v", "ON_ERROR_STOP=1", "-c", "CREATE EXTENSION IF NOT EXISTS vector")
        scenario.compose("run", "--rm", "--no-deps", "migrate", "migrate", "apply", "--semantic", "--config", "/config/config.json", "--api-role", "catalog_api", "--worker-role", "catalog_worker")
        scenario.env["CATALOG_SEMANTIC_SEARCH"] = json.dumps({"enabled": True, "endpoint": "https://federation-fixture:8443/v1/embeddings",
            "model": "peer-pagination", "profileRevision": "one", "dimensions": 3, "similarityCutoff": 0.5,
            "caFile": "/etc/open-agentic-catalog/federation/trust/functional-ca.pem", "allowedAddressPrefixes": [scenario.env["CATALOG_FUNCTIONAL_SUBNET"]], "batchSize": 8})
    else:
        scenario.env.pop("CATALOG_SEMANTIC_SEARCH", None)
    scenario.compose("up", "--detach", "--no-deps", "--force-recreate", "api", "worker")
    def ready():
        try:
            return scenario.b.request("GET", "/readyz", retry_unavailable=True)[0].get("status") == "ready"
        except (ClientFailure, AssertionError):
            return False
    scenario.wait(ready)
    scenario.compose("exec", "-T", "worker", "/usr/local/bin/catalogctl", "probe", "--url", "http://127.0.0.1:9090/readyz")


def qualify(scenario, source, fresh_ids):
    """Add real peer/CLI/browser/hybrid evidence without replacing lifecycle checks."""
    before_index = observations(scenario.b, source["id"])
    core_expected = scenario.b_private_native | fresh_ids | {x["identifier"] for x in before_index}
    core_body = {"text": "peer", "origins": ["local", "indexed", "live"], "limit": 1}
    walk(scenario, scenario.b, "/api/v1/catalog/search", core_body, core_expected, tier="core")
    walk(scenario, scenario.b, "/api/v1/catalog/search", {"text": "freshpeer", "origins": ["live"], "limit": 1}, fresh_ids, tier="core")
    retry_control(scenario, scenario.b, "/api/v1/catalog/search", core_body, "nextCursor")
    retry_control(scenario, scenario.b_private, "/ard/v1/search", {"query": {"text": "peer"}, "federation": "auto", "pageSize": 1}, "pageToken")
    cursor, seen = "", []
    for _ in range(len(fresh_ids)):
        args = ["exec", "-T", "-e", "CATALOG_REMOTE_TOKEN=local-admin", "api", "/usr/local/bin/catalogctl", "remote", "search", "--base-url", "http://127.0.0.1:8080", "--text", "freshpeer", "--origin", "live", "--limit", "1"]
        if cursor:
            args += ["--cursor", cursor]
        page = json.loads(scenario.compose(*args, capture=True))["data"]
        assert len(page["items"]) == 1 and page["durableTotal"] == 0 and page["items"][0]["type"] == "live", "CLI live page invalid"
        seen.append(page["items"][0]["identifier"])
        cursor = page.get("nextCursor", "")
    assert not cursor and set(seen) == fresh_ids and len(seen) == len(fresh_ids), "CLI opaque traversal lost or repeated results"

    # The real UI page size is 24. Twenty-five peers exercise its unmodified
    # request and pagination controls, without intercepting responses or limits.
    extras = [scenario.create_item(f"browserpeer-{i:02d}") for i in range(25)]
    live_ids = {row["resource"]["canonicalIdentifier"] for row in extras}
    scenario.snapshot(scenario.publisher_client, "peer-a.test")
    directory = scenario.fixture / "peer-ui"
    directory.mkdir(mode=0o700)
    command(["go", "build", "-o", str(directory / "catalog-ui"), "./catalog-ui/cmd/catalog-ui"], scenario.env, 120)
    command(["npm", "--prefix", "catalog-ui", "run", "build", "--", "--outDir", str(directory / "assets")], scenario.env, 120)
    expected = core_expected | live_ids
    core_browser = browser(scenario, directory, "core", expected, live_ids, source)
    enabled = False
    try:
        enabled = True
        semantic(scenario, True)
        scenario.wait(lambda: scenario.b.request("GET", "/api/v1/capabilities")[0]["search"].get("indexing", {}).get("state") == "ready", timeout=120)
        durable = scenario.b.request("POST", "/api/v1/catalog/search", {"text": "peer", "origins": ["local", "indexed"], "limit": 100})[0]
        assert durable["tier"] == "hybrid" and not durable.get("partialIndex") and durable["durableTotal"] == len(core_expected - fresh_ids), "hybrid durable baseline incomplete"
        pages = walk(scenario, scenario.b, "/api/v1/catalog/search", core_body, expected, tier="hybrid")
        flattened = [row for page in pages for row in page["items"]]
        count = len(durable["items"])
        assert flattened[:count] == durable["items"] and all(row["type"] == "live" for row in flattened[count:]), "hybrid prefix reordered or live tail interleaved"
        retry_control(scenario, scenario.b, "/api/v1/catalog/search", core_body, "nextCursor")
        public_body = {"query": {"text": "peer", "filter": {"metadata.license": "Apache-2.0"}}, "federation": "auto", "pageSize": 1}
        scenario.wait(lambda: header(scenario.b_private.request("POST", "/ard/v1/search", {**public_body, "federation": "none"})[1], "Catalog-Search-Tier") == "hybrid", timeout=120)
        public_pages = walk(scenario, scenario.b_private, "/ard/v1/search", public_body, expected, public=True, tier="hybrid")
        local = scenario.b_private.request("POST", "/ard/v1/search", {**public_body, "federation": "none", "pageSize": 100})[0]["results"]
        assert [row for page in public_pages for row in page["results"]][:len(local)] == local, "public hybrid prefix changed"
        retry_control(scenario, scenario.b_private, "/ard/v1/search", public_body, "pageToken")
        hybrid_browser = browser(scenario, directory, "hybrid", expected, live_ids, source)
    finally:
        scenario.continuation_fault(False)
        if enabled:
            semantic(scenario, False)
        for item in extras:
            path = "/api/v1/listing-releases/" + item["release"]["id"]
            _, headers = scenario.publisher_client.request("GET", path)
            scenario.publisher_client.request("POST", path + ":retire", {"reason": "Remove owned browser pagination controls"},
                headers={"Idempotency-Key": key("peers"), "If-Match": header(headers, "ETag")})
        scenario.snapshot(scenario.publisher_client, "peer-a.test")
        assert scenario.public.request("POST", "/ard/v1/search", {"query": {"text": "browserpeer"}, "federation": "none", "pageSize": 1})[0]["results"] == [], "temporary browser releases remained published"
    assert observations(scenario.b, source["id"]) == before_index, "client traversal persisted live observations"
    return {"core": True, "hybrid": True, "liveOnly": True, "cli": True, "browser": [core_browser, hybrid_browser]}
