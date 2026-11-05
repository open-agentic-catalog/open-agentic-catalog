#!/usr/bin/env python3
"""Check the private worker's fixed contract during existing outage phases."""

from datetime import datetime
import re
import argparse
import json
import os
from pathlib import Path
import sys
import time

from federation import Client, source_revision, sync, wait_job, wait_until
from journeys import header
from http_client import decode
from runner import record_scenario_failure


COMPONENTS = {"job_worker", "outbox_processor", "source_maintainer", "housekeeper", "operations_server"}


def check_process_info(value):
    assert isinstance(value["instanceId"], str) and re.fullmatch(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}", value["instanceId"]), "invalid process identity"
    assert type(value["uptimeSeconds"]) is int and value["uptimeSeconds"] >= 0, "invalid process uptime"
    for field in ("startedAt", "observedAt"):
        stamp = value[field]
        assert isinstance(stamp, str) and len(stamp) <= 35 and stamp.endswith("Z"), "invalid process timestamp"
        datetime.fromisoformat(stamp)
    # Clock corrections can make observation wall time precede startup time.


def check_status(value, commit, ready, expected_version="functional", source_state="unknown"):
    assert set(value) == {"status", "role", "state", "ready", "degraded", "components", "dependencies", "version", "commit", "sourceState", "housekeeping", "instanceId", "startedAt", "observedAt", "uptimeSeconds"}, "worker status fields changed"
    check_process_info(value)
    assert value["role"] == "worker" and value["state"] == "running" and value["version"] == expected_version and value["commit"] == commit, "worker build/role mismatch"
    assert value["sourceState"] == source_state, "worker source state mismatch"
    assert value["ready"] is ready and value["dependencies"] == {"postgresql": ready}, "worker dependency readiness mismatch"
    # Database readiness can recover before every periodic worker has retried.
    # Both running and degraded are valid during this outage/recovery journey;
    # startup/stopped/unknown components are not. The listener stays running.
    assert set(value["components"]) == COMPONENTS and set(value["components"].values()) <= {"running", "degraded"}, "worker component contract mismatch"
    assert value["components"]["operations_server"] == "running", "worker operations listener is not running"
    degraded = not ready or "degraded" in value["components"].values()
    expected_status = "not_ready" if not ready else "degraded" if degraded else "ready"
    assert value["status"] == expected_status and value["degraded"] is degraded, "worker degradation mismatch"
    assert set(value["housekeeping"]) == {"interval", "terminalJobRetention", "acknowledgedOutboxRetention", "batchLimit"}, "worker housekeeping fields changed"
    assert value["housekeeping"]["batchLimit"] == 100, "worker housekeeping bound changed"


def phase_complete(value, phase, observed_degraded):
    if phase == "degraded":
        # Independent jittered retries reenter running between backoffs. Both
        # components must be observed degraded in this continuously-not-ready
        # phase, but their backoffs need not coincide in one status sample.
        required = {"job_worker", "outbox_processor"}
        observed_degraded.update(name for name in required if value["components"][name] == "degraded")
        return required <= observed_degraded
    return not value["degraded"] and all(state == "running" for state in value["components"].values())


def run(url, commit, ready, phase=None, expected_version="functional", source_state="unknown"):
    api = Client(url)
    observed_degraded = set()
    started = time.monotonic()
    last_status, samples = None, 0

    def status():
        nonlocal last_status, samples
        value, headers = api.request("GET", "/status")
        check_status(value, commit, ready, expected_version, source_state)
        assert header(headers, "Cache-Control") == "no-store", "worker status is cacheable"
        last_status = {key: value[key] for key in ("status", "ready", "degraded", "components")}
        samples += 1
        return phase is None or phase_complete(value, phase, observed_degraded)

    try:
        wait_until(f"worker {phase or 'status'} component transition", status)
    except Exception as error:
        if phase in ("degraded", "recovered"):
            error.worker_status_failure = {
                "lastStatus": last_status, "samples": samples,
                "observedDegraded": sorted(observed_degraded),
                "elapsedMillis": round((time.monotonic() - started) * 1000),
            }
        raise
    version, _ = api.request("GET", "/version")
    assert set(version) == {"status", "role", "state", "degraded", "version", "commit", "sourceState"}, "worker version fields changed"
    assert version["status"] == "version" and version["role"] == "worker" and version["version"] == expected_version and version["commit"] == commit, "worker version identity mismatch"
    assert version["sourceState"] == source_state, "worker version source state mismatch"
    print("Private worker build, bounded status and dependency transition passed.")


def check_process_identity(before, after):
    # Only container ID, PID, start time and restart count are captured, never
    # the full inspect document containing deployment credentials.
    assert len(before) == len(after) == 2, "API/worker identities missing"
    assert all(len(item) == 4 and item[0] and item[1] > 0 and item[2] and item[3] >= 0 for item in before), "invalid process identity"
    assert before == after, "API or worker restarted during database recovery"


def recover_work(api, source_id):
    accepted = sync(api, source_id, source_revision(api, source_id), "database-recovery")
    if accepted["existing"]:
        # Automatic maintenance can already own a live Job. Finish that one,
        # then require a newly accepted command; do not retry until green.
        previous = wait_job(api, accepted["job"]["id"], {"succeeded", "failed", "cancelled"})
        assert previous["state"] == "succeeded", "existing recovery Job did not succeed"
        accepted = sync(api, source_id, source_revision(api, source_id), "database-recovery-fresh")
    assert accepted["existing"] is False, "recovery command did not create fresh work"
    submitted = accepted["job"]
    completed = wait_job(api, submitted["id"], {"succeeded", "failed", "cancelled"})
    assert completed["id"] == submitted["id"] and completed["state"] == "succeeded" and completed["result"].get("snapshotId"), "fresh recovery Job did not succeed"


def read_report(path):
    with Path(path).open("rb") as stream:
        return decode(stream.read((1 << 20) + 1))


def diagnostics(value, commit, role, expected_version="functional"):
    assert set(value) == {"status", "role", "version", "commit", "schema", "profiles", "operations", "configuration", "workLimits"}, "diagnostic fields changed"
    assert value["status"] == "ready" and value["role"] == role and value["version"] == expected_version and value["commit"] == commit, "diagnostic build or role mismatch"
    assert value["schema"]["startupCompatible"] is True and value["schema"]["embeddedVersion"] > 0, "diagnostic schema not compatible"
    pool = value["configuration"]["database"]
    assert pool["maxConnLifetime"] == "45m0s" and pool["maxConnIdleTime"] == "3m0s" and pool["healthCheckPeriod"] == "15s", "database pool policy not delivered"
    if role == "worker":
        worker = value["configuration"]["worker"]
        assert worker["dependencyRetryInitialDelay"] == "1.25s" and worker["dependencyRetryCeiling"] == "2.5s", "worker restart policy not delivered"
    raw = json.dumps(value)
    assert not any(secret in raw for secret in ("postgres://", "postgresql://", "local-admin", "BEGIN PRIVATE KEY")), "diagnostics leaked credentials"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--url")
    parser.add_argument("--commit", required=True)
    parser.add_argument("--version", default=os.environ.get("CATALOG_FUNCTIONAL_VERSION", "functional"))
    parser.add_argument("--source-state", choices=("clean", "modified", "unknown"), default=os.environ.get("CATALOG_FUNCTIONAL_SOURCE_STATE", "unknown"))
    parser.add_argument("--ready", choices=("yes", "no"))
    parser.add_argument("--diagnostics", choices=("api", "worker"))
    parser.add_argument("--phase", choices=("degraded", "recovered"))
    parser.add_argument("--base-url")
    parser.add_argument("--source-report")
    parser.add_argument("--before")
    parser.add_argument("--after")
    args = parser.parse_args()
    try:
        if args.diagnostics:
            diagnostics(decode(sys.stdin.buffer.read((1 << 20) + 1)), args.commit, args.diagnostics, args.version)
        elif args.before or args.after:
            if not args.before or not args.after:
                parser.error("process comparison requires --before and --after")
            check_process_identity(read_report(args.before), read_report(args.after))
        else:
            if not args.url or not args.ready:
                parser.error("worker status requires --url and --ready")
            run(args.url, args.commit, args.ready == "yes", args.phase, args.version, args.source_state)
            if args.phase == "recovered":
                if not args.base_url or not args.source_report:
                    parser.error("recovery requires --base-url and --source-report")
                api = Client(args.base_url, token=os.environ.get("CATALOG_FUNCTIONAL_ADMIN_TOKEN", "local-admin"))
                recover_work(api, read_report(args.source_report)["sourceId"])
    except Exception as error:
        phase = None if args.diagnostics or args.before or args.after else args.phase
        record_scenario_failure("operations", error, phase=phase)
        raise SystemExit("Worker operations scenario failed; private diagnostics are not emitted.") from None


if __name__ == "__main__":
    main()
