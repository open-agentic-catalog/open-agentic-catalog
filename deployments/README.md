# Deployment assets

These assets package the Catalog or support its local test environments. They do not install or operate a production database, identity provider or Kubernetes cluster. Start with the [deployment contract](../documentation/src/content/docs/operations/deployment.mdx) for runtime inputs and operator responsibilities.

| Directory | Contents |
| --- | --- |
| [container/](container/README.md) | Container build maintenance and compatibility `Dockerfile`, kept byte-identical to the root [Containerfile](../Containerfile). Edit the canonical file and synchronize the copy; `make container-sync-check` detects drift. |
| [helm/open-agentic-catalog/](helm/open-agentic-catalog/README.md) | The product chart: API/worker Deployments, migration hook, optional UI, configuration, role identities, probes and values schema. Its `ci/` directory contains render-test values, not production presets. |
| [helm/open-agentic-catalog-monitoring/](helm/open-agentic-catalog-monitoring/README.md) | Independently optional Prometheus/Grafana chart, packaged with shared dashboard assets; not an application dependency. |
| [postgres/](postgres/README.md) | Local Compose database bootstrap, not application migrations or a production database installer. |
| [monitoring/](monitoring/README.md) | Shared optional Compose/Helm monitoring assets and reusable Catalog dashboard queries. |
| [embeddings/](embeddings/README.md) | Pinned local BGE/MiniLM profiles used by optional Compose inference and qualification harnesses. Runtime enablement and semantic-schema setup remain explicit. |

Compose models live at the repository root; disposable functional, performance and Minikube fixtures live under [test/](../test/README.md). Production consumers can use the image outside Kubernetes or adapt the Helm chart while preserving the documented runtime boundary.

From the repository root, `make container-check` checks source consistency and container lint; `make container-verify` builds and verifies the selected image. `make helm-check` tests renders and chart packaging without a cluster. `make deploy-check` additionally validates Kubernetes schemas and may download schemas. Runtime changes need the relevant Compose and Minikube gates in the [testing guide](../documentation/src/content/docs/contributing/testing.md). These commands do not publish artifacts.
