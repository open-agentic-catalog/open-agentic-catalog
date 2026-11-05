# Helm charts

| Chart | Contents | Installation |
| --- | --- | --- |
| [open-agentic-catalog/](open-agentic-catalog/README.md) | Catalog API, worker, migration hook, optional UI/session-mode PgBouncer, configuration and role-specific identities. | Install the source chart or its packaged archive with deployment-specific values and existing database/identity inputs. Enable the separate-image UI with `ui.enabled=true` and its required session/OIDC inputs. |
| [open-agentic-catalog-monitoring/](open-agentic-catalog-monitoring/README.md) | Optional Prometheus, Grafana and the shared Catalog dashboard. | Package first to include the shared assets, then install as a separate release with `enabled=true`; the default renders no resources. |

Both charts require Kubernetes 1.34+. The application chart has no dependency on the monitoring chart. Neither installs PostgreSQL or an identity provider.

Run `make helm-package CHART_VERSION=0.1.0 APP_VERSION=0.1.0` from the repository root to produce both archives in `output/artifacts/`. `make monitoring-package` packages only the monitoring chart into `output/helm-monitoring/`. See [build and release](../../documentation/src/content/docs/contributing/build-and-release.md) for version selection and output handling.

Edit each chart's `values.yaml`, `values.schema.json`, templates and README together when adding or changing an input. Shared monitoring assets remain under [deployments/monitoring/](../monitoring/README.md), not copied into the source chart. Application `ci/` values are test fixtures, not production installation presets.

`make helm-check` validates rendering, configuration, rejection cases and packaging without a cluster. `make deploy-check` additionally checks Kubernetes resource schemas. The [Helm tests](../../test/helm/README.md) and [owned Minikube tests](../../test/minikube/README.md) describe the static and runtime checks.
