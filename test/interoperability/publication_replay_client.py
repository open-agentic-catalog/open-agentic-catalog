"""Real Catalog acceptance checks inside the disposable API network namespace."""

from base64 import b64encode
from collections import Counter
from contextlib import contextmanager
from hashlib import sha256
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import ssl
import sys
from threading import Event, Thread
from urllib.parse import urlencode

from agentfinder_contract import TENANT, check_raw, digest
from agentfinder_runner import Catalog, ORIGIN, PREFIX
from peer_catalog import sync_attempt, wait_for
from publication_replay import EXERCISE_PHASES, FEEDS, KINDS, PATH, PROFILE, REGISTRY, check_read_audits, inventory, read_capture, replay_route

FIXTURE = Path("/fixture")
RESULT = Path("/result/checkpoint.json")


@contextmanager
def publisher(raw, media_type):
    state = {"mode": "healthy", "body": raw, "requests": Counter(), "unexpected": [], "refusedFallback": Counter()}
    closing = Event()

    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *args):
            pass

        def do_GET(self):
            mode = state["mode"]
            route = replay_route(self.path, self.headers, mode)
            if route == "fallback":
                state["refusedFallback"][mode] += 1
                self.send_error(404)
                return
            if route == "unexpected":
                state["unexpected"].append((mode, self.command))
                self.send_error(404)
                return
            state["requests"][mode] += 1
            if mode == "timeout":
                closing.wait(8)
                return
            if mode == "outage":
                self.send_error(503)
                return
            body = state["body"]
            if mode == "malformed":
                body = b'{"entries":['
            elif mode == "oversize":
                body = body.ljust((2 << 20) + 1, b" ")  # Still the same valid JSON document.
            self.send_response(200)
            self.send_header("Content-Type", media_type)
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            try:
                self.wfile.write(body)
            except OSError:
                if mode != "oversize":
                    raise

        def send_error(self, code, *args):
            if code == 501:  # BaseHTTPRequestHandler refused a method besides GET.
                state["unexpected"].append((state["mode"], self.command))
            super().send_error(code, *args)

    with ThreadingHTTPServer(("0.0.0.0", 8443), Handler) as server:
        tls = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
        tls.load_cert_chain(FIXTURE / "server.pem", FIXTURE / "server-key.pem")
        server.socket = tls.wrap_socket(server.socket, server_side=True)
        thread = Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            yield state
        finally:
            closing.set()
            server.shutdown()
            thread.join()
        assert not state["unexpected"], ("unexpected fixture request or credentials", state["unexpected"])


def inspect(api, state, feed):
    before = state["requests"]["healthy"]
    document, _ = api.request(PREFIX + "source-inspections", {"url": ORIGIN + PATH, "networkProfile": "functional-fixture"}, expected=201)
    assert state["requests"]["healthy"] == before + 1, "inspection must fetch just the publication"
    assert document["sourceType"] == "hybrid" and document["profileId"] == PROFILE and document["mediaType"] == "application/json"
    assert document["resolvedUrl"] == ORIGIN + PATH and document["bodyDigest"] == digest(state["body"])
    entries = inventory(state["body"], feed)
    registry = next(e for e in entries if e["type"] == REGISTRY)
    assert document["entryCount"] == len(entries) and document["capabilities"]["enumerable"]
    assert document["capabilities"]["search"] and not document.get("registryListUrl")
    assert document["registrySearchUrl"] == registry["url"].rstrip("/") + "/search"
    assert api.request(PREFIX + "source-inspections/" + document["id"])[0] == document
    return document


def complete(api, job_id, source, raw, count):
    def finished():
        job = api.request("/api/v1/jobs/" + job_id)[0]
        assert job["state"] not in ("failed", "dead_letter", "cancelled"), (job_id, job["state"])
        return job if job["state"] == "succeeded" else None
    job = wait_for("publication synchronization", finished, timeout=135)
    snap = api.request(PREFIX + "snapshots/" + job["result"]["snapshotId"])[0]
    assert snap["state"] == "complete" and snap["enumerationComplete"] and snap["withdrawalAuthority"]
    assert snap["sourceId"] == source["id"] and snap["sourceRevision"] == source["revision"]
    assert snap["memberCount"] == count and snap["bodyDigest"] == digest(raw)
    assert sync_attempt(snap, job_id, request=api.request) == job["attempt"]
    return snap


def search(api, source_id, entries, feed):
    expected = {e["identifier"]: KINDS[e["type"]] for e in entries if e["type"] in KINDS}
    queries = [({}, set(expected)), ({"text": feed["selected"]}, {feed["selected"]})]
    if feed["expansionFailures"]:
        queries.append(({"text": "urn:air:api.live.nevermined.app:service:ai-agent-oracle"}, set()))
    for query, matches in queries:
        page, _ = api.request("/api/v1/catalog/search", {"origins": ["indexed"], "limit": 100, **query}, token="local-reader")
        assert page["partial"] is False and not page.get("nextCursor")
        items = page["items"]
        keys = [i["identifier"] for i in items]
        assert len(set(keys)) == len(items) and set(keys) == matches, keys
        for item in items:
            assert item["type"] == "indexed" and item["indexed"]["presentationSourceId"] == source_id
            assert item["kind"] == expected[item["identifier"]] and item["versionPresent"] is False and not item.get("version")


def members(api, source_id, raw, entries, feed, *, evidence=False):
    rows = api.members(source_id)
    downloads = {}
    by_id = {row["identifier"]: row for row in rows}
    assert len(by_id) == len(rows) == len(entries) and set(by_id) == {e["identifier"] for e in entries}
    for e in entries:
        row = by_id[e["identifier"]]
        assert row["tenantId"] == TENANT and row["sourceId"] == source_id and row["freshness"] == "current"
        assert row["mediaType"] == e["type"] and row["versionPresent"] is False and not row.get("version")
        assert row["rawEntryAvailable"] and row.get("kind") == KINDS.get(e["type"])
        compatibility = row["compatibility"]
        assert compatibility["profileId"] == PROFILE
        assert compatibility["support"] == ("supported" if e["type"] in KINDS else "unknown")
        invalid = e["identifier"].rsplit(":", 1)[1] in feed["expansionFailures"]
        codes = {f["code"] for f in compatibility.get("findings", [])}
        assert "ARD_ENTRY_SCHEMA_INVALID" not in codes and ("ARD_JSONLD_EXPANSION_FAILED" in codes) == invalid
        assert compatibility["structural"] == ("invalid" if invalid else "valid")
        assert bool(row.get("normalizedEntryDigest")) == (e["type"] in KINDS)
        if evidence:
            exact, headers = api.http.request("catalog", "/api/v1/observations/" + row["id"] + "/raw")
            check_raw(exact, e, row, raw)
            assert headers["content-digest"] == "sha-256=:" + b64encode(sha256(exact).digest()).decode() + ":"
            request_id = headers["x-request-id"]
            assert request_id and request_id not in downloads, "missing or repeated evidence request ID"
            downloads[request_id] = row["id"], row["exactEntryDigest"], len(exact)
            if invalid or e["identifier"].endswith(":ai-agent-oracle"):
                assert json.loads(exact)["nvm:catalog"] == e["nvm:catalog"] and e["nvm:catalog"]
    if evidence:
        events = api.pages("/api/v1/audit-events", action="evidence.raw_observation.read")
        admin = api.request("/api/v1/me")[0]
        check_read_audits(downloads, events, admin["membershipId"], TENANT)
    search(api, source_id, entries, feed)
    return sorted((r["id"], r["identifier"], r["exactEntryDigest"], r["firstSeenAt"]) for r in rows)


def private_access(api, source_id, observation):
    reader = api.request("/api/v1/me", token="local-reader")[0]
    role = api.request("/api/v1/access/roles", {"name": "Publication metadata reader", "permissions": ["source.read"],
                       "reason": "Read observation summaries"}, expected=201)[0]
    api.request("/api/v1/access/assignments", {"roleDefinitionId": role["id"], "membershipId": reader["membershipId"],
                "reason": "Read observation summaries"}, expected=201)
    page = api.request("/api/v1/observations?" + urlencode({"sourceId": source_id, "freshness": "current", "limit": 100}), token="local-reader")[0]
    assert any(r["id"] == observation for r in page["items"])
    path = "/api/v1/observations/" + observation + "/raw"
    api.request(path, token="local-reader", expected=403)
    api.request(path, token="other-tenant-admin", expected=404)


def exercise(api, state, feed, raw, entries):
    phases = dict.fromkeys(EXERCISE_PHASES, "unrun")

    def passed(phase):
        phases[phase] = "passed"
        (RESULT.parent / "exercise.json").write_text(json.dumps(phases))

    if len(feed["captures"]) > 1:
        state["body"] = read_capture(FIXTURE / "capture-1.json", feed["captures"][1])
        paired = inspect(api, state, feed)
        state["body"] = raw
    else:
        paired = None
    observed = inspect(api, state, feed)
    state["mode"] = "malformed"
    refused, _ = api.request(PREFIX + "source-inspections", {"url": ORIGIN + PATH, "networkProfile": "functional-fixture"}, expected=400)
    assert refused["code"] == "INVALID_ARGUMENT" and state["requests"]["malformed"] == 1
    state["mode"] = "healthy"
    source = api.request(PREFIX + "sources", {"inspectionId": observed["id"], "displayName": feed["publisher"],
        "retrievalMode": "indexed", "updateMode": "manual_review", "networkProfile": "functional-fixture",
        "scope": {"publishers": [feed["publisher"]]}, "reason": "Replay local ARD publication"}, expected=201)[0]
    source_id = source["id"]
    assert source["scope"]["publishers"] == [feed["publisher"]]
    passed("inspection")

    def finish(job_id):
        return complete(api, job_id, source, raw, len(entries))

    first = finish(api.enqueue(source_id)["id"])
    stable = members(api, source_id, raw, entries, feed, evidence=True)
    passed("retention")
    private_access(api, source_id, api.members(source_id)[0]["id"])
    passed("permissions")
    assert first["added"] == len(entries)
    latest = finish(api.enqueue(source_id)["id"])
    assert latest["id"] != first["id"]
    assert members(api, source_id, raw, entries, feed) == stable
    passed("repeat")
    faults = {}
    for mode in ("outage", "timeout", "malformed", "oversize"):
        assert all(latest[key] == 0 for key in ("added", "updated", "withdrawn"))
        state["mode"] = mode
        prior_requests = state["requests"][mode]
        job_id = api.enqueue(source_id)["id"]

        def failed():
            row = api.request("/api/v1/jobs/" + job_id)[0]
            assert row["state"] != "succeeded", mode + " accepted"
            return row if row["state"] in ("waiting", "failed", "dead_letter") else None

        failure = wait_for(mode, failed, timeout=100)
        failed_ids = {e["targetId"] for e in api.pages("/api/v1/audit-events", action="source.sync.failed")
                      if e["details"]["facts"]["jobId"] == job_id and e["details"]["facts"]["jobAttempt"] == failure["attempt"]}
        assert len(failed_ids) == 1 and state["requests"][mode] > prior_requests, "failed refresh did not reach the fault fixture"
        failed_snapshot = api.request(PREFIX + "snapshots/" + next(iter(failed_ids)))[0]
        assert failed_snapshot["state"] == "failed" and sync_attempt(failed_snapshot, job_id, request=api.request) == failure["attempt"]
        assert not failed_snapshot["enumerationComplete"] and not failed_snapshot["withdrawalAuthority"]
        assert api.request(PREFIX + "sources/" + source_id)[0]["lastCompleteSnapshotId"] == latest["id"]
        assert members(api, source_id, raw, entries, feed) == stable
        state["mode"] = "healthy"
        latest = finish(job_id if failure["state"] == "waiting" else api.enqueue(source_id)["id"])
        faults[mode] = {"jobState": failure["state"], "failedSnapshot": failed_snapshot["id"], "recoverySnapshot": latest["id"]}
        assert all(latest[key] == 0 for key in ("added", "updated", "withdrawn"))
        passed(mode)
    assert state["requests"]["healthy"] == len(feed["captures"]) + 6, "unexpected healthy manifest request"
    assert api.request(PREFIX + "snapshots/" + first["id"])[0] == first
    assert members(api, source_id, raw, entries, feed) == stable
    inspection = {k: observed.get(k) for k in ("sourceType", "profileId", "capabilities", "registrySearchUrl", "registryListUrl", "mediaType", "bodyDigest")}
    inspection["warningCodes"] = sorted({w["code"] for w in observed.get("warnings", [])})
    return {"inspection": inspection, "malformedInspectionCode": refused["code"],
            "pairedTransportDigest": (paired or observed)["bodyDigest"], "sourceId": source_id, "firstSnapshot": first,
            "lastSnapshotId": latest["id"], "inventoryDigest": digest(json.dumps(stable).encode()), "rawVerified": len(stable),
            "indexed": sum(e["type"] in KINDS for e in entries), "expansionFailures": feed["expansionFailures"],
            "requests": dict(state["requests"]), "refusedFallback": dict(state["refusedFallback"]), "faults": faults}


def main(provider, phase):
    feed = FEEDS[provider]
    raw = read_capture(FIXTURE / "capture-0.json", feed["captures"][0])
    entries = inventory(raw, feed)
    api = Catalog("http://127.0.0.1:8080", "https://127.0.0.1:8443", FIXTURE / "trust/functional-ca.pem")
    with publisher(raw, feed["header"]) as state:
        if phase == "exercise":
            result = exercise(api, state, feed, raw, entries)
            RESULT.write_text(json.dumps(result))
        elif phase == "restart":
            saved = json.loads(RESULT.read_text())
            source_id = saved["sourceId"]
            assert api.request(PREFIX + "sources/" + source_id)[0]["lastCompleteSnapshotId"] == saved["lastSnapshotId"]
            assert api.request(PREFIX + "snapshots/" + saved["firstSnapshot"]["id"])[0] == saved["firstSnapshot"]
            stable = members(api, source_id, raw, entries, feed, evidence=True)
            assert saved["inventoryDigest"] == digest(json.dumps(stable).encode())
            assert not state["requests"], "persistent readback contacted publisher"
        else:
            raise ValueError("unknown phase")


if __name__ == "__main__":
    if not __debug__:
        raise RuntimeError("qualification requires Python assertions")
    main(*sys.argv[1:])
