"""Fixed MVP assertions distinguish artifact overlap from failing ARD federation."""

import hashlib
import json
import re

from peer_contract import A2A, CURRENT

PROFILE = "a2a-agent-card-1.0.1-3303592"
DISPOSITIONS = {"artifact": "supported", "discovery": "compatible-empty",
                "search": "peer-incompatible", "conformance": "partial-compatibility"}
ERRORS = ["GET /agents response is not a valid paginated object. Missing 'items' array.",
          "POST /search request failed: 400 Bad Request"]
ENTRY_ERRORS = [{"path": ["identifier"], "validator": "pattern"},
                {"path": ["metadata", "agntcy.dir.trust.v1.Status"], "validator": "type"},
                {"path": ["metadata", "agntcy.dir.usage.v1.Metrics"], "validator": "type"}]


def document_digest(document):
    """Compare semantic JSON without depending on either implementation's formatting."""
    return hashlib.sha256(json.dumps(document, sort_keys=True, separators=(",", ":"), ensure_ascii=False).encode()).hexdigest()


def check_checker(mode, evidence, expected_errors=None):
    """Accept only the pinned observed outcome, not an arbitrary checker failure."""
    if mode not in ("manifest", "publisher", "registry") or not isinstance(evidence, dict):
        raise ValueError("invalid checker mode/evidence")
    code = 1 if mode == "registry" else 0
    if type(evidence.get("exit_code")) is not int or evidence["exit_code"] != code:
        raise ValueError("unexpected native checker exit")
    text = evidence.get("output")
    if not isinstance(text, str) or not text.strip() or evidence.get("stderr") != "":
        raise ValueError("missing or failed native checker output")
    clean = re.sub(r"\x1b\[[0-9;]*m", "", text)
    warnings = [line.strip() for line in clean.splitlines() if line.strip().startswith("⚠")]
    errors = [line.strip()[1:].strip() for line in clean.splitlines() if line.strip().startswith("✗")]
    if evidence.get("warnings") != warnings or "skipping" in clean.lower():
        raise ValueError("missing or contradictory checker evidence")
    if mode == "registry":
        if (errors != (ERRORS if expected_errors is None else expected_errors) or "CONFORMANCE STATUS: FAIL" not in clean
                or "Found 2 critical specification errors." not in clean
                or evidence.get("summary_warnings") is not None or len(warnings) != 1
                or "POST /explore responded with 404" not in warnings[0]):
            raise ValueError("Registry failure differs from the reviewed List/Search incompatibility")
        return
    count = 2 if mode == "publisher" else 1
    if (errors or "CONFORMANCE STATUS: PASS" not in clean
            or clean.count("✓ Manifest validates against ArdManifest.") != 1
            or clean.count("✓ All 0 entries validate against ArdEntry.") != 1
            or type(evidence.get("summary_warnings")) is not int or evidence["summary_warnings"] != count
            or f"Validated with 0 critical specification errors and {count} warnings." not in clean
            or len(warnings) != count + int(mode == "publisher")
            or not any("Found 'collections' at root." in line for line in warnings)):
        raise ValueError("missing exact empty manifest/schema/warning evidence")
    if mode == "publisher" and not ("No manifest at " in warnings[0] and "(404 Not Found)" in warnings[0]
                                     and "Reachable only at " in warnings[1]):
        raise ValueError("missing predecessor publisher resolution evidence")


def raw_entry_evidence(entry, schema, cid, media_type=A2A, expected_errors=None):
    """Resolve only the vendored schema; report each actual raw projection error."""
    from jsonschema import Draft202012Validator, FormatChecker
    from referencing import Registry, Resource
    registry = Registry().with_resource(schema["$id"], Resource.from_contents(schema))
    validator = Draft202012Validator({"$ref": schema["$id"] + "#/$defs/ArdEntry"},
                                    registry=registry, format_checker=FormatChecker())
    assert entry["identifier"] == "urn:ai:org.agntcy:cid:" + cid, "wrong native record projection"
    assert entry["type"] == media_type, "native projection changed format"
    errors = sorted([dict(path=list(error.absolute_path), validator=error.validator)
                     for error in validator.iter_errors(entry)], key=lambda item: str(item["path"]))
    assert errors == (ENTRY_ERRORS if expected_errors is None else expected_errors), "raw-entry schema findings differ from reviewed identifier/metadata gaps"
    return dict(profile=CURRENT, entries=1, valid=0, errors=errors, identifier=entry["identifier"])


def require(evidence, **expected):
    if not isinstance(evidence, dict):
        raise ValueError("missing assertion evidence")
    for key, value in expected.items():
        if type(evidence.get(key)) is not type(value) or evidence[key] != value:
            raise ValueError("missing or contradictory evidence: " + key)


def check_search_errors(nested, scalar):
    """Distinguish the query-shape refusal from an unconfigured native extractor."""
    for document, code in ((nested, 3), (scalar, 14)):
        require(document, code=code, details=[])
        message = document.get("message")
        if not isinstance(message, str) or len(message) > 1024:
            raise ValueError("missing or oversized native Search error")
    if "invalid value for string field query: {" not in nested["message"]:
        raise ValueError("native Search did not reject the query object")
    if scalar["message"] != "natural-language search is unavailable: no OASF extractor is configured on this gateway":
        raise ValueError("native Search did not report the absent extractor")


def check_cases(cases, cid, expected=None):
    """Require all four groups and typed evidence; unrun or partial work cannot pass."""
    expected = expected if expected is not None else dict(dispositions=DISPOSITIONS, checker_errors=ERRORS,
        entry_errors=ENTRY_ERRORS, manifest_entries=0, members=0, observations=0, nested_status=400, scalar_status=503)
    dispositions = expected["dispositions"]
    if not isinstance(cases, list) or len(cases) != len(dispositions):
        raise ValueError("incomplete AGNTCY case inventory")
    seen = set()
    for case in cases:
        if not isinstance(case, dict):
            raise ValueError("invalid AGNTCY case")
        name = case.get("name")
        if not isinstance(name, str) or name not in dispositions or name in seen:
            raise ValueError("unknown or duplicate AGNTCY case")
        seen.add(name)
        require(case, disposition=dispositions[name], status="pass")
        evidence = case.get("evidence")
        if not isinstance(evidence, dict):
            raise ValueError("missing AGNTCY case evidence")
        if name == "artifact":
            require(evidence, cid=cid, profile=PROFILE, validation="complete", retained=True, native_preserved=True)
            digests = [evidence.get(key) for key in ("input_sha256", "export_sha256", "retained_sha256")]
            if not isinstance(digests[0], str) or not re.fullmatch(r"[0-9a-f]{64}", digests[0]) or len(set(digests)) != 1:
                raise ValueError("A2A content did not survive interchange")
        elif name == "discovery":
            require(evidence, native_record=cid, manifest_entries=expected["manifest_entries"], profile=CURRENT, source_type="ard_manifest",
                    state="complete", members=expected["members"], observations=expected["observations"], current_pointer=True, bytes_preserved=True)
            if not all(isinstance(evidence.get(key), str) and evidence[key] for key in ("source_id", "job_id", "snapshot_id")):
                raise ValueError("missing real worker snapshot identity")
        elif name == "search":
            require(evidence, nested_status=expected["nested_status"], scalar_status=expected["scalar_status"], catalog_status=400, catalog_code="INVALID_ARGUMENT")
            check_search_errors(evidence.get("nested_error"), evidence.get("scalar_error"))
        else:
            modes = evidence.get("modes", {})
            if set(modes) != {"manifest", "publisher", "registry"}:
                raise ValueError("incomplete native conformance modes")
            for mode, result in modes.items():
                check_checker(mode, result, expected["checker_errors"])
            require(evidence["raw_entry"], profile=CURRENT, entries=1, valid=0, identifier="urn:ai:org.agntcy:cid:" + cid)
            if evidence["raw_entry"].get("errors") != expected["entry_errors"]:
                raise ValueError("missing independent raw-entry schema findings")
    return len(cases)
