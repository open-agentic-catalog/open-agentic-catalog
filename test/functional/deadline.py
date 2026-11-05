#!/usr/bin/env python3
"""Check the packaged API's bounded request, replay, and retry contracts."""

import argparse
import json
import time
import urllib.error
import urllib.request
import urllib.parse

from http_client import ClientFailure, decode, request as bounded_request
from response_contract import validate
import operation_coverage as coverage


def request(value, expected=None):
    parsed = urllib.parse.urlsplit(value.full_url)
    started = time.monotonic()
    try:
        status, headers, raw = bounded_request(value)
    except ClientFailure:
        if coverage.enabled():
            validate(value.get_method(), parsed.path, 0, {}, b"", expected)
        raise
    elapsed = time.monotonic() - started
    validate(value.get_method(), parsed.path, status, dict(headers.items()), raw, expected)
    return status, headers, raw, elapsed


def namespace_read(base_url):
    return urllib.request.Request(
        base_url.rstrip("/") + "/api/v1/namespaces?limit=1",
        headers={"Authorization": "Bearer local-admin"},
        method="GET",
    )


def namespace_command(base_url, run_id, key_variant):
    body = json.dumps(
        {
            "slug": "deadline-" + run_id,
            "displayName": "Deadline recovery " + run_id,
        },
        separators=(",", ":"),
        sort_keys=True,
    ).encode("utf-8")
    return urllib.request.Request(
        base_url.rstrip("/") + "/api/v1/namespaces",
        data=body,
        headers={
            "Authorization": "Bearer local-admin",
            "Content-Type": "application/json",
            "Idempotency-Key": "deadline-" + key_variant + "-" + run_id,
        },
        method="POST",
    )


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--operation", choices=("read", "command"), required=True)
    parser.add_argument(
        "--expect",
        choices=("deadline", "success", "deadline-replay", "success-replay"),
        required=True,
    )
    parser.add_argument("--run-id")
    parser.add_argument("--key-variant", default="deadline")
    arguments = parser.parse_args()
    if arguments.operation == "read":
        if arguments.expect not in ("deadline", "success"):
            parser.error("read only supports deadline or success expectations")
        value = namespace_read(arguments.base_url)
    else:
        if not arguments.run_id:
            parser.error("command requires --run-id")
        value = namespace_command(
            arguments.base_url, arguments.run_id, arguments.key_variant
        )
    expected = 503 if arguments.expect in ("deadline", "deadline-replay") else (200 if arguments.operation == "read" else 201)
    status, headers, raw, elapsed = request(value, (expected,))
    document = decode(raw)
    if arguments.expect in ("deadline", "deadline-replay"):
        if status != 503 or document.get("code") != "REQUEST_DEADLINE_EXCEEDED":
            raise SystemExit(
                f"blocked product {arguments.operation} returned HTTP {status}"
            )
        if not headers.get("Content-Type", "").startswith("application/problem+json"):
            raise SystemExit(
                f"blocked product {arguments.operation} did not return RFC 9457 problem details"
            )
        if arguments.expect == "deadline" and (elapsed < 2.0 or elapsed > 8.0):
            raise SystemExit(f"product deadline completed outside its bounded envelope: {elapsed:.3f}s")
        replayed = headers.get("Idempotency-Replayed") == "true"
        if replayed != (arguments.expect == "deadline-replay"):
            raise SystemExit(
                "deadline replay header did not match"
            )
        return
    if arguments.operation == "read":
        if status != 200 or not isinstance(document.get("items"), list):
            raise SystemExit(f"retried product read returned HTTP {status}")
        return
    expected_slug = "deadline-" + arguments.run_id
    if status != 201 or document.get("slug") != expected_slug:
        raise SystemExit(f"retried product command returned HTTP {status}")
    replayed = headers.get("Idempotency-Replayed") == "true"
    if replayed != (arguments.expect == "success-replay"):
        raise SystemExit(
            "success replay header did not match"
        )
    if not headers.get("Location", "").startswith("/api/v1/namespaces/"):
        raise SystemExit("successful namespace command omitted its resource location")


if __name__ == "__main__":
    try:
        main()
    except ClientFailure as error:
        raise SystemExit(str(error)) from None
    except Exception:
        raise SystemExit("Functional scenario failed; private diagnostics are not emitted.") from None
