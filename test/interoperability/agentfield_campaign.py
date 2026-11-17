"""Real native management and Catalog publication checks in the owned API namespace."""

import hashlib
import json
from pathlib import Path
import socket
import sys
import urllib.request

from agntcy_lab import LocalHTTP, UnexpectedStatus
from ard_registry_campaign import Campaign as Publication
from agentfield_contract import CONTROL, KEY, PATH, PRIVATE, SOURCE, check_dashboard, check_refusal, check_search
from peer_catalog import wait_for

DIRECTORY = Path("/result")


class Campaign(Publication):
    """Reuse existing real governance helpers; native credentials stay on loopback."""

    def __init__(self):
        super().__init__()
        self.native = LocalHTTP({"catalog": "http://127.0.0.1:9010"}, "/ca.pem", KEY)
        self.observer = LocalHTTP({"observer": "http://127.0.0.1:9012"}, "/ca.pem")

    def dashboard(self):
        return self.native.document("catalog", PATH)[0]

    def traffic(self):
        return self.observer.document("observer", "/state")[0]

    def search(self, text, source=SOURCE, size=10):
        body = {"query": {"text": text}, "registries": [source], "federation": "auto", "pageSize": size}
        before = len(self.traffic()["requests"])
        result = self.native.document("catalog", PATH + "/external/search", body)[0]
        observed = self.traffic()["requests"][before:]
        return result, observed

    def import_entry(self, entry, source):
        self.native.document("catalog", PATH + "/imports", {"source_registry": source, "entry": entry})
        imports = self.state.setdefault("imports", [])
        for index, (saved, _) in enumerate(imports):
            if saved["identifier"] == entry["identifier"]:
                imports[index] = [entry, source]
                break
        else:
            imports.append([entry, source])
        return self.saved()

    def saved(self):
        return check_dashboard(self.dashboard(), self.state.get("imports", []))

    def activate(self):
        """Bind real ARD bytes to the immutable snapshot and selected releases."""
        snapshots = []
        for host in ("peer-a.test", "private-a.test"):
            candidate, _ = self.api("/api/v1/publication/snapshots", {"host": host}, token="local-publisher", expected=201)
            self.api("/api/v1/publication/snapshots/" + candidate["id"] + ":activate", {"host": host, "reason": "Owned AgentField qualification"},
                     token="local-publisher", etag=candidate["etag"])
            active, _ = self.api("/api/v1/publication/snapshots/" + candidate["id"] + "?host=" + host)
            assert active["state"] == "active" and active["id"] == candidate["id"] and active["etag"] == candidate["etag"]
            snapshots.append(active)
        with self.http.opener.open(urllib.request.Request("https://peer-a.test:8443/.well-known/ard.json"), timeout=5) as response:
            raw = response.read((2 << 20) + 1)
            assert len(raw) <= 2 << 20 and response.headers.get_content_type() == "application/json"
            document = json.loads(raw)
            assert response.headers.get("ETag") == snapshots[0]["etag"]
        expected = [item for item in self.state["items"] if not item.get("retired")]
        by_id = {entry["identifier"]: entry for entry in document["entries"]}
        ids = {item["resource"]["canonicalIdentifier"] for item in expected}
        assert len(by_id) == len(document["entries"]) == len(ids) + 1, "unexpected publication membership"
        assert len([entry for key, entry in by_id.items() if key not in ids and entry["type"] == "application/ai-registry+json"]) == 1
        for item in expected:
            entry = by_id[item["resource"]["canonicalIdentifier"]]
            assert entry["type"] == "application/mcp-server-card+json" and entry["version"] == "1.0.0"
            assert entry["metadata"]["catalogReleaseId"] == item["release"]["id"], "published entry lost its release binding"
        self.state["publication"] = {"snapshots": snapshots, "sha256": hashlib.sha256(raw).hexdigest(), "document": document}
        return document

    def catalog_stable(self):
        """Re-read retained revisions/releases and active publication after native operations."""
        for item in self.state["items"]:
            release, _ = self.api("/api/v1/listing-releases/" + item["release"]["id"])
            if not item.get("retired"):
                assert release == item["release"], "native operation changed Catalog release"
            else:
                for key in ("id", "versionRevisionId", "listingRevisionId", "activatedBy", "activatedAt"):
                    assert release[key] == item["release"][key], "withdrawal rewrote immutable release evidence"
            revision, _ = self.api("/api/v1/version-revisions/" + item["version"]["revision"]["id"])
            for key in ("id", "contentSetDigest", "representations"):
                assert revision["revision"][key] == item["version"]["revision"][key], "native operation changed retained definition"
        for selected in self.state["publication"]["snapshots"]:
            active, _ = self.api("/api/v1/publication/snapshots/" + selected["id"] + "?host=" + selected["host"])
            assert active == selected and active["state"] == "active", "native operation changed active snapshot"
        with self.http.opener.open(urllib.request.Request("https://peer-a.test:8443/.well-known/ard.json"), timeout=5) as response:
            raw = response.read((2 << 20) + 1)
            assert len(raw) <= 2 << 20
        assert hashlib.sha256(raw).hexdigest() == self.state["publication"]["sha256"], "native operation changed publication bytes"
        return {"releaseCount": len(self.state["items"]), "sealedDefinitionsUnchanged": True,
                "activeSnapshotIds": [row["id"] for row in self.state["publication"]["snapshots"]], "publicationUnchanged": True}

    def seed(self):
        namespace, _ = self.api("/api/v1/namespaces", {"slug": "agentfield-fixture", "displayName": "AgentField fixture"}, expected=201)
        publisher, headers = self.api("/api/v1/publishers", {"displayName": "AgentField fixture", "domains": ["catalog.example.com"]}, expected=201)
        self.api("/api/v1/publishers/" + publisher["id"] + "/publication-domains/catalog.example.com:approve", {"reason": "Owned fixture domain"}, etag=headers["etag"])
        self.state.update(publisher=publisher["id"], items=[])
        for name in ("subject", "control"):
            resource, _ = self.api("/api/v1/resources", {"namespaceId": namespace["id"], "kind": "mcp_server",
                "canonicalIdentifier": "urn:air:catalog.example.com:agentfield-fixture:" + name}, token="local-contributor", expected=201)
            version, _ = self.api("/api/v1/resources/" + resource["id"] + "/versions", {"version": "1.0.0", "representations": [{"role": "primary",
                "mediaType": "application/mcp-server-card+json", "data": {"$schema": "https://static.modelcontextprotocol.io/schemas/v1/server-card.schema.json",
                "name": "com.example/" + name, "title": "AgentField " + name, "description": "Passive AgentField metadata",
                "version": "1.0.0", "remotes": [{"type": "streamable-http", "url": "https://127.0.0.1:8445/never-call",
                "supportedProtocolVersions": ["2025-11-25"]}]}}]}, token="local-contributor", expected=201)
            listing, _ = self.api("/api/v1/catalog-listings", {"namespaceId": namespace["id"], "resourceId": resource["id"]}, token="local-contributor", expected=201)
            item = {"resource": resource, "version": version, "listing": listing}
            self.publish(item, "AgentField " + name)
            self.state["items"].append(item)
        self.activate()

    def run(self, phase):
        if phase == "native-control":
            def ready():
                try:
                    return self.dashboard()
                except (OSError, UnexpectedStatus):
                    return None
            wait_for("native AgentField authenticated JSON routes", ready, timeout=60)
            with socket.create_connection(("127.0.0.1", 8445), timeout=3):
                pass
            wait_for("healthy inert TCP trap", lambda: self.traffic()["trapConnections"] == 1, timeout=5)
            self.observer.document("observer", "/calibrate", {"resetCalibration": True})
            self.native.token = ""
            self.native.document("catalog", PATH, expected=401)
            self.native.token = KEY
            check_dashboard(self.dashboard(), [])
            result, observations = self.search("native control", CONTROL)
            assert len(observations) == 1
            comparison = check_search(observations[0]["response"], result, CONTROL)
            assert comparison["upstreamContinuationPresent"] and "unknownEvidence" in comparison["omittedFields"]
            assert comparison["sourceReplaced"]
            self.import_entry(result["results"][0], CONTROL)
            shapes = {}
            for shape in ("shape-sparse", "shape-empty-data"):
                decoded, wire = self.search(shape, CONTROL)
                assert len(wire) == 1
                check_search(wire[0]["response"], decoded, CONTROL)
                row = decoded["results"][0]
                if shape == "shape-sparse":
                    assert row["type"] == row["displayName"] == "" and "data" not in row
                else:
                    assert row["data"] == {} and "trustManifest" not in row
                    self.import_entry(row, CONTROL)
                shapes[shape] = {"wire": wire, "decoded": decoded}
            before_update = self.saved()
            updated, wire = self.search("control-new-version", CONTROL)
            assert len(wire) == 1
            check_search(wire[0]["response"], updated, CONTROL)
            selected = updated["results"][0]
            assert selected["identifier"] == result["results"][0]["identifier"] and selected["version"] == "2.0.0"
            assert self.saved() == before_update, "Search silently refreshed an existing import"
            after_update = self.import_entry(selected, CONTROL)
            old = next(row for row in before_update if row["identifier"] == selected["identifier"])
            current = next(row for row in after_update if row["identifier"] == selected["identifier"])
            assert len(after_update) == len(before_update) and current["id"] == old["id"]
            assert current["description"] == selected["description"] != old["description"] and "version" not in current
            invalid = {key: value for key, value in selected.items() if key != "identifier"}
            problem, _ = self.native.document("catalog", PATH + "/imports", {"source_registry": CONTROL, "entry": invalid}, expected=400)
            assert "entry.identifier is required" in json.dumps(problem), "invalid import reached the wrong refusal"
            assert self.saved() == after_update, "rejected import changed saved state"
            result = {"search": result, "wire": observations, "comparison": comparison, "shapes": shapes,
                      "reimport": {"wire": wire, "search": updated, "before": before_update, "after": after_update},
                      "rejectedImport": {"status": 400, "problem": problem, "stateUnchanged": True},
                      "importRequests": self.state["imports"], "stored": self.saved(), "managementUnauthenticatedStatus": 401}
        elif phase == "seed":
            self.seed()
            result = {"publication": self.state["publication"], "items": self.state["items"]}
        elif phase == "search":
            paged, observations = self.search("registryfixture", size=1)
            assert len(observations) == 1
            comparison = check_search(observations[0]["response"], paged, SOURCE)
            assert comparison["upstreamContinuationPresent"], "real continuation was not exercised"
            self.state["selectedId"] = self.state["items"][0]["resource"]["canonicalIdentifier"]
            selected, exact = self.search(self.state["selectedId"])
            assert len(exact) == 1
            check_search(exact[0]["response"], selected, SOURCE)
            assert len(selected["results"]) == 1 and selected["results"][0]["identifier"] == self.state["selectedId"]
            self.state["selected"] = selected["results"][0]
            empty, empty_wire = self.search("zznoagentfieldmetadatamatchzz")
            assert empty["results"] == [] and empty["sources"] == [{"url": SOURCE, "status": "ok"}]
            assert len(empty_wire) == 1 and empty_wire[0]["status"] == 200 and empty_wire[0]["response"]["results"] == []
            # Searching cannot persist imports or create bindings.
            self.saved()
            for observation in observations + exact:
                assert observation["body"]["query"]["text"] in ("registryfixture", self.state["selectedId"])
                assert observation["body"]["federation"] == "none" and "registries" not in observation["body"]
                assert not observation["authorizationPresent"], "native management credentials reached discovery"
                assert observation["status"] == 200
            result = {"paged": paged, "selected": selected, "wire": observations + exact, "comparison": comparison,
                      "empty": {"search": empty, "wire": empty_wire}}
        elif phase == "import":
            self.state["persisted"] = self.import_entry(self.state["selected"], SOURCE)
            result = {"stored": self.state["persisted"], "dashboard": self.dashboard()}
        elif phase in ("restart", "tls-recovery"):
            def persisted():
                try:
                    return self.saved()
                except (OSError, UnexpectedStatus):
                    return None
            actual = wait_for("native persisted imports after restart", persisted, timeout=60)
            assert actual == self.state["persisted"], "native restart changed saved imports"
            result = {"stored": actual}
        elif phase == "refusals":
            saved = self.saved()
            denied = []
            for blocked in ("https://127.0.0.1:8445/blocked", SOURCE + "/other", "https://peer-a.test:8444/ard/v10"):
                before = self.traffic()
                refusal, requests = self.search("private sentinel", blocked)
                check_refusal(refusal, blocked, "blocked", "not allowlisted")
                assert not requests and self.traffic()["trapConnections"] == before["trapConnections"], "blocked destination contacted"
                assert self.traffic()["tlsConnections"] == before["tlsConnections"], "blocked destination attempted TLS"
                denied.append(refusal)
            private, observed = self.search(self.state["selectedId"], PRIVATE)
            check_refusal(private, PRIVATE, "error", "401")
            assert len(observed) == 1 and observed[0]["status"] == 401 and not observed[0]["authorizationPresent"]
            self.observer.document("observer", "/fault", {"unavailable": True})
            try:
                outage, observed = self.search(self.state["selectedId"])
                check_refusal(outage, SOURCE, "error", "503")
                assert len(observed) == 1 and observed[0]["status"] == 503
            finally:
                self.observer.document("observer", "/fault", {"unavailable": False})
            recovered, observed = self.search(self.state["selectedId"])
            check_search(observed[0]["response"], recovered, SOURCE)
            assert self.saved() == saved
            result = {"blocked": denied, "private": private, "outage": outage, "recovered": recovered}
        elif phase == "tls-refusal":
            def ready():
                try:
                    return self.dashboard()
                except (OSError, UnexpectedStatus):
                    return None
            wait_for("native untrusted-CA instance", ready, timeout=60)
            before = self.traffic()
            response, observations = self.search(self.state["selectedId"])
            check_refusal(response, SOURCE, "error", "certificate signed by unknown authority")
            after = self.traffic()
            assert after["tlsConnections"] > before["tlsConnections"] and not observations, "TLS control failed before reaching owned TLS"
            assert self.saved() == self.state["persisted"]
            result = {"refusal": response, "tlsConnections": after["tlsConnections"] - before["tlsConnections"], "httpRequests": 0}
        elif phase == "draft":
            item = self.state["items"][0]
            # Published Versions are sealed. Create a new unpublished Version.
            path = "/api/v1/resources/" + item["resource"]["id"] + "/versions"
            original = self.state["selected"]["data"]
            changed = {**original, "title": "Unpublished AgentField draft", "version": "1.1.0"}
            draft, _ = self.api(path, {"version": "1.1.0", "representations": [{"role": "primary", "mediaType": "application/mcp-server-card+json", "data": changed}]},
                                token="local-contributor", expected=201)
            assert draft["revision"]["id"] != item["version"]["revision"]["id"]
            response, observations = self.search(self.state["selectedId"])
            check_search(observations[0]["response"], response, SOURCE)
            assert response["results"] == [self.state["selected"]], "unpublished revision changed discovery"
            assert self.saved() == self.state["persisted"]
            result = {"draft": draft, "search": response}
        elif phase == "withdrawal":
            item = self.state["items"][0]
            release_path = "/api/v1/listing-releases/" + item["release"]["id"]
            original, headers = self.api(release_path, token="local-publisher")
            self.api(release_path + ":retire", {"reason": "Owned AgentField withdrawal"}, token="local-publisher", etag=headers["etag"])
            item["retired"] = True
            self.activate()
            response, observed = self.search(self.state["selectedId"])
            assert response["results"] == [] and response["sources"] == [{"url": SOURCE, "status": "ok"}]
            assert len(observed) == 1 and observed[0]["status"] == 200
            assert self.saved() == self.state["persisted"], "withdrawal silently changed passive import"
            retained, _ = self.api(release_path, token="local-publisher")
            for key in ("id", "versionRevisionId", "listingRevisionId"):
                assert retained[key] == original[key], "withdrawal rewrote release evidence"
            result = {"search": response, "retainedRelease": retained, "publication": self.state["publication"], "stored": self.saved()}
        elif phase == "final-checks":
            traffic = self.traffic()
            assert traffic["trapCalibrated"] and traffic["trapConnections"] == 0, "resource or forbidden destination was contacted"
            assert all(not row["authorizationPresent"] for row in traffic["requests"]), "credential crossed discovery boundary"
            assert self.saved() == self.state["persisted"]
            result = {"traffic": traffic, "dashboard": self.dashboard()}
        else:
            raise ValueError("unknown native phase")
        if self.state.get("items"):
            result["catalogReadback"] = self.catalog_stable()
        self.path.write_text(json.dumps(self.state) + "\n")
        (DIRECTORY / (phase + ".json")).write_text(json.dumps(result, indent=2) + "\n")


if __name__ == "__main__":
    if not __debug__:
        raise RuntimeError("native qualification requires assertions")
    Campaign().run(sys.argv[1])
