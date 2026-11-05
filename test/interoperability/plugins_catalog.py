"""Real governance and CLI selection for an external-archive Plugin release."""

import copy

from catalog_control import CatalogControl, validate_search_record
from plugins_data import NAME, MEDIA, PROFILE, TAG, IDENTIFIER
from plugins_archive import digest


class Catalog(CatalogControl):
    request_command = ("python", "-B", "/fixture/plugins_http.py", "request")
    key_prefix = "plugins-"

    def version(self, resource, version, archive, locator):
        return self.request(
            "/api/v1/resources/" + resource + "/versions",
            {
                "version": version,
                "representations": [
                    {
                        "role": "primary",
                        "mediaType": MEDIA,
                        "url": locator,
                        "expectedDigest": digest(archive),
                    }
                ],
            },
            actor="local-contributor",
            status=201,
        )[0]

    def publish(self, archive, locator):
        namespace, _ = self.request(
            "/api/v1/namespaces",
            {"slug": "plugins", "displayName": "Owned Plugins"},
            status=201,
        )
        publisher, etag = self.request(
            "/api/v1/publishers",
            {"displayName": "Owned publisher", "domains": ["catalog.example.com"]},
            status=201,
        )
        self.request(
            "/api/v1/publishers/"
            + publisher["id"]
            + "/publication-domains/catalog.example.com:approve",
            {"reason": "Owned fixture"},
            etag=etag,
        )
        resource, _ = self.request(
            "/api/v1/resources",
            {
                "namespaceId": namespace["id"],
                "canonicalIdentifier": IDENTIFIER,
                "kind": "plugin",
            },
            actor="local-contributor",
            status=201,
        )
        version = self.version(resource["id"], "1.0.0", archive, locator)
        assert version["evidence"]["state"] == "complete"
        listing, etag = self.request(
            "/api/v1/catalog-listings",
            {"namespaceId": namespace["id"], "resourceId": resource["id"]},
            actor="local-contributor",
            status=201,
        )
        body = {
            "versionRevisionId": version["revision"]["id"],
            "displayName": "Owned Plugin",
            "description": "Independent immutable Plugin consumption",
            "tags": [TAG],
            "representativeQueries": [
                "Find owned invoice Plugin",
                "Find immutable Plugin package",
            ],
            "publisherId": publisher["id"],
            "license": "Apache-2.0",
            "visibility": "public",
        }
        revision, etag = self.request(
            "/api/v1/catalog-listings/" + listing["id"] + "/revisions",
            body,
            actor="local-contributor",
            etag=etag,
            status=201,
        )
        submission, sub_etag = self.request(
            "/api/v1/catalog-listings/" + listing["id"] + "/submissions",
            {},
            actor="local-contributor",
            status=201,
        )
        self.request(
            "/api/v1/review-submissions/" + submission["id"] + ":approve",
            {"reason": "Owned independent review"},
            actor="local-reviewer",
            etag=sub_etag,
        )
        released, _ = self.request(
            "/api/v1/catalog-listings/" + listing["id"] + ":publish",
            {"submissionId": submission["id"]},
            actor="local-publisher",
            etag=etag,
            status=201,
        )
        expected = {
            "resource": resource["id"],
            "version": version["version"]["id"],
            "versionRevision": version["revision"]["id"],
            "listing": listing["id"],
            "listingRevision": revision["revision"]["id"],
            "release": released["release"]["id"],
            "evidence": version["evidence"]["id"],
        }
        snapshot, etag = self.request(
            "/api/v1/publication/snapshots",
            {"host": "127.0.0.1"},
            actor="local-publisher",
            status=201,
        )
        active, _ = self.request(
            "/api/v1/publication/snapshots/" + snapshot["id"] + ":activate",
            {"host": "127.0.0.1", "reason": "Owned Plugin qualification"},
            actor="local-publisher",
            etag=etag,
        )
        assert active["state"] == "active" and active["id"] == snapshot["id"]
        self.snapshot = snapshot["id"]
        self.publication = self.request("/.well-known/ard.json", actor=None)
        return expected, body

    def discover(self, expected, archive, locator):
        search = self.cli("search", "--kind", "plugin", "--tag", TAG, "--limit", "2")[
            "data"
        ]
        assert (
            search["durableTotal"] == 1
            and not search["partial"]
            and len(search["items"]) == 1
        )
        item = search["items"][0]
        assert (
            item["type"] == "local"
            and item["local"]["release"]["id"] == expected["release"]
        )
        record = self.cli("record", "--id", item["local"]["release"]["id"])["data"]
        validate_search_record(item, record)
        for field, identity in expected.items():
            assert record[field]["id"] == identity, "changed published identity"
        assert record["listing"]["currentReleaseId"] == expected["release"]
        assert record["version"]["sealedRevisionId"] == expected["versionRevision"]
        assert (
            record["release"]["versionRevisionId"]
            == record["listingRevision"]["versionRevisionId"]
            == expected["versionRevision"]
        )
        assert record["release"]["listingRevisionId"] == expected["listingRevision"]
        assert (
            record["resource"]["kind"] == "plugin"
            and record["evidence"]["state"] == "complete"
        )
        representations = record["versionRevision"]["representations"]
        assert len(representations) == 1
        representation = representations[0]
        assert (
            representation["url"] == locator
            and representation["contentDigest"] == digest(archive)
            and representation["size"] == len(archive)
        )
        assert not any(
            representation.get(key) for key in ("data", "packageData", "artifactId")
        )
        assert (
            representation["profileId"] == PROFILE
            and representation["validationState"] == "valid"
        )
        assert representation["normalizedFacts"]["name"] == NAME
        manifest, etag = self.request("/.well-known/ard.json", actor=None)
        assert (manifest, etag) == self.publication
        entries = [e for e in manifest["entries"] if e.get("identifier") == IDENTIFIER]
        assert len(entries) == 1
        entry = entries[0]
        assert (
            entry["url"] == locator and entry["type"] == MEDIA and not entry.get("data")
        )
        assert entry["metadata"]["packageDigest"] == digest(archive) and entry[
            "metadata"
        ]["packageSize"] == len(archive)
        snapshot, _ = self.request(
            "/api/v1/publication/snapshots/" + self.snapshot + "?host=127.0.0.1",
            actor="local-publisher",
        )
        assert snapshot["id"] == self.snapshot and snapshot["state"] == "active"
        return record

    def draft(self, expected, body, archive, locator):
        version = self.version(expected["resource"], "1.0.1", archive, locator)
        assert (
            version["evidence"]["state"] == "complete"
            and version["revision"]["id"] != expected["versionRevision"]
        )
        _, etag = self.request(
            "/api/v1/catalog-listings/" + expected["listing"], actor="local-contributor"
        )
        body = copy.deepcopy(body)
        body.update(
            versionRevisionId=version["revision"]["id"],
            description="Unpublished changed Plugin",
        )
        later, _ = self.request(
            "/api/v1/catalog-listings/" + expected["listing"] + "/revisions",
            body,
            actor="local-contributor",
            etag=etag,
            status=201,
        )
        assert (
            later["revision"]["id"] != expected["listingRevision"]
            and later["listing"]["currentReleaseId"] == expected["release"]
        )
