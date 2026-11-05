---
title: "ADR-0019: Native Agent Plugin packages"
---

- Status: Accepted

## Context

A Plugin groups portable Skills and MCP configuration into one versioned directory. Catalog must inspect, govern and discover the complete package without executing its components or becoming an artifact host. Existing text-document retention and Skill-only external handoff do not supply that workflow.

## Decision

Support Agent Plugins 1.0.0 as one governed Plugin Resource. Ingest a complete ZIP or single-member gzipped tar archive from an anonymous HTTPS URL through the hardened fetcher, with same-origin redirects only. Compute SHA-256 over the original archive bytes using identity HTTP content encoding and compare an optional expected digest. Publish the complete archive URL and its digest using the existing discovery metadata projection.

Retain exact bounded manifest and component documents in PostgreSQL. Also retain deterministic inventory, archive identity and retrieval evidence. Archive bytes are transient; do not host or retain them. This is a Plugin-specific exception to retaining the complete representation document, not permission for arbitrary URL-only representations.

Revalidation and publication verify the bound archive identity, existing retrieval freshness and current fetch policy. Changed content requires a new reviewed revision under an unsealed version. Once approval seals a version label, replacement content requires a new version label. Neither path rewrites sealed evidence.

Use pinned schemas plus semantic checks, preserving the distinction between format findings and Catalog admission. Unsafe or unbounded archives fail before persistence. Bounded invalid components block publication with individual findings. No partial-package approval is offered. Client-specific extension contents remain inert. Catalog does not implement runtime loading, installation, MCP authentication or invocation.

## Alternatives and consequences

Hosting archives would preserve availability independently of the publisher but add binary storage, retention and download responsibilities. It is not selected. A manifest-only URL cannot deliver the components. Git checkout or OCI pulling would introduce separate retrieval and credential contracts and is not required for HTTPS archive acquisition.

Publishers must maintain accessible immutable archive URLs. Catalog can detect changed or unavailable content during verification, but cannot restore a removed external archive. A database restore retains inspection evidence without a local archive cache. Downstream clients remain responsible for checksum verification and their own runtime authorization.

## Development fixture exception

The selected local demo may enable the narrowly scoped `demoArchives` transport only in the development environment with development authentication. It admits one operator-selected literal private/loopback HTTPS origin. It requires mounted private trust, the fixed development tenant and a prepared allowlist of exact archive paths, digests and sizes. The allowlist holds at most 64 ZIPs, each at most 64 KiB.

Fetching still uses the hardened boundary with one pinned address, anonymous GET, no redirects and bounded bytes. Production and OIDC startup reject the transport. The application Helm chart permits only its disabled value. Generic Plugin representations continue to use public routing without private-profile or credential authority.

This synthetic local facility supports verification and development. It is not a Catalog archive host, installer or private artifact-provider feature. Its independent fixture tool serves validated prepared archive bytes and never executes components. Ordinary governance and retained inspection evidence remain required.
