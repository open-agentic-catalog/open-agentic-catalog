---
title: "Persistence and schema evolution"
---

[PostgreSQL 18](https://www.postgresql.org/docs/18/index.html) with [pgcrypto](https://www.postgresql.org/docs/18/pgcrypto.html) stores transactional Catalog state, bounded documents, evidence, snapshots, durable work, and replay responses. External artifact ecosystems retain their own content authority.

## Storage authorities

### Authoritative PostgreSQL

PostgreSQL retains canonical metadata, bounded exact documents, lifecycle pointers, evidence, audit, outbox records, Jobs, observations, and publication snapshots.

### External content authorities

Git commits and directories retain source and Skill trees. OCI manifest digests identify Agent and MCP runtimes and optional Skill releases. Reviewed HTTPS URLs remain delivery locations while PostgreSQL binds the exact validation bytes. For Plugins, PostgreSQL retains manifest/component documents and inventory bound to the complete external archive's checksum and size. Archive bytes remain external.

### Derived versus recoverable

Search documents, text indexes, optional embeddings, and caches are rebuildable and never supersede governed records. Jobs, outbox acknowledgement, snapshots, and optional delivery evidence are durable recovery state and must not be silently recreated.

Unified Catalog Search uses one tenant-scoped, read-only PostgreSQL [repeatable-read transaction](https://www.postgresql.org/docs/18/transaction-iso.html#XACT-REPEATABLE-READ). It selects local releases and indexed observations, totals, bounded metadata, and cursor generations together. Concurrent commits cannot split these durable result sets across generations. The transaction closes before optional live discovery, so outbound requests do not hold a database connection. Continuations reject a generation changed since the previous page.

## Durable table areas

| Area | Current tables | Responsibility |
| --- | --- | --- |
| Identity | `tenants`, `tenant_identity_realms`, `tenant_memberships`, `namespaces`, `namespace_memberships`, `role_assignments`, `tenant_authorization_state`, `role_definitions`, `role_permissions`, `access_groups`, `access_group_members` | Exact realm and subject mapping, tenant/namespace membership, current permission revision, built-in/custom roles, direct-member groups, and scoped assignment history. |
| Governed resources | `publishers`, `publisher_domain_verifications`, `resources`, `resource_versions`, `resource_version_revisions`, `representations`, `document_blobs`, `artifact_references`, `skill_file_inventory` | Stable identity, versions, exact bounded bytes, representation bindings, immutable Skill handoff, and Plugin inspection documents/inventory. Publisher verification retains bounded DNS challenges, proofs and check results. |
| Listing and review | `catalog_listings`, `listing_revisions`, `evidence_sets`, `version_validation_runs`, `review_submissions`, `listing_releases` | Immutable reviewed metadata, append-only validation, decisions, seals, and release lifecycle. |
| Private feedback | `item_feedback` | Mutable comments and responses with immutable item context, terminal-state guards, and API-only access. |
| Personal library | `personal_items` | Bounded human-owned labels, exact resource references and validated search criteria; forced tenant/owner RLS, revision guards, serialized quotas and atomic private audit. My work is derived from exact retained transitions, not a second status table. |
| Federation | `source_inspections`, `source_change_previews`, `federation_sources`, `federation_source_revisions`, `federation_source_audiences`, `federation_source_referrals`, `source_snapshots`, `source_retrieval_documents`, `source_retrieval_states`, `source_retrieval_attempts`, `source_maintenance_state`, `observations`, `observation_occurrences` | Reviewed topology, immutable source history, private conditional-retrieval state and evidence, automatic admission, exact and normalized observations, per-appearance provenance, complete snapshots, deterministic grouping, and reconciliation. |
| Tenant operational settings | `tenant_settings` | Audited live overrides, inheritance and revision-fenced admission; startup connectivity and secrets remain external. |
| Listing artwork | `source_artwork_heads`, `source_artwork_revisions` | Current local overrides and immutable history, bound to an exact source generation and resource identity; image bytes remain external. |
| Human ratings | `rating_targets`, `item_ratings`, `item_rating_revisions`, `rating_sharing` | Version-specific opinions, minimal history, aggregates and audience consent; no trust or search-ranking authority. |
| Search | `catalog_search_generations` plus derived columns and indexes on governed release and observation tables | Conservative cursor invalidation and rebuildable core-lexical-v1 projections; never authoritative resource or observation state. |
| Publication | `publication_snapshots` | Exact host/audience bundle bytes, membership, exclusions, document allowlist, history, and active pointer. |
| Commands and work | `idempotency_keys`, `jobs`, `audit_events`, `outbox_events`, `webhook_endpoints`, `webhook_deliveries`, `webhook_notification_state` | Crash-safe replay, leased work, current versioned attributable audit, reliable post-commit notification, optional delivery, and coalesced tenant notification-loss counters. |
| Schema | `catalog_schema_migrations` | Forward-only migration identity and checksum history. |

## Migration history

Migrations 000001-000047 are immutable historical steps. Later migrations replace earlier pre-release formats; descriptions below do not imply continued acceptance of those formats. The current runtime uses exact runtime privilege manifest v27. Optional semantic tables have a separate checksum ledger and explicit schema-owner setup. Core migrations remain independent of pgvector. Enabled runtimes validate semantic tables and exact grants but never install extensions or execute data definition language (DDL). See [hybrid-search storage and recovery](../operations/hybrid-search.mdx).

### 000001 - Catalog foundation

Tenants, lifecycle, audit, outbox, webhooks, commands, Jobs, federation, observations, publication, indexes, and forced RLS.

### 000002 - Publication-domain approval

Reason-bearing administrator authorization kept separate from external publisher authority.

### 000003 - Atomic command results

Protocol-v2 reservation and exact response completion aligned with domain transactions.

### 000004 - Catalog documents

Tenant-scoped digest-bound PostgreSQL document blobs become authoritative for bounded textual content.

### 000005 - Skill packages

Archive digest, inventory, assessment state, and immutable Git/OCI handoff without archive retention.

### 000006 - Identity and discovery

Realms, memberships, namespace memberships, role assignments, and identity-aware host isolation.

### 000007 - Controlled federation

Immutable source revisions, discovery audiences, approved referral edges, and topology evidence.

### 000008 - Renewable validation

Forced-RLS append-only validation runs freeze evidence separately from immutable content revisions.

### 000009 - HTTPS representations

Bounded retrieval aggregates and profile-v6 evidence bind external URLs to exact retained validation bytes.

### 000010 - Observation provenance

Typed collision-safe identity keys, exact and normalized entry evidence, immutable occurrences, direct authoritative withdrawal, and local adoption provenance.

### 000011 - Bounded search

Conservative tenant generations, local query indexes, rebuildable indexed-observation projections, and transaction-driven cursor invalidation.

### 000012 - Practical administration

Terminal membership offboarding and dependency-aware Resource tombstones with retained identifiers and history.

### 000013 - Automatic source maintenance

Additive nullable due-source and identity projections with mixed-version fallbacks, private retrieval documents and conditional state, immutable attempts, exact Registry-page occurrence locations, and crash-safe admission markers.

### 000014 - API contract strictness

Demonstrated administration-page indexes plus an explicit internal-outbox discriminator for safe public event dispatch.

### 000015 - Runtime authority and retained history

Exact parent constraints, lifecycle guards, mutable source-run diagnostics, mandatory PostgreSQL inline or document authority for new bounded representations, a schema-owner-only one-way legacy document-link enrichment, and the contract point consumed by exact API and worker grant reconciliation.

### 000016 - Audit investigation and export

Nullable legacy-aware format version, prospective bounded insert guard, operator-only exception inventory, exact investigation indexes, and the manifest-v2 audit baseline. Later migrations extend runtime authority; see [exact grants and extension access](../operations/database-roles.md#exact-grants-and-extension-access) for the maintained privilege rules.

### 000017 - Bounded search repair

A sparse tenant-and-ID index contains only normalized observations with incomplete derived search fields, allowing repair completion to use a bounded existence probe without adding runtime authority.

### 000018 - Operational housekeeping

Prospective source-inspection use tracking, immutable terminal-Job and acknowledged-outbox cleanup markers, class-specific bounded eligibility indexes and guards, and exact worker housekeeping authority under manifest v4.

### 000019 - Webhook dispatch scheduling

Worker-owned outbox deferral, a ready-work index, guarded lease-to-deferral transitions, and manifest v5 keep waiting notifications out of the claim queue while unrelated work progresses. Pending endpoint selection is database-bounded; application bounds and enabled developer scenarios are covered by the webhook tests.

### 000020 - Webhook encryption-key maintenance

Owner-only ciphertext rewrapping preserves all endpoint history. A bounded, audited operator command verifies each replacement and resumes safely while all runtimes remain stopped. Runtime privilege manifest v5 is unchanged.

### 000021 - Current command protocol

Only atomic PostgreSQL commands remain. Obsolete response columns and runtime grants are removed under manifest v6; incompatible development rows cause an explicit refusal, not conversion or deletion.

### 000022 - Current evidence baseline

Current attributed audit and validation records, exact observation provenance, PostgreSQL document authority, closed notification references, and current source-sync Jobs. Pre-release columns, grants, coercion, and unsupported-state exceptions are removed under manifest v7.

### 000023 - Webhook delivery retention

Prospective markers, bounded cleanup indexes and parent-aware guards preserve unfinished recipient evidence and audit.

### 000024 - Notification capacity and reconciliation

Atomic bounded public-event admission, forced-RLS coalesced tenant loss state and guarded invoker triggers under manifest v9.

### 000025 - Notification write authority

Manifest v10 removes runtime state DML. Two fixed owner-authorized trigger functions, without runtime EXECUTE, preserve atomic counters; temporary-trigger depth is not authorization. All other triggers remain invoker-only.

### 000026 - Private trace causality

Optional immutable Job and outbox creating-span context; manifest v11 adds exact inserts and required reads, never update authority or public trace fields. No backfill or telemetry readiness dependency.

### 000027 - Consistent lexical search

Unicode token and sort-key normalization shared with Go scoring and cursors. Retained indexed vectors require bounded tenant-scoped repair; manifest v12 grants only the required helpers and projection-version reads.

### 000028 - Authoring histories

Additive tenant/parent/UUID indexes support bounded version, listing, revision, release and review reads. No data conversion or runtime privilege changes.

### 000029 - Stored local search projections

Resources store a generated identifier vector; immutable ListingRevisions store generated display-name, description, tag and representative-query vectors plus a normalized sort key. PostgreSQL computes these fields on write, using the existing Unicode normalization. Queries merge positionless vectors without retokenizing every candidate; field weights and exact-identifier matching remain unchanged. Original metadata and digests remain authoritative. Manifest v14 grants only API reads and the text-array helper used by listing inserts, never direct projection writes or extra worker authority. The migration materializes existing rows with runtimes drained; no local repair job or extra service is needed.

Ordinary count/page selection does not join current validation evidence for every candidate. Bounded result hydration still reads the latest evidence, and trust facets retain their current-evidence scalar projection. This avoids unused work without caching trust or changing validation/publication eligibility.

### 000030 - Stored indexed ranking vectors

Observations store generated identifier, raw display-name, description, tag and representative-query vectors for field-specific ranking. The existing combined vector remains the matching and repair boundary. Empty display names still receive no display-field boost. PostgreSQL materializes existing rows with runtimes drained; updates and bounded projection repair maintain the generated fields automatically. Manifest v14 permits API reads and the array helper for worker ingestion, but no worker reads or direct runtime writes of ranking vectors. Retained source evidence, conflict grouping and normalized digests are unchanged.

### 000031 - Native Plugin inspection

Resources accept the Plugin kind. Representations retain bounded Plugin manifest/component documents, file inventory and assessments in an immutable JSON inspection; the external archive remains identified by its SHA-256 digest and size. Same-tenant parent checks require a Plugin resource, and document-retention exceptions apply only to the pinned Plugin archive formats. Manifest v15 adds the inspection column to API insertion and API/worker reads, without update authority. Drain runtimes before migrating and reconciling grants; repair forward after a failed reconciliation.

### 000032 - Private item feedback

The `item_feedback` table binds each record to a tenant, human membership, Resource and optional exact release/version revision. It has bounded text, forced tenant RLS, indexed pagination, immutable context and terminal-state guards. Manifest v16 grants only API reads/inserts and explicit mutable-column updates; workers have no feedback access. Feedback transitions, safe audit facts and command receipts commit together without an outbox event. Feedback never alters published evidence or search projections.

### 000033 - Feedback trigger resolution

Pin the feedback guard's search path to `pg_catalog`; tenant-table reads remain explicitly schema-qualified. Restore verification check set 2 checks feedback context and terminal text without returning private bodies. Drain runtimes, migrate and reconcile both exact role manifests before starting matching binaries. Failed reconciliation is repaired forward with runtimes stopped.

### 000034 - Authorization core

Adds tenant authorization revision/fencing state, immutable built-in role definitions and permission sets, and same-tenant assignment references. New tenants initialize this state once; retained tenants receive deterministic built-ins without rewriting audit or assignment identity. An inconsistent active namespace membership gate refuses migration. Identity administration, provisioning and realm transitions acquire exclusive authorization admission before tenant/object locks; effective changes advance the counter atomically. API manifest v18 gains read-only permission data and exact revision updates; workers gain no permission reads. This migration establishes the permission core. Current domain enforcement, collection selection, and bounded clients use the retained policy defined in [ADR-0022](decisions/0022-local-postgresql-authorization.md). Later numbered migrations extend authority as described below.

### 000035 - Authorization administration

Migration 000035 adds audited custom role metadata, same-realm direct-member groups, stable group relations and direct/group assignments under manifest v19. Permission-set replacement and scope compatibility are checked at commit; built-ins remain immutable. Workers receive no administration authority.

### 000036 - Current permission replay admission

Migration 000036 adds bounded original replay authorization to command completion under manifest v20. It refuses every unexpired reservation and every unfinished unreleased reservation, including expired pending work. Drain runtimes and let replay windows expire; inspect and release only proven interrupted work through the supported owner repair before retrying migration. Earlier committed migrations remain applied. It preserves current tenant-wide development bundles as explicit companion assignments and adds no tenant authority to namespace-only assignments. Public discovery snapshots and immutable evidence remain unchanged.

### 000037 - Listing artwork and source overrides

Migration 000037 adds tenant-scoped artwork override heads and immutable revisions. Each override names one source generation, exact resource identity, version presence and kind. Source generation guards prevent reuse after a replacement interface. Observation metadata retains only bounded presentation candidates. Manifest v21 gives the API exact override grants and the worker only the observation metadata writes required for synchronization. Image bytes remain external.

### 000038 - Human resource ratings

Migration 000038 adds exact rating targets, current individual opinions, immutable minimal revisions and audience-sharing choices. Database triggers maintain count/sum totals atomically. Manifest v22 restricts individual records to the API and permits worker aggregate reads. Built-in permission initialization also grants the selected rating actions to future tenants.

### 000039 - Indexed declaration filters

Migration 000039 derives bounded license and category fields from retained normalized observations. A separate version marker identifies observations that need repair. Absent or unusable declarations remain unavailable independently; a valid empty topic array remains known-empty. Original bytes and digests do not change.

Manifest v23 permits API reads and the helper calls required by ingestion. Runtime roles cannot assign these derived fields. Drain runtimes, apply migrations and exact grants, then run the [bounded tenant repair](../operations/search-projection-repair.mdx) before restart. No unbounded document backfill runs during migration.

### 000040 - Unicode version lengths

Migration 000040 permits up to 128 Unicode code points in source artwork version labels. It replaces the former 128-byte check. Version presence, exact identity, immutable history and document byte limits remain unchanged. No data rewrite, search repair or additional runtime privilege is required.

### 000041 - Tenant operational policy

Migration 000041 adds tenant settings with forced RLS, revision guards and bounded automatic-source overrides. It adds the two tenant settings permissions to existing and future Tenant Administrator templates. Custom roles keep their existing permissions.

### 000042 - New Job attempt defaults

Migration 000042 adds the optional tenant attempt override, bounded from 1 through 20. New default-budget admissions resolve this value under the settings fence. Existing Jobs and explicit budgets remain unchanged.

### 000043 - Private human library and exact work attribution

Migration 000043 adds `personal_items`, with forced tenant and owner RLS, active human ownership and a serialized 200-item quota. Items retain labels and exact references or validated criteria, without resource documents. The API has explicit column privileges and owner-bound deletion authority. Workers have no library grants. Required audit records commit with each mutation; these private preferences create no public outbox event.

An audit expression index supports exact membership/realm work selection. New Version and Listing revision transitions include their immutable revision ID in safe audit facts. Older transitions without that binding are omitted from My work. The migration adds `library.own` and the global `library_member` template. Existing Discovery reader and Tenant administrator templates include this permission; namespace-capable templates remain unchanged.

Removal physically deletes the selected saved item. Required audit and idempotency evidence retain their ordinary lifecycles. This is not a compliance retention or deletion guarantee. Repair uses the exact migration/manifest workflow and supported restore verification; do not infer missing ownership or reset retained data.

### 000044 - Principal lookups and group archival guards

Migration 000044 adds a partial exact-target index for immutable Resource creation and review-submission audit attribution. Creator/reviewer checks use retained membership and realm evidence without changing public attribution text or stored Resources/submissions. The replaced group lifecycle guard refuses archival while active assignments or member relations remain. Existing archived history stays unchanged. No runtime grant or privilege-manifest version changes are required. Apply through the normal owner-only migration workflow; there is no automatic migration or SQL rollback. If it fails, keep runtimes stopped, repair the reported infrastructure/privilege problem and rerun the forward migration. Active groups require explicit member/assignment cleanup before archival; never reopen archived groups or fabricate revocation history.

### 000045 - Exact Namespace lookup

Migration 000045 adds a tenant/display-name/slug/ID B-tree index for the administration inventory. Exact name filters and current visibility precede the limit, while duplicate names retain stable slug/ID ordering. Publisher display-name lookup uses the existing index; declared-domain matching stays in the protected SQL selection. No retained data, RLS policy, function or runtime grants change; manifest v26 remains current. Follow the drained owner-only migration procedure. Index construction holds the normal transactional build lock and adds storage/write maintenance; failure rolls back this step and requires deliberate owner retry, with no automatic rollback of earlier migrations. The [migration notes](../../../../../migrations/README.md) and Administration record retain the plan evidence and declared-domain scan limit.

### 000046 - Publisher search ordering

Migration 000046 adds Publisher changes to tenant search-generation invalidation. Publisher display names can determine column order, so an earlier cursor must not continue through a rename. The trigger reuses the existing generation function. No retained record, table, function, RLS policy or runtime grants change; manifest v26 remains current. Follow the drained schema-owner workflow and retry a failed migration deliberately after correcting its cause.

### 000047 - Publisher domain verification

Migration 000047 removes the obsolete `publishers.verified_domains` column after preserving its complete approval union in `approved_publication_domains`. It rejects undeclared values instead of discarding them. Existing approvals do not become DNS evidence. The migration preserves Publisher identities, revisions and timestamps.

The new `publisher_domain_verifications` table retains bounded challenges, proof and the latest check result for each declared domain. Tenant RLS, immutable identities, revision guards and Publisher-first application locks protect this state. Domain removal deletes its verification state in the same transaction. Clearing DNS evidence does not revoke approval. Runtime privilege manifest v27 grants exact API columns and no worker access to this table. Restore check set 7 includes the new state without disclosing TXT values.

Drain runtimes before the schema owner applies the migration and reconciles both manifests. Runtime startup never applies migrations. After a failed migration, correct the reported inconsistency and retry through the existing maintenance procedure. No database reset, automatic rollback or mixed-version runtime overlap is supported.

## Database identities and RLS

| Identity | Required attributes | Scope | Never reuse for |
| --- | --- | --- | --- |
| API | Directly authenticated `LOGIN NOINHERIT NOBYPASSRLS`, no assumed role, incoming or outgoing memberships, or elevated cluster attributes; exact manifest only. | Tenant-bound reads and column-level writes after transaction-local tenant, actor, and membership context. | Cross-tenant leases, schema changes, migration history, or tenant bootstrap. |
| Worker | Directly authenticated `LOGIN NOINHERIT BYPASSRLS`, no assumed role, incoming or outgoing memberships, or other elevated attributes; exact manifest only. | Cross-tenant leases and bounded worker operations whose repository queries retain explicit tenant predicates. | Serving authenticated product requests or changing schema. |
| Migrator / operator | Dedicated Catalog schema and extension ownership or alteration authority. | Forward migrations, exact privilege reconciliation, first identity provisioning, and reviewed repair workflows. | Long-running API or worker pods, or another application's objects. |

### Exact runtime authority

Every Catalog table and column defaults to unavailable. The manifest names readable, insertable, and updatable columns, plus narrow deletes. The API receives the lexical query parser, sort-name normalizer, and token/vector builders. The worker receives ingestion token/vector builders and audit/public-outbox insert columns for synchronization and publication-candidate results. It also receives exact bounded housekeeping reads, marker updates, and deletes.

Runtimes receive no table-wide data manipulation language (DML), grant option, sequence, schema-create, trigger, migration-ledger, tenant-bootstrap, or future-object authority. Worker audit reads, updates, and deletes remain denied. Startup rejects missing grants, excess effective grants, delegation authority, and direct `PUBLIC` drift.

### Narrow extension authority

Reconciliation first revokes runtime and `PUBLIC` execution from every `pgcrypto` function and extension functions in application-visible schemas. It then grants the reviewed overloads. The complete `pgcrypto` allowlist grants `digest(bytea, text)` to API and worker and `digest(text, text)` to API.

When `catalog_semantic_schema` exists, reconciliation also grants the reviewed pgvector functions for each runtime role. These grants remain required when semantic search is disabled. Installing pgvector alone does not authorize them. See [exact grants and extension access](../operations/database-roles.md#exact-grants-and-extension-access) for the reconciliation scope and conditional grants.

A command prepares external results before its atomic write transaction. The external preparation phase cannot modify Catalog state.

1. Reserve the key with a scoped canonical request and authorization digest.

1. Prepare through short tenant-scoped reads and bounded external validation. Do not hold an interactive transaction during an external call.

1. In the atomic write transaction, lock the saved command reservation. Recheck the current state. Write the domain changes. Append the required audit and outbox records.

1. Retain the exact response status, safe headers, and body in the same commit.

Ordinary command transactions use the connection's default isolation level and recheck current permissions inside that transaction. PostgreSQL defaults to Read Committed. Preparatory permission reads can use Repeatable Read; they do not replace that final check. Selected publication transitions explicitly use Serializable isolation. The command's domain changes, required evidence and saved response commit together regardless of its selected isolation level. See [write admission and revocation](decisions/0022-local-postgresql-authorization.md#write-admission-and-revocation).

## Storage boundaries

### Bounded documents

Each document and the combined documents of one VersionRevision are limited to 16 MiB. The service checks UTF-8 and rejects NUL bytes. Documents are tenant-scoped, bound by SHA-256, and verified on reads.

### Skill packages and runtimes

Package bytes are transient. Complete Skill source and releases remain in immutable Git or OCI; deployable Agent and MCP runtimes remain in OCI.

### PostgreSQL-only documents

No filesystem, memory or S3 document bridge. Removed settings and unsupported persisted development formats fail closed without conversion or deletion.

### Managed media

Managed media is unavailable; startup fails if it is enabled. A future S3-compatible capability may hold only Catalog icons and screenshots. Core readiness does not depend on it.

## History, cleanup, and restore

### Governed history

Sealed revisions, EvidenceSets, validation runs, review submissions, releases, publication snapshots, source provenance, used inspections, adopted provenance, identity history, and audit evidence remain retained. Lifecycle transitions append or move explicit pointers instead of rewriting evidence. Destruction of governed history requires a deferred policy, authorization, legal-hold, privacy, and preservation contract; it is not ordinary housekeeping.

### Automatic housekeeping

ADR-0015 assigns routine expiry to one independently supervised worker loop. Compose and Kubernetes need no cleanup API, manual `catalogctl` run, CronJob, or second credential. Reads enforce expiry even when reclamation is delayed.

Separate bounded `FOR UPDATE SKIP LOCKED` transactions collect eligible rows:

- Expired completed or released replay rows, selected through opaque reservation IDs.
- Expired previews through ordinary indexes.
- Unused inspections.
- Obsolete per-revision validator and admission state.
- Terminal Job leaves.
- Acknowledged outbox rows without a delivery reference.
- Eligible webhook delivery evidence through parent-aware retention rules.

Preview creation audit remains retained independently. A tenant-inclusive source-revision link preserves used inspections without worker audit-read authority. Pending, unexpired, current, live, referenced, and governed rows remain.

Immutable database-time markers give eligible terminal rows their full configured minimum retention. After eligible delivery evidence disappears, its outbox parent starts a full outbox retention interval. Configuration exposes bounded cadence plus Job and acknowledged-outbox retention through JSON, environment, Compose, Helm, and diagnostics. Delivery evidence uses the acknowledged-outbox retention setting. Replay, preview, and inspection windows, class order, batch limits, and SQL-time limits remain code-defined. Each automatic batch is crash-safe through its transaction and emits bounded low-cardinality signals. It does not count entire tables or create a growing audit-receipt stream. PostgreSQL autovacuum makes deleted tuple space reusable.

### Isolated restore verification

Before any restore mutation, migration planning rejects changed checksums and applied versions absent from the embedded bundle. `catalogctl restore verify` connects to the selected PostgreSQL restore for bounded logical-integrity checks. It does not fetch upstream resource documents. It requires one explicitly selected tenant in a named, isolated, quiescent restore.

The command checks the exact migration ledger, named role manifests, forced row-level security (RLS), and exact policy definitions. Checks include verified identity-realm resolution and critical guard definitions and state. It reuses owning validators for semantics, digests, payloads, and references not guaranteed by current validated constraints. Federation checks compare snapshot retrieval projections with immutable attempt rows. They privately validate conditional bytes and revision-policy bindings without exposing URLs or validators.

Closed versioned reports separate traversal from page-local integrity. They distinguish current violations, repairable projections, and operational attention. A continuation binds the recovery label, tenant, roles, schema, check set, page limit, stage, and position. It is neither authorization nor tamper evidence. The label does not prove backup provenance or tenant-set completeness. Exact audit comparison requires separately trusted evidence from the same quiescent recovery point.

Projection-repair completion uses a sparse-index existence probe. Repair only reported search drift, then run a full rescan. Invalid retained publication evidence stops qualification. Structurally valid active snapshots remain unchanged. A separate current-policy review can request a normal unactivated candidate through the authenticated API.

### Database authority and audit investigation

Every version-1 mutation bundle is checked against its exact tenant, target, and allowed store action before persistence. Paired public notifications must also match the normalized commit time and closed action, target, digest, and host reference. Schema-owner identity provisioning and realm transitions use this same boundary rather than a separate SQL-only convention.

The migration history above records exact privilege, lifecycle, document-authority, audit, and housekeeping changes. Current formats retain attributed audit, complete provenance, and PostgreSQL document authority. They reject unsupported development formats without conversion or deletion. Apply migrations with application runtimes stopped as described in the [database role guide](../operations/database-roles.md).

Actors with `audit.read` can combine actor, action, request, paired target, and half-open time filters before a bounded newest-first page limit. A synchronous idempotent export requires `audit.export`. It selects one repeatable-read, oldest-first range with ceilings of 1,000 rows, 64 KiB per record, and 8 MiB per response. Access evidence and the exact replay response commit atomically; the export creates no Job or export object.

The offline verifier proves file self-consistency. The read-only restore comparer requires a separately trusted manifest and external provenance for the same quiescent database recovery point. Classified source, publication, Job, repair, and exact private-evidence operations use typed safe facts. Each evidence bundle binds the exact tenant, target, and allowed store action. The reviewed map requires or forbids a notification for each action. It preserves intentional mappings, including source test to source-revision activation and rollback to publication activation. Audit-only operations bypass the outbox; source and publication events use the reviewed public registry.

[Database role guide](../operations/database-roles.md) - Role attributes, grants, RLS tests, and identity provisioning.

[Backup and restore](../operations/backup-restore.mdx) - Authoritative state, isolated recovery, verification, and outbound safety.

[Audit investigation and export](../operations/audit.md) - Exact filters, bounded export, file verification, and recovery-point comparison.

[Command repair](../operations/idempotency-repair.mdx) - Safe inventory, plan, and release of proven interrupted protocol-v2 reservations.

[Automatic housekeeping](../operations/housekeeping.md) - Eligible classes, retention settings, migration boundary, signals, and troubleshooting.

[PostgreSQL topology decision](decisions/0004-postgresql-topology-and-provider-neutrality.md) - Why the service requires PostgreSQL without selecting an SRE provider or HA topology.
