"""Real governance and CLI selection for a package-only Skill release."""

import base64
import copy

from catalog_control import CatalogControl, validate_search_record
from skills_git import NAME, digest

TAG = "independent-skill-handoff"


class Catalog(CatalogControl):
    request_command = ("python", "-B", "/fixture/skills_fixture.py")
    key_prefix = "skills-"

    def version(self, resource, version, archive, locator):
        return self.request("/api/v1/resources/" + resource + "/versions",
                            {"version": version, "representations": [{"role": "primary", "mediaType": "application/agent-skills+zip",
                             "packageData": base64.b64encode(archive).decode(), "handoff": {"git": locator}}]},
                            actor="local-contributor", status=201)[0]

    def publish(self, archive, locator):
        namespace, _ = self.request("/api/v1/namespaces", {"slug": "skills", "displayName": "Owned Skills"}, status=201)
        publisher, etag = self.request("/api/v1/publishers", {"displayName": "Owned publisher", "domains": ["catalog.example.com"]}, status=201)
        self.request("/api/v1/publishers/" + publisher["id"] + "/publication-domains/catalog.example.com:approve", {"reason": "Owned fixture"}, etag=etag)
        resource, _ = self.request("/api/v1/resources", {"namespaceId": namespace["id"], "canonicalIdentifier": "urn:air:catalog.example.com:skills:" + NAME,
                                  "kind": "skill"}, actor="local-contributor", status=201)
        version = self.version(resource["id"], "1.0.0", archive, locator)
        assert version["evidence"]["state"] == "complete"
        listing, etag = self.request("/api/v1/catalog-listings", {"namespaceId": namespace["id"], "resourceId": resource["id"]}, actor="local-contributor", status=201)
        body = {"versionRevisionId": version["revision"]["id"], "displayName": "Owned Skill", "description": "Independent immutable Skill consumption",
                "tags": [TAG], "representativeQueries": ["Find owned invoice Skill", "Find immutable Skill package"],
                "publisherId": publisher["id"], "license": "Apache-2.0", "visibility": "public"}
        revision, etag = self.request("/api/v1/catalog-listings/" + listing["id"] + "/revisions", body, actor="local-contributor", etag=etag, status=201)
        submission, sub_etag = self.request("/api/v1/catalog-listings/" + listing["id"] + "/submissions", {}, actor="local-contributor", status=201)
        self.request("/api/v1/review-submissions/" + submission["id"] + ":approve", {"reason": "Owned independent review"}, actor="local-reviewer", etag=sub_etag)
        released, _ = self.request("/api/v1/catalog-listings/" + listing["id"] + ":publish", {"submissionId": submission["id"]}, actor="local-publisher", etag=etag, status=201)
        expected = {"resource": resource["id"], "version": version["version"]["id"], "versionRevision": version["revision"]["id"], "listing": listing["id"],
                    "listingRevision": revision["revision"]["id"], "release": released["release"]["id"], "evidence": version["evidence"]["id"]}
        return expected, body

    def discover(self, expected, archive, locator):
        search = self.cli("search", "--kind", "skill", "--tag", TAG, "--limit", "2")["data"]
        assert search["durableTotal"] == 1 and not search["partial"] and len(search["items"]) == 1
        item = search["items"][0]
        assert item["type"] == "local" and item["local"]["release"]["id"] == expected["release"]
        record = self.cli("record", "--id", item["local"]["release"]["id"])["data"]
        validate_search_record(item, record)
        for field, identity in expected.items():
            assert record[field]["id"] == identity, "changed published identity"
        assert record["listing"]["currentReleaseId"] == expected["release"]
        assert record["version"]["sealedRevisionId"] == expected["versionRevision"]
        assert record["release"]["versionRevisionId"] == record["listingRevision"]["versionRevisionId"] == expected["versionRevision"]
        assert record["release"]["listingRevisionId"] == expected["listingRevision"]
        assert record["resource"]["kind"] == "skill" and record["evidence"]["state"] == "complete"
        representations = record["versionRevision"]["representations"]
        assert len(representations) == 1
        representation = representations[0]
        assert representation["handoff"] == {"git": locator} and representation["contentDigest"] == digest(archive)
        assert not any(representation.get(key) for key in ("data", "packageData", "url", "artifactId"))
        assert representation["profileId"] == "agent-skills-69ef37e" and representation["validationState"] == "valid"
        assert representation["normalizedFacts"]["name"] == NAME
        return record

    def draft(self, expected, body, archive, locator):
        version = self.version(expected["resource"], "1.0.1", archive, locator)
        assert version["evidence"]["state"] == "complete" and version["revision"]["id"] != expected["versionRevision"]
        _, etag = self.request("/api/v1/catalog-listings/" + expected["listing"], actor="local-contributor")
        body = copy.deepcopy(body)
        body.update(versionRevisionId=version["revision"]["id"], description="Unpublished changed Skill")
        later, _ = self.request("/api/v1/catalog-listings/" + expected["listing"] + "/revisions", body, actor="local-contributor", etag=etag, status=201)
        assert later["revision"]["id"] != expected["listingRevision"] and later["listing"]["currentReleaseId"] == expected["release"]


def same_retained(before, after):
    """Later candidate pointers are mutable; published revisions/evidence are not."""
    for key in ("release", "versionRevision", "listingRevision", "evidence"):
        assert before[key] == after[key], "changed retained " + key
    for key in ("resource", "version", "listing"):
        assert before[key]["id"] == after[key]["id"]
    assert before["listing"]["currentReleaseId"] == after["listing"]["currentReleaseId"]
