"""Closed scale profiles layered on the existing owned performance lifecycle."""
import json
import math
import os
import re
import sys
import threading
import time
import urllib.request

FIXTURE_VERSION = 2

CASES = ("all", "common", "local", "indexed", "rare", "absent", "exact-local", "exact-indexed",
         "kind", "tag", "publisher", "facets", "wide", "later", "terminal", "public")
ORDER_CASES = ("publisher-asc-first", "publisher-asc-third", "publisher-desc-first", "publisher-desc-third")
PLUGIN_CASES = ("plugin", "plugin-local", "plugin-indexed")
# Conservative finite guards; qualification, not these budgets, establishes a
# size's measured behavior. No global PostgreSQL or host settings are changed.
# The 1k pilot used about 32 MiB of database space; retain an eightfold guard
# plus the separate 512 MiB free-space margin. Guards are not capacity promises.
BUDGETS = {1000: (600, 256 << 20), 10000: (1800, 2 << 30), 100000: (7200, 16 << 30)}
# Exact-role indexed page plans contain up to 260 nodes in the 1k fixture.
# Keep complete trees within a finite guard; byte/depth/child bounds also apply.
PLAN_NODE_LIMIT = 320
PLAN_FAILURES = frozenset("scale-plan-" + code for code in (
    "helper-failed", "helper-deadline", "helper-output-limit", "helper-connection", "helper-repository",
    "helper-reader", "helper-policy", "helper-preparation", "helper-arguments", "helper-report-json",
    "helper-report-output-limit", "output-limit", "envelope", "json",
    "inventory", "sample-shape", "sample-key", "sample-timing", "depth-limit", "node-limit",
    "node-shape", "node-statistics", "children-limit"))


def selected_cases(environment, failure):
    """Keep the default inventory unchanged and select one closed experiment."""
    value = environment.get("PERF_SCALE_WORKLOAD", "standard")
    if value == "standard":
        return CASES
    if value == "publisher-sort":
        if items(environment, failure) not in (1000, 10000):
            raise failure("publisher-sort-size-not-selected")
        for key, expected in (("PERF_DURATION_SECONDS", "30"), ("PERF_WARMUP_SECONDS", "5"), ("PERF_WORKERS", "2")):
            if environment.get(key, "") not in ("", expected):
                raise failure("publisher-sort-settings-are-fixed")
        return ("local", "later", *ORDER_CASES)
    raise failure("invalid-scale-workload")


def plan_failure(error):
    """Retain only a fixed failure stage, never child output or parser causes."""
    code = str(error)
    return code if code in PLAN_FAILURES else "scale-plan-unexpected-failure"


def plans_enabled(environment, failure):
    value = environment.get("PERF_SQL_PLANS", "off")
    if value not in ("off", "on"):
        raise failure("invalid-scale-plan-control")
    return value == "on"


def collect_plans(local, environment, directory, port):
    """Run the prebuilt, read-only probe after measurement, before owned cleanup."""
    selected = environment | {"CATALOG_SCALE_PLAN_PORT": str(port)}
    failure = "scale-plan-helper-failed"
    def failed_stdout(raw):
        nonlocal failure
        prefix = b"scale-plan-failure: "
        stages = [line[len(prefix):] for line in raw.splitlines() if line.startswith(prefix)]
        if len(stages) == 1:
            code = "scale-plan-helper-" + stages[0].decode("ascii", errors="replace")
            if code in PLAN_FAILURES:
                failure = code
    try:
        raw = local.safety.command([str(directory / "scale-plans"), "-test.run=^TestPostgresScalePlans$", "-test.timeout=390s", "-test.v"], selected, 400, capture=True, failed_stdout=failed_stdout)
    except (local.Failure, OSError) as error:
        code = {"deadline": "scale-plan-helper-deadline", "child-output-limit": "scale-plan-helper-output-limit"}.get(str(error), failure)
        raise local.Failure(code) from None
    if len(raw) > 512 << 10:
        raise local.Failure("scale-plan-output-limit")
    lines = [line[len(b"scale-plans: "):] for line in raw.splitlines() if line.startswith(b"scale-plans: ")]
    if len(lines) != 1:
        raise local.Failure("scale-plan-envelope")
    try:
        rows = local.safety.decode(lines[0])
    except (local.Failure, ValueError, TypeError):
        raise local.Failure("scale-plan-json") from None
    expected = {(repository, case, mode, statement) for repository in ("local", "indexed")
                for case in (("all", "common", "rare", "absent", "exact-" + repository, "kind") + (("local-first", "local-third") if repository == "local" else ()))
                for mode in ("force_custom_plan", "force_generic_plan") for statement in ("count", "page")}
    expected.update(("local", case, mode, "page") for case in ORDER_CASES
                    for mode in ("force_custom_plan", "force_generic_plan"))
    expected.update(("local", "facets", mode, "facets") for mode in ("force_custom_plan", "force_generic_plan"))
    expected.update((scope, case, mode, "summary") for scope, cases in (
        ("overview", ("resources", "releases", "indexed", "reviews-pending", "reviews-oldest", "reviews-decisions", "sources", "source-problems")),
        ("overview-sparse", ("resources", "reviews-pending"))) for case in cases
        for mode in ("force_custom_plan", "force_generic_plan"))
    if type(rows) is not list or len(rows) != len(expected):
        raise local.Failure("scale-plan-inventory")
    for row in rows:
        if type(row) is not dict or set(row) != {"repository", "case", "mode", "statement", "status", "planningMillis", "executionMillis", "plan"}:
            raise local.Failure("scale-plan-sample-shape")
        key = tuple(row[name] for name in ("repository", "case", "mode", "statement"))
        if any(type(value) is not str for value in key) or key not in expected or row["status"] not in ("measured", "query-failed"):
            raise local.Failure("scale-plan-sample-key")
        expected.remove(key)
        if any(type(row[name]) not in (int, float) or not math.isfinite(row[name]) or not 0 <= row[name] < 1e9 for name in ("planningMillis", "executionMillis")):
            raise local.Failure("scale-plan-sample-timing")
        pending, nodes = [(row["plan"], 0)], 0
        while pending:
            node, depth = pending.pop()
            nodes += 1
            if depth > 32:
                raise local.Failure("scale-plan-depth-limit")
            if nodes > PLAN_NODE_LIMIT:
                raise local.Failure("scale-plan-node-limit")
            if type(node) is not dict or "Node Type" not in node or set(node) - {"Node Type", "Relation Name", "Actual Rows", "Actual Loops", "Shared Hit Blocks", "Shared Read Blocks", "Temp Written Blocks", "Plans"}:
                raise local.Failure("scale-plan-node-shape")
            if any(type(node.get(name, "")) is not str or len(node.get(name, "")) > 128 for name in ("Node Type", "Relation Name")):
                raise local.Failure("scale-plan-node-shape")
            if any(type(node[name]) not in (int, float) or not math.isfinite(node[name]) or not 0 <= node[name] < 1e20 for name in ("Actual Rows", "Actual Loops", "Shared Hit Blocks", "Shared Read Blocks", "Temp Written Blocks") if name in node):
                raise local.Failure("scale-plan-node-statistics")
            # The helper omits zero counters, empty names and empty child lists.
            children = node.get("Plans")
            if children is not None and (type(children) is not list or len(children) > 128):
                raise local.Failure("scale-plan-children-limit")
            pending.extend((child, depth + 1) for child in children or [])
    return {"status": "collected", "scope": "owned-fixture-search-and-overview-after-measurement", "samples": rows}


def container_state(local, environment, identifiers, baseline=None):
    """Reject hidden restarts, OOM kills or replacement during measurement."""
    rows = local.safety.decode(local.command(["docker", "inspect", *identifiers.values()], environment, 15, True))
    if type(rows) is not list or len(rows) != len(identifiers):
        raise local.Failure("container-state-missing")
    result = {}
    for row in rows:
        if type(row) is not dict or type(row.get("State")) is not dict:
            raise local.Failure("container-state-missing")
        role = next((name for name, value in identifiers.items() if row.get("Id") == value), None)
        state = row.get("State", {})
        started = state.get("StartedAt")
        restarts = row.get("RestartCount")
        if role is None or role in result or state.get("Running") is not True or state.get("OOMKilled") is not False or type(restarts) is not int or restarts != 0 or not isinstance(started, str) or not 1 <= len(started) <= 64:
            raise local.Failure("container-restarted-or-stopped")
        result[role] = {"containerId": row["Id"], "startedAt": started, "restarts": restarts, "oomKilled": False}
    if baseline is not None and result != baseline:
        raise local.Failure("container-restarted-or-stopped")
    return result


def items(environment, failure):
    raw = environment.get("PERF_ITEMS", "1000")
    if raw not in ("1000", "10000", "100000"):
        raise failure("invalid-scale-size")
    return int(raw)


def preflight(local, environment, count):
    available = daemon_free_bytes(local, environment)
    if available is not None and available < BUDGETS[count][1] + (512 << 20):
        raise local.Failure("scale-daemon-disk-budget")
    return {"availableBytes": available, "requiredBytes": BUDGETS[count][1] + (512 << 20),
            "capacityCheck": "local-daemon-path" if available is not None else "unavailable"}


def daemon_free_bytes(local, environment):
    root = local.command(["docker", "info", "--format", "{{.DockerRootDir}}"], environment, 15, True).decode().strip()
    available = None
    # A daemon path need not be visible to a client on another machine. Do not
    # substitute free space on the repository filesystem for daemon capacity.
    endpoint = local.command(["docker", "context", "inspect", "default", "--format", "{{.Endpoints.docker.Host}}"], environment, 15, True).decode().strip()
    if endpoint.startswith("unix://") and os.path.isabs(root) and os.path.isdir(root) and environment.get("DOCKER_HOST", "") == "":
        stats = os.statvfs(root)
        available = stats.f_bavail * stats.f_frsize
        if available < 128 << 20:
            raise local.Failure("scale-daemon-disk-budget")
    return available


def request(api, body, http, failure, token="local-reader"):
    status, _, raw = http.request(urllib.request.Request(api + "/api/v1/catalog/search",
        data=json.dumps(body).encode(), headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"}))
    if status != 200:
        category = str(status) if type(status) is int and 100 <= status <= 599 else "unknown"
        try:
            problem = http.decode(raw)
        except http.ClientFailure:
            problem = None
        if type(problem) is dict:
            code = problem.get("code")
            if type(code) is str and code in {"INVALID_ARGUMENT", "UNAUTHENTICATED", "FORBIDDEN", "NOT_FOUND", "CONFLICT", "DEPENDENCY_UNAVAILABLE", "WORK_LIMIT_EXCEEDED", "INTERNAL"}:
                category += "-" + code
                if status == 400 and code == "INVALID_ARGUMENT":
                    if problem.get("detail") == "live search cursor is invalid or expired; restart the search":
                        category += "-invalid-live-cursor"
                    elif problem.get("detail") == "catalog cursor is stale":
                        category += "-stale-generation"
        raise failure("scale-correctness-read-failed:" + category)
    return http.decode(raw)


def prepare(api, count, http, failure):
    page = request(api, {"limit": 20}, http, failure)
    if page.get("durableTotal") != count or len(page.get("items", [])) != 20 or not page.get("nextCursor"):
        raise failure("scale-achieved-size-mismatch")
    other = request(api, {"text": "Scale", "limit": 20}, http, failure, "second-reader")
    if other.get("durableTotal") != 0 or other.get("items") != []:
        raise failure("scale-cross-tenant-leak")
    local = request(api, {"text": "urn:air:scale.example:scale:local-000000", "limit": 1}, http, failure)
    try:
        publisher = local["items"][0]["publisher"]
    except (KeyError, IndexError, TypeError):
        raise failure("scale-local-sentinel-missing") from None
    return {"items": count, "publisher": publisher}


def later_cursor(api, http, failure, terminal=False, order=""):
    if order not in ("", "publisher_asc", "publisher_desc"):
        raise failure("invalid-scale-order")
    cursor, generation, seen = "", None, set()
    limit = 5 if terminal else 20
    for _ in range(1 if terminal else 2):
        query = {"text": "Scale Local", "limit": limit}
        if order:
            query["order"] = order
        if terminal:
            query["tags"] = ["terminal"]
        if cursor:
            query["cursor"] = cursor
        page = request(api, query, http, failure)
        if generation is not None and page.get("generation") != generation:
            raise failure("scale-static-generation-changed")
        generation = page.get("generation")
        identifiers = [item.get("id") for item in page.get("items", [])]
        if len(identifiers) != limit or len(set(identifiers)) != limit or seen.intersection(identifiers):
            raise failure("scale-continuation-overlap")
        seen.update(identifiers)
        cursor = page.get("nextCursor")
        if not isinstance(cursor, str) or not 0 < len(cursor) <= 2048:
            raise failure("scale-continuation-missing")
    return cursor


def seed_command(directory, fixture, count, *, public=False):
    """Run the exact helper beside the invocation-owned PostgreSQL process."""
    if (type(fixture) is not dict or set(fixture) != {"postgres", "image", "project", "nonce"}
            or any(type(value) is not str for value in fixture.values())
            or not re.fullmatch(r"[a-f0-9]{64}", fixture["postgres"])
            or not re.fullmatch(r"sha256:[a-f0-9]{64}", fixture["image"])
            or not re.fullmatch(r"[a-f0-9]{24}", fixture["nonce"])
            or not re.fullmatch(r"open-agentic-catalog-perf-[a-f0-9]{24}", fixture["project"])
            or type(count) is not int or count not in BUDGETS or type(public) is not bool
            or public and count != 1000):
        raise ValueError("invalid-seed-fixture")
    return ["docker", "run", "--rm", "--pull=never", "--name", fixture["project"] + "-seed",
            "--label", "com.docker.compose.project=" + fixture["project"],
            "--label", "com.open.agentic.catalog.performance.owner=" + fixture["nonce"],
            "--network", "container:" + fixture["postgres"], "--read-only", "--cap-drop=ALL",
            "--security-opt", "no-new-privileges", "--pids-limit", "64", "--cpus", "2",
            "--memory", "512m", "--user", str(os.getuid()) + ":" + str(os.getgid()),
            "--env", "GOMAXPROCS=2", "--env", "GOMEMLIMIT=256MiB",
            "--mount", "type=bind,src=" + str(directory / "scale-seed") + ",dst=/seed,readonly",
            "--mount", "type=bind,src=" + str(directory) + ",dst=/evidence",
            "--entrypoint", "/seed", fixture["image"], "5432", str(count), str(BUDGETS[count][0]),
            "/evidence/seed-result.json", *(["public"] if public else [])]


def seed(local, environment, directory, fixture, count, report, observe, abort, *, public=False):
    binary = directory / "scale-seed"
    arguments = seed_command(directory, fixture, count, public=public)
    local.command(["go", "build", "-o", str(binary), "./test/performance/seed"], environment | {"CGO_ENABLED": "0"}, 120)
    setup_seconds = BUDGETS[count][0]
    report["seedSamples"] = []
    stop = threading.Event()
    began = time.monotonic()
    def sample():
        while not stop.is_set():
            try:
                value = observe()
                value["elapsedSeconds"] = round(time.monotonic() - began, 3)
                if len(report["seedSamples"]) >= 80:
                    raise local.Failure("scale-seed-sample-limit")
                report["seedSamples"].append(value)
                local.safety.write_document(directory / "progress.json", {"stage": "scale-seed", "status": "checkpoint-not-a-verdict", "latest": value})
                print("Scale seed elapsed=" + str(value["elapsedSeconds"]) + "s; databaseBytes=" + str(value["database"]["databaseBytes"]), flush=True)
            except (local.Failure, OSError, ValueError, KeyError, TypeError):
                report["seedObservationFailed"] = True
                abort(stop)
                return
            if stop.wait(max(10, math.ceil(setup_seconds / 70))):
                return
    observer = threading.Thread(target=sample, daemon=True)
    observer.start()
    try:
        report["seedExecution"] = {"transport": "owned-postgres-loopback", "imageId": fixture["image"],
                                   "preparationWorkers": 2, "publicationWorkers": 1,
                                   "cpus": 2, "memoryBytes": 512 << 20, "goMemoryLimitBytes": 256 << 20}
        raw = local.command(arguments, environment, setup_seconds + 10, True)
        # The helper emits only fixed numeric progress, never documents/DSNs.
        lines = raw.decode().splitlines()
        if not lines or not lines[-1].startswith("seed complete: "):
            raise local.Failure("scale-seed-report-missing")
        report["seedSummary"] = lines[-1]
    finally:
        pending_failure = sys.exception()
        stop.set()
        observer.join(30)
        report["seedElapsedSeconds"] = round(time.monotonic() - began, 3)
        if (directory / "seed-result.json").is_file():
            try:
                report["seedResult"] = local.safety.read_document(directory / "seed-result.json")
            except (local.Failure, OSError, ValueError):
                report["seedReportStatus"] = "unreadable"
                if pending_failure is None:
                    raise local.Failure("scale-seed-report-invalid") from None
    if observer.is_alive() or report.get("seedObservationFailed"):
        raise local.Failure("scale-seed-observation-incomplete")
    result = report.get("seedResult", {})
    if result.get("status") != "passed" or result.get("stage") != "inventory" or not isinstance(result.get("inventory"), dict) or result["inventory"].get("fixtureVersion") != FIXTURE_VERSION:
        raise local.Failure("scale-seed-report-incomplete")


def overview(api, http, failure, inventory):
    """Check bounded dashboard populations against independent owner setup counts."""
    expected = inventory.get("overviewExpected")
    required = {"resources", "agent", "mcp_server", "skill", "plugin", "published", "namespaces", "publishers", "indexed", "sources", "human", "service", "roles", "assignments", "feedback", "ratings", "sparseResources"}
    if type(expected) is not dict or set(expected) != required or any(type(n) is not int or n < 0 for n in expected.values()) or not inventory.get("sparseNamespace"):
        raise failure("scale-overview-inventory-invalid")
    sizes, values = {}, {}
    for name in ("resources", "releases", "namespaces", "publishers", "indexed", "reviews", "publication", "sources", "jobs", "administration", "feedback", "ratings"):
        status, headers, raw = http.request(urllib.request.Request(api + "/api/v1/overview/" + name,
            headers={"Authorization": "Bearer local-admin"}))
        if status != 200 or "no-store" not in str({key.lower(): value for key, value in headers.items()}.get("cache-control", "")) or not 0 < len(raw) <= 131072:
            raise failure("scale-overview-response-invalid")
        value = http.decode(raw)
        if type(value) is not dict or value.get("tenantId") != "00000000-0000-7000-8000-000000000001" or value.get("scope") != "current_access" or not value.get("observedAt") or value.get("selectionLimit") != (64 if name == "publication" else 10000):
            raise failure("scale-overview-scope-invalid")
        sizes[name], values[name] = len(raw), value
    def count(value, wanted):
        if type(value) is not dict or type(value.get("value")) is not int or type(value.get("limited")) is not bool or value != {"value": min(wanted, 10000), "limited": wanted > 10000}:
            raise failure("scale-overview-count-mismatch")
    count(values["resources"].get("total"), expected["resources"])
    kinds = values["resources"].get("kinds", [])
    if type(kinds) is not list or any(type(row) is not dict or type(row.get("count")) is not int or row["count"] < 0 for row in kinds):
        raise failure("scale-overview-kind-mismatch")
    actual_kinds = {row.get("kind"): row["count"] for row in kinds}
    if len(actual_kinds) != len(kinds):
        raise failure("scale-overview-kind-mismatch")
    if expected["resources"] <= 10000:
        if actual_kinds != {kind: expected[kind] for kind in ("agent", "mcp_server", "skill", "plugin")}:
            raise failure("scale-overview-kind-mismatch")
    elif "kinds" in values["resources"]:
        raise failure("scale-overview-kind-mismatch")
    count(values["releases"].get("published"), expected["published"])
    for name in ("namespaces", "publishers"):
        count(values[name].get("count"), expected[name])
    if expected["indexed"] + 7 > 10000:
        if values["indexed"].get("count") is not None or values["indexed"].get("limited") is not True:
            raise failure("scale-overview-indexed-mismatch")
    elif values["indexed"].get("count") != expected["indexed"] or values["indexed"].get("limited") is not False:
        raise failure("scale-overview-indexed-mismatch")
    count(values["sources"].get("total"), expected["sources"])
    for kind in ("human", "service"):
        count(values["administration"][kind].get("total"), expected[kind])
    count(values["administration"]["activeRoles"].get("total"), expected["roles"])
    count(values["administration"].get("activeAssignments"), expected["assignments"])
    count(values["feedback"].get("open"), expected["feedback"])
    count(values["ratings"].get("total"), expected["ratings"])
    status, _, raw = http.request(urllib.request.Request(api + "/api/v1/me?limit=100", headers={"Authorization": "Bearer scale-sparse"}))
    me = http.decode(raw)
    if status != 200 or not 0 < len(raw) <= 131072 or type(me) is not dict or len(me.get("namespaceGrants", [])) != 100 or not me.get("nextCursor") or any(row.get("namespaceId") == inventory.get("sparseNamespace") for row in me["namespaceGrants"]):
        raise failure("scale-overview-sparse-navigation-invalid")
    sizes["self"] = len(raw)
    status, headers, raw = http.request(urllib.request.Request(api + "/api/v1/overview/resources", headers={"Authorization": "Bearer scale-sparse"}))
    sparse = http.decode(raw)
    if status != 200 or not 0 < len(raw) <= 131072 or "no-store" not in str({key.lower(): value for key, value in headers.items()}.get("cache-control", "")):
        raise failure("scale-overview-sparse-read-failed")
    if type(sparse) is not dict or sparse.get("tenantId") != values["resources"]["tenantId"] or sparse.get("scope") != "current_access" or not sparse.get("observedAt"):
        raise failure("scale-overview-scope-invalid")
    sizes["resources-sparse"] = len(raw)
    count(sparse.get("total"), expected["sparseResources"])
    return {"status": "passed", "scope": "before-timed-workload", "requests": 14,
            "maximumResponseBytes": max(sizes.values()), "encodedBytes": sizes, "expected": expected}


def publisher_access(api, http, failure, count, namespace):
    """Check every sparse page against the unchanged fixture-v2 oracle."""
    expected = ["urn:air:scale.example:scale:local-%06d" % i for i in range(count * 9 // 10 - 3) if i % 5 == 4]
    for order in ("", "publisher_asc", "publisher_desc"):
        query = {"text": "Scale Local", "limit": 20, "origins": ["local"]}
        if order:
            query["order"] = order
        generation = None
        for offset in range(0, len(expected), 20):
            sparse = request(api, query, http, failure, "scale-sparse")
            rows = sparse.get("items", [])
            if (sparse.get("durableTotal") != len(expected) or sparse.get("partial")
                    or [row.get("identifier") for row in rows] != expected[offset:offset + 20]
                    or any(row.get("type") != "local" or row.get("local", {}).get("resource", {}).get("namespaceId") != namespace for row in rows)
                    or not sparse.get("generation") or generation is not None and sparse["generation"] != generation):
                raise failure("scale-publisher-sparse-access")
            generation = sparse["generation"]
            cursor = sparse.get("nextCursor", "")
            if (bool(cursor) != (offset + 20 < len(expected)) or not isinstance(cursor, str) or len(cursor) > 2048):
                raise failure("scale-publisher-sparse-access")
            query["cursor"] = cursor
        query.pop("cursor", None)
        other = request(api, query, http, failure, "second-reader")
        if other.get("durableTotal") != 0 or other.get("items") != [] or other.get("nextCursor"):
            raise failure("scale-publisher-cross-tenant")
    return {"orders": 3, "sparseResults": len(expected), "otherTenantResults": 0}
