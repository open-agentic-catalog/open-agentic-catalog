#!/usr/bin/env python3
"""Own the default-off, webhook and OIDC Compose profiles and bounded evidence.

This is not a scenario interpreter: the ordered units below correspond to
assertions in Makefile's private test-functional-workflow recipe.
"""

from datetime import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import selectors
import shlex
import signal
import socket
import subprocess
import sys
import tempfile
import time


if not __debug__:
    raise RuntimeError("functional assertions must be enabled")


MAX_DOCUMENT = 1 << 20
MAX_CHILD_OUTPUT = 8 << 20
RUN_SECONDS = 20 * 60
CLEANUP_SECONDS = 90
STEPS = {
    "startup-contract": ["api-health", "worker-health", "served-openapi", "migration-and-restore", "development-provisioning"],
    "automatic-federation": ["source-sync-smoke", "catalog-search-smoke"],
    "packaged-smoke": ["smoke-command", "membership-smoke", "administration-smoke", "governance-smoke", "catalog-search-smoke", "publication-smoke", "public-discovery"],
    "representative-response-contracts": ["api-health", "served-openapi", "administration-smoke", "public-discovery"],
    "audit-export": ["audit-export", "static-operator-files"],
    "ard-conformance": ["public-discovery"],
    "command-drain-replay": ["administration-smoke", "process-topology"],
    "database-recovery": ["api-health", "worker-health", "worker-observability", "process-topology"],
    "automatic-housekeeping": ["process-topology"],
    "recreation-replay": ["smoke-command", "process-topology"],
    "restore-repair": ["migration-and-restore"],
    "role-restart": ["process-topology", "public-discovery"],
    "operator-workflows": ["idempotency-repair", "source-administration", "operator-diagnostics", "remote-cli"],
    "extended-core": ["current-access", "authoring-history-reads", "administration-gaps", "private-feedback", "governance-smoke", "governance-transitions", "catalog-search-smoke", "catalog-read-gaps", "publication-smoke", "publication-history", "public-discovery", "source-administration"],
    "credential-transport": ["source-sync-smoke"],
    "oidc-private-trust": ["process-topology"],
}
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")
WEBHOOK_STEPS = {
    "startup-contract": STEPS["startup-contract"],
    "packaged-smoke": STEPS["packaged-smoke"],
    "enabled-webhooks": ["optional-webhooks"],
}
WEBHOOK_CASES = [
    "signed-delivery-and-authorization", "duplicate-and-out-of-order-reconciliation",
    "retry-throttle-loss-and-readiness", "database-outage-and-dispatcher-recovery", "routine-and-emergency-signing-rotation",
    "api-test-admission", "worker-loss-and-lease-recovery",
    "encryption-key-replacement-and-old-backup", "two-workers-and-competing-tenant",
    "capacity-and-current-state-reconciliation", "prospective-delivery-retention",
    "publication-report-example",
    "endpoint-disable-and-default-off-transition",
]
OIDC_STEPS = {
    "startup-contract": STEPS["startup-contract"],
    "packaged-smoke": STEPS["packaged-smoke"],
    "independent-oidc": ["membership-smoke", "identity-provisioning", "identity-realm-operations", "public-discovery"],
}
TRACING_STEPS = {
    "startup-contract": STEPS["startup-contract"],
    "packaged-smoke": STEPS["packaged-smoke"],
    "enabled-tracing": ["process-topology", "worker-observability"],
}
FAILOVER_STEPS = {
    "startup-contract": STEPS["startup-contract"],
    "packaged-smoke": STEPS["packaged-smoke"],
    "writer-recovery": ["process-topology", "idempotency-repair", "public-discovery"],
}
FAILOVER_PROFILES = ("failover-planned", "failover-abrupt")
BACKUP_PROFILES = ("backup-restore", "pitr-recovery")
BACKUP_STEPS = {
    "startup-contract": STEPS["startup-contract"],
    "packaged-smoke": STEPS["packaged-smoke"],
    "backup-recovery": ["migration-and-restore", "audit-export", "optional-webhooks", "process-topology"],
}
PEER_STEPS = {
    "startup-contract": STEPS["startup-contract"],
    "federation-peers": ["source-sync-smoke", "catalog-search-smoke", "public-discovery", "publication-smoke"],
}
OIDC_CASES = ["provider-startup", "dedicated-mapping", "discovery-isolation", "membership-lifecycle", "shared-mapping", "realm-operations", "provider-outage", "scope-ceiling"]
OIDC_PHASES = ["setup", "provision", "provision-replay", "dedicated-startup", "capabilities", "membership", "token-rejection", "discovery", "membership-lifecycle", "shared-mapping", "realm-operations", "provider-outage", "scope-ceiling", "identity-report"]
OIDC_PHASES += ["membership-lifecycle-read"] + [
    "membership-lifecycle-" + action + suffix
    for action in ("suspend", "reactivate", "offboard")
    for suffix in ("", "-readback", "-access")
]


def oidc_progress(directory):
    path = directory / "oidc-cases.json"
    value = read_document(path) if path.exists() else {"completed": []}
    if not isinstance(value, dict) or set(value) != {"completed"}:
        raise Failure("invalid-oidc-progress")
    completed = value["completed"]
    if not isinstance(completed, list) or completed != OIDC_CASES[:len(completed)]:
        raise Failure("invalid-oidc-progress")
    return completed


def valid_oidc_identity(value):
    return (isinstance(value, dict) and set(value) == {"configurationDigest", "indexDigest"}
            and all(isinstance(item, str) and DIGEST.fullmatch(item) for item in value.values()))


WEBHOOK_WAITS = (
    "api-ready", "api-health", "delivery", "throttle-waiting", "retry-recovery", "terminal-response", "retry-exhaustion",
    "initial-housekeeping", "delivery-parent-acknowledged", "delivery-markers-elapsed", "delivery-cleanup", "test-active",
    "accepted-before-loss", "crash-disconnected", "crash-lease-expired", "crash-parent-acknowledged", "two-worker-drain", "capacity-drain", "disabled-endpoint-drain",
    "database-down", "dispatcher-recovered", "persisted-retry-metric",
    "publication-report-startup", "publication-report-activation", "publication-report-unpublish",
)
WEBHOOK_STATES = ("pending", "waiting", "succeeded", "dead_letter")
WEBHOOK_DATABASE_FIELDS = {"eventExists", "endpointExists", "eventPredatesEndpoint", "eventAcknowledged",
                           "eventLeased", "deliveryExists", "endpointActive", "eventTypeMatches"}


def valid_webhook_database(value):
    return (isinstance(value, dict) and set(value) == WEBHOOK_DATABASE_FIELDS
            and all(type(item) is bool or item is None for item in value.values()))


def valid_webhook_failure(value):
    """Allow one bounded polling observation, never arbitrary error content."""
    return (isinstance(value, dict)
            and set(value) - {"database"} == {"case", "wait", "expectedState", "observedState", "attempt", "status", "category"}
            and ("database" not in value or value["database"] == "unavailable" or valid_webhook_database(value["database"]))
            and value["case"] in WEBHOOK_CASES and value["wait"] in WEBHOOK_WAITS
            and value["expectedState"] in (None, *WEBHOOK_STATES)
            and value["observedState"] in ("unobserved", "missing", *WEBHOOK_STATES)
            and (value["attempt"] is None or type(value["attempt"]) is int and 0 <= value["attempt"] <= 10000)
            and (value["status"] is None or type(value["status"]) is int and 100 <= value["status"] <= 599)
            and value["category"] in ("timeout", "unexpected-terminal", "http", "transport", "contract",
                                      "invalid-response", "assertion", "observation"))


def webhook_evidence(directory):
    path = directory / "webhooks.json"
    value = read_document(path) if path.exists() else {"completed": []}
    if not isinstance(value, dict) or set(value) - {"crash", "publicationReport"} not in ({"completed"}, {"completed", "failure"}):
        raise Failure("invalid-webhook-progress")
    completed = value["completed"]
    if not isinstance(completed, list) or completed != WEBHOOK_CASES[:len(completed)]:
        raise Failure("invalid-webhook-progress")
    if "failure" in value and (not valid_webhook_failure(value["failure"])
                               or len(completed) >= len(WEBHOOK_CASES)
                               or value["failure"]["case"] != WEBHOOK_CASES[len(completed)]):
        raise Failure("invalid-webhook-progress")
    from webhook_crash import valid_evidence
    if ("worker-loss-and-lease-recovery" in completed and "crash" not in value
            or "crash" in value and not valid_evidence(value["crash"])):
        raise Failure("invalid-webhook-crash-evidence")
    from publication_report import valid_evidence as valid_report
    if ("publication-report-example" in completed and "publicationReport" not in value
            or "publicationReport" in value and not valid_report(value["publicationReport"])):
        raise Failure("invalid-webhook-publication-report-evidence")
    return value


def selected_steps(profile):
    if profile in ("security-passive", "security-active"):
        return {"startup-contract": ["process-topology"], "packaged-smoke": STEPS["packaged-smoke"], "security-scan": ["catalog-search-smoke"]}
    if profile == "generated-api":
        return {"startup-contract": ["process-topology"], "packaged-smoke": STEPS["packaged-smoke"], "generated-api": ["catalog-search-smoke"]}
    if profile == "peers":
        return PEER_STEPS
    if profile in BACKUP_PROFILES:
        return BACKUP_STEPS
    if profile in FAILOVER_PROFILES:
        return FAILOVER_STEPS
    if profile == "tracing":
        return TRACING_STEPS
    if profile == "default-off":
        return STEPS
    if profile == "webhooks":
        return WEBHOOK_STEPS
    if profile == "oidc":
        return OIDC_STEPS
    raise Failure("invalid-profile")


HTTP_FAILURE_CATEGORIES = ("DEPENDENCY_UNAVAILABLE", "REQUEST_DEADLINE_EXCEEDED", "INTERNAL",
                           "PUBLIC_CURSOR_STALE", "PUBLIC_CURSOR_INVALID", "CATALOG_CURSOR_STALE", "CATALOG_CURSOR_INVALID",
                           "CATALOG_CURSOR_BINDING_INVALID", "CATALOG_CURSOR_EXPIRED", "LIVE_CURSOR_INVALID", "HYBRID_CURSOR_INVALID",
                           "LIVE_WINDOW_CHANGED", "unclassified")


def http_failure_category(problem, status, path):
    """Classify exact reviewed errors without retaining response text."""
    if not isinstance(problem, dict):
        return "unclassified"
    if problem.get("code") in ("DEPENDENCY_UNAVAILABLE", "REQUEST_DEADLINE_EXCEEDED", "INTERNAL"):
        return problem["code"]
    if status == 400 and path == "/api/v1/catalog/search" and problem.get("code") == "INVALID_ARGUMENT":
        detail = problem.get("detail")
        for message, category in (("catalog cursor is invalid for this request", "CATALOG_CURSOR_BINDING_INVALID"),
                                  ("catalog cursor has expired", "CATALOG_CURSOR_EXPIRED"),
                                  ("live search cursor is invalid or expired; restart the search", "LIVE_CURSOR_INVALID"),
                                  ("hybrid cursor is invalid or expired; restart the search", "HYBRID_CURSOR_INVALID")):
            if detail == message:
                return category
        if detail in ("catalog generation changed; restart the search", "catalog cursor is stale", "hybrid search generation changed; restart the search"):
            return "CATALOG_CURSOR_STALE"
        if detail in ("catalog cursor is invalid", "catalog cursor is invalid for unified search", "catalog cursor mode is invalid", "live-only cursor mode is invalid", "live-only search cursor is invalid"):
            return "CATALOG_CURSOR_INVALID"
        if detail == "live search window changed; restart the search":
            return "LIVE_WINDOW_CHANGED"
    if status == 400 and path == "/ard/v1/search" and problem.get("errorCode") == "INVALID_ARGUMENT":
        message = problem.get("message")
        if message in ("ARD hybrid pageToken is stale; restart the search", "ARD pageToken is stale", "page token is stale"):
            return "PUBLIC_CURSOR_STALE"
        if message in ("page token is invalid", "page token is invalid for this request", "page token has expired", "live search cursor is invalid or expired; restart the search"):
            return "PUBLIC_CURSOR_INVALID"
        if message == "live search window changed; restart the search":
            return "LIVE_WINDOW_CHANGED"
    return "unclassified"


class Failure(Exception):
    """Only fixed codes and a closed optional HTTP diagnostic leave a child."""

    def __init__(self, code, http_failure=None):
        super().__init__(code)
        self.http_failure = None
        if (isinstance(http_failure, dict) and set(http_failure) == {"status", "category"}
                and type(http_failure["status"]) is int and 100 <= http_failure["status"] <= 599
                and http_failure["category"] in HTTP_FAILURE_CATEGORIES):
            self.http_failure = dict(http_failure)


def http_failure_chunk(tail, block):
    """Recognize only the existing uncaught Client assertion, across pipe reads.

    The bounded tail is transient, never report content. Require line boundaries
    so arbitrary prefixes, suffixes, exception text and partial lines cannot be
    mistaken for this closed diagnostic. It is evidence, not a root-cause claim.
    """
    value = tail + block
    diagnostic = None
    categories = b"|".join(re.escape(category.encode("ascii")) for category in HTTP_FAILURE_CATEGORIES)
    for match in re.finditer(rb"\nAssertionError: functional federation request returned HTTP ([1-5][0-9]{2}) \((" + categories + rb")\)\r?(?=\n)", value):
        diagnostic = {"status": int(match[1]), "category": match[2].decode("ascii")}
    return value[-160:], diagnostic


def http_failure_details(error):
    """Accept one complete fixed client diagnostic, without arbitrary error text."""
    if not isinstance(error, AssertionError):
        return None
    message = str(error)
    if len(message) > 128 or not message.isascii() or "\n" in message or "\r" in message:
        return None
    _, diagnostic = http_failure_chunk(b"\n", ("AssertionError: " + message + "\n").encode("ascii"))
    return diagnostic


def validate_search_page_failure(value):
    """Retain page position and a closed HTTP category, never cursors or queries."""
    fields = {"surface", "page", "continuation", "completedPages", "httpFailure"}
    if (not isinstance(value, dict) or not fields <= set(value) or set(value) - fields - {"clock"}
            or value["surface"] not in ("control", "public")
            or type(value["page"]) is not int or not 1 <= value["page"] <= 64
            or type(value["completedPages"]) is not int or value["completedPages"] != value["page"] - 1
            or type(value["continuation"]) is not bool or value["continuation"] != (value["page"] > 1)):
        raise Failure("invalid-search-page-failure")
    diagnostic = value["httpFailure"]
    if diagnostic is not None and Failure("search-page-failed", diagnostic).http_failure is None:
        raise Failure("invalid-search-page-failure")
    if "clock" in value:
        timing = value["clock"]
        if (not isinstance(timing, dict) or set(timing) != {"responseDeltaSeconds", "cursorExpiryMinusResponseDateSeconds"}
                or any(type(item) is not int or not -86400 <= item <= 86400 for item in timing.values())):
            raise Failure("invalid-search-page-failure")
    return value


PREFLIGHT_FAILURES = {
    b"Functional federation fixture did not become ready.": "fixture-readiness-timeout",
    b"Functional curl cannot verify the fixture certificate.": "curl-tls-unsupported",
}


def readiness_failure_chunk(tail, block):
    """Classify existing shell wait failures without retaining child output."""
    for message, code in PREFLIGHT_FAILURES.items():
        if re.search(rb"\n" + re.escape(message) + rb"\r?(?=\n)", tail + block):
            return code
    match = re.search(rb"\nFunctional (API|worker) (read-only readiness|dependency readiness|readiness) timed out waiting for (?:worker )?HTTP (?:200|503)(?:; last status was [0-9]{3})?\.\r?(?=\n)", tail + block)
    if match is None:
        return None
    return match[1].decode("ascii").lower() + "-" + match[2].decode("ascii").replace(" ", "-") + "-timeout"


def scenario_failure_location(scenario, error):
    """Retain only bounded source locations, never exception text or locals."""
    if scenario not in ("federation", "core", "credentials", "operators", "operations", "webhooks", "backup_restore", "pitr_recovery", "peers"):
        return None
    filename = str(Path(__file__).with_name(scenario + ".py").resolve())
    helper_name = {"backup_restore": "authorization_recovery", "peers": "peer_pagination"}.get(scenario)
    helper_filename = str(Path(__file__).with_name((helper_name or "") + ".py").resolve())
    helper_lines = []
    lines, frame = [], error.__traceback__
    for _ in range(64):
        if frame is None:
            break
        if os.path.abspath(frame.tb_frame.f_code.co_filename) == filename and 1 <= frame.tb_lineno <= 10000:
            lines.append(frame.tb_lineno)
        if helper_name and os.path.abspath(frame.tb_frame.f_code.co_filename) == helper_filename and 1 <= frame.tb_lineno <= 10000:
            helper_lines.append(frame.tb_lineno)
        frame = frame.tb_next
    if lines and helper_lines:
        return {"scenario": helper_name, "lines": helper_lines[-3:]}
    return {"scenario": scenario, "lines": lines[-3:]} if lines else None


def record_scenario_failure(scenario, error, phase=None):
    """Best-effort checkpoint a safe source location without replacing the failure."""
    location = scenario_failure_location(scenario, error)
    if location is not None and scenario == "operations" and phase in ("degraded", "recovered"):
        location["phase"] = phase
        if hasattr(error, "worker_status_failure"):
            try:
                location["workerStatus"] = validate_worker_status_failure(error.worker_status_failure)
            except Failure:
                pass  # Invalid diagnostics must not replace the original failure.
    directory = os.environ.get("CATALOG_FUNCTIONAL_STATE_DIRECTORY")
    if location is not None and directory:
        try:
            write_document(Path(directory) / "scenario-failure.json", location)
        except (OSError, Failure):
            pass  # Diagnostic failure must not replace the original failure.
    if scenario == "peers" and directory and hasattr(error, "search_page_failure"):
        try:
            value = validate_search_page_failure(error.search_page_failure)
            write_document(Path(directory) / "search-page-failure.json", value)
        except (OSError, Failure):
            pass


def read_scenario_failure(directory, profile):
    """Accept only bounded source locations belonging to this profile's scenarios."""
    path = directory / "scenario-failure.json"
    if not path.exists():
        return None
    value = read_document(path)
    expected = {"default-off": ("federation", "core", "credentials", "operators", "operations"), "webhooks": ("webhooks",), "backup-restore": ("backup_restore", "authorization_recovery"), "pitr-recovery": ("pitr_recovery", "backup_restore", "authorization_recovery"), "peers": ("peers", "peer_pagination")}.get(profile, ())
    if (not isinstance(value, dict) or not {"scenario", "lines"} <= set(value)
            or set(value) - {"scenario", "lines", "phase", "workerStatus"}
            or value["scenario"] not in expected
            or ("phase" in value and (value["scenario"] != "operations" or value["phase"] not in ("degraded", "recovered")))
            or not isinstance(value["lines"], list) or not 1 <= len(value["lines"]) <= 3
            or any(type(line) is not int or not 1 <= line <= 10000 for line in value["lines"])):
        raise Failure("invalid-scenario-location")
    if "workerStatus" in value:
        if value["scenario"] != "operations" or value.get("phase") not in ("degraded", "recovered"):
            raise Failure("invalid-scenario-location")
        validate_worker_status_failure(value["workerStatus"])
    return value


def validate_worker_status_failure(value):
    """Accept fixed states and monotonic progress, never raw worker responses."""
    if (not isinstance(value, dict) or set(value) != {"lastStatus", "observedDegraded", "samples", "elapsedMillis"}
            or type(value["samples"]) is not int or not 0 <= value["samples"] <= 1000
            or type(value["elapsedMillis"]) is not int or not 0 <= value["elapsedMillis"] <= 3600000
            or not isinstance(value["observedDegraded"], list)
            or any(name not in ("job_worker", "outbox_processor") for name in value["observedDegraded"])
            or value["observedDegraded"] != sorted(set(value["observedDegraded"]))):
        raise Failure("invalid-worker-status-evidence")
    last = value["lastStatus"]
    if last is None:
        if value["samples"] or value["observedDegraded"]:
            raise Failure("invalid-worker-status-evidence")
    elif (not isinstance(last, dict) or set(last) != {"status", "ready", "degraded", "components"}
            or last["status"] not in ("ready", "not_ready", "degraded")
            or type(last["ready"]) is not bool or type(last["degraded"]) is not bool
            or not isinstance(last["components"], dict)
            or set(last["components"]) != {"job_worker", "outbox_processor", "source_maintainer", "housekeeper", "operations_server"}
            or any(state not in ("running", "degraded") for state in last["components"].values())
            or not value["samples"]):
        raise Failure("invalid-worker-status-evidence")
    return value


def validate_job_wait_failure(value):
    """Closed diagnostic schema; never accept Job IDs, results or error text."""
    fields = {"state", "failureClass", "attempt", "attemptLimit", "cancellationRequested", "elapsedMillis"}
    if (not isinstance(value, dict) or not fields <= set(value) or set(value) - fields - {"scheduledDelayMillis"}
            or ("scheduledDelayMillis" in value and (type(value["scheduledDelayMillis"]) is not int or not -604800000 <= value["scheduledDelayMillis"] <= 604800000))
            or value["state"] not in ("queued", "running", "waiting", "succeeded", "failed", "cancelled", "dead_letter")
            or value["failureClass"] not in ("", "invalid_input", "policy_denied", "authentication", "rate_limited", "transient_upstream", "permanent_upstream", "dependency", "internal", "cancelled")
            or type(value["cancellationRequested"]) is not bool
            or any(type(value[key]) is not int or not 0 <= value[key] <= bound
                   for key, bound in (("attempt", 1000), ("attemptLimit", 1000), ("elapsedMillis", 3600000)))):
        raise Failure("invalid-job-wait-evidence")
    return value


def record_job_wait_failure(job, elapsed):
    """Best-effort last observation, without another request or a longer wait."""
    directory = os.environ.get("CATALOG_FUNCTIONAL_STATE_DIRECTORY")
    if not directory or not isinstance(job, dict):
        return
    try:
        value = {key: job.get(key) for key in ("state", "attempt", "attemptLimit", "cancellationRequested")}
        value["failureClass"] = job.get("failureClass", "")
        value["elapsedMillis"] = round(elapsed * 1000)
        # Relative persisted scheduling time is useful without retaining exact
        # timestamps, identifiers or any private Job document.
        if isinstance(job.get("notBefore"), str) and isinstance(job.get("updatedAt"), str):
            before, updated = datetime.fromisoformat(job["notBefore"]), datetime.fromisoformat(job["updatedAt"])
            if before.tzinfo is not None and updated.tzinfo is not None:
                value["scheduledDelayMillis"] = round((before - updated).total_seconds() * 1000)
        write_document(Path(directory) / "job-wait-failure.json", validate_job_wait_failure(value))
    except (OSError, Failure, ValueError, OverflowError):
        pass  # Evidence must never mask the original request/assertion failure.


def read_job_wait_failure(directory):
    """Read the bounded checkpoint before owned fixture cleanup removes it."""
    path = directory / "job-wait-failure.json"
    return validate_job_wait_failure(read_document(path)) if path.exists() else None


def closed_object(pairs):
    """Reject duplicate JSON members before conversion can discard earlier values."""
    result = {}
    for key, value in pairs:
        if key in result:
            raise Failure("duplicate-json-key")
        result[key] = value
    return result


def decode(raw):
    """Decode bounded supervisor JSON without duplicate keys or non-finite constants."""
    if len(raw) > MAX_DOCUMENT:
        raise Failure("document-limit")
    try:
        def nonfinite(_value):
            """Reject the permissive Python decoder's nonstandard numeric constants."""
            raise ValueError()
        return json.loads(raw, object_pairs_hook=closed_object, parse_constant=nonfinite)
    except (ValueError, UnicodeError) as error:
        raise Failure("invalid-json") from error


def read_document(path):
    """Read at most the supervisor document bound plus one byte before decoding."""
    with Path(path).open("rb") as source:
        return decode(source.read(MAX_DOCUMENT + 1))


def write_document(path, document):
    """Atomically replace an owned checkpoint with bounded, fsynced JSON bytes.

    The temporary file stays in the same parent for atomic rename and is removed
    on failure. Callers supply a path inside the supervisor-owned directory.
    """
    raw = json.dumps(document, sort_keys=True, separators=(",", ":")).encode() + b"\n"
    if len(raw) > MAX_DOCUMENT:
        raise Failure("report-limit")
    descriptor, name = tempfile.mkstemp(prefix=".pending-", dir=path.parent)
    try:
        with os.fdopen(descriptor, "wb") as target:
            target.write(raw)
            target.flush()
            os.fsync(target.fileno())
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def command(arguments, environment, seconds, limit=MAX_CHILD_OUTPUT, capture=False, failed_stdout=None):
    """Capture stdout only, draining and bounding both streams without hanging.

    Every child owns a process group. Timeout, output overflow and interruption
    terminate that group, including grandchildren which still hold pipe ends.
    Stderr diagnostics never become machine-readable command results.
    """
    if seconds <= 0:
        raise Failure("deadline")
    process = subprocess.Popen(arguments, env=environment, stdout=subprocess.PIPE,
                               stderr=subprocess.PIPE, start_new_session=True)
    output = bytearray()
    http_failure = None
    failure_code = "child-failed"
    count = 0
    deadline = time.monotonic() + seconds
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(process.stdout, selectors.EVENT_READ, b"\n")
            selector.register(process.stderr, selectors.EVENT_READ, b"\n")
            while selector.get_map():
                remaining = deadline - time.monotonic()
                if remaining <= 0:
                    raise Failure("deadline")
                for key, _ in selector.select(min(remaining, 0.25)):
                    block = os.read(key.fileobj.fileno(), 65536)
                    if not block:
                        selector.unregister(key.fileobj)
                        continue
                    count += len(block)
                    if count > limit:
                        raise Failure("child-output-limit")
                    failure_code = readiness_failure_chunk(key.data, block) or failure_code
                    diagnostic_tail, observed = http_failure_chunk(key.data, block)
                    selector.modify(key.fileobj, selectors.EVENT_READ, diagnostic_tail)
                    if observed is not None:
                        http_failure = observed
                    if capture and key.fileobj is process.stdout:
                        output.extend(block)
            process.wait(timeout=max(0.001, deadline - time.monotonic()))
        if process.returncode != 0:
            if capture and failed_stdout is not None:
                failed_stdout(bytes(output))
            raise Failure(failure_code, http_failure)
        return bytes(output)
    except subprocess.TimeoutExpired as error:
        raise Failure("deadline") from error
    finally:
        # A successfully exited leader can also leave an inherited pipe or
        # background process. The group is exclusively invocation-owned.
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        process.wait(timeout=5)
        process.stdout.close()
        process.stderr.close()


def digest_files(paths):
    digest = hashlib.sha256()
    total = 0
    for path in sorted(paths):
        value = Path(path)
        if value.is_symlink():
            raise Failure("source-not-regular")
        digest.update(len(path.encode()).to_bytes(8, "big") + path.encode())
        if not value.exists():
            digest.update(b"deleted")
            continue
        if not value.is_file():
            raise Failure("source-not-regular")
        digest.update(b"file" + value.stat().st_size.to_bytes(8, "big"))
        with value.open("rb") as source:
            while block := source.read(65536):
                total += len(block)
                if total > 256 << 20:
                    raise Failure("source-limit")
                digest.update(block)
    return "sha256:" + digest.hexdigest()


def inventory(steps=None):
    steps = STEPS if steps is None else steps
    data = read_document("test/functional/coverage.json")
    if not isinstance(data, dict) or set(data) != {"formatVersion", "scenarios"} or type(data["formatVersion"]) is not int:
        raise Failure("inventory-mismatch")
    families = data.get("scenarios", [])
    if not isinstance(families, list) or not all(isinstance(family, dict) for family in families):
        raise Failure("inventory-mismatch")
    names = [family.get("id") for family in families]
    required = {name for values in steps.values() for name in values}
    if data.get("formatVersion") != 1 or not names or not all(isinstance(name, str) for name in names) or len(set(names)) != len(names) or not required.issubset(names):
        raise Failure("inventory-mismatch")
    for family in families:
        if (not isinstance(family["id"], str) or not re.fullmatch(r"[a-z][a-z0-9-]{0,99}", family["id"])
                or family.get("status") not in ("covered", "partial", "missing", "not-applicable")
                or family.get("disposition") not in ("compose", "minikube", "conditional", "development-only", "lower-level-only")
                or not isinstance(family.get("gaps"), list)
                or not isinstance(family.get("surfaces"), list) or not family["surfaces"]):
            raise Failure("inventory-mismatch")
    return [{"id": item["id"], "inventoryStatus": item["status"],
             "disposition": item["disposition"], "surfaceCount": len(item["surfaces"]),
             "remainingObligations": len(item["gaps"]),
             "obligationReference": "test/functional/coverage.json#" + item["id"],
             "steps": [step for step, names in steps.items() if item["id"] in names]}
            for item in families]


def progress(directory, steps=None):
    steps = STEPS if steps is None else steps
    path = directory / "steps.json"
    completed = read_document(path) if path.exists() else []
    if not isinstance(completed, list) or completed != list(steps)[:len(completed)]:
        raise Failure("invalid-step-order")
    return completed


def mark(directory, step, steps=None):
    steps = STEPS if steps is None else steps
    completed = progress(directory, steps)
    if len(completed) == len(steps) or step != list(steps)[len(completed)]:
        raise Failure("unexpected-step")
    write_document(directory / "steps.json", completed + [step])


def retain_version(directory, raw, commit):
    write_document(directory / "version.json", version_identity(decode(raw), commit,
                   os.environ.get("CATALOG_FUNCTIONAL_VERSION", "functional"),
                   os.environ.get("CATALOG_FUNCTIONAL_SOURCE_STATE", "unknown")))


def version_identity(value, commit, version="functional", source_state="unknown"):
    """Validate and project the served build identity without file writes."""
    if (not isinstance(value, dict) or value.get("service") != "open-agentic-catalog"
            or value.get("version") != version or value.get("commit") != commit or value.get("sourceState") != source_state):
        raise Failure("build-identity-mismatch")
    # Never retain arbitrary added fields or diagnostic strings.
    result = {key: value[key] for key in ("service", "version", "commit", "sourceState")}
    for key in ("ardProfile", "aiCatalogProfile"):
        name = value.get(key)
        if not isinstance(name, str) or not re.fullmatch(r"[a-zA-Z0-9_.-]{1,100}", name):
            raise Failure("profile-identity-mismatch")
        result[key] = name
    return result


def retain_smoke(directory, raw, run_id):
    value = decode(raw)
    if (not isinstance(value, dict) or value.get("status") != "passed" or value.get("runId") != run_id
            or value.get("baseUrl") != "http://127.0.0.1:8080"):
        raise Failure("smoke-result-mismatch")
    checks, resources = value.get("checks"), value.get("resources")
    if (not isinstance(checks, list) or not 1 <= len(checks) <= 1000
            or not all(isinstance(check, str) and 0 < len(check) <= 300 for check in checks)
            or not isinstance(resources, list) or len(resources) != 3
            or not all(isinstance(item, dict) and isinstance(item.get("kind"), str) for item in resources)
            or {item.get("kind") for item in resources} != {"agent", "mcp_server", "skill"}):
        raise Failure("smoke-result-mismatch")
    summary = {"checkCount": len(checks), "resourceCount": len(resources)}
    for key in ("ardEntries", "aiCatalogEntries"):
        if type(value.get(key)) is not int or not 3 <= value[key] <= 1000:
            raise Failure("smoke-result-mismatch")
        summary[key] = value[key]
    # Do not copy check text, resource identifiers, URLs or response bodies.
    summary["resultDigest"] = "sha256:" + hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()
    write_document(directory / "smoke.json", summary)
    if os.environ.get("CATALOG_FUNCTIONAL_PROFILE") == "generated-api":
        from generated_api import retain_seed
        retain_seed(directory, resources)


def port():
    with socket.socket() as listener:
        listener.bind(("127.0.0.1", 0))
        return listener.getsockname()[1]


def verify_fixture_mount(engine, image, fixture, environment, seconds, project):
    """Prove the engine can read the fixture directory before the stack starts.

    A container engine that runs in a VM shares only some host paths. An
    unshared bind mount is empty, so later fixture failures give no cause. A
    probe that cannot run (uncached image, no engine support) is inconclusive.
    Its invocation label and name keep it within cleanup after a client timeout.
    """
    marker = fixture / ".mount-probe"
    marker.write_text("probe\n", encoding="ascii")
    try:
        listing = command(engine + ["run", "--rm", "--pull", "never", "--network", "none",
                                    "--name", project + "-mount-probe", "--label", "com.docker.compose.project=" + project,
                                    "--entrypoint", "ls",
                                    "--volume", str(fixture) + ":/probe:ro", image, "-A", "/probe"],
                          environment, seconds, capture=True, limit=4096)
    except Failure as error:
        if str(error) != "child-failed":
            raise
        return False
    finally:
        marker.unlink(missing_ok=True)
    if b".mount-probe" not in listing.split():
        raise Failure("fixture-directory-not-shared")
    return True


def compose_environment(base, suffix, fixture):
    environment = dict(base)
    index = secrets.randbelow(4096)
    selected = {
        "PYTHONDONTWRITEBYTECODE": "1",
        "PYTHONOPTIMIZE": "0",
        "IMAGE": "open-agentic-catalog:functional-" + suffix,
        "VERSION": "functional", "COMMIT": base["CATALOG_FUNCTIONAL_COMMIT"],
        "CATALOG_FUNCTIONAL_SUFFIX": suffix,
        "CATALOG_FUNCTIONAL_FIXTURE_IMAGE": "open-agentic-catalog:functional-" + suffix + "-fixture",
        "CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY": str(fixture),
        "CATALOG_COMPOSE_HTTP_PORT": str(port()),
        "CATALOG_COMPOSE_POSTGRES_PORT": "0",
        "CATALOG_COMPOSE_FIXTURE_PORT": str(port()),
        "CATALOG_COMPOSE_SUBNET": f"172.31.{index // 16}.{index % 16 * 16}/28",
        "CATALOG_COMPOSE_FEDERATION_POLICY_FILE": str(fixture / "policy.json"),
        "CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY": str(fixture / "trust"),
        "CATALOG_HTTP_REQUEST_TIMEOUT": "3s", "CATALOG_WORKER_SOURCE_MAINTENANCE_INTERVAL": "1m",
        "CATALOG_WORKER_HOUSEKEEPING_INTERVAL": "1m", "CATALOG_WORKER_TERMINAL_JOB_RETENTION": "24h",
        "CATALOG_WORKER_ACKNOWLEDGED_OUTBOX_RETENTION": "1h",
    }
    if base.get("CATALOG_FUNCTIONAL_PROFILE") in FAILOVER_PROFILES:
        selected["CATALOG_FUNCTIONAL_REPLICATION_PASSWORD"] = secrets.token_hex(24)
        selected["COMPOSE_PROFILES"] = ""
    if base.get("CATALOG_FUNCTIONAL_PROFILE") in BACKUP_PROFILES:
        selected.update(COMPOSE_PROFILES="", CATALOG_FUNCTIONAL_BACKUP_DATABASE="postgres",
                        CATALOG_FUNCTIONAL_WEBHOOK_ENABLED="true")
    if base.get("CATALOG_FUNCTIONAL_PROFILE") == "webhooks":
        selected.update(CATALOG_FUNCTIONAL_REPORT_UID=str(os.getuid()), CATALOG_FUNCTIONAL_REPORT_GID=str(os.getgid()))
    while selected["CATALOG_COMPOSE_HTTP_PORT"] == selected["CATALOG_COMPOSE_FIXTURE_PORT"]:
        selected["CATALOG_COMPOSE_FIXTURE_PORT"] = str(port())
    if base.get("CATALOG_FUNCTIONAL_PROFILE") in ("peers", *BACKUP_PROFILES):
        selected["COMPOSE_PROFILES"] = ""
        selected["CATALOG_FUNCTIONAL_ARCHIVE_UID"] = str(os.getuid())
        selected["CATALOG_FUNCTIONAL_ARCHIVE_GID"] = str(os.getgid())
        selected["CATALOG_FUNCTIONAL_ARCHIVE_IP"] = f"172.31.{index // 16}.{index % 16 * 16 + 12}"
    if base.get("CATALOG_FUNCTIONAL_PROFILE") == "peers":
        selected["CATALOG_FUNCTIONAL_PEER_HTTP_PORT"] = str(port())
        while selected["CATALOG_FUNCTIONAL_PEER_HTTP_PORT"] in (selected["CATALOG_COMPOSE_HTTP_PORT"], selected["CATALOG_COMPOSE_FIXTURE_PORT"]):
            selected["CATALOG_FUNCTIONAL_PEER_HTTP_PORT"] = str(port())
    # Make deliberately unexports normal runtime/Compose settings. These
    # private launcher inputs must survive the inner fixed workflow instead.
    selected["CATALOG_FUNCTIONAL_HTTP_PORT"] = selected["CATALOG_COMPOSE_HTTP_PORT"]
    selected["CATALOG_FUNCTIONAL_SUBNET"] = selected["CATALOG_COMPOSE_SUBNET"]
    environment.update(selected)
    return environment


def finalize(report, completed, failure, cleanup_ok):
    steps = selected_steps(report.get("profile", "default-off"))
    if report.get("profile", "default-off") == "default-off":
        from ard_checker import valid_evidence
        if not valid_evidence(report.get("ardConformance"), report.get("runId")):
            failure = failure or "missing-ard-conformance-evidence"
    if report.get("profile") == "webhooks":
        from webhook_crash import valid_evidence
        if report.get("webhookCases") != WEBHOOK_CASES:
            failure = failure or "missing-webhook-case"
        if not valid_evidence(report.get("webhookCrash")):
            failure = failure or "missing-webhook-crash-evidence"
        from publication_report import valid_evidence as valid_report
        if not valid_report(report.get("webhookPublicationReport")):
            failure = failure or "missing-webhook-publication-report-evidence"
    if not completed or completed != list(steps):
        failure = failure or "missing-required-step"
    if not report.get("images") or not report.get("servedVersion") or (report.get("profile") != "peers" and not report.get("smoke")):
        failure = failure or "missing-identity"
    if not cleanup_ok:
        failure = failure or "cleanup-failed"
    if "operationCoverage" in report:
        from operation_coverage import validate_coverage
        try:
            validate_coverage(report["operationCoverage"], report.get("profile", "default-off"))
            if any(row["contract"] == "failed" or row["outcome"] == "unexpected" for row in report["operationCoverage"]["rows"]):
                failure = failure or "operation-expectation-failed"
        except Exception:
            failure = failure or "invalid-operation-coverage"
    report["status"] = "failed" if failure else "passed"
    report["failureCode"] = failure
    report["cleanup"] = "passed" if cleanup_ok else "failed"
    report["steps"] = [{"id": step, "status": "passed" if step in completed else "not-run"} for step in steps]
    # A baseline pass can never assert complete R-02, even when all its units ran.
    report["qualification"] = "enabled-webhook-profile" if report.get("profile") == "webhooks" else "partial-default-off-baseline"
    if report.get("profile") == "tracing":
        from tracing_recovery import valid_evidence
        report["qualification"] = "otlp-tracing-profile"
        if not valid_evidence(report.get("tracingRecovery")):
            report.update(status="failed", failureCode=failure or "missing-tracing-recovery-evidence")
            return False
    if report.get("profile") == "peers":
        from peers import valid_evidence, valid_inventories, valid_contacts, valid_identity, valid_contract_failures
        from peer_pagination import valid_evidence as valid_pagination
        from peer_drafts import valid_evidence as valid_drafts
        report["qualification"] = "two-catalog-federation"
        if (not valid_evidence(report.get("peers")) or not valid_inventories(report.get("peerInventories"))
                or not valid_contacts(report.get("peerContacts")) or not valid_identity(report)
                or not valid_pagination(report.get("peerPagination")) or not valid_drafts(report.get("peerDrafts"))
                or "operationCoverage" not in report):
            failure = failure or "missing-peer-evidence"
        if "peerContractFailures" in report:
            failure = failure or ("peer-contract-failure" if valid_contract_failures(report["peerContractFailures"]) else "invalid-peer-contract-failures")
        report.update(status="failed" if failure else "passed", failureCode=failure)
        if failure:
            return False
    if report.get("profile") in BACKUP_PROFILES:
        from backup_restore import valid_evidence
        report["qualification"] = "logical-backup-recovery" if report["profile"] == "backup-restore" else "postgres-point-in-time-recovery"
        if not valid_evidence(report.get("backupRestore"), physical=report["profile"] == "pitr-recovery"):
            report.update(status="failed", failureCode=failure or "missing-backup-evidence")
            return False
    if report.get("profile") == "pitr-recovery":
        from pitr_recovery import valid_evidence as valid_pitr
        if not valid_pitr(report.get("pitrRecovery")):
            report.update(status="failed", failureCode=failure or "missing-pitr-evidence")
            return False
    if report.get("profile") in FAILOVER_PROFILES:
        from failover import valid_evidence
        report["qualification"] = "postgres-writer-recovery"
        if not valid_evidence(report.get("failover"), report["profile"]):
            report.update(status="failed", failureCode=failure or "missing-failover-evidence")
            return False
    if report.get("profile") in ("security-passive", "security-active"):
        report["qualification"] = "scoped-security-pilot"
        from security_scan import valid_evidence
        if not valid_evidence(report.get("security"), report["profile"]):
            report.update(status="failed", failureCode=failure or "missing-security-evidence")
            return False
    if report.get("profile") == "generated-api":
        from generated_api import valid_authoring, valid_evidence
        report["qualification"] = "generated-api-pilot"
        authority = report.get("operationCoverage", {}).get("denominator")
        if not valid_evidence(report.get("generatedAPI"), authority) or not valid_authoring(report.get("generatedAPI", {}).get("authoring"), authority):
            report.update(status="failed", failureCode=failure or "missing-generated-evidence")
            return False
    if report.get("profile") == "oidc":
        report["qualification"] = "independent-oidc-profile"
        if report.get("oidcCases") != OIDC_CASES or not valid_oidc_identity(report.get("oidc")):
            report.update(status="failed", failureCode=failure or "missing-provider-identity")
            return False
    return not failure


def cleanup_images(environment, profile, suffix):
    """Borrowed candidates never belong to fixture cleanup."""
    images = []
    if not environment.get("CATALOG_FUNCTIONAL_CANDIDATE_IMAGE"):
        images.append(environment["IMAGE"])
    images.append(environment["CATALOG_FUNCTIONAL_FIXTURE_IMAGE"])
    if profile in ("security-passive", "security-active"):
        images.append("open-agentic-catalog-ui:functional-" + suffix)
    return images


class Runner:
    def __init__(self, base, profile="default-off"):
        self.steps = selected_steps(profile)
        self.base = dict(base)
        self.base["CATALOG_FUNCTIONAL_PROFILE"] = profile
        self.root = Path.cwd()
        self.suffix = secrets.token_hex(6)
        self.project = "open-agentic-catalog-functional-" + self.suffix
        self.directory = None
        self.environment = None
        self.compose = shlex.split(base.get("CATALOG_FUNCTIONAL_COMPOSE_COMMAND", "docker compose"))
        self.engine = shlex.split(base.get("CATALOG_FUNCTIONAL_ENGINE_COMMAND", "docker"))
        self.make = shlex.split(base.get("CATALOG_FUNCTIONAL_MAKE_COMMAND", "make"))
        if not self.compose or not self.engine or not self.make:
            raise Failure("empty-command")
        report_directory = self.root / "output" / "functional" / ("run-" + self.suffix)
        report_directory.mkdir(mode=0o700, parents=True, exist_ok=False)
        self.report_path = report_directory / "report.json"
        self.report = {"formatVersion": 1, "runId": "functional-" + self.suffix,
                       "profile": profile, "status": "incomplete",
                       "qualification": "partial-default-off-baseline", "cleanup": "not-run",
                       "steps": [{"id": name, "status": "not-run"} for name in self.steps]}
        write_document(self.report_path, self.report)

    def execute(self):
        deadline = time.monotonic() + RUN_SECONDS
        # Allocate under the checkout, not the system temporary directory. A
        # container engine that runs in a VM, such as Colima, shares the home
        # directory but not the host's per-user temporary directory.
        self.directory = Path(tempfile.mkdtemp(prefix="open-agentic-catalog-functional-", dir=self.report_path.parent))
        self.fixture = self.directory / "fixture"
        self.fixture.mkdir(mode=0o700)
        self.state = self.directory / "state"
        self.state.mkdir(mode=0o700)
        commit = command(["git", "rev-parse", "HEAD"], self.base, 10, capture=True).decode().strip()
        if not re.fullmatch(r"[0-9a-f]{40,64}", commit):
            raise Failure("invalid-source-identity")
        paths = command(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"], self.base, 10, capture=True)
        self.report["source"] = {"commit": commit, "worktreeDigest": digest_files(set(paths.decode().rstrip("\0").split("\0")))}
        self.report["inventoryDigest"] = digest_files(["test/functional/coverage.json"])
        self.report["contractDigest"] = digest_files(["api/openapi/control-v1.yaml"])
        self.report["configurationDigest"] = digest_files(["config/postgres.json", "compose.yaml", "test/functional/compose.yaml", "test/functional/oidc-trust.compose.yaml", "test/functional/compose.oidc.yaml", "test/functional/compose.credentials.yaml", "test/functional/core.py", "test/functional/feedback.py", "test/functional/credentials.py", "test/functional/operators.py", "test/functional/operations.py", "test/functional/journeys.py", "test/functional/response_contract.py", "test/functional/oidc-realm.json", "test/functional/oidc.py", "test/functional/webhooks.compose.yaml", "test/functional/webhooks.py", "test/functional/webhook_crash.py", "test/functional/publication_report.py", "examples/publication-report/go.mod", "examples/publication-report/schema.sql", "test/functional/fixture/webhook_barrier.go", "test/functional/tracing.compose.yaml", "test/functional/tracing.py", "test/functional/tracing_recovery.py", "test/functional/tracing_viewer.py", "test/tracing/viewer.compose.yaml", "test/tracing/jaeger.yaml", "test/functional/fixture/tracing.go", "test/tracing/otlp.go", "test/functional/ard_checker.py", "test/packaging/ard_conformance.py", "schemas/upstream/ard/ard-0.91-aa3e598/conformance/bin/conformance-test", "schemas/upstream/ard/ard-0.91-aa3e598/spec/schemas/ard-entry.schema.json", "samples/cards/order-mcp.json", "test/functional/runner.py", "Makefile"])
        if self.report["profile"] in FAILOVER_PROFILES:
            self.report["failoverConfigurationDigest"] = digest_files(["test/functional/failover.compose.yaml", "test/functional/failover.py"])
        if self.report["profile"] in BACKUP_PROFILES:
            self.report["backupConfigurationDigest"] = digest_files(["test/functional/backup_restore.compose.yaml", "test/functional/backup_restore.py", "test/functional/backup_ratings.py", "test/functional/peers.py", "test/demo/main.go", "samples/packages/repository-essentials/plugin.json"])
        if self.report["profile"] == "pitr-recovery":
            self.report["pitrConfigurationDigest"] = digest_files(["test/functional/pitr_recovery.compose.yaml", "test/functional/pitr_recovery.py", "test/functional/pitr.py", "test/functional/pitr_archive.sh", "test/functional/pitr_pg_hba.conf"])
        if self.report["profile"] == "peers":
            self.report["peerConfigurationDigest"] = digest_files(["test/functional/peers.compose.yaml", "test/functional/peers.py", "test/functional/peer_pagination.py", "test/functional/peer_drafts.py", "catalog-ui/scripts/peer-journeys.mjs", "catalog-ui/scripts/draft-transfer-journeys.mjs", "test/functional/fixture/peers.go", "test/demo/main.go", "samples/packages/repository-essentials/plugin.json"])
        self.report["families"] = inventory(self.steps)
        self.base["CATALOG_FUNCTIONAL_COMMIT"] = commit
        self.compose += ["--env-file", "/dev/null", "-f", "compose.yaml", "-f", "test/functional/compose.yaml", "-p", self.project]
        if self.report["profile"] in FAILOVER_PROFILES:
            self.compose += ["-f", "test/functional/failover.compose.yaml", "--profile", "failover"]
        if self.report["profile"] == "webhooks":
            self.compose += ["-f", "test/functional/webhooks.compose.yaml", "--profile", "publication-report"]
        if self.report["profile"] in BACKUP_PROFILES:
            self.compose += ["-f", "test/functional/webhooks.compose.yaml", "-f", "test/functional/backup_restore.compose.yaml", "--profile", "backup-restore"]
        if self.report["profile"] == "pitr-recovery":
            self.compose += ["-f", "test/functional/pitr_recovery.compose.yaml", "--profile", "pitr-tools"]
        if self.report["profile"] == "peers":
            self.compose += ["-f", "test/functional/peers.compose.yaml"]
        if self.report["profile"] in ("security-passive", "security-active"):
            self.compose += ["-f", "test/security/compose.yaml"]
        self.environment = compose_environment(self.base, self.suffix, self.fixture)
        if self.base.get("CATALOG_CANDIDATE_BACKEND"):
            if self.report["profile"] != "default-off" or self.engine != ["docker"]:
                raise Failure("candidate-profile-not-supported")
            candidate = json.loads(command([sys.executable, "-B", "test/ci/candidate.py", "admit", "--backend",
                                           self.base["CATALOG_CANDIDATE_BACKEND"]], self.base, 60, capture=True))["backend"]
            self.environment.update(IMAGE=candidate["imageId"], CATALOG_FUNCTIONAL_CANDIDATE_IMAGE=candidate["imageId"],
                                    CATALOG_FUNCTIONAL_VERSION=candidate["build"]["version"],
                                    CATALOG_FUNCTIONAL_SOURCE_STATE=candidate["build"]["sourceState"])
            self.report["candidate"] = {key: candidate[key] for key in ("imageId", "build", "source", "platform")}
        self.environment["CATALOG_FUNCTIONAL_STATE_DIRECTORY"] = str(self.state)
        if self.report["profile"] == "generated-api":
            from generated_api import prepare
            prepare(self.environment)
            self.compose += ["-f", "test/functional/generated-api.compose.yaml"]
            self.report["generatedConfigurationDigest"] = digest_files(["test/functional/generated-api.compose.yaml", "test/functional/generated_api.py"])
        write_document(self.report_path, self.report)
        print(f"Functional {self.report['profile']} run {self.report['runId']}; bounded output is not copied into evidence.", flush=True)
        probe_image = re.search(r"(?m)^\s+image: (pgvector/pgvector:\S+)$", (self.root / "compose.yaml").read_text())
        if probe_image is not None:
            verify_fixture_mount(self.engine, probe_image[1], self.fixture, self.environment, min(180, deadline - time.monotonic()), self.project)
        command(self.make + ["--no-print-directory", "test-functional-workflow"] + (["CATALOG_ENV_FILE=/dev/null"] if self.report["profile"] == "pitr-recovery" else []), self.environment, deadline - time.monotonic())
        self.report["servedVersion"] = read_document(self.state / "version.json")
        if self.report["profile"] == "peers":
            from peers import valid_inventories, valid_contacts
            self.report["peers"] = read_document(self.state / "peers.json")
            self.report["peerInventories"] = read_document(self.state / "peer-inventories.json")
            self.report["peerContacts"] = read_document(self.state / "peer-contacts.json")
            from peer_pagination import valid_evidence as valid_pagination
            self.report["peerPagination"] = read_document(self.state / "peer-pagination.json")
            from peer_drafts import valid_evidence as valid_drafts
            self.report["peerDrafts"] = read_document(self.state / "peer-drafts.json")
            if not valid_drafts(self.report["peerDrafts"]):
                raise Failure("invalid-peer-draft-checkpoint")
            if not valid_inventories(self.report["peerInventories"]) or not valid_contacts(self.report["peerContacts"]) or not valid_pagination(self.report["peerPagination"]):
                raise Failure("invalid-peer-checkpoint")
        else:
            self.report["smoke"] = read_document(self.state / "smoke.json")
        if self.report["profile"] == "default-off":
            self.report["ardConformance"] = read_document(self.state / "ard-conformance.json")
        if self.report["profile"] == "oidc":
            self.report["oidc"] = read_document(self.state / "oidc.json")
        if self.report["profile"] == "generated-api":
            self.report["generatedAPI"] = read_document(self.state / "generated-api.json")
        if self.report["profile"] == "tracing":
            tracing_state = read_document(self.report_path.parent / "tracing.json")
            if tracing_state.get("phase") != "passed":
                raise Failure("incomplete-tracing-recovery")
            self.report["tracingRecovery"] = tracing_state.get("recovery")
            if self.base.get("CATALOG_TRACING_VIEWER", "off") == "on":
                viewer = read_document(self.report_path.parent / "tracing-viewer.json")
                if viewer.get("status") != "passed":
                    raise Failure("incomplete-tracing-viewer-retention")
                self.report["tracingViewer"] = viewer
        self.capture_identity(deadline)

    def capture_identity(self, deadline):
        """Retain actual owned image/source identity, including completed failed investigations."""
        if self.report["profile"] == "peers":
            self.report["peerIdentityVerified"] = False
        self.report["servedVersion"] = read_document(self.state / "version.json")
        images = {}
        services = ["api", "worker", "postgres", "federation-fixture"]
        if self.report["profile"] in ("security-passive", "security-active"):
            services += ["zap", "security-ui"]
        if self.report["profile"] == "peers":
            services += ["api-a", "worker-a", "postgres-a", "peer-archives"]
        if self.report["profile"] in BACKUP_PROFILES:
            self.report["backupRestore"] = read_document(self.state / "backup-restore.json")
            services += ["restored", "peer-archives"]
        if self.report["profile"] == "pitr-recovery":
            self.report["pitrRecovery"] = read_document(self.state / "pitr-recovery.json")
            services += ["wal-tools", "negative"]
        if self.report["profile"] == "webhooks":
            services.append("publication-report")
        if self.report["profile"] in FAILOVER_PROFILES:
            self.report["failover"] = read_document(self.state / "failover.json")
            services.append("standby")
        for service in services:
            identifier = command(self.compose + ["ps", "--all", "-q", service], self.environment, min(10, deadline - time.monotonic()), capture=True).decode().strip()
            if not re.fullmatch(r"[0-9a-f]{64}", identifier):
                raise Failure("container-identity-mismatch")
            digest = command(self.engine + ["container", "inspect", "--format", "{{.Image}}", identifier], self.environment, min(10, deadline - time.monotonic()), capture=True).decode().strip()
            if not DIGEST.fullmatch(digest):
                raise Failure("image-identity-mismatch")
            images[service] = {"configurationDigest": digest}
        built = command(self.engine + ["image", "inspect", "--format", "{{.Id}}", self.environment["IMAGE"]], self.environment, min(10, deadline - time.monotonic()), capture=True).decode().strip()
        if images["api"]["configurationDigest"] != built or images["worker"]["configurationDigest"] != built:
            raise Failure("runtime-image-mismatch")
        if self.report["profile"] == "peers" and (images["api-a"]["configurationDigest"] != built or images["worker-a"]["configurationDigest"] != built or images["peer-archives"]["configurationDigest"] != built or images["postgres"] != images["postgres-a"]):
            raise Failure("peer-image-mismatch")
        if self.report["profile"] in FAILOVER_PROFILES and images["postgres"] != images["standby"]:
            raise Failure("replication-image-mismatch")
        if self.report["profile"] in BACKUP_PROFILES and (images["postgres"] != images["restored"] or images["peer-archives"]["configurationDigest"] != built):
            raise Failure("restore-image-mismatch")
        if self.report["profile"] == "pitr-recovery" and any(images[name] != images["postgres"] for name in ("wal-tools", "negative")):
            raise Failure("pitr-image-mismatch")
        if self.report["profile"] == "webhooks" and images["publication-report"]["configurationDigest"] != built:
            raise Failure("publication-report-image-mismatch")
        self.report["images"] = images
        if self.report["profile"] in ("peers", *BACKUP_PROFILES):
            archive_key = "peerArchiveInputs" if self.report["profile"] == "peers" else "backupArchiveInputs"
            self.report[archive_key] = {name: hashlib.sha256((self.fixture / name).read_bytes()).hexdigest() for name in ("demo-archives", "archives/allowlist.json")}
        final_paths = command(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"], self.base, min(10, deadline - time.monotonic()), capture=True)
        if digest_files(set(final_paths.decode().rstrip("\0").split("\0"))) != self.report["source"]["worktreeDigest"]:
            raise Failure("source-changed-during-run")
        if self.report["profile"] == "peers":
            self.report["peerIdentityVerified"] = True

    def cleanup(self):
        deadline = time.monotonic() + CLEANUP_SECONDS
        success = True
        if self.environment is not None:
            commands = [self.compose + ["down", "--volumes", "--remove-orphans", "--timeout", "10"]]
            # Only unique tags assigned by this invocation are removable. A
            # missing tag (e.g. failed build) is not a cleanup failure.
            for arguments in commands:
                try:
                    command(arguments, self.environment, min(45, deadline - time.monotonic()))
                except (Failure, OSError):
                    success = False
            # A timed-out Docker client can leave its daemon-owned probe alive.
            # Compose does not own this one-off container; remove only its exact
            # invocation name and project label, then use the checks below.
            try:
                probe = command(self.engine + ["container", "ls", "--all", "--quiet", "--no-trunc",
                                "--filter", "label=com.docker.compose.project=" + self.project,
                                "--filter", "name=^/" + self.project + "-mount-probe$"],
                                self.environment, min(10, deadline - time.monotonic()), capture=True).decode().strip()
                if probe:
                    if not re.fullmatch(r"[0-9a-f]{64}", probe):
                        raise Failure("invalid-mount-probe-identity")
                    try:
                        command(self.engine + ["container", "rm", "--force", "--volumes", probe], self.environment, min(10, deadline - time.monotonic()))
                    except Failure as error:
                        if str(error) != "child-failed":
                            raise
                        # An auto-removed probe can disappear after listing.
                        # The independent label check still requires no residue.
            except (Failure, OSError, UnicodeError):
                success = False
            # Compose can exit successfully with profile-scoped resources left
            # behind. Check ownership labels, not just the down exit status.
            for kind in ("container", "volume", "network"):
                arguments = self.engine + [kind, "ls", "--quiet", "--filter", "label=com.docker.compose.project=" + self.project]
                if kind == "container":
                    arguments.append("--all")
                try:
                    if command(arguments, self.environment, min(10, deadline - time.monotonic()), capture=True).strip():
                        success = False
                except (Failure, OSError):
                    success = False
            images = cleanup_images(self.environment, self.report["profile"], self.suffix)
            for image in images:
                try:
                    found = command(self.engine + ["image", "ls", "--quiet", "--no-trunc", image], self.environment, min(10, deadline - time.monotonic()), capture=True)
                    if found.strip():
                        command(self.engine + ["image", "rm", image], self.environment, min(10, deadline - time.monotonic()))
                except (Failure, OSError):
                    success = False
        if self.directory is not None:
            try:
                # Bound even filesystem cleanup in its own process group. The
                # exact path was allocated by this invocation, never by input.
                command([sys.executable, "-c", "import shutil,sys; shutil.rmtree(sys.argv[1])", str(self.directory)],
                        self.base, deadline - time.monotonic())
            except (Failure, OSError):
                success = False
        return success


def run(profile="default-off"):
    runner = None
    failure = ""
    completed = []
    cleanup_ok = False
    try:
        runner = Runner(os.environ, profile)
        runner.execute()
    except Failure as error:
        failure = str(error)
        if runner is not None and error.http_failure is not None:
            runner.report["httpFailure"] = error.http_failure
    except Exception:
        # This orchestration boundary must not print an arbitrary exception
        # containing command output, URLs, credentials or private paths.
        failure = "harness-failed"
    except KeyboardInterrupt:
        failure = "interrupted"
    finally:
        # A second interrupt cannot bypass the independently bounded cleanup.
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        if runner is not None:
            try:
                completed = progress(runner.state, runner.steps) if hasattr(runner, "state") else []
                if failure and hasattr(runner, "state"):
                    location = read_scenario_failure(runner.state, runner.report["profile"])
                    if location is not None:
                        runner.report["failureLocation"] = location
                    job_wait = read_job_wait_failure(runner.state)
                    if job_wait is not None:
                        runner.report["jobWaitFailure"] = job_wait
                    search_page = runner.state / "search-page-failure.json"
                    if runner.report["profile"] == "peers" and search_page.exists():
                        runner.report["searchPageFailure"] = validate_search_page_failure(read_document(search_page))
                if runner.report["profile"] == "peers" and hasattr(runner, "state"):
                    from peers import PHASES, valid_progress, valid_inventories, valid_contacts, valid_contract_failures
                    from peer_pagination import valid_evidence as valid_pagination
                    from peer_drafts import valid_evidence as valid_drafts, valid_failure as valid_draft_failure
                    for filename, field, validator in (("peers.json", "peers", valid_progress), ("peer-inventories.json", "peerInventories", valid_inventories), ("peer-contacts.json", "peerContacts", valid_contacts), ("peer-pagination.json", "peerPagination", valid_pagination), ("peer-drafts.json", "peerDrafts", valid_drafts), ("peer-draft-failure.json", "peerDraftFailure", valid_draft_failure)):
                        checkpoint = runner.state / filename
                        if checkpoint.exists():
                            value = read_document(checkpoint)
                            if not validator(value):
                                raise Failure("invalid-peer-checkpoint")
                            runner.report[field] = value
                    contract_path = runner.state / "peer-contract-failures.json"
                    if contract_path.exists():
                        values = read_document(contract_path)
                        if not valid_contract_failures(values):
                            raise Failure("invalid-peer-contract-failure")
                        runner.report["peerContractFailures"] = values
                    path = runner.state / "peers-phase.json"
                    if path.exists():
                        phase = read_document(path)
                        if not isinstance(phase, dict) or set(phase) != {"phase"} or phase["phase"] not in PHASES:
                            raise Failure("invalid-peer-phase")
                        runner.report["lastPhase"] = phase["phase"]
                    if failure and valid_progress(runner.report.get("peers")) and len(runner.report["peers"]["completed"]) == len(PHASES):
                        runner.capture_identity(time.monotonic() + 60)
                if runner.report["profile"] == "webhooks" and hasattr(runner, "state"):
                    evidence = webhook_evidence(runner.state)
                    runner.report["webhookCases"] = evidence["completed"]
                    if "crash" in evidence:
                        runner.report["webhookCrash"] = evidence["crash"]
                    if "publicationReport" in evidence:
                        runner.report["webhookPublicationReport"] = evidence["publicationReport"]
                    if "failure" in evidence:
                        runner.report["webhookFailure"] = evidence["failure"]
                        failure = failure or "webhook-wait-failed"
                if runner.report["profile"] in ("security-passive", "security-active") and hasattr(runner, "state"):
                    path = runner.state / "security.json"
                    if path.exists():
                        runner.report["security"] = read_document(path)
                if runner.report["profile"] == "generated-api" and hasattr(runner, "state"):
                    from generated_api import retain
                    retain(runner.state, runner.report_path.parent)
                if runner.report["profile"] == "default-off" and hasattr(runner, "state"):
                    from ard_checker import valid_evidence
                    path = runner.state / "ard-conformance.json"
                    if path.exists():
                        value = read_document(path)
                        if not valid_evidence(value, runner.report["runId"]):
                            raise Failure("invalid-ard-conformance-evidence")
                        runner.report["ardConformance"] = value
                if runner.report["profile"] == "oidc" and hasattr(runner, "state"):
                    runner.report["oidcCases"] = oidc_progress(runner.state)
                    failure_path = runner.state / "oidc-failure.json"
                    if failure_path.exists():
                        phase = read_document(failure_path)
                        if not isinstance(phase, dict) or set(phase) != {"phase"} or phase["phase"] not in OIDC_PHASES:
                            raise Failure("invalid-oidc-failure-phase")
                        runner.report["failurePhase"] = phase["phase"]
                if runner.report["profile"] in FAILOVER_PROFILES and hasattr(runner, "state"):
                    from failover import PHASES
                    phase_path = runner.state / "failover-phase.json"
                    if phase_path.exists():
                        phase = read_document(phase_path)
                        if not isinstance(phase, dict) or set(phase) != {"phase"} or phase["phase"] not in PHASES:
                            raise Failure("invalid-failover-phase")
                        runner.report["failurePhase"] = phase["phase"]
                if runner.report["profile"] in BACKUP_PROFILES and hasattr(runner, "state"):
                    from backup_restore import PHASES
                    phase_path = runner.state / "backup-phase.json"
                    if phase_path.exists():
                        phase = read_document(phase_path)
                        if not isinstance(phase, dict) or set(phase) != {"phase"} or phase["phase"] not in PHASES:
                            raise Failure("invalid-backup-phase")
                        runner.report["failurePhase"] = phase["phase"]
                if runner.report["profile"] == "pitr-recovery" and hasattr(runner, "state"):
                    from pitr_recovery import STAGES
                    path = runner.state / "pitr-stage.json"
                    if path.exists():
                        stage = read_document(path)
                        if not isinstance(stage, dict) or set(stage) != {"stage"} or stage["stage"] not in STAGES:
                            raise Failure("invalid-pitr-stage")
                        runner.report["pitrStage"] = stage["stage"]
            except (Failure, OSError):
                failure = failure or "invalid-progress"
            try:
                from operation_coverage import collect
                if hasattr(runner, "state"):
                    runner.report["operationCoverage"] = collect(runner.state, runner.report["profile"])
                else:
                    failure = failure or "missing-operation-coverage"
            except Exception:
                failure = failure or "invalid-operation-coverage"
            cleanup_ok = runner.cleanup()
    if runner is None:
        print("Functional harness initialization failed.", file=sys.stderr)
        return 1
    passed = finalize(runner.report, completed, failure, cleanup_ok)
    try:
        write_document(runner.report_path, runner.report)
    except (Failure, OSError):
        print("Functional report could not be finalized; run is incomplete.", file=sys.stderr)
        return 1
    print(f"Functional {runner.report['status']}: {runner.report_path.relative_to(runner.root)}", flush=True)
    if not passed:
        print(f"Safe failure: {runner.report['failureCode']}; completed {len(completed)}/{len(runner.steps)} units.", file=sys.stderr)
    return 0 if passed else 1


def main():
    if sys.argv[1:] == ["run-pitr-prepare"]:
        from pitr import run_prepare
        return run_prepare()
    if sys.argv[1:] == ["run-failover"]:
        # Each scenario gets fresh ownership, volumes and its own final report.
        for profile in FAILOVER_PROFILES:
            def interrupt(_number, _frame):
                raise KeyboardInterrupt
            signal.signal(signal.SIGINT, interrupt)
            signal.signal(signal.SIGTERM, interrupt)
            if run(profile):
                return 1
        return 0
    if sys.argv[1:] in (["run"], ["run-webhooks"], ["run-oidc"], ["run-tracing"], ["run-backup-restore"], ["run-pitr-recovery"], ["run-peers"], ["run-generated-api"], ["run-security-passive"], ["run-security-active"]):
        def interrupt(_number, _frame):
            raise KeyboardInterrupt
        signal.signal(signal.SIGTERM, interrupt)
        return run({"run": "default-off", "run-webhooks": "webhooks", "run-oidc": "oidc", "run-tracing": "tracing", "run-backup-restore": "backup-restore", "run-pitr-recovery": "pitr-recovery", "run-peers": "peers", "run-generated-api": "generated-api", "run-security-passive": "security-passive", "run-security-active": "security-active"}[sys.argv[1]])
    directory = Path(os.environ["CATALOG_FUNCTIONAL_STATE_DIRECTORY"])
    if len(sys.argv) == 3 and sys.argv[1] == "mark":
        mark(directory, sys.argv[2], selected_steps(os.environ.get("CATALOG_FUNCTIONAL_PROFILE", "default-off")))
    elif sys.argv[1:] == ["identity"]:
        retain_version(directory, sys.stdin.buffer.read(MAX_DOCUMENT + 1), os.environ["CATALOG_FUNCTIONAL_COMMIT"])
    elif sys.argv[1:] == ["smoke"]:
        retain_smoke(directory, sys.stdin.buffer.read(MAX_DOCUMENT + 1), "functional-" + os.environ["CATALOG_FUNCTIONAL_SUFFIX"])
    else:
        raise Failure("invalid-internal-command")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (Failure, OSError, KeyError, ValueError):
        print("Functional evidence operation failed.", file=sys.stderr)
        sys.exit(1)
