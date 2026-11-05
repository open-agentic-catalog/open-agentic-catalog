---
title: "Architecture decisions"
sidebar:
  label: Decision overview
  order: 0
---

These decisions describe the current Catalog architecture, its rationale, alternatives and consequences. They separate Catalog responsibilities from infrastructure that operators supply. They also distinguish portability requirements from tested deployment support.

An `Accepted` decision defines an architecture choice; it does not establish that every conditional capability is available. Use the API reference and feature guides for current behavior. Keep unselected proposals in the repository backlog.

## Decision set

| ADR | Question | Decision status |
| --- | --- | --- |
| [0001](0001-deployment-portability-and-support-boundary.md) | Where can the service run, and what does the project support? | Accepted |
| [0003](0003-runtime-packaging-and-local-test-environments.md) | Which container, Compose, Minikube and Helm workflows does the project maintain? | Accepted |
| [0004](0004-postgresql-topology-and-provider-neutrality.md) | How is PostgreSQL supplied without tying the service to a managed provider? | Accepted |
| [0005](0005-object-storage-modes.md) | What belongs in PostgreSQL, Git, OCI, or optional media storage? | Accepted |
| [0006](0006-identity-and-tenancy.md) | Who authenticates callers, and how is a tenant selected? | Accepted |
| [0007](0007-secrets-and-credential-resolution.md) | Does the service need Vault or another secret manager? | Accepted |
| [0008](0008-federated-source-connectivity.md) | Which public and private source networks can federation reach? | Accepted |
| [0009](0009-public-and-private-discovery.md) | Is ARD discovery public, authenticated, or both? | Accepted |
| [0010](0010-federation-execution-modes.md) | Who controls federation, and how does ARD Search use that configuration? | Accepted |
| [0011](0011-search-and-embedding-tiers.md) | Why does core search stand alone, and how does optional hybrid retrieval use one configured model? | Accepted |
| [0012](0012-event-delivery.md) | What event machinery is required, and are external notifications optional? | Accepted |
| [0013](0013-audit-evidence-and-verification.md) | What audit evidence is retained, and what can its verification prove? | Accepted |
| [0014](0014-bounded-synchronous-work.md) | How is synchronous product work bounded and canceled without a quota system? | Accepted |
| [0015](0015-operational-housekeeping-and-retention.md) | Which cleanup is automatic, configurable, or policy-dependent, and which process owns it? | Accepted |
| [0016](0016-static-documentation-with-starlight.md) | How are searchable service docs delivered without an application dependency or duplicate authority? | Accepted |
| [0017](0017-first-release-baseline.md) | Does the first release support earlier development formats? | Accepted |
| [0018](0018-optional-catalog-browser-client.md) | How does an optional browser UI authenticate and use the Catalog without changing backend authority? | Accepted |
| [0019](0019-native-agent-plugin-packages.md) | How are complete Plugin packages inspected and discovered without hosting or executing them? | Accepted |
| [0020](0020-private-item-feedback.md) | How can authenticated users submit private item feedback without changing publication, discovery scores or immutable evidence? | Accepted |
| [0021](0021-logging-observability-and-audit.md) | How should configurable service logs, safe failure diagnostics, metrics, traces and durable audit work together? | Accepted |
| [0022](0022-local-postgresql-authorization.md) | How can configurable roles, groups and namespace-private access use the existing PostgreSQL authority? | Accepted |
| [0023](0023-human-resource-ratings.md) | How do human ratings retain version identity, privacy and separate peer summaries? | Accepted |
| [0024](0024-optional-ans-registration-verification.md) | What can optional ANS registration verification establish, and which authority must operators supply? | Accepted |
| [0025](0025-tenant-operational-settings.md) | Which tenant operations can administrators change without replacing processes? | Accepted |

## Maintain the current decisions

Update the owning ADR in place when an accepted choice changes. Keep its identifier and route. Describe the complete current decision, without amendment journals, supersession chains or accounts of earlier implementations. Git retains the edit history.

Preserve the problem, rationale, meaningful alternatives, consequences and current limits. Link related decisions when they cover different responsibilities. Add an ADR only for a distinct architecture question that has no existing owner.

Reconcile affected design chapters, contracts, configuration, deployment guidance and tests in the same change. Keep implementation work in the backlog and dated measurements in test records. Updating an ADR does not authorize a breaking released contract, data conversion or deployment change. Released API and stored-data compatibility requirements still apply.

## Scope already established

The Catalog does not implement these capabilities:

- Managed artifact security or general advanced trust and governance. Offline ANS verification and explicitly requested private registration assessments are the narrow exception in ADR-0024.
- External penetration testing, privacy assurance or legal assurance.
- Compliance-specific retention, legal hold, privacy-request automation or contractual deletion workflows.

These exclusions do not remove the Catalog's baseline safeguards. Authentication, tenant isolation, authorization, audit, safe fetching, operational cleanup, dependency readiness and accurate capability reporting remain required. See [security controls](../security.mdx) for their architecture boundaries.
