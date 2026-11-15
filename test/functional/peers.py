#!/usr/bin/env python3
"""One owned, bounded two-Catalog federation journey; never a remote seeder."""

import copy
import hashlib
import json
import federation as federation_module
import peer_pagination
import peer_drafts
import os
import ipaddress
import shutil
from pathlib import Path
import re
import shlex
import sys
import time
import urllib.parse

from federation import Client, key, observations, snapshot_items, sync, wait_job
from http_client import fixture_tls_context, ClientFailure
from journeys import header
from runner import Failure, command, http_failure_details, read_document, record_scenario_failure, write_document


TENANT = "00000000-0000-7000-8000-000000000001"
KINDS = ["agent", "mcp_server", "skill", "plugin"]
PUBLIC_B = "peer-b.test"
PUBLIC_A = "peer-a.test"
PRIVATE_A = "private-a.test"
PRIVATE_B = "private.catalog.test"
SUBJECT_IRI = "http://purl.org/dc/terms/subject"
PHASES = ("startup", "seed", "index", "unchanged", "scope", "update", "removal",
          "api-recovery", "unpublish-recovery", "live", "live-paging", "client-pagination", "credentials", "reciprocal", "adoption", "automatic", "pause-resume", "source-removal", "draft-transfer", "complete")


def valid_progress(value):
    return (isinstance(value, dict) and set(value) == {"completed", "catalogs", "publishedKinds"}
            and isinstance(value["completed"], list) and value["completed"] == list(PHASES[:len(value["completed"])])
            and type(value["catalogs"]) is int and value["catalogs"] == 2
            and value["publishedKinds"] == (KINDS if "seed" in value["completed"] else []))


def valid_evidence(value):
    return valid_progress(value) and value["completed"] == list(PHASES)


def valid_inventories(value):
    if not isinstance(value, dict) or set(value) != {"a", "b"}:
        return False
    for side in ("a", "b"):
        rows = value[side]
        if not isinstance(rows, list) or len(rows) != len(KINDS):
            return False
        names = ("agent", "mcp", "skill", "plugin") if side == "a" else KINDS
        expected = [{"identifier": "urn:air:catalog.example.com:peers-" + side + ":peer-" + side + "-" + name, "kind": kind}
                    for name, kind in zip(names, KINDS, strict=True)]
        if rows != expected:
            return False
    return True



def valid_identity(report):
    """Require owned image/archive evidence after the final unchanged-source check."""
    images = report.get("images")
    services = {"api", "worker", "postgres", "federation-fixture", "api-a", "worker-a", "postgres-a", "peer-archives"}
    if (report.get("peerIdentityVerified") is not True or not isinstance(images, dict) or set(images) != services
            or any(not isinstance(row, dict) or set(row) != {"configurationDigest"}
                   or not isinstance(row["configurationDigest"], str)
                   or not re.fullmatch(r"sha256:[0-9a-f]{64}", row["configurationDigest"]) for row in images.values())):
        return False
    if any(images[service] != images["api"] for service in ("worker", "api-a", "worker-a", "peer-archives")) or images["postgres"] != images["postgres-a"]:
        return False
    archives = report.get("peerArchiveInputs")
    return (isinstance(archives, dict) and set(archives) == {"demo-archives", "archives/allowlist.json"}
            and all(isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value) for value in archives.values()))


def valid_contacts(value):
    return (isinstance(value, dict) and 0 < len(value) <= 180 and all(
        isinstance(name, str) and re.fullmatch(r"(?:peer-a|private-a|peer-b)\.test:8443 (?:GET /(?:\.well-known/(?:ard|ai-catalog)\.json|ard/v1/agents) read|POST /ard/v1/(?:search|explore) (?:none|auto|referrals|other)) [1-5][0-9]{2}", name)
        and type(count) is int and 0 < count <= 10000 for name, count in value.items()))


def valid_contract_failures(value):
    return (isinstance(value, list) and 0 < len(value) <= 32 and all(
        isinstance(row, dict) and set(row) == {"operation", "phase", "status", "continuationUnavailable"}
        and row["operation"] == "searchCatalog" and row["phase"] in PHASES
        and type(row["status"]) is int and 100 <= row["status"] <= 599
        and type(row["continuationUnavailable"]) is bool for row in value))


def prepare(environment):
    """Generate invocation-only identities and exact outbound routes before boot."""
    if environment.get("CATALOG_FUNCTIONAL_PROFILE") != "peers":
        raise Failure("invalid-peer-profile")
    directory = Path(environment["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"])
    config = read_document(Path("config/postgres.json"))
    config["auth"]["developmentTokens"] = {"peer-" + token: actor for token, actor in config["auth"]["developmentTokens"].items()}
    config["discovery"]["hosts"] = [
        {"host": host, "tenantId": TENANT, "mode": mode, "publisherDomain": "catalog.example.com"}
        for host, mode in ((PUBLIC_A, "anonymous"), (PRIVATE_A, "authenticated"))]
    write_document(directory / "peer-a.json", config)
    (directory / "peer-a.json").chmod(0o444)
    policy = read_document(directory / "policy.json")
    route = copy.deepcopy(policy["privateProfiles"][0])
    route["name"] = "peer-catalog"
    route["origins"] = ["https://" + host + ":8443" for host in (PUBLIC_A, PRIVATE_A, PUBLIC_B)]
    policy["privateProfiles"].append(route)
    write_document(directory / "policy.json", policy)
    (directory / "policy.json").chmod(0o444)
    for name, token in (("peer-reader", "peer-local-reader"), ("peer-other", "peer-other-tenant-admin"), ("peer-wrong-deployment", "local-reader")):
        (directory / "credentials" / (name + ".token")).write_text(token, encoding="ascii")
        (directory / "credentials" / (name + ".token")).chmod(0o444)
        write_document(directory / "credentials" / (name + ".json"), {
            "schemaVersion": 1, "tenantId": TENANT, "revision": "one", "origin": "https://" + PRIVATE_A + ":8443",
            "type": "bearer", "secretFile": name + ".token"})
        (directory / "credentials" / (name + ".json")).chmod(0o444)


def prepare_plugin_archives(environment, packages, *, include_mcp=False):
    """Reuse the maintained native Plugin tree and archive tool, inside this invocation."""
    directory = Path(environment["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"])
    archives = directory / "archives"
    archives.mkdir(mode=0o700)
    origin = "https://" + str(ipaddress.ip_network(environment["CATALOG_FUNCTIONAL_SUBNET"])[12]) + ":8443"
    resources = []
    for name, version in packages:
        package = archives / name
        shutil.copytree(Path(__file__).resolve().parents[2] / "samples/packages/repository-essentials", package)
        if include_mcp:
            write_document(package / "mcp.json", {
                "$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
                "mcpServers": {"tools": {"type": "stdio", "command": "node"}}})
        manifest = read_document(package / "plugin.json")
        manifest.update(name=name, version=version)
        write_document(package / "plugin.json", manifest)
        resources.append({"key": name, "kind": "plugin", "name": name, "description": "Owned peer Plugin metadata",
            "namespace": "peers", "publisher": "peer", "identifier": "urn:air:catalog.example.com:peers:" + name,
            "version": version, "state": "published", "origin": "synthetic",
            "listing": {"tags": ["peers"], "representativeQueries": ["Find " + name, "Find peer Plugin metadata"], "license": "Apache-2.0", "visibility": "public"},
            "representation": {"mediaType": "application/agent-plugins+zip", "packageDirectory": name}})
    write_document(archives / "catalog.json", {"format": 1, "dataset": "peer-fixtures", "revision": "1", "namespaces": [{"key": "peers", "displayName": "Peers"}],
        "publishers": [{"key": "peer", "displayName": "Peer", "domains": ["catalog.example.com"]}],
        "profiles": {"peers": [r["key"] for r in resources]}, "resources": resources})
    command([str(directory / "demo-archives"), "--directory", str(archives), "--prepare", "--data-file", str(archives / "catalog.json"),
        "--sample-set", "peers", "--fixture-ip", origin[8:-5], "--archive-origin", origin], environment, 60)
    write_document(directory / "archive-origin.json", {"origin": origin})
    return origin


def prepare_archives(environment):
    directory = Path(environment["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"])
    origin = prepare_plugin_archives(environment, (("a-plugin", "1.0.0"), ("b-plugin", "1.0.0"),
                                                  ("fresh-plugin", "1.0.0"), ("a-plugin-v2", "2.0.0")))
    # B retains its loopback and authenticated hosts, adding only the fixed TLS peer host.
    config = read_document(directory / "peer-a.json")
    config["demoArchives"] = {"origin": origin, "trustBundleFile": "/config/archives-cert.pem", "allowlistFile": "/config/archives-allowlist.json"}
    (directory / "peer-a.json").chmod(0o600)
    write_document(directory / "peer-a.json", config)
    (directory / "peer-a.json").chmod(0o444)



class HostClient(Client):
    def __init__(self, base, token=None, host=None, ssl_context=None):
        super().__init__(base, token, ssl_context)
        self.host = host

    def request(self, method, path, body=None, expected=(200,), headers=None, *, retry_unavailable=False, decode_body=True):
        headers = dict(headers or {})
        if self.host:
            headers["Host"] = self.host
        return super().request(method, path, body, expected, headers,
                               retry_unavailable=retry_unavailable, decode_body=decode_body)


def mutate(client, path, body, status=200, etag=None, method="POST"):
    headers = {"Idempotency-Key": key("peers")}
    if etag:
        headers["If-Match"] = etag
    return client.request(method, path, body, (status,), headers)


def registry_members(client):
    """A stable small fixture must traverse actual opaque tokens without repeats."""
    token, tokens, members = "", set(), []
    for _ in range(12):
        query = {"pageSize": 2}
        if token:
            query["pageToken"] = token
        page, _ = client.request("GET", "/ard/v1/agents?" + urllib.parse.urlencode(query))
        assert isinstance(page.get("items"), list) and len(page["items"]) <= 2, "peer List page invalid"
        members.extend(page["items"])
        token = page.get("pageToken", "")
        if not token:
            assert len(members) == page["total"], "peer List total mismatch"
            ids = [entry["identifier"] for entry in members]
            assert len(ids) == len(set(ids)), "peer List repeated a member"
            return members
        assert isinstance(token, str) and 0 < len(token) <= 4096 and token not in tokens, "peer List token invalid"
        tokens.add(token)
    raise Failure("peer-list-page-limit")


def check_indexed_search(document, indexed, source_id):
    """Check exact observation projections, not merely the result count."""
    expected = {item["identifier"]: item for item in indexed}
    results = document["items"]
    assert not document["partial"] and not document.get("nextCursor"), "peer Search incomplete"
    assert len(results) == len(indexed) and {item["identifier"] for item in results} == set(expected), "peer Search membership mismatch"
    for result in results:
        observation = expected[result["identifier"]]
        assert result["type"] == "indexed" and all(result[field] == observation[field] for field in ("kind", "version", "versionPresent")), "peer Search identity mismatch"
        evidence = result["indexed"]
        assert evidence["normalizedEntryDigest"] == observation["normalizedEntryDigest"], "peer Search content mismatch"
        assert evidence["sourceIds"] == [source_id] and evidence["sourceCount"] == 1 and not any(evidence[field] for field in ("sourcesTruncated", "conflict", "sameSourceConflict")), "peer Search provenance mismatch"


class Peers:
    def __init__(self, environment):
        self.env = dict(environment)
        if self.env.get("CATALOG_FUNCTIONAL_PROFILE") != "peers" or not re.fullmatch(r"[0-9a-f]{12}", self.env.get("CATALOG_FUNCTIONAL_SUFFIX", "")):
            raise Failure("invalid-peer-owner")
        self.state = Path(self.env["CATALOG_FUNCTIONAL_STATE_DIRECTORY"])
        self.fixture = Path(self.env["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"])
        self.env.update(IMAGE="open-agentic-catalog:functional-" + self.env["CATALOG_FUNCTIONAL_SUFFIX"],
                        VERSION="functional", COMMIT=self.env["CATALOG_FUNCTIONAL_COMMIT"],
                        CATALOG_COMPOSE_HTTP_PORT=self.env["CATALOG_FUNCTIONAL_HTTP_PORT"],
                        CATALOG_COMPOSE_FIXTURE_PORT=self.env["CATALOG_FUNCTIONAL_FIXTURE_PORT"], CATALOG_COMPOSE_POSTGRES_PORT="0",
                        CATALOG_COMPOSE_SUBNET=self.env["CATALOG_FUNCTIONAL_SUBNET"],
                        CATALOG_COMPOSE_FEDERATION_POLICY_FILE=str(self.fixture / "policy.json"),
                        CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY=str(self.fixture / "trust"), COMPOSE_PROFILES="",
                        # Make unexports runtime settings. Preserve the owning
                        # workflow's fixed inputs for later process recreation.
                        CATALOG_HTTP_REQUEST_TIMEOUT="3s", CATALOG_WORKER_SOURCE_MAINTENANCE_INTERVAL="1m",
                        CATALOG_WORKER_HOUSEKEEPING_INTERVAL="1m", CATALOG_WORKER_TERMINAL_JOB_RETENTION="24h",
                        CATALOG_WORKER_ACKNOWLEDGED_OUTBOX_RETENTION="1h")
        self.compose_args = shlex.split(self.env.get("CATALOG_FUNCTIONAL_COMPOSE_COMMAND", "docker compose")) + [
            "--env-file", "/dev/null", "-f", "compose.yaml", "-f", "test/functional/compose.yaml", "-f", "test/functional/peers.compose.yaml",
            "-p", "open-agentic-catalog-functional-" + self.env["CATALOG_FUNCTIONAL_SUFFIX"]]
        self.end = time.monotonic() + 600
        self.completed = []
        self.b = Client(self.env["CATALOG_FUNCTIONAL_BASE_URL"], "local-admin")
        self.other = Client(self.b.base_url, "other-tenant-admin")
        self.b_public = Client(self.b.base_url)
        self.b_private = HostClient(self.b.base_url, "local-reader", PRIVATE_B)

    def compose(self, *arguments, capture=False):
        return command(self.compose_args + list(arguments), self.env, min(45, self.end - time.monotonic()), capture=capture)

    def phase(self, name):
        assert name == PHASES[len(self.completed)], "peer phase order mismatch"
        write_document(self.state / "peers-phase.json", {"phase": name})
        print("Peer phase: " + name, flush=True)

    def passed(self, name):
        assert name == PHASES[len(self.completed)], "peer progress mismatch"
        self.completed.append(name)
        write_document(self.state / "peers.json", {"completed": self.completed, "catalogs": 2, "publishedKinds": KINDS if "seed" in self.completed else []})

    def wait(self, callback, timeout=60, interval=0.2):
        end = min(self.end, time.monotonic() + timeout)
        while time.monotonic() < end:
            value = callback()
            remaining = end - time.monotonic()
            if remaining <= 0:
                break
            if value:
                return value
            time.sleep(min(interval, remaining))
        raise Failure("peer-state-timeout")

    def snapshot(self, client, host):
        candidate, _ = mutate(client, "/api/v1/publication/snapshots", {"host": host}, 201)
        return mutate(client, "/api/v1/publication/snapshots/" + candidate["id"] + ":activate",
                      {"host": host, "reason": "Owned peer publication"}, etag=candidate["etag"])[0]

    def create_item(self, name, kind="agent", visibility="public", draft=False):
        resource, _ = mutate(self.author, "/api/v1/resources", {"namespaceId": self.namespace,
            "canonicalIdentifier": "urn:air:catalog.example.com:" + self.namespace_slug + ":" + name, "kind": kind}, 201)
        # Reuse the maintained Agent example; other representations mirror the
        # existing demo's inert MCP Card and Skill metadata, never remote code.
        if kind == "agent":
            example = read_document(Path("api/examples/agent-lifecycle.json"))
            representation = next(r["body"]["representations"][0] for r in example["requests"] if r["name"] == "Create Agent version")
            representation["data"]["capabilities"]["streaming"] = False
        elif kind == "mcp_server":
            representation = {"role": "primary", "mediaType": "application/mcp-server-card+json", "data": {
                "$schema": "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json",
                "name": "com.example/peer", "title": "Peer MCP", "description": "Peer metadata", "version": "1.0.0",
                "remotes": [{"type": "streamable-http", "url": "https://mcp.example.com/peer", "supportedProtocolVersions": ["2025-11-25"]}]}}
        elif kind == "plugin":
            archive = "b-plugin" if self.namespace_slug == "peers-b" else "fresh-plugin" if name.startswith("freshpeer") else "a-plugin"
            representation = self.plugin_representation(archive)
        else:
            representation = {"role": "primary", "mediaType": "application/agent-skills+md", "content": "---\nname: peer-skill\ndescription: Inert peer test instructions.\nlicense: Apache-2.0\n---\n# Instructions\nReview catalog metadata.\n"}
        version, _ = mutate(self.author, "/api/v1/resources/" + resource["id"] + "/versions", {"version": "1.0.0", "representations": [representation]}, 201)
        listing, _ = mutate(self.author, "/api/v1/catalog-listings", {"namespaceId": self.namespace, "resourceId": resource["id"]}, 201)
        item = {"resource": resource, "version": version, "listing": listing, "name": name, "visibility": visibility}
        self.publish_item(item, draft)
        return item

    def publish_item(self, item, draft=False):
        path = "/api/v1/catalog-listings/" + item["listing"]["id"]
        _, headers = self.author.request("GET", path)
        revision, headers = mutate(self.author, path + "/revisions", {"versionRevisionId": item["version"]["revision"]["id"],
            "displayName": item["name"], "description": "Peer catalog discovery " + item["name"], "tags": ["peers", "peer-a" if self.namespace_slug == "peers-a" else "peer-b", item["resource"]["kind"]] if item["visibility"] == "public" else ["private-control"],
            "representativeQueries": ["Find " + item["name"], "Find peer catalog metadata"], "publisherId": self.publisher,
            "license": "Apache-2.0", "categories": ["integration", item["resource"]["kind"]] if item["visibility"] == "public" else ["private-control"],
            "visibility": item["visibility"]}, 201, header(headers, "ETag"))
        listing_etag = header(headers, "ETag")
        item["listingRevision"] = revision["revision"]
        if draft:
            return
        submission, headers = mutate(self.author, path + "/submissions", {}, 201)
        mutate(self.reviewer, "/api/v1/review-submissions/" + submission["id"] + ":approve", {"reason": "Independent peer fixture review"}, etag=header(headers, "ETag"))
        publish = {"submissionId": submission["id"]}
        if "release" in item:
            publish.update(priorDisposition="superseded", priorReason="Owned peer listing update")
        item["release"] = mutate(self.publisher_client, path + ":publish", publish, 201, listing_etag)[0]["release"]

    def connect(self, host=PUBLIC_A, credential=None):
        access = {"url": "https://" + host + ":8443/.well-known/ard.json", "networkProfile": "peer-catalog"}
        if credential:
            access["credentialBinding"] = credential
        inspection, _ = mutate(self.b, "/api/v1/federation/source-inspections", access, 201)
        assert inspection["sourceType"] == "hybrid" and not inspection.get("registryListUrl"), "peer discovery classification mismatch"
        body = {"inspectionId": inspection["id"], "displayName": "Owned Catalog B" if host == PUBLIC_B else "Owned Catalog A", "retrievalMode": "indexed",
                "scope": {"kinds": KINDS}, "updateMode": "manual_review", "networkProfile": "peer-catalog", "reason": "Approve owned peer"}
        if credential:
            body["credentialBinding"] = credential
        return mutate(self.b, "/api/v1/federation/sources", body, 201)[0]

    def change_source(self, source, patch):
        path = "/api/v1/federation/sources/" + source["id"]
        current, headers = self.b.request("GET", path)
        preview, _ = mutate(self.b, path + "/change-previews", {"intent": "update", **patch})
        changed, _ = mutate(self.b, path, {**patch, "changePreviewDigest": preview["digest"], "reason": "Owned peer scope or live change"}, etag=header(headers, "ETag"), method="PATCH")
        assert changed["revision"] == current["revision"] + 1, "peer source revision mismatch"
        return changed

    def finish_job(self, job):
        def read():
            current = self.b.request("GET", "/api/v1/jobs/" + job["id"])[0]
            if current["state"] in ("failed", "cancelled", "dead_letter"):
                raise Failure("peer-sync-terminal-failure")
            return current if current["state"] == "succeeded" else None
        done = self.wait(read)
        assert done["id"] == job["id"], "peer Job identity changed"

    def synchronize(self, source):
        current = self.b.request("GET", "/api/v1/federation/sources/" + source["id"])[0]
        job = sync(self.b, source["id"], current["revision"], "peer")["job"]
        self.finish_job(job)
        return observations(self.b, source["id"])

    def capture_correction_evidence(self, source, identifier):
        """Bind one complete generation to exact bytes and retained provenance."""
        current = self.b.request("GET", "/api/v1/federation/sources/" + source["id"])[0]
        assert current["id"] == source["id"], "correction returned another source"
        snapshot = self.b.request("GET", "/api/v1/federation/snapshots/" + current["lastCompleteSnapshotId"])[0]
        assert snapshot["id"] == current["lastCompleteSnapshotId"] and snapshot["sourceId"] == source["id"] and snapshot["state"] == "complete" and snapshot["enumerationComplete"], "correction lacks a complete source snapshot"
        selected = [row for row in observations(self.b, source["id"]) if row["identifier"] == identifier]
        assert len(selected) == 1, "correction observation identity is ambiguous or missing"
        observation = selected[0]
        assert observation["sourceId"] == source["id"], "correction returned another source's observation"
        raw = self.b.request("GET", "/api/v1/observations/" + observation["id"] + "/raw", decode_body=False)[0]
        assert observation["exactEntryDigest"] == "sha256:" + hashlib.sha256(raw).hexdigest(), "correction bytes disagree with retained digest"
        page = self.b.request("GET", "/api/v1/observations/" + observation["id"] + "/occurrences?limit=100")[0]
        assert not page.get("nextCursor"), "correction fixture exceeded its occurrence bound"
        occurrences = [row for row in page["items"] if row["snapshotId"] == snapshot["id"]]
        assert len(occurrences) == 1 and occurrences[0]["evidenceComplete"], "correction lacks exact complete occurrence"
        occurrence = occurrences[0]
        assert occurrence["observationId"] == observation["id"] and occurrence["sourceId"] == source["id"] and occurrence["sourceRevision"] == snapshot["sourceRevision"] and occurrence["exactEntryDigest"] == observation["exactEntryDigest"], "correction provenance is inconsistent"
        return {"snapshot": snapshot, "observationId": observation["id"], "raw": raw, "occurrence": occurrence}

    def check_correction_history(self, retained, corrected):
        """A/B/C must differ, while both older byte/provenance records survive."""
        generations = [*retained, corrected]
        assert len(generations) == 3 and len({row["snapshot"]["id"] for row in generations}) == 3, "correction did not traverse A/B/C snapshots"
        assert len({row["raw"] for row in generations}) == 3, "correction did not change A/B/C content"
        for saved in retained:
            identifier = saved["observationId"]
            raw = self.b.request("GET", "/api/v1/observations/" + identifier + "/raw", decode_body=False)[0]
            assert raw == saved["raw"], "forward correction rewrote historical bytes"
            snapshot = self.b.request("GET", "/api/v1/federation/snapshots/" + saved["snapshot"]["id"])[0]
            assert snapshot == saved["snapshot"], "forward correction rewrote historical snapshot"
            page = self.b.request("GET", "/api/v1/observations/" + identifier + "/occurrences?limit=100")[0]
            assert not page.get("nextCursor") and saved["occurrence"] in page["items"], "forward correction rewrote historical occurrence"

    def failure_recovery(self, source, fault, recover):
        before = observations(self.b, source["id"])
        last = self.b.request("GET", "/api/v1/federation/sources/" + source["id"])[0]
        snapshots = {item["id"] for item in snapshot_items(self.b, source["id"])}
        try:
            fault()
            job = sync(self.b, source["id"], last["revision"], "peer-fault")["job"]
            failed = self.wait(lambda: next((item for item in snapshot_items(self.b, source["id"])
                if item["id"] not in snapshots and item["state"] == "failed"), None))
            assert not failed["enumerationComplete"] and not failed.get("withdrawalAuthority"), "failed peer enumeration gained authority"
            retained = self.b.request("GET", "/api/v1/federation/sources/" + source["id"])[0]
            assert retained["lastCompleteSnapshotId"] == last["lastCompleteSnapshotId"], "failed peer sync replaced complete snapshot"
            assert observations(self.b, source["id"]) == before, "peer outage changed membership"
            events = self.b.request("GET", "/api/v1/audit-events?" + urllib.parse.urlencode({"action": "source.sync.failed", "targetType": "source_snapshot", "targetId": failed["id"], "limit": 10}))[0]["items"]
            assert any(event["details"]["facts"].get("jobId") == job["id"] and event["details"]["facts"].get("jobAttempt", 0) >= 1 for event in events), "peer failure lacks Job attempt attribution"
        finally:
            recover()
        state = self.wait(lambda: (lambda j: j if j["state"] in ("succeeded", "failed", "cancelled", "dead_letter", "waiting") else None)(self.b.request("GET", "/api/v1/jobs/" + job["id"])[0]))
        if state["state"] in ("failed", "cancelled", "dead_letter"):
            self.synchronize(source)
        else:
            self.finish_job(job)
        recovered = self.b.request("GET", "/api/v1/federation/sources/" + source["id"])[0]
        assert recovered["lastCompleteSnapshotId"] != last["lastCompleteSnapshotId"] and not recovered.get("lastError"), "peer recovery did not advance complete state"
        assert any(item["id"] == recovered["lastCompleteSnapshotId"] and item["state"] == "complete" and item["enumerationComplete"] for item in snapshot_items(self.b, source["id"])), "peer recovery lacks complete enumeration"
        assert {item["id"] for item in observations(self.b, source["id"])} == {item["id"] for item in before}, "peer recovery changed observation identities"

    def run(self):
        self.phase("startup")
        address = self.compose("port", "api-a", "8080", capture=True).decode().strip()
        if not re.fullmatch(r"127\.0\.0\.1:[0-9]{1,5}", address):
            raise Failure("nonlocal-peer-listener")
        base = "http://" + address
        self.a = Client(base, "peer-local-admin")
        self.author = Client(base, "peer-local-contributor")
        self.reviewer = Client(base, "peer-local-reviewer")
        self.publisher_client = Client(base, "peer-local-publisher")
        def ready():
            try:
                return self.a.request("GET", "/readyz")[0]
            except (ClientFailure, AssertionError):
                return None
        self.wait(ready)
        self.compose("exec", "-T", "worker-a", "/usr/local/bin/catalogctl", "probe", "--url", "http://127.0.0.1:9090/readyz")
        assert self.a.request("GET", "/version")[0] == self.b.request("GET", "/version")[0], "peer versions differ"
        Client(base, "local-admin").request("GET", "/api/v1/resources", expected=(401,))
        tls = fixture_tls_context(self.fixture / "trust/functional-ca.pem")
        self.public = HostClient("https://127.0.0.1:" + self.env["CATALOG_COMPOSE_FIXTURE_PORT"], host=PUBLIC_A + ":8443", ssl_context=tls)
        self.private = HostClient(self.public.base_url, "peer-local-reader", PRIVATE_A + ":8443", tls)
        self.passed("startup")

        self.phase("seed")
        self.namespace_slug = "peers-a"
        self.namespace = mutate(self.a, "/api/v1/namespaces", {"slug": self.namespace_slug, "displayName": "Peers A"}, 201)[0]["id"]
        publisher, headers = mutate(self.a, "/api/v1/publishers", {"displayName": "Peer Publisher", "domains": ["catalog.example.com"]}, 201)
        self.publisher = publisher["id"]
        mutate(self.a, "/api/v1/publishers/" + self.publisher + "/publication-domains/catalog.example.com:approve", {"reason": "Owned peer domain"}, etag=header(headers, "ETag"))
        items = [self.create_item(name, kind) for name, kind in zip(("peer-a-agent", "peer-a-mcp", "peer-a-skill", "peer-a-plugin"), KINDS, strict=True)]
        draft = self.create_item("draft", draft=True)
        private = self.create_item("tenant", visibility="tenant")
        self.snapshot(self.publisher_client, PUBLIC_A)
        self.snapshot(self.publisher_client, PRIVATE_A)
        self.b_owner = copy.copy(self)
        self.b_owner.a = self.b
        self.b_owner.author = Client(self.b.base_url, "local-contributor")
        self.b_owner.reviewer = Client(self.b.base_url, "local-reviewer")
        self.b_owner.publisher_client = Client(self.b.base_url, "local-publisher")
        self.b_owner.namespace_slug = "peers-b"
        self.b_owner.namespace = mutate(self.b, "/api/v1/namespaces", {"slug": "peers-b", "displayName": "Peers B"}, 201)[0]["id"]
        publisher_b, headers_b = mutate(self.b, "/api/v1/publishers", {"displayName": "Publisher B", "domains": ["catalog.example.com"]}, 201)
        self.b_owner.publisher = publisher_b["id"]
        mutate(self.b, "/api/v1/publishers/" + publisher_b["id"] + "/publication-domains/catalog.example.com:approve", {"reason": "Owned B domain"}, etag=header(headers_b, "ETag"))
        self.b_items = [self.b_owner.create_item("peer-b-" + kind, kind) for kind in KINDS]
        self.b_owner.create_item("b-draft", draft=True)
        b_private = self.b_owner.create_item("b-tenant", visibility="tenant")
        self.b_private_native = {x["resource"]["canonicalIdentifier"] for x in self.b_items} | {b_private["resource"]["canonicalIdentifier"]}
        self.baseline_resources = {r["id"]: r for r in self.b.request("GET", "/api/v1/resources")[0]["items"]}
        self.expected_resources = set(self.baseline_resources)
        self.baseline_releases = [self.b.request("GET", "/api/v1/listing-releases/" + x["release"]["id"])[0] for x in self.b_items]
        self.b_native = {r["resource"]["canonicalIdentifier"] for r in self.b_items}
        write_document(self.state / "peer-inventories.json", {"a": [{"identifier": r["resource"]["canonicalIdentifier"], "kind": r["resource"]["kind"]} for r in items],
            "b": [{"identifier": r["resource"]["canonicalIdentifier"], "kind": r["resource"]["kind"]} for r in self.b_items]})
        self.snapshot(self.b, "127.0.0.1")
        self.snapshot(self.b, PUBLIC_B)
        self.snapshot(self.b, PRIVATE_B)
        self.baseline_entries = {x["identifier"]: x for x in self.b_public.request("GET", "/.well-known/ard.json")[0]["entries"] if x["type"] != "application/ai-registry+json"}
        assert set(self.baseline_entries) == self.b_native, "B public publication disclosed private or draft controls"
        initial = registry_members(self.public)
        ids = {item["resource"]["canonicalIdentifier"] for item in items}
        media = {"agent": "application/a2a-agent-card+json", "mcp_server": "application/mcp-server-card+json", 'skill': 'text/markdown; profile="urn:air:agent-skills"', "plugin": "application/agent-plugins+zip"}
        for client, inventory in ((self.public, items), (self.b_public, self.b_items)):
            published = {x["identifier"]: x for x in registry_members(client)}
            for item, kind in zip(inventory, KINDS, strict=True):
                entry = published[item["resource"]["canonicalIdentifier"]]
                assert entry["type"] == media[kind] and entry["version"] == "1.0.0", "native publication kind or version mismatch"
                assert entry["metadata"]["license"] == "Apache-2.0" and set(entry[SUBJECT_IRI]) == {"integration", kind}, "reviewed listing declarations missing"
            query = {"@context": {"topic.example": "http://purl.org/dc/terms/"}, "text": "peer", "filter": {"topic.example:subject": "integration", "metadata.license": "Apache-2.0"}}
            selected = client.request("POST", "/ard/v1/search", {"query": query, "federation": "none", "pageSize": 20})[0]
            assert {x["identifier"] for x in selected["results"]} == {x["resource"]["canonicalIdentifier"] for x in inventory}, "listing filters disclosed private/draft data or omitted a kind"
            explored = client.request("POST", "/ard/v1/explore", {"query": query, "resultType": {"facets": [{"field": SUBJECT_IRI}, {"field": "metadata.license"}]}})[0]
            assert {x["value"]: x["count"] for x in explored["facets"][SUBJECT_IRI]["buckets"]} == {"integration": 4, **dict.fromkeys(KINDS, 1)}, "topic facet counts mismatch"
            assert explored["facets"]["metadata.license"]["buckets"] == [{"value": "Apache-2.0", "count": 4}], "license facet counts mismatch"
            native_cases = (
                ("agent", {"data.skills.id": "resolve-order-exceptions", "data.capabilities.streaming": "false",
                           "data.supportedInterfaces.protocolBinding": "JSONRPC", "data.supportedInterfaces.protocolVersion": "1.0"}),
                ("mcp_server", {"data.remotes.type": "streamable-http", "data.remotes.supportedProtocolVersions": "2025-11-25"}),
            )
            for kind, filters in native_cases:
                native_query = {"@context": {"native.body": "https://agenticresourcediscovery.org/ns#data"}, "text": "peer",
                                "filter": {"native.body" + field[4:]: value for field, value in filters.items()}}
                matched = client.request("POST", "/ard/v1/search", {"query": native_query, "federation": "none"})[0]
                expected_id = next(x["resource"]["canonicalIdentifier"] for x in inventory if x["resource"]["kind"] == kind)
                assert [x["identifier"] for x in matched["results"]] == [expected_id], "native filters omitted a card or invented Skill/Plugin facts"
                native_facets = client.request("POST", "/ard/v1/explore", {"query": native_query,
                    "resultType": {"facets": [{"field": "native.body" + field[4:]} for field in filters]}})[0]
                for field, value in filters.items():
                    assert native_facets["facets"][field]["buckets"] == [{"value": value, "count": 1}], "native facet counts mismatch"

        registry = {entry["identifier"] for entry in initial if entry["type"] == "application/ai-registry+json"}
        assert len(registry) == 1 and {entry["identifier"] for entry in initial} == ids | registry, "public peer seed mismatch"
        assert private["resource"]["canonicalIdentifier"] in {e["identifier"] for e in registry_members(self.private)}, "private publication control missing"
        assert draft["resource"]["canonicalIdentifier"] not in {e["identifier"] for e in registry_members(self.private)}, "draft disclosed"
        assert len(self.expected_resources) == 6 and not self.b_native & ids, "Catalog inventories are not independent"
        self.b.request("GET", "/api/v1/resources/" + items[0]["resource"]["id"], expected=(404,))
        self.passed("seed")

        self.phase("index")
        self.compose("stop", "--timeout", "10", "peer-archives")
        source = self.connect()
        indexed = self.synchronize(source)
        self.check_index(indexed, initial, ids, source)
        correction_history = [self.capture_correction_evidence(source, items[0]["resource"]["canonicalIdentifier"])]
        self.other.request("GET", "/api/v1/federation/sources/" + source["id"], expected=(404,))
        for item in indexed:
            self.other.request("GET", "/api/v1/observations/" + item["id"] + "/raw", expected=(404,))
        Client(self.b.base_url, "local-reader").request("GET", "/api/v1/federation/sources/" + source["id"], expected=(403,))
        mutate(self.other, "/api/v1/federation/sources/" + source["id"] + "/change-previews", {"intent": "remove"}, 404)
        self.check_no_adoption()
        self.passed("index")

        self.phase("unchanged")
        again = self.synchronize(source)
        assert {x["id"] for x in again} == {x["id"] for x in indexed}, "unchanged peer sync duplicated observations"
        self.check_combined(indexed)
        self.check_control_declarations(indexed)
        self.passed("unchanged")
        self.phase("scope")
        source = self.change_source(source, {"scope": {"kinds": ["skill"]}})
        search = self.b.request("POST", "/api/v1/catalog/search", {"tags": ["peers"], "origins": ["indexed"], "limit": 20})[0]
        check_indexed_search(search, [item for item in indexed if item["kind"] == "skill"], source["id"])
        for scope, selected in (({"kinds": ["plugin"]}, [x for x in indexed if x["kind"] == "plugin"]),
                                ({"identifiers": [items[0]["resource"]["canonicalIdentifier"]]}, [x for x in indexed if x["kind"] == "agent"]),
                                ({"tags": ["skill"]}, [x for x in indexed if x["kind"] == "skill"])):
            source = self.change_source(source, {"scope": scope})
            page = self.b.request("POST", "/api/v1/catalog/search", {"origins": ["indexed"], "tags": ["peers"], "limit": 20})[0]
            check_indexed_search(page, selected, source["id"])
            self.check_no_adoption()
        retained = {row["id"]: self.b.request("GET", "/api/v1/observations/" + row["id"] + "/raw")[0] for row in indexed}
        for excluded in ([row["identifier"]] for row in indexed):
            source = self.change_source(source, {"scope": {"kinds": KINDS, "excludedIdentifiers": excluded}})
            selected = [row for row in indexed if row["identifier"] not in excluded]
            page = self.b.request("POST", "/api/v1/catalog/search", {"origins": ["indexed"], "tags": ["peers"], "limit": 20})[0]
            check_indexed_search(page, selected, source["id"])
            legacy = self.b.request("POST", "/api/v1/observations:search", {"tags": ["peers"], "limit": 20})[0]
            assert {row["observation"]["identifier"] for row in legacy["items"]} == {row["identifier"] for row in selected}, "observation Search bypassed current scope"
            query = {"origins": ["indexed"], "tags": ["peers"], "limit": 1}
            seen = set()
            previous_headers = None
            for page_number in range(1, len(indexed) + 1):
                page, previous_headers = peer_pagination.request_page(
                    self.b, "/api/v1/catalog/search", query, page_number,
                    previous_headers=previous_headers)
                identifiers = {row["identifier"] for row in page["items"]}
                assert not identifiers & set(excluded) and not identifiers & seen, "excluded or repeated entry on a later page"
                seen |= identifiers
                if not page.get("nextCursor"):
                    break
                query["cursor"] = page["nextCursor"]
            assert seen == {row["identifier"] for row in selected}, "exclusion pagination lost eligible entries"
        source = self.change_source(source, {"scope": {"kinds": KINDS, "excludedIdentifiers": sorted(ids)}})
        empty = self.b.request("POST", "/api/v1/catalog/search", {"origins": ["indexed"], "limit": 20})[0]
        assert not empty["items"] and empty["durableTotal"] == 0 and not empty["partial"], "excluded inventory is not a healthy empty view"
        for observation_id, raw in retained.items():
            assert self.b.request("GET", "/api/v1/observations/" + observation_id + "/raw")[0] == raw, "exclusion changed administrative evidence"
        withdrawn = self.synchronize(source)
        assert not any(row["freshness"] == "current" for row in withdrawn), "excluded synchronization retained current members"
        source = self.change_source(source, {"scope": {"kinds": KINDS, "excludedIdentifiers": []}})
        absent = self.b.request("POST", "/api/v1/catalog/search", {"origins": ["indexed"], "limit": 20})[0]
        assert absent["items"] == [], "clearing exclusions invented current evidence"
        recovered = self.synchronize(source)
        assert {row["identifier"] for row in recovered if row["freshness"] == "current"} == ids, "resync did not restore all four kinds"
        restored = self.b.request("POST", "/api/v1/catalog/search", {"origins": ["indexed"], "tags": ["peers"], "limit": 20})[0]
        check_indexed_search(restored, indexed, source["id"])
        self.passed("scope")
        self.phase("update")
        self.compose("start", "peer-archives")
        items[0]["name"] = "agent-revised"
        self.publish_item(items[0])
        self.snapshot(self.publisher_client, PUBLIC_A)
        updated = self.synchronize(source)
        self.check_index(updated, registry_members(self.public), ids, source)
        correction_history.append(self.capture_correction_evidence(source, items[0]["resource"]["canonicalIdentifier"]))
        old = next(x for x in indexed if x["identifier"] == items[0]["resource"]["canonicalIdentifier"])
        new = next(x for x in updated if x["identifier"] == old["identifier"])
        assert new["identityDigest"] != old["identityDigest"], "changed publication did not converge"
        # Use the same governed workflow for a valid forward correction C.
        # Keep this indexed lifecycle proof separate from later live paging.
        items[0]["name"] = "peer-a-agent-corrected"
        self.publish_item(items[0])
        self.snapshot(self.publisher_client, PUBLIC_A)
        updated = self.synchronize(source)
        self.check_index(updated, registry_members(self.public), ids, source)
        corrected = self.capture_correction_evidence(source, items[0]["resource"]["canonicalIdentifier"])
        self.check_correction_history(correction_history, corrected)
        # Reuse the independently verified B/C correction evidence for the UI.
        # The earlier A snapshot belongs to a different source configuration.
        prior = correction_history[-1]
        comparison = {"beforeId": prior["snapshot"]["id"], "afterId": corrected["snapshot"]["id"],
                      "sourceRevision": corrected["snapshot"]["sourceRevision"],
                      "identifier": items[0]["resource"]["canonicalIdentifier"],
                      "beforeObservationId": prior["observationId"], "afterObservationId": corrected["observationId"],
                      "beforeDigest": prior["occurrence"]["exactEntryDigest"],
                      "afterDigest": corrected["occurrence"]["exactEntryDigest"]}
        assert prior["snapshot"]["sourceRevision"] == comparison["sourceRevision"], "comparison fixture crossed source revisions"
        # Exercise the shipped read-only CLI against these real retained B/C
        # memberships, independently of the browser's local request driver.
        query = urllib.parse.urlencode({"otherSnapshotId": comparison["afterId"], "view": "changes", "limit": 100})
        expected_comparison = self.b.request("GET", f"/api/v1/federation/snapshots/{comparison['beforeId']}/comparison?{query}")[0]
        cli_comparison = json.loads(self.compose("exec", "-T", "--env", "CATALOG_REMOTE_TOKEN=local-admin",
            "--env", "CATALOG_DATABASE_DSN_FILE=/does-not-exist", "api", "/usr/local/bin/catalogctl", "remote", "source-compare",
            "--base-url", "http://127.0.0.1:8080", "--before-snapshot-id", comparison["beforeId"],
            "--after-snapshot-id", comparison["afterId"], "--view", "changes", "--limit", "100", capture=True))
        assert cli_comparison["meta"]["requestId"] and cli_comparison["data"] == expected_comparison, "packaged CLI changed saved comparison"
        assert not expected_comparison.get("nextCursor") and any(row["change"] == "changed" and row["before"]["observationId"] == comparison["beforeObservationId"] and row["after"]["observationId"] == comparison["afterObservationId"] for row in expected_comparison["items"]), "CLI fixture lacks the independently retained changed entry"
        self.check_no_adoption()
        items[2]["name"] = "peer-a-skill-draft-edit"
        self.publish_item(items[2], draft=True)

        sealed = self.synchronize(source)
        assert {x["identityDigest"] for x in sealed} == {x["identityDigest"] for x in updated}, "draft changed published observations"
        plugin = items[3]
        old_plugin_version = copy.deepcopy(plugin["version"])
        plugin["version"] = mutate(self.author, "/api/v1/resources/" + plugin["resource"]["id"] + "/versions",
            {"version": "2.0.0", "representations": [self.plugin_representation("a-plugin-v2")]}, 201)[0]
        self.publish_item(plugin)
        self.snapshot(self.publisher_client, PUBLIC_A)
        changed_plugin = self.synchronize(source)
        previous = next(x for x in updated if x["kind"] == "plugin")
        current = next(x for x in changed_plugin if x["kind"] == "plugin")
        assert current["version"] == "2.0.0" and current["normalizedEntryDigest"] != previous["normalizedEntryDigest"], "Plugin version did not converge"
        self.b.request("GET", "/api/v1/observations/" + previous["id"] + "/raw")
        self.author.request("GET", "/api/v1/version-revisions/" + old_plugin_version["revision"]["id"])
        self.compose("stop", "--timeout", "10", "peer-archives")
        self.passed("update")
        self.phase("removal")
        path = "/api/v1/listing-releases/" + items[3]["release"]["id"]
        _, headers = self.publisher_client.request("GET", path)
        mutate(self.publisher_client, path + ":retire", {"reason": "Owned peer removal"}, etag=header(headers, "ETag"))
        self.snapshot(self.publisher_client, PUBLIC_A)
        remaining = ids - {items[3]["resource"]["canonicalIdentifier"]}
        self.check_index(self.synchronize(source), registry_members(self.public), remaining, source)
        history = observations(self.b, source["id"], "all")
        assert any(x["identifier"] not in remaining and x["freshness"] == "withdrawn" for x in history), "absent manifest member not withdrawn"
        self.passed("removal")
        self.phase("api-recovery")
        source = self.change_source(source, {"retrievalMode": "hybrid", "live": {"enabled": True, "queryPrivacyAcknowledged": True, "federationMode": "none"},
            "discoveryAssignments": [{"host": PRIVATE_B, "audience": "authenticated"}]})
        def unavailable():
            self.compose("stop", "--timeout", "10", "api-a")
            self.b.request("GET", "/readyz")
            self.check_combined(observations(self.b, source["id"]))
            ard, response_headers = self.b_private.request("POST", "/ard/v1/search", {"query": {"text": "peer"}, "federation": "auto", "pageSize": 100})
            assert {x["identifier"] for x in ard["results"]} == self.b_private_native and "partial" in header(response_headers, "Server-Timing"), "ARD outage lost local results or partial signal"
            control = self.b.request("POST", "/api/v1/catalog/search", {"text": "peer", "tags": ["peers"], "origins": ["local", "indexed", "live"], "limit": 100})[0]
            assert control["partial"] and control["diagnostics"] and {x["identifier"] for x in control["items"]} == self.b_native | remaining, "Control outage lost retained results or diagnostics"
        self.failure_recovery(source, unavailable, lambda: (self.compose("start", "api-a"), self.wait(ready)))
        source = self.change_source(source, {"retrievalMode": "indexed", "live": {"enabled": False}})
        self.passed("api-recovery")
        self.phase("unpublish-recovery")
        def unpublish():
            mutate(self.publisher_client, "/api/v1/publication:unpublish", {"host": PUBLIC_A, "reason": "Owned peer unpublication"})
            self.public.request("GET", "/.well-known/ard.json", expected=(404,))
        self.failure_recovery(source, unpublish, lambda: self.snapshot(self.publisher_client, PUBLIC_A))
        self.passed("unpublish-recovery")
        self.phase("live")
        source = self.change_source(source, {"retrievalMode": "hybrid", "live": {"enabled": True, "queryPrivacyAcknowledged": True, "federationMode": "none"},
            "discoveryAssignments": [{"host": PRIVATE_B, "audience": "authenticated"}]})
        self.compose("start", "peer-archives")
        fresh = [self.create_item("freshpeer-" + kind, kind) for kind in KINDS]
        self.snapshot(self.publisher_client, PUBLIC_A)
        self.compose("stop", "--timeout", "10", "peer-archives")
        fresh_ids = {x["resource"]["canonicalIdentifier"] for x in fresh}
        assert not fresh_ids & {x["identifier"] for x in observations(self.b, source["id"])}, "live controls were already indexed"
        source = self.check_live(source, fresh_ids)
        before_exclusions = observations(self.b, source["id"])
        for excluded in ([identifier] for identifier in sorted(fresh_ids)):
            source = self.change_source(source, {"scope": {"kinds": KINDS, "excludedIdentifiers": excluded}})
            control = self.b.request("POST", "/api/v1/catalog/search", {"text": "freshpeer", "origins": ["live"], "limit": 20})[0]
            assert {row["identifier"] for row in control["items"]} == fresh_ids - set(excluded) and not control["partial"], "live exclusion lost other kinds or reported failure"
        source = self.change_source(source, {"scope": {"kinds": KINDS, "excludedIdentifiers": sorted(fresh_ids)}})
        empty = self.b.request("POST", "/api/v1/catalog/search", {"text": "freshpeer", "origins": ["live"], "limit": 20})[0]
        assert not empty["items"] and not empty["partial"], "all-excluded live response is not healthy"
        public = self.b_private.request("POST", "/ard/v1/search", {"query": {"text": "freshpeer"}, "federation": "auto", "pageSize": 20})[0]
        assert public["results"] == [], "public live Search bypassed exclusions"
        assert observations(self.b, source["id"]) == before_exclusions, "live exclusions changed indexed evidence"
        source = self.change_source(source, {"scope": {"kinds": KINDS}})
        restored = self.b.request("POST", "/api/v1/catalog/search", {"text": "freshpeer", "origins": ["live"], "limit": 20})[0]
        assert {row["identifier"] for row in restored["items"]} == fresh_ids and not restored["partial"], "live exclusion clearing did not recover"
        self.check_no_adoption()
        self.passed("live")
        self.phase("live-paging")
        source = self.check_live_paging(source, fresh_ids)
        self.passed("live-paging")
        self.phase("client-pagination")
        from peer_pagination import qualify
        write_document(self.state / "peer-pagination.json", qualify(self, source, fresh_ids, comparison))
        self.passed("client-pagination")
        self.phase("credentials")
        # Keep the private endpoint published for both negative and positive
        # credential checks, so absence cannot masquerade as access denial.
        self.snapshot(self.publisher_client, PRIVATE_A)
        registry_members(self.private)
        for credential in (None, "peer-other", "peer-wrong-deployment"):
            access = {"url": "https://" + PRIVATE_A + ":8443/.well-known/ard.json", "networkProfile": "peer-catalog"}
            if credential:
                access["credentialBinding"] = credential
            self.b.request("POST", "/api/v1/federation/source-inspections", access, expected=(400,), headers={"Idempotency-Key": key("peer-denied")})
        # Qualify the actual source credential in inspection and worker sync.
        authenticated = self.connect(PRIVATE_A, "peer-reader")
        private_index = self.synchronize(authenticated)
        assert private["resource"]["canonicalIdentifier"] in {x["identifier"] for x in private_index}, "authenticated peer credential not used by worker"
        assert draft["resource"]["canonicalIdentifier"] not in {x["identifier"] for x in private_index}, "authenticated sync imported draft"
        self.passed("credentials")
        self.phase("reciprocal")
        reverse = copy.copy(self)
        reverse.b = self.a
        reverse_source = reverse.connect(PUBLIC_B)
        reverse_index = reverse.synchronize(reverse_source)
        assert {x["identifier"] for x in reverse_index} == self.b_native, "reciprocal sync imported foreign observations"
        assert {x["id"] for x in reverse.synchronize(reverse_source)} == {x["id"] for x in reverse_index}, "reciprocal sync duplicated observations"
        before_native = {x["identifier"]: x for x in registry_members(self.public) if x["type"] != "application/ai-registry+json"}
        self.snapshot(self.publisher_client, PUBLIC_A)
        self.snapshot(self.b, PUBLIC_B)
        assert before_native == {x["identifier"]: x for x in registry_members(self.public) if x["type"] != "application/ai-registry+json"}, "regeneration exported indexed entries"
        self.check_no_adoption()
        reverse_source = reverse.change_source(reverse_source, {"retrievalMode": "hybrid", "live": {"enabled": True, "queryPrivacyAcknowledged": True, "federationMode": "none"},
            "discoveryAssignments": [{"host": PRIVATE_A, "audience": "authenticated"}]})
        before = self.stats()
        reciprocal = HostClient(self.a.base_url, "peer-local-reader", PRIVATE_A).request("POST", "/ard/v1/search", {"query": {"text": "peer-b"}, "federation": "auto", "pageSize": 20})[0]
        assert {x["identifier"] for x in reciprocal["results"]} == self.b_native, "reciprocal live mismatch"
        self.assert_contacts(before, PUBLIC_B, 1)
        before = self.stats()
        reciprocal_back = self.b_private.request("POST", "/ard/v1/search", {"query": {"text": "freshpeer"}, "federation": "auto", "pageSize": 20})[0]
        assert {x["identifier"] for x in reciprocal_back["results"]} == fresh_ids, "reciprocal B live mismatch"
        self.assert_contacts(before, PUBLIC_A, 1)
        self.passed("reciprocal")
        self.phase("adoption")
        self.compose("start", "peer-archives")
        indexed = self.synchronize(source)
        adopted = self.adopt(indexed, items[0]["resource"]["canonicalIdentifier"])
        self.passed("adoption")
        self.phase("automatic")
        source = self.automatic(source, items[0], adopted)
        self.passed("automatic")
        self.phase("pause-resume")
        source = self.pause_resume(source, fresh_ids)
        self.passed("pause-resume")
        self.phase("source-removal")
        self.remove_source(source, adopted)
        self.passed("source-removal")
        self.check_no_adoption()
        self.phase("draft-transfer")
        peer_drafts.qualify(self)
        self.passed("draft-transfer")
        self.phase("complete")
        write_document(self.state / "peer-contacts.json", self.stats())
        self.passed("complete")

    def check_index(self, indexed, entries, ids, source):
        assert len(indexed) == len(ids) and {x["identifier"] for x in indexed} == ids, "peer membership mismatch"
        expected = {entry["identifier"]: entry for entry in entries}
        for item in indexed:
            assert item["sourceId"] == source["id"] and item["freshness"] == "current" and item["version"] == expected[item["identifier"]]["version"], "peer identity or provenance mismatch"
            raw = self.b.request("GET", "/api/v1/observations/" + item["id"] + "/raw")[0]
            assert raw == expected[item["identifier"]], "peer ingestion changed published entry"
        search = self.b.request("POST", "/api/v1/catalog/search", {"tags": ["peers"], "origins": ["indexed"], "limit": 20})[0]
        check_indexed_search(search, indexed, source["id"])

    def plugin_representation(self, name):
        origin = read_document(self.fixture / "archive-origin.json")["origin"]
        entries = read_document(self.fixture / "archives/allowlist.json")["entries"]
        entry = next(x for x in entries if x["path"] == "/" + name + ".zip")
        return {"role": "primary", "mediaType": "application/agent-plugins+zip", "url": origin + entry["path"], "expectedDigest": entry["digest"]}

    def stats(self):
        return HostClient(self.public.base_url, ssl_context=self.public.ssl_context).request("GET", "/peer-stats")[0]

    def page_stats(self):
        counts = HostClient(self.public.base_url, ssl_context=self.public.ssl_context).request("GET", "/peer-pages")[0]
        assert isinstance(counts, dict) and len(counts) <= 36, "peer page counters invalid"
        assert all(re.fullmatch(r"(?:peer-a|private-a|peer-b)\.test:8443 (?:initial|continuation) [1-5][0-9]{2}", name)
                   and type(count) is int and 0 < count <= 10000 for name, count in counts.items()), "unsafe peer page counters"
        return counts

    def continuation_fault(self, enabled, *, initial=False, page_size=None):
        body = {"enabled": enabled, **({"initial": True} if initial else {}), **({"pageSize": page_size} if page_size is not None else {})}
        result = HostClient(self.public.base_url, ssl_context=self.public.ssl_context).request("POST", "/peer-fault", body)[0]
        assert result == body, "peer fault control mismatch"

    def assert_page_contacts(self, before, *, failed=False, continuations=1):
        after = self.page_stats()
        expected = {PUBLIC_A + ":8443 initial 200": 1,
                    PUBLIC_A + ":8443 continuation " + ("503" if failed else "200"): continuations}
        expected = {name: count for name, count in expected.items() if count}
        actual = {name: after.get(name, 0) - before.get(name, 0) for name in before.keys() | after.keys()
                  if after.get(name, 0) != before.get(name, 0)}
        assert actual == expected, "peer paging did not start fresh, continued unexpectedly or missed its fault"

    def assert_contacts(self, before, host, count):
        after = self.stats()
        def total(value, target, mode):
            prefix = target + ":8443 POST /ard/v1/search " + mode + " "
            return sum(number for name, number in value.items() if name.startswith(prefix))
        for target in (PUBLIC_A, PRIVATE_A, PUBLIC_B):
            expected = count if target == host else 0
            assert total(after, target, "none") - total(before, target, "none") == expected, "peer Search contact count mismatch"
            for mode in ("auto", "referrals", "other"):
                assert total(after, target, mode) == total(before, target, mode), "peer query recursed or disclosed unexpected mode"

    def check_combined(self, indexed):
        expected = self.b_native | {x["identifier"] for x in indexed}
        seen, cursors = [], set()
        previous_headers = None
        query = {"origins": ["local", "indexed"], "tags": ["peers"], "limit": 2}
        for page_number in range(1, 13):
            page, headers = peer_pagination.request_page(self.b, "/api/v1/catalog/search", query, page_number, previous_headers=previous_headers)
            assert page["durableTotal"] == len(expected) and not page["partial"], "combined Search total mismatch"
            assert len(page["items"]) <= 2, "combined Search page exceeds limit"
            for item in page["items"]:
                assert item["type"] == ("local" if item["identifier"] in self.b_native else "indexed"), "combined Search provenance mismatch"
            seen.extend(x["identifier"] for x in page["items"])
            cursor = page.get("nextCursor")
            if not cursor:
                assert len(seen) == len(expected) and set(seen) == expected, "combined Search pagination omitted or repeated entries"
                break
            assert cursor not in cursors, "combined Search cursor repeated"
            cursors.add(cursor)
            query["cursor"] = cursor
            previous_headers = headers
        else:
            raise Failure("peer-search-page-limit")
        for kind in KINDS:
            page = self.b.request("POST", "/api/v1/catalog/search", {"origins": ["local", "indexed"], "kinds": [kind], "tags": ["peers"], "limit": 20})[0]
            selected = {x["resource"]["canonicalIdentifier"] for x in self.b_items if x["resource"]["kind"] == kind} | {x["identifier"] for x in indexed if x["kind"] == kind}
            assert {x["identifier"] for x in page["items"]} == selected, "combined Search kind filter mismatch"
        page = self.b.request("POST", "/api/v1/catalog/search", {"origins": ["local", "indexed"], "tags": ["peer-b"], "limit": 20})[0]
        assert {x["identifier"] for x in page["items"]} == self.b_native, "peer-specific tag filter mismatch"

    def check_control_declarations(self, indexed):
        """Check filtered stored pages without changing the existing unfiltered journey."""
        expected = self.b_native | {x["identifier"] for x in indexed}
        query = {"tags": ["peers"], "license": " Apache-2.0 ", "categories": [" INTEGRATION ", "integration"], "limit": 1}
        seen, cursors = [], set()
        previous_headers = None
        for page_number in range(1, 13):
            page, headers = peer_pagination.request_page(self.b, "/api/v1/catalog/search", query, page_number, previous_headers=previous_headers)
            assert page["durableTotal"] == len(expected) and not page["partial"], "declaration-filtered total mismatch"
            assert len(page["items"]) == 1 and page["liveCount"] == 0, "filtered stored page admission mismatch"
            item = page["items"][0]
            assert item["type"] == ("local" if item["identifier"] in self.b_native else "indexed"), "filtered provenance mismatch"
            seen.append(item["identifier"])
            cursor = page.get("nextCursor")
            if not cursor:
                break
            assert cursor not in cursors, "filtered continuation repeated"
            cursors.add(cursor)
            query["cursor"] = cursor
            previous_headers = headers
        else:
            raise Failure("peer-filtered-search-page-limit")
        assert set(seen) == expected and len(seen) == len(expected), "filtered traversal lost or duplicated identities"
        for kind in KINDS:
            request = {"license": "Apache-2.0", "categories": ["INTEGRATION", kind], "origins": ["indexed"], "tags": ["peers"], "limit": 20}
            page = self.b.request("POST", "/api/v1/catalog/search", request)[0]
            selected = {x["identifier"] for x in indexed if x["kind"] == kind}
            assert {x["identifier"] for x in page["items"]} == selected and page["durableTotal"] == len(selected), "indexed category conjunction mismatch"
            absent = self.b.request("POST", "/api/v1/catalog/search", {**request, "license": "apache-2.0"})[0]
            assert absent["items"] == [] and absent["durableTotal"] == 0, "license case was coerced"

    def check_live(self, source, fresh_ids):
        query = {"query": {"@context": {"client.topic": "http://purl.org/dc/terms/"}, "text": "freshpeer", "filter": {"client.topic:subject": "integration", "metadata.license": "Apache-2.0"}}, "federation": "auto", "pageSize": 20}
        before_index = observations(self.b, source["id"])
        before = self.stats()
        live = self.b_private.request("POST", "/ard/v1/search", query)[0]
        assert {x["identifier"] for x in live["results"]} == fresh_ids and len(live["results"]) == len(fresh_ids), "all-kind peer live membership mismatch"
        assert all(x["source"].startswith("https://" + PUBLIC_A + ":8443/") for x in live["results"]), "peer live source mismatch"
        assert all("integration" in x[SUBJECT_IRI] and x["metadata"]["license"] == "Apache-2.0" for x in live["results"]), "live merge lost reviewed declarations"

        self.assert_contacts(before, PUBLIC_A, 1)
        for field, value, kind in (("data.capabilities.streaming", "false", "agent"),
                                   ("data.remotes.supportedProtocolVersions", "2025-11-25", "mcp_server")):
            before = self.stats()
            native = self.b_private.request("POST", "/ard/v1/search", {
                "query": {"@context": {"native.body": "https://agenticresourcediscovery.org/ns#data"},
                          "text": "freshpeer", "filter": {"native.body" + field[4:]: value}}, "federation": "auto", "pageSize": 20})[0]
            expected = {x for x in fresh_ids if x.endswith(":freshpeer-" + kind)}
            assert {x["identifier"] for x in native["results"]} == expected and len(expected) == 1, "live native filters lost a card or invented URL-only facts"
            self.assert_contacts(before, PUBLIC_A, 1)
        assert self.b_public.request("POST", "/ard/v1/search", query)[0]["results"] == [], "private peer exposed anonymously"
        none = self.b_private.request("POST", "/ard/v1/search", {**query, "federation": "none"})[0]
        assert none["results"] == [], "unsynchronized peer became local"
        source = self.change_source(source, {"referrals": {"enabled": True}})
        before = self.stats()
        referrals = self.b_private.request("POST", "/ard/v1/search", {**query, "query": {"text": "peer"}, "federation": "referrals"})[0]
        assert {x["identifier"] for x in referrals["results"]} == self.b_private_native and len(referrals.get("referrals", [])) == 1, "peer referral membership mismatch"
        self.assert_contacts(before, PUBLIC_A, 0)
        source = self.change_source(source, {"referrals": {"enabled": False}, "live": {"enabled": True, "queryPrivacyAcknowledged": True, "federationMode": "none"}})
        # Client pages replay the unchanged peer window until it is exhausted.
        from peer_pagination import walk
        expected = self.b_private_native | fresh_ids | {x["identifier"] for x in before_index}
        walk(self, self.b_private, "/ard/v1/search", {"query": {"text": "peer"}, "federation": "auto", "pageSize": 2}, expected, public=True, tier="core")
        before = self.stats()
        control = self.b.request("POST", "/api/v1/catalog/search", {"text": "freshpeer", "origins": ["local", "indexed", "live"], "limit": 20})[0]
        assert {x["identifier"] for x in control["items"]} == fresh_ids and all(x["type"] == "live" for x in control["items"]), "Control transient live projection mismatch"
        assert control["durableTotal"] == 0 and not control["partial"], "Control live durable count mismatch"
        self.assert_contacts(before, PUBLIC_A, 1)
        for kind in KINDS:
            before = self.stats()
            filtered = self.b.request("POST", "/api/v1/catalog/search", {"text": "freshpeer", "origins": ["live"], "license": "Apache-2.0", "categories": [" INTEGRATION ", kind.upper()], "limit": 20})[0]
            selected = {identifier for identifier in fresh_ids if identifier.endswith(":freshpeer-" + kind)}
            assert {row["identifier"] for row in filtered["items"]} == selected and len(selected) == 1, "live category conjunction lost or invented a kind"
            assert filtered["liveCount"] == 1 and filtered["durableTotal"] == 0 and not filtered["partial"], "filtered live counts or health mismatch"
            self.assert_contacts(before, PUBLIC_A, 1)
        walk(self, self.b, "/api/v1/catalog/search", {"text": "peer", "origins": ["local", "indexed", "live"], "limit": 2}, expected, tier="core")
        live_pages = walk(self, self.b, "/api/v1/catalog/search", {"text": "freshpeer", "origins": ["live"], "limit": 2}, fresh_ids, tier="core")
        assert len(live_pages) == 2 and all(page["durableTotal"] == 0 and page["liveCount"] == 2 and not page["partial"] for page in live_pages), "live-only pages lost counts or completeness"
        before = self.stats()
        diagnostic_body = {"text": "freshpeer", "pageSize": 2, "sourceIds": [source["id"]], "reason": "Qualify bounded upstream continuation diagnostics"}
        diagnostic_headers = {"Idempotency-Key": key("bounded-live-diagnostic")}
        diagnostic = self.b.request("POST", "/api/v1/federation/sources:live-search", diagnostic_body, headers=diagnostic_headers)[0]
        assert diagnostic["partial"] and len(diagnostic["results"]) == 2, "administrator diagnostic discarded the useful bounded peer page"
        assert all(x["identifier"] in fresh_ids and x["sourceId"] == source["id"] and x["source"].startswith("https://" + PUBLIC_A + ":8443/") for x in diagnostic["results"]), "administrator diagnostic lost peer attribution"
        assert any(x["status"] == "continuation_unavailable" and x["sourceId"] == source["id"] for x in diagnostic["diagnostics"]), "administrator diagnostic omitted the continuation warning"
        self.assert_contacts(before, PUBLIC_A, 1)
        before = self.stats()
        replay, replay_headers = self.b.request("POST", "/api/v1/federation/sources:live-search", diagnostic_body, headers=diagnostic_headers)
        assert replay == diagnostic and header(replay_headers, "Idempotency-Replayed") == "true", "administrator diagnostic replay changed"
        self.assert_contacts(before, PUBLIC_A, 0)
        assert observations(self.b, source["id"]) == before_index, "live reads or diagnostics persisted observations"
        return source

    def check_live_paging(self, source, fresh_ids):
        """Qualify scoped later-page admission and recovery through real peers."""
        original_scope = copy.deepcopy(source["scope"])
        self.continuation_fault(False, page_size=1)
        before_index = observations(self.b, source["id"])
        direct_body = {"query": {"text": "freshpeer"}, "federation": "none", "pageSize": 1}
        first = self.public.request("POST", "/ard/v1/search", direct_body)[0]
        assert len(first["results"]) == 1 and first.get("pageToken"), "direct peer control lacks continuation"
        second = self.public.request("POST", "/ard/v1/search", {**direct_body, "pageToken": first["pageToken"]})[0]
        assert len(second["results"]) == 1 and second.get("pageToken"), "direct later-page control missing"
        target = second["results"][0]["identifier"]
        assert target in fresh_ids and first["results"][0]["identifier"] != target, "direct peer target is not a distinct later result"
        third = self.public.request("POST", "/ard/v1/search", {**direct_body, "pageToken": second["pageToken"]})[0]
        assert len(third["results"]) == 1, "direct third-page control missing"
        third_target = third["results"][0]["identifier"]
        assert third_target in fresh_ids and third_target not in {target, first["results"][0]["identifier"]}, "direct third-page target is not distinct"
        try:
            source = self.change_source(source, {"scope": {"identifiers": [target]}})
            ard_body = {**direct_body, "federation": "auto"}
            # Private-network sources cannot participate anonymously. The
            # public ARD protocol is qualified on its exact authenticated host.
            before = self.stats()
            assert self.b_public.request("POST", "/ard/v1/search", ard_body)[0]["results"] == [], "private-network paging leaked anonymously"
            self.assert_contacts(before, PUBLIC_A, 0)
            diagnostic_body = {"text": "freshpeer", "pageSize": 1, "sourceIds": [source["id"]], "reason": "Qualify scoped peer pages"}
            cases = [(self.b_private, "/ard/v1/search", ard_body, "results"),
                     (self.b, "/api/v1/catalog/search", {"text": "freshpeer", "origins": ["live"], "limit": 1}, "items"),
                     (self.b, "/api/v1/federation/sources:live-search", diagnostic_body, "results")]
            for client, path, body, field in cases:
                before, pages = self.stats(), self.page_stats()
                headers = {"Idempotency-Key": key("peer-pages")} if path.endswith(":live-search") else None
                audit_path = "/api/v1/audit-events?action=federation.live_search.diagnostic&limit=100"
                if headers:
                    audits = {x["id"] for x in self.b.request("GET", audit_path)[0]["items"]}
                document = client.request("POST", path, body, headers=headers)[0]
                assert [x["identifier"] for x in document[field]] == [target], "scoped live query omitted its later-page target or admitted an excluded result"
                result = document[field][0]
                if field == "items":
                    assert result["type"] == "live" and document["durableTotal"] == 0 and document["liveCount"] == 1, "later peer page gained durable authority"
                else:
                    assert result["source"].startswith("https://" + PUBLIC_A + ":8443/"), "later peer page lost source attribution"
                # Replay surfaces collect the bounded window, including the
                # third upstream page. The diagnostic stops at its one match.
                requests = 2 if headers else 3
                self.assert_contacts(before, PUBLIC_A, requests)
                self.assert_page_contacts(pages, continuations=requests - 1)
                if headers:
                    after_audits = {x["id"] for x in self.b.request("GET", audit_path)[0]["items"]}
                    assert len(after_audits - audits) == 1, "multi-page diagnostic did not create one audit"
                    before = self.stats()
                    replay, replay_headers = client.request("POST", path, body, headers=headers)
                    assert replay == document and header(replay_headers, "Idempotency-Replayed") == "true", "multi-page diagnostic replay changed"
                    assert {x["id"] for x in self.b.request("GET", audit_path)[0]["items"]} == after_audits, "diagnostic replay duplicated audit"
                    self.assert_contacts(before, PUBLIC_A, 0)

            source = self.change_source(source, {"scope": {"identifiers": [third_target]}})
            before, pages = self.stats(), self.page_stats()
            reached = self.b_private.request("POST", "/ard/v1/search", ard_body)[0]
            assert [x["identifier"] for x in reached["results"]] == [third_target], "configured three-page allowance was not delivered"
            self.assert_contacts(before, PUBLIC_A, 3)
            self.assert_page_contacts(pages, continuations=2)

            # Allow one entry on the first two-result page and another on its
            # continuation. A failed continuation must retain the first entry.
            self.continuation_fault(False, page_size=2)
            direct_body = {"query": {"text": "peer"}, "federation": "none", "pageSize": 2}
            first = self.public.request("POST", "/ard/v1/search", direct_body)[0]
            assert len(first["results"]) == 2 and first.get("pageToken"), "direct outage control lacks continuation"
            second = self.public.request("POST", "/ard/v1/search", {**direct_body, "pageToken": first["pageToken"]})[0]
            assert len(second["results"]) == 2, "direct outage continuation missing"
            retained = first["results"][0]["identifier"]
            later = second["results"][0]["identifier"]
            assert later not in {x["identifier"] for x in first["results"]}, "outage target appeared on the first page"
            source = self.change_source(source, {"scope": {"identifiers": [retained, later]}})
            cases = [(self.b_private, "/ard/v1/search", {**direct_body, "federation": "auto"}, "results"),
                     (self.b, "/api/v1/catalog/search", {"text": "peer", "origins": ["live"], "limit": 2}, "items"),
                     (self.b, "/api/v1/federation/sources:live-search", {"text": "peer", "pageSize": 2, "sourceIds": [source["id"]], "reason": "Qualify later peer outage"}, "results")]
            for client, path, body, field in cases:
                self.continuation_fault(True, page_size=2)
                before, pages = self.stats(), self.page_stats()
                headers = {"Idempotency-Key": key("peer-page-outage")} if path.endswith(":live-search") else None
                document, response_headers = client.request("POST", path, body, headers=headers)
                identifiers = {x["identifier"] for x in document[field]}
                local = self.b_private_native if client is self.b_private else set()
                assert retained in identifiers and later not in identifiers and identifiers <= local | {retained}, "peer outage erased a valid earlier entry or admitted an unavailable result"
                if path == "/ard/v1/search":
                    assert identifiers & local and "partial" in header(response_headers, "Server-Timing"), "later-page outage lost local discovery or its partial signal"
                    assert not any(name in document for name in ("diagnostics", "partial", "sourceIds")), "public discovery exposed private diagnostics"
                else:
                    assert document["partial"] and any(x["status"] == "failed" and x["sourceId"] == source["id"] for x in document["diagnostics"]), "later-page outage omitted its failure diagnostic"
                self.assert_contacts(before, PUBLIC_A, 2)
                self.assert_page_contacts(pages, failed=True)
            # Mixed Control Search separately retains its durable local view.
            local = self.b.request("POST", "/api/v1/catalog/search", {"text": "peer", "origins": ["local"], "limit": 100})[0]
            assert {x["identifier"] for x in local["items"]} == self.b_private_native and local["durableTotal"] == len(self.b_private_native), "local-only Control baseline omitted authorized tenant-visible releases"
            mixed_body = {"text": "peer", "origins": ["local", "live"], "limit": 2}
            mixed = self.b.request("POST", "/api/v1/catalog/search", mixed_body)[0]
            assert mixed["partial"] and mixed["durableTotal"] == local["durableTotal"] and any(x["type"] == "local" for x in mixed["items"]), "later-page outage lost the durable Control view"
            seen = [x["identifier"] for x in mixed["items"] if x["type"] == "local"]
            before, cursors = self.stats(), set()
            assert mixed.get("nextCursor"), "mixed outage did not preserve local continuation"
            while mixed.get("nextCursor"):
                cursor = mixed["nextCursor"]
                assert cursor not in cursors and len(cursors) < 12, "outage continuation repeated a cursor"
                cursors.add(cursor)
                mixed = self.b.request("POST", "/api/v1/catalog/search", {**mixed_body, "cursor": cursor})[0]
                assert mixed["durableTotal"] == local["durableTotal"] and all(x["type"] == "local" for x in mixed["items"]), "outage continuation changed the durable view or repeated live results"
                seen.extend(x["identifier"] for x in mixed["items"])
            assert len(seen) == len(self.b_private_native) and set(seen) == self.b_private_native, "outage continuation lost or repeated local releases"
            self.assert_contacts(before, PUBLIC_A, 0)
            self.continuation_fault(False)
            for client, path, body, field in cases:
                before, pages = self.stats(), self.page_stats()
                headers = {"Idempotency-Key": key("peer-page-recovery")} if path.endswith(":live-search") else None
                document = client.request("POST", path, body, headers=headers)[0]
                assert {retained, later} <= {x["identifier"] for x in document[field]}, "fresh peer query failed recovery"
                if path != "/ard/v1/search":
                    assert not any(x["status"] == "failed" for x in document["diagnostics"]), "fresh peer query retained an earlier failure"
                requests = 2 if headers else 3
                self.assert_contacts(before, PUBLIC_A, requests)
                self.assert_page_contacts(pages, continuations=requests - 1)
        finally:
            try:
                self.continuation_fault(False, page_size=0)
            finally:
                if source["scope"] != original_scope:
                    source = self.change_source(source, {"scope": original_scope})
        assert observations(self.b, source["id"]) == before_index, "multi-page live queries created or changed indexed observations"
        self.check_combined(before_index)
        self.check_no_adoption()
        return source

    def adopt(self, indexed, agent_identifier):
        adopted = []
        for kind in ("agent", "plugin"):
            observation = next(x for x in indexed if x["kind"] == kind and (x["identifier"] == agent_identifier if kind == "agent" else "freshpeer" in x["identifier"]))
            occurrences = self.b.request("GET", "/api/v1/observations/" + observation["id"] + "/occurrences?limit=100")[0]["items"]
            complete = self.b.request("GET", "/api/v1/federation/sources/" + observation["sourceId"])[0]["lastCompleteSnapshotId"]
            occurrence = next(x for x in occurrences if x["snapshotId"] == complete)
            assert occurrence["evidenceComplete"], "adoption occurrence incomplete"
            body = {field: occurrence[field] for field in ("snapshotId", "retrievalDigest", "entryLocation")}
            body.update(namespaceId=self.b_owner.namespace, publisherId=self.b_owner.publisher)
            path = "/api/v1/observations/" + observation["id"] + ":adopt"
            Client(self.b.base_url, "local-reader").request("POST", path, body, expected=(403,), headers={"Idempotency-Key": key("reader-adopt")})
            source = self.b.request("GET", "/api/v1/federation/sources/" + observation["sourceId"])[0]
            original_scope = copy.deepcopy(source["scope"])
            source = self.change_source(source, {"scope": {**original_scope, "excludedIdentifiers": [observation["identifier"]]}})
            self.b.request("POST", path, body, expected=(412,), headers={"Idempotency-Key": key("excluded-adopt")})
            self.check_no_adoption()
            source = self.change_source(source, {"scope": original_scope})
            headers = {"Idempotency-Key": key("peer-adopt")}
            result, _ = self.b.request("POST", path, body, expected=(201,), headers=headers)
            self.expected_resources.add(result["resource"]["id"])
            replay, replay_headers = self.b.request("POST", path, body, expected=(201,), headers=headers)
            assert replay == result and header(replay_headers, "Idempotency-Replayed") == "true", "adoption replay duplicated mutation"
            source = self.change_source(source, {"scope": {**original_scope, "excludedIdentifiers": [observation["identifier"]]}})
            excluded_replay, replay_headers = self.b.request("POST", path, body, expected=(201,), headers=headers)
            assert excluded_replay == result and header(replay_headers, "Idempotency-Replayed") == "true", "source exclusion broke completed local adoption replay"
            assert self.b.request("GET", "/api/v1/resources/" + result["resource"]["id"])[0]["id"] == result["resource"]["id"], "source exclusion removed adopted resource"
            source = self.change_source(source, {"scope": original_scope})
            Client(self.b.base_url, "local-reader").request("POST", path, body, expected=(403,), headers=headers)
            revision = result["versionRevision"]
            assert result["resource"]["canonicalIdentifier"] == observation["identifier"] and revision["adoption"]["snapshotId"] == complete, "adoption provenance mismatch"
            assert result["listingRevision"]["visibility"] == "private" and not result["listing"].get("currentReleaseId"), "adoption implicitly published"
            events = self.b.request("GET", "/api/v1/audit-events?" + urllib.parse.urlencode({"action": "observation.adopted", "targetType": "resource", "targetId": result["resource"]["id"]}))[0]["items"]
            assert len(events) == 1, "adoption replay duplicated audit"
            # Notification authority is private operational evidence, checked in
            # the owned database without mutating it or granting runtime SQL.
            rows = self.compose("exec", "-T", "postgres", "psql", "-U", "catalog", "-d", "catalog", "-Atc",
                "SELECT count(*) FROM outbox_events WHERE tenant_id='" + TENANT + "' AND subject='" + result["resource"]["id"] + "' AND event_type='com.open.agentic.catalog.observation.adopted.v1'", capture=True)
            assert rows.strip() == b"1", "adoption replay duplicated or omitted notification"
            if kind == "plugin":
                representation = revision["representations"][0]
                assert representation["contentDigest"] == self.plugin_representation("fresh-plugin")["expectedDigest"], "adoption changed Plugin archive identity"
                package = representation["plugin"]
                manifest = next(x for x in package["documents"] if x["path"] == "plugin.json")
                assert json.loads(manifest["content"])["name"] == "fresh-plugin" and package["files"], "adoption omitted exact Plugin manifest/inventory"
            retained = self.b.request("GET", "/api/v1/version-revisions/" + revision["id"])[0]["revision"]
            assert retained["contentSetDigest"] == revision["contentSetDigest"] and retained["adoption"]["snapshotId"] == complete, "adoption readback lost retained content or provenance"
            # Capture the durable GET projection before remote updates and
            # compare the same read surface during subsequent synchronization.
            result["versionRevision"] = retained
            result["resource"] = self.b.request("GET", "/api/v1/resources/" + result["resource"]["id"])[0]
            adopted.append(result)
            self.check_no_adoption()
        return adopted

    def automatic(self, source, item, adopted):
        path = "/api/v1/federation/sources/" + source["id"]
        effective = json.loads(self.compose("exec", "-T", "worker", "/usr/local/bin/catalogctl", "config", "validate", "--config", "/config/config.json", "--role", "worker", capture=True))
        assert effective["configuration"]["worker"]["sourceMaintenanceInterval"] == "1m0s", "worker cadence differs from fixture policy"
        source = self.change_source(source, {"updateMode": "automatic"})
        previous = source.get("lastCompleteSnapshotId")
        first = self.wait_automatic(source, previous)
        preview = mutate(self.b, path + "/change-previews", {"intent": "remove"})[0]
        item["name"] = "peer-a-agent-automatic"
        self.publish_item(item)
        self.snapshot(self.publisher_client, PUBLIC_A)
        second = self.wait_automatic(source, first["id"])
        assert second["id"] != first["id"], "automatic source never refreshed"
        current = self.b.request("GET", path)[0]
        assert current["revision"] == source["revision"], "automatic refresh changed source policy"
        self.b.request("POST", path + ":remove", {"changePreviewDigest": preview["digest"], "reason": "Reject stale removal preview"},
            expected=(412,), headers={"Idempotency-Key": key("stale-preview"), "If-Match": '"rev-' + str(current["revision"]) + '"'})
        entry = next(x for x in registry_members(self.public) if x["identifier"] == item["resource"]["canonicalIdentifier"])
        observation = next(x for x in observations(self.b, source["id"]) if x["identifier"] == entry["identifier"])
        assert self.b.request("GET", "/api/v1/observations/" + observation["id"] + "/raw")[0] == entry, "automatic refresh did not converge on exact updated entry"
        self.check_adopted(adopted)
        return current

    def wait_automatic(self, source, previous):
        def read():
            current = self.b.request("GET", "/api/v1/federation/sources/" + source["id"])[0]
            if current.get("lastCompleteSnapshotId") == previous:
                return None
            snapshot = next((x for x in snapshot_items(self.b, source["id"]) if x["id"] == current.get("lastCompleteSnapshotId")), None)
            if not snapshot or snapshot["state"] != "complete" or not snapshot["enumerationComplete"]:
                return None
            audit = self.b.request("GET", "/api/v1/audit-events?" + urllib.parse.urlencode({"action": "source.sync.completed", "targetType": "source_snapshot", "targetId": snapshot["id"]}))[0]["items"]
            assert len(audit) == 1 and audit[0]["details"]["facts"]["trigger"] == "automatic", "refresh was not worker-admitted"
            job = self.b.request("GET", "/api/v1/jobs/" + audit[0]["details"]["facts"]["jobId"])[0]
            if job["state"] != "succeeded":
                return None
            return snapshot
        return self.wait(read, timeout=120)

    def pause_resume(self, source, fresh_ids):
        path = "/api/v1/federation/sources/" + source["id"]
        source = mutate(self.b, path + ":pause", {"reason": "Owned pause"}, etag='"rev-' + str(source["revision"]) + '"')[0]
        # Manual sync is an asynchronous command: acceptance is not execution
        # approval. The worker must refuse a paused revision without fetching
        # the peer or creating a snapshot. Automatic admission stays suppressed.
        before = self.stats()
        snapshot_ids = {x["id"] for x in snapshot_items(self.b, source["id"])}
        rejected = self.b.request("POST", path + ":sync", {}, expected=(202,), headers={"Idempotency-Key": key("paused-sync"), "If-Match": '"rev-' + str(source["revision"]) + '"'})[0]
        job = wait_job(self.b, rejected["job"]["id"], ("failed",))
        assert job["failureClass"] == "invalid_input", "paused manual sync did not fail closed"
        assert {x["id"] for x in snapshot_items(self.b, source["id"])} == snapshot_ids, "paused manual sync created a snapshot"
        self.assert_contacts(before, PUBLIC_A, 0)
        before = self.stats()
        page = self.b_private.request("POST", "/ard/v1/search", {"query": {"text": "freshpeer"}, "federation": "auto", "pageSize": 20})[0]
        assert page["results"] == [], "paused peer participated in live discovery"
        self.assert_contacts(before, PUBLIC_A, 0)
        snapshot_ids = {x["id"] for x in snapshot_items(self.b, source["id"])}
        admission_path = "/api/v1/audit-events?" + urllib.parse.urlencode({"action": "source.sync.requested", "targetType": "federation_source", "targetId": source["id"]})
        admissions = self.b.request("GET", admission_path)[0]["items"]
        until = time.monotonic() + 80
        def still_paused():
            assert {x["id"] for x in snapshot_items(self.b, source["id"])} == snapshot_ids, "paused source created snapshot"
            assert self.b.request("GET", admission_path)[0]["items"] == admissions, "paused source admitted a Job"
            self.compose("exec", "-T", "worker", "/usr/local/bin/catalogctl", "probe", "--url", "http://127.0.0.1:9090/readyz", capture=True)
            return time.monotonic() >= until
        self.wait(still_paused, timeout=100, interval=2)
        source = mutate(self.b, path + ":resume", {"reason": "Owned resume"}, etag='"rev-' + str(source["revision"]) + '"')[0]
        self.wait_automatic(source, source.get("lastCompleteSnapshotId"))
        before = self.stats()
        live = self.b_private.request("POST", "/ard/v1/search", {"query": {"text": "freshpeer"}, "federation": "auto", "pageSize": 20})[0]
        assert {x["identifier"] for x in live["results"]} == fresh_ids, "resumed source failed live recovery"
        self.assert_contacts(before, PUBLIC_A, 1)
        return self.b.request("GET", path)[0]

    def check_adopted(self, adopted):
        for result in adopted:
            revision = self.b.request("GET", "/api/v1/version-revisions/" + result["versionRevision"]["id"])[0]["revision"]
            assert revision == result["versionRevision"], "remote sync overwrote adopted revision"
            assert self.b.request("GET", "/api/v1/resources/" + result["resource"]["id"])[0] == result["resource"], "remote sync changed adopted local Resource"
        self.check_no_adoption()

    def remove_source(self, source, adopted):
        path = "/api/v1/federation/sources/" + source["id"]
        stale = '"rev-' + str(source["revision"]) + '"'
        source = self.change_source(source, {"updateMode": "manual_review"})
        current, headers = self.b.request("GET", path)
        preview = mutate(self.b, path + "/change-previews", {"intent": "remove"})[0]
        body = {"changePreviewDigest": preview["digest"], "reason": "Owned source removal"}
        self.b.request("POST", path + ":remove", body, expected=(412,), headers={"Idempotency-Key": key("stale-etag"), "If-Match": stale})
        evidence = observations(self.b, source["id"])
        retained_raw = {item["id"]: self.b.request("GET", "/api/v1/observations/" + item["id"] + "/raw")[0] for item in evidence}
        retained_snapshots = [self.b.request("GET", "/api/v1/federation/snapshots/" + item["id"])[0] for item in snapshot_items(self.b, source["id"])]
        assert retained_snapshots, "no snapshot evidence exists before removal"
        mutate(self.b, path + ":remove", body, 204, etag=header(headers, "ETag"))
        self.b.request("GET", path, expected=(404,))
        self.other.request("GET", "/api/v1/federation/sources/" + source["id"] + "/snapshots", expected=(404,))
        # The removed parent route is hidden; immutable evidence remains
        # available through its exact snapshot identity, not that parent List.
        self.b.request("GET", path + "/snapshots", expected=(404,))
        for snapshot in retained_snapshots:
            snapshot_path = "/api/v1/federation/snapshots/" + snapshot["id"]
            assert self.b.request("GET", snapshot_path)[0] == snapshot, "source removal deleted or changed retained snapshot evidence"
            self.other.request("GET", snapshot_path, expected=(404,))
        for observation in evidence:
            raw_path = "/api/v1/observations/" + observation["id"] + "/raw"
            assert self.b.request("GET", raw_path)[0] == retained_raw[observation["id"]], "source removal changed retained raw evidence"
            self.other.request("GET", raw_path, expected=(404,))
        page = self.b.request("POST", "/api/v1/catalog/search", {"origins": ["indexed"], "limit": 100})[0]
        assert all(source["id"] not in x["indexed"]["sourceIds"] for x in page["items"]), "removed source still contributed to Search"
        before = self.stats()
        self.b_private.request("POST", "/ard/v1/search", {"query": {"text": "freshpeer"}, "federation": "auto", "pageSize": 20})
        self.assert_contacts(before, PUBLIC_A, 0)
        self.check_adopted(adopted)

    def check_no_adoption(self):
        resources = {x["id"]: x for x in self.b.request("GET", "/api/v1/resources")[0]["items"]}
        assert set(resources) == self.expected_resources, "peer sync implicitly adopted resources"
        assert all(resources.get(identifier) == value for identifier, value in self.baseline_resources.items()), "federation modified B local Resources"
        assert all(self.b.request("GET", "/api/v1/listing-releases/" + value["id"])[0] == value for value in self.baseline_releases), "federation modified B releases"
        manifest = self.b_public.request("GET", "/.well-known/ard.json")[0]
        assert {x["identifier"]: x for x in manifest["entries"] if x["type"] != "application/ai-registry+json"} == self.baseline_entries, "peer sync changed publication or re-exported observations"
        local = self.b_private.request("POST", "/ard/v1/search", {"query": {"text": "peer"}, "federation": "none", "pageSize": 100})[0]
        assert {x["identifier"] for x in local["results"]} == self.b_private_native, "local-only discovery leaked observations or drafts"



def run_observed(scenario):
    """Continue only read-only Search schema failures to investigate later phases.

    The original observer records the failed contract. The invocation still
    fails at the end, with no passed qualification or silently skipped check.
    Mutations, transport errors and semantic assertions remain fail-fast.
    """
    if scenario.env.get("CATALOG_FUNCTIONAL_PEER_INVESTIGATE") != "on":
        scenario.run()
        return
    original = federation_module.observe_response
    failures = []
    def observe(method, path, status, headers, body, expected):
        try:
            return original(method, path, status, headers, body, expected)
        except ClientFailure as error:
            if method != "POST" or path != "/api/v1/catalog/search" or str(error) != "functional response contract mismatch":
                raise
            document = json.loads(body)
            # Fixed contract identity and boolean only: no response contents.
            failures.append({"operation": "searchCatalog", "phase": PHASES[len(scenario.completed)], "status": status,
                "continuationUnavailable": any(x.get("status") == "continuation_unavailable" for x in document.get("diagnostics", []))})
            write_document(scenario.state / "peer-contract-failures.json", failures)
            if len(failures) >= 32:
                raise Failure("peer-contract-failure-limit") from None
    federation_module.observe_response = observe
    try:
        scenario.run()
        if failures:
            raise Failure("peer-contract-failure")
    finally:
        federation_module.observe_response = original


def main():
    if sys.argv[1:] == ["prepare"]:
        prepare(os.environ)
        prepare_archives(os.environ)
    elif not sys.argv[1:]:
        run_observed(Peers(os.environ))
    else:
        raise Failure("invalid-peer-command")


def report_failure(error):
    """Preserve every reviewed HTTP category without printing arbitrary errors."""
    record_scenario_failure("peers", error)
    diagnostic = http_failure_details(error)
    if diagnostic is not None:
        print(f"\nAssertionError: functional federation request returned HTTP {diagnostic['status']} ({diagnostic['category']})", file=sys.stderr)
    print("Two-Catalog journey failed; see the report's lastPhase.", file=sys.stderr)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        report_failure(error)
        sys.exit(1)
