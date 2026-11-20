---
title: "Concepts and lifecycle"
description: "How resource content, listing metadata, review, publication and federation fit together."
---

The Catalog governs metadata about Agents, MCP Servers, Skills and Plugins. It does not run them. Finding a record does not grant permission to install, deploy or invoke the resource.

Use the [glossary](../reference/glossary.md) to look up object names, access-control terms and standards acronyms while following this workflow.

To browse a service your administrator already runs, [connect as a reader](reader-quickstart.mdx). [Understand a result and its evidence](understand-results.mdx) explains exact references, local approval and external provenance.

## The objects you work with

| Object | What it means |
| --- | --- |
| Resource | The stable identity of an Agent, MCP Server, Skill or Plugin inside a tenant and namespace. |
| ResourceVersion | A named version of that resource, with pointers to its candidate and sealed content revision. A version label is not an HTTP concurrency token. |
| VersionRevision | An immutable set of resource representations, each identifying a format/profile and content digest. Validation runs are recorded separately, so revalidation does not rewrite the content. |
| CatalogListing and ListingRevision | The stable listing and immutable revisions of its discoverable metadata, such as title, description, publisher and visibility. Listing metadata is separate from resource content. |
| ReviewSubmission | The exact content revision, listing revision, evidence and policy submitted for a review decision. Approval is about that submitted combination, not all future edits. |
| ListingRelease | The governed release that binds approved revisions. Release disposition can change while those revisions remain immutable. |
| Publication snapshot | An immutable host/profile-specific discovery document generated from eligible releases. Host-bound discovery content comes from the active, audience-scoped snapshot; generating a candidate is not activation. |
| Federation source and observation | An administrator-approved external source and evidence of an exact external occurrence. Observing external metadata does not adopt it as a locally governed resource. |
| Job | Durable asynchronous work with a state to inspect, such as source synchronization or publication-candidate generation. Request acceptance is not proof that the work succeeded. |

Resource and revision IDs identify records. Digests identify exact content. HTTP ETags protect updates against concurrent changes. Use the value required by the [API contract](../reference/api-conventions.md); these values are not interchangeable.

A resource identity does not identify the running client or grant permission to use the resource. See the [identity map and client handoff](integrating.mdx#keep-identities-and-credentials-separate).

## From authoring to discovery

Each step changes or creates a specific Catalog object:

1. An authorized contributor creates a resource, version, representations and listing metadata in a permitted namespace.
2. Validate the exact candidate content. Version creation and explicit revalidation return evidence synchronously. Inspect the evidence state and findings before proceeding. Poll a Job only when the operation returns one, such as source synchronization or semantic reindexing. The publication-candidate endpoint returns a snapshot synchronously.
3. Submit the exact content and listing revisions for review. The review decision applies to the retained evidence and policy, with the service's role and separation-of-duties rules enforced.
4. An authorized publisher releases the approved combination. This establishes a governed Catalog release; it does not itself activate a public discovery document.
5. Generate a publication candidate for an eligible configured host and profile. Inspect the result, then explicitly activate it. Host-bound discovery reads the active snapshot, never drafts or arbitrary private records. ARD Search can additionally return approved live results or referrals under the host's audience policy.

Follow [Review, publish and maintain a resource](publishing.mdx) for step-by-step instructions for authoring, review, publication, discovery, corrections and withdrawal. Use [integration guidance](integrating.mdx) for authentication, idempotency, concurrency, pagination and asynchronous results.

Authenticated Catalog browse and search can read eligible published or deprecated local releases without a discovery snapshot. The UI uses these reads. Snapshot activation separately exposes releases through a configured ARD or AI Catalog host.

Federated Catalog search can include approved indexed and live observations. These external results do not become locally reviewed releases.

Updates create new immutable revisions rather than rewriting sealed or published evidence. Release withdrawal, revocation and publication rollback have eligibility and safety checks; they are not arbitrary history edits. See [API surfaces](../reference/api-conventions.md#surfaces), [discovery publication](../reference/api-conventions.md#discovery-publication) and the [architecture overview](../architecture/overview.mdx).

Listing relationships reference existing Resources and Versions in the same tenant. See [relationship types and examples](resource-authoring.mdx#describe-listing-relationships). They do not grant access, install dependencies or change release state.

## Organize and reuse resources

Choose a workflow for the result you need:

| You want to... | Use | What it does |
| --- | --- | --- |
| Place resources under one ownership and access policy | [Namespaces](resource-authoring.mdx#manage-namespaces-and-publishers) | A namespace owns Resources and their Listings. It does not alias resources from another namespace. |
| Find released items with common metadata | [Namespace and tag filters](search-cookbook.mdx#restrict-the-same-query-to-one-namespace) | Filters narrow readable results without copying resources. A namespace filter selects local releases only. |
| Keep an exact result for yourself | [Private saved references](catalog-ui.mdx#save-resources-and-searches) | A saved reference identifies an exact local release or a specific external result. It does not preserve access to that target. |
| Repeat a query later | [Private named searches](search-cookbook.mdx#save-this-group-as-a-private-named-search) | A named search stores private criteria, not results. Select **Search** in its editor to read current matches. |
| Describe related resources | [Listing relationships](resource-authoring.mdx#describe-listing-relationships) | Governed metadata declares dependencies or recommendations. It does not grant access or compute a collection. |
| Distribute Skills and MCP configuration together | [Agent Plugins](agent-plugins.mdx) | Catalog inspects an externally prepared archive. It does not assemble a package from Catalog IDs. |
| Govern an observed external item locally | [Observation adoption](../operations/federation.mdx#adopt-one-observation-as-a-local-draft) | Adoption creates a local draft from selected evidence, with retained provenance. |
| Move editable content between Catalogs | [Reusable draft transfer](draft-transfer.mdx) | Reusable JSON files carry editable content through the UI or CLI. Destination permissions, identifiers and package requirements still apply. |
| Expose selected MCP tools through one endpoint | [An external gateway such as ContextForge](contextforge.mdx#discover-and-use-a-published-virtual-server) | The gateway creates the virtual server and enforces runtime access. Catalog can govern metadata about that MCP server. |

Resources and their Listings share a namespace. Canonical Resource identifiers are unique across the tenant. Creating another namespace does not let you reuse an existing identifier or alias the Resource.

Tags belong to governed Listing metadata, not viewer-private labels. Changing a released tag requires a new Listing revision, review and release. Follow the [namespace and tag example](search-cookbook.mdx#restrict-the-same-query-to-one-namespace) to group existing releases for browsing.

Adoption and draft transfer create drafts under destination governance. Neither transfers approvals or access grants. Later source changes do not update the local draft automatically. Follow the destination's review and publication workflow.

A Plugin release pins the inspected package content. Prepare the complete package outside the Catalog before inspection. Catalog does not aggregate callable tools, create virtual servers or generate runtime endpoints.

### Current releases and shared collections

**Catalog** shows the current published or deprecated release for each eligible local Listing. A saved resource reference records an exact release and revision. A replacement release, release retirement or access loss can make that reference unavailable. It does not grant access to release history.

Shared collections organize existing local Listings across namespaces within a tenant. A collection follows each Listing's current published or deprecated release. Curated subsets for individual discovery hosts remain unavailable. Namespace filters and private named searches do not change public snapshots. Follow [discovery publication](publishing.mdx#release-and-publish-the-browser-agent) for the separate host and snapshot workflow.

## Local governance and federation are distinct

Follow [federation source operations](../operations/federation.mdx) to connect, synchronize and maintain an approved upstream.

A source can participate in administrator-selected indexed, live or referral discovery. Its provenance and partial-failure information remain visible; an incomplete synchronization must not withdraw the last complete snapshot. Clients cannot supply arbitrary federation destinations or choose the server's topology.

An external observation remains external until an authorized adoption creates a locally governed draft with provenance. That draft still goes through local validation, review and release. See the [source onboarding walkthrough](local-development.mdx#connect-a-federated-source) and [standards reference](../reference/standards.mdx).

## Curate a shared collection

Use **Shared collections** to create a named selection in an owner namespace. Open a readable local Catalog record and select **Add to collection**. Select a destination collection, then review and confirm the addition. Other readers see only its currently eligible entries that their own permissions allow.

Collections support Agents, MCP Servers, Skills and Plugins. Membership references the Listing, so its next eligible release appears on a fresh read. Draft changes remain invisible. Unavailable references stay retained and can reappear after access or eligibility returns. Collection removal never edits or withdraws the source entry.

Use **Search collection entries** and **Resource kinds** to narrow one collection. Select **Apply filters** to load the first matching page; editing the controls alone sends no request. Multiple kinds match any selected kind, and text must also match. **Clear filters** returns to browsing. **Refresh current entries** refreshes the applied selection and preserves pending filter and metadata edits.

Text matches every whole-word token across the released display name and description. Matching is case-insensitive for Unicode letters and numbers; whitespace and punctuation separate tokens. There is no stemming, prefix matching, substring matching, accent folding or wildcard operator. For example, `invoice cafe` matches a name containing `Invoice` and a description containing `CAFE`. The prefix `inv` does not match these words. Punctuation-only text matches nothing.

Text is limited to 256 Unicode code points before normalization. Whitespace-only text and no selected kinds leave browsing unrestricted. Tags, identifiers and unpublished candidates are not searched. Matching never changes the Listing-ID order or collection membership.

Only currently readable, eligible matches contribute to a page and its continuation. A stale cursor requires an explicit first-page refresh using the applied selection; it never silently applies pending edits. Filtering grants no additional target access and provides no hidden counts.

Collection names and descriptions are shared plain text. Do not put information there that collection readers should not receive. Authored text is not automatically redacted when access to a mentioned entry changes. Collection revisions and update times can reveal curation activity.

A collection permits 200 retained references, including hidden or unavailable entries. Each namespace permits 100 collections and each tenant permits 1,000. The name permits 160 Unicode code points and the description 2,000. Pages default to 20 entries and permit at most 100. Capacity refusals can reveal occupied capacity, but member pages provide no hidden counts or placeholders.

A manager can remove an exact known membership after target access is lost. Successful removal confirms that reference was present; it returns no target metadata. Managers can also delete the collection and all its references. The ordinary browser does not provide an inventory of hidden targets.

Collections are available in the authenticated Catalog without a deployment feature flag. Access depends on current scoped role assignments. An account without collection permissions cannot read or manage them.

Reading requires `collection.read` in the owner namespace. Management requires `collection.manage` and collection read there. Adding and browsing a target separately require `resource.read` in its canonical namespace. Contributors can manage collections in their assigned scope; reader, reviewer and publisher templates can read them. Tenant Administrators receive both permissions. Custom roles select the finite actions explicitly. Both human and service memberships can use the API.

For a restricted account, [create a custom role](access-management.mdx#create-a-custom-role-with-explicit-read-companions) with `collection.read` and assign it in the collection's owner namespace. Add `collection.manage` when that account should curate entries. Grant `resource.read` separately in each target namespace. Changing or revoking a grant takes effect on the next authorized read; it does not delete retained collections or references.

Collections are authenticated metadata views. They do not grant source access, change public discovery feeds, install a resource or create a callable virtual MCP server. A personal bookmark belongs to one human and retains an exact release reference. A private follow tracks the current release of one local Listing and remembers the release revision its owner last acknowledged. Shared collections have namespace ownership and follow current releases.

## Evidence and notifications have different purposes

Audit records support bounded investigation of classified actions. Optional webhooks notify receivers of committed changes. Delivery is at-least-once and unordered. Receivers must read current authorized state; notifications are not an audit archive.

Jobs track work. Metrics describe operational health without exposing private records. See [audit operations](../operations/audit.md), [webhook operation and reconciliation](../operations/webhooks.mdx), and [health and metrics](../operations/observability.mdx).

PostgreSQL is the production system of record. Search projections and delivery work do not become a second metadata authority. Credentials, resource runtimes and complete source trees belong at their documented external boundaries, not in a new Catalog-managed execution platform.
