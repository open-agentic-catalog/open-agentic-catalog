---
name: recovery-and-repair
description: Plan, investigate and execute supported Open Agentic Catalog restore and repair procedures, including interrupted commands and derived-state recovery. Use for retained-state recovery or disposable recovery drills; ordinary SQL changes, deployment and read-only health diagnosis use their owning workflows.
---

# Recovery and repair

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md`, the current persistence baseline (`CONTEXT.md#postgresql-transactions-and-migrations`) and only the runbook for the requested operation below. Preserve plan-only, review-only and unit-only scope. A request to diagnose failure does not authorize repair or reconnecting a restored deployment.

## Establish the target and evidence

Identify the exact environment, compatible binary/image, configuration, database authority, tenant scope, recovery point or reservation, and current writer/ingress/egress state. Use protected deployment inputs for credentials; never print DSNs, keys or private records. Distinguish retained data from an invocation-owned disposable fixture and retain the existing authorization for the selected operation. Resolve missing target or mutation scope before applying changes; do not add another approval step when that exact action is already authorized.

Inspect current reports and owning code before choosing a remedy. PostgreSQL starting, a free advisory lock, a command exiting successfully or a derived index being complete each proves something narrower than recovery. Unknown formats, ledger/checksum mismatch, unsupported authority, unexplained integrity findings or uncertain old-writer fencing stop the dependent mutation. Preserve evidence instead of resetting data or weakening guards. Use debugging and performance (`.agents/skills/debugging-and-performance/SKILL.md`) for diagnosis and database changes (`.agents/skills/database-changes/SKILL.md`) for an authorized implementation fix.

## Follow the operation's actual contract

| Operation | Procedure and decision boundary |
| --- | --- |
| Failed migration or grant reconciliation | Follow maintenance (`documentation/src/content/docs/operations/maintenance.mdx#upgrade-and-failed-migration`) and the exact migration plan. Keep required runtimes drained, correct reported inputs and rerun the compatible apply command. Do not edit numbered SQL or ledgers. Helm rollback does not reverse committed migrations. |
| Isolated restore | Follow backup and restore (`documentation/src/content/docs/operations/backup-restore.mdx#verify-an-isolated-database-restore`). Keep writers and ordinary ingress/egress disabled; reconcile intended schema/grants, then verify every independently inventoried tenant through a gap-free continuation chain. A verifier exit of zero means it emitted a report, not that findings are acceptable. Review all pages, integrity findings and operational warnings before repair. |
| Interrupted idempotent command | Follow idempotency repair (`documentation/src/content/docs/operations/idempotency-repair.mdx`). Establish that the original execution ended and fence an old writer after failover. List/plan are non-mutating; apply requires the unchanged eligible plan digest and reason. Completed commands use retained response replay. After an ambiguous apply, re-plan and correlate retained state/audit evidence instead of blindly retrying a stale plan. |
| Derived search projection drift | Follow search-projection repair (`documentation/src/content/docs/operations/search-projection-repair.mdx`). This is a bounded per-tenant direct apply, with no plan/dry-run command. Process eligible drift until the terminal result, preserving authoritative observations. Re-verify the whole tenant after restore repair; completion does not prove every result or existing projection correct. |
| Webhook ciphertext-key recovery | Follow key replacement (`documentation/src/content/docs/operations/webhooks.mdx#deployment-encryption-key-replacement`). Stop all send-capable runtimes, including disabled drainers; retain required old/new keys through partial traversal and backup recovery. Preserve endpoint history and signing secrets. An attestation flag does not stop processes, and encryption-key replacement is distinct from receiver signing-secret rotation. |

For recovery-point evidence, use the audit binding/comparison procedure (`documentation/src/content/docs/operations/audit.md#bind-and-check-a-recovery-point`). An export's internal checksum does not authenticate a backup. Use independently retained provenance and the matching trusted manifest when available; never claim missing evidence was reconstructed. Compare before repair and keep writers quiescent across verification.

Review restored custom roles, group membership, active and revoked assignments and the current authorization revision before reopening ingress. An older backup can restore access revoked after capture; repeat provisioning does not reconstruct missing revocations or authorize restored traffic. The maintained recovery fixtures also verify exact scoped decisions and current-authority command replay.

Do not reset leases, acknowledge uncertain deliveries or replace publication snapshots to make a restore pass. Supported claim recovery and semantic reindexing follow their own contracts. Restoring traffic requires the runbook's completed checks and authorization for that transition; a successful drill grants neither production recovery authority nor an RPO/RTO promise.

## Verify and hand off

Select the fixture for the changed recovery procedure. Use `make test-backup-restore` for logical backups. Use `make test-postgres-pitr` for physical backup and WAL recovery. Use `make test-semantic-restore` for optional semantic state and `make test-minikube-backup-restore` for the Helm restore procedure.

Use `make test-postgres-failover` for writer changes. Use the affected default-off or webhook packaged scenario in the functional harness (`test/functional/README.md`) for interrupted-command/key maintenance. Reuse existing assertions and post-code verification (`.agents/skills/post-code-verification/SKILL.md`); these are functional checks and remain unrun under a unit-only limit.

Validate an actual retained deployment using its authorized state and audit reads. Smoke creates records and is not an automatic recovery health probe. Report the selected target and safe build identity, applied batches, complete verification evidence, ambiguous/failed outcomes, cleanup and remaining traffic restrictions. Keep raw evidence protected/ignored and update the owning record or runbook when its procedure changes.
