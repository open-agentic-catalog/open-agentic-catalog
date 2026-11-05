---
title: "ADR-0006: Identity and tenancy"
---

- Status: Accepted

## Question

Which identity responsibilities belong in the Catalog, and how does a verified caller resolve to one tenant?

## Context

Authentication establishes who or what is calling. Authorization establishes what that principal may do. Tenancy establishes which organization's data the request can address. These remain separate controls.

Resolve tenant identity through trusted deployment configuration and provisioned mappings before reading tenant-owned data. Row-level security, idempotency, cursors, Jobs, audit, source credentials and publication all depend on that tenant. The Catalog must not trust a tenant UUID selected through a request header, path, body or arbitrary claim.

The Catalog does not need to become an identity provider. The owning platform, UI, CLI, IDE, automation environment, or external identity service already handles login, consent, token acquisition, refresh, client credentials, and workload identity.

## Decision

The Catalog API is a token verifier, not an OAuth authorization server. It does not implement login, passwords, sessions, consent, PKCE, token issuance, refresh, client registration, or an identity directory. The optional browser host owns its OIDC client login, PKCE and encrypted sessions under [ADR-0018](0018-optional-catalog-browser-client.md). The separate [human CLI login](../../reference/commands.md#human-cli-login) owns an explicit private credential file and serialized refresh. Both clients send the user's access token without changing API authority.

Production callers present a signed JSON Web Token (JWT) access token whose audience includes the Catalog. A standalone deployment configures the trusted issuer and verification keys. An integrated platform may pass through, exchange, or mint a Catalog-audience token, but the resulting token must preserve a verified actor and organization identity. The Catalog validates signature, algorithm, issuer, audience, subject, issued-at time, expiry, optional not-before time and bounded clock skew. If validation cannot complete, it fails closed.

Opaque bearer tokens and a generic trusted-header mode are not part of the production contract. They may be added only through a later adapter with an equivalent verified identity boundary. Fixed bearer tokens remain development and test fixtures and cannot start in a production profile.

### Tenancy modes

Both modes use the same domain model, tenant columns, and forced PostgreSQL row-level security:

| Mode | Tenant derivation | Intended use |
| --- | --- | --- |
| Dedicated | One internal tenant UUID is bound to deployment configuration. A caller cannot select or override it. | Default for customer-controlled and single-organization deployments. |
| Shared | A trusted external organization key from the verified identity maps to an explicitly provisioned internal tenant UUID. | Opt-in for a shared platform deployment. |

Shared mode keys the mapping by identity realm and external organization key. An unknown key fails closed and never self-provisions a tenant. Dedicated mode retains tenant isolation internally; it does not use a separate schema or remove tenant keys.

### Authorization

The Catalog owns domain authorization: action permissions, assignment scopes, record visibility, ownership, lifecycle rules, separation of duties and audit. It stores memberships and scoped assignments in PostgreSQL. The identity provider does not need to carry Catalog namespace UUIDs. A future identity-provider group reconciliation feature would require a reviewed administrative allowlist and could never grant a role directly from a request-time claim.

OAuth scopes or equivalent upstream grants are an optional coarse ceiling. They can reduce what a token may do. Their absence does not require a duplicate permission model. A broad scope never expands a narrower Catalog permission or assignment.

Human and service callers use the same verified-token, tenant, authorization, row-level security, and audit path. Service credentials or workload identities are obtained outside the Catalog and map to explicit service actors; they do not bypass domain controls.

Tenant and realm provisioning, mapping, and realm suspension are operator or owning-platform operations. Whole-tenant hard deletion is outside v1. Support break-glass access remains disabled until an owning organization defines an audited, time-bounded policy.

### Scoped permission model

[Scoped authorization](0022-local-postgresql-authorization.md) defines tenant permissions, custom roles, direct-member groups and exact-namespace assignments in PostgreSQL. Authentication, realm, tenancy and OAuth checks remain independent. Runtime decisions use current retained permissions; compact principals and bounded self-access hints are not authorization authority. The [identity and security guide](../security.mdx) describes the implemented model.

### Public discovery

Public ARD access is decided separately in ADR-0009. Anonymous publication, when enabled, runs as a fixed anonymous principal against an active immutable publication snapshot and cannot reach private Catalog state. The pinned [ARD v0.91 specification](https://agenticresourcediscovery.org/spec/) does not require this service to operate an OAuth authorization server; authenticated discovery-host and Control API policy remain Catalog-owned deployment contracts rather than ARD conformance conditions.

### Provider-neutral qualification

Production interoperability requires both protocol-independent verifier tests and at least one independent provider path. The independent-provider test uses an exact digest-pinned provider for local HTTPS [OIDC discovery](https://openid.net/specs/openid-connect-discovery-1_0.html), JSON Web Key Set (JWKS) retrieval and client-credentials token acquisition. Controlled-provider tests retain deterministic malformed-document, redirect, rotation, cache-expiry and outage cases.

The independent Keycloak scenario follows the [local testing boundary](0003-runtime-packaging-and-local-test-environments.md#packaged-oidc-qualification). Its results apply only to the tested configuration. The provider remains external test tooling, not a runtime dependency.

A selected deployment still qualifies its actual issuer, token-acquisition owner, claim mapping, certificates, and failure behavior through selected-deployment tests.

Client credentials fits the automated real-provider scenario because it represents an application or service caller without automating login, consent, browser redirects, or passwords. The real-provider path proves a Catalog service membership. Human membership classification and denial remain testable with controlled signed OIDC tokens because actor type is Catalog-owned membership state rather than a trusted token role; the actual human login and token acquisition flow belongs to the owning UI, IDE, CLI, platform, or selected-deployment test. Direct Access Grants and other password flows are not introduced merely to manufacture a human token.

The default verifier continues to use the embedded maintained public roots. The verifier accepts one optional bounded private certificate authority (CA) bundle. Only the dedicated configured-issuer discovery and discovered-key transport use it. An internal enterprise issuer and the isolated HTTPS provider test need no process-wide root override.

The bundle does not disable hostname or chain verification. It creates no general trust store, client-certificate support or provider-specific adapter.

## Rationale

- One signed-token boundary integrates with product UIs, IDE extensions, CLIs, automation, platform clients, and standalone identity providers without duplicating their login flows.
- Audience binding prevents a token intended for another service from becoming a Catalog credential.
- Dedicated tenancy keeps the common self-managed deployment simple while preserving a safe path to shared deployments.
- Internal tenant UUID mapping prevents callers from selecting another tenant and keeps external organization identifiers out of persistence keys.
- Catalog-owned namespace authorization avoids oversized, stale, and provider-specific tokens.
- Optional scope ceilings preserve upstream least privilege without forcing two independent role systems to stay synchronized.

## Consequences

- Deployment configuration supplies tenancy mode, trusted issuer and audience, optional issuer-specific private trust, tenant or organization mapping, and optional OAuth scope enforcement. The verifier uses a fixed asymmetric-algorithm allowlist and bounded time rules, not configurable algorithms or clock skew. The verified `sub` identifies the subject; Catalog membership supplies actor type and authority. Direct token roles, groups, and tenant UUIDs do not grant Catalog authority.
- Shared deployments require durable, explicit identity-realm-to-tenant mappings. Operator commands suspend and reactivate realms. Membership administration supports suspension, reactivation and terminal offboarding. Suspending the final active realm requires operators to unpublish active snapshots and disable every configured discovery host for that tenant. The [realm command](../../operations/database-roles.md#identity-realm-administration) checks these prerequisites; it does not change hosts or snapshots. v1 does not add a tenant Control API or hard-delete workflow.
- The signed JWT access-token verifier, dedicated and shared tenant resolver, durable human and service memberships, tenant and exact-namespace assignments, final-administrator protection, optional scope ceiling, key-rotation refresh, and fixed bounded provider-outage behavior are implemented. `catalogctl identity provision` establishes explicit identity state without inferring authority from token claims or historical activity.
- The dedicated verifier supports the optional bounded `auth.trustBundleFile` input. Public roots remain the default, private certificates never enter other clients' pools, and process-wide root overrides are not a supported substitute. Trust loads at API startup; diagnostics report configured presence and the exact loaded content digest, not the file path or PEM bytes.
- Further identity-provider group mapping, if a demonstrated integration requirement later needs it, requires a separate reviewed design and explicit allowlist. It may only create or reconcile Catalog-owned assignments through an administrative boundary and can never become a direct request-time role grant.
- No authorization-server, login UI, token exchange, or credential issuance code belongs in `catalogd`. The optional UI host's client login and session boundary remains separate under ADR-0018.
- No identity-provider SDK, Keycloak extension, provider database, realm administration API, or provider availability check belongs in the runtime or readiness path.

## Verification

- Positive and negative signature, algorithm, issuer, audience, subject, issued-at, expiry, not-before, key-rotation, and clock-skew tests pass.
- Dedicated binding, shared external-key mapping, unknown organization, request-supplied tenant selectors, service actors, and cross-tenant access are tested and fail closed.
- Role, namespace, visibility, separation-of-duties, and optional scope-ceiling tests prove that external claims cannot expand Catalog authority.
- PostgreSQL API transactions set the resolved tenant before tenant-table access, bind the resolved actor to authorization and audit evidence, and preserve forced row-level security in both modes.
- Local development cannot accidentally start with fixed tokens under a production profile.
- Provider outage tests prove bounded cached-key behavior and rejection after the safe cache window.
- The packaged independent-provider gate obtains a genuine Catalog-audience access token through the exact pinned independent provider's client-credentials flow and proves the service-account subject resolves only through its provisioned Catalog membership in dedicated and shared configurations. For the selected Keycloak canary, the gate also binds its exact upstream image and complete image-license inventory under ADR-0003's non-distributed test-tool rule.
- Private-trust tests prove a matching private issuer and its discovered HTTPS key endpoint work only through the configured OIDC transport, a hostname mismatch and an unconfigured private CA fail, public-root behavior remains available, and unrelated HTTPS clients do not inherit the private CA.
