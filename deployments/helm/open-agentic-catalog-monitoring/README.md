# Optional monitoring chart

Requires Kubernetes 1.34+, independently of the application chart. The chart constraint `>=1.34.0-0` accepts vendor suffixes; see [cluster prerequisites](../../../documentation/src/content/docs/operations/deployment.mdx#kubernetes-prerequisites). Pass `--kube-version 1.34.0` when rendering or linting the package offline. Real installation uses the selected cluster's version.

This independently installed chart defaults to `enabled: false` and renders no resources. Set `--set enabled=true` to deploy Prometheus and Grafana for one explicitly selected Catalog namespace/release; `make monitoring-install` sets this flag automatically. The application chart does not install or depend on it. Both services are private; enabled Grafana requires an existing admin Secret. Storage is ephemeral unless separate existing claims are selected. Setting `enabled=false` removes monitoring resources but retains external claims and credentials; re-enable with the same values to reuse them.

Run `make monitoring-package` from the repository root. Install the resulting `output/helm-monitoring/open-agentic-catalog-monitoring-0.1.0.tgz`, not this source directory: packaging includes the authoritative image pins, dashboard and applicable Grafana configuration from `deployments/monitoring/`. No chart dependencies or network downloads are needed to package it.

For supplied chart/application versions, `make helm-package CHART_VERSION=0.1.0 APP_VERSION=0.1.0` packages this companion and the independent application chart under `output/artifacts/`, using the same shared-asset assembly. It includes the project license and does not change the upstream monitoring image pins. See [build and release](../../../documentation/src/content/docs/contributing/build-and-release.md).

Existing Secret and PVC references accept Kubernetes DNS subdomain names, including dots, up to 253 characters. Namespace values retain the 63-character DNS label limit.

See [Monitoring with Prometheus and Grafana](../../../documentation/src/content/docs/operations/helm-monitoring.md) for installation, values, permissions, storage, credentials and removal. The same guide covers existing collectors and files under `deployments/monitoring/`. `values.schema.json` validates the supported inputs. `make helm-check`, `make deploy-check` and `make test-minikube-monitoring` cover rendering, Kubernetes schemas and isolated runtime qualification.
