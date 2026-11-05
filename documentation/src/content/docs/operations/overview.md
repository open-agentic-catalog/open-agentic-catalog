---
title: "Operations overview"
description: "Find the right runbook for installation, routine operation, diagnosis and recovery."
---

Choose a [setup](../index.mdx#choose-a-setup), then follow the [deployment guide](deployment.mdx) for PostgreSQL, authentication and network access. The [configuration reference](../reference/configuration.mdx) lists supported settings and prerequisites for optional features.

## Find the procedure for your task

| When you need to... | Start with | Important boundary |
| --- | --- | --- |
| Install the service | [Installation and maintenance](maintenance.mdx), [deployment](deployment.mdx), [configuration](../reference/configuration.mdx), [Helm values](../../../../../deployments/helm/open-agentic-catalog/README.md) | Validate your selected inputs and required dependencies; do not copy development identities into production. |
| Run containers without Compose or Kubernetes | [Docker with PostgreSQL](docker.mdx) | Mount separate role credentials, apply migrations as owner and preserve database state when replacing containers. |
| Use managed container hosting | [IBM Cloud Code Engine](ibm-code-engine.mdx), [Azure Container Apps](azure-container-apps.mdx), [Google Cloud Run](google-cloud-run.mdx) | Follow the provider CLI procedure after checking database permissions, network access and process-lifecycle requirements. These cloud deployments have not been qualified. |
| Add the browsing and administration UI | [Optional UI deployment](deployment.mdx#optional-catalog-ui), [UI architecture](../architecture/catalog-ui.mdx) | Enable the separate browser host, configure OIDC and shared session keys, and provision Catalog memberships. The API remains the authorization authority. |
| Configure API authentication and browser login | [Authentication and sign-in](sign-in.md) | Configure the API audience and Catalog identity mapping first. The optional UI uses its own confidential client and ID-token audience. |
| Grant, inspect or revoke user access | [Scoped access management](../guides/access-management.mdx) | Catalog grants are separate from issuer accounts. Revoking one assignment can leave another path effective. |
| Prepare PostgreSQL and the first administrator | [Database roles and identity setup](database-roles.md) | Keep schema-owner, API and worker credentials separate. Runtime processes never migrate automatically. |
| Upgrade or recover a failed migration | [Upgrade procedure](maintenance.mdx#upgrade-and-failed-migration) | Inspect the migration plan and drain requirements first. A previous container is not automatically a safe database rollback. |
| Rotate credentials, signing keys or trust | [Credential rotation](maintenance.mdx#rotate-credentials-and-trust), [webhook keys](webhooks.mdx#deployment-encryption-key-replacement), [security](security.md) | Different credential types have different overlap and drain rules; never remove keys still needed to decrypt retained state. |
| Connect monitoring or investigate slowness | [Health and metrics](observability.mdx), [monitoring with Prometheus and Grafana](helm-monitoring.md), [performance and benchmarking](../guides/performance-and-monitoring.md) | Connect existing monitoring or deploy the optional companion chart. Use the dashboard and scrape files under `deployments/monitoring/`. |
| Collect logs or review logging controls | [Output and limits](observability.mdx#logs-and-trace-correlation), [logging decision](../architecture/decisions/0021-logging-observability-and-audit.md) | Backend/UI support strict startup levels and text/JSON formats, defaulting to INFO text. Safe terminal/HTTP diagnostics, backend event consistency and UI dependency/session events are implemented. |
| Diagnose an unhealthy or degraded process | [Troubleshooting](troubleshooting.md) | Distinguish process liveness, required database readiness and optional component degradation before restarting anything. |
| Connect another Catalog or investigate source freshness | [Federation sources](federation.mdx) | Inspect and approve the exact upstream, then verify the completed Job and snapshot. A failed sync preserves the last complete view. |
| Enable notifications | [Webhooks](webhooks.mdx), [receiver example](../../../../../examples/webhookreceiver/README.md) | Disabled by default. Receivers verify signatures, deduplicate and reconcile current state. |
| Export distributed traces | [OTLP tracing](observability.mdx#distributed-tracing), [tracing configuration](../reference/configuration.mdx#optional-tracing-configuration) | Disabled by default. Configure one endpoint and scoped credentials/trust; export failure does not change readiness. |
| Back up or restore | [Backup and restore](backup-restore.mdx) | Deployment owners select backup methods and objectives. Qualify an isolated restore and external side-effect controls before reopening traffic. |
| Investigate evidence or a security incident | [Audit](audit.md), [incident response](incident-response.md) | Use the documented authority and bounded export path; redact secrets and private content from support evidence. |
| Repair interrupted commands or derived search state | [Idempotency repair](idempotency-repair.mdx), [search projection repair](search-projection-repair.mdx) | Inspect first, then use the exact focused operator action. Do not replace a repair with ad hoc SQL or broad deletion. |
| Review retention or remove an installation | [Retention and disclosure routes](#find-retention-and-disclosure-guidance), [housekeeping](housekeeping.md), [routine care and removal](maintenance.mdx#routine-care-and-removal) | Automatic operational cleanup is not legal retention, tenant deletion or permission to destroy shared storage. |

The [command reference](../reference/commands.md) distinguishes development Make targets from packaged operator commands. The [testing guide](../contributing/testing.md) explains local qualification without prescribing a production platform.

Start replica and failure-domain planning with the [deployment plan](deployment.mdx#plan-replicas-and-availability). It links the database budget and separates application replicas from database and pooler availability. Use [service recovery](deployment.mdx#recover-service-after-disruption) to select a failure response, resolve uncertain commands and check restored capacity.

### Find retention and disclosure guidance

[Membership offboarding](../guides/access-management.mdx#suspend-or-offboard-a-membership) explains the records that survive access removal. Use [feedback clearing](../guides/feedback.mdx#limits-and-retention) for supported text removal and [rating sharing](../guides/ratings.mdx#share-aggregate-summaries) for consent-controlled summaries. [Saved-item recovery](backup-restore.mdx#private-saved-items) explains ownership and records that an older backup can restore. Client bookmarks and exported files have a separate [VS Code lifecycle](../../../../../extensions/vscode/README.md).

Review [embedding-provider disclosure](hybrid-search.mdx#data-sent-to-the-provider) and [federation query disclosure](federation.mdx#choose-indexed-live-and-referral-participation) before enabling those paths. The [evidence-channel comparison](../architecture/operations.mdx#evidence-channels-are-intentionally-different) distinguishes logs, audit and delivery state. These feature guides own their retention and access rules; suspending a member is not a general deletion procedure.

For capacity pressure, use [storage-growth planning](maintenance.mdx#plan-storage-growth). Keep database growth, eligible housekeeping state and independently retained backups separate.

## Before making a change

Identify the exact deployment and relevant tenant or host. Confirm the configuration, binary version and database authority. Collect bounded status and diagnostics first. Do not expose DSNs, tokens, signing keys or private documents. Follow the runbook's prerequisites, required identity, drain rules and verification steps. A successful shell command alone does not establish acceptance.

Stop if any of these conditions applies:

- The target is ambiguous.
- A required prerequisite is missing.
- Role verification fails.
- A restore or repair check reports inconsistent state.

Keep affected runtimes stopped when the runbook requires it. Retain safe evidence. Correct the reported problem before retrying. Do not weaken role checks or lifecycle guards to make maintenance proceed.

## What the application owns

Catalog owns role-specific health, safe limits, graceful drain, durable work recovery, bounded operational signals and focused repair/restore checks. Deployment operators own capacity, replicas, PostgreSQL availability and backup execution, monitoring and alert routing, infrastructure recovery and on-call processes. Operators also select the service-level agreement (SLA), backup schedule and maintenance window. They set the recovery-point objective (RPO) and recovery-time objective (RTO).

For optional Vault delivery, follow the [Kubernetes Vault tutorial](vault.md). Native Kubernetes Secrets remain the default.
