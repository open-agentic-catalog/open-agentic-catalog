"""Selected signed-event acceptance, forced worker loss and durable recovery."""

import hashlib
import json
import re
import shlex

from journeys import audit_once, header, namespace_state
from runner import Failure, command

CHECKS = ["accepted-in-flight", "worker-killed", "unacknowledged-without-outcome",
          "lease-expired", "same-event-redelivered", "parent-acknowledged",
          "origin-unchanged", "other-tenant-unchanged", "replacement-process"]


def valid_evidence(value):
    return (isinstance(value, dict)
            and set(value) == {"selectionDigest", "payloadDigest", "attempts", "workerExitCode", "checks"}
            and all(isinstance(value[key], str) and re.fullmatch(r"[0-9a-f]{64}", value[key])
                    for key in ("selectionDigest", "payloadDigest"))
            and type(value["attempts"]) is int and value["attempts"] == 2
            and type(value["workerExitCode"]) is int and value["workerExitCode"] == 137
            and value["checks"] == CHECKS)


def check_receiver(value, selection, phase, digest=None):
    if (not isinstance(value, dict) or set(value) != {*selection, "released", "attempts"}
            or any(value[key] != expected for key, expected in selection.items())
            or value["released"] is not (phase == "recovered")):
        raise Failure("crash-receiver-selection-invalid")
    attempts = value["attempts"]
    expected = ([False, "blocked"],) if phase == "accepted" else ([False, "disconnected"],)
    if phase == "recovered":
        expected += ([True, "released"],)
    if not isinstance(attempts, list) or len(attempts) != len(expected):
        raise Failure("crash-receiver-attempts-invalid")
    for item, (duplicate, outcome) in zip(attempts, expected, strict=True):
        if (not isinstance(item, dict) or set(item) != {"digest", "duplicate", "outcome"}
                or not isinstance(item["digest"], str) or not re.fullmatch(r"[0-9a-f]{64}", item["digest"])
                or item["duplicate"] is not duplicate or item["outcome"] != outcome
                or digest is not None and item["digest"] != digest):
            raise Failure("crash-receiver-evidence-invalid")
        digest = item["digest"]
    return digest


def snapshot(journey, tenant, endpoint, event):
    # All inputs are exact owned UUIDs; no arbitrary SQL or dependency errors enter reports.
    if not all(isinstance(value, str) and re.fullmatch(r"[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}", value)
               for value in (tenant, endpoint, event)):
        raise Failure("crash-invalid-identity")
    raw = journey.sql("SELECT json_build_object('digest',encode(sha256(convert_to(jsonb_build_array("
                      "e.id,e.tenant_id,e.event_type,e.subject,e.data,e.created_at)::text,'UTF8')),'hex'),"
                      "'acknowledged',e.published_at IS NOT NULL,'leased',e.lease_owner IS NOT NULL,"
                      "'leaseExpired',e.lease_expires_at<=clock_timestamp(),"
                      "'leaseCleared',e.lease_owner IS NULL AND e.lease_expires_at IS NULL,"
                      "'deliveryCount',(SELECT count(*) FROM webhook_deliveries d WHERE d.tenant_id=e.tenant_id "
                      "AND d.event_id=e.id AND d.endpoint_id='" + endpoint + "')) "
                      "FROM outbox_events e WHERE e.tenant_id='" + tenant + "' AND e.id='" + event + "'")
    value = json.loads(raw)
    if (set(value) != {"digest", "acknowledged", "leased", "leaseExpired", "leaseCleared", "deliveryCount"}
            or not isinstance(value["digest"], str) or not re.fullmatch(r"[0-9a-f]{64}", value["digest"])
            or any(type(value[key]) is not bool for key in ("acknowledged", "leased", "leaseCleared"))
            or value["leaseExpired"] is not None and type(value["leaseExpired"]) is not bool
            or type(value["deliveryCount"]) is not int):
        raise Failure("crash-database-observation-invalid")
    return value


def check_pending(value, *, expired):
    if (value["acknowledged"] or not value["leased"] or value["leaseCleared"]
            or value["deliveryCount"] != 0 or value["leaseExpired"] is not expired):
        raise Failure("crash-original-outcome-already-persisted")


def qualify(journey, endpoint):
    # Settle one known other-tenant control before touching the worker. Ordinary
    # operational activity is allowed; compare only this retained namespace/audit.
    other = journey.namespace(journey.other)
    audit_once(journey.other, "namespace.created", "namespace", other["id"])
    other_before = journey.other.request("GET", "/api/v1/namespaces/" + other["id"])[0]
    before = journey.process_identities()
    journey.compose("stop", "--timeout", "20", "worker")
    namespace = journey.namespace()
    event = journey.event_id(namespace)
    selection = {"endpointId": endpoint["endpoint"]["id"], "eventId": event,
                 "source": "https://github.com/open-agentic-catalog/open-agentic-catalog/tenants/" + namespace["tenantId"]}
    initial = snapshot(journey, namespace["tenantId"], selection["endpointId"], event)
    if initial["acknowledged"] or initial["leased"] or initial["deliveryCount"] != 0:
        raise Failure("crash-control-not-pristine")
    engine = shlex.split(journey.env.get("CATALOG_FUNCTIONAL_ENGINE_COMMAND", "docker"))
    journey.receiver.request("POST", "/webhooks/barrier", dict(selection, action="arm"), expected=(204,))
    def receiver():
        return journey.receiver.request("GET", "/webhooks/barrier")[0]
    failed = True
    try:
        journey.compose("start", "worker")
        def accepted():
            value = receiver()
            if not value["attempts"]:
                return None
            check_receiver(value, selection, "accepted")
            return value
        observed = journey.wait("accepted-before-loss", accepted)
        digest = check_receiver(observed, selection, "accepted")
        # The barrier cannot send a successful reply until explicitly released.
        journey.compose("kill", "--signal", "SIGKILL", "worker")
        raw = command(engine + ["inspect", "--format", '{"Running":{{.State.Running}},"ExitCode":{{.State.ExitCode}},"StartedAt":{{json .State.StartedAt}}}', before[1][0]], journey.env, 10, limit=4096, capture=True)
        killed = json.loads(raw)
        if killed["Running"] or killed["ExitCode"] != 137 or killed["StartedAt"] == before[1][2]:
            raise Failure("crash-worker-loss-not-confirmed")
        pending = snapshot(journey, namespace["tenantId"], selection["endpointId"], event)
        check_pending(pending, expired=False)
        if pending["digest"] != initial["digest"]:
            raise Failure("crash-event-changed")
        def disconnected():
            value = receiver()
            if value["attempts"][0]["outcome"] == "blocked":
                return False
            check_receiver(value, selection, "lost", digest)
            return True
        journey.wait("crash-disconnected", disconnected)
        failed = False
    finally:
        try:
            journey.receiver.request("POST", "/webhooks/barrier", dict(selection, action="release"), expected=(204,))
        except Exception:
            # Keep the original failure; the receiver timeout and supervisor
            # cleanup remain independent of this best-effort abort connection.
            if not failed:
                raise
    def expired():
        value = snapshot(journey, namespace["tenantId"], selection["endpointId"], event)
        if value["leaseExpired"] is not True:
            return False
        check_pending(value, expired=True)
        if value["digest"] != initial["digest"]:
            raise Failure("crash-event-changed")
        return True
    journey.wait("crash-lease-expired", expired)
    journey.compose("start", "worker")
    journey.ready()
    delivered = journey.delivery(endpoint, event)
    if (delivered["tenantId"] != namespace["tenantId"] or delivered["endpointId"] != selection["endpointId"]
            or delivered["eventType"] != "com.open.agentic.catalog.namespace.created.v1"
            or delivered["attempt"] != 1 or delivered["responseStatus"] != 204):
        raise Failure("crash-delivery-outcome-invalid")
    check_receiver(receiver(), selection, "recovered", digest)
    def acknowledged():
        value = snapshot(journey, namespace["tenantId"], selection["endpointId"], event)
        if not value["acknowledged"]:
            return False
        if (not value["leaseCleared"] or value["leased"] or value["deliveryCount"] != 1
                or value["digest"] != initial["digest"]):
            raise Failure("crash-parent-outcome-invalid")
        return True
    journey.wait("crash-parent-acknowledged", acknowledged)
    current, headers = journey.api.request("GET", "/api/v1/namespaces/" + namespace["id"])
    if namespace_state(current) != namespace_state(namespace) or header(headers, "ETag") != '"rev-1"':
        raise Failure("crash-origin-changed")
    audit_once(journey.api, "namespace.created", "namespace", namespace["id"])
    count = journey.sql("SELECT count(*) FROM outbox_events WHERE tenant_id='" + namespace["tenantId"] + "' AND subject='" + namespace["id"] + "' AND event_type='com.open.agentic.catalog.namespace.created.v1'")
    if count != "1":
        raise Failure("crash-origin-duplicated")
    if journey.other.request("GET", "/api/v1/namespaces/" + other["id"])[0] != other_before:
        raise Failure("crash-other-tenant-changed")
    audit_once(journey.other, "namespace.created", "namespace", other["id"])
    after = journey.process_identities()
    if after[0] != before[0] or after[1][0] != before[1][0] or after[1][1] <= 0 or after[1][2] == killed["StartedAt"]:
        raise Failure("crash-replacement-process-invalid")
    return {"selectionDigest": hashlib.sha256(json.dumps(selection, sort_keys=True).encode()).hexdigest(),
            "payloadDigest": digest, "attempts": 2, "workerExitCode": 137, "checks": CHECKS}
