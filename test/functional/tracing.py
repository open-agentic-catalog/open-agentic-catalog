#!/usr/bin/env python3
"""Qualify trace export in the supervisor-owned Compose stack, then disable it."""

import json
import os
from pathlib import Path
import re
import shlex
import subprocess
import sys
import time
import urllib.request

from federation import Client, key, wait_until, wait_job
from runner import Failure, command, write_document
from http_client import fixture_tls_context, request as bounded_request
import tracing_recovery
import tracing_viewer

PHASE = "setup"


def main():
    env = dict(os.environ)
    suffix = env["CATALOG_FUNCTIONAL_SUFFIX"]
    if not re.fullmatch(r"[0-9a-f]{12}", suffix):
        raise Failure("invalid-tracing-owner")
    fixture = Path(env["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"])
    recovery = None
    def phase(name):
        global PHASE
        PHASE = name
        write_document(Path("output/functional") / ("run-" + suffix) / "tracing.json", {"phase": name, "recovery": recovery})

    viewer_selected = tracing_viewer.selected(env)
    phase("setup")
    for name, scheme in (("CATALOG_FUNCTIONAL_BASE_URL", "http"), ("CATALOG_FUNCTIONAL_FIXTURE_URL", "https")):
        if not re.fullmatch(scheme + r"://127\.0\.0\.1:[0-9]{1,5}", env[name]):
            raise Failure("nonlocal-tracing-listener")
    env.update(IMAGE="open-agentic-catalog:functional-" + suffix, VERSION="functional",
               COMMIT=env["CATALOG_FUNCTIONAL_COMMIT"], COMPOSE_PROGRESS="quiet",
               CATALOG_COMPOSE_HTTP_PORT=env["CATALOG_FUNCTIONAL_HTTP_PORT"], CATALOG_COMPOSE_POSTGRES_PORT="0",
               CATALOG_COMPOSE_FIXTURE_PORT=env["CATALOG_FUNCTIONAL_FIXTURE_PORT"],
               CATALOG_COMPOSE_SUBNET=env["CATALOG_FUNCTIONAL_SUBNET"],
               CATALOG_COMPOSE_FEDERATION_POLICY_FILE=str(fixture / "policy.json"),
               CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY=str(fixture / "trust"))
    api = Client(env["CATALOG_FUNCTIONAL_BASE_URL"], "local-admin")
    receiver = Client(env["CATALOG_FUNCTIONAL_FIXTURE_URL"], ssl_context=fixture_tls_context(str(fixture / "trust" / "functional-ca.pem")))
    settings = {"enabled": True, "endpoint": "https://federation-fixture:8443/v1/traces", "installation": "compose-tracing-" + suffix,
                "sampleRatio": 1, "allowedAddressPrefixes": [env["CATALOG_FUNCTIONAL_SUBNET"]],
                "headersFile": "/etc/open-agentic-catalog/tracing/tracing-headers.json", "caFile": "/etc/open-agentic-catalog/tracing/trust/functional-ca.pem"}
    (fixture / "tracing-headers.json").write_text('{"Authorization":"Bearer tracing-fixture"}\n', encoding="ascii")
    (fixture / "tracing-headers.json").chmod(0o444)

    def compose(*args, enabled=True):
        env["CATALOG_FUNCTIONAL_TRACING"] = json.dumps(settings, separators=(",", ":"))
        argv = shlex.split(env.get("CATALOG_FUNCTIONAL_COMPOSE_COMMAND", "docker compose"))
        argv += ["--env-file", "/dev/null", "-f", "compose.yaml", "-f", "test/functional/compose.yaml"]
        if enabled:
            argv += ["-f", "test/functional/tracing.compose.yaml"]
        return command(argv + ["-p", "open-agentic-catalog-functional-" + suffix, *args], env, 120, capture=True)

    def ready():
        def probe():
            try:
                api.request("GET", "/readyz", retry_unavailable=True)
                compose("exec", "-T", "worker", "/usr/local/bin/catalogctl", "probe", "--url", "http://127.0.0.1:9090/readyz")
                return True
            except Exception:
                return False
        wait_until("tracing roles ready", probe)

    def spans():
        capture = receiver.request("GET", "/tracing/state")[0]
        decoded = subprocess.run([str(fixture / "trace-decode")], input=json.dumps(capture).encode(), capture_output=True, timeout=15, check=False)
        if decoded.returncode or len(decoded.stdout) > 24 << 20:
            raise Failure("invalid-packaged-otlp")
        return json.loads(decoded.stdout) or []

    assert not receiver.request("GET", "/tracing/state")[0]["batches"], "disabled process exported traces"
    phase("enable")
    compose("up", "-d", "--no-deps", "--force-recreate", "api", "worker")
    ready()
    phase("process-identity")
    api_process, _ = api.request("GET", "/api/v1/service-information")
    worker_address = compose("port", "worker", "9090").decode().strip()
    if not re.fullmatch(r"127\.0\.0\.1:[0-9]{1,5}", worker_address):
        raise Failure("nonlocal-worker-status")
    worker_process, _ = Client("http://" + worker_address).request("GET", "/status")
    assert api_process["role"] == "api" and worker_process["role"] == "worker"
    assert api_process["instanceId"] != worker_process["instanceId"]
    phase("ingress")
    parent = tracing_recovery.fresh_parent()
    api.request("GET", "/api/v1/namespaces?limit=2", headers={"Traceparent": parent, "Baggage": "private=fixture-secret", "Tracestate": "private=fixture-secret"})
    def ingress():
        return next((s for s in spans() if s["traceId"] == parent[3:35] and s["parentId"] == parent[36:52]), None)
    root = wait_until("upstream parent export", ingress)
    assert root["service"] == "open-agentic-catalog-api" and root["instance"] == api_process["instanceId"]
    phase("source")
    inspection = api.request("POST", "/api/v1/federation/source-inspections", {"url": "https://federation-fixture:8443/.well-known/ard.json", "networkProfile": "functional-fixture"}, expected=(201,), headers={"Idempotency-Key": key("trace-inspect")})[0]
    source = api.request("POST", "/api/v1/federation/sources", {"inspectionId": inspection["id"], "displayName": "Trace fixture", "retrievalMode": "indexed", "scope": {"kinds": ["agent"]}, "updateMode": "manual_review", "networkProfile": "functional-fixture", "reason": "Trace qualification"}, expected=(201,), headers={"Idempotency-Key": key("trace-source")})[0]
    sync_key = key("trace-sync")
    phase("job")
    sync_path = "/api/v1/federation/sources/" + source["id"] + ":sync"
    headers = {"Idempotency-Key": sync_key, "If-Match": '"rev-' + str(source["revision"]) + '"', "Traceparent": parent}
    queued = api.request("POST", sync_path, {}, expected=(202,), headers=headers)[0]
    completed_job = wait_job(api, queued["job"]["id"], {"succeeded"})
    replay_parent = tracing_recovery.fresh_parent()
    replay = api.request("POST", sync_path, {}, expected=(202,), headers=dict(headers, Traceparent=replay_parent))[0]
    assert queued == replay
    phase("causal-link")
    def linked():
        values = spans()
        producers = {(s["traceId"], s["spanId"]) for s in values if s["name"] == "job enqueue" and s["traceId"] == parent[3:35]}
        return next((s for s in values if s["name"] == "job process" and any((link["traceId"], link["spanId"]) in producers for link in s["links"] or [])), None)
    attempt = wait_until("durable Job causal link", linked)
    assert attempt["service"] == "open-agentic-catalog-worker" and attempt["instance"] == worker_process["instanceId"] and attempt["instance"] != root["instance"] and attempt["traceId"] != root["traceId"]
    assert any(s["name"] == "federation sync" and s["traceId"] == attempt["traceId"] for s in spans()), "revision-bound worker sync omitted its domain span"
    if viewer_selected:
        phase("viewer-retention")
        def actual_handoff():
            values = spans()
            write_document(Path("output/functional") / ("run-" + suffix) / "tracing-viewer-spans.json", values)
            try:
                return tracing_viewer.handoff(values, parent, api_process["instanceId"], worker_process["instanceId"], settings["installation"], replay_parent)
            except Failure:
                return None
        case = wait_until("actual creating request and worker spans", actual_handoff)
        capture = receiver.request("GET", "/tracing/state")[0]
        # Decode the exact batches that will be relayed, not an earlier snapshot.
        decoded = subprocess.run([str(fixture / "trace-decode")], input=json.dumps(capture).encode(), capture_output=True, timeout=15, check=False)
        if decoded.returncode or len(decoded.stdout) > 24 << 20:
            raise Failure("invalid-viewer-otlp")
        case = tracing_viewer.handoff(json.loads(decoded.stdout), parent, api_process["instanceId"], worker_process["instanceId"], settings["installation"], replay_parent)
        write_document(Path("output/functional") / ("run-" + suffix) / "tracing-viewer.json", {"status": "pending", "spans": case})
        evidence = tracing_viewer.qualify(env, command, capture, case, completed_job, source, api_process, worker_process)
        current_api, _ = api.request("GET", "/api/v1/service-information")
        current_worker, _ = Client("http://" + worker_address).request("GET", "/status")
        if current_api["instanceId"] != api_process["instanceId"] or current_worker["instanceId"] != worker_process["instanceId"]:
            raise Failure("viewer-replaced-original-catalog-process")
        write_document(Path("output/functional") / ("run-" + suffix) / "tracing-viewer.json", evidence)

    def metrics():
        status, _, raw = bounded_request(urllib.request.Request(env["CATALOG_FUNCTIONAL_BASE_URL"] + "/metrics"))
        if status != 200:
            raise Failure("tracing-metrics-unavailable")
        return {name: float(value) for name, value in re.findall(rb"^(open_agentic_catalog_(?:go_allocated_bytes_total|go_heap_bytes|trace_export_spans_total|trace_export_failures_total)) ([0-9.eE+\-]+)$", raw, re.MULTILINE)}

    def identity():
        container = compose("ps", "-q", "api").decode().strip()
        if not re.fullmatch(r"[a-f0-9]{64}", container):
            raise Failure("invalid-tracing-container")
        engine = shlex.split(env.get("CATALOG_FUNCTIONAL_ENGINE_COMMAND", "docker"))
        selected = "{{.Id}} {{.State.Pid}} {{.State.StartedAt}} {{.RestartCount}} {{.State.Running}}"
        return tracing_recovery.process_identity(command(engine + ["inspect", "--format", selected, container], env, 15, capture=True))

    recovery = tracing_recovery.qualify(api, receiver, spans=spans, metrics=metrics, identity=identity,
                                       ready=ready, restart=lambda: compose("restart", "api"), phase=phase, root=root)
    phase("disable")
    compose("up", "-d", "--no-deps", "--force-recreate", "api", "worker", enabled=False)
    ready()
    receiver.request("POST", "/tracing/control", {"reset": True}, expected=(204,))
    api.request("GET", "/api/v1/namespaces?limit=2")
    assert not receiver.request("GET", "/tracing/state")[0]["batches"]
    phase("overhead")
    samples = []
    for mode, ratio, rejected in (("off", 0, False), ("sampled", 0.1, False), ("full", 1, False), ("unavailable", 1, True)):
        settings.update(enabled=mode != "off", sampleRatio=ratio)
        compose("up", "-d", "--no-deps", "--force-recreate", "api", "worker")
        ready()
        receiver.request("POST", "/tracing/control", {"reset": True, "reject": rejected}, expected=(204,))
        # Validate the response contract once. Do not include the external
        # contract driver's process startup in the timed HTTP samples.
        api.request("GET", "/api/v1/catalog/resources?limit=10")
        measured_request = urllib.request.Request(env["CATALOG_FUNCTIONAL_BASE_URL"] + "/api/v1/catalog/resources?limit=10", headers={"Authorization": "Bearer local-admin"})
        for _ in range(20):
            if bounded_request(measured_request)[0] != 200:
                raise Failure("tracing-warmup-request-failed")
        before = metrics()
        latencies = []
        for _ in range(200):
            start = time.perf_counter()
            status, _, _ = bounded_request(measured_request)
            latencies.append((time.perf_counter() - start) * 1000)
            if status != 200:
                raise Failure("tracing-measured-request-failed")
        after = metrics()
        # Flush successful samples or give the rejected export its bounded window;
        # these waits are outside application latency/allocation measurements.
        if mode != "off":
            wait_until("trace export accounting", lambda: metrics().get(b"open_agentic_catalog_trace_export_spans_total", 0) > 0, timeout=12)
        final = metrics()
        container = compose("ps", "-q", "api").decode().strip()
        if not re.fullmatch(r"[a-f0-9]{12,64}", container):
            raise Failure("invalid-tracing-container")
        engine = shlex.split(env.get("CATALOG_FUNCTIONAL_ENGINE_COMMAND", "docker"))
        stats = json.loads(command(engine + ["stats", "--no-stream", "--format", "{{json .}}", container], env, 15, capture=True))
        latencies.sort()
        samples.append({"mode": mode, "requests": 200, "errors": 0, "p50Millis": latencies[99], "p95Millis": latencies[189],
                        "allocatedBytes": after[b"open_agentic_catalog_go_allocated_bytes_total"] - before[b"open_agentic_catalog_go_allocated_bytes_total"],
                        "heapBytes": after[b"open_agentic_catalog_go_heap_bytes"], "cpuSnapshot": stats["CPUPerc"], "memorySnapshot": stats["MemUsage"],
                        "submittedSpans": final[b"open_agentic_catalog_trace_export_spans_total"], "failedBatches": final[b"open_agentic_catalog_trace_export_failures_total"]})
        if rejected and samples[-1]["failedBatches"] < 1:
            raise Failure("missing-trace-outage-signal")
        ready()
    write_document(Path("output/functional") / ("run-" + suffix) / "tracing-overhead.json", {"samples": samples, "scope": "single-API serial warm reads; submission counters are not retained spans; CPU/memory are container snapshots"})
    receiver.request("POST", "/tracing/control", {"reject": False}, expected=(204,))
    compose("up", "-d", "--no-deps", "--force-recreate", "api", "worker", enabled=False)
    ready()
    phase("passed")
    print("Tracing Compose: parent/SQL/Job links, replay, observed export failure, unchanged-process recovery, separate restart and disabled transition passed.", flush=True)


if __name__ == "__main__":
    try:
        main()
    except Exception:
        print("Tracing Compose qualification failed; inspect the owned test stage.", file=sys.stderr)
        raise SystemExit(1) from None
