---
title: "ADR-0023: Human resource ratings"
---

- Status: Accepted

## Context

Readers need version-specific opinions without confusing them with governance approval or search relevance. External resources also need source-specific identity. Existing private feedback has a different purpose and remains unchanged.

## Decision

Use the existing Go API, PostgreSQL, authentication, authorization and UI. Add no service, collector, installation telemetry or review platform. The [ratings guide](../../guides/ratings.mdx) defines the user workflow.

A local rating target consists of a tenant, a Resource and an immutable VersionRevision. A foreign target is tenant, Source, presentation generation, exact AIR, advertised version presence/value and kind. Entry digests provide attribution without silently changing the target. Source replacement cannot inherit earlier authority's votes.

Derive the private principal key from verified tenant, issuer and subject. Membership recreation preserves uniqueness. Only authenticated humans with current target visibility and `ratings.own` may submit. Namespace grants apply to local resources; foreign discovery uses tenant permission. An OAuth `ratings.write` ceiling narrows these permissions.

Own withdrawal requires current human identity and ownership, including after target visibility or rating permission is lost. Moderation requires tenant `ratings.moderate`; audience sharing requires `ratings.sharing.manage`. Both use the `tenant.admin` OAuth ceiling. Role names and browser hints are not authority.

Store one current score from one to five. Withdrawal/removal clears the score and consent. Removal blocks fresh submission until an administrator permits it. Retain a tombstone and minimal immutable score/state history for uniqueness and integrity. Do not expose private principal hashes or create a public rater list.

Commands atomically commit current state, trigger-maintained count/sum totals, minimal history, audit and durable replay. Commands are audit-only and create no notification outbox rows. Audit facts contain revision/state or enabled state, not scores, consent or user text. Moderation and sharing retain a bounded administrator reason.

Use tenant RLS, same-tenant relationships and exact API/worker privilege manifests. Only the API reads individual opinions or changes them. The worker reads aggregate targets and sharing choices for publication. Trigger-owned counters and immutable history cannot be directly changed by runtime roles.

Add rating data only to final discovery pages the caller is already authorized to read. Use bounded batch queries. Do not alter ranking, selection or cursor identity. Signed rating proofs for live results use a separate HMAC purpose and expire after five minutes. They bind the current identity, source, policy and credentials. No network request occurs during an opinion write.

## Optional sharing

Require deployment enablement, audited tenant/audience opt-in and individual consent. Defaults disable sharing and suppress groups below five consented opinions. Only locally governed released versions qualify.

Publish a closed, bounded aggregate using the Catalog-owned extension IRI `https://github.com/open-agentic-catalog/open-agentic-catalog/schemas/ratings/v1#summary`. ARD uses an inline JSON-LD `@json` term; AI Catalog uses its extension map. This is not a new upstream standard or rating-submission protocol.

The aggregate binds reporter origin, AIR, advertised version, scale, count/sum and UTC measurement time. It contains no individual identity. Malformed optional summaries are omitted without rejecting the enclosing peer resource. Unknown extension versions remain evidence but are not rendered as supported ratings.

Normal immutable publication captures a summary. Generation, activation and rollback compare current consented totals and audience choice. Activation holds PostgreSQL sharing and aggregate locks through commit. Published copies remain immutable after later withdrawal; normal replacement or safety withdrawal controls local serving.

## Adoption signals and limits

Show an authorized **Imported here** link from exact retained provenance. Never infer installation, execution or browser file saves. Foreign totals and peer claims remain separate. No comments, weighted scores, popularity ranking, identity linking or automatic publication are selected.

Private histories remain in ordinary backups. Clearing a current score is not forensic erasure or a contractual deletion guarantee. Account uniqueness does not prove one natural person. Source metadata can change under an advertised version.

## Verification

Use isolated target, arithmetic, lifecycle, receipt and projection tests. Real PostgreSQL tests exercise two APIs, concurrent writers, exact grants, RLS and durable replay. Browser tests exercise human controls and moderator actions. Publication and peer tests verify consent suppression, source-separated claims and immutable snapshots. Keep these results separate from packaged qualification and source-coverage measurements.
