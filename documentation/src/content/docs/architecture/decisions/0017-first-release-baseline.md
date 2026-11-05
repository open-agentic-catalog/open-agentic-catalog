---
title: "ADR-0017: Current-format first release without development-version compatibility"
---

- Status: Accepted

## Context

The Catalog has no supported released predecessor. Supporting older development formats would add runtime, schema and verification costs without a compatibility requirement.

## Decision

The first supported release uses the reviewed current contracts only. Do not ship compatibility with unreleased Catalog binaries, databases, object layouts, Job payloads, audit records or operator formats. Packaging and compatibility tests need no hypothetical predecessor, obsolete-storage inventory or backfill system.

Preserve current domain history, security and recovery behavior and explicitly selected upstream standards interoperability. Memory development mode remains supported.

Future upgrade and path-major API compatibility obligations start with actual supported releases. Historical development commits are not supported predecessors. Pre-release contract cleanup updates code, OpenAPI, examples and tests together without aliases for old clients.

Resetting development data requires explicit authorization for the exact environment. Incompatible existing state must fail clearly rather than be silently converted or destroyed. Retain applied migration checksums and use forward-only schema cleanup under the current repository rules, without a mixed-version support window or compatibility backfill framework.

## Alternatives and consequences

- Retaining a bridge until an inventory proves that no development data exists was rejected: data safety is an operator concern, not a reason to ship support for an unreleased format.
- A general migration/export/backfill system for older development builds was rejected as unnecessary scope.
- Cleanup can break unreleased development clients/configurations; migrate maintained fixtures and examples in the same change and document reset/refusal behavior. Current-only tests replace obsolete acceptance tests while retaining adversarial coverage.

See [migration requirements](../../operations/database-roles.md#migration-drain-and-provisioning-requirements) for schema changes and deployment sequencing.
