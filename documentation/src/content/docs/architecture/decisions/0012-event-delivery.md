---
title: "ADR-0012: Internal outbox and optional webhooks"
---

- Status: Accepted

## Question

What event machinery does the Catalog actually need, and must a deployment operate an external event broker?

## Context

Some Catalog state changes need reliable work after the database transaction commits. An administrator may also want another system to learn that a resource was published, revoked, synchronized, or otherwise changed. Neither need makes an external event platform part of the Catalog's system of record.

Covered workflows write independently classified domain events to a PostgreSQL transactional outbox with the state change that caused them. A signed [CloudEvents](https://cloudevents.io/) webhook adapter provides optional delivery. The public, internal, and audit-only classification covers core transitions. Webhook administration, delivery outcomes, and sensitive delivery reads retain their own audit evidence.

Capacity, retention, monitoring, and owned Compose/Helm scenarios have maintained local/CI tests. Keep local results under ignored `output/`; operators separately qualify their selected deployment. Requiring Kafka, a schema registry, or another broker would add installation, operations, credentials, networking, monitoring, and recovery work without a confirmed consumer or throughput requirement.

## Decision

PostgreSQL remains the only required event infrastructure:

1. A state transition and every reviewed public or internal outbox event it requires commit in the same PostgreSQL transaction. Audit-only transitions do not manufacture an outbox row. The outbox is the reliable handoff for concrete post-commit notification work; it is not another source of truth.
2. The worker processes outbox records using leases and idempotent handlers. Process loss may cause another attempt, but cannot acknowledge an uncommitted state change.
3. Signed HTTPS webhooks are the only external event-delivery mechanism in the current design. They are an optional capability configured by an administrator for an actual receiver.
4. A deployment with no webhook receiver remains a complete Catalog. It requires no webhook endpoint, webhook key, external event service, or webhook readiness dependency.
5. Webhook delivery uses at-least-once retry semantics and is not guaranteed to be globally or per-resource ordered; finite retry limits do not guarantee receipt. Every event has a stable ID, type, subject, and time. Receivers deduplicate by the CloudEvent `(source, id)` pair and fetch the current authorized state from the Catalog API when they need authoritative data.
6. Retry, dead-letter evidence, tenant isolation, safe outbound delivery, explicitly scoped send concurrency and bounded pending backlog, secret rotation, and observable failure are required when webhooks are enabled. Generic replay infrastructure and long-lived event streaming are not required. After receiver connectivity or key configuration is repaired, the receiver can observe later events or retrieve current authorized state from the Catalog API.
7. No Kafka, broker adapter, schema-registry service, broker deployment asset, or broker qualification is part of this design. A demonstrated streaming requirement must revise this decision before a broker integration is added.
8. Notification-backlog saturation does not reject an otherwise valid Catalog mutation. Bound notification admission and storage, retain a bounded durable indication of missed notifications and required reconciliation, and expose safe operational signals. Preserve the atomic domain, required audit, and classified outbox boundary; database and required-evidence failures still fail the mutation. Do not silently discard notifications or move an unlimited backlog into a second missed-event store. Receivers recover current authorized state, not an implied historical replay stream.

Public-event admission is serialized at the PostgreSQL insert boundary. Pending public events are limited to 1,000 per tenant, including claims and deferrals. At saturation, the required outbox record commits already acknowledged and updates bounded loss counters. Capacity and terminal-recipient loss share one tenant row. An authorized reconciliation read provides current-state refresh checkpoints, without a reset or replay API.

Only fixed owner-authorized triggers write the loss counters. Runtime roles have no direct write or trigger-function execution authority. Delivery retention is parent-aware and separate from authoritative domain/audit retention. The pending-event bound is not a recipient quota or a total database-size limit.

Delivery-work bounds require explicit units and scope, including synchronous test sends and all live or draining replicas. A serial dispatch loop bounds simultaneous worker sends, not requests per second. One synchronous test slot per API and serial worker sends supply the implemented concurrency bound. Measure any additional rate promise before adopting it; it does not mandate a distributed per-endpoint limiter, parallel dispatcher, or new component. Delivery-evidence retention belongs to ADR-0015's application-owned housekeeping, while SRE owns replica budgets, database capacity and monitoring integration. Receiver outages must not become required-readiness or liveness dependencies.

Audit remains separate from the outbox and webhooks. Audit records explain who did what and must satisfy the bounded evidence and verification contract in ADR-0013; an audit action does not automatically define a deliverable event, and a received or retained webhook is not Catalog audit evidence. Protocol-v2 idempotency repairs are audit-only because they have no post-commit consumer. Retained internal rows are non-deliverable. Another internal type requires a concrete consumer.

A new public type updates the Go registry, checked-in event JSON Schema, notification reference, OpenAPI webhook selector, examples, and contract tests together. Emission begins only after old dispatchers have fully drained: an older worker deliberately acknowledges an unknown type as non-deliverable, so emitting a newly registered type during a mixed-worker interval could lose the notification. Audit-action additions alone do not require that event rollout.

### External scanner integration

Scanner execution stays outside the Catalog API and worker. Neither process has a scanner adapter, automatic assessment write-back or publication-blocking scanner policy. Software-assessment capability remains unavailable. External scanning does not enable it or establish Catalog trust.

A webhook receiver must verify the signature and event envelope, durably accept and deduplicate work before acknowledgement, and reconcile current authorized state. Use the existing [Go publication-report receiver](../../../../../../examples/publication-report/README.md) for a complete example. An event does not authorize resource execution or contact with a listed runtime. Collection credentials, scanner execution and external reports remain outside Catalog. Any future assessment integration needs an explicit producer, result, freshness and revocation policy before it can affect publication or trust.

The optional [MCP Scanner example](../../guides/mcp-scanner.mdx) collects definitions from explicitly configured endpoints after signed release notifications. It also accepts saved captures for offline scanning. Native scanner and complete Compose qualification remain pending; receiver tests use an executable double. Its reports never change Catalog records or publication eligibility.

## Why this decision

- It preserves atomic, recoverable post-commit work without introducing another required datastore.
- It gives integrations a simple notification mechanism when they actually need one.
- It preserves the same standalone application contract across environments. Compose and Minikube are disposable qualification fixtures, not a required production topology; deployers choose infrastructure and integrations.
- It avoids promising ordering, replay, retention, or throughput semantics for which no consumer requirement exists.
- It keeps the Catalog API and PostgreSQL state authoritative after duplicate, delayed, or missed notifications.

## Consequences

- PostgreSQL and the worker are sufficient for standalone operation.
- Webhook routes, configuration, secrets, delivery storage, metrics, and release checks are conditional on webhook enablement.
- The checked-in versioned CloudEvents JSON Schema is sufficient for the webhook contract; no separate schema-registry service is needed.
- A public-type expansion has an explicit old-worker drain point before any new emitter starts.
- Operators choose bounded retry and delivery-evidence retention within the service's supported ranges.
- Consumers must be idempotent and cannot derive correctness from arrival order.
- The production configuration gate and deployment templates must remain capability-aware so disabled webhooks never require a key or receiver and never perform outbound delivery. The worker still acknowledges public outbox records without delivering them; enablement must follow the runbook's drain ordering.

## Rejected alternatives

### Require an external event broker

Rejected because no current Catalog workflow needs streaming throughput or broker-specific delivery semantics, and the dependency would materially complicate every deployment.

### Make webhooks mandatory

Rejected because a standalone Catalog and its direct API clients do not need an event receiver. Optional integration cannot become a readiness dependency for the core service.

### Guarantee per-resource delivery order and general replay

Rejected because consumers can deduplicate notifications and retrieve authoritative current state. Those guarantees require additional queue coordination, retention, and operator tooling without a demonstrated use case.

## Verification

- Saturation tests prove Catalog mutations still commit their required evidence, pending public work and coalesced loss state remain bounded, missed notifications are explicitly observable after restart, and another tenant can progress. Receiver reconciliation restores current authorized state after capacity loss without ordered replay. Database or required-evidence failure still prevents a successful mutation response.

- Every in-scope tenant domain or focused operator transition has its matching audit evidence, and every reviewed public or internal transition has its independently classified outbox evidence; a rolled-back transition has neither, while an audit-only transition has no outbox row. Leases, heartbeats, acknowledgement, and other implementation bookkeeping remain outside the public and audit registries.
- Process loss and lease expiry retry safely without acknowledging an uncommitted event.
- The service and worker pass startup, readiness, mutation, and backlog tests with webhooks disabled and no webhook key present.
- Webhook-enabled tests prove signed delivery with bounded retries, `(source, id)` deduplication guidance, retry, dead-letter evidence, redaction, tenant isolation, safe fetching, bounded backpressure, and key rotation.
- Delayed, duplicate, and out-of-order test deliveries do not require a receiver to reconstruct state; the receiver can retrieve the current authorized resource.
- No service configuration, container, Compose service, Helm dependency, readiness check, or release test requires an event broker.
