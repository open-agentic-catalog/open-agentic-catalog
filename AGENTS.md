# AGENTS.md

Repository-wide guidance for coding agents working on the Open Agentic Catalog Service. This file is self-contained and is the root instruction source for this repository.

Run commands from the repository root. Resolve literal instruction paths relative to that root, including paths in `.agents/skills/`. Markdown links remain relative to their source file.

## Start with the service documentation

Start with [documentation/README.md](documentation/README.md) and follow only the documents relevant to the task. This repository contains its own product, architecture, implementation-status, backlog, API, configuration, and operations context; do not assume another repository or product documentation is authoritative for it.

Use these sources according to their authority:

| Need | Authority |
| --- | --- |
| Load the application context in one file | `CONTEXT.md` for a consolidated overview; exact contracts remain owned by the sources below |
| Resume the current implementation sequence | Optional local `continue.md` when present, then the authoritative sources below |
| Run, build, and evaluate the service | `README.md`, `DEVELOPING.md` and `Makefile` |
| Understand runnable behavior and known gaps | `CONTEXT.md`, checked against owning code, contracts and guides |
| Understand architecture and invariants | `DESIGN.md` for orientation, then the owning chapter under `documentation/src/content/docs/architecture/` |
| Plan remaining implementation work | Optional local `todo/todo.md`; otherwise use the current request |
| Revisit deferred proposals and their triggers | Optional local `todo/deferred.md` |
| Review completed or otherwise closed tasks | Optional local `todo/done.md` |
| Review accepted architecture decisions | `documentation/src/content/docs/architecture/decisions/index.md` and the applicable ADR |
| Change the implemented HTTP API | `api/openapi/control-v1.yaml` |
| Change API conventions or examples | `documentation/src/content/docs/reference/api-conventions.md` and `api/README.md` |
| Change runtime configuration | `documentation/src/content/docs/reference/configuration.mdx` and `config/*.json` |
| Change deployment or operations | The applicable file under `documentation/src/content/docs/operations/` |
| Understand standards and interoperability boundaries | `documentation/src/content/docs/reference/standards.mdx` |
| Change a standards adapter | `schemas/PROFILES.md` and the pinned upstream profile |
| Report or handle a suspected vulnerability | `SECURITY.md` and `documentation/src/content/docs/operations/incident-response.md` |
| Follow contribution and verification rules | `CONTRIBUTING.md` |
| Maintain the reader manual and site tooling | `documentation/src/content/docs/contributing/documentation.mdx` and `documentation/content-map.json` |

Keep target behavior and implemented behavior distinct. Architecture constraints may describe conditional capabilities; never use them alone to claim that a feature works. Update `CONTEXT.md` whenever implementation materially changes the current boundary.

Local task tracking and reviewed engineering notes may be excluded from a public source copy. Local notes are optional context, not authority over maintained code, contracts or documentation. Builds, tests and documentation must work without them. Keep raw logs and generated reports under ignored `output/`. Keep private workspace locations out of repository documentation and skills. The local `continue.md` handoff is gitignored; use it when present, without making it a prerequisite.

## What this service is

This is a standalone Go Catalog service for governed authoring, versioning, publication, discovery, and federation of Agents, MCP Servers, Skills, and Plugins. It is a modular monolith with independently runnable API and worker roles plus operator commands:

- `catalogd api`: authenticated control API, Catalog reads, publication, and discovery endpoints.
- `catalogd worker`: durable background jobs and optional webhook dispatch.
- `catalogd all`: combined in-memory development process only.
- `catalogctl`: explicit human OIDC login for remote commands and online import/export, native and reusable JSON draft preview/apply/resume and export, explicit remote API reads and Tenant Administrator semantic reindex requests, profile verification, diagnostics, migrations, focused repair, bootstrap, and smoke workflows.

The optional `catalog-ui` Go host serves the React client, handles development identity selection or OIDC code/PKCE login, and forwards requests to one configured API. It has a separate image and no database access. Browser permissions supplement, never replace, backend authorization.

Development identity shortcuts are passwordless. Use the [optional local Keycloak setup](documentation/src/content/docs/operations/oidc-keycloak.md#run-the-interactive-local-tutorial) for username/password sign-in. Production uses configured OIDC and must disable development identities.

Authentication resolves compact principals; `/api/v1/me` supplies bounded current permission hints and `/api/v1/access/self` pages assignment provenance. The any-scope permission union is navigation only; actions use tenant or exact-namespace permission plus any OAuth ceiling. Built-in names are templates, not UI authorization checks.

Discovery is metadata. It never implies approval, trust, installation, deployment, invocation, runtime authorization, or permission to execute a resource. Agent execution, chat, A2A task handling, invocation of discovered MCP tools, Skill execution, runtime provisioning, gateway enforcement, and automatic installation are outside this service.

The service must remain independently deployable. Do not add a runtime, build, configuration, documentation, or release dependency on another product's UI, control plane, repository, backlog, or development environment. Product UIs, IDE extensions, CLIs, automation, and platforms are clients of the service-owned API and discovery contracts.

Root `VERSION` owns the intended application release base. OpenAPI `info.version` follows this base. Managed builds use `test/packaging/versioning.py` once for bounded version, full commit and source state; runtime code never queries Git. Follow the [build policy](documentation/src/content/docs/contributing/build-and-release.md#application-version-and-release-checks) for explicit bumps, overrides and maintainer-created signed release tags. Version commands do not commit, tag or publish, and build identity is not startup history or artifact attestation.

## Operational simplicity and least surprise

Design the Catalog as a conventional, independently deployable, containerized application, following applicable twelve-factor principles. Prefer established protocols, standard libraries and existing infrastructure capabilities over custom mechanisms. The [design philosophy](documentation/src/content/docs/architecture/design.mdx#operational-simplicity-and-least-surprise) explains these rules and their relationship to the domain invariants below.

- Keep configuration explicit, validated and documented; inject secrets externally through supported environment or file inputs. Emit service logs to stdout and CLI diagnostics to stderr; deployment tooling owns collection and rotation.
- Leave database HA, replication, election, promotion, writer routing and backup orchestration to deployment infrastructure. Catalog owns connection recovery, accurate health signals and safe application behavior; it must not become an infrastructure controller.
- Recover from transient dependency failures where safe. Reuse existing driver/pool behavior first. Application-owned retries use bounded exponential backoff with random jitter where appropriate, cancellation and documented, configurable operational budgets with sensible defaults. Avoid stacked retry loops, retries of permanent failures and retrying beyond the caller's deadline. Do not impose one retry policy on connection pools, health probes, request handling and durable Jobs.
- Never blindly repeat mutations with an uncertain outcome. Preserve transaction, idempotency and lease guarantees; use the supported repair boundary when automatic recovery cannot establish safety.
- Make startup, shutdown, readiness and recovery predictable. Keep durable authority outside process-local state; temporary dependency loss must not become a liveness restart loop, while invalid startup configuration and fatal process failures remain explicit failures.
- Add a custom mechanism or dependency only for a demonstrated requirement that existing components cannot satisfy. Document its purpose, defaults, failure modes and operator responsibilities; prefer the smallest complete change over speculative abstractions or configuration knobs.

## Fixed architecture boundaries

The rules in this section are accepted target boundaries. Check `CONTEXT.md` before assuming each one is implemented. Under ADR-0017 the Catalog has no released predecessor; do not preserve obsolete development formats. Complete current-format replacements, schema cleanup and tests together; no compatibility inventory/backfill product or old-runtime overlap is required. Never reset stored data without exact scope and explicit authorization.

- PostgreSQL 18 with `pgcrypto` is the production system of record. PostgreSQL application roles never run migrations automatically. A dedicated Catalog schema owner applies migrations and reconciles the versioned exact API and worker privilege manifests under one advisory lock.
- Core exact, lexical, filtered, faceted, List, browse, and federated discovery works without embeddings. Optional hybrid retrieval uses `pgvector` and one configured embedding endpoint/model per deployment. Workers index bounded metadata asynchronously; fresh queries fall back to lexical search during indexing or provider unavailability. Relevance errors are measured limitations, not an implementation veto; authorization, explicit filters, index integrity and safe continuation remain strict requirements. Model changes may use lexical search during reindexing, without simultaneous model endpoints. See [application context](CONTEXT.md#search-and-optional-hybrid-retrieval) and the [hybrid-search runbook](documentation/src/content/docs/operations/hybrid-search.mdx) for tested behavior and limits.
- Agent and deployable MCP runtimes belong in OCI registries. Source and complete Skill trees belong in Git or immutable OCI releases.
- External listing artwork uses sealed descriptors or opted-in native hints, exact operator media origins and credential-free hardened delivery. Bytes remain transient; artwork does not grant trust or network authority. Managed-media storage remains unimplemented and fails startup when enabled. A future S3-compatible provider is limited to icons and screenshots, not a second metadata authority.
- The backend validates signed JWT access tokens; it is not an OAuth authorization server and issues no production tokens. The optional UI handles login against an operator-configured issuer, not token issuance.
- Runtime secrets are injected by the deployment. Source records retain opaque credential bindings; the Catalog is not a general secret manager and does not require Vault.
- Federation sources, scopes, indexed/live/referral participation, audiences, and query disclosure are administrator-managed. Ordinary clients cannot submit arbitrary source URLs or select a topology.
- Every outbound request to a Catalog-controlled or resource-declared destination, including federation sources, external artifacts, remote contexts, and webhooks, uses the hardened fetch boundary. OIDC provider discovery and key retrieval use the dedicated verifier against an operator-configured issuer; they must not become tenant-selected federation destinations.
- PostgreSQL transactional outbox records provide the reliable post-commit notification boundary. Signed HTTPS webhooks are optional, at-least-once, unordered notifications. Do not add Kafka, Event Streams, another broker, or a dormant broker abstraction without a new accepted requirement.
- The backend packages both commands in one non-root `FROM scratch` OCI image for `linux/amd64` and `linux/arm64`, built from a digest-pinned Go toolchain. Both backend binaries are static and stripped and embed the maintained Go fallback public root set. The optional UI has its own Containerfile, static Go host, browser assets and image. The application Helm chart supports Kubernetes 1.34+ and can deploy the UI with `ui.enabled=true`; the backend does not depend on it. All workloads receive configuration, private trust, credentials and state through scoped deployment inputs. Compose and Minikube-through-Helm are local test environments. Operators supply compatible PostgreSQL and other infrastructure. Keep runtime behavior independent of Kubernetes and do not add a platform matrix or adapters speculatively.
- The application Helm chart stays independent of the separately optional monitoring companion chart. The companion packages shared Prometheus/Grafana configuration and upstream image references; it has no application readiness dependency, Operator, CRD or cluster-wide RBAC. Its collector identity and external storage/credentials remain separate from Catalog workloads and PostgreSQL.
- Optional chart-managed PgBouncer uses one private `Recreate` replica and session pooling for API/worker with separate exact login identities. Runtime admission rejects advertised non-session mode; missing status does not qualify an older pooler. Enabling provisions the component, never rewrites DSN Secrets. Preserve verified TLS on both hops, separate backend budgets and direct schema-owner operations; exclude the owner from pooler authentication. Database infrastructure supplies a stable writer endpoint. Follow the connection-pooling runbook and selected `test-pgbouncer`/`test-minikube-pgbouncer` gates for changes; do not globally redirect integration-owner DSNs.
- SRE owns replicas, autoscaling, PostgreSQL HA and backup, regions, availability objectives, recovery objectives, monitoring integration, and on-call operations. The service owns safe limits, health signals, graceful shutdown, observable degradation, and restore-safe behavior.
- ADR-0024 defines the `ans-registration-v1` verifier, bounded acquisition and explicit private assessments for ANS V1/V2 public evidence. Preserve separate signature, inclusion, target and current-status results; accepted authority comes from operator public keys and exact publisher domains. Default-off startup configuration loads local public keys without network calls. Source-bound Jobs fence exact entries, revisions, policies, credentials and leases; completion and required audit commit atomically. Private reads require current permissions and audit before disclosure. Use the separately selected `make test-trust-verification` target for real local ANS qualification; require result and cleanup success. Ordinary discovery makes no receipt requests; broad trust and rights remain `not_assessed`.
- Managed-artifact security, general advanced trust and governance, external penetration/privacy/legal assurance, compliance-specific retention, legal hold, privacy-request automation, and contractual deletion guarantees are deferred. Report unavailable or unassessed capability honestly rather than simulating completion.

Use the migration and runtime privilege code for current format versions; do not maintain another version inventory here. Preserve required audit attribution, command binding and federation provenance, rejecting obsolete formats without conversion or deletion.

Runtime/Helm webhooks default off; local Compose enables them unless `LOCAL_WEBHOOKS=off`. Keycloak is an optional local identity provider and test tool, not a backend dependency. The [security runbook](documentation/src/content/docs/operations/security.md) owns the maintained threat/control/test mapping.

ADR-0022 defines the implemented finite scoped PostgreSQL authorization model. Current action/scope admission, private collection/search preselection, fenced replay, compact authentication and bounded role/group/scoped-assignment browser management use retained policy. Ordinary grants confer exactly their selected template; only explicit development/bootstrap/conversion bundles add companions. Follow the accepted transaction, replay and current-format cutover decisions; never use actor role projections or browser capability hints as authority. The authorization qualification record owns tested replica, cost, recovery and cutover limits.

## Package ownership

Keep domain behavior in its owning package and infrastructure details at the edge:

| Path | Responsibility |
| --- | --- |
| `cmd/catalogd/` | Process roles, supervision, signal handling, and shutdown orchestration. |
| `cmd/catalogctl/` | Native and reusable JSON draft preview/apply/resume and export, remote API reads, Tenant Administrator semantic reindex requests and operator, migration, diagnostics, profile, and smoke commands. API-client commands use explicit client inputs, not runtime configuration or database authority. |
| `catalog-ui/` | Optional React client, fixed-upstream Go authentication host, independent image and browser/unit/tooling tests; no SQL or backend domain implementation. |
| `extensions/vscode/` | Optional SDK-based desktop consumer. The native host owns connections and credentials; no Catalog administration or backend domain implementation. |
| `internal/a2apb/` | Generated binding for the pinned A2A protocol. |
| `internal/audit/` | Versioned audit records, execution attribution, action validation, deterministic bounded export encoding, and verifier contracts. |
| `internal/artwork/` | Strict artwork descriptors, bounded static raster validation and transient delivery through a narrow fetch port; no network policy, storage or hosting authority. |
| `internal/catalog/` | Canonical domain types, lifecycle rules, search contracts, repository ports, errors, identifiers, and digests. |
| `internal/overview/` | Shared bounded summary values, scopes and the fixed selection limit; no policy or persistence authority. |
| `internal/events/` | Public event vocabulary, notification references, CloudEvents projection, and schema validation. |
| `internal/ratings/` | Human version-specific opinions, private consent, aggregate arithmetic, moderation and optional publisher summaries; no trust or installation authority. |
| `internal/feedback/` | Private human submissions, administrator responses, immutable item context and safe text clearing. |
| `internal/personal/` | Human-owned private saved resource references and named search criteria, finite quotas and audited commands; no authority over saved targets or private record cache. |
| `internal/identity/` | Actors, roles, namespace grants, and authorization context. |
| `internal/adapter/` | Native Agent, MCP Server, Skill, and Plugin representation adapters. |
| `internal/safeerr/` | Reviewed diagnostic messages, fixed operation stages and the fatal owned-panic classification; no logging or infrastructure dependencies. |
| `internal/jsondoc/` | Duplicate-key-safe JSON validation used at untrusted document boundaries. |
| `internal/draftfile/` | Bounded reusable draft file contract, strict codec and Listing projection; no file I/O, network access, native admission or command authority. |
| `internal/standards/` | Pinned ARD, AI Catalog, JSON Schema, and JSON-LD behavior. |
| `internal/federation/` | Source inspection, onboarding, synchronization, observations, and live discovery. |
| `internal/publication/` | Candidate generation, validation, activation, rollback, unpublish, and public snapshots. |
| `internal/tenantsettings/` | Typed tenant operational overrides, inheritance, exact validators and audited memory policy; deployment configuration stays platform-owned. |
| `internal/jobs/` | Durable work, leases, retry, cancellation, and terminal states. |
| `internal/housekeeping/` | Fair bounded worker cadence and class-level cleanup contracts; PostgreSQL owns exact eligibility and locking. |
| `internal/webhook/` | Optional endpoint lifecycle, encryption, signing, delivery, retry, and evidence. |
| `internal/platform/authn/` | Development identities and the current OIDC token-verification boundary. |
| `internal/platform/clock/` and `internal/platform/id/` | Injectable time and UUIDv7 generation. |
| `internal/platform/processinfo/` | One in-memory execution identity and monotonic startup/uptime state, shared by status, metrics and enabled backend traces; no history or node registry. |
| `internal/platform/buildinfo/` | Bounded embedded version, commit and source-state projection; no runtime Git access. |
| `internal/platform/catalogmcp/` | Static read-only Control MCP tools, stdio and finite stateless HTTP, closed arguments, bounded metadata projections and protocol lifecycle; no execution or ARD wrapper. |
| `internal/platform/cliauth/` | Explicit human CLI OIDC/PKCE login, private origin/identity-bound credentials and serialized refresh; no token issuance, runtime configuration or implicit cache. |
| `internal/platform/controlclient/` | Shared origin-bound CLI HTTP transport, credential files, safe envelopes and selected Search inputs; no database or arbitrary metadata destinations. |
| `internal/platform/config/` | Strict typed configuration and environment overrides. |
| `internal/platform/embeddings/` | Optional fixed-endpoint inference client, query/batch limits, vector validation and provider-state reporting; indexing and ranking belong to the Catalog domain. |
| `internal/platform/database/` | PostgreSQL repositories, exact runtime privilege manifests, role validation, RLS context, migrations, lifecycle enforcement, and integration boundaries. |
| `internal/platform/httpserver/` | Routing, middleware, strict HTTP decoding, authorization handoff, and response encoding. |
| `internal/platform/fetcher/` | Shared outbound URL, DNS, redirect, response-size, and SSRF controls. |
| `internal/platform/idempotency/` | Command reservation, atomic completion, replay, repair-lock, and request-binding contracts. |
| `internal/platform/observability/` | Provider-neutral low-cardinality metrics, fixed lifecycle and component state, and bounded diagnostics projections. |
| `internal/platform/trustevidence/` | Credential-free ANS evidence acquisition through the hardened fetcher, with startup authority policies and fixed request/byte bounds; no Jobs or persistence. |
| `internal/platform/tracing/` | Optional process-owned OpenTelemetry SDK, bounded OTLP/HTTP export and private cross-process causality behind domain tracing ports. Collector transport uses the endpoint-bound adapter in `internal/platform/fetcher/`. |
| `internal/platform/operations/` | Worker-only health, status, version, and metrics HTTP surface with no product routes. |
| `internal/platform/runtime/` | Composition root that wires domain services to platform adapters. |
| `internal/platform/webhooksender/` | Safe-fetcher-backed HTTPS webhook transport. |
| `internal/integration/` | In-process end-to-end workflows across the composed service. |
| `api/` | Embedded OpenAPI, event schema, and reviewed API examples. |
| `migrations/` | Immutable embedded forward-only PostgreSQL migrations. |
| `schemas/` | Service-owned schemas and immutable pinned upstream profile bundles. |
| `deployments/` | Backend container compatibility copy, local PostgreSQL bootstrap, application/monitoring Helm charts and shared monitoring assets. |
| `samples/` | Single development sample YAML, exact native assets and same-source CLI embedding; data never grants runtime or network authority. |
| `sdk/go/`, `sdk/python/`, `sdk/typescript/` and `sdk/rust/` | Independent generated clients and bounded transports; no application-module runtime dependency, implicit credentials or resource execution. |
| `test/` | Explicit functional, deployment, performance and tooling harnesses; generated Go-client compatibility and API validation have separate locked tools. |

Domain packages must not import HTTP handlers, SQL implementation details, storage SDKs, OIDC clients, or deployment-specific providers. Add a narrow port in the owning domain only when there is a real caller and implementation. Avoid speculative interfaces and provider abstractions.

## Domain and persistence invariants

- Every tenant-owned persistent record and tenant-scoped lookup includes the tenant boundary. Preserve PostgreSQL RLS and add negative cross-tenant tests for new paths. The worker's bounded global Job and outbox candidate selection plus class-specific housekeeping transactions are the deliberate cross-tenant operational boundary; every follow-up read, write, relation check, and delete includes the explicit tenant predicate even though the worker role uses `BYPASSRLS`.
- API connections authenticate directly as the exact-manifest least-privilege `LOGIN NOINHERIT NOBYPASSRLS` role. Workers authenticate directly as the distinct exact-manifest `LOGIN NOINHERIT BYPASSRLS` role for cross-tenant leases. Neither may belong to another database role or be reached by assuming a role from a more privileged session. Migrations use a dedicated Catalog schema-owning identity.
- An in-scope domain or focused operator transition, its audit evidence, and every independently classified public or internal outbox record commit atomically. Audit-only actions do not manufacture outbox rows. Never perform an external call while holding an interactive domain transaction open.
- Stable Resources, immutable VersionRevisions, immutable ListingRevisions, review submissions, releases, source observations, and publication snapshots retain their identity and history. Do not rewrite sealed or published evidence.
- A publication snapshot's immutable row identity, host, profiles, generation, ETag, creation time, state, and activation time must agree with its retained document. Workers may insert immutable candidates for the implemented generation Job, but only the API role may activate, retire, restore, or unpublish them.
- Mutating HTTP commands require idempotency. Revision-changing commands require optimistic concurrency through `If-Match`.
- Replays must not bypass current authentication, authorization, tenant isolation, or disclosure rules.
- Use service clocks and `internal/platform/id` rather than ad hoc time or identifier generation where deterministic testing matters.
- Derived search indexes, embeddings, caches, and delivery queues are rebuildable. They never become authoritative state.
- A partial or failed source synchronization cannot withdraw the last complete source snapshot.
- Discovery or ingestion of an external record never silently adopts it as a locally governed resource.
- A hard collection or work bound is enforced at the database selection boundary with exact lookup or `limit + 1`, not by hydrating an entire tenant collection and truncating it in Go. Publication construction, activation, and rollback eligibility use the same bounded projection and work ceiling.

## API rules

- `api/openapi/control-v1.yaml` is authoritative for every implemented API-listener route, method, parameter, body, response, authentication rule, and public endpoint. The separate worker-only listener is defined by `internal/platform/operations/`, its contract tests, and `documentation/src/content/docs/operations/observability.mdx`; do not expose those operations through the product API or treat their absence from Control OpenAPI as a missing product route.
- An HTTP change is incomplete until routing, OpenAPI, authorization, request and response types, positive and negative contract tests, and relevant examples or documentation agree.
- Keep `/api/v1` path-major compatibility for supported releases. ADR-0017 permits deliberate pre-release contract cleanup without aliases for older development clients; update routing, OpenAPI, examples and positive/negative tests together. Once released, do not change existing field meaning, defaults, authorization, ordering or error semantics in place.
- Mutations use `Idempotency-Key`; changes to an existing revision use `If-Match` and return an updated ETag.
- Control errors use RFC 9457 problem details with a stable service code and `X-Request-ID`. ARD discovery retains its pinned error envelope. Enabled `/mcp` uses safe JSON-RPC errors and tool refusals, with HTTP OAuth challenges where required. Public errors must not expose internal causes, SQL, secrets, another tenant, or private object existence.
- Request objects are closed unless a standards payload or extension map deliberately allows unknown fields. Reject duplicate keys, unknown or wrongly cased control fields, explicit nulls in non-nullable fields, malformed UTF-8 or unpaired surrogate escapes, trailing values, invalid media types, and over-limit bodies. Measure schema string lengths in Unicode code points; keep transport and retained-document byte budgets separate.
- Preserve raw standards input and forward-compatible unknown fields only at the explicit evidence or extension boundary. Do not relax the service's own control objects.
- Public discovery reads only active immutable audience-scoped snapshots. It must never reach drafts, mutable records, source credentials, internal diagnostics, or tenant-private evidence.
- `api/embed.go` embeds the checked-in contract and examples directly. Update the source assets; do not create a separate generated copy.

## Database and migration rules

- PostgreSQL 18 is the supported major. Keep the exact-major startup check and required-extension validation aligned with documentation and tests.
- Operator setup must provide schema ownership, access across forced RLS and extension-function grant authority. A non-superuser owner or trusted extension installation alone does not establish those permissions; follow the database-role guide and qualify the selected owner.
- Never edit a numbered migration after it may have been applied. Add the next forward-only migration and preserve checksum verification.
- Use expand-and-contract changes for rolling compatibility with actual supported releases. There is no supported predecessor or mixed-version window. Use forward-only cleanup without a speculative backfill system. Large backfills for a future accepted upgrade belong in resumable jobs, not one schema migration transaction.
- Database changes require repository tests, real PostgreSQL integration coverage, RLS denial tests, migration plan/apply coverage, and a documented repair or rollback boundary.
- Every added or changed table, column, sequence, or function must update the API and worker privilege manifests deliberately. Keep `SELECT`, `INSERT`, and `UPDATE` column-explicit; do not grant table-wide runtime DML or any grant option. Runtime roles must not participate on either side of a database-role membership. New objects default to unavailable, and runtime startup rejects both missing and excess effective privileges plus direct `PUBLIC` grants on Catalog objects and application-visible extension functions that would otherwise be hidden by an equivalent role grant.
- Treat installed extensions as a separate runtime privilege boundary. Reconciliation and startup inspection deny every `pgcrypto` function and every extension function in an application-visible schema except exact overloads deliberately required by a reviewed manifest; an optional extension being installed does not make it executable by either runtime role.
- Preserve database-enforced immutable history, exact same-tenant parent constraints, legal current-pointer changes, terminal states, monotonic operational fields, and the documented lifecycle transitions. Do not weaken a guard to accommodate an invalid repository write.
- Every bounded representation is authoritative through PostgreSQL inline_document or a non-null document_blob_id, except inspected Skill packages and the Plugin-specific retention design in ADR-0019. Plugins retain bounded exact manifest/component documents and inventory bound to an external archive digest; archive bytes remain transient. This is not an arbitrary URL-only exception. Migration 000022 removes predecessor acceptance. API and worker roles have no representation update grant; never reconstruct or rewrite retained evidence.
- `catalogctl migrate apply` validates both existing runtime roles before migration, holds one session advisory lock through all forward migrations and exact grant reconciliation, and reports both manifests. If reconciliation fails after a migration commits, keep runtimes stopped, correct the reported role or grant problem, and rerun it.
- Do not weaken `FORCE ROW LEVEL SECURITY`, role verification, tenant context setup, tenant-inclusive keys, or lease ownership checks to make a test pass.
- Do not add application-startup migration authority. `catalogctl migrate plan` and `catalogctl migrate apply` remain separate operator actions. Normal release deployments use the operator-side `make helm-deploy` or local `make compose-up` maintenance workflow; raw Helm hooks do not drain old runtimes. Preserve explicit target scope, complete old-Pod drain, owner-only migration, target-value replica recovery and no automatic SQL/application rollback. See the [maintenance guide](documentation/src/content/docs/operations/maintenance.mdx#coordinated-release-deployment).

## Standards and federation rules

- Vendored upstream standards under `schemas/upstream/` are immutable build inputs bound by `schemas/checksums.txt` and documented in `schemas/PROFILES.md`.
- Never silently update a profile from an upstream `main` branch. Add a new versioned profile directory, retain its license and immutable commit, update checksums and compatibility notes, then run profile and conformance tests.
- Reference implementations are interoperability evidence, not normative authority. Record their exact source revision and declared ARD generation, distinguish hosted claims from inspectable code, and reduce useful current or legacy wire cases to local static fixtures.

  External implementation execution requires an explicitly selected test scope. The maintained HF client qualification runs pinned, unmodified Search/navigation against a disposable local Catalog plus owned loopback controls; use `make interoperability-tools-install` and `make test-interoperability` following `test/interoperability/README.md`. Separate assertion status from capability disposition: valid sparse-result rejection, private navigation failures and cross-origin token forwarding remain client limitations. Its tools, synthetic credentials, bounded execution and cleanup stay isolated.

  The separately selected `make test-interoperability-hf` qualifies the same pinned HF implementation as owned HTTPS peers, including API/worker snapshots, bounded referral traffic and outage recovery. It preserves native conformance failures separately from passing characterization. Both targets remain local; neither authorizes hosted Registry calls, other peer implementations or runtime dependencies.
- Separately selected `make mcp-catalog-tools-install` installs the locked official Python MCP client. `make test-interoperability-mcp-catalog` qualifies stdio and HTTP with both selected protocols, current API authority, metadata-only results and owned cleanup. Follow the [Catalog guide](test/interoperability/mcp/README.md#catalog-control-stdio-qualification). Require all seventeen legacy groups plus the complete four-way PostgreSQL replica/restart matrix. Keep the separate Inspector/Everything tooling and handoff unchanged. Neither Catalog transport implements an ARD wrapper or resource execution.
- Separately selected `make test-interoperability-agntcy AGNTCY_LAB_DIR=/path/to/lab` qualifies the pinned existing local Directory lab and a temporary memory Catalog following `test/interoperability/agntcy/README.md`. Preserve explicit lab selection, fixed local endpoints, the retained deterministic seed, invocation-owned client caches and cleanup. This A2A artifact/ARD characterization does not provision a cluster or establish nonempty ARD federation.
- Separately selected `make agntcy-tools-install` acquires pinned test-only Directory inputs. `make test-minikube-agntcy` deploys them beside the current Helm API/worker and separate PostgreSQL in an invocation-owned Calico profile. Follow the [fresh deployment guide](test/interoperability/agntcy/README.md#fresh-owned-deployment). Preserve private trust, local schemas, actual build identities, enforcing network controls and failure/interruption cleanup. The target qualifies the six-record/five-projection native corpus, paging/filter behavior, four resource-kind dispositions, the shared four MVP groups and PostgreSQL source/Job/snapshot bindings. Native A2A/MCP/Skill file imports use static enrichment and bounded private inputs. Check summaries, diagnostics, artifacts and CID files; CLI exit zero alone is insufficient. Preserve expected native conformance and import failures as peer incompatibilities. Empty-manifest synchronization and incompatible collection links do not establish nonempty ARD federation. Keep the retained-lab target and its exact admission checks unchanged.
- Separately selected `make gateway-tools-install` and `make test-interoperability-gateway` acquire exact test-only inputs and qualify native Gateway auth/discovery and Catalog enrollment/lifecycle beside a fresh Helm Catalog/PostgreSQL in owned Calico Minikube. Follow `test/interoperability/gateway/README.md`. Preserve native nginx/auth, scoped keys, private TLS, model-free lexical results, original schema/checker failures and isolated cleanup. Require exact members/bytes, owner SQL bindings, same-Job outage recovery and immutable source history through reviewed replacement. Native reverse import refuses the private destination before HTTP despite reporting successful zero imports; do not claim bidirectional ingestion. MongoDB SSPL and the peer image closure remain separate test-tool license boundaries.
- `internal/a2apb/a2a.pb.go` is generated from the pinned A2A protocol. Do not hand-edit it. Regeneration requires the documented toolchain, upstream commit evidence, and compatibility tests.
- Structural validity, semantic compatibility, publisher trust, local review, rights, relevance, and invocation authorization are separate results. Never collapse them into one trusted or approved flag.
- ARD top-level members other than `entries` are transport-defined. Generic hosted manifests use the current ARD model; `specVersion` or `host` alone never selects predecessor AI Catalog. Under ARD section 5.1, `/.well-known/ai-catalog.json` and `rel="ai-catalog"` locate equivalent entry sources; neither alone selects the legacy payload profile. Preserve explicit media types, Link `type`, nested-entry declarations and retained profiles, reject contradictory explicit signals, and require administrative reinspection before an approved source changes its resolved endpoint, profile, or Registry interface.
- MCP Registry `server.json` and the experimental MCP Server Card are different formats. Never relabel one as the other.
- Agent Skill packages are metadata and files to validate, never code to execute. Archive handling must remain bounded against traversal, links, file count, depth, decompression, and expansion attacks.
- Agent Plugins 1.0.0 is a portable directory contract for a root `plugin.json`, Agent Skills, MCP configuration, and client extensions. It is not an ARD profile, Agent format, archive format, installer, or runtime. Native `plugin` resources use the pinned 1.0.0 profile and ADR-0019 HTTPS archive inspection, immutable evidence and existing governance. Unsupported future types remain unknown observations; no installer, component execution or archive hosting is implemented.
- Do not describe `application/agent-plugins+json` as a standardized ARD type. The active specifications do not currently define that mapping, and the AI Catalog draft's Plugin archive labels do not establish Agent Plugins 1.0.0 conformance.
- Preserve source provenance and partial-failure diagnostics. Live federation must remain bounded by approved topology, origin policy, fan-out, hops, deadlines, result limits, and loop detection.
- Use the `gh` CLI exclusively for every GitHub operation, including retrieving upstream profile content or metadata.

## Security rules

- Treat every request, token claim, standards document, URL, redirect, DNS result, archive, remote context, external response, and webhook destination as untrusted.
- Do not create a raw `http.Client` for external content. Extend or call `internal/platform/fetcher` so URL normalization, DNS/IP policy, redirect checks, timeouts, byte limits, and response handling remain consistent.
- Fixed-destination clients are distinct from resource fetching: Catalog CLI API commands use their origin-bound transport, and `catalog-ui/internal/web` uses separate fixed API/issuer transports. Preserve their origin, redirect, byte, deadline and private-trust checks; never reuse them to fetch a URL supplied by Catalog metadata or the browser.
- Never log or return bearer tokens, cookies, database DSNs, webhook secrets, source credentials, private keys, raw private documents, complete user queries, or unrestricted external response bodies.
- Treat dependency error chains and recovered panic values as private input, including command stderr, listener fallbacks and standard-library client output. Preserve the command-owned standard-log safety adapter; never forward its raw text into `slog`. JSON formatting is not redaction.

  Preserve internal error classification and project safe code-owned diagnostics at the output boundary; DEBUG cannot relax privacy. Keep logs, sampled traces, metrics, required transactional audit and independently classified outbox notifications separate. Use `internal/safeerr` only with reviewed code-owned messages/stages; never mark a dependency string safe. Owned panic guards must preserve cancellation, cleanup and fatal classification.

  Backend/UI startup logging uses strict `logLevel`/`logFormat`, defaulting to INFO structured text; preserve configured handlers, stderr operator diagnostics and independent UI configuration. ADR-0021 and the observability runbook define fixed event/field policies and bounded repeat warnings. UI diagnostics admit local/upstream request IDs separately and never forward browser correlation to API/issuer transports. Keep audit and notification authority independent of logging configuration.
- Keep secrets out of JSON configuration, `.env.example`, images, Helm values, fixtures, logs, diagnostics, and support output. Configuration stores only non-secret values and opaque references.
- Keep authentication, Catalog role authorization, namespace authorization, and tenant isolation separate. A broad OAuth scope or upstream group never grants a Catalog role by itself.
- Optional capability failure must follow its declared fail-closed, retry, or degraded behavior. Disabled capabilities must require no provider, credential, key, endpoint, or readiness dependency.
- Add adversarial tests whenever changing parsers, URL handling, redirects, archives, identity claims, authorization, RLS, public hosts, signatures, cursors, idempotency, or outbound delivery.

## Configuration rules

- Configuration parsing is strict, typed, duplicate-key safe, unknown-field rejecting, and cross-field validated before startup.
- A new or changed setting requires implementation tests, every affected sample profile, `documentation/src/content/docs/reference/configuration.mdx`, `.env.example` when relevant, diagnostics redaction, and Helm or Compose wiring where applicable.
- The three live tenant overrides use `internal/tenantsettings`, its private API and retained policy under ADR-0025. Keep deployment inputs separate. Saves/reset require current authority, exact validators and atomic audit; resolve policy at admission without rewriting admitted Jobs. Do not add a generic reload controller or persist credentials in settings.
- Runtime environment overrides remain an explicit allowlist. Do not automatically map arbitrary environment variables into configuration.
- Make targets may load the selected Make-compatible `.env` file. `catalogd` and `catalogctl` never load an env file themselves; direct processes and deployments receive explicit environment variables from their launcher.
- Production readiness checks only dependencies required by the process role and enabled fail-closed capabilities. An optional webhook receiver or a semantic provider with a documented lexical fallback is not a liveness dependency.
- Do not add an external product dependency for configuration. The service must continue to run standalone.

## Health, readiness, and operations

- API `/livez` is process liveness only and must not contact PostgreSQL or optional providers.
- API `/readyz` must be unavailable or not ready until startup completes, becomes not ready during drain, and fails when the API cannot safely use its required PostgreSQL role. Every newly opened PostgreSQL API or worker runtime connection and each readiness check require a read/write-capable session; intentionally read-only operator inspection uses a separate connection contract and is not a runtime readiness path.
- Worker-capable roles expose a separate operations-only surface for startup, process liveness, required PostgreSQL readiness, fixed component degradation, metrics, and drain state without product APIs. Keep its port off the product Service and Ingress.
- Optional downstream receiver or provider outages are component degradation when durable retry or documented fallback is safe. They must not cause a liveness restart loop.
- The Helm API and worker Deployments have role-specific startup, liveness, and readiness probes. Compose keeps the worker listener unpublished and probes it through the bounded loopback-only `catalogctl probe` command.
- Preserve graceful shutdown: stop new HTTP work or worker claims, become unready, drain within the configured bound, and leave unfinished work recoverable through leases and idempotency.
- Expose safe low-cardinality metrics. Never use tenant IDs, resource IDs, URLs, query text, publisher names, or trace IDs as metric labels.
- Do not hardcode or document a universal SLA, RPO, RTO, region topology, backup schedule, or capacity promise. Record measured test evidence and leave deployment objectives to SRE.

## Repository distribution boundary

Development and agent verification run locally. The project repository is `https://github.com/open-agentic-catalog/open-agentic-catalog`. The maintainer alone handles external publication and data uploads. Agents must never publish or upload project data to external services, including private services.

- Do not create hosted repositories or configure Git remotes for this project. Do not push branches or tags.
- Do not publish source, packages, images, charts, documentation or releases to external services.
- Do not dispatch hosted CI jobs or submit signatures or attestations to registries or transparency logs.
- Do not upload source, logs, artifacts, dependency inventories, SBOMs, scan reports or project telemetry to external services.
- Inspect commands for automatic uploads or telemetry before running them. Disable external submissions, or leave the command unrun if it has no local-only mode. Report the resulting verification limit. The maintainer performs any required external submission.
- External research, source inspection, searches and dependency, tool, model and image downloads are allowed. These permissions do not authorize uploads of project data.
- Local builds, packages, tests, commits when requested, and disposable local Catalog publication tests remain allowed. Prepare artifacts and instructions locally for the maintainer.

Document tested prerequisites and compatibility, not LTS, maintenance duration or SLA promises. Standard-profile review is part of ordinary updates, not a formal ownership/cadence gate. Project source uses [Apache License 2.0](LICENSE). Preserve third-party attribution and licenses.

## Documentation and tracking

- Project documentation includes usage, contribution, architecture, ADRs, development, testing, deployment, operations, reference material, engineering records and trackers. Apply the same writing and link guidance to all of it. The published manual serves users, developers, architects and operators. Site inclusion describes publication location, not audience or review scope. Repository-only documents also require clear prose; retain useful evidence and chronology without work-session journals.

- Reader guides, references and runbooks are maintained in `documentation/src/content/docs/`. Register pages in `documentation/content-map.json`; keep generated Starlight projections, build output, npm dependencies and raw evidence out of commits and site inclusion. Architecture and ADRs are authored in `documentation/src/content/docs/architecture/`; `CONTEXT.md` summarizes current implementation. Use reviewed local notes only when provided. Keep raw logs and generated reports under ignored `output/`. There is no separate `docs/` tree.

  For site/content changes run `make docs-site-check` in addition to the existing source gates; test built-site search, navigation and responsive behavior after theme, plugin or tooling changes. Keep site publication and hosted CI/CD operations maintainer-only under the distribution boundary above.

- Follow the writing style in `documentation/src/content/docs/contributing/documentation.mdx` for all project prose, including HTML, API descriptions, command help and comments. Write current facts and direct instructions, not conversation history, approval narratives or work-session journals. Keep session-specific permissions and test history in task responses or records, not workflow comments or usage guides. Keep planning in the tracker. Update the optional local handoff only when it is present. Retain dates only when chronology matters; preserve technical roles, security requirements and unresolved failures.
- Let ordinary application UI prose use the available layout width and wrap naturally. Do not add arbitrary character/column caps or manual line breaks. Source-formatting line width is not a presentation rule. Preserve meaningful supplied paragraphs, data/code formatting and usable container bounds. Documentation-site reading measures are a separate concern.
- Apply ASD-STE100 Simplified Technical English principles where practical. Aim for 20 words per instruction, 25 per descriptive sentence and six sentences per paragraph. Prefer active voice, simple tenses and consistent terms. Preserve articles, exact names, requirements and uncertainty. Explain jargon and remove filler, invented acronyms and work-session journals. The [writing guide](documentation/src/content/docs/contributing/documentation.mdx#writing-style) explains these principles without claiming full ASD-STE100 compliance.
- Keep `CONTEXT.md` a self-contained, compact application overview. Reconcile affected summaries when behavior, configuration, workflows or the active queue changes; retain explicit source and measurement baselines. It does not replace owning contracts, trackers or records and remains outside the reader site. When local `continue.md` is present, keep it focused on current state, next work and unresolved limits. Do not require it in a fresh checkout.
- The entire `todo/` directory is gitignored and optional. Do not require or create it for builds, verification or ordinary contributions. When it exists, `todo/todo.md` contains active work and pending decisions; `todo/deferred.md` contains deferred proposals; `todo/done.md` contains closed tasks. Follow local `todo/README.md`: move each full entry and owned details, preserving its ID, acceptance criteria, disposition and evidence links, and update incoming links. Do not leave a checked copy in an open tracker or duplicate definitions across files. Promote or reopen into the active tracker with explicit remaining scope; defer into the deferred tracker with a trigger.

  `CONTEXT.md` describes current functionality. When `todo/` exists, `make docs-check` requires all three trackers and enforces open/closed placement and ID uniqueness. Keep public documentation independent of local trackers.
- Keep `DESIGN.md` as a compact architecture entry point. Update the owning manual chapter for detailed design changes; do not maintain another endpoint inventory, test catalog or implementation log in the root. Conditional design must not imply an implemented capability.
- Use ADRs for durable service architecture choices with real alternatives and consequences. Do not create ADRs for ordinary implementation details or SRE-selected replicas, regions, backup schedules, monitoring products, SLA, RPO, or RTO.
- Maintain each ADR as the current decision and update it in place. Keep its identifier and route. Do not add supersession chains, amendment journals, obsolete backlog references or implementation histories. Git retains edit history; released API and stored-data compatibility requirements remain separate.
- Accepted ADRs must be reconciled with `DESIGN.md`, `CONTEXT.md`, configuration, API contracts, deployment documentation, tests and any local trackers where affected.
- API changes update OpenAPI, API conventions, examples where useful, and contract tests.
- Configuration changes update the configuration reference and all affected example profiles.
- Standards changes update `schemas/PROFILES.md`, checksums, licenses, compatibility fixtures, and conformance evidence.
- Deployment, storage, security, recovery, or webhook changes update the corresponding runbook.
- Keep Markdown in plain ASCII with one-line paragraphs. Run `make docs-check` after documentation changes.

## Go style and dependency discipline

- Use `gofmt` and match the surrounding naming and package idiom.
- Pass `context.Context` through blocking, database, and network boundaries and honor cancellation and deadlines.
- Return typed safe domain errors through `internal/catalog/errors.go`; wrap internal causes without exposing them to clients.
- Prefer deterministic injected clocks, ID generators, fetchers, senders, and stores in tests.
- Keep handlers and services small enough that authorization, transaction, and side-effect boundaries remain visible.
- Do not panic for request, dependency, or external-input failures. Panic is reserved for impossible embedded-build invariants.
- Avoid new dependencies when the standard library or an existing pinned dependency is sufficient. A new dependency needs a narrow purpose, maintenance and license review, cancellation and resource-bound analysis, and updated `go.mod` and `go.sum` through Go tooling.
- Run `go mod tidy` only when imports or module requirements change, and review both module files for unrelated drift.
- Do not add a framework, code generator, broker, cache server, search service, workflow engine, or provider SDK without an accepted requirement.

## Task-specific skills

Use the matching repository skill when a task needs its workflow. Keep routine local edits scoped to their owning package and use only the relevant skills; their instructions do not expand the user's authorization or replace the authorities above.

| Task | Skill |
| --- | --- |
| Plan a feature, design decision or change spanning ownership boundaries | [Change planning](.agents/skills/change-planning/SKILL.md) |
| Change an HTTP contract and its affected consumers | [API contract changes](.agents/skills/api-contract-changes/SKILL.md) |
| Change authoring, sealed review/release evidence or immutable discovery publication | [Governed publication](.agents/skills/governed-publication/SKILL.md) |
| Change PostgreSQL repositories, migrations, grants or transactions | [Database changes](.agents/skills/database-changes/SKILL.md) |
| Plan or execute supported restore and retained-state repair procedures | [Recovery and repair](.agents/skills/recovery-and-repair/SKILL.md) |
| Investigate/change search semantics, indexing or relevance evaluation | [Search and relevance](.agents/skills/search-and-relevance/SKILL.md) |
| Change background Jobs, outbox delivery, leases or worker recovery | [Durable workflows](.agents/skills/durable-workflows/SKILL.md) |
| Change source lifecycle, synchronization, provenance or live federation | [Federation development](.agents/skills/federation-development/SKILL.md) |
| Review security or change a substantive trust boundary | [Security review](.agents/skills/security-review/SKILL.md) |
| Interpret or change a pinned format and qualify conformance | [Standards conformance](.agents/skills/standards-conformance/SKILL.md) |
| Qualify independent local clients, resource consumers or Catalog peers | [Interoperability qualification](.agents/skills/interoperability-qualification/SKILL.md) |
| Change a browser interaction or UI-host session/forwarding behavior | [UI development](.agents/skills/ui-development/SKILL.md) |
| Change builds/configuration/Compose or start the local API/UI/docs environment | [Build and deployment](.agents/skills/build-and-deployment/SKILL.md) |
| Change runtime settings, capability activation, secret delivery or credential rotation | [Configuration and credentials](.agents/skills/configuration-and-credentials/SKILL.md) |
| Review/update Helm charts or qualify installation, upgrades and Minikube behavior | [Helm and Minikube](.agents/skills/helm-and-minikube/SKILL.md) |
| Review or update a dependency, toolchain or tool/image pin | [Dependency maintenance](.agents/skills/dependency-maintenance/SKILL.md) |
| Measure performance or optimize code, PostgreSQL queries and indexes | [Performance testing and optimization](.agents/skills/performance-testing-and-optimization/SKILL.md) |
| Diagnose failures or maintain metrics, diagnostics and tracing | [Debugging and performance](.agents/skills/debugging-and-performance/SKILL.md) |
| Maintain reader documentation, site tooling, trackers or handoff | [Documentation maintenance](.agents/skills/documentation-maintenance/SKILL.md) |

The testing skills below own test authoring and post-change verification. Skills use the existing documentation and Make targets; they do not require a separate runner or framework.

## Test and verification workflow

Prefer Make targets because they encode pinned tools and repository policy. Use focused tests while iterating, then run the appropriate complete gates. After implementation or when checking completed work, use the repository [post-code-verification skill](.agents/skills/post-code-verification/SKILL.md) to select lint, unit, functional and dead-code checks without duplicating aggregate runs. For writing, reviewing or reclassifying tests, use the repository [unit-testing skill](.agents/skills/unit-testing/SKILL.md) or [functional-testing skill](.agents/skills/functional-testing/SKILL.md) for the relevant boundary. The [testing guide](documentation/src/content/docs/contributing/testing.md#writing-tests) owns the detailed authoring rules; skills reuse its conventions and the existing Make targets.

Classify a test by everything it executes, including imports, initializers, `TestMain`, helpers and cleanup. Unit tests must not start listeners or servers, make real network/DNS requests, spawn subprocesses, execute application binaries, or access real databases, browsers or providers. Normal test compilation and runner workers are allowed. Temporary files, `httptest.NewRecorder`, injected transports, fake repositories and scripted transactions are allowed; `httptest.NewServer` and a Go test that launches Python are functional. Composed application workflows remain functional even when their stores are in memory.

Establish the expected behavior from the owning contract before writing assertions. Investigate plausible defects by tracing callers, failure paths, boundaries and state transitions; do not treat current implementation output as the specification. Use independent expected results and valid controls that isolate the intended rejection. Investigate a discrepancy before changing an expectation; reproduce confirmed defects with a failing regression, fix the implementation within scope, then verify the regression and affected gates. Check existing tests for the same behavior, extend a focused case where appropriate, and assert observable results, errors, state or forbidden side effects. Use deterministic clocks and synchronization, bounded waits and owned cleanup. Do not add speculative production seams, weaken a contract, duplicate whole workflows or force unreachable states to raise coverage. Fake SQL tests do not replace real PostgreSQL/RLS verification.

Structured Go properties use pinned Rapid in ordinary untagged `*_test.go` files, one `rapid.Check` per top-level `TestProperty...`. Preserve examples and fuzz targets, use independent expectations and bounded synthetic generators, and keep unit isolation. Run `make test-property` alone for verified deeper case counts; see [structured properties](documentation/src/content/docs/contributing/testing.md#structured-go-properties) for fixed ordinary-suite settings, replay/corpus handling and failure classification. Fix genuine findings with minimized regressions; generator, ignored-replay and incomplete-run failures are not mutation catches.

For changes to the reviewed JSON, identity, in-memory idempotency, outbound fetcher, authentication, discovery identifier/cursor, administration pagination, audit export, Skill/Plugin archive inspection, UI session/fixed-origin, webhook retry/signing, publication bounds or configured egress-policy sources or their assertions, run the affected `make test-mutation MUTATION_PACKAGE=...` selection separately, following [mutation testing](documentation/src/content/docs/contributing/testing.md#mutation-testing-and-assertion-strength). It measures complete package units with fixed source/operator scope; its own subprocess fixtures are functional. Review survivors and typed timeouts, reject incomplete/error reports, and preserve ordinary coverage gates. Runner/tool changes also require the documented actual-tool qualification. Do not run mutation analysis as an implicit prerequisite of every lint or unit invocation.

Preserve assertions and discovery when splitting mixed files, including negative cases, fuzz seeds, benchmarks and explicit opt-in tests. Python uses `test_unit_*.py` and `test_functional_*.py`; Node tooling uses `*.unit.test.mjs` and `*.functional.test.mjs`. Keep shared helpers inert and undiscoverable as test cases. Run each affected layer through its maintained gate and report skips, prerequisites and unrun scopes. Keep unit, broad local, PostgreSQL and frontend coverage separate; compare matching denominators and never add their percentages.

Install tools explicitly when missing or after their locks change: `make dev-tools-install` for the locked common Python group, `make validation-tools-install`, `make workflow-tools-install` and the pinned [Ruff executable](documentation/src/content/docs/contributing/testing.md#python-linting) before `make check`, `make client-go-tools-install` before `make client-go-check`, `make ui-install` for frontend checks, and `make docs-install` for the site. `make python-lint` accepts a `RUFF` executable path and is read-only; default Go tests and `make verify` do not require Ruff or actionlint. These are separate toolchains; Go verification does not install or run the npm gates. Install Chromium with `make ui-browser-install` only for browser testing. Keep existing development servers and retained data separate from disposable test fixtures.

The [GitHub Actions workflows](.github/WORKFLOWS.md) call existing Make targets. Contribution checks run on pull requests and pushes to `main`. Pages prepares the manual on `main`; deployment requires maintainer activation. Other suites use reusable workflows or manual dispatch.

Keep tool acquisition separate from checks and avoid a second build/test implementation. `workflow-check` requires installed actionlint and ShellCheck; it does not acquire tools. Keep verification results in engineering records.

For every Go change:

```sh
gofmt -w path/to/changed.go
go test ./path/to/changed/package -shuffle=on -count=1
make test-unit
make check verify
```

Run `test-unit` separately; use `coverage-unit` instead when measuring the same complete isolated selection. The normal `check verify` invocation shares common prerequisites. An explicit unit-only request limits test execution to isolated selections; select requested linters separately and report broader functional gates as unrun.

For documentation-only changes:

```sh
make check
```

`check` includes `docs-check`; use the latter alone for focused prose/link iteration. For documentation site/content changes, also run `make docs-site-check` after explicitly installing the pinned documentation dependencies with `make docs-install`. It runs documentation JavaScript lint and Astro/TypeScript diagnostics before native tests and site builds. This separate Node-based gate is not part of `make check` or `make verify`. Preview built output after navigation, search, theme or tooling changes using the authoring guide's browser checks.

Use additional gates when the boundary changes:

| Change | Required additional verification |
| --- | --- |
| HTTP route, lifecycle, authorization, publication, or discovery | `go test -tags=functional ./api ./internal/platform/httpserver`, relevant package tests, disposable `make run`, and `make smoke` |
| OpenAPI or API-validation tooling | `make api-check`, `make api-contract-check`, `make client-go-check` and `make sdk-check SDK_LANGUAGE=go` / `SDK_LANGUAGE=python` / `SDK_LANGUAGE=typescript` / `SDK_LANGUAGE=rust` for affected contracts; `make api-validation-test` for validator rules, launcher or tool changes. Use `make ui-client-check` after API changes; preserve deliberate raw-evidence client paths. |
| SDK source, generated operations or SDK tooling | `make sdk-check` checks all four SDKs by default; select `SDK_LANGUAGE=go`, `python`, `typescript` or `rust` for an affected language after explicit `sdk-tools-install`. Regenerate intentionally with `sdk-generate`. `sdk-package` builds local artifacts without tests or publication; qualify them separately. Follow [the SDK harness](test/sdk/README.md). Root Go gates do not cover the nested SDK module. Keep SDK units, functional fixtures, runtime dependencies and handwritten coverage separate from the application. |
| Optional VS Code extension or its tooling | Follow `test/vscode/README.md`. Run `make vscode-check`, `make test-vscode` and affected SDK checks separately after explicit installation. Root Go/UI checks do not qualify the extension. Keep packaged host and real HTTP/TLS fixtures functional; isolated extension units use injected boundaries. |
| UI application, authentication host or client tooling | `make test-ui` includes `ui-check`, `ui-test` and the browser workflows; use its component targets during iteration without repeating them before an unchanged aggregate run. Packaging/login changes also need `make test-ui-compose` and `make test-ui-oidc`; Helm/session changes use the applicable `make test-minikube-ui` or `make test-minikube-replicas` scenario. |
| UI appearance or frontend dependencies | `make ui-visual` for appearance or dependency changes. Use `make ui-visual-update` only for intentional differences, review every changed image, then compare again without updates. Dependency changes also require local `make ui-deps-check` and the affected gates in [UI dependency maintenance](documentation/src/content/docs/reference/dependencies.md#ui-dependency-maintenance). `ui-deps-audit` submits dependency metadata to its registry; external registry audits are maintainer-only. Leave them unrun and report that limit. Root `make security` does not replace these full-lockfile checks, including development dependencies. |
| PostgreSQL schema, repository, RLS, role, lease, or transaction | `make test-integration` against disposable PostgreSQL 18, including exact-role positive and negative paths plus direct-SQL lifecycle and cross-parent denial where applicable |
| Audit export, AIR identifier, cursor, URL validation, JSON-LD, Skill Markdown, or discovery parser | `make test-fuzz` or the applicable focused `go test -fuzz` invocation |
| Container, Compose, Helm, security context, scheduling, disruption, network isolation, or probes | `make compose-check`, `make container-verify` with exact image metadata, `make helm-check`, `make deploy-check`, and the relevant Compose flow. Run `make test-minikube` and `make test-minikube-topology` when the change affects the maintained Helm runtime boundary; record the tested build and platform; reuse applicable runtime evidence for metadata-only changes; the Kubernetes 1.34 chart/schema baseline and Linux AMD64/ARM64 builds do not require a distro, engine or native-hardware matrix. |
| Security-sensitive dependency or boundary | `make security` plus focused adversarial tests |
| Hybrid retrieval, indexing, embedding transport or provider deployment | `make embeddings-check` for service-free checks; `make test-embeddings` for the real-model HTTP adapter; `make test-semantic` for enabled Compose indexing/search, CLI/UI, outage recovery and the 1k pilot; `make test-minikube-semantic` for enabled Helm/multiple replicas. Follow `test/embeddings/README.md` for explicit model installation and owned cleanup. Core-mode passes alone do not qualify hybrid behavior. |
| Cross-replica sessions, command replay, concurrency or process replacement | `make test-minikube-replicas` for the owned three-UI/API/worker scenario; independent-Catalog federation remains `make test-federation-peers`, not database replication. |
| Vendored standards profile | `make profiles`, `make conformance-fixture`, compatibility tests, and `make conformance-live` against a disposable local service when output changes |
| Major workflow | `make test-e2e` for the in-process path and `make test-functional` for the isolated packaged Compose path; use `make smoke` directly only against a deliberately selected disposable local service |

`make verify` aliases `make test-all`: Go/shell lint, the default suite with race detection and coverage, profiles, conformance fixtures, and stripped-binary checks without running external services. This includes the UI Go host, but only compiles/analyzes the tagged PostgreSQL, Helm and Minikube test source; use their explicit targets to execute it. `make check` separately covers documentation, Python, YAML, container/Compose source and API linting, including installed Ruff, API-tooling ESLint and Redocly tools. Neither includes frontend checks, generated Go-client qualification or built-site verification.

A first run may download pinned modules and tools into the Go cache. External integration gates require their documented environment variables and disposable dependencies.

Keep unit and in-memory component tests beside their code. `make test` delegates to `test-functional-local`, adding the `functional` tag for local server, subprocess and composed application tests. Keep isolated cases untagged; split mixed files without losing assertions or shared inert helpers. `test-unit` selects isolated Go tests across the owned roots with `-tags=minikube`, including its pure helpers without a cluster. It enables no functional, database or Helm tests.

Python helper units use `test-tooling-unit`; native Node units use their owning npm scripts. Run it alone; workflow env files are bypassed. Real PostgreSQL tests require the `integration` build tag and an explicit disposable-database gate. Use `make coverage-unit` alone for the complete isolated Go selection with an application-only denominator, weighted file/package summaries and a validated atomic `coverage/unit.out` profile. Generated bindings, examples and harnesses are excluded from that denominator; database and zero-hit application code stay included.

Default coverage uses `coverage/default.out`; `coverage/core.out` filters database-package source rows from the same broad local run. Actual Helm packaging tests run through `make helm-package-test`, included in `make helm-check`, not the default suite. Follow the [test-placement guide](documentation/src/content/docs/contributing/testing.md#test-placement-and-selection) rather than creating a second unit-test hierarchy.

`make smoke` creates governed records and activates a public snapshot. Never point it at a shared or production deployment without explicit authorization. Stop disposable services and containers after testing. `make compose-reset` deletes local service volumes and requires deliberate approval when existing data might matter.

Frontend dead-code checks use the pinned local Knip through `make ui-dead-code-report` and `make ui-dead-code-check`, installed by `make ui-install`. The enforcing check runs once through `ui-check`/`test-ui`, separately from backend and isolated-unit targets. Review both full and production-browser graphs before removing findings; preserve tests, lazy/framework entry points and the narrow generated-schema/configuration exceptions. Report mode still fails on analyzer/configuration errors. See [UI dead-code analysis](documentation/src/content/docs/contributing/testing.md#ui-dead-code-analysis); the UI Go host stays in the existing Go gate.

The optional product UI is implemented in `catalog-ui/`; use its README and test matrix for current scope, selected Node/npm toolchain and explicit client checks. Ordinary UI source tests remain independent of the documentation build. The UI image builds the static manual in a separate stage and serves its fixed `/docs/` route; preserve separate React/manual evidence and CSP policies. Backend builds remain independent. Browser validation covers real authenticated workflows, keyboard interaction, responsive layout, theme persistence and browser errors against owned test data. The Starlight manual includes the architecture pages and diagrams and has its own navigation/search/layout checks; their browser tests are not product-UI or service API conformance evidence.

For changes to the Job retry ceiling or its formal tooling, follow the [formal harness](test/formal/README.md) and run `make test-formal` explicitly with installed tools. `formal-tools-install` is separate acquisition; ordinary gates never invoke Java. Preserve exact schedule/jitter and worker regressions: the proof covers only the documented arithmetic obligations, not whole-service behavior or test coverage.

## Generated and local artifacts

- `make clean-dry-run` previews the explicit build/dependency inventory; `make clean` removes it, including standard coverage/scratch, Python caches, npm dependencies, UI/docs output, VS Code tools/staging and conventional Rust targets. Retained reports, configuration and application data are preserved; custom destinations must stay outside the explicit inventory. Do not clean files used by retained servers or concurrent builds/tests; the writer lock covers participating VS Code workflows only. Use disposable repositories to test cleanup; see the [cleanup guide](documentation/src/content/docs/contributing/build-and-release.md#clean-local-builds-and-dependencies).
- `make clean-output-dry-run` previews the separate disposable-output inventory; `make clean-output` removes only selected reports, logs, caches and test work. Unknown output, runtime inputs, models and nested Git subtrees remain. Keep these targets independent of normal cleanup. Stop relevant writers first, and test only in owned disposable repositories unless retained-checkout execution is explicitly requested. See [output cleanup](documentation/src/content/docs/contributing/build-and-release.md#clean-generated-output-separately).
- If disk space is insufficient, stop and notify the user. Do not create or use RAM-backed storage for build or test artifacts. Do not redirect outputs into `/dev/shm`, replace artifact directories with storage symlinks, or relocate outputs to bypass capacity limits.
- Do not commit or review `bin/`, `coverage/`, `objects/`, `objects-*`, `.env`, logs, profiling outputs, test binaries, or local container volumes as source.
- `.env.example` is the only environment template. It must contain safe non-secret local defaults only.
- `Containerfile` is canonical. `deployments/container/Dockerfile` must remain byte-identical, and `.containerignore` must remain byte-identical to `.dockerignore`; `make container-sync-check` enforces both pairs.
- Do not hand-edit generated protocol bindings or generated test output. Update the source, documented generator, pinned version, and verification evidence instead.
- Reviewed visual baselines under `catalog-ui/tests/browser/snapshots/` are maintained test inputs, not disposable output. Follow their README for the pinned comparison environment and explicit update/review procedure; never regenerate expected images merely to make a failing check pass. Actual/diff screenshots and raw browser reports stay under ignored `output/`.
- Do not modify vendored upstream schemas in place. Add a new pinned profile when upstream bytes change.

## Git and workspace safety

Before editing, inspect the current branch, `git status --short`, and relevant diffs. Treat existing modifications and untracked files as user-owned, preserve unrelated work, and report when an intended edit overlaps it.

Use `rg` and `rg --files` for repository searches. Prefer patch-based, reviewable edits over whole-file rewrites. Never use destructive Git commands, broad cleanup, migration mutation, `make compose-reset`, volume deletion, or removal of local object data without confirming the exact disposable target and authorization.

Do not stage or commit unless explicitly authorized. Use signed conventional commits and never mention AI. Use the `gh` CLI exclusively for read-only GitHub operations. Git remotes, pushes and external publication remain maintainer-only under the distribution boundary above.
