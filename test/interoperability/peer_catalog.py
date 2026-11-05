"""Real Catalog API/worker phases, executed only inside its owned network namespace."""

import json
import sys
import time
import traceback
import urllib.error
import urllib.parse
import urllib.request
import uuid

from client_contract import PRIVATE, PUBLIC
from peer_contract import A2A, CURRENT, LEGACY, MCP, REGISTRY, TENANT, outcome
from peer_wire import NoRedirect
from qualification import prepare

PREFIX = "/api/v1/federation/"
EXPECTED = {
    "challenge": {
        "skill:triage-skill": "application/ai-skill", "mcp:echo-tools": MCP, "a2a:planner": A2A,
        "catalog:bundle": "application/ai-catalog+json", "bundle:skill": "application/ai-skill", "bundle:mcp": MCP,
        **{f"registry:{name}": REGISTRY for name in ("tools", "skills", "nested", "empty")},
    },
    "primary": {"discover": REGISTRY, "spaces": REGISTRY},
}


def api(path, body=None, *, expected=200, etag=None, token="local-admin", private=False, method=None):
    """Bounded fixed-origin requests with fresh command keys and no redirects."""
    assert path.startswith("/") and not path.startswith("//")
    headers = {"Accept": "application/json", "Authorization": "Bearer " + token}
    data = None
    if body is not None:
        data = json.dumps(body).encode()
        headers.update({"Content-Type": "application/json", "Idempotency-Key": "hf-peer-" + uuid.uuid4().hex})
    if etag:
        headers["If-Match"] = etag
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    try:
        response = opener.open(urllib.request.Request((PRIVATE if private else PUBLIC) + path, data, headers, method=method), timeout=12)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        raw = response.read(2 * 1024 * 1024 + 1)
        assert len(raw) <= 2 * 1024 * 1024, "API body exceeds bound"
        document = json.loads(raw) if raw else None
        assert response.status == expected, f"{path}: expected {expected}, got {response.status}: {document}"
        return document, {key.lower(): value for key, value in response.headers.items()}


def pages(path, *, request=None, identity=None, **query):
    """Small pages ensure that a first-page-only assertion cannot claim completeness."""
    request = api if request is None else request
    identity = (lambda item: item["id"]) if identity is None else identity
    items, seen = [], set()
    for _ in range(30):
        page, _ = request(path + "?" + urllib.parse.urlencode(dict(query, limit=3)))
        assert isinstance(page["items"], list)
        items.extend(page["items"])
        cursor = page.get("nextCursor")
        if not cursor:
            assert len({identity(item) for item in items}) == len(items), "duplicate paginated items"
            return items
        assert cursor not in seen, "pagination loop"
        seen.add(cursor)
        query["cursor"] = cursor
    raise AssertionError("pagination bound exceeded")


def wait_for(label, callback, timeout=65):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = callback()
        if result:
            return result
        time.sleep(0.15)
    raise AssertionError("deadline exceeded: " + label)


def inspect(url, *, profile="hf-owned", expected=201, token="local-admin"):
    return api(PREFIX + "source-inspections", {"url": url, "networkProfile": profile}, expected=expected, token=token)[0]


def connect(inspection):
    body = dict(inspectionId=inspection["id"], displayName="Owned HF peer",
                retrievalMode="indexed", updateMode="manual_review", networkProfile="hf-owned",
                scope={"publishers": ["discover.dev"]}, reason="Qualify owned unmodified HF peer")
    return body


def source(source_id):
    return api(PREFIX + "sources/" + source_id)


def enqueue(source_id):
    _, headers = source(source_id)
    receipt, _ = api(PREFIX + "sources/" + source_id + ":sync", {}, etag=headers["etag"], expected=202)
    assert receipt["existing"] is False, "sync reused a previous Job"
    return receipt["job"]


def completed(job_id):
    def terminal():
        job, _ = api("/api/v1/jobs/" + job_id)
        assert job["state"] not in ("failed", "cancelled"), "sync terminated before recovery"
        return job if job["state"] == "succeeded" else None
    job = wait_for("sync completion", terminal)
    snapshot, _ = api(PREFIX + "snapshots/" + job["result"]["snapshotId"])
    assert snapshot["state"] == "complete" and snapshot["enumerationComplete"] is True
    assert job["id"] == job_id and sync_attempt(snapshot, job_id) == job["attempt"], "completion lost Job attribution"
    return job, snapshot


def sync_attempt(snapshot, job_id, *, request=None):
    """Bind immutable snapshot evidence to the exact worker Job and attempt."""
    action = "source.sync.completed" if snapshot["state"] == "complete" else "source.sync.failed"
    events = pages("/api/v1/audit-events", request=request, action=action)
    matching = [event for event in events if event["targetId"] == snapshot["id"]]
    assert len(matching) == 1, "snapshot lacks exactly one completion audit"
    event = matching[0]
    facts = event["details"]["facts"]
    assert event["action"] == action and event["tenantId"] == snapshot["tenantId"] == TENANT
    assert facts["jobId"] == job_id and facts["jobKind"] == "source.sync", "snapshot belongs to another Job"
    assert facts["sourceId"] == snapshot["sourceId"] and facts["sourceRevision"] == snapshot["sourceRevision"]
    assert facts["state"] == snapshot["state"]
    assert type(facts["jobAttempt"]) is int and facts["jobAttempt"] > 0, "invalid snapshot attempt"
    return facts["jobAttempt"]


def current(source_id):
    return pages("/api/v1/observations", sourceId=source_id, freshness="current")


def identities(members):
    return sorted((item["sourceKey"], item["identityDigest"]) for item in members)


def bootstrap(state, cases):
    prepare()
    for peer, origin in state["origins"].items():
        inspections = [inspect(origin + suffix) for suffix in ("", "/.well-known/ai-catalog.json")]
        for inspection in inspections:
            profile = CURRENT if peer == "challenge" else LEGACY
            assert inspection["sourceType"] == "hybrid" and inspection["profileId"] == profile
            assert inspection["capabilities"]["enumerable"] is True and not inspection.get("registrySearchUrl")
            assert "ARD_MULTIPLE_REGISTRY_ENDPOINTS" in {f["code"] for f in inspection["warnings"]}
            assert inspection["entryCount"] == (8 if peer == "challenge" else 2)
            retained, _ = api(PREFIX + "source-inspections/" + inspection["id"])
            assert retained == inspection, "inspection evidence changed"
        publisher = "discover.dev" if peer == "challenge" else "huggingface.co"
        body = connect(inspections[0])
        body.update(scope={"publishers": [publisher]}, nesting={"enabled": True, "maxDepth": 2, "allowedOrigins": [origin]})
        connected, _ = api(PREFIX + "sources", body, expected=201)
        source_id = connected["id"]
        job, snapshot = completed(enqueue(source_id)["id"])
        members = current(source_id)
        prefix = "urn:air:discover.dev:challenge:" if peer == "challenge" else "urn:air:huggingface.co:registry:"
        expected = {prefix + key: value for key, value in EXPECTED[peer].items()}
        assert {item["identifier"]: item["mediaType"] for item in members} == expected
        assert len(members) == len(expected) == snapshot["memberCount"]
        supported = []
        for item in members:
            assert item["tenantId"] == TENANT and item["sourceId"] == source_id and item["rawEntryAvailable"] is True
            raw, headers = api("/api/v1/observations/" + item["id"] + "/raw")
            assert raw["identifier"] == item["identifier"] and raw["type"] == item["mediaType"]
            assert headers.get("content-digest") and headers.get("content-disposition", "").startswith("attachment;")
            if item.get("normalizedEntryDigest"):
                supported.append(item["identifier"])
        expected_supported = {prefix + key for key, value in EXPECTED[peer].items() if value in (MCP, A2A)} if peer == "challenge" else set()
        assert set(supported) == expected_supported
        retained, _ = source(source_id)
        assert retained["lastCompleteSnapshotId"] == snapshot["id"] == job["result"]["snapshotId"]
        state[peer] = dict(source_id=source_id, snapshot_id=snapshot["id"], identities=identities(members), job=job["id"])
        cases.append(outcome("indexed-" + peer, dict(count=len(members), supported=len(supported),
                     profile=inspections[0]["profileId"], enumerable=True, search="", type=inspections[0]["sourceType"],
                     identifiers=sorted(expected), job_id=job["id"], snapshot_id=snapshot["id"])))
        live = connect(inspections[0])
        live.update(retrievalMode="live", live={"enabled": True, "queryPrivacyAcknowledged": True, "federationMode": "none"})
        rejected, _ = api(PREFIX + "sources", live, expected=412)
        assert rejected["code"] == "PRECONDITION_FAILED"

    challenge = state["origins"]["challenge"]
    primary = inspect(state["origins"]["primary"] + "/search", expected=400)
    assert primary["code"] == "INVALID_ARGUMENT"
    routes = ("/search", "/registries/nested/search", "/registries/deep/search")
    selected = [inspect(challenge + suffix) for suffix in routes]
    for inspection, suffix in zip(selected, routes, strict=True):
        assert inspection["sourceType"] == "ard_registry" and inspection["profileId"] == CURRENT
        assert inspection["registrySearchUrl"] == challenge + suffix and not inspection.get("registryListUrl")
        assert not inspection["capabilities"].get("enumerable")
    cases.append(outcome("selected-search", {"challenge_routes": [i["registrySearchUrl"] for i in selected], "primary_status": 400, "list": False}))
    assignment = [{"host": "localhost", "audience": "authenticated"}]
    live = connect(selected[0])
    live.update(retrievalMode="live", live={"enabled": True, "queryPrivacyAcknowledged": True, "federationMode": "referrals"}, discoveryAssignments=assignment)
    for mode, update in (("indexed", "manual_review"), ("hybrid", "manual_review"), ("live", "automatic")):
        candidate = {**live, "retrievalMode": mode, "updateMode": update}
        if mode == "indexed":
            candidate["live"] = {"enabled": False}
        rejected, _ = api(PREFIX + "sources", candidate, expected=412)
        assert rejected["code"] == "PRECONDITION_FAILED"
    cases.append(outcome("source-admission", {"missing_list_rejections": 3, "manifest_live_rejections": 2}))
    rejected, _ = api(PREFIX + "sources", {**live, "discoveryAssignments": [{"host": "127.0.0.1", "audience": "anonymous"}]}, expected=403)
    assert rejected["code"] == "SOURCE_POLICY_DENIED"
    untrusted = inspect(challenge, profile="hf-untrusted", expected=503)
    wrong_tenant = inspect(challenge, token="other-tenant-admin", expected=403)
    assert untrusted["code"] == "DEPENDENCY_UNAVAILABLE" and wrong_tenant["code"] == "SOURCE_POLICY_DENIED"
    cases.append(outcome("trust-admission", {"untrusted": 503, "wrong_tenant": 403, "anonymous": 403}))
    deep_body = {**live, "inspectionId": selected[2]["id"], "live": {**live["live"], "referralOnly": True, "federationMode": "none"}}
    deep, _ = api(PREFIX + "sources", deep_body, expected=201)
    # The grandchild is authorized and available; only the root's one-hop limit
    # prevents the nested peer's unsolicited deeper referral from reaching it.
    child_body = {**live, "inspectionId": selected[1]["id"], "live": {**live["live"], "referralOnly": True},
                  "referrals": {"enabled": True, "maxHops": 2, "approvedSourceIds": [deep["id"]]}}
    child, _ = api(PREFIX + "sources", child_body, expected=201)
    root, _ = api(PREFIX + "sources", {**live, "referrals": {"enabled": True, "maxHops": 1, "approvedSourceIds": [child["id"]]}}, expected=201)
    state.update(root_id=root["id"], child_id=child["id"], deep_id=deep["id"])


def live(state, cases):
    response, _ = api(PREFIX + "sources:live-search", {"text": "order", "pageSize": 20, "sourceIds": [state["root_id"]], "reason": "Qualify approved HF referral traversal"})
    assert response["results"] == [] and response["partial"] is True
    actual = {(d.get("sourceId"), d["status"]) for d in response["diagnostics"]}
    assert actual == {(source_id, status) for source_id in (state["root_id"], state["child_id"]) for status in ("succeeded", "invalid_response")}, actual
    cases.append(outcome("live-admission", {"results": 0, "partial": True, "diagnostics": response["diagnostics"]}))


def public(state, cases):
    response, headers = api("/ard/v1/search", {"query": {"text": "order"}, "federation": "auto", "pageSize": 20}, token="local-reader", private=True)
    local, _ = api("/ard/v1/search", {"query": {"text": "order"}, "federation": "none", "pageSize": 20}, token="local-reader", private=True)
    assert len(local["results"]) == 3 and response["results"] == local["results"]
    assert headers.get("server-timing") == 'federation;desc="partial"'
    assert set(response) <= {"results", "referrals", "pageToken"} and not response.get("referrals")
    assert all(state[key] not in json.dumps(response) for key in ("root_id", "child_id", "deep_id")), "private source diagnostic leaked"
    cases.append(outcome("public-partial", {"local_results": 3, "partial_signal": headers["server-timing"], "private_diagnostics": False}))


def outage(state, cases):
    previous = state["challenge"]
    baseline = {snapshot["id"] for snapshot in pages(PREFIX + "sources/" + previous["source_id"] + "/snapshots")}
    job = enqueue(previous["source_id"])
    state["recovery_job"] = job["id"]
    def failed():
        snapshots = pages(PREFIX + "sources/" + previous["source_id"] + "/snapshots")
        return next((s for s in snapshots if s["state"] == "failed" and s["id"] not in baseline), None)
    failed_snapshot = wait_for("failed snapshot", failed, timeout=20)
    failed_attempt = sync_attempt(failed_snapshot, job["id"])
    retained, _ = source(previous["source_id"])
    assert retained["lastCompleteSnapshotId"] == previous["snapshot_id"]
    assert identities(current(previous["source_id"])) == [tuple(i) for i in previous["identities"]]
    attempted, _ = api("/api/v1/jobs/" + job["id"])
    assert attempted["attempt"] >= failed_attempt and attempted["state"] in ("running", "waiting")
    state["failed_attempt"] = failed_attempt
    cases.append(outcome("outage", {"job_id": job["id"], "failed_snapshot_id": failed_snapshot["id"], "attempt": failed_attempt, "preserved_members": 10}))


def recovery(state, cases):
    previous = state["challenge"]
    job, snapshot = completed(state["recovery_job"])
    assert job["attempt"] > state["failed_attempt"] and snapshot["id"] != previous["snapshot_id"]
    assert snapshot["sourceId"] == previous["source_id"] and snapshot["memberCount"] == 10
    assert all(snapshot[key] == 0 for key in ("added", "updated", "withdrawn"))
    retained, _ = source(previous["source_id"])
    assert retained["lastCompleteSnapshotId"] == snapshot["id"]
    assert identities(current(previous["source_id"])) == [tuple(i) for i in previous["identities"]]
    cases.append(outcome("recovery", {"job_id": job["id"], "attempt": job["attempt"], "members": 10, "added": 0, "updated": 0, "withdrawn": 0}))


def trust(state, _cases):
    from peer_trust import qualify
    state["trust"] = qualify(api, state["challenge"]["source_id"], reader="local-reader", other="other-tenant-admin")


def main():
    if not __debug__:
        raise RuntimeError("peer qualification requires assertions")
    state = json.loads(sys.argv[2])
    cases = []
    try:
        {"bootstrap": bootstrap, "live": live, "public": public, "outage": outage, "recovery": recovery, "trust": trust}[sys.argv[1]](state, cases)
        print(json.dumps({"state": state, "cases": cases}))
        return 0
    except (AssertionError, RuntimeError, OSError, ValueError, KeyError, TypeError) as error:
        print(json.dumps({"state": state, "cases": cases, "error": str(error) or traceback.format_exc(limit=2)}))
        return 1


if __name__ == "__main__":
    sys.exit(main())
