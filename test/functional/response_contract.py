"""Bind existing journey responses to the same pinned Go schema validator."""

import base64
import json
import os
import subprocess
import sys

from http_client import ClientFailure, MAX_HEADERS, MAX_RESPONSE
import operation_coverage as coverage


def validate(method, path, status, headers, body, expected=None, *, collect=True):
    """Check an exchange with the pinned Go driver without exposing its raw output.

    Coverage mode additionally records the caller's expected-status disposition;
    collect=False still validates responses but keeps them out of that inventory.
    """
    if coverage.enabled() and collect:
        return observe(method, path, status, headers, body, expected)
    executable = os.environ.get("CATALOG_FUNCTIONAL_CONTRACT_DRIVER")
    if not executable:
        raise ClientFailure("functional response validator unavailable")
    if len(body) > MAX_RESPONSE or sum(len(k) + len(v) for k, v in headers.items()) > MAX_HEADERS:
        raise ClientFailure("functional response contract limit")
    value = {"method": method, "path": path, "status": status,
             "headers": {k: [v] for k, v in headers.items()},
             "body": base64.b64encode(body).decode("ascii")}
    try:
        result = subprocess.run([executable, "--validate-stdin"],
                                input=json.dumps(value, separators=(",", ":")).encode(),
                                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                                timeout=10, check=False)
        if result.returncode:
            raise ClientFailure("functional response contract mismatch")
    except (OSError, subprocess.TimeoutExpired):
        raise ClientFailure("functional response validator failed") from None


def observe(method, path, status, headers, body, expected=None):
    """Record unexpected responses without weakening the caller's checks."""
    executable = os.environ.get("CATALOG_FUNCTIONAL_CONTRACT_DRIVER")
    if not executable or len(body) > MAX_RESPONSE or sum(len(k) + len(v) for k, v in headers.items()) > MAX_HEADERS:
        raise ClientFailure("functional response contract limit or unavailable driver")
    value = {"method": method, "path": path, "status": status,
             "headers": {k: [v] for k, v in headers.items()},
             "body": base64.b64encode(body).decode("ascii")}
    try:
        result = subprocess.run([executable, "--observe-stdin"],
                                input=json.dumps(value, separators=(",", ":")).encode(),
                                stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=10, check=False)
        if result.returncode or len(result.stdout) > 4096:
            raise ClientFailure("functional response observation failed")
        from http_client import decode
        observation = decode(result.stdout)
        coverage.record(observation, expected)
        if observation["contract"] == "failed":
            raise ClientFailure("functional response contract mismatch")
    except (OSError, subprocess.TimeoutExpired, KeyError):
        raise ClientFailure("functional response validator failed") from None


def representative(base, document):
    """Retain the fixed driver's safe observations, including partial failures."""
    executable = os.environ["CATALOG_FUNCTIONAL_CONTRACT_DRIVER"]
    result = subprocess.run([executable, "--report", "--base-url", base, "--document-id", document],
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, timeout=70, check=False)
    if not result.stdout.strip() or len(result.stdout) > 16384:
        raise ClientFailure("missing or excessive representative observations")
    from http_client import decode
    for line in result.stdout.splitlines():
        value = decode(line)
        expected = value.pop("expected")
        coverage.record(value, (expected,))
    if result.returncode:
        raise ClientFailure("representative response contracts failed")


if __name__ == "__main__":
    try:
        if len(sys.argv) != 3:
            raise ClientFailure("representative arguments required")
        representative(*sys.argv[1:])
    except Exception:
        print("Representative response contracts failed.", file=sys.stderr)
        sys.exit(1)
