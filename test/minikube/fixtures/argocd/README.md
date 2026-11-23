# Native Argo CD fixture inputs

[pins.json](pins.json) binds the Linux AMD64 CLI, upstream installation manifest, original image references and immutable registry digests. The local installer verifies the file checksums and caches the selected image platform. Runtime uses invocation-specific local tags and records their image configuration IDs. The Minikube baseline supplies Kubernetes, Calico, PostgreSQL and the Catalog dependency fixtures. The full Python image also provides Git for the read-only repository. No fixture Git commit is written to the project checkout.

The Git fixture's init container clones the immutable bundle into a Pod-owned volume. The HTTPS server mounts the clone read-only and reuses it when the container restarts.

| Input | Provenance and licensing |
| --- | --- |
| Argo CD v3.6.2, CLI, install manifest and controllers | [Argo CD at the pinned commit](https://github.com/argoproj/argo-cd/tree/8e8268ef2f3df99ca95663f5080fa6056f156805), Apache-2.0. The controller image bundles Helm, Git and OS packages with separate notices. |
| Dex v2.46.0 | [Dex source and license](https://github.com/dexidp/dex/tree/v2.46.0), Apache-2.0. Part of the upstream installation. |
| Redis 8.10.1 Alpine | [Redis licensing](https://github.com/redis/redis/blob/8.10.1/LICENSE.txt), choice of RSALv2, SSPLv1 or AGPLv3 for the Redis source. Redis modules and Alpine packages have their own terms. |
| Python 3.13.18, Git and Debian Bookworm | [Official Python image](https://github.com/docker-library/python), [Python license](https://docs.python.org/3/license.html), [Git GPLv2](https://git-scm.com/about/free-and-open-source) and Debian packages' individual copyright and license notices. |
| Kubernetes 1.37.2 containerd preload | The [Minikube project](https://github.com/kubernetes/minikube) distributes the archive. The images and packages in the archive retain their individual licenses. The installer verifies the exact archive digest in the pin file. |

For startup, sync and cleanup, see the [operator guide](../../../../documentation/src/content/docs/operations/argocd.mdx).
