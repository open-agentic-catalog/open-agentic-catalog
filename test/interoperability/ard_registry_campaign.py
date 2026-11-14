"""Real governance and native REST assertions, only in the owned API namespace."""

import hashlib
import json
from pathlib import Path
import sys
import urllib.error
import urllib.request

from agntcy_lab import LocalHTTP, UnexpectedStatus
from peer_catalog import wait_for
from ard_registry_contract import SOURCE, EXTENSION, check_readback

DIRECTORY = Path("/result")


class Campaign:
    """Keep Catalog credentials on the fixed control origin and peer reads anonymous."""

    def __init__(self):
        self.http = LocalHTTP({"catalog": "http://127.0.0.1:8080", "peer": "http://127.0.0.1:9010", "untrusted": "http://127.0.0.1:9011", "fixture": "https://127.0.0.1:8443"}, "/ca.pem")
        self.path = DIRECTORY / "state.json"
        self.state = json.loads(self.path.read_text()) if self.path.exists() else {}

    def api(self, path, body=None, *, token="local-admin", expected=200, etag=None):
        """Use fresh native command keys and selected development actors."""
        self.http.token = token
        return self.http.document("catalog", path, body, expected=expected, etag=etag)

    def control(self, **fault):
        """Select only existing invocation-owned source failures."""
        return self.http.document("fixture", "/peer-fault", fault)[0]

    def traffic(self):
        """Read exact source request/status counts, without wall-clock ordering."""
        return self.http.document("fixture", "/peer-stats")[0]

    def publish(self, item, text):
        """Replace a Listing through authoring, independent review and publication."""
        path = "/api/v1/catalog-listings/" + item["listing"]["id"]
        _, headers = self.api(path, token="local-contributor")
        _, headers = self.api(path + "/revisions", {"versionRevisionId": item["version"]["revision"]["id"],
            "displayName": text, "description": "Registry lifecycle " + text, "tags": ["registryfixture"],
            "representativeQueries": ["Find registryfixture metadata", "Find " + text], "publisherId": self.state["publisher"],
            "license": "Apache-2.0", "visibility": "public"}, token="local-contributor", expected=201, etag=headers["etag"])
        listing_etag = headers["etag"]
        submission, _ = self.api(path + "/submissions", {}, token="local-contributor", expected=201)
        _, review_headers = self.api("/api/v1/review-submissions/" + submission["id"], token="local-reviewer")
        self.api("/api/v1/review-submissions/" + submission["id"] + ":approve", {"reason": "Owned independent registry review"}, token="local-reviewer", etag=review_headers["etag"])
        body = {"submissionId": submission["id"]}
        if item.get("release"):
            body.update(priorDisposition="superseded", priorReason="Owned registry Listing update")
        item["release"] = self.api(path + ":publish", body, token="local-publisher", expected=201, etag=listing_etag)[0]["release"]

    def activate(self):
        """Capture the exact active host/audience publication and release identities."""
        candidate, _ = self.api("/api/v1/publication/snapshots", {"host": "peer-a.test"}, token="local-publisher", expected=201)
        self.api("/api/v1/publication/snapshots/" + candidate["id"] + ":activate", {"host": "peer-a.test", "reason": "Owned registry publication"}, token="local-publisher", etag=candidate["etag"])
        # Fixed owned source, using the same CA and unchanged proxy bytes as the native crawler.
        with self.http.opener.open(urllib.request.Request(SOURCE), timeout=5) as response:
            raw = response.read((2 << 20) + 1)
            assert len(raw) <= 2 << 20 and response.headers.get_content_type() == "application/ai-catalog+json"
            document = json.loads(raw)
            etag = response.headers.get("ETag")
        assert document["specVersion"] == "1.0" and etag == candidate["aiCatalogEtag"]
        expected = [item for item in self.state["items"] if not item.get("retired")]
        assert len(document["entries"]) == len(expected)
        by_id = {entry["identifier"]: entry for entry in document["entries"]}
        assert set(by_id) == {item["resource"]["canonicalIdentifier"] for item in expected}
        for item in expected:
            entry = by_id[item["resource"]["canonicalIdentifier"]]
            assert entry["type"] == "application/mcp-server-card+json" and entry["version"] == "1.0.0"
            assert entry["extensions"][EXTENSION]["catalogReleaseId"] == item["release"]["id"]
        self.state["entries"] = document["entries"]
        self.state["publication"] = {"id": candidate["id"], "host": candidate["host"], "audience": candidate["audience"],
            "profile": candidate["aiCatalogProfile"], "etag": etag, "sha256": hashlib.sha256(raw).hexdigest()}
        return document

    def seed(self):
        """Create two governed public MCP Cards, without calling their declared runtimes."""
        namespace, _ = self.api("/api/v1/namespaces", {"slug": "registry-fixture", "displayName": "Registry fixture"}, expected=201)
        publisher, headers = self.api("/api/v1/publishers", {"displayName": "Registry fixture", "domains": ["catalog.example.com"]}, expected=201)
        self.api("/api/v1/publishers/" + publisher["id"] + "/publication-domains/catalog.example.com:approve", {"reason": "Owned fixture domain"}, etag=headers["etag"])
        self.state = {"publisher": publisher["id"], "items": []}
        for name in ("control", "subject"):
            resource, _ = self.api("/api/v1/resources", {"namespaceId": namespace["id"], "kind": "mcp_server", "canonicalIdentifier": "urn:air:catalog.example.com:registry-fixture:" + name}, token="local-contributor", expected=201)
            version, _ = self.api("/api/v1/resources/" + resource["id"] + "/versions", {"version": "1.0.0", "representations": [{"role": "primary", "mediaType": "application/mcp-server-card+json", "data": {
                "$schema": "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json", "name": "com.example/" + name,
                "title": "Registry fixture " + name, "description": "Inert registry metadata", "version": "1.0.0",
                "remotes": [{"type": "streamable-http", "url": "https://mcp.example.com/never-called", "supportedProtocolVersions": ["2025-11-25"]}]}}]}, token="local-contributor", expected=201)
            listing, _ = self.api("/api/v1/catalog-listings", {"namespaceId": namespace["id"], "resourceId": resource["id"]}, token="local-contributor", expected=201)
            item = {"resource": resource, "version": version, "listing": listing}
            self.publish(item, "Registry " + ("control" if name == "control" else "oldsubjectterm"))
            self.state["items"].append(item)
        self.activate()

    def observe(self, failed=False, error_contains=None, after=None):
        """Wait for exact native List/Search/snapshot metadata and a current source status."""
        def current():
            try:
                page, _ = self.http.document("peer", "/api/agents?pageSize=100")
                search, _ = self.http.document("peer", "/api/search", {"query": {"text": "registryfixture"}, "federation": "none", "pageSize": 100})
                snapshot = json.loads(Path("/peer-state/snapshot.json").read_text())
                check_readback(self.state["entries"], page, search, snapshot)
                status, _ = self.http.document("peer", "/api/sources")
                assert status["count"] == 1 and len(status["sources"]) == 1
                source = status["sources"][0]
                assert source["url"] == SOURCE and source["id"] == "owned-oac" and source["entryCount"] == len(self.state["entries"])
                assert source["ok"] is not failed
                if error_contains:
                    assert error_contains in source.get("error", ""), "observed another phase's failed crawl"
                traffic = self.traffic()
                if after:
                    assert traffic.get(after[0], 0) > after[1], "no new source request observed"
                return {"list": page, "search": search, "snapshot": snapshot, "sourceStatus": source, "traffic": traffic}
            except (AssertionError, OSError, ValueError, UnexpectedStatus):
                return None
        return wait_for("native registry exact state", current, timeout=45)

    def run(self, phase):
        """Execute one selected real boundary phase; retain state for the next child."""
        if phase == "seed":
            self.seed()
            result = {"publication": self.state["publication"], "entries": self.state["entries"]}
        elif phase in ("initial", "restart"):
            result = self.observe(failed=phase == "restart")
            if phase == "restart":
                assert result["traffic"].get(self.success_key(), 0) == self.state["outageSuccesses"], "restart fetched fresh data during outage"
                assert result["traffic"].get(self.failure_key(), 0) > self.state["outageFailures"], "no failed refresh after restart"
        elif phase in ("update", "recovery"):
            if phase == "recovery":
                self.control(enabled=False)
            self.publish(self.state["items"][1 if phase == "update" else 0], "Registry " + ("newsubjectterm" if phase == "update" else "recoveredcontrolterm"))
            prior = self.state["entries"][0]
            self.activate()
            result = self.observe()
            if phase == "update":
                assert prior in self.state["entries"], "control changed during subject update"
                old, _ = self.http.document("peer", "/api/search", {"query": {"text": "oldsubjectterm"}, "federation": "none"})
                new, _ = self.http.document("peer", "/api/search", {"query": {"text": "newsubjectterm"}, "federation": "none"})
                assert old["results"] == [] and len(new["results"]) == 1 and new["results"][0]["identifier"] == self.state["items"][1]["resource"]["canonicalIdentifier"]
            result["publication"] = self.state["publication"]
        elif phase == "withdrawal":
            item = self.state["items"][1]
            path = "/api/v1/listing-releases/" + item["release"]["id"]
            _, headers = self.api(path, token="local-publisher")
            self.api(path + ":retire", {"reason": "Owned registry removal"}, token="local-publisher", etag=headers["etag"])
            item["retired"] = True
            control = next(e for e in self.state["entries"] if e["identifier"] == self.state["items"][0]["resource"]["canonicalIdentifier"])
            self.activate()
            assert self.state["entries"] == [control], "withdrawal changed control metadata"
            result = self.observe()
            result["publication"] = self.state["publication"]
        elif phase in ("malformed", "outage"):
            self.control(enabled=True, **({"malformed": True} if phase == "malformed" else {"offline": True}))
            before = self.traffic()
            key = self.success_key() if phase == "malformed" else self.failure_key()
            result = self.observe(failed=True, error_contains="Malformed JSON" if phase == "malformed" else "HTTP 503", after=(key, before.get(key, 0)))
            if phase == "outage":
                self.state["outageSuccesses"] = result["traffic"].get(self.success_key(), 0)
                self.state["outageFailures"] = result["traffic"].get(self.failure_key(), 0)
        elif phase == "tls-refusal":
            def refused():
                try:
                    status, _ = self.http.document("untrusted", "/api/sources")
                    page, _ = self.http.document("untrusted", "/api/agents")
                    assert page["items"] == [] and page["total"] == 0
                    assert status["sources"][0]["ok"] is False and status["sources"][0]["error"] == "fetch failed"
                    return {"list": page, "sourceStatus": status}
                except (AssertionError, OSError):
                    return None
            result = wait_for("native untrusted-CA refusal", refused, timeout=25)
        else:
            raise ValueError("unknown phase")
        self.path.write_text(json.dumps(self.state, indent=2) + "\n")
        (DIRECTORY / (phase + ".json")).write_text(json.dumps(result, indent=2) + "\n")

    @staticmethod
    def success_key():
        return "peer-a.test:8443 GET /.well-known/ai-catalog.json read 200"

    @staticmethod
    def failure_key():
        return "peer-a.test:8443 GET /.well-known/ai-catalog.json read 503"


if __name__ == "__main__":
    if sys.flags.optimize:
        raise RuntimeError("assertions are required")
    Campaign().run(sys.argv[1])
