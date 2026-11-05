"""Independent Catalog evidence and failure oracles for the selected A2A handoff."""

import hashlib
import json

import a2a_contract as control
from catalog_control import CATALOG_DENIALS, catalog_denial, validate_search_record
from common import Result

CARD = "application/a2a-agent-card+json"
PROFILE = "a2a-agent-card-1.0.1-3303592"
IDENTIFIER = "urn:air:catalog.example.com:a2a:hello-world"
TAG = "a2a-handoff-qualification"
GOVERNED_CASES = ("governance", "cli-discovery", "public-projection", "revision-binding")


def canonical(card):
    """Match Catalog semantic JSON for this ASCII, number-free deployment card only."""
    def number_free(value):
        if isinstance(value, dict):
            return all(number_free(item) for item in value.values())
        if isinstance(value, list):
            return all(number_free(item) for item in value)
        return value is None or isinstance(value, (str, bool))

    assert number_free(card), "numeric JSON needs a separate digest review"
    raw = json.dumps(card, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    assert raw.isascii() and not any(c in raw for c in "<>&"), "card exceeds reviewed digest oracle"
    return raw.encode()


def digest(card):
    return "sha256:" + hashlib.sha256(canonical(card)).hexdigest()


def representations(card):
    return [{"role": "primary", "mediaType": CARD, "data": card}]


def validate_record(record, expected, card):
    for key in ("resource", "version", "versionRevision", "listingRevision", "release"):
        assert record[key]["id"] == expected[key], "changed " + key + " identity"
    assert record["resource"]["canonicalIdentifier"] == IDENTIFIER and record["resource"]["kind"] == "agent"
    assert record["version"]["resourceId"] == expected["resource"] and record["version"]["version"] == "1.0.0"
    assert record["version"]["sealedRevisionId"] == expected["versionRevision"]
    assert record["versionRevision"]["resourceVersionId"] == expected["version"]
    assert record["listingRevision"]["versionRevisionId"] == record["release"]["versionRevisionId"] == expected["versionRevision"]
    assert record["release"]["listingRevisionId"] == expected["listingRevision"]
    assert record["listingRevision"]["listingId"] == record["release"]["listingId"] == record["listing"]["id"] == expected["listing"]
    assert record["release"]["state"] == "published" and record["listing"]["currentReleaseId"] == expected["release"]
    assert record["versionRevision"]["contentSetDigest"] == expected["contentSetDigest"] == record["evidence"]["subjectDigest"]
    assert record["evidence"]["state"] == "complete"
    assert record["versionRevision"]["evidenceSetId"] == record["evidence"]["id"]
    actual = record["versionRevision"]["representations"]
    assert len(actual) == 1
    selected = actual[0]
    assert (selected["role"], selected["mediaType"], selected["profileId"], selected["validationState"]) == ("primary", CARD, PROFILE, "valid")
    assert selected["data"] == card and selected["contentDigest"] == digest(card)
    assert selected["size"] == len(canonical(card)) and not selected.get("url")
    control.select_card(selected["data"])
    return selected["data"]


def select_record(search, read, expected, card):
    page = search["data"]
    assert page["partial"] is False and not page.get("nextCursor") and len(page["items"]) == 1
    item = page["items"][0]
    assert item["type"] == "local" and item["kind"] == "agent" and item["versionPresent"] is True and item["version"] == "1.0.0"
    selected = item["local"]["release"]["id"]
    assert selected == expected["release"] and selected not in (item["id"], item["local"]["resource"]["id"])
    record = read(selected)["data"]
    validate_record(record, expected, card)
    validate_search_record(item, record)
    return record


def projection(manifest, record, card):
    entries = manifest["entries"]
    assert len(entries) == 2
    matches = [entry for entry in entries if entry["identifier"] == IDENTIFIER]
    assert len(matches) == 1
    entry = matches[0]
    assert entry["metadata"]["catalogReleaseId"] == record["release"]["id"]
    assert entry["type"] == CARD and entry["version"] == "1.0.0" and entry["data"] == card and not entry.get("url")
    for key in ("displayName", "description", "tags"):
        assert entry[key] == record["listingRevision"][key]
    other = next(e for e in entries if e["identifier"] != IDENTIFIER)
    assert other["type"] == "application/ai-registry+json" and other["metadata"]["catalogReleaseId"] == "registry:self"


def failure_result(case, result):
    """Require the native SDK transport failure, never an arbitrary nonzero exit."""
    assert case in ("untrusted-ca", "unavailable") and result.code == 3 and not result.stderr.strip()
    error = json.loads(result.stdout)
    assert set(error) == {"error", "causes"} and error["error"] == "A2AClientError"
    causes = error["causes"]
    assert any(item["type"] == "ConnectError" for item in causes)
    if case == "untrusted-ca":
        assert any(item["type"] == "SSLCertVerificationError" for item in causes)
        assert any("CERTIFICATE_VERIFY_FAILED" in item["message"] for item in causes), "not a trust failure"
    else:
        assert any("All connection attempts failed" in item["message"] for item in causes), "not a listener outage"
        assert any(item["type"] == "ConnectionRefusedError" and "('127.0.0.1', 9443)" in item["message"] for item in causes), "not the owned listener"
        assert not any("CERTIFICATE_VERIFY_FAILED" in item["message"] for item in causes)
    return {"exit": result.code, "outcome": "tls-rejected" if case == "untrusted-ca" else "unavailable", "native": error}


def complete(report):
    assert report["scope"] == "a2a-governed-handoff"
    control.complete(dict(report, scope="direct-control", observations=report["direct_observations"]))
    assert set(report["governed_cases"]) == set(GOVERNED_CASES) and all(v is True for v in report["governed_cases"].values())
    selection = report["selection"]
    assert selection["versionRevision"] != selection["draftRevision"] and selection["listingRevision"] != selection["draftListingRevision"]
    assert selection["cardDigest"] == digest(report["result"]["card"]) and selection["profiles"] == {CARD: PROFILE}
    assert report["metadata_observations"] == report["direct_observations"]
    assert report["handoff_exit"] == 0 and report["handoff_result"]["card"] == report["result"]["card"]
    control.check_result(report["handoff_result"], governed=True)
    observed = report["handoff_observations"]
    assert observed[:3] == report["direct_observations"]
    control.check_traffic(observed[3:], report["handoff_result"], governed=True)
    assert report["handoff_result"]["send"]["id"] != report["result"]["send"]["id"]
    assert set(report["catalog_denials"]) == set(CATALOG_DENIALS)
    for case, (_, status, code) in CATALOG_DENIALS.items():
        denied = report["catalog_denials"][case]
        assert (denied["api_status"], denied["api_code"], denied["cli_exit"], denied["outcome"]) == (status, code, 1, "denied")
        catalog_denial(case, Result(denied["cli_exit"], "", denied["cli_error"]))
        assert denied["handoff_created"] is False and denied["observations"] == report["metadata_observations"]
    assert set(report["failures"]) == {"untrusted-ca", "unavailable"}
    for case, result in report["failures"].items():
        assert failure_result(case, Result(result["exit"], json.dumps(result["native"]), "")) == result
    assert report["observations"] == observed and report["server_stopped"] is True and report["post_failure_integrity"] is True
    assert set(report["binaries"]) == {"catalogd", "catalogctl"}
    assert all(len(value) == 64 and int(value, 16) >= 0 for value in report["binaries"].values())
