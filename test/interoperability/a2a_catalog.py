"""Bounded Catalog API setup and real CLI readback inside owned A2A isolation."""

import copy

from catalog_control import CatalogControl
import a2a_handoff as handoff


class Catalog(CatalogControl):
    """Govern the A2A deployment using shared Catalog control operations."""

    request_command = ("python", "-B", "/fixture/a2a_fixture.py", "catalog-request")
    key_prefix = "a2a-"

    def __init__(self, directory, container, card):
        super().__init__(directory, container)
        self.card = copy.deepcopy(card)

    def discover(self, expected):
        search = self.cli("search", "--kind", "agent", "--tag", handoff.TAG, "--limit", "2")
        return handoff.select_record(search, lambda selected: self.cli("record", "--id", selected), expected, self.card)

    def publish(self):
        namespace, _ = self.request("/api/v1/namespaces", dict(slug="a2a", displayName="A2A handoff"), status=201)
        publisher, etag = self.request("/api/v1/publishers", dict(displayName="Owned fixture", domains=["catalog.example.com"]), status=201)
        self.request("/api/v1/publishers/" + publisher["id"] + "/publication-domains/catalog.example.com:approve",
                     {"reason": "Qualify local A2A handoff"}, etag=etag)
        resource, _ = self.request("/api/v1/resources", dict(namespaceId=namespace["id"], canonicalIdentifier=handoff.IDENTIFIER, kind="agent"), actor="local-contributor", status=201)
        version, _ = self.request("/api/v1/resources/" + resource["id"] + "/versions",
                                  dict(version="1.0.0", representations=handoff.representations(self.card)), actor="local-contributor", status=201)
        validated, _ = self.request("/api/v1/version-revisions/" + version["revision"]["id"] + ":validate", {}, actor="local-contributor", status=201)
        assert validated["evidence"]["state"] == "complete", validated["evidence"]["findings"]
        assert validated["revision"]["contentSetDigest"] == version["revision"]["contentSetDigest"]
        listing, etag = self.request("/api/v1/catalog-listings", dict(namespaceId=namespace["id"], resourceId=resource["id"]), actor="local-contributor", status=201)
        listing_body = dict(versionRevisionId=version["revision"]["id"], displayName="Owned Hello World Agent", description="Explicit local A2A handoff qualification.",
                            tags=[handoff.TAG], representativeQueries=["Find owned Hello World Agent", "Find local echo qualification"],
                            publisherId=publisher["id"], license="Apache-2.0", visibility="public")
        revision, listing_etag = self.request("/api/v1/catalog-listings/" + listing["id"] + "/revisions", listing_body, actor="local-contributor", etag=etag, status=201)
        submission, etag = self.request("/api/v1/catalog-listings/" + listing["id"] + "/submissions", {}, actor="local-contributor", status=201)
        self.request("/api/v1/review-submissions/" + submission["id"] + ":approve", {"reason": "Independent local fixture review"}, actor="local-reviewer", etag=etag)
        release, _ = self.request("/api/v1/catalog-listings/" + listing["id"] + ":publish", {"submissionId": submission["id"]}, actor="local-publisher", etag=listing_etag, status=201)
        snapshot, etag = self.request("/api/v1/publication/snapshots", {"host": "127.0.0.1"}, actor="local-publisher", status=201)
        active, _ = self.request("/api/v1/publication/snapshots/" + snapshot["id"] + ":activate", {"host": "127.0.0.1", "reason": "Local A2A publication"}, actor="local-publisher", etag=etag)
        assert active["id"] == snapshot["id"] and active["state"] == "active"
        expected = dict(resource=resource["id"], version=version["version"]["id"], versionRevision=version["revision"]["id"],
                        listing=listing["id"], listingRevision=revision["revision"]["id"], release=release["release"]["id"],
                        snapshot=snapshot["id"], contentSetDigest=version["revision"]["contentSetDigest"])
        return expected, listing_body

    def draft(self, expected, listing_body):
        card = copy.deepcopy(self.card)
        card.update(version="1.0.1", description="Later unpublished deployment; never invoked.")
        card["supportedInterfaces"][0]["url"] = "https://127.0.0.1:9443/draft-agent"
        version, _ = self.request("/api/v1/resources/" + expected["resource"] + "/versions",
                                  dict(version="1.0.1", representations=handoff.representations(card)), actor="local-contributor", status=201)
        assert version["revision"]["id"] != expected["versionRevision"] and version["version"]["id"] != expected["version"]
        assert version["revision"]["contentSetDigest"] != expected["contentSetDigest"]
        assert version["evidence"]["state"] == "complete" and version["version"]["version"] == "1.0.1"
        assert not version["version"].get("sealedRevisionId")
        assert {r["mediaType"]: r["data"] for r in version["revision"]["representations"]} == {handoff.CARD: card}
        _, etag = self.request("/api/v1/catalog-listings/" + expected["listing"], actor="local-contributor")
        body = copy.deepcopy(listing_body)
        body.update(versionRevisionId=version["revision"]["id"], description="Unpublished later listing draft.")
        later, _ = self.request("/api/v1/catalog-listings/" + expected["listing"] + "/revisions", body, actor="local-contributor", etag=etag, status=201)
        assert later["revision"]["id"] != expected["listingRevision"]
        retained, _ = self.request("/api/v1/catalog-listings/" + expected["listing"], actor="local-contributor")
        assert retained["candidateRevisionId"] == later["revision"]["id"] and retained["currentReleaseId"] == expected["release"]
        return version["revision"]["id"], later["revision"]["id"]

    def qualify(self):
        expected, listing_body = self.publish()
        capabilities = self.cli("capabilities")["data"]
        assert "agent" in capabilities["resourceKinds"] and "catalog.search" in capabilities["operations"]
        record = self.discover(expected)
        manifest, manifest_etag = self.request("/.well-known/ard.json", actor=None)
        self.publication = (manifest, manifest_etag)
        handoff.projection(manifest, record, self.card)
        draft_id, draft_listing = self.draft(expected, listing_body)
        selected = self.discover(expected)
        assert selected["listing"]["candidateRevisionId"] == draft_listing
        # The mutable listing/version projections may advance; the selected immutable evidence must not.
        for key in ("versionRevision", "listingRevision", "release", "evidence"):
            assert selected[key] == record[key], "draft changed immutable release evidence"
        after, after_etag = self.request("/.well-known/ard.json", actor=None)
        assert after == manifest and after_etag == manifest_etag
        snapshot, _ = self.request("/api/v1/publication/snapshots/" + expected["snapshot"] + "?host=127.0.0.1", actor="local-publisher")
        assert snapshot["state"] == "active"
        handoff.projection(after, selected, self.card)
        return selected, dict(expected, draftRevision=draft_id, draftListingRevision=draft_listing, cardDigest=handoff.digest(self.card), evidence=selected["evidence"]["id"],
                              profiles={r["mediaType"]: r["profileId"] for r in selected["versionRevision"]["representations"]}), dict.fromkeys(handoff.GOVERNED_CASES, True)

    def verify_retained(self, expected, selected):
        """Runtime failures must leave the selected Catalog evidence readable and unchanged."""
        self.request("/livez", actor=None)
        self.request("/readyz", actor=None)
        record = self.cli("record", "--id", expected["release"])["data"]
        handoff.validate_record(record, expected, self.card)
        assert record == selected, "runtime failure changed the Catalog record"
        assert self.request("/.well-known/ard.json", actor=None) == self.publication
        snapshot, _ = self.request("/api/v1/publication/snapshots/" + expected["snapshot"] + "?host=127.0.0.1", actor="local-publisher")
        assert snapshot["id"] == expected["snapshot"] and snapshot["state"] == "active"
