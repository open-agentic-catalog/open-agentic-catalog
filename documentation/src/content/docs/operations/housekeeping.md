---
title: "Automatic housekeeping"
---

The PostgreSQL-backed `catalogd worker` reclaims disposable operational rows. It runs one bounded cycle at startup, then repeats at the configured interval. Replicas cooperate through database row locks. No Control API call, `catalogctl` command, CronJob, scheduler extension or extra credential is required. This workflow does not delete governed records or enforce legal retention.

## What is reclaimed

| Class | Eligible state | State deliberately retained |
| --- | --- | --- |
| Command replay | Expired completed protocol-v2 rows and expired explicitly released incomplete protocol-v2 rows. | Pending unreleased protocol-v2, and unexpired released bindings. An incoming command can reuse an expired completed or released key atomically without waiting for the worker. |
| Source previews | Preview rows after their fixed expiry. | The independent audit event and all source history. |
| Source inspections | Expired inspections that no source revision in the same tenant uses. | Used inspections. |
| Source coordination | Conditional-retrieval validator and automatic-admission rows for a non-current or removed source revision when no valid live synchronization Job targets that exact revision. | Retrieval documents and attempts, snapshots, observations, occurrences, source revisions, and current or in-use coordination state. Current synchronization retains its existing authority to invalidate one exact bad validator. |
| Terminal Jobs | Unmarked terminal rows receive one immutable cleanup marker. After the stored cleanup time has passed, unleased rows with no retained children are deleted before their retained parents. | Active, unmarked, leased, or child-referenced rows. |
| Webhook delivery | Delivery evidence with a matching acknowledged, unleased parent, including abandoned waiting deliveries; or an exact terminal synthetic test with no same-tenant parent. Record a future cleanup time in an immutable marker. Delete the row only after that time has passed. | Every unfinished-parent recipient, arbitrary orphan, mismatched parent, nonterminal synthetic test, endpoint configuration and audit history. |
| Acknowledged outbox | Published, unleased rows without delivery evidence receive one immutable marker. It records database time plus the effective retention interval. The rows can be deleted only after that stored cleanup time has passed. | Unpublished, leased, unmarked, or delivery-referenced rows. Delivery cleanup must finish before parent marking. |

Each selection phase handles at most 100 logical rows in stable eligibility order. It uses `FOR UPDATE SKIP LOCKED` and rechecks state and same-tenant references in one transaction. Each statement has a two-second timeout; lock acquisition has a 250-millisecond timeout.

Terminal-Job, webhook-delivery and outbox operations can each mark one page and delete one elapsed page per cycle. Source coordination divides one page between its two tables. A serial cycle gives all seven classes an opportunity before waiting.

A crash before commit rolls back that batch. A crash after commit leaves the batch complete. Housekeeping creates no audit or outbox row. PostgreSQL [autovacuum](https://www.postgresql.org/docs/18/routine-vacuuming.html#AUTOVACUUM) subsequently reclaims dead tuples for reuse.

## Configuration

Semantic indexing uses a separate supervised five-second maintenance pass. Each pass admits at most 16 Jobs, with at most 64 active automatic Jobs per configured profile. It removes at most 128 obsolete memberships and 128 unused vectors. Unreferenced vectors have a one-hour reuse grace period.

Live Jobs retain their generations. Current ready generations remain available until replacement or exclusion. After a model change, cancel queued Jobs for a retired profile only when no compatible worker should resume them. This does not delete authoritative metadata. See [hybrid-search recovery](hybrid-search.mdx#indexing-and-recovery).

| JSON field | Environment | Default | Accepted range | Meaning |
| --- | --- | --- | --- | --- |
| `worker.housekeepingInterval` | `CATALOG_WORKER_HOUSEKEEPING_INTERVAL` | `5m` | `1m`-`1h` | Delay between complete bounded cycles. |
| `worker.terminalJobRetention` | `CATALOG_WORKER_TERMINAL_JOB_RETENTION` | `168h` | `24h`-`2160h` | Minimum time after a terminal Job is first marked before it may be deleted. |
| `worker.acknowledgedOutboxRetention` | `CATALOG_WORKER_ACKNOWLEDGED_OUTBOX_RETENTION` | `24h` | `1h`-`720h` | Minimum retention after a row is first marked for cleanup. Applies to eligible delivery evidence and, later, its unreferenced acknowledged outbox row. |

These settings apply at startup across the service. Cleanup markers are immutable, so changed retention applies only to newly marked rows. It never accelerates deletion of a previously marked row. Contention, references, outages and bounded work can retain rows longer. There is no exact deletion-time guarantee. There are no per-tenant, class, cron, concurrency, batch-size or enable settings.

## Rollout and rollback

Drain API and worker processes before applying contract migrations and reconciling both exact database roles. Start only matching binaries. Unsupported development state is rejected without conversion or deletion. Restore inspection-use links and immutable Job, delivery and outbox cleanup markers with the database.

The API cannot read housekeeping-only markers. Workers receive narrow replay and ephemeral-state reads plus guarded cleanup writes. Housekeeping grants do not include replay bodies, client keys, principal-binding digests, inspection documents or audit reads. Synchronization retains its separate authority to delete one exact invalid retrieval validator. Keep normal PostgreSQL autovacuum enabled. Do not run `VACUUM FULL` for housekeeping.

## Observe and troubleshoot

Eligible webhook delivery evidence uses the acknowledged-outbox retention setting. The parent outbox row starts its own full interval only after its last delivery reference disappears. Total retention can therefore exceed two intervals.

Pending public notifications have a separate ceiling of 1,000 events per tenant, with retained counters for coalesced losses. Neither mechanism limits authoritative Catalog records, audit growth or total database size. See [webhooks](webhooks.mdx) for reconciliation and measurement units.

Worker `/status` reports the effective interval, retention values and fixed batch limit. Follow [Read a running worker's status](observability.mdx#read-a-running-workers-status) for private access and response interpretation.

The component name is `housekeeper`. A database dependency failure marks it degraded. The supervisor restarts it with bounded exponential backoff and random jitter, separately from the normal housekeeping interval. See [worker dependency restarts](../reference/configuration.mdx#worker-dependency-restarts). `/livez` remains independent of database availability. `/readyz` separately checks whether the worker can safely use its required PostgreSQL role.

Metrics report cycle and class outcomes, duration, reclaimed and marked rows, saturated batches, observed age and last-success timestamps. Observed age comes from the latest bounded successful selection. Saturation means that an operation reached its bound; it is not a whole-table backlog count. Labels contain only fixed class and outcome values.

Investigate repeated failure, sustained saturation, increasing observed age or an old last-success timestamp. Compare these signals with worker readiness, database health, lock pressure and storage trends.

If housekeeping is not progressing:

1. Check worker `/livez`, `/readyz`, and `/status`, then inspect the `housekeeper` component and PostgreSQL dependency state.
1. Confirm the worker runs the matching binary and configuration for the applied migrations. Compare the diagnostics fields `schema.embeddedVersion` and `schema.runtimePrivilegeManifestVersion` with the [current schema and grant requirements](deployment.mdx#release-sequence); follow [database roles](database-roles.mdx) if startup detects a mismatch. Do not downgrade grants to a historical housekeeping manifest.
1. Check failed cycle and batch counters before increasing any database resource. The SQL time and batch bounds are deliberate; a contended row is retried in a later cycle.
1. Verify that retained rows are genuinely eligible. Pending replay state, inspection use links, live source-sync Jobs, retry children, leases, and webhook deliveries are intentional blockers.
1. Correct the underlying database, privilege, or lifecycle defect and let automatic retry proceed. Do not bypass guards with ad hoc deletion or invent a manual trigger.

## Verification

Run unit and real PostgreSQL tests when changing eligibility, locking, privileges or lifecycle rules. The packaged Compose test creates disposable and protected fixtures while its worker is stopped. It then starts the worker and checks automatic cleanup, prospective markers, protected states and readiness. Cleanup adds no audit or outbox event for those fixtures. The test requires no manual housekeeping trigger.

```sh
make check
make verify
make test-integration
make test-functional
make helm-check
make deploy-check
```

[ADR-0015](../architecture/decisions/0015-operational-housekeeping-and-retention.md) explains the design and alternatives. Compliance-specific retention, legal hold, privacy automation, and deletion of governed history remain outside this workflow.
