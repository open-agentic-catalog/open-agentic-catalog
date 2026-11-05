---
title: "ADR-0013: Audit evidence and verification"
---

- Status: Accepted

## Question

What audit evidence must the v1 Catalog retain, how is it kept useful and bounded, and what integrity claims can the service make without becoming a compliance ledger or adding another storage system?

## Context

Catalog transitions and sensitive reads need attributable, bounded audit evidence. Shared record validation and database guards keep that evidence consistent without retaining private payloads. Audit investigation and exports must enforce current tenant permissions.

Audit, notification, and operational telemetry answer different questions. Audit records attributable reviewed activity. The outbox hands a specifically classified post-commit notification to a concrete consumer. Logs, metrics, and traces diagnose service behavior. Duplicating every audit action into every channel would increase retained sensitive data and background work without improving authority.

PostgreSQL can make an audit row atomic with a Catalog transition and can prevent application roles from changing it. It cannot prove that its schema owner, storage administrator, or a compromised backup omitted or rewrote rows. A digest embedded only in the same database or export has the same trust boundary as the bytes it describes. Exact comparison with a backup also requires both sides to represent the same database snapshot; a wall-clock cutoff alone is insufficient while concurrent transactions can commit earlier-timestamped rows after an export snapshot was taken.

## Decision

Audit operations use current tenant action permissions under [scoped authorization](0022-local-postgresql-authorization.md). Authentication, OAuth ceilings and tenant isolation remain independent checks.

### Record and transaction boundary

Use one small dependency-neutral `internal/audit` package for the service-owned record, execution context, validator, action registry, and bounded encoder. Keep searchable investigation keys in the existing dedicated PostgreSQL columns. Require `format_version` 1 in persistence, API, and export. Unversioned records or incomplete attribution are unsupported.

Version 1 details use a bounded common envelope for non-indexed attribution and action-specific typed facts. A breaking meaning or shape requires another record version. Unsupported development state is refused without manufacturing attribution.

The Go validator enforces the complete encoded-record ceiling plus field, nesting, and action-specific fact rules. The reason rule trims leading and trailing Unicode whitespace. It requires valid UTF-8 and 1 through 2,000 Unicode code points, and rejects Unicode control characters.

A prospective insert trigger independently requires version 1 for every new row. It enforces per-column byte limits, digest-or-empty syntax, JSON-object details, and a conservative serialized-details byte limit. The immutability guard prevents updates, so no update path needs to retrofit these checks. PostgreSQL storage size is not treated as identical to JSON response encoding.

An owning repository appends audit alone or audit plus independently classified outbox records inside its existing transaction. The version-1 boundary binds tenant, target, normalized committed time, and allowed audit action to the exact store operation. It enforces the required, forbidden, or mapped notification type. Notification references contain only the reviewed action, target, state digest, and publication host.

The boundary includes schema-owner identity provisioning and realm transitions. A source test can map to source-revision activation; publication rollback can map to publication activation. These mappings preserve distinct audit meanings.

No generic unit-of-work layer is added. A state-changing HTTP command commits its transition, evidence, and exact protocol-v2 response together. Replay adds no evidence. A worker records each distinct domain result it commits and links the service executor to bounded retained Job facts. Audit-only actions create no outbox row.

Protocol-v2 idempotency repair is audit-only because it has no post-commit consumer. Retained internal rows are non-deliverable. Repair plans and results use version 2 and bind the exact reviewed command. Unsupported plans must be regenerated and reviewed. Repair does not depend on webhook enablement.

### Coverage and minimization

Retain audit for the reviewed transition and access matrix: successful in-scope state or focused operator transitions, exact private VersionRevision and Catalog-document access, retained source-inspection and raw-observation access, audit reads and exports, and authorized source operations that reach the hardened outbound boundary. The source and publication actions that are useful to external tenant integrations receive separately reviewed public event types. Leases, heartbeats, automatic-maintenance admission, ordinary API-safe reads, public discovery, and ordinary Catalog or ARD search do not become a universal audit stream.

A sensitive-read event means that the service authorized and successfully prepared the named bytes or bounded result slice and committed its audit row before writing the response. It cannot prove that the client or network received those bytes.

For outbound source inspection, test, or diagnostic search, the service attempts to append the audit outcome after the external response. A PostgreSQL transaction cannot atomically include a remote read. Diagnostic evidence names only configured sources that reached the outbound fetch boundary. Pre-boundary rejections do not count as contacts. A complete mix of accepted and valid empty results is successful, not partial.

If the final append fails, the service withholds result bytes and emits only a redacted operational failure signal. This does not provide distributed atomicity.

The indexed actor column has one unambiguous version-1 principal meaning. An authenticated tenant action uses the Catalog membership ID. A worker uses a code-owned service-executor ID. A schema-owner command uses PostgreSQL `session_user`.

Unversioned records are rejected. Actor type and realm ID remain bounded attribution facts when applicable, while `current_user` separately records a schema-owner command's effective authority. Service-populated fields otherwise retain stable IDs, bounded enums and counts, and versioned state or evidence digests. Service-populated facts exclude these values:

- Credentials, tokens or claims, external identity subjects or issuers, and raw keys.
- Request or key digests, command execution tokens, queries, and query digests.
- Documents, source bodies, URLs, headers, IP addresses, user agents, and unbounded arrays.

A required reason remains bounded user-authored text that the service deliberately retains. Client documentation warns that it is not a secret field. The service does not claim to detect and remove sensitive content from user-authored reasons.

### Investigation, export, and restore comparison

Actors with current tenant `audit.read` permission can page audit records through exact actor-principal ID, action, request-ID, paired target, and half-open time filters applied in PostgreSQL before the limit. The cursor binds every normalized filter, authorization digest, order, and limit. OAuth scope enforcement remains a separate ceiling on the current permission. Indexes are added only for demonstrated query plans.

The `POST /api/v1/audit-exports` command requires current tenant `audit.export` permission, idempotency, and the `audit.export` OAuth scope when enforced. The Security Reviewer template supplies the permission; custom roles can supply it too. The command synchronously returns a closed export bounded by row count and encoded bytes.

It accepts the same exact filters as investigation plus one mandatory half-open time range. The service establishes one repeatable database snapshot in the protocol-v2 transaction, chooses a server cutoff before selection, requires the exclusive range end not to exceed it, and orders the selected rows by `(occurred_at ASC, id ASC)`.

It canonically encodes the ordered `events` array under a service-owned v1 profile without converting retained JSON numbers through `float64`. It digests that exact encoding and appends an access event at or after the cutoff. The exact response commits to the ordinary 24-hour replay row.

A zero-row range is valid. Its first and last positions are absent. The access event is outside its own range, and no export domain object, background Job, or object-store copy is created.

Row, record, and encoded-response ceilings are code-defined contract limits. Deployment settings require a measured need for configuration. Aggregate row or response overflow returns a stable range-too-large problem and no partial export. The caller must narrow the range. A malformed or individually oversized record fails closed and is never omitted. Narrowing the range cannot repair corrupted evidence.

The export's embedded manifest proves only bounded structural self-consistency. A `catalogctl` verifier can either validate that file or use a read-only connection to compare an isolated restored database with a separately trusted manifest without appending more audit or starting outbound work.

The supported exact-restore workflow quiesces ordinary Catalog API and worker writers while it captures the manifest and backup recovery point. The export command's own audit and replay rows fall outside the compared range. The implemented restore sequence records one external recovery-point identifier, binds the separately trusted manifest to that same quiesced interval, and rejects a time cutoff without matching provenance. Backup tooling and SRE own the recovery-point identifier and trusted storage binding.

Audit exports provide no database-only tamper detection, audit sequence or hash chain. Their service-owned encoding profile does not use [RFC 8785](https://www.rfc-editor.org/rfc/rfc8785.html). The separate [ANS verifier](0024-optional-ans-registration-verification.md) uses RFC 8785 for signed JSON. An export signer or immutable-log provider requires a separate assurance requirement.

## Consequences

- New audit writes have one versioned, bounded, testable shape without forcing domain packages to import each other.
- Only attributed version-1 audit records are supported. Incompatible retained development state fails validation without being rewritten, falsely enriched or automatically deleted.
- Audit-only work does not consume outbox capacity, and new public event types still follow the full-drain rollout required by ADR-0012.
- Exact response replay bounds the synchronous export's memory and persistence cost. If the export exceeds an aggregate limit, callers must narrow its range or apply more exact filters. One malformed or oversized record fails closed and is never omitted.
- Sensitive-read evidence records authorization and preparation, not network delivery.
- An external request followed by database failure has an unavoidable evidence limitation that is reported honestly rather than hidden behind a false atomicity claim.
- Housekeeping reclaims only mechanically eligible operational state and does not delete audit evidence. A future audit-retention requirement must define authorization, preservation, legal-hold interaction, and verification explicitly; privacy automation and compliance-specific export remain deferred.

## Rejected alternatives

### Record every request

Rejected because public, malformed, and ordinary read traffic could amplify sensitive durable state and cost without a defined investigation requirement. Redacted operational telemetry is the appropriate channel for those paths.

### Create an internal outbox row for every audit action

Rejected because an outbox is a work handoff, not a second audit log. A record with no consumer is redundant queue traffic and misleading evidence.

### Build asynchronous export storage

Rejected because the required export can be safely bounded and replayed synchronously. A Job, export table, object store, cleanup workflow, and download authorization would add complexity without a demonstrated larger-export requirement.

### Claim tamper evidence from an in-database chain or embedded digest

Rejected because a sufficiently privileged database actor could rewrite both data and chain. Stronger assurance requires an independently controlled trusted manifest or immutable external log and an explicit operating requirement.

## Verification

- Unit and PostgreSQL tests cover the versioned validator, action registry, immutable Job-context enrichment, redaction, byte/nesting bounds and unsupported-format rejection.
- Attribution tests cover stable membership, database session-versus-effective-role identity, safe construction failures and equal external subjects in different realms.
- Persistence tests cover direct-insert guards, RLS, exact worker insert authority, transaction rollback, replay-once evidence and audit-only classification.
- Binding tests cover allowed store actions, reviewed audit-to-notification mappings, normalized paired timestamps, exact closed notification references and exact retry initiator binding.
- Manual synchronization tests cover exact source, revision, trigger, stable initiator and deduplication binding for new or safely reused work.
- Contract tests keep OpenAPI, operation-specific reason bodies, scope ceilings, exact public-event type membership, action and event registries, event JSON Schema, examples, and handler authorization aligned.
- Sensitive-read tests prove that an audit failure returns no response bytes and that the recorded action means prepared access rather than delivery confirmation.
- Federation tests cover pre-boundary rejection, egress denial, transport, HTTP, parse, partial, success, final-audit failure, diagnostic replay, and normative-interface failure precedence over optional fallback probes without retaining queries, URLs, or response bodies.
- Export tests cover stable-snapshot selection, lossless JSON numbers, array-encoding digest, aggregate and single-record limit failures, cutoff exclusion, exact replay, duplicate keys, malformed and unknown versions, self-consistency, and honest non-tamper-proof reporting.
- Restore fixtures reject a manifest without matching recovery-point provenance, reject a different recovery point, and accept an exact comparison captured during the same quiesced-writer interval.
- Current-baseline tests require attributed version-1 audit, refuse unsupported retained development state without conversion, reject obsolete repair plans and prove protocol-v2 repair creates audit but no outbox row.
