# Architecture

This is the repository's architecture entry point. The [design principles](documentation/src/content/docs/architecture/design.mdx), [architecture chapters](documentation/src/content/docs/architecture/overview.mdx) and [accepted ADRs](documentation/src/content/docs/architecture/decisions/index.md) contain the detailed design and diagrams. The [application context](CONTEXT.md#implemented-features-and-defaults) records what works. Optional local `todo/todo.md` records proposed work.

A design constraint on a future feature is not a claim that it exists.

## Core decisions

- **Operational simplicity and least surprise.** Use standard application and infrastructure mechanisms, explicit configuration and predictable failure behavior. Deployment infrastructure owns PostgreSQL HA; Catalog owns safe reconnection and bounded application recovery. Reuse existing driver/pool and retry policies rather than adding competing controllers or retry layers. See the [design philosophy](documentation/src/content/docs/architecture/design.mdx#operational-simplicity-and-least-surprise).
- **One modular Go service.** API and worker roles scale independently around the same domain model and PostgreSQL authority. The optional UI and remote CLI commands are API clients; operator commands use separately scoped database authority.
- **Governed metadata, not execution.** Agents, MCP Servers, Skills and Plugins have explicit adapters and lifecycles. Discovery never grants installation, invocation, trust or approval.
- **Immutable evidence.** Separate logical Resources, version labels, content revisions, listing revisions, submissions, releases, source observations and publication snapshots. Corrections append evidence instead of rewriting approved history.
- **Tenant isolation at every boundary.** Verified identity resolves Catalog-owned membership and grants. All tenant-owned queries and relations retain the tenant predicate; PostgreSQL RLS and exact role grants enforce the persistence boundary.
- **Atomic commands and durable work.** A mutation, its audit, classified outbox records and exact replay response commit together. Replays reauthorize; revision changes require optimistic concurrency. External calls do not hold interactive transactions open.
- **Snapshot-backed discovery.** Public reads use only an active, validated, host-bound snapshot. Publication and rollback recheck eligibility. Invalidating a release or its required evidence withdraws the affected snapshot rather than exposing stale authority.
- **Controlled federation.** Administrators select sources, scopes, audiences and disclosure within deployment egress policy. Partial synchronization preserves the last complete view. External observations never become local governed resources without explicit adoption.
- **Bounded work before hydration.** Queries, archives, fetches, queues, pages and publication construction have enforced bounds. Core search works without embeddings; derived projections never become authoritative.
- **Explicit optional integrations.** Hybrid search, webhooks, trace export and monitoring remain independently optional. Hybrid search uses one configured embedding endpoint and rebuildable pgvector state, with lexical fallback. Managed media and software assessment remain unavailable. Do not add speculative providers, brokers, caches or coordination services.
- **Portable runtime and recoverable state.** Runtime code does not depend on Kubernetes. Separate migration authority, exact database roles, writer readiness, graceful drain and lease recovery remain required under any launcher.

## Where to change the design

| Topic | Maintained detail |
| --- | --- |
| Domain types, review, releases and search | [Domain model](documentation/src/content/docs/architecture/domain.mdx) |
| Private item feedback | [Feedback design](documentation/src/content/docs/architecture/decisions/0020-private-item-feedback.md) and [reader/admin guide](documentation/src/content/docs/guides/feedback.mdx). |
| Human resource ratings | [Ratings decision](documentation/src/content/docs/architecture/decisions/0023-human-resource-ratings.md) and [rating guide](documentation/src/content/docs/guides/ratings.mdx). |
| Package ownership and command flow | [Components](documentation/src/content/docs/architecture/components.mdx) and colocated source READMEs |
| HTTP and versioning | [API architecture](documentation/src/content/docs/architecture/api-surfaces.md), [API conventions](documentation/src/content/docs/reference/api-conventions.md) and [OpenAPI](api/openapi/control-v1.yaml) |
| Schema, roles, migrations and storage | [Persistence](documentation/src/content/docs/architecture/persistence.md) |
| Public snapshots and external delivery | [Publication](documentation/src/content/docs/architecture/publication.mdx) |
| Source inspection, reconciliation and live discovery | [Federation](documentation/src/content/docs/architecture/federation.mdx) |
| Identity, egress and threat controls | [Security](documentation/src/content/docs/architecture/security.mdx) |
| Runtime roles, replicas and the UI | [Deployment](documentation/src/content/docs/architecture/deployment.mdx) and [UI architecture](documentation/src/content/docs/architecture/catalog-ui.mdx) |
| Jobs, audit, webhooks, telemetry and recovery | [Operations](documentation/src/content/docs/architecture/operations.mdx) |
| Standards interpretation and pinned inputs | [Standards](documentation/src/content/docs/reference/standards.mdx) and [profiles](schemas/PROFILES.md) |

Change the owning chapter and applicable contracts, code and tests together. Use an ADR for a durable architecture choice with meaningful alternatives, not an ordinary implementation detail. Keep setup commands in [Developing locally](DEVELOPING.md), verification procedures in the [testing guide](documentation/src/content/docs/contributing/testing.md) and execution order in the tracker.

## Compatibility

[ADR-0017](documentation/src/content/docs/architecture/decisions/0017-first-release-baseline.md) defines the current-format first-release baseline: there is no released predecessor. Reject incompatible development formats without inventing provenance or deleting retained data. Applied numbered migrations remain immutable; future supported upgrades must preserve their actual API and stored-data contracts.
