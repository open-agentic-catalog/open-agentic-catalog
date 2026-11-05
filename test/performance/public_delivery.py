"""Governed public-manifest preparation and authority checks, outside timing."""
import base64
import hashlib
import re
import urllib.request

REGISTRY = "application/ai-registry+json"


def read(journey, *, host="127.0.0.1", etag=None, token=None, expected=200):
    headers = {"Host": host}
    if etag is not None:
        headers["If-None-Match"] = etag
    if token is not None:
        headers["Authorization"] = "Bearer " + token
    status, returned, raw = journey.http.request(urllib.request.Request(
        journey.api + "/.well-known/ard.json", headers=headers))
    if status != expected or expected == 304 and raw:
        raise journey.failure("public-delivery-status-mismatch")
    if expected in (401, 403, 404):
        problem = journey.http.decode(raw)
        code = {401: "UNAUTHENTICATED", 403: "FORBIDDEN", 404: "NOT_FOUND"}[expected]
        if type(problem) is not dict or set(problem) != {"errorCode", "message"} or problem.get("errorCode") != code or type(problem.get("message")) is not str:
            raise journey.failure("public-delivery-denial-mismatch")
    return {key.lower(): value for key, value in returned.items()}, raw


def describe(journey, headers, raw, expected_identifiers=None):
    document = journey.http.decode(raw)
    entries = document.get("entries") if type(document) is dict else None
    if (type(entries) is not list or not entries or any(type(e) is not dict or type(e.get("identifier")) is not str or not e["identifier"] or not e.get("type") for e in entries)
            or len({e["identifier"] for e in entries}) != len(entries)
            or sum(e["type"] == REGISTRY for e in entries) != 1):
        raise journey.failure("public-delivery-entry-mismatch")
    identifiers = {entry["identifier"] for entry in entries}
    if expected_identifiers is not None and identifiers != expected_identifiers:
        raise journey.failure("public-delivery-population-mismatch")
    digest = hashlib.sha256(raw)
    content_digest = "sha-256=:" + base64.b64encode(digest.digest()).decode() + ":"
    etag = headers.get("etag", "")
    if not re.fullmatch(r'"[!#-~]{1,126}"', etag) or headers.get("content-digest") != content_digest or not 0 < len(raw) <= 16 << 20:
        raise journey.failure("public-delivery-identity-mismatch")
    return {"entries": len(entries), "bytes": len(raw), "etag": etag, "digest": "sha256:" + digest.hexdigest()}, identifiers


def snapshot(journey, host, key):
    candidate, headers = journey.command("/publication/snapshots", {"host": host}, key + "-create", token="local-publisher")
    journey.command("/publication/snapshots/" + candidate["id"] + ":activate",
                    {"host": host, "reason": "Measure owned public delivery"}, key + "-activate", headers.get("ETag"), "local-publisher", 200)
    return candidate


def begin(journey):
    original = snapshot(journey, "127.0.0.1", "delivery-small")
    headers, raw = read(journey)
    small, identifiers = describe(journey, headers, raw)
    if small["entries"] != 4:
        raise journey.failure("public-delivery-small-fixture-changed")
    return {"original": original, "small": small, "identifiers": identifiers}


def unchanged(journey, expected):
    headers, raw = read(journey)
    value, _ = describe(journey, headers, raw)
    if value != expected:
        raise journey.failure("public-delivery-snapshot-changed")
    headers, _ = read(journey, etag=expected["etag"], expected=304)
    if headers.get("etag") != expected["etag"]:
        raise journey.failure("public-delivery-conditional-identity-mismatch")


def prepare(journey, state):
    large = snapshot(journey, "127.0.0.1", "delivery-large")
    expected = state["identifiers"] | {f"urn:air:scale.example:scale:local-{i*4+1:06d}" for i in range(997)}
    headers, raw = read(journey)
    manifest, _ = describe(journey, headers, raw, expected)
    if manifest["entries"] != 1001:
        raise journey.failure("public-delivery-size-mismatch")
    # The old small ETag cannot turn a newly activated document into a 304.
    changed_headers, changed_raw = read(journey, etag=state["small"]["etag"])
    if (changed_headers.get("etag"), changed_raw) != (headers["etag"], raw):
        raise journey.failure("public-delivery-activation-mismatch")
    unchanged(journey, manifest)
    for supplied in (None, manifest["etag"]):
        read(journey, host="unconfigured.invalid", etag=supplied, expected=404)
        read(journey, host="localhost", etag=supplied, expected=401)
        read(journey, host="localhost", etag=supplied, token="invalid-owned-token", expected=401)
        read(journey, host="localhost", etag=supplied, token="other-tenant-admin", expected=403)
        read(journey, host="localhost", etag=supplied, token="local-reader", expected=404)
    snapshot(journey, "localhost", "delivery-protected")
    protected_headers, protected_raw = read(journey, host="localhost", token="local-reader", etag=manifest["etag"])
    protected, _ = describe(journey, protected_headers, protected_raw)
    if protected["entries"] != 1001 or protected["etag"] == manifest["etag"]:
        raise journey.failure("public-delivery-audience-binding-mismatch")
    protected_headers, _ = read(journey, host="localhost", token="local-reader", etag=protected["etag"], expected=304)
    if protected_headers.get("etag") != protected["etag"]:
        raise journey.failure("public-delivery-protected-etag-mismatch")
    for supplied in (None, protected["etag"]):
        read(journey, host="localhost", etag=supplied, expected=401)
        read(journey, host="localhost", etag=supplied, token="invalid-owned-token", expected=401)
        read(journey, host="localhost", etag=supplied, token="other-tenant-admin", expected=403)
    journey.command("/publication:unpublish", {"host": "127.0.0.1", "reason": "Check withdrawal before conditional response"},
                    "delivery-unpublish", token="local-publisher", status=200)
    for supplied in (None, manifest["etag"]):
        read(journey, etag=supplied, expected=404)
    _, old_headers = journey.request("GET", "/publication/snapshots/" + state["original"]["id"] + "?host=127.0.0.1", token="local-publisher")
    refused, _ = journey.request("POST", "/publication/snapshots/" + state["original"]["id"] + ":rollback",
                                 {"host": "127.0.0.1", "reason": "Refuse obsolete publication membership"},
                                 "local-publisher", "delivery-rollback-small", old_headers.get("ETag"), (422,))
    if refused.get("code") != "PUBLICATION_NOT_READY":
        raise journey.failure("public-delivery-stale-membership-refusal-mismatch")
    read(journey, etag=manifest["etag"], expected=404)
    successor = snapshot(journey, "127.0.0.1", "delivery-successor")
    current, current_headers = journey.request("GET", "/publication/snapshots/" + large["id"] + "?host=127.0.0.1", token="local-publisher")
    if current["id"] != large["id"] or current["state"] != "retired":
        raise journey.failure("public-delivery-retained-identity-mismatch")
    restored, _ = journey.command("/publication/snapshots/" + large["id"] + ":rollback",
                                  {"host": "127.0.0.1", "reason": "Verify exact retained public content"},
                                  "delivery-rollback-large", current_headers.get("ETag"), "local-publisher", 200)
    retired, _ = journey.request("GET", "/publication/snapshots/" + successor["id"] + "?host=127.0.0.1", token="local-publisher")
    if restored["id"] != large["id"] or restored["state"] != "active" or retired["state"] != "retired":
        raise journey.failure("public-delivery-rollback-state-mismatch")
    unchanged(journey, manifest)
    return {"manifest": manifest, "smallManifest": state["small"],
            "checks": {"activation": True, "unpublish": True, "rollback": True, "staleMembershipRefused": True, "host": True, "audience": True, "access": True}}
