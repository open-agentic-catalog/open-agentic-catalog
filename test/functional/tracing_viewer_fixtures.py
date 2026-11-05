"""Inert synthetic trace identities for isolated and HTTP fixture checks."""

PARENT = "00-" + "a" * 32 + "-" + "b" * 16 + "-01"
REPLAY_PARENT = "00-" + "2" * 32 + "-" + "4" * 16 + "-01"


def fixture_case():
    common = {"traceId": "a" * 32, "service": "open-agentic-catalog-api", "instance": "api-instance",
              "installation": "owned", "links": None}
    request = dict(common, name="HTTP POST /api/v1/federation/sources/{sourceCommand}", spanId="c" * 16, parentId="b" * 16)
    enqueue = dict(common, name="job enqueue", spanId="d" * 16, parentId="c" * 16)
    worker = dict(common, service="open-agentic-catalog-worker", instance="worker-instance", traceId="e" * 32)
    attempt = dict(worker, name="job process", spanId="f" * 16, parentId="", links=[{"traceId": "a" * 32, "spanId": "d" * 16}])
    sync = dict(worker, name="federation sync", spanId="1" * 16, parentId="f" * 16)
    replay = dict(common, name=request["name"], traceId="2" * 32, spanId="3" * 16, parentId="4" * 16)
    return {"request": request, "enqueue": enqueue, "attempt": attempt, "sync": sync, "replay": replay}


def stored_trace(values):
    """Independent Jaeger response fixture, with one process for each selected role."""
    spans, processes = [], {}
    for want in values:
        process_id = want["instance"]
        processes[process_id] = {"serviceName": want["service"], "tags": [
            {"key": "service.instance.id", "value": process_id}, {"key": "service.namespace", "value": "owned"}]}
        references = ([{"refType": "CHILD_OF", "traceID": want["traceId"], "spanID": want["parentId"]}] if want["parentId"] else [])
        references += [{"refType": "FOLLOWS_FROM", "traceID": link["traceId"], "spanID": link["spanId"]} for link in want["links"] or []]
        spans.append({"traceID": want["traceId"], "spanID": want["spanId"], "operationName": want["name"], "processID": process_id, "references": references, "startTime": 1_800_000_000_000_000, "duration": 500})
    return {"data": [{"traceID": values[0]["traceId"], "spans": spans, "processes": processes}], "errors": None}
