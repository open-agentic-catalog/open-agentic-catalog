"""Pure validation for held work in the selected abrupt-loss fixture."""
import json
import re
from urllib.parse import urlsplit


def validate_held_input(raw):
    """Validate identifiers before composing SQL; never accept caller SQL."""
    if len(raw) > 65536:
        raise ValueError("held-work-input-too-large")
    value = json.loads(raw)
    if not isinstance(value, dict) or set(value) != {"tenant", "reservation", "job", "outbox", "outboxDocument", "processes", "listeners"}:
        raise ValueError("invalid-held-work-input")
    for key in ("tenant", "reservation", "job", "outbox"):
        if not isinstance(value[key], str) or not re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", value[key]):
            raise ValueError("invalid-held-work-identifier")
    if not isinstance(value["outboxDocument"], dict) or value["outboxDocument"].get("id") != value["outbox"] or value["outboxDocument"].get("tenant_id") != value["tenant"]:
        raise ValueError("invalid-held-outbox")
    processes = value["processes"]
    if (not isinstance(processes, list) or len(processes) != 4
            or any(not isinstance(v, dict) or set(v) != {"instanceId", "startedAt"}
                   or any(not isinstance(s, str) or not s or len(s) > 128 for s in v.values()) for v in processes)
            or len({v["instanceId"] for v in processes}) != 4):
        raise ValueError("invalid-original-processes")
    listeners = value["listeners"]
    if not isinstance(listeners, list) or len(listeners) != 4 or any(not isinstance(v, str) or len(v) > 128 for v in listeners):
        raise ValueError("invalid-owned-listeners")
    for listener in listeners:
        parsed = urlsplit(listener)
        if (parsed.scheme != "http" or parsed.hostname != "127.0.0.1" or not parsed.port
                or parsed.path or parsed.query or parsed.fragment or parsed.username or parsed.password):
            raise ValueError("invalid-owned-listeners")
    if len(set(listeners)) != 4:
        raise ValueError("invalid-owned-listeners")
    return value


RECOVERY_PROOFS = (
    "activeRepairRefused", "originalRequestUnwound", "interruptedRepair", "staleRepairRefused",
    "sameKeyReplay", "currentAuthorityDenied", "logicalEvidence", "jobRecovered",
    "staleOwnerRefused", "exactOutboxAcknowledged", "originalProcesses", "publicSnapshotRetained",
)


def validate_recovery_outcomes(value):
    """Reject partial recovery even when fencing and promotion passed."""
    if (not isinstance(value, dict) or set(value) != {*RECOVERY_PROOFS, "jobAttempt", "jobOwner"}
            or any(value[key] is not True for key in RECOVERY_PROOFS)
            or type(value["jobAttempt"]) is not int or value["jobAttempt"] < 2
            or not isinstance(value["jobOwner"], str) or not 1 <= len(value["jobOwner"]) <= 128):
        raise ValueError("incomplete-recovery-outcomes")
