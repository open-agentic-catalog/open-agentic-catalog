"""Independent Agent Finder retention assertions, with no processes or network."""

import base64
import hashlib
import json
import re

import agentfinder_tools as tools

TENANT = "00000000-0000-7000-8000-000000000001"
LEGACY = "ai-catalog-1.0-b062278"
PHASES = ("preflight", "setup", "confinement", "inspection", "retention", "repeat", "outage", "recovery", "refusals", "final-checks")


def digest(raw):
    """Use the same qualified digest syntax as retained Catalog evidence."""
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def identity(entry):
    """Keep absent versions distinct from supplied values."""
    return entry["identifier"], "version" in entry, entry.get("version", "")


def check_members(members, entries, source_id):
    """Empty output, duplicate identities and substituted metadata cannot pass."""
    expected = {identity(entry): entry for entry in entries}
    assert len(members) == len(entries) == tools.TOTAL_ENTRIES, "incomplete administrative inventory"
    assert len({row["id"] for row in members}) == len(members), "duplicate observation IDs"
    seen = set()
    for row in members:
        key = row["identifier"], row["versionPresent"], row.get("version", "")
        assert type(row["versionPresent"]) is bool and key in expected and key not in seen, "observation identity changed"
        seen.add(key)
        assert row["sourceId"] == source_id and row["tenantId"] == TENANT, "observation scope changed"
        assert row["mediaType"] == expected[key]["type"] and row["freshness"] == "current", "observation type/freshness changed"
        assert not row.get("kind") and not row.get("normalizedEntryDigest"), "unsupported media acquired a search projection"
        assert row["rawEntryAvailable"] is True and row["compatibility"]["profileId"] == LEGACY
        assert row["compatibility"]["support"] == "unknown" and row["compatibility"]["structural"] == "valid"


def check_raw(raw, entry, member, artifact):
    """Require both original entry bytes and the matching administrative digest."""
    assert json.loads(raw) == entry and raw in artifact, "raw publisher metadata changed"
    assert digest(raw) == member["exactEntryDigest"], "raw entry digest changed"


def check_sql_members(rows, entries, members, source_id, snapshot):
    """Bind every native entry to its retained bytes and exact snapshot occurrence."""
    assert len(rows) == len(entries), "incomplete SQL membership"
    by_location = {row["entryLocation"]: row for row in rows}
    assert len(by_location) == len(rows), "duplicate SQL occurrence location"
    by_identity = {(m["identifier"], m["versionPresent"], m.get("version", "")): m for m in members}
    for index, entry in enumerate(entries):
        row = by_location["/entries/" + str(index)]
        member = by_identity[identity(entry)]
        raw = base64.b64decode(row["raw"], validate=True)
        assert json.loads(raw) == row["entry"] == entry, "SQL raw metadata changed"
        assert row["id"] == member["id"] and row["exactEntryDigest"] == digest(raw) == member["exactEntryDigest"]
        assert row["sourceId"] == source_id and row["tenantId"] == TENANT
        assert row["snapshotId"] == snapshot["id"] and row["sourceRevision"] == snapshot["sourceRevision"]
        assert row["retrievalDigest"] == "sha256:" + tools.EXPECTED_SHA256 and row["evidenceComplete"] is True
        assert row["versionPresent"] is ("version" in entry) and row["version"] == entry.get("version")


def check_bindings(value):
    """Require all independent SQL predicates, not truthy or missing values."""
    required = {"source_bound", "inspection_bound", "connection_bound", "job_bound", "snapshot_bound", "audit_bound", "no_adoption"}
    assert set(value) == required and all(value[k] is True for k in required), "SQL source/Job/snapshot binding failed"


def uuid_literal(value):
    """Admit only native UUID values before interpolation into read-only SQL."""
    assert isinstance(value, str) and re.fullmatch(r"[a-f0-9]{8}(?:-[a-f0-9]{4}){3}-[a-f0-9]{12}", value), "invalid evidence UUID"
    return value


def finish(report):
    """A successful exit requires every phase and independent owned cleanup."""
    return (report.get("cleanup") == "complete" and not report.get("error")
            and report.get("phases") == dict.fromkeys(PHASES, "passed"))


def merge_client(report, child):
    """Preserve partial HTTP outcomes without replacing supervisor ownership."""
    assert all(child.get(k) == report[k] for k in ("project", "sourceCommit", "sourceDigest")), "client report source/ownership changed"
    assert child.get("phase") in PHASES and set(child.get("phases", {})) == set(PHASES), "invalid client phase inventory"
    assert all(v in ("unrun", "passed", "failed") for v in child["phases"].values()), "invalid client phase outcome"
    for key in ("phase", "phases", "inspection", "genericTransportRefusal", "sourceId", "retention", "repeat", "outage", "recovery", "refusals", "members", "traffic", "unexpectedResponses", "error"):
        if key in child:
            report[key] = child[key]
