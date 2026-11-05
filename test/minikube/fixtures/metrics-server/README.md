# Metrics Server test input

This fixture copies the Metrics Server v0.9.1 deployment manifest. It supplies CPU metrics only in the disposable autoscaling scenario.

- Upstream: <https://github.com/kubernetes-sigs/metrics-server/releases/tag/v0.9.1>.
- Commit: `550205bb75d8997b3d61d55f3972c302cede0545`.
- Release date: October 29, 2026. The maintainer approved this exact 14-day-rule exception on November 1, 2026.
- License: Apache-2.0. Keep the adjacent upstream `LICENSE`.
- Original `components.yaml` SHA256: `3c7f57cdbee0f57fb0259a150e8f6d6cb4f29d72d1aa5dce8803151dd6d4c2a1`.
- Normalized JSON SHA256: `32d55bd09f7d61eeee20e543364730403cb77b291e67a59a4a12ae0dfe72ea4e`.

The JSON contains the same nine upstream objects. `autoscaling.go` checks its digest before use. The test aliases the pinned image to an invocation-owned local tag and forbids image pulls. It verifies the running image identity, sets one replica and bounds CPU and memory. It adds `--kubelet-insecure-tls` because the disposable Minikube kubelet uses a self-signed certificate. The upstream APIService also skips certificate verification. These settings do not qualify production TLS. The application chart does not install these objects or grant runtime access to metrics.

The selected multi-platform image digest is `sha256:9b0ba1926e1d3089673f45461c3c1416109dea33b3ba3434048defd380df329b`. The fixture executes only its Linux AMD64 image, `sha256:4dbfba731a021437b4cab5ba2092569edbd898030c27d10c6ec85e03b63789f6`. The image is an external test tool. Its code is not imported into or bundled with Catalog images.
