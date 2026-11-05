#!/usr/bin/env python3
"""Exercise bounded audit investigation and export against the Compose API."""

import argparse
import datetime
import email.utils
import json
import pathlib
import time
import urllib.error
import urllib.parse
import urllib.request

from http_client import ClientFailure, decode, request as bounded_request
from response_contract import validate
import operation_coverage as coverage


def request(base_url, method, path, token, body=None, key=None, expected=(200,)):
    headers = {"Authorization": "Bearer " + token}
    encoded = None
    if body is not None:
        encoded = json.dumps(body, separators=(",", ":"), sort_keys=True).encode("utf-8")
        headers["Content-Type"] = "application/json"
    if key is not None:
        headers["Idempotency-Key"] = key
    value = urllib.request.Request(base_url + path, data=encoded, headers=headers, method=method)
    try:
        status, response_headers, raw = bounded_request(value)
    except ClientFailure:
        if coverage.enabled():
            validate(method, path, 0, {}, b"", expected)
        raise
    validate(method, path, status, dict(response_headers.items()), raw, expected)
    return status, response_headers, raw


def require(condition, message):
    if not condition:
        raise SystemExit(message)


def wait_for_server_cutoff(base_url, occurred_before):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        status, headers, _ = request(base_url, "GET", "/version", "local-admin")
        require(status == 200, "server-time probe returned HTTP " + str(status))
        date_value = headers.get("Date")
        require(date_value, "server-time probe omitted its Date header")
        server_time = email.utils.parsedate_to_datetime(date_value)
        if server_time >= occurred_before + datetime.timedelta(seconds=1):
            return
        time.sleep(0.1)
    raise SystemExit("server cutoff did not advance beyond the selected audit event")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--run-id", required=True)
    arguments = parser.parse_args()

    status, _, raw_page = request(arguments.base_url, "GET", "/api/v1/audit-events?limit=20", "local-admin")
    require(status == 200, "audit investigation returned HTTP " + str(status))
    page = decode(raw_page)
    items = page.get("items", [])
    require(items, "audit investigation returned no events after the smoke workflow")
    attributable = [item for item in items if item.get("formatVersion") == 1 and item.get("requestId")]
    selected = min(attributable, key=lambda item: item["at"], default=None)
    require(selected is not None, "audit investigation returned no attributable version-1 HTTP event")

    occurred_at = datetime.datetime.fromisoformat(selected["at"].replace("Z", "+00:00"))
    occurred_before = occurred_at + datetime.timedelta(microseconds=1)
    # The export contract refuses a range end newer than its own server-side
    # cutoff. Use the server's HTTP Date rather than assuming client/server
    # wall clocks are ordered around this just-created event.
    wait_for_server_cutoff(arguments.base_url, occurred_before)
    export_body = {
        "actor": selected["actor"],
        "action": selected["action"],
        "requestId": selected["requestId"],
        "targetType": selected["targetType"],
        "targetId": selected["targetId"],
        "occurredFrom": occurred_at.isoformat().replace("+00:00", "Z"),
        "occurredBefore": occurred_before.isoformat().replace("+00:00", "Z"),
        "reason": "Functional restore evidence " + arguments.run_id,
    }
    key = "functional-audit-export-" + arguments.run_id
    status, first_headers, first_raw = request(arguments.base_url, "POST", "/api/v1/audit-exports", "local-admin", export_body, key)
    require(status == 200, "audit export returned HTTP " + str(status))
    require(first_headers.get("Content-Disposition") == 'attachment; filename="open-agentic-catalog-audit-export.json"', "audit export omitted its download header")
    exported = decode(first_raw)
    require(exported.get("formatVersion") == 1, "audit export format version is invalid")
    require(exported.get("manifest", {}).get("rowCount") == 1, "exact audit export did not return one row")
    require(exported.get("events", [{}])[0].get("id") == selected["id"], "audit export returned the wrong event")

    status, replay_headers, replay_raw = request(arguments.base_url, "POST", "/api/v1/audit-exports", "local-admin", export_body, key)
    require(status == 200, "audit export replay returned HTTP " + str(status))
    require(replay_headers.get("Idempotency-Replayed") == "true", "audit export replay was not identified")
    require(replay_headers.get("Content-Disposition") == first_headers.get("Content-Disposition"), "audit export replay changed its download header")
    require(replay_raw == first_raw, "audit export replay changed the exact response")

    export_request_id = first_headers.get("X-Request-ID")
    require(export_request_id, "audit export omitted its request identifier")
    evidence_query = urllib.parse.urlencode({"action": "audit.exported", "requestId": export_request_id, "limit": "10"})
    status, _, evidence_raw = request(arguments.base_url, "GET", "/api/v1/audit-events?" + evidence_query, "local-admin")
    require(status == 200, "audit export evidence lookup returned HTTP " + str(status))
    require(len(decode(evidence_raw).get("items", [])) == 1, "audit export replay appended duplicate access evidence")

    status, _, other_raw = request(arguments.base_url, "POST", "/api/v1/audit-exports", "other-tenant-admin", export_body, key + "-other")
    require(status == 200, "other-tenant audit export returned HTTP " + str(status))
    other = decode(other_raw)
    require(other.get("manifest", {}).get("rowCount") == 0 and not other.get("events"), "audit export crossed its tenant boundary")

    missing_range = {"occurredBefore": export_body["occurredBefore"], "reason": "Missing lower range"}
    status, _, _ = request(arguments.base_url, "POST", "/api/v1/audit-exports", "local-admin", missing_range, key + "-invalid", (400,))
    require(status == 400, "audit export accepted a missing range endpoint")
    status, _, _ = request(arguments.base_url, "POST", "/api/v1/audit-exports", "local-reader", export_body, key + "-reader", (403,))
    require(status == 403, "audit export accepted a reader without Security Reviewer authority")

    pathlib.Path(arguments.output).write_bytes(first_raw)


if __name__ == "__main__":
    try:
        main()
    except Exception:
        raise SystemExit("Functional audit scenario failed; private diagnostics are not emitted.") from None
