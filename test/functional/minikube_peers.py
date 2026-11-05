"""Owned three-kind federation between independent Helm Catalogs, not a remote seeder."""
import json
import os
from pathlib import Path
import sys
import time
import urllib.parse

from federation import Client, observations, snapshot_items, sync
from http_client import fixture_tls_context
from journeys import header
from peers import HostClient, Peers, mutate, registry_members
from peer_pagination import request_page
from runner import Failure, http_failure_details, validate_search_page_failure

KINDS = ("agent", "mcp_server", "skill")


def validate_members(rows, expected, source):
    """Reject duplicates, incomplete membership and wrong source attribution."""
    assert len(rows) == len(expected), "peer membership count differs"
    assert {row["identifier"] for row in rows} == set(expected), "peer membership differs"
    assert len({row["id"] for row in rows}) == len(rows), "peer observation identity repeats"
    assert all(row["sourceId"] == source and row["freshness"] == "current" for row in rows), "peer provenance differs"


class ClusterPeer(Peers):
    def __init__(self, config, side):
        self.a = self.b = Client(config["base"], config["tokens"]["admin"])
        self.author = Client(config["base"], config["tokens"]["contributor"])
        self.reviewer = Client(config["base"], config["tokens"]["reviewer"])
        self.publisher_client = Client(config["base"], config["tokens"]["publisher"])
        self.reader = Client(config["base"], config["tokens"]["reader"])
        self.other = Client(config["base"], config["tokens"]["other"])
        self.host = config["host"]
        self.authenticated_host = config["authenticatedHost"]
        self.namespace_slug = "peers-" + side
        self.end = time.monotonic() + 240

    def seed(self, side):
        self.namespace = mutate(self.a, "/api/v1/namespaces", {"slug": self.namespace_slug, "displayName": "Cluster peer " + side}, 201)[0]["id"]
        publisher, headers = mutate(self.a, "/api/v1/publishers", {"displayName": "Cluster publisher " + side, "domains": ["catalog.example.com"]}, 201)
        self.publisher = publisher["id"]
        mutate(self.a, "/api/v1/publishers/" + self.publisher + "/publication-domains/catalog.example.com:approve", {"reason": "Owned cluster publisher"}, etag=header(headers, "ETag"))
        items = [self.create_item("peer-" + side + "-" + kind, kind) for kind in KINDS]
        self.create_item("private-control", visibility="tenant")
        self.create_item("draft-control", draft=True)
        self.snapshot(self.publisher_client, self.host)
        return items

    def connect(self, host, credential=None):
        assert credential is None
        inspection = mutate(self.b, "/api/v1/federation/source-inspections", {"url": "https://" + host + ":8443/.well-known/ard.json", "networkProfile": "functional-fixture"}, 201)[0]
        assert inspection["sourceType"] == "hybrid", "peer does not advertise actual Catalog discovery"
        return mutate(self.b, "/api/v1/federation/sources", {"inspectionId": inspection["id"], "displayName": "Independent Helm peer", "retrievalMode": "indexed", "scope": {"kinds": list(KINDS)}, "updateMode": "manual_review", "networkProfile": "functional-fixture", "reason": "Owned Minikube peer"}, 201)[0]


def resource_ids(client):
    return {row["id"] for row in client.request("GET", "/api/v1/resources?limit=100")[0]["items"]}


def validate_search_sources(rows, source):
    """Require the selected source in every indexed Search result."""
    assert rows and all(row["indexed"]["sourceIds"] == [source] for row in rows), "search lost source attribution"


def control_page(client, expected, source=None):
    cursor = None
    seen = []
    previous_headers = None
    for page_number in range(1, 21):
        body = {"tags": ["peers"], "origins": ["indexed"] if source else ["local"], "limit": 1}
        if cursor:
            body["cursor"] = cursor
        page, headers = request_page(client, "/api/v1/catalog/search", body, page_number, previous_headers=previous_headers)
        assert not page["partial"] and page["durableTotal"] == len(expected), "peer search completeness differs"
        seen.extend(row["identifier"] for row in page["items"])
        if source:
            validate_search_sources(page["items"], source)
        cursor = page.get("nextCursor")
        if not cursor:
            break
        previous_headers = headers
    assert not cursor and len(seen) == len(expected) and set(seen) == set(expected), "peer pagination lost or duplicated a result"


def run(config, phase):
    os.environ["CATALOG_FUNCTIONAL_CONTRACT_DRIVER"] = config["validator"]
    a, b = ClusterPeer(config["a"], "a"), ClusterPeer(config["b"], "b")
    state_path = Path(config["state"])
    tls = fixture_tls_context(config["ca"])
    public_a = HostClient(config["frontend"], host=a.host + ":8443", ssl_context=tls)
    control = Client(config["frontend"], ssl_context=tls)
    checks = []
    if phase == "lifecycle":
        items_a, items_b = a.seed("a"), b.seed("b")
        expected_a = {item["resource"]["canonicalIdentifier"] for item in items_a}
        expected_b = {item["resource"]["canonicalIdentifier"] for item in items_b}
        before_a, before_b = resource_ids(a.a), resource_ids(b.a)
        assert before_a.isdisjoint(before_b), "independent Catalogs reuse resource IDs"
        Client(config["a"]["base"], config["b"]["tokens"]["admin"]).request("GET", "/api/v1/me", expected=(401,))
        source = b.connect(a.host)
        rows = b.synchronize(source)
        validate_members(rows, expected_a, source["id"])
        b.check_index(rows, registry_members(public_a), expected_a, source)
        b.other.request("GET", "/api/v1/federation/sources/" + source["id"], expected=(404,))
        b.reader.request("GET", "/api/v1/federation/sources/" + source["id"], expected=(403,))
        for row in rows:
            b.other.request("GET", "/api/v1/observations/" + row["id"] + "/raw", expected=(404,))
        b.a.request("GET", "/api/v1/resources/" + items_a[0]["resource"]["id"], expected=(404,))
        assert resource_ids(b.a) == before_b, "indexing silently adopts remote Resources"
        control_page(b.a, expected_a, source["id"])
        control_page(b.a, expected_b)
        repeated = b.synchronize(source)
        assert {r["id"] for r in repeated} == {r["id"] for r in rows}, "unchanged sync duplicates observations"
        checks.extend(("three-kind-exact-bytes-and-provenance", "private-draft-tenant-and-issuer-denials", "no-implicit-adoption", "one-item-pagination-and-unchanged-sync"))
        source = b.change_source(source, {"retrievalMode": "hybrid", "live": {"enabled": True, "queryPrivacyAcknowledged": True, "federationMode": "none"},
            "discoveryAssignments": [{"host": b.authenticated_host, "audience": "authenticated"}]})
        live = b.a.request("POST", "/api/v1/catalog/search", {"text": "peer", "origins": ["live"], "tags": ["peers"], "limit": 20})[0]
        assert not live["partial"] and {r["identifier"] for r in live["items"]} == expected_a, "live discovery differs"
        last = b.a.request("GET", "/api/v1/federation/sources/" + source["id"])[0]
        previous = {r["id"] for r in rows}
        prior_snapshots = {r["id"] for r in snapshot_items(b.a, source["id"])}
        try:
            control.request("POST", "/peer-fault", {"enabled": True, "offline": True})
            partial = b.a.request("POST", "/api/v1/catalog/search", {"text": "peer", "tags": ["peers"], "origins": ["local", "indexed", "live"], "limit": 100})[0]
            assert partial["partial"] and {r["identifier"] for r in partial["items"]} == expected_a | expected_b, "outage loses complete local/indexed results"
            job = sync(b.a, source["id"], source["revision"], "cluster-outage")["job"]
            failed = b.wait(lambda: next((s for s in snapshot_items(b.a, source["id"]) if s["state"] == "failed" and s["id"] not in prior_snapshots), None))
            query = urllib.parse.urlencode({"action": "source.sync.failed", "targetType": "source_snapshot", "targetId": failed["id"], "limit": 10})
            audits = b.a.request("GET", "/api/v1/audit-events?" + query)[0]["items"]
            assert any(row["details"]["facts"].get("jobId") == job["id"] for row in audits), "failed snapshot lacks exact Job attribution"
            retained = b.a.request("GET", "/api/v1/federation/sources/" + source["id"])[0]
            assert retained["lastCompleteSnapshotId"] == last["lastCompleteSnapshotId"], "failure replaces complete snapshot"
            assert {r["id"] for r in observations(b.a, source["id"])} == previous, "outage withdraws indexed membership"
        finally:
            control.request("POST", "/peer-fault", {"enabled": False})
        b.finish_job(job)
        recovered = b.a.request("GET", "/api/v1/federation/sources/" + source["id"])[0]
        assert recovered["lastCompleteSnapshotId"] != last["lastCompleteSnapshotId"] and not recovered.get("lastError"), "same Job does not recover"
        checks.extend(("live-query-and-preserved-partial-results", "failed-sync-preserves-complete-snapshot", "same-job-recovery"))
        old_digest = next(row["identityDigest"] for row in rows if row["identifier"] == items_a[0]["resource"]["canonicalIdentifier"])
        items_a[0]["name"] = "peer-a-agent-updated"
        a.publish_item(items_a[0])
        a.snapshot(a.publisher_client, a.host)
        updated = b.synchronize(source)
        assert next(row["identityDigest"] for row in updated if row["identifier"] == items_a[0]["resource"]["canonicalIdentifier"]) != old_digest, "update does not converge"
        path = "/api/v1/listing-releases/" + items_a[1]["release"]["id"]
        _, headers = a.publisher_client.request("GET", path)
        mutate(a.publisher_client, path + ":retire", {"reason": "Owned peer withdrawal"}, etag=header(headers, "ETag"))
        a.snapshot(a.publisher_client, a.host)
        expected_a.remove(items_a[1]["resource"]["canonicalIdentifier"])
        validate_members(b.synchronize(source), expected_a, source["id"])
        reverse = a.connect(b.host)
        validate_members(a.synchronize(reverse), expected_b, reverse["id"])
        a.snapshot(a.publisher_client, a.host)
        assert {r["identifier"] for r in registry_members(public_a) if r["type"] != "application/ai-registry+json"} == expected_a, "reciprocal publication exports indexed entries"
        assert resource_ids(a.a) == before_a and resource_ids(b.a) == before_b, "federation changes native resource ownership"
        state = {"source": source, "reverse": reverse, "expectedA": sorted(expected_a), "expectedB": sorted(expected_b), "resourcesA": sorted(before_a), "resourcesB": sorted(before_b)}
        state_path.write_text(json.dumps(state))
        state_path.chmod(0o600)
        checks.extend(("update-and-authoritative-withdrawal", "reciprocal-federation-without-reexport"))
    elif phase == "replacement":
        state = json.loads(state_path.read_text())
        validate_members(b.synchronize(state["source"]), state["expectedA"], state["source"]["id"])
        validate_members(a.synchronize(state["reverse"]), state["expectedB"], state["reverse"]["id"])
        assert resource_ids(a.a) == set(state["resourcesA"]) and resource_ids(b.a) == set(state["resourcesB"]), "process replacement loses retained data"
        control_page(b.a, state["expectedA"], state["source"]["id"])
        checks.append("both-catalogs-retain-native-and-synchronized-state-after-replacement")
    else:
        raise ValueError("unknown owned peer phase")
    return {"phase": phase, "checks": checks, "kinds": list(KINDS), "catalogs": 2}


def main():
    if sys.flags.optimize:
        raise ValueError("assertions must remain enabled")
    config = json.loads(sys.stdin.read(65537))
    try:
        print(json.dumps(run(config, sys.argv[1])))
    except Exception as error:
        # Do not print response bodies, credentials, private metadata or dependency exceptions.
        import traceback
        frames = traceback.extract_tb(sys.exc_info()[2])
        locations = [f"{Path(f.filename).name}:{f.lineno}" for f in frames if Path(f.filename).name in ("minikube_peers.py", "peers.py", "federation.py")]
        print("Owned Catalog peer phase failed at " + ",".join(locations), file=sys.stderr)
        diagnostic = http_failure_details(error)
        if diagnostic is not None:
            print(f"functional federation request returned HTTP {diagnostic['status']} ({diagnostic['category']})", file=sys.stderr)
        if hasattr(error, "search_page_failure"):
            try:
                value = validate_search_page_failure(error.search_page_failure)
                print("Search page failure: " + json.dumps(value, sort_keys=True), file=sys.stderr)
            except Failure:
                pass
        raise SystemExit(1) from None


if __name__ == "__main__":
    main()
