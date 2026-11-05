"""Bounded test-only operation counters and explicit report aggregation.

The Go driver owns route matching. These files contain only its contract IDs,
fixed outcome labels and counts, never HTTP content or caller-selected labels.
"""

import json
import os
from pathlib import Path
import re
import secrets
import shlex
import sys
import threading

from http_client import ClientFailure


MAX_ROWS = 4096
MAX_COUNT = 1_000_000
MAX_STREAMS = 128
OUTCOMES = {"positive", "negative", "unexpected", "unchecked", "transport"}
CONTRACTS = {"passed", "failed", "not-checked"}
_lock = threading.Lock()
_streams = {}


def enabled():
    """Require explicit opt-in to collect observations from a functional run."""
    return os.environ.get("CATALOG_FUNCTIONAL_OPERATION_COVERAGE") == "on"


def catalog_origin(base):
    """Only the supervisor-selected API origin is automatically observed.

    Optional peer frontends can explicitly opt in; worker and fixture clients
    must not be selected by route shape, token presence or loopback address.
    """
    return enabled() and base.rstrip("/") == os.environ.get("CATALOG_FUNCTIONAL_BASE_URL", "").rstrip("/")


def valid_ids(value):
    """Validate the bounded, sorted operation denominator emitted by the Go driver."""
    if (not isinstance(value, list) or not 1 <= len(value) <= 1000
            or not all(isinstance(v, str) and re.fullmatch(r"[A-Za-z][A-Za-z0-9_]{0,99}", v) for v in value)
            or value != sorted(set(value))):
        raise ClientFailure("invalid operation denominator")
    return value


def checked_observation(value, ids):
    """Check fixed observation fields, including transport-failure status semantics."""
    if (not isinstance(value, dict) or set(value) != {"operationId", "status", "contract"}
            or value["operationId"] not in ids or type(value["status"]) is not int
            or value["status"] != 0 and not 100 <= value["status"] <= 599
            or value["contract"] not in CONTRACTS
            or (value["status"] == 0) != (value["contract"] == "not-checked")):
        raise ClientFailure("invalid operation observation")
    return value


def outcome(status, expected):
    """Distinguish asserted success/error cases from unexpected or unchecked traffic."""
    if status == 0:
        return "transport"
    if expected is None:
        return "unchecked"
    if status not in expected:
        return "unexpected"
    return "positive" if 200 <= status < 400 else "negative"


def record(value, expected=None):
    """Checkpoint one process/step under a lock; no shared multiwriter file."""
    if not enabled():
        return
    from runner import progress, read_document, selected_steps, write_document
    try:
        directory = Path(os.environ["CATALOG_FUNCTIONAL_STATE_DIRECTORY"])
        ids = valid_ids(read_document(directory / "operations.json"))
        value = checked_observation(value, ids)
        steps = selected_steps(os.environ.get("CATALOG_FUNCTIONAL_PROFILE", "default-off"))
        completed = progress(directory, steps)
        if len(completed) >= len(steps):
            raise ClientFailure("operation outside active step")
        step = list(steps)[len(completed)]
        key = (str(directory), step, os.getpid())
        with _lock:
            if key not in _streams:
                _streams[key] = (directory / ("operations-" + secrets.token_hex(12) + ".json"), {})
            path, counts = _streams[key]
            row = (value["operationId"], value["status"], value["contract"], outcome(value["status"], expected))
            counts[row] = counts.get(row, 0) + 1
            if len(counts) > MAX_ROWS or counts[row] > MAX_COUNT:
                raise ClientFailure("operation counter limit")
            rows = [dict(zip(("operationId", "status", "contract", "outcome", "count"), (*k, v), strict=True))
                    for k, v in sorted(counts.items())]
            write_document(path, {"step": step, "rows": rows})
    except ClientFailure:
        raise
    except Exception:
        raise ClientFailure("operation collection failed") from None


def validate_rows(rows, ids, steps, with_step=False):
    """Reject duplicate, unbounded or contradictory per-operation counter rows."""
    if not isinstance(rows, list) or len(rows) > MAX_ROWS:
        raise ClientFailure("invalid operation rows")
    seen = set()
    for row in rows:
        keys = {"operationId", "status", "contract", "outcome", "count"} | ({"step"} if with_step else set())
        if not isinstance(row, dict) or set(row) != keys:
            raise ClientFailure("invalid operation row")
        checked_observation({k: row[k] for k in ("operationId", "status", "contract")}, ids)
        if (row["outcome"] not in OUTCOMES or type(row["count"]) is not int
                or not 1 <= row["count"] <= MAX_COUNT or with_step and row["step"] not in steps
                or (row["status"] == 0) != (row["outcome"] == "transport")
                or row["outcome"] == "positive" and not 200 <= row["status"] < 400
                or row["outcome"] == "negative" and row["status"] < 400):
            raise ClientFailure("invalid operation counters")
        key = tuple(row[k] for k in sorted(keys - {"count"}))
        if key in seen:
            raise ClientFailure("duplicate operation counter")
        seen.add(key)
    return rows


def collect(directory, profile):
    """Read only invocation-owned checkpoints, before supervisor cleanup."""
    from runner import read_document, selected_steps
    ids = valid_ids(read_document(directory / "operations.json"))
    steps = selected_steps(profile)
    paths = list(directory.glob("operations-*.json"))
    if not paths or len(paths) > MAX_STREAMS:
        raise ClientFailure("missing or excessive operation streams")
    counts = {}
    for path in paths:
        if path.is_symlink() or not re.fullmatch(r"operations-[0-9a-f]{24}\.json", path.name):
            raise ClientFailure("invalid operation stream")
        data = read_document(path)
        if not isinstance(data, dict) or set(data) != {"step", "rows"} or data["step"] not in steps:
            raise ClientFailure("invalid operation step")
        for row in validate_rows(data["rows"], ids, steps):
            key = (data["step"], row["operationId"], row["status"], row["contract"], row["outcome"])
            counts[key] = counts.get(key, 0) + row["count"]
            if len(counts) > MAX_ROWS or counts[key] > MAX_COUNT:
                raise ClientFailure("operation counter limit")
    rows = [dict(zip(("step", "operationId", "status", "contract", "outcome", "count"), (*k, v), strict=True))
            for k, v in sorted(counts.items())]
    if not rows:
        raise ClientFailure("operation collection contains no observations")
    return {"formatVersion": 1, "denominator": ids, "rows": rows,
            "unobservedCollectors": ["smoke", "remote-cli", "shell-probes", "publication-report-sdk"]}


def validate_coverage(value, profile):
    """Validate a report's observation envelope against the selected profile steps."""
    from runner import selected_steps
    if (not isinstance(value, dict)
            or set(value) != {"formatVersion", "denominator", "rows", "unobservedCollectors"}
            or type(value["formatVersion"]) is not int or value["formatVersion"] != 1
            or value["unobservedCollectors"] != ["smoke", "remote-cli", "shell-probes", "publication-report-sdk"]):
        raise ClientFailure("invalid operation coverage")
    ids = valid_ids(value["denominator"])
    if not value["rows"]:
        raise ClientFailure("operation coverage contains no observations")
    validate_rows(value["rows"], ids, selected_steps(profile), with_step=True)
    return value


def summarize(paths):
    """Union explicit compatible reports; never discover or rerun a test."""
    from runner import DIGEST, Failure, digest_files, read_document, selected_steps, version_identity
    if not 1 <= len(paths) <= 32:
        raise ClientFailure("select between one and 32 functional reports")
    inventory = read_document("test/functional/coverage.json")
    dispositions = {surface[5:]: family["disposition"] for family in inventory["scenarios"]
                    for surface in family["surfaces"] if surface.startswith("http:")}
    webhook_ids = {surface[5:] for family in inventory["scenarios"] if family["id"] == "optional-webhooks"
                   for surface in family["surfaces"] if surface.startswith("http:")}
    seen = {}
    identity = None
    observed, positive, negative = set(), set(), set()
    runs = []
    denominator = None
    passed = True
    for path in paths:
        path = Path(path)
        if path.is_symlink() or not path.is_file():
            raise ClientFailure("report must be a regular file")
        report = read_document(path)
        if not isinstance(report, dict) or type(report.get("formatVersion")) is not int or report["formatVersion"] != 1:
            raise ClientFailure("unsupported functional report")
        run_id = report.get("runId")
        if not isinstance(run_id, str) or not re.fullmatch(r"functional-[0-9a-f]{12}", run_id):
            raise ClientFailure("invalid run identity")
        if run_id in seen:
            if seen[run_id] != report:
                raise ClientFailure("conflicting duplicate run")
            continue
        seen[run_id] = report
        if (report.get("status") not in ("passed", "failed", "incomplete")
                or report.get("cleanup") not in ("passed", "failed", "not-run")):
            raise ClientFailure("invalid run result")
        if not isinstance(report.get("configurationDigest"), str) or not DIGEST.fullmatch(report["configurationDigest"]):
            raise ClientFailure("invalid configuration identity")
        coverage = validate_coverage(report.get("operationCoverage"), report.get("profile"))
        ids = coverage["denominator"]
        if set(ids) != set(dispositions):
            raise ClientFailure("operation inventory mismatch")
        if report.get("contractDigest") != digest_files(["api/openapi/control-v1.yaml"]) or report.get("inventoryDigest") != digest_files(["test/functional/coverage.json"]):
            raise ClientFailure("report contract or inventory differs from checkout")
        try:
            source = report["source"]
            image = report["images"]["api"]["configurationDigest"]
            if (not DIGEST.fullmatch(image) or not DIGEST.fullmatch(source["worktreeDigest"])
                    or not re.fullmatch(r"[0-9a-f]{40,64}", source["commit"])
                    or report["images"]["worker"]["configurationDigest"] != image):
                raise ValueError()
            served = version_identity(report["servedVersion"], source["commit"])
            current = (source, report["contractDigest"], report["inventoryDigest"], image, served)
        except (KeyError, TypeError, ValueError, Failure):
            raise ClientFailure("missing or invalid build identity") from None
        if identity is not None and identity != current:
            raise ClientFailure("incompatible report identities")
        identity, denominator = current, ids
        steps = selected_steps(report["profile"])
        expected_steps = [{"id": step, "status": "passed"} for step in steps]
        eligible = report["status"] == "passed" and report["cleanup"] == "passed" and not report.get("failureCode") and report.get("steps") == expected_steps
        if any(row["contract"] == "failed" or row["outcome"] == "unexpected" for row in coverage["rows"]):
            eligible = False
        passed = passed and eligible
        for row in coverage["rows"]:
            if row["status"]:
                observed.add(row["operationId"])
            if eligible and row["contract"] == "passed":
                if row["outcome"] == "positive":
                    positive.add(row["operationId"])
                if row["outcome"] == "negative":
                    negative.add(row["operationId"])
        runs.append({"runId": run_id, "profile": report["profile"], "status": report["status"],
                     "cleanup": report["cleanup"], "eligible": eligible,
                     "configurationDigest": report.get("configurationDigest")})
    operations = [{"operationId": key, "observed": key in observed, "expectedPositive": key in positive,
                   "expectedNegative": key in negative, "disposition": dispositions[key],
                   "disabledProfiles": [r["profile"] for r in runs if key in webhook_ids and r["profile"] not in ("webhooks", "backup-restore", "pitr-recovery")]}
                  for key in denominator]
    gaps = [item["operationId"] for item in operations if not item["expectedPositive"]
            and item["disposition"] != "lower-level-only"
            and len(item["disabledProfiles"]) != len(runs)]
    return {"formatVersion": 1, "status": "passed" if passed else "failed", "runs": runs,
            "totalOperations": len(denominator), "observedOperations": len(observed),
            "expectedPositiveOperations": len(positive), "packagedGaps": gaps, "operations": operations}


def main():
    try:
        result = summarize(shlex.split(os.environ.get("FUNCTIONAL_REPORTS", "")))
        print(json.dumps(result, sort_keys=True, indent=2))
        return 0 if result["status"] == "passed" else 1
    except Exception:
        # No raw file content, paths or arbitrary exceptions in diagnostics.
        print("Functional operation summary failed: select valid compatible reports.", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
