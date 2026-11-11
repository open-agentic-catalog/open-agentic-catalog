#!/usr/bin/env python3
"""Fixed mounted bearer/API-key/mTLS journeys using the existing HTTPS origin."""

import json
import os
from pathlib import Path
import secrets
import time

from core import mutate
from federation import Client, key, snapshot_items, wait_job
from http_client import fixture_tls_context
from journeys import header
from operators import Operators
from runner import record_scenario_failure, write_document


def completed_snapshot(api, source_id, job_id):
    """Read this Job's snapshot; tied timestamps do not imply history order."""
    job = wait_job(api, job_id, {"succeeded"})
    result = job["result"]
    assert result["type"] == "source_snapshot" and result["sourceId"] == source_id and result["state"] == "complete", "sync Job returned a different outcome"
    snapshot, _ = api.request("GET", "/api/v1/federation/snapshots/" + result["snapshotId"])
    assert snapshot["id"] == result["snapshotId"] and snapshot["sourceId"] == source_id and snapshot["state"] == "complete", "sync Job snapshot identity changed"
    return snapshot


def wait_revoked(api, path, seconds=15):
    """Wait until the API container sees the renamed credential file.

    Engines that share host paths through a VM (for example virtiofs) can show a
    rename late. A bounded poll replaces an immediate assertion. The strict 503
    check that follows still runs, and a binding that stays readable still fails.
    """
    deadline = time.monotonic() + seconds
    while True:
        _, headers = api.request("GET", path)
        request_headers = {"Idempotency-Key": key("credentials-revocation"), "If-Match": header(headers, "ETag")}
        # Both statuses are asserted outcomes here; only the body tells them apart.
        result, _ = api.request("POST", path + ":test", {}, (200, 503), request_headers)
        if result.get("code") == "DEPENDENCY_UNAVAILABLE":
            return
        assert time.monotonic() < deadline, "revoked binding stayed readable"
        time.sleep(0.25)


def run():
    owned = Operators(os.environ)
    fixture = Path(owned.env["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"])
    directory = fixture / "credentials"
    owned.env["CATALOG_COMPOSE_FEDERATION_CREDENTIALS_DIRECTORY"] = str(directory)
    owned.compose("-f", "test/functional/compose.credentials.yaml", "up", "--detach", "--no-deps", "--no-build", "api", "worker")
    owned.wait(lambda: owned.api.request("GET", "/readyz", retry_unavailable=True)[0])
    origin = Client("https://127.0.0.1:" + owned.env["CATALOG_COMPOSE_FIXTURE_PORT"], ssl_context=fixture_tls_context(fixture / "trust/functional-ca.pem"))
    for kind in ("bearer", "api_key", "mtls"):
        endpoint = "/credentials/" + kind + "/manifest.json"
        origin.request("GET", endpoint, expected=(401,))
        inspection, _ = mutate(owned.api, "POST", "/api/v1/federation/source-inspections", {
            "url": "https://federation-fixture:8443" + endpoint, "networkProfile": "functional-fixture", "credentialBinding": kind}, 201)
        assert inspection["credentialRevision"] == "one", "inspection lost credential revision"
        source, headers = mutate(owned.api, "POST", "/api/v1/federation/sources", {
            "inspectionId": inspection["id"], "displayName": "Owned credential source " + kind, "retrievalMode": "indexed",
            "scope": {"kinds": ["agent"]}, "updateMode": "manual_review", "networkProfile": "functional-fixture", "credentialBinding": kind,
            "reason": "Mounted transport qualification"}, 201)
        path = "/api/v1/federation/sources/" + source["id"]
        job, _ = mutate(owned.api, "POST", path + ":sync", {}, 202, header(headers, "ETag"))
        first = completed_snapshot(owned.api, source["id"], job["job"]["id"])
        assert first["credentialRevision"] == "one" and first["memberCount"] == 1, "worker lost credential-bound evidence"

        metadata_path = directory / (kind + ".json")
        metadata = json.loads(metadata_path.read_bytes())
        if kind != "mtls":
            replacement = directory / (kind + ".next")
            replacement.write_text(secrets.token_hex(32), encoding="ascii")
            replacement.chmod(0o444)
            replacement.replace(directory / (kind + ".token"))
        else:
            metadata["certificateFile"], metadata["privateKeyFile"] = "client-two.pem", "client-two-key.pem"
        metadata["revision"] = "two"
        write_document(metadata_path, metadata)
        metadata_path.chmod(0o444)
        _, headers = owned.api.request("GET", path)
        result, headers = mutate(owned.api, "POST", path + ":test", {}, etag=header(headers, "ETag"))
        assert result["status"] == "succeeded" and result["credentialRevision"] == "two", "mounted rotation required a restart"
        job, _ = mutate(owned.api, "POST", path + ":sync", {}, 202, header(headers, "ETag"))
        rotated = completed_snapshot(owned.api, source["id"], job["job"]["id"])
        assert rotated["credentialRevision"] == "two", "worker reused old credential evidence"

        revoked = metadata_path.with_suffix(".revoked")
        metadata_path.replace(revoked)
        try:
            wait_revoked(owned.api, path)
            _, headers = owned.api.request("GET", path)
            result, _ = mutate(owned.api, "POST", path + ":test", {}, 503, header(headers, "ETag"))
            assert result["code"] == "DEPENDENCY_UNAVAILABLE", "revoked binding did not fail closed"
            retained, retained_headers = owned.api.request("GET", path)
            assert retained["credentialRevision"] == "two" and header(retained_headers, "ETag") == header(headers, "ETag"), "failed credential test changed approved source"
            owned.api.request("GET", "/readyz")
            assert any(item["id"] == first["id"] for item in snapshot_items(owned.api, source["id"])), "revocation lost retained snapshot"
        finally:
            revoked.replace(metadata_path)
        _, headers = owned.api.request("GET", path)
        result, _ = mutate(owned.api, "POST", path + ":test", {}, etag=header(headers, "ETag"))
        assert result["status"] == "succeeded", "restored binding did not recover"
    print("Mounted bearer, API-key and mTLS API/worker evidence, revision replacement and revocation passed.")


if __name__ == "__main__":
    try:
        run()
    except Exception as error:
        record_scenario_failure("credentials", error)
        raise
