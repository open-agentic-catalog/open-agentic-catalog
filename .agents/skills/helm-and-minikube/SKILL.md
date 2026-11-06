---
name: helm-and-minikube
description: Review, update, lint, package and test Open Agentic Catalog Helm charts, and deploy or diagnose the owned Minikube scenarios. Use for chart values/templates, Helm upgrades and Kubernetes qualification; image-only or Compose-only work uses build and deployment.
---

# Helm and Minikube

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md`, the chart index (`deployments/helm/README.md`) and the affected application (`deployments/helm/open-agentic-catalog/README.md`) or monitoring (`deployments/helm/open-agentic-catalog-monitoring/README.md`) chart guide. Preserve review-only, plan-only and unit-only limits. A lint request does not imply deploying a cluster; an authorized chart implementation includes its applicable verification.

## Review and update the owning chart

Trace the changed input through `values.yaml`, `values.schema.json`, helpers/templates, rendered environment or mounts, and the actual runtime consumer. Update those owners, the chart README and affected representative `ci/` values together. Reuse Helm contract tests (`test/helm/README.md`) for defaults, enabled/disabled options, invalid inputs and upgrade renders; extend semantic assertions rather than snapshotting every manifest. Rendered environment tests use the service's typed role validators, so valid YAML alone is insufficient. CI values are test inputs, not production presets.

Preserve stable names/selectors, distinct role identities and Secret references, restricted Pod security, private worker operations, role-specific probes/drain and bounded resources. Disabled options must render no unwanted resources, mounts or dependency requirements. Review configuration checksums and no-op reapplication; changing externally managed Secret/ConfigMap contents needs the documented rollout because Helm cannot hash unread external contents.

Keep the application and monitoring charts independent. Neither installs PostgreSQL or an identity provider. Monitoring defaults off, uses its own narrow collector RBAC and external credentials/claims, and cannot become an application readiness dependency. Shared monitoring assets belong in `deployments/monitoring/`; install the assembled archive, not the incomplete monitoring source directory. `make helm-package` packages both charts with explicit `CHART_VERSION` and `APP_VERSION`; `make monitoring-package` assembles just the companion. Review actual archive contents and image tag/digest selection without rewriting source metadata or publishing artifacts.

Use build and deployment (`.agents/skills/build-and-deployment/SKILL.md`) when runtime configuration or image mechanics also change, and dependency maintenance (`.agents/skills/dependency-maintenance/SKILL.md`) for tool/image version selection. Read current compatibility constraints and Make pins instead of inventing another supported-version matrix.

## Preserve migration and upgrade ordering

Inspect the pre-install/pre-upgrade account, configuration and Job hooks, their ordering, failure retention and exact retry cleanup. Only the dedicated schema owner applies forward migrations and reconciles the exact runtime grants; API/worker Pods do not gain that authority. Read the current persistence baseline (`CONTEXT.md#postgresql-transactions-and-migrations`), actual migration plan and drain requirements (`documentation/src/content/docs/operations/database-roles.mdx#migration-drain-and-provisioning-requirements`) before changing an upgrade procedure.

Hooks do not drain existing workloads. A required full drain happens before the migration hook; an intermediate Helm upgrade to scale replicas down runs its hook too early. Helm rollback or `--atomic` cannot undo committed database migrations. Use database changes (`.agents/skills/database-changes/SKILL.md`) for SQL/grant changes, preserve failed-hook evidence and never reset retained data to make an upgrade pass.

For an explicitly requested installation or update outside the disposable harness, select the exact kube-context, namespace, release, artifact and values. Follow the deployment runbook (`documentation/src/content/docs/operations/deployment.mdx`) or monitoring procedures (`documentation/src/content/docs/operations/helm-monitoring.mdx`), verify existing inputs and the migration boundary, and retain authorization for that scope. Never use the ambient current context as an implicit deployment choice.

For application release deployment, use `make helm-deploy-check` followed by `make helm-deploy` with those explicit inputs under the coordinated maintenance procedure (`documentation/src/content/docs/operations/maintenance.mdx#coordinated-release-deployment`). The check validates selected inputs and ownership without changing workloads; it does not establish database credential acceptance. The deployment command drains API then worker, waits for old Pods to disappear, runs the owner migration hooks and restores the target values' replica intent. Raw Helm upgrade is not this coordinated procedure. Preserve the documented external reconciler/autoscaler preconditions and forward recovery after failure.

## Select source and rendering checks

Use post-code verification (`.agents/skills/post-code-verification/SKILL.md`) for shared lint/analysis and AGENTS.md's additional gates. The maintained Helm checks are:

| Need | Gate |
| --- | --- |
| Strict application lint plus chart semantics, configuration, rejection and packaging tests | `make helm-check`; includes `helm-lint` and `helm-test`, including `helm-package-test` and companion-chart contracts. |
| Kubernetes resource schemas plus the complete Helm/container source checks | `make deploy-check`; includes `helm-check`, `helm-schema-check`, `monitoring-schema-check` and `container-check`. Requires the container engine and schema-source access. |
| Minikube harness assertions without creating a cluster | `make test-minikube-contract`; includes local functional socket cases, so it is not a pure-unit gate. |

Use narrower `make helm-lint`, `make helm-test` or `make helm-render` while iterating. Do not repeat a complete constituent gate immediately before its aggregate without a changed input or unresolved failure. Tagged compilation is not execution; cluster-free Helm/process/socket tests remain functional. Under a unit-only limit, use the existing isolated selections and report real-boundary checks as unrun.

## Deploy and test with the owned Minikube harness

Read the harness guide (`test/minikube/README.md`) and maintained prerequisites (`documentation/src/content/docs/operations/deployment.mdx#maintained-local-minikube-verification`). Check host capacity, tools, cached digest-bound images and any explicit preload archive before starting. Use the maintained Make targets and their preflight; do not substitute an existing cluster or weaken version/image checks. `CATALOG_ENV_FILE=/dev/null` omits workflow env-file settings, not inherited environment variables.

When the Helm runtime boundary changes, run the basic and topology gates required by AGENTS.md, then add the affected optional scenario:

| Gate | Boundary qualified |
| --- | --- |
| `make test-minikube` | Install, failed migration hook/retry, identities/probes, smoke, retained state, checksum rollout and no-op reapplication. |
| `make test-minikube-upgrade` | Focused three-node coordinated drain/hook ordering, failed migration/startup recovery, cross-node replica intent and retained state; reports `upgradeOnly: true`, excluding broader topology checks. |
| `make test-minikube-topology` | Replica placement, PDB/eviction/drain, node loss, replay/lease takeover and enforcing Calico network isolation. |
| `make test-minikube-ui` | Optional packaged UI, subpath probes and shared sessions; not browser JavaScript or human OIDC login. |
| `make test-minikube-replicas` | Three UI/API/worker replicas, concurrent integrity, replacement and bounded traffic; separate from multi-node loss. |
| `make test-minikube-peers` | Federation between two independent Helm releases with separate namespaces, PostgreSQL stores and credentials; distinct from shared-database replicas and physical replication. |
| `make test-minikube-monitoring` | Companion enable/disable, discovery, dashboards, isolation and retained external PVCs. |
| `make test-minikube-tracing` | Cross-replica/process causality, export credentials/trust, collector outages and disablement. |
| `make test-minikube-pgbouncer` | Optional one-instance session pooler, two API/worker replicas, direct migration hooks, private TLS, enforcing policy, replacement and explicit direct-return/disablement. |
| `make test-minikube-agntcy` | Fresh pinned Directory qualification with separate namespaces/databases, current Catalog build/version checks, native inventory/paging/filters, artifact overlap, conformance characterization, persisted empty federation, private TLS and enforced isolation. Requires explicit `agntcy-tools-install`; see the selected scope (`test/interoperability/agntcy/README.md#fresh-owned-deployment`). |
| `make test-minikube-vault` | Optional external static Vault/VSO delivery, database/source rotations, scoped denial, provider recovery and owned removal; requires explicit `vault-tools-install`. |
| `make test-minikube-vault-cleanup` | Two smaller owned runs: injected post-sync failure and real SIGTERM, each requiring independent cleanup and explicit partial-scope reporting. |
| `make test-minikube-semantic` | Hybrid retrieval, model replicas, provider egress/fallback and disablement; follow the embedding prerequisites (`test/embeddings/README.md`). |

Each runtime target already depends on Helm and harness checks. Run cluster scenarios sequentially without concurrent Compose/network changes. The harness owns a private profile, home, kubeconfig, images, credentials and port-forwards, with bounded diagnostics and cleanup. Investigate failed phases using that invocation's context; never delete other profiles, prune Docker globally or delete retained namespaces/PVCs to recover a test.

Record source/dirty state, chart values, built and running image identities, tested platform, assertions and cleanup. Check both the final report and exit status: successful assertions do not cancel a cleanup failure. Keep raw output ignored and durable evidence in the owning record. Report missing prerequisites and unrun scenarios accurately; local qualification is not a production-capacity or distribution guarantee.
