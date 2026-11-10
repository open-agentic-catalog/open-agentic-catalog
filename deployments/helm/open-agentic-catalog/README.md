# Open Agentic Catalog Helm chart

This [Helm](https://helm.sh/docs/) chart deploys the Catalog API, worker and migration Job from one Open Container Initiative (OCI) image. The migration Job runs before installation or upgrade. It can also deploy the [optional UI](#optional-catalog-ui) from its separate image. It does not install PostgreSQL, an identity provider, an ingress controller, autoscaling, a network-policy controller, a secret manager, or optional providers.

The application chart does not install [Prometheus](https://prometheus.io/docs/introduction/overview/), [Grafana](https://grafana.com/docs/grafana/latest/) or monitoring permissions. The [optional companion chart](../../../documentation/src/content/docs/operations/helm-monitoring.mdx) provides these in a separately installed release, while [local monitoring](../../../documentation/src/content/docs/guides/performance-and-monitoring.md) supports Compose. Production operators may use either the companion chart or their own monitoring; infrastructure collectors remain SRE-owned. `LOCAL_MONITORING` remains a Compose selector, not an application chart value.

Use the [replica and availability plan](../../../documentation/src/content/docs/operations/deployment.mdx#plan-replicas-and-availability) for defaults, operator inputs, a reviewed values overlay, connection budgets and health/drain checks. Follow [service recovery](../../../documentation/src/content/docs/operations/deployment.mdx#recover-service-after-disruption) for failure handling, maintenance links and local test limits.

## Optional PgBouncer

`pgbouncer.enabled` defaults to false. When enabled, the chart creates one restricted session-mode [PgBouncer](https://www.pgbouncer.org/config.html#pool_mode) Deployment with `Recreate`. It also creates a private Service, tokenless ServiceAccount, non-secret configuration and scoped NetworkPolicy. It needs existing authentication/TLS Secrets, writer CA and explicit writer peers. Its generated ServiceAccount must differ from all three backend accounts. This rule also applies to externally managed backend accounts.

The runtime DSN Secrets must be deliberately routed to `<fullname>-pgbouncer:6432`; migration and other owner operations remain direct. Enabling never rewrites credentials or creates PostgreSQL users.

`databaseTrustBundleConfigMap` independently mounts `ca.pem` at `/etc/open-agentic-catalog/database/ca.pem` in API, worker and migration for private database TLS. The UI receives no database input.

Same-name Secret or ConfigMap edits require deliberate consumer replacement. Follow [database certificate activation](../../../documentation/src/content/docs/operations/database-roles.mdx#activate-projected-database-certificates-in-kubernetes) for CA overlap, server replacement and old-CA removal. `make test-minikube-pgbouncer` checks projection separately from activation, fresh SQL on both hops, retained API data and worker Jobs.

Read the [pooling runbook](../../../documentation/src/content/docs/operations/database-roles.mdx#connection-pooling-boundary) for both TLS hops, exact authentication-file requirements, configurable capacities/deadlines, rollout demand, stable writer endpoints, explicit credential replacement and return to direct connections. Session pooling preserves command locks and does not multiplex idle application sessions. Default per-role capacities require PostgreSQL connection headroom; operator sizing remains necessary.

Use the [replica and dependency worksheet](../../../documentation/src/content/docs/operations/database-roles.mdx#replica-and-dependency-budget) for direct and pooled examples, including terminating overlap and client/file-descriptor bounds. `ci/pgbouncer.yaml` is a rendering fixture. Qualify local compatibility with `make test-pgbouncer`, then actual chart deployment/network enforcement with `make test-minikube-pgbouncer`; neither uses retained environments.

## Portability and customization

Install the chart into a namespace on a dedicated or shared cluster; application processes need no cluster-admin rights, Kubernetes API client or service-account token. The installer's permissions, admission policy, network isolation, ingress, database, secrets and monitoring are supplied by the deployment owner. Minikube node names, fixture DNS/subnets, local PostgreSQL and monitoring resources are not chart prerequisites.

Use the existing values for supported configuration; downstream operators may adapt manifests for their platform. Preserve the [runtime and deployment contract](../../../documentation/src/content/docs/operations/deployment.mdx#artifact-and-deployment-ownership), especially exact database roles, explicit migration ordering, restricted security, probes/drain and private worker operations. Record modified manifests and validate affected behavior: upstream tested-build evidence covers the shipped chart and recorded values, not arbitrary downstream changes. The same OCI image can be launched outside Kubernetes without this chart when an operator supplies equivalent process supervision and inputs; no separate platform package or test matrix is implied.

### Pod resolver options

`podDNSOptions` sets Kubernetes resolver options for API, worker, migration and enabled UI/PgBouncer Pods. Its default is an empty array. Each item requires `name` and accepts an optional string `value`. Changing these options changes the Pod template and requires new Pods.

For example, `use-vc` selects TCP for the Go DNS resolver:

```yaml
podDNSOptions:
  - name: use-vc
```

Allow TCP port 53 to the selected DNS service when you use this option. Resolver support depends on the process's DNS library. Catalog still checks destination policy after resolution. The local topology fixture selects this option after observed UDP DNS waits; see the [test procedure](../../../test/minikube/README.md#drain-and-disruption-qualification).

## Prerequisites

- CNCF-certified Kubernetes 1.34+ with the [documented cluster capabilities](../../../documentation/src/content/docs/operations/deployment.mdx#kubernetes-prerequisites). The chart constraint `>=1.34.0-0` accepts vendor suffixes without a distribution allowlist. Offline rendering uses `make helm-render` or an explicit `--kube-version 1.34.0`.
- A compatible PostgreSQL 18 database with `pgcrypto`, a dedicated Catalog schema-owning migration identity, and the separate restricted API and worker identities described in the database-role guide.
- Existing Secrets for the three database DSNs and the cursor-signing key.
- A production OIDC issuer and audience, one selected tenancy mode, the matching tenant or organization mapping, a public base URL, and a reviewed federation policy. Configure exact discovery-host mappings when discovery is needed; set `configEnv.CATALOG_DISCOVERY_HOSTS: '[]'` to disable discovery while retaining the authenticated Catalog API.
- Initial Catalog tenant, identity realm and administrator membership must be explicitly provisioned with the schema-owner command; an OIDC token alone grants no Catalog role. Follow [initial identity provisioning](../../../documentation/src/content/docs/operations/database-roles.mdx#initial-catalog-identity-provisioning).
- Every externally referenced ServiceAccount, image pull Secret, ConfigMap, credential Secret, and optional capability Secret must exist before the workload or migration hook that consumes it. By default the chart creates the three role-specific ServiceAccounts itself.

See the [deployment guide](../../../documentation/src/content/docs/operations/deployment.mdx), [database-role guide](../../../documentation/src/content/docs/operations/database-roles.mdx), and [configuration reference](../../../documentation/src/content/docs/reference/configuration.mdx) for the complete service contract.

## Install or upgrade

The current schema runs through migration 000047 with exact privilege manifest v27. Documentation contract tests compare these values with embedded migrations and manifest code.

When crossing migration 000039, keep API and worker Deployments at zero after migration and grant reconciliation. Run the [bounded search-projection repair](../../../documentation/src/content/docs/operations/search-projection-repair.mdx) for every tenant before starting the upgraded workloads. The migration hook does not perform this repair.

Create the namespace and deployment-owned Secrets and ConfigMaps first. Plan the forward-only database change with `catalogctl migrate plan` from the exact release image, then install or upgrade with a timeout longer than `migration.activeDeadlineSeconds` plus image-pull and scheduling time.

```sh
make helm-deploy-check HELM_CONTEXT=my-cluster HELM_NAMESPACE=open-agentic-catalog \
  HELM_RELEASE=open-agentic-catalog HELM_VALUES=/path/to/catalog-values.yaml
make helm-deploy HELM_CONTEXT=my-cluster HELM_NAMESPACE=open-agentic-catalog \
  HELM_RELEASE=open-agentic-catalog HELM_VALUES=/path/to/catalog-values.yaml \
  HELM_DEPLOY_TIMEOUT=20m
```

The [pre-install or pre-upgrade hook](https://helm.sh/docs/topics/charts_hooks/) applies migrations and reconciles the exact runtime privilege manifest before either Deployment changes. Set `databaseRoles.api` and `databaseRoles.worker` to the existing distinct LOGIN roles authenticated directly by the corresponding runtime DSNs; each name is limited to 63 UTF-8 bytes and cannot contain whitespace. The command validates role attributes and memberships before a migration, holds one advisory lock through migration and reconciliation, and fails the hook unless both bounded privilege reports are compliant.

Runtime startup rejects an assumed role whose session identity differs from its current role.

Migrations are forward-only, so do not treat Helm rollback or `--atomic` as a database rollback mechanism; follow the version-specific compatibility and rollback boundaries in the deployment guide.

Use the [migration guide](../../../migrations/README.md) for the current schema and exact privilege-manifest requirements.

The coordinator validates the selected target, stops API then worker and waits for all old Pods to disappear before invoking Helm. It restores replica intent from target values on a retry, even when live Deployments are already at zero. Suspend external reconciliation/autoscaling and serialize deployment operations first. Use the selected image's `migrate plan` and [migration requirements](../../../migrations/README.md) for current schema/checksum/repair information.

Pause the external HPA's recreator. Remove every HPA that targets the API or worker Deployment. Inactive HPAs still block deployment. Restore the reviewed HPA only after target readiness and authorized data readback. See the [external autoscaler handoff](../../../documentation/src/content/docs/operations/maintenance.mdx#hand-off-an-external-autoscaler).

A release explicitly requiring stopped-runtime repair uses the [manual owner sequence](../../../documentation/src/content/docs/operations/maintenance.mdx#upgrade-and-failed-migration) before startup; the coordinator does not run arbitrary repair commands. Never use an intermediate Helm upgrade to scale down, because its pre-upgrade hook runs before the scale change. Keep runtimes stopped on migration/grant failure and repair forward. Unsupported development formats are refused, not converted or deleted.

The default values are safe placeholders for rendering, not production identity or secret material. Replace the image reference, OIDC and public URLs, tenancy mapping, and every Secret reference. Replace the example discovery-host mapping with your exact hosts, or set it to `[]` when discovery is not needed.

The generated Service name must start with a lowercase letter and contain only lowercase letters, digits and hyphens, ending in a letter or digit. If the Helm release name starts with a digit, set an explicit `fullnameOverride`, such as `catalog-123`. The chart checks the final generated name before installation rather than requiring the cluster's `RelaxedServiceNameValidation` feature gate. See [Kubernetes naming rules](https://kubernetes.io/docs/concepts/overview/working-with-objects/names/#rfc-1035-label-names). Changing an installed release's naming overrides changes resource identities; choose them before installation.

## Image selection

`image.tag` defaults to the chart `appVersion` with `+` replaced by `_` when empty. Set `image.digest` to an exact `sha256:` manifest digest for an immutable deployment; a digest takes precedence over the tag. `imagePullSecrets` is projected into API, worker, and migration Pods without placing registry credentials in chart values.

Use `make helm-package CHART_VERSION=0.1.0 APP_VERSION=0.1.0` from the repository root to create versioned application and independent monitoring archives under `output/artifacts/`. The target includes the project license, excludes CI fixtures, and lints/renders the actual archives before publishing local output. It never rewrites the tracked chart or publishes to a registry. See [build and release](../../../documentation/src/content/docs/contributing/build-and-release.md) for version metadata, image tags/digests and output collisions.

```yaml
image:
  repository: registry.example.com/catalog/open-agentic-catalog
  tag: ""
  digest: sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef
  pullPolicy: IfNotPresent

imagePullSecrets:
  - name: catalog-registry
```

Use a new tag or digest for each release. Updating registry content behind an unchanged Pod template does not itself trigger a Deployment rollout.

## Configuration and tenancy

`configEnv` is a closed schema of non-secret service environment variables. The chart pins settings that must agree with its listeners, probes, role split, and mounted paths. The application still performs its complete typed and cross-field validation before startup. Chart schema validation covers values shape and supported keys; it cannot validate mounted Secret contents or establish dependency readiness. Helm does not consume a local `.env` or arbitrary runtime environment overrides. Review [configuration delivery, capability activation and validation](../../../documentation/src/content/docs/reference/configuration.mdx#configuration-delivery-and-precedence) before selecting values; use existing Secret references for scoped confidential inputs, never `configEnv`.

Optional hybrid search uses `semanticSearch.enabled=true` with one configured endpoint, model, profile revision and dimensions. The chart does not deploy a model service. Core search requires only PostgreSQL 18 with `pgcrypto`; enabling hybrid search additionally requires administrator-installed pgvector 0.8.7.

When enabled, the migration hook adds `--semantic` for the optional schema and exact-grant reconciliation; it never installs extensions or contacts inference. Drain old API/worker Pods before optional schema/grant changes: a Helm hook does not drain them for you.

Disabling retrieval preserves derived state and its exact grants. See [hybrid search operations](../../../documentation/src/content/docs/operations/hybrid-search.mdx) for setup, failure behavior and every setting.

Use `semanticSearch.existingSecret` for optional token/private-CA files. Enabled API and worker Pods mount it read-only at `/etc/open-agentic-catalog/embeddings`; set `tokenFile` and/or `caFile` to the selected file paths. Migration and UI Pods never receive this Secret. Disabled semantic search requires neither a provider nor this mount. Query and indexing timeouts must fit the configured API request timeout and worker lease; the application validates those bounds before startup. The owned `make test-minikube-semantic` fixture supplies local model replicas separately from the chart.

Dedicated tenancy is the default. For shared tenancy, set the mode and organization claim; the renderer automatically omits the inherited dedicated tenant ID. A dedicated overlay similarly omits an inactive organization claim, so neither mode depends on a YAML `null` merge trick.

```yaml
configEnv:
  CATALOG_AUTH_TENANCY_MODE: shared
  CATALOG_AUTH_ORGANIZATION_CLAIM: organization_id
```

The chart creates a default-deny federation-policy ConfigMap unless `federation.policy.existingConfigMap` is set. Both roles receive the same policy reference, optional trust ConfigMap, and optional credential Secret. The credential resolver reads mounted files for each outbound request; the policy and trust configuration are startup inputs.

Live Search defaults to one page per peer. Set `configEnv.CATALOG_FEDERATION_MAX_LIVE_PAGES_PER_SOURCE: "3"` to permit deeper paging; accepted string values are `"1"` through `"5"`. The shared configuration checksum triggers a rollout when this startup setting changes. Pages share the existing fan-out, deadline and per-peer byte limits. See [live peer configuration](../../../documentation/src/content/docs/reference/configuration.mdx#live-peer-pages) for partial results and safety rules.

OIDC private trust is separate: set `auth.trustBundleConfigMap` to an existing ConfigMap containing `ca.pem`. Only that key is mounted read-only into the API at `/etc/open-agentic-catalog/oidc/trust/ca.pem`; the worker and migration hook receive neither the mount nor its environment setting. The bundle extends public roots only for configured-issuer discovery and declared HTTPS signing keys, with normal hostname verification.

The process limit is 4 MiB; Kubernetes also enforces its own smaller ConfigMap object limit.

Prefer a new ConfigMap name for each reviewed trust revision: it changes the API checksum without rolling the worker. In-place content replacement requires an explicit API rollout because external ConfigMap bytes are not part of the rendered chart checksum. After rollout, compare the loaded trust digest in API diagnostics; never use a global root override or disabled TLS verification.

Set `configEnv.CATALOG_JOBS_MAX_ATTEMPTS` (default `"5"`, range 1-20) and `configEnv.CATALOG_JOBS_RETRY_CEILING` (default `1h`, range 5s-1h) for shared API/worker Job policy. The existing ConfigMap checksum rolls both workloads after changes. Stored limits and already scheduled retries do not change; the retry ceiling caps the automatic jitter window, while valid source minima can exceed it within the fixed 24-hour total bound.

Set `configEnv.CATALOG_WORKER_DEPENDENCY_RETRY_INITIAL_DELAY` (default `1s`) and `configEnv.CATALOG_WORKER_DEPENDENCY_RETRY_CEILING` (default `30s`) for process-component restarts. Worker validation requires `1s <= initial <= ceiling <= 5m`, including fractional values. These share the configuration checksum rollout; they do not change healthy poll/maintenance cadence or durable Job retries. See [worker restart semantics](../../../documentation/src/content/docs/reference/configuration.mdx#worker-dependency-restarts).

## Validation diagnostic budgets

`configEnv.CATALOG_VALIDATION_MAX_FINDINGS` defaults to `"128"` (range 1-512). `configEnv.CATALOG_VALIDATION_MAX_DIAGNOSTIC_BYTES` defaults to `"65536"` (range 1024-262144 bytes). Chart schema/templates and runtime validation reject values outside these bounds. Both values enter the shared ConfigMap and API/worker rollout checksum. They bound new finding arrays independently of retained evidence reads and raw documents; see [scope, exhaustion and historical read limits](../../../documentation/src/content/docs/reference/configuration.mdx#validation-diagnostic-limits).

## Logging

Logging defaults to INFO structured text. Select startup levels and formats in your existing deployment values:

| Helm value | Default | Allowed values | Consumers |
| --- | --- | --- | --- |
| `configEnv.CATALOG_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` | API, worker and migration command. |
| `configEnv.CATALOG_LOG_FORMAT` | `text` | `text`, `json` | API, worker and migration command. |
| `ui.config.logLevel` | `info` | `debug`, `info`, `warn`, `error` | Enabled UI host only. |
| `ui.config.logFormat` | `text` | `text`, `json` | Enabled UI host only. |

For collector-friendly JSON, merge this fragment into the deployment values; keep existing images, identity settings and Secret references. It does not enable the UI or configure its required login inputs:

```yaml
configEnv:
  CATALOG_LOG_LEVEL: info
  CATALOG_LOG_FORMAT: json
ui:
  config:
    logLevel: info
    logFormat: json
```

Apply the values through the [existing install or upgrade procedure](#install-or-upgrade). Normal migration-hook and drain requirements still apply; changing logging does not bypass release checks. Backend changes alter `checksum/config` and roll API and worker Pods. UI-only changes alter `checksum/ui-config` and roll UI Pods without changing backend Pod templates. The migration hook receives backend logging settings, but its stdout result and concise terminal stderr contract remain unchanged. There is no hot reload: editing a live ConfigMap alone is not the supported rollout procedure.

Schema validation rejects empty, null, unsupported and wrongly cased values. Backend settings do not configure the UI, and Make's `CATALOG_UI_LOG_*` launcher variables are not Helm values. All normal service levels use stdout; terminal failures use stderr. The chart needs no log directory, writable log volume, logging sidecar or monitoring release. Capture, access, forwarding, retention and rotation belong to the cluster's log pipeline. See [inspect application logs](../../../documentation/src/content/docs/operations/observability.mdx#inspect-application-logs), [configuration precedence](../../../documentation/src/content/docs/reference/configuration.mdx#logging-configuration) and [ADR-0021](../../../documentation/src/content/docs/architecture/decisions/0021-logging-observability-and-audit.md).

## Secrets and rotation

Database DSNs, cursor signing, optional webhook encryption, and federation credentials are existing Secret references. Secret values never belong in a values file.

Vault is optional. The default deployment needs no Vault server, VSO controller or provider CRDs. A separately installed operator can synchronize values into the existing Secret references. See [optional external Vault delivery](../../../documentation/src/content/docs/operations/maintenance.mdx#optional-external-vault-delivery) for the boundary and current testing limits.

Changing a Secret's contents under the same name does not change the Pod template. Roll the API after changing its database, cursor, or webhook environment-backed Secret; roll the worker after changing its database, cursor, or webhook environment-backed Secret. Coordinate cursor-key and webhook-key rotation with their documented protocols rather than replacing bytes in place.

Federation credential files use the generation and revision contract in the configuration reference and can rotate through one atomic projected Secret because the resolver reads them per request. Roll both Deployments after changing policy or trust bytes under an unchanged external ConfigMap name, and verify the reported policy identity and digest before resuming affected sources. A change to the chart-managed policy document, policy reference, or `configEnv` changes a chart-owned checksum and rolls both Deployments automatically.

`podAnnotations` can carry operator annotations or a deliberate rollout marker. The chart owns `checksum/config`, `checksum/federation-policy`, `checksum/webhook-egress` and `checksum/database-trust-reference`; the values schema rejects attempts to replace any of them. Use a separate annotation key for an operator rollout marker. Service, ServiceAccount and UI Ingress annotation maps reject Helm hook and retention keys. These maps cannot set Helm hooks or retention policies.

## Workloads, migration, and availability

The migration Job receives only migration configuration, the migration DSN, and the two non-secret runtime role names, has a configurable deadline, retry bound, completion TTL, resources, and Pod annotations, and never receives either runtime role's ServiceAccount or token. By default a weight `-20` pre-install or pre-upgrade hook creates a distinct tokenless migration ServiceAccount before the weight `-10` migration ConfigMap and weight `-5` Job. All three use stable names with `before-hook-creation`: successful hook resources are removed, while a failed hook remains inspectable and is replaced before a safe rerun. An externally managed migration ServiceAccount may be selected instead.

Helm can remove the successful ServiceAccount and ConfigMap after the migration Job fails. Inspect the retained Job and Pod before the Job's completion TTL expires. A retry recreates the prerequisites.

The Job uses the dedicated schema owner to apply migrations and reconcile grants; it never receives either runtime DSN. A failed migration or noncompliant privilege report blocks the release before the application Deployments change.

Release upgrades use the coordinated maintenance workflow to stop and drain both runtime roles, including disabled webhook drainers. The hook alone does not stop old replicas. Keep runtimes stopped until target migrations and exact grant reconciliation succeed, and complete any explicitly required stopped-runtime repair through the manual owner procedure before starting matching binaries. Do not bypass excess-grant rejection or restart old binaries across this boundary. See the [deployment sequence](../../../documentation/src/content/docs/operations/deployment.mdx#release-sequence) and [database-role runbook](../../../documentation/src/content/docs/operations/database-roles.mdx) for failure recovery; webhook encryption-key replacement is a separate operation.

API and worker replica counts, resources, shared scheduling inputs, role-specific topology spread, and PodDisruptionBudgets are configurable. Both disruption budgets default to `maxUnavailable: 1` and can be disabled independently. Topology spread is disabled by default; when enabled, each role's selector is scoped to the current `pod-template-hash` so old ReplicaSet Pods do not distort new-revision placement during a rollout. The Service defaults to `ClusterIP`; ingress, certificates, external routing, rate enforcement, autoscaling, broader placement objectives, and availability objectives remain operator-owned.

Set `terminationGracePeriodSeconds` above `CATALOG_HTTP_SHUTDOWN_TIMEOUT` plus the time needed to remove endpoints. Product requests use the cooperative `CATALOG_HTTP_REQUEST_TIMEOUT` work deadline; the write and shutdown timeouts must each also cover the fixed five-second command-cleanup allowance and one-second response margin. The defaults are a 10-second request deadline, 20-second HTTP drain, and 40-second Pod termination grace. Workers stop claiming work and leave interrupted work recoverable through leases. Their named `operations` port serves direct startup, liveness, and required-PostgreSQL readiness probes; it is a container port only and is absent from the product Service and Ingress.

The Catalog API, worker, migration and UI containers use a read-only root filesystem, drop all Linux capabilities and forbid privilege escalation. They require a non-root identity and runtime-default seccomp. API, worker and migration receive bounded writable `/tmp` volumes. The UI mounts only configuration, session inputs and selected trust bundles. Kubernetes service-link injection and ServiceAccount token automount are disabled. These workloads do not pin `runAsUser`, so OpenShift restricted-v2 can select an arbitrary user ID.

The optional PgBouncer container uses UID, GID and fsGroup 70. Its admission policy must permit that fixed identity. See [pooler identity and storage](../../../documentation/src/content/docs/operations/database-roles.mdx#connection-pooling-boundary) before enabling it.

Helm rejects generated names that collide with another enabled chart resource. Use a shorter name or a different ending if `fullnameOverride` or `nameOverride` causes this error. Existing names remain unchanged for valid values.

## Optional metrics collection

Follow [Monitoring with Prometheus and Grafana](../../../documentation/src/content/docs/operations/helm-monitoring.mdx) for an existing-collector procedure or the independent optional monitoring chart. Use the dashboard, scrape example and provisioning files under `deployments/monitoring/` directly from your repository checkout.

Metrics are emitted without a collector dependency. The chart installs neither a monitoring stack nor ServiceMonitor/PodMonitor CRDs. SRE can use an existing Prometheus or an OpenTelemetry Collector with its Prometheus receiver; collection and downstream export are configured in that system, not in Catalog `configEnv`.

| Pod selection | Scrape endpoint | Dashboard role |
| --- | --- | --- |
| Release `app.kubernetes.io/instance` plus `app.kubernetes.io/component=api` | Each Pod's named `http` port, 8080, `/metrics` | `catalog_role=api` |
| Same release plus `app.kubernetes.io/component=worker` | Each Pod's named `operations` port, 9090, `/metrics` | `catalog_role=worker` |

Scope discovery to the selected namespace and Helm release. Retain a distinct `instance` for every Pod endpoint. The [scrape configuration example](../../monitoring/kubernetes-scrape.example.yaml) supplies those filters and the dashboard labels; replace its namespace, release and installation label.

Do not scrape the load-balanced API Service as a substitute for all replicas. Include unready Pods. A running unready process can still expose useful degradation signals.

Scrape authentication/discovery RBAC and network access belong to the collector; Catalog service accounts remain tokenless and the private worker port stays off the product Service and ingress.

Existing `podAnnotations` and `service.annotations` can support a deployment's annotation-based discovery, but annotations have no built-in scraping behavior. `podAnnotations` applies to both roles, so a shared `prometheus.io/port` value is unsuitable for their different ports. Label-and-named-port discovery needs no additional chart value. If the deployment already uses Prometheus Operator, its own PodMonitor resources can select these same role labels and ports; no Operator installation is required by Catalog.

For OpenTelemetry, place the example's `scrape_configs` under `receivers.prometheus.config` in the operator's Collector configuration and connect that receiver to its metrics pipeline and chosen exporter. Select a Collector distribution that includes the receiver. Assign each target to one scraper to prevent duplicate collection. Direct OTLP metrics/log export is not implemented; application OTLP/HTTP tracing is independently configurable under [optional trace export](#optional-trace-export). See the [telemetry integration boundary](../../../documentation/src/content/docs/operations/observability.mdx#collector-integration) for scope and upstream configuration guidance.

## Optional webhooks

Webhooks are disabled by default. The [webhook runbook](../../../documentation/src/content/docs/operations/webhooks.mdx) defines enable/disable ordering and encryption-key maintenance. For a private receiver, `webhooks.egressConfigMap` names one existing ConfigMap with `policy.json` and referenced CA certificates, mounted read-only into enabled API and worker Pods at `/etc/open-agentic-catalog/webhooks`; it is absent from migration Pods and disabled profiles. The policy is separate from federation and issuer trust, with exact origin/address/tenant assignment. Use versioned ConfigMap names for rollout and configure corresponding egress rules. The chart does not automatically order enabled API startup against a disabled drainer.

## Optional trace export

Set `configEnv.CATALOG_TRACING` to a JSON string with `enabled: true`, one complete OTLP/HTTP trace endpoint and an installation label. For example, public-CA HTTPS without credentials uses `'{"enabled":true,"endpoint":"https://collector.example.com:4318/v1/traces","installation":"catalog-prod"}'`. Private collectors additionally require explicit approved address prefixes and, for private PKI, a CA file. Sampling defaults to 10%; the default ignores untrusted remote sampling flags.

Use `secrets.tracingInputs: {name: catalog-tracing}` for an existing Secret containing the referenced `headersFile`, `caFile` and optional client certificate/key. Both runtime roles mount it read-only at `/etc/open-agentic-catalog/tracing` only when tracing is enabled. It is never mounted by the migration Job.

The tracing setting must be a JSON object, and `enabled` must be boolean when supplied. Enabled tracing files under `/etc/open-agentic-catalog/tracing/` require `secrets.tracingInputs`; Helm rejects a missing mount. An explicit CA path supplied by another existing mount does not require a second Secret.

No credentials belong in `configEnv`; public unauthenticated export needs no Secret. Updating Secret contents requires a rolling restart.

The chart deploys no trace backend and does not depend on the optional monitoring companion. See the [configuration contract](../../../documentation/src/content/docs/reference/configuration.mdx#optional-tracing-configuration) and [operations guide](../../../documentation/src/content/docs/operations/observability.mdx#distributed-tracing).

`make test-minikube-tracing` qualifies disabled/enabled mounts, private HTTPS, two API replicas, linked worker lease recovery, collector outages and process identity after rollout in an owned local cluster.

Changing `configEnv.CATALOG_TRACING` rolls both API and worker Pods through their configuration checksums, including when export is disabled. Changing the referenced Secret name also changes both enabled Pod templates. Neither change adds tracing configuration or credentials to the migration hook.

## Values reference

| Values path | Purpose |
| --- | --- |
| `image`, `imagePullSecrets` | Select one tag or immutable digest and existing registry pull credentials for all roles. |
| `terminationGracePeriodSeconds` | Bound Kubernetes termination for API and worker Pods; keep it above the application drain timeout. |
| `api`, `worker` | Set replicas, resources, optional role-specific topology spread, and independently enabled disruption budgets. |
| `migration` | Bound hook execution, retries, retained completion time, resources, and Pod annotations. |
| `databaseRoles.api`, `databaseRoles.worker` | Select the existing distinct PostgreSQL roles whose exact Catalog-schema manifests the migration hook reconciles; values are role names, not credentials. |
| `pgbouncer` | Disabled-by-default private session pooler; explicit authentication/TLS references, writer endpoint/peers, per-role capacities, queue/connect/idle limits and resources. Enabling it never changes runtime DSN Secrets; owner operations remain direct. See the pooling runbook for the complete settings and prerequisites. |
| `databaseTrustBundleConfigMap` | Optional existing public CA ConfigMap with `ca.pem`, mounted read-only into API, worker and the direct migration hook; DSNs explicitly select its path. It is independent of pooler enablement and is never mounted into the UI. |
| `service` | Select `ClusterIP`, `NodePort`, or `LoadBalancer`, its port, and non-secret annotations. |
| `serviceAccounts.api`, `serviceAccounts.worker`, `serviceAccounts.migration` | Create or reference three distinct role identities. Helm creates the chart-managed migration account before its ConfigMap and Job. Token automount is disabled. Helm can remove successful prerequisites after the Job fails; a retry recreates them. Inspect any retained hook resources before deliberate cleanup. |
| `secrets.apiDatabase`, `secrets.workerDatabase`, `secrets.migrationDatabase` | Reference distinct Secret names and keys for each database role. |
| `secrets.cursorSigning` | Reference the required cursor key used by API and worker roles. |
| `secrets.tracingInputs` | Optionally reference one existing Secret with tracing authentication and TLS files; mounted into API and worker Pods only while trace export is enabled. |
| `secrets.webhookEncryption` | Reference the key required only when webhooks are enabled. |
| `webhooks.egressConfigMap` | Optionally mount an existing `policy.json` and its private receiver CA files into enabled API and worker Pods only; no migration mount. |
| `secrets.federationCredentials` | Reference the mounted credential tree required only when credential resolution is enabled. |
| `federation.policy` | Supply one non-secret policy document or an existing ConfigMap name and key. |
| `federation.trustBundleConfigMap` | Optionally mount public CA bundle files for named private-network profiles into both roles. |
| `auth.trustBundleConfigMap` | Existing ConfigMap with fixed `ca.pem` key for API-only OIDC private trust; empty means public roots only. |
| `configEnv` | Supply the closed non-secret runtime configuration allowlist. |
| `podDNSOptions` | Set optional Kubernetes resolver options for chart-managed Pods; defaults to `[]`. |
| `podAnnotations` | Add the same non-secret operator annotations to API and worker Pods. |
| `nodeSelector`, `tolerations`, `affinity` | Apply shared scheduling constraints to API, worker, migration and enabled UI/PgBouncer Pods. |
| `nameOverride`, `fullnameOverride` | Override generated names while preserving the 63-character limit and the Service's lowercase-letter prefix. |

API, worker and migration use distinct configurable ServiceAccounts and stable role labels. The chart creates no role-based access control (RBAC) grants. API and worker support role-specific topology spread plus shared node selectors, tolerations and affinity.

The worker reports startup, liveness, readiness, degradation and drain state through its operations-only listener. Operators supply production network egress policies for their exact external origins and private routes. The distinct workload labels let those policies select each role independently.

## Local validation

Select a gate for the change. Run cluster scenarios separately, after their [host and tool prerequisites](../../../test/minikube/README.md).

| Gate | Scope |
| --- | --- |
| `make deploy-check` | Complete local Helm, Kubernetes schema and container checks. It includes `helm-check`. |
| `make helm-check` | Strict lint, rendered contracts and actual packaging tests. It includes `helm-lint` and `helm-test`. |
| `make helm-schema-check` | Kubernetes schema validation for defaults, all profiles and the upgrade render. |
| `make helm-render` | Render defaults for manual inspection. |
| `make test-minikube-contract` | Harness tests without a cluster. |
| `make test-minikube` | Owned cluster installation, failed migrations, retry and retained-state upgrades. |
| `make test-minikube-upgrade` | Coordinated upgrades across nodes. It stops before the broader topology campaign. |
| `make test-minikube-topology` | Cross-node upgrades, federation, disruption, node loss and network policies. |
| `make test-minikube-pgbouncer` | Session pooling, private TLS, coordinated deployment and return to direct database connections. |
| `make test-minikube-ui` | Optional UI, subpaths and sessions across replicas. |
| `make test-minikube-peers` | Federation between two independent releases and PostgreSQL instances. |
| `make test-minikube-replicas` | Three replicas per role, command integrity, replacement and short load. |
| `make test-minikube-monitoring` | Independent monitoring chart, dashboards and retained storage. |
| `make test-minikube-tracing` | Optional trace export, causality and collector outages. |
| `make test-minikube-semantic` | Hybrid search with the selected model and multiple replicas. |

Local lint and schema gates select every YAML profile under `ci/`. Helm tests check accounts, Secret references, hooks, probes, security, scheduling and configuration delivery. The typed runtime loader validates the rendered settings for each backend role. Packaging tests check metadata, image selection, archive contents, minimum versions and output safety.

Use `make helm-lint`, `make helm-test` or `make helm-package-test` for a focused repeat after a failure. Their local checks use Helm, Go and Python. They do not contact a cluster. The schema gate retrieves upstream schemas, as described below.

The explicit Kubernetes 1.34.0 validation input matches the chart minimum; source and package tests also check version rejection and vendor-suffix acceptance.

`make helm-schema-check` renders the defaults, every representative profile and the upgrade shape. It validates each result strictly with [Kubeconform](https://github.com/yannh/kubeconform). `make deploy-check` combines that schema check with the complete Helm and container-source contracts. The pinned validator retrieves Kubernetes 1.34.0 schemas from its upstream source, so schema validation requires network access. Version-constraint tests additionally verify source and packaged chart acceptance/rejection; mandatory offline schema packaging is not required.

The chart supports CNCF-certified Kubernetes 1.34+ distributions meeting its API and security prerequisites. `Chart.yaml` enforces the minimum; schema checks use 1.34.0 and the maintained Minikube default is Kubernetes 1.37.1. The test-environment list is not a distribution allowlist. Local build/package/test commands do not publish artifacts.

`make test-minikube-contract` exercises the harness without creating a cluster. The listed cluster targets require the host and local image inputs documented in the [testing guide](../../../documentation/src/content/docs/contributing/testing.md). Each builds uniquely tagged local images, verifies running application image identities and creates and removes only invocation-owned resources through bounded setup and cleanup. Run the targets sequentially; use `CATALOG_ENV_FILE=/dev/null` to exclude local `.env` overrides. They do not reuse an existing development cluster.

| Target | Scenario |
| --- | --- |
| `test-minikube` | Single-node installation, failed-hook safety and retry, restricted admission, role identities, probes, governed smoke, Pod recreation, retained state, checksum rollout, no-op reapplication and optional webhook delivery/disablement. |
| `test-minikube-topology` | Three-node revision-scoped replica spread, cross-API replay, worker lease takeover, Eviction/PDB enforcement, drain, direct Pod loss, pinned Calico backend/UI NetworkPolicy allow/deny and survivor continuity after worker-node loss. |
| `test-minikube-ui` | Default-off UI, two-replica shared sessions and authenticated API requests, synthetic mounted-key staging/promotion and emergency retirement with process replacement, subpath routing/probes and disabling UI without removing the backend. HTTP/session checks, not browser JavaScript or human OIDC login. |
| `test-minikube-replicas` | Three UI/API/worker replicas, concurrent duplicate commands and optimistic updates, exact readback/audit/replay, shared sessions after process replacement, bounded mixed traffic and resource observations. Single-node fixture; topology/node loss remain the separate scenario above. |
| `test-minikube-monitoring` | Separate optional companion, scoped Pod discovery, dashboard queries, replacement-Pod metrics, missing-Secret isolation and external-PVC history across disable/re-enable and uninstall/reinstall. |
| `test-minikube-tracing` | Two-API parent propagation/replay, linked worker takeover, export credentials/private trust, collector failure isolation, new process identities after rollout and disablement. |

Results apply to the built images, chart values and recorded platform, not arbitrary downstream chart or infrastructure changes. The [Minikube harness guide](../../../test/minikube/README.md) explains scenario ownership and cleanup.

## Optional Catalog UI

The browser host is disabled by default. `ui.enabled=true` adds a separate Deployment, private Service, ConfigMap and tokenless ServiceAccount. It uses the independently built `catalog-ui/Containerfile` image. The API and worker retain their existing configuration, credentials and lifecycle. No UI resources or UI Secret references are rendered when disabled.

When enabled, its generated ServiceAccount name must differ from all three backend account names, including externally managed accounts. Rendering rejects collisions. This prevents migration-hook replacement or cleanup from removing the UI account.

Build with `make ui-container-build UI_IMAGE=open-agentic-catalog-ui:dev`. Supply a tag or digest available to your cluster; building locally does not publish or load the image into a cluster. A minimal production values file is:

```yaml
ui:
  enabled: true
  image:
    repository: registry.example.com/catalog-ui
    tag: "0.1.0" # Replace with your built image version.
  existingSecret: catalog-ui-inputs
  config:
    origin: https://browse.example.com
    issuer: https://identity.example.com
    clientId: catalog-browser
```

Create `catalog-ui-inputs` in the release namespace before enabling the UI. It must contain `keys.json` (one to three objects with independent random 64-byte `hash` and 32-byte `block` keys encoded as hex) and `client-secret` (the OIDC client secret). Use an existing secret-management workflow or `kubectl create secret generic catalog-ui-inputs --from-file=keys.json=/protected/keys.json --from-file=client-secret=/protected/client-secret`. Do not put their contents in values or checked-in files.

All UI replicas mount the same inputs.

The identity provider must allow the exact redirect `<origin><basePath>/auth/callback`, and its access token must target the Catalog API audience; its ID token targets `ui.config.clientId`. Register additional API scopes under `ui.config.scopes` when required. For feedback with API scope enforcement, request `catalog.read` and `feedback.write`; administrator triage also needs `tenant.admin`. Saved items and named searches need `catalog.read` for reads and `personal.write` for saves, updates and removals. These scopes must be allowed by the issuer and never grant Catalog roles. `openid` is added automatically; offline refresh is not supported.

Apply the values through the [coordinated install or upgrade procedure](#install-or-upgrade). Include the required backend configuration and database Secrets. `ui.config.upstream` defaults to this release's API Service; set an explicit fixed origin only when the API is elsewhere. An image digest takes precedence over a tag. Empty image tags use the chart appVersion, with build metadata normalized as in the backend image. Node selection, tolerations, affinity and image-pull Secrets use the chart's existing settings; UI replicas, resources and Pod annotations are separate under `ui`.

| Setting | Use |
| --- | --- |
| `ui.config.origin` | Exact browser origin. Production requires HTTPS. The reverse proxy must preserve Host and Origin. |
| `ui.config.basePath` | Empty or a path such as `/catalog`, without a trailing slash. `/docs` and its descendants are reserved for the packaged manual. Do not strip the base path at ingress. |
| `ui.config.documentationUrl` | Empty selects the bundled manual at `/docs/`. Optional credential-free HTTPS override, without query or fragment. Changes roll UI Pods; external availability does not affect readiness. |
| `ui.apiCAConfigMap`, `ui.issuerCAConfigMap` | Optional separate existing ConfigMaps, each with `ca.pem`, scoped to the named transport. |
| `ui.ingress.enabled` | Render a standard TLS Ingress. Also set `host`, `tlsSecretName` and optional `className`/`annotations`. The host must match the configured HTTPS origin. Helm hook and retention annotations are reserved. Path-rewrite annotations are unsupported. |
| `ui.networkPolicy.enabled` | Off by default; when enabled, isolate UI Pods. Built-in egress permits the release's API Pods on TCP 8080 and `kube-system` Pods labelled `k8s-app=kube-dns` on TCP/UDP 53. |
| `ui.networkPolicy.ingress` | Standard NetworkPolicy ingress rules for your ingress controller or other authorized callers; empty admits no incoming ordinary-Pod traffic through this policy. |
| `ui.networkPolicy.extraEgress` | Standard egress rules for the selected issuer, external API, custom DNS or other explicitly configured transport. No hostname-based firewall is implied. |

A container network interface (CNI) plugin that enforces [NetworkPolicy](https://kubernetes.io/docs/concepts/services-networking/network-policies/) is required for isolation. If existing policies also isolate the API, permit incoming TCP 8080 from the same release's UI component.

Adapt DNS rules when your cluster uses NodeLocal DNS or different labels. The built-in rules grant no UI access to PostgreSQL or worker operations.

Additional policies are additive. Operator-supplied extra rules can broaden access. Review the complete effective policy set.

Node traffic, probes and `kubectl port-forward` do not prove ingress isolation for ordinary Pod traffic.

Probes use `/livez` and `/readyz` at the root, independent of the browser base path. UI liveness does not contact the API or identity provider. Readiness reports initialized local state that is not draining. Upstream outages remain request/login failures, not a restart trigger.

The [UI network-policy procedure](../../../documentation/src/content/docs/operations/deployment.mdx#optional-ui-network-policy) explains issuer and custom-DNS rules. The maintained Minikube topology scenario tests UI isolation, federation, disruption and node loss. See [the tested scenarios](../../../test/minikube/README.md) for scope and prerequisites. These results apply to the recorded chart, images and local platform.

The UI image includes the public manual. A root UI ingress already covers `/docs/`; a non-root UI ingress adds a `/docs` Prefix route to the same Service. Both paths use the same host and TLS settings. Preserve Host and both path prefixes without rewriting them. No separate documentation Deployment, port or egress rule is required. An external override changes navigation, not the local route. See [Help and documentation](../../../documentation/src/content/docs/guides/catalog-ui.mdx#open-the-manual-and-api-reference).

For local tests only, set `ui.config.environment=development`, `ui.config.authMode=development`, a literal-loopback origin such as `http://127.0.0.1:14180`, and mount `users.json` instead of `client-secret` in the UI Secret. Leave issuer/client/scopes unset and ingress disabled. The file maps aliases/labels to development tokens accepted by your test API. Port-forward the UI Service's 8080 port to the exact configured browser port; do not expose this mode publicly. `make test-minikube-ui` creates its own cluster and scoped test inputs, exercises two UI replicas and removes its resources automatically. This synthetic login test does not qualify an external provider's human login flow.

The host reads mounted keys and CA files at startup. Roll out UI Pods after changing existing Secrets or trust ConfigMaps; Helm's configuration checksum handles chart-rendered config changes, not external input contents.

For key rotation, first deploy the old primary and new secondary keys to every replica. Then promote the new primary while retaining the old pair. After the last old-key writer stops, wait for the greater of `ui.config.sessionSeconds` and five minutes. Keep the old pair available for reads during that interval. Then remove it in a third rollout. Keep at most three pairs.

Reuse the same origin/base path/upstream/cookie name across replicas. Disable with `ui.enabled=false` to remove the chart-owned UI resources without removing backend data or the externally managed UI Secret.

### Request and detail-response budgets

Backend `configEnv.CATALOG_HTTP_MAX_BODY_BYTES` controls request admission (default 2 MiB, allowed 1 KiB-64 MiB). The independent UI uses `ui.config.requestBytes` and `ui.config.responseBytes` (defaults 2 MiB and 16 MiB; each allowed 1 byte-64 MiB). Rendered settings are validated separately and require rollout of affected processes.

For a workflow admitting larger Cards, `33554432` request bytes and `67108864` UI response bytes are supported explicit settings, not new defaults. Compact browse/Search pages do not include retained documents; selected preview/detail reads still need enough response capacity. These controls do not rewrite retained evidence or change its exact digest. CLI `remote record --max-response-bytes` is independent of chart configuration. See [payload and client limits](../../../documentation/src/content/docs/reference/configuration.mdx#payload-and-client-limits) for full routes, units and remaining exceptional-response limits.

The repository's [payload values overlay](ci/payload-values.yaml) combines those transport budgets with 256 findings and 128 KiB of compact diagnostic JSON per new array. It is a render-tested example, excluded from packaged chart archives, not a complete production profile: merge only the required values into deployment-owned settings and provide the UI's separate enablement, identity and Secret inputs. Backend values are quoted strings under `configEnv`; UI byte budgets are integers under `ui.config`. Backend ConfigMap changes roll API and worker Pods; independent UI configuration changes roll UI Pods.

`make helm-check` renders the overlay together with the existing UI fixture, validates backend settings through the typed runtime loader and checks delivered UI JSON. Job results remain compact references, observation groups retain the fixed 100-contribution preview, and occurrence histories use their existing paged endpoint. These read-projection contracts do not introduce new chart settings or change access to retained evidence.

## PostgreSQL pool timing

The `configEnv` values `CATALOG_DATABASE_MAX_CONN_LIFETIME`, `CATALOG_DATABASE_MAX_CONN_IDLE_TIME` and `CATALOG_DATABASE_HEALTH_CHECK_PERIOD` default to `30m`, `5m` and `30s`. These positive Go durations reach API, worker and migration pools. Configuration updates roll the API and worker Pods. The runtime parser validates positivity and representability; schema validation checks string shape. The optional-values profile exercises explicit overrides.

These values override the corresponding parsed DSN pool timings. Acquired transactions are not interrupted at the lifetime threshold. Keep per-process sizing and total replicas within the deployment's database connection budget. See the [configuration reference](../../../documentation/src/content/docs/reference/configuration.mdx#postgresql-pool-settings) for precedence, units, bounds, diagnostics and restart behavior.

## Coordinated release upgrades

Use `make helm-deploy-check` followed by `make helm-deploy` with explicit `HELM_CONTEXT`, `HELM_NAMESPACE`, `HELM_RELEASE`, `HELM_CHART` and `HELM_VALUES`. `HELM_DEPLOY_TIMEOUT` defaults to `10m`. The local chart may be a directory or packaged archive; select immutable target images and complete values. See [installation and maintenance](../../../documentation/src/content/docs/operations/maintenance.mdx#coordinated-release-deployment) for prerequisites, recovery and the manual path for migrations requiring stopped-runtime repair.

This operator-side workflow validates before draining both runtime roles, then invokes this chart's existing migration hook. It creates no runtime RBAC. The hook alone does not drain old Pods; raw `helm upgrade` is therefore not the general release-upgrade command. Coordinate external GitOps/autoscaling and serialize deployment operations. Desired replica counts come from target values, even after a failed attempt leaves live replicas at zero. Never use automatic Helm rollback after possible SQL mutation.

### External listing artwork

`configEnv.CATALOG_LISTING_MEDIA` carries the closed media configuration JSON. Its default `{"allowedOrigins":[]}` denies image fetching. Add exact HTTPS origins already admitted by the federation policy; an internal origin may name an existing private profile through `networkProfile`. Existing federation trust mounts and tenant assignments apply. Resource metadata cannot select profiles or inherit credentials. Limits and bindings change the normal configuration checksum and roll API/worker Pods. See [listing artwork configuration](../../../documentation/src/content/docs/reference/configuration.mdx#listing-artwork). Managed-media uploads remain unavailable.

## Optional rating summaries

Set `configEnv.CATALOG_RATINGS_SHARING_ENABLED: "true"` to permit publication sharing and `configEnv.CATALOG_RATINGS_MINIMUM_SHARED_COUNT: "5"` to select its minimum. The minimum accepts 5-1,000. Defaults are disabled and five. API and worker receive the same values. Tenant/audience opt-in and individual consent remain separately required; existing published copies are not recalled. OIDC scoring needs `ratings.write` in `ui.config.scopes` and issuer configuration. See the [ratings guide](../../../documentation/src/content/docs/guides/ratings.mdx).

## Publisher domain verification

`configEnv.CATALOG_PUBLISHER_DOMAIN_VERIFICATION` controls Publisher DNS TXT verification and defaults to `{"mode":"disabled"}`. Select `optional` or `required_for_approval` through the existing JSON setting. Follow [the setup procedure](../../../documentation/src/content/docs/reference/configuration.mdx#enable-publisher-dns-verification) for modes, limits and API resolver access. The chart creates no DNS records or provider and adds no worker verification queries.

## Optional ANS verification keys

`configEnv.CATALOG_TRUST_VERIFICATION` carries the closed verification JSON; its default is `{"enabled":false}`. Enablement loads local public keys without contacting ANS or changing ordinary discovery. Administrators can request durable assessment Jobs for exact source entries and read private results. Follow the [assessment procedure](../../../documentation/src/content/docs/operations/federation.mdx#request-an-ans-registration-assessment) after configuring an authority policy and the source.

Set `trustVerification.publicKeysConfigMap` to an existing ConfigMap containing the accepted public P-256 SPKI PEM files. Reference files below `/etc/open-agentic-catalog/trust-verification/` in each named policy. API and worker mount that ConfigMap read-only only when enabled. Migration and UI workloads receive no verification keys. This ConfigMap is separate from TLS roots and credential Secrets; the chart creates neither keys nor ANS services. Existing federation origins, private routes, tenant assignments and TLS validation still apply.

Settings changes update both runtime configuration checksums. Changing the ConfigMap name updates both Pod templates. Replacing externally managed ConfigMap contents requires an explicit rollout of API and worker. Disabled values render no verification mount, even when a ConfigMap name remains configured. See [configuration and limits](../../../documentation/src/content/docs/reference/configuration.mdx#ans-registration-verification) and [authority rotation](../../../documentation/src/content/docs/operations/security.md#ans-verification-authorities).

### Read-only MCP runtime inputs

`configEnv` accepts `CATALOG_MCP_ENABLED`, `CATALOG_MCP_ALLOWED_ORIGINS`, `CATALOG_MCP_MAX_TOOL_BYTES` and `CATALOG_MCP_MAX_CONCURRENCY`. MCP remains disabled by default and uses the existing API listener. Origins are a JSON string array; numeric limits are decimal strings. See [runtime configuration](../../../documentation/src/content/docs/reference/configuration.mdx#optional-http-mcp). The optional `api.ingress` exposes only MCP and its OAuth resource metadata. It is independent of `ui.enabled` and uses the existing API Service.

```yaml
api:
  ingress:
    enabled: true
    className: nginx
    host: catalog.example.com
    tlsSecretName: catalog-mcp-tls
configEnv:
  CATALOG_PUBLIC_BASE_URL: https://catalog.example.com
  CATALOG_MCP_ENABLED: "true"
  CATALOG_MCP_ALLOWED_ORIGINS: '["https://assistant.example.com"]'
```

Provide the TLS Secret and an ingress controller in your deployment. The chart installs neither. Set the ingress host to the HTTPS public origin without a port, path, query or credentials. The chart rejects a disabled MCP endpoint, mismatched host, missing TLS Secret name, and rewrite or configuration-snippet annotations.

An external cert-manager Certificate can manage `api.ingress.tlsSecretName`. Keep its Issuer, Certificate and controllers outside this release. Verify both Secret keys before starting the consumer and check renewal through a fresh TLS connection. See [MCP ingress certificates](../../../documentation/src/content/docs/operations/deployment.mdx#optional-mcp-ingress-certificates) for local setup, independent client trust, manual renewal, issuer recovery and scoped cleanup.

Both `/mcp` and `/.well-known/oauth-protected-resource/mcp` use `Exact` paths. No Control API, worker operations or UI route is added. Preserve Host, Origin, Authorization and MCP headers. Do not strip or rewrite either path. Configure edge rate limits and TLS policy in your controller. If you already manage ingress, leave this option disabled and route these two paths there.

If your policies isolate the API, permit controller traffic to its TCP port 8080. Preserve required API access to DNS, PostgreSQL and the configured issuer. This option does not create backend network policies.

The UI keeps its separate ingress and `/docs/` routes. Do not route MCP through the UI session host. Restart API Pods after changing runtime settings or issuer inputs. See the [MCP OAuth instructions](../../../documentation/src/content/docs/guides/mcp-server.mdx#oauth-discovery-and-permissions) for client registration and audience mapping.

## Live tenant policy and deployment defaults

The chart delivers startup inputs. Saved automatic-source and new-Job attempt overrides remain in PostgreSQL and take precedence after upgrades or Pod replacement. Keep shared API/worker defaults consistent. The chart neither imports nor resets tenant policy. Inspect **Administration > Settings** or the private settings API; static configuration diagnostics show only startup inputs. Use audited saves/reset rather than direct SQL. See the [configuration reference](../../../documentation/src/content/docs/reference/configuration.mdx#live-tenant-settings).

## API request statistics

`configEnv.CATALOG_HTTP_SERVICE_ACTIVITY_ENABLED` defaults to `"false"`. Set it to `"true"` to permit cross-tenant process statistics through `GET /api/v1/service-activity`. Current tenant-wide `access.manage` and enforced `tenant.admin` remain required. The configuration change rolls the backend workloads through the existing checksum. It adds no collector, Service or ingress path. See [request statistics](../../../documentation/src/content/docs/operations/observability.mdx#read-api-request-statistics).
