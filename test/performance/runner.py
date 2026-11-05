#!/usr/bin/env python3
"""Own a bounded development search trial and its independent cleanup record."""

import base64
import fcntl
import hashlib
import importlib.util
import math
import os
from pathlib import Path
import re
import secrets
import signal
import socket
import stat
import sys
import tempfile
import threading
import time
import urllib.request
import urllib.parse

ROOT = Path(__file__).resolve().parents[2]
spec_benchmarks = importlib.util.spec_from_file_location("catalog_benchmarks", ROOT / "test/performance/benchmarks.py")
benchmarks = importlib.util.module_from_spec(spec_benchmarks)
spec_benchmarks.loader.exec_module(benchmarks)
spec = importlib.util.spec_from_file_location("catalog_monitoring", ROOT / "test/monitoring/launcher.py")
local = importlib.util.module_from_spec(spec)
spec.loader.exec_module(local)
spec_http = importlib.util.spec_from_file_location("catalog_bounded_http", ROOT / "test/functional/http_client.py")
http = importlib.util.module_from_spec(spec_http)
spec_http.loader.exec_module(http)
http.REQUEST_SECONDS = 4
spec_scenarios = importlib.util.spec_from_file_location("catalog_performance_scenarios", ROOT / "test/performance/scenarios.py")
scenarios = importlib.util.module_from_spec(spec_scenarios)
spec_scenarios.loader.exec_module(scenarios)
spec_scale = importlib.util.spec_from_file_location("catalog_scale", ROOT / "test/performance/scale.py")
scale = importlib.util.module_from_spec(spec_scale)
spec_scale.loader.exec_module(scale)
spec_public = importlib.util.spec_from_file_location("catalog_public_delivery", ROOT / "test/performance/public_delivery.py")
public_delivery = importlib.util.module_from_spec(spec_public)
spec_public.loader.exec_module(public_delivery)
Failure = local.Failure
RUN_ID = re.compile(r"[a-f0-9]{24}\Z")
OWNER_LABEL = "com.open.agentic.catalog.performance.owner"


def address(raw):
    value = raw.decode().strip()
    if not re.fullmatch(r"127\.0\.0\.1:[1-9][0-9]{0,4}", value) or int(value.rsplit(":", 1)[1]) > 65535:
        raise Failure("nonlocal-fixture-address")
    return "http://" + value


def get(url, password=None):
    request = urllib.request.Request(url)
    if password is not None:
        request.add_header("Authorization", "Basic " + base64.b64encode(("catalog:" + password).encode()).decode())
    status, _, body = http.request(request)
    if status != 200:
        raise Failure("unexpected-http-status")
    return body


def wait_http(url, seconds=60):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            value = get(url)
        except (Failure, http.ClientFailure):
            pass
        else:
            if time.monotonic() < deadline:
                return value
            break
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            break
        time.sleep(min(0.25, remaining))
    raise Failure("fixture-readiness-deadline")


def engine_identity(environment):
    value = local.command(["docker", "info", "--format", "{{.ID}}"], environment, 15, True).decode().strip()
    if not value or len(value) > 256:
        raise Failure("missing-engine-identity")
    return value


def source_identity(environment):
    commit = local.command(["git", "rev-parse", "HEAD"], environment, 5, True).decode().strip()
    paths = local.command(["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"], environment, 5, True)
    digest = hashlib.sha256()
    total = 0
    for name in sorted(set(paths.split(b"\0")) - {b""}):
        path = ROOT / os.fsdecode(name)
        if path.is_symlink():
            raise Failure("unsafe-source-input")
        digest.update(len(name).to_bytes(8, "big") + name)
        if not path.exists():
            digest.update(b"deleted")
            continue
        if not path.is_file():
            raise Failure("unsafe-source-input")
        digest.update(b"file" + path.stat().st_size.to_bytes(8, "big"))
        with path.open("rb") as source:
            while block := source.read(65536):
                total += len(block)
                if total > 128 << 20:
                    raise Failure("source-input-limit")
                digest.update(block)
        digest.update(b"\0" + name + b"\0")
    return {"commit": commit, "sourceDigest": "sha256:" + digest.hexdigest()}


def model(nonce, monitored, project):
    labels = {OWNER_LABEL: nonce}
    services = {name: {"labels": labels} for name in ["postgres", "migrate", "provision", "api", "worker"]}
    # Parallel hash queries need shared memory independently of the overall
    # container limit. Keep this explicit instead of inheriting Docker's 64 MiB.
    services["postgres"].update(cpus=1, mem_limit="512m", shm_size="256m")
    services["api"].update(cpus=1, mem_limit="256m")
    services["worker"].update(cpus=0.5, mem_limit="256m", ports=["127.0.0.1:0:9090"])
    volumes = {"catalog-postgres-18": {"labels": labels}}
    networks = {"default": {"labels": labels}}
    if monitored:
        for name in ["prometheus", "grafana"]:
            services[name] = {"labels": labels}
            volumes["monitoring-" + name] = {"labels": labels}
        networks["monitoring"] = {"labels": labels}
    # Reserved by this invocation before Compose, never caller-owned networks.
    networks = {name: {"external": True, "name": project + "_" + name} for name in networks}
    return {"services": services, "volumes": volumes, "networks": networks}


def state_model(state):
    if type(state.get("version")) is not int or state["version"] != 2:
        raise Failure("invalid-ownership-record")
    return model(state["nonce"], state["monitoring"], state["project"])


def reserve_networks(state, environment):
    subnets = {}
    for name in (["default", "monitoring"] if state["monitoring"] else ["default"]):
        network = state["project"] + "_" + name
        for attempt in range(8):
            # Same private /28 fixture space as functional qualification, but
            # Docker reserves the pool before policy generation or Compose up.
            index = secrets.randbelow(4096)
            subnet = f"172.31.{index // 16}.{index % 16 * 16}/28"
            try:
                local.command(["docker", "network", "create", "--driver", "bridge", "--subnet", subnet,
                               "--label", "com.docker.compose.project=" + state["project"],
                               "--label", OWNER_LABEL + "=" + state["nonce"], network], environment, 15, True)
            except Failure as error:
                # A timeout can leave daemon work in flight; do not retry it.
                # Any created owned network is covered by independent cleanup.
                if str(error) != "child-failed":
                    raise
                if attempt == 7:
                    raise Failure("fixture-network-reservation-failed") from None
                continue
            inspected = local.safety.decode(local.command(["docker", "network", "inspect", network], environment, 15, True))
            if len(inspected) != 1:
                raise Failure("fixture-network-inspection-failed")
            info = inspected[0]
            labels = info.get("Labels", {})
            pools = info.get("IPAM", {}).get("Config", [])
            if (info.get("Name") != network or info.get("Driver") != "bridge"
                    or labels.get(OWNER_LABEL) != state["nonce"]
                    or labels.get("com.docker.compose.project") != state["project"]
                    or len(pools) != 1 or pools[0].get("Subnet") != subnet):
                raise Failure("fixture-network-inspection-failed")
            subnets[name] = subnet
            break
    return subnets


def fixture_policy(subnets):
    return {"schemaVersion": 1, "policyId": "performance-owned", "revision": 1,
            "public": {"mode": "allowlist", "origins": []},
            "privateProfiles": [{"name": "compose-catalogs", "origins": ["http://api:8080"],
                                 "addressRanges": list(subnets.values()),
                                 "assignedTenantIds": ["00000000-0000-7000-8000-000000000001"],
                                 "transportRef": "direct"}]}


def owned_resources(state, environment, execute=None):
    execute = execute or local.command
    project = state["project"]
    found = {}
    for kind, list_command in [("container", ["ps", "-aq"]), ("volume", ["volume", "ls", "-q"]), ("network", ["network", "ls", "-q"])]:
        raw = execute(["docker", *list_command, "--filter", "label=com.docker.compose.project=" + project], environment, 15, True)
        ids = raw.decode().splitlines()
        if len(ids) > 20:
            raise Failure("owned-resource-count-limit")
        for identifier in ids:
            inspection = local.safety.decode(execute(["docker", kind, "inspect", identifier], environment, 15, True))[0]
            labels = inspection["Config"].get("Labels", {}) if kind == "container" else inspection.get("Labels", {})
            if labels.get(OWNER_LABEL) != state["nonce"]:
                raise Failure("foreign-resource-ownership")
        found[kind] = ids
    return found


def cleanup(state, directory, environment):
    deadline = time.monotonic() + 90
    def execute(arguments, selected_environment, seconds=15, capture=False):
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise Failure("cleanup-deadline")
        return local.command(arguments, selected_environment, min(seconds, remaining), capture)
    if engine_identity(environment) != state["engine"]:
        raise Failure("engine-identity-changed")
    # A failed parallel Compose create can leave daemon-side work completing
    # after its CLI exits. Require two quiet inventories, not one instant scan.
    quiet = 0
    while quiet < 2:
        found = owned_resources(state, environment, execute)
        if any(found.values()):
            quiet = 0
            # Delete only inspected identities, independently of mutable files.
            if found["container"]:
                execute(["docker", "container", "rm", "--force", *found["container"]], environment, 45)
            for kind in ("network", "volume"):
                if found[kind]:
                    execute(["docker", kind, "rm", *found[kind]], environment, 30)
        else:
            quiet += 1
        if quiet < 2:
            time.sleep(1)
    raw = execute(["docker", "image", "ls", "-q", "--filter", "label=" + OWNER_LABEL + "=" + state["nonce"]], environment, 15, True)
    if raw.strip():
        inspected = local.safety.decode(execute(["docker", "image", "inspect", state["image"]], environment, 15, True))[0]
        if inspected["Config"].get("Labels", {}).get(OWNER_LABEL) != state["nonce"]:
            raise Failure("foreign-image")
        execute(["docker", "image", "rm", state["image"]], environment, 30)


def validate_state(state, run_id):
    required = {"version", "runId", "project", "image", "nonce", "engine", "monitoring"}
    if not isinstance(run_id, str) or not RUN_ID.fullmatch(run_id) or not isinstance(state, dict) or set(state) != required or type(state["version"]) is not int or state["version"] != 2 or state["runId"] != run_id or state["project"] != "open-agentic-catalog-perf-" + run_id or state["image"] != "open-agentic-catalog:perf-" + run_id or not isinstance(state["nonce"], str) or not RUN_ID.fullmatch(state["nonce"]) or not isinstance(state["engine"], str) or not 1 <= len(state["engine"]) <= 256 or type(state["monitoring"]) is not bool:
        raise Failure("invalid-ownership-record")


def metrics_sample(url, worker=False):
    body = get(url + "/metrics")
    if len(body) > 1 << 20:
        raise Failure("metrics-limit")
    selected = {}
    names = {"open_agentic_catalog_http_requests_total", "open_agentic_catalog_http_errors_total", "open_agentic_catalog_go_heap_bytes", "open_agentic_catalog_go_goroutines", "open_agentic_catalog_operational_snapshot_available", "open_agentic_catalog_job_oldest_runnable_age_seconds", "open_agentic_catalog_outbox_oldest_pending_age_seconds"}
    names |= {"open_agentic_catalog_postgresql_pool_acquires_total", "open_agentic_catalog_postgresql_pool_acquire_seconds_total", "open_agentic_catalog_postgresql_pool_acquire_canceled_total", "open_agentic_catalog_postgresql_pool_empty_acquires_total", "open_agentic_catalog_postgresql_pool_empty_acquire_seconds_total", "open_agentic_catalog_go_allocated_bytes_total", "open_agentic_catalog_go_gc_cycles_total"}
    # Only exact application-owned labels enter evidence. Other exposition is
    # ignored, not copied into a support report or turned into dynamic labels.
    labeled = re.compile(r'open_agentic_catalog_(?:postgresql_pool_connections\{state="(?:acquired|idle|maximum|total)"\}|postgresql_(?:operations_total|operation_duration_seconds_total)\{operation="(?:select|with|insert|update|delete|begin|commit|rollback|other)",outcome="(?:succeeded|failed)"\}|http_responses_total\{method="POST",route="/api/v1/catalog/(?:search|explore)",status_class="[1-5]xx"\})\Z')
    maintenance = re.compile(r'open_agentic_catalog_(?:outbox_events\{state="(?:pending|acknowledged)"\}|jobs\{state="(?:queued|running|waiting|succeeded|failed|cancelled|dead_letter)"\}|housekeeping_batches_total\{class="(?:idempotency|source_preview|source_inspection|source_coordination|terminal_job|webhook_delivery|acknowledged_outbox)",outcome="(?:succeeded|failed)"\})\Z')
    for line in body.decode().splitlines():
        parts = line.split()
        if parts and (parts[0] in names or labeled.fullmatch(parts[0]) or maintenance.fullmatch(parts[0])):
            if len(parts) != 2 or parts[0] in selected:
                raise Failure("invalid-metric-shape")
            value = float(parts[1])
            if not 0 <= value < 1e20:
                raise Failure("invalid-metric-value")
            selected[parts[0]] = value
    required = {"open_agentic_catalog_go_heap_bytes", "open_agentic_catalog_go_goroutines"}
    if worker:
        required |= {"open_agentic_catalog_operational_snapshot_available", "open_agentic_catalog_job_oldest_runnable_age_seconds", "open_agentic_catalog_outbox_oldest_pending_age_seconds"}
    if not required <= selected.keys():
        raise Failure("required-metrics-missing")
    if selected.get("open_agentic_catalog_operational_snapshot_available", 1) != 1:
        raise Failure("worker-snapshot-unavailable")
    return selected


def diagnostic_sample(api, worker):
    sample = {"atUnixMillis": time.time_ns() // 1000000, "api": metrics_sample(api), "worker": metrics_sample(worker, worker=True)}
    for role in ("api", "worker"):
        required = {"open_agentic_catalog_postgresql_pool_acquires_total", "open_agentic_catalog_postgresql_pool_empty_acquire_seconds_total", 'open_agentic_catalog_postgresql_pool_connections{state="maximum"}'}
        if not required <= sample[role].keys():
            raise Failure("required-pool-metrics-missing")
    if not any(key.startswith('open_agentic_catalog_postgresql_operations_total{') for key in sample["api"]):
        raise Failure("required-sql-metrics-missing")
    if not any(key.startswith('open_agentic_catalog_http_responses_total{') for key in sample["api"]):
        raise Failure("required-search-metrics-missing")
    return sample


def byte_quantity(raw):
    match = re.fullmatch(r"([0-9]+(?:\.[0-9]+)?)(B|kB|MB|GB|KiB|MiB|GiB)", raw.strip())
    if not match:
        raise Failure("invalid-resource-sample")
    units = {"B": 1, "kB": 1000, "MB": 1000**2, "GB": 1000**3, "KiB": 1024, "MiB": 1024**2, "GiB": 1024**3}
    result = float(match[1]) * units[match[2]]
    if not math.isfinite(result) or result > 1 << 50:
        raise Failure("invalid-resource-sample")
    return round(result)


def resource_sample(identifiers, environment):
    if not 3 <= len(identifiers) <= 5 or len(set(identifiers.values())) != len(identifiers) or any(not re.fullmatch(r"[a-f0-9]{64}", value) for value in identifiers.values()):
        raise Failure("invalid-owned-resource-ids")
    raw = local.command(["docker", "stats", "--no-stream", "--no-trunc", "--format", "{{json .}}", *identifiers.values()], environment, 15, True)
    result = {}
    for line in raw.splitlines():
        row = local.safety.decode(line)
        if type(row) is not dict or any(type(row.get(key)) is not str for key in ("ID", "CPUPerc", "MemUsage", "PIDs")):
            raise Failure("invalid-resource-sample")
        role = next((name for name, identifier in identifiers.items() if row.get("ID") == identifier), None)
        if role is None or role in result:
            raise Failure("unexpected-resource-sample")
        cpu = row.get("CPUPerc", "")
        if not re.fullmatch(r"[0-9]+(?:\.[0-9]+)?%", cpu):
            raise Failure("invalid-resource-sample")
        memory = row.get("MemUsage", "").split(" / ")
        if len(memory) != 2 or not re.fullmatch(r"[0-9]{1,7}", row.get("PIDs", "")):
            raise Failure("invalid-resource-sample")
        result[role] = {"cpuPercent": float(cpu[:-1]), "workingSetBytes": byte_quantity(memory[0]), "memoryLimitBytes": byte_quantity(memory[1]), "pids": int(row["PIDs"])}
        if not 0 <= result[role]["cpuPercent"] <= 100000:
            raise Failure("invalid-resource-sample")
    if result.keys() != identifiers.keys():
        raise Failure("resource-samples-missing")
    return result


def database_sample(compose, environment, maximum=512 << 20):
    # A fixed read-only fixture-owner observation. No application role gains
    # monitoring privileges and no SQL text, relation names or DSNs survive.
    query = """BEGIN READ ONLY; SET LOCAL statement_timeout='2s';
SELECT json_build_object('databaseBytes',pg_database_size(current_database()),
'readBlocks',blks_read,'hitBlocks',blks_hit,'tempBytes',temp_bytes,
'insertedRows',tup_inserted,'updatedRows',tup_updated,'deletedRows',tup_deleted,
'databaseStatsReset',coalesce(extract(epoch FROM stats_reset),0),
'walBytes',(SELECT wal_bytes FROM pg_stat_wal),
'walStatsReset',(SELECT coalesce(extract(epoch FROM stats_reset),0) FROM pg_stat_wal))
FROM pg_stat_database WHERE datname=current_database(); COMMIT;"""
    raw = local.command(compose + ["exec", "-T", "postgres", "psql", "-X", "-qAt", "-v", "ON_ERROR_STOP=1", "-U", "catalog", "-d", "catalog", "-c", query], environment, 5, True)
    value = local.safety.decode(raw)
    expected = {"databaseBytes", "readBlocks", "hitBlocks", "tempBytes", "insertedRows", "updatedRows", "deletedRows", "databaseStatsReset", "walBytes", "walStatsReset"}
    if type(value) is not dict or set(value) != expected or any(type(v) not in (int, float) or not math.isfinite(v) or not 0 <= v < 1e20 for v in value.values()):
        raise Failure("invalid-database-sample")
    if value["databaseBytes"] > maximum:
        raise Failure("fixture-database-growth-budget")
    return value


def drain_worker(worker):
    began = time.monotonic()
    keys = ['open_agentic_catalog_jobs{state="' + state + '"}' for state in ("queued", "running", "waiting")]
    keys.append('open_agentic_catalog_outbox_events{state="pending"}')
    deadline = began + 30
    while time.monotonic() < deadline:
        values = metrics_sample(worker, worker=True)
        if not set(keys) <= values.keys():
            raise Failure("required-drain-signals-missing")
        observed = time.monotonic()
        if observed >= deadline:
            break
        if sum(values[key] for key in keys) == 0:
            return {"status": "passed", "elapsedSeconds": round(observed - began, 3), "nonterminalJobs": 0, "pendingOutbox": 0}
        time.sleep(min(.5, deadline - observed))
    raise Failure("worker-drain-incomplete")


def abort_trial(stop):
    # Reuse the installed interruption handler and subprocess-group cleanup.
    # A required observer or writer failure must not leave an hour of readers
    # running with unusable evidence. Teardown already ignores this signal.
    if not stop.is_set():
        os.kill(os.getpid(), signal.SIGALRM)


def publication_clock_guard(compose, environment):
    # On an activation failure only, classify one known fixture DB guard.
    # Raw logs/SQL/parameters are never emitted or retained in the report.
    try:
        raw = local.command(compose + ["logs", "--no-color", "--tail", "40", "postgres"], environment, 5, True)
        return b"active publication snapshot activation time is inconsistent" in raw
    except (Failure, OSError, ValueError):
        return None


def database_failure_evidence(compose, environment):
    """Classify recent fixture errors without retaining SQL, parameters or logs."""
    try:
        raw = local.command(compose + ["logs", "--no-color", "--tail", "80", "postgres"], environment, 5, True)
    except (Failure, OSError, ValueError):
        return {"status": "unavailable"}
    return {
        "status": "observed",
        "sharedMemoryExhausted": b"could not resize shared memory segment" in raw and b"No space left on device" in raw,
        "memoryExhausted": b"out of memory" in raw,
        "statementTimeout": b"canceling statement due to statement timeout" in raw,
        "serializationFailure": b"could not serialize access" in raw,
        "deadlock": b"deadlock detected" in raw,
    }


def validate_observation_window(samples, final, started, maximum_gap):
    """Require bounded observations across the entire parent sampling window."""
    if len(samples) < 2:
        raise Failure("required-sample-gap")
    previous = started
    for observation in [*samples, final]:
        began, ended = observation["startedElapsedSeconds"], observation["elapsed"]
        if not previous <= began <= ended or ended - previous > maximum_gap:
            raise Failure("required-sample-gap")
        previous = ended


def soak_evidence(samples, final, window):
    # Use complete collection passes inside the child's parent-monotonic
    # bracket, excluding pre-workload setup and post-workload drain samples.
    samples = [sample for sample in samples if window["startedElapsedSeconds"] <= sample["startedElapsedSeconds"] <= sample["elapsed"] <= window["finishedElapsedSeconds"]]
    if len(samples) < 50:
        raise Failure("soak-observation-incomplete")
    classes = ("idempotency", "source_preview", "source_inspection", "source_coordination", "terminal_job", "webhook_delivery", "acknowledged_outbox")
    cycles = {}
    for name in classes:
        key = 'open_agentic_catalog_housekeeping_batches_total{class="' + name + '",outcome="succeeded"}'
        failed = key.replace('outcome="succeeded"', 'outcome="failed"')
        if key not in samples[0].get("worker", {}) or key not in final.get("worker", {}) or failed not in final.get("worker", {}):
            raise Failure("soak-maintenance-evidence-missing")
        cycles[name] = final["worker"][key] - samples[0]["worker"][key]
        if cycles[name] < 1 or final["worker"][failed] != samples[0]["worker"].get(failed):
            raise Failure("soak-maintenance-incomplete")
    early, late = samples[:3], samples[-3:]
    hours = (sum(x["elapsed"] for x in late) - sum(x["elapsed"] for x in early)) / (3 * 3600)
    if hours <= 0:
        raise Failure("soak-observation-window-invalid")
    def slope(path):
        def value(sample):
            for key in path:
                sample = sample[key]
            return sample
        return round((sum(value(x) for x in late) - sum(value(x) for x in early)) / (3 * hours), 3)
    trends = {role: {"workingSetBytesPerHour": slope(("resources", role, "workingSetBytes")), "heapBytesPerHour": slope((role, "open_agentic_catalog_go_heap_bytes")), "goroutinesPerHour": slope((role, "open_agentic_catalog_go_goroutines"))} for role in ("api", "worker")}
    return {"successfulHousekeepingChecks": cycles, "earlyLateTrends": trends, "databaseBytesPerHour": slope(("database", "databaseBytes")), "retention": "unchanged; history growth is not a leak; eligible/protected retention cases use the PostgreSQL integration gate"}


def positive_samples(rows, count):
    if not isinstance(rows, list) or len(rows) != count:
        return False
    try:
        values = [float(row["value"][1]) for row in rows]
    except (KeyError, IndexError, TypeError, ValueError):
        return False
    return all(math.isfinite(value) and value > 0 for value in values)


MONITORING_TARGETS = {
    "api": {"catalog_deployment": "local", "catalog_role": "api", "job": "catalog-api", "instance": "api:8080"},
    "worker": {"catalog_deployment": "local", "catalog_role": "worker", "job": "catalog-worker", "instance": "worker:9090"},
}


def monitoring_targets(targets):
    if not isinstance(targets, list) or len(targets) != 2:
        raise Failure("prometheus-targets-incomplete")
    remaining = list(MONITORING_TARGETS.values())
    for target in targets:
        if not isinstance(target, dict) or target.get("health") != "up" or target.get("labels") not in remaining:
            raise Failure("prometheus-target-identity-mismatch")
        remaining.remove(target["labels"])


def monitoring_sample_times(rows):
    # timestamp(metric) returns the underlying scrape timestamp as the value.
    # The instant-query evaluation timestamp in value[0] proves no freshness.
    if not isinstance(rows, list) or len(rows) != 2:
        raise Failure("runtime-sample-identity-mismatch")
    observed = {}
    for row in rows:
        if not isinstance(row, dict):
            raise Failure("runtime-sample-identity-mismatch")
        labels = row.get("metric")
        roles = [role for role, expected in MONITORING_TARGETS.items() if labels == expected]
        if len(roles) != 1 or roles[0] in observed:
            raise Failure("runtime-sample-identity-mismatch")
        values = row.get("value")
        if not isinstance(values, list) or len(values) != 2:
            raise Failure("invalid-runtime-sample-time")
        try:
            value = float(values[1])
        except (TypeError, ValueError):
            raise Failure("invalid-runtime-sample-time") from None
        if not math.isfinite(value) or value <= 0:
            raise Failure("invalid-runtime-sample-time")
        observed[roles[0]] = value
    return observed


def recovered_monitoring_samples(targets, query, seconds=60):
    deadline = time.monotonic() + seconds
    baseline = None
    expression = 'timestamp(open_agentic_catalog_go_heap_bytes{catalog_deployment="local",job=~"catalog-(api|worker)",catalog_role=~"api|worker"})'
    while time.monotonic() < deadline:
        try:
            monitoring_targets(targets())
            observed = monitoring_sample_times(query(expression))
            if baseline is None:
                baseline = observed
            elif time.monotonic() < deadline and all(observed[role] > baseline[role] for role in MONITORING_TARGETS):
                return {"baseline": baseline, "fresh": observed}
        except (Failure, http.ClientFailure):
            pass
        time.sleep(.25)
    raise Failure("collector-recovery-sample-deadline")


def monitoring_retained_read(api, seeded):
    # Independently expected content comes from the completed seed command,
    # rather than accepting whichever record a read happens to return.
    request = urllib.request.Request(api + "/api/v1/resources/" + urllib.parse.quote(seeded["resourceId"], safe=""),
                                     headers={"Authorization": "Bearer local-reader"})
    status, _, raw = http.request(request)
    if status != 200:
        raise Failure("collector-outage-read-failed")
    resource = http.decode(raw)
    if not isinstance(resource, dict) or any(resource.get(field) != seeded[expected] for field, expected in
                                           [("id", "resourceId"), ("canonicalIdentifier", "identifier"), ("kind", "kind")]):
        raise Failure("collector-outage-content-mismatch")
    return {"authorizedRead": True, "retainedContentMatched": True}


def check_query_fixture(state, environment, path):
    # Docker cp rejects read-only root filesystems, including tmpfs targets.
    # Execute the same pinned tool with a read-only synthetic fixture mount.
    local.command(["docker", "run", "--rm", "--pull=never", "--name", state["project"] + "-query-check", "--label", "com.docker.compose.project=" + state["project"], "--label", OWNER_LABEL + "=" + state["nonce"], "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--user", "65534:65534", "--cpus", ".5", "--memory", "512m", "--pids-limit", "128", "--tmpfs", "/tmp:size=16m,mode=1777", "--tmpfs", "/prometheus:size=16m,mode=1777", "--mount", "type=bind,src=" + str(path) + ",dst=/tests.json,readonly", "--entrypoint", "/bin/promtool", local.image_pins()["prometheus"], "test", "rules", "/tests.json"], environment, 30)


def occupied_port_check(state, environment, port):
    # Reuse the owned collector's allocated engine port, not a host socket:
    # the Docker daemon can be in a separate VM. No foreign service is touched.
    if type(port) is not int or not 1 <= port <= 65535:
        raise Failure("invalid-owned-collector-port")
    # A single real Docker bind boundary, after timed work. No retained stack
    # is recreated or rolled back; ordinary owned cleanup reaps this container.
    raw = local.command(["docker", "create", "--pull=never", "--name", state["project"] + "-port-check", "--label", "com.docker.compose.project=" + state["project"], "--label", OWNER_LABEL + "=" + state["nonce"], "--network", state["project"] + "_monitoring", "--publish", "127.0.0.1:" + str(port) + ":9090", "--tmpfs", "/prometheus:size=16m,mode=1777", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--cpus", ".5", "--memory", "128m", "--pids-limit", "64", "--user", "65534:65534", local.image_pins()["prometheus"]], environment, 15, True)
    identifier = raw.decode().strip()
    if not re.fullmatch(r"[a-f0-9]{64}", identifier):
        raise Failure("port-check-identity-invalid")
    try:
        local.command(["docker", "start", identifier], environment, 15)
    except Failure as error:
        if str(error) != "child-failed":
            raise
    else:
        raise Failure("occupied-port-not-refused")
    value = local.safety.decode(local.command(["docker", "inspect", identifier], environment, 15, True))[0]
    if value["State"]["Running"] or value["Config"]["Labels"].get(OWNER_LABEL) != state["nonce"]:
        raise Failure("port-check-state-invalid")
    if not any(message in value["State"].get("Error", "").lower() for message in ("address already in use", "port is already allocated")):
        raise Failure("port-check-bind-failure-unproven")
    return {"occupiedPortRefused": True, "recovery": "owned cleanup; retained reconfiguration requires explicit retry"}


def workload_settings(mode, environment):
    if mode not in {"smoke", "monitoring", "load", "soak", "scale", "public-delivery"}:
        raise Failure("invalid-profile")
    if mode == "public-delivery":
        for name, expected in {"PERF_DURATION_SECONDS": "60", "PERF_WARMUP_SECONDS": "5", "PERF_WORKERS": "2"}.items():
            if environment.get(name, "") not in ("", expected):
                raise Failure("public-delivery-settings-are-fixed")
        return {"warmupSeconds": 5, "seconds": 60, "workers": 2, "maxRequests": 50000}
    if mode == "soak":
        for name, expected in {"PERF_DURATION_SECONDS": "3600", "PERF_WARMUP_SECONDS": "5", "PERF_WORKERS": "2"}.items():
            if environment.get(name, "") not in ("", expected):
                raise Failure("soak-profile-is-fixed")
        return {"warmupSeconds": 5, "seconds": 3600, "workers": 2, "maxRequests": 720000}
    selected = dict(environment)
    if selected.get("PERF_DURATION_SECONDS", "") == "":
        selected.pop("PERF_DURATION_SECONDS", None)
    duration = local.bounded_integer(selected, "PERF_DURATION_SECONDS", 120 if mode == "monitoring" else 60 if mode == "load" else 30, 120 if mode == "monitoring" else 30, 180)
    warmup = local.bounded_integer(selected, "PERF_WARMUP_SECONDS", 5, 0, 10)
    workers = local.bounded_integer(selected, "PERF_WORKERS", 2, 1, 8)
    if mode == "scale":
        scale.items(environment, Failure)
        if (len(scale.selected_cases(environment, Failure)) + 1) * (duration + warmup + 15) > 880:
            raise Failure("scale-phase-budget-exceeded")
    # Fixed 10 ms think time limits each worker to fewer than 100 requests/s.
    # Retain the default ceiling, scaling only to avoid truncating a valid run.
    return {"warmupSeconds": warmup, "seconds": duration, "workers": workers,
            "maxRequests": max(50000, workers * duration * 100)}


def validate_workload(value, profile):
    """Accept only the exact bounded aggregate, never arbitrary child output."""
    def require(condition):
        if not condition:
            raise Failure("invalid-workload-report")
    def integer(number, maximum=10**16):
        return type(number) is int and 0 <= number <= maximum
    def number(item, maximum):
        return type(item) in (int, float) and math.isfinite(item) and 0 <= item <= maximum
    require(type(value) is dict and set(value) == {"schemaVersion", "model", "profile", "baselineResults", "responseDigest", "bucketUpperSeconds", "warmup", "measured", "verdict", "generator"})
    generator = value["generator"]
    require(type(generator) is dict and set(generator) == {"gomaxprocs", "heapBytesAtFinish", "allocatedBytes", "gcCycles", "goroutinesAtFinish"} and all(integer(v) for v in generator.values()))
    require(generator["gomaxprocs"] > 0 and generator["goroutinesAtFinish"] > 0 and generator["heapBytesAtFinish"] <= generator["allocatedBytes"])
    require(type(value["schemaVersion"]) is int and value["schemaVersion"] == 3)
    require(value["model"] == "closed-loop-development-diagnostic" and value["profile"] == profile)
    require(type(value["profile"]) is dict and all(type(v) is (dict if k in {"scale", "public"} else str if k in {"scenario", "query"} else int) for k, v in value["profile"].items()))
    public_case = profile.get("public")
    if public_case is not None:
        require(profile.get("scenario") == "public" and type(public_case) is dict and set(public_case) == {"entries", "bytes", "etag", "digest", "conditional"})
        require(type(public_case["entries"]) is int and public_case["entries"] == 1001)
        require(type(public_case["bytes"]) is int and 0 < public_case["bytes"] <= 16 << 20)
        require(type(public_case["conditional"]) is bool and type(public_case["etag"]) is str and re.fullmatch(r'"[!#-~]{1,126}"', public_case["etag"]) is not None)
        require(type(public_case["digest"]) is str and re.fullmatch(r"sha256:[a-f0-9]{64}", public_case["digest"]) is not None)
    scale_case = profile.get("scale")
    if scale_case is not None:
        require(profile.get("scenario") == "scale" and type(scale_case) is dict)
        require(set(scale_case) in ({"items", "case", "publisher"}, {"items", "case", "publisher", "cursor"}))
        require(type(scale_case["items"]) is int and scale_case["items"] in scale.BUDGETS and scale_case["case"] in scale.CASES[:-1] + scale.PLUGIN_CASES + scale.ORDER_CASES)
        require(type(scale_case["publisher"]) is str and re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", scale_case["publisher"]) is not None)
        require("cursor" not in scale_case or (scale_case["case"] in {"later", "terminal"} or scale_case["case"].endswith("-third")) and type(scale_case["cursor"]) is str and 0 < len(scale_case["cursor"]) <= 2048)
        require(("cursor" in scale_case) == ((scale_case["case"] in {"later", "terminal"} or scale_case["case"].endswith("-third"))))
    else:
        require(profile.get("scenario") != "scale")
    if "mixedScaleItems" in profile:
        require(profile.get("scenario") == "mixed" and type(profile["mixedScaleItems"]) is int and profile["mixedScaleItems"] in scale.BUDGETS)
        require(value["baselineResults"] == 20)
    require(value["verdict"] in ("passed", "failed", "incomplete"))
    require(integer(value["baselineResults"], 100000 if scale_case else 1001 if public_case else 20))
    if public_case and value["verdict"] == "passed":
        require(value["baselineResults"] == 1001 and value["responseDigest"] == public_case["digest"])
    require(type(value["responseDigest"]) is str and (value["responseDigest"] == "" or re.fullmatch(r"sha256:[a-f0-9]{64}", value["responseDigest"]) is not None))
    require(type(value["bucketUpperSeconds"]) is list and all(type(v) in (float, int) for v in value["bucketUpperSeconds"]) and value["bucketUpperSeconds"] == [.001, .005, .01, .025, .05, .1, .25, .5, 1, 2, 3, 5])
    failures = 0
    for name, seconds in [("warmup", profile["warmupSeconds"]), ("measured", profile["seconds"])]:
        phase = value[name]
        require(type(phase) is dict and set(phase) == {"count", "succeeded", "failed", "elapsedSeconds", "latencySecondsTotal", "latencyBuckets", "latencyOverflow", "outcomes", "httpFailureStatuses", "startedAtUnixMillis", "issuanceEndedAtUnixMillis", "finishedAtUnixMillis", "complete", "clockAdjustmentMillis", "successfulResponseBytes"})
        require(type(phase["clockAdjustmentMillis"]) is int and abs(phase["clockAdjustmentMillis"]) <= 86400000)
        require(all(integer(phase[key], profile["maxRequests"]) for key in ("count", "succeeded", "failed", "latencyOverflow")))
        require(phase["count"] == phase["succeeded"] + phase["failed"] and type(phase["complete"]) is bool)
        require(integer(phase["successfulResponseBytes"], phase["succeeded"] * ((16 << 20) if public_case else (1 << 20))))
        if public_case:
            require(phase["successfulResponseBytes"] == phase["succeeded"] * (0 if public_case["conditional"] else public_case["bytes"]))
        else:
            require((phase["successfulResponseBytes"] > 0) == (phase["succeeded"] > 0))
        require(number(phase["elapsedSeconds"], seconds + 15) and number(phase["latencySecondsTotal"], profile["maxRequests"] * 15))
        outcomes = phase["outcomes"]
        require(type(outcomes) is dict and set(outcomes) == {"timeout", "transport", "status", "body", "correctness", "canceled"})
        require(all(integer(v, phase["failed"]) for v in outcomes.values()) and sum(outcomes.values()) == phase["failed"])
        statuses = phase["httpFailureStatuses"]
        require(type(statuses) is dict and len(statuses) <= 502 and all(type(k) is str and re.fullmatch(r"(?:[1-5][0-9]{2}|400-stale-generation|400-invalid-live-cursor)", k) and integer(v, phase["failed"]) and v > 0 for k, v in statuses.items()) and sum(statuses.values()) == outcomes["status"])
        buckets = phase["latencyBuckets"]
        require(type(buckets) is list and len(buckets) == 12 and all(integer(v, phase["count"]) for v in buckets))
        require(buckets == sorted(buckets) and buckets[-1] + phase["latencyOverflow"] == phase["count"])
        times = [phase[key] for key in ("startedAtUnixMillis", "issuanceEndedAtUnixMillis", "finishedAtUnixMillis")]
        require(all(integer(v) for v in times) and times == sorted(times))
        if times[0]:
            require(abs((times[-1] - times[0]) / 1000 - phase["elapsedSeconds"]) <= .01)
            require(times[1] - times[0] <= seconds * 1000 + 1)
        else:
            require(times == [0, 0, 0] and phase["count"] == 0 and phase["elapsedSeconds"] == 0)
        if phase["complete"] and seconds:
            require(times[0] > 0 and times[1] - times[0] >= seconds * 1000 - 1 and phase["count"] > 0)
        if seconds == 0:
            require(times == [0, 0, 0] and phase["count"] == 0)
        failures += phase["failed"]
    if value["warmup"]["finishedAtUnixMillis"] and value["measured"]["startedAtUnixMillis"]:
        require(value["warmup"]["finishedAtUnixMillis"] + value["warmup"]["clockAdjustmentMillis"] <= value["measured"]["startedAtUnixMillis"] + 1)
    if value["verdict"] != "incomplete":
        require(value["baselineResults"] >= (0 if profile.get("scenario") == "scale" else 3) and value["responseDigest"] != "" and value["warmup"]["complete"] and value["measured"]["complete"])
        require((failures == 0) == (value["verdict"] == "passed"))
    return value


def workload_result(binary, directory, profile, environment):
    path = directory / "workload.json"
    if path.exists() or path.is_symlink():
        raise Failure("workload-report-already-exists")
    failed = False
    try:
        local.command([str(binary), str(directory / "profile.json"), str(path)], environment, profile["seconds"] + profile["warmupSeconds"] + 15)
    except Failure:
        failed = True
    try:
        info = path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_size > 16384 or info.st_mode & 0o077:
            raise Failure("unsafe-workload-report")
        value = validate_workload(local.safety.read_document(path), profile)
    except (OSError, ValueError, TypeError, KeyError) as error:
        raise Failure("missing-or-invalid-workload-report") from error
    return value, failed


def run(mode):
    settings = workload_settings(mode, os.environ)
    scale_items = scale.items(os.environ, Failure) if mode == "scale" else None
    scale_cases = scale.selected_cases(os.environ, Failure) if scale_items else ()
    sql_plans = scale.plans_enabled(os.environ, Failure) if scale_items else False
    database_budget = scale.BUDGETS[scale_items][1] if scale_items else 512 << 20
    if os.environ.get("LOCAL_MONITORING") or os.environ.get("PERF_PROFILE"):
        raise Failure("measurement-profile-is-selected-by-target")
    environment = local.isolated_environment()
    run_id = secrets.token_hex(12)
    directory = local.private_directory(ROOT / "coverage/performance" / run_id)
    report = {"version": 1, "runId": run_id, "profile": mode, "settings": settings, "status": "incomplete", "cleanup": "incomplete", "samples": []}
    state = {"version": 2, "runId": run_id, "project": "open-agentic-catalog-perf-" + run_id, "image": "open-agentic-catalog:perf-" + run_id, "nonce": secrets.token_hex(12), "engine": engine_identity(environment), "monitoring": mode == "monitoring"}
    local.safety.write_document(directory / "ownership.json", state)
    local.safety.write_document(directory / "owned-compose.json", state_model(state))
    lock = (directory / "owner.lock").open("x")
    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
    stop = threading.Event()
    sampler = None
    writer = None
    journey = None
    delivery_journey, delivery_state = None, None
    writer_stop = threading.Event()
    failure = None
    began = time.monotonic()
    original_handlers = {}
    stage = "source"
    def interrupted(_signal, _frame):
        raise Failure("interrupted-or-deadline")
    for number in (signal.SIGINT, signal.SIGTERM, signal.SIGALRM):
        original_handlers[number] = signal.signal(number, interrupted)
    signal.alarm(scale.BUDGETS[scale_items][0] + 1800 if scale_items else 4800 if mode == "soak" else 1800 if mode == "load" else 900)
    try:
        report["source"] = source_identity(environment)
        if scale_items:
            report["scaleWorkload"] = os.environ.get("PERF_SCALE_WORKLOAD", "standard")
            stage = "scale-capacity"
            report["scaleBudgets"] = {"items": scale_items, "seedSeconds": scale.BUDGETS[scale_items][0], "databaseBytes": database_budget, "minimumFreeBytesDuringRun": 128 << 20}
            report["scaleCapacity"] = scale.preflight(local, environment, scale_items)
            if sql_plans:
                stage = "scale-plan-build"
                local.command(["go", "test", "-tags=integration", "-c", "-o", str(directory / "scale-plans"), "./internal/platform/database"], environment, 180, True)
        with socket.socket() as reservation:
            reservation.bind(("127.0.0.1", 0))
            api_port = str(reservation.getsockname()[1])
        environment.update(IMAGE=state["image"], VERSION="performance", COMMIT=report["source"]["commit"], CATALOG_COMPOSE_HTTP_PORT=api_port, CATALOG_COMPOSE_POSTGRES_PORT="0")
        if state["monitoring"]:
            environment = local.monitoring_environment(environment, local.secret_file(directory), "0", "0")
        compose = local.compose_arguments(state["project"], state["monitoring"])
        compose[compose.index("-p"):compose.index("-p")] = ["-f", str(ROOT / "test/performance/compose.yaml"), "-f", str(directory / "owned-compose.json")]
        print("Catalog trial " + run_id + ": build and setup", flush=True)
        stage = "image-build"
        local.command(["docker", "build", "--label", OWNER_LABEL + "=" + state["nonce"], "--build-arg", "VERSION=performance", "--build-arg", "COMMIT=" + report["source"]["commit"], "-t", state["image"], "-f", str(ROOT / "Containerfile"), str(ROOT)], environment, 600)
        report["imageId"] = local.command(["docker", "image", "inspect", "--format", "{{.Id}}", state["image"]], environment, 15, True).decode().strip()
        stage = "network-reservation"
        report["fixtureSubnets"] = reserve_networks(state, environment)
        policy = directory / "federation-policy.json"
        local.safety.write_document(policy, fixture_policy(report["fixtureSubnets"]))
        # The private parent protects host access; both non-root app roles need
        # to read this non-secret, exact-origin policy through their bind mount.
        policy.chmod(0o444)
        environment["CATALOG_COMPOSE_FEDERATION_POLICY_FILE"] = str(policy)
        if mode in {"load", "soak", "scale", "public-delivery"}:
            configuration = directory / "config.json"
            configured = scenarios.configuration(local.safety.read_document(ROOT / "config/postgres.json"), scale=bool(scale_items))
            if mode == "public-delivery":
                configured["discovery"]["hosts"].append({"host": "localhost", "tenantId": "00000000-0000-7000-8000-000000000001", "mode": "authenticated", "publisherDomain": "catalog.example.com"})
            local.safety.write_document(configuration, configured)
            configuration.chmod(0o444)  # Synthetic development identities only.
            environment["CATALOG_PERF_CONFIG_FILE"] = str(configuration)
        stage = "compose-start"
        local.command(compose + ["up", "-d", "--no-build"], environment, 180)
        stage = "loopback-discovery"
        api = address(local.command(compose + ["port", "api", "8080"], environment, 15, True))
        worker = address(local.command(compose + ["port", "worker", "9090"], environment, 15, True))
        stage = "readiness"
        wait_http(api + "/readyz")
        wait_http(worker + "/readyz")
        if state["monitoring"]:
            prometheus = address(local.command(compose + ["port", "prometheus", "9090"], environment, 15, True))
            grafana = address(local.command(compose + ["port", "grafana", "3000"], environment, 15, True))
            wait_http(prometheus + "/-/ready")
            wait_http(grafana + "/api/health", 90)
        stage = "seed-smoke"
        seeded_output = local.command(compose + ["exec", "-T", "api", "/usr/local/bin/catalogctl", "smoke", "--base-url", "http://127.0.0.1:8080", "--run-id", "perf-" + run_id, "--timeout", "3m", "--admin-token", "local-admin", "--contributor-token", "local-contributor", "--reviewer-token", "local-reviewer", "--publisher-token", "local-publisher", "--reader-token", "local-reader", "--other-tenant-token", "other-tenant-admin"], environment, 185, state["monitoring"])
        if mode in {"load", "soak", "scale"}:
            stage = "seed-indexed"
            local.command(compose + ["exec", "-T", "api", "/usr/local/bin/catalogctl", "smoke", "--base-url", "http://127.0.0.2:8080", "--run-id", "remote-" + run_id, "--timeout", "3m", "--admin-token", "second-admin", "--contributor-token", "second-contributor", "--reviewer-token", "second-reviewer", "--publisher-token", "second-publisher", "--reader-token", "second-reader", "--other-tenant-token", "local-admin"], environment, 185)
            journey = scenarios.Journey(api, run_id, http, Failure)
            journey.connect(manual=bool(scale_items))
            report["dataset"] = {"localReleases": 3, "indexedPublicReleases": 3, "tenants": 2, "maxGeneratedReleases": 60 if mode == "soak" else max(1, settings["seconds"] // 30), "state": "fresh-invocation-owned"}
        if mode == "public-delivery":
            stage = "public-small-snapshot"
            delivery_journey = scenarios.Journey(api, run_id, http, Failure)
            delivery_state = public_delivery.begin(delivery_journey)
        stage = "resource-verification"
        report["servedVersion"] = local.safety.decode(get(api + "/version"))
        report["resources"] = owned_resources(state, environment)
        resource_ids = {}
        for name, cpu, memory in [("api", 1000000000, 256 << 20), ("worker", 500000000, 256 << 20), ("postgres", 1000000000, 512 << 20)]:
            identifier = local.command(compose + ["ps", "-q", name], environment, 15, True).decode().strip()
            resource_ids[name] = identifier
            info = local.safety.decode(local.command(["docker", "inspect", identifier], environment, 15, True))[0]
            if info["HostConfig"]["NanoCpus"] != cpu or info["HostConfig"]["Memory"] != memory:
                raise Failure("resource-limits-not-enforced")
            if name == "postgres":
                if info["HostConfig"]["ShmSize"] != 256 << 20:
                    raise Failure("shared-memory-limit-not-enforced")
                report["postgresSharedMemoryBytes"] = info["HostConfig"]["ShmSize"]
        if state["monitoring"]:
            for name in ("prometheus", "grafana"):
                resource_ids[name] = local.command(compose + ["ps", "-q", name], environment, 15, True).decode().strip()
        seed_fixture = {"postgres": resource_ids["postgres"], "image": report["imageId"],
                        "project": state["project"], "nonce": state["nonce"]}
        if scale_items:
            stage = "scale-seed"
            report["preSeedDrain"] = drain_worker(worker)
            local.command(compose + ["stop", "--timeout", "30", "worker"], environment, 45)
            port = int(address(local.command(compose + ["port", "postgres", "5432"], environment, 15, True)).rsplit(":", 1)[1])
            scale.seed(local, environment, directory, seed_fixture, scale_items, report,
                       lambda: {"resources": resource_sample(resource_ids, environment), "database": database_sample(compose, environment, database_budget), "daemonFreeBytes": scale.daemon_free_bytes(local, environment)}, abort_trial)
            stage = "scale-worker-restart"
            local.command(compose + ["start", "worker"], environment, 30)
            worker = address(local.command(compose + ["port", "worker", "9090"], environment, 15, True))
            wait_http(worker + "/readyz")
            stage = "scale-seed-drain"
            report["seedDrain"] = drain_worker(worker)
            stage = "scale-profile"
            scale_profile = scale.prepare(api, scale_items, http, Failure)
            stage = "scale-scoped-access"
            report["publisherAccess"] = scale.publisher_access(api, http, Failure, scale_items, report["seedResult"]["inventory"]["sparseNamespace"])
            stage = "scale-overview"
            report["overview"] = scale.overview(api, http, Failure, report["seedResult"]["inventory"])
            report["dataset"] = {"fixtureVersion": report["seedResult"]["inventory"]["fixtureVersion"], "localReleases": scale_items * 9 // 10, "indexedGroups": scale_items // 10, "searchableItems": scale_items, "tenants": 2, "state": "fresh-invocation-owned", "representationReuse": "small Agent/MCP/Skill documents and three exact Plugin archives; every fiftieth Agent/Skill document larger", "ingestionThroughput": "not-measured"}
        if mode == "public-delivery":
            stage = "public-seed"
            report["preSeedDrain"] = drain_worker(worker)
            local.command(compose + ["stop", "--timeout", "30", "worker"], environment, 45)
            port = int(address(local.command(compose + ["port", "postgres", "5432"], environment, 15, True)).rsplit(":", 1)[1])
            scale.seed(local, environment, directory, seed_fixture, 1000, report,
                       lambda: {"resources": resource_sample(resource_ids, environment), "database": database_sample(compose, environment, database_budget)}, abort_trial, public=True)
            local.command(compose + ["start", "worker"], environment, 30)
            worker = address(local.command(compose + ["port", "worker", "9090"], environment, 15, True))
            wait_http(worker + "/readyz")
            stage = "public-authority-controls"
            report["publicDelivery"] = public_delivery.prepare(delivery_journey, delivery_state)
            report["seedDrain"] = drain_worker(worker)
            report["dataset"] = {"localReleases": 1000, "ardEntries": 1001, "mcpServers": 998, "agents": 1, "skills": 1, "state": "fresh-invocation-owned", "ingestionThroughput": "not-measured"}
        # Establish continuity only after intentional fixture setup, including
        # scale's worker stop/start. Collector restarts are a separate scenario.
        process_ids = {role: resource_ids[role] for role in ("api", "worker", "postgres")}
        report["containerBaseline"] = scale.container_state(local, environment, process_ids)
        stage = "client-build"
        binary = directory / "load-client"
        local.command(["go", "build", "-o", str(binary), "./test/performance"], environment, 120)
        duration = settings["seconds"]
        profile = settings | {"port": int(api.rsplit(":", 1)[1])}
        local.safety.write_document(directory / "profile.json", profile)
        observation_started = round(time.monotonic() - began, 3)
        def sample():
            while not stop.is_set():
                try:
                    if len(report["samples"]) >= (96 if scale_items else 80):
                        raise Failure("sample-window-limit")
                    collection_started = round(time.monotonic() - began, 3)
                    observation = diagnostic_sample(api, worker)
                    observation.update(startedElapsedSeconds=collection_started, stage=stage, resources=resource_sample(resource_ids, environment), database=database_sample(compose, environment, database_budget))
                    observation["containers"] = scale.container_state(local, environment, process_ids, report["containerBaseline"])
                    if scale_items:
                        observation["daemonFreeBytes"] = scale.daemon_free_bytes(local, environment)
                    observation["elapsed"] = round(time.monotonic() - began, 3)
                    report["samples"].append(observation)
                    progress = {"version": 1, "runId": run_id, "status": "checkpoint-not-a-verdict", "stage": stage, "elapsedSeconds": observation["elapsed"], "sampleCount": len(report["samples"]), "committedPublications": journey.stats["committedPublications"] if journey is not None else 0, "latest": observation}
                    local.safety.write_document(directory / "progress.json", progress)
                except (Failure, http.ClientFailure, ValueError, OSError, KeyError, TypeError) as error:
                    report["sampleFailure"] = True
                    report["sampleFailureCode"] = str(error) if isinstance(error, Failure) else "bounded-observer-failure"
                    abort_trial(stop)
                    return
                print("Catalog trial " + run_id + ": " + stage + "; elapsed=" + str(observation["elapsed"]) + "s; samples=" + str(len(report["samples"])) + "; committed=" + str(progress["committedPublications"]), flush=True)
                if stop.wait(60 if mode == "soak" else 10):
                    return
        sampler = threading.Thread(target=sample, daemon=True)
        sampler.start()
        stage = "workload"
        report["phaseWindows"] = {}
        def observed_workload(name, child_directory, selected):
            # Parent monotonic brackets align samples even if wall time steps.
            window = {"startedElapsedSeconds": round(time.monotonic() - began, 3)}
            report["phaseWindows"][name] = window
            try:
                return workload_result(binary, child_directory, selected, environment)
            finally:
                window["finishedElapsedSeconds"] = round(time.monotonic() - began, 3)
        if mode == "public-delivery":
            report["readScenarios"] = {}
            for label, conditional in (("public-full", False), ("public-conditional", True)):
                stage = label
                selected = profile | {"scenario": "public", "public": report["publicDelivery"]["manifest"] | {"conditional": conditional}}
                child_directory = local.private_directory(directory / label)
                local.safety.write_document(child_directory / "profile.json", selected)
                value, failed = observed_workload(label, child_directory, selected)
                if conditional:
                    report["readScenarios"][label] = value
                else:
                    report["workload"] = value
                if failed or value["verdict"] != "passed":
                    raise Failure("public-delivery-workload-failed")
        if mode == "load":
            report["readScenarios"] = {}
            for scenario in ("local", "indexed", "public"):
                stage = "reads-" + scenario
                child_directory = local.private_directory(directory / scenario)
                selected = profile | {"scenario": scenario}
                if scenario == "indexed":
                    selected["query"] = "remote-" + run_id
                local.safety.write_document(child_directory / "profile.json", selected)
                value, failed = observed_workload(scenario, child_directory, selected)
                report["readScenarios"][scenario] = value
                if failed or value["verdict"] != "passed":
                    raise Failure("representative-read-failed")
        if scale_items:
            report["readScenarios"] = {}
            report["scalePreflight"] = {}
            for case in scale.CASES + scale.PLUGIN_CASES + scale.ORDER_CASES:
                stage = "scale-preflight-" + case
                selected_scale = scale_profile | {"case": case}
                if case in scale.ORDER_CASES and case.endswith("-third"):
                    selected_scale["cursor"] = scale.later_cursor(api, http, Failure, order="publisher_" + case.split("-")[1])
                if case in {"later", "terminal"}:
                    selected_scale["cursor"] = scale.later_cursor(api, http, Failure, terminal=case == "terminal")
                selected = profile | {"warmupSeconds": 0, "seconds": 1, "scenario": "scale", "scale": selected_scale}
                if case == "public":
                    selected = profile | {"warmupSeconds": 0, "seconds": 1, "scenario": "public"}
                child_directory = local.private_directory(directory / ("preflight-" + case))
                local.safety.write_document(child_directory / "profile.json", selected)
                value, failed = workload_result(binary, child_directory, selected, environment)
                report["scalePreflight"][case] = value
                if failed or value["verdict"] != "passed":
                    raise Failure("scale-preflight-failed")
            for case in scale_cases:
                stage = "scale-" + case
                selected_scale = scale_profile | {"case": case}
                if case in scale.ORDER_CASES and case.endswith("-third"):
                    selected_scale["cursor"] = scale.later_cursor(api, http, Failure, order="publisher_" + case.split("-")[1])
                if case in {"later", "terminal"}:
                    selected_scale["cursor"] = scale.later_cursor(api, http, Failure, terminal=case == "terminal")
                selected = profile | {"scenario": "scale", "scale": selected_scale}
                if case == "public":
                    selected = profile | {"scenario": "public"}
                child_directory = local.private_directory(directory / case)
                local.safety.write_document(child_directory / "profile.json", selected)
                value, failed = observed_workload(case, child_directory, selected)
                report["readScenarios"][case] = value
                if failed or value["verdict"] != "passed":
                    raise Failure("scale-read-failed")
        stage = "workload"
        if journey is not None:
            profile["scenario"] = "mixed"
            if scale_items:
                profile["mixedScaleItems"] = scale_items
            local.safety.write_document(directory / "profile.json", profile)
            report["mixedJourney"] = {"status": "incomplete"}
            def mixed_writer():
                journey.mixed(writer_stop, duration, settings["warmupSeconds"], report["mixedJourney"])
                if report["mixedJourney"].get("status") != "passed":
                    abort_trial(writer_stop)
            writer = threading.Thread(target=mixed_writer, daemon=True)
            writer.start()
        if mode == "public-delivery":
            child_failed = False
        else:
            report["workload"], child_failed = observed_workload(profile.get("scenario", "smoke"), directory, profile)
        if writer is not None:
            writer_stop.set()
            writer.join(90)
            if writer.is_alive() or report["mixedJourney"].get("status") != "passed" or report["mixedJourney"].get("committedPublications", 0) == 0:
                raise Failure("mixed-workload-incomplete")
        workloads = [report["workload"], *report.get("readScenarios", {}).values()]
        report["wallClockCorrelation"] = "unavailable-clock-step" if any(abs(workload[phase]["clockAdjustmentMillis"]) > 10 for workload in workloads for phase in ("warmup", "measured")) else "available"
        stop.set()
        sampler.join(30)
        if sampler.is_alive() or report.get("sampleFailure"):
            raise Failure("required-monitoring-incomplete")
        if mode == "public-delivery":
            public_delivery.unchanged(delivery_journey, report["publicDelivery"]["manifest"])
        report["workerDrain"] = drain_worker(worker)
        collection_started = round(time.monotonic() - began, 3)
        report["final"] = diagnostic_sample(api, worker)
        report["final"]["startedElapsedSeconds"] = collection_started
        report["final"]["resources"] = resource_sample(resource_ids, environment)
        report["final"]["database"] = database_sample(compose, environment, database_budget)
        report["final"]["containers"] = scale.container_state(local, environment, process_ids, report["containerBaseline"])
        if scale_items:
            report["final"]["daemonFreeBytes"] = scale.daemon_free_bytes(local, environment)
            achieved = scale.request(api, {"limit": 1}, http, Failure)
            expected = scale_items + report["mixedJourney"]["committedPublications"]
            if achieved.get("durableTotal") != expected:
                raise Failure("scale-mixed-growth-mismatch")
            report["dataset"]["postMixedSearchableItems"] = expected
            if set(report.get("readScenarios", {})) != set(scale_cases):
                raise Failure("scale-case-coverage-incomplete")
        report["observation"] = {"intervalSeconds": 60 if mode == "soak" else 10, "maximumGapSeconds": 95 if mode == "soak" else 45, "containerMemory": "Docker working set, not RSS", "cpuThrottling": "unavailable", "hostContention": "uncontrolled-development-runner"}
        report["final"]["elapsed"] = round(time.monotonic() - began, 3)
        validate_observation_window(report["samples"], report["final"], observation_started, report["observation"]["maximumGapSeconds"])
        if any(report["final"]["database"][key] != sample["database"][key] for sample in report["samples"] for key in ("databaseStatsReset", "walStatsReset")):
            raise Failure("database-statistics-reset")
        if mode == "soak":
            report["soakEvidence"] = soak_evidence(report["samples"], report["final"], report["phaseWindows"]["mixed"])
        if child_failed or report["workload"]["verdict"] != "passed":
            raise Failure("workload-failed-or-incomplete")
        if state["monitoring"]:
            stage = "monitoring-verification"
            prometheus = address(local.command(compose + ["port", "prometheus", "9090"], environment, 15, True))
            grafana = address(local.command(compose + ["port", "grafana", "3000"], environment, 15, True))
            wait_http(prometheus + "/-/ready")
            wait_http(grafana + "/api/health")
            def targets():
                return local.safety.decode(get(prometheus + "/api/v1/targets"))["data"]["activeTargets"]
            monitoring_targets(targets())
            password = (directory / "grafana-password").read_text().strip()
            dashboard = local.safety.decode(get(grafana + "/api/dashboards/uid/catalog-overview", password))
            if dashboard["dashboard"]["uid"] != "catalog-overview":
                raise Failure("dashboard-not-provisioned")
            query_fixture = directory / "query-tests.json"
            local.safety.write_document(query_fixture, local.queries.fixture(dashboard["dashboard"]))
            query_fixture.chmod(0o444)  # Only synthetic series; owned private parent.
            stage = "query-semantics"
            check_query_fixture(state, environment, query_fixture)
            stage = "collector-config"
            local.command(compose + ["exec", "-T", "prometheus", "/bin/promtool", "check", "config", "/etc/prometheus/prometheus.yaml"], environment, 20)
            stage = "stored-series"
            def query(expression):
                response = local.safety.decode(get(prometheus + "/api/v1/query?" + urllib.parse.urlencode({"query": expression})))
                if response.get("status") != "success" or response.get("warnings"):
                    raise Failure("prometheus-query-failed")
                return response["data"]["result"]
            heap = query("open_agentic_catalog_go_heap_bytes{job=~\"catalog-.*\"}")
            if not positive_samples(heap, 2):
                raise Failure("runtime-series-not-stored")
            traffic = query('sum(increase(open_agentic_catalog_http_responses_total{job="catalog-api",route="/api/v1/catalog/search",status_class="2xx"}[2m]))')
            if not positive_samples(traffic, 1):
                raise Failure("synthetic-traffic-not-stored")
            expressions = 0
            for panel in dashboard["dashboard"]["panels"]:
                for target in panel.get("targets", []):
                    expression = local.queries.expression(target["expr"])
                    query(expression)
                    expressions += 1
            if expressions != 32:
                raise Failure("dashboard-queries-missing")
            # Collector outages must not gate the Catalog or its worker.
            stage = "collector-outage"
            previous = monitoring_sample_times(query('timestamp(open_agentic_catalog_go_heap_bytes{catalog_deployment="local",job=~"catalog-(api|worker)",catalog_role=~"api|worker"})'))
            local.command(compose + ["stop", "--timeout", "10", "prometheus", "grafana"], environment, 40)
            get(api + "/readyz")
            get(worker + "/readyz")
            seeded = local.safety.decode(seeded_output)
            if seeded.get("status") != "passed" or seeded.get("runId") != "perf-" + run_id or len(seeded.get("resources", [])) != 3:
                raise Failure("monitoring-seed-evidence-incomplete")
            outage_read = monitoring_retained_read(api, seeded["resources"][0])
            local.command(compose + ["start", "prometheus", "grafana"], environment, 40)
            # Docker may reallocate a dynamic host port on stop/start.
            prometheus = address(local.command(compose + ["port", "prometheus", "9090"], environment, 15, True))
            grafana = address(local.command(compose + ["port", "grafana", "3000"], environment, 15, True))
            wait_http(prometheus + "/-/ready")
            wait_http(grafana + "/api/health")
            recovered = recovered_monitoring_samples(targets, query)
            report["monitoring"] = {"targetIdentities": MONITORING_TARGETS, "collectorOutageRead": outage_read, "sampleTimesBeforeOutage": previous, "sampleTimesRecoveryBaseline": recovered["baseline"], "sampleTimesAfterRecovery": recovered["fresh"], "freshCollectionAfterRecovery": True, "targets": 2, "dashboard": "catalog-overview", "queries": expressions, "syntheticQueryGroups": len(local.queries.fixture(dashboard["dashboard"])["tests"]), "storedRuntimeSeries": len(heap), "collectorOutageIndependence": True, "images": local.image_pins()}
            stage = "occupied-port"
            report["monitoring"]["portConflict"] = occupied_port_check(state, environment, urllib.parse.urlsplit(prometheus).port)
            get(api + "/readyz")
            get(worker + "/readyz")
        if report["source"] != source_identity(environment):
            raise Failure("source-changed-during-trial")
        report["status"] = "passed-development-diagnostic"
    except (Failure, http.ClientFailure, OSError, ValueError, KeyError) as error:
        failure = error
        report["failureStage"] = stage
        report["failure"] = str(error) if isinstance(error, Failure) else "bounded-fixture-failure"
        if delivery_journey is not None and stage in {"public-small-snapshot", "public-authority-controls"}:
            # Keys are fixed fixture stages, never resource names or request bodies.
            report["publicDeliveryCommand"] = delivery_journey.last_command
        if journey is not None:
            command = journey.last_command if stage == "seed-indexed" else report.get("mixedJourney", {}).get("failedCommand")
            if command is not None:
                report["failureCommand"] = command
            if command is not None and journey.failure_clock is not None:
                report["activationClockGuardObserved"] = publication_clock_guard(compose, environment)
    finally:
        signal.alarm(0)
        # Cleanup has its own bounded commands and must survive another Ctrl-C.
        for number in original_handlers:
            signal.signal(number, signal.SIG_IGN)
        stop.set()
        writer_stop.set()
        if writer is not None:
            writer.join(90)
        if sampler is not None:
            sampler.join(30)
        if failure is not None and report.get("seedResult", {}).get("status") == "passed":
            # Capture this before diagnostic queries or teardown can displace
            # the failure from the bounded log tail. Absence is not exoneration.
            report["databaseFailureEvidence"] = database_failure_evidence(compose, environment)
        if sql_plans:
            report["sqlPlans"] = {"status": "skipped"}
            if (report.get("seedResult", {}).get("status") == "passed"
                    and str(failure) != "interrupted-or-deadline"
                    and (writer is None or not writer.is_alive())
                    and (sampler is None or not sampler.is_alive())):
                try:
                    print("Catalog trial " + run_id + ": post-measurement SQL plans", flush=True)
                    report["sqlPlans"] = scale.collect_plans(local, environment, directory, port)
                except (Failure, OSError, ValueError, KeyError, TypeError) as error:
                    report["sqlPlans"] = {"status": "failed", "failure": scale.plan_failure(error)}
                    if failure is None:
                        failure = Failure("scale-plans-incomplete")
                        report.update(status="incomplete", failureStage="scale-plans", failure=str(failure))
        try:
            cleanup(state, directory, environment)
            report["cleanup"] = "passed"
            for path in [directory / "grafana-password", directory / "load-client", directory / "scale-seed", directory / "scale-plans"]:
                if path.exists():
                    path.unlink()
        except (Failure, OSError, ValueError, KeyError) as error:
            failure = error
            report["cleanup"] = "failed"
            report["status"] = "incomplete"
        try:
            local.safety.write_document(directory / "report.json", report)
        finally:
            for number, handler in original_handlers.items():
                signal.signal(number, handler)
            lock.close()
    print("Catalog trial report: " + str(directory / "report.json"), flush=True)
    if "workload" in report:
        measured = report["workload"]["measured"]
        print("Measured " + str(measured["count"]) + " requests; " + str(measured["failed"]) + " failed; elapsed " + str(round(measured["elapsedSeconds"], 3)) + " seconds. Cleanup: " + report["cleanup"], flush=True)
    if failure is not None:
        raise Failure("trial-incomplete-see-safe-report")


def benchmark():
    environment = local.isolated_environment()
    directory = local.private_directory(ROOT / "coverage/performance" / ("bench-" + secrets.token_hex(12)))
    source = source_identity(environment)
    report = {"version": 2, "status": "incomplete", "source": source, "scope": "Go runtime metric collection and exposition only"}
    try:
        measured_environment = benchmarks.metadata(local.command, environment)
        report["environment"] = measured_environment
        raw = local.command(benchmarks.ARGS, environment, 130, True)
        try:
            benchmarks.samples(raw.decode())
        except ValueError as error:
            raise Failure("benchmark-samples-incomplete-or-source-changed") from error
        if source_identity(environment) != source or benchmarks.metadata(local.command, environment) != measured_environment:
            raise Failure("benchmark-samples-incomplete-or-source-changed")
        report.update(status="passed-development-diagnostic", output=raw.decode())
    finally:
        local.safety.write_document(directory / "report.json", report)
        print("Runtime benchmark report: " + str(directory / "report.json"), flush=True)


def clean(run_id):
    if not RUN_ID.fullmatch(run_id):
        raise Failure("invalid-run-id")
    directory = ROOT / "coverage/performance" / run_id
    if not directory.is_dir():
        raise Failure("missing-run-record")
    local.private_directory(directory)
    for name in ["owner.lock", "ownership.json", "owned-compose.json"]:
        if (directory / name).is_symlink() or not (directory / name).is_file():
            raise Failure("unsafe-run-record")
    with (directory / "owner.lock").open("r+") as lock:
        try:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise Failure("run-still-active") from None
        state = local.safety.read_document(directory / "ownership.json")
        validate_state(state, run_id)
        if local.safety.read_document(directory / "owned-compose.json") != state_model(state):
            raise Failure("owned-model-changed")
        cleanup(state, directory, local.isolated_environment())
        report_path = directory / "report.json"
        if report_path.exists() and not report_path.is_symlink():
            report = local.safety.read_document(report_path)
            report["cleanup"] = "passed"
            local.safety.write_document(report_path, report)
        for name in ["grafana-password", "load-client", "scale-seed", "scale-plans"]:
            path = directory / name
            if path.is_symlink():
                raise Failure("unsafe-artifact")
            if path.exists():
                path.unlink()
    print("Verified cleanup for " + run_id + "; reports retained.")


def check_models():
    # Static interpolation/merge checks allocate no engine resources.
    environment = local.monitoring_environment(local.isolated_environment(), "/dev/null", "0", "0")
    with tempfile.TemporaryDirectory(prefix="catalog-perf-model-") as temporary:
        path = Path(temporary) / "owned-compose.json"
        for monitored in (False, True):
            project = "open-agentic-catalog-performance-check"
            expected = model("check", monitored, project)
            local.safety.write_document(path, expected)
            compose = local.compose_arguments(project, monitored)
            compose[compose.index("-p"):compose.index("-p")] = ["-f", str(ROOT / "test/performance/compose.yaml"), "-f", str(path)]
            rendered = local.safety.decode(local.command(compose + ["config", "--format", "json"], environment, 30, True))
            for network in rendered["networks"].values():
                if network.get("ipam") == {}:
                    network.pop("ipam")  # Compose may retain a reset empty object.
            if rendered["networks"] != expected["networks"]:
                raise Failure("performance-network-model-mismatch")


if __name__ == "__main__":
    try:
        if len(sys.argv) == 2 and sys.argv[1] == "clean":
            clean(os.environ.get("PERF_RUN_ID", ""))
        elif len(sys.argv) == 2 and sys.argv[1] == "check":
            check_models()
        elif len(sys.argv) == 2 and sys.argv[1] == "bench":
            benchmark()
        elif len(sys.argv) == 2:
            run(sys.argv[1])
        else:
            raise Failure("invalid-invocation")
    except (Failure, OSError, ValueError, KeyError) as error:
        print("Catalog trial failed: " + (str(error) if isinstance(error, Failure) else "local-input-error"), file=sys.stderr)
        sys.exit(1)
