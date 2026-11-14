# API assets

This directory contains the implemented HTTP contract, webhook event schema and reviewed examples for Agents, MCP Servers, Skills and Plugins. Start with the [OpenAPI document](openapi/control-v1.yaml) for operations and the [API conventions](../documentation/src/content/docs/reference/api-conventions.md) for authentication, request rules, trace correlation and browser preflight.

Personal-library listing accepts an optional literal `label` filter alongside `type`. Matching preserves case, significant spaces and Unicode spelling; filtering precedes page limits and is bound into owner/current-authority cursors. Omit the parameter for all labels; a supplied empty or invalid label is rejected. The [personal-workspace guide](../documentation/src/content/docs/guides/catalog-ui.mdx#save-resources-and-searches) explains the UI, supported criteria and independent target admission.

For example, authenticated `GET /api/v1/personal/items?type=search&label=%20Support%20&limit=25` finds the current human's named searches labelled exactly ` Support `, including the two spaces. Follow its returned cursor with the same filters; labels need not be unique. Changing or omitting either filter starts a new page sequence.

Optional ANS registration assessments use explicit source-administrator commands and durable Jobs. Private result reads require current evidence permission and an audit write before disclosure. Generic Jobs omit assessment evidence. Follow the [assessment procedure](../documentation/src/content/docs/operations/federation.mdx#request-an-ans-registration-assessment); ordinary discovery never starts these checks.

## Authoritative contracts

Historical source comparison uses `GET /api/v1/federation/snapshots/{snapshotId}/comparison` with an exact `otherSnapshotId`. Both memberships must be complete and share one source configuration revision. The response pages occurrence-bound compact changes without raw documents, source requests or mutations. Current `source.read` permission and the applicable `sources.read` scope are required. Follow [Compare two saved synchronizations](../documentation/src/content/docs/operations/federation.mdx#compare-two-saved-synchronizations) for result meanings, duplicate keys, input bounds and continuation.

| Asset | Purpose |
| --- | --- |
| [Control API OpenAPI](openapi/control-v1.yaml) | [OpenAPI 3.1](https://spec.openapis.org/oas/v3.1.0.html) contract for every implemented API-listener HTTP route, operation, parameter, request body, response, authentication rule, and public endpoint. |
| [Catalog event schema](schemas/catalog-event-v1.schema.json) | [CloudEvents](https://cloudevents.io/) notification-envelope contract for optional signed webhook delivery. |
| [Embedded API package](embed.go) | Exact OpenAPI, event-schema, and example bytes served or consumed by the Go runtime and tests. |

The running service returns the exact embedded OpenAPI document at `GET /openapi/control-v1.yaml` with `ETag`, `Content-Digest` and a five-minute public cache policy. Send the exact returned ETag in `If-None-Match` to receive `304` with no body when unchanged. Import either the checked-in file or that endpoint into an OpenAPI 3.1-compatible client, documentation viewer, or request generator.

### Reusable draft file contract

The OpenAPI component `CatalogDraftFile` defines `open-agentic-catalog-draft-v1`. It contains one Resource's kind, canonical identifier, Version label, representations and optional Listing metadata. It excludes destination identities and command recovery state. Native JSON, Markdown and extension editor JSON remain text, including unfinished input.

The [Go codec](../internal/draftfile/draftfile.go) and [browser guard](../catalog-ui/src/views/draft-file.ts) enforce the file controls and byte limits. Both use the [shared fixtures](testdata/catalog-draft/README.md). The encoded ceiling is 64 MiB. Native document and Plugin preparation budgets remain separate, at 16 MiB each. Raw browser editors retain their 2 MiB limits. Deployment request and response limits still apply.

Skill packages require separately supplied content and the existing immutable handoff. Plugin archive references retain an optional checksum; stored exports must supply the retained checksum. Plugin preparation is a separate input mode and contains no archive bytes. Listing artwork contains authoring declarations, with no sealed digests or rights assessment.

`GET /api/v1/version-revisions/{revisionId}/export` returns the complete file as a private JSON attachment. Optional `listingRevisionId` selects exact matching Listing metadata. Current metadata/content permissions and any enforced `catalog.read` scope apply. Required VersionRevision and document read audits precede disclosure. Exact reads need no history permission. Missing retained content causes an error; the exporter does not refetch source URLs.

The CLI supports single and batch downloads with `--format draft`. Native export remains the default. Import uses `import preview`, `apply` and `resume` with explicit destination identities. The UI can download a reusable file and load it into the authoring wizard. Follow the [transfer tutorial](../documentation/src/content/docs/guides/draft-transfer.mdx) or the [command reference](../documentation/src/content/docs/reference/commands.md#export-a-reusable-draft-json-file).

The inline capabilities example shows the current default-off configuration with a 2 MiB request limit and the pinned standards bundle. Read `GET /api/v1/capabilities` on the target deployment for its actual limits and enabled options; example values are not deployment-independent defaults.

Reusable parameter components define their name, location and schema directly for editor and generator compatibility. Schema Object samples use OpenAPI 3.1 `examples` arrays; Media Type Object samples may use singular `example`. Commands that require a JSON body use POST or PATCH, including role revocation, draft deletion and source removal; see the [command conventions](../documentation/src/content/docs/reference/api-conventions.md#practical-administration).

The API listener supports only paths in OpenAPI. The separate worker-only listener has its own health, status, version, and metrics contract in the [operations package](../internal/platform/operations) and [worker-health runbook](../documentation/src/content/docs/operations/observability.mdx); it serves no Control or discovery route.

The default-off `GET /api/v1/service-activity` route requires deployment enablement, current tenant-wide `access.manage` and enforced `tenant.admin`. It returns bounded cross-tenant process counters, with no query selectors or body. See [request statistics](../documentation/src/content/docs/operations/observability.mdx#read-api-request-statistics).

Webhook and HTTP MCP routes have separate capability gates. Webhook routes are registered only when `GET /api/v1/capabilities` reports `optionalCapabilities.webhooks` as `enabled`. `/mcp` and its OAuth metadata route require `mcp.enabled`; the metadata route also requires OIDC authentication. Disabled routes return 404. A bidirectional route audit checks documented operations against literal router registrations. It also checks the explicit dispatchers for colon-suffixed commands.

Service-owned success bodies use operation-specific closed schemas and named API-safe projections. Request constraints agree with runtime validation. Administration collections use fixed ordering and repository-bounded cursor pages. Raw standards documents, representation data, JSON-LD contexts, live upstream entries, and reviewed extension or evidence maps remain deliberately extensible at their named boundaries.

The checked-in event schema defines the complete implemented CloudEvents envelope, tenant-bound `source`, immutable `dataschema`, current public type registry, and one small closed notification-reference payload. The Go registry, OpenAPI selectors, JSON Schema, and reviewed Catalog, source, and host-bound publication webhook examples are checked for exact agreement. Internal or unknown outbox types are never webhook-deliverable, including to wildcard subscriptions. Idempotency repair is audit-only because no post-commit consumer exists; an internal event type requires a concrete consumer. See [Webhook operations](../documentation/src/content/docs/operations/webhooks.mdx) for enablement, delivery limits, key maintenance and recovery.

`WebhookEventSelector` lists accepted versioned public types and their exact lowercase short aliases. Create/update accepts 1-64 input selectors before alias normalization, deduplication and sorting; endpoint responses contain canonical types. A wildcard (`*`) subscribes to all supported public notifications, including future public types. Specific selectors alongside it do not narrow coverage or admit private/internal events. Tenant settings changes remain audit-only. The UI's exhaustive labels are checked against the generated OpenAPI types and service-owned event schema; no new event is created by adding a checkbox.

Webhook inventory accepts intersecting exact `name` and `status` query filters, for example `GET /api/v1/webhooks?name=Compliance&status=active&limit=1`. Delivery evidence accepts exact `deliveryId`, `eventId` and `state` filters within one endpoint, for example `GET /api/v1/webhooks/{webhookId}/deliveries?eventId={eventId}&state=dead_letter&limit=1`. Filters precede limits and participate in tenant/current-authorization-bound continuations. Each row is the latest retained endpoint/event outcome, not every send. A test-slot admission refusal has HTTP 503 and `WEBHOOK_TEST_BUSY`, meaning no send; a receiver 503 is a delivery result in an API HTTP 200. Other server/transport failures remain potentially uncertain.

## Reviewed examples

Snapshot inventory supports an optional exact state filter, for example `GET /api/v1/publication/snapshots?host=catalog.example.com&state=active&limit=1`. Omit `state` for all retained states; supplied values must be exactly `candidate`, `active` or `retired`. Empty, repeated and incorrectly cased values are rejected. Filtering precedes selection limits, and continuations bind the selected state as well as host, tenant, current authorization, ordering and page size. A summary is an observation; its ETag identifies retained content rather than continued activity. Full inspection uses the returned exact snapshot ID and the same host.

| Example | Demonstrates |
| --- | --- |
| [Agent lifecycle](examples/agent-lifecycle.json) | Namespace, publisher, Agent version, renewable validation, listing, independent review, governed publication, and public snapshot activation. |
| [HTTPS representation](examples/https-representation.json) | Credential-free URL input, exact-origin prerequisite, retained validation-byte binding, renewable 24-hour evidence, publication-time verification, and external delivery semantics. |
| [Hybrid search](examples/hybrid-search.json) | Automatic retrieval modes, explicit lexical fallback, Control origin/Listing-namespace and same-token continuation examples and the separate administrator reindex body. |
| [Federation source](examples/federation-source.json) | Administrator inspection with a named network profile and opaque credential binding, indexed/live/referral topology, exact scope and discovery audience, reviewed update behavior, and bounded nesting. ARD request modes cannot expand this configuration. |
| [Audit export request](examples/audit-export-request.json) | Exact optional actor, action, request, and paired-target filters plus the mandatory half-open range and retained operator reason for one bounded synchronous export. |
| [ARD manifest](examples/ard-manifest.json) | A valid public ARD document independently validated and checked by the unchanged pinned CLI alongside the four official examples. |
| [Webhook delivery](examples/webhook-delivery.json) | The common closed CloudEvents reference envelope delivered to a webhook receiver. |
| [Source webhook delivery](examples/source-webhook-delivery.json) | A self-describing source-removal notification whose target remains usable after the current source read returns not found. |
| [Publication webhook delivery](examples/publication-webhook-delivery.json) | A host-bound publication activation notification that identifies the exact immutable snapshot and discovery host. |

The lifecycle, HTTPS representation, and federation files are annotated templates, not complete HTTP bodies or executable scripts. In the lifecycle template, send only each step's `body` as JSON with `Content-Type: application/json`. Use `actor` to select the development Bearer token. Send `idempotencyKey` in the `Idempotency-Key` header. Replace the `ifMatch` instruction with the returned ETag and send it in `If-Match`. `save` describes response IDs to retain, not a request member.

The HTTPS template's `createVersion` object is the body for `POST /api/v1/resources/{resourceId}/versions`. The federation template's `inspect` and `connect` objects are bodies for separate `POST /api/v1/federation/source-inspections` and `POST /api/v1/federation/sources` commands. Follow each template's prerequisites; the local profile does not configure the example partner network or credentials.

The audit-export file is a direct body for `POST /api/v1/audit-exports`. Replace its illustrative filters and time interval with the investigation range. The upper bound must not exceed the server cutoff. The ARD manifest and webhook files demonstrate output documents, not Control API requests. Webhook files show payloads only: real delivery includes sender-generated signature headers described in the webhook runbook, and a receiver must verify the actual delivered bytes.

IDs, dates, digests, domains, and endpoint URLs in these examples are illustrative; external URLs are not hosted demo services. A real client must retain IDs, `ETag` values, cursors, and Job locations returned by preceding operations rather than constructing them. The lifecycle example builds a snapshot synchronously and uses its returned candidate ID and ETag for activation.

For an executable walkthrough, start a disposable local service with `make run` and run `make smoke` in another terminal. The smoke client follows returned IDs and ETags through its Agent, MCP Server and Skill fixtures and validates both governed and public discovery; it does not execute these JSON templates. Smoke creates records and activates a discovery snapshot, so use an explicitly selected disposable service, not a shared deployment.

[Catalog Agent Plugins](../documentation/src/content/docs/guides/agent-plugins.mdx) provides separate resource/version request examples, checksum rules, response interpretation and correction/revalidation steps. Plugin creation returns retained inspection rather than hosted archive bytes. Check `evidence.state` even after HTTP 201; a successfully retained revision may have blocking format findings. Package authoring requires an anonymously accessible public HTTPS archive, not a private fixture URL or an inline JSON manifest.

## Overview reads

Use separate GET requests for each permitted summary. For example:

```http
GET /api/v1/overview/resources HTTP/1.1
Authorization: Bearer <access-token>
```

A complete Resource response can contain:

```json
{
  "tenantId": "00000000-0000-7000-8000-000000000001",
  "scope": "current_access",
  "observedAt": "2026-10-25T12:00:00Z",
  "selectionLimit": 10000,
  "total": {"value": 10, "limited": false},
  "kinds": [
    {"kind": "agent", "count": 4},
    {"kind": "mcp_server", "count": 3},
    {"kind": "skill", "count": 2},
    {"kind": "plugin", "count": 1}
  ]
}
```

This example uses synthetic counts. Resource reads require current `resource.read` access and enforced `catalog.read`. Namespace grants apply before selection, including grants beyond the first `/me` page.

When `total.limited` is true, `total.value` is 10,000 and `kinds` is absent. Indexed summaries use a different limit result: `limited: true` omits `count`. Their selection ceiling applies to source contributions before identity deduplication. A limited indexed result supplies no identity count.

Each response includes `Cache-Control: no-store`. Separate requests have separate observation times. Refresh the required summary with another GET. These reads create no Job, fetch no resource archive and make no live federation query.

Private Overview reads include `/api/v1/overview/reviews` and `/api/v1/overview/publication`. Review counts use current `resource.read_history` visibility. Publication metadata requires tenant `publication.read`. Both require `catalog.read` when scopes are enforced. Responses use `no-store`. See the [Overview guide](../documentation/src/content/docs/guides/catalog-ui.mdx#read-the-content-overview) for intervals, independent limits, median calculations and configuration differences.

Operations adds `/api/v1/overview/sources` under `source.read` and enforced `sources.read`, plus `/api/v1/overview/jobs` under `job.read` and enforced `catalog.read`. Job family and deployment-target checks precede counts. Both reads return current scope, server observation time and independent count limits. Source problems use the latest completed current-revision attempt. Job due time uses scheduling or lease expiry. The UI reuses `/api/v1/capabilities` for existing index details; no new indexing contract or provider probe is added.

Enabled webhooks add `/api/v1/overview/webhook-endpoints` under `webhook.read` and `/api/v1/overview/webhook-deliveries` under `webhook.read_evidence`. Preserve its `webhook.read` companion and enforced `webhooks.manage`. Each distribution has its own 10,001-row selection limit. Delivery counts exclude synthetic tests. Up to five active endpoints have a failed latest retained production attempt since their current update. No event body, URL, signing material or receiver diagnostic is returned. Notification counters reuse `/api/v1/webhooks/reconciliation` without changing its contract.

Administration adds `/api/v1/overview/administration`, `/api/v1/overview/feedback` and `/api/v1/overview/ratings`. They require `access.manage`, `feedback.moderate` and `ratings.moderate`, respectively, with enforced `tenant.admin`. They require no Resource-read permission or `catalog.read` scope. Counts have independent 10,000-row ceilings. Incomplete distributions are omitted. Feedback's oldest original submission time remains independently exact.

Access summaries count stored memberships, active definitions and retained active assignments, not effective authority or identity-provider accounts. Feedback excludes deleted Resources; rating moderation retains records with unavailable targets. Summaries contain no subjects, feedback text, rater identities or target metadata. See the [Administration overview](../documentation/src/content/docs/guides/catalog-ui.mdx#read-the-administration-overview) for filter links and empty or limited results.

## Request rules

Review queues accept `unreleased=true` only with `state=approved`. For example, `GET /api/v1/review-submissions?state=approved&unreleased=true&limit=25` excludes submissions referenced by any retained release. Retired and revoked releases still count as previous releases. False or omission adds no release filter. Empty, repeated or nonboolean values are rejected. The release filter participates in the signed cursor binding.

Namespace and Publisher inventories have optional exact filters. For example, `GET /api/v1/namespaces?slug=tools&displayName=Tools&limit=25` intersects the stable slug and exact stored name. `GET /api/v1/publishers?declaredDomain=example.com&limit=25` matches declared domains, independently of approval. Slugs/domains normalize as on creation; display names preserve case and spacing and need not be unique. Omit unused criteria. Selection and current visibility precede the page limit, and continuations bind the filters and current authority. See [directory lookup](../documentation/src/content/docs/guides/resource-authoring.mdx#manage-namespaces-and-publishers) for inputs, scopes and recovery.

- Optional `/mcp` uses stateless, finite MCP JSON-RPC and per-request Bearer authentication. It shares existing Control read authority and emits HTTP challenges for token/scope refusal. OIDC exposes public `/.well-known/oauth-protected-resource/mcp` metadata. Both paths are unavailable when disabled. See [MCP HTTP](../documentation/src/content/docs/guides/mcp-server.mdx#connect-over-http).
- `/api/v1` requires a Bearer identity. API health, version, metrics, and OpenAPI routes are unauthenticated and must be limited by deployment routing where appropriate. ARD, AI Catalog, Registry, and publication-authorized document routes are host-dependent: each exact configured host is anonymous or Bearer-authenticated, while unknown, disabled, and inactive hosts return no discovery data. OpenAPI represents this conditional discovery rule with anonymous-or-Bearer alternatives.
- Authenticated `POST /api/v1/catalog/search` returns the closed unified local, indexed, or live result contract. The service derives tenant source topology, ranks external projections locally under `core-lexical-v1`, continues durable results and one unchanged bounded live window, and never accepts a client-selected source, provider, model, or tier.
- Tenant Administrators can page and read memberships, suspend or reactivate access, terminally offboard a member and its active grants, and optimistically update namespace or Publisher display metadata. Identity-realm suspension remains a separate schema-owner `catalogctl` operation.
- Security Reviewers can investigate tenant audit events with exact actor, action, request-ID, paired target, and half-open time filters. Version-1 response actions are the exact closed service registry represented in OpenAPI; the input filter accepts a bounded action string, and an unknown action matches no current records. `POST /api/v1/audit-exports` additionally requires an idempotency key, a mandatory half-open time range, a bounded non-secret reason, and the optional `audit.export` OAuth scope ceiling when scope enforcement is enabled. The response contains at most 1,000 rows and 8 MiB, ordered oldest first. Its manifest protects the exact deterministic `events` array. The service does not retain a separate export object.
- `POST /api/v1/resources/{resourceId}:delete` soft-deletes only an eligible unpublished local draft after dependency and namespace-authority checks. It retains the canonical identifier and history while every current aggregate and document read fails closed.
- Use a new `Idempotency-Key` for each new control command, and reuse the same key and identical request when retrying that command within its replay window. Read-only Search and Explore do not require a key. Operations that change revisioned state also require its current `ETag` in `If-Match`, as specified by their OpenAPI parameters; an exact retry retains its original precondition. See the API conventions for the 24-hour replay window and expired-key behavior.
- Revalidating an immutable VersionRevision appends a current EvidenceSet and changes only its projected `evidenceSetId`; it does not use `If-Match` or rewrite content, prior evidence, or approval.
- Every representation `mediaType` must be a syntactically valid explicit type and subtype no longer than 255 characters, without outer whitespace or control characters; a bare label is rejected. The exact retained declaration selects validation behavior rather than being inferred or silently relabeled from content.
- A representation `url` is credential-free HTTPS and must be permitted by the deployment-owned anonymous public route. Creation and revalidation retain exact bounded validation bytes and retrieval evidence; publication fetches the same external URL again, and the Catalog does not rehost those bytes merely because it retains their document binding.
- Control requests use one field line each for `Authorization`, `Content-Type`, `Idempotency-Key` and `If-Match`. Repeated lines, including case variants and identical values, fail with `400 INVALID_ARGUMENT` before authentication or command reservation. Other HTTP fields retain their own repetition semantics.
- Request bodies require a JSON object and `Content-Type: application/json`; charset parameters are accepted. Omit the body only where OpenAPI makes it optional. Null, arrays, missing or incompatible media types, duplicate keys, unknown control fields, trailing JSON, and over-limit bodies fail. Named standards payloads and extension maps remain extensible.
- Control API errors use [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457.html) `application/problem+json` with a stable service `code`. ARD Registry errors use the pinned protocol's `errorCode` and `message` envelope. Both include `X-Request-ID`. Enabled `/mcp` uses MCP JSON-RPC responses, safe tool refusals and HTTP OAuth challenges. It does not use the Control response envelope.
- Discovery is not approval, installation, deployment, invocation, or runtime authorization.

See [API conventions](../documentation/src/content/docs/reference/api-conventions.md) for roles, OAuth scopes, errors, pagination, caching, and compatibility policy. See [Standards and interoperability](../documentation/src/content/docs/reference/standards.mdx) for the distinction between discovery, resource representations, packages, and execution. See the [local walkthrough](../documentation/src/content/docs/guides/local-development.mdx) for the complete runnable sequence.

## Validate a change

`GET /api/v1/publication/hosts` requires tenant-level `publication.read` and, when scopes are enforced, `catalog.read`. It returns the current tenant's exact configured discovery hostnames and modes. The complete sorted inventory has a 64-host deployment ceiling. It includes disabled choices and returns an empty array when the tenant has no configured hosts. It exposes no tenant IDs, publisher domains, credentials or internal endpoints. Inventory reads do not build or activate snapshots; existing exact-host publication commands recheck authority and reject disabled or removed bindings.

The [Go-client compatibility harness](../test/clientcompat/README.md) generates and compiles the complete contract with pinned oapi-codegen, then tests representative authenticated in-memory reads, commands and lossless raw evidence. Run `make client-go-tools-install` once and `make client-go-check` after contract or tooling changes. `make client-go-generate` produces an ignored inspection copy. Typed arbitrary-JSON maps can lose numeric precision; use the documented raw response/`WithBody` path and a bounded transport. The existing CLI and UI clients remain independent.

```sh
make validation-tools-install
make api-check
make api-contract-check
make check
make verify
```

Install the API tooling once, and again after its lockfiles change, using Node.js 22.13+ within major 22, npm 10+ and the repository Go toolchain. `api-check` combines existing Go specification tests, yamllint and configured Redocly lint without rewriting the contract. `api-contract-check` runs the HTTP/event component tests. Tool updates also run `make api-validation-test`. Use `make api-diff API_BASE=/absolute/path/to/baseline.yaml` for a semantic report or `make api-breaking-check` with the same explicit baseline to fail breaking changes. See [local API validation](../test/validation/README.md) for installation, rule scope, input restrictions and known OpenAPI 3.1 tool limitations.

Contract tests reject duplicate JSON members in every embedded example and schema. They parse OpenAPI, require unique operation IDs, and check route parity and command headers. They validate lifecycle, HTTPS representation, federation and audit-export request examples against their schemas. They also compare served bytes and conditional download responses with the embedded document. Event-package tests validate all three webhook examples against the compiled event contract; `make verify` also compares the capabilities example with the composed default-off runtime and validates the ARD example through the pinned conformance fixtures. Schema validation does not execute a workflow or prove that an illustrative external endpoint is reachable. An API change is incomplete until its OpenAPI operation, example or guide where relevant, implementation, authorization rule, and positive and negative contract tests agree.
