# Open Agentic Catalog: application context

This overview describes the implemented Catalog and its limits. Exact contracts remain owned by the code, API definitions and guides listed below.

The service provides governed Agent, MCP Server, Skill and Plugin metadata, scoped access, immutable publication and bounded federation. The optional UI includes content, access and operational administration, protected drafts, private personal workflows and a connected API reference. The sections below describe these capabilities without requiring the closed-task history.

Keep raw measurements and reports under ignored `output/`. Local notes are optional context and are not required to build, test or understand the application. A passing repeat does not explain an earlier failure. See [remaining limits](#remaining-work-and-explicit-limitations) before selecting qualification or deployment work.

## Reading map

- [Purpose and product boundary](#purpose-and-product-boundary)
- [System shape and process roles](#system-shape-and-process-roles)
- [Implemented features and defaults](#implemented-features-and-defaults)
- [Domain vocabulary](#domain-vocabulary)
- [Authoring, review and release lifecycle](#authoring-review-and-release-lifecycle)
- [Identity, tenants and permissions](#identity-tenants-and-permissions)
- [HTTP contracts and client behavior](#http-contracts-and-client-behavior)
- [Publication and public discovery](#publication-and-public-discovery)
- [Search and optional hybrid retrieval](#search-and-optional-hybrid-retrieval)
- [Federation and provenance](#federation-and-provenance)
- [Standards and retained representations](#standards-and-retained-representations)
- [PostgreSQL, transactions and migrations](#postgresql-transactions-and-migrations)
- [Jobs, notifications and housekeeping](#jobs-notifications-and-housekeeping)
- [Audit, feedback and recovery](#audit-feedback-and-recovery)
- [Security and outbound networking](#security-and-outbound-networking)
- [The optional browser UI](#the-optional-browser-ui)
- [Configuration and secrets](#configuration-and-secrets)
- [Run the application locally](#run-the-application-locally)
- [Deployment and operational behavior](#deployment-and-operational-behavior)
- [Observability and performance](#observability-and-performance)
- [Testing and quality gates](#testing-and-quality-gates)
- [Coverage requirements and verification boundaries](#coverage-requirements-and-verification-boundaries)
- [Repository ownership and change rules](#repository-ownership-and-change-rules)
- [Development skills](#development-skills)
- [Remaining work and explicit limitations](#remaining-work-and-explicit-limitations)
- [Source authority and maintenance](#source-authority-and-maintenance)
- [Sample and client workflows](#sample-and-client-workflows)
- [Catalog consumer behavior](#catalog-consumer-behavior)
- [Live tenant operational settings](#live-tenant-operational-settings)
- [Optional LangGraph client example](#optional-langgraph-client-example)

## Purpose and product boundary

Open Agentic Catalog is an independently deployable Go service for governed authoring, versioning, review, publication, discovery and federation of four kinds of agentic resources:

| Kind | What the Catalog manages |
| --- | --- |
| Agent | Agent metadata, including a validated A2A Agent Card and immutable references to a separately operated runtime. |
| MCP Server | Discovery or distribution metadata using the supported MCP Registry or experimental Server Card format. |
| Skill | Agent Skill Markdown or an inspected package, with complete file inventory and immutable client handoff where required. |
| Plugin | An Agent Plugins 1.0.0 package with a manifest, Skills, MCP configuration and optional client extensions, inspected without execution. |

It records what each resource is, which exact version was reviewed, who approved it, what an audience can discover and where external evidence originated. Discovery grants no trust, installation, deployment or execution authority. The Catalog does not run agents, process A2A tasks, invoke MCP tools, execute packages, host chat or provision runtimes; downstream clients own those decisions.

The application is a standalone modular monolith. Other UIs, CLIs, IDEs and assistants use its HTTP/discovery contracts. Redis, Kafka, Vault, PgBouncer, model providers and Kubernetes are not required runtime dependencies.

Optional chart-managed PgBouncer uses session pooling, one private `Recreate` replica and distinct API/worker logins. Both TLS hops remain verified; schema-owner operations connect directly. Enablement never rewrites DSN Secrets. Warm pgx sessions need separate backend budgets. Runtime admission refuses advertised non-session mode. The [pooling runbook](documentation/src/content/docs/operations/database-roles.mdx#connection-pooling-boundary) owns enablement, maintenance and capacity limits.

The [design philosophy](documentation/src/content/docs/architecture/design.mdx#operational-simplicity-and-least-surprise) favors operational simplicity and least surprise: explicit configuration and external secrets, replaceable processes, externally retained durable state and deployment-collected logs. Infrastructure owns PostgreSQL HA, promotion, fencing and writer routing; Catalog owns safe connection recovery, readiness and durable-work semantics. Reuse driver/pool behavior and existing bounded retry policies, applying configurable backoff/jitter where appropriate without stacked loops or blind replay of uncertain writes. New custom mechanisms need a demonstrated requirement; this guidance introduces no new runtime setting or capability.

Project source uses [Apache License 2.0](LICENSE). Third-party licenses remain intact, including CC-BY-4.0 for some retained specifications/examples.

## System shape and process roles

```text
Browser --> optional React client + Go UI host --user access token--> Catalog API
CLI / automation / other clients ---------------------------------> Catalog API
Anonymous or authenticated discovery clients ---------------------> Catalog API
                                                                        |
                                                             domain services
                                                                        |
                                                                PostgreSQL 18
                                                                        |
Catalog worker <------ durable Jobs, outbox and housekeeping -------------+

Schema-owner catalogctl --> migrations, provisioning and focused repair
API / worker -----------> approved fetches and optional embeddings
Worker -----------------> optional signed HTTPS webhooks
API / worker -----------> metrics; optional bounded OTLP trace export
Documentation site -----> independent static reader manual
```

| Executable or role | Responsibility | Authority and state |
| --- | --- | --- |
| `catalogd api` | Control API, authenticated Catalog reads and host-bound public discovery. | Exact least-privilege API PostgreSQL identity; owns public snapshot activation. |
| `catalogd worker` | Durable Jobs, source maintenance, optional semantic indexing, outbox delivery and housekeeping. | Distinct worker PostgreSQL identity; private operations listener, no product API. |
| `catalogd all` | API and worker together for quick development. | In-memory repositories only; unsuitable for a durable PostgreSQL deployment. |
| `catalogctl` | Human OIDC login, native/reusable draft preview, apply/resume and export, explicit remote reads, semantic reindex, configuration/profile checks, diagnostics and operator actions. | `remote`, online import and export use explicit bearer credentials or a selected login file; migration, bootstrap and repair use separate database authority. |
| `catalog-ui` | Serve the React application, handle browser login/session state and forward allowed calls to one API. | No SQL access or backend domain implementation. |

`internal/platform/runtime` validates inputs and wires domain ports to adapters; `cmd/catalogd` owns supervision, listeners, signals and drain. Domain packages must not import SQL, HTTP handlers, OIDC libraries or deployment providers. The memory implementation supports development/tests; only real PostgreSQL tests establish SQL, locking, grants and row-level security (RLS) behavior.

## Implemented features and defaults

| Area | Current behavior |
| --- | --- |
| Governance | Four resource kinds, immutable revisions and evidence, independent review, sealed version labels, releases and retained history. |
| Identity | Development fixtures or signed JWT access tokens, Catalog-owned realms/memberships/grants, suspension and terminal offboarding. |
| Discovery | Authenticated browse/Search/Explore, published records, ARD and AI Catalog snapshots, Registry Search/Explore/List and publication-authorized documents. |
| Federation | Administrative inspection, approved topology, indexed/live/hybrid participation, conditional retrieval, synchronization Jobs, provenance and explicit adoption. |
| Private feedback | Human submissions, own/admin reads, exact item context, one current response and revision-fenced lifecycle. |
| Operations | Exact runtime roles, migration planning/application, readiness/drain, audit export and isolated restore verification, bounded repair and cleanup. |
| Browser client | Search with common filter controls and bounded local category suggestions; authoring, review, releases, publication, source administration/adoption, access administration, Jobs, webhooks, audit and connected API reference. |
| Hybrid retrieval | Implemented but default off; requires explicit compatible inference configuration and separately applied semantic schema. |
| Webhooks | Implemented; runtime and Helm default off, retained local Compose defaults on unless overridden. |
| Tracing | Implemented bounded OTLP/HTTP export; default off. |
| Monitoring | Optional Prometheus/Grafana Compose tooling and a separate optional Helm companion; default off. |
| External artwork | Sealed local descriptors, source opt-in and consuming-tenant overrides; transient bounded PNG/JPEG delivery through exact configured origins. Empty media origins deny retrieval. |
| Managed media and software assessment | Unavailable; enabling their flags fails startup. |

Inspect results, not just exit status: a successful read can return a failed Job, Search can be partial, and restore verification can report integrity findings. Accepted asynchronous work is not completed work.

## Domain vocabulary

| Term | Meaning |
| --- | --- |
| Tenant | The isolation boundary for governed state, identity, administration and audit. |
| Realm | A provisioned mapping from verified external issuer/organization identity to an internal tenant. |
| Membership | A Catalog-owned human or service identity in a realm, with active, suspended or offboarded state. |
| Role assignment | A global or supported namespace grant to a membership. |
| Namespace | Ownership and authorization scope for authoring and listing work. |
| Publisher | Publisher metadata and declared publication domains; administrative domain approval is distinct from external ownership verification. |
| Resource | Stable identity and kind. An AIR identifier is a domain-anchored name such as `urn:air:catalog.example.com:default:order-exception-agent`. |
| ResourceVersion | A version label for a resource, sealed by its first approval. |
| VersionRevision | An immutable exact content revision with representation identity and digests. |
| Listing / ListingRevision | The listing identity and immutable presentation revisions: display metadata, visibility, tags, queries and related discovery fields. |
| ValidationRun / EvidenceSet | Exact validation findings and retained evidence for the immutable subject. Structural validity and assessment states remain separate. |
| ReviewSubmission | A frozen bundle of exact content, listing and validation identities presented for an independent decision. |
| ListingRelease | A published approved bundle selected by a listing, with explicit lifecycle state. |
| PublicationSnapshot | An immutable host/audience/profile-bound discovery bundle; one active pointer determines public output. |
| FederationSource | An administrator-approved external interface and immutable configuration revisions. |
| SourceSnapshot / Observation | A synchronization view and attributed external evidence. An observation does not become a local governed resource automatically. |
| Job | Durable asynchronous work with leases, ownership, progress, cancellation, retries and terminal state. |
| Audit event | Bounded attributed evidence for a classified action. |
| Outbox record | A transactionally committed notification or internal event, independently classified from audit. |

## Authoring, review and release lifecycle

The UI keeps new Resource identity and Version content in one workspace. Next/Back retain private input and staged files without writes. One confirmation uses separate command keys and retains acknowledged progress. Local examples, AIR construction and escaped previews/downloads supplement native document imports.

Lazy Agent and MCP editors generate A2A Cards, Server Cards and Registry metadata. Imports retain untouched Advanced fields and separate version meanings. Signed Agent Cards require an explicit unsigned copy before guided changes. The lazy Skill editor generates quoted YAML frontmatter and Markdown. Unsupported YAML stays unchanged in Advanced. Complete-package guidance preserves Git/OCI handoffs and uploaded archives when a primary document is added.

The lazy Plugin editor generates manifest, Skill and named MCP files locally. Advanced editors download exact text. Prepared files retain discard protection after Next/Back. Authors package and host the complete archive. ZIP/gzipped-tar controls preserve explicit URLs and checksums.

Optional Listing preparation shares the workspace and final confirmation. Exact-source reuse supports sealed Versions. Validation receipts retain correction input, exact evidence and Version ETags. Listing saves bind the acknowledged revision and preserve existing candidate metadata. The [authoring manual](documentation/src/content/docs/guides/resource-authoring.mdx) and [Plugin tutorial](documentation/src/content/docs/guides/agent-plugins.mdx#create-a-review-tools-plugin-in-the-browser) cover setup, role handoffs and recovery.

The `CatalogDraftFile` schema and pure Go/browser codecs define reusable JSON authoring input for all four kinds. Native documents and extension editor input remain text, including incomplete values and exact numeric tokens. Package references and preparation use separate modes; files exclude destination identities and governance evidence. The encoded ceiling is 64 MiB; raw browser text retains its 2 MiB limit.

The API exports exact VersionRevisions and optional matching Listing metadata after current metadata/content permission and required private-read audits. `catalogctl export --format draft` saves one file with every representation, through exact release or revision selection. Existing native export remains the default; batch mode retains its frozen selections and budgets. `catalogctl import preview/apply/resume` accepts the shared file with explicit destination and separate Skill package inputs. Finite command plans bind exact Resource/Listing identities and ETags, preserve original-key recovery and verify retained records.

`catalogctl export` also generates one remote MCP client configuration for VS Code, Claude Code or Cursor. It selects retained Registry or Server Card metadata from a current local published/deprecated release. `--remote N` uses original 1-based positions. Private files and setup notes omit header values. Clients own target authentication, settings and execution. The web handoff previews and downloads the same formats on explicit selection. Stored documents require current content admission and verified bytes. Session and selection changes clear temporary output; no target server is contacted.

Reusable JSON drafts support UI/CLI import and export for all four kinds, including Rust SDK export. The [transfer tutorial](documentation/src/content/docs/guides/draft-transfer.mdx) covers both directions, separate package inputs and interrupted imports. Imported content follows normal destination review and publication. UI actions stage fresh editor replacement, preserve active raw inputs and download exact saved API files. Partial-save recovery blocks replacement until explicit rearming.

Publisher domain verification is an optional DNS TXT workflow in the backend, HTTP API, four SDKs and Publisher page. It defaults off. Required mode checks fresh stored proof for new Publisher domain approvals. Expiry or clearing does not revoke an existing approval. Inconsistent command replies require explicit recovery with the original command. The [publishing guide](documentation/src/content/docs/guides/publishing.mdx#publisher-approval-and-dns-evidence) owns setup, permissions, renewal and recovery.

The ordinary workflow is:

1. Establish namespace, Publisher and grants; approve the publication domain where needed.
2. A Contributor creates a Resource/version and appends an immutable content revision.
3. Create a Listing revision containing visibility and discovery metadata.
4. Validate the exact content against format, safety, size and pinned-profile rules; retain retrieval digests/provenance where applicable.
5. Submit the frozen content/listing/validation bundle. Later edits or validation cannot change it.
6. A different actor approves, requests changes or rejects it. Self-review is denied even for broad roles. Corrections require new revisions/submission; decisions remain retained.
7. A Publisher creates the approved ListingRelease for authenticated Catalog reads. Public releases require two to five representative queries.
8. Generate, inspect and activate a host-bound snapshot before external discovery changes.

Release states are `published`, `deprecated`, `retired`, `superseded` and `revoked`. Deprecation can retain discovery with guidance; retirement, supersession and revocation withdraw affected public snapshots. None rewrites approved evidence.

Visibility is another independent dimension:

| Visibility | Meaning |
| --- | --- |
| `private` | Available only through current authorized Control/Catalog reads; excluded from discovery-host snapshots. Visibility does not grant read permission; namespace assignments separately restrict private access. |
| `tenant` | Eligible for the owning tenant's authenticated discovery host after governance and activation. |
| `public` | Eligible for anonymous discovery after governance and activation. A public draft remains unpublished. |

Unused drafts have a bounded deletion path that retains identifier reservation and required evidence. Sealed history is not arbitrarily deletable or reclaimed by housekeeping.

## Identity, tenants and permissions

Authentication verifies identity; active Catalog membership and current scoped permission assignments establish authority; OAuth scopes narrow it; domain lookups and RLS enforce tenancy. These checks are independent.

Production validates signed JWT access tokens against the configured issuer/audience, time/identity claims and bounded duplicate-safe JSON. Dedicated tenancy selects a configured tenant; shared tenancy resolves a configured organization claim through provisioned realms. Token groups, role claims and tenant UUIDs do not grant Catalog authority.

The [authentication setup and provider guides](documentation/src/content/docs/operations/sign-in.md) cover Keycloak, Entra ID, Auth0, Okta, [IBM Security Verify](documentation/src/content/docs/operations/oidc-ibm-security-verify.md) and Cognito. Only the maintained local Keycloak fixture is provider-qualified. Built-in human clients require matching ID/access subjects and origin-bound issuer endpoints; the API's optional scope ceiling reads only a string `scope`, not `scp`. The guides document hosted-provider limits without contacting hosted identities.

`make local-auth-up` starts one isolated Catalog with local Keycloak password accounts; `local-auth-down` retains its data and credentials. Catalog memberships remain explicit and separate from provider accounts. `make test-local-auth` qualifies account changes, current access, retained restart, TLS renewal and human CLI login through the documented setup. Production uses separately configured OIDC; development shortcuts remain passwordless.

API, UI and human CLI inputs preserve the exact issuer, including its trailing slash. CLI discovery normalizes only the metadata request path; callback, ID-token and saved refresh checks retain exact identity. The Auth0 guide describes built-in CLI setup with its separate tenant-wide default-audience requirement and an external-client alternative. Hosted-provider compatibility remains unqualified.

| Built-in template | Primary authority |
| --- | --- |
| `reader` | Resource metadata, history, content and namespace reads, plus own human ratings. |
| `contributor` | The same reads and own ratings, plus resource creation/editing, own eligible draft deletion, validation and review submission. |
| `reviewer` | The same reads and own ratings, plus review decisions, retaining no-self-approval. |
| `publisher` | The same reads and own ratings, plus Listing release publication and transitions; no whole-host publication authority. |
| `discovery_reader` | Private saved library, own ratings, authenticated discovery, federation participation, publisher reads and Job read admission; private Jobs additionally require their family/target authority. |
| `publisher_creator` | Publisher reads and creation. |
| `publication_admin` | Whole-host snapshot reads/generation/activation/rollback/unpublish and publication-family Job controls. |
| `source_admin` | Federation source management, inspection, synchronization/evidence, federation participation and source-family Job controls. |
| `security_reviewer` | Audit reads and bounded exports. |
| `webhook_admin` | Webhook management, tests, rotation and evidence. |
| `feedback_member` | Human own-feedback workflows, with current item-read authority where required. |
| `library_member` | Own human saved resource references and named searches; target access remains separate. |
| `tenant_admin` | All supported permissions, including protected direct tenant-level `access.manage`. |
| `service_client` | Inert identity template; no implied permission. |

The Catalog owns 50 finite permissions and 14 exact built-in templates. Custom roles select reviewed permissions from that vocabulary; direct-member groups are Catalog data within one realm. Direct/group assignments are additive at tenant or exact-namespace scope, with no role hierarchy, nesting or automatic IdP-group adoption. Access administration remains protected direct tenant authority; custom/group assignments cannot delegate it. Reader, Contributor, Reviewer, Publisher and eligible custom roles support namespace assignments; namespace-only users can browse and work with their authorized population without a global Reader grant.

Ordinary assignments grant exactly the selected template. Owner provisioning, development bundles and forward migration explicitly add tenant companions for global Reader, Contributor and Publisher workflows; scoped assignments receive none. Roles/groups/assignments change through audited APIs without restarting processes. Reads and collection selection use current coherent policy; commands and private replay hold the shared admission fence, while policy writers acquire it exclusively. Continuations refuse changed policy rather than preserving stale access. PostgreSQL remains the only production authority; there is no allow cache or policy service.

Suspension is reversible; offboarding is terminal, atomically revokes grants and retains history. The last active Tenant Administrator is protected. Realm/first-administrator bootstrap and recovery require schema-owner operations; there is no public signup or first-login administrator.

`GET /api/v1/me` returns finite current tenant permissions, navigation-only available permissions, one bounded namespace-hint page, authorization revision and the independent scope ceiling, without tokens or raw claims. `/api/v1/access/self` separately pages direct/group provenance; an exact namespace lookup resolves only the selected scope. Browser hints never grant authority. Backend requests and replays recheck authority: role removal applies on the next request, but returned data cannot be recalled. Scope changes require a new token or expiry. Known current-policy dependency failures return safe 503 on control and authenticated discovery reads without an invalid-token challenge; the UI preserves an unexpired session during the outage.

Development supplies synthetic `local-admin`, `local-reader`, `local-contributor`, `local-reviewer`, `local-publisher` and second-tenant tokens. Production rejects these fixtures.

## HTTP contracts and client behavior

`api/openapi/control-v1.yaml` defines the product listener and is embedded at `/openapi/control-v1.yaml`. It covers `/api/v1`, public discovery and API operational endpoints; the separate worker listener has its own operations contract.

| Surface | Representative routes and purpose |
| --- | --- |
| Bootstrap reads | `/api/v1/capabilities`, `/api/v1/me`. |
| Governance | `/api/v1/resources`, resource versions/revisions, catalog listings/revisions, review submissions and listing releases. |
| Catalog | `GET /api/v1/catalog/resources`, exact published-record reads, `POST /api/v1/catalog/search` and `POST /api/v1/catalog/explore`. |
| Semantic maintenance | `POST /api/v1/catalog:reindex` returns authorized rebuild work when enabled. |
| Publication | `/api/v1/publication/snapshots`, snapshot `:activate` and `:rollback`, and `/api/v1/publication:unpublish`. |
| Federation | Source inspections, sources, change previews, `:test`, `:sync`, pause/resume/removal, snapshots, observations and explicit adoption. |
| Administration | Memberships, role assignments, namespaces and Publishers. |
| Work and evidence | Jobs, webhooks/deliveries, audit events/exports and private feedback. |
| Discovery documents | `GET /.well-known/ard.json`, `GET /.well-known/ai-catalog.json`. |
| ARD Registry | `POST /ard/v1/search`, `POST /ard/v1/explore`, `GET /ard/v1/agents` for List. |
| Document delivery | Authenticated artifact content and snapshot-authorized `/public/v1/artifacts/{artifactId}`. |
| API operations | `/livez`, `/readyz`, `/version`, `/metrics`. |

Control objects reject duplicate keys, unknown fields, trailing values, invalid media types and oversized bodies. Standards evidence/extension boundaries can preserve unknown upstream members without relaxing Control requests.

Mutations require `Idempotency-Key`; revision-changing operations also require the returned `If-Match` ETag, commonly `"rev-N"`. Reservations bind canonical request, authority and tenant. State, required audit, classified outbox and replay response commit together; replay rechecks current access.

After an uncertain transport outcome, retry with the same key and inputs. Cancellation does not prove rollback, and a new key risks duplicate work. Retain returned ETags rather than inventing them. Follow the Job after asynchronous `202` acceptance.

Errors use RFC 9457 problem details, stable codes and `X-Request-ID`: input `400`, identity `401`, permission `403`, hidden/missing resource `404`, lifecycle/replay conflict `409`, revision precondition `412`, domain validation `422`, required work/dependency unavailable `503`. Public errors must conceal internal causes and private existence.

`catalogctl remote` uses an explicit API origin and manual bearer credentials or `--login-file`, never runtime SQL authority. `capabilities`, `search`, `record` and `job` are reads; `reindex` is a Tenant Administrator mutation requiring reason/idempotency key. Responses are bounded JSON. `catalogctl auth login` acquires human credentials through public-client PKCE; explicit login-file selection supports bounded serialized refresh. Online import and all export modes also accept explicit login files through the same provider. Manual bearer inputs remain independent. There is no redirect follow, URL execution or automatic all-page traversal. HTTP requires literal loopback; otherwise use HTTPS. The portable discovery Skill uses only capabilities/search/record.

`catalogctl mcp serve` provides six static read-only Control tools over stdio using the official Go MCP SDK v1.8.0. The tools include local Explore facets and current-access pages, with navigation-only permission hints and no subject, realm or membership identifiers. The [MCP guide](documentation/src/content/docs/guides/mcp-server.mdx) owns setup and tool examples.

API origin, token file and private CA are operator inputs, never model arguments. Search defaults to local releases; indexed/live selection retains current federation permissions and explicit live query disclosure. One complete page preserves order, counts, diagnostics and cursor. Detail projections expose identities and references without native instructions or automatic downloads.

Default call limits are 30 seconds, 8 MiB API response, 256 KiB serialized result and four concurrent calls. Incoming frames are capped at 256 KiB and arguments at 64 KiB. Whole-result refusal prevents skipped continuation. This is not the ARD Registry wrapper or resource execution. Optional API `/mcp` adds finite stateless HTTP with current per-request authentication, both selected protocol versions, exact origins, bounded concurrency/output and OIDC resource metadata.

The separate `make test-interoperability-mcp-catalog` target qualifies official Python MCP SDK 2.3.0 with stdio/HTTP and both selected protocols. It retains seventeen legacy authority/fault/lifecycle groups and adds 52 PostgreSQL matrix groups, four API restarts and cleanup.

The Catalog MCP HTTP transport provides shared reads, local facets and caller-access pages. It supports Authorization Code/S256 PKCE, private-CA TLS, browser CORS and optional exact-path Helm ingress. Independent-client tests cover both supported protocols and shared-PostgreSQL replica/restart behavior. Live filters include Markdown Skill types. The implementation uses the official MCP SDK without adding a root-service dependency on a generated Catalog SDK.

## Publication and public discovery

Startup maps exact hosts to tenants, modes and publisher domains. No wildcard, query-selected tenant or token-selected audience is supported. Discovery rejects `Forwarded`, `X-Forwarded-Host` and `X-Original-Host`; ingress must preserve the intended Host and strip those fields.

| Mode | Public-route behavior |
| --- | --- |
| Disabled, unknown or inactive host | No discovery response; ordinarily `404`. |
| Anonymous | Only the active snapshot's eligible public releases. Supplying a token does not expand the audience. |
| Authenticated | A verified token must resolve to the host's tenant; the snapshot can include tenant and public releases, never private listings. |

Candidates project eligible approved releases into ARD, AI Catalog, Registry data and a document allowlist, retaining membership, exclusions, profiles, generation, digests and ETags. Workers can build candidates; API activation rechecks current eligibility/integrity and atomically moves the active pointer with evidence/replay.

Selection bounds precede hydration: 1,000 eligible releases, 32 MiB of governed projection and separate entry/exclusion/artifact/work limits, including 16 MiB documents and 36 MiB snapshots. A Registry self-entry can increase emitted entries beyond the release count. Bounds are not sizing recommendations.

Public reads use only the active immutable bundle. Well-known aliases use strong ETags, Content-Digest and revalidation; authenticated output is private and varies on authorization. Search/Explore use `no-store`; List/document delivery have short audience-appropriate caching. A document digest grants no access without an active snapshot allowlist.

Domain revocation, separate release-transition commands and blocked external revalidation transactionally withdraw affected snapshots. Replacement publication preserves existing active snapshots; build and activate a new candidate to publish the replacement metadata. Rollback rechecks current eligibility, representation and profile constraints; unpublish retains configuration/history.

External HTTPS validation retains exact evidence with 24-hour freshness. Identical bytes renew it; drift requires a new VersionRevision. Publication refetches reviewed identity. Blocked revalidation withdraws snapshots; expiry/policy drift marks readiness stale without rewriting them.

## Search and optional hybrid retrieval

Authoring, Members, Explore Browse, Namespaces and Publishers support keyboard-accessible header sorting of the loaded page. Reviews, Jobs, My work and Saved items offer the same controls. Dates compare actual timestamps, while progress, attempts and domain counts compare numerically. Paging and Refresh retain the selected column without fetching other pages. Initial order comes from each API.

Publisher domains sort the complete bounded list. ARD and AI Catalog entries sort each complete retained document before local pagination. Selecting a publication column resets only that inventory's local page. Empty and missing versions remain distinct. Catalog's separate Search ordering applies before pagination.

Explore's Browse refresh belongs to its filter action row, with space before results.

Core search needs no inference: browse, exact identifiers, lexical terms, kind/publisher/tag/visibility filters and local facets work independently. PostgreSQL scopes, matches, counts, sorts and selects keyset pages before hydration. A small page can still require expensive matching/count work.

Catalog Search supports Name, Kind and Publisher ordering in both directions across local and indexed keyword matches, followed by a separately ordered bounded live window. Omitted order keeps relevance, including optional semantic matching, or name order without text. Sort changes restart paging; saved searches retain the order. Current local Publisher display names determine Publisher order, with generation invalidation after Publisher changes. Administration summaries wrap full opaque acting identities without expanding narrow pages.

Catalog Search merges local releases, grouped indexed observations and bounded live results with core-window continuation; local exact typed identities win collisions. Control Explore covers local releases. Public Search/Explore/List use the active host snapshot, without automatically republishing indexed observations.

Cursors bind caller/authority, query/filters, purpose, page size, profile, generations and expiry under a signature. Changes can require a fresh search. Control Search supports core, hybrid, live-only and browser continuation within one unchanged bounded live window. Public ARD `auto` adds live results on page one while preserving complete local snapshot traversal, including displaced local results. PostgreSQL cursor-expiry qualification still requires a stable-clock environment.

Local Search shares a narrow match set for exact count/page; Explore shares it for totals/facets. Unnamed parameter-bound SQL supports request-specific planning. Indexed count/page remain separate, skipping page selection after an exact zero count. Preserve these paths when diagnosing custom/generic plans.

Optional hybrid search uses pgvector and one configured OpenAI-compatible embedding endpoint/model. Workers index bounded display names, descriptions, tags and representative queries into complete generations; eligible indexed observations can fall back to their identifier. Public inputs come from active immutable snapshots, separately from Control vectors. The API sends eligible query text; the client adds configured prefixes and separate provider authentication. Code, artifacts, context maps, feedback, identity and audit documents are excluded as fields, but allowed text is not automatically redacted. Tenant exclusions disable indexing and query disclosure after consistent API/worker configuration and restart; they do not recall in-flight requests or establish provider-side deletion/retention policy.

Hybrid preserves authorization, filters, exact identity and continuation semantics. Fresh requests may explicitly fall back during indexing/provider failure or work overflow; successful empty results stay empty, and continuations cannot silently switch tiers. Model/profile changes rebuild derived vectors while lexical search remains available. Relevance is not trust.

Public fresh queries above 1,000 filtered entries fall back before inference. Enabling hybrid requires the separate semantic schema and matching API/worker configuration; installing pgvector or starting a model alone is insufficient.

## Federation and provenance

Origin inspection uses the existing well-known and advertised discovery paths. It does not read `/robots.txt`. Optional `Agentmap:` discovery is not implemented.

Operators define reachable origins, address policy, trust and private routes; Source Administrators approve sources within them. Ordinary callers cannot choose source URLs/topology or redirect credentials.

Inspect, connect, test/synchronize, then preview/apply consequential changes. Inspections bind endpoint, interface/profile, capabilities, policy/credential revisions and scope for 15 minutes. Connectivity/profile/interface drift requires reinspection.

Live peer Search defaults to one page; `federation.maxLivePagesPerSource` accepts 1-5 through JSON, `CATALOG_FEDERATION_MAX_LIVE_PAGES_PER_SOURCE`, Compose and Helm `configEnv`. Extra pages share the deadline and an 8 MiB per-peer body allowance. Source/credential changes discard affected contributions, continuation redirects are refused and healthy duplicate copies survive. Remaining upstream tokens make results partial. Core and live-only client pages can replay one unchanged bounded live window; hybrid pages preserve durable order before a distinct live tail. See [configuration](documentation/src/content/docs/reference/configuration.mdx#live-peer-pages). The existing two-Catalog harness qualifies opt-in three-page traversal, partial outage and fresh-query recovery; anonymous discovery still excludes private-network sources.

Source modes `indexed`, `live` and `hybrid` mean retained enumeration, live Registry queries or both. This federation hybrid mode is independent of semantic hybrid search. Scopes restrict identifiers, versions, kinds, Publishers and tags; administrators select host/audience participation and query disclosure. Private sources cannot join anonymous discovery; referral edges require approved sources and separate contact authority.

Manual/scheduled synchronization share `source.sync` Jobs and revision deduplication. Workers retrieve bounded documents, retain raw observations/provenance, validate projections and atomically commit snapshots. Partial, failed, canceled or stale-revision work cannot replace the last complete view.

Conditional bytes/validators bind source, profile, policy, credentials and request. A `304` produces fresh evidence without skipping child/context checks. List cursors are request-local, not incremental-sync state.

Complete authoritative manifests can prove withdrawal. Pinned Registry List lacks cross-page snapshot guarantees: unseen evidence becomes stale, not proven deleted. Search-only Registries remain live-only; projection expiry is not deletion.

Live discovery bounds topology, fan-out, hops, loops, time, bytes and results. Peer failures preserve valid results; valid-empty is success. ARD `none` uses the local snapshot, `referrals` adds descriptors without contact, and core `auto` can continue an unchanged bounded live window. Hybrid `auto` preserves exact durable order before a distinct live tail.

Explicit adoption creates local governed work retaining origin evidence; validation/review/publication still apply. Change previews bind affected observation membership/content and source revision, so synchronization can invalidate a preview without changing configuration.

## Standards and retained representations

Profiles are immutable vendored inputs bound by `schemas/checksums.txt`, retained commits and licenses. Builds/tests do not track upstream `main`; ignored `specs/` checkouts are research only.

| Active profile | Purpose |
| --- | --- |
| `ard-0.91-aa3e598` | ARD manifests, Registry interface, context, schemas and official conformance runner. |
| `ai-catalog-1.0-b062278` | Separate AI Catalog compatibility and structural Trust Manifest semantics. |
| `a2a-agent-card-1.0.1-3303592` | Agent Cards via generated pinned ProtoJSON bindings and service safety checks. |
| `mcp-registry-server-1.8.1-f52dc85` | MCP Registry `server.json` distribution metadata. |
| `mcp-server-card-v1-experimental-526201b` | Experimental MCP Server Card; distinct from Registry metadata. |
| `agent-skills-69ef37e` | Agent Skills frontmatter and package requirements. |
| `agent-plugins-1.0.0-1fc1b627` | Agent Plugins directory, manifest and MCP configuration contracts. |

ARD ingestion combines duplicate-safe JSON, embedded Draft 2020-12 schema and semantic checks. Generic manifests use ARD. The older well-known path and `ai-catalog` relation locate equivalent entry sources, not a legacy payload profile. Explicit media types, Link `type`, nested declarations and retained profiles select AI Catalog compatibility; `specVersion` or `host` alone does not. The pinned Trust Manifest casing discrepancy permits either spelling alone, rejects both together and preserves raw evidence.

Complete entries and sparse live Search share present media-type parsing and exact `@id`/`identifier` agreement. Sparse results can omit type, name and delivery. Unfamiliar valid parameterized types remain accepted without registration lookups; EntryFields/query-context validation stays structural. Invalid live entries produce sanitized partial evidence without removing healthy local/peer results or changing retained bytes. Local publication omits peer Trust Manifests; eligible live Search projects their declarations under lowercase `trustManifest`, without cryptographic trust claims.

Validity, compatibility, publisher alignment, trust, rights, approval, relevance and invocation permission are separate. Automatic resource-signature verification remains unavailable. Optional DNS challenges record separate point-in-time Publisher evidence; public trust/rights remain `not_assessed`. The separate file-only ANS command checks accepted registration evidence under ADR-0024. MCP metadata checks do not qualify its runtime protocol; parsed A2A signature headers are not verified signatures.

Representation storage follows the actual content contract:

- Inline object JSON is retained with canonical identity/digest rules.
- Bounded UTF-8, NUL-free text is retained in tenant-scoped PostgreSQL document blobs, SHA-256 bound and verified on reads. Documents and combined VersionRevision content have 16 MiB ceilings.
- External credential-free HTTPS documents retain exact validation bytes and provenance while the URL remains the external delivery authority.
- Skill archives are bounded ZIP or single-member gzipped-tar inspections with path/file/depth/expansion limits, complete inventories and digests. They are not extracted for execution or retained as hosted archives. Immutable Git/OCI handoff remains separate.
- Plugin HTTPS ZIP/gzipped-tar archives retain bounded exact manifest/component documents and inventory tied to archive SHA-256/size; encoded inspection is capped at 20 MiB. Archive bytes stay external and transient. Public output identifies the complete archive, not a Catalog-hosted component download.
- Git object IDs and OCI manifest digests are immutable locator metadata. The Catalog does not clone, pull, prove runtime availability, install or execute them.

Unknown types can remain external observations without native governance support. Agent Plugins 1.0.0 defines a package directory, not an ARD Plugin media type; `application/agent-plugins+json` is not standardized here.

AI Catalog namespace validation preserves AIR-only entry trust binding. Both public levels accept two-label reverse-DNS keys and supported absolute URIs, while rejecting literal whitespace/control characters. ARD validation, pinned profiles and exact source evidence remain unchanged.

## PostgreSQL, transactions and migrations

PostgreSQL 18 with `pgcrypto` owns identity, governed history, publications, provenance, documents, Jobs, outbox, replay and audit. Search projections and optional vectors are rebuildable derivatives.

| Database identity | Required boundary |
| --- | --- |
| API | Direct `LOGIN NOINHERIT NOBYPASSRLS`; exact column-level privileges and tenant context. |
| Worker | Distinct direct `LOGIN NOINHERIT BYPASSRLS`; narrow cross-tenant selection and explicit tenant predicates for follow-up operations. |
| Schema owner | Dedicated migration, exact-grant reconciliation, provisioning and selected repair authority. |

Runtime roles have no memberships and cannot be assumed from privileged sessions. Startup rejects missing/excess privileges, unsafe PUBLIC/extension grants, unsupported PostgreSQL major and non-writer connections. Runtime connections/readiness require read/write sessions; read-only operator inspection is separate.

Tenant identity belongs in rows, keys, relations and lookups. Forced RLS supplements explicit predicates; worker BYPASSRLS permits bounded global candidate selection, never tenant-free follow-up writes. Database guards protect immutable history, same-tenant parents, legal pointer/lifecycle changes and monotonic fields.

Command state, required audit, classified outbox and replay commit atomically; audit-only actions need no notification. External calls must not hold interactive domain transactions open, and local rollback cannot undo external acceptance.

The [migration guide](migrations/README.md) and [runtime privilege implementation](internal/platform/database/privileges.go) own current schema and grant details. Command and retained-format versions belong to their owning implementations. Optional semantic schema has its own checksum and explicit application path.

ADR-0017 defines no released predecessor: reject obsolete development state without speculative conversion or deletion. Applied numbered migrations are immutable and checksum-verified; schema changes add the next forward migration. Compatibility work must address actual supported releases.

The owner validates both runtime roles and holds one session advisory lock through transactional migrations and exact grant reconciliation. Drain runtimes, review the plan, apply/reconcile and start matching binaries. If reconciliation fails after a committed migration, keep runtimes stopped, correct the issue and rerun apply. Runtime startup never migrates; Helm rollback does not undo schema.

Index changes require measured role/tenant plans, representative data, read benefit, write/storage costs and locking/drain guidance. `CREATE INDEX CONCURRENTLY` cannot run in the current transaction-per-migration runner.

## Jobs, notifications and housekeeping

Jobs run synchronization, publication construction, optional ANS registration assessments and enabled semantic work. Claims are bounded, leased and owner-fenced; heartbeats do not require visible progress. Lease loss prevents stale completion. Shutdown stops claims and leaves unfinished work lease-recoverable rather than falsely canceling it. Retries preserve attempt history/parentage; API authority still owns snapshot activation. New Job limits default to five attempts (`jobs.maxAttempts`, `1..20`), retained across deduplication and explicit retry. Retryable failures use a bounded upstream minimum plus full jitter under `jobs.retryCeiling` (default `1h`, `5s..1h`), with a total 24-hour delay bound. Source and embedding-provider 429/503 hints are retained; query-time lexical fallback adds no retry wait. Settings load at startup and affect new limits/future scheduling, not retained due times. Lease reclaims and later fresh maintenance Jobs are separate from the normal failure budget.

Worker components use independent restart windows after classified dependency failures. `worker.dependencyRetryInitialDelay` defaults to `1s`; `worker.dependencyRetryCeiling` defaults to `30s`, with `1s <= initial <= ceiling <= 5m`. Windows double and saturate before upper-half jitter. A callback running at least 60s resets its current failure window; retry waits do not count. Retries continue until cancellation, while terminal failures stop. Healthy cadence is separate; a running-state observation means callback entry, not successful work. Settings are startup-only and included in safe diagnostics.

Outbox records provide reliable post-commit notification without a broker. Service-owned CloudEvents carry safe references, not raw records or secrets. Optional HTTPS webhooks add per-endpoint HMAC/timestamps and encrypted signing secrets. Delivery is at least once, unordered and retry-bounded: receiver acceptance can precede failed local acknowledgement. Receivers deduplicate `(source, id)` and read authorized current state when ordering matters.

Disabled mode has a no-network drainer; never overlap it with an enabled dispatcher during rollout. Receiver-signing rotation differs from encryption-key replacement, which requires all send-capable runtimes stopped and recovery keys retained. Receiver outages do not fail core readiness.

Fair bounded housekeeping reclaims eligible replay rows, expired previews, unused inspections, obsolete validator/admission state, terminal Job leaves, delivery evidence and acknowledged unreferenced outbox rows. Current, live, referenced and governed evidence remains. Defaults: five-minute cadence, seven-day terminal-Job retention and 24-hour acknowledged-outbox retention. PostgreSQL enforces minimum eligibility timestamps; delayed reclamation never extends logical validity. Autovacuum handles dead tuples. Housekeeping is not a legal-retention/history-deletion product.

## Audit, feedback and recovery

Audit uses tenant-bound membership, service-executor or database-login principals, versioned safe facts and enforced command/target binding. Service-populated facts exclude credentials, raw documents, unrestricted queries and external identity claims. Required reasons are deliberately retained bounded user-authored text; callers must not include secrets, and validation is not automatic secret detection. Required audit is independent of log level or trace sampling. Ordinary reads, authentication/authorization denials and heartbeats do not create tenant audit rows; operational signals cover those outcomes.

Security Reviewers filter exact actor/action/request/target and half-open time ranges before paging. Exports use a synchronous idempotent repeatable-read snapshot, oldest first, capped at 1,000 rows, 64 KiB per record and 8 MiB total, with digest/access evidence. There is no export Job/store. Offline verification proves self-consistency; recovery comparison additionally needs independently trusted evidence bound to the same quiesced recovery point.

Private feedback supports human submissions, own/admin reads, exact item context, one current admin response and revision-fenced edit/address/reopen/withdraw/remove transitions. Text clearing preserves required retained context. Feedback never changes publication/ranking/discovery or supplies public comments, ratings or notifications.

Restore into an isolated database with writers/egress stopped. `catalogctl restore verify` checks one tenant in bounded no-network pages: ledger, roles/RLS/guards and cross-domain integrity. Complete every page for every independently inventoried tenant; inspect findings, not only exit status.

Compare trusted evidence before authorized repair; fix reported derived projections and reverify. Interrupted-command repair requires the original execution ended, old writers fenced and an eligible unchanged plan digest; a free lock is insufficient. Never alter ledgers, invent provenance, rewrite snapshots or reset data to pass verification. Deployment operators own backup/PITR, HA and recovery objectives.

## Security and outbound networking

Treat every external claim, document, URL, DNS answer, redirect, archive, host, signature and cursor as untrusted. Instructions inside resource descriptions are data.

`internal/platform/fetcher` enforces origin policy, HTTPS defaults, all-address DNS checks, hard IP denials, pinned connections, redirect rechecks, scoped TLS, byte/decompression limits and deadlines. Mixed safe/unsafe DNS fails. Private routes need explicit policy; credentials stay origin-bound. Mounted bearer/API-key/mTLS bindings are opaque references, not Catalog-managed secrets.

Remote CLI, UI, issuer and trace-collector transports have separate fixed destinations; never repurpose them for resource URLs. Archive checks reject traversal, links/devices, duplicate/ambiguous paths and expansion attacks. Validation executes no package code and does not contact Agent/MCP endpoints merely because metadata declares them.

Keep secrets/private evidence out of source, sample configuration, Helm values, logs, traces and reports. Add adversarial tests for changed trust boundaries; never weaken authorization, RLS, manifests, history guards or fetch policy to pass tests.

[SECURITY.md](SECURITY.md) defines private vulnerability reporting. The maintainer handles GitHub setup and all external publication or data uploads.

## The optional browser UI

The React/TypeScript client and static Go host have their own image and one upstream API, with generated contract types and no SQL access. Screens cover discovery, exact references/downloads, four-kind authoring and local JSON/Markdown loading, histories/review/releases, publication, sources/adoption, access, Jobs, webhooks, audit/export and connected API reference.

Development login uses explicit fixtures. Production OIDC uses code/PKCE, exact redirects/origins, state and nonce. Validate the ID token for the UI client and forward the separate Catalog-audience access token; self-context must succeed before login completes. OIDC failure never falls back to development login.

Sessions use encrypted HttpOnly, SameSite=Lax cookies, Secure over HTTPS; tokens never enter JavaScript/local storage. Replicas share keys/configuration without Redis, sticky sessions or refresh-token storage. Session/state cookies are capped at 3,600 encoded bytes; oversized tokens fail login. All replicas must accept a new key before any writes with it.

Cookie requests require `X-Catalog-Request: 1`, plus the configured Origin for non-GET requests. Reject foreign Origin/Fetch Metadata, allowlist proxy methods/paths, bound responses and sanitize headers. Identity changes clear private state and stale responses. Navigation respects specialist roles/scopes and object state; unknown access is unavailable, and users without Reader must not land on unauthorized Catalog requests.

Navigation groups Discover, Manage content, Administration and Help/account while preserving each link's role/scope requirements. Four-kind controls include Plugins. Read-only previews support safe outside mouse/touch dismissal with focus restoration; preview/detail tags reuse exact submitted filters without private URL/storage persistence. Consumer handoff remains metadata-only: explicit safe HTTPS Open links and exact Copy/download controls do not install or invoke resources. Catalog Previous rereads cursors from a bounded ten-cursor history.

Field-linked authoring errors, explicit stale-versus-uncertain recovery and selected-label retention preserve exact command keys and ETags. Administration displays checked operational summaries, distinguishes a finished synchronization attempt from the last complete snapshot and links to indexing on Service. Header and result controls wrap at 320px with doubled text. Real assistive-technology use remains unqualified. Browser Plugin authoring passes the local memory-backed fixture. Independent PostgreSQL Catalogs also qualify UI/CLI Plugin transfers.

Jobs navigation requires tenant `job.read`, a readable tenant family and enforced `catalog.read`. Families use `source.read`, `source.read_evidence`, `publication.read` or `operations.reindex`; the API still checks exact targets. Direct routes explain missing family access without reading records. ANS Job cancel/retry requires evidence, source-management and federation-discovery permissions, the selected Job action and enforced `catalog.write`. Cancellation is independent of current enqueue eligibility; retry rechecks current source/policy/credentials. Private ANS results remain a separate explicit read.

Explore Browse, Resources, Reviews, Namespaces, Publishers, Members and Publication keep a recovery pager after failed continuation without retaining stale rows. First page, failed Reload and failed Refresh restart without a cursor while preserving filters or host. Normal Refresh rereads the selected page. Shared pagination wraps for enlarged text. Catalog search separately retains its displayed results and captured cursor for explicit retry after temporary continuation outages; rejected cursors require a fresh search.

`make ui-dev` builds assets and runs the Go host, without hot reload. Rebuild/restart after changes to keep its cached entry page aligned with hashed assets. Standalone Vite does not supply sessions or API forwarding.

Overview dashboard implements Content, Operations and Administration views at `/overview`. Independent API reads count authorized Resources, current releases, directories and distinct indexed identities. Fixed selection limits, explicit limited results, observation times and current scoped authorization apply before aggregation. Four compact kind tiles show exact counts. Review charts switch to daily outcomes. Jobs and membership states each use one table; links apply public kind/origin presets without storing private criteria.

Catalog remains the landing page and first discovery link. Review workload, pending-age bands, 30-day activity, decision medians and configured publication metadata are implemented. Approved review queues support the exact unreleased filter.

Operations adds authorized Source status and sync evidence, Job state counts and due time, plus existing index status. Source-only scope can open this tab without `catalog.read`. Enabled webhook summaries show endpoint states, retained production delivery states and up to five latest retained failures. Notification counters reuse the existing reconciliation read.

Administration adds protected membership, active definition and retained assignment counts, plus open-feedback categories and retained rating states. Groups use the current realm; membership totals cover the tenant. Matching state/type/category links preserve URL history without private text.

Shared controls, discovery/content, Operations, Administration, personal pages, navigation, Help and recovery use explicit intent, local icons and contextual state tones. Native kinds retain the Catalog palette. Source configuration, work outcomes and ANS checks remain distinct from health, trust and effective access. Active assignments do not prove effective permission.

## Configuration and secrets

Direct binaries, Make, Compose, Helm and the independent UI use explicit configuration and secret inputs. The [configuration reference](documentation/src/content/docs/reference/configuration.mdx#configuration-delivery-and-precedence) owns precedence, loading and capability activation. Credential replacement follows each input's loading and revocation contract. The [maintenance runbook](documentation/src/content/docs/operations/maintenance.mdx#rotate-credentials-and-trust) owns replacement and backup-key requirements. Optional external VSO delivery adds no application Vault SDK, dynamic database role or general hot reload.

Validation diagnostic limits are startup-configurable: `validation.maxFindings` defaults to 128 (1-512), and `validation.maxDiagnosticBytes` to 65536 JSON bytes (1024-262144), per representation and separately per aggregate revision/evidence array. Environment overrides are `CATALOG_VALIDATION_MAX_FINDINGS` and `CATALOG_VALIDATION_MAX_DIAGNOSTIC_BYTES`, forwarded by Make/Compose and exposed through Helm `configEnv`. Exhaustion retains a blocking error. Paths/messages/codes have fixed 1024/2048/128-byte bounds. Lower budgets do not rewrite or invalidate retained evidence or replay; explicit revalidation creates a new run and preserves immutable representation rules. PostgreSQL rejects individual historical findings arrays above 32 MiB before application transfer. Raw-document transport limits remain separate.

Backend request admission defaults to 2 MiB through `http.maxBodyBytes`/`CATALOG_HTTP_MAX_BODY_BYTES` (1 KiB-64 MiB). Helm and local launchers supply independent UI request/response budgets of 2 MiB/16 MiB, each at most 64 MiB; direct UI JSON requires both fields. Backend overrides do not change UI budgets. Job results use fixed compact schemas for snapshot references or indexing completion counts; observation groups have a fixed 100-contribution preview, with paged occurrence histories and separately authorized full reads. The [payload configuration section](documentation/src/content/docs/reference/configuration.mdx#payload-and-client-limits) covers examples, effective-setting inspection, rollouts and full-object routes; the chart's tested payload overlay changes no defaults.

Precedence: compiled defaults, optional `--config` JSON, explicit environment overrides, role-specific validation. Typed JSON rejects duplicate/unknown members, nulls, trailing values and invalid combinations. Settings are startup-only. Groups cover runtime/HTTP, database, identity, Job retry policy, worker, discovery/publication, federation and optional capabilities.

| Input | Purpose |
| --- | --- |
| `config/local.json` | Memory-backed development identities and loopback discovery. |
| `config/postgres.json` | Separate-role retained Compose development baseline. |
| `config/production.example.json` | Production-shaped template with illustrative values and optional capabilities off; deployment inputs still must be supplied. |
| `.env` / `CATALOG_ENV_FILE` | Optional Make-compatible workflow settings selected by Make. The binaries never load them. |
| `CATALOG_DATABASE_DSN` or `CATALOG_DATABASE_DSN_FILE` | Selected process/operator database identity; mutually exclusive direct/file inputs. |
| `CATALOG_CURSOR_SIGNING_KEY` or its `_FILE` form | Base64-encoded 32-byte deployment key; rotation invalidates outstanding cursors. |
| `CATALOG_WEBHOOK_ENCRYPTION_KEY` or its `_FILE` form | Separate 32-byte key needed only for enabled webhooks. |
| `CATALOG_DISCOVERY_HOSTS` | Explicit host mapping override; no implicit tenant/audience fallback. |
| `CATALOG_SEMANTIC_SEARCH` | Closed optional semantic configuration object; API and worker must agree. |
| `CATALOG_TRACING` | Closed optional OTLP configuration with endpoint, identity and bounded sampling/export controls. |
| `LOCAL_UI`, `LOCAL_MONITORING` | Select optional Compose UI/collector services. |
| `LOCAL_WEBHOOKS` | Supplies runtime webhook enablement unless explicitly overridden by `CATALOG_WEBHOOKS_ENABLED`. |
| `LOCAL_EMBEDDINGS` | Starts the model fixture; semantic schema/runtime enablement remains separate. |

The UI host loads its own strict JSON file; it does not load backend configuration or map arbitrary environment variables. Backend and UI `logLevel`/`logFormat` fields default to `info`/`text`; backend `CATALOG_LOG_LEVEL`/`CATALOG_LOG_FORMAT` override its JSON baseline. UI launchers render independent `CATALOG_UI_LOG_LEVEL`/`CATALOG_UI_LOG_FORMAT`. Helm uses `configEnv.CATALOG_LOG_LEVEL`/`CATALOG_LOG_FORMAT` for API, worker and migration settings, and `ui.config.logLevel`/`logFormat` for the UI. Backend changes roll API/worker Pods; UI-only changes roll UI Pods. Empty/null/unsupported values fail startup, including migration validation. Logging selection is independent of tracing and requires restart. The [configuration examples](documentation/src/content/docs/reference/configuration.mdx#logging-configuration) and [log-inspection guide](documentation/src/content/docs/operations/observability.mdx#inspect-application-logs) own operator procedures.

Only allowlisted non-empty runtime variables apply. Direct/file secret alternatives are mutually exclusive; mounted inputs are process/transport-scoped. Production requires PostgreSQL, API OIDC/tenancy and injected keys, rejecting memory/development profiles and unsafe HTTP public/CORS origins. PostgreSQL auto-migration and combined roles are rejected in every environment. Static validation does not prove live readiness.

The product deadline defaults to 10 seconds; socket timeouts are separate, with 20-second local shutdown. Bodies and feature-specific documents/archives/exports have separate bounds. Health, metrics, static contracts and worker operations are outside the product deadline. Its fixed cleanup window can only finish/roll back already-produced atomic work.

## Run the application locally

Run these commands from the repository root with GNU Make, Python 3.12-3.14, Git and curl. Checkout pins are Go 1.27.3 and, for UI/documentation source builds, Node.js 22.23.4/npm 10.9.9. Compose needs Docker with Compose v2 and BuildKit, but no host Go or Node.js. Follow the [README quickstart](README.md#quick-start) for the shortest persistent setup.

### Memory-backed API and worker

```sh
make run
```

The API is `http://127.0.0.1:8080`. In another terminal:

```sh
curl -fsS http://127.0.0.1:8080/readyz
curl -fsS -H 'Authorization: Bearer local-admin' \
  http://127.0.0.1:8080/api/v1/capabilities
```

`local-admin` is a synthetic development token. Stopping this process loses its in-memory state. No UI, database or documentation build is needed to start it.

`make demo-seed` loads the selected profile through governed API commands. The curated profile is the developer default; minimal/expanded profiles remain explicit selections.

Extending an original receipt requires `DEMO_EXTEND=true`. Receipt/progress recovery checks authoritative owned state, refuses drift/collisions and preserves unrelated eligible releases. Unchanged reruns do not repeat authoring commands. `LOCAL_DEMO_ARCHIVES=on` selects prepared fixtures for Compose. Startup never seeds; stop/down preserve data, receipts and TLS. Focused browser/model fixtures select `DEMO_SAMPLE_SET=minimal` explicitly.

`make smoke` mutates data through governance, replay, cross-tenant denial, independent review and discovery.

Neither is a health check or data-repair tool.

### Direct Docker

The [Docker guide](documentation/src/content/docs/operations/docker.mdx) runs separate API/worker images against operator-supplied PostgreSQL without Compose or Kubernetes. Source builds use the container Go compiler; explicitly select `CONTAINER_PLATFORM` to avoid host Go detection. Use selected `-e` or Docker-compatible `--env-file` inputs and separate mounted DSNs. Never pass Make's workflow `.env` wholesale. The [owner setup](documentation/src/content/docs/operations/database-roles.mdx#prepare-an-isolated-non-superuser-owner) requires forced-RLS access and extension-function grant authority. A Docker memory choice is also available in the local quickstart.

### Persistent Compose with UI

```sh
make compose-up LOCAL_UI=on
make compose-ps LOCAL_UI=on
```

Defaults are loopback API 8080, UI 4180 and PostgreSQL 55432; stop conflicting processes first. Compose builds/recreates selected services, retains its named database volume, migrates/provisions development identities as owner, then starts distinct API and worker identities. Worker operations stay unpublished.

The UI also serves the manual at `/docs/`. A new database starts without sample resources; load them explicitly or use the authoring UI.

Disposable packaged tests do not refresh a retained Compose deployment. Rerun `make compose-up` with its existing UI, monitoring, port and capability options after application changes. Then verify `/readyz`, build identity and actual browser login. Preserve named volumes and session keys. Read API `/version` and authenticated UI `/_build` after each rebuild. Matching record counts alone do not establish full content equality.

Add `LOCAL_MONITORING=on` for Prometheus/Grafana on loopback ports 19090/13000 with generated project-local credentials. Inspect assigned ports when overridden. `LOCAL_WEBHOOKS=off` disables Compose's default webhook enablement unless runtime configuration explicitly overrides it.

`LOCAL_EMBEDDINGS=on` starts the installed offline model fixture. Full semantic enablement additionally needs owner-applied schema, matching API/worker `CATALOG_SEMANTIC_SEARCH`, a permitted private route and readable model cache.

`make compose-stop LOCAL_UI=on` retains containers/volumes; `make compose-down LOCAL_UI=on` removes project containers/network but retains data. `make compose-reset LOCAL_UI=on` deletes data and requires exact scope authorization. Preserve all selected project, port and service settings. Preserve unrelated services and existing settings: `test -e .env || cp .env.example .env` creates workflow settings only when absent.

### Separate UI development

With an owned local backend already running:

```sh
make ui-install
make ui-dev
```

Open `http://127.0.0.1:4180` and select the appropriate development identity. This launcher does not seed or reset the backend. Stopping it leaves the backend alone.

### Local documentation

The optional `make docs-pdf` exports a sealed manual to `output/documentation-pdf/catalog-manual.pdf`. It includes all registered pages and generated API pages in sidebar order. Documentation-owned Playwright and its explicitly installed headless browser provide local rendering. Export preserves the site and previous successful PDF on failure. Normal builds/checks need no browser. See the [PDF procedure](documentation/src/content/docs/contributing/documentation.mdx#export-the-complete-manual-to-pdf) for installation, explicit tests, reports and format limits.

```sh
make docs-install
make docs-build
make docs-preview
```

Open `http://127.0.0.1:4321/`. Build/preview must use the same `DOCS_BASE` for subpaths. Preview serves existing output without rebuilding. The Astro/Starlight site is independent of the Catalog runtime.

### Binaries and remote reads

```sh
make build
./bin/catalogd version
./bin/catalogctl config validate --config config/local.json --role all
CATALOG_REMOTE_BASE_URL=http://127.0.0.1:8080 \
  CATALOG_REMOTE_TOKEN=local-reader CATALOG_REMOTE_TOKEN_FILE= \
  ./bin/catalogctl remote capabilities
```

The same inputs support `remote search --text orders --limit 10`, `remote record --id <release-uuid>` and `remote job --id <job-uuid>`. Record lookup needs a release UUID, not a Resource UUID. Production uses a protected identity-provider access token through a manual token file or an explicitly selected human login file.

## Deployment and operational behavior

The manual includes conditional deployment guides for [IBM Cloud Code Engine](documentation/src/content/docs/operations/ibm-code-engine.mdx), [Azure Container Apps](documentation/src/content/docs/operations/azure-container-apps.mdx) and [Google Cloud Run](documentation/src/content/docs/operations/google-cloud-run.mdx), with managed PostgreSQL services. Their exact database-role authority, ingress and container-isolation requirements need provider qualification; these are documented procedures, not tested cloud support.

The backend non-root `FROM scratch` image packages both static stripped commands for Linux AMD64/ARM64 using a digest-pinned Go toolchain and embedded fallback public roots. It has no shell/package manager; invoke `catalogctl` directly for operations. The UI image separately packages its host/assets.

Local image builds/inspection, licenses, SPDX SBOMs and chart packaging publish nothing. The application chart supports Kubernetes 1.34+, optional UI, role-specific probes, scoped configuration/Secrets, migration hooks and optional network policy; operators supply PostgreSQL. The separate monitoring chart adds no application readiness dependency, Operator, CRD or cluster-wide RBAC.

The optional [MCP certificate procedure](documentation/src/content/docs/operations/deployment.mdx#optional-mcp-ingress-certificates) uses external cert-manager and an ingress controller. Its owned Minikube scenario checks leaf/key replacement, stable CA trust, issuer failure/recovery and unchanged Pod identities. The Catalog gains no controller dependency or certificate authority.

API `/livez` reports process liveness without PostgreSQL or optional-provider calls. `/readyz` stays unavailable until startup completes, becomes unready during drain and fails when the required API database role cannot safely use a writer. The worker exposes its own operations-only startup/liveness/readiness/status/version/metrics/drain signals. Those routes do not belong on the product API Service or Ingress.

Shutdown stops new work/claims, marks readiness false and drains bounded work, leaving unfinished work recoverable. Optional provider/receiver/telemetry outages follow their degradation/retry/fallback policy; invalid enabled configuration fails startup.

SRE owns replicas, autoscaling, database HA/backups, writer routing, regions, access controls, retention, capacity and on-call response. No universal SLA/RPO/RTO is promised. Compose and Minikube-through-Helm qualify owned test environments; adopters still qualify their deployments.

## Observability and performance

The UI performance harness uses seven journeys, one warm-up and five recorded observations. It enforces 500 KiB entry JavaScript, 100 KiB entry CSS, 1,184 KiB total JavaScript and 200 KiB total CSS ceilings. Compact Search and explicit detail checks remain separate. Durations are report-only. `UI_PERFORMANCE_TIMEOUT_SCALE` defaults to 3 and accepts integers 1-10; it changes test patience only.

Backend and UI service logs use stdout with a startup-configurable threshold (`debug|info|warn|error`) and format (`text|json`), defaulting to INFO structured text. Command failures now project reviewed messages and fixed stage/code/type fields, never raw dependency chains. Service terminal failures use the effective format on stderr after configuration, with structured-text bootstrap fallback; CLI results remain stdout and remote-client errors retain their bounded JSON shape.

API, worker operations and UI listeners have explicit safe HTTP fallback logging. Owned process and Job panic boundaries cancel, drain and exit unsuccessfully; heartbeat cleanup and PostgreSQL rollback leave interrupted leases recoverable. Fatal runtime and dependency-goroutine failures retain explicit limits.

Common bounded process fields, UTC timestamps, explicit duration units, final-response severity, bounded warnings and trace-independent terminal Job diagnostics are implemented. UI lifecycle, bounded API/issuer failure/recovery and DEBUG session events are implemented. The host logs fixed route families and separately validated local/upstream request IDs without forwarding browser correlation to the API or issuer. Recovery describes an observed operation, not readiness or authorization.

Command entry points also project Go's process-wide standard logger, including HTTP/1 unsolicited-response bytes, into one fixed bounded warning; configured startup binds its level/format/stream without replacing `slog.Default`. Direct stderr and runtime crash limits remain explicit. File capture/rotation and audit authority remain unchanged.

One `internal/platform/processinfo` value owns backend startup time, monotonic elapsed time and the execution ID shared by status and enabled tracing. The independent UI host owns its own value. Metrics expose process startup time and uptime without execution-ID labels. See [current-process information](documentation/src/content/docs/operations/observability.mdx#current-process-startup-information).

Metrics are fixed and low cardinality. Never label them with tenants, resources, URLs, queries, publishers or trace IDs. The dashboard selects a Prometheus source, installation, API/worker roles and time range. Scrape-supplied deployment/role/instance labels are separate from application metric vocabulary. Missing series, failed snapshots and measured zero have different meanings. Shared database Job/outbox gauges must not be summed across worker replicas.

The [Service activity API](documentation/src/content/docs/operations/observability.mdx#read-api-request-statistics) defaults off. Enabled reads require current tenant-wide `access.manage` and enforced OAuth scope `tenant.admin`. The bounded response combines one process's traffic across tenants without tenant, user, resource or query identifiers. Collectors are not required. The Activity tab in Overview displays exact counters and bounded breakdowns. Refresh is manual unless the user enables visible-tab live reads. Failed, stale, reset and changed-execution samples cannot supply an interval rate.

Job and webhook retry-delay histograms observe successfully persisted waiting transitions, including hints and jitter, using retained completion and due timestamps. The dashboard exposes retry rates and mean scheduled delays. These process-local samples reset on restart and can miss a commit followed by process loss; they are not durable audit or actual queue-wait measurements. Supervisor retries retain safe delay warnings and fixed component state. UI retries remain manual and create no additional telemetry requests.

Use HTTP latency/error rates, pool acquisition/waits, SQL operation counts/duration, heap/GC, process/container resources, worker progress and bounded database/WAL/temp observations together. SQL spans measure statement execution without raw SQL or pool acquisition. Trace request paths through actual handlers/domain/repositories, then use exact-role query plans or focused profiles to test a bottleneck hypothesis.

Optional OTLP/HTTP tracing uses one endpoint-bound transport and process-owned identity, bounded queues/retries/deadlines and private attributes. Background attempts start separate traces linked to retained creating context; they are not one indefinitely open parent span. Sampling can omit a cause. Export submission counters do not prove backend storage. Trace context is not forwarded to federation, artifact or webhook destinations. Collector failure leaves readiness unchanged.

| Target | Measurement |
| --- | --- |
| `make bench` | Repeated focused Go runtime metric collection/exposition samples. |
| `make bench-search` | Real PostgreSQL batch/point hydration and allocations. |
| `make bench-search-plans` | Exact-role bounded Search/Explore SQL diagnostics; surrounding benchmark setup time is not query latency. |
| `make test-load-smoke` | Short owned HTTP Search workload. |
| `make test-load` | Local/indexed/public reads and bounded mixed mutations. |
| `make test-scale PERF_ITEMS=1000 PERF_SQL_PLANS=on` | Owned 1k scale scenario and optional post-measurement plans; supported larger sizes are 10k and 100k. |
| `make test-public-delivery` | Governed 1,001-entry public manifest; separate full 200 and conditional 304 phases with fixed reader/time bounds. |
| `make test-soak` | Fixed finite one-hour mixed workload and growth observations. |
| `make test-monitoring` | Collector/provisioning/query qualification and measured application work; browser rendering is separate. |

Run isolated measurement targets as single Make goals. Compare serial trials with matching data, duration, warm-up, concurrency, host/cache state, instrumentation and resource limits. Do not build, browse dashboards, run EXPLAIN or edit repository files during comparable timed work. Source-bound fixtures reject even documentation edits made during a trial. Build/seeding and plan probes are separate from measured HTTP phases.

Reports under ignored `coverage/performance/` distinguish workload failure, incomplete evidence and cleanup. Closed-loop achieved throughput is not offered-rate saturation capacity. Bucket data cannot yield exact sampled percentiles, and percentiles must not be averaged. PostgreSQL plan evidence must preserve actual role, RLS, parameters, snapshot and predicates; an owner plan can bypass the policy being diagnosed. `EXPLAIN ANALYZE` executes the query and has overhead. A sequential scan is not automatically a missing-index defect.

Public conditional requests omit response-body bytes but still read and validate the complete retained snapshot before the ETag check. Smaller responses alone do not establish lower CPU use or latency. Measure those effects before selecting a cache.

Scale qualification applies only to the selected corpus, size, source, privileges and workload. Four-kind 1k/10k trials do not qualify a four-kind 100k workload. Do not infer production capacity from local trials or combine different source revisions into a growth curve.

## Testing and quality gates

### Unit and functional boundaries

Unit tests exercise isolated decisions and components using in-process values, recorders, controlled transports, fakes and deterministic clocks. They do not start HTTP servers/listeners, invoke subprocess fixtures, access real databases or require a prebuilt Catalog/Compose environment. The Go test toolchain necessarily compiles tests; the restriction concerns application/process fixtures, not normal test compilation.

Functional tests exercise a real boundary or a composed application workflow. A memory-backed author-to-publication journey is functional even when driven through `httptest.ResponseRecorder` without a network listener. An isolated handler using controlled ports can be unit-level even if it uses a concrete domain helper. Mocked orchestration does not establish that a real deployment works.

Go tests stay beside source. Untagged files contain isolated tests and inert shared helpers. `functional` enables local listeners, subprocesses and composed workflows; `integration` enables PostgreSQL; `helm` and `minikube` mark their harness selections. Isolated Minikube helpers can be selected without creating a cluster. Python uses `test_unit_*.py` versus `test_functional_*.py`; Node tooling uses `.unit.test.mjs` versus `.functional.test.mjs`. Test selection includes initializers and cleanup, not only test bodies.

Required SDK selections and isolated Python/Node tooling owners require native test completion, including required files and discovered cases. Focused architecture, documentation, conformance and process gates require every declared Go test to pass. Compile-only and optional functional selections keep their separate policies. Bounded properties compare retained authorization, Job lease histories, lexical traversal and sample execution identities with independent expectations. The Skill archive fuzzer also validates generated inventories and unsafe neighbors.

### Core commands

| Target | What actually runs |
| --- | --- |
| `make test-unit` | Complete isolated Go selection, including UI Go host and isolated Minikube helpers; run alone. |
| `make coverage-unit` | Same Go unit selection plus application coverage/reporting; run alone instead of duplicating `test-unit` for the same measurement. |
| `make test-tooling-unit` | Isolated Python tooling tests. |
| `make test` / `make test-functional-local` | Units plus local functional tests; listeners/process fixtures allowed, no pre-existing deployed stack. |
| `make test-e2e` | Selected in-process memory-backed authoring-to-discovery journey. |
| `make test-race` / `make test-shuffle` | Concurrency/order checks on the broad local selection. |
| `make check` | Go/static/dead-code, shell, Markdown, JSON/JSON-LD/YAML, configuration, container/Compose source, API, Ruff and installed actionlint checks; no deployed stack. |
| `make verify` / `make test-all` | Broad local Go/race/coverage, profiles, conformance fixtures and binary checks; not every repository/environment test. |
| `make test-integration` | Real PostgreSQL 18 repositories, migrations, privileges, RLS, lifecycle and transaction assertions in an owned database. |
| `make test-process-postgres` | Real CLI/API/worker composition and restart behavior with PostgreSQL. |
| `make test-functional` | Owned packaged Compose profiles for default-off, webhooks and OIDC workflows. |
| `make test-ui` | UI static checks, frontend/Node/Go-host tests, isolated build and Chromium journeys. |
| `make ui-test-unit` | Frontend/component coverage, isolated Node helpers and isolated Go host tests. |
| `make test-ui-compose` / `make test-ui-oidc` | Packaged HTTPS/UI behavior and real human OIDC code/PKCE login. |
| `make deploy-check` | Container-source, chart/render/schema checks, with tools/schema sources but no live cluster. |
| `make test-minikube` | Owned Helm installation, persistence and runtime qualification. |
| `make test-minikube-peers` | Two independent Helm Catalogs and PostgreSQL stores, publication, federation, outage recovery and process replacement. |
| `make docs-check` / `make docs-site-check` | Source Markdown/contracts versus built reader-site/tooling/root-and-subpath checks. |

Additional Minikube targets cover topology, replicas, monitoring, UI, tracing and semantic search. `test-minikube-contract` exercises harness units/local functional fixtures without a cluster. Compilation or rendering alone does not qualify actual cluster behavior. `embeddings-check` is service-free; `test-embeddings`, `test-semantic` and `test-minikube-semantic` use explicitly installed real model fixtures at their respective boundaries. Backup/restore, PostgreSQL failover, federation-peer and tracing targets have separate owned scenarios.

For ordinary backend changes, iterate on focused tests, then run the complete isolated unit selection and `make check verify`, plus affected real-boundary gates. `check verify` can share prerequisites in one invocation. Unit-only and measurement targets must remain separate Make invocations because they deliberately sanitize environment selection. `CATALOG_ENV_FILE=/dev/null` disables local workflow settings for normal gates when desired.

Frontend changes use `make ui-install` and one-time `make ui-browser-install`, then `make test-ui`. Appearance or dependency changes also require comparison with reviewed `ui-visual` baselines. Dependency changes also need local `ui-deps-check` and affected packaging qualification. External registry audits such as `ui-deps-audit` upload dependency data and are maintainer-only; agents report them unrun. Do not regenerate screenshots to hide a regression. Reader-site changes require `docs-site-check`; repository-only prose/skills use source gates without manufacturing application regressions.

### Tooling and dead code

Contribution checks run existing lint/unit targets on pull requests and pushes to `main`. Changed inputs select UI, documentation and SDK checks. Root `pyproject.toml` and `uv.lock` changes select all conditional consumers of shared Python tooling. Failed fuzz measurements retain bounded synthetic counterexamples from every maintained fuzz package, with package selection checked against `make test-fuzz`. Successful SDK checks prepare local package receipts bound to their source and run. Ordinary hosted checks can upload selected reports, but only release qualification uploads SDK, VSIX, chart and image archives.

Pages prepares matching HTML and PDF output; delivery requires maintainer activation. Version-tag and manual release qualification tests exact backend/UI candidates through existing AMD64 API and browser journeys. Main merges build AMD64 images and charts without publication. ARM64 receives inspection and scans. Release downloads include all SDKs, Linux CLI archives, the tested VSIX, charts and checksums. GHCR delivery requires explicit activation and protected-environment approval. Draft creation and final publication remain maintainer-operated. Local verification never publishes or uploads. The [workflow guide](.github/WORKFLOWS.md) describes selections and qualification limits.

The documentation job in Source quality runs `make docs-check`, then `make docs-site-check`, with separate summary outcomes. Its `docs` prerequisite profile installs pinned Markdownlint, Go and the documentation Node/npm toolchain. The job needs no Docker socket or other source linters.

The Go selection in the SDK workflow runs `webhook-report-check`, which includes the SDK gate and independent publication-report receiver checks. `client-go-check` retains its own result. The selected receiver profile excludes SDK and functional coverage. The act wrapper has no SDK selection.

The functional workflow offers eight fixed choices, including logical backup/restore, two-Catalog federation and standalone webhook receiver tests. Peers use the UI prerequisite profile and all Chromium setup steps. Backup/restore and receiver choices use the non-UI profile. Report each native campaign and peer cursor failure separately, preserving actual backward-clock evidence. Peer cursor-expiry qualification still requires a stable-clock environment. No hosted execution or publication is performed.

Go quality gates include formatting, vet, Staticcheck, golangci-lint and architecture/source-selection checks. Other checks cover ShellCheck, Markdownlint, strict JSON/JSON-LD/YAML, container source, Compose models, OpenAPI and Ruff. API-tooling JavaScript has ESLint, and the reader-site gate includes ESLint and Astro diagnostics. UI static checks include TypeScript, generated API drift, ESLint and Prettier. Install the documented pinned prerequisites; `make validation-tools-install` owns locked API tooling. Use the pinned shared development environment installed by `make dev-tools-install`, preserving explicit tool overrides if PATH selects an incompatible Ruff.

`dead-code-report` and `dead-code-check` cover the Go application, including the UI host, and the selected test graph. The check runs through lint/check/verify. Production and test reachability are separate: tests can keep otherwise unreachable functions alive. Exact justified exceptions are reviewed; no wildcard baseline or fake caller should silence a finding. Coverage alone is not proof that code is dead. `ui-dead-code-report` and `ui-dead-code-check` use locally installed Knip 6.41.0 to inspect full application/test/tooling and production-browser graphs. The enforcing check runs once through `ui-check`/`test-ui`, outside backend and isolated-unit gates. Generated schema exports/types and the framework-consumed Playwright configuration export are the only reporting exceptions; guarded browser configuration is inspected without launching fixtures. Native report mode tolerates findings but fails on tool/configuration errors. CSS/layout behavior still needs browser checks.

### API, schemas and conformance

API changes require routing, OpenAPI, request/response types, authorization, positive/negative tests, examples and affected generated clients to agree. API tools perform lint, validation, local HTTP/event contracts, compatibility checks and representative generated Go-client workflows. The generated-client harness is not a published SDK or runtime generator dependency.

`make profiles` verifies pinned artifact identities. `make conformance-fixture` independently schema-validates the service ARD fixture and all four checksummed upstream examples, passes upstream examples through actual ingestion, tests resolution/profile boundaries and runs the unchanged official CLI over all five fixtures. It also exercises the static reference corpus and service handlers. `verify` includes it; isolated units do not launch that CLI.

The fixture, live and packaged functional Make paths require the selected Python interpreter to have `jsonschema` 4.26.0 and a valid pinned Draft 2020-12 schema. Install the shared dev group with `make dev-tools-install` and use its interpreter; checks never install dependencies. The maintained wrapper runs the unchanged official CLI and requires manifest and per-entry schema success evidence, rejecting skipped validation. Registry mode checks protocol shapes rather than manifest schemas. Optional JSON Schema `format` assertions are not enabled by the upstream validator; independent Go and protocol checks retain their own scope.

`conformance-live` tests manifest, publisher and Registry modes against an explicitly selected already-published disposable instance; it does not start or seed it. The packaged default-off functional fixture creates its own publication and runs all three modes. The official runner permits some optional endpoint omissions and has limited per-result coverage, so owned positive/negative/pagination/authorization/federation assertions remain necessary.

Independent pinned `hf-discover` client tests use a disposable local Catalog through `interoperability-tools-install` and `test-interoperability`. The tests cover full-entry Search, native-library structured filters and complete public snapshot continuation, selected public current/predecessor navigation and local HTTP characterization of sparse-result rejection, private-manifest token omission, cross-origin Registry token forwarding and source rewriting. Reports separate assertion status from capability disposition. The separate `test-interoperability-hf` target exercises owned HTTPS HF servers with exact private trust, real API/worker snapshots, bounded live discovery and recovery. Neither target establishes hosted HF behavior or every standards requirement. External peer execution remains explicitly scoped; ordinary helper tests do not run HF.

The [generated API harness](test/apigenerated/README.md) covers bounded Resource/Version creation, exact tenant-bound readback and PostgreSQL state comparisons. Real-tool calibration checks remain separate from the original operation pilot and Namespace histories. Run the owning gate with installed tools and require complete reports and cleanup.

Isolated OIDC token fuzzing checks that malformed encoding stops before verification and membership lookup. Unrelated mutation survivors remain separate from this encoding boundary.

## Coverage requirements and verification boundaries

Coverage measures weighted statement execution over a specified source inventory. It does not establish branch completeness, API-operation completeness or assertion quality. Combine compatible block hits, never suite percentages or duplicate counts. Go, frontend, instrumented-browser and API-operation reports have separate denominators.

| Gate | Output and maintained requirement |
| --- | --- |
| `make coverage-unit`, alone | `coverage/unit.out`; 90% overall and for each package in the Makefile's [CRITICAL_COVERAGE_PACKAGES](Makefile). |
| `make coverage-check coverage-core-check` | Broad local `coverage/default.out` at 90%; filtered `coverage/core.out` at 93% overall and 90% for each critical package. Core is not a unit-only suite. |
| `make coverage-postgres-check` | Real repository `coverage/postgres.out` at 95%. |
| `make coverage-process-postgres-profile` | Real CLI/API/worker process supplement, with its own inventory. |
| `make coverage-full`, alone | Fresh compatible unit/local/PostgreSQL/process profiles and deduplicated `coverage/full.out`; 95% combined and 80% for each executable application file. |

Profile targets can rerun tests. Inspect saved profiles only after verifying their source/run identity. Failed gates can leave an older successful report; do not attribute that report to the failed run. Frontend `ui-coverage` and selected `functional-coverage` API-operation reports remain separate.

Packaged, database, cluster, interoperability, model and performance qualifications retain their recorded source and scope. Later passes do not explain earlier failures. Follow [testing and coverage accounting](documentation/src/content/docs/contributing/testing.md#coverage-accounting) for current selections and reports.

[Local workflow testing](test/ci/README.md#local-workflow-commands) provides pinned act setup, eight fixed commands, a non-root runner and isolated source/configuration. Report native job results separately from helper checks, runtime controls and cleanup. Hosted execution and external uploads remain maintainer-only.

Restore fixtures compare exact Plugin and private saved-item reads with the original database and archive server stopped. Valid Plugin inspection does not require an archive blob. The publication-report fixture checks worker delivery, exact activation/unpublish receipts, restricted permissions and a separate receiver database.

## Repository ownership and change rules

| Location | Ownership |
| --- | --- |
| `cmd/catalogd`, `cmd/catalogctl` | Process lifecycle and explicit remote/operator commands. |
| `internal/{catalog,identity,feedback}` | Core governed model/search, authority and private feedback. |
| `internal/{adapter,standards,jsondoc,draftfile,a2apb}` | Format adapters, pinned semantics, duplicate-safe JSON, reusable draft codecs and generated A2A binding. |
| `internal/{publication,federation}` | Immutable discovery and external-source lifecycle/provenance. |
| `internal/{jobs,housekeeping,webhook}` | Durable asynchronous work, bounded cleanup and optional notifications. |
| `internal/{audit,events}` | Attributed evidence, bounded export and public event vocabulary. |
| `internal/platform/database` | Repositories, SQL/RLS, manifests, migrations, guards and integration tests. |
| `internal/platform/{httpserver,operations}` | Product HTTP transport and separate worker operations surface. |
| `internal/platform/{authn,fetcher,embeddings,webhooksender}` | Identity verification and scoped external transports. |
| `internal/platform/{clock,id,buildinfo,processinfo,config,observability,tracing,runtime}` | Injectable primitives, strict startup inputs, telemetry and composition. |
| `internal/integration`, `test` | Composed memory workflows and explicit functional/deployment/performance/tooling harnesses. |
| `catalog-ui` | React client, fixed-upstream host, own image and tests. |
| `api`, `schemas`, `migrations` | Embedded contracts/examples, immutable upstream profiles and forward SQL history. |
| `deployments`, root Compose/Containerfile | Runtime packaging, application/monitoring charts and local fixture inputs. |
| `documentation` | Static reader manual, architecture/ADRs and site tooling. |
| `todo`, optional local `continue.md` | Active/deferred/closed tracking and a gitignored implementation handoff when present. |
| `.agents/skills` | Repository development workflows; distinct from resource Skills that the product catalogs. |

Braces group sibling directories. Keep SQL/HTTP at their owning edges; interfaces need a real domain caller and implementation. Reuse fixtures, Make targets and narrow helpers without speculative frameworks/providers or parallel coverage systems.

Changes to HTTP behavior require the contract and affected consumers together. Configuration changes require typed parsing/tests, samples, environment allowlist, diagnostics redaction and affected launcher/chart wiring. Database changes require real PostgreSQL, RLS/role/migration coverage and repair/rollback boundaries. Standards upgrades require a new immutable profile directory, retained license/commit, checksums and compatibility/conformance tests. Do not hand-edit generated A2A bindings or generated UI API types.

Backend compatibility Containerfiles and ignore files must retain their required matching copies. Preserve static non-root packaging, scoped Secrets and role-specific readiness. Tests must assert behavior, exact state/evidence and rejection boundaries; do not lower thresholds, weaken guards, omit original assertions or broaden dead-code exceptions simply to pass.

Use `rg` for repository searches and review the working tree before edits. Preserve unrelated changes and retained local services/data. Stage or commit only when asked; use signed conventional commits. Git remote changes, pushes, hosted jobs, external publication and project-data uploads are maintainer-only. Agents must never perform those actions, including external signatures or attestations. Use `gh` only for read-only GitHub operations.

External research, source inspection and dependency, tool, model and image downloads remain allowed. These permissions do not authorize uploads of project data.

Project prose is plain ASCII, with one-line paragraphs and direct current facts. Reader guides/architecture/ADRs belong in the manual; dated measurements belong in repository-only records; plans and closure belong in their trackers. Keep this consolidated context aligned without turning it into a work-session diary or another competing backlog.

ADRs describe current architecture choices and are updated in place. Preserve their identifiers and routes; keep supersession chains, amendment journals and obsolete implementation narratives out of them. Git retains edit history. Released API and stored-data compatibility obligations remain separate from ADR maintenance.

## Development skills

Twenty-two repository skills under `.agents/skills/<name>/SKILL.md` route tasks to existing authorities/tools. Instructions do not enforce a sandbox or authorize unrelated actions.

| Skill | Use |
| --- | --- |
| `change-planning` | Scoped investigation, cross-package design and accepted decisions. |
| `api-contract-changes` | Coordinated HTTP contract, implementation and consumer updates. |
| `governed-publication` | Authoring, exact validation/review subjects, releases and immutable discovery activation/withdrawal. |
| `database-changes` | SQL, migrations, grants, locking and real persistence verification. |
| `recovery-and-repair` | Supported restore, interrupted-command and derived-state repair. |
| `search-and-relevance` | Search semantics, indexing, ranking and held-out quality evaluation. |
| `durable-workflows` | Jobs, outbox, leases, retries, cancellation and worker recovery. |
| `federation-development` | Source onboarding, synchronization, provenance and bounded live/referral work. |
| `security-review` | Substantive trust-boundary and adversarial review. |
| `standards-conformance` | Pinned format interpretation, updates and conformance. |
| `interoperability-qualification` | Selected independent local consumers/peers, native controls, exact governed handoffs and capability dispositions. |
| `ui-development` | Browser interactions and Go-host session/forwarding behavior. |
| `build-and-deployment` | Make/configuration/images/Compose and local API/UI/docs sessions. |
| `configuration-and-credentials` | Runtime precedence/validation, capability activation, secret delivery and per-consumer rotation. |
| `helm-and-minikube` | Chart updates, lint/render/schema/package checks and owned cluster scenarios. |
| `dependency-maintenance` | Reviewed toolchain/library/image pins and license/security qualification. |
| `debugging-and-performance` | General failure diagnosis and metrics/diagnostics/tracing maintenance. |
| `performance-testing-and-optimization` | Measured load/scale/soak, dashboard/trace/code investigation and justified query/index improvements. |
| `documentation-maintenance` | Manual/site, references, tracking and handoff consistency. |
| `unit-testing` | Valuable isolated tests and unit coverage without real infrastructure. |
| `functional-testing` | Real HTTP/process/database/browser/deployment assertions. |
| `post-code-verification` | Appropriate lint/test/coverage/dead-code gates and evidence reporting. |

Performance owns comparative measurements/index justification; database changes owns migration correctness; search owns retrieval quality; debugging owns general diagnosis/instrumentation. Relevance and latency are separate results.

The separate client artifact `examples/client-skills/catalog-discovery` instructs assistants to use read-only remote CLI commands and treat metadata as untrusted. It supplies no MCP service, installer or new authority.

Optional rich coverage reporters use a separate locked Go tool module and installed metadata checks. A shared Trivy version pin serves source, UI lockfile and image checks. `container-scan` and `ui-container-scan` inspect exact local image IDs/platforms, require a fresh advisory database and complete inventories. High/Critical findings or incomplete work fail the gate. UI scans include production npm evidence retained in the image. Static ARM64 scanning is distinct from target execution.

## Remaining work and explicit limitations

Optional Vault/VSO delivery uses standard Kubernetes Secrets. Native Kubernetes Secrets remain the default; no Vault runtime SDK or chart dependency is added. The [Vault tutorial](documentation/src/content/docs/operations/vault.md) covers setup, activation, rotation, provider recovery and scoped removal. Provider image findings require separate review.

`make test-postgres-pitr` tests an owned PostgreSQL 18 historical restore, exact named-point replay and missing-required-WAL rejection. It checks tenant isolation, audit, grants, command replay and resumed work. The preparation-only target remains available. Production backup infrastructure and recovery objectives remain operator-owned.

Nonempty Directory transfer testing requires an unmodified compatible ARD peer interface. The selected Directory v1.8.0 exposes an empty manifest, incompatible List/Search messages, invalid payloads and native import failures. These peer limitations are not unfinished Catalog fixes. Nonempty transfer remains unrun until that prerequisite exists. The retained lab and accepted ARD profile remain separate inputs.

The CI installer rejects local hosted-only use before downloads or filesystem writes. Local act execution does not qualify hosted events, permissions or OIDC.

Backward wall-clock steps can put a fresh cursor beyond its permitted future-expiry window. Cursor-expiry and lock qualification still require a selected stable-clock environment. Individual refusals without guard evidence remain unclassified. Do not change clock guards or host settings to make a test pass.

| Limit | Current boundary |
| --- | --- |
| Dependency advisories | Native MCP Scanner qualification remains blocked by its pinned LiteLLM closure. Fresh scans determine other dependency and image findings; historical clean scans do not establish current security. |
| Local wall-clock instability | Backward host/database time steps have been observed; stable-clock expiry and lock qualification remain incomplete. Feature-specific passes do not establish stable clocks. Preserve guards, deadlines and attribution limits. |
| Native editor qualification | Windows/macOS support is implemented but has no native qualification. Linux desktop and same-machine Remote SSH/browser selections do not qualify other placements, OS associations, production keychains or screen readers. Some earlier credential, bookmark and CDP failures lack confirmed causes. |
| Web UI qualification | Some lazy-screen, API startup and Plugin module-fetch failures remain unclassified. Separate access and audit failures retain their own scope; passing repeats do not establish their causes. |
| Topology qualification | Earlier inspection deadlines and one UI-login transport failure remain unexplained. Independent Catalogs, shared-database replicas and physical replication require separate tests. |
| Browser and packaged refusals | Group/assignment chronology, audit/readiness, CLI callbacks and reduced-scope startup failures have distinct observations. Do not infer a shared cause from later passing repeats. |
| Peer and recovery failures | Initial peer HTTP 400 responses, webhook missing-delivery/rotation failures, two recovery interruptions and cancellation-503 attribution remain unresolved where exact predicates are absent. |
| External interoperability | Pinned local clients and peers qualify selected exchanges only. Pi/pi-ard retains authentication, media-type and formatted-provenance limits. ContextForge prompt forwarding has a private-CA limitation. Native errors and zero-import outcomes are not successful nonempty federation. No hosted compatibility is implied. |
| Assurance | Automated accessibility does not replace manual zoom or assistive-technology checks. The dark preview has bounded painted-contrast checks for one axe overlap ambiguity. Image metadata checks do not qualify deleted-layer secret detection. Coverage, mutation tests, formal proofs, scanners and API-operation observations measure different scopes. |

Select cleanup deliberately, stop affected producers and follow the [cleanup guide](documentation/src/content/docs/contributing/build-and-release.md#clean-local-builds-and-dependencies). Retained-checkout deletion is not selected for this review. The broad `make verify` aggregate includes disposable cleanup fixtures; it does not clean the retained checkout.

Managed icons/screenshots storage and software assessment are unimplemented and fail startup when enabled. General crawling, agent execution, runtime provisioning, public individual comments, feedback notifications, remote SDK publication, legal-hold/privacy automation and external compliance assurance are unavailable. Enabled explicit ANS registration assessments do not establish broad software trust or rights.

The semantic tier is implemented with measured relevance limits. Core discovery works without it. Obsolete development state is rejected without automatic backfill or deletion; architecture decisions alone do not establish implemented behavior.

## Source authority and maintenance

This guide supplies orientation and planning context. Exact contracts remain owned by these sources and must change with implementation:

| Concern | Owning source |
| --- | --- |
| Repository-wide agent and contribution rules | `AGENTS.md`, `CONTRIBUTING.md`. |
| Runnable commands and prerequisites | `Makefile`, `README.md`, `DEVELOPING.md`, owning package/harness README. |
| Current implemented boundary | This overview, checked against owning code, contracts and guides. |
| Optional local work tracking | Gitignored `todo/` contains active, deferred and closed tasks when present. Gitignored `continue.md` is an optional local handoff. Neither is required for builds, tests or documentation. |
| Product HTTP contract | `api/openapi/control-v1.yaml` and embedded examples. |
| Configuration | Typed config code, `config/`, `.env.example` and the manual configuration reference. |
| Architecture and durable decisions | `documentation/src/content/docs/architecture/`, including the ADR index. |
| Standards bytes and identity | `schemas/PROFILES.md`, `schemas/checksums.txt`, immutable `schemas/upstream/`. |
| Test rules and dated results | Manual `contributing/testing.md`, harness READMEs, optional private notes and raw reports under ignored `output/`. |
| Procedures for deployment/recovery/security | Manual `operations/` runbooks. |
| Reader site | `documentation/content-map.json`, authored manual pages and site tooling. |
| Reusable development workflows | `.agents/skills/`. |

The independent reader site includes only registered content, not repository records, trackers or this root guide. There is no separate `docs/` tree. Keep generated projections, research checkouts, dependencies, coverage profiles, logs and raw evidence out of commits.

When refreshing this context, check runnable behavior, defaults, command existence, profile versions and remaining work against owning code and guides. Preserve operational limits and unresolved failures. Distinguish source/lint checks from actual database, browser, model or deployment qualification.

## Sample and client workflows

Sample loading is an explicit governed workflow. Select curated, minimal or expanded data from the maintained YAML. Frozen inputs, exact receipts, authoritative readback and immutable archive paths protect retained evidence. Unchanged reruns issue no authoring commands; evidence reads and membership provisioning can still audit. The [sample guide](samples/README.md) owns preparation, launch and recovery.

Development archives admit only the fixed demo tenant and prepared exact paths/digests/sizes through the configured TLS origin and allowlist. Production and Helm cannot enable this exception. Ordinary federation and fetching keep separate authority. `make demo-up` checks, prepares, launches, waits and loads; plain Compose startup never seeds.

The [Rust report](examples/rust-catalog-report/README.md), [LangGraph assistant](examples/langgraph/README.md) and [VS Code client](extensions/vscode/README.md) are independent consumers. Their tools and runtime dependencies stay outside application images. The extension supports explicit native MCP use, verified Skill/Plugin management and bounded A2A text conversations with separate target consent and credentials. Browsing alone executes nothing. Each client guide describes supported workflows and qualification limits.

VS Code copied references reject duplicate keys through the shared bounded decoder before exact identity admission. Package credential writes use the existing execution-host lock and index generation checks. Contention and stale indexes fail without a write; uncertain storage failures require explicit offline Forget All recovery with other windows closed. Separate remote hosts do not share that filesystem coordination.

`samples/catalog.yaml` and its bounded relative native assets supply the development inventory and standalone CLI inputs. Shared validation freezes those bytes before preview, archive preparation or governed loading. `demo-archives-prepare` retains immutable digest-addressed ZIP objects and an atomic exact allowlist. Development consumers receive only public trust and allowlist inputs; prepared paths cannot change or disappear. The [sample guide](samples/README.md) owns profiles, receipts, recovery and archive setup.

`LOCAL_DEMO_RUNTIMES=on` selects persistent local Hello World and Everything peers with private TLS, separate synthetic MCP credentials and configurable ports. `demo-runtimes-test` checks selected running peers; `test-demo-runtimes` owns a disposable lifecycle. The separate `test-demo-catalog` gate exercises YAML, PostgreSQL, browser handoff, native calls, append-only extension and outage recovery. Sample loading and browsing do not invoke hosted destinations.

The independent [Go publication report](examples/publication-report/README.md) stores HTTPS notifications and results in its own PostgreSQL database. It reads current snapshot summaries through the Go SDK and reconciles periodically. The [TypeScript ARD List client](examples/typescript-ard-list/README.md) uses an installed SDK package and reports complete, limited or failed traversal. Both examples remain outside backend and SDK runtime dependencies.

The [Python service reader](examples/tutorials/local-oidc/service-read.py) uses an installed SDK package, explicit credentials and private trust. It rejects malformed subject inputs before API calls and uses fixed diagnostics. The [native-import starter](examples/native-import/README.md) supplies an Agent Card and standalone Skill document for offline preview, governed creation and receipt-based recovery.

The [ContextForge guide](documentation/src/content/docs/guides/contextforge.mdx) provides isolated Compose setup, Catalog MCP proxying and CLI-selected virtual-server handoff. The maintained fixture covers both supported protocols, six tools, tenant isolation, revocation, credential replacement and recovery. Governed echo and static resource reads remain explicit client actions. Runtime outages do not change sealed Catalog metadata. Native prompt forwarding retains the peer's private-CA limitation.

The trusted ContextForge image has no release-age wait. Explicit installation verifies the latest stable release and pins its digest. Report execution, cleanup, image advisories and unresolved failures separately.

## Catalog consumer behavior

Control Browse, Search and detail show release-linked Publisher and Listing namespace names. Bounded license text remains separate from native license declarations. Selected local/indexed/live origins and an optional local namespace restrict access before counts, ranking and pagination. Excluded live origins make no peer requests. Live-only Search bypasses durable and embedding queries. Same-tab detail return retains bounded browser state that clears when identity changes, then reads current results again; reload starts fresh.

Listing history uses bounded continuation and release-ID ordering. It shows immutable references, disposition and current-pointer facts without per-row private-evidence reads. Release history, current Catalog eligibility and public discovery snapshots remain separate. The [consumer guide](documentation/src/content/docs/guides/catalog-ui.mdx) owns interaction details.

Public ARD license/topic filters use reviewed immutable Listing fields. Exact declarations and literal Dublin Core subjects remain distinct from arbitrary native values. Control Search applies license and category filters across selected origins; Explore remains local-only. All selected categories must match. Live filtering reports missing required evidence as partial rather than silently accepting it. Existing active snapshots retain their bytes; new generation and activation expose changed publication metadata. The [standards reference](documentation/src/content/docs/reference/standards.mdx#license-topic-and-metadata-filters) owns exact limits and matching rules.

Nine supported native declaration paths expose selected A2A skills, streaming, interface and input/output metadata plus MCP transport/version metadata. Exact media-type and scalar checks preserve missing, false and null distinctions. Separate predicates can match different array members; matches do not establish runtime compatibility. URL-only representations supply no native declarations. See [native filters and facets](documentation/src/content/docs/reference/standards.mdx#native-card-filters-and-facets).

### Artwork and ratings

Immutable ListingRevisions retain reviewed artwork descriptors. Source artwork defaults off; source opt-in and tenant-private overrides remain separate from local governance. Exact configured origins, raster validation and bounded transient delivery control image requests. Empty origins deny retrieval. Five-minute live receipts bind the current caller, authorization, source, policy and override; public snapshots omit private locators and consuming overrides. Managed uploads and byte caches remain unavailable; [artwork configuration](documentation/src/content/docs/reference/configuration.mdx#listing-artwork) owns defaults and limits.

Ratings allow one authenticated human vote per tenant and exact local revision or foreign source/version target. Peer summaries require deployment/audience opt-in and the human's consent; local and source-reported opinions remain separate. Authorized adoption evidence can show an imported local counterpart without claiming popularity or measured installations. Ratings do not replace private feedback or establish trust. [ADR-0023](documentation/src/content/docs/architecture/decisions/0023-human-resource-ratings.md) and the [ratings guide](documentation/src/content/docs/guides/ratings.mdx) own these rules.

### Current-process and build identity

Authenticated API `/api/v1/service-information`, UI `/_status` and worker-private `/status` report their own process observations. Build identity, execution identity, UTC startup/observation times and monotonic uptime remain separate facts. Manual refresh can reach another replica; startup is not upgrade time or a live-node inventory. The Catalog does not store persistent startup history. The [observability guide](documentation/src/content/docs/operations/observability.mdx#current-process-startup-information) owns response interpretation.

Root `VERSION` owns the intended release base and OpenAPI `info.version`. The explicit version tool updates OpenAPI, SDK and extension metadata without changing dependencies, committing, tagging or publishing. Managed builds capture bounded version, full commit and source state once. Git-less inputs and explicit fixtures cannot claim clean provenance. Build identity is not uptime, deployment time, image digest or attestation. The [build and version policy](documentation/src/content/docs/contributing/build-and-release.md#application-version-and-release-checks) owns derived files, tools and refresh requirements.

## Live tenant operational settings

Private `/api/v1/tenant-settings` and Administration > Settings expose three optional overrides: automatic source admission, its interval and new default-budget Job attempts. Omission inherits each consuming process's startup defaults; false pauses automatic admission. Exact ETags bind saved state and API defaults, including before the first save. Saves and reset require current tenant-wide permissions, the enforced OAuth ceiling, idempotency and a reason, and commit audit evidence. Existing work, explicit Job limits and retry-child budgets remain unchanged. The [configuration reference](documentation/src/content/docs/reference/configuration.mdx#live-tenant-settings) owns limits and delivery.

## Optional LangGraph client example

The [LangGraph guide](examples/langgraph/README.md) publishes under Build integrations. Its independent Python environment uses the SDK, two read-only tools and an operator-owned local Ollama model. Credentials and namespace stay outside model arguments; Search is local-only and recommendations bind exact records. The example adds no resource execution, installation, mutation, persistent checkpoint or hosted tracing. Framework packages and model weights remain outside application images and SDK runtime requirements. Isolated, scripted HTTP and actual-model checks have separate Make gates and do not establish universal model quality or Go coverage.

The [MCP Scanner example](examples/mcp-scanner/README.md) supplies capture validation, a locked YARA runner and private reports outside the Catalog runtime. Its signed receiver supports durable acceptance, explicit local HTTPS mappings, bounded MCP metadata reads and reconciliation. Receiver tests use a scanner executable double.

The Compose harness and manual guide are implemented, but native scanner and full worker-delivery qualification await supported dependency fixes. The example supplies no Catalog assessment capability.
