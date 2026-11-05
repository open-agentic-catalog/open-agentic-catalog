# Coordinated Catalog deployment

`make helm-deploy` is an operator-side maintenance workflow for the shipped application chart. It uses the existing owner-only migration hook; it adds no runtime migration authority, cluster controller or application RBAC. A brief outage is deliberate.

Use [installation and maintenance](../../documentation/src/content/docs/operations/maintenance.mdx#coordinated-release-deployment) for prerequisites, commands and failure recovery.

The caller must select an existing Kubernetes context/namespace, release, local chart and complete values file. Helm 3.15+ and kubectl are required, together with the repository's Go toolchain when using Make. Values own the intended API/worker replicas, including retries after they have been drained to zero.

Target chart/values are copied to a private temporary directory before validation and removed on exit. Retain your reviewed original artifacts and image digests for recovery; this directory is not a deployment journal.

The tool renders and performs a server-connected Helm dry-run, verifies existing Deployment ownership/names/selectors, and rejects HPAs targeting either runtime. The UI and PgBouncer Deployments are separate from the two drain targets. It scales API to zero and waits for all selected Pods to disappear, then does the same for workers. Only then does Helm run the migration hook and deploy the target.

It verifies/restores the configured replica counts and waits for rollout readiness. `helm-deploy-check` performs the same preflight without draining or applying. It does not prove database admission, Secret contents or runtime readiness.

Only one deployment operation may run at a time. The operator must suspend external GitOps/reconciliation and autoscaling before invoking it; the database advisory lock does not serialize deployment operations. The tool does not suspend controllers, create namespaces, provision databases/identities, reset data, run smoke commands, perform data repair, install dependencies, download charts or roll back SQL.

For an explicitly documented migration that requires stopped-runtime repair, use the manual owner sequence before starting the application.

`HELM_DEPLOY_TIMEOUT` defaults to `10m`, admitted range `1s` through `1h`. Drain, Helm deployment and readiness have bounded phases; preflight is bounded at two minutes and the whole invocation at four phase bounds plus two minutes. A failed Helm apply makes a bounded best-effort request to set both target replica counts to zero and reports whether that succeeded.

Inspect remaining Pods/hooks yourself after interruption or failed cleanup. No `--atomic`, automatic rollback or old-image restart is used. Diagnostics expose fixed stages, not raw Helm/kubectl output that may contain secrets.

## Verification

- `make test-unit` runs isolated target, ownership, ordering, cancellation, error and local-input checks. No cluster or subprocess is used by these tests.
- `make helm-check` includes real local Helm package/render tests for defaults, UI and PgBouncer values under the `helm` tag.
- `make test-minikube` uses the compiled coordinator for installation, failed-hook recovery and retained upgrades with two API/worker replicas. It tests preflight refusal without Pod replacement, a real ledger barrier proving zero old Pods while the hook waits, failed migration with both roles stopped, startup failure after successful migration, retry from zero with the configured count and repeat application preserving governed smoke state.
- `make test-minikube-autoscaling` adds real active/inactive targeting-HPA refusal, an unaffected same-namespace HPA and Pod, the coordinated-upgrade failure controls and readiness before HPA reinstatement. No external HPA recreator runs in that fixture.
- Existing PostgreSQL integration tests own checksums, advisory locking, atomic migration failure and exact-grant reconciliation. The coordinator does not reimplement those assertions.

Local development uses `make compose-up`: build and validate both target runtime configurations before stopping API then worker; existing migration/provisioning dependencies gate startup, and Compose waits for healthy services. Native launcher units cover validation, drain and startup failure ordering; packaged tests retain real database/runtime verification.
