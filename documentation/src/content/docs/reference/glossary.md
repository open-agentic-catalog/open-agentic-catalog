---
title: Glossary
description: Plain-language definitions of Catalog objects, standards, access controls and operational terms.
tableOfContents:
  maxHeadingLevel: 2
---

Terms used in the Catalog UI, API and documentation, listed alphabetically. Follow [Concepts and lifecycle](../guides/concepts.md) for the workflow, or use documentation search to find a particular term. Definitions describe Catalog usage; linked guides contain the detailed contracts and procedures.

## A-C

### A2A and Agent Card

Agent2Agent (A2A) defines communication between agents. An Agent Card describes an agent's capabilities, interfaces and authentication requirements. Catalog validates the card as metadata; it does not send A2A tasks. See [standards and formats](standards.mdx).

### Access token and ID token

An access token is the credential sent to the Catalog API. An ID token tells the login client who signed in; it is not an API credential. The UI, CLI and VS Code extension use separate login clients. See [API and browser sign-in](../operations/sign-in.md#add-optional-browser-login).

### Actor and subject

An actor is the identity making a Catalog request, resolved to a tenant, membership and current permissions. Its subject identifies the human or service principal; in OIDC mode it comes from the verified token's `sub` claim, not a display name or an assumed email address. Audit actor filters use separate stable principal IDs, not that external subject. See [authentication](api-conventions.md#authentication-and-authorization) and [audit attribution](../operations/audit.md#access-and-evidence-model).

### Adoption

An explicit authorized command that creates a local draft from one retained external observation occurrence. It preserves provenance and does not skip local validation, review or publication. See [local governance and federation](../guides/concepts.md#local-governance-and-federation-are-distinct).

### Agent

One of the four governed resource kinds, represented by `agent` in the API. Catalog describes and versions the agent; its running service remains external. An Agent's advertised capabilities do not grant a Catalog user permission to invoke it.

### Agent Plugin

A package rooted at `plugin.json` that can contain Agent Skills, MCP configuration and client extensions. The `plugin` resource kind uses bounded archive inspection and whole-package governance; Catalog does not install or execute the package. See [Catalog Agent Plugins](../guides/agent-plugins.mdx).

### Agent Skill

Reusable instructions in `SKILL.md`, optionally with supporting files. Catalog's `skill` resource kind validates the document or supported package without executing it. This is distinct from an A2A Agent Card's list of advertised "skills". See [Skills and Plugins](standards.mdx#skills-and-plugins).

### AI Catalog

A catalog document format describing resources and their artifact links or inline content. Catalog reads and publishes it alongside ARD, using a separate pinned profile; the two document formats are not interchangeable. See [standards](standards.mdx).

### AIR identifier

A resource identifier in the `urn:air:...` form used by discovery, such as `urn:air:example.com:default:assistant`. It names the resource independently of a Catalog database UUID or version label. Its publisher component is not proof of domain ownership. See [identifier rules](api-conventions.md).

### ARD

Agentic Resource Discovery: the standard for finding agentic resources through discovery documents and Registry operations. It describes availability and metadata before invocation, not installation or runtime authorization. See the [ARD discovery](standards.mdx#discover-resources-with-ard).

### Artifact and handoff

An artifact is the document or package describing or delivering a resource. A handoff supplies exact external references, such as a Git commit or OCI digest, for a consuming client. Catalog retention varies by representation: retained validation evidence is not necessarily a complete archive backup. See [representation forms](../architecture/domain.mdx#representation-model).

### Audience and visibility

An API audience identifies the intended recipient of an access token through its `aud` claim. A publication audience determines which eligible releases a discovery snapshot exposes. Listing visibility is `private`, `tenant` or `public`; public visibility on a draft does not publish it. Private visibility is not a per-user setting. See [token audiences](../operations/sign-in.md#configure-the-api-and-tenant-mapping) and [visibility and lifecycle](../architecture/domain.mdx#visibility-is-separate-from-lifecycle).

### Audit record

Retained, tenant-bound evidence of a classified action, including its attribution and affected objects. Audit records are not request logs, metrics, traces or a complete record of every read. See [audit evidence](../operations/audit.md).

### Authentication and authorization

Authentication establishes the caller's identity; authorization decides which Catalog actions that identity may perform. A valid access token alone does not grant a Catalog role, namespace permission or another tenant's data. See [access rules](api-conventions.md#authentication-and-authorization).

### CatalogListing

The stable identity of a listing in a Catalog namespace. Its presentation is recorded in immutable ListingRevisions, separately from resource content. A ListingRelease selects an approved combination for governed discovery. See [the domain model](../architecture/domain.mdx).

### CloudEvents

The [CloudEvents](https://cloudevents.io/) envelope format used by webhook notifications. The `(source, id)` pair identifies an event for receiver deduplication; a signature verifies the notification, not the receiver's permission to read Catalog data. See [receiver integration](../guides/integrating.mdx).

### Confidential and public client

A confidential OAuth client can protect a client secret, as the Go UI host does. A public client, such as the CLI or desktop extension, cannot rely on a secret. These Catalog clients use authorization code with PKCE. See [browser client registration](../operations/sign-in.md#add-optional-browser-login) and [human CLI registration](../operations/sign-in.md#register-a-human-cli-client).

### Credential binding

An opaque name that connects approved source configuration to a deployment-injected credential. It is not the secret value and does not make Catalog a secret manager. See [runtime configuration](configuration.mdx) and [source connectivity](../architecture/federation.mdx).

### CSRF

Cross-site request forgery: another website tries to make a browser perform an unwanted action using its existing login session. The UI host checks the request origin for login selection, logout and cookie-authenticated mutations. These checks complement session cookies and backend authorization. See [browser session security](../architecture/catalog-ui.mdx#local-test-users-and-oidc).

### Cursor

An opaque continuation token returned with a page of results. It binds the relevant request and access conditions; search cursors additionally bind data generations or publication snapshots. Do not construct or edit one, and restart when it becomes stale. See [pagination](api-conventions.md#pagination-and-search-consistency).

## D-I

### Delegation

Authority to act on behalf of another party, such as a consenting user. Authenticating a service's own identity is not user delegation. Token exchange and actor chains require the issuer and receiving service's supported policy; Catalog does not exchange tokens or enforce delegated `act` chains. See [client handoff](../guides/integrating.mdx#example-a-client-uses-a-discovered-resource).

### Digest

A hash that binds particular bytes or a defined canonical encoding, commonly SHA-256. A digest helps detect changes. It does not establish a Resource UUID, signature or proof that content is safe. Retained document identifiers and some ETags derive from content digests. The relevant representation contract determines which bytes are hashed.

### Discovery host

A configured hostname bound to one tenant, publisher domain and discovery mode. Host-bound ARD and AI Catalog reads use its active audience-scoped publication snapshot; clients cannot choose another tenant through the query. See [host isolation](../architecture/publication.mdx#exact-discovery-host-isolation).

### ETag and If-Match

An HTTP ETag identifies a response representation or revision. For revision-changing commands, send the current value in `If-Match` to reject stale updates. It is not the resource's named version; idempotency protects a different boundary. See [integration guidance](../guides/integrating.mdx).

### EvidenceSet and ValidationRun

An EvidenceSet contains validation findings, state, profile, policy and applicable retrieval evidence bound to exact content. A ValidationRun records an append-only validation result for a VersionRevision. Revalidation can renew evidence without changing the immutable content or rewriting a prior review. See [validation evidence](../architecture/domain.mdx).

### Facet

A category and count summarizing matching records, such as resource kind or tag. Catalog Explore computes facets over local matched records; its counts are not totals across every indexed or live federation source. See [search behavior](../architecture/domain.mdx#search-architecture).

### Federation

Discovery across administrator-approved external sources. One Catalog reads another's published metadata and retains external observations with source provenance. Sources can also contribute live results or referrals under configured scope and disclosure policy. Source synchronization does not copy databases, drafts, memberships or local governance state. Adoption into local governance requires an explicit command. Follow the [two-Catalog and VS Code tutorial](../guides/two-catalog-federation.mdx) for a complete indexed-synchronization example. See [source setup and synchronization](../operations/federation.mdx) and [federation architecture](../architecture/federation.mdx) for operating rules.

### Idempotency

Protection against repeating a command's effects when its result is uncertain. A mutation's `Idempotency-Key` binds its request and authority; an eligible completed replay returns the saved response. Replay is time-bounded and still checks current access. It does not provide permanent exactly-once protection. See [retry and reconciliation](../guides/integrating.mdx#retry-and-reconcile-uncertain-outcomes).

### Identity provider (IdP)

The service that signs users in and issues tokens. Keycloak manages the example's users and passwords; Catalog manages their Catalog access. A provider account does not create a Catalog membership or role assignment. See [provider setup](../operations/sign-in.md#choose-an-identity-provider) and [Catalog access management](../guides/access-management.mdx).

### Identity realm

The Catalog mapping from a verified issuer and, where configured, an external organization to a Catalog tenant. Dedicated mode selects one configured tenant; shared mode also requires an organization mapping. It is distinct from a Keycloak realm, user membership and role assignment. See [dedicated and shared mapping](../operations/sign-in.md#configure-the-api-and-tenant-mapping) and [identity-realm administration](../operations/database-roles.md#identity-realm-administration).

### Indexed search

Search over retained external observations and their derived projections in Catalog's PostgreSQL database. Indexed records retain source provenance and are not locally governed releases. This is distinct from querying an upstream source live. See [search pagination](../architecture/domain.mdx#authenticated-federated-result).

### Issuer

The exact URL identifying the token issuer. Catalog accepts tokens only from its configured issuer. A Keycloak example is `https://identity.example.com/realms/catalog`; it is a realm URL, not a login endpoint. See [Keycloak realm setup](../operations/oidc-keycloak.md#prepare-the-realm-and-api-audience).

## J-P

### Job

Durable asynchronous work, such as source synchronization or semantic reindexing. A `202` response accepts work; inspect the returned Job until its terminal outcome. Not every mutation or validation operation creates a Job. See [following durable work](../guides/integrating.mdx#track-jobs-to-completion).

### JSON-LD

[JSON for Linked Data](https://www.w3.org/TR/json-ld11/). A context maps property names to shared identifiers so discovery metadata and queries can express common terms. Catalog supports a bounded pinned interpretation, not arbitrary remote contexts or deep-field queries. See [standards support](standards.mdx).

### JWT

[JSON Web Token](https://www.rfc-editor.org/rfc/rfc7519): the signed access-token format verified by the production API. Catalog checks the configured issuer, audience and required claims, then resolves current Catalog membership and permissions. Token-supplied role or group claims do not create Catalog authority. See [authentication](api-conventions.md#authentication-and-authorization).

### Keycloak realm

A container for identity-provider users, passwords and OAuth clients in Keycloak. It is separate from a Catalog identity realm and tenant. One issuer can serve multiple Catalog tenants through explicit mappings; no universal one-to-one relationship is required. See [Keycloak setup](../operations/oidc-keycloak.md) and [tenant mapping](../operations/sign-in.md#configure-the-api-and-tenant-mapping).

### LangGraph

A Python library for stateful model and tool workflows. The optional Catalog example uses it to search and read release metadata through the Python SDK. It does not execute discovered resources. See [the discovery assistant](../../../../../examples/langgraph/README.md).

### Lease and claim fencing

A lease gives a worker temporary ownership of work. Claim fencing rejects outcomes written by an expired or replaced claimant. Leases allow recovery after worker loss; they do not guarantee that an external receiver sees a request exactly once. See [worker and delivery behavior](../architecture/operations.mdx).

### Lexical and semantic search

Lexical search matches words and identifiers in metadata. The core profile works without embeddings and requires all query terms to match searchable fields. Optional hybrid search adds semantic matches using metadata embeddings and deterministic rank fusion. A score expresses relevance, not confidence, approval or trust. See [search tiers](api-conventions.md#search-tiers) and [hybrid search](../operations/hybrid-search.mdx).

### ListingRelease

The governed release of an approved listing/content combination. Its disposition can be published, deprecated, retired, superseded or revoked while the bound revisions remain immutable. Creating a release does not activate a host publication snapshot. See [release lifecycle](../architecture/domain.mdx#release-state-machine).

### ListingRevision

An immutable revision of listing presentation, including metadata and visibility. Editing presentation creates another revision rather than changing the one already reviewed or released. Resource content is versioned separately in VersionRevisions.

### Live search

A bounded request to an approved external source during search. Results are transient and can be partial; they are not automatically indexed. Core Search can continue an unchanged bounded window. See [live continuation behavior](../architecture/domain.mdx#authenticated-federated-result).

### MCP

Model Context Protocol: a protocol for applications to use tools, resources and prompts from an MCP server. Catalog's `mcp_server` kind holds server metadata, not a running server. Registry `server.json`, experimental Server Cards and Plugin `mcp.json` configuration are different formats. See [MCP metadata](standards.mdx#mcp-metadata-formats).

### Membership

A human or service identity's relationship to a Catalog tenant. Active membership allows identity resolution; role assignments supply permissions tenant-wide or in an exact namespace. Provider accounts, passwords and PostgreSQL login roles are separate records. Suspension and offboarding affect Catalog access independently of token expiry. See [membership administration](../operations/database-roles.md#tenant-membership-administration) and [scoped grants](../guides/access-management.mdx).

### Namespace

A Catalog grouping for resource/listing ownership and scoped role grants inside a tenant. It is not a Kubernetes namespace. An AIR identifier can also contain namespace segments; its text is not a Catalog namespace UUID or an authorization grant. See [identity and ownership](../architecture/domain.mdx#identity-and-ownership).

### OAuth client

An application registered with the identity provider to request tokens. The UI, CLI and VS Code extension use separate registrations. A client ID identifies that registration; it does not grant Catalog permissions or necessarily equal a service account's subject. See [browser registration](../operations/sign-in.md#add-optional-browser-login), [CLI registration](../operations/sign-in.md#register-a-human-cli-client) and [VS Code sign-in](../../../../../extensions/vscode/README.md).

### Observation and occurrence

An observation retains external discovery metadata and compatibility/provenance evidence. An occurrence identifies its exact source snapshot, retrieval and entry location. Selecting an occurrence makes adoption reproducible; merely observing it does not establish local approval.

### OCI

Open Container Initiative: the image/distribution conventions used for Catalog container images and supported external handoff references. An immutable manifest digest identifies OCI content; a mutable tag alone does not. Catalog does not pull or execute referenced resource images. See [container deployment](../operations/deployment.mdx).

### OIDC and PKCE

OpenID Connect (OIDC) lets a login client verify a user's identity through an identity provider. Browser, human CLI and desktop extension login use Proof Key for Code Exchange (PKCE). PKCE binds authorization-code exchange to the login attempt. The API validates access tokens; Catalog does not issue production tokens or manage provider passwords. See [authentication and sign-in](../operations/sign-in.md) and [human CLI login](commands.md#human-cli-login).

### Ollama

The local model server used by the LangGraph example. It supplies tool-calling inference; the assistant searches the Catalog through its separate API. It is not required for ordinary Catalog discovery. See [the discovery assistant's model setup](../../../../../examples/langgraph/README.md).

### OpenAPI

An [OpenAPI](https://spec.openapis.org/oas/v3.1.0) document is the machine-readable HTTP API description used for the API reference, client types and contract checks. The Control OpenAPI describes the API listener; the worker's separate operations listener has its own documented health/metrics contract. See [API surfaces](../architecture/api-surfaces.md).

### OpenTelemetry and OTLP

[OpenTelemetry](https://opentelemetry.io/docs/what-is-opentelemetry/) provides telemetry instrumentation and export conventions; OTLP is its transport protocol. Optional Catalog tracing exports spans over OTLP/HTTP. Metrics remain Prometheus-format scrape endpoints; Catalog does not push OTLP metrics or logs. See [observability](../operations/observability.mdx).

### Outbox

A PostgreSQL record committed with a classified state change and its audit evidence, for post-commit processing. Optional webhook delivery consumes eligible public events. An outbox is not a second metadata store or a guarantee of downstream business completion. See [events and delivery](../architecture/operations.mdx#events-and-post-commit-delivery).

### Problem details

The structured error body returned by the Control API using RFC 9457 and `application/problem+json`. Catalog adds a stable `code` for programmatic handling and a `requestId` for diagnostic correlation. Public discovery uses its separate ARD error envelope, so clients should follow the contract for the endpoint they call. See [responses and errors](api-conventions.md#responses-and-errors).

### Profile

A named set of interpretation rules. A standards profile pins an upstream format; a validation profile defines evidence semantics; a search profile defines matching behavior. A configuration profile is a runtime-settings preset. These are different uses of the word, not interchangeable API versions. See [standards profiles](standards.mdx#supported-formats-and-versions).

### Projection

A derived view of authoritative records, such as search fields or an audience-specific discovery document. Search projections are rebuildable. Published snapshots are retained immutable evidence and cannot be rewritten as an ordinary index repair. See [persistence](../architecture/persistence.md).

### Prometheus and Grafana

Prometheus collects and queries time-series metrics; Grafana displays dashboards backed by a data source. Catalog exposes Prometheus-format metrics and provides optional monitoring deployments and a dashboard. Neither tool is required to run the Catalog or export OTLP traces. See [performance and monitoring](../guides/performance-and-monitoring.md).

### Proof of possession

Evidence that a caller controls the key bound to a credential, not merely a copy of the credential. DPoP uses request-specific signed proofs; certificate-bound tokens require verification of the client-certificate binding. Catalog's bearer JWT verification does not check either type of proof. See [credential limits](../operations/security.md#credential-and-authorization-limits).

### Publication snapshot

An immutable bundle generated from eligible local releases for a particular discovery host and audience. Activating it exposes its metadata through that host. It is not a database backup or a federation-source snapshot. See [publication](../architecture/publication.mdx).

### Publisher

A Catalog organization/domain metadata record associated with listings. Separately, the `publisher` role permits publication actions, and an AIR identifier has a publisher component. Administrative approval of a publication domain is not verified external domain ownership. See [governance concepts](../guides/concepts.md).

## R-Z

### RBAC

Role-based access control. Catalog roles and applicable namespace grants determine permitted actions. They are separate from Kubernetes RBAC and PostgreSQL login privileges; an identity provider's group does not automatically grant a Catalog role. See [Catalog access rules](api-conventions.md#authentication-and-authorization).

### Readiness and liveness

Liveness indicates that a process is alive. Readiness indicates that it can safely serve its role, including required PostgreSQL access, and becomes false during drain. Optional collector or receiver outages are not liveness failures. See [health and metrics](../operations/observability.mdx).

### Referral

An approved upstream discovery locator returned for a client to query separately. It is not an indexed resource, and it does not grant access to the upstream. Source policy controls which referrals Catalog can disclose. See [federation](../architecture/federation.mdx).

### Registry

In ARD, a discovery service with Search and optionally Explore/List operations. An MCP Registry describes MCP distribution metadata, while an OCI registry stores container/artifact content. These are distinct contracts; supporting one does not imply support for the others. See [standards](standards.mdx).

### Representation

One format-specific description or delivery form within a VersionRevision, identified by media type, profile and digest. A revision can contain several representations. The supported form determines whether Catalog retains a document, package inspection evidence or an external handoff. See [representation forms](../architecture/domain.mdx#representation-model).

### Resource

The stable tenant-owned identity of an Agent, MCP Server, Skill or Plugin. Its database ID, canonical identifier, named versions and release UUIDs serve different purposes. Creating a Resource does not publish or deploy it.

Private Control records can use bounded non-AIR identifiers; AIR values are parsed and normalized when supplied. Discovery snapshots require a valid AIR identifier. Changing a display label does not convert an opaque identifier into an AIR value. See [identifier rules](api-conventions.md#identifier-and-version-lengths) and [private native drafts](../guides/resource-authoring.mdx#preview-a-native-draft-locally).

### ResourceVersion

A named version of a Resource, with candidate and sealed content-revision pointers. The label may come from the resource format; it is not an ETag or the integer revision of a mutable Catalog record. See [content and review lifecycle](../guides/concepts.md).

### Reusable draft file

A JSON file containing one Resource's kind, canonical identifier, Version content and optional Listing metadata. It contains editable input, without database record IDs or approval evidence. Import requires an explicit destination. Skill package bytes remain separate. See [Move drafts between Catalogs](../guides/draft-transfer.mdx) for UI and CLI workflows.

### ReviewSubmission

The exact content revision, listing revision and validation run submitted for review. Approval, changes required and rejection are decisions on that submission, not future edits. Approval and publication are separate commands. See [review lifecycle](../architecture/domain.mdx#review-state-machine).

### Revision and VersionRevision

An immutable VersionRevision records exact resource content; a ListingRevision records exact listing metadata. Mutable records also expose revision counters for optimistic concurrency. Creating a new content or listing revision does not rewrite earlier approved evidence. See [the domain model](../architecture/domain.mdx).

### RLS

PostgreSQL row-level security, which restricts rows by tenant context for the API role. Workers use a separate restricted role with deliberate cross-tenant selection authority and explicit tenant predicates in subsequent operations. RLS does not replace Catalog authorization. See [database identities](../operations/database-roles.md).

### SBOM

Software bill of materials: an inventory of software components. The image SBOM target describes Catalog's own built image; it does not assess every resource listed in the Catalog or prove an artifact safe. See [build and release](../contributing/build-and-release.md).

### Scope

An OAuth scope limits a token's allowed actions when scope enforcement is enabled. It does not grant a Catalog permission. Catalog assignments grant permissions tenant-wide or in one exact namespace; that namespace scope is not an OAuth claim. A federation source scope selects which external items can participate. None of these scopes is the deployment's network-egress allowlist. See [API authorization](api-conventions.md#authentication-and-authorization), [scoped grants](../guides/access-management.mdx) and [federation policy](../architecture/federation.mdx).

### Service account

A nonhuman identity used by automation. It needs its own Catalog service membership and grants. Its access-token subject can differ from its OAuth client ID. A client-credentials token alone grants no Catalog authority. See [automation access](../operations/sign-in.md#give-automation-api-access) and [Keycloak service accounts](../operations/oidc-keycloak.md#give-a-service-account-access).

### Source and source revision

A Source is an administrator-managed connection to an external discovery endpoint, with rules for scope, indexing, live queries and referrals. Its immutable revisions retain the reviewed configuration used for retrieval and provenance. A source revision records configuration, not the content collected by a synchronization run. See [source onboarding](../architecture/federation.mdx#source-onboarding).

### Source snapshot

Retained evidence of source synchronization and its observations. Synchronization reads published metadata from the configured source; it does not copy that Catalog's private authoring or access records. Incomplete or failed runs do not replace the last complete source view. A complete upstream Registry List is not proof that an unseen item was deleted. See [source ingestion](../architecture/federation.mdx#indexed-ingestion-pipeline).

### SPIFFE, SPIRE and SVID

[SPIFFE](https://spiffe.io/docs/latest/spiffe-about/overview/) defines workload identity contracts. A SPIFFE ID is a URI naming a workload within a trust domain; it is not an AIR identifier or proof of public DNS ownership. SPIRE is an implementation that attests workloads and issues SPIFFE Verifiable Identity Documents (SVIDs), using X.509 certificates or JWTs. A SPIRE Agent is an infrastructure process, not a cataloged AI Agent. Catalog has no native SVID authentication or Workload API integration; see [token requirements](configuration.mdx#authentication-and-catalog-identity).

### SSRF

Server-side request forgery: untrusted input tricks a server into contacting an unintended destination. Catalog's hardened fetcher checks URLs, resolved addresses and redirects against deployment policy, with bounded time and response sizes. An approved source or credential binding does not bypass those checks. See [outbound security controls](../operations/security.md#federation-and-public-surface-controls).

### Tenant

The primary Catalog data-isolation boundary, containing memberships, access policy, namespaces and governed records. It is separate from a Keycloak realm and a Catalog identity realm. Production identity configuration resolves the tenant; callers cannot gain access by supplying a tenant UUID in a query or token claim. See [tenancy and identity](../architecture/domain.mdx#identity-and-ownership) and [dedicated/shared mappings](../operations/sign-in.md#configure-the-api-and-tenant-mapping).

### Trace and span

A trace groups related diagnostic spans; a span records one operation. Durable worker attempts start separate traces linked to the creating context. Sampling and export failures can omit traces, so they are not audit evidence. See [distributed tracing](../operations/observability.mdx#distributed-tracing).

### Trust and assessment

Judgments about publisher authenticity, software safety or rights, distinct from structural validation and local review. Optional [ANS assessments](../operations/security.md#ans-verification-authorities) check signed registration evidence. They do not establish software safety, legal rights or publisher ownership. Broad trust and rights remain `not_assessed`. A valid document, approved listing or matching digest is not a successful security assessment. See [standards boundaries](standards.mdx).

### Webhook

An optional signed HTTPS notification of a committed event, using a CloudEvents envelope. Delivery can repeat, arrive out of order or exhaust retries. Receivers verify and deduplicate notifications, durably accept work and reconcile current authorized state. See [webhook operations](../operations/webhooks.mdx).

### Workload identity

The identity of a running program, issued by its deployment's identity infrastructure. It is distinct from a Resource/AIR identifier and from a human user's identity. Catalog maps an accepted caller credential to current Catalog membership and permissions; discovery does not grant the workload permission to invoke another resource. See [the identity map and worked flows](../guides/integrating.mdx#keep-identities-and-credentials-separate).
