#!/usr/bin/env python3
"""Exercise automatic federation through the packaged Compose API and worker."""

import argparse
from datetime import datetime
import json
import os
import threading
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

from http_client import fixture_tls_context, ClientFailure, decode, request as bounded_request, response_date
from response_contract import validate as validate_response
from response_contract import observe as observe_response
import operation_coverage as coverage
from runner import http_failure_category, record_job_wait_failure, record_scenario_failure


class Client:
    def __init__(self, base_url, token=None, ssl_context=None, *, catalog=None):
        self.base_url = base_url.rstrip("/")
        self.token = token
        self.ssl_context = ssl_context
        self.catalog = coverage.catalog_origin(base_url) if catalog is None else catalog

    def request(self, method, path, body=None, expected=(200,), headers=None, *, retry_unavailable=False, decode_body=True):
        if retry_unavailable and (method != "GET" or path != "/readyz"):
            raise ClientFailure("readiness retry classification requires readyz")
        observed_expected = (*expected, 503) if retry_unavailable else expected
        raw = None if body is None else json.dumps(body, separators=(",", ":")).encode()
        request_headers = dict(headers or {})
        if self.token:
            request_headers["Authorization"] = "Bearer " + self.token
        if raw is not None:
            request_headers["Content-Type"] = "application/json"
        request = urllib.request.Request(self.base_url + path, data=raw, headers=request_headers, method=method)
        try:
            status, headers, payload = bounded_request(request, self.ssl_context)
        except ClientFailure:
            if coverage.enabled() and self.catalog:
                observe_response(method, path, 0, {}, b"", observed_expected)
            raise
        response_headers = dict(headers.items())
        if coverage.enabled() and self.catalog:
            observe_response(method, path, status, response_headers, payload, observed_expected)
        if status not in expected:
            # Preserve only a closed diagnostic category, never arbitrary
            # problem detail, URLs, tokens or fixture response bodies.
            category = "unclassified"
            try:
                category = http_failure_category(decode(payload), status, path)
            except ClientFailure:
                pass
            error = AssertionError(f"functional federation request returned HTTP {status} ({category})")
            error.response_date = response_date(response_headers)
            raise error
        if not (coverage.enabled() and self.catalog) and path.startswith(("/api/v1/", "/ard/v1/", "/.well-known/ard.json")):
            # Keep existing peer-response validation even when that origin is
            # outside the selected API's operation accounting.
            validate_response(method, path, status, response_headers, payload, collect=False)
        document = None
        if not decode_body:
            return payload, response_headers
        if payload:
            document = decode(payload)
        return document, response_headers


def key(label):
    return f"functional-federation-{label}-{uuid.uuid4()}"


def wait_until(label, callback, timeout=45):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        last = callback()
        if last and time.monotonic() < deadline:
            return last
        remaining = deadline - time.monotonic()
        if remaining > 0:
            time.sleep(min(0.25, remaining))
    raise AssertionError(f"timed out waiting for {label}")


def snapshot_items(api, source_id):
    document, _ = api.request("GET", f"/api/v1/federation/sources/{source_id}/snapshots?limit=50")
    return document["items"]


def observations(api, source_id, freshness="current"):
    query = urllib.parse.urlencode({"sourceId": source_id, "freshness": freshness})
    document, _ = api.request("GET", "/api/v1/observations?" + query)
    return document["items"]


def source_revision(api, source_id):
    document, _ = api.request("GET", f"/api/v1/federation/sources/{source_id}")
    return document["revision"]


def audit_events(api, action):
    query = urllib.parse.urlencode({"action": action, "limit": 100})
    document, _ = api.request("GET", "/api/v1/audit-events?" + query)
    return document["items"]


def sync(api, source_id, revision, label):
    document, _ = api.request(
        "POST",
        f"/api/v1/federation/sources/{source_id}:sync",
        {},
        expected=(202,),
        headers={"Idempotency-Key": key(label), "If-Match": f'"rev-{revision}"'},
    )
    return document


def wait_job(api, job_id, states):
    started, last = time.monotonic(), None

    def observe():
        nonlocal last
        last = api.request("GET", f"/api/v1/jobs/{job_id}")[0]
        if last["state"] in states:
            return last
        if last["state"] in ("succeeded", "failed", "cancelled", "dead_letter"):
            raise AssertionError("Job reached an unexpected terminal state")
        return None

    try:
        return wait_until("requested Job state", observe)
    except Exception:
        record_job_wait_failure(last, time.monotonic() - started)
        raise


def set_fixture(fixture, mode, delay_millis=0):
    fixture.request("POST", "/control", {"mode": mode, "delayMillis": delay_millis})


def qualify_snapshot_comparison(api, fixture, source_id, initial, recovered, partial):
    """Compare exact Registry history with no source requests or current-view changes."""
    source_before, _ = api.request("GET", f"/api/v1/federation/sources/{source_id}")
    current_before = observations(api, source_id)
    requests_before, _ = fixture.request("GET", "/state")

    def pages(before, after, view):
        items, cursor = [], ""
        for _ in range(10):
            # UUID letter case must not replace a selected snapshot or break
            # continuation. Retained entry identifiers keep their exact case.
            before_id = before["id"] if cursor else before["id"].upper()
            after_id = after["id"] if cursor else after["id"].upper()
            query = {"otherSnapshotId": after_id, "view": view, "limit": 1}
            if cursor:
                query["cursor"] = cursor
            page, _ = api.request("GET", f"/api/v1/federation/snapshots/{before_id}/comparison?" + urllib.parse.urlencode(query))
            assert page["sourceId"] == source_id and page["sourceRevision"] == initial["sourceRevision"], "comparison lost retained source binding"
            assert page["before"]["id"] == before["id"] and page["after"]["id"] == after["id"], "comparison substituted a snapshot"
            assert len(page["items"]) <= 1, "comparison exceeded its requested page"
            for row in page["items"]:
                for side, snapshot in (("before", before), ("after", after)):
                    if side in row:
                        member = row[side]
                        occurrence = member["occurrence"]
                        assert occurrence["snapshotId"] == snapshot["id"] and occurrence["observationId"] == member["observationId"], "comparison lost exact occurrence binding"
                        assert not ({"rawEntry", "normalizedEntry", "freshness", "lastSeenAt"} & member.keys()), "comparison disclosed native or mutable observation data"
            items.extend(page["items"])
            cursor = page.get("nextCursor", "")
            if not cursor:
                return items
        raise AssertionError("comparison continuation did not terminate")

    rows = pages(initial, recovered, "all")
    by_identifier = {(row.get("before") or row["after"])["identifier"]: row for row in rows}
    assert len(rows) == 2 and set(by_identifier) == {
        "urn:air:functional.example:default:one", "urn:air:functional.example:default:two"
    }, "historical comparison lost selected Registry membership"
    reused = by_identifier["urn:air:functional.example:default:one"]
    absent = by_identifier["urn:air:functional.example:default:two"]
    assert reused["change"] == "unchanged" and reused["before"]["observationId"] == reused["after"]["observationId"], "reused observation was compared through its creation snapshot"
    assert absent["change"] == "absent" and "after" not in absent and not recovered["withdrawalAuthority"], "Registry absence was overstated as withdrawal"
    assert [row["change"] for row in pages(initial, recovered, "changes")] == ["absent"], "changed-only comparison included unchanged evidence"
    assert [row["change"] for row in pages(recovered, initial, "changes")] == ["added"], "reverse comparison lost directional membership"
    assert pages(recovered, recovered, "changes") == [], "same-snapshot comparison manufactured a difference"
    problem, _ = api.request("GET", f"/api/v1/federation/snapshots/{initial['id']}/comparison?otherSnapshotId={partial['id']}", expected=(412,))
    assert problem["code"] == "PRECONDITION_FAILED", "incomplete comparison lost its stable refusal"
    source_after, _ = api.request("GET", f"/api/v1/federation/sources/{source_id}")
    requests_after, _ = fixture.request("GET", "/state")
    assert source_after == source_before and observations(api, source_id) == current_before, "comparison changed current synchronization state"
    for field in ("manifestRequests", "listRequests", "contextRequests"):
        assert requests_after[field] == requests_before[field], "historical comparison contacted its upstream source"


def check_job_completion(events, job):
    """Bind completion to one Job attempt, not a changing source-wide count."""
    matching = [event for event in events
                if event.get("details", {}).get("facts", {}).get("jobId") == job["id"]
                and event["details"]["facts"].get("jobAttempt") == job["attempt"]]
    if len(matching) != 1 or matching[0]["targetId"] != job["result"]["snapshotId"]:
        raise AssertionError("final synchronization attempt lacks one matching completion audit")


def check_retry_wait(job, failure_class):
    """Use persisted times; PostgreSQL/JSON round trips have microsecond precision."""
    delay = (datetime.fromisoformat(job["notBefore"]) - datetime.fromisoformat(job["updatedAt"])).total_seconds()
    if (job["state"] != "waiting" or job["attemptLimit"] != 7 or job["failureClass"] != failure_class
            or not 2 - 0.000001 <= delay <= 7.5 + 0.000001):
        record_job_wait_failure(job, 0)
        raise AssertionError("source hint or configured retry window was not retained")


def qualify_retry_hints(api, fixture, source_id):
    results = []
    for mode, failure_class in (("throttled", "rate_limited"), ("unavailable", "transient_upstream")):
        source, _ = api.request("GET", f"/api/v1/federation/sources/{source_id}")
        members = sorted(item["id"] for item in observations(api, source_id))
        set_fixture(fixture, mode)
        job = sync(api, source_id, source["revision"], mode)["job"]
        # Attempt two's uncapped window is 15s, so it exercises the 5.5s deployment cap.
        waiting = wait_until("second hinted source failure", lambda: (
            lambda value: value if value["state"] == "waiting" and value["attempt"] >= 2 else None
        )(api.request("GET", f"/api/v1/jobs/{job['id']}")[0]))
        check_retry_wait(waiting, failure_class)
        retained, _ = api.request("GET", f"/api/v1/federation/sources/{source_id}")
        if retained["lastCompleteSnapshotId"] != source["lastCompleteSnapshotId"] or sorted(item["id"] for item in observations(api, source_id)) != members:
            raise AssertionError("transient response replaced the complete source view")
        set_fixture(fixture, "replacement")
        recovered = wait_job(api, job["id"], {"succeeded"})
        if (recovered["id"] != job["id"] or recovered["attempt"] <= waiting["attempt"] or recovered["attemptLimit"] != 7
                or datetime.fromisoformat(recovered["updatedAt"]) < datetime.fromisoformat(waiting["notBefore"])
                or recovered.get("failureClass") or recovered.get("safeError")):
            raise AssertionError("hinted Job did not recover cleanly after its retained due time")
        check_job_completion(audit_events(api, "source.sync.completed"), recovered)
        results.append({"status": 429 if mode == "throttled" else 503, "jobId": job["id"], "attempts": recovered["attempt"], "notBefore": waiting["notBefore"]})
    return results


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--fixture-url", required=True)
    parser.add_argument("--ca-file", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument(
        "--admin-token",
        default=os.environ.get("CATALOG_FUNCTIONAL_ADMIN_TOKEN", "local-admin"),
        help="administrator bearer token (prefer CATALOG_FUNCTIONAL_ADMIN_TOKEN)",
    )
    args = parser.parse_args()

    api = Client(args.base_url, token=args.admin_token)
    fixture_context = fixture_tls_context(args.ca_file)
    fixture = Client(args.fixture_url, ssl_context=fixture_context)
    fixture.request("GET", "/healthz")

    inspection, _ = api.request(
        "POST",
        "/api/v1/federation/source-inspections",
        {"url": "https://federation-fixture:8443/.well-known/ard.json", "networkProfile": "functional-fixture"},
        expected=(201,),
        headers={"Idempotency-Key": key("inspect")},
    )
    if not inspection["capabilities"].get("list") or not inspection["capabilities"].get("enumerable"):
        raise AssertionError("inspection did not establish bounded List capability")
    if inspection.get("registryListUrl") != "https://federation-fixture:8443/ard/v1/agents":
        raise AssertionError("inspection selected the wrong List endpoint")
    retained_inspection, _ = api.request("GET", "/api/v1/federation/source-inspections/" + inspection["id"])
    assert retained_inspection == inspection, "inspection readback changed retained evidence"

    source_display_name = "Functional automatic Registry " + args.run_id
    source, _ = api.request(
        "POST",
        "/api/v1/federation/sources",
        {
            "inspectionId": inspection["id"],
            "displayName": source_display_name,
            "retrievalMode": "indexed",
            "scope": {"kinds": ["agent"]},
            "updateMode": "automatic",
            "networkProfile": "functional-fixture",
            "reason": "Approve the inspected functional source",
        },
        expected=(201,),
        headers={"Idempotency-Key": key("connect")},
    )
    source_id = source["id"]

    initial = wait_until(
        "automatic two-page Registry synchronization",
        lambda: next(
            (
                item
                for item in snapshot_items(api, source_id)
                if item["state"] == "complete"
                and item["memberCount"] == 2
                and item.get("reconcileCurrentView") is True
            ),
            None,
        ),
    )
    if initial["withdrawalAuthority"]:
        raise AssertionError("Registry List incorrectly claimed withdrawal authority")
    current = observations(api, source_id)
    if {item.get("identifier") for item in current} != {
        "urn:air:functional.example:default:one",
        "urn:air:functional.example:default:two",
    }:
        raise AssertionError("automatic List inventory was not indexed")
    for item in current:
        occurrence_page, _ = api.request(
            "GET", f"/api/v1/observations/{item['id']}/occurrences?limit=10"
        )
        if len(occurrence_page["items"]) != 1 or occurrence_page["items"][0]["entryLocation"] != "/items/0":
            raise AssertionError(
                "Registry List evidence did not identify the exact page member"
            )

    before_partial = {item["id"] for item in snapshot_items(api, source_id)}
    set_fixture(fixture, "partial")
    partial_job = sync(api, source_id, source_revision(api, source_id), "partial")["job"]
    partial = wait_until(
        "non-authoritative partial Registry snapshot",
        lambda: next(
            (
                item
                for item in snapshot_items(api, source_id)
                if item["id"] not in before_partial and item["state"] == "incomplete"
            ),
            None,
        ),
    )
    if partial["enumerationComplete"] or partial.get("reconcileCurrentView"):
        raise AssertionError("partial Registry run gained reconciliation authority")
    if len(observations(api, source_id)) != 2:
        raise AssertionError("partial Registry run replaced the last complete current view")
    wait_job(api, partial_job["id"], {"waiting", "running"})

    set_fixture(fixture, "replacement")
    recovered_job = wait_job(api, partial_job["id"], {"succeeded"})
    if recovered_job["attempt"] < 2:
        raise AssertionError("partial job did not recover through a later attempt")
    recovered = wait_until(
        "replacement Registry snapshot",
        lambda: next(
            (
                item
                for item in snapshot_items(api, source_id)
                if item["state"] == "complete"
                and item["memberCount"] == 1
                and item["id"] != initial["id"]
            ),
            None,
        ),
    )
    if not any(item.get("reused") for item in recovered.get("retrievals", [])):
        raise AssertionError("replacement did not conditionally refresh retained child evidence")
    current = observations(api, source_id)
    history = observations(api, source_id, "all")
    if len(current) != 1 or current[0].get("identifier") != "urn:air:functional.example:default:one":
        raise AssertionError("replacement current view is wrong")
    if not any(
        item.get("identifier") == "urn:air:functional.example:default:two" and item["freshness"] == "stale"
        for item in history
    ):
        raise AssertionError("missing Registry member was not retained as stale evidence")

    set_fixture(fixture, "replacement", delay_millis=1000)
    before_dedup = {item["id"] for item in snapshot_items(api, source_id)}
    revision = source_revision(api, source_id)
    barrier = threading.Barrier(3)
    sync_results = []
    sync_errors = []

    def concurrent_sync(index):
        try:
            barrier.wait()
            sync_results.append(sync(api, source_id, revision, f"dedupe-{index}"))
        except Exception as error:  # surfaced in the main thread below
            sync_errors.append(error)

    threads = [threading.Thread(target=concurrent_sync, args=(index,)) for index in range(2)]
    for thread in threads:
        thread.start()
    barrier.wait()
    for thread in threads:
        thread.join()
    if sync_errors:
        raise sync_errors[0]
    if len(sync_results) != 2 or {item["existing"] for item in sync_results} != {False, True}:
        raise AssertionError("concurrent sync did not deduplicate one live Job")
    job_ids = {item["job"]["id"] for item in sync_results}
    if len(job_ids) != 1:
        raise AssertionError("concurrent sync returned different Jobs")
    conditional_job = wait_job(api, next(iter(job_ids)), {"succeeded"})
    conditional = wait_until(
        "conditional terminal List snapshot",
        lambda: next(
            (
                item
                for item in snapshot_items(api, source_id)
                if item["id"] == conditional_job["result"]["snapshotId"]
                and any(retrieval.get("status") == 304 and retrieval.get("reused") for retrieval in item.get("retrievals", []))
            ),
            None,
        ),
    )
    if conditional["id"] in before_dedup:
        raise AssertionError("deduplicated Job reused an earlier synchronization result")

    qualify_snapshot_comparison(api, fixture, source_id, initial, recovered, partial)

    fixture_state, _ = fixture.request("GET", "/state")
    if fixture_state["conditionalListRequests"] < 1 or fixture_state["conditionalContextRequests"] < 1:
        raise AssertionError("conditional retrieval was not observed by the fixture")
    if fixture_state["maxActiveListRequests"] != 1:
        raise AssertionError("duplicate Jobs performed concurrent List retrieval")
    if fixture_state["modeRequests"].get("initial", 0) < 3 or fixture_state["modeRequests"].get("partial", 0) < 2 or fixture_state["modeRequests"].get("replacement", 0) < 2:
        raise AssertionError("fixture did not observe every pagination phase")

    inspected = [item for item in audit_events(api, "source.inspected") if item["targetId"] == inspection["id"]]
    connected = [item for item in audit_events(api, "source.connected") if item["targetId"] == source_id]
    requested = [item for item in audit_events(api, "source.sync.requested") if item["targetId"] == source_id]
    completed = audit_events(api, "source.sync.completed")
    check_job_completion(completed, conditional_job)
    completed_by_snapshot = {item["targetId"]: item for item in completed}
    failed = [item for item in audit_events(api, "source.sync.failed") if item["targetId"] == partial["id"]]
    if len(inspected) != 1 or len(connected) != 1:
        raise AssertionError("source inspection or connection audit is incomplete")
    if len(requested) != 3:
        raise AssertionError("manual synchronization request audit count is wrong")
    deduplicated = [item["details"]["facts"]["deduplicated"] for item in requested]
    if deduplicated.count(False) != 2 or deduplicated.count(True) != 1:
        raise AssertionError("manual synchronization deduplication audit is wrong")
    if set((initial["id"], recovered["id"], conditional["id"])) - completed_by_snapshot.keys():
        raise AssertionError("committed complete snapshots lack audit evidence")
    if len(failed) != 1:
        raise AssertionError("committed incomplete snapshot lacks one failure audit record")
    automatic_facts = completed_by_snapshot[initial["id"]]["details"]["facts"]
    if completed_by_snapshot[initial["id"]]["actor"] != "catalog-worker" or automatic_facts.get("trigger") != "automatic" or automatic_facts.get("initiator") != "catalog-source-maintainer":
        raise AssertionError("automatic synchronization attribution is incomplete")
    retry_facts = failed[0]["details"]["facts"]
    recovered_facts = completed_by_snapshot[recovered["id"]]["details"]["facts"]
    failed_attempt = retry_facts.get("jobAttempt", 0)
    recovered_attempt = recovered_facts.get("jobAttempt", 0)
    if (
        retry_facts.get("jobId") != partial_job["id"]
        or recovered_facts.get("jobId") != partial_job["id"]
        or failed_attempt < 1
        or recovered_attempt <= failed_attempt
        or recovered_attempt != recovered_job["attempt"]
    ):
        raise AssertionError("retried synchronization attempt evidence is incomplete")
    encoded_audit = json.dumps(inspected + connected + requested + completed + failed, sort_keys=True).lower()
    for private_value in ("https://federation-fixture:8443/.well-known/ard.json", source_display_name.lower()):
        if private_value in encoded_audit:
            raise AssertionError("federation audit retained a disallowed private value")

    automatic_event = completed_by_snapshot[initial["id"]]
    automatic_job, _ = api.request("GET", "/api/v1/jobs/" + automatic_event["details"]["facts"]["jobId"])
    if automatic_job["attemptLimit"] != 7 or partial_job["attemptLimit"] != 7:
        raise AssertionError("manual and automatic source producers lost the configured Job limit")
    retry_hints = qualify_retry_hints(api, fixture, source_id)
    fixture_state, _ = fixture.request("GET", "/state")
    if any(fixture_state["modeRequests"].get(mode, 0) < 1 for mode in ("throttled", "unavailable")):
        raise AssertionError("fixture did not observe both transient HTTP responses")

    print(
        json.dumps(
            {
                "sourceId": source_id,
                "automaticSnapshotId": initial["id"],
                "partialSnapshotId": partial["id"],
                "recoveredSnapshotId": recovered["id"],
                "conditionalSnapshotId": conditional["id"],
                "retriedAttempts": recovered_job["attempt"],
                "retryHints": retry_hints,
                "fixture": fixture_state,
            },
            sort_keys=True,
        )
    )


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        record_scenario_failure("federation", error)
        raise SystemExit("Functional scenario failed; private diagnostics are not emitted.") from None
