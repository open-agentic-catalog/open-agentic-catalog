"""Real Catalog live discovery against the unchanged local ANS services."""

import base64
import hashlib
import json
from pathlib import Path
import sys
import traceback
import urllib.parse
import urllib.request

if __package__ in (None, ""):
    sys.path.insert(0, str(Path(__file__).resolve().parents[1]))

from agntcy_lab import LocalHTTP, UnexpectedStatus
from peer_catalog import pages, wait_for
from peer_contract import CURRENT
from ans import native

PREFIX = "/api/v1/federation/"
SEARCH = "/api/v1/catalog/search"
PHASES = ("bootstrap", "discovery", "shared", "revocation", "feed-outage", "feed-recovery", "finder-outage", "finder-recovery")


class DiscoveryHost(urllib.request.BaseHandler):
    """Select the configured authenticated host without changing the loopback destination."""
    def http_request(self, request):
        if urllib.parse.urlsplit(request.full_url).path == "/ard/v1/search":
            request.add_unredirected_header("Host", "localhost")
        return request


class Catalog(LocalHTTP):
    # Exceed both the Catalog's and HTTPS edge's five-second dependency deadlines.
    timeout = 12

    def __init__(self, settings, *, authenticated_host=True):
        super().__init__({"catalog": settings["catalog"]}, settings["ca"], settings["token"])
        if authenticated_host:
            self.opener.add_handler(DiscoveryHost())
        self.settings, self.captures = settings, []

    def request(self, peer, path, body=None, **options):
        try:
            raw, headers = super().request(peer, path, body, **options)
        except UnexpectedStatus as error:
            self.capture(path, error.status, error.raw)
            raise
        self.capture(path, options.get("expected", 200), raw)
        return raw, headers

    def capture(self, path, status, raw):
        assert len(self.captures) < 256, "Catalog capture bound exceeded"
        assert not any(self.settings[key].encode() in raw for key in ("token", "reader", "other", "key")), "credential in response"
        self.captures.append(dict(path=path, status=status, sha256=hashlib.sha256(raw).hexdigest(), raw_base64=base64.b64encode(raw).decode()))

    def document(self, path, body=None, *, token=None, **options):
        saved = self.token
        try:
            self.token = saved if token is None else token
            return super().document("catalog", path, body, **options)
        finally:
            self.token = saved

    def pages(self, path, **query):
        return pages(path, request=self.document, **query)


def identifiers(names):
    return {"urn:air:" + name + ".ans.test:agents:" + name for name in names}


def assert_live(rows, state, entries, *, variants=False):
    actual = [row["identifier"] for row in rows]
    if variants:
        expected = {(row["identifier"], row["type"], row["url"]) for row in entries}
        selected = [(row["identifier"], row["entry"]["type"], row["entry"]["url"]) for row in rows]
        assert len(selected) == len(set(selected)) and set(selected) == expected, "Catalog native variants differ"
    else:
        assert len(actual) == len(set(actual)) and set(actual) == {entry["identifier"] for entry in entries}, "Catalog live membership differs"
    for row in rows:
        assert row["sourceId"] == state["source_id"] and row["source"] == state["origin"]
        assert row["versionPresent"] is True and row["version"] == "1.0.0"
        assert row["entry"] in entries, "Catalog changed native metadata, trust extension or delivery"
        assert row["entry"]["trustManifest"]["identity"] == "https://" + row["identifier"].split(":")[2]
        if "kind" in row:
            assert row["kind"] == {native.MCP: "mcp_server", native.A2A: "agent"}[row["entry"]["type"]], "Catalog kind differs from native media type"


def assert_ard(rows, state, entries):
    actual = [row["identifier"] for row in rows]
    expected = set(state["locals"]) | {entry["identifier"] for entry in entries}
    assert len(actual) == len(set(actual)) and set(actual) == expected, "ARD exact local/live membership differs"
    for row in rows:
        assert "TrustManifest" not in row, "ARD emitted uppercase trust metadata"
        if row["identifier"] in state["locals"]:
            assert "trustManifest" not in row, "ARD local result inherited peer trust metadata"
    for entry in entries:
        row = next(row for row in rows if row["identifier"] == entry["identifier"])
        for field in ("identifier", "version", "displayName", "type", "url", "description", "tags", "capabilities", "metadata", "score", "source"):
            assert row.get(field) == entry.get(field), "ARD changed native " + field
        # Optional under ARD, but selected Catalog behavior preserves admitted claims.
        assert row.get("trustManifest") == entry["trustManifest"], "ARD changed disclosed trust metadata"
    assert state["source_id"] not in json.dumps(rows), "private source ID leaked through ARD"


def ard_contracts(document):
    # Preserve the original Search outcome; its lowercase trust member is open.
    original = native.validate(document, "SearchResponse")
    assert not original, "ARD response violates original pinned Search schema"
    alias, checked = [], 0
    for index, row in enumerate(document["results"]):
        if "trustManifest" not in row:
            continue
        assert "TrustManifest" not in row, "ARD emitted conflicting trust spellings"
        fields = {**row, "TrustManifest": row["trustManifest"]}
        del fields["trustManifest"]
        alias.extend({"result": index, **error} for error in native.validate(fields, "EntryFields"))
        checked += 1
    assert not alias, "ARD lowercase trust metadata violates the pinned field schema"
    return dict(original_search_errors=original, lowercase_alias_errors=alias, trust_manifests_checked=checked)


def direct(api, state, text="order"):
    return api.document(PREFIX + "sources:live-search", dict(text=text, pageSize=20, sourceIds=[state["source_id"]], reason="Qualify unchanged local ANS Finder"))[0]


def controls(api, peer, state, names):
    entries, native_pages = native.traverse(peer, {"text": "order"})
    assert {entry["identifier"] for entry in entries} == identifiers(names)
    before = edge_requests(peer)
    response = direct(api, state)
    assert response["partial"] is False, response
    assert_live(response["results"], state, entries)
    observed = edge_requests(peer)[len(before):]
    assert len(observed) == len(entries), "Catalog did not traverse the native one-result pages"
    assert not observed[0]["body"].get("pageToken")
    tokens = [row["body"].get("pageToken") for row in observed[1:]]
    assert all(tokens) and len(tokens) == len(set(tokens)), "Catalog reused a peer token"
    assert tokens == [page["pageToken"] for page in native_pages[:-1]], "Catalog did not use the returned native tokens"
    assert all(row["body"]["query"]["text"] == "order" and row["body"]["federation"] == "none" and not row["authorizationPresent"] for row in observed)
    mixed, _ = api.document(SEARCH, dict(text="order", limit=20), token=api.settings["reader"])
    live = [row for row in mixed["items"] if row["type"] == "live"]
    assert mixed["partial"] is False and len(live) == len(entries)
    assert_live([{**row, **row["live"]} for row in live], state, entries)
    local = sorted(row["identifier"] for row in mixed["items"] if row["type"] == "local")
    assert state["locals"] == local, "healthy local results lost"
    public, headers = api.document("/ard/v1/search", dict(query={"text": "order"}, federation="auto", pageSize=20), token=api.settings["reader"])
    contract = ard_contracts(public)
    assert not headers.get("warning")
    assert_ard(public["results"], state, entries)
    return dict(direct=response, mixed=mixed, ard=public, ard_contract=contract, peer_requests=observed, entries=entries)


def edge_requests(peer):
    body = peer.document("finder-edge", "/_qualification/requests")
    assert body["overflow"] is False
    return body["requests"]


def bootstrap(api, peer, state):
    origin = api.settings["origin"]
    inspect_body = dict(url=origin + "/v1/search", networkProfile="functional-fixture")
    for changes, token, status, code in (({}, api.settings["reader"], 403, "FORBIDDEN"), ({}, api.settings["other"], 403, "SOURCE_POLICY_DENIED"), ({"networkProfile": "ans-untrusted"}, None, 503, "DEPENDENCY_UNAVAILABLE")):
        result, _ = api.document(PREFIX + "source-inspections", {**inspect_body, **changes}, token=token, expected=status)
        assert result["code"] == code
    inspection, _ = api.document(PREFIX + "source-inspections", inspect_body, expected=201)
    assert inspection["sourceType"] == "ard_registry" and inspection["profileId"] == CURRENT
    assert inspection["resolvedUrl"] == inspection["registrySearchUrl"] == origin + "/v1/search"
    assert not inspection.get("registryListUrl") and inspection["capabilities"]["enumerable"] is False
    retained, _ = api.document(PREFIX + "source-inspections/" + inspection["id"])
    assert retained == inspection
    connect = dict(inspectionId=inspection["id"], displayName="Owned ANS Finder", retrievalMode="live", updateMode="manual_review", networkProfile="functional-fixture", scope={"publishers": [name + ".ans.test" for name in ("alpha", "beta", "gamma", "shared")]}, reason="Qualify native local live discovery", live=dict(enabled=True, queryPrivacyAcknowledged=True, federationMode="none"), discoveryAssignments=[dict(host="localhost", audience="authenticated")])
    refused, _ = api.document(PREFIX + "sources", {**connect, "retrievalMode": "indexed", "live": dict(enabled=False)}, expected=412)
    assert refused["code"] == "PRECONDITION_FAILED"
    refused, _ = api.document(PREFIX + "sources", {**connect, "discoveryAssignments": [dict(host="127.0.0.1", audience="anonymous")]}, expected=403)
    assert refused["code"] == "SOURCE_POLICY_DENIED"
    source, _ = api.document(PREFIX + "sources", connect, expected=201)
    assert source["retrievalMode"] == "live" and source["registrySearchUrl"] == origin + "/v1/search"
    resources = api.pages("/api/v1/resources")
    assert len(resources) == 3 and {row["kind"] for row in resources} == {"agent", "mcp_server", "skill"}, "governed smoke inventory differs"
    state.update(source_id=source["id"], inspection_id=inspection["id"], origin=origin, resources=sorted(row["id"] for row in resources), locals=sorted(row["canonicalIdentifier"] for row in resources))
    api.document(PREFIX + "sources/" + source["id"], token=api.settings["other"], expected=404)
    before = edge_requests(peer)
    refused, _ = api.document("/ard/v1/search", dict(query={"text": "order"}, federation="auto"), token="", expected=401)
    assert refused["errorCode"] == "UNAUTHENTICATED" and edge_requests(peer) == before, "unauthenticated discovery contacted the private peer"
    snapshot, headers = api.document("/api/v1/publication/snapshots", dict(host="localhost"), expected=201)
    assert snapshot["host"] == "localhost" and snapshot["audience"] == "authenticated"
    active, _ = api.document("/api/v1/publication/snapshots/" + snapshot["id"] + ":activate", dict(host="localhost", reason="Publish the owned authenticated discovery control"), etag=headers["etag"])
    assert active["state"] == "active"
    public = Catalog(api.settings, authenticated_host=False)
    anonymous, _ = public.document("/ard/v1/search", dict(query={"text": "order"}, federation="auto", pageSize=20), token="")
    contract = ard_contracts(anonymous)
    assert len(anonymous["results"]) == 3
    assert_ard(anonymous["results"], state, [])
    assert edge_requests(peer) == before, "anonymous host contacted the private peer"
    api.captures.extend(public.captures)
    return dict(inspection=inspection, source=source, authenticated_snapshot=active, anonymous_local_only=anonymous, ard_contract=contract)


def discovery(api, peer, state):
    result = controls(api, peer, state, ("alpha", "beta", "gamma"))
    query = dict(text="order", origins=["live"], limit=1)
    initial, _ = api.document(SEARCH, query, token=api.settings["reader"])
    assert initial.get("nextCursor")
    state["cursor"] = initial["nextCursor"]
    seen, cursors, pages_seen = [], set(), []
    response = initial
    for _ in range(4):
        pages_seen.append(response)
        assert response["partial"] is False and len(response["items"]) == 1
        seen.extend(row["identifier"] for row in response["items"])
        cursor = response.get("nextCursor")
        if not cursor:
            break
        assert cursor not in cursors
        cursors.add(cursor)
        response, _ = api.document(SEARCH, {**query, "cursor": cursor}, token=api.settings["reader"])
    assert len(seen) == 3 and len(set(seen)) == 3 and set(seen) == identifiers(("alpha", "beta", "gamma"))
    result["catalog_pages"] = pages_seen
    return result


def shared(api, peer, state):
    entries, _ = native.traverse(peer, {"text": "shared"})
    assert len(entries) == 2 and {entry["type"] for entry in entries} == {native.MCP, native.A2A}
    response = direct(api, state, "shared")
    assert response["partial"] is False and len(response["results"]) == 2
    assert_live(response["results"], state, entries, variants=True)
    filtered = {}
    for media_type, kind in ((native.MCP, "mcp_server"), (native.A2A, "agent")):
        result, _ = api.document(SEARCH, dict(text="shared", origins=["live"], kinds=[kind], limit=20), token=api.settings["reader"])
        assert result["partial"] is False and len(result["items"]) == 1
        row = result["items"][0]
        assert row["type"] == "live" and row["kind"] == kind and row["live"]["entry"] in entries and row["live"]["entry"]["type"] == media_type
        filtered[kind] = result
    before = edge_requests(peer)
    context = {"identifier": "urn:example:identifier"}
    rejected, _ = api.document(PREFIX + "sources:live-search", {"text": "order", "pageSize": 20, "sourceIds": [state["source_id"]], "reason": "Characterize native context refusal", "@context": context})
    observed = edge_requests(peer)[len(before):]
    assert len(observed) == 1 and observed[0]["body"]["query"]["@context"] == context
    assert rejected["partial"] is True and rejected["results"] == [] and "failed" in {row["status"] for row in rejected["diagnostics"]}
    return dict(native=entries, catalog=response, kinds=filtered, context=rejected, disposition="private administrator Search preserves exact native variants; reader Search selects either format with its kind filter")


def revocation(api, peer, state):
    baseline = json.loads((Path(api.settings["output"]) / "qualification.json").read_text())
    registration = next(row for row in baseline["phases"]["registrations"] if row["host"] == "beta.ans.test")
    revoked = peer.document("ra", "/v1/agents/" + registration["agentId"] + "/revoke", dict(reason="CESSATION_OF_OPERATION", comments="Owned interoperability revocation"), auth=True)
    assert revoked["status"] == "REVOKED" and revoked["agentId"] == registration["agentId"]
    def delivered():
        feed = peer.document("ra", "/v1/agents/events?limit=100")
        matching = [row for row in feed["items"] if row["eventType"] == "AGENT_REVOKED" and row["agentId"] == registration["agentId"]]
        return matching[0] if matching else None
    event = wait_for("acknowledged ANS revocation", delivered, timeout=60)
    assert event["ansName"] == registration["ansName"] and event["logId"]
    def removed():
        rows, _ = native.traverse(peer, {"text": "order"})
        return rows if {row["identifier"] for row in rows} == identifiers(("alpha", "gamma")) else None
    wait_for("Finder removal", removed, timeout=30)
    explore = peer.document("finder-edge", "/v1/explore", dict(query={"text": "order"}, resultType={"facets": [dict(field="tags")]}))
    assert not native.validate(explore, "ExploreResponse")
    assert "beta" not in {bucket["value"] for bucket in explore["facets"]["tags"]["buckets"]}
    problem, _ = api.document(SEARCH, dict(text="order", origins=["live"], limit=1, cursor=state["cursor"]), token=api.settings["reader"], expected=400)
    assert problem["code"] == "INVALID_ARGUMENT" and "restart" in problem["detail"]
    result = controls(api, peer, state, ("alpha", "gamma"))
    initial, _ = api.document(SEARCH, dict(text="order", origins=["live"], limit=1), token=api.settings["reader"])
    assert initial["partial"] is False and len(initial["items"]) == 1
    first = initial["items"][0]["identifier"]
    assert first in identifiers(("alpha", "gamma"))
    state["cursor"] = initial["nextCursor"]
    state["continuation_identifiers"] = sorted(identifiers(("alpha", "gamma")) - {first})
    return dict(**result, event=event, revoked=revoked, explore=explore, restart=problem)


def unavailable(api, peer, state, phase):
    if phase == "feed-outage":
        def stale():
            response = peer.document("finder-edge", "/v1/search", dict(query={"text": "order"}, federation="none", pageSize=1))
            return response if response.get("staleSince") else None
        wire = wait_for("native stale feed signal", stale, timeout=25)
        errors = native.validate(wire, "SearchResponse")
        assert errors and any(error["rule"] == "additionalProperties" for error in errors), "unexpected stale schema outcome"
        assert not native.validate({key: value for key, value in wire.items() if key != "staleSince"}, "SearchResponse"), "stale response has another schema defect"
        status = "invalid_response"
    else:
        peer.request("finder-edge", "/healthz")
        wire = peer.request("finder-edge", "/v1/search", dict(query={"text": "order"}), expected=502).decode()
        errors, status = [], "failed"
    result = direct(api, state)
    assert result["results"] == [] and result["partial"] is True
    assert status in {row["status"] for row in result["diagnostics"]}, result
    mixed, _ = api.document(SEARCH, dict(text="order", limit=20), token=api.settings["reader"])
    assert mixed["partial"] is True and mixed["liveCount"] == 0
    assert sorted(row["identifier"] for row in mixed["items"] if row["type"] == "local") == state["locals"]
    ard, headers = api.document("/ard/v1/search", dict(query={"text": "order"}, federation="auto", pageSize=20), token=api.settings["reader"])
    contract = ard_contracts(ard)
    assert_ard(ard["results"], state, [])
    assert 'federation;desc="partial"' in headers.get("server-timing", "")
    problem, _ = api.document(SEARCH, dict(text="order", origins=["live"], limit=1, cursor=state["cursor"]), token=api.settings["reader"], expected=503)
    assert problem["code"] == "DEPENDENCY_UNAVAILABLE"
    return dict(native=wire, schema_errors=errors, direct=result, mixed=mixed, ard=ard, ard_contract=contract, retry_same_cursor=problem)


def recovery(api, peer, state):
    def fresh():
        body = peer.document("finder-edge", "/v1/search", dict(query={"text": "order"}, federation="none", pageSize=1))
        return body if not body.get("staleSince") and body.get("results") else None
    wait_for("Finder fresh retained state", fresh, timeout=40)
    result = controls(api, peer, state, ("alpha", "gamma"))
    replay, _ = api.document(SEARCH, dict(text="order", origins=["live"], limit=1, cursor=state["cursor"]), token=api.settings["reader"])
    assert replay["partial"] is False and len(replay["items"]) == 1 and not replay.get("nextCursor")
    assert_live([{**row, **row["live"]} for row in replay["items"]], state, [entry for entry in result["entries"] if entry["identifier"] in state["continuation_identifiers"]])
    result["same_cursor_recovered"] = replay
    return result


def main():
    assert __debug__, "ANS qualification requires assertions"
    phase, settings_path, state_path, output = sys.argv[1:]
    assert phase in PHASES
    settings = json.loads(Path(settings_path).read_text())
    native.API_SCHEMA = json.loads(Path(settings["schema"]).read_text())
    state_file = Path(state_path)
    state = json.loads(state_file.read_text()) if state_file.exists() else {}
    api, peer = Catalog(settings), native.Native(settings)
    report = dict(phase=phase, status="fail")
    try:
        if phase == "bootstrap":
            evidence = bootstrap(api, peer, state)
        elif phase == "discovery":
            evidence = discovery(api, peer, state)
        elif phase == "shared":
            evidence = shared(api, peer, state)
        elif phase == "revocation":
            evidence = revocation(api, peer, state)
        elif phase.endswith("outage"):
            evidence = unavailable(api, peer, state, phase)
        else:
            evidence = recovery(api, peer, state)
        assert sorted(row["id"] for row in api.pages("/api/v1/resources")) == state["resources"], "live discovery created Resources"
        assert api.pages("/api/v1/observations", sourceId=state["source_id"]) == []
        assert api.pages(PREFIX + "sources/" + state["source_id"] + "/snapshots") == []
        report.update(status="pass", evidence=evidence)
    except (AssertionError, RuntimeError, OSError, ValueError, KeyError, TypeError) as error:
        frame = traceback.extract_tb(error.__traceback__)[-1]
        report["error"] = f"{type(error).__name__} at {Path(frame.filename).name}:{frame.lineno}: {error}"
    report.update(state=state, captures=api.captures, native_captures=peer.captures)
    raw = json.dumps(report, indent=2)
    for key in ("key", "token", "reader", "other"):
        raw = raw.replace(settings[key], "[redacted]")
    Path(output).write_text(raw + "\n")
    Path(output).chmod(0o600)
    state_file.write_text(json.dumps(state))
    state_file.chmod(0o600)
    print(json.dumps(dict(phase=phase, status=report["status"], error=report.get("error", ""))))
    return int(report["status"] != "pass")


if __name__ == "__main__":
    sys.exit(main())
