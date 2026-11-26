"""Digest-bound, locally supplied ARD publications. No acquisition or URL resolution."""

from collections import Counter
from hashlib import sha256
import json
from pathlib import Path
from urllib.parse import urlsplit

from agentfinder_tools import unique_object
from peer_contract import has_credential_header

PROFILE = "ard-0.91-aa3e598"
PATH = "/.well-known/ard.json"
REGISTRY = "application/ai-registry+json"
MCP = "application/mcp-server-card+json"
A2A = "application/a2a-agent-card+json"
KINDS = {MCP: "mcp_server", A2A: "agent"}
EXERCISE_PHASES = ("inspection", "retention", "permissions", "repeat", "outage", "timeout", "malformed", "oversize")
FEEDS = {
    "desvela": {
        "publisher": "desvela.dev", "header": "application/json",
        "types": {MCP: 4, REGISTRY: 1}, "warnings": 0,
        "selected": "urn:air:desvela.dev:registry:preflight", "expansionFailures": [],
        "captures": [("DESVELA_CAPTURE", 7025, "a12cc3f1e4941c60789eaeac7377e31bdc6c33422a7ff4d4d2a6a28389dc88a2")],
    },
    "nevermined": {
        "publisher": "api.live.nevermined.app", "header": "application/json; charset=utf-8",
        "types": {"application/json": 139, "application/openapi+json": 17, A2A: 1, REGISTRY: 1}, "warnings": 157,
        "selected": "urn:air:api.live.nevermined.app:service:flylabs", "expansionFailures": ["quicknode-mpp", "tempo-rpc"],
        "captures": [
            ("NEVERMINED_CAPTURE", 552014, "9c11d1130515fe577615d0e9b794ead276bda7fcca9df14d26a2a6c796493518"),
            ("NEVERMINED_TRANSPORT_CAPTURE", 551036, "47fde7c56fcdc26ed60e3ca74bb476db65953da2d4abf60460b5824a4648eaee"),
        ],
    },
}


class CapturePrerequisiteError(ValueError):
    """The original publication is unavailable for qualification."""


def replay_route(path, headers, mode):
    """Classify an anonymous GET using the HTTP server's case-insensitive headers."""
    if has_credential_header(headers):
        return "unexpected"
    if path == PATH:
        return "manifest"
    if path == "/.well-known/ai-catalog.json" and mode in ("malformed", "outage"):
        return "fallback"
    return "unexpected"


def read_capture(value, pin):
    variable, size, digest = pin
    if not value or not Path(value).is_file():
        raise CapturePrerequisiteError(f"Set {variable} to the private local capture (SHA-256 {digest})")
    with Path(value).open("rb") as stream:
        raw = stream.read(size + 1)
    if len(raw) != size or sha256(raw).hexdigest() != digest:
        raise CapturePrerequisiteError(f"{variable} does not match the recorded size and SHA-256")
    return raw


def inventory(raw, feed):
    entries = json.loads(raw, object_pairs_hook=unique_object)["entries"]
    assert Counter(e["type"] for e in entries) == feed["types"], "unexpected media type inventory"
    identifiers = {e["identifier"] for e in entries}
    assert len(identifiers) == len(entries) and feed["selected"] in identifiers
    for entry in entries:
        assert entry["identifier"].split(":")[0:3] == ["urn", "air", feed["publisher"]]
        assert "version" not in entry
        trust = entry.get("trustManifest")
        if feed["publisher"] == "api.live.nevermined.app" and entry["type"] != REGISTRY:
            identity = urlsplit(trust["identity"])
            assert trust["identityType"] == "https" and identity.scheme == "https" and identity.hostname == feed["publisher"]
        else:
            assert trust is None
    return entries


def check_read_audits(downloads, events, membership_id, tenant_id):
    """Match this batch of successful HTTP reads to their exact committed audit records."""
    matched = set()
    for event in events:
        request_id = event.get("requestId")
        if request_id not in downloads:
            continue
        assert request_id not in matched, "duplicate audit for one evidence read"
        matched.add(request_id)
        assert event["tenantId"] == tenant_id and event["actor"] == membership_id, "wrong evidence reader"
        assert event["action"] == "evidence.raw_observation.read" and event["targetType"] == "observation"
        facts = event["details"]["facts"]
        assert (event["targetId"], facts["contentDigest"], facts["sizeBytes"]) == downloads[request_id], "wrong audited evidence"
    assert matched == downloads.keys(), "missing audit for an evidence read"
