---
name: durable-workflows
description: Review and change Open Agentic Catalog background Jobs, transactional outbox delivery, worker supervision and bounded housekeeping. Use for lease, retry, cancellation, crash-recovery or asynchronous side-effect semantics; ordinary SQL changes and operator repair use their owning workflows.
---

# Durable workflows

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md` and only the affected Jobs (`internal/jobs/README.md`), webhook (`internal/webhook/README.md`), housekeeping (`internal/housekeeping/README.md`) or process (`cmd/catalogd/README.md`) owner. Preserve plan-only, review-only and unit-only scope. Establish the accepted behavior and existing assertions before changing a scheduler or adding another background mechanism.

## Trace one complete attempt

Follow the initiating command through atomic enqueue/outbox creation, claim, handler/external action, evidence persistence and terminal acknowledgement. Distinguish HTTP command replay, active-Job deduplication, operator retry and webhook redelivery; they do not provide the same guarantee. Preserve the initiating actor/command and separate attempt attribution. Use the existing Job and outbox contracts, not a new broker, queue abstraction or durable telemetry system.

Identify tenant scope, revision/generation inputs, lease owner/expiry, progress and attempt bounds at each transition. Bounded global claim selection belongs to workers; subsequent reads/writes include the tenant and required ownership fence. Domain mutation, classified audit/outbox and replay response retain their transaction contract. Handler and network work run outside interactive database transactions. Use database changes (`.agents/skills/database-changes/SKILL.md`) when SQL, guards or grants change.

## Preserve failure and side-effect semantics

Lease renewal must not depend on a handler voluntarily reporting progress. Losing the heartbeat/lease stops the handler and prevents stale success or failure persistence. Check the final ownership fence after potentially slow work, not merely before starting it. Keep cancellation and timeouts propagated and resource use bounded.

Separate caller-requested cancellation from process shutdown. The former uses the authorized cooperative cancellation/terminal-state contract; shutdown stops new claims and leaves unfinished leased work recoverable. A process exit must not manufacture a user cancellation or successful completion. Retry classification, backoff and exhaustion preserve retained history; explicit retry follows the existing new-work/parent contract rather than reopening a sealed terminal record.

For webhook delivery, receiver acceptance and local acknowledgement cannot be atomic. A send followed by lost lease, failed persistence or process death can be delivered again. Preserve at-least-once, unordered behavior, stable event identity and receiver deduplication guidance. Re-read endpoint eligibility before sending, use the hardened sender, fence delivery evidence and acknowledge only when the contract permits. Do not claim exactly-once external effects from a transactional outbox.

Enabled dispatch and disabled draining have different consequences. The disabled drainer permanently acknowledges records without sending and must not overlap an enabled dispatcher; follow enable/disable operations (`documentation/src/content/docs/operations/webhooks.mdx#enable-or-disable-safely`). A receiver outage is retry/degradation, not permission to mark uncertain work delivered or make process liveness depend on the receiver.

For housekeeping, use the existing fair cadence and bounded database selection/eligibility. Retention of disposable operational state does not authorize deleting audit or governed history. For synchronization or indexing handlers, apply their owning federation (`.agents/skills/federation-development/SKILL.md`) or search (`.agents/skills/search-and-relevance/SKILL.md`) completion rules; successful work on one page/item does not make a generation complete.

## Extend the right assertions

Inspect current tests for the affected transition and add only missing observable outcomes. Useful failure points include claim contention, renewal without progress, owner loss during work, cancellation versus shutdown, external success before local persistence failure, retry exhaustion and process replacement. Assert durable state, evidence and forbidden stale writes; a handler returning an error or a counter increment alone is insufficient.

Use deterministic clocks and synchronization through existing seams. Unit testing (`.agents/skills/unit-testing/SKILL.md`) owns isolated classification, retry and state decisions; functional testing (`.agents/skills/functional-testing/SKILL.md`) owns listeners, processes, composed workflows and databases. Do not add sleeps, artificial production interfaces or duplicate entire end-to-end fixtures merely to raise coverage.

## Qualify and report

Coordinate common lint/unit/local/race gates through post-code verification (`.agents/skills/post-code-verification/SKILL.md`). Real PostgreSQL assertions qualify lease contention, tenant fences and transaction rollback. Use the affected owned packaged scenario (`test/functional/README.md`) for process interruption or webhook send/acknowledgement recovery. Select the Helm/Minikube (`.agents/skills/helm-and-minikube/SKILL.md`) topology or replica scenario when cross-process takeover, drain or deployment changes require it; not every retry-unit change requires a cluster.

Use recovery and repair (`.agents/skills/recovery-and-repair/SKILL.md`) for separately authorized repair of existing data. Do not edit lease columns or acknowledge queues as test cleanup on retained environments. Report the before/after guarantee, failure assertions, actual gates, cleanup and remaining limits. Unit-only evidence leaves SQL/process semantics explicitly unqualified.
