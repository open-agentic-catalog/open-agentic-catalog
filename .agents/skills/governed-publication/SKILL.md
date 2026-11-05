---
name: governed-publication
description: Review or change Open Agentic Catalog authoring, validation evidence, review, releases and immutable discovery publication. Use for eligibility, lifecycle, activation, rollback or safety withdrawal; search ranking and external source synchronization use their owning skills.
---

# Governed publication

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md`, the Catalog domain (`internal/catalog/README.md`), publication owner (`internal/publication/README.md`) and applicable publication architecture (`documentation/src/content/docs/architecture/publication.mdx`). Check implementation status (`CONTEXT.md`) and the actual operation before changing a transition. Preserve plan-only, review-only and unit-only scope.

## Bind the exact subject

Trace the Resource, immutable VersionRevision and ListingRevision, validation run, ReviewSubmission, ListingRelease and discovery snapshot involved. Follow current pointers and retained IDs/digests independently; reading the latest draft or validation is not evidence for an earlier sealed submission. Inspect authorization, namespace/tenant predicates, ETags and command replay at each affected transition. Current authorization applies to replays; even Tenant Administrator cannot approve their own submitted work.

Keep authoring, validation, independent review, release publication, candidate construction and public activation distinct. A release does not establish an active public snapshot, and a new draft or later release does not rewrite an active bundle. Imported observations become governed resources only through explicit authorized adoption. Structural validity, publisher-domain approval, trust, legal rights and runtime invocation authority remain separate outcomes.

For representation changes, use standards conformance (`.agents/skills/standards-conformance/SKILL.md`) to establish the pinned requirement and follow the existing adapter/evidence boundary. Preserve all supported resource kinds without inventing an ARD Plugin mapping. Complete package trees and external archive delivery have different retention contracts; immutable evidence cannot be reconstructed from a later fetch.

## Preserve eligibility and atomic consequences

Construction, activation and rollback share current bounded eligibility checks. Follow the database selection bound, retained document integrity, exact host/audience/profile/generation and membership/exclusions through each phase. Candidate failure leaves existing public state intact. Workers may generate immutable candidates; API authority owns active-pointer transitions. Public reads serve exact active snapshots and their document allowlist rather than draft tables.

Inspect current release eligibility and publisher-domain approval before rollback; retained history is not unconditional authority to reactivate. Safety withdrawal for domain revocation, release transitions or explicitly blocked external revalidation must follow the owning atomic command and preserve history. External-evidence expiry or policy drift has its documented stale-readiness behavior; do not equate it with explicit blocked revalidation or silently rewrite retained snapshots.

Keep the transition, required audit, independently classified outbox records and replay result atomic. Preserve evidence attribution and emit withdrawal consequences only for affected active pointers. External retrieval uses the hardened fetcher outside interactive transactions. Use database changes (`.agents/skills/database-changes/SKILL.md`) for persistence/guards/grants, API contract changes (`.agents/skills/api-contract-changes/SKILL.md`) for wire semantics, and durable workflows (`.agents/skills/durable-workflows/SKILL.md`) for generation Jobs rather than introduce another publication orchestrator.

## Verify evidence and public behavior

Extend existing cases with a valid eligible control and the missing rejection or state change. Useful assertions include sealed-subject identity, no self-approval, stale revision/evidence rejection, replacement eligibility, failed candidate preservation, current eligibility on rollback, exact safety withdrawal, and cross-tenant or audience denial. Assert retained state and forbidden mutations alongside errors. Use unit testing (`.agents/skills/unit-testing/SKILL.md`) for isolated decisions; composed workflows, real retrieval and SQL remain functional (`.agents/skills/functional-testing/SKILL.md`).

Select gates through post-code verification (`.agents/skills/post-code-verification/SKILL.md`): existing `make test-e2e` qualifies composed governance, SQL changes need real PostgreSQL evidence, and HTTP/lifecycle changes need the affected disposable packaged workflow. Discovery-output changes add the applicable pinned conformance modes. Run `make test-federation-peers` when the changed behavior affects cross-Catalog transfer or withdrawal, not every local eligibility edit. Preserve privacy, snapshot identity, cleanup and unrun scope; reconcile the owning architecture, workflow guide and implementation status only where behavior changes.
