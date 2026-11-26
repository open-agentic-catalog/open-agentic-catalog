"""Compare native anonymous reads with a separately authenticated, disposable Catalog."""

from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import ssl
import sys
from threading import Thread

from agentfinder_contract import TENANT, check_raw, digest
from agentfinder_runner import Catalog, PREFIX
from agentic_registry_native import LIST, MANIFEST, MCP, ORIGIN, PUBLISHER, REGISTRY, SEARCH, SKILL, Registry, exchange, inventory, observe, request
from peer_catalog import sync_attempt, wait_for
from peer_contract import CURRENT, has_credential_header
from publication_replay_client import complete

RESULT = Path("/result")


def allows_discovery_request(method, path, headers):
    return (method, path) in {
        ("GET", MANIFEST), ("GET", "/.well-known/ai-catalog.json"),
        ("GET", LIST + "?pageSize=1"), ("POST", SEARCH),
    } and not has_credential_header(headers)


@contextmanager
def bridge():
    """Terminate private TLS and forward the original bytes only on selected discovery routes."""
    state = {"upstreamErrors": 0, "manifests": [], "listProbes": 0, "search": [], "requests": 0, "unexpected": 0}

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_):
            pass

        def forward(self):
            if not allows_discovery_request(self.command, self.path, self.headers):
                state["unexpected"] += 1
                self.send_error(403)
                return
            state["requests"] += 1
            raw = None
            if self.command == "POST":
                size = int(self.headers["Content-Length"])
                assert 0 < size <= 65536
                raw = self.rfile.read(size)
                body = json.loads(raw)
                if body.get("federation") != "none":
                    state["unexpected"] += 1
                    self.send_error(400)
                    return
                state["search"].append(body)
            if self.path == LIST + "?pageSize=1":
                state["listProbes"] += 1
            try:
                status, media, reply = exchange(self.command, self.path, raw)
            except OSError:
                state["upstreamErrors"] += 1
                self.send_error(503)
                return
            if self.path == MANIFEST and status == 200:
                state["manifests"].append(digest(reply))
            self.send_response(status)
            self.send_header("Content-Type", media)
            self.send_header("Content-Length", str(len(reply)))
            self.end_headers()
            self.wfile.write(reply)

        do_GET = do_POST = forward

        def send_error(self, code, *args):
            if code == 501:
                state["unexpected"] += 1
            super().send_error(code, *args)

    with ThreadingHTTPServer(("0.0.0.0", 8443), Handler) as server:
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain("/fixture/server.pem", "/fixture/server-key.pem")
        server.socket = tls.wrap_socket(server.socket, server_side=True)
        thread = Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            yield state
        finally:
            server.shutdown()
            thread.join()
        assert not state["unexpected"], f'{state["unexpected"]} unexpected discovery requests'


def stored(api, source, raw):
    entries = inventory(json.loads(raw)["entries"])
    members = inventory(api.members(source["id"]))
    assert set(members) == set(entries)
    for identity, entry in entries.items():
        row = members[identity]
        assert row["sourceId"] == source["id"] and row["tenantId"] == TENANT and row["freshness"] == "current"
        assert row["versionPresent"] == ("version" in entry) and row.get("version") == entry.get("version")
        assert row["mediaType"] == entry["type"] and row["rawEntryAvailable"]
        supported = entry["type"] == "application/mcp-server-card+json"
        assert row["compatibility"]["support"] == ("supported" if supported else "unknown")
        assert bool(row.get("normalizedEntryDigest")) == supported
        actual, _ = api.http.request("catalog", "/api/v1/observations/" + row["id"] + "/raw")
        check_raw(actual, entry, row, raw)
    page = api.request("/api/v1/catalog/search", {"origins": ["indexed"], "limit": 20}, token="local-reader")[0]
    assert not page["partial"] and not page.get("nextCursor")
    actual = inventory(page["items"])
    assert set(actual) == {k for k, v in entries.items() if v["type"] == "application/mcp-server-card+json"}
    for item in actual.values():
        assert item["type"] == "indexed" and item["indexed"]["presentationSourceId"] == source["id"]
        assert item["version"] == "1.0.0" and item["displayName"] == MCP
    return {k: {"id": v["id"], "digest": v["exactEntryDigest"]} for k, v in members.items()}


def durable_work(api, source):
    jobs = {j["id"] for j in api.pages("/api/v1/jobs") if j["kind"] == "source.sync"}
    snapshots = {s["id"] for s in api.pages(PREFIX + "sources/" + source["id"] + "/snapshots")}
    return jobs, snapshots


def live_item(row, native, source):
    evidence = row["live"]
    assert row["type"] == "live" and row["kind"] == "mcp_server" and row["displayName"] == native["displayName"]
    assert row["identifier"] == native["identifier"] and row["versionPresent"] is True and row["version"] == native["version"]
    # The fixture name query matches only the live display name and description:
    # core-lexical-v1 scores 40 + 20 + 10 independently of the Registry's score.
    assert type(row["score"]) is int and row["score"] == 70, "wrong Catalog name-query score"
    assert evidence["sourceId"] == source["id"] and evidence["source"] == native["source"], "wrong Registry attribution"
    # Python dict equality treats a JSON boolean as equal to 0 or 1.
    assert json.dumps(evidence["entry"], sort_keys=True) == json.dumps(native, sort_keys=True), "changed native Search entry"
    return {"identifier": row["identifier"], "version": row["version"], "catalogScore": row["score"],
            "upstreamScore": evidence["entry"]["score"], "source": evidence["source"], "sourceId": evidence["sourceId"]}


def live(api, state, source, name, native):
    previous = durable_work(api, source)
    before = len(state["search"])
    page = api.request("/api/v1/catalog/search", {"text": name, "origins": ["live"], "limit": 20}, token="local-reader")[0]
    assert len(state["search"]) == before + 1 and state["search"][-1]["query"]["text"] == name, "fresh query did not reach selected peer"
    omitted_skill = name == SKILL and bool(native)
    assert page["partial"] is omitted_skill and page["durableTotal"] == 0 and not page.get("nextCursor"), page.get("diagnostics")
    diagnostics = [(item["sourceId"], item["status"]) for item in page.get("diagnostics", [])]
    assert diagnostics == ([(source["id"], "invalid_response")] if omitted_skill else []), diagnostics
    actual = inventory(page["items"])
    expected = native if name == MCP else {}
    assert set(actual) == set(expected) and page["liveCount"] == len(expected)
    assert durable_work(api, source) == previous, "live read created durable work"
    return {"nativeMatches": len(native), "partial": page["partial"], "diagnostics": page.get("diagnostics", []),
            "items": [live_item(row, native[key], source) for key, row in actual.items()]}


def withdrawn_evidence(api, report):
    rows = api.pages("/api/v1/observations", sourceId=report["source"], freshness="all")
    history = {row["id"]: row for row in rows}
    withdrawn = {}
    for name, previous in ((SKILL, "restored"), (MCP, "recovered")):
        identifier = report["corpus"][name]["identifier"]
        original = report["history"][previous][identifier]
        row = history[original["id"]]
        assert row["sourceId"] == report["source"] and row["identifier"] == identifier
        assert row["freshness"] == "withdrawn" and row["exactEntryDigest"] == original["digest"], "wrong archived observation"
        raw, _ = api.http.request("catalog", "/api/v1/observations/" + row["id"] + "/raw")
        assert digest(raw) == original["digest"], "withdrawn evidence bytes changed"
        withdrawn[name] = {"id": row["id"], "freshness": row["freshness"], "digest": row["exactEntryDigest"]}
    return withdrawn


def failed_refresh(api, state, source, last, expected_state, expected_class):
    before = state["requests"]
    job_id = api.enqueue(source["id"])["id"]

    def failed():
        row = api.request("/api/v1/jobs/" + job_id)[0]
        assert row["state"] != "succeeded", "unexpected successful replacement"
        return row if row["state"] in ("waiting", "failed", "dead_letter") else None

    job = wait_for("failed refresh", failed, timeout=100)
    assert (job["state"], job["failureClass"]) == (expected_state, expected_class), (job["state"], job["failureClass"])
    matches = [e for e in api.pages("/api/v1/audit-events", action="source.sync.failed")
               if e["details"]["facts"]["jobId"] == job_id and e["details"]["facts"]["jobAttempt"] == job["attempt"]]
    event, = matches
    snap = api.request(PREFIX + "snapshots/" + event["targetId"])[0]
    assert sync_attempt(snap, job_id, request=api.request) == job["attempt"]
    assert snap["sourceId"] == source["id"] and snap["policyDigest"] == source["policyDigest"]
    assert state["requests"] > before and snap["state"] == "failed" and not snap["withdrawalAuthority"] and not snap["enumerationComplete"]
    assert api.request(PREFIX + "sources/" + source["id"])[0]["lastCompleteSnapshotId"] == last["id"]
    return {"id": job_id, "state": job["state"], "failureClass": job["failureClass"],
            "failedSnapshotId": snap["id"], "lastCompleteSnapshotId": last["id"]}


def exercise(api, state):
    peer = Registry(Path("/fixture/peer-password").read_text())
    corpus = peer.seed()
    report = {"corpus": corpus, "snapshots": {}, "history": {}, "live": {}}
    raw, native = observe(RESULT, "initial", corpus, "1.0.0")
    error = request("POST", "/api/v1/ard/explore", {}, status=501)
    assert error["errorCode"] == "NOT_IMPLEMENTED", error
    report["explore"] = {"status": 501, "response": error}
    inspection = {"url": ORIGIN + MANIFEST, "networkProfile": "functional-fixture"}
    inspected = api.request(PREFIX + "source-inspections", inspection, expected=201)[0]
    assert inspected["bodyDigest"] == digest(raw) and inspected["sourceType"] == "hybrid" and inspected["entryCount"] == len(json.loads(raw)["entries"])
    assert inspected["profileId"] == CURRENT and inspected["resolvedUrl"] == inspection["url"]
    assert inspected["capabilities"]["enumerable"] and inspected["capabilities"]["search"] and not inspected["capabilities"].get("list")
    assert inspected["registrySearchUrl"] == ORIGIN + "/api/v1/ard/search" and not inspected.get("registryListUrl")
    source = api.request(PREFIX + "sources", {"inspectionId": inspected["id"], "displayName": "Local Agentic Resource Registry",
        "retrievalMode": "hybrid", "updateMode": "manual_review", "networkProfile": "functional-fixture", "scope": {"publishers": [PUBLISHER]},
        "discoveryAssignments": [{"host": "private.catalog.test", "audience": "authenticated"}],
        "live": {"enabled": True, "queryPrivacyAcknowledged": True, "federationMode": "none"}, "reason": "Qualify local metadata"}, expected=201)[0]
    event, = api.pages("/api/v1/audit-events", action="source.connected")
    assert event["targetId"] == source["id"] and event["details"]["facts"]["inspectionId"] == inspected["id"]
    assert source["policyDigest"] == inspected["policyDigest"]
    report["source"] = source["id"]
    report["connection"] = {"inspectionId": inspected["id"], "profileId": inspected["profileId"], "capabilities": inspected["capabilities"],
                            "policyDigest": source["policyDigest"], "sourceRevision": source["revision"],
                            "tenantId": source["tenantId"], "retrievalMode": source["retrievalMode"]}
    assert [row["id"] for row in api.pages(PREFIX + "sources")] == [source["id"]]

    def refresh(phase, raw, native):
        job_id = api.enqueue(source["id"])["id"]
        snap = complete(api, job_id, source, raw, len(json.loads(raw)["entries"]))
        report["history"][phase] = stored(api, source, raw)
        report["snapshots"][phase] = {"id": snap["id"], "job": job_id, "digest": digest(raw)}
        report["live"][phase] = {name: live(api, state, source, name, native[name]) for name in (SKILL, MCP)}
        print("Qualified", phase, flush=True)
        return snap

    first = refresh("initial", raw, native)
    path = PREFIX + "sources/" + source["id"]
    api.request("/api/v1/observations/" + report["history"]["initial"][REGISTRY]["id"] + "/raw", token="other-tenant-admin", expected=404)
    count = len(state["search"])
    denied, _ = api.request("/api/v1/catalog/search", {"origins": ["live"], "text": MCP}, token="local-template-reader", expected=403)
    assert denied["code"] == "FORBIDDEN" and len(state["search"]) == count
    report["refusedLiveSearch"] = {"status": 403, "code": denied["code"], "peerSearchRequests": len(state["search"]) - count}
    pending = peer.successor(corpus[SKILL])
    raw, native = observe(RESULT, "pending", corpus, "1.0.0")
    unchanged = refresh("pending", raw, native)
    assert first["id"] != unchanged["id"] and report["history"]["initial"] == report["history"]["pending"]
    peer.approve(pending)
    raw, native = observe(RESULT, "approved", corpus, "1.1.0")
    refresh("approved", raw, native)
    key = corpus[SKILL]["identifier"]
    assert report["history"]["approved"][key]["id"] != report["history"]["pending"][key]["id"]
    request("PATCH", "/api/v1/admin/recommended", {"entity_type": "skill", "entity_id": corpus[SKILL]["id"], "recommended": True}, token=peer.curator)
    raw, native = observe(RESULT, "recommended", corpus, "1.1.0", True)
    stable = refresh("recommended", raw, native)
    assert report["history"]["recommended"][key]["digest"] != report["history"]["approved"][key]["digest"]
    peer.setting("deployment.public_registry_enabled", "false")
    disabled_raw, empty = observe(RESULT, "disabled", corpus, None, enabled=False)
    report["live"]["disabled"] = {name: live(api, state, source, name, empty[name]) for name in (SKILL, MCP)}
    probes = state["listProbes"]
    changed = api.request(PREFIX + "source-inspections", inspection, expected=201)[0]
    assert changed["sourceType"] == "ard_registry" and changed["entryCount"] == 1
    assert changed["profileId"] == CURRENT and all(changed["capabilities"][k] for k in ("search", "list", "enumerable"))
    assert state["listProbes"] == probes + 1 and changed["registryListUrl"] == ORIGIN + LIST
    _, headers = api.request(path)
    refused, _ = api.http.document("catalog", path, {"inspectionId": changed["id"], "reason": "Check incompatible interface"},
                      method="PATCH", etag=headers["etag"], expected=412)
    assert refused["code"] == "PRECONDITION_FAILED" and changed["bodyDigest"] == digest(disabled_raw)
    before = len(state["manifests"])
    fault = failed_refresh(api, state, source, stable, "failed", "invalid_input")
    assert state["manifests"][before:] == [digest(disabled_raw)], "refresh must read the Registry-only publication"
    report["registryDisabled"] = {"inspectionId": changed["id"], "sourceType": changed["sourceType"],
                                  "capabilities": changed["capabilities"], "patchStatus": 412, "patchCode": refused["code"], "refresh": fault}
    assert stored(api, source, raw) == report["history"]["recommended"]
    peer.setting("deployment.public_registry_enabled", "true")
    raw, native = observe(RESULT, "restored", corpus, "1.1.0", True)
    refresh("restored", raw, native)
    peer.archive(corpus[SKILL])
    raw, native = observe(RESULT, "archived", corpus, None)
    last = refresh("archived", raw, native)
    assert last["withdrawn"] == 1 and key not in report["history"]["archived"]
    report["searchRequests"] = len(state["search"])
    (RESULT / "checkpoint.json").write_text(json.dumps(report))


def main(phase):
    api = Catalog("http://127.0.0.1:8080", "https://127.0.0.1:8443", "/fixture/trust/functional-ca.pem")
    with bridge() as state:
        if phase == "exercise":
            exercise(api, state)
        elif phase == "withdraw-mcp":
            report = json.loads((RESULT / "checkpoint.json").read_text())
            source = api.request(PREFIX + "sources/" + report["source"])[0]
            peer = Registry(Path("/fixture/peer-password").read_text())
            path = "/api/v1/mcps/" + report["corpus"][MCP]["id"]
            request("PATCH", path + "/archive", token=peer.author)
            assert request("GET", path, token=peer.author)["status"] == "archived"
            raw, native = observe(RESULT, phase, report["corpus"], None, mcp_public=False)
            job = api.enqueue(source["id"])
            snapshot = complete(api, job["id"], source, raw, len(json.loads(raw)["entries"]))
            assert snapshot["withdrawn"] == 1
            report["history"][phase] = stored(api, source, raw)
            report["snapshots"][phase] = {"id": snapshot["id"], "job": job["id"], "digest": digest(raw)}
            report["live"][phase] = {MCP: live(api, state, source, MCP, native[MCP])}
            report["withdrawals"] = withdrawn_evidence(api, report)
            report["searchRequests"] += len(state["search"])
            (RESULT / "checkpoint.json").write_text(json.dumps(report))
        elif phase in ("outage", "recovery", "restart"):
            report = json.loads((RESULT / "checkpoint.json").read_text())
            source = api.request(PREFIX + "sources/" + report["source"])[0]
            raw = (RESULT / "archived-manifest.json").read_bytes()
            last = report["snapshots"]["archived"]
            if phase == "outage":
                try:
                    exchange("GET", MANIFEST)
                except OSError:
                    pass
                else:
                    raise AssertionError("stopped peer still served a manifest")
                report["outage"] = failed_refresh(api, state, source, last, "waiting", "transient_upstream")
                assert state["upstreamErrors"] >= 1
                assert stored(api, source, raw) == report["history"]["archived"]
                (RESULT / "checkpoint.json").write_text(json.dumps(report))
                return
            if phase == "recovery":
                recovered, native = observe(RESULT, "recovered", report["corpus"], None)
                assert recovered == raw, "native data changed across peer restart"
                job = api.request("/api/v1/jobs/" + report["outage"]["id"])[0]
                job_id = job["id"] if job["state"] in ("running", "waiting", "queued", "succeeded") else api.enqueue(source["id"])["id"]
                snap = complete(api, job_id, source, raw, len(json.loads(raw)["entries"]))
                assert snap["id"] != last["id"]
                report["snapshots"]["recovered"] = {"id": snap["id"], "job": job_id, "digest": digest(raw)}
                report["history"]["recovered"] = stored(api, source, raw)
                assert report["history"]["recovered"] == report["history"]["archived"]
                report["live"]["recovered"] = {MCP: live(api, state, source, MCP, native[MCP])}
                report["searchRequests"] += len(state["search"])
                (RESULT / "checkpoint.json").write_text(json.dumps(report))
                return
            assert source["lastCompleteSnapshotId"] == report["snapshots"]["recovered"]["id"]
            assert stored(api, source, raw) == report["history"]["recovered"]
            for phase, snapshot in report["snapshots"].items():
                snap = api.request(PREFIX + "snapshots/" + snapshot["id"])[0]
                assert snap["sourceId"] == source["id"] and snap["bodyDigest"] == snapshot["digest"]
                sync_attempt(snap, snapshot["job"], request=api.request)
            assert state["requests"] == 0
        else:
            raise ValueError("unknown phase")


if __name__ == "__main__":
    main(sys.argv[1])
