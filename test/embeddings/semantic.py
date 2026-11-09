"""Qualify automatic hybrid retrieval using only an invocation-owned Compose stack.

No caller URL, database or hosted provider is accepted. The pinned MiniLM cache
must already exist. Cleanup removes only this random project's resources.
"""

import argparse
from collections import Counter
import importlib.util
import json
import os
from pathlib import Path
import re
import secrets
import signal
import socket
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid

import local

ROOT = Path(__file__).resolve().parents[2]
PERF_SPEC = importlib.util.spec_from_file_location("semantic_performance", ROOT / "test/performance/runner.py")
performance = importlib.util.module_from_spec(PERF_SPEC)
PERF_SPEC.loader.exec_module(performance)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    """Never forward fixture credentials away from the assigned origin."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def command(args, environment, timeout=180):
    """Bound commands and withhold arbitrary container output from reports."""
    result = subprocess.run(args, cwd=ROOT, env=environment, capture_output=True, text=True, timeout=timeout, check=False)
    if result.returncode:
        raise RuntimeError(f"{args[:3]} failed: {result.stderr[-2500:]}")
    return result.stdout


def available_port():
    """Reserve a candidate loopback port; Compose fails safely on a bind race."""
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def request(origin, path, body=None, token="local-admin", key="", expected=200):
    """Call only the fixture-assigned loopback API with synthetic credentials."""
    headers = {"Accept": "application/json", "Authorization": "Bearer " + token}
    if body is not None:
        headers["Content-Type"] = "application/json"
    if key:
        headers["Idempotency-Key"] = key
    req = urllib.request.Request(origin + path, data=None if body is None else json.dumps(body).encode(), headers=headers)
    try:
        response = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect()).open(req, timeout=15)
    except urllib.error.HTTPError as error:
        response = error
    with response:
        raw = response.read(16 * 1024 * 1024 + 1)
        if response.status != expected or len(raw) > 16 * 1024 * 1024:
            raise RuntimeError(f"fixture request {path} returned {response.status}, expected {expected}")
        return json.loads(raw), dict(response.headers)


def wait_until(action, predicate, seconds=120, guard=None):
    """Poll bounded observable progress, not fixed startup sleeps."""
    deadline = time.monotonic() + seconds
    last = None
    while time.monotonic() < deadline:
        if guard is not None:
            guard()
        if time.monotonic() >= deadline:
            break
        try:
            last = action()
            if predicate(last) and time.monotonic() < deadline:
                return last
        except (OSError, RuntimeError) as error:
            last = str(error)
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            break
        time.sleep(min(0.25, remaining))
    raise RuntimeError(f"qualification condition did not converge: {last}")


def control_page(page, total, size, tier="hybrid"):
    """Require a complete local page; callers bind identities to their seed."""
    profile = page.get("profile", "")
    if (page.get("tier") != tier or page.get("durableTotal") != total or len(page["items"]) != size
            or page.get("partial") or page.get("partialIndex") or page.get("liveCount") != 0
            or (tier == "hybrid" and (re.fullmatch(r"sha256:[0-9a-f]{64}", profile) is None or page.get("fallbackReason")))
            or (tier == "core" and profile != "core-lexical-v1")):
        raise RuntimeError("invalid semantic Control page")
    identities = {}
    for item in page["items"]:
        if item.get("type") != "local" or not item.get("local") or item["id"] != "local:" + item["local"]["release"]["id"]:
            raise RuntimeError("invalid semantic local result")
        identities[item["identifier"]] = item["id"]
    if len(identities) != size or len(set(identities.values())) != size:
        raise RuntimeError("repeated semantic result")
    return identities


def public_page(page, headers, size, more, tier="hybrid"):
    """Check public transport metadata, membership size and continuation."""
    identities = {item["identifier"] for item in page["results"]}
    if (headers.get("Catalog-Search-Tier") != tier or len(page["results"]) != size or len(identities) != size
            or bool(page.get("pageToken")) != more or "tier" in page
            or headers.get("Catalog-Search-Partial-Index") == "true"):
        raise RuntimeError("invalid semantic public page")
    return identities


def require_facets(facets, field, expected):
    """Compare exact fixture counts independently of Search's reported totals."""
    buckets = facets[field]["buckets"]
    counts = {bucket["value"]: bucket["count"] for bucket in buckets}
    if set(facets) != {field} or len(counts) != len(buckets) or counts != expected or facets[field].get("otherCount", 0) != 0:
        raise RuntimeError("semantic facet counts disagree with fixture")


def scale_traversal(fetch):
    """Visit the seed's entire local population before measured load begins."""
    expected = {f"urn:air:scale.example:scale:local-{i:06d}":
                ("agent", "mcp_server", "skill", "plugin")[i % 4] for i in range(997)}
    query = {"text": "Scale", "tags": ["scale"], "limit": 20}
    seen, result_ids, cursors, kinds = set(), set(), set(), Counter()
    profile, generation = None, None
    for number in range(50):
        page = fetch(dict(query))
        control_page(page, 997, 20 if number < 49 else 17)
        if profile is None:
            profile = page["profile"]
            generation = page.get("generation")
        if (page["profile"] != profile or page.get("generation") != generation
                or not isinstance(generation, str) or re.fullmatch(r"sha256:[0-9a-f]{64}", generation) is None):
            raise RuntimeError("scale traversal changed search profile or generation")
        for item in page["items"]:
            identifier = item["identifier"]
            if (identifier not in expected or identifier in seen or item["id"] in result_ids
                    or item["kind"] != expected[identifier] or "scale" not in item.get("tags", [])):
                raise RuntimeError("scale traversal identity, kind or filter mismatch")
            seen.add(identifier)
            result_ids.add(item["id"])
            kinds[item["kind"]] += 1
        cursor = page.get("nextCursor", "")
        if number < 49:
            if not isinstance(cursor, str) or not cursor or cursor in cursors:
                raise RuntimeError("scale traversal ended early or repeated a cursor")
            cursors.add(cursor)
            query["cursor"] = cursor
        elif cursor:
            raise RuntimeError("scale traversal did not terminate")
    if seen != set(expected) or kinds != {"agent": 250, "mcp_server": 249, "skill": 249, "plugin": 249}:
        raise RuntimeError("scale traversal population mismatch")
    return {"pages": 50, "items": len(seen), "kindCounts": dict(kinds), "profile": profile, "generation": generation}


def fixture_environment():
    """Keep installed browser selection while excluding caller runtime authority."""
    environment = {key: os.environ[key] for key in ("PATH", "HOME", "DOCKER_CONFIG", "XDG_RUNTIME_DIR", "PLAYWRIGHT_BROWSERS_PATH") if key in os.environ}
    environment.update(DOCKER_CONTEXT="default", CATALOG_ENV_FILE="/dev/null", EMBEDDING_PROFILE="minilm-l6-v1")
    return local.inputs(environment)


def qualify(coverage=None, restore_prepare=False, restore=False):
    """Exercise enabled indexing, exact facets, idempotent admin and outage fallback."""
    restore_mode = restore_prepare or restore
    suffix = uuid.uuid4().hex[:16]
    project = "catalog-semantic-" + suffix
    environment = fixture_environment()
    # Refuse missing offline inputs before Compose can partially create resources.
    command(["docker", "image", "inspect", "--format={{.Id}}", environment["CATALOG_EMBEDDING_IMAGE"]], environment)
    if coverage is not None:
        environment["CATALOG_UI_E2E_COVERAGE"] = str(coverage)
    port = available_port()
    database_port = available_port()
    subnet = f"172.29.{secrets.randbelow(256)}.0/24"
    configuration = {"enabled": True, "endpoint": "http://embeddings:80/v1/embeddings", "model": environment["CATALOG_EMBEDDING_MODEL"],
                     "profileRevision": environment["CATALOG_EMBEDDING_REVISION"], "dimensions": 384,
                     "similarityCutoff": 0.2, "queryTimeout": "2s", "batchTimeout": "10s", "batchSize": 8,
                     "allowHttp": True, "allowedAddressPrefixes": [subnet]}
    environment.update(IMAGE="open-agentic-catalog:semantic-" + suffix, VERSION="semantic-test", COMMIT=command(["git", "rev-parse", "HEAD"], environment).strip(),
                       CATALOG_COMPOSE_HTTP_PORT=str(port), CATALOG_COMPOSE_POSTGRES_PORT=str(database_port), CATALOG_COMPOSE_SUBNET=subnet,
                       CATALOG_SEMANTIC_SEARCH=json.dumps(configuration), CATALOG_WEBHOOKS_ENABLED="false")
    compose = ["docker", "compose", "--env-file", "/dev/null", "-f", "compose.yaml", "-f", "compose.embeddings.yaml", "-f", "test/embeddings/semantic.compose.yaml", "-p", project]
    if restore_mode:
        compose[-2:-2] = ["-f", "test/embeddings/semantic_restore.compose.yaml"]
    origin = f"http://127.0.0.1:{port}"
    report = {"profile": "minilm-l6-v1", "commit": environment["COMMIT"], "checks": []}
    report["source"] = performance.source_identity(environment)
    report["model"] = {"name": environment["CATALOG_EMBEDDING_MODEL"], "revision": environment["CATALOG_EMBEDDING_REVISION"], "image": environment["CATALOG_EMBEDDING_IMAGE"], "dimensions": 384}
    report["configuration"] = configuration
    report["tools"] = {"go": command(["go", "version"], environment).strip(), "node": command(["node", "--version"], environment).strip()}
    with tempfile.TemporaryDirectory(prefix=project) as directory:
        binary = str(Path(directory) / "catalogctl")
        command(["go", "build", "-o", binary, "./cmd/catalogctl"], environment)
        if not restore_mode:
            command(["go", "build", "-o", str(Path(directory) / "catalog-ui"), "./catalog-ui/cmd/catalog-ui"], environment)
            load_binary = str(Path(directory) / "performance")
            command(["go", "build", "-o", load_binary, "./test/performance"], environment)
            seed_binary = str(Path(directory) / "scale-seed")
            command(["go", "build", "-o", seed_binary, "./test/performance/seed"], environment)
            plan_binary = str(Path(directory) / "semantic-plans")
            command(["go", "test", "-c", "-tags=integration", "-o", plan_binary, "./internal/platform/database"], environment)
            command(["npm", "--prefix", "catalog-ui", "run", "build", "--", "--outDir", str(Path(directory) / "assets")], environment)
        try:
            command(compose + ["build", "migrate"], environment, timeout=600)
            command(compose + ["up", "-d", "--no-build"], environment, timeout=180)
            report["catalogImage"] = command(["docker", "image", "inspect", "--format={{.Id}}", environment["IMAGE"]], environment).strip()
            model_container = command(compose + ["ps", "--quiet", "embeddings"], environment).strip()
            if command(["docker", "inspect", "--format={{.Config.User}}", model_container], environment).strip() != "10001:10001":
                raise RuntimeError("offline embedding service must run as its explicit non-root identity")
            report["checks"].append("non-root-offline-model")
            wait_until(lambda: request(origin, "/api/v1/capabilities")[0], lambda value: value["search"]["semantic"] == "enabled")
            identifiers = {}
            for role in ("api", "worker", "postgres", "embeddings"):
                identifier = command(compose + ["ps", "--quiet", role], environment).strip()
                identifiers[role] = command(["docker", "inspect", "--format={{.Id}}", identifier], environment).strip()
            baseline = performance.scale.container_state(performance.local, environment, identifiers)
            report["initialContainerState"] = dict(baseline)
            demo = json.loads(command([binary, "seed-demo", "--sample-set", "minimal", "--base-url", origin, "--state-file", str(Path(directory) / "demo.json")], environment))
            published = [item for item in demo["items"] if item["state"] == "published"]
            expected = {item["identifier"]: "local:" + item["releaseId"] for item in published}
            if len(expected) != 3:
                raise RuntimeError("missing published seed identities")
            ready = wait_until(lambda: request(origin, "/api/v1/capabilities")[0], lambda value: value["search"].get("indexing", {}).get("state") == "ready")
            if ready["search"]["indexing"]["indexed"] != 3 or ready["search"]["indexing"]["failed"] != 0:
                raise RuntimeError("published metadata membership mismatch")
            # No manual reindex has been requested: these Jobs came from the worker maintainer.
            page, _ = request(origin, "/api/v1/jobs?limit=100")
            automatic_jobs = [job for job in page["items"] if job["kind"] == "semantic.reindex"]
            if not automatic_jobs or any(job["attemptLimit"] != 7 for job in automatic_jobs):
                raise RuntimeError("automatic semantic admission lost the configured Job limit")
            report["automaticJobs"] = [{"id": job["id"], "attemptLimit": job["attemptLimit"]} for job in automatic_jobs]
            report["checks"].append("automatic-indexing")
            # The fixture documents describe orders. This paraphrase must add a
            # semantic-only match; the cutoff is a test setting, not a quality promise.
            query = {"text": "purchase assistance", "limit": 3}
            result, _ = request(origin, "/api/v1/catalog/search", query)
            paraphrase_total = result["durableTotal"]
            if not 1 <= paraphrase_total <= 3:
                raise RuntimeError("hybrid search did not retrieve fixture metadata")
            paraphrase = control_page(result, paraphrase_total, paraphrase_total)
            if not paraphrase.items() <= expected.items() or result.get("nextCursor"):
                raise RuntimeError("hybrid paraphrase changed fixture membership")
            report["checks"].append("hybrid-search")
            facets, _ = request(origin, "/api/v1/catalog/explore", {"query": {"text": query["text"]}, "facets": [{"field": "kind"}]})
            if facets.get("tier") != "hybrid" or facets["matched"] != paraphrase_total:
                raise RuntimeError("Search/Explore hybrid matching disagrees")
            report["checks"].append("hybrid-facets")
            # Explicit sorting must use lexical membership even with an active
            # model. This paraphrase has no literal words in the seed metadata.
            lexical, _ = request(origin, "/api/v1/catalog/search", {**query, "order": "name_asc"})
            if control_page(lexical, 0, 0, "core") or lexical.get("nextCursor") or lexical.get("fallbackReason"):
                raise RuntimeError("explicit sorting retained semantic-only matches or reported a fallback")
            if restore_mode:
                import semantic_restore
                reader_page, _ = request(origin, "/api/v1/catalog/search", query, token="local-reader")
                reader = control_page(reader_page, paraphrase_total, paraphrase_total)
                if reader != paraphrase:
                    raise RuntimeError("semantic-restore-reader-baseline-mismatch")
                denied = {}
                for label, token in (("grantless", "local-template-service-client"), ("otherTenant", "other-tenant-admin")):
                    page, _ = request(origin, "/api/v1/catalog/search", query, token=token)
                    if page.get("items") != [] or page.get("durableTotal") != 0:
                        raise RuntimeError("semantic-restore-denied-baseline-leaked")
                    denied[label] = 0
                documents = {}
                paths = ["/.well-known/ard.json", "/api/v1/publication/snapshots/" + demo["snapshotId"] + "?host=127.0.0.1"]
                for item in published:
                    paths += ["/api/v1/resources/" + item["resourceId"], "/api/v1/catalog/resources/" + item["releaseId"]]
                for path in paths:
                    value, headers = request(origin, path)
                    etag = next((value for name, value in headers.items() if name.lower() == "etag"), "")
                    if not etag:
                        raise RuntimeError("semantic-restore-baseline-etag-missing")
                    documents[path] = {"document": value, "etag": etag}
                report["restore"] = semantic_restore.prepare(command, compose, environment, directory, {
                    "expected": expected, "hybrid": paraphrase, "lexical": {}, "reader": reader,
                    "denied": denied, "query": query, "profile": result["profile"],
                    "resources": [{key: item[key] for key in ("resourceId", "releaseId", "versionRevisionId", "identifier")} for item in published],
                    "snapshotId": demo["snapshotId"], "documents": documents,
                    "configuration": configuration, "model": report["model"]})
                report["checks"].append("semantic-backup-restore-before-runtime")
                if restore:
                    report["restore"] = semantic_restore.resume(command, compose, environment, report["restore"], origin, request, wait_until, control_page)
                    report["checks"].append("restored-search-permissions-and-supported-reindex")
                local.inputs(environment)
                if report["source"] != performance.source_identity(environment):
                    raise RuntimeError("semantic qualification source changed during execution")
                return report
            ordered_query = {"text": "order", "order": "name_desc", "limit": 1}
            ordered, _ = request(origin, "/api/v1/catalog/search", ordered_query)
            ordered_names = ["Triage order exception Skill", "Order integration MCP Server", "Order exception Agent"]
            ordered_seen = set()
            for position, name in enumerate(ordered_names):
                found = control_page(ordered, 3, 1, "core")
                item = ordered["items"][0]
                cursor = ordered.get("nextCursor", "")
                if (item["displayName"] != name or found[item["identifier"]] != expected.get(item["identifier"])
                        or item["id"] in ordered_seen or bool(cursor) != (position < 2) or ordered.get("fallbackReason")):
                    raise RuntimeError("explicit sorting lost ordered lexical membership or continuation")
                ordered_seen.add(item["id"])
                if cursor:
                    ordered, _ = request(origin, "/api/v1/catalog/search", {**ordered_query, "cursor": cursor})
            if ordered_seen != set(expected.values()):
                raise RuntimeError("explicit sorting lost a seeded release")
            report["checks"].append("explicit-column-order-with-semantic-enabled")
            query = {"text": "order", "limit": 1}
            result, _ = request(origin, "/api/v1/catalog/search", query)
            following, seen, cursors = result, {}, set()
            for position in range(3):
                found = control_page(following, 3, 1)
                cursor = following.get("nextCursor", "")
                if (seen.keys() & found.keys() or following["profile"] != result["profile"]
                        or bool(cursor) != (position < 2) or (cursor and cursor in cursors)):
                    raise RuntimeError("hybrid continuation changed membership, profile or termination")
                seen.update(found)
                cursors.add(cursor)
                if cursor:
                    following, _ = request(origin, "/api/v1/catalog/search", {**query, "cursor": cursor})
            if seen != expected:
                raise RuntimeError("hybrid continuation lost fixture identities")
            facets, _ = request(origin, "/api/v1/catalog/explore", {"query": {"text": "order"}, "facets": [{"field": "kind"}]})
            if facets.get("tier") != "hybrid" or facets["matched"] != 3 or facets["profile"] != result["profile"]:
                raise RuntimeError("hybrid Explore lost fixture population")
            require_facets(facets["facets"], "kind", Counter(item["kind"] for item in published))
            report["checks"].append("continuation")
            for token in ("local-reader",):
                caps, _ = request(origin, "/api/v1/capabilities", token=token)
                if "indexing" in caps["search"]:
                    raise RuntimeError("reader received private indexing counts")
                request(origin, "/api/v1/catalog:reindex", {"reason": "Denied reader rebuild"}, token=token, key="reader-reindex-denied", expected=403)
            body = {"reason": "Qualify explicit rebuild"}
            job, _ = request(origin, "/api/v1/catalog:reindex", body, key="semantic-reindex-first", expected=202)
            replay, _ = request(origin, "/api/v1/catalog:reindex", body, key="semantic-reindex-first", expected=202)
            if replay != job or job["attemptLimit"] != 7:
                raise RuntimeError("reindex replay changed")
            wait_until(lambda: request(origin, "/api/v1/jobs/" + job["id"])[0], lambda value: value["state"] == "succeeded")
            report["checks"].append("admin-reindex-replay-authorization")
            public_job, _ = request(origin, "/api/v1/catalog:reindex", {"reason": "Qualify public snapshot", "snapshotId": demo["snapshotId"]}, key="semantic-public-reindex", expected=202)
            wait_until(lambda: request(origin, "/api/v1/jobs/" + public_job["id"])[0], lambda value: value["state"] == "succeeded")
            # Browser rebuilds finish before capturing the public continuation:
            # a rebuild changes its index generation, independently of outage.
            snapshot_path = "/api/v1/publication/snapshots/" + demo["snapshotId"] + "?host=127.0.0.1"
            snapshot_before, snapshot_headers = request(origin, snapshot_path)
            snapshot_etag = snapshot_headers.get("Etag")
            if not snapshot_etag or snapshot_etag != snapshot_before["etag"]:
                raise RuntimeError("snapshot ETag missing or inconsistent")
            browser_hybrid = json.loads(command(["node", "test/embeddings/browser.mjs", directory, origin, "hybrid", demo["snapshotId"]], environment))
            for browser_job in browser_hybrid["jobs"]:
                completed = wait_until(lambda job_id=browser_job["id"]: request(origin, "/api/v1/jobs/" + job_id)[0], lambda value: value["state"] == "succeeded")
                if completed["id"] != browser_job["id"] or completed["kind"] != "semantic.reindex":
                    raise RuntimeError("browser reindex Job binding mismatch")
            snapshot_after, snapshot_headers = request(origin, snapshot_path)
            if snapshot_before != snapshot_after or snapshot_headers.get("Etag") != snapshot_etag:
                raise RuntimeError("search rebuild changed publication evidence")
            wait_until(lambda: request(origin, "/api/v1/capabilities")[0], lambda value: value["search"].get("indexing", {}).get("state") == "ready")
            report["checks"].append("browser-hybrid-reader-admin-public-reindex-narrow-screen")
            # The seed tag excludes the Registry self-entry without guessing
            # whether the model would rank its description above the cutoff.
            public_query = {"query": {"text": "order", "filter": {"tags": ["catalog-demo-v1"]}}, "pageSize": 1}
            public, headers = request(origin, "/ard/v1/search", public_query)
            public_next, seen, cursors = public, set(), set()
            for position in range(3):
                found = public_page(public_next, headers, 1, position < 2)
                cursor = public_next.get("pageToken", "")
                if seen & found or (cursor and cursor in cursors):
                    raise RuntimeError("public continuation repeated identities or token")
                seen.update(found)
                cursors.add(cursor)
                if cursor:
                    public_next, headers = request(origin, "/ard/v1/search", {**public_query, "pageToken": cursor})
            if seen != set(expected):
                raise RuntimeError("public continuation lost fixture identities")
            public_facets, headers = request(origin, "/ard/v1/explore", {"query": public_query["query"], "resultType": {"facets": [{"field": "type"}]}})
            if headers.get("Catalog-Search-Tier") != "hybrid" or public_facets.get("resultType") != "facets":
                raise RuntimeError("public facet retrieval mismatch")
            require_facets(public_facets["facets"], "type", Counter(entry["type"] for entry in snapshot_before["ard"]["entries"] if entry["identifier"] in expected))
            report["checks"].append("public-search-facets-continuation")
            remote_env = {**environment, "CATALOG_REMOTE_BASE_URL": origin, "CATALOG_REMOTE_TOKEN": "local-admin"}
            remote = json.loads(command([binary, "remote", "search", "--text", query["text"]], remote_env))
            if control_page(remote["data"], 3, 3) != expected or remote["data"].get("nextCursor"):
                raise RuntimeError("remote CLI dropped retrieval metadata")
            report["checks"].append("remote-cli")
            performance.scale.container_state(performance.local, environment, identifiers, baseline)
            command(compose + ["stop", "embeddings"], environment)
            degraded, _ = request(origin, "/api/v1/catalog/search", {**query, "limit": 3})
            if control_page(degraded, 3, 3, "core") != expected or degraded.get("nextCursor") or degraded.get("fallbackReason") != "provider_unavailable":
                raise RuntimeError("provider outage did not report lexical fallback")
            no_semantic, _ = request(origin, "/api/v1/catalog/search", {"text": "purchase assistance", "limit": 3})
            lexical = control_page(no_semantic, no_semantic["durableTotal"], no_semantic["durableTotal"], "core")
            if (not lexical.items() < paraphrase.items() or no_semantic.get("nextCursor")
                    or no_semantic.get("fallbackReason") != "provider_unavailable"):
                raise RuntimeError("fixture did not establish a semantic-only candidate")
            public_core, headers = request(origin, "/ard/v1/search", {**public_query, "pageSize": 3})
            if public_page(public_core, headers, 3, False, "core") != set(expected) or headers.get("Catalog-Search-Fallback") != "provider_unavailable":
                raise RuntimeError("public fallback lost fixture identities")
            request(origin, "/ard/v1/search", {**public_query, "pageToken": public["pageToken"]}, expected=503)
            # Manual rebuilding changed the earlier control generation, so this
            # continuation must restart, not silently re-rank lexically.
            request(origin, "/api/v1/catalog/search", {**query, "cursor": result["nextCursor"]}, expected=400)
            report["checks"].append("provider-outage-fallback")
            browser_core = json.loads(command(["node", "test/embeddings/browser.mjs", directory, origin, "core", demo["snapshotId"]], environment))
            report["checks"].append("browser-lexical-fallback")
            command(compose + ["start", "embeddings"], environment)
            baseline["embeddings"] = performance.scale.container_state(performance.local, environment, {"embeddings": identifiers["embeddings"]})["embeddings"]
            report["recoveredContainerState"] = performance.scale.container_state(performance.local, environment, identifiers, baseline)
            wait_until(lambda: request(origin, "/api/v1/catalog/search", query)[0], lambda value: value.get("tier") == "hybrid")
            recovered, _ = request(origin, "/api/v1/catalog/search", {"text": "purchase assistance", "limit": 3})
            if control_page(recovered, paraphrase_total, paraphrase_total) != paraphrase or recovered.get("nextCursor"):
                raise RuntimeError("provider recovery lost semantic-only identities")
            public_recovered, headers = request(origin, "/ard/v1/search", {**public_query, "pageSize": 3})
            if public_page(public_recovered, headers, 3, False) != set(expected):
                raise RuntimeError("public recovery lost fixture identities")
            if browser_hybrid["version"] != browser_core["version"]:
                raise RuntimeError("semantic browser identity changed")
            report["tools"]["chromium"] = browser_hybrid["version"]
            report["checks"].append("provider-recovery")
            load_profile = Path(directory) / "load-profile.json"
            load_profile.write_text(json.dumps({"scenario": "hybrid", "port": port, "warmupSeconds": 1, "seconds": 10, "workers": 4, "maxRequests": 4000}))
            load_report = Path(directory) / "load-report.json"
            with subprocess.Popen([load_binary, str(load_profile), str(load_report)], cwd=ROOT, env=environment, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL) as process:
                try:
                    for i in range(8):
                        if process.poll() is not None:
                            break
                        request(origin, "/api/v1/catalog:reindex", {"reason": "Concurrent load rebuild"}, key=f"semantic-load-rebuild-{i}", expected=202)
                        time.sleep(0.5)
                    if process.wait(timeout=30) != 0:
                        raise RuntimeError("hybrid load correctness gate failed")
                finally:
                    if process.poll() is None:
                        process.terminate()
                        try:
                            process.wait(timeout=5)
                        except subprocess.TimeoutExpired:
                            process.kill()
                            process.wait()
            report["load"] = json.loads(load_report.read_text())
            performance.scale.container_state(performance.local, environment, identifiers, baseline)
            report["resources"] = performance.resource_sample(identifiers, environment)
            report["checks"].append("bounded-four-reader-concurrent-reindex-load")
            # Reuse governed scale seeding, not SQL inserts or a second import
            # implementation. Only this invocation's loopback DB is accepted.
            command(compose + ["stop", "worker"], environment)
            seed_report = Path(directory) / "scale-seed-report.json"
            started = time.monotonic()
            command([seed_binary, str(database_port), "1000", "600", str(seed_report), "local"], environment, timeout=620)
            report["scaleSeedSeconds"] = time.monotonic() - started
            report["scaleSeed"] = json.loads(seed_report.read_text())
            if report["scaleSeed"].get("status") != "passed" or report["scaleSeed"].get("inventory", {}).get("fixtureVersion") != 2:
                raise RuntimeError("1k shared corpus version mismatch")
            command(compose + ["start", "worker"], environment)
            baseline["worker"] = performance.scale.container_state(performance.local, environment, {"worker": identifiers["worker"]})["worker"]
            started = time.monotonic()
            wait_until(lambda: request(origin, "/api/v1/capabilities")[0], lambda value: value["search"].get("indexing", {}).get("state") == "ready" and value["search"]["indexing"]["indexed"] == 1000 and value["search"]["indexing"]["failed"] == 0, seconds=600, guard=lambda: performance.scale.container_state(performance.local, environment, identifiers, baseline))
            report["scaleIndexSeconds"] = time.monotonic() - started
            report["scaleTraversal"] = scale_traversal(lambda query: request(origin, "/api/v1/catalog/search", query)[0])
            facets, _ = request(origin, "/api/v1/catalog/explore", {"query": {"text": "Scale", "tags": ["scale"]}, "facets": [{"field": "kind"}]})
            if facets["matched"] != 997 or facets.get("tier") != "hybrid":
                raise RuntimeError("1k hybrid filtering, facets or continuation mismatch")
            kinds = {row["value"]: row["count"] for row in facets["facets"]["kind"]["buckets"]}
            if kinds != {"agent": 250, "mcp_server": 249, "skill": 249, "plugin": 249}:
                raise RuntimeError("1k four-kind hybrid population mismatch")
            load_profile.write_text(json.dumps({"scenario": "hybrid", "hybridItems": 1000, "port": port, "warmupSeconds": 1, "seconds": 10, "workers": 4, "maxRequests": 4000}))
            scale_load_report = Path(directory) / "scale-load-report.json"
            command([load_binary, str(load_profile), str(scale_load_report)], environment, timeout=40)
            report["scaleLoad"] = json.loads(scale_load_report.read_text())
            report["scaleResources"] = performance.resource_sample(identifiers, environment)
            report["scaleContainerState"] = performance.scale.container_state(performance.local, environment, identifiers, baseline)
            raw_plans = command([plan_binary, "-test.run=^TestPostgresSemanticPlans$", "-test.timeout=60s", "-test.v"], environment | {"CATALOG_SEMANTIC_PLAN_PORT": str(database_port)}, timeout=70)
            plan_lines = [line.removeprefix("semantic-plans: ") for line in raw_plans.splitlines() if line.startswith("semantic-plans: ")]
            if len(plan_lines) != 1:
                raise RuntimeError("missing semantic SQL plans")
            report["scalePlans"] = json.loads(plan_lines[0])
            report["checks"].append("1k-real-model-index-filter-facet-continuation-load")
            local.inputs(environment)  # Reverify the same read-only model cache.
        finally:
            if restore_mode:
                import semantic_restore
                semantic_restore.cleanup(command, compose, environment)
            else:
                command(compose + ["down", "--volumes", "--remove-orphans"], environment, timeout=120)
                command(["docker", "image", "rm", environment["IMAGE"]], environment)
    if report["source"] != performance.source_identity(environment):
        raise RuntimeError("semantic qualification source changed during execution")
    report["cleanup"] = "complete"
    if coverage is not None:
        execution = {"profile": "semantic", "status": "passed", "passed": 2, "failed": 0, "skipped": 0,
                     "contexts": 2, "browser": browser_hybrid["version"], "cleanup": "passed", "executionUnit": "semantic browser workflow"}
        (coverage / "execution.json").write_text(json.dumps(execution))
        (coverage / "semantic.json").write_text(json.dumps(report))
        command(["node", "test/embeddings/coverage.mjs", str(coverage)], environment)
        report["coverage"] = str(coverage)
    print(json.dumps(report, indent=2), flush=True)


def main():
    """Unwind owned cleanup on interruption without changing the host clock."""
    def interrupted(signum, frame):
        raise KeyboardInterrupt(f"signal {signum}")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--coverage", action="store_true", help="Collect both real browser workflows after successful owned cleanup")
    parser.add_argument("--restore-prepare", action="store_true", help="Qualify a small semantic backup/restore before runtime startup")
    parser.add_argument("--restore", action="store_true", help="Qualify restored semantic search, permissions and supported reindex")
    args = parser.parse_args()
    if sum((args.coverage, args.restore_prepare, args.restore)) > 1:
        parser.error("choose only one coverage or restore mode")
    coverage = None
    if args.coverage:
        parent = ROOT / "output/coverage/e2e/semantic"
        parent.mkdir(parents=True, exist_ok=True)
        coverage = Path(tempfile.mkdtemp(prefix="run-", dir=parent))
    previous = signal.signal(signal.SIGTERM, interrupted)
    try:
        report = qualify(coverage, restore_prepare=args.restore_prepare, restore=args.restore)
        if args.restore_prepare or args.restore:
            report["cleanup"] = "complete"
            print(json.dumps(report, indent=2), flush=True)
    except BaseException:
        if coverage is not None:
            (coverage / "failed.json").write_text(json.dumps({"status": "failed", "profile": "semantic"}))
        raise
    finally:
        signal.signal(signal.SIGTERM, previous)


if __name__ == "__main__":
    main()
