---
title: "ADR-0010: Admin-managed federation execution"
---

- Status: Accepted

## Question

Who decides which external Catalogs participate in discovery, and how do the ARD `none`, `referrals`, and `auto` request modes relate to that decision?

## Context

Two different controls can otherwise be confused:

1. Federation administration decides which Catalogs are connected, what resources they may contribute, whether they are synchronized or queried live, and where their results may appear.
2. The ARD Search request's `federation` member selects `none`, `referrals`, or `auto` for one request.

The ARD request member is not authority to configure a federation. A caller must not supply a source URL or select an unapproved source. A request must not broaden source scope, change its audience, or bypass deployment egress policy. Conversely, an ordinary Catalog user should not have to understand Registry topology before searching.

Synchronization and live search use different execution paths. Synchronization retains external observations ahead of a query; these do not become locally governed resources. Live search sends the query to an approved Registry during the request and returns transient results without adopting, publishing, installing, or executing them.

## Decision

Administrators configure federation centrally. Ordinary users search the connected Catalogs through one interface.

Connecting or changing a source requires current tenant `source.manage` permission. The Source Administrator and Tenant Administrator templates supply it; a custom role can supply it too. For each source, the administrator selects:

- the exact inspected Registry or Catalog endpoint;
- `indexed`, `live`, or `hybrid` retrieval;
- the allowed resource kinds, publishers, tags, identifiers, and versions;
- whether the source may participate in live search or be returned as a referral;
- the discovery hosts and anonymous or authenticated audiences in which it may appear; and
- acknowledgement that live search sends user query text and supported filters to that source.

SRE owns the deployment egress boundary and hard operational limits. SRE does not approve individual searches or maintain the product-level source list.

The authenticated Catalog search API applies the administrator's active source configuration automatically. It does not expose arbitrary source URLs or a per-search federation topology picker. Product interfaces show useful source attribution and ordinary result filters, but federation setup remains an administration workflow.

The standard ARD Search endpoint continues to support all three protocol modes exactly:

| ARD mode | Behavior inside the administrator-approved topology |
| --- | --- |
| `none` | Search only the current host-and-audience publication snapshot. Indexed observations remain attributed private evidence unless explicitly adopted and governed locally. |
| `referrals` | Return the local result set plus only the Registry referrals approved for that discovery host and audience. |
| `auto` | Search the local set and eligible live sources within host, audience and request bounds. Core mode continues an unchanged bounded live window; hybrid mode preserves the exact durable order, followed by live results. |

The pinned [ARD Search contract](https://github.com/ards-project/ard-spec/blob/aa3e598bb7752a9175897823234311216acfa864/spec/ard.md#532-search-post-search) defines omitted `federation` as `auto`, and the service preserves that wire behavior. A protocol client may request a more restrictive mode. No request mode can add a source or exceed its configured scope. If no live source is eligible, `auto` produces the local result set.

A signed token continues an unchanged bounded live window and preserves complete durable traversal. Continuation repeats approved peer queries while live results remain. An exhausted live window leaves ordinary durable continuation, which makes no peer request. Core order keeps displaced local matches reachable; hybrid order returns durable matches first, followed by live results. Exhaustive frozen pagination across changing upstream Registries remains deferred.

Live `auto` federation follows only the administrator-approved topology. A newly observed referral is evidence, not permission to contact another Registry; an administrator must inspect and approve it independently before it can participate in a later request. Exact source lookup uses its ID or normalized endpoint. Live, referral, and topology selection applies host, audience, policy, and `limit + 1` in PostgreSQL before fetch or traversal. It does not load all active tenant sources.

Upstream request modes, referral depth, and fan-out remain bounded by that source configuration and deployment limits. The service never delegates unbounded recursive `auto` behavior or relies on a client-supplied topology.

The service structurally validates every remote response. It reapplies source scope and discovery audience before merging. A governed local exact identifier and typed version remains the authoritative response projection. The required upstream `source` field remains untrusted reported metadata, bound to evidence for the configured responding source. The service does not substitute it, treat it as verified provenance, or contact it.

Query forwarding includes only normalized search text, supported filters, and safe bounded context. It excludes tenant IDs, user identity, roles, groups, local visibility, internal policy, and unrelated request context. Administrator acknowledgement is recorded when live search is enabled; anonymous participation separately acknowledges that the Registry identity and returned source origins may be disclosed publicly. Individual users are not asked to approve every query.

Automatic federation remains bounded by deployment-configured deadlines, fan-out, approved referral depth, response and result limits, per-source concurrency, and cancellation. General HTTP request rates belong at ingress, but process memory remains protected by service work bounds. Add a small process-local semaphore only when measured allocation and concurrency require it. Durable-work admission, replica-coordinated source rates, and stateful circuit breakers require measured traffic or outage evidence that the existing bounds are insufficient. Partial upstream failure does not invalidate a valid local result set. Public ARD response bodies remain conformant and expose no undeclared diagnostic members; permitted administrative APIs and observability carry detailed source outcomes.

## Why this decision

- Administrators, not end users, are accountable for source selection, scope, disclosure, and audience.
- Users receive the product outcome they need: one search across the Catalogs their organization connected.
- ARD clients remain interoperable without turning a request parameter into federation-management authority.
- An explicit approved topology is easier to understand, secure, operate, and test than client-selected or implicit recursive discovery.
- Synchronized and live sources can coexist without implying that a discovered result has been locally governed or installed.

## Consequences

- The source onboarding and change-preview workflows must expose retrieval, scope, live/referral participation, audience, and query-disclosure consequences clearly.
- Internal Catalog search must derive source participation from current administrator configuration rather than caller-supplied source IDs.
- The existing source-administration live-search operation may retain configured-source selection for diagnostics, but it is not the ordinary product search contract.
- Referrals discovered from another Registry remain evidence until an administrator independently inspects and connects them.
- Anonymous discovery can use only sources explicitly approved for that anonymous host with separate query-forwarding and public source-origin disclosure acknowledgements; authenticated discovery additionally enforces the exact host tenant and audience.
- Source attribution, partial-source diagnostics, and operational limits remain necessary even though topology selection is removed from the ordinary user experience.

## Rejected alternatives

### Let every client select sources or URLs

Rejected because it bypasses administrative scope, creates an SSRF and disclosure boundary in the search API, and makes every client understand federation configuration.

### Expose federation setup in the ordinary search UI

Rejected because connected sources are an administrative concern. Search users need provenance and useful filtering, not infrastructure controls.

### Ignore the ARD federation member

Rejected because it would break the pinned interoperability contract. The member controls request behavior within the approved topology; it does not configure that topology.

### Follow an upstream referral without administrative approval

Rejected because query propagation, cost, failure, and disclosure would extend to a Registry this administrator did not approve. A referral can participate only after independent inspection and configuration, whether it is then queried directly or as an explicitly bounded approved hop.

## Verification

- Creating or changing source participation, scope, audience, or query-disclosure settings requires current tenant `source.manage` permission. A template name or namespace grant alone supplies no authority.
- Ordinary Catalog search has no caller-supplied source URL or source-set override and automatically uses the current configured topology.
- ARD contract tests prove omitted `federation` means `auto` and that `none`, `referrals`, and `auto` never exceed the approved sources, scopes, or audience.
- Every contacted Registry and referral hop is present in the active administrator-approved topology; an unconfigured referral is never contacted.
- Remote results are validated and locally re-scoped before merge, and live discovery never causes adoption, publication, installation, or execution.
- A governed local result wins an exact typed-identity collision, reported upstream source metadata never becomes contact authority, and every stable local result remains reachable after a bounded `auto` merge, including live replay in core mode.
- Partial upstream failures preserve valid local results while public response bodies remain conformant and detailed diagnostics remain authorized.
