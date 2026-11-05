---
name: federation-development
description: Review and change Open Agentic Catalog source onboarding, indexed synchronization, provenance and bounded live/referral discovery. Use for federation lifecycle or cross-Catalog behavior; format-only interpretation, ordinary local search and client discovery use their existing workflows.
---

# Federation development

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md`, the federation owner (`internal/federation/README.md`), relevant architecture (`documentation/src/content/docs/architecture/federation.mdx`) and implementation status (`CONTEXT.md`). Preserve plan-only, review-only and unit-only scope. Identify the affected source type, execution mode and actual administrative/query contract before editing.

## Follow source authority and participation

Trace inspection, approval/connection, revision, preview, pause/removal and indexed/live/referral participation through current callers and tests. Source Administrators choose within the deployment's egress policy; ordinary callers cannot submit URLs or select a topology. Query disclosure, audiences and private routes remain explicitly governed. Observations retain external provenance until explicit authorized adoption; synchronization does not publish locally governed resources.

Use standards conformance (`.agents/skills/standards-conformance/SKILL.md`) to establish an affected wire requirement from the pinned profile. Endpoint, profile and Registry-interface evidence remain bound to inspection/approval. Contradictory discovery signals fail closed; an approved source changing its resolved interface requires reinspection. Do not treat a reference implementation or current upstream branch as normative authority.

Keep mutating source operations, previews and consequences aligned with API contracts (`.agents/skills/api-contract-changes/SKILL.md`). Removal/update previews must bind the current affected observations according to their contract; source revision alone does not detect a changed synchronized population. Preserve current authorization, idempotency, stale-preview rejection and action-specific audit/outbox classification. An outbound diagnostic can create evidence and is not automatically a read-only health check.

## Preserve transport and retrieval evidence

All resource/source/context retrieval goes through the hardened fetch boundary (`internal/platform/fetcher/README.md`). Preserve URL/DNS/IP, redirect, origin-bound credentials, private trust, byte/request/depth/time bounds and cancellation. A failed or unauthorized attempt must not leak tokens, topology or raw response bodies. Use security review (`.agents/skills/security-review/SKILL.md`) for substantive trust-boundary changes.

Trace conditional state through retrieval handling (`internal/federation/sync_retrieval.go`), not just an ETag comparison. Reuse depends on retained bytes and matching source/profile, policy, credential revision and request identity. An accepted `304` creates new attempt evidence; an unchanged parent must not suppress visiting changing children or contexts. Preserve rejected-validator recovery, aggregate work limits and safe provenance. Multi-page Registry List cursors remain request-local rather than becoming a persisted incremental cursor chain.

## Preserve complete and partial outcomes

Follow the whole synchronization Job into snapshot commit and observation changes. Incomplete, failed, cancelled or stale-revision work must not replace prior complete membership. Retain truthful attempt diagnostics and the last usable snapshot. Withdrawal authority differs by source: a complete authoritative manifest traversal can justify withdrawal, while a complete non-authoritative Registry List updates its current projection and marks unseen prior evidence stale. Search-only Registries provide no enumerable inventory for indexed synchronization.

For live discovery, preserve current eligible topology, one end-to-end budget, origin/fan-out/hop/result bounds and loop detection. One source failure does not erase completed local results. Keep safe partial reporting and valid-empty success distinct. Core live continuation replays an unchanged window of at most 50 candidates with fixed expiry and current access evidence. Changed verified evidence requires restart; temporary replay failure must permit retrying the same token. Ordinary durable continuation makes no peer calls. Hybrid pages preserve exact durable order before a distinct live tail. Live-only paging makes no durable or embedding calls. Use search and relevance (`.agents/skills/search-and-relevance/SKILL.md`) only when result composition or ranking changes.

Scheduled synchronization uses existing durable workflows (`.agents/skills/durable-workflows/SKILL.md`); SQL snapshot/evidence changes use database changes (`.agents/skills/database-changes/SKILL.md`). Do not refetch and overwrite immutable provenance to repair a missing retained document or turn remote availability into Catalog record authority.

## Verify the affected lifecycle

Reuse isolated manager/parser cases for decisions, owned HTTPS fixtures for routing/redirects/conditional retrieval, and real PostgreSQL tests for atomic snapshot replacement, tenant scope and retained evidence. Extend existing failure/recovery assertions instead of duplicating entire ingestion journeys. Classify tests with unit testing (`.agents/skills/unit-testing/SKILL.md`) and functional testing (`.agents/skills/functional-testing/SKILL.md`); fake fetchers do not prove TLS or SQL behavior.

Use post-code verification (`.agents/skills/post-code-verification/SKILL.md`) for shared gates. `make test-functional-default-off` covers packaged Registry synchronization; `make test-federation-peers` qualifies two owned independent Catalogs. Select these for their affected workflow, not every parser edit. Discovery-output changes also use the standards skill's conformance checks, including schema execution evidence.

External implementation execution remains a separately selected scope under interoperability qualification (`test/interoperability/README.md`). When local HF peer qualification is explicitly selected, use `make test-interoperability-hf`: verify real API approval and worker snapshots, exact private trust/audiences, approved-only referrals, partial local-result preservation and same-Job recovery. Keep peer limitations and native conformance failures visible even when characterization passes. A federation change alone does not authorize hosted Registry calls or executing another peer. Use local static fixtures for researched wire cases and `gh` for GitHub retrieval. Report source/profile/build identity, preserved complete/partial behavior, actual assertions/gates, cleanup and unrun boundaries; unit-only scope leaves real transport, database and peer qualification unrun.
