---
title: "ADR-0009: Public and private discovery"
---

- Status: Accepted

## Question

Who may call this Catalog's ARD, Registry, AI Catalog, and published representation endpoints, and does that require an external product control plane?

## Context

Inbound discovery is separate from outbound federation. ADR-0008 governs which remote URLs this service may contact. This decision governs which callers may discover resources published by this service.

The Catalog has its own administrative API for resources, sources, lifecycle, publication, and audit. That API is part of this service; it is not a dependency on another product. A standalone deployment and an integrated deployment use the same image, API, data model, PostgreSQL schema, publication engine, and Helm chart.

[ARD v0.91](https://github.com/ards-project/ard-spec/blob/aa3e598bb7752a9175897823234311216acfa864/spec/ard.md) defines well-known publication discovery and a Registry REST interface but does not define a Registry authentication scheme. Its OpenAPI includes `401` responses, and its conformance tool can attach operator-supplied headers, but private clients need out-of-band credential configuration. Authentication to an Agent or MCP Server after discovery remains a separate artifact-protocol concern.

## Decision

The service exposes three access planes. The application assigns distinct route families and authorization contracts; the deployment owns their network routing and isolation.

| Plane | Access | Purpose |
| --- | --- | --- |
| Catalog administration | Signed JWT required | Catalog records, lifecycle, source management, publication, audit, and complete authorized tenant queries. |
| Discovery | Exact host configured as `disabled`, `anonymous`, or `authenticated` | ARD, Registry, AI Catalog, and authorized published representations. |
| Operations | Unauthenticated application contract, restricted to a cluster or SRE network by deployment routing | API liveness, readiness, version, metrics, and served OpenAPI plus worker startup, liveness, readiness, status, version, and metrics on a separate operations-only listener. |

The API operations routes share the API listener and Kubernetes Service. SRE must keep them off unintended external ingress. The worker listener is a named container port that is absent from the product Service and Ingress and contains no product routes. Diagnostics, migrations, identity-realm administration, and focused repair are `catalogctl` operator commands using operator-provided authority; they are not HTTP endpoints on either listener.

Discovery has no default host mapping. Enabling it requires an explicit operator host mapping and an active immutable snapshot. Unknown and disabled hosts return `404` before authentication or tenant-state lookup. An authenticated host checks the Bearer identity, host tenant, current tenant `discovery.read` permission, and any required OAuth scope before snapshot access; missing or invalid credentials return `401`, and a tenant mismatch, denied permission, or insufficient scope returns `403`, even when no snapshot is active. After host authorization, an absent active snapshot returns `404`.

### Host mapping

One exact normalized DNS host or canonical IP address maps to one internal tenant UUID, one exposure mode, and one publisher domain. Separate service-wide startup settings select the implemented ARD and AI Catalog output profiles applied to every enabled host. These deployment-owned settings form the complete v1 publication configuration. The tenant is never selected through a query parameter, request body, arbitrary header, token-supplied internal UUID, or path segment.

One tenant may use separate hosts for separate audiences, for example:

- `catalog.example.com` for anonymous public discovery.
- `catalog.internal.example.com` for authenticated tenant discovery.

Attaching a token never changes a host's audience or adds records. Hostname, TLS identity, tenant, mode, and publication scope remain one reviewed mapping. The service does not trust forwarding metadata. Discovery requests with `Forwarded`, `X-Forwarded-Host`, or `X-Original-Host` fail closed. Ingress must preserve the reviewed `Host` and strip those fields; any future trusted-proxy support requires explicit ingress-address and conflict controls justified by a selected deployment need.

### Anonymous discovery

An anonymous host serves local discovery metadata from the currently active immutable public snapshot. Candidate construction includes every eligible approved release with `public` visibility and a representation safe for the selected output profile. ARD Search may additionally return an unchanged bounded live window in core mode, a separate group of live results after the durable hybrid ranking, or approved referrals under ADR-0010's exact host, audience and disclosure policy; neither becomes part of the retained snapshot.

The snapshot excludes Catalog-owned private state: drafts, tenant-only and namespace-private records, raw observations, source configuration, credentials, Jobs, audit and unpublished evidence. It excludes internal tenant/database identifiers and mutable Catalog administration state. Public output is a discovery description, not installation, invocation, entitlement, trust, or runtime authorization.

Approved native metadata can contain private-address runtime URLs. Authors and reviewers choose the content disclosed in those documents. Publication does not automatically redact those endpoint references. A reference grants no Catalog outbound or execution authority.

Anonymous discovery authorization uses a fixed anonymous principal. Local result content comes only from the host's active snapshot and publication-authorized bounded documents; handlers never query mutable authoring records to fill a missing field. ARD Search separately uses the administrator-approved federation topology for permitted live queries or referral metadata, without exposing private source configuration or raw observations. Managed media remains unavailable.

### Authenticated discovery

An authenticated host requires the ADR-0006 signed JWT access-token contract. The token may come from any issuer configured by the standalone deployment or from an integrated platform; no product-specific gateway, trusted identity header, or separate login flow is required. An ingress gateway may add routing, rate limiting, or another independent check, but the Catalog still validates the Catalog-audience token.

The resolved token tenant must match the host's configured tenant. The caller also needs current tenant `discovery.read` permission and the `catalog.read` OAuth scope when scope enforcement is enabled. Authenticated discovery serves an immutable private-discovery snapshot containing every currently eligible approved release with `tenant` or `public` visibility. Namespace-private resources remain on the authenticated Catalog query API, subject to current permissions in each namespace. Their future ARD exposure requires an audience-specific publication model. Do not mutate one shared manifest for each user.

Private discovery uses the same ARD documents and operations as anonymous discovery. It does not invent a product-specific schema. Clients configure the bearer credential out of band because the pinned ARD profile does not define Registry authentication discovery.

### Standalone and integrated operation

Standalone operation is the baseline architecture. It requires PostgreSQL 18, deployment configuration, TLS when network-exposed, and a customer-selected signed JWT issuer for production administration or authenticated discovery. Compose development may use fixed test identities.

Integrations are optional adapters:

- Product UIs, IDE extensions, CLIs, automation, and platform integrations call the same APIs.
- An integrated platform may issue or exchange a Catalog-audience JWT under ADR-0006.
- A product-specific connection object is only one optional implementation of ADR-0007's outbound source CredentialBinding. It is unrelated to inbound discovery and is never a service dependency.
- Standalone source credentials resolve through deployment-mounted files that SRE may populate from Kubernetes or an external secret system; public sources require no credential binding.

The Catalog owns its resource lifecycle, federation sources, snapshot history, and discovery responses in both modes. The deployment owns the exact startup host mappings. These operations require no external product control plane.

### Caching and delivery

- A CDN is optional and never a core dependency.
- Anonymous well-known aliases use `public, no-cache, no-transform` with validators so every reuse revalidates the active pointer. Authenticated aliases use the corresponding private policy and vary on authorization.
- ARD Search and Explore use `no-store`. ARD List and publication-authorized documents use a fixed 60-second public or private `must-revalidate` policy according to host mode; authenticated responses vary on authorization.
- `stale-if-error` is absent from revocable active aliases and publication-authorized documents. A deployment must not place a less restrictive cache in front of them without a separately qualified withdrawal bound.
- A shared public CDN is optional and never stores authenticated discovery by default. Ingress owns general request-rate enforcement, but it is not the service process's memory-safety boundary. Database selection is bounded, and ADR-0014 supplies one cooperative product-work deadline. Fixed FIFO Job and automatic-source work have qualified bounded progress without a tenant scheduler. One process-local publication-construction slot limits the measured memory demand. Add fairness or admission controls only when evidence shows that the existing limits are insufficient.

## Rationale

- Anonymous ARD lets a client discover deliberately public capabilities before it knows the publisher's identity platform.
- Authenticated ARD supports enterprise federation without exposing tenant content or inventing another discovery format.
- Exact host mapping is the only unambiguous tenant selector available before anonymous authentication and prevents host confusion.
- Separate public and private hosts keep caching, publication scope, observability, and incident withdrawal understandable.
- Immutable snapshots prevent a public handler from leaking drafts or mutable state and make rollback and withdrawal explicit.
- Provider-neutral JWT validation and optional adapters preserve standalone operation.

## Consequences

- Exact normalized multi-host mappings, a disabled default, anonymous and authenticated modes, resolved-token tenant matching, audience-isolated immutable snapshots, publication-authorized documents, private/public cache separation, and synchronous domain or release withdrawal are implemented. Unknown, malformed, conflicting, and forwarded hosts fail closed before mutable tenant state is consulted.
- Deployment configuration declares each discovery host, tenant mapping, mode, and publisher domain plus the service-wide output profiles. Service tests cover host, cache, and withdrawal behavior. The adopting operator qualifies ingress, rate enforcement, and cache integration. Trusted forwarding, configurable cache bounds, or an anonymous CDN are added only when a selected deployment demonstrates a need. Forwarding-host headers remain rejected until a trusted-forwarding boundary exists. Curated per-host membership or update modes require a demonstrated future requirement and a separate decision rather than a speculative database control plane.
- Public and private discovery have separate host, authorization, cache, immutable-snapshot integrity, revocation, and negative data-leakage tests.
- A client of a private Registry needs an out-of-band bearer credential binding because ARD does not define how to acquire it.
- General discovery signing remains unavailable. Optional [ANS registration verification](0024-optional-ans-registration-verification.md) has its own limited authority contract. Indexing and publication confer no invocation authority.
- More granular namespace or partner-specific ARD audiences require a distinct future audience design or exact hosts; they are not silently approximated through mutable per-request manifests.

## Verification

- Disabled, unknown, conflicting, and spoofed hosts return no discovery data.
- Anonymous discovery responses expose active public snapshot content, its authorized documents, and only the bounded live results or referrals explicitly permitted by the host's federation policy. They never expose control, tenant-private, namespace-private, private source configuration, raw evidence, Job, audit, credential, or operations data. Separate unauthenticated operations routes retain the deployment-routing restrictions described above.
- An authenticated request with a matching host and tenant, current `discovery.read` permission, and any enforced `catalog.read` scope can read the active tenant-discovery snapshot and only the live/referral additions permitted for that host and audience; missing, invalid, wrong-audience, expired, or cross-tenant tokens and insufficient current permission or scope fail closed.
- Adding a token to an anonymous host does not expand results, and omitting one from an authenticated host returns the documented authentication challenge.
- Host mappings prove one tenant and one mode per host while allowing separate public and authenticated hosts for the same tenant.
- Snapshot activation, rollback, unpublish, fixed cache revalidation, and emergency withdrawal preserve immutable local rendering: anonymous handlers never reconstruct local results from mutable authoring state. The adopting operator measures complete withdrawal timing through its actual ingress and caches.
- Qualify the same image through standalone Compose and Helm-through-Minikube tests without an external product service. An optional integrated issuer must satisfy the same token and API contracts; local results do not qualify every external issuer.
