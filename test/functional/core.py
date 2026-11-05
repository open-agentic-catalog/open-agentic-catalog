#!/usr/bin/env python3
"""Fixed late-stage Catalog journeys over the existing disposable smoke seed.

Run after replay/restore qualification: release withdrawal deliberately changes
the seed. No fixture SQL substitutes for a product mutation or readback.
"""

import argparse
import base64
from datetime import datetime
import hashlib
import io
import json
import os
from pathlib import Path
import urllib.parse
import zipfile

from federation import Client, key, observations, snapshot_items
import feedback
from http_client import fixture_tls_context
from journeys import audit_once, header
from runner import read_document, record_scenario_failure, write_document


def mutate(api, method, path, body, status=200, etag=None):
    headers = {"Idempotency-Key": key("core")}
    if etag:
        headers["If-Match"] = etag
    result, response = api.request(method, path, body, (status,), headers)
    replay, replay_headers = api.request(method, path, body, (status,), headers)
    assert result == replay and header(response, "ETag") == header(replay_headers, "ETag"), "core replay mismatch"
    assert header(replay_headers, "Idempotency-Replayed") == "true", "core command did not replay"
    return result, response


def records(api):
    values = []
    cursor = ""
    seen = set()
    for _ in range(10):
        page, _ = api.request("GET", "/api/v1/catalog/resources?" + urllib.parse.urlencode({"limit": 1, "cursor": cursor}))
        assert len(page["items"]) <= 1, "browse page exceeded bound"
        values.extend(page["items"])
        cursor = page.get("nextCursor", "")
        if not cursor:
            assert len(values) == page["total"], "browse total mismatch"
            return values
        assert cursor not in seen, "browse cursor loop"
        seen.add(cursor)
    raise AssertionError("owned browse exceeded fixture bound")


def history(api, path, expected, **filters):
    """Traverse a tiny owned history against known IDs, not first-response truth."""
    assert len(expected) <= 8, "history fixture exceeded its small bound"
    wanted = sorted(expected, key=lambda item: item["id"])
    items, cursors = [], set()
    cursor = ""
    for _ in range(len(wanted) + 1):
        parameters = dict(filters, limit=1)
        if cursor:
            parameters["cursor"] = cursor
        query = urllib.parse.urlencode(parameters)
        page, _ = api.request("GET", path + "?" + query)
        assert len(page["items"]) <= 1, "history page exceeded limit"
        items.extend(page["items"])
        cursor = page.get("nextCursor", "")
        if not cursor:
            assert [item["id"] for item in items] == [item["id"] for item in wanted], "history lost, duplicated or reordered records"
            for actual, reference in zip(items, wanted, strict=True):
                assert all(actual.get(name) == value for name, value in reference.items()), "history content mismatch"
            return items
        assert cursor not in cursors, "history cursor loop"
        cursors.add(cursor)
    raise AssertionError("history did not terminate within fixture bound")


def revision_summary(revision, digest_field):
    return {"id": revision["id"], "revisionNumber": revision["revisionNumber"], "digest": revision[digest_field]}


def exact_record(api, summary):
    """Check every projected field, then retain the complete record for journeys."""
    exact, _ = api.request("GET", "/api/v1/catalog/resources/" + summary["release"]["id"])
    assert "representations" not in summary["versionRevision"] and "findings" not in summary["evidence"], "browse returned document bodies"
    for section in ("resource", "version", "versionRevision", "listing", "listingRevision", "release", "evidence"):
        for field, value in summary[section].items():
            if section == "evidence" and field in ("trust", "legalRights"):
                assert value == {"state": exact[section][field]["state"]}, "browse/exact status divergence"
            else:
                assert value == exact[section][field], "browse/exact divergence"
    assert exact["versionRevision"]["representations"], "exact read lost retained documents"
    return exact


def browse(api, other, run_id):
    values = [exact_record(api, summary) for summary in records(api)]
    assert len(values) == 3 and {value["resource"]["kind"] for value in values} == {"agent", "mcp_server", "skill"}, "three-kind browse mismatch"
    for value in values:
        path = "/api/v1/catalog/resources/" + value["release"]["id"]
        other.request("GET", path, expected=(404,))
    explore, _ = api.request("POST", "/api/v1/catalog/explore", {"query": {"tags": [run_id]}, "facets": [{"field": "kind"}]})
    assert explore["matched"] == 3, "facet total mismatch"
    buckets = explore["facets"]["kind"]["buckets"]
    assert {item["value"]: item["count"] for item in buckets} == {"agent": 1, "mcp_server": 1, "skill": 1}, "facet kind counts mismatch"
    cursor = ""
    identifiers = []
    for _ in range(4):
        query = {"tags": [run_id], "limit": 1}
        if cursor:
            query["cursor"] = cursor
        page, _ = api.request("POST", "/api/v1/catalog/search", query)
        assert page["tier"] == "core" and not page["partial"], "unexpected owned search degradation"
        identifiers += [item["local"]["release"]["id"] for item in page["items"] if item["type"] == "local"]
        cursor = page.get("nextCursor", "")
        if not cursor:
            break
    assert not cursor and set(identifiers) == {value["release"]["id"] for value in values} and len(identifiers) == 3, "search continuation lost or duplicated records"
    api.request("POST", "/api/v1/catalog/search", {"tags": [run_id], "limit": 1, "cursor": "tampered"}, (400,))
    return values


def plugin_admission(api, other, seed, run_id):
    """Exercise container admission without a public archive or private-IP bypass."""
    resource, headers = mutate(api, "POST", "/api/v1/resources", {
        "namespaceId": seed["resource"]["namespaceId"],
        "canonicalIdentifier": "urn:air:catalog.example.com:readiness:plugin-" + run_id,
        "kind": "plugin"}, 201)
    path = "/api/v1/resources/" + resource["id"]
    retained, _ = api.request("GET", path)
    # PostgreSQL retains microseconds; compare timestamp values at that precision
    # while requiring every other returned field to match exactly.
    comparable = []
    for value in (resource, retained):
        value = dict(value)
        for field in ("createdAt", "updatedAt"):
            value[field] = datetime.fromisoformat(value[field].replace("Z", "+00:00"))
        comparable.append(value)
    assert comparable[0] == comparable[1] and retained["kind"] == "plugin", "Plugin resource readback changed"
    other.request("GET", path, expected=(404,))
    representation = {"role": "primary", "mediaType": "application/agent-plugins+zip", "url": "https://127.0.0.1/plugin.zip"}
    for digest in (None, "", "sha256:invalid", 1):
        api.request("POST", path + "/versions", {"version": "1.0.0", "representations": [dict(representation, expectedDigest=digest)]}, (400,), {"Idempotency-Key": key("plugin-digest")})
    # An omitted checksum is valid, but the production fetch policy must reject
    # this private destination before connecting. Failed admission persists no version.
    api.request("POST", path + "/versions", {"version": "1.0.0", "representations": [representation]}, (422,), {"Idempotency-Key": key("plugin-private")})
    versions, _ = api.request("GET", path + "/versions")
    assert versions["items"] == [] and not versions.get("nextCursor"), "rejected Plugin admission persisted a version"
    api.request("POST", "/api/v1/observations:search", {"kinds": ["agent", "mcp_server", "skill", "plugin"]})
    mutate(api, "POST", path + ":delete", {"reason": "Owned Plugin draft cleanup"}, 204, header(headers, "ETag"))
    api.request("GET", path, expected=(404,))


def skill_package(revised=False, *, invalid=False):
    """Build inert archive bytes, optionally with invalid Skill frontmatter."""
    data = io.BytesIO()
    with zipfile.ZipFile(data, "w", compression=zipfile.ZIP_DEFLATED) as archive:
        frontmatter = "---\nname: safe-skill\ndescription: Owned packaged inspection.\n"
        if invalid:
            frontmatter += 'compatibility: ""\n'
        archive.writestr("safe-skill/SKILL.md", frontmatter + "---\n# Instructions\n" + ("Updated inert instructions.\n" if revised else ""))
        archive.writestr("safe-skill/scripts/run.sh", "#!/bin/sh\necho must-not-run\n")
    return base64.b64encode(data.getvalue()).decode("ascii")


def blocked_package(value, encoded):
    """Check failed inspection against fixture bytes, not response-derived truth."""
    assert value["evidence"]["state"] == "blocked", "invalid Skill evidence was not blocked"
    assert any(finding["code"] == "SKILL_COMPATIBILITY_INVALID" and finding["severity"] == "error"
               and finding["path"] == "/representations/0/package/SKILL.md/frontmatter/compatibility"
               for finding in value["evidence"]["findings"]), "invalid Skill finding lost its attribution"
    representation = value["revision"]["representations"][0]
    archive = base64.b64decode(encoded, validate=True)
    assert representation["contentDigest"] == "sha256:" + hashlib.sha256(archive).hexdigest() and representation["size"] == len(archive), "failed inspection changed archive identity"
    package = representation["package"]
    assert representation["validationState"] == "invalid" and package["structureState"] == "failed", "failed archive claimed successful validation"
    assert package["fileCount"] == 0 and not package.get("files") and not representation.get("normalizedFacts"), "failed archive retained partial successful inventory"
    assert "packageData" not in json.dumps(value) and "must-not-run" not in json.dumps(value), "failed archive leaked transient bytes"


def archive_journey(api, other, seed, run_id):
    resource, _ = mutate(api, "POST", "/api/v1/resources", {
        "namespaceId": seed["resource"]["namespaceId"], "canonicalIdentifier": "urn:air:catalog.example.com:readiness:archive-" + run_id, "kind": "skill"}, 201)
    handoff = {"git": {"repository": "https://git.example.com/team/skills.git", "commit": "a" * 40, "directory": "skills/safe-skill"},
               "oci": {"reference": "registry.example.com/team/safe-skill@sha256:" + "b" * 64}}
    invalid = skill_package(invalid=True)
    blocked, _ = mutate(api, "POST", "/api/v1/resources/" + resource["id"] + "/versions", {
        "version": "1.0.0", "representations": [{"role": "primary", "mediaType": "application/agent-skills+zip", "packageData": invalid, "handoff": handoff}]}, 201)
    blocked_package(blocked, invalid)
    blocked_path = "/api/v1/version-revisions/" + blocked["revision"]["id"]
    retained_blocked, _ = api.request("GET", blocked_path)
    blocked_package(retained_blocked, invalid)
    other.request("GET", blocked_path, expected=(404,))
    revalidated, _ = mutate(api, "POST", blocked_path + ":validate", {}, 201)
    blocked_package(revalidated, invalid)
    assert revalidated["evidence"]["id"] != blocked["evidence"]["id"] and revalidated["revision"]["representations"] == blocked["revision"]["representations"], "revalidation erased failure or rewrote inspection"
    version_path = "/api/v1/resource-versions/" + blocked["version"]["id"]
    _, before_headers = api.request("GET", version_path)
    correction = {"representations": [{"role": "primary", "mediaType": "application/agent-skills+zip", "packageData": skill_package(), "handoff": handoff}]}
    value, _ = mutate(api, "POST", version_path + "/revisions", correction, 201, header(before_headers, "ETag"))
    assert value["revision"]["id"] != blocked["revision"]["id"] and value["revision"]["contentSetDigest"] != blocked["revision"]["contentSetDigest"], "correction rewrote the blocked revision"
    # A rejected stale append must leave the corrected candidate and history intact.
    problem, _ = api.request("POST", version_path + "/revisions", correction, (412,), {"Idempotency-Key": key("archive-stale"), "If-Match": header(before_headers, "ETag")})
    assert problem["code"] == "PRECONDITION_FAILED", "stale archive revision failed for an unrelated reason"
    assert value["evidence"]["state"] == "complete", "package inspection blocked"
    representation = value["revision"]["representations"][0]
    package = representation["package"]
    assert package["fileCount"] == 2 and package["rootDirectory"] == "safe-skill" and package["structureState"] == "passed", "package inventory mismatch"
    assert representation["handoff"] == handoff and "packageData" not in json.dumps(value) and "must-not-run" not in json.dumps(value), "package bytes leaked or handoff changed"
    path = "/api/v1/version-revisions/" + value["revision"]["id"]
    retained, _ = api.request("GET", path)
    assert retained["revision"]["representations"][0]["handoff"] == handoff, "package handoff not persisted"
    other.request("GET", path, expected=(404,))
    version_path = "/api/v1/resource-versions/" + value["version"]["id"]
    version, headers = api.request("GET", version_path)
    assert version["id"] == value["version"]["id"] and version["candidateRevisionId"] == value["revision"]["id"] and version["revision"] == value["version"]["revision"], "stale append changed the corrected version"
    appended, _ = mutate(api, "POST", version_path + "/revisions", {
        "representations": [{"role": "primary", "mediaType": "application/agent-skills+zip", "packageData": skill_package(revised=True), "handoff": handoff}]}, 201, header(headers, "ETag"))
    assert appended["revision"]["id"] != value["revision"]["id"] and appended["version"]["revision"] == version["revision"] + 1, "revision append overwrote history"
    prior, _ = api.request("GET", path)
    assert prior["revision"] == retained["revision"], "revision append changed prior representation"
    reader = Client(api.base_url, "local-reader")
    versions_path = "/api/v1/resources/" + resource["id"] + "/versions"
    history(reader, versions_path, [{"id": value["version"]["id"], "version": "1.0.0"}])
    revisions_path = version_path + "/revisions"
    history(reader, revisions_path, [revision_summary(v["revision"], "contentSetDigest") for v in (blocked, value, appended)])
    prior_blocked, _ = api.request("GET", blocked_path)
    blocked_package(prior_blocked, invalid)
    assert prior_blocked["revision"]["representations"] == blocked["revision"]["representations"] and prior_blocked["revision"]["contentSetDigest"] == blocked["revision"]["contentSetDigest"], "correction changed retained blocked history"
    for private_path in (versions_path, revisions_path):
        other.request("GET", private_path, expected=(404,))
    first, _ = reader.request("GET", revisions_path + "?limit=1")
    foreign = "/api/v1/resource-versions/" + seed["version"]["id"] + "/revisions?" + urllib.parse.urlencode({"limit": 1, "cursor": first["nextCursor"]})
    reader.request("GET", foreign, expected=(400,))
    # Extend the existing archive journey through independently reviewed
    # publication. Retire its release before the original three-item checks.
    archive_publication(api, resource, appended, blocked)


def archive_publication(api, resource, version, blocked):
    validated, _ = mutate(api, "POST", "/api/v1/version-revisions/" + version["revision"]["id"] + ":validate", {}, 201)
    assert validated["evidence"]["state"] == "complete" and validated["revision"]["contentSetDigest"] == version["revision"]["contentSetDigest"], "revalidation changed archive content"
    publisher, headers = mutate(api, "POST", "/api/v1/publishers", {"displayName": "Archive publisher", "domains": ["catalog.example.com"]}, 201)
    publisher_path = "/api/v1/publishers/" + publisher["id"]
    publisher, headers = mutate(api, "PATCH", publisher_path, {"displayName": "Reviewed archive publisher", "reason": "Owned metadata correction"}, etag=header(headers, "ETag"))
    retained, _ = api.request("GET", publisher_path)
    assert retained["displayName"] == "Reviewed archive publisher" and retained["revision"] == publisher["revision"], "publisher update not persisted"
    mutate(api, "POST", publisher_path + "/publication-domains/catalog.example.com:approve", {"reason": "Owned archive authority"}, etag=header(headers, "ETag"))
    listing, headers = mutate(api, "POST", "/api/v1/catalog-listings", {"namespaceId": resource["namespaceId"], "resourceId": resource["id"]}, 201)
    listing_path = "/api/v1/catalog-listings/" + listing["id"]
    blocked_listing, headers = mutate(api, "POST", listing_path + "/revisions", {
        "versionRevisionId": blocked["revision"]["id"], "displayName": "Blocked archive", "description": "Invalid Skill frontmatter",
        "publisherId": publisher["id"], "visibility": "tenant", "license": "Apache-2.0"}, 201, header(headers, "ETag"))
    # Correcting a version does not silently retarget an existing listing revision.
    failed_headers = {"Idempotency-Key": key("archive-blocked-submission")}
    for _ in range(2):
        problem, _ = api.request("POST", listing_path + "/submissions", {}, (422,), failed_headers)
        assert problem["code"] == "VALIDATION_FAILED", "blocked archive submission failed for an unrelated reason"
    history(api, "/api/v1/review-submissions", [], listingId=listing["id"])
    current, current_headers = api.request("GET", listing_path)
    assert current["candidateRevisionId"] == blocked_listing["revision"]["id"] and header(current_headers, "ETag") == header(headers, "ETag"), "rejected submission changed the listing"
    listing_revision, headers = mutate(api, "POST", listing_path + "/revisions", {
        "versionRevisionId": validated["revision"]["id"], "displayName": "Reviewed archive", "description": "Owned inert Skill archive",
        "publisherId": publisher["id"], "visibility": "tenant", "license": "Apache-2.0"}, 201, header(headers, "ETag"))
    listing_etag = header(headers, "ETag")
    submission, headers = mutate(api, "POST", listing_path + "/submissions", {}, 201)
    reviewer = Client(api.base_url, "local-reviewer")
    approved, _ = mutate(reviewer, "POST", "/api/v1/review-submissions/" + submission["id"] + ":approve", {"reason": "Independent archive review"}, etag=header(headers, "ETag"))
    assert approved["submission"]["state"] == "approved", "archive review not approved"
    published, _ = mutate(api, "POST", listing_path + ":publish", {"submissionId": submission["id"]}, 201, listing_etag)
    record, _ = api.request("GET", "/api/v1/catalog/resources/" + published["release"]["id"])
    assert record["resource"]["id"] == resource["id"] and record["listingRevision"]["publisherId"] == publisher["id"], "archive publication lost identity"
    reader, other = Client(api.base_url, "local-reader"), Client(api.base_url, "other-tenant-admin")
    evidence_path = "/api/v1/review-submissions/" + submission["id"] + "/evidence"
    bound, _ = reader.request("GET", evidence_path)
    assert (bound["id"], bound["digest"], bound["policyVersion"]) == (submission["evidenceSetId"], submission["evidenceDigest"], submission["policyVersion"]), "submission evidence binding changed"
    renewed, _ = mutate(api, "POST", "/api/v1/version-revisions/" + version["revision"]["id"] + ":validate", {}, 201)
    assert renewed["evidence"]["id"] != bound["id"], "revalidation did not create new evidence"
    retained_bound, _ = reader.request("GET", evidence_path)
    assert retained_bound == bound, "historical submission returned latest evidence"
    other.request("GET", evidence_path, expected=(404,))

    # Replace this small tenant-only release, retaining both immutable histories.
    _, current_headers = api.request("GET", listing_path)
    second_revision, headers = mutate(api, "POST", listing_path + "/revisions", {
        "versionRevisionId": version["revision"]["id"], "displayName": "Reviewed archive updated", "description": "Revised listing metadata",
        "publisherId": publisher["id"], "visibility": "tenant", "license": "Apache-2.0"}, 201, header(current_headers, "ETag"))
    listing_etag = header(headers, "ETag")
    second_submission, headers = mutate(api, "POST", listing_path + "/submissions", {}, 201)
    mutate(reviewer, "POST", "/api/v1/review-submissions/" + second_submission["id"] + ":approve", {"reason": "Independent revised review"}, etag=header(headers, "ETag"))
    replacement, _ = mutate(api, "POST", listing_path + ":publish", {"submissionId": second_submission["id"], "priorDisposition": "superseded", "priorReason": "Reviewed listing replacement"}, 201, listing_etag)
    prior_release, _ = reader.request("GET", "/api/v1/listing-releases/" + published["release"]["id"])
    assert prior_release["state"] == "superseded" and prior_release["replacementReleaseId"] == replacement["release"]["id"], "replacement lost prior release linkage"
    api.request("GET", "/api/v1/catalog/resources/" + prior_release["id"], expected=(404,))
    listing_histories = (
        ("/api/v1/resources/" + resource["id"] + "/listings", [{"id": listing["id"], "currentReleaseId": replacement["release"]["id"]}]),
        (listing_path + "/revisions", [revision_summary(v["revision"], "digest") for v in (blocked_listing, listing_revision, second_revision)]),
        (listing_path + "/releases", [{"id": prior_release["id"], "state": "superseded"}, {"id": replacement["release"]["id"], "state": "published"}]),
    )
    for history_path, expected in listing_histories:
        history(reader, history_path, expected)
        other.request("GET", history_path, expected=(404,))
    submissions_path = "/api/v1/review-submissions"
    history(reader, submissions_path, [{"id": v["id"], "listingId": listing["id"], "state": "approved"} for v in (submission, second_submission)], listingId=listing["id"], state="approved")
    history(reader, submissions_path, [], listingId=listing["id"], state="rejected")
    other.request("GET", submissions_path + "?listingId=" + listing["id"], expected=(404,))
    published = replacement
    release_path = "/api/v1/listing-releases/" + published["release"]["id"]
    _, headers = api.request("GET", release_path)
    retired, _ = mutate(api, "POST", release_path + ":retire", {"reason": "Finish owned archive trial"}, etag=header(headers, "ETag"))
    assert retired["state"] == "retired", "archive release not retired"
    api.request("GET", "/api/v1/catalog/resources/" + retired["id"], expected=(404,))


def observed_reads(api, public, values):
    # Assert the existing seed instead of reconstructing another smoke suite.
    page, _ = api.request("GET", "/api/v1/resources?limit=100")
    assert {v["resource"]["id"] for v in values}.issubset({v["id"] for v in page["items"]}), "resource list lost seed"
    jobs, _ = api.request("GET", "/api/v1/jobs?limit=1")
    assert len(jobs["items"]) <= 1, "Job list exceeded requested page"
    for value in values:
        revision, _ = api.request("GET", "/api/v1/listing-revisions/" + value["listingRevision"]["id"])
        assert revision == value["listingRevision"], "listing revision differs from published record"
    skill = next(v for v in values if v["resource"]["kind"] == "skill")
    artifact = skill["versionRevision"]["representations"][0]["artifactId"]
    raw, headers = api.request("GET", "/api/v1/artifacts/" + artifact + "/content", decode_body=False)
    assert hashlib.sha256(raw).hexdigest() == artifact and int(header(headers, "Content-Length")) == len(raw), "private document bytes changed"
    manifest, _ = public.request("GET", "/.well-known/ai-catalog.json")
    assert len(manifest["entries"]) >= 3, "AI Catalog omitted published seed"
    listing, _ = public.request("GET", "/ard/v1/agents?pageSize=100")
    expected = {v["resource"]["canonicalIdentifier"] for v in values}
    assert expected.issubset({v["identifier"] for v in listing["items"]}), "ARD List omitted published seed"
    facets, _ = public.request("POST", "/ard/v1/explore", {"query": {}, "resultType": {"facets": [{"field": "type"}]}})
    assert len(facets["facets"]) == 1, "ARD Explore omitted requested facet"


def membership_journey(api, run_id):
    assignment, _ = mutate(api, "POST", "/api/v1/role-assignments", {"subject": run_id + "-observed-reader", "actorType": "human", "role": "reader", "reason": "Owned membership lifecycle"}, 201)
    path = "/api/v1/memberships/" + assignment["membershipId"]
    membership, headers = api.request("GET", path)
    assert membership["subject"] == assignment["subject"], "membership subject mismatch"
    page, _ = api.request("GET", "/api/v1/memberships?limit=1")
    assert len(page["items"]) == 1 and page.get("nextCursor"), "membership pagination missing"
    next_page, _ = api.request("GET", "/api/v1/memberships?" + urllib.parse.urlencode({"limit": 1, "cursor": page["nextCursor"]}))
    assert len(next_page["items"]) == 1 and next_page["items"][0]["id"] != page["items"][0]["id"], "membership continuation repeated first page"
    grants, _ = api.request("GET", "/api/v1/role-assignments?" + urllib.parse.urlencode({"membershipId": membership["id"], "limit": 10}))
    assert [v["id"] for v in grants["items"]] == [assignment["id"]], "member-scoped grants mismatch"
    for action, state in (("suspend", "suspended"), ("reactivate", "active"), ("offboard", "offboarded")):
        changed, headers = mutate(api, "POST", path + ":" + action, {"reason": "Owned membership " + action}, etag=header(headers, "ETag"))
        retained, _ = api.request("GET", path)
        assert retained["state"] == state and retained["revision"] == changed["membership"]["revision"], "membership transition not persisted"


def snapshot(api):
    candidate, _ = mutate(api, "POST", "/api/v1/publication/snapshots", {"host": "127.0.0.1"}, 201)
    activated, _ = mutate(api, "POST", "/api/v1/publication/snapshots/" + candidate["id"] + ":activate",
                         {"host": "127.0.0.1", "reason": "Owned lifecycle qualification"}, etag=candidate["etag"])
    return activated


def publication_history(api, public):
    first = snapshot(api)
    second = snapshot(api)
    hosts, _ = api.request("GET", "/api/v1/publication/hosts")
    assert {"host": "127.0.0.1", "mode": "anonymous"} in hosts["items"], "tenant publication host choices missing"
    history, _ = api.request("GET", "/api/v1/publication/snapshots?host=127.0.0.1&limit=10")
    assert {first["id"], second["id"]}.issubset({item["id"] for item in history["items"]}), "snapshot history missing"
    retained, _ = api.request("GET", "/api/v1/publication/snapshots/" + first["id"] + "?host=127.0.0.1")
    restored, _ = mutate(api, "POST", "/api/v1/publication/snapshots/" + first["id"] + ":rollback",
                        {"host": "127.0.0.1", "reason": "Owned rollback qualification"}, etag=retained["etag"])
    assert restored["id"] == first["id"] and restored["state"] == "active", "rollback did not restore selected snapshot"
    audit_once(api, "publication.rolled_back", "publication_snapshot", first["id"])
    _, headers = public.request("GET", "/.well-known/ard.json")
    assert header(headers, "ETag") == restored["etag"], "rollback public validator mismatch"
    mutate(api, "POST", "/api/v1/publication:unpublish", {"host": "127.0.0.1", "reason": "Owned unpublish qualification"})
    audit_once(api, "publication.unpublished", "publication_snapshot", first["id"])
    public.request("GET", "/.well-known/ard.json", expected=(404,))
    public.request("GET", "/.well-known/ard.json", expected=(404,), headers={"If-None-Match": restored["etag"]})
    snapshot(api)
    _, headers = public.request("GET", "/.well-known/ard.json")
    public.request("GET", "/.well-known/ard.json", expected=(304,), headers={"If-None-Match": header(headers, "ETag")})


def administration(api, other, public, base, seed, run_id):
    publisher_path = "/api/v1/publishers/" + seed["listingRevision"]["publisherId"]
    publisher, headers = api.request("GET", publisher_path)
    other.request("GET", publisher_path, expected=(404,))
    page, _ = api.request("GET", "/api/v1/publishers?limit=100")
    assert publisher["id"] in {item["id"] for item in page["items"]}, "publisher list lost exact record"
    domain = run_id + ".catalog.example.com"
    revoked, headers = mutate(api, "POST", publisher_path + "/publication-domains/" + domain + ":revoke",
                              {"reason": "Owned domain withdrawal"}, etag=header(headers, "ETag"))
    assert domain not in revoked.get("approvedPublicationDomains", []), "publisher approval not revoked"
    audit_once(api, "publisher.publication_domain_revoked", "publisher", publisher["id"])
    public.request("GET", "/.well-known/ard.json", expected=(404,))
    mutate(api, "POST", publisher_path + "/publication-domains/" + domain + ":approve",
           {"reason": "Restore owned domain approval"}, etag=header(headers, "ETag"))
    snapshot(api)
    # A namespace-scoped grant affects the same ordinary reader immediately;
    # the configured development token cannot manufacture or retain the role.
    reader = Client(base, "local-reader")
    me, _ = reader.request("GET", "/api/v1/me")
    assert me["subject"] == "local-reader" and "resource.read" in me["permissions"] and "roles" not in me and not me["scopeEnforcement"], "self access did not resolve current reader"
    Client(base).request("GET", "/api/v1/me", expected=(401,))
    body = {"namespaceId": seed["resource"]["namespaceId"], "canonicalIdentifier": "urn:air:catalog.example.com:readiness:role-" + run_id, "kind": "skill"}
    reader.request("POST", "/api/v1/resources", body, (403,), {"Idempotency-Key": key("role-denied")})
    assignment, headers = mutate(api, "POST", "/api/v1/role-assignments", {
        "subject": "local-reader", "actorType": "human", "role": "contributor", "namespaceId": body["namespaceId"], "reason": "Owned scoped grant"}, 201)
    access_path = "/api/v1/me?namespaceId=" + body["namespaceId"]
    access, _ = reader.request("GET", access_path)
    assert access["membershipId"] == assignment["membershipId"] and len(access["namespaceGrants"]) == 1 and access["namespaceGrants"][0]["namespaceId"] == body["namespaceId"] and set(access["namespaceGrants"][0]["permissions"]) == set(me["permissions"]) | {"namespace.read", "resource.read", "resource.read_history", "resource.read_content", "resource.create", "resource.edit", "resource.delete_draft", "resource.validate", "review.submit"}, "self access omitted scoped grant"
    create_headers = {"Idempotency-Key": key("scoped-create")}
    resource, resource_headers = reader.request("POST", "/api/v1/resources", body, (201,), create_headers)
    replay, replay_headers = reader.request("POST", "/api/v1/resources", body, (201,), create_headers)
    assert replay == resource and header(replay_headers, "Idempotency-Replayed") == "true", "scoped create did not replay"
    revoked, _ = mutate(api, "POST", "/api/v1/role-assignments/" + assignment["id"] + ":revoke", {"reason": "Owned grant revocation"}, etag=header(headers, "ETag"))
    assert revoked["state"] == "revoked", "role grant not revoked"
    access, _ = reader.request("GET", access_path)
    assert access["namespaceGrants"] == [] and access["permissions"] == me["permissions"], "self access retained revoked grant"
    audit_once(api, "identity.role_assignment.revoked", "role_assignment", assignment["id"])
    denied, denied_headers = reader.request("POST", "/api/v1/resources", body, (403,), create_headers)
    assert denied["code"] == "FORBIDDEN" and not header(denied_headers, "Idempotency-Replayed"), "revoked authority disclosed cached success"
    restored_grant, restored_headers = mutate(api, "POST", "/api/v1/role-assignments", {
        "subject": "local-reader", "actorType": "human", "role": "contributor", "namespaceId": body["namespaceId"], "reason": "Restore owned scoped authority"}, 201)
    try:
        replay, replay_headers = reader.request("POST", "/api/v1/resources", body, (201,), create_headers)
        assert replay == resource and header(replay_headers, "ETag") == header(resource_headers, "ETag") and header(replay_headers, "Idempotency-Replayed") == "true", "restored authority lost original command response"
        audit_once(api, "resource.created", "resource", resource["id"])
    finally:
        mutate(api, "POST", "/api/v1/role-assignments/" + restored_grant["id"] + ":revoke", {"reason": "Finish owned replay check"}, etag=header(restored_headers, "ETag"))
    body["canonicalIdentifier"] += "-denied"
    reader.request("POST", "/api/v1/resources", body, (403,), {"Idempotency-Key": key("role-revoked")})
    mutate(api, "POST", "/api/v1/resources/" + resource["id"] + ":delete", {"reason": "Owned draft cleanup"}, 204, header(resource_headers, "ETag"))
    api.request("GET", "/api/v1/resources/" + resource["id"], expected=(404,))
    # An active member without a Reader grant can still inspect their own access.
    grants, _ = api.request("GET", "/api/v1/role-assignments?membershipId=" + me["membershipId"] + "&limit=100")
    assert not grants.get("nextCursor"), "owned reader grants exceeded fixture bound"
    global_reader = [v for v in grants["items"] if v["role"] == "reader" and not v.get("namespaceId") and v["state"] == "active"]
    assert len(global_reader) == 1, "reader grant fixture is ambiguous"
    grant = global_reader[0]
    mutate(api, "POST", "/api/v1/role-assignments/" + grant["id"] + ":revoke", {"reason": "Owned no-role access check"}, etag='"rev-' + str(grant["revision"]) + '"')
    try:
        access, _ = reader.request("GET", "/api/v1/me")
        assert "resource.read" not in access["availablePermissions"] and access["namespaceGrants"] == [], "self access manufactured product authority"
        reader.request("GET", "/api/v1/resources/" + seed["resource"]["id"] + "/versions", expected=(403,))
    finally:
        mutate(api, "POST", "/api/v1/role-assignments", {"subject": "local-reader", "actorType": "human", "role": "reader", "reason": "Restore owned reader"}, 201)
    access, _ = reader.request("GET", "/api/v1/me")
    assert access["permissions"] == me["permissions"], "reader access did not recover"


def review_journey(api, reviewer, seed):
    path = "/api/v1/catalog-listings/" + seed["listing"]["id"]
    body = {name: seed["listingRevision"][name] for name in ("versionRevisionId", "displayName", "description", "publisherId", "visibility")}
    for action, state in (("request-changes", "changes_required"), ("reject", "rejected")):
        listing, headers = api.request("GET", path)
        body["description"] = "Owned " + action + " review"
        mutate(api, "POST", path + "/revisions", body, 201, header(headers, "ETag"))
        submission, headers = mutate(api, "POST", path + "/submissions", {}, 201)
        target = "/api/v1/review-submissions/" + submission["id"]
        decision, _ = mutate(reviewer, "POST", target + ":" + action, {"reason": "Owned review qualification"}, etag=header(headers, "ETag"))
        retained, headers = api.request("GET", target)
        assert retained["state"] == state and retained["revision"] == decision["submission"]["revision"], "review state not persisted"
        reviewer.request("POST", target + ":approve", {"reason": "Must remain terminal"}, (409,), {"Idempotency-Key": key("terminal"), "If-Match": header(headers, "ETag")})


def release_journey(api, public, values):
    def transition(value, action, state, **extra):
        path = "/api/v1/listing-releases/" + value["release"]["id"]
        before, headers = api.request("GET", path)
        result, _ = mutate(api, "POST", path + ":" + action, {"reason": "Owned release qualification", **extra}, etag=header(headers, "ETag"))
        retained, _ = api.request("GET", path)
        assert result["state"] == state, "release response state mismatch: " + result["state"]
        assert retained["state"] == state, "release retained state mismatch: " + retained["state"]
        assert retained["revision"] == before["revision"] + 1, "release revision mismatch: " + str(before["revision"]) + "/" + str(retained["revision"])
        audit_once(api, "listing_release." + state, "listing_release", result["id"])
    transition(values[0], "deprecate", "deprecated")
    transition(values[0], "supersede", "superseded", replacementId=values[1]["release"]["id"])
    transition(values[1], "retire", "retired")
    transition(values[2], "revoke", "revoked")
    assert records(api) == [], "withdrawn releases remain discoverable"
    public.request("GET", "/.well-known/ard.json", expected=(404,))


def live_search(api):
    fixture = Path(os.environ["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"])
    origin = Client("https://127.0.0.1:" + os.environ["CATALOG_COMPOSE_FIXTURE_PORT"], ssl_context=fixture_tls_context(fixture / "trust/functional-ca.pem"))
    response, _ = api.request("POST", "/api/v1/catalog/search", {"text": "order", "limit": 20})
    assert {item["type"] for item in response["items"]} == {"local", "indexed", "live"}, "composed search lost a result tier"
    assert response["liveCount"] == 1 and not response["partial"], "bounded live success mismatch"
    # The packaged API uses real SQL projections and an owned HTTPS peer.
    # Tiny pages must preserve the complete merge, including displaced durable rows.
    cursor, paged_ids, cursors = "", [], set()
    for _ in range(len(response["items"]) + 1):
        query = {"text": "order", "limit": 1}
        if cursor:
            query["cursor"] = cursor
        page, _ = api.request("POST", "/api/v1/catalog/search", query)
        assert len(page["items"]) == 1 and not page["partial"], "packaged core page changed population"
        paged_ids.append(page["items"][0]["id"])
        cursor = page.get("nextCursor", "")
        if not cursor:
            break
        assert cursor not in cursors, "packaged core cursor loop"
        cursors.add(cursor)
    else:
        raise AssertionError("packaged core continuation exceeded fixture bound")
    assert len(set(paged_ids)) == len(paged_ids) and paged_ids == [item["id"] for item in response["items"]], "packaged core traversal lost, duplicated or reordered results"
    indexed, _ = api.request("POST", "/api/v1/observations:search", {"text": "order", "limit": 10})
    assert indexed["items"] and all(v["observation"]["id"] for v in indexed["items"]), "indexed search lost source observations"
    diagnostic, _ = mutate(api, "POST", "/api/v1/federation/sources:live-search", {"text": "order", "reason": "Owned live diagnostic"})
    assert len(diagnostic["results"]) == 1 and not diagnostic["partial"], "live diagnostic lost successful source"
    origin.request("POST", "/control", {"mode": "partial"})
    try:
        degraded, _ = api.request("POST", "/api/v1/catalog/search", {"text": "order", "limit": 20})
        assert degraded["partial"] and degraded["liveCount"] == 0 and degraded["diagnostics"], "live failure lost safe partial evidence"
        assert {item["id"] for item in degraded["items"]} == {item["id"] for item in response["items"] if item["type"] != "live"}, "live failure lost durable search state"
    finally:
        origin.request("POST", "/control", {"mode": "initial"})
    recovered, _ = api.request("POST", "/api/v1/catalog/search", {"text": "order", "limit": 20})
    assert not recovered["partial"] and recovered["liveCount"] == 1, "live recovery failed"
    candidate, _ = mutate(api, "POST", "/api/v1/publication/snapshots", {"host": "private.catalog.test"}, 201)
    mutate(api, "POST", "/api/v1/publication/snapshots/" + candidate["id"] + ":activate", {"host": "private.catalog.test", "reason": "Owned authenticated traversal"}, etag=candidate["etag"])
    for mode in ("none", "referrals", "auto"):
        query = {"query": {"text": "order"}, "federation": mode, "pageSize": 1}
        results = []
        for _ in range(6):
            page, headers = api.request("POST", "/ard/v1/search", query, headers={"Host": "private.catalog.test"})
            assert header(headers, "Cache-Control") == "no-store" and "Authorization" in header(headers, "Vary"), "authenticated Search cache boundary changed"
            results.extend(page["results"])
            if mode == "referrals":
                assert len(page.get("referrals", [])) == 1, "approved referral missing"
            if not page.get("pageToken"):
                break
            if mode == "auto" and "pageToken" not in query:
                # A second authorized principal cannot reuse this caller's token.
                Client(api.base_url, "local-reader").request("POST", "/ard/v1/search", {**query, "pageToken": page["pageToken"]},
                                                            expected=(400,), headers={"Host": "private.catalog.test"})
            query["pageToken"] = page["pageToken"]
        else:
            raise AssertionError("owned ARD traversal exceeded fixture bound")
        print("ARD traversal", mode, "results", len(results), "unique", len({item["identifier"] for item in results}))
        assert len(results) == (4 if mode == "auto" else 3) and len({item["identifier"] for item in results}) == len(results), "ARD traversal lost or duplicated results"
    origin.request("POST", "/control", {"mode": "partial"})
    try:
        degraded, headers = api.request("POST", "/ard/v1/search", {"query": {"text": "order"}, "federation": "auto", "pageSize": 10}, headers={"Host": "private.catalog.test"})
        assert len(degraded["results"]) == 3 and "partial" in header(headers, "Server-Timing") and "diagnostics" not in degraded, "ARD partial failure lost local results or leaked diagnostics"
    finally:
        origin.request("POST", "/control", {"mode": "initial"})


def source_journey(api, other, seed):
    sources, _ = api.request("GET", "/api/v1/federation/sources?limit=10")
    assert len(sources["items"]) == 1, "owned source count mismatch"
    source = sources["items"][0]
    path = "/api/v1/federation/sources/" + source["id"]
    retained, headers = api.request("GET", path)
    other.request("GET", path, expected=(404,))
    tested, headers = mutate(api, "POST", path + ":test", {}, etag=header(headers, "ETag"))
    assert tested["status"] == "succeeded", "source test failed"
    preview, _ = mutate(api, "POST", path + "/change-previews", {"intent": "update", "displayName": "Owned revised source"})
    updated, headers = mutate(api, "PATCH", path, {"displayName": "Owned revised source", "changePreviewDigest": preview["digest"], "reason": "Owned source update"}, etag=header(headers, "ETag"))
    assert updated["displayName"] == "Owned revised source", "source update not retained"
    current = observations(api, source["id"])
    assert current, "current source observation missing"
    observed = current[0]
    reader = Client(api.base_url, "local-reader")
    identity = {"identifier": observed["identifier"], "versionPresent": str(observed["versionPresent"]).lower()}
    if observed["versionPresent"]:
        identity["version"] = observed["version"]
    indexed_path = "/api/v1/catalog/indexed-record?" + urllib.parse.urlencode(identity)
    indexed, _ = reader.request("GET", indexed_path)
    assert indexed["type"] == "indexed" and indexed["identifier"] == observed["identifier"], "indexed bookmark lost identity"
    assert indexed["versionPresent"] == observed["versionPresent"] and indexed.get("version", "") == observed.get("version", ""), "indexed bookmark lost version identity"
    search, _ = reader.request("POST", "/api/v1/catalog/search", {"text": observed["identifier"], "limit": 10})
    matches = [item for item in search["items"] if item["type"] == "indexed" and item["identifier"] == observed["identifier"]
               and item["versionPresent"] == observed["versionPresent"] and item.get("version", "") == observed.get("version", "")]
    assert len(matches) == 1 and matches[0]["indexed"] == indexed["indexed"], "Search and bookmark provenance diverged"
    assert indexed["indexed"]["sourceIds"] == [source["id"]] and not indexed["indexed"]["sourcesTruncated"], "bookmark lost owned source attribution"
    other.request("GET", indexed_path, expected=(404,))
    for action in ("pause", "resume"):
        updated, headers = mutate(api, "POST", path + ":" + action, {"reason": "Owned source administration"}, etag=header(headers, "ETag"))
        retained, read_headers = api.request("GET", path)
        assert retained["revision"] == updated["revision"] and header(read_headers, "ETag") == header(headers, "ETag"), "source transition readback mismatch"
        bookmark, _ = reader.request("GET", indexed_path, expected=(404,) if action == "pause" else (200,))
        if action == "resume":
            # Current source authority advances; retained entry provenance does not.
            expected_bookmark = {**indexed, "indexed": {**indexed["indexed"], "presentationSourceRevision": updated["revision"]}}
            assert bookmark == expected_bookmark, "resumed indexed bookmark changed retained provenance or source authority"
    occurrence_path = "/api/v1/observations/" + observed["id"]
    raw, raw_headers = api.request("GET", occurrence_path + "/raw")
    assert raw["identifier"] == observed["identifier"] and header(raw_headers, "Content-Digest"), "raw observation binding mismatch"
    other.request("GET", occurrence_path + "/raw", expected=(404,))
    occurrences, _ = api.request("GET", occurrence_path + "/occurrences?limit=10")
    occurrence = occurrences["items"][0]
    group, _ = api.request("GET", "/api/v1/observation-groups?" + urllib.parse.urlencode({"identifier": observed["identifier"], "versionPresent": str(observed["versionPresent"]).lower()}))
    assert group["identifier"] == observed["identifier"] and group["members"], "observation group lost identity"
    adopted, _ = mutate(api, "POST", occurrence_path + ":adopt", {
        "snapshotId": occurrence["snapshotId"], "retrievalDigest": occurrence["retrievalDigest"], "entryLocation": occurrence["entryLocation"],
        "namespaceId": seed["resource"]["namespaceId"], "publisherId": seed["listingRevision"]["publisherId"], "version": "1.0.0", "description": "Owned external draft adoption"}, 201)
    retained, _ = api.request("GET", "/api/v1/version-revisions/" + adopted["versionRevision"]["id"])
    assert retained["revision"]["adoption"]["observationId"] == observed["id"] and adopted["listingRevision"]["visibility"] == "private", "adoption lost provenance or became public"
    resource_path = "/api/v1/resources/" + adopted["resource"]["id"]
    _, resource_headers = api.request("GET", resource_path)
    api.request("POST", resource_path + ":delete", {"reason": "Must retain adopted history"}, (409,), {"Idempotency-Key": key("adoption-delete"), "If-Match": header(resource_headers, "ETag")})
    snapshots = snapshot_items(api, source["id"])
    exact, _ = api.request("GET", "/api/v1/federation/snapshots/" + snapshots[0]["id"])
    assert exact["id"] == snapshots[0]["id"], "source snapshot readback mismatch"
    preview, _ = mutate(api, "POST", path + "/change-previews", {"intent": "remove"})
    _, headers = api.request("GET", path)
    mutate(api, "POST", path + ":remove", {"changePreviewDigest": preview["digest"], "reason": "Owned source removal"}, 204, header(headers, "ETag"))
    api.request("GET", path, expected=(404,))
    reader.request("GET", indexed_path, expected=(404,))
    retained = historical_observation(api, observed["id"])
    assert retained["id"] == observed["id"] and retained["freshness"] != "current", "source removal lost historical observation"
    api.request("GET", "/api/v1/observations?" + urllib.parse.urlencode({"sourceId": source["id"], "freshness": "all"}), expected=(404,))
    for action in ("source.paused", "source.resumed", "source.removed"):
        audit_once(api, action, "federation_source", source["id"])
    return {"sourceId": source["id"], "observationId": observed["id"], "adoptedRevisionId": adopted["versionRevision"]["id"]}


def historical_observation(api, identifier):
    page, _ = api.request("GET", "/api/v1/observations?freshness=all&limit=100")
    assert not page.get("nextCursor"), "owned observation inventory exceeded fixture bound"
    matches = [item for item in page["items"] if item["id"] == identifier]
    assert len(matches) == 1, "retained observation missing or duplicated"
    return matches[0]


def verify_checkpoint(base):
    api = Client(base, "local-admin")
    state = read_document(Path(os.environ["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"]) / "core-state.json")
    for expected in state["releases"]:
        retained, _ = api.request("GET", "/api/v1/listing-releases/" + expected["id"])
        assert {name: retained[name] for name in expected} == expected, "restart lost release lifecycle"
    api.request("GET", "/api/v1/federation/sources/" + state["sourceId"], expected=(404,))
    observed = historical_observation(api, state["observationId"])
    assert observed["freshness"] == "source_removed", "restart lost source removal evidence"
    adopted, _ = api.request("GET", "/api/v1/version-revisions/" + state["adoptedRevisionId"])
    assert adopted["revision"]["adoption"]["observationId"] == state["observationId"], "restart lost adoption provenance"
    Client(base, "local-reader").request("POST", "/api/v1/resources", {
        "namespaceId": state["namespaceId"], "canonicalIdentifier": "urn:air:catalog.example.com:readiness:revoked-after-restart", "kind": "skill"},
        (403,), {"Idempotency-Key": key("revoked-after-restart")})
    Client(base).request("GET", "/.well-known/ard.json", expected=(404,))


def run(base, run_id, with_source=False):
    api, other = Client(base, "local-admin"), Client(base, "other-tenant-admin")
    public, reviewer = Client(base), Client(base, "local-reviewer")
    values = browse(api, other, run_id)
    observed_reads(api, public, values)
    membership_journey(api, run_id)
    feedback.run(api, other, base, values[0])
    if with_source:
        live_search(api)
    archive_journey(api, other, values[0], run_id)
    plugin_admission(api, other, values[0], run_id)
    publication_history(api, public)
    administration(api, other, public, base, values[0], run_id)
    review_journey(api, reviewer, values[0])
    release_journey(api, public, values)
    if with_source:
        checkpoint = source_journey(api, other, values[0])
        checkpoint["namespaceId"] = values[0]["resource"]["namespaceId"]
        checkpoint["releases"] = []
        for value in values:
            retained, _ = api.request("GET", "/api/v1/listing-releases/" + value["release"]["id"])
            checkpoint["releases"].append({name: retained[name] for name in ("id", "state", "revision")})
        write_document(Path(os.environ["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"]) / "core-state.json", checkpoint)
    print("Packaged browse, facets, archive, review, publication history and release journeys passed.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base-url", required=True)
    parser.add_argument("--run-id", required=True)
    parser.add_argument("--with-source", action="store_true")
    parser.add_argument("--verify", action="store_true")
    args = parser.parse_args()
    try:
        if args.verify:
            verify_checkpoint(args.base_url)
        else:
            run(args.base_url, args.run_id, args.with_source)
    except Exception as error:
        record_scenario_failure("core", error)
        raise
