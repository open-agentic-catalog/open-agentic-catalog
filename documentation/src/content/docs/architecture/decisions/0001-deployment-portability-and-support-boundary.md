---
title: "ADR-0001: Deployment portability and support boundary"
---

- Status: Accepted

## Question

Which runtime prerequisites and compatibility claims accompany the container image and Helm chart?

## Context

An OCI container can start on many platforms, but packaging portability is not the same as a supported operating contract. A support statement answers questions that the image alone cannot answer: which Kubernetes API versions are tested, which storage and ingress capabilities are assumed, how upgrades work, what failure behavior is expected, and which combinations receive compatibility and recovery coverage.

Without a boundary, "runs on Kubernetes" can accidentally imply that every distribution, version, ingress controller, storage class, security policy, CPU architecture, and upgrade path is supported. The service should remain provider-neutral without making an untestable promise.

## Runtime contract

- Support CNCF-certified Kubernetes 1.34+ distributions meeting the deployment prerequisites, including OpenShift, through standard APIs and portable Helm templates. Both charts declare the minimum and validate against 1.34 schemas. Test environments are evidence, not an allowlist.
- Validate locally with Docker Compose and Minikube through the Helm chart. Do not maintain a separate distribution certification matrix at this stage.
- Publish OCI images for `linux/amd64` and `linux/arm64`.
- Build `catalogd` and `catalogctl` as CGO-free static Go executables and package them in a literal `FROM scratch` runtime stage.
- Pin the multi-architecture Go builder image and every Go module input. `scratch` is Docker's reserved empty base and has no external image manifest or digest to pin.
- Strip the Go symbol table and DWARF data, remove source paths and the linker build ID, disable automatic toolchain and module changes during the container build, and retain explicit version, commit and source state plus Go module build information needed for dependency inspection.
- Embed the maintained Go `golang.org/x/crypto/x509roots/fallback` NSS root set in both executables so public HTTPS works without an operating-system trust store. Keep that dependency current as security material.
- Supply private CA and mTLS material through connection-scoped deployment mounts and references. Do not add a global insecure mode, copy private trust material into the image, or broaden one private source's trust to unrelated outbound connections.
- Run as non-root and remain compatible with Kubernetes restricted and OpenShift restricted-v2 security policies.
- Keep the application and Helm chart independent of managed versus self-operated Kubernetes.
- Ship one backend OCI image and one application Helm chart. The optional Catalog UI has its own image; see [ADR-0018](0018-optional-catalog-browser-client.md). An independently optional monitoring companion chart provides Prometheus/Grafana configuration without adding an application dependency. An operator may adapt the charts or launch the image without Kubernetes while preserving the documented runtime contract; no separate platform-specific or offline package is selected.

## Decision

Adopt a portable artifact contract with the following validation and operator boundaries:

| Class | Purpose | Commitment |
| --- | --- | --- |
| Local | Fast development with binaries or Compose | Functional workflow testing, no availability claim. |
| Local cluster | Minikube using Helm | Kubernetes install, reconfiguration, role separation, probes, topology, disruption, and network-policy testing; real-version upgrade testing begins after a supported predecessor exists. |
| Operator deployment | SRE-selected environment; standard Kubernetes chart supplied | The operator supplies compatible dependencies and policies in a dedicated or shared cluster. A non-Kubernetes launcher may supply equivalent runtime behavior. Neither possibility certifies an untested stack or creates a new permanent test matrix. |

The application does not distinguish managed from self-operated Kubernetes. Operators select the distribution, database, ingress, certificates and secret integration. They will also select the provider if managed media is implemented and enabled.

Compose tests container behavior. Static checks validate Helm templates and the Kubernetes 1.34 minimum. Isolated single-node and three-node Minikube profiles test developer-built Helm installation, reconfiguration, topology, disruption and NetworkPolicy. No mandatory offline schema store or separate candidate runner is required. Real upgrade and rollback cases begin after a supported predecessor exists.

The service targets CNCF-conformant Kubernetes distributions, including OpenShift. This does not certify every distribution and version. Describe a named distribution as tested only after the recorded build runs there. An OKD or OpenShift restricted-SCC execution claim requires actual OKD admission.

The canonical Containerfile implements the scratch runtime, embedded public roots, numeric non-root identity, read-only-compatible filesystem layout, explicit OCI metadata, and stripped static build. The maintained Minikube topology supplies real restricted-admission and operator-owned NetworkPolicy evidence for developer-built images. Existing-image inspection, shipped license files, local-image SBOMs and versioned chart packages are implemented; Linux AMD64 and ARM64 are supported through the OCI image contract, target-specific builds and binary inspection. CI/CD and publishing are configured separately.

## Why this matters

- It preserves portability while making support and testing honest.
- It removes the operating-system userland, shell, package manager, dynamic loader, and system CA package from the runtime attack surface while retaining the exact runtime data the service needs.
- It tells SRE which dependencies the chart expects instead of silently provisioning production infrastructure.
- It defines which portable upgrade and failover paths must pass before artifact release and which backup or environment paths instead qualify a selected production deployment.
- It prevents provider-specific behavior from leaking into domain code.
- It lets local Minikube testing catch Kubernetes failures that Compose cannot reproduce.

<!-- markdownlint-disable MD033 -->
<a id="operator-owned-deployment-clarification"></a>
<!-- markdownlint-enable MD033 -->

## Deployment responsibilities

Production infrastructure is selected and operated by the adopting deployment team. Compose and Minikube remain controlled local test fixtures, never production blueprints. The OCI runtime has no Kubernetes API dependency; Helm is the maintained Kubernetes packaging, not a requirement for launching the image. Operators choose compatible managed or self-operated PostgreSQL and infrastructure independently, subject to ADR-0004's exact database contract.

Downstream chart customization is expected, not automatically qualified. Prefer existing values, preserve runtime/security/persistence invariants, record the effective manifests and revalidate affected boundaries. Upstream results apply to recorded image/chart inputs, not arbitrary operator modifications. Shared clusters do not grant the app cluster-wide authority or permit test harnesses to seed/reset/delete shared resources.

Named-deployment acceptance belongs to the operator unless the project selects a specific environment claim. Retain bounded generic image, Kubernetes-version and application database-recovery evidence; do not add a cloud/distribution/provider matrix or require every adopter to reproduce our local topology.

## Compatibility, not an operating commitment

Document the required APIs, Kubernetes 1.34 compatibility minimum and actual test environments. Deployment operators manage patch schedules, availability and recovery objectives. Local repository tools build and test artifacts without publishing them.

## Consequences

- The Helm chart must use stable Kubernetes APIs and document required cluster capabilities.
- Both charts use `kubeVersion: ">=1.34.0-0"`. The suffix permits vendor-version strings without an engine or distribution allowlist; it also accepts upstream prerelease strings, which does not establish production suitability for preview builds. Check version acceptance/rejection and schemas at the minimum, and retain ordinary Minikube runtime coverage. A new tag does not require extra clusters, native ARM hardware or separate container-engine qualification.
- Provider-specific annotations and integrations belong in values or overlays, not shared templates unless optional.
- Docker Compose is the maintained local container environment. Minikube through Helm supplies the maintained developer cluster environment without claiming production qualification.
- OpenShift and other CNCF-conformant distributions are compatibility targets based on standard APIs and restricted security behavior, not separately certified environments. A selected K3s, OKD, OpenShift, cloud, runtime, ingress, or storage result applies only to the exact recorded stack.
- A discovered portability defect is handled as a service or chart compatibility issue unless the deployment depends on a non-standard cluster capability.
- No decision about managed versus self-managed Kubernetes is required from product architecture.
- The application binary, rather than an operating-system package, owns the default public root set. Root changes require a dependency update, rebuild, scan, and release; `govulncheck` and dependency review cover the pinned root package.
- Private trust remains operator-supplied and narrowly scoped. PostgreSQL, identity, managed media, federation, and webhook clients must receive only the trust and client identity intended for that connection.
- Named time-zone data, user-name lookup, a shell, and command-line diagnostic tools are absent. The current service uses UTC and numeric identities; a future named-zone requirement must deliberately embed Go `time/tzdata`. Operational debugging uses logs, metrics, API diagnostics, or an authorized ephemeral debug container rather than modifying the release image.
- An image scan with no operating-system packages is not complete security evidence. Go dependencies, reachable symbols, embedded root material, configuration, manifests, and the final image still require the release security gates.

## Verification

- Compose smoke tests pass without Kubernetes.
- The documented automated tests cover fresh single-node and bounded multi-node Minikube installation. They verify exact image identity from local build to Pod and developer-image reconfiguration. They test revision-scoped replica spread, Eviction-based disruption, separate direct Pod loss and hard-spread degradation after node loss. They check NetworkPolicy enforcement and require no request to the denied fixture. Each test removes only its own resources under a separate time limit. Keep these as ordinary local/CI build tests; real supported upgrade and rollback-before-contract-migration join only after a predecessor exists.
- Source and packaged charts enforce the Kubernetes 1.34 minimum and pass schema checks at 1.34.0. Record the existing Minikube runtime version and fixture details as test evidence, not a required production stack or a claim of runtime testing at every accepted version.
- The rendered chart passes schema validation and runs without privileged containers.
- The Helm chart uses stable standard APIs and contains no mandatory cloud- or distribution-specific integration.
- Documentation distinguishes the portable support contract, actual build/test results and operator-owned environment responsibilities.
- Linux AMD64 and ARM64 image builds and static inspection verify target architecture, static/stripped binaries and included licenses. Native ARM hardware and per-engine runtime qualification are not release requirements.
- Both executables are statically linked with CGO disabled, contain no symbol or DWARF tables, expose their explicit version and commit, and run from a literal scratch filesystem as a numeric non-root user.
- A scratch-image test proves public HTTPS verification through the embedded root set with no system CA files. Connection tests prove mounted private roots and mTLS credentials are accepted only by their assigned dependency or federation profile once those capabilities are enabled.
- Image inspection proves the runtime contains only the two executables, packaged license/notice files and deliberately created application directories, with configuration, credentials, writable temporary space, and persistent state supplied as mounts.
