---
title: "ADR-0003: Runtime packaging and local test environments"
---

- Status: Accepted

## Question

Which workflows does the project maintain for development, testing and deployment?

## Context

Direct binaries, Compose and Minikube test different boundaries. A direct process provides a fast development loop. Compose tests containers and external dependencies. Minikube tests Helm and Kubernetes workload behavior. Operators select production infrastructure and qualify its ingress, identity, storage and availability configuration.

Controlled identity fixtures can produce malformed input, key rotation and outages. An independent provider tests ordinary protocol compatibility. Neither replaces the other.

## Decision

Maintain four workflows:

1. Run `make run` for the combined, in-memory development process.
2. Run `make compose-up` for local PostgreSQL 18, separate API and worker processes, and persistent development data.
3. Use the owned Minikube harness for Helm installation, migration ordering, workload restart, reconfiguration, isolation, disruption and topology tests.
4. Supply the standard application Helm chart for operator-selected Kubernetes deployments with external dependencies.

The OCI process contract also permits an operator-owned launcher without Kubernetes. Docker Compose and Minikube remain the maintained local orchestration choices. No additional distribution or container-engine matrix is required without a specific compatibility need.

Compose and Minikube supply disposable database and identity fixtures separately from the application chart. An optional monitoring companion chart is independent of application installation and readiness. Operators may customize deployment inputs while preserving the runtime, security, migration and health contracts in [deployment portability](0001-deployment-portability-and-support-boundary.md).

### Packaged OIDC qualification

Use both controlled fixtures and one independent provider. The independent packaged scenario uses the official Keycloak image pinned in the [OIDC overlay](../../../../../../test/functional/compose.oidc.yaml). It tests real HTTPS discovery, JWKS retrieval and short-lived Catalog-audience tokens under the Catalog production configuration.

The provider runs with fresh disposable storage and generated TLS and client-secret inputs. It imports a temporary realm file and uses the client-credentials grant. It enables neither password grants nor administrative bootstrap for this scenario. Provision Catalog membership explicitly after token acquisition; decoded claims do not grant authority.

Keep credentials, tokens and private keys out of arguments, reports and committed fixtures. Use one exact issuer and scoped private trust without host-DNS changes or insecure certificate verification.

Controlled fixtures own malformed-document, redirect, rotation, cache-expiry and outage cases. Injected clocks test exact time boundaries. The Minikube identity fixture tests in-cluster configuration and verifier behavior; it does not replace the independent production-profile HTTPS scenario.

Keycloak is external test tooling. It is absent from Catalog images, Go runtime dependencies, the normal development topology and the application Helm chart. Its source license does not describe every component in its image. Keep tool reviews and image inventories under ignored `output/`. Do not bundle or mirror the provider with Catalog artifacts.

Use the existing [functional testing workflow](../../contributing/testing.md#functional-coverage-inventory) for commands, prerequisites, resource limits and cleanup. Compatibility results apply to the exact tested provider configuration, not every issuer.

### Local cluster testing

The Minikube harness owns a collision-resistant profile, temporary configuration, explicit context and disposable dependency storage. It records the built image identity and requires every application Pod to use that exact image. Deadlines, bounded reports and independent cleanup prevent interference with retained developer clusters.

The single-node scenario tests installation, migration failure and safe retry, restart persistence and configuration. The multi-node scenario adds replica coordination, worker takeover, disruption, node loss and enforced NetworkPolicy. Application traffic must prove both allowed and denied connections. These tests do not establish production capacity or availability.

Test actual supported release upgrades when a supported predecessor exists. Current development reconfiguration does not simulate a historical release or require a separate candidate runner.

## Publishing ownership

Local Make targets build, inspect, test and package artifacts. Architecture builds use separate local tags and require no local registry or release-bundle service. The maintainer alone creates release tags and handles external publication or uploads. Follow the [build policy](../../contributing/build-and-release.md#application-version-and-release-checks).

## Image and chart contract

- One non-root scratch backend image contains static, stripped `catalogd` and `catalogctl` executables.
- The optional UI has its own image and no database authority.
- API, worker and migration workloads use separate Kubernetes ServiceAccounts and database identities. The chart grants no application Kubernetes RBAC and mounts no service-account tokens by default.
- Schema-owner migrations run explicitly. The coordinated deployment workflow drains runtimes before migration; raw Helm hooks do not drain them.
- Migration prerequisite hooks precede the migration Job. Failed Jobs remain available for bounded diagnosis, and retries recreate the required hooks safely.
- Containers use read-only filesystems, no capabilities and restricted security settings.
- Values accept external endpoints, configuration and secret references without selecting a cloud vendor.
- Operators configure production ingress. Local NodePort and port-forwarding remain test conveniences.

## Alternatives and consequences

Controlled fixtures alone cannot prove independent-provider compatibility. Keycloak alone makes precise malformed-input and timing tests brittle. One real provider plus controlled fixtures covers both boundaries without maintaining several provider products.

A production-mode Keycloak cluster, hosted identity account or custom provider image would test additional infrastructure without a Catalog requirement. Operator deployment acceptance remains separate from repository qualification.

Minikube-specific settings must not become production defaults. Additional Kubernetes distributions, supplied-image runners or orchestration tools need a concrete boundary that existing tests cannot cover.

## Verification

Use the existing Compose, independent OIDC and Minikube gates. Verify exact image identity, role separation, migration ordering, retained state, authentication, tenant isolation and owned cleanup. Use the multi-node gate for replica replay, worker takeover, disruption and network enforcement. Preserve exact PostgreSQL privilege and grant-failure tests in the database suite instead of duplicating them in every deployment fixture.

Record build-specific results and unresolved failures in test records. Passing these fixtures does not certify an operator's production deployment.
