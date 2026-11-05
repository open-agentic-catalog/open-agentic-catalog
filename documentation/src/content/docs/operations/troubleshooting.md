---
title: Troubleshoot safely
---

Identify the exact image version, profile digest and process role. Record the safe request ID, time window and affected operation. Use [observability](observability.mdx) to inspect metrics and bounded diagnostics. Do not collect SQL dumps or complete request payloads. Do not include tokens, DSNs, source URLs, private documents or webhook keys in a support bundle.

| Symptom | Read-only checks | Action and stop condition |
| --- | --- | --- |
| Startup refuses configuration or database authority | Role-specific config validation, migration plan, safe startup error and exact-role reports. | Correct the named input; if migration/reconciliation is incomplete, stop both runtimes and follow [database roles](database-roles.md). Do not use a privileged runtime DSN. |
| Process is live but not ready | Check startup/drain state and required PostgreSQL connectivity/read-write session capability for that role. | Correct the database route/role or wait for controlled drain. Liveness must not restart a healthy process for an optional receiver outage. |
| Local cluster startup cannot create an inotify instance or file watcher | Inspect host inotify quotas and the failing process's open-file limit. "Too many open files" can have either cause. | Follow [host inotify tuning](deployment.mdx#inotify-limits-for-local-clusters). Keep higher existing limits. Change the container host, not the Catalog Pod. |
| JWT rejected or authorized token denied | Check configured issuer/audience/time, issuer-specific trust and active tenant membership/namespace grants. | Correct identity configuration or use authorized provisioning. Do not log the token, bypass verification or infer private object existence from a denial. |
| Synchronization fails or results appear stale | Inspect the exact source's state, Job, bounded diagnostics and last complete snapshot. | Correct approved destination/credential/policy inputs, then use normal retry. Partial failure must preserve the previous complete snapshot; do not delete evidence to force a refresh. |
| Jobs remain queued or retry | Check worker readiness, fixed component state, leases and terminal Job errors. | Restore the worker/dependency and let normal leases recover. Retry/cancel only through supported APIs; do not mark a Job successful in SQL. |
| Mutation conflicts or uncertain replay | Compare request identity, key and ETag; retain only safe request IDs. | Reconcile stale revisions; retry the identical uncertain command. Use [idempotency repair](idempotency-repair.mdx) only after its lock-backed plan proves interruption. |
| Derived search is inconsistent | Complete tenant-bounded read-only restore verification when appropriate and identify exact repairable projection findings. | Use [search projection repair](search-projection-repair.mdx), then reverify. Do not rebuild retained evidence from current upstream bytes. |
| Webhook backlog or receiver gaps | Inspect endpoint state, delivery outcomes, optional degradation and bounded reconciliation state. | Follow [webhook recovery](webhooks.mdx); correct the receiver or rotate the exact secret if needed. Duplicate delivery is expected; an empty queue is not proof of receipt. |
| Latency, memory or database pressure | Compare app request/SQL/pool metrics, worker progress and deployment CPU/memory/connection/disk signals. | Use the [performance guide](../guides/performance-and-monitoring.md) in an owned fixture to attribute cost. Tune documented bounds using evidence; no universal threshold or mandatory Grafana is implied. |
| Missing traces or increasing export failures | Check tracing enablement on the affected replica, sampling, per-process export counters and collector service/installation filters. | Follow [tracing troubleshooting](observability.mdx#verify-and-troubleshoot-tracing) for endpoint, network, trust and credential checks. Collector failure does not make the application unready; do not restart healthy workloads solely for a collector outage. |

## Find missing Catalog content

For host routing or cache disclosure failures, follow [ingress and cache verification](deployment.mdx#verify-discovery-through-ingress-and-caches). For disk pressure, use the [storage-growth worksheet](maintenance.mdx#plan-storage-growth) before selecting cleanup or infrastructure changes.

Identify which view or endpoint has missing content. **Authoring** shows permitted local Resources and their history. **Catalog** and **Explore local catalog** show current released content, with their selected filters. ARD and AI Catalog discovery use active immutable snapshots for an exact configured host and audience. A successful write to one surface does not update every other surface.

Use authorized exact reads and bounded pages to check the retained state. Keep the IDs in your own investigation; do not expose another tenant's records or include private identifiers in a support bundle. A `404` or empty authorized result does not prove whether a hidden record exists.

| Symptom | Check | Next action |
| --- | --- | --- |
| A saved Resource appears in Authoring but not Catalog | Read its versions, listings and release history. A retained draft, complete validation or approved submission is not a published release. | Continue the [review and release procedure](../guides/publishing.mdx). Do not reload samples to create a missing release. |
| A released item is absent from the UI | Check the signed-in tenant and current namespace permissions. Clear text, kind, tag, publisher, visibility, local namespace and origin filters. Confirm the Listing's current release and disposition. | Restart the search with the intended filters. Compare an authorized exact Catalog record read with [local discovery and pagination](../guides/integrating.mdx#example-filter-and-continue-control-api-search). |
| A release is excluded from a publication candidate | Read the candidate's `exclusions`, including each entry's `profiles` and `reasons`. Check visibility, primary representation, evidence and representative queries. | Correct the owning content or listing through the governed workflow, then build a new candidate. Follow [snapshot publication](../guides/publishing.mdx#build-and-activate-discovery-separately). |
| The discovery endpoint returns no item or `404` | Check the actual hostname, ingress Host handling, configured mode and snapshot audience. Inspect history for that exact host and confirm a snapshot is active. | Activate an eligible candidate for the intended host, or restore publication through the [publication procedure](../guides/publishing.mdx). Changing a request body or forwarded-host header does not select another host. |
| Catalog shows a replacement release but ARD still shows the old entry | Compare the current release with the active snapshot's immutable membership and creation time. Release publication does not automatically rebuild the active snapshot. | Build and activate a replacement snapshot after reviewing its full membership and exclusions. |
| External results or source observations are missing | Check retrieval mode, source scope, source status, the returned Job and `lastCompleteSnapshotId`. A live-only source produces no durable indexed inventory. | Follow [federation result checks](federation.mdx#test-synchronize-and-inspect-the-result). Preserve the last complete membership after a failed or partial attempt. |

For local state, use `GET /api/v1/resources/{resourceId}/versions`, `GET /api/v1/resources/{resourceId}/listings` and `GET /api/v1/catalog-listings/{listingId}/releases`. These histories require their current read permissions; follow `nextCursor`. An exact local Catalog record uses `GET /api/v1/catalog/resources/{catalogRecordId}` with the release UUID, not a Resource UUID or Search result prefix.

Use the owning namespace UUID for the local namespace filter. The Resource and its Listing share that namespace. A Resource UUID or the textual namespace segment in an AIR identifier is not a substitute. A namespace filter narrows Control API Search to local results. Explicit indexed/live origins require current `federation.discover`; default Search can return only authorized local content. See [origin and namespace selection](../guides/search-cookbook.mdx#origin-and-local-namespace-filters).

An authorized Publication administrator can read `GET /api/v1/publication/hosts`, then `GET /api/v1/publication/snapshots?host={host}`. Read a selected snapshot with `GET /api/v1/publication/snapshots/{snapshotId}?host={host}`. Confirm `host`, `audience` and `state`; only `active` supplies discovery content. A candidate is not active. A retired snapshot cannot supply current discovery.

Anonymous snapshots include eligible public releases. Authenticated snapshots can also include tenant releases, but never namespace-private content. Authenticated discovery requires the host's tenant, current `discovery.read` and `catalog.read` when OAuth scopes are enforced. The URL's host and deployment map select the audience. See [discovery-host configuration](../reference/configuration.mdx#discovery-host-configuration).

Publication exclusion reasons are findings, not HTTP problem codes. `REPRESENTATIVE_QUERIES_REQUIRED` means the ARD entry lacks the required two to five queries. `LISTING_NOT_PUBLIC` identifies content outside an anonymous audience. `STANDARD_PUBLIC_REPRESENTATION_REQUIRED` means the release lacks a valid supported primary representation. `PUBLICATION_EVIDENCE_BLOCKED` identifies incomplete, mismatched or expired evidence. `PUBLIC_DELIVERY_FORM_REQUIRED` identifies content without a supported public delivery form. Some reasons apply to only one generated profile.

For example, an MCP Registry record alone cannot replace a primary MCP Server Card. A package-only Skill can remain readable through the authenticated Catalog while lacking a public delivery form. Check [native resource content](../guides/resource-authoring.mdx) and [evidence distinctions](../reference/standards.mdx#validation-approval-and-trust) before changing a representation or its media type.

A withdrawal can retire an entire containing snapshot. Current-release revocation or publisher-domain revocation can make subsequent discovery return `404`. Read the authorized release and snapshot history before preparing a replacement. Do not edit retained snapshot bytes or use a stale candidate to restore withdrawn content.

For external observations, inspect `GET /api/v1/observations?sourceId={sourceId}&freshness=current`. Use `freshness=all` to inspect retained non-current history. Follow pages and compare complete snapshot and occurrence IDs. An incomplete attempt cannot prove remote withdrawal. A paused source stops indexed search and live participation while retaining evidence. Source removal ends participation without deleting an adopted local draft.

Core Search continuation resumes durable results and an unchanged bounded live window. Changed live evidence requires a new search. A temporary replay failure permits retry with the same cursor. Hybrid Search returns local and indexed results in their exact order, followed by live results. Live-only Control Search pages without durable queries or embeddings.

Check `partial` and diagnostics before treating external discovery as complete. ARD `none` searches only local snapshot content; `referrals` returns approved descriptors without contacting them. See [retrieval and referral modes](federation.mdx#choose-indexed-live-and-referral-participation).

## Check sign-in and unavailable actions

Provider login, API authentication and Catalog authorization are separate checks. An issuer can authenticate a subject without granting that subject any Catalog task. Browser permission hints never replace current backend checks.

| Symptom | Check | Next action |
| --- | --- | --- |
| Provider login succeeds but Catalog access is refused | Check the API issuer/audience, tenant mapping and exact Catalog realm/subject membership. Unknown, suspended or offboarded memberships cannot authenticate. | Use the [sign-in failure procedure](sign-in.md#check-representative-failures). Ask the responsible administrator to correct the intended membership; do not self-provision from token claims. |
| My access loads but Catalog or authoring actions are unavailable | Check tenant permissions, the exact namespace projection and the OAuth ceiling. `availablePermissions` means an action is available somewhere. | Refresh My access and inspect direct/group paths through [access management](../guides/access-management.mdx). Grant only the required role and scope through the authorized administrator. |
| A visible action is denied after policy changed | Check the current membership and assignment state, then refresh browser hints. An earlier hint or stored command response does not restore lost authority. | Reconcile current access before preparing a new command. Preserve exact retry inputs when the earlier outcome remains uncertain. |
| Permissions appear sufficient but a workflow is refused | Check human-only admission, independent review, sealed content, lifecycle state, revision and required evidence. | Follow the refusal's owning [review and publication step](../guides/publishing.mdx). A Tenant administrator cannot bypass separation of duties or a token's scope ceiling. |

For an active member, `GET /api/v1/me?namespaceId={namespaceId}` checks the selected namespace without exposing its existence when no grant applies. `GET /api/v1/access/self` pages effective assignment paths. Read every relevant page and restart pagination after a policy change. Removing one direct or group path can leave another grant effective.

When OAuth ceilings are enabled, local Catalog, namespace and history reads need `catalog.read` in addition to Catalog permissions. Writes, reviews, publication and administration use their separate scopes. Issuer roles/groups and a broad token scope create no Catalog permission. Use the [permission and scope matrix](../reference/api-conventions.md#authentication-and-authorization).

## Diagnose rejected content and unavailable packages

Read the HTTP status and stable problem `code` before interpreting validation findings. Control API problems include `X-Request-ID` and a bounded safe detail. Retain the request ID and operation; do not copy the native document or external response into support output.

| Result | Meaning and bounded response |
| --- | --- |
| `400 INVALID_ARGUMENT` | Check the closed request object, field casing, duplicate keys, explicit nulls, Unicode, `Content-Type`, delivery form and request byte limit. Correct the rejected input before issuing a new command. |
| `201` with blocked evidence | The Catalog retained a revision, but one or more validation findings block the workflow. Inspect `evidence.state`, `evidence.findings` and representation results. Correct an unsealed revision, or create a new version after sealing. |
| `422 VALIDATION_FAILED` | Check the operation's safe detail and evidence conditions. Plugin archive response type, checksum or unsafe structure can fail before a usable revision is retained. Do not treat every native format failure as the same HTTP outcome. |
| `422 PUBLICATION_NOT_READY` | Check candidate eligibility, evidence and host publication state through the publication owner. Do not infer the missing condition from the status alone. |
| `422 WORK_LIMIT_EXCEEDED` | The operation exceeds a supported work boundary. Narrow the selected work or use its documented operator procedure; do not assume this is the HTTP body limit. |
| `503 DEPENDENCY_UNAVAILABLE` | Check the named operation's dependency, approved route and bounded diagnostics. An external representation fetch can fail without creating a VersionRevision. Restore the dependency and use the supported retry boundary. |

The backend request default is 2 MiB. It includes JSON and base64 overhead. Retained textual documents have a separate fixed 16 MiB per-revision budget. Skill/Plugin archives have separate compressed, expanded, file-count, depth and ratio bounds. Read the [payload limits](../reference/configuration.mdx#payload-and-client-limits) and the [Skill package contract](../reference/api-conventions.md#agent-skill-package-requests). Increasing a transport limit does not relax archive structure or validation rules.

Malformed or over-limit control JSON returns `400`; do not assume `413` or `415`. A standards document admitted inside the explicit native payload can instead produce retained blocked evidence. For a Plugin whose safe inventory cannot be retained, inspection can return `422 VALIDATION_FAILED`. Findings such as `PLUGIN_ARCHIVE_INVALID` are validation findings, not new HTTP problem codes.

An external document or Plugin archive uses anonymous public HTTPS retrieval through the hardened fetch boundary. The deployment must permit its exact origin. Source credentials and private federation profiles do not authorize resource-package retrieval. Inspect TLS trust, DNS/address policy, redirects, response media type, byte bounds and expected checksum through the owning [Plugin troubleshooting](../guides/agent-plugins.mdx#troubleshoot-authoring) or [resource authoring guide](../guides/resource-authoring.mdx).

Keep the request and retained-content identity separate from current remote bytes. Correct the source package or approved deployment input, then create the appropriate new revision or validation run. Do not rewrite retained evidence, disable TLS verification, add a private-network bypass or run unbounded retries.

Other common stable results include `401 UNAUTHENTICATED`, `403 FORBIDDEN`, `403 SOURCE_POLICY_DENIED`, `404 NOT_FOUND`, `409 VERSION_SEALED` and `412 PRECONDITION_FAILED`. An unavailable optional capability, wrong lifecycle state or stale ETag needs its own correction. The [response contract](../reference/api-conventions.md#responses-and-errors) owns the complete semantics.

When the reader checks do not explain the result, ask the operator to correlate the safe request ID with [application logs](observability.mdx#logs-and-trace-correlation) and [bounded diagnostics](observability.mdx#operator-diagnostics). Diagnostics must use the affected process role and reviewed inputs; a successful check in a separate process does not establish a running replica's state.

## Clock corrections and transition ordering

Source configuration, successful source tests and synchronization use the saved source timestamp as a lower bound for new transition times. New synchronization retrieval evidence stays within its snapshot's ordered start/completion interval. Publication candidate creation and activation use microsecond precision. Activation cannot precede creation, and rollback advances the snapshot's previous activation time by at least one PostgreSQL microsecond.

Membership suspension, reactivation and offboarding use the previous membership update as a lower bound. Corresponding audit and notification timestamps agree with the transition. Existing history is not rewritten, and database constraints remain enforced.

Job heartbeats, completion, failure and cancellation also keep update timestamps nondecreasing. A heartbeat cannot shorten its current lease, and a retry cannot be scheduled before the retained failure transition. Lease ownership and expiry predicates still use the sampled wall time. Clamping retained metadata does not bypass those checks. Terminal Jobs remain immutable.

These rules preserve transition ordering. They do not correct clocks or measure elapsed time. They tolerate a backward wall-clock step in these workflows, including after a process restart. Token validity, credential and inspection expiry, request deadlines and lease eligibility keep their existing clock semantics.

Clock corrections can also interrupt Search pagination. A backward adjustment can put a newly issued cursor beyond its permitted future lifetime. The Catalog rejects that continuation. Start a new search after the clock stabilizes. A cursor refusal alone does not establish a clock cause.

This is not a service-wide guarantee against arbitrary clock skew. Deployment operators must synchronize clocks. Do not change the host clock or disable history guards to diagnose a failure. Regression tests inject a fake service clock or explicit store timestamps.

## Escalation and evidence

For suspected isolation, integrity or credential compromise, preserve evidence. Follow [incident response](incident-response.md) and [security reporting](../../../../../SECURITY.md). The deployment owner determines severity, escalation and recovery objectives. Keep traffic or outbound work blocked while integrity is uncertain. Mutating smoke tests belong only in disposable environments unless separately authorized with an exact scope.

A useful handoff contains version/profile identity, redacted configuration differences, role and health states, bounded metrics over a named interval, safe request IDs, exact failed command/check, attempted containment and unresolved findings. Identify missing evidence. Do not replace it with a successful process exit or an unrelated developer test run.
