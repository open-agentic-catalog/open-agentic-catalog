---
title: "ADR-0007: Secrets and credential resolution"
---

- Status: Accepted

## Question

Does the Catalog need Vault or its own secret-management system?

## Context

The service needs runtime secrets such as PostgreSQL credentials and purpose-specific cryptographic keys. Authenticated federated sources may also need a token, API key, client certificate, or other credential selected during source onboarding.

How those values are stored, rotated, and delivered is an SRE and platform choice. [Kubernetes Secrets](https://kubernetes.io/docs/concepts/configuration/secret/), mounted files, workload identity, Vault, cloud secret managers, External Secrets, Container Storage Interface (CSI) drivers or a platform connection service can all satisfy different deployments. Building a provider into the Catalog core would reduce portability and duplicate infrastructure responsibilities.

## Decision

The Catalog does not deploy, require, or implement a secret manager. SRE selects the secret system and delivery mechanism for each environment.

### Runtime secrets

The service reads runtime values from injected environment variables or mounted files at its deployment boundary. It has no built-in secret-provider SDK or credential-chain integration. It does not need to know whether Kubernetes, Vault, a cloud secret manager, or another system supplied them.

- Compose uses disposable local environment values or mounted files.
- Minikube uses native Kubernetes Secrets by default.
- Production uses the mechanism selected and operated by SRE.
- Helm accepts secret names and key references; secret values do not belong in checked-in values, images, or ConfigMaps.
- Workload identity is preferred where a provider supports it, but it is not a Catalog-specific requirement.

### Federated-source credentials

PostgreSQL stores only an opaque `CredentialBinding` and non-secret authentication metadata. A narrow `CredentialResolver` port resolves that binding just in time when an outbound request requires it.

Authorized API clients select an existing binding; the Catalog does not accept or manage raw source credentials:

- In any deployment, the baseline binding is an SRE-provisioned opaque reference understood by the configured resolver.
- An integrated platform may optionally resolve that reference through an existing governed connection adapter.
- The implemented resolver reads a deployment-mounted credential tree. External secret systems may populate that tree. A direct provider adapter would require a separate reviewed implementation; none is supplied or required.
- If no resolver is configured, public and otherwise unauthenticated federation remains available while credentialed source operations report that capability as unavailable.

Resolve only the credential needed for the current request. Limit metadata and token or API-key files to 64 KiB each. Limit certificate and private-key files to 1 MiB each. The mounted-file resolver reads files for each request and keeps no credential cache.

Exclude resolved credentials from errors, logs, traces, audit payloads, Jobs, observations and API responses. Attach them only to the exact approved origin and authentication profile. They are never forwarded across an origin-changing redirect.

### Service-generated secrets

A secret created by a Catalog-owned protocol is a narrow exception, not a general credential store. Webhook signing generates a per-endpoint secret. The Catalog retains encrypted ciphertext so the worker can sign deliveries.

Its encryption key is injected by the deployment. This exception must remain purpose-separated, access-controlled and rotatable. Creation and rotation responses return plaintext; ordinary endpoint reads omit it.

It does not allow source credentials or arbitrary user secrets to be stored in Catalog tables.

## Rationale

- Kubernetes-native injection is sufficient for simple deployments and local Minikube testing.
- Vault remains available when an SRE already operates it, without becoming a product dependency.
- The image and Helm chart use standard Kubernetes interfaces. A selected deployment must qualify its Kubernetes environment; this does not claim a tested distribution matrix.
- Opaque bindings preserve standalone operation while allowing an integrated platform to reuse its governed connection service through an optional adapter without copying credentials into Catalog state.
- Public federation remains usable when private credentials are not configured.

## Responsibility boundary

| Owner | Responsibility |
| --- | --- |
| SRE or owning platform | Choose and operate the provider, create bindings, authorize workloads, inject platform values, rotate and revoke credentials, configure availability, and handle provider incidents. |
| Catalog | Store opaque references, authorize their selection, resolve only when required, bind credentials to approved origins, redact values, fail the affected operation closed, expose capability state, and audit reference use without secret material. |

## Consequences

- There is no mandatory Vault, KMS, CSI, External Secrets, or cloud-secret-manager dependency.
- The Helm chart and configuration model expose references and mounting hooks rather than secret values or one provider's schema.
- Credentialed inspection, testing, synchronization and live retrieval require enabled credential resolution. The selected binding must resolve for the exact tenant and origin. A missing, revoked, malformed, expired or changing binding fails only the affected operation. The resolver is not a separate global readiness dependency.
- Provider-specific installation, authentication, high availability, encryption, and rotation remain deployment documentation or SRE automation, not Catalog domain logic.
- Production cursor keys are deployment-injected. Rotating a key invalidates outstanding short-lived cursors. General artifact signing and advanced trust remain unavailable. The optional [ANS verifier](0024-optional-ans-registration-verification.md) uses operator-supplied public keys, not a Catalog secret manager. Dual-key webhook rotation applies only when the deployment intentionally enables webhooks.

## Verification

- The service runs in Compose and Minikube without Vault or another secret-manager process.
- Production startup rejects missing secrets required by enabled capabilities without printing their values.
- Public federation works with no credential resolver; an operation that selects a credential binding fails safely and without secret detail when resolution is disabled or the binding cannot be resolved.
- Resolver contract tests cover authorization failure, wrong origin, expiry, rotation, outage, timeout, cancellation, redaction, and redirect stripping.
- One source binding can be revoked without restarting unrelated tenants or exposing prior values.
- Deployment manifests contain secret references only, and diagnostics and support bundles reveal neither secret values nor raw credentials.
