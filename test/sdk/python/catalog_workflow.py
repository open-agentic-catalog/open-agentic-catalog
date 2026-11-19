"""Python's actual governed API exchange, launched by the reused Go fixture."""
import asyncio
import json
import os
from pathlib import Path
from catalog_sdk.generated.models.revision_evidence_envelope import RevisionEvidenceEnvelope
import sys
from uuid import UUID
from urllib.parse import urlencode

from catalog_sdk import Client, Config, SDKError
from catalog_sdk.generated.api.shared_collections_api import SharedCollectionsApi
from catalog_sdk.generated.models.create_collection import CreateCollection
from catalog_sdk.generated.models.add_collection_member import AddCollectionMember
from catalog_sdk.generated.models.update_collection import UpdateCollection
from catalog_sdk.generated.api.federation_api import FederationApi
from catalog_sdk.generated.models.adopt_resource_version import AdoptResourceVersion
from catalog_sdk.generated.api.identity_api import IdentityApi
from catalog_sdk.generated.api.catalog_lifecycle_api import CatalogLifecycleApi
from catalog_sdk.generated.api.catalog_discovery_api import CatalogDiscoveryApi
from catalog_sdk.generated.api.jobs_api import JobsApi
from catalog_sdk.generated.models.create_namespace import CreateNamespace
from catalog_sdk.generated.models.patch_namespace import PatchNamespace
from catalog_sdk.generated.models.create_resource import CreateResource
from catalog_sdk.generated.models.resource_kind import ResourceKind
from catalog_sdk.generated.models.create_version import CreateVersion
from catalog_sdk.generated.models.representation_input import RepresentationInput
from catalog_sdk.generated.models.reason import Reason
from catalog_sdk.generated.models.create_publisher import CreatePublisher
from catalog_sdk.generated.models.create_listing import CreateListing
from catalog_sdk.generated.models.create_listing_revision import CreateListingRevision
from catalog_sdk.generated.models.publish_listing import PublishListing
from catalog_sdk.generated.models.catalog_search import CatalogSearch
from catalog_sdk.generated.models.audit_details_v1 import AuditDetailsV1


async def qualify_version_adoption():
    """Decode real enabled-operation responses through the installed SDK."""
    fixture = json.loads(os.environ["CATALOG_SDK_VERSION_ADOPTION"])
    client = Client(Config(origin=fixture["origin"], allow_loopback_http=True, token="admin"))
    federation = FederationApi(client)
    target = await federation.get_version_adoption_target_with_http_info(UUID(fixture["body"]["observationId"]))
    assert target.status_code == 200
    assert str(target.data.resource_id) == fixture["resourceId"]
    assert str(target.data.prior_version_revision_id) == fixture["priorVersionRevisionId"]
    assert target.data.version == "opaque-first" and target.data.upstream_version == "opaque-next"
    body = AdoptResourceVersion(**fixture["body"])
    created = await federation.adopt_resource_version_with_http_info(resource_id=UUID(fixture["resourceId"]), idempotency_key="python-enabled-adoption", adopt_resource_version=body)
    assert created.status_code == 201 and str(created.data.resource_id) == fixture["resourceId"]
    assert created.data.version.version == "opaque-next" and created.data.evidence.state == "complete"
    proof = created.data.version_revision.adoption
    assert str(proof.source_id) == fixture["sourceId"]
    assert str(proof.observation_id) == fixture["body"]["observationId"]
    assert str(proof.snapshot_id) == fixture["body"]["snapshotId"]
    assert proof.retrieval_digest == fixture["body"]["retrievalDigest"] and proof.entry_location == fixture["body"]["entryLocation"]
    assert created.data.version_revision.representations[0].validation_state == "valid"
    replay = await federation.adopt_resource_version_with_http_info(resource_id=UUID(fixture["resourceId"]), idempotency_key="python-enabled-adoption", adopt_resource_version=body)
    assert replay.status_code == 201 and replay.raw_data == created.raw_data
    assert replay.headers["Idempotency-Replayed"] == "true"
    duplicate = await federation.adopt_resource_version_with_http_info(resource_id=UUID(fixture["resourceId"]), idempotency_key="python-duplicate-adoption", adopt_resource_version=body)
    assert duplicate.status_code == 409 and duplicate.data.code == "CONFLICT"


async def qualify_reviewed_corpus():
    """Read all reviewed source cases through the installed typed decoder."""
    corpus = json.loads(Path(os.environ["CATALOG_SDK_CORPUS"]).read_text())
    assert len(corpus["cases"]) == 6
    async with Client(Config(origin=corpus["origin"], allow_loopback_http=True, token="reader")) as reader, Client(Config(origin=corpus["origin"], allow_loopback_http=True, token="other")) as other:
        api = CatalogDiscoveryApi(reader)
        for expected in corpus["cases"]:
            reply = await api.get_catalog_record_with_http_info(UUID(expected["releaseId"]))
            assert reply.status_code == 200
            record = reply.data.to_dict()
            assert record["resource"]["kind"] == expected["kind"]
            assert record["resource"]["canonicalIdentifier"] == expected["identifier"]
            assert str(record["release"]["id"]) == expected["releaseId"]
            assert len(record["versionRevision"]["representations"]) == 1
            rep = record["versionRevision"]["representations"][0]
            for key, value in expected["representation"].items():
                assert rep[key] == value, (expected["name"], key)
            if "data" in expected:
                assert rep["data"] == expected["data"]
            if "description" in expected:
                assert rep["normalizedFacts"]["description"] == expected["description"]
            if "text" in expected:
                content = await reader.raw("GET", f'/api/v1/artifacts/{rep["artifactId"]}/content')
                assert content.status == 200 and content.data == expected["text"].encode("utf-8")
            if "fileCount" in expected:
                package = rep["plugin" if expected["kind"] == "plugin" else "package"]
                assert len(package["files"]) == expected["fileCount"]
            if "documents" in expected:
                docs = rep["plugin"]["documents"]
                assert len(docs) == len(expected["documents"])
                assert {d["path"]: d["content"] for d in docs} == expected["documents"]
            hidden = await CatalogDiscoveryApi(other).get_catalog_record_with_http_info(UUID(expected["releaseId"]))
            assert hidden.status_code == 404
    print("Python installed SDK: six reviewed upstream cases, exact native content and tenant denial passed")


async def qualify_private_resources(admin, namespace_id):
    """Keep private opaque identities readable through real typed API exchanges."""
    lifecycle = CatalogLifecycleApi(admin)
    cases = [
        ("agent", " URN:AIR:CATALOG.EXAMPLE.COM:python:private-control ", "urn:air:catalog.example.com:python:private-control"),
        ("mcp_server", " custom:python-sdk-private ", "custom:python-sdk-private"),
        ("skill", "Café:e\u0301:🙂", "Café:e\u0301:🙂"),
        ("plugin", "🙂" * 512, "🙂" * 512),
    ]
    retained = set()
    for index, (kind, supplied, expected) in enumerate(cases):
        body = CreateResource(namespaceId=namespace_id, kind=ResourceKind(kind), canonicalIdentifier=supplied)
        key = f"python-private-{index}"
        try:
            created = await lifecycle.create_resource_with_http_info(key, body)
        except SDKError as error:
            # Observe an uncertain create without repeating its mutation. This
            # proves whether decoding failed after the server committed.
            readback = await admin.raw("GET", "/api/v1/resources?" + urlencode({"canonicalIdentifier": expected, "kind": kind}))
            rows = json.loads(readback.data).get("items", []) if readback.status == 200 else []
            raise AssertionError(f"Private Resource create failed: {error.code}, HTTP {error.status}; readback HTTP {readback.status}, retained matches {len(rows)}") from error
        assert created.status_code == 201
        assert created.data.namespace_id == namespace_id and created.data.kind == kind
        assert created.data.canonical_identifier == expected
        retained.add(str(created.data.id))
        replay = await lifecycle.create_resource_with_http_info(key, body)
        assert replay.status_code == 201 and replay.raw_data == created.raw_data
        assert replay.headers["Idempotency-Replayed"] == "true" and replay.headers["ETag"] == created.headers["ETag"]
        exact = await lifecycle.get_resource_with_http_info(created.data.id)
        assert exact.status_code == 200 and exact.raw_data == created.raw_data
        assert exact.headers["ETag"] == created.headers["ETag"]
        page = await lifecycle.list_resources_with_http_info(canonical_identifier=expected, kind=ResourceKind(kind), limit=1)
        assert page.status_code == 200 and len(page.data.items) == 1 and not page.data.next_cursor
        assert page.data.items[0].id == created.data.id and page.data.items[0].canonical_identifier == expected
    malformed = await lifecycle.create_resource_with_http_info(
        "python-private-malformed-air", CreateResource(namespaceId=namespace_id, kind=ResourceKind("agent"), canonicalIdentifier="urn:air:bad"),
    )
    assert malformed.status_code == 400 and malformed.data.code == "INVALID_ARGUMENT"
    page = await lifecycle.list_resources_with_http_info(limit=100)
    assert page.status_code == 200 and {str(item.id) for item in page.data.items} == retained


async def qualify_validation_maintenance(origin, admin, namespace_id):
    """Read actual blocked evidence through the generated maintenance binding."""
    api = CatalogLifecycleApi(admin)
    resource = await api.create_resource_with_http_info("python-maintenance-resource", CreateResource(namespaceId=namespace_id, kind=ResourceKind("agent"), canonicalIdentifier="urn:air:catalog.example.com:python:maintenance"))
    assert resource.status_code == 201
    created = await api.create_resource_version_with_http_info(resource_id=resource.data.id, idempotency_key="python-maintenance-version", create_version=CreateVersion(version="79.0.0", representations=[RepresentationInput(role="primary", mediaType="application/a2a-agent-card+json", data={"name": "Incomplete maintenance fixture"})]))
    assert created.status_code == 201 and created.data.evidence.state == "blocked"
    page = await api.list_validation_maintenance_with_http_info(state="blocked", horizon_hours=24, limit=100)
    assert page.status_code == 200 and page.headers["Cache-Control"] == "no-store"
    row = next(item for item in page.data.items if item.revision_id == created.data.revision.id)
    assert row.evidence_id == created.data.evidence.id and row.conditions == ["blocked"] and not row.applicable and row.expires_at is None
    reader = Client(Config(origin=origin, allow_loopback_http=True, token="reader"))
    denied = await CatalogLifecycleApi(reader).list_validation_maintenance_with_http_info(state="blocked", horizon_hours=24)
    assert denied.status_code == 200 and not denied.data.items


async def qualify_published_discovery(origin, admin, namespace_id, resource, version, native):
    """Exercise nonempty generated response models through the installed package."""
    identity = IdentityApi(admin)
    lifecycle = CatalogLifecycleApi(admin)
    publisher = await identity.create_publisher_with_http_info(
        idempotency_key="python-publisher",
        create_publisher=CreatePublisher(displayName="Python Publisher", domains=["catalog.example.com"]),
    )
    assert publisher.status_code == 201, publisher.status_code
    exact_publisher = await identity.list_publishers_with_http_info(limit=1, display_name="Python Publisher", declared_domain=" CATALOG.Example.COM. ")
    assert exact_publisher.status_code == 200 and len(exact_publisher.data.items) == 1
    assert exact_publisher.data.items[0].id == publisher.data.id and not exact_publisher.data.next_cursor
    approved = await identity.approve_publisher_publication_domain_with_http_info(
        idempotency_key="python-domain", if_match=publisher.headers["ETag"],
        publisher_id=publisher.data.id, domain="catalog.example.com", reason=Reason(reason="Local SDK qualification"),
    )
    assert approved.status_code == 200, approved.status_code
    reviewer = Client(Config(origin=origin, allow_loopback_http=True, token="reviewer"))
    releases = set()
    identities = {}
    listing_extensions = {"detail":"python-detail-sentinel","large":9007199254740993,"null":None,"values":[None,False,""],"additional_properties":{"must":"stay nested"},"model_extra":"wire value"}
    cases = [
        ("urn:air:catalog.example.com:python:agent", "tenant"),
        ("custom:python-tenant:e\u0301:🙂", "tenant"),
        ("custom:python-private:e\u0301:🙂", "private"),
    ]
    for i, (identifier, visibility) in enumerate(cases):
        if i:
            second = await lifecycle.create_resource_with_http_info(
                idempotency_key=f"python-additional-agent-{i}",
                create_resource=CreateResource(namespaceId=namespace_id, kind=ResourceKind("agent"), canonicalIdentifier=identifier),
            )
            assert second.status_code == 201, second.status_code
            resource = second.data
            version = await lifecycle.create_resource_version_with_http_info(
                resource_id=resource.id, idempotency_key=f"python-additional-version-{i}",
                create_version=CreateVersion(version="1.0.0", representations=[native]),
            )
            assert version.status_code == 201 and version.data.evidence.state == "complete", version.status_code
        listing = await lifecycle.create_catalog_listing_with_http_info(
            idempotency_key=f"python-listing-{i}",
            create_listing=CreateListing(namespaceId=namespace_id, resourceId=resource.id),
        )
        assert listing.status_code == 201, listing.status_code
        revision = await lifecycle.create_listing_revision_with_http_info(
            idempotency_key=f"python-listing-revision-{i}", if_match=listing.headers["ETag"], listing_id=listing.data.id,
            create_listing_revision=CreateListingRevision(
                versionRevisionId=version.data.revision.id, displayName=f"Python Agent {i}", description="SDK published discovery",
                publisherId=publisher.data.id, visibility=visibility, tags=["python-sdk"], extensions=listing_extensions,
            ),
        )
        assert revision.status_code == 201, revision.status_code
        submitted = await lifecycle.submit_listing_for_review_with_http_info(
            idempotency_key=f"python-submit-{i}", listing_id=listing.data.id, body={},
        )
        assert submitted.status_code == 201, submitted.status_code
        reviewed = await CatalogLifecycleApi(reviewer).approve_review_submission_with_http_info(
            idempotency_key=f"python-approve-{i}", if_match=submitted.headers["ETag"],
            submission_id=submitted.data.id, reason=Reason(reason="Reviewed local SDK evidence"),
        )
        assert reviewed.status_code == 200, reviewed.status_code
        current = await lifecycle.get_catalog_listing_with_http_info(listing.data.id)
        assert current.status_code == 200, current.status_code
        published = await lifecycle.publish_catalog_listing_with_http_info(
            idempotency_key=f"python-publish-{i}", if_match=current.headers["ETag"], listing_id=listing.data.id,
            publish_listing=PublishListing(submissionId=submitted.data.id),
        )
        assert published.status_code == 201, published.status_code
        if i == 0:
            collections = SharedCollectionsApi(admin)
            created = await collections.create_collection_with_http_info("python-collection", CreateCollection(namespaceId=namespace_id, name="Python SDK collection", description="Original note"))
            assert created.status_code == 201 and created.data.revision == 1
            added = await collections.add_collection_member_with_http_info(created.data.id, "python-member", created.headers["ETag"], AddCollectionMember(listingId=listing.data.id))
            assert added.status_code == 201 and added.data.revision == 2
            replayed = await collections.add_collection_member_with_http_info(created.data.id, "python-member", created.headers["ETag"], AddCollectionMember(listingId=listing.data.id))
            assert replayed.headers["Idempotency-Replayed"] == "true" and replayed.data == added.data
            async with Client(Config(origin=origin, allow_loopback_http=True, token="reader")) as collection_reader:
                reads = SharedCollectionsApi(collection_reader)
                directory = await reads.list_collections_with_http_info(namespace_id=namespace_id, limit=100)
                assert directory.status_code == 200 and [item.id for item in directory.data.items] == [created.data.id] and not directory.data.next_cursor
                metadata = await reads.get_collection_with_http_info(created.data.id)
                assert metadata.status_code == 200 and metadata.data.name == "Python SDK collection" and metadata.data.description == "Original note" and metadata.data.revision == 2
                page = await reads.list_collection_members_with_http_info(created.data.id, limit=1)
                assert page.status_code == 200 and len(page.data.items) == 1 and not page.data.next_cursor
                assert page.data.items[0].listing.id == listing.data.id and page.data.items[0].release.id == published.data.release.id
                assert page.headers["Cache-Control"] == "no-store" and "ETag" not in page.headers
                selected = await reads.list_collection_members_with_http_info(created.data.id, text="SDK", kinds=["agent", "skill"])
                assert selected.status_code == 200 and [item.listing.id for item in selected.data.items] == [listing.data.id]
                excluded = await reads.list_collection_members_with_http_info(created.data.id, text="collection-filter-no-match", kinds=["agent", "skill"])
                assert excluded.status_code == 200 and not excluded.data.items and not excluded.data.next_cursor
                wrong_kind = await reads.list_collection_members_with_http_info(created.data.id, kinds=["mcp_server", "plugin"])
                assert wrong_kind.status_code == 200 and not wrong_kind.data.items
                updated = await collections.update_collection_with_http_info(created.data.id, "python-update-collection", metadata.headers["ETag"], UpdateCollection(name="Updated Python collection", description=""))
                assert updated.status_code == 200 and updated.data.revision == 3
                stale = await collections.update_collection_with_http_info(created.data.id, "python-stale-collection", metadata.headers["ETag"], UpdateCollection(name="Stale"))
                assert stale.status_code == 412 and stale.data.code == "PRECONDITION_FAILED"
                denied = await reads.delete_collection_with_http_info(created.data.id, "python-reader-delete-collection", updated.headers["ETag"])
                assert denied.status_code == 403 and denied.data.code == "FORBIDDEN"
                readback = await reads.get_collection_with_http_info(created.data.id)
                assert readback.status_code == 200 and readback.data.name == "Updated Python collection" and readback.data.description == "" and readback.data.revision == 3
                removed = await collections.remove_collection_member_with_http_info(created.data.id, listing.data.id, "python-remove-member", updated.headers["ETag"])
                assert removed.status_code == 200 and removed.data.revision == 4
                empty = await reads.list_collection_members_with_http_info(created.data.id)
                assert empty.status_code == 200 and not empty.data.items and not empty.data.next_cursor
                deleted = await collections.delete_collection_with_http_info(created.data.id, "python-delete-collection", removed.headers["ETag"])
                assert deleted.status_code == 200 and deleted.data.revision == 5
                absent = await reads.get_collection_with_http_info(created.data.id)
                assert absent.status_code == 404 and absent.data.code == "NOT_FOUND"
                preserved = await CatalogDiscoveryApi(collection_reader).get_catalog_record_with_http_info(published.data.release.id)
                assert preserved.status_code == 200 and preserved.data.release.id == published.data.release.id
        releases.add(str(published.data.release.id))
        identities[str(published.data.release.id)] = (identifier, visibility)
    reader = Client(Config(origin=origin, allow_loopback_http=True, token="reader"))
    discovery = CatalogDiscoveryApi(reader)
    for native, count in (({"agentDefaultInputModes": ["application/json"]}, len(releases)), ({"agentStreaming": False}, 0), ({"agentSkillIds": ["not-declared-sdk-sentinel"]}, 0)):
        selected = await discovery.search_catalog_with_http_info(catalog_search=CatalogSearch.from_dict({"origins": ["local"], "nativeCapabilities": native, "tags": ["python-sdk"]}))
        assert selected.status_code == 200 and selected.data.durable_total == count
    unmatched = await discovery.search_catalog_with_http_info(catalog_search=CatalogSearch(agentProtocol="HTTP+JSON", origins=["local"], tags=["python-sdk"]))
    assert unmatched.status_code == 200 and unmatched.data.durable_total == 0 and unmatched.data.items == []
    cursor = None
    seen = set()
    for i in range(len(cases)):
        page = await discovery.search_catalog_with_http_info(catalog_search=CatalogSearch(agentProtocol="JSONRPC", limit=1, tags=["python-sdk"], origins=["local"], cursor=cursor))
        assert page.status_code == 200 and len(page.data.items) == 1 and not page.data.partial
        item = page.data.items[0].to_dict()
        assert item["type"] == "local" and item["kind"] == "agent" and item["tags"] == ["python-sdk"]
        release_id = item["local"]["release"]["id"]
        assert release_id in releases and release_id not in seen
        seen.add(release_id)
        identifier, visibility = identities[release_id]
        assert item["identifier"] == item["local"]["resource"]["canonicalIdentifier"] == identifier
        assert item["local"]["listingRevision"]["visibility"] == visibility
        record = await discovery.get_catalog_record_with_http_info(UUID(release_id))
        assert record.status_code == 200 and str(record.data.release.id) == release_id
        assert record.data.resource.canonical_identifier == identifier and record.data.listing_revision.visibility == visibility
        assert record.data.listing_revision.extensions.to_dict() == listing_extensions
        assert b"9007199254740993" in record.raw_data and b'[null,false,""]' in record.raw_data
        cursor = page.data.next_cursor
        assert bool(cursor) == (i < len(cases) - 1), "Expected exactly three local release Search pages"
    assert seen == releases
    cursor = None
    first_cursor = None
    seen = set()
    for i in range(len(cases)):
        browse = await discovery.browse_catalog_with_http_info(namespace_id=namespace_id, kind=ResourceKind("agent"), limit=1, cursor=cursor)
        assert browse.status_code == 200 and len(browse.data.items) == 1 and browse.data.total == len(cases)
        item = browse.data.items[0]
        release_id = str(item.release.id)
        assert release_id in releases and release_id not in seen
        seen.add(release_id)
        identifier, visibility = identities[release_id]
        assert item.resource.canonical_identifier == identifier and item.listing_revision.visibility == visibility
        cursor = browse.data.next_cursor
        assert bool(cursor) == (i < len(cases) - 1)
        if i == 0:
            first_cursor = cursor
    assert seen == releases
    wrong_filter = await discovery.browse_catalog_with_http_info(namespace_id=namespace_id, kind=ResourceKind("skill"), limit=1, cursor=first_cursor)
    assert wrong_filter.status_code == 400 and wrong_filter.data.code == "INVALID_ARGUMENT"
    foreign = CatalogDiscoveryApi(Client(Config(origin=origin, allow_loopback_http=True, token="other")))
    hidden = await foreign.get_catalog_record_with_http_info(UUID(next(iter(releases))))
    assert hidden.status_code == 404 and hidden.data.code == "NOT_FOUND"
    foreign_cursor = await foreign.browse_catalog_with_http_info(namespace_id=namespace_id, kind=ResourceKind("agent"), limit=1, cursor=first_cursor)
    assert foreign_cursor.status_code == 400 and foreign_cursor.data.code == "INVALID_ARGUMENT"
    audit_api = CatalogDiscoveryApi(admin)
    audit_page = await audit_api.list_audit_events_with_http_info(action="resource.created", limit=1)
    assert audit_page.status_code == 200 and len(audit_page.data.items) == 1 and audit_page.data.next_cursor
    event = audit_page.data.items[0]
    assert event.action == "resource.created" and event.details["principalType"] == "catalog_membership"
    assert event.details["actorType"] == "human"
    assert AuditDetailsV1.from_dict(event.details).to_dict() == event.details
    audit_next = await audit_api.list_audit_events_with_http_info(action="resource.created", limit=1, cursor=audit_page.data.next_cursor)
    assert audit_next.status_code == 200 and len(audit_next.data.items) == 1
    assert audit_next.data.items[0].id != event.id


async def main(origin):
    await qualify_version_adoption()
    admin = Client(Config(origin=origin, allow_loopback_http=True, token="admin"))
    federation = FederationApi(admin)
    selected = UUID("00000000-0000-7000-8000-000000000001")
    refused = await federation.get_version_adoption_target_with_http_info(selected)
    assert refused.status_code == 403 and refused.data.code == "FORBIDDEN"
    refused = await federation.adopt_resource_version_with_http_info(idempotency_key="python-disabled-adoption", resource_id=selected, adopt_resource_version=AdoptResourceVersion(observationId=selected, snapshotId=selected, priorVersionRevisionId=selected, retrievalDigest="sha256:" + "a" * 64, entryLocation="/entries/0"))
    assert refused.status_code == 403 and refused.data.code == "FORBIDDEN"
    identity = IdentityApi(admin)
    created = []
    for i in range(2):
        result = await identity.create_namespace_with_http_info(f"python-ns-{i}", CreateNamespace(slug=f"python{i}", displayName=f"Python {i}"))
        assert result.status_code == 201, result.status_code
        assert result.headers["ETag"] and result.headers["X-Request-ID"]
        created.append(result)
    ns = created[0]
    first = await identity.update_namespace_with_http_info(namespace_id=ns.data.id, idempotency_key="python-update", if_match=ns.headers["ETag"], patch_namespace=PatchNamespace(displayName="Updated", reason="SDK qualification"))
    replay = await identity.update_namespace_with_http_info(namespace_id=ns.data.id, idempotency_key="python-update", if_match=ns.headers["ETag"], patch_namespace=PatchNamespace(displayName="Updated", reason="SDK qualification"))
    assert first.status_code == replay.status_code == 200, (first.status_code, replay.status_code)
    assert replay.raw_data == first.raw_data and replay.headers["Idempotency-Replayed"] == "true"
    assert replay.headers["ETag"] == first.headers["ETag"]
    stale = await identity.update_namespace_with_http_info(namespace_id=ns.data.id, idempotency_key="python-stale", if_match=ns.headers["ETag"], patch_namespace=PatchNamespace(displayName="Stale", reason="SDK qualification"))
    assert stale.status_code == 412 and stale.data.code == "PRECONDITION_FAILED"
    key_reuse = await identity.update_namespace_with_http_info(namespace_id=ns.data.id, idempotency_key="python-update", if_match=ns.headers["ETag"], patch_namespace=PatchNamespace(displayName="Changed", reason="SDK qualification"))
    assert key_reuse.status_code == 409 and key_reuse.data.code == "IDEMPOTENCY_KEY_REUSED"
    exact = await identity.list_namespaces_with_http_info(limit=1, slug=" PYTHON0 ", display_name="Updated")
    assert exact.status_code == 200 and len(exact.data.items) == 1 and exact.data.items[0].id == ns.data.id
    assert not exact.data.next_cursor
    absent = await identity.list_namespaces_with_http_info(slug="python0", display_name="updated")
    assert absent.status_code == 200 and not absent.data.items
    # Fetch exactly one page; preserve and submit its opaque cursor explicitly.
    page = await identity.list_namespaces_with_http_info(limit=1)
    assert len(page.data.items) == 1 and page.data.next_cursor
    page2 = await identity.list_namespaces_with_http_info(limit=1, cursor=page.data.next_cursor)
    assert page.data.items[0].id != page2.data.items[0].id and not page2.data.next_cursor
    lifecycle = CatalogLifecycleApi(admin)
    await qualify_private_resources(admin, ns.data.id)
    resources = []
    for kind in ("agent", "mcp_server", "skill", "plugin"):
        result = await lifecycle.create_resource_with_http_info("python-" + kind, CreateResource(namespaceId=ns.data.id, kind=ResourceKind(kind), canonicalIdentifier="urn:air:catalog.example.com:python:" + kind))
        assert result.status_code == 201, (kind, result.status_code)
        resources.append(result.data)
    card = {"name":"Python Agent", "description":"Exact evidence", "version":"1.0.0", "supportedInterfaces":[{"url":"https://agents.example.com/python", "protocolBinding":"JSONRPC", "protocolVersion":"1.0"}], "capabilities":{"extensions":[{"uri":"https://example.com/python", "params":{"large":9007199254740993,"nested":[None,False,""]}}]}, "defaultInputModes":["application/json"], "defaultOutputModes":["application/json"], "skills":[{"id":"python", "name":"Python", "description":"Evidence", "tags":["sdk"]}]}
    native = RepresentationInput.model_validate({"role":"primary", "mediaType":"application/a2a-agent-card+json", "data":card})
    version = await lifecycle.create_resource_version_with_http_info(resource_id=resources[0].id, idempotency_key="python-version", create_version=CreateVersion(version="1.0.0", representations=[native]))
    assert version.status_code == 201 and version.data.evidence.state == "complete", version.status_code
    exact = await admin.raw("GET", "/api/v1/version-revisions/" + str(version.data.revision.id))
    assert exact.status == 200 and b"9007199254740993" in exact.data and b'[null,false,""]' in exact.data
    assert RevisionEvidenceEnvelope.from_json(exact.data.decode("utf-8")).namespace_id == ns.data.id
    reader = Client(Config(origin=origin, allow_loopback_http=True, token="reader"))
    denied = await IdentityApi(reader).create_namespace_with_http_info("python-denied", CreateNamespace(slug="denied",displayName="Denied"))
    assert denied.status_code == 403 and denied.data.code == "FORBIDDEN"
    denied_replay = await IdentityApi(reader).update_namespace_with_http_info(
        namespace_id=ns.data.id, idempotency_key="python-update", if_match=ns.headers["ETag"],
        patch_namespace=PatchNamespace(displayName="Updated", reason="SDK qualification"),
    )
    assert denied_replay.status_code == 403 and denied_replay.data.code == "FORBIDDEN"
    assert "Idempotency-Replayed" not in denied_replay.headers
    readback = await identity.get_namespace_with_http_info(ns.data.id)
    assert readback.status_code == 200 and readback.raw_data == first.raw_data
    assert readback.headers["ETag"] == first.headers["ETag"]
    namespaces_after_denial = await identity.list_namespaces_with_http_info()
    assert namespaces_after_denial.status_code == 200 and not namespaces_after_denial.data.next_cursor
    assert {x.id for x in namespaces_after_denial.data.items} == {x.data.id for x in created}
    other = Client(Config(origin=origin, allow_loopback_http=True, token="other"))
    hidden = await IdentityApi(other).get_namespace_with_http_info(ns.data.id)
    assert hidden.status_code == 404 and hidden.data.code == "NOT_FOUND"
    # Reuse the existing fixture's retained failed Job, then inspect its private child.
    failed_id = UUID("00000000-0000-7000-8000-000000000090")
    jobs = JobsApi(admin)
    job = await jobs.retry_job_with_http_info(idempotency_key="python-job-retry", job_id=failed_id, reason=Reason(reason="SDK qualification"))
    assert job.status_code == 202 and job.headers["Location"] == "/api/v1/jobs/" + str(job.data.id)
    queued = await jobs.get_job_with_http_info(job.data.id)
    assert queued.status_code == 200 and queued.data.state == "queued"
    private = await JobsApi(other).get_job_with_http_info(job.data.id)
    assert private.status_code == 404
    anonymous = Client(Config(origin=origin, allow_loopback_http=True))
    refused = await IdentityApi(anonymous).list_namespaces_with_http_info()
    assert refused.status_code == 401
    await qualify_published_discovery(origin, admin, ns.data.id, resources[0], version, native)
    await qualify_reviewed_corpus()
    await qualify_validation_maintenance(origin, admin, ns.data.id)
    print("Python Catalog workflow passed: four kinds, native evidence, governed publication, Search/Browse/audit pages, replay/conflict, current-permission replay refusal, denied-write readback and private Jobs")


if __name__ == "__main__":
    asyncio.run(main(sys.argv[1]))
