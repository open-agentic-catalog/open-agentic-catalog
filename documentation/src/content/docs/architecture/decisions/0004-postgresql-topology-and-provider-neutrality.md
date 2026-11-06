---
title: "ADR-0004: PostgreSQL topology and provider neutrality"
---

- Status: Accepted

## Question

Which PostgreSQL capabilities does Catalog require, and which topology and provider choices belong to the deployment operator?

## Context

The catalog needs a transactional authority for tenants, resources, immutable revisions, source snapshots, publication state, jobs, audit, and idempotency. PostgreSQL is already the fixed database architecture because transactions, row-level security, JSON, and lexical search can coexist without a second authoritative system. Optional pgvector support adds semantic retrieval without changing that authority.

That decision does not require a managed database. Managed, operator-managed, and externally hosted PostgreSQL can expose the same connection contract. What matters to the service is the PostgreSQL version, required extensions, role boundaries, TLS, backup behavior, and availability - not who operates it.

## Decision

Keep PostgreSQL 18 at the current minor release as the production system of record and make its topology provider-neutral. PostgreSQL 18 is the supported major; adding another major requires the same migration, persistence, RLS, failure, backup, and upgrade evidence.

The application consumes three DSNs with distinct authority:

| Role | Purpose | Required boundary |
| --- | --- | --- |
| Migration | Apply reviewed forward migrations and reconcile runtime grants | Dedicated Catalog schema owner; does not serve traffic or belong to either runtime role. |
| API | Tenant-scoped request transactions | Directly authenticated `LOGIN NOINHERIT NOBYPASSRLS`; no assumed role, role membership, schema ownership, migration-ledger access, or elevated cluster rights; exact table, column, and function manifest only. |
| Worker | Cross-tenant leased background work | Directly authenticated `LOGIN NOINHERIT BYPASSRLS`; no assumed role, role membership, or schema ownership; exact leased-work, federation, publication, outbox, and optional-webhook manifest only. |

Direct PostgreSQL remains the default. [Optional PgBouncer](../../operations/database-roles.mdx#connection-pooling-boundary) uses session pooling because API and worker commands hold session advisory locks across transactions. The runtimes keep separate login identities; schema-owner operations connect directly and stay outside pooler authentication. Operators explicitly route runtime DSNs and verify TLS on both hops; enabling the pooler does not rewrite DSN Secrets. The chart deploys one private replica with `Recreate` replacement. Runtime admission rejects advertised non-session modes; absent status does not qualify an older pooler.

`catalogctl migrate apply` accepts the exact existing API and worker role names. Both must differ from each other and from the current migration identity. Before changing the schema, the command validates their attributes and rejects incoming or outgoing memberships. It holds one session advisory lock across all pending forward migrations and grant reconciliation. Each migration commits in its own forward transaction.

The current migration identity must own the active Catalog schema directly or through PostgreSQL's `pg_database_owner` binding for a database it owns. Reconciliation removes runtime and effective `PUBLIC` authority on schemas, tables, sequences, Catalog functions and application-visible extension functions. It removes unsafe global and schema-specific defaults for the dedicated migration owner. It then installs the versioned service manifest and verifies the effective result. A reconciliation failure after a migration commit leaves runtimes stopped and is repaired by correcting the reported database role, extension ownership, or grant problem and repeating the same command.

The API and worker repeat exact role and effective-privilege verification at startup. They never self-grant, migrate, or repair privileges. `SELECT`, `INSERT`, and `UPDATE` are column-explicit rather than table-wide, no runtime grant includes delegation authority, and startup rejects a manually added grant option. A new table, column, sequence, or function remains unavailable until the owning code change updates the reviewed manifest; newly created functions do not inherit PostgreSQL's default `PUBLIC EXECUTE` authority.

The manifest treats extensions as a separately owned boundary. Reconciliation removes `PUBLIC` and runtime execution from every installed `pgcrypto` function. It also removes execution from every non-PL/pgSQL extension function in `pg_catalog` and every extension function in another schema that either runtime can use. This includes locally installed pgvector functions. An unrelated extension hidden in a schema neither runtime can use remains outside the Catalog boundary.

For `pgcrypto`, reconciliation grants only `digest(bytea, text)` to both runtimes and `digest(text, text)` to the API. These are the only permitted `pgcrypto` overloads.

Provisioned semantic tables enable a separate exact pgvector manifest for each runtime role. Reconciliation grants no pgvector execution when the semantic tables are absent. The grants remain required while the semantic tables exist, even when semantic search is disabled. Runtime verification rejects execution of unlisted application-visible extension functions. It also rejects delegation and direct `PUBLIC` grants. PostgreSQL's built-in PL/pgSQL handlers in `pg_catalog` remain outside this application inventory.

Local Compose installs pgvector through the checked-in PostgreSQL image and initialization contract. The maintained Minikube development profile reuses that pgvector-capable image in its local dependency layer but creates only core `pgcrypto`; semantic search remains disabled there. Production Helm values point to an SRE-supplied PostgreSQL endpoint, whether managed or self-operated. The production chart does not install a database.

A production database need not use the local fixture image, run inside Kubernetes or share the Catalog's cluster. Managed offerings must actually permit the required PostgreSQL major/extensions, direct role attributes including worker `BYPASSRLS`, membership restrictions, dedicated schema ownership and extension privilege reconciliation. If they cannot, report that incompatibility rather than weaken isolation. Sharing a PostgreSQL server does not imply sharing a Catalog object owner or applying local bootstrap scripts to another application's schema. The deployment administrator owns provisioning and verifies that application-visible extension/default-grant changes do not affect unrelated consumers.

## Required database contract

- PostgreSQL 18 is the supported major, and deployments apply current minor security and bug-fix releases.
- `pgcrypto` is required. pgvector is optional for core operation and required only when the semantic-search tier is enabled.
- Production database connections use server-authenticated TLS. SRE selects the supported PostgreSQL authentication method - workload identity where available, password, or mutual TLS - and supplies credentials and trust material through deployment secrets.
- The Catalog configuration bounds each runtime connection pool. A deployment may impose stricter connection-level statement and lock timeouts or an application name through its protected PostgreSQL DSN; ADR-0014's service-owned cooperative product-work deadline is propagated through database calls because a per-statement limit cannot bound the complete HTTP workflow.
- Migrations are checksummed, forward-only and explicit. Drain runtimes before applying contract changes that affect their invariants. There is no supported predecessor requiring development-version overlap. Future supported upgrades must preserve their declared compatibility window.
- The migration identity is dedicated to the Catalog schema and owns or can alter the extensions installed in application-visible schemas. Its default privileges are part of the service security contract because PostgreSQL function defaults are role-global rather than schema-local.
- Every Catalog schema object change updates the exact API and worker privilege manifest in the same reviewed change. Runtime roles receive no future-object defaults, sequence access, table-wide DML, schema creation, trigger management, or migration-ledger access.
- Runtime verification checks direct `PUBLIC` grants on Catalog objects and application-visible extension functions separately from a role's effective access so a redundant public grant is still rejected as unsafe drift.
- Backup, point-in-time recovery, replica/failover configuration, patching, and monitoring belong to the database operator.
- PostgreSQL-backed API and worker pools combine pgx's standard read/write-session acceptance with any protected-DSN validator. Every new physical runtime connection must report `transaction_read_only=off` before admission. Startup establishes an acceptable connection and verifies the PostgreSQL version, directly authenticated LOGIN identity, effective role attributes, absence of incoming or outgoing memberships, exact Catalog privilege manifest and required extensions.
- Readiness checks connectivity and read/write session capability with a bounded query. It does not repeat the full startup privilege audit or perform a canary write. These runtime checks do not change the generic connection contract for intentionally read-only restore and audit operators.
- SRE supplies an operator-managed read/write Service, managed-database writer endpoint or equivalent route. A multi-host DSN with `target_session_attrs=read-write` adds standard selection; it does not replace the runtime invariant.

## Why this matters

- RLS and separate roles are part of tenant isolation, not a managed-service preference.
- Database constraints and triggers protect retained history, exact parent relationships, legal current-pointer changes, terminal states, and monotonic operational fields even if a repository write is defective.
- Bounded representations use PostgreSQL inline JSON or an exact PostgreSQL document link. Inspected Skill packages retain inventory and immutable external handoff without archive bytes. [Plugin packages](0019-native-agent-plugin-packages.md) retain exact bounded manifest/component documents and inventory bound to the external archive digest. This separate exception does not permit arbitrary URL-only representations. Retained representation rows cannot be updated or deleted.
- Database availability determines catalog availability and committed-state durability.
- SRE-owned backup and failover choices determine deployment recovery behavior; the Catalog service does not set those objectives.
- Connection limits and failover behavior affect API and worker retry design.
- A provider-neutral DSN contract lets the same service run on cloud or local PostgreSQL deployments.

## Consequences

- SRE may choose a managed, operator-managed or self-operated PostgreSQL service that satisfies the complete contract. No provider brand, local image or wire-compatibility claim establishes compatibility by itself; qualification is needed only for a named provider claim.
- The project must test actual PostgreSQL 18 behavior, not every provider product or a merely wire-compatible alternative. Provider-specific qualification can be added to the release matrix.
- `make test-postgres-failover` uses actual PostgreSQL 18 physical replication to exercise one planned switchover and one abrupt fenced-primary failure through an isolated writer route. It records replay position and replication mode and verifies application readiness, retained state and recovery without restarting the API or worker. This local test does not establish a zero-loss promise for asynchronous replication or qualify an operator's failover system.
- Replication and failover are not backup evidence. Before a named production deployment is declared ready, a separate isolated point-in-time restore drill uses that deployment's selected backup mechanism and records the achieved data point and elapsed recovery phases. That environment-specific drill does not block publication of the provider-neutral image and chart.
- In-cluster PostgreSQL is acceptable for local and controlled testing. Production suitability depends on the operator's backup, failover, upgrades, and storage design.
- Compose installs pgvector, the maintained Minikube fixture uses the same capable image without creating the optional extension, and core tests also prove operation against plain PostgreSQL 18 without it.
- Semantic search cannot be enabled in production unless SRE supplies a PostgreSQL service with the pinned pgvector contract.
- Sharing the Catalog migration identity with another application's object owner is unsupported because secure PostgreSQL function defaults are configured at the owning-role level. An extension installed into an application-visible schema must be owned or alterable by the migration identity so reconciliation can remove installation-default execution grants.

See [database roles](../../operations/database-roles.mdx) and [backup and recovery](../../operations/backup-restore.mdx) for operational procedures.
