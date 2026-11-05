"""Two finite Catalog journeys, using only the supervisor's disposable API."""
import concurrent.futures
import copy
import datetime
import json
import threading
import time
import urllib.request


def configuration(base, scale=False):
    value = copy.deepcopy(base)
    tenant = "00000000-0000-7000-8000-000000000002"
    for role in ("admin", "contributor", "reviewer", "publisher", "reader"):
        user = copy.deepcopy(value["auth"]["developmentTokens"]["local-" + role])
        user.update(tenantId=tenant, subject="second-" + role)
        value["auth"]["developmentTokens"]["second-" + role] = user
    if scale:
        sparse = copy.deepcopy(value["auth"]["developmentTokens"]["local-reader"])
        sparse.update(subject="scale-sparse", roleTemplates=["service_client"])
        value["auth"]["developmentTokens"]["scale-sparse"] = sparse
    value["discovery"]["hosts"].append({"host": "api", "tenantId": tenant, "mode": "anonymous", "publisherDomain": "catalog.example.com"})
    value["discovery"]["hosts"].append({"host": "127.0.0.2", "tenantId": tenant, "mode": "anonymous", "publisherDomain": "catalog.example.com"})
    return value


class Journey:
    def __init__(self, api, run_id, http, failure):
        self.api, self.run_id, self.http, self.failure = api, run_id, http, failure
        self.lock = threading.Lock()
        self.last_command = "none"
        self.source = None
        self.failure_clock = None
        self.stats = {"attemptedPublications": 0, "committedPublications": 0, "terminalSyncJobs": 0, "idempotentPairs": 0, "commandRequests": 0,
                      "staleRevisionRejections": 0, "staleCursorRejections": 0,
                      "crossTenantDenials": 0, "pollRequests": 0, "verificationRequests": 0}

    def request(self, method, path, body=None, token="local-admin", key=None, etag=None, expected=(200,)):
        if key:
            with self.lock:
                self.stats["commandRequests"] += 1
                self.last_command = key
        headers = {"Authorization": "Bearer " + token}
        if key:
            headers["Idempotency-Key"] = "perf-" + self.run_id + "-" + key
        if etag:
            headers["If-Match"] = etag
        data = None if body is None else json.dumps(body, separators=(",", ":")).encode()
        if data is not None:
            headers["Content-Type"] = "application/json"
        status, returned, raw = self.http.request(urllib.request.Request(self.api + "/api/v1" + path, data=data, headers=headers, method=method))
        if status not in expected:
            raise self.failure("journey-unexpected-status-" + str(status))
        return self.http.decode(raw) if raw else None, returned

    def command(self, path, body, key, etag=None, token="local-admin", status=201):
        document, headers = self.request("POST", path, body, token, key, etag, (status,))
        if status == 202:
            if set(document) != {"job", "existing"} or type(document["existing"]) is not bool:
                raise self.failure("journey-invalid-sync-acceptance")
            document = document["job"]
        return document, headers

    def wait_job(self, job):
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            self.stats["pollRequests"] += 1
            result, _ = self.request("GET", "/jobs/" + job["id"])
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise self.failure("journey-job-deadline")
            if type(result) is not dict or type(result.get("state")) is not str:
                raise self.failure("journey-invalid-job-state")
            state = result["state"]
            if state == "succeeded":
                return result
            if state in {"failed", "cancelled", "dead_letter"}:
                raise self.failure("journey-job-failed")
            if state not in {"queued", "running", "waiting"}:
                raise self.failure("journey-invalid-job-state")
            time.sleep(min(.25, remaining))
        raise self.failure("journey-job-deadline")

    def connect(self, manual=False):
        # Keep catalogctl smoke's loopback-only safety intact. The source's
        # Docker alias is an independently built snapshot of the same tenant.
        snapshot, headers = self.command("/publication/snapshots", {"host": "api"}, "remote-snapshot", token="second-publisher")
        self.command("/publication/snapshots/" + snapshot["id"] + ":activate", {"host": "api", "reason": "Expose owned public source metadata"}, "remote-activate", headers.get("ETag"), "second-publisher", 200)
        inspection, _ = self.command("/federation/source-inspections", {"url": "http://api:8080/.well-known/ard.json", "networkProfile": "compose-catalogs"}, "inspect")
        self.source, _ = self.command("/federation/sources", {"inspectionId": inspection["id"], "displayName": "Owned performance source", "retrievalMode": "indexed", "scope": {"kinds": ["agent", "mcp_server", "skill"]}, "updateMode": "manual_review" if manual else "automatic", "networkProfile": "compose-catalogs", "reason": "Approve invocation-owned public metadata"}, "source")
        job, _ = self.command("/federation/sources/" + self.source["id"] + ":sync", {}, "initial-sync", etag='"rev-1"', status=202)
        self.wait_job(job)
        page, _ = self.request("POST", "/catalog/search", {"limit": 20}, token="local-reader")
        if page["durableTotal"] != 6 or sum(item["type"] == "indexed" for item in page["items"]) != 3:
            raise self.failure("journey-indexed-seed-incomplete")
        item = next(item for item in page["items"] if item["type"] == "local")
        self.namespace = item["local"]["resource"]["namespaceId"]
        self.publisher = item["publisher"]
        self.domain = "perf-" + self.run_id + ".catalog.example.com"
        self.stats = dict.fromkeys(self.stats, 0)  # Setup is not measured work.

    def pair(self, callback):
        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            futures = [pool.submit(callback) for _ in range(2)]
            results = [future.result() for future in futures]
        if results[0][0]["id"] != results[1][0]["id"] or not any(headers.get("Idempotency-Replayed") == "true" for _, headers in results):
            raise self.failure("journey-duplicate-effect")
        self.stats["idempotentPairs"] += 1
        return results[0]

    def activate(self, snapshot, headers, key):
        wall, monotonic = time.time_ns(), time.monotonic_ns()
        try:
            return self.command("/publication/snapshots/" + snapshot["id"] + ":activate", {"host": "127.0.0.1", "reason": "Publish the bounded workload"}, key, headers.get("ETag"), "local-publisher", 200)
        except Exception:
            end_wall, elapsed = time.time_ns(), time.monotonic_ns() - monotonic
            self.failure_clock = {"clockAdjustmentMillis": round((end_wall - wall - elapsed) / 1e6), "candidateAgeAtRequestMillis": None, "candidateAgeAtFailureMillis": None}
            try:
                created = datetime.datetime.fromisoformat(snapshot["createdAt"].replace("Z", "+00:00")).timestamp() * 1000
                self.failure_clock.update(candidateAgeAtRequestMillis=round(wall / 1e6 - created), candidateAgeAtFailureMillis=round(end_wall / 1e6 - created))
            except (AttributeError, KeyError, TypeError, ValueError, OverflowError):
                pass
            raise

    def iteration(self, index):
        self.stats["attemptedPublications"] += 1
        prefix = "mixed-" + str(index)
        before, _ = self.request("POST", "/catalog/search", {"limit": 1}, token="local-reader")
        self.stats["verificationRequests"] += 1
        identifier = "urn:air:" + self.domain + ":performance:skill-" + str(index)
        resource, _ = self.pair(lambda: self.command("/resources", {"namespaceId": self.namespace, "canonicalIdentifier": identifier, "kind": "skill"}, prefix + "-resource"))
        version, _ = self.command("/resources/" + resource["id"] + "/versions", {"version": "1.0.0", "representations": [{"role": "primary", "mediaType": "application/agent-skills+md", "content": "---\nname: performance-skill\ndescription: Bounded mixed workload fixture.\n---\n# Instructions\nRead metadata only.\n"}]}, prefix + "-version")
        listing, listing_headers = self.command("/catalog-listings", {"namespaceId": self.namespace, "resourceId": resource["id"]}, prefix + "-listing")
        body = {"versionRevisionId": version["revision"]["id"], "displayName": "Mixed performance " + str(index), "description": "A bounded governed workload.", "publisherId": self.publisher, "representativeQueries": ["Find the governed performance skill", "Discover bounded mixed workload metadata"], "license": "Apache-2.0", "visibility": "public"}
        _, revision_headers = self.command("/catalog-listings/" + listing["id"] + "/revisions", body, prefix + "-revision", listing_headers.get("ETag"))
        self.request("POST", "/catalog-listings/" + listing["id"] + "/revisions", body, key=prefix + "-stale", etag=listing_headers.get("ETag"), expected=(412,))
        self.stats["staleRevisionRejections"] += 1
        submission, submission_headers = self.command("/catalog-listings/" + listing["id"] + "/submissions", {}, prefix + "-submit")
        self.command("/review-submissions/" + submission["id"] + ":approve", {"reason": "Independent synthetic review"}, prefix + "-approve", submission_headers.get("ETag"), "local-reviewer", 200)
        self.command("/catalog-listings/" + listing["id"] + ":publish", {"submissionId": submission["id"]}, prefix + "-publish", revision_headers.get("ETag"))
        snapshot, headers = self.command("/publication/snapshots", {"host": "127.0.0.1"}, prefix + "-snapshot", token="local-publisher")
        self.activate(snapshot, headers, prefix + "-activate")
        self.request("GET", "/resources/" + resource["id"], token="other-tenant-admin", expected=(404,))
        self.stats["crossTenantDenials"] += 1
        self.request("POST", "/catalog/search", {"limit": 1, "cursor": before["nextCursor"]}, token="local-reader", expected=(400,))
        self.stats["staleCursorRejections"] += 1
        self.stats["verificationRequests"] += 2
        source, headers = self.request("GET", "/federation/sources/" + self.source["id"])
        self.stats["verificationRequests"] += 1
        job, _ = self.pair(lambda: self.command("/federation/sources/" + source["id"] + ":sync", {}, prefix + "-sync", headers.get("ETag"), status=202))
        self.wait_job(job)
        self.stats["terminalSyncJobs"] += 1
        found, _ = self.request("POST", "/catalog/search", {"text": identifier, "limit": 20}, token="local-reader")
        self.stats["verificationRequests"] += 1
        if found["durableTotal"] != 1 or len(found["items"]) != 1 or found["items"][0]["identifier"] != identifier:
            raise self.failure("journey-publication-readback-failed")
        status, _, raw = self.http.request(urllib.request.Request(self.api + "/.well-known/ard.json"))
        manifest = self.http.decode(raw)
        self.stats["verificationRequests"] += 1
        # Three smoke releases plus the Registry self entry precede writes.
        if status != 200 or len(manifest["entries"]) != 5 + index or identifier not in {entry["identifier"] for entry in manifest["entries"]}:
            raise self.failure("journey-public-snapshot-readback-failed")
        self.stats["committedPublications"] += 1

    def mixed(self, stop, seconds, warmup, output):
        began = time.monotonic()
        try:
            # One dependent workflow every 30s in the short case, every 60s
            # in the hour. No generated corpus exceeds 63 local releases.
            interval = 60 if seconds == 3600 else 30
            planned = max(1, seconds // interval)
            output["plannedPublications"] = planned
            if stop.wait(warmup + 2):
                raise self.failure("journey-interrupted")
            for index in range(planned):
                self.iteration(index)
                if stop.wait(max(0, began + warmup + 2 + (index + 1) * interval - time.monotonic())):
                    break
            if self.stats["committedPublications"] != planned:
                raise self.failure("journey-incomplete")
            output.update(self.stats, status="passed", elapsedSeconds=round(time.monotonic() - began, 3))
        except Exception as error:
            # Retain only fixed aggregates, never response/credential details.
            output.update(self.stats, status="failed", failure=str(error) if isinstance(error, self.failure) else "mixed-journey-failed", failedCommand=self.last_command)
            if self.failure_clock is not None:
                output["failureClock"] = self.failure_clock
