# Flux maintenance fixture

This fixture runs Flux v2.10.0 on the disposable Linux AMD64 Minikube profile. Source-controller v1.10.1 reads two immutable test chart versions from a cluster-local Helm repository. Helm-controller v1.7.1 owns one standalone HelmRelease. No Git server, hosted registry publication or enclosing Kustomization is required.

## Prepare the test inputs

From a Linux AMD64 checkout with Python 3, Docker, the GitHub CLI and the [Minikube prerequisites](../../README.md), run:

```sh
make flux-tools-install
```

The target verifies the CLI archive before running `flux install --export`, checks the generated manifest, and caches it in `.tools/minikube-flux/`. It pulls missing controller and chart-server images by digest. Rerunning it reuses verified local inputs. The checksums and image references are in [pins.json](pins.json). The export and image pulls do not contact a Kubernetes cluster. See the [Flux release](https://github.com/fluxcd/flux2/releases/tag/v2.10.0) and [upstream license](https://github.com/fluxcd/flux2/blob/v2.10.0/LICENSE).

## Run the qualification

Run `make test-tooling-unit PYTHON_UNIT_DIRS=minikube` to check the installer and handoff decisions without a cluster. For the full deployment, run:

```sh
make test-minikube-flux
```

Use `MINIKUBE_PRELOAD_ARCHIVE` if your installation needs the Kubernetes preload archive. The harness creates and deletes its own profile, network, image aliases and port-forwards. Host-side input caches remain available for later runs. The final JSON identifies the controller images, chart digests, Helm revisions, runtime Pods and HPA identities. Require a successful command exit as well as the report.
