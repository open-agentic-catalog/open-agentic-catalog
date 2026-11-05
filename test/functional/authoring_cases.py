"""Isolated binding and state oracles for the bounded authoring selection."""

import copy
import base64
import hashlib
import json


def valid_wire_body(raw, validator):
    """Malformed serialized JSON is an invalid control input, not a tool crash."""
    try:
        value = json.loads(raw.decode("utf-8") if isinstance(raw, bytes) else raw)
    except (ValueError, TypeError, UnicodeError):
        return False
    # A broken validator is not evidence of an invalid request.
    return validator.is_valid(value)


def reproducer_body(body):
    if isinstance(body, bytes):
        return {"encoding": "base64", "value": base64.b64encode(body).decode("ascii")}
    return body if isinstance(body, (dict, list, str, int, float, bool, type(None))) else None


def native(label):
    """Keep native parser generation out of control-envelope tests."""
    return [{"role": "primary", "mediaType": "application/a2a-agent-card+json", "data": {
        "name": "Generated authoring control", "description": "Owned inert metadata",
        "supportedInterfaces": [{"url": "https://agent.example.com/a2a", "protocolBinding": "JSONRPC", "protocolVersion": "1.0"}],
        "version": label, "capabilities": {},
        "defaultInputModes": ["text/plain"], "defaultOutputModes": ["text/plain"],
        "skills": [{"id": "generated", "name": "Generated", "description": "Owned inert metadata", "tags": ["generated"]}]}}]


def bind_body(name, body, namespace, label, valid_field):
    """Bind prerequisites, preserving missing/invalid control fields for rejection.

    The caller must validate the serialized bound body before counting a negative.
    valid_field uses the original field schema, never the application's response.
    """
    value = copy.deepcopy(body)
    if not isinstance(value, dict):
        return value
    if name == "createResource":
        if "namespaceId" in value:
            value["namespaceId"] = namespace
        if "canonicalIdentifier" in value and valid_field("canonicalIdentifier", value["canonicalIdentifier"]):
            value["canonicalIdentifier"] = "urn:air:catalog.example.com:generated-authoring:" + label
    else:
        if "version" in value and valid_field("version", value["version"]):
            value["version"] = label
        # All native fields are fixed prerequisites. Missing representations
        # remain missing; repaired negatives are discarded after serialization.
        if "representations" in value:
            value["representations"] = native(label)
    return value


def unchanged(before, after):
    assert before == after, "rejected authoring mutation changed stored state"


def resource_created(body, created, readback, expected_tenant):
    for value in (created, readback):
        assert value["tenantId"] == expected_tenant, "wrong Resource tenant"
        assert value["namespaceId"] == body["namespaceId"] and value["canonicalIdentifier"] == body["canonicalIdentifier"]
        assert value["kind"] == body["kind"] and value["revision"] == 1, "wrong created Resource"
    from namespace_sequence import stored
    assert stored(created) == stored(readback), "Resource readback differs"


def version_created(body, parent, created, version, revision, expected_tenant):
    from namespace_sequence import stored
    expected, initial = created["version"], created["revision"]
    assert stored(expected) == stored(version), "Version readback differs"
    assert stored(initial) == stored(revision), "immutable revision readback differs"
    assert version["resourceId"] == parent and version["version"] == body["version"], "wrong Version parent/label"
    assert version["revision"] == 1 and not version.get("sealedRevisionId"), "wrong initial Version state"
    assert version["candidateRevisionId"] == revision["id"] and revision["resourceVersionId"] == version["id"], "wrong immutable revision reference"
    assert revision["revisionNumber"] == 1 and len(revision["representations"]) == 1, "wrong initial revision"
    assert version["tenantId"] == revision["tenantId"] == expected_tenant, "wrong Version/revision tenant"
    assert created["evidence"]["id"] == revision["evidenceSetId"], "wrong evidence reference"
    supplied, retained = body["representations"][0], revision["representations"][0]
    # The fixed ASCII Card uses the documented canonical JSON data form.
    raw = supplied["content"].encode() if "content" in supplied else json.dumps(supplied["data"], sort_keys=True, separators=(",", ":")).encode()
    assert retained["role"] == supplied["role"] and retained["mediaType"] == supplied["mediaType"], "wrong retained representation"
    assert retained["size"] == len(raw) and retained["contentDigest"] == "sha256:" + hashlib.sha256(raw).hexdigest(), "wrong retained native content"
    if "content" in supplied:
        assert retained["artifactId"] == hashlib.sha256(raw).hexdigest(), "wrong retained document reference"
    assert retained["validationState"] == "valid", "known-valid native control failed validation: " + str([
        item["code"] for item in retained.get("findings", [])])
