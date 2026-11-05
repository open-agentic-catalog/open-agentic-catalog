"""Normal Catalog governance shared by the owned proxy and handoff datasets."""


async def publish(c, org, *, kind, identifier, version, representations, name, tags, publisher, description):
    import json
    from pathlib import Path

    identities = json.loads(Path("/state/catalog-identities.json").read_text())
    author = c.SECRETS["tokens"][org + "-contributor"]
    reviewer = c.SECRETS["tokens"][org + "-reviewer"]
    publisher_token = c.SECRETS["tokens"][org + "-publisher"]
    resource = await c.request("/api/v1/resources", {"namespaceId": identities[org]["namespace"], "canonicalIdentifier": identifier, "kind": kind}, author)
    created = await c.request("/api/v1/resources/" + resource["id"] + "/versions", {"version": version, "representations": representations}, author)
    validated = await c.request("/api/v1/version-revisions/" + created["revision"]["id"] + ":validate", {}, author)
    c.require(validated["evidence"]["state"] == "complete", "valid governed fixture evidence")
    listing, etag = await c.request("/api/v1/catalog-listings", {"namespaceId": identities[org]["namespace"], "resourceId": resource["id"]}, author, return_headers=True)
    body = {"versionRevisionId": created["revision"]["id"], "displayName": name, "description": description, "tags": tags,
            "representativeQueries": ["Find ContextForge metadata", "Find inert proxy examples"], "publisherId": publisher, "license": "Apache-2.0", "visibility": "public"}
    revision, listing_etag = await c.request("/api/v1/catalog-listings/" + listing["id"] + "/revisions", body, author, etag=etag, return_headers=True)
    submission, review_etag = await c.request("/api/v1/catalog-listings/" + listing["id"] + "/submissions", {}, author, return_headers=True)
    await c.request("/api/v1/review-submissions/" + submission["id"] + ":approve", {"reason": "Independent fixture review"}, reviewer, etag=review_etag, expected=200)
    release = await c.request("/api/v1/catalog-listings/" + listing["id"] + ":publish", {"submissionId": submission["id"]}, publisher_token, etag=listing_etag)
    return {"resource": resource["id"], "version": created["version"]["id"], "release": release["release"]["id"], "listing": listing["id"],
            "versionRevision": created["revision"]["id"], "listingRevision": revision["revision"]["id"], "publisher": publisher, "namespace": identities[org]["namespace"],
            "contentSetDigest": created["revision"]["contentSetDigest"], "listingBody": body}
