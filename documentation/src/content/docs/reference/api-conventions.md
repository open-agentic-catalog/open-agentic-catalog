---
title: "API conventions"
---

The embedded [OpenAPI document](../../../../../api/openapi/control-v1.yaml) is the authoritative contract for implemented HTTP paths, methods, request bodies, and response schemas. The service publishes the exact embedded bytes at `GET /openapi/control-v1.yaml` with `ETag` and `Content-Digest` headers.

Use the [API reference](../../../../../api/README.md) and OpenAPI contract for supported operations; [design principles](../architecture/design.mdx) explain the architectural boundaries without defining additional endpoints.

## Surfaces

- API operational endpoints are `/livez`, `/readyz`, `/version`, `/metrics`, and `/openapi/control-v1.yaml`. They share the API listener but require no bearer token, so deployment routing keeps metrics and the contract document off unintended ingress.
- The separate worker operations listener serves only `/startupz`, `/livez`, `/readyz`, `/status`, `/version`, and `/metrics`. It is deliberately absent from Control OpenAPI because it is a role-specific platform contract, not a product API; the Helm Service and Ingress do not expose it.
- Authenticated control operations live beneath `/api/v1/`.
- Optional `/mcp` exposes authenticated read-only Control tools over stateless HTTP. Its public OAuth resource metadata is available only when the capability is enabled in OIDC mode. See [the MCP guide](../guides/mcp-server.mdx#connect-over-http).
- Exact-host discovery uses `/.well-known/ard.json`, `/.well-known/ai-catalog.json`, `/ard/v1/search`, `/ard/v1/explore`, `/ard/v1/agents`, and immutable publication-authorized bounded-document URLs. Each configured host is disabled, anonymous, or authenticated. The v1 paths and parameters retain the words `public` and `artifact` for compatibility even when an authenticated host serves them.
- Only paths in OpenAPI are supported integration contracts. The router intentionally exposes no undocumented compatibility aliases, and a bidirectional route audit guards that boundary.

The list above describes the current executable. Each exact canonical hostname or IP address maps to one tenant, mode, publisher domain, and immutable publication manager; separate service-wide settings provide the ARD and AI Catalog output profiles applied to all enabled managers. Unknown, disabled, inactive, malformed, forwarded, or conflicting hosts return `404`. An anonymous host ignores an attached token and serves only a public-audience snapshot. An authenticated host uses the same paths, requires a signed JWT with the configured Catalog audience and a host-to-resolved-tenant match, and serves a separate tenant/public-audience snapshot with private caching and `Vary: Authorization`. Namespace-private releases remain on the control API.

Each v1 candidate includes every currently eligible release for that host audience.

Trusted-proxy support, configurable cache bounds, and CDN integration are optional deployment refinements added only when needed; a database-managed publication-configuration control plane is not required.

The control API has four governed resource kinds: `agent`, `mcp_server`, `skill` and `plugin`. Unsupported external types remain unknown observations rather than governed resources. [Plugin authoring](../guides/agent-plugins.mdx) accepts complete HTTPS archive URLs with an optional expected SHA-256 checksum; exact inspection documents and archive identity are retained, not binary archive bytes. See [Standards and interoperability](standards.mdx).

`GET /api/v1/me` returns the active principal, current authorization revision, finite tenant `permissions`, finite `availablePermissions` union and one bounded page of `namespaceGrants` with effective permissions. `availablePermissions` is a navigation hint that an action is available somewhere, never authority for an arbitrary namespace. Namespace hints combine tenant and exact-namespace paths. It needs active authentication but no product permission or coarse scope.

`limit` is 1-100 (default 20); `cursor` binds identity, policy revision, scope ceiling, filters and page size. `namespaceId` selects only the caller's exact scope hint, without disclosing another namespace. Policy changes require restarting continuation.

`GET /api/v1/access/self` separately pages this caller's effective direct/group assignment sources, with role and group provenance. Neither endpoint returns raw claims, legacy role-name arrays, tokens or another member's access. Clients must check current API decisions after using hints.

The service has no supported released predecessor. Pre-release contract corrections update OpenAPI, implementation, migrations, examples and tests together without aliases for older development clients. After release, `/api/v1` compatibility rules preserve field meaning, defaults, authorization and error behavior.

## Reusable draft downloads

`GET /api/v1/version-revisions/{revisionId}/export` returns one reusable JSON file with all representations. Optional `listingRevisionId` includes exact matching Listing metadata. The server prepares the complete bounded file before it sends attachment headers. Success uses `Cache-Control: private, no-store`, a fixed filename and a `Content-Digest` for the file bytes. Failures use the normal problem-details response.

Current `resource.read` and `resource.read_content` permissions apply in the Resource's Namespace. Enforced OAuth scopes require `catalog.read`. Exact reads do not require history permission. Required VersionRevision and document read audits must succeed before disclosure. Listing reads retain their current permission-only contract.

The file contains editable input, without source identities or governance evidence. Package references remain separate from native document text. See [reusable draft downloads](commands.md#export-a-reusable-draft-json-file) for CLI examples, byte limits and package requirements.

## Process information

`GET /api/v1/service-information` returns the responding API process's build, startup time, observation time, elapsed uptime and instance ID. It requires authentication but no additional role or OAuth scope. Its handler does not query a database or provider. Responses use `Cache-Control: no-store`.

Read all fields from one response. A later request can reach another replica. Startup time is not deployment time, and uptime uses a monotonic clock rather than the difference between UTC timestamps. The public `/version` contract is unchanged. See [current-process information](../operations/observability.mdx#current-process-startup-information) for the separate UI and worker surfaces.

## Federation authority

Federation configuration is an administrative contract. A Source Administrator or Tenant Administrator selects public routing or one tenant-assigned private profile before inspection. The administrator also selects any optional opaque credential binding before inspection. The administrator then connects the source using that exact inspection before it expires. The connection must keep the inspected routing and credential binding unchanged.

The administrator controls indexed, live, or hybrid retrieval; identifier, version, kind, publisher, and tag scope; live and referral participation; exact discovery host and audience; approved source-to-source edges; query-disclosure acknowledgement; and separate anonymous approval to disclose the Registry identity and returned source origins publicly. Changing the network profile or credential binding requires a fresh matching inspection plus the normal change preview and optimistic update. Callers cannot submit an arbitrary source to a search, broaden its scope, or turn an observed referral into contact authority.

The standard host-bound ARD Search `federation` member remains available for interoperability and defaults to `auto` when omitted. `none`, `referrals`, and `auto` select behavior only inside active administrator topology for the resolved host and audience. Remote results are validated and re-scoped, referral output is bounded, and a referral hop additionally requires an approved source edge plus a structurally valid exact endpoint advertised by the current response. The service tracks visited canonical endpoints and advertised Registry identifiers to prevent duplicate contact. The authenticated source live-search operation may select among already configured source IDs for administrative diagnostics; it is not the ordinary product search contract. Authenticated Control API Catalog Search accepts no source selector and derives eligible indexed and direct-live participation from the active administrator-owned topology for the actor's tenant.

Omitted or explicit `auto` ARD Search may merge bounded live peer pages with the local immutable snapshot. The [peer page limit](configuration.mdx#live-peer-pages) defaults to one. In core mode, continuation replays an unchanged window of up to 50 live candidates. Local offsets advance only by emitted local results, so live displacement cannot hide a local match.

Hybrid mode preserves the exact durable order. Live results follow those matches. A governed local exact identifier and typed version wins a remote collision anywhere in the complete matched snapshot. Required safe HTTPS `source` metadata is preserved separately from hidden evidence of the configured responding Registry. ARD permits those origins to differ; neither is contacted or promoted into verified provenance, referral, or trust authority.

Control Catalog Search and the authenticated administrator live-search diagnostic expose `continuation_unavailable` when a peer has a remaining token after bounded paging. They retain useful accepted results, set `partial: true` and explain that the remaining continuation was not followed. A consumed token alone does not produce that warning. This is a completion limitation, not a failed search or malformed response. Catalog/UI clients preserve the available results and partial warning. Core live continuation repeats the approved bounded peer traversal while live results remain. Ordinary durable continuation makes no peer calls. Replaying an administrator diagnostic command returns its original result without another upstream call. The public ARD envelope is unchanged.

## Search tiers

Search uses the core lexical profile by default. Optional hybrid search adds semantic matches and deterministic rank fusion using one deployment-configured embedding model. Requests do not accept a provider, model, vector index, ranking weight or core-only switch. Exact identifiers, empty queries, List and browse remain model-free. Capabilities report configured availability and last observed provider state without making inference calls; response `tier`, `profile`, `fallbackReason` and `partialIndex` describe the actual retrieval. Tenant administrators additionally receive bounded indexing state and counts. See [hybrid search](../operations/hybrid-search.mdx) for limits, exclusions and fallback.

`POST /api/v1/catalog/search` returns a closed source-tagged union: `local` contains a governed Catalog record, `indexed` a selected observation with source/conflict evidence, and `live` transient upstream entry evidence. Exact identifiers rank first. Core lexical matching lowercases and tokenizes metadata and requires every query term. Enabled hybrid matching includes lexical hits or eligible semantic hits and preserves exact precedence. It deduplicates identities before reciprocal rank fusion with a constant of 60 (RRF60).

Local and indexed matches share complete totals; Explore uses the same matching rule over local releases only. `durableTotal` excludes live results and `liveCount` counts only live items present in the response. Control live results use the core scorer, never a peer's score as local ranking authority. Core continuation binds the unchanged live window and durable generations. Hybrid continuation preserves the exact durable ranking. Live results follow those matches.

The shared `publisher` filter is source-aware because local governance and external AIR identity have different authorities: it compares the Catalog Publisher UUID for a local release and the normalized AIR publisher component for indexed or live entries. The type-tagged result exposes the corresponding value, and the nested local record retains its full Publisher binding. Clients that need deterministic local-only traversal can use Catalog browse and its local filters.

The OpenAPI document includes the implemented webhook operation family because enabled deployments serve it without a version change. Clients must first inspect `GET /api/v1/capabilities`: webhook routes are registered only when `optionalCapabilities.webhooks` is `enabled`, and a disabled deployment exposes no webhook API.

When webhooks are enabled, `GET /api/v1/webhooks/reconciliation` requires current tenant-wide `webhook.read_evidence` permission. It also requires `webhooks.manage` when OAuth scopes are enforced. The response contains bounded pending-event counts and cumulative capacity-loss and terminal-recipient-loss checkpoints. This read does not acknowledge or reset losses and offers no historical replay. Counts may decrease after a database restore; consumers refresh current authorized state after checkpoint changes or disabled intervals.

Synchronous `POST /api/v1/webhooks/{webhookId}:test` has one process-local slot and returns `503 WEBHOOK_TEST_BUSY` without sending when another eligible test occupies it; authentication, tenant lookup and revision checks still precede admission. See the [webhook runbook](../operations/webhooks.mdx) for receiver behavior and the authoritative OpenAPI for response schemas.

## Bounded validation diagnostics

New validation reports follow the [configured diagnostic budgets](configuration.mdx#validation-diagnostic-limits). The `limits.validationMaxFindings` and `limits.validationMaxDiagnosticBytes` capabilities describe limits on new reports. They do not limit reads of retained reports. Exhaustion retains one `VALIDATION_DIAGNOSTICS_EXHAUSTED` error and blocks review/publication. Raw representation evidence remains intact.

Revalidation creates new evidence; it does not overwrite the original report. Exact retained detail and idempotent replay preserve their stored findings and digests, including reports larger than a subsequently lowered admission budget. PostgreSQL rejects an individual pathological historical findings array above 32 MiB before transferring it to the application, with `422 WORK_LIMIT_EXCEEDED`. Existing full-response client ceilings still apply.

## Authentication and authorization

Every `/api/v1/` request requires `Authorization: Bearer <token>`. The development profile maps fixed local tokens from `config/local.json`; production uses a configured OIDC issuer and audience.

OIDC mode accepts only a compact signed JWT access token with the configured issuer and Catalog audience, a bounded supported asymmetric signing algorithm, non-empty `sub`, required `iat` and `exp`, and valid optional `nbf`. Dedicated deployments bind every verified principal to the configured internal tenant UUID. Shared deployments read one configured string organization claim and resolve its exact issuer-and-organization mapping. The subject must already identify an active Catalog membership in that realm. Token-carried tenant UUIDs, roles, groups, and namespace assignments do not grant Catalog authority; the authenticated membership is resolved from Catalog state and domain actions check current retained permission assignments. Authentication uses compact `ResolvePrincipal`; bounded self-access projections query current retained policy separately.

A classified failure to resolve current Catalog policy, such as PostgreSQL unavailability, returns `503 DEPENDENCY_UNAVAILABLE` on both control and authenticated discovery reads, without an `invalid_token` challenge. Invalid tokens and inactive or missing memberships retain generic `401` rejection. Clients should preserve an unexpired session during a Catalog outage; a later request rechecks current policy after recovery. Dependency causes and private membership details are never public diagnostics.

OAuth scopes are an optional ceiling controlled by `auth.enforceScopes`. When enabled, the verified token's whitespace-separated `scope` claim must contain the route scope below in addition to the required Catalog permission. When disabled, missing scopes do not block a request, but a token scope never creates a Catalog permission in either mode. Verification-key metadata and responses are bounded and duplicate-key safe. A known cached key may bridge an identity-provider outage for at most 15 minutes; after that interval the provider metadata and signing key must be refreshed successfully before a token is accepted. HTTPS issuers cannot downgrade discovery, redirects, or key retrieval to HTTP.

Built-in roles are exact permission templates, not a role-name hierarchy. Custom roles and additive direct/group assignments use the same finite actions. Tenant assignments cover their namespace-capable actions; exact-namespace assignments cover only that namespace. Domain lifecycle, creator, human-only and no-self-review checks still apply.

| Built-in template | Primary authority |
| --- | --- |
| `reader` | Resource metadata, history, content and namespace reads; eligible human ratings. |
| `contributor` | Those reads plus resource creation/editing, own eligible draft deletion, validation and review submission. |
| `reviewer` | Those reads plus review decisions, retaining no-self-approval. |
| `publisher` | Those reads plus Listing release publication and transitions; no whole-host publication authority. |
| `discovery_reader` | Authenticated discovery, federation participation, Publisher reads and eligible human saved items/ratings. No local Resource-read grant; `job.read` alone exposes no operational Jobs without family/target authority. |
| `publisher_creator` | Publisher reads and creation. |
| `publication_admin` | Whole-host snapshot reads/generation/activation/rollback/unpublish and publication-family Job controls. |
| `source_admin` | Federation source management, inspection, synchronization/evidence, federation participation and source-family Job controls. |
| `security_reviewer` | Audit reads and bounded exports. |
| `webhook_admin` | Webhook management, tests, rotation and evidence. |
| `library_member` | A human member's own saved references and named searches; current target and search-execution authority still applies. |
| `feedback_member` | Human own-feedback workflows, with current item-read authority where required. |
| `tenant_admin` | All supported permissions, including protected direct tenant-level `access.manage`. |
| `service_client` | Inert identity template; no implied permission. |

A new assignment grants exactly its selected template. Explicit development and bootstrap bundles add tenant-level companion assignments to global Reader, Contributor and Publisher grants. Namespace grants never receive these tenant supplements. Roles/permissions and OAuth scopes remain independent checks. See [authorization](../architecture/security.mdx) for the finite permission vocabulary and current authorized collection/UI boundary.

| Scope | Route family |
| --- | --- |
| `catalog.read` | Catalog and namespace reads, Search, Explore, reviews, releases, ratings and publication reads. |
| `ratings.write` | Human submission or withdrawal of the caller's own version-specific rating. Rating reads use `catalog.read`; moderation and sharing administration use `tenant.admin`. See [ratings permissions](../guides/ratings.mdx#before-you-start). |
| `feedback.write` | Human `feedback.own` submissions and own feedback edits/withdrawal; no resource authoring rights. Reads use `catalog.read`; administrator inbox/actions use `tenant.admin`. |
| `personal.write` | Human `library.own` saves, updates and removals. Reads and exact-reference checks use `catalog.read`. This scope grants no resource authoring or tenant administration. |
| `catalog.write` | Resource, version, listing, submission, validation, and explicit observation-adoption writes. |
| `catalog.review` | Review decisions. |
| `catalog.publish` | Listing release and publication commands. |
| `sources.read` | Source, observation, and live-search reads. |
| `sources.manage` | Source inspection and lifecycle commands. |
| `publishers.read`, `publishers.manage` | Publisher reads and mutations. |
| `audit.read` | Audit event reads. |
| `audit.export` | Bounded deterministic audit export. |
| `webhooks.manage` | Webhook reads and mutations. |
| `tenant.admin` | Membership lifecycle, namespace creation and updates, Catalog role-assignment and access-policy administration, tenant operational settings, private feedback inbox/respond/reopen/remove, and ratings moderation and sharing administration. |

Development authentication does not enforce OAuth scopes, but it resolves its fixed tokens through the same Catalog membership and permission model and enforces current domain permissions and tenant boundaries. PostgreSQL Compose provisions those configured fixtures before API and worker startup; the in-memory profile seeds equivalent state directly.

## Request rules

- Control paths under `/api/v1/` reject repeated `Authorization`, `Content-Type`, `Idempotency-Key` and `If-Match` field lines. They return `400 INVALID_ARGUMENT` before authentication or command reservation. Names are case-insensitive. Identical repeated values are also rejected. Control revision commands require one current validator. Other HTTP fields retain their own list and repetition semantics.
- JSON request bodies require an object and `Content-Type: application/json`. Charset parameters such as `charset=utf-8` are accepted. Omit an optional body entirely when appropriate. `{}` is not interchangeable with `null`.
- Namespace, publisher and webhook PATCH commands use closed JSON objects. Omitted fields retain their values. `application/merge-patch+json` and null-valued fields return `400 INVALID_ARGUMENT`.
- Invalid request bodies return `400`. This includes missing or incompatible media types, top-level null or arrays, duplicate keys, and unknown or wrongly cased control fields. Explicit null in typed control fields, malformed JSON, trailing values and bodies over the configured limit also return `400`. Nested standards payloads and explicit extension maps retain their documented flexibility.
- JSON text must use valid UTF-8 and paired surrogate escapes. The service rejects malformed Unicode instead of silently replacing it. Persisted namespace, publisher, listing/adoption and source display text, and webhook names, reject NUL characters. This field restriction permits valid NUL escapes in arbitrary JSON payloads. Schema string lengths count Unicode code points. Request and retained-document byte budgets count bytes.
- Every representation `mediaType` must parse as an explicit type and subtype, such as `application/json`. The maximum length is 255 characters. Outer whitespace and control characters are forbidden. A bare token such as `json` is invalid. The service retains the exact declaration and uses it to select the adapter; content sniffing does not replace it. For credential-free HTTPS representations, retrieval evidence records the lowercase parameter-free response base type, which must match the declared base type.
- Mutating Control commands require `Idempotency-Key`. Check the OpenAPI operation for its required headers; a POST method alone does not make a request a mutation. Read-only Search, Explore, `POST /api/v1/ratings/read`, `/api/v1/artwork/read` and `/api/v1/access/capabilities` do not require it. The maximum key length is 200 characters.
- Reusing a key with the same actor, route, query, precondition, media type, and body returns the stored response with `Idempotency-Replayed: true`. Reusing it for different input returns `409`.
- A completed command has a fixed 24-hour replay window from its original reservation. After expiry, the same key can start a new command. Expiry does not make a pending reservation safe to discard. The reservation remains unavailable until the command finishes or an operator proves interruption of the command and applies the focused repair.

  A released reservation remains bound to its original request during the replay window. After expiry, a different authorized request may replace it without waiting for physical cleanup. PostgreSQL accepts only command protocol v2. It rejects unsupported development protocols without replay or conversion. This code-defined client contract has no runtime retention setting.
- PostgreSQL commands serialize the scoped key across replicas. The exact retained response and every command database mutation commit in the same transaction. Outbound preparation remains outside that transaction. A concurrently active or interrupted pending command returns `503` with `Retry-After`. Operators inspect and release only proven interrupted protocol-v2 reservations through the [idempotency repair runbook](../operations/idempotency-repair.mdx).
- Authentication and current OAuth-scope authorization run before replay. Protocol-v2 reservations bind stable tenant, realm and membership command identity separately from the current policy decision digest. Replay checks the original successful actions and scopes against current policy under a shared fence before returning retained bytes. Current authentication, OAuth ceilings and human-only requirements still apply. Unrelated policy changes do not duplicate execution. Deleted-target replay does not rerun lifecycle or ETag checks.

  Lost authority denies disclosure. Retained responses without original disclosure checks fail closed, except code-owned early `400 INVALID_ARGUMENT` parser problems. Those problems contain no private domain result and replay only for a current retained membership. Job results additionally require current family and target authority. A stored response never restores lost access.

  `X-Request-ID` and `Idempotency-Replayed` describe the current HTTP attempt. They are not copied from retained headers. An exactly replayed problem body keeps the originating command's `requestId` as execution evidence.
- Commands that mutate an existing revisioned object require `If-Match` with the current `ETag`, normally in the form `"rev-<number>"`. Missing or stale preconditions return `412`. Appending validation evidence through `POST /api/v1/version-revisions/{revisionId}:validate` does not mutate the VersionRevision and therefore uses idempotency but no `If-Match`.
- Publication snapshot activation and rollback use the exact ETag returned by the snapshot read. Do not construct a revision-number validator.
- A safe caller-provided `X-Request-ID` may contain letters, digits, `.`, `_`, or `-` and be at most 128 characters. Otherwise the service creates one.
- Request timestamps are not authoritative. The service clock supplies lifecycle and audit timestamps.
- An operation's required reason is durable user-authored audit text. It is not a private note. Keep it concise. Do not include credentials, bearer tokens, URLs, raw queries, documents, source responses or other secret or private payloads. Bounded validation and service-side structured-field redaction cannot reliably sanitize arbitrary prose.
- `ListingRevision.extensions` is an open service-control metadata map retained in the immutable listing digest. It is not automatically emitted as an ARD JSON-LD term or AI Catalog namespaced extension; a public mapping requires an explicit profile rule rather than guessing from the key.

Listing extensions preserve JSON numbers without conversion through floating-point values. PostgreSQL still applies its [JSON numeric limits](https://www.postgresql.org/docs/18/datatype-json.html#JSON-INPUT-OUTPUT). Unsupported numbers return `400 INVALID_ARGUMENT`; they do not create a ListingRevision or its mutation audit and outbox records.

## Revision validation responses

`GET /api/v1/version-revisions/{revisionId}` and `POST /api/v1/version-revisions/{revisionId}:validate` return `namespaceId`, `revision` and `evidence`. The namespace comes from the same-tenant Resource. It is response-only and does not change the immutable revision. Reads require content authority and successful audit retention; validation requires separate validation authority and its required audit. Failure discloses neither the namespace nor evidence.

If present, the validation command's body must be an empty JSON object. Supplying `namespaceId` is invalid. Clients can use the returned namespace for exact permission hints without parent metadata reads. The API rechecks authority for each command.

## Audit investigation and export

`GET /api/v1/audit-events` requires current `audit.read` authority and applies exact `actor`, `action`, `requestId`, paired `targetType` plus `targetId`, inclusive `occurredFrom`, and exclusive `occurredBefore` filters before the PostgreSQL page limit. Results are newest first with a maximum of 100. Its signed cursor binds the exact filters, page size, actor authorization, and `(occurredAt,id)` position. Each successfully prepared page appends audit-only evidence containing its count and event-array digest before the response is disclosed.

For `formatVersion: 1`, the response `action` is limited to the exact service-owned registry enumerated in OpenAPI and checked against the Go registry by contract tests. The action filter remains a bounded patterned string; an unknown action safely matches no current records.

`POST /api/v1/audit-exports` requires current `audit.export` authority, a unique `Idempotency-Key`, a bounded reason, and a mandatory half-open time range whose upper bound is no later than the server cutoff. It applies the same optional exact filters in one repeatable-read transaction and returns at most 1,000 events, 64 KiB per encoded record, and 8 MiB in total. Events are oldest first in `(at,id)` order, and all manifest times are normalized to UTC microsecond precision to match PostgreSQL. The closed manifest binds the normalized filters, first and last positions, count, exact events-array digest, cutoff, generation time, tenant, and `open-agentic-catalog-audit-export-v1` encoding profile. The export-access record and retained command response commit atomically; replay returns the exact saved response and creates no duplicate evidence. No export object or Job is created.

`AUDIT_EXPORT_RANGE_TOO_LARGE` requires a narrower range or more exact filters. Malformed or oversized records fail closed without silent omission or successful-access evidence. All audit records require `formatVersion: 1` and stable Catalog membership, service executor or database login attribution.

`catalogctl audit verify-export` checks bounded structural self-consistency without a database. `bind-manifest` and `compare-restore` support exact read-only restored-range comparison only when an external backup workflow binds the separately retained trusted manifest and database recovery point to the same quiesced-writer interval. See the [audit runbook](../operations/audit.md) for the assurance boundary and sequence.

## Authoring inventory and review context

`GET /api/v1/resources` accepts optional `canonicalIdentifier` and `kind` filters. The identifier is an exact match after the same trimming and AIR scheme/publisher canonicalization used at creation. Namespace/name case and opaque Unicode spelling remain significant. Omit `canonicalIdentifier` to remove that filter; a present empty or whitespace-only value is invalid. Tenant, namespace permission, deletion and both filters are applied before the page limit. Ordering stays descending `(updatedAt,id)`; signed continuation binds both canonicalized filters, page size and current authorization. A different filter or policy requires a fresh read.

Review pages still require `resource.read_history`. Each row can include `canonicalIdentifier` when current `resource.read` permits it, and `submittedDisplayName` when current `resource.read_content` permits it. Fields are independent and omitted when unavailable. The name comes from that submission's exact ListingRevision, never the newest candidate. The server projects this bounded context with the page; clients need no per-row metadata or content reads. These display fields are not retained submission evidence.

## Practical administration

Resource, version and listing histories use bounded cursor pages with immutable UUID ordering. The review inbox uses the same ordering. Reopen saved work through these reads:

- `GET /api/v1/resources/{resourceId}/versions` and `/listings`.
- `GET /api/v1/resource-versions/{versionId}/revisions`.
- `GET /api/v1/catalog-listings/{listingId}/revisions` and `/releases`.
- `GET /api/v1/review-submissions`, with optional `listingId` and `state` filters.

These reads require their canonical namespace action and `catalog.read`. Histories use `resource.read_history`. Unfiltered private collections select permitted namespaces before SQL ordering, limits, counts and facets. A valid current member without applicable grants receives an empty collection. Revision pages contain summaries without private representation bodies.

`GET /api/v1/review-submissions/{submissionId}/evidence` requires current `resource.read_content` at the submission's namespace. It returns exact submitted evidence independently of the latest validation run. The service audits the read and fails closed if it cannot retain that evidence.

`GET /api/v1/role-assignments?membershipId=...` pages only the selected member's grants under protected `access.manage` and `tenant.admin`. `GET /api/v1/me?namespaceId=...` returns only the caller's grant for the selected namespace; an absent grant returns an empty list without revealing whether the namespace exists. Omitting these filters preserves ordinary pagination and authorization-bound cursor semantics.

`GET /api/v1/catalog/indexed-record` takes a canonical `identifier`, explicit `versionPresent` and optional `version`. It returns a current reader-visible indexed projection, not raw observations or live content. An absent version differs from an explicitly empty version. Current source eligibility and tenant isolation are rechecked on each lookup. An indexed-only bookmark remains usable when a visible local counterpart exists; unavailable identities return `404`. Use a release UUID with the local record endpoint, not a prefixed Search result ID.

Source inspections and change previews are short-lived command inputs, not permanent administration history. Their own expiry remains authoritative even if physical cleanup is delayed. An immutable source revision keeps its referenced current-format inspection after expiry. An expired unreferenced inspection may return `404` after worker housekeeping. Audit evidence for the action is independent of the preview or inspection document.

Job List/Get return bounded metadata and, when valid, a compact result reference. Exact reads require `job.read` plus actual family and target authority. Job pages apply supported family reads and existing or configured targets before SQL ordering and limits. Family reads require `source.read` for `source.sync`, `source.read_evidence` for `source.trust_verify`, `publication.read` for `publication.generate`, or `operations.reindex` for `semantic.reindex`. A partial family grant receives only that family. Generic `job.read` without family authority receives an empty page.

`GET /api/v1/jobs` accepts optional `kind`, `state` and `parentJobId` filters. Each supplied filter must have one nonempty value. Kind is a literal, case-sensitive string of at most 100 Unicode code points without NUL. Unknown kinds return an empty authorized selection. State accepts the seven Job states; parentJobId accepts an exact UUID. Filtering children does not read or authorize their parent. Filters intersect with current family/target admission before ordering and page limits. Continuation cursors bind the filters, current tenant/access, ordering and limit. Changing a filter requires a new first page.

`GET /api/v1/federation/sources` accepts optional exact `displayName` and `status` filters before page limits. Display names retain case, spaces and duplicate identities. Supplied names allow 1-160 Unicode code points without NUL. Status accepts `active`, `paused` or `error`; omission includes all nonremoved sources. Empty, repeated and removed-state values are invalid. Signed continuation binds filters, tenant, current authorization, ordering and limit. This metadata read contacts no source. Removed configurations remain unavailable through directory and exact Source reads; exact retained snapshots and observations follow their separate admission rules.

For example, request `GET /api/v1/jobs?kind=source.sync&state=waiting&limit=25` to find retryable source work. Add `parentJobId=<original-job-uuid>` to select retry children. Use `GET /api/v1/jobs/<job-uuid>` for one exact retained Job. These reads preserve the same tenant, family, target and retention restrictions.

Job pages omit unknown families and removed or unconfigured targets. A source synchronization result identifies its source snapshot and completion state. A publication result identifies its snapshot, host, generation and state. Complete snapshots remain available through their owning authorized endpoints. Reads exclude execution payloads, lease ownership, causal context and embedded snapshot documents. Missing, unsupported or malformed retained results omit the optional summary while leaving the Job readable.

Succeeded `semantic.reindex` Jobs can expose `{"type":"semantic_index","generationId":"<job-uuid>","members":0}`. The generation UUID must match the Job ID. The nonnegative integer counts accounted inputs, including reused vectors and inputs recorded with failures. It does not count only new or successful embeddings, or establish the current active index. Other states, missing or malformed results omit this variant. Its generationId is separate from a publication snapshot's numeric generation.

Trust-verification Jobs expose bounded metadata without a result summary or private assessment. Reading an assessment requires separate current Job/source-evidence authorization and a successful audit before disclosure. See [ANS assessment operations](../operations/federation.mdx#request-an-ans-registration-assessment).

PostgreSQL projects result fields before transfer, including for older Jobs that retain full snapshots. Internal execution and retry reads preserve their complete inputs.

Jobs are operational history rather than governed Catalog evidence. Housekeeping first marks a terminal Job and retains it for at least the configured worker retention. After that period, an unleased leaf with no retry child may be reclaimed and return `404`. Active Jobs and retry parents remain available while their state or lineage requires them.

A published outbox row without webhook delivery evidence follows its separately configured prospective retention. This does not change public event or audit contracts.

Tenant Administrators can read one tenant membership or traverse the tenant membership collection through a repository-bounded signed cursor. The cursor is opaque, expires after 15 minutes, and binds the current actor authorization digest and page size. Suspension immediately prevents identity resolution but remains reversible. Reactivation is allowed only from suspension. Offboarding is terminal and atomically revokes every active global role assignment and namespace membership for that membership.

Each lifecycle command requires a bounded reason, `Idempotency-Key` and the current membership `ETag`. To protect the final active Tenant Administrator, the service serializes the checks for role revocation and membership lifecycle commands.

Identity-realm state is deliberately not tenant-controlled. A schema-owner operator uses `catalogctl identity realms list`, `suspend`, or `reactivate` with exact tenant and realm identifiers, revision, operator, and reason. Suspending the final active realm requires every configured discovery host for the tenant to be disabled and every active publication snapshot to be unpublished. Publication activation and rollback fail while the tenant has no active realm.

`PATCH /api/v1/namespaces/{namespaceId}` updates display metadata, and `PATCH /api/v1/publishers/{publisherId}` updates display metadata or the complete declared-domain set. They require current `namespace.manage` at the selected namespace or tenant-level `publisher.manage`, respectively, plus their unchanged OAuth ceiling, idempotency and optimistic concurrency. Declared domains are canonicalized and cannot be removed while approved or referenced by an active publication snapshot.

`POST /api/v1/resources/{resourceId}:delete` accepts a bounded reason and soft-deletes only an unpublished local draft with no adoption provenance, review submission, or release history. Deletion requires `resource.delete_draft` in the canonical namespace; deleting another creator's draft additionally requires protected `access.manage`. The canonical identifier, child history, deletion evidence, audit, and outbox remain retained, while current Resource, child, artifact, browse, and search reads return no deleted aggregate data. This is dependency-aware retirement of an unused draft, not hard deletion or privacy erasure.

Role revocation, draft deletion and source removal are body-bearing POST commands: `/api/v1/role-assignments/{assignmentId}:revoke`, `/api/v1/resources/{resourceId}:delete` and `/api/v1/federation/sources/{sourceId}:remove`. Each requires an audit reason, `Idempotency-Key` and the current `If-Match`. Source removal also requires `changePreviewDigest` from a current removal preview. Webhook disable remains a bodyless `DELETE /api/v1/webhooks/{webhookId}`. GET, HEAD and DELETE operations do not take request bodies.

## Observation provenance and adoption

Observation list, search and group responses never inline raw source entries. Their repository projections and occurrence-history parent checks exclude raw and normalized document bytes; availability is a retained-presence check, not a new cryptographic verification. `rawEntryAvailable` identifies exact decoded bytes available through the separately authorized `/raw` route. Current PostgreSQL observations require those retained bytes; missing bytes are an integrity error, not a supported JSONB fallback or enrichment workflow. `exactEntryDigest` names their SHA-256. The current format rejects incompatible development observations and incomplete occurrence provenance. It does not invent original evidence. A supported structurally valid observation also carries a SHA-256 over the service-owned `catalog-observation-v1` normalized entry so JSON formatting, object-key order, tag order, and representative-query whitespace do not create false semantic differences.

`GET /api/v1/observations/{observationId}/occurrences` returns a bounded page of at most 100 immutable appearances, with `nextCursor` when another page exists and no total count. Each occurrence identifies the source revision, complete or incomplete snapshot, containing retrieval digest, exact `/entries/<index>` manifest location or `/items/<index>` Registry List page location, entry digests, and observation time. Source revision and complete retained provenance are mandatory; normalized digests remain absent for unsupported upstream observations.

`GET /api/v1/observation-groups` requires an exact normalized AIR identifier and the typed version key: `versionPresent=true` requires an exact non-empty version, while `versionPresent=false` requires no version. An explicitly empty present version is rejected. The deterministic group contains only current normalized observations. Equivalent normalized entries share a member while retaining each exact source contribution and occurrence count; differing normalized members set `conflict`, and more than one normalized member from the same source sets `sourceConflict` on that source's contributions. The preview contains at most 100 contributions and reports `contributionsTruncated`; conflict checks still include members outside that prefix. Selected occurrence counts and latest tuples share one database-statement snapshot. Missing selected evidence fails the group instead of returning an incomplete summary. Use each observation's paginated history for retained occurrences; this is not group pagination or a constant-cost count guarantee.

`POST /api/v1/observations/{observationId}:adopt` is an idempotent `catalog.write` command. The actor must have current `source.read_evidence` for the selected source evidence and `resource.create` in the requested namespace, including their companion reads. The Tenant Administrator template supplies those actions. The request selects one exact occurrence by snapshot, containing retrieval digest, and entry location, plus an existing tenant publisher. The observation must be current, complete, structurally valid, supported, normalized, and free of a current same-source conflict; the occurrence must match its immutable digests and a complete snapshot. An observed version is preserved exactly, while an unversioned observation requires an explicit local version. Optional local display name, description, categories, URLs, license, terms, and visibility remain governed local choices; visibility defaults to `private`.

Successful adoption creates the Resource, ResourceVersion, VersionRevision, validation evidence, CatalogListing, ListingRevision, audit event, and outbox event in one repository transaction. The PostgreSQL command path also stores the exact replay response atomically with that transaction. The VersionRevision retains the selected observation, source revision, snapshot, retrieval digest, entry location, exact and normalized entry digests, normalization profile, actor, and adoption time. Source credentials are never copied or reused; a credential-free observed HTTPS representation is independently fetched through the ordinary hardened public representation boundary. Adoption creates a draft only and does not imply approval, trust, rights, publication, installation, deployment, invocation, or execution.

## Agent Skill package requests

Standalone Skill Markdown and packaged `SKILL.md` use the same frontmatter checks. Validation checks YAML-decoded strings before display normalization:

- Names cannot contain surrounding spaces or tabs.
- Descriptions must contain non-whitespace text and be at most 1,024 characters.
- Optional `compatibility` may be omitted. When provided, it must contain non-whitespace text and be at most 500 characters.

Limits count Unicode characters, not UTF-8 bytes. Quotes and YAML indentation are syntax. Decoded whitespace and block-scalar newlines count toward the limits. For example, `description: |` retains a final newline, while `description: |-` removes it.

Valid descriptions retain their trimmed display projection without changing original document bytes. Invalid values produce field-specific findings and blocked evidence. Validation does not silently correct them into valid input.

`packageData` supplies transient base64 bytes only for a governed `skill` representation. Its media type must be `application/agent-skills+zip` or `application/agent-skills+gzip`. GZIP means one gzip member containing one tar archive. A representation must use exactly one delivery form. Do not combine `packageData` with `data`, `content` or `url`. It requires an immutable Git handoff, OCI handoff or both in `handoff`.

A Git handoff requires a credential-free HTTPS repository URL without query, fragment or encoded path. It also requires a complete 40- or 64-character hexadecimal commit object ID, normalized to lower case. Its normalized relative directory must end with the validated Skill root.

An OCI handoff requires an explicit registry and lower-case repository without a mutable tag. It uses an exact `@sha256:<64 lower-case hex>` digest. These retained locators do not direct network activity. Request processing does not clone Git, pull OCI, fetch artifacts, verify remote existence, install a Skill or execute package content.

Package inspection enforces these limits:

| Input | Maximum |
| --- | --- |
| Decoded archive | 16 MiB |
| Combined regular-file content | 64 MiB |
| One regular file | 16 MiB |
| Archive entries and regular files | 1024 each |
| Path depth below the Skill root | 16 segments |
| Normalized path length | 1024 bytes |
| Expanded-to-compressed ratio | 100 |

The configured HTTP body limit includes base64 and JSON overhead. It can impose a lower effective archive limit. ZIP central-directory entry counts are checked before archive objects are allocated. The gzip ratio applies during decompression of the complete tar stream, including metadata and padding.

Structural validation rejects multi-disk, ZIP64, encrypted, traversing, absolute, duplicate, case-colliding and file-as-directory packages. It also rejects linked, special-file, concatenated-gzip and malformed packages. The service retains structural failures as an invalid representation with blocked validation evidence and no partial inventory. Such packages cannot enter review.

A successful version response contains the archive digest and compressed size, complete sorted file inventory, independent package assessment states and normalized immutable handoff. Later authenticated VersionRevision and Catalog record reads retain these fields. They never contain `packageData` or a Catalog artifact identifier. Optional malware, secret, license, SBOM and provenance assessments currently report `not_assessed`.

A package-only governed release remains available through authenticated Catalog metadata. Public ARD and AI Catalog snapshots exclude it with `PUBLIC_DELIVERY_FORM_REQUIRED`. The service does not retain its archive or invent a public HTTPS URL.

## Version validation runs

Creating a VersionRevision records its first profile-v7 EvidenceSet. Profile v7 normalizes digest-bound timestamps to PostgreSQL microsecond precision. Deterministic validation over immutable inline JSON, bounded PostgreSQL documents, or retained Skill package inventory and immutable handoff has no wall-clock expiry. Evidence about changeable HTTPS delivery bytes expires exactly 24 hours after retrieval.

A caller with current `resource.validate` can `POST /api/v1/version-revisions/{revisionId}:validate` with an optional empty object. The command appends a tenant-bound run under the current adapter profile and policy. The response shows the new EvidenceSet in `revision.evidenceSetId`. VersionRevision content, the content-set digest, creation fields, prior evidence, approval and release history do not change.

The command rereads and verifies retained representation identity before validation. Missing or corrupt local bytes, ambiguous JSON, metadata, inventory, or handoff fail closed without appending a run. A URL representation is then retrieved through the deployment-owned anonymous public route with the shared HTTPS, DNS, IP, redirect, deadline, and response-size controls. The response must remain on the same origin. Retrieval evidence records no credential or private-network profile.

A successful status, matching base media type and exact decoded bytes matching the immutable digest renew the 24-hour evidence window. An unavailable route, non-200 status, media mismatch, unsafe response or digest drift records a blocked run. Drift never replaces retained bytes. Accepting changed content requires a new VersionRevision. An explicit blocked revalidation withdraws active snapshots containing a release for that revision.

Expiry or deployment-policy change marks readiness stale but does not silently rewrite an immutable active snapshot. A current adapter may derive a different representation profile, normalized facts or forward-compatible unknown-field projection. Such a run is blocked because changing those immutable VersionRevision fields requires a new revision.

The service does not retain or execute Skill archive bytes. Package revalidation checks the immutable inspected inventory and handoff. Changed package content requires a new VersionRevision with new transient `packageData`.

A submitted review remains frozen to the exact validation run it selected. If another run is recorded before a decision, approval fails. A reviewer can record a terminal change request or rejection. The contributor then creates a new submission against the current run.

Submission, approval, and publication each lock the VersionRevision and verify that the expected current run did not change between the service check and database commit. Later validation does not rewrite a terminal approval. Publication separately requires a current complete run over the same immutable content under the active profile and policy.

URL-backed document publication first verifies the authoritative PostgreSQL bytes. It then performs a fresh anonymous fetch under the same policy. Status, base media type, size, digest and bytes must still match. The publication audit event retains this bounded verification. External calls occur outside the database transaction. A blocked or stale current run prevents publication, while a later successful identical revalidation can restore readiness without invalidating the historical approval.

A release can move from `published` to `deprecated`, `superseded`, `retired`, or `revoked`. Deprecation may name another published or deprecated release in the same tenant, while supersession requires one; the target may belong to another CatalogListing so an old governed identity can direct clients to its governed successor. Only those two commands accept request field `replacementId`, which is returned as `replacementReleaseId` on the release.

Only deprecation accepts `sunsetAt`, and the timestamp must be later than the transition time. Retirement and revocation accept no replacement guidance. Revocation is terminal. A retired or superseded release may later be revoked.

The database independently enforces the same-tenant, non-self replacement relationship, legal current-release pointer, approved submission lineage, and immutable release identity so a direct defective write cannot invent history that the API would reject.

Creating a document URL representation is synchronous. Its normalized origin must be permitted by the active deployment public egress policy. The default empty allowlist rejects every such request. An operator must add the exact public origin or deliberately select the broader public mode. Network, routing-policy, redirect or malformed safety evidence failures return a dependency error and create no VersionRevision.

A safely fetched response may have an application-level status, media or standards-validation problem. The service can retain an immutable blocked revision with exact bounded response bytes and findings. That revision cannot be submitted or published. Status and response-media failures belong to the renewable EvidenceSet, rather than immutable VersionRevision findings. Correcting only that response metadata can produce a later complete run without rewriting the revision. Structural findings about retained bytes remain immutable.

An external representation remains an external delivery URL even when PostgreSQL retains its exact validation bytes. An authorized tenant reader can inspect those bytes through the authenticated bounded-document endpoint while an active Resource representation references the document. A deleted draft cannot be recovered through that endpoint. A digest-bound document alias alone never allows public delivery through the Catalog artifact route.

Plugin archive URLs use a separate inspected-package contract. The optional [development demo archive fixture](configuration.mdx#development-demo-archives) admits only bounded prepared archives by exact path, digest and size. Ordinary and production representations retain public-only routing. URL-policy violations, unsafe archive structure and checksum mismatches return `422` and create no partial VersionRevision. Network outages remain dependency failures.

A safely inspected package with invalid component metadata can retain blocked evidence. PostgreSQL retains bounded inspection documents, inventory, and the archive digest and size. It does not retain the archive itself. Revalidation and publication check retained evidence and freshly retrieved archive identity. See [Agent Plugins](../guides/agent-plugins.mdx) for delivery forms, limits and validation behavior.

## Identifier and version lengths

Opaque resource identifiers permit up to 512 Unicode code points. Version labels permit up to 128 code points. Relationship targets use the same limits.

These limits count characters, not UTF-8 bytes. A combining mark counts as a separate code point. The Catalog does not normalize Unicode or change case for opaque identifiers or version labels. Composed and decomposed spellings remain distinct. Authoring commands trim surrounding whitespace before validation.

AIR identifiers retain ASCII syntax and the Catalog's 512-byte parser limit. Unicode version labels do not relax AIR syntax. Source allowlists and exclusions count Unicode code points for their string limits. Valid AIR values still use the AIR parser.

Request bodies, native documents, archives and retained evidence have separate byte budgets. A value within its character limit can still exceed a request byte budget. Invalid UTF-8 and NUL are rejected.

## Responses and errors

Successful JSON responses use `application/json; charset=utf-8`. Created and updated resources return an `ETag` where revision concurrency applies, and asynchronous work returns a `Location` when documented. Every response includes `X-Request-ID`.

Control errors use `application/problem+json` and [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457.html) fields:

```json
{
  "type": "https://catalog.example.com/problems/precondition-failed",
  "title": "Precondition failed",
  "status": 412,
  "detail": "If-Match is required for this command",
  "instance": "/api/v1/listing-releases/0198...:revoke",
  "code": "PRECONDITION_FAILED",
  "requestId": "4e8c..."
}
```

Internal and dependency failures do not expose dependency details. Use `requestId` to correlate safe structured logs. Router-generated unknown-route and method-not-allowed failures use the same safe response convention: control and service paths return `NOT_FOUND` or `METHOD_NOT_ALLOWED` problem details, and a `405` retains the HTTP `Allow` header. Public discovery paths instead retain the pinned ARD error shape with `errorCode` and `message`, including router-generated failures.

The optional [Catalog MCP endpoint](../guides/mcp-server.mdx#connect-over-http) uses JSON-RPC responses and safe tool refusals. Missing or invalid Bearer credentials receive a 401 challenge. Insufficient OAuth scope receives a 403 challenge. Enabled MCP method refusals retain their JSON-RPC shape and `Allow` header. Disabled MCP paths return ordinary 404 problem details.

### Error codes and next actions

The table gives Control API status codes. ARD discovery uses `application/json` with `errorCode` and `message`. Its mappings follow this table, except `PUBLICATION_NOT_READY` returns 503. Only codes applicable to the requested operation can occur; discovery does not expose every Control workflow.

| Code | Control status | Meaning and next action |
| --- | --- | --- |
| `INVALID_ARGUMENT` | 400 | Correct the request fields, encoding or continuation inputs. Restart a search when its cursor is no longer valid. |
| `UNAUTHENTICATED` | 401 | Supply a valid access token. Check its issuer, audience and expiry. |
| `FORBIDDEN` | 403 | Check current membership, grants and any OAuth scope ceiling. A denial does not establish whether a private target exists. |
| `SOURCE_POLICY_DENIED` | 403 | Ask an administrator to inspect the approved source and network policy. Do not bypass the fetch boundary. |
| `NOT_FOUND` | 404 | Check the identifier, host and current access. The target can be absent, unavailable or hidden from this caller. |
| `METHOD_NOT_ALLOWED` | 405 | Use a documented method. Check the `Allow` header. |
| `CONFLICT` | 409 | Read the current state and reconcile the command. Inspect chronology or concurrent work when the reported conflict requires it. |
| `VERSION_SEALED` | 409 | Create a new version label through the authoring workflow. The existing label's content is sealed. |
| `SEPARATION_OF_DUTIES` | 409 | Use a different eligible reviewer. Administrator permission does not waive independent review. |
| `INVALID_TRANSITION` | 409 | Check the object's lifecycle and complete the required prior step. |
| `IDEMPOTENCY_KEY_REUSED` | 409 | Restore the original request for that key. Use a distinct key only for a genuinely different command. |
| `PRECONDITION_FAILED` | 412 | Read the current ETag and review the change before submitting a new command. |
| `VALIDATION_FAILED` | 422 | Correct the reported content or prerequisite. Use the available validation diagnostics. |
| `UNSUPPORTED_PROFILE` | 422 | Select a profile supported by this deployment. Do not relabel incompatible content. |
| `PUBLICATION_NOT_READY` | 422 | Inspect candidate validation, release eligibility and active publication state. ARD reads return 503 when their publication is unavailable. |
| `AUDIT_EXPORT_RANGE_TOO_LARGE` | 422 | Narrow the export range. Preserve the documented ordering and verification procedure. |
| `WORK_LIMIT_EXCEEDED` | 422 | Reduce the work through the operation's documented workflow. Pagination alone may not reduce construction work. |
| `DEPENDENCY_UNAVAILABLE` | 503 | Inspect readiness, the affected dependency and any `Retry-After` header. Reconcile uncertain commands before another write. |
| `WEBHOOK_TEST_BUSY` | 503 | Another endpoint test occupies its admission slot. Follow any `Retry-After` advice before an explicit retry. |
| `REQUEST_DEADLINE_EXCEEDED` | 503 | The cooperative work deadline expired. Reduce expensive read scope; reconcile a mutation whose result is uncertain. |
| `INTERNAL` | 500 | Retain the safe request ID and contact the operator. Do not assume the mutation rolled back. |

An HTTP status alone does not prove rollback or make a mutation safe to repeat. Follow [uncertain-outcome reconciliation](../guides/integrating.mdx#retry-and-reconcile-uncertain-outcomes). Keep the original idempotency key, body and preconditions for an identical retry. Retrying does not extend the original replay window. Use [focused idempotency repair](../operations/idempotency-repair.mdx) only for a proven interrupted reservation.

For rejected native content or unavailable packages, use [content troubleshooting](../operations/troubleshooting.md#diagnose-rejected-content-and-unavailable-packages). MCP keeps its separate [JSON-RPC and tool-refusal contract](../guides/mcp-server.mdx#connect-over-http); do not interpret a tool refusal as a Control problem document.

## Trace correlation

With tracing disabled, a valid incoming [W3C Trace Context](https://www.w3.org/TR/trace-context/) version-00 `traceparent` provides log correlation only; no spans are created or exported. When enabled, a single valid `traceparent` becomes the server span's remote parent. Invalid or duplicate context is ignored without rejecting the product request; `tracestate` and baggage are discarded. Already-approved CORS origins may preflight `traceparent`.

No trace response header or tenant-facing trace lookup API is exposed; use `X-Request-ID` for support correlation. Each replay or Job poll creates its own HTTP operation. Asynchronous Job/webhook attempts link to private creation context without exposing it in public JSON or events. No tracing headers are forwarded to federation, artifact or webhook destinations. See [sampling and propagation boundaries](../operations/observability.mdx#distributed-tracing).

Catalog browse filters `kind` and `visibility` may be omitted; when present they must contain a supported enum value. An explicitly empty filter returns `400`/`INVALID_ARGUMENT` instead of widening the browse.

### Core live continuation

Core Search admits at most 50 live candidates before it slices client pages. A one-item page can therefore continue live overflow. This applies to mixed-origin Control Search and public ARD `auto` Search. Hybrid Search preserves the exact durable ranking. Live results follow those matches. Live-only Control Search pages the same bounded window without durable queries or embeddings.

Each continuation that needs live results repeats fresh approved peer traversal. The existing fan-out, hop, peer-page, deadline and byte limits apply to each replay. Smaller client pages can increase peer traffic. No stored Search session or peer token is used.

Search tokens have a 4,096-byte ceiling and one fixed 15-minute expiry. Unrelated collection and List token limits are unchanged. Tokens bind the query, page size, tenant, caller and current authorization. Public tokens also bind the publication host and audience. Both formats bind durable generations and verified live evidence, including actual fetch policy and credential revisions. Peer tokens, trace IDs and request receipts are excluded.

A changed verified window returns `400`; restart the search. Temporary replay failure returns `503`; retry the same token. An unsafe first traversal preserves healthy results and continues durable matches only. Expected traversal caps alone do not prevent continuation of the admitted window. The cursor does not promise exhaustive peer results or stability across updates.

Hybrid order combines two groups. All durable results retain their fused ranks and appear before live results. Upstream scores never reorder those ranks. Each result retains its integer score from 0 to 100 and its bound source. The final durable page still issues a token when live results remain, including an empty durable population. Hybrid continuation rechecks the ranking profile, query vector and generations even on a live-only tail page.

Live-only Control Search skips durable selection and embedding calls. Its token binds current access, query criteria and verified live evidence. `durableTotal` remains zero; `liveCount` counts only the current page. Facets retain their existing local population.

The service checks exact identities across all selected, authorized durable matches to remove duplicate live results. Local results win over indexed results, then live results. An excluded origin does not suppress a selected one. Core Control order is score, name and result ID. Public core order is score, identifier, version and source.

## Pagination and search consistency

`GET /api/v1/catalog/resources` returns at most 100 `CatalogSummary` items. `POST /api/v1/catalog/search` keeps common identity, display text, tags, publisher and score in its result envelope; `local` contains only local identities, release and validation status. These are explicit collection contracts, not incomplete `CatalogRecord` objects. Representations, findings, normalized facts, package inventories, listing extensions and relationships are available through authorized detail reads.

Use `local.release.id` from Search, or `release.id` from browse, with `GET /api/v1/catalog/resources/{catalogRecordId}` for the complete current record. The UI fetches that record only when opening a selected preview or detail page. A release withdrawn or replaced between Search and selection can return `404`; refresh Search explicitly. No detail is prefetched for every result. Full publication and retained evidence contracts are unchanged.

PostgreSQL selects the page and its summary metadata in the same transaction, trimming the repository continuation sentinel before metadata reads. Summary reads check external retrieval bindings without loading document bodies; they do not revalidate Skill package inventories/revision digests or Plugin document hashes/inventories. Exact detail and publication retain their full evidence checks. The memory repository also projects metadata before cloning retained bodies. The [payload and client limits](configuration.mdx#payload-and-client-limits) distinguish page bounds from exact-document retrieval.

Catalog Search accepts up to 32 exact tags, each up to 80 Unicode code points. Tags are trimmed, lowercased and deduplicated; all selected tags must match. Search text, publisher selectors and tags reject NUL. Text and publisher selectors have separate UTF-8 byte limits of 500 and 256; their OpenAPI character ceilings do not replace these byte budgets. Listing tags and categories use the same character limit. A supplementary character such as an emoji counts once, while a combining accent counts separately from its base letter. Tag limits count characters, not UTF-8 bytes; Unicode composition normalization is not applied. Federation tag scopes and indexed tag filtering use the same 80-code-point limit.

Search and Explore also accept `license` and `categories`. Local releases use immutable Listing declarations. Indexed and live entries use bounded `metadata.license` strings and literal Dublin Core subjects. License matching trims query whitespace and preserves case. It compares the exact declaration, without SPDX interpretation. Submit up to 256 Unicode code points; an empty license disables that filter.

All selected categories must match after trimming, lowercasing and deduplication. Submit up to 16 category labels, each with 1 to 80 code points. These limits apply before trimming. Whitespace-only labels and NUL are invalid. Missing declarations do not match.

Search applies these filters across selected origins. Omitted origins use the caller's authorized population. A namespace filter still selects local releases only; explicit indexed or live origins with a namespace return `400`. Explore covers local releases only. Durable filtering occurs before ranking, totals and pagination, including hybrid search. Explore applies its filters before facet counts. Changing a filter invalidates an existing cursor.

Live Control Search checks the complete predicate before candidate admission. It forwards an exact license filter. It omits topic filters because ARD's exact-value OR cannot require every normalized category to match.

Live entries without required usable evidence are omitted with an `insufficient_evidence` partial diagnostic. A known nonmatch is an ordinary omission. Peer work remains bounded, so live results can be incomplete. Public ARD filters and contexts are unchanged.

Each PostgreSQL Catalog Search reads local and indexed durable results from one consistent snapshot. Concurrent writes appear on a subsequent request, not halfway through that durable result set. Live results are fetched separately after the snapshot closes. A cursor from an earlier page still requires restarting the search if its generation has changed.

Authenticated Catalog Search uses `core-lexical-v1`: tokens are Unicode letters and numbers, lowercased without stemming, accent folding or compatibility folding. All query tokens must occur across the searchable fields; matches use the existing exact-identifier and field-weight scores. Empty or whitespace-only text browses eligible records with zero scores; punctuation-only text does not match. Results sort by descending score, then display name with Unicode whitespace collapsed and simple lowercase applied, then stable result ID. Equal normalized names therefore remain distinct and paginate deterministically.

Search text is not a query language: `invoice | warehouse` requires both words, quotes do not require a phrase, and `inv*` does not match `invoice`. Repeating a term does not increase its score. Unicode composition normalization is not applied, so precomposed accented characters and a base letter followed by a combining accent can produce different tokens.

An exact identifier match scores 100. Otherwise, a match starts at 40 when every query term occurs somewhere in the identifier, display name, description, tags or representative queries. Add the following bonuses only when that field contains every query term; tags and representative queries are each treated as one combined field.

| Field | Bonus |
| --- | --- |
| Display name | 20 |
| Representative queries | 15 |
| Description | 10 |
| Tags | 5 |
| Identifier | 5 |

Bonuses add together, so a non-exact match scores at most 95. These scores describe lexical matching, not trust or approval.

PostgreSQL uses its built-in Unicode collation for normalization regardless of the database's default locale. Missing or outdated indexed projections return `503 DEPENDENCY_UNAVAILABLE`; complete the [bounded projection repair](../operations/search-projection-repair.mdx) before resuming search after the normalization upgrade.

- Where an implemented collection accepts `limit`, values are strict integer literals from 1 through 100; decimal or exponent spellings such as `1.0` and `1e0` are rejected. Empty, repeated, nonnumeric, zero, negative, or oversized values return `400`.
- Administration collections use a v1 default of 20 and maximum of 100, fixed endpoint-specific ordering, database seek predicates and `limit + 1`, safe summary projections where the stored object is too large or private, and purpose-bound signed cursors. They do not issue a collection-wide `count(*)` or return a misleading `total`; `nextCursor` alone signals continuation. This applies to memberships, namespaces, publishers, Resources, role assignments, sources and source snapshots, observations and occurrences, source-administrator observation search, publication snapshots, Jobs, audit events, and enabled webhook endpoints and deliveries.
- An administration cursor expires after 15 minutes and binds the current authorization digest, endpoint purpose, parent identifier, normalized filters, fixed order, and page size. It cannot be reused by another actor or tenant or with changed filters, parents, ordering, or limits. Administration traversal does not invent a materialized session or synthetic generation for mutable rows; concurrent changes can alter a later page, so a client that needs a fresh coherent view restarts traversal.
- Observation-group attribution returns at most 100 source IDs and reports truncation, while complete same-source conflict detection remains a tenant-scoped PostgreSQL operation rather than a response-driven scan.
- Catalog Search defaults to 20 results when `limit` is omitted.
- A Catalog Search `nextCursor` is opaque and expires after 15 minutes. It is authenticated and bound to the local or unified search surface, tenant, subject, permissions, policy, normalized query, page size, `core-lexical-v1`, and the conservative local and indexed tenant search generations. It contains only the last deterministic sort position and non-sensitive generations, not query text or records.
- A cursor cannot be reused by another actor or with changed filters or page size. Tampered and expired cursors return `400`.
- If a matching search record changes between pages, its generation-bound cursor becomes stale. Restart the search instead of modifying or decoding the token.
- Catalog Explore computes facets over the exact local matched set and does not accept `limit` or `cursor` inside its query.
- Public ARD `none` and `referrals` Search plus List use request-bound snapshot cursors. Core `auto` continues an unchanged bounded live window alongside complete local snapshot traversal. Hybrid `auto` preserves the exact durable order. Live results follow those matches. Neither mode promises exhaustive peer inventory or a frozen session across updates.

## Discovery publication

Discovery endpoints return `404` until a candidate for their exact configured host is built and activated. Publication control requests name that host explicitly. Activation revalidates the exact eligible membership. Anonymous manifests and bounded documents use public cache directives; authenticated discovery uses private directives and varies on authorization. Both include immutable digest and cache validators, and conditional requests may return `304`. The document handler first validates the complete active snapshot, verifies that it references the exact content identifier, and then reads only the immutable tenant-scoped document bytes. Publication-domain revocation or current-release revocation withdraws every affected active host snapshot in the same PostgreSQL command transaction, so a later discovery request fails closed instead of hydrating mutable Catalog state. Public projection accepts the authoritative terminal-only AIR form while retaining publisher-domain authorization; service-generated identifiers remain explicitly namespaced.

Discovery is metadata only. A result does not mean that an Agent is authorized to run, an MCP Server is connected, a Skill is installed, or its publisher is trusted.

## Local Catalog presentation labels

Local collection summaries, Search's local envelope and full Catalog records may include `display`: current `publisherName`/`namespaceName` (each at most 160 Unicode code points), exact compact listing `license` (at most 256 code points), or `licenseOmitted: true` when the complete declaration exceeds that bound. License text and omission cannot coexist; missing/empty declarations have neither. The full ListingRevision retains its complete license. This presentation bound is separate from the existing document and transport byte limits.

Labels are limited to the current release's tenant-owned publisher and Listing namespace. Reading these labels requires the release's existing authorization, not directory enumeration rights; direct Publisher/Namespace routes retain their own checks. Display hydration and the compact license projection do not change relevance scoring, filter meaning or immutable release evidence. Publisher filters continue to use the local Publisher UUID or external AIR authority. Indexed/live results do not acquire local directory labels. Public ARD metadata and publication snapshots are unchanged.

Explicit local publisher ordering uses the current publisher display name during query selection, with the existing identifier fallback. Its keyset position retains that sort value; final display hydration remains bounded to the selected page. PostgreSQL Publisher inserts, updates and deletes invalidate tenant Search generations, even for cursors that use another order. Restart Search after such a cursor refusal. See [publisher Search ordering](../architecture/persistence.md#000046---publisher-search-ordering).

## Catalog origin and local namespace selection

Authenticated `POST /api/v1/catalog/search` accepts optional `origins`, a nonempty unique subset of `local`, `indexed`, `live` (maximum three), and optional `namespaceId`, the local namespace UUID that owns the Listing. Omitted origins preserve existing participation. A namespace narrows omitted origins to local only; when both are explicit, origins must be exactly `["local"]`. UUIDs outside the caller's tenant or with no matching releases return no matches without an existence lookup. Unknown values, nulls, empty arrays, duplicates and invalid UUIDs are rejected. These are Control API selectors; public ARD dynamic filters and entries are unchanged.

Local browse (`GET /api/v1/catalog/resources`) and local Explore query accept the same optional `namespaceId`. Explore totals/facets remain local and conditional on all submitted criteria. Local identity precedence suppresses indexed duplicates only when local origins are selected. Live remains text-dependent, transient and policy-governed. Core mixed-origin Search can continue an unchanged bounded live window. Live-only requests page the same verified window with `durableTotal: 0`, without durable selection or embeddings; `liveCount` counts returned items. Excluded populations are skipped before ranking/counting/page selection, and excluded live sources receive no requests. Both lexical and hybrid continuations bind normalized origin/namespace criteria and current principal/permission/generation state; changing filters requires a fresh request.

License and category filters apply across selected origins. License is a trimmed, case-sensitive exact declaration of at most 256 Unicode code points. All selected categories must match after trim/lowercase normalization, with at most 16 raw terms of 80 code points. Local releases use immutable Listing metadata. External entries use bounded `metadata.license` strings and literal Dublin Core subjects. Missing or unusable declarations do not match. Live results without required evidence produce a bounded `insufficient_evidence` partial diagnostic. Known declaration mismatches are ordinary omissions. These fields do not change ARD filters or expand source authority.

### Private collection authorization

Namespace directories, Resources, release-backed browse/search, facet aggregates and the unfiltered review inbox use current direct/group permissions before selection. Namespace, metadata, history and retained-content permissions remain separate. PostgreSQL selects the active principal, authorization revision, eligible rows, exact totals and facets in one repeatable-read snapshot; the bounded memory implementation holds its current policy lock for local reads. No namespace allowlist is expanded into a request, and overlapping direct/group paths do not duplicate rows. Global read decisions are computed once per selection transaction; scoped access uses same-tenant semijoins.

Search cursors bind the current authorization revision. Changed policy invalidates existing continuations; a policy change between cursor binding and SQL selection also requires restarting the read. Exact targets still require their canonical permission; an empty authorized collection does not grant detail or raw-content access.

Default unified Search limits callers without tenant-level `federation.discover` to permitted local records. Explicit indexed/live origin requests require that permission before external participation or query disclosure. Indexed local precedence considers only visible current published/deprecated local releases: a hidden or historical local match cannot suppress an otherwise eligible external result or alter its conflict/provenance evidence. Public ARD Search/Explore remain separate active immutable audience-snapshot reads; namespace release authority does not grant host publication authority.

## Rating commands

Target totals and own-rating reads use `catalog.read`. Human scoring and own withdrawal use `ratings.write`. The moderation inbox, moderator commands and sharing reads or changes use `tenant.admin`. Scopes narrow retained Catalog permissions. The [ratings guide](../guides/ratings.mdx#before-you-start) lists the required permissions.

Writes require `Idempotency-Key`; changes require the current ETag. A replay acknowledgement describes its original command, not the current score. Submit and sharing responses do not expose an individual's score. Read current state through the authorized rating endpoints.

## Personal work and saved items

`GET /api/v1/me/work?queue=drafts` selects caller-owned work. The other queue values are `submitted`, `changes_required` and `approved`. The latter excludes an exact submission with any retained release. Each continuation binds the actor, current policy, queue and page size. Work selection requires current per-field Resource permissions without granting audit access.

`/api/v1/personal/items` supports owner-only paged reads and explicit creation. Exact item routes support GET, PATCH and DELETE. Commands require `Idempotency-Key`; PATCH and DELETE also require `If-Match`. Receipts contain only item ID and revision. `POST /api/v1/personal/items/{itemId}/resolve` accepts `{}` and performs a read without idempotency or network discovery. An unavailable exact target returns `status: unavailable`, without changing the saved identity. A saved search cannot be resolved or executed through this route.

The `library.own` permission requires a human membership. When scope enforcement is enabled, reads and resolve require `catalog.read`; mutations require `personal.write`. Entries belong to tenant, membership and realm; callers cannot supply an owner. Owner management survives target access loss, but saving a changed reference requires current target admission. See [library limits and consent](../guides/catalog-ui.mdx#save-resources-and-searches).

## Service activity reads

`GET /api/v1/service-activity` is default-off. Enable `http.serviceActivityEnabled` before use. GET and implicit HEAD require active membership, tenant-wide `access.manage` and enforced `tenant.admin`. The response shares process traffic across tenants. It accepts no query parameters or body and uses `Cache-Control: no-store`. Counters are exact decimal strings; preserve their integer precision. See [request statistics](../operations/observability.mdx#read-api-request-statistics).
