"""Retain one actual privacy-checked Catalog Job handoff in the local viewer."""

import base64
from datetime import datetime, timezone
import json
from pathlib import Path
import re
import shlex
import time
import urllib.request
import urllib.parse

from federation import wait_until
from http_client import decode, request
from runner import Failure, write_document

PROJECT = "open-agentic-catalog-trace-viewer"


def selected(environment):
    mode = environment.get("CATALOG_TRACING_VIEWER", "off")
    if mode not in ("off", "on"):
        raise Failure("invalid-tracing-viewer-selection")
    return mode == "on"


def browser_selected(environment):
    mode = environment.get("CATALOG_TRACING_BROWSER", "off")
    if mode not in ("off", "on") or (mode == "on" and not selected(environment)):
        raise Failure("invalid-tracing-browser-selection")
    return mode == "on"


def qualify_browser(environment, command, backend, case, window):
    directory = Path(environment["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"])
    filename = directory / "tracing-browser-input.json"
    # The exact identity projection was checked against the original decoded
    # exports and retained backend above. No synthetic viewer spans are made.
    write_document(filename, {"origin": backend["queryUrl"], "spans": case, "window": window})
    try:
        raw = command(["node", "test/tracing/browser.mjs", str(filename)], environment, 180, capture=True)
        result = decode(raw)
        if not isinstance(result, dict):
            raise Failure("tracing-browser-incomplete")
        controls = result.get("controls")
        rounded_window = [window[0] // 60000000 * 60000000, (window[1] + 59999999) // 60000000 * 60000000]
        if (result.get("status") != "passed" or result.get("attemptTraceId") != case["attempt"]["traceId"]
                or result.get("linkedTraceId") != case["enqueue"]["traceId"] or result.get("highlightedSpanId") != case["enqueue"]["spanId"]
                or result.get("parentSpanId") != case["request"]["spanId"] or result.get("diagnostics") != "passed"
                or result.get("wrongLinkRefused") is not True
                or result.get("searchWindowMicros") != rounded_window or not isinstance(result.get("browser"), str) or not result["browser"]
                or not isinstance(controls, list) or len(controls) != 3
                or controls[:2] != [{"selection": "wrongInstallation", "count": 0}, {"selection": "wrongService", "count": 0}]
                or not isinstance(controls[2], dict)
                or controls[2].get("selection") != "correct" or type(controls[2].get("count")) is not int or not 1 <= controls[2]["count"] <= 20):
            raise Failure("tracing-browser-incomplete")
        return result
    finally:
        filename.unlink(missing_ok=True)


def viewer_identity(value, expected_image):
    """Use only the pinned viewer's two loopback bindings, never supplied URLs."""
    if value.get("image") != expected_image or not re.fullmatch(r"[a-f0-9]{64}", value.get("id", "")):
        raise Failure("unexpected-tracing-viewer-image")
    endpoints = []
    for port in ("16686/tcp", "4318/tcp"):
        bindings = value.get("ports", {}).get(port)
        if (not isinstance(bindings, list) or len(bindings) != 1 or bindings[0].get("HostIp") != "127.0.0.1"
                or not re.fullmatch(r"[0-9]{1,5}", bindings[0].get("HostPort", ""))
                or not 1024 <= int(bindings[0]["HostPort"]) <= 65535):
            raise Failure("nonlocal-tracing-viewer-listener")
        endpoints.append("http://127.0.0.1:" + bindings[0]["HostPort"])
    if endpoints[0] == endpoints[1]:
        raise Failure("overlapping-tracing-viewer-listeners")
    return endpoints


def owned_viewer(environment, command):
    engine = shlex.split(environment.get("CATALOG_FUNCTIONAL_ENGINE_COMMAND", "docker"))
    raw = command([*engine, "ps", "--no-trunc", "--filter", "label=com.docker.compose.project=" + PROJECT,
                   "--filter", "label=com.docker.compose.service=jaeger", "--format", "{{.ID}}"], environment, 15, capture=True)
    container = raw.decode().strip()
    if not re.fullmatch(r"[a-f0-9]{64}", container):
        raise Failure("missing-owned-tracing-viewer")
    projection = '{"id":{{json .Id}},"image":{{json .Config.Image}},"ports":{{json .NetworkSettings.Ports}}}'
    raw = command([*engine, "inspect", "--format", projection, container], environment, 15, capture=True)
    value = decode(raw)
    source = Path("test/tracing/viewer.compose.yaml").read_text(encoding="ascii")
    image = re.search(r"^    image: (\S+)$", source, re.MULTILINE)
    if image is None:
        raise Failure("missing-tracing-viewer-pin")
    query, ingest = viewer_identity(value, image[1])
    return {"containerId": container, "image": image[1], "queryUrl": query, "otlpUrl": ingest}


def handoff(values, parent, api_instance, worker_instance, installation, replay_parent):
    """Select only the actual enqueue, its creating request and its attempt."""
    def choose(name, trace=None, instance=None):
        return [s for s in values if s["name"] == name and s["installation"] == installation
                and (trace is None or s["traceId"] == trace) and (instance is None or s["instance"] == instance)]
    producers = choose("job enqueue", parent[3:35], api_instance)
    if len(producers) != 1:
        raise Failure("missing-unique-tracing-enqueue")
    enqueue = producers[0]
    requests = [s for s in values if s["traceId"] == enqueue["traceId"] and s["spanId"] == enqueue["parentId"]
                and s["parentId"] == parent[36:52] and s["name"] == "HTTP POST /api/v1/federation/sources/{sourceCommand}"
                and s["service"] == "open-agentic-catalog-api" and s["instance"] == api_instance and s["installation"] == installation]
    attempts = [s for s in choose("job process", instance=worker_instance)
                if s["parentId"] == "" and s["traceId"] != enqueue["traceId"]
                and s["links"] == [{"traceId": enqueue["traceId"], "spanId": enqueue["spanId"]}]]
    if len(requests) != 1 or len(attempts) != 1 or enqueue["service"] != "open-agentic-catalog-api" or attempts[0]["service"] != "open-agentic-catalog-worker":
        raise Failure("missing-exact-tracing-cause")
    attempt = attempts[0]
    children = [s for s in choose("federation sync", attempt["traceId"], worker_instance)
                if s["parentId"] == attempt["spanId"] and s["service"] == "open-agentic-catalog-worker"]
    if len(children) != 1 or api_instance == worker_instance:
        raise Failure("missing-tracing-sync-child")
    replay = [s for s in values if s["traceId"] == replay_parent[3:35] and s["parentId"] == replay_parent[36:52]
              and s["name"] == requests[0]["name"] and s["service"] == requests[0]["service"]
              and s["instance"] == api_instance and s["installation"] == installation]
    if (replay_parent[3:35] == parent[3:35] or len(replay) != 1
            or any(s["name"] == "job enqueue" and s["traceId"] == replay_parent[3:35] for s in values)):
        raise Failure("replay-changed-tracing-cause")
    return {"request": requests[0], "enqueue": enqueue, "attempt": attempt, "sync": children[0], "replay": replay[0]}


def retained_trace(document, expected):
    """Require exact persisted IDs and process identity in the Jaeger result."""
    data = document.get("data")
    if document.get("errors") or not isinstance(data, list) or len(data) != 1:
        raise Failure("missing-retained-tracing-trace")
    trace = data[0]
    if trace.get("traceID") != expected[0]["traceId"]:
        raise Failure("wrong-retained-tracing-trace")
    for want in expected:
        spans = [s for s in trace.get("spans", []) if s.get("spanID") == want["spanId"]]
        if len(spans) != 1:
            raise Failure("missing-retained-tracing-span")
        span = spans[0]
        process = trace.get("processes", {}).get(span.get("processID"), {})
        tags = {t.get("key"): t.get("value") for t in process.get("tags", [])}
        if (span.get("traceID") != want["traceId"] or span.get("operationName") != want["name"]
                or process.get("serviceName") != want["service"] or tags.get("service.instance.id") != want["instance"]
                or tags.get("service.namespace") != want["installation"]):
            raise Failure("wrong-retained-tracing-process")
        references = span.get("references", [])
        parents = [(r.get("traceID"), r.get("spanID")) for r in references if r.get("refType") == "CHILD_OF"]
        links = [(r.get("traceID"), r.get("spanID")) for r in references if r.get("refType") == "FOLLOWS_FROM"]
        if parents != ([(want["traceId"], want["parentId"])] if want["parentId"] else []):
            raise Failure("wrong-retained-tracing-parent")
        if links != [(link["traceId"], link["spanId"]) for link in want["links"] or []]:
            raise Failure("wrong-retained-tracing-link")



def search_trace_ids(document):
    data = document.get("summaries")
    if document.get("error") or not isinstance(data, list) or len(data) > 20:
        raise Failure("invalid-tracing-viewer-search")
    ids = [trace.get("traceId", "") for trace in data]
    if any(not re.fullmatch(r"[a-f0-9]{32}", trace) for trace in ids) or len(set(ids)) != len(ids):
        raise Failure("invalid-tracing-viewer-search")
    return set(ids)


def filter_controls(query_url, case, window):
    """Calibrate negative filters, then require the exact fresh cause/attempt."""
    evidence = []
    for name in ("request", "attempt"):
        span = case[name]
        results = {}
        for selection in ("wrongInstallation", "wrongService", "correct"):
            installation = span["installation"] + "-absent" if selection == "wrongInstallation" else span["installation"]
            service = span["service"]
            if selection == "wrongService":
                service = "open-agentic-catalog-worker" if name == "request" else "open-agentic-catalog-api"
            parameters = urllib.parse.urlencode({"query.serviceName": service, "query.operationName": span["name"], "query.searchDepth": 20,
                                                 "query.startTimeMin": datetime.fromtimestamp(window[0] / 1_000_000, timezone.utc).isoformat(),
                                                 "query.startTimeMax": datetime.fromtimestamp(window[1] / 1_000_000, timezone.utc).isoformat(),
                                                 "query.attributes": json.dumps({"service.namespace": installation, "service.instance.id": span["instance"]})})
            status, _, raw = request(urllib.request.Request(query_url + "/api/v3/trace-summaries?" + parameters))
            if status != 200:
                raise Failure("tracing-viewer-filter-query-failed")
            ids = search_trace_ids(decode(raw))
            if (selection == "correct" and span["traceId"] not in ids) or (selection != "correct" and ids):
                raise Failure("tracing-viewer-filter-mismatch")
            results[selection] = len(ids)
        evidence.append({"span": name, "traceId": span["traceId"], **results})
    return evidence


def qualify(environment, command, capture, case, job, source, api_process, worker_process):
    backend = owned_viewer(environment, command)
    # The caller decodes this same capture through the existing privacy gate
    # before relay. No synthetic span, fixture auth header or browser state is sent.
    batches = capture.get("batches")
    if not isinstance(batches, list) or not 1 <= len(batches) <= 64:
        raise Failure("invalid-tracing-viewer-batches")
    payloads = [base64.b64decode(value, validate=True) for value in batches]
    if any(not 0 < len(payload) <= 1 << 20 for payload in payloads) or sum(map(len, payloads)) > 4 << 20:
        raise Failure("oversized-tracing-viewer-capture")
    deadline = time.monotonic() + 60
    for raw in payloads:
        if time.monotonic() >= deadline:
            raise Failure("tracing-viewer-ingestion-deadline")
        status, _, body = request(urllib.request.Request(backend["otlpUrl"] + "/v1/traces", data=raw,
                                                       headers={"Content-Type": "application/x-protobuf"}))
        if status != 200 or body not in (b"", b"\x0a\x00"):
            # Canonical OTLP success can omit partial_success or include its
            # empty message. Populated rejection/error responses must fail.
            raise Failure("tracing-viewer-ingestion-failed")

    stored_times = []
    def stored():
        times = []
        for group in ((case["request"], case["enqueue"]), (case["attempt"], case["sync"]), (case["replay"],)):
            status, _, raw = request(urllib.request.Request(backend["queryUrl"] + "/api/traces/" + group[0]["traceId"]))
            if status == 404:
                return False
            if status != 200:
                raise Failure("tracing-viewer-query-failed")
            document = decode(raw)
            if not document.get("data"):
                return False
            try:
                retained_trace(document, group)
            except Failure as error:
                if str(error) in ("missing-retained-tracing-span", "missing-retained-tracing-trace"):
                    return False
                raise
            for item in document["data"][0]["spans"]:
                start, duration = item.get("startTime"), item.get("duration")
                if type(start) is not int or type(duration) is not int or start <= 0 or duration < 0:
                    raise Failure("invalid-tracing-viewer-time-window")
                times.extend((start, start + duration))
        stored_times[:] = times
        return True
    wait_until("actual Jaeger trace retention", stored, timeout=60)
    window = [min(stored_times) - 1_000_000, max(stored_times) + 1_000_000]
    filters = filter_controls(backend["queryUrl"], case, window)
    result = {"status": "passed", "backend": backend, "installation": case["attempt"]["installation"],
            "job": {"id": job["id"], "kind": job["kind"], "state": job["state"]}, "sourceId": source["id"],
            "apiInstance": api_process["instanceId"], "workerInstance": worker_process["instanceId"],
            "sampleRatio": 1, "filterControls": filters, "searchWindowMicros": window, "replayUsesOriginalCause": True, "relay": "unchanged-privacy-checked-otlp", "spans": case}
    if browser_selected(environment):
        result["browser"] = qualify_browser(environment, command, backend, case, window)
    return result
