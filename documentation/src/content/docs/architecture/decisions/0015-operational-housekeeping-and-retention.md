---
title: "ADR-0015: Operational housekeeping and retention ownership"
---

- Status: Accepted

## Question

Which Catalog records should be reclaimed automatically, which retention periods should be deployment settings, and when is a privileged operator command actually warranted?

## Context

The Catalog already creates several fundamentally different kinds of PostgreSQL state. Idempotency responses, source change previews, and unused source inspections carry an explicit expiry. Conditional-retrieval validators and automatic-source admission markers are derived coordination state for one immutable source revision. Terminal Jobs are a client-visible operational history, while acknowledged outbox rows are a completed delivery queue. Catalog revisions, publication snapshots, source provenance, used inspections, and audit events are governed history.

These record classes have different authority and retention rules. Disposable operational state needs automatic expiry and reclamation. Governed history remains retained. A privileged deletion plan for every expired response would add permanent receipts and require another scheduler.

PostgreSQL autovacuum reclaims dead tuples after deletion; it does not select expired live rows. The application owns bounded eligibility checks and deletes. ARD defines discovery behavior, not the Catalog's internal retention policy or a cleanup HTTP API.

Use ordinary PostgreSQL [queue locking](https://www.postgresql.org/docs/18/sql-select.html#SQL-FOR-UPDATE-SHARE) for cooperative cleanup and [autovacuum](https://www.postgresql.org/docs/18/routine-vacuuming.html) for tuple reclamation. No external retention framework is required.

## Decision

### Make expiry semantics independent of reclamation

Every read or command continues to enforce the row's stored expiry and state even when physical cleanup is delayed or unavailable. Cleanup is eventual storage reclamation, never the authority that makes an expired preview invalid or a completed replay response reusable. An expired released protocol-v2 key is also reclaimable atomically by an incoming new request, so correctness does not depend on the housekeeping cadence.

Only command protocol v2 is supported. Incompatible state is rejected before runtime startup under the [current-format boundary](0017-first-release-baseline.md). Pending unreleased protocol-v2 reservations remain recovery state until their command finishes or the existing reviewed repair proves interruption. An unexpired released protocol-v2 row remains bound to its original request and authorization projection. Signed cursors contain their own bounded validity and have no stored cleanup row.

### Run routine housekeeping in the existing worker role

Run one independently supervised, context-cancellable housekeeping loop in `catalogd worker`; the combined in-memory development role uses a no-op adapter because its process-local state has no durable storage pressure. It runs in Compose and Kubernetes without a second image, credential, Helm workload, or external scheduler. Multiple worker replicas cooperate through bounded database transactions. Housekeeping failure reports a fixed degraded component and safe low-cardinality metrics, but it does not turn process liveness into a dependency check or prevent the Job and outbox loops from recovering their own work. One serial loop gives each class one bounded opportunity per cycle. A replay backlog cannot starve preview, source-state, Job, or outbox reclamation.

Each record class has a separate repository operation, eligibility predicate, supporting index, batch ceiling, per-cycle work ceiling, and metric class. Classes whose delete authority is new have a database lifecycle guard. Conditional-retrieval validators are the deliberate exception. Synchronization already needs exact delete authority to invalidate a bad current validator. Housekeeping reuses it through a bounded obsolete-revision query. A cleanup-only trigger would break valid current-revision invalidation.

A partial index is used only when a stable non-time predicate excludes permanently ineligible states; the preview expiry path correctly uses an ordinary `(expires_at, id)` index because every preview may eventually qualify. Automatic selection orders stable logical identities by eligibility time and identity, with `FOR UPDATE SKIP LOCKED`. It can safely skip a contended eligible row. No operator reviewed an exact set, and a later cycle retries the row.

The delete and its eligibility recheck share one transaction. A crash before commit deletes nothing. A crash after commit needs no replay receipt. Repeating the operation is naturally idempotent. Physical `ctid`, `OFFSET`, whole-table counts, external calls, and unbounded cascades are not used.

The worker receives only the exact additional column reads, marker updates, and deletes required by implemented classes. Replay cleanup selects and deletes through the non-null unique identity formed by tenant and `reservation_id`. It never reads a client idempotency key, request digest, authorization digest, response headers, or response body. The worker gains no general audit-read access. Cleanup eligibility comes from the owning row's relational state, expiry, and references. The originating version-1 audit event remains independently retained.

Database guards independently reject an ineligible direct mutation for replay rows, previews, inspections, automatic-admission state, Jobs, and outbox rows. Retrieval-validator safety relies on the bounded transaction's exact obsolete-revision and live-Job recheck because its pre-existing current-validator invalidation cannot be trigger-restricted to housekeeping. These controls defend against application defects; they do not claim that a table owner cannot alter its own guard.

Housekeeping uses the worker's existing cross-tenant `BYPASSRLS` boundary only in bounded transactions. Every join includes the exact tenant key. No selected private value is logged or exported.

Housekeeping produces cycle and failure counters, duration histograms, reclaimed-row counters, batch-saturation counters, last-success state, and the oldest eligible age actually observed by an indexed bounded selection. Labels use only fixed class and outcome vocabularies.

It does not run a whole-table count merely to report backlog, claim to count rows skipped under lock, or emit identifiers, retention values, or private fields as labels. It emits no audit event or outbox row for each batch. Deterministic expiry of disposable state is internal maintenance. Permanently retained evidence for every removed row would defeat the storage bound. Existing audit records for the originating command remain unchanged.

### Automatically reclaim only mechanically eligible state

The mechanically reclaimable classes are:

1. Expired completed protocol-v2 idempotency rows, plus expired released incomplete protocol-v2 rows. Neither pending state nor an unexpired released binding is eligible.
2. Expired source change previews. A preview is short-lived command input rather than governed history; its independently retained creation event carries the bounded consequence facts after the preview document is gone. No audit join, new marker, or preview-use relation is needed.
3. Expired source inspections not referenced by a source revision. Connect and connectivity revalidation bind the exact inspection with a tenant-inclusive foreign key and recheck expiry while locking it in the source-revision transaction. Used inspections remain retained.
4. Conditional-retrieval validator state and automatic-admission markers for a non-current or terminally removed source revision after no live source-sync Job can use that exact revision. Immutable retrieval documents, attempts, snapshots, occurrences, and observations are not coordination state and remain retained.
5. Terminal Job leaves and acknowledged outbox rows after their configured retention eligibility. A Job with a retained child is kept until the retry lineage can be removed leaf first. An outbox event is kept while unpublished, leased, or referenced by any delivery row.

   Delivery cleanup runs as a separate class before outbox cleanup. Preserve recipient-suppression evidence while the parent event remains unfinished; an acknowledged, unleased parent can also leave abandoned `waiting` deliveries after endpoint or filter changes or disabled draining. Recognized synthetic test deliveries require separate eligibility because they intentionally have no outbox parent. This cleanup extends the existing bounded worker loop, not a new operator deletion workflow, and does not reclaim endpoint configuration or audit history.

Terminal Jobs are visible through the Control API and acknowledged outbox rows are useful for operational investigation, so their minimum retention periods are explicit deployment configuration with strict bounds and safe defaults. Terminal-Job retention cannot be shorter than the fixed 24-hour command-replay window. A retained command response must not name a Job that housekeeping was already permitted to remove. The fixed 24-hour replay window, 10-minute preview authorization window, and 15-minute inspection authorization window remain service contracts rather than tenant or deployment settings. Batch sizes, per-cycle work, two-second statement timeouts, 250-millisecond lock timeouts, and class order remain code-defined safety limits until measurement demonstrates that a deployment knob is necessary.

Housekeeping assigns an immutable `cleanup_after` marker to an unmarked terminal Job or published, unleased outbox event. The marker uses database time plus the effective retention setting.

It does not require API cancellation, Job completion, outbox publication, or other writers to consume worker policy. Eligible terminal rows follow the same conservative rule: the row receives the full configured interval after the first successful bounded marking transaction, which is never earlier than its terminal transition. Existing markers never change. A configuration change applies only to rows marked afterward and cannot accelerate an already marked row.

Delivery-referenced outbox rows remain unmarked until all their eligible delivery evidence has been reclaimed. Exact terminal synthetic tests may be reclaimed without an outbox parent, but arbitrary missing or mismatched parents remain protected. Delivery evidence receives the existing acknowledged-outbox retention interval after marking. Its parent later receives its own full interval. No new policy field is required.

Database guards permit only null-to-future marker enrichment; every other retained field stays unchanged. Deletion requires a valid marker, terminal state, elapsed database time, and safe class-specific references.

### Keep configuration small and explicit

The existing `worker` group contains exactly three housekeeping fields:

| Field | Default | Accepted range | Environment variable |
| --- | --- | --- | --- |
| `worker.housekeepingInterval` | `5m` | `1m` through `1h` | `CATALOG_WORKER_HOUSEKEEPING_INTERVAL` |
| `worker.terminalJobRetention` | `168h` | `24h` through `2160h` | `CATALOG_WORKER_TERMINAL_JOB_RETENTION` |
| `worker.acknowledgedOutboxRetention` | `24h` | `1h` through `720h` | `CATALOG_WORKER_ACKNOWLEDGED_OUTBOX_RETENTION` |

Go duration syntax has no day suffix, so examples use hours.

These values are service-wide worker policy, not tenant-controlled settings. Changes use the ordinary coordinated worker rollout. There is no hot reload.

These values are operational defaults, not an SLA or compliance policy. The five-minute default is much slower than the 250-millisecond Job poll and limits ordinary reclamation lag. The one-minute floor prevents hot polling. The one-hour ceiling keeps backlog discovery regular.

Seven days gives time to investigate client-visible Jobs and exceeds the mandatory 24-hour replay floor. An acknowledged outbox row defaults to one day. Retained audit evidence is the governed action record; the completed delivery queue is operational state.

The 90-day Job and 30-day outbox ceilings keep these tables operational rather than accidental archives. A demonstrated need can revise those versioned validation bounds; it should not be bypassed with an arbitrary duration or converted into tenant retention policy.

Keep defaults, sample profiles, environment allowlists, diagnostics, Compose, Helm, tests and the configuration reference consistent. There is no `enabled` switch. Required storage hygiene must not silently disappear in production.

There are no per-class enable flags, per-tenant periods, cron expressions, concurrency controls, batch-size knobs, or exact deletion-time promises. The interval controls how often the worker offers a bounded cycle. The two retention settings control minimum eligibility. Contention, outages, references, and bounded batches can retain rows longer.

### Runtime ownership

Every inspection consumer records a tenant-inclusive use link. Cleanup markers, indexes and class-specific guards enforce eligibility. The PostgreSQL worker uses `internal/housekeeping` and `internal/platform/database`; `catalogd all` supplies the no-op adapter. The [runtime privilege manifests](../../operations/database-roles.md#exact-grants-and-extension-access) preserve separate delivery, outbox, inspection and replay authority.

The worker operations surface exposes the fixed `housekeeper` component and bounded aggregate metrics. The packaged Compose functional scenario creates eligible and protected rows, starts the worker, and proves automatic reclamation without a manual cleanup command.

### Keep governed evidence out of core cleanup

Housekeeping does not delete Catalog revisions, publication snapshots, source revisions, source snapshots, observations, occurrences, immutable retrieval documents or attempts, used inspections, audit events, tenants, memberships, or other governed history. Non-current does not mean disposable. Those records stay retained unless a later explicit product and assurance requirement defines retention authority, legal-hold interaction, export prerequisites, reference disposition, authorization, and verification.

### Do not add a cleanup product API or deployment scheduler

Ordinary clients and tenant administrators do not choose when internal replay, preview, validator, Job, or outbox storage is reclaimed. A cleanup HTTP API would expand authorization and runtime delete surface without supplying a reliable scheduler. `catalogctl` remains the right boundary for migrations, proof-driven repair, diagnostics, and isolated restore work, not recurring service hygiene.

Do not add a Kubernetes CronJob, system cron entry, PostgreSQL scheduling extension, or cleanup Job in the Catalog's own Jobs table. The existing worker already owns durable background operation and runs in both maintained environments. A second scheduler would duplicate credentials and health behavior. It could miss or repeat runs and would still need the same database concurrency guards. Do not add table partitioning, a generic retention framework, a receipt table, or application-driven vacuuming without measured scale evidence.

## Consequences

- Routine reclamation happens without human intervention and behaves the same in Compose and Kubernetes.
- Command correctness does not depend on whether the housekeeping loop ran recently.
- A worker compromise gains narrowly guarded cleanup capability, so migrations, exact privilege manifests, restore verification, and negative role tests must change together.
- Every inspection consumer writes a tenant-inclusive use link. Schema changes follow the coordinated migration workflow and current-format compatibility boundary.
- Configured terminal-work retention begins when housekeeping first marks an eligible terminal row and is therefore at least the configured interval after the terminal transition, not a promise that deletion occurs at an exact instant. Contention, worker outage, and reference blockers can retain a row longer.
- Automatic batches can return an inconsistent candidate view by design; this is safe for independent eligible rows and is not reused for investigation, export, repair, or future policy-selected evidence deletion.
- Deletion creates dead tuples that ordinary PostgreSQL autovacuum reclaims. Housekeeping does not promise immediate operating-system disk shrinkage.
- ARD wire behavior and conformance do not change, and no ARD route or payload is added.
- The [housekeeping runbook](../../operations/housekeeping.md) documents the implemented metrics, settings, commands, and failure behavior.

## Rejected alternatives

### Require `catalogctl plan/apply` for every class

Rejected for routine expiry because it makes normal storage safety depend on repeated human action, needs a separate scheduler to become automatic, and adds reason, digest, replay-receipt, and audit machinery without a policy decision to review. It remains a viable pattern only for a future explicitly authorized governed-evidence deletion.

### Add an authenticated cleanup API

Rejected because cleanup is not a tenant product action or an ARD operation. It would broaden API-role deletion, authorization, idempotency, OpenAPI, and attack surface while still requiring something to call it periodically.

### Add a Kubernetes CronJob or external scheduler

Rejected because Compose is also maintained, CronJob execution is approximate, and a second workload would duplicate image, database credentials, security context, probes, configuration, and release evidence. The database operation must already be safely repeatable across replicas, so the existing worker is the smaller portable owner.

### Enqueue cleanup in the Catalog Jobs table

Rejected because routine Job cleanup would generate more Jobs that themselves need retention and would add payload, lease, retry, and audit behavior to a small database maintenance loop. Add a durable Job only if measured work later cannot fit the bounded loop.

### Delete governed source and audit history

Rejected because eligibility depends on preservation policy, references and legal-hold requirements that remain outside the current feature. Routine storage hygiene does not authorize destruction of governed evidence.

### Use one generic retention engine or table partitioning

Rejected because the classes have different state machines and blockers. Explicit small queries and guards are easier to review and test. Partitioning and a generic policy framework require measured volume and operational ownership that do not yet exist.

## Verification

- Focused unit tests cover runner validation, fixed class order, continuation after one class fails, immediate and scheduled cycles, scheduled failure, cancellation, observer aggregation, all three configuration defaults and bounds, role composition, and memory-profile no-op behavior.
- Real PostgreSQL 18 tests prove the ordinary and partial index definitions and validity, plus empty, exact-limit, overflow, concurrent two-store `SKIP LOCKED` progress, database-time boundaries, opaque replay-row selection through `reservation_id`, exact worker privilege and API denial, cross-tenant joins, lifecycle-guard denial, and preservation of pending, unexpired, current, referenced, or malformed rows. Use representative-backlog `EXPLAIN` and throughput measurements. Do not make a tiny integration fixture assert PostgreSQL's cost-based plan choice.
- Idempotency tests cover the protocol-v2 cleanup matrix, unsupported-protocol rejection, current authentication and authorization on replay, owner-only pending repair, preservation of pending rows, and atomic new-request reuse of an expired released row without waiting for cleanup.
- Federation tests prove preview expiry without worker audit reads, retained preview-creation evidence, inspection-use tracking, source-revision and inspection locking, unsupported-state refusal, exact live-Job blockers, and preservation of used inspections and immutable retrieval evidence.
- Job and outbox tests prove the replay-window configuration floor, terminal-only retention, immutable null-to-future database-time marking for eligible rows, leaf-first lineage deletion, retry-parent revalidation, prospective configuration changes, delivery-reference non-marking, and no effect on claims, retries, cancellations, dispatcher acknowledgement, or webhook-disabled draining.
- Together, runner, dependency-supervisor, metrics, and worker-operations tests cover component and policy reporting, degradation and recovery, required-database readiness, cancellation, and prompt drain without making cleanup a liveness dependency.
- The isolated packaged Compose flow creates expired and blocked fixtures in its disposable PostgreSQL, observes automatic cleanup and retained negative cases through bounded operator assertions, restarts the worker, and proves recovery without a manual cleanup call.
- Migration and PostgreSQL tests cover every added index definition and validity. Restore verification covers the current columns, privileges, triggers, and retained-state contracts. Configuration contract tests cover JSON, environment, diagnostics, Compose, and Helm propagation without exposing these worker settings through a product API. `make check`, `make verify`, `make test-integration`, `make test-functional`, and `make security` cover the implemented housekeeping behavior. The maintained workload harness supplies measurement; passing correctness tests does not establish sustainable cleanup throughput or a universal capacity claim.
