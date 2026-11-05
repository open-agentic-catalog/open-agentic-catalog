"""Opt-in local peer checks for an enabled, explicitly selected ANS verifier."""

import base64
import hashlib
import json
import os
from pathlib import Path
import sys

from peer_catalog import pages, wait_for

PREFIX = "/api/v1/federation/sources/"


def selected(value=None):
    value = os.environ.get("INTEROPERABILITY_TRUST_VERIFICATION", "off") if value is None else value
    if value not in ("off", "on"):
        raise ValueError("INTEROPERABILITY_TRUST_VERIFICATION must be off or on")
    return value == "on"


def configuration(origin, publisher, profile, directory):
    """Public fixture authority only; this test does not deploy an ANS service."""
    return dict(enabled=True, policies=[dict(name="owned-peer", profile="ans-registration-v1",
        registrationAuthority="ans-ra-local", logOrigin="owned-ans", evidenceOrigin=origin,
        networkProfile=profile, publisherDomains=[publisher],
        registrationKeys=[dict(id="ans-ra-signer", file=directory + "/ra.pem")], logKeyFiles=[directory + "/tl.pem"])])


def trust_jobs(request):
    return [row for row in pages("/api/v1/jobs", request=request) if row["kind"] == "source.trust_verify"]


def no_implicit_jobs(request):
    assert trust_jobs(request) == [], "ordinary peer discovery created a trust Job"


def target(source, row, entry):
    """Select the retained exact entry without adding or repairing peer evidence."""
    assert "trustManifest" not in entry, "review the changed native peer evidence before qualifying it"
    body = dict(sourceRevision=source["revision"], identifier=row["identifier"], versionPresent=row["versionPresent"],
                mediaType=row["mediaType"], delivery="data" if "data" in entry else "url",
                exactEntryDigest=row["exactEntryDigest"], observationId=row["id"], reason="Qualify absent native ANS evidence")
    if row["versionPresent"]:
        body["version"] = row["version"]
    return body


def qualify(request, source_id, *, empty=False, reader=None, other=None):
    """Run after the complete ordinary peer journey, before supervisor cleanup."""
    no_implicit_jobs(request)
    path = PREFIX + source_id
    source, headers = request(path)
    assert not source.get("trustVerificationPolicy"), "ordinary source selected a trust authority"
    rows = pages("/api/v1/observations", request=request, sourceId=source_id, freshness="current")
    if empty:
        assert rows == [], "Directory manifest no longer empty; reassess qualification scope"
        return dict(status="pass", discoveryJobs=0, disposition="not-applicable", reason="The native manifest has no admitted entry.")
    assert rows, "peer has no admitted indexed entry"
    row = next(row for row in rows if row.get("normalizedEntryDigest"))
    entry, _ = request("/api/v1/observations/" + row["id"] + "/raw")
    problem, _ = request(path + "/trust-assessments", target(source, row, entry), expected=503)
    assert problem["code"] == "DEPENDENCY_UNAVAILABLE", "unselected authority did not refuse assessment"
    no_implicit_jobs(request)
    membership = sorted((item["identifier"], item["exactEntryDigest"]) for item in rows)
    changes = {"trustVerificationPolicy": "owned-peer"}
    preview, _ = request(path + "/change-previews", {"intent": "update", **changes})
    request(path, {**changes, "changePreviewDigest": preview["digest"], "reason": "Select the explicit local fixture authority"}, method="PATCH", etag=headers["etag"])
    source, headers = request(path)
    assert source["trustVerificationPolicy"] == "owned-peer"
    # Policy selection changes the source revision. Re-sync normally; stale
    # observations must not bypass exact-target admission.
    receipt, _ = request(path + ":sync", {}, expected=202, etag=headers["etag"])
    sync_id = receipt["job"]["id"]
    sync = terminal(request, sync_id)
    snapshot, _ = request("/api/v1/federation/snapshots/" + sync["result"]["snapshotId"])
    assert snapshot["state"] == "complete" and snapshot["enumerationComplete"] is True
    current_source, _ = request(path)
    assert current_source["revision"] == source["revision"] and current_source["lastCompleteSnapshotId"] == snapshot["id"]
    rows = pages("/api/v1/observations", request=request, sourceId=source_id, freshness="current")
    assert sorted((item["identifier"], item["exactEntryDigest"]) for item in rows) == membership
    row = next(item for item in rows if item["id"] == row["id"] and item["exactEntryDigest"] == row["exactEntryDigest"])
    no_implicit_jobs(request)
    job, _ = request(path + "/trust-assessments", target(source, row, entry), expected=202)
    assert job["kind"] == "source.trust_verify"
    final = terminal(request, job["id"])
    assert final["attempt"] == 1 and not any(key in final for key in ("result", "privateResult", "evidence"))
    result, _ = request(path + "/trust-assessments/" + job["id"])
    validate_assessment(result, source_id, row["exactEntryDigest"])
    for token, status in ((reader, 403), (other, 404)):
        if token is not None:
            request(path + "/trust-assessments/" + job["id"], token=token, expected=status)
    assert [item["id"] for item in trust_jobs(request)] == [job["id"]], "unexpected implicit assessment"
    events = [event for event in pages("/api/v1/audit-events", request=request, action="source.trust.completed") if event["targetId"] == job["id"]]
    assert len(events) == 1 and events[0]["details"]["facts"]["entryDigest"] == row["exactEntryDigest"]
    return dict(status="pass", discoveryJobs=0, disposition="incomplete-native-evidence", source_id=source_id,
                job_id=job["id"], assessment=result, entry_digest=row["exactEntryDigest"], sync_id=sync_id)


def terminal(request, identifier):
    def done():
        row, _ = request("/api/v1/jobs/" + identifier)
        assert row["state"] not in ("failed", "dead_letter", "cancelled"), "peer qualification Job failed"
        return row if row["state"] == "succeeded" else None
    return wait_for("peer qualification Job", done, timeout=120)


def validate_assessment(document, source_id, digest):
    result = document["result"]
    assert document["current"] is False and document["applicability"] == "negative_or_insufficient"
    assert result["sourceId"] == source_id and result["exactEntryDigest"] == digest
    assert "sha256:" + hashlib.sha256(base64.b64decode(result["evidence"]["entry"], validate=True)).hexdigest() == digest
    assessment = result["assessment"]
    assert assessment["currentRegistration"] is False and assessment["receipt"] == {"state": "unavailable", "code": "receipt_missing"}
    assert all(assessment[key]["state"] == "not_assessed" for key in ("producer", "log", "checkpoint", "inclusion", "status"))
    assert assessment["target"]["state"] in ("not_assessed", "unsupported")
    assert all(result[key] == "not_assessed" for key in ("softwareTrust", "artifactIntegrity", "rights"))
    assert not any(result["evidence"].get(key) for key in ("receipt", "checkpoint", "checkpointHistory", "statusToken"))


def main():
    if not __debug__:
        raise RuntimeError("peer trust qualification requires assertions")
    if sys.argv[1] == "config":
        print(json.dumps(configuration(*sys.argv[2:])))
        return
    from agntcy_lab import LocalHTTP, UnexpectedStatus
    settings = json.loads(Path(sys.argv[1]).read_text())
    api = LocalHTTP({"catalog": settings["catalog"]}, settings["certificate"], settings["token"])
    def request(path, body=None, *, token=None, **options):
        saved = api.token
        try:
            api.token = saved if token is None else token
            try:
                return api.document("catalog", path, body, **options)
            except UnexpectedStatus as error:
                problem = json.loads(error.raw)
                message = json.dumps({key: problem.get(key) for key in ("status", "code", "detail")})
                assert all(token not in message for token in (settings["token"], settings["reader"], settings["other"]))
                raise AssertionError("unexpected peer Catalog response: " + message) from error
        finally:
            api.token = saved
    result = qualify(request, settings["source_id"], empty=settings["empty"], reader=settings["reader"], other=settings["other"])
    assert all(token not in json.dumps(result) for token in (settings["token"], settings["reader"], settings["other"]))
    Path(sys.argv[2]).write_text(json.dumps(result) + "\n")


if __name__ == "__main__":
    main()
