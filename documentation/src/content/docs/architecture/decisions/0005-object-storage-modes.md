---
title: "ADR-0005: Catalog content, external artifacts, and optional media storage"
---

- Status: Accepted

## Question

Which bytes belong in PostgreSQL, Git, an OCI registry, or S3-compatible storage, and does a usable Catalog require filesystem or S3 storage?

## Context

The Catalog needs exact metadata and standards documents, but it must not become another source-code repository, runtime registry or generic artifact store. Agent and deployable MCP Server runtimes belong in OCI registries. Source belongs in Git. A hosted MCP Server may expose an HTTPS endpoint. Full SBOMs and attestations remain beside the artifact or in their owning evidence system. A reference in Catalog metadata does not establish a verified software assessment.

Agent Skills need a more precise boundary. The Agent Skills specification defines a directory containing `SKILL.md` and optional `scripts/`, `references/`, `assets/`, and other files. It does not define one mandatory distribution service. A single-file Skill can be retained as a bounded document in PostgreSQL. A complete Skill release is referenced through a Git commit and directory or an immutable OCI artifact; files inside the Skill, including its own images and templates, travel with that release.

Catalog listing icons and screenshots are different. They are presentation media, not executable Resources, and may be hosted through an optional S3-compatible media service and CDN. The Catalog remains fully usable without managed media by using approved external HTTPS references or default artwork.

Deployed Catalogs retain bounded document bytes in PostgreSQL. Inspected Agent Skill packages retain inventory, assessment state and immutable Git or OCI handoff there; archive bytes are transient. No filesystem or S3 document store is supported.

## Decision

PostgreSQL 18 is the only mandatory storage dependency for the core Catalog. Store catalog metadata, normalized facts, bounded exact standards documents, retained source evidence, publication state, digests, and lifecycle records there.

Use authoritative external locations for executable or installable artifacts:

| Content | Authoritative location | Catalog record |
| --- | --- | --- |
| Agent runtime | OCI registry, pinned by digest | Image reference, digest, platforms, and deployment metadata. |
| Deployable MCP Server runtime | OCI registry, pinned by digest | Image reference, digest, transports, and configuration metadata. |
| Hosted MCP Server | HTTPS endpoint | Endpoint and validated Server Card or registration metadata. |
| Source code | Git | Repository, immutable commit, and optional subdirectory. |
| Agent or MCP standards document | PostgreSQL | Exact bounded document, normalized fields, media type, size, and digest. |
| Single-file Agent Skill | PostgreSQL or immutable Git reference | Exact `SKILL.md`, parsed metadata, source, and digest. |
| Complete Agent Skill | Git commit plus directory, OCI artifact pinned by digest, or both | Parsed metadata, file inventory and digests, source reference, and release reference. |
| Complete Agent Plugin | Credential-free external HTTPS ZIP or gzipped-tar archive | Archive digest/size and immutable bounded manifest/component documents, file inventory and retrieval evidence in PostgreSQL; archive bytes are transient. See [native Plugin packages](0019-native-agent-plugin-packages.md). |
| SBOM, provenance, or attestation | OCI referrer or owning evidence system; bounded documents may be retained in PostgreSQL | Explicit reference, digest or retained document; no implemented scanner assessment. |
| Catalog icon or screenshot | Approved external HTTPS location or optional S3-compatible media service | URL, digest, media type, dimensions, and accessible description. |

Do not mirror external runtimes, repositories, Skill releases, SBOM authorities, or attestations merely to publish a Catalog entry. ARD and AI Catalog output advertise the approved representation and its delivery reference.

S3-compatible storage is a future optional managed-media capability, not a core dependency. No managed-media storage adapter is implemented, and enabling the flag currently fails startup. A future implementation must use one provider-neutral adapter for qualified S3-compatible services. Integration qualification uses an operator-selected disposable endpoint whose implementation and license are reviewed separately; the repository does not bundle an S3 server. The default Compose workflow and maintained Minikube profiles do not need an S3 service.

## External listing artwork

Local authors store a versioned artwork descriptor in the immutable ListingRevision. Review and release select its icon and ordered screenshots. The descriptor contains accessible text, declared rights, attribution and validated image facts. PNG and JPEG bytes remain at operator-approved HTTPS origins. The Catalog validates and forwards them transiently; it does not cache, transform or store images.

Consuming tenants can replace, suppress or inherit foreign artwork. An audited override has immutable revisions and names the source, approved interface generation and exact resource identity. It does not edit an observation, adopt a resource or appear in a public snapshot. Source administrators control overrides; current discovery permissions govern delivery.

An optional namespaced ARD JSON literal and AI Catalog extension carry publisher artwork. Source artwork defaults off. Receiving a descriptor never approves its origin or establishes publisher trust. The hardened outbound boundary applies exact media-origin bindings and existing tenant-assigned private routes. Image requests carry no source credentials, cookies or client certificates. Redirects and automatic retries are prohibited.

Sealed image descriptors bind exact bytes. Changed bytes require another reviewed descriptor. Native A2A and MCP icon references are mutable display hints, subject to the same delivery limits. Live results use short-lived, tenant/principal-bound signed receipts. Current source, permission, policy and override changes invalidate those receipts.

A missing or rejected image leaves the kind glyph and usable resource metadata. Empty origin configuration denies image fetching. This feature does not enable managed-media storage or require S3. See the [configuration reference](../../reference/configuration.mdx#listing-artwork) and [authoring guide](../../guides/resource-authoring.mdx#icons-and-screenshots).

## Agent Skill package and scanning policy

ZIP and GZIP Agent Skill representations are supported and are not blocked merely because malware scanning is disabled or unavailable. Structural validation is implemented; scanner integrations and policy-required passed assessments are not. The assessment rules below define the optional target, not an available scanning service.

- Structural package validation is mandatory whenever the service inspects or extracts a package: bound compressed and expanded sizes, file count, depth and ratio; normalize paths; reject traversal, absolute paths, duplicate normalized paths, special files, unsafe links, and missing or invalid root `SKILL.md`; and never execute package content.
- Malware, secret, license, SBOM, and provenance scanners are optional evidence providers. Their state is reported honestly as `not_assessed`, `passed`, `failed`, or `unavailable`.
- `not_assessed` and `unavailable` do not block Catalog publication by default. Installation decisions belong to clients, not the Catalog. A tenant or deployment policy may require a passed assessment for its own workflow.
- A confirmed blocking finding from an enabled scanner blocks use under the policy that requested the scan.
- Catalog validation, optional security assessment, and client execution permission remain separate decisions.

This policy applies to the recognized Agent Skills package formats. It does not turn the Catalog into a general-purpose package-upload service or authorize arbitrary binary execution.

## Why this matters

- PostgreSQL makes bounded Catalog documents transactional with their revisions, audit, publication, and backup state.
- Git and OCI retain the authoring and immutable-release behavior their ecosystems already provide.
- Agent Skills remain complete directories rather than being reduced incorrectly to one Markdown document.
- Multi-replica Catalog operation does not require a shared blob service when externally owned artifacts are referenced rather than mirrored.
- Optional managed media supports listing icons and screenshots without making S3 a core dependency.
- Separating structural validation from optional scanning preserves interoperability without making unsupported security claims.

## Consequences

- The PostgreSQL model has bounded exact-document storage, exact anonymous HTTPS retrieval evidence and retained validation bytes, plus tenant-scoped Agent Skill package inventory and immutable Git or OCI handoff. Optional managed media remains disabled until deliberately implemented and qualified.
- Core production configuration does not require S3. Retained development data must not be reset automatically.
- The Agent Skills model has source and release references, full directory inventory, package-format validation, and authenticated client handoff metadata. Optional assessment providers and policy-required passed states remain tracked work; a separately submitted approved HTTPS representation can provide public delivery without turning transient package bytes into Catalog authority.
- Managed listing-media uploads require a separate optional capability, size and type limits, safe image handling, lifecycle, and provider qualification.
- Managed media has no adapter or placeholder storage SDK. PostgreSQL remains the deployed document authority.
- Backup and restore for the core service centers on PostgreSQL; an enabled media backend has its own provider-owned recovery contract.

Applied migration files retain their checksums. Storage preserves exact tenant/document foreign keys, immutable history and forced RLS. The [current-format compatibility boundary](0017-first-release-baseline.md) requires forward-only schema changes and refuses unsupported state without conversion or deletion.

The inspected Skill shape remains supported without retained archive bytes. Its structural summary, independent assessment states, exact Git or OCI handoff and sorted inventory commit transactionally. Tenant-inclusive foreign keys remain required. Reject obsolete package or evidence formats; do not fabricate missing evidence from unsupported rows.

## Verification

- Core API, worker, publication, federation, and Agent Skills Markdown workflows pass with PostgreSQL and no S3 process.
- A complete single-file Skill is retained and served transactionally from PostgreSQL.
- Git-referenced and OCI-referenced complete Skill fixtures preserve exact commit or digest, directory path, assessment state, and inventory in authenticated Catalog records; package-only public snapshot candidates record an explicit missing-delivery exclusion.
- ZIP and GZIP fixtures prove bounded structural handling, pre-allocation entry-count defense, traversal and expansion rejection, no execution or archive retention, complete file digests, and honest optional assessment state. Enabled scanner and policy-required passed-state behavior remains future acceptance evidence.
- Future managed-media acceptance must test selected S3-compatible providers without changing the core Catalog contract.
- Backup and restore prove that all required core Catalog content is recoverable from PostgreSQL and that externally referenced artifacts retain immutable locators.
