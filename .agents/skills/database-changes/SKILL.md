---
name: database-changes
description: Change Open Agentic Catalog PostgreSQL repositories, migrations, exact privileges, locking and transactional behavior with real database verification. Use for persistence changes; read-only diagnosis and isolated helper-test additions alone use their existing workflows.
---

# Database changes

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md`, the database package guide (`internal/platform/database/README.md`), migration rules (`migrations/README.md`) and the applicable role runbook (`documentation/src/content/docs/operations/database-roles.md`). Check the current persistence baseline (`CONTEXT.md#postgresql-transactions-and-migrations`) rather than copying version numbers into the change plan. Preserve any plan-only, review-only or unit-only scope.

## Trace authority and the transaction

Identify the owning domain transition or read, concrete repository callers and operator/runtime role. Follow tenant context, predicates and same-tenant parent relations through every query. Worker global candidate selection is a deliberate bounded exception; follow-up access still uses the selected tenant. Bound database selection before hydration.

Inspect the transaction, command coordinator and existing failure tests. A classified mutation, audit, outbox and replay response must retain their required atomicity; audit-only actions do not gain artificial outbox events. Keep external calls outside interactive transactions. Preserve immutable evidence, lifecycle guards, optimistic concurrency, lease ownership, cancellation and cleanup. Use existing query/transaction abstractions and fixtures; do not broaden interfaces to make SQL easier to mock.

## Coordinate schema and privileges

For schema changes, add the next forward migration and preserve applied files and checksum verification. Core and optional semantic schema have distinct setup boundaries. Consult existing migrations and current manifests to determine what changes; a repository-only fix does not automatically require DDL or a manifest version bump. For query/index tuning, performance testing and optimization (`.agents/skills/performance-testing-and-optimization/SKILL.md`) owns the measured hypothesis and comparison; this workflow owns migration and persistence correctness.

For changed schema authority, review both exact runtime manifests in `internal/platform/database/privileges.go`, affected extension-function grants and startup admission. Preserve column-explicit least privilege, direct login, role separation and forced RLS. Update affected catalog/restore inventory expectations when schema guards change. Never weaken grants, constraints, RLS or triggers to accommodate an invalid repository write.

Document migration planning/application, reconciliation failure recovery and the actual drain/rollback boundary. Runtime processes never acquire migration authority. Use the current first-release compatibility policy; do not invent a predecessor backfill or silently reset stored data. Authoring and testing migrations does not authorize applying them to retained or shared databases. Follow the owning runbook for any separately authorized operator action.

## Qualify the real boundary

Use unit testing (`.agents/skills/unit-testing/SKILL.md`) for helper decisions and error/cleanup mapping, and functional testing (`.agents/skills/functional-testing/SKILL.md`) for the SQL boundary. Extend existing integration fixtures with the cases affected by the change: successful exact-role access, cross-tenant/parent denials, missing/excess privilege rejection, retained migration checksums, fresh/repeated application, lifecycle guards, atomic rollback and lock/cancellation behavior. Check durable state and evidence after failures; a SQL string expectation does not establish PostgreSQL semantics.

Run focused tests while editing, then `make test-integration` for the owned disposable PostgreSQL fixture, or `make coverage-postgres-check` when instrumented execution is needed. Do not repeat both solely to obtain the same integration evidence. Use `make test-process-postgres` when the change affects actual CLI/API/worker startup or process composition. The `-dsn` alternatives require an explicitly owned disposable database; ambient development configuration is not sufficient selection. Preserve cleanup on failure and keep DSNs/credentials out of reports.

Coordinate ordinary lint, unit and local verification through post-code verification (`.agents/skills/post-code-verification/SKILL.md`); add API or deployment checks only for affected boundaries. Under a unit-only execution limit, report SQL/RLS/migration qualification as unrun and do not claim the persistence change is fully verified. Update the owning package/runbook and implementation status where behavior changes. Report migration/grant impact, passed checks, cleanup and unresolved deployment or repair requirements.

For optional PgBouncer connection changes, follow the pooling runbook (`documentation/src/content/docs/operations/database-roles.md#connection-pooling-boundary`). Keep runtime session pooling and distinct identities, direct owner operations and both TLS hops. Run selected `make test-pgbouncer` plus unchanged direct integration/process gates; do not reroute dynamic-role owner fixtures. The optional chart boundary additionally requires `test-minikube-pgbouncer` and its applicable ordinary chart/cluster gates. A missing pool-mode ParameterStatus is not legacy-pooler qualification, and session pooling does not multiplex warm idle pgx clients.
