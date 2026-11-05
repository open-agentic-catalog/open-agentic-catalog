---
title: "ADR-0008: Federated source connectivity"
---

- Status: Accepted

## Question

Who may register a federated Catalog URL, and under what policy may the service connect to public or private destinations?

## Context

Every federated source begins as a URL. Public and private describe the resolved address and network path. They do not select a source format:

- A public source resolves only to publicly routable addresses and is reachable over ordinary HTTPS egress.
- A private source resolves to an approved enterprise, VPN, VPC, cluster, or other non-public address and may require private DNS, routing, a proxy, a private CA, or mTLS.

The Catalog makes the request from its own API or worker identity. A supplied URL therefore grants outbound authority unless the service checks it. Unchecked destinations could include Kubernetes services, cloud metadata, administrative APIs, another tenant's network, or hosts that later resolve to private addresses.

Source registration is not an anonymous or ordinary contributor operation. External publishers expose their own ARD or AI Catalog endpoint; they cannot register themselves with this service. Each operation requires its current tenant permission: `source.inspect` for inspection and connectivity tests, `source.manage` for source configuration, and `source.sync` for synchronization. The Source Administrator and Tenant Administrator templates supply these permissions. Custom roles can supply them too; template names are not authority.

## Decision

All federation uses one hardened fetcher and one deployment-owned federation egress policy. The ordinary API never accepts an unrestricted network toggle, CIDR, proxy, DNS server, CA bundle, client key, or policy bypass.

### Deployment policy

SRE supplies a versioned structured policy through a mounted JSON document selected by `federation.policyFile` or `CATALOG_FEDERATION_POLICY_FILE`. The environment variable identifies the file; it does not contain a large JSON policy or any secret value. Compose can mount a local policy file, and Helm can create or reference a ConfigMap containing the same document. The implemented document shape and rollout contract are defined in the configuration reference.

The policy has two concepts:

1. A public HTTPS mode: `disabled`, `allowlist`, or `any_public_https`.
2. Zero or more named private profiles containing exact approved origins, expected address ranges, tenant assignments, optional private CA files, and the `direct` transport. Ports are part of each exact origin; mTLS credentials use a separate opaque source binding. Custom-DNS and proxy adapters are not implemented.

Production defaults to `allowlist`. An empty allowlist disables new external-source inspection rather than allowing arbitrary egress. An operator may deliberately select `any_public_https` when authorized source managers should be able to connect any source that resolves exclusively to public addresses. Private destinations always require an exact named profile; no setting enables every RFC1918 or internal destination.

An origin is the normalized scheme, hostname, and effective port, such as `https://catalog.partner.example:443`. Initial support uses exact origins rather than wildcard hostnames. Paths are governed by the source's seed and path-scope rules. Every cross-origin redirect, nested catalog, Registry referral, remote context, or linked artifact must independently satisfy the active policy before it is contacted.

Policy documents contain no credentials. They may reference a mounted CA path. Source records separately select opaque credential bindings resolved through the deployment-mounted tree under ADR-0007; client keys never belong in the policy document.

### Source onboarding

The authorized source-onboarding workflow is:

1. Enter the source URL and select one of the network choices made available to the tenant: Public HTTPS or an assigned private profile.
2. Before the first request, the Catalog checks the caller's `source.inspect` permission and verifies that the normalized origin is allowed by the selected policy.
3. The hardened fetcher resolves and validates the destination, performs bounded inspection, and returns redacted endpoint, redirect, address-class, TLS, media-type, profile, and compatibility evidence. Generic manifests use the current ARD profile; exact discovery locations, Link relations, explicit media types, nested declarations, and retained source evidence distinguish predecessor AI Catalog compatibility, while contradictory explicit signals fail closed.
4. The administrator reviews the discovered interface, exact resource scope, query-disclosure behavior, credentials when needed, and resulting changes before connecting it.
5. The source stores the selected policy name and the policy digest used for its last successful test. Every later fetch uses the currently active policy and records its digest; it never relies on a stale permission copied into a job.

A referral or discovered link may be retained as evidence without being contacted. It becomes a reachable source only after it satisfies the same policy and onboarding controls. There is no self-registration callback and federation requires no new inbound network path.

### Application enforcement

The Catalog enforces all of the following even when SRE also supplies a firewall, proxy, service mesh, or [Kubernetes NetworkPolicy](https://kubernetes.io/docs/concepts/services-networking/network-policies/):

- HTTPS only, except exact development or test fixtures that cannot start in production.
- Normalized URLs with no user information, fragments, ambiguous host forms, control characters, or recognized secret query parameters.
- Exact origin and port checks before network activity.
- A and AAAA resolution before every connection, with every result required to match the selected address policy. Mixed allowed and denied answers fail closed.
- Destination pinning for the connection, with policy reapplied after every redirect to prevent DNS rebinding and time-of-check/time-of-use changes.
- Permanent denial of loopback, link-local, multicast, unspecified, special-use, and cloud or platform metadata destinations. Private profiles cannot override these hard denials.
- Deployment-defined management-plane denials, including Kubernetes or platform control endpoints, independent of any broader private range allowed for an actual Catalog.
- TLS 1.2 or later, hostname verification, bounded private trust material, and client keys used only through references.
- Bounded time, request and response size, redirects, decompression, nesting, pages, concurrency, and live-query fanout.
- No credential forwarding across an origin change. Credentialed requests reject origin-changing redirects unless a future authentication profile explicitly proves a safe handoff without forwarding the original credential.
- Redacted evidence and audit records with no credentials, private query values, or unrestricted internal addresses in routine logs and metrics.

### SRE enforcement

SRE owns actual reachability: cluster DNS, routes, firewall, Kubernetes NetworkPolicy, egress gateway or proxy, service mesh, VPN or private link, CA mounts, mTLS identity, and profile assignment. The service and Helm chart do not install or require any particular networking product.

The Helm chart provides the application policy, trust, and credential mounting hooks but does not create API or worker NetworkPolicy objects. The optional UI has its own default-off, UI-only NetworkPolicy; it does not constrain backend federation egress. A production SRE supplies and verifies the independent API/worker network boundary for the selected networking implementation. Kubernetes NetworkPolicy is a defense-in-depth layer, not a substitute for URL, DNS, redirect, and credential checks in the Catalog.

### Local environments

- Compose includes exact local fixture origins in a development-only policy and reaches them through the Compose network.
- The maintained Minikube topology gate installs test-owned role-selecting NetworkPolicies around the Helm workloads and exercises one allowed in-cluster source plus one otherwise application-authorized decoy denied before it receives a request.
- Local profiles may allow exact HTTP or private fixture destinations but cannot be loaded by a production configuration.
- The same Catalog image and policy model are used in local and production environments; no special federation binary is built.

## Rationale

- Private catalogs are a valid enterprise federation requirement, but unrestricted internal egress is not.
- Admin-only registration reduces exposure but does not replace network safety: an account can be compromised, a URL can be mistyped, and an approved remote host can redirect or change DNS.
- A deployment-owned allowlist makes the reachable network boundary explicit without coupling the service to one proxy, service mesh, cloud, or Kubernetes distribution.
- Exact origins and named private profiles are easier to review and audit than a global `allowPrivateNetworks` switch.
- The public-mode choice lets a tightly controlled deployment require pre-approval while allowing another deployment to give trusted Source Administrators broader public federation without permitting private access.
- Application and network controls cover different failure modes and remain useful independently.

## Consequences

- The structured policy and profile resolver are the supported federation network controls. Unknown configuration fields fail validation.
- API and worker replicas must load the same policy revision and digest. Invalid policy fails startup. A missing profile blocks affected sources without deleting their last complete snapshots. Policy loads at process start, so changes use a reviewed rollout and restrictive changes pause affected sources until every role reports the new digest.
- Removing an origin or profile stops all future inspections, synchronizations, live queries, and nested fetches using it. Existing observations retain their source and retrieval provenance. An egress-policy change does not silently withdraw observations or mark them stale. Automatic maintenance treats the source as ineligible and preserves its last complete membership. Any later administrative freshness transition needs its own explicit reviewed semantics. Webhooks use the same hardened transport boundary but their endpoint policy is not federation-source state.
- A tested source cannot change its resolved endpoint, discovered format profile, or Registry interface during synchronization. The run fails without replacing its last complete membership. An administrator must inspect and connect the changed interface as a new Source identity; PATCH cannot replace the existing Source's interface. Retained observations preserve their original Source provenance. A network-profile or credential-binding change can use same-interface reinspection and a reviewed update to the existing Source.
- Source configuration and outbound operations must be authorized. Administrator source operations retain independently classified audit evidence and applicable outbox events. A strictly validated and authorized inspection, test, or diagnostic records a bounded outcome once it reaches the hardened outbound boundary. Outcomes distinguish success, egress-policy refusal before a socket, transport or HTTP failure, parse failure, and partial results. Failures before that boundary do not create durable audit records. These include authentication, authorization, malformed input, missing source, and stale preconditions. Operational telemetry records bounded redacted outcomes where useful, including approved live-query success, partial results, rejection, and failure. Service-populated evidence and ordinary telemetry exclude secrets, raw queries, query digests, URLs, and remote content. A required user-authored audit reason is separately bounded. It is not suitable for those values.
- Private-profile configuration remains an SRE deployment action. Actors with the required source permissions can select only profiles assigned to their tenant; they cannot mutate the underlying network policy through the Catalog API.
- Outbound polling and bounded live queries are sufficient for federation. The Catalog does not expose a source callback endpoint.

## Verification

- Authorization tests deny inspection and registration without the corresponding tenant permission. Namespace grants and another tenant's profile assignment confer no source authority.
- Policy tests cover disabled, empty allowlist, exact public origin, deliberate any-public mode, exact private origin, profile assignment, unknown profile, profile removal, policy digest change, and inconsistent API or worker policy.
- Safe-fetch tests cover IPv4 and IPv6 loopback, private and special-use ranges, cloud metadata, Kubernetes management destinations, mixed DNS answers, DNS rebinding, redirects, user info, secret query parameters, ports, TLS, private CA, mTLS, timeouts, decompression, and size limits.
- Redirect, nested-catalog, referral, context, artifact, webhook, and live-query tests prove every outbound origin is checked and credentials never cross origins.
- Compose functional tests reach one exact local source and reject unapproved destinations. The maintained Helm-through-Minikube topology tests supply equivalent in-cluster evidence under an enforcing Calico CNI.
- NetworkPolicy-disabled and network-policy-enabled tests prove the application still rejects denied destinations, while the adopting SRE must separately verify enforcement by the selected production CNI.
- Removing a profile blocks future work without corrupting or withdrawing the last complete source snapshot.
- Discovery tests prove mandatory ARD well-known and Link handling, current-versus-predecessor profile classification with generic media, rejection of contradictory explicit signals, and synchronization refusal after unreviewed endpoint, profile, or Registry-interface drift.
