"""Selected fresh OTLP acceptance after observed failure on the original process."""

import math
import re
import secrets

from federation import wait_until
from runner import Failure

FAILURES = b"open_agentic_catalog_trace_export_failures_total"
SUBMITTED = b"open_agentic_catalog_trace_export_spans_total"
CHECKS = ["original-export-failed", "product-ready-during-outage", "same-process-fresh-export", "separate-restart-fresh-export"]


def counter(values, name):
    value = values.get(name)
    if type(value) not in (int, float) or not math.isfinite(value) or value < 0 or value != int(value):
        raise Failure("invalid-trace-counter")
    return int(value)


def process_identity(raw):
    # Docker's selected fields only; never inspect container environment/secrets.
    fields = raw.decode("ascii").strip().split()
    if (len(fields) != 5 or not re.fullmatch(r"[a-f0-9]{64}", fields[0])
            or not fields[1].isdigit() or int(fields[1]) < 1
            or not re.fullmatch(r"[0-9T:.Z+\-]{20,40}", fields[2])
            or not fields[3].isdigit() or fields[4] != "true"):
        raise Failure("invalid-tracing-process")
    return tuple(fields[:4])


def fresh_parent():
    return "00-" + secrets.token_hex(16) + "-" + secrets.token_hex(8) + "-01"


def matching_span(values, parent, root):
    return next((span for span in values if span["traceId"] == parent[3:35]
                 and span["parentId"] == parent[36:52] and span["name"] == root["name"]
                 and span["service"] == "open-agentic-catalog-api"
                 and span["installation"] == root["installation"]), None)


def valid_evidence(value):
    if not isinstance(value, dict) or set(value) != {"failedBefore", "failedAfter", "submittedBefore", "submittedAfter", "originalInstance", "recoveredInstance", "restartedInstance", "recoveryParent", "restartParent", "checks"}:
        return False
    if any(type(value[k]) is not int or value[k] < 0 for k in ("failedBefore", "failedAfter", "submittedBefore", "submittedAfter")):
        return False
    if any(not isinstance(value[k], str) or not re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", value[k]) for k in ("originalInstance", "recoveredInstance", "restartedInstance")):
        return False
    if any(not isinstance(value[k], str) or not re.fullmatch(r"00-[0-9a-f]{32}-[0-9a-f]{16}-01", value[k]) for k in ("recoveryParent", "restartParent")):
        return False
    return (value["failedAfter"] > value["failedBefore"] and value["submittedAfter"] > value["submittedBefore"]
            and value["originalInstance"] == value["recoveredInstance"] != value["restartedInstance"]
            and value["recoveryParent"][3:35] != value["restartParent"][3:35] and value["checks"] == CHECKS)


def qualify(api, receiver, *, spans, metrics, identity, ready, restart, phase, root):
    original = identity()
    before = metrics()
    failed_before, submitted_before = counter(before, FAILURES), counter(before, SUBMITTED)
    phase("outage")
    receiver.request("POST", "/tracing/control", {"reject": True}, expected=(204,))
    for _ in range(10):
        api.request("GET", "/api/v1/namespaces?limit=2")
    ready()

    def failed():
        values = metrics()
        if identity() != original:
            raise Failure("trace-outage-process-changed")
        failures, submitted = counter(values, FAILURES), counter(values, SUBMITTED)
        if failures < failed_before or submitted < submitted_before:
            raise Failure("trace-outage-counters-reset")
        return (failures, submitted) if failures > failed_before and submitted > submitted_before else None

    failed_after, submitted_after = wait_until("original trace export failure", failed)
    ready()
    phase("recover-original")
    receiver.request("POST", "/tracing/control", {"reject": False}, expected=(204,))
    recovery_parent = fresh_parent()
    api.request("GET", "/api/v1/namespaces?limit=2", headers={"Traceparent": recovery_parent, "Baggage": "private=fixture-secret", "Tracestate": "private=fixture-secret"})
    recovered = wait_until("original process fresh export", lambda: matching_span(spans(), recovery_parent, root))
    if recovered["instance"] != root["instance"] or identity() != original:
        raise Failure("trace-recovery-process-changed")
    ready()
    phase("restart")
    restart()
    ready()
    replacement = identity()
    if replacement[0] != original[0] or replacement[2] == original[2]:
        raise Failure("trace-restart-process-unchanged")
    restart_parent = fresh_parent()
    api.request("GET", "/api/v1/namespaces?limit=2", headers={"Traceparent": restart_parent})
    restarted = wait_until("restarted process fresh export", lambda: matching_span(spans(), restart_parent, root))
    evidence = {"failedBefore": failed_before, "failedAfter": failed_after,
                "submittedBefore": submitted_before, "submittedAfter": submitted_after,
                "originalInstance": root["instance"], "recoveredInstance": recovered["instance"],
                "restartedInstance": restarted["instance"], "recoveryParent": recovery_parent,
                "restartParent": restart_parent, "checks": list(CHECKS)}
    if not valid_evidence(evidence):
        raise Failure("invalid-trace-recovery-evidence")
    return evidence
