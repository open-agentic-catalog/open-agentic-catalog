---
title: "ADR-0020: Private authenticated item feedback"
---

- Status: Accepted

## Context

Catalog needs a way for users to report problems, suggest improvements and describe positive experiences with a particular resource. Publication review already records whether exact revisions are eligible for release; it is not a user-feedback channel. Listing Support URLs already provide an external reporting option, but do not retain an in-Catalog submission associated with the selected resource and version.

The pinned ARD and AI Catalog profiles define discovery and extensible metadata, not a shared feedback-submission or moderation protocol. ARD Search scores express query relevance, not user satisfaction or approval. Feedback therefore has its own Control API authority and is not publisher-authored listing metadata, a Trust Manifest claim or a public discovery projection.

## Decision

Private feedback is implemented in the existing Go service and PostgreSQL database, with the optional UI as an API client. The [feedback guide](../../guides/feedback.mdx) describes the Control API, submitter and moderator workflows and concrete limits. No configuration flag or additional deployment dependency is required.

### Submitters, visibility and management

Only active authenticated human Catalog members with tenant `feedback.own` and current target `resource.read` permission may submit feedback. Derive the submitter from resolved membership, never a request-supplied identity. Anonymous visitors and service accounts cannot submit, even for public listings. Reuse configured development human identities for tests; do not introduce another login mechanism.

Only the submitter with current `feedback.own` permission or a caller with tenant `feedback.moderate` permission may read a submission. Feedback stays private when a listing is public. Reads and changes recheck current membership and feedback permissions. Losing target-read access does not remove access to the submitter's own feedback or disclose current target data. A moderator can remove retained feedback after its submitter loses access.

Target reads can use tenant or exact-namespace `resource.read` assignments. Enforce same-tenant/resource/version relationships explicitly. Feedback permissions are tenant-only. Namespace workflow grants, publisher metadata and `createdBy` do not grant access to other users' feedback. Tenant Administrator includes moderation authority; a custom role can receive `feedback.moderate` explicitly. No namespace-specific moderation or inferred resource ownership is selected.

Submitting feedback does not require catalog-authoring authority. When scope enforcement is enabled, `feedback.write` is the OAuth ceiling for submitter commands. Submission, editing and withdrawal require current human membership and tenant `feedback.own` permission. Only creation additionally requires current target `resource.read` permission. Own-feedback reads use `catalog.read`.

Moderator inbox and management commands use `tenant.admin` plus `feedback.moderate`. An OAuth scope is a ceiling, not a Catalog permission grant. Do not require `catalog.write` through a generic mutation fallback.

A broad read scope cannot authorize management of somebody else's feedback. Minimal withdrawal/removal acknowledgements reveal no resource content.

### Resource and version association

Associate each record with one stable locally governed Resource and its tenant. Support all four governed kinds, including explicitly adopted resources. Unadopted indexed/live observations remain outside this feature: equal names or external identifiers do not merge feedback across catalogs.

For feedback from a published item page, capture the exact ListingRelease and its immutable VersionRevision. The API derives related identities from that release and checks any supplied relationship. Allow resource-level feedback without a version when no specific version is selected. A version-specific submission must retain an exact revision, not a floating version label. The UI supplies context automatically and labels general versus version-specific feedback. The target and context are immutable after submission; edits change text/category, not the subject.

Version publication, listing metadata edits, deprecation and retirement do not retarget or delete feedback. Archived context remains identifiable wherever existing access permits it. Feedback never republishes a snapshot, changes relevance scores, participates in approval or modifies sealed content. Listing and version history remain immutable. Mutable feedback is a separate record class; it does not permit evidence rewrites.

### User experience and lifecycle

Offer a category and bounded plain-text comment: problem, documentation correction, suggestion, positive feedback or other. The form states who can read the submission and warns against including credentials or sensitive logs. No attachments, HTML/Markdown rendering, automatic link previews or fetching of user-supplied URLs are required.

Provide an item-page form, the submitter's own submissions and a paginated moderator inbox. Filter the inbox by resource, category and state. The authorized Administration overview shows bounded feedback counts without comment text. Full-text indexing is not required. A moderator can mark feedback addressed and provide one current bounded response visible to the submitter. This is not a conversation thread or an assignment system.

The normal states are `open` and `addressed`. A submitter edit reopens addressed feedback and clears the old response. An earlier resolution cannot silently cover changed text. Moderators can reopen a record explicitly, clearing the previous response.

Withdrawal by the submitter and removal by a moderator are terminal states. Terminal records cannot accept new text or be restored; a new report is a new submission. Moderators may replace their response but cannot rewrite the submitter's comment.

Ordinary repeated reports are allowed within admission bounds; idempotency prevents retry duplication, not distinct reports about the same item.

### Text retention, audit and replay

Store only the current comment and current moderator response. Do not introduce immutable comment-edit history. Withdrawal or removal clears both text fields in the same transaction as the state change and minimal audit evidence. Retain only the reference/state metadata needed for integrity, audit and safe replay; avoid copying display names or private resource documents into tombstones. Addressed feedback remains readable until withdrawn or removed; there is no automatic age-based deletion in this first version.

Audit creation, edits, responses, state changes, withdrawal and removal using closed safe facts such as record ID, actor, state and revision. Do not put comment/response bodies, arbitrary excerpts or user-supplied URLs in audit reasons, logs, traces, metrics or notifications. Do not reuse generic before/after serialization that would capture those bodies. Feedback commands are audit-only: no outbox event, webhook, email or external ticket integration is selected.

Command success responses contain an acknowledgement, record reference and ETag, not comment or response bodies; clients retrieve current text through authorized GETs. This avoids retaining readable text in durable idempotency responses after withdrawal. Replay still reauthorizes, and an old acknowledgement is evidence of its original command, not the current feedback state. Private content responses use non-cacheable handling and the UI clears removed text from its state. Failed writes must not echo submitted text into persisted error responses.

Clearing live fields is not forensic erasure of database pages, backups, prior authorized downloads or browser screenshots. Existing backup and audit retention remain separate. This feature does not implement legal hold, privacy-request automation, a contractual deletion guarantee or a general retention service.

### Persistence, bounds and deployment

Keep feedback in its owning service/repository operations with an in-memory implementation for service-free tests and PostgreSQL for deployed state. Apply a forward-only migration, tenant-inclusive foreign keys and indexes, RLS, exact API privileges, legal state transitions and optimistic concurrency. Feedback text does not need worker access. Commands, minimal audit and replay commit atomically, with no external call in the transaction.

Closed request objects, text/body limits, bounded pages and process-local write admission keep feedback work bounded. The [feedback guide](../../guides/feedback.mdx#limits-and-retention) records the concrete limits shared by API, service and UI validation. No Redis or generalized quota system is needed. Process-local admission is not a global multi-replica quota or a storage cap. Any subsequently selected shared submission cap must be enforced transactionally in PostgreSQL. Page queries are bounded at selection, and ordinary Catalog search does not fetch feedback per result.

No new service, worker loop, image, provider, Helm dependency or mandatory external account is needed. Feedback has no enablement flag. OIDC deployments request the required scopes through the UI's existing scope configuration; the issuer must allow those scopes, and Catalog membership grants remain separately required.

## Alternatives and consequences

| Alternative | Tradeoff and disposition |
| --- | --- |
| External support links only | Already available and appropriate for existing issue systems. They remain supported, but do not provide retained local feedback/version context. |
| Authenticated ratings | Separate from private feedback. [ADR-0023](0023-human-resource-ratings.md) selects one-to-five-star opinions with independent identity, version totals, visibility and moderation. Ratings do not reuse feedback status or ARD relevance scores. |
| Public comments or discussion threads | Enable community exchange but create broader disclosure and moderation requirements. Not needed for private actionable feedback. |
| Automatic access for resource creators or publishers | Avoids administrator triage but assumes an ownership model that Catalog does not implement. Use explicit `feedback.moderate` authority. Namespace-specific delegation remains unselected. |
| External tickets, email or webhook delivery | Can connect to existing workflows, but adds disclosure, credentials, retry and reconciliation obligations. The inbox works without them. |
| Immutable feedback revisions | Preserve every edit but complicate withdrawal and retain potentially sensitive text unnecessarily. Keep current text plus minimal immutable audit instead. |

Moderator triage may become a bottleneck in a large deployment. Assign moderation authority explicitly; broad authoring roles do not grant private-feedback access. Private feedback does not help readers compare popularity and must not be presented as doing so. An accepted architecture does not establish interoperability for exporting feedback to another catalog.

## Verification boundary

Implementation acceptance requires unit and in-memory API tests, real PostgreSQL/RLS and concurrency tests, and browser tests of submission, own-history, administrator response and withdrawal. Cover anonymous/service-account denial, guessed IDs, cross-tenant and mismatched revision references, current permissions on replay, membership suspension/offboarding, archived resources, safe text rendering, stale edits and response races. Verify cleared text is absent from current reads, cached command responses and audit/log output. Confirm feedback activity leaves immutable publication bytes, ETags and ordinary search behavior unchanged. Backup/restore coverage must include the new record class without changing existing recovery responsibilities.

## Standards and comparison evidence

The pinned discovery profiles do not define a feedback-management protocol. Feedback remains a private Catalog Control API feature.

- [Pinned ARD specification](https://github.com/ards-project/ard-spec/blob/aa3e598bb7752a9175897823234311216acfa864/spec/ard.md): extensible discovery entries and relevance-only Search scores.
- [Pinned AI Catalog extensions](https://github.com/Agent-Card/ai-catalog/blob/b062278fd7f5a83202d33208f1d07c5b5eb7ccb3/specification/ai-catalog.md#extensions): namespaced custom metadata, not an interoperable comment/moderation protocol.
- [HF Discover source](https://github.com/huggingface/hf-discover/blob/49c927439fcaa8f210cfd42186c0641acef579fa/src/discover/server.py): reads source-provided HF Space likes; this is distinct from a local feedback-submission API.
- [Backstage entity feedback](https://github.com/backstage/community-plugins/blob/eb016447e42bb3f5571e54a4956ef23229604151/workspaces/entity-feedback/plugins/entity-feedback/README.md): authenticated ratings and feedback demonstrate separate satisfaction and response concepts. Its ownership and notification model is not copied.
- [Hugging Face discussions](https://huggingface.co/docs/hub/repositories-pull-requests-discussions): a broader repository collaboration model, not an ARD requirement.
- [OWASP authorization guidance](https://cheatsheetseries.owasp.org/cheatsheets/Authorization_Cheat_Sheet.html) and [logging guidance](https://cheatsheetseries.owasp.org/cheatsheets/Logging_Cheat_Sheet.html): explicit per-request authorization and exclusion of sensitive content from operational logs.
