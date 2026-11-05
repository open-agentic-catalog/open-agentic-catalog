---
name: security-review
description: Review Open Agentic Catalog trust boundaries and remediate demonstrated defects. Use for requested security reviews or substantive authentication, authorization, tenant, outbound-fetch, parser, archive, signature or secret-handling changes; routine scanner runs use post-code verification.
---

# Security review

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md`, the relevant security architecture (`documentation/src/content/docs/architecture/security.mdx`) and the maintained threat/control/test mapping (`documentation/src/content/docs/operations/security.md#security-controls-and-regression-coverage`). Check implementation status (`CONTEXT.md`) before treating a designed control as implemented. Preserve review-only and unit-only limits; a review does not authorize a deployment change or tests against third-party/shared systems.

## Trace the affected boundary

Identify the attacker-controlled input, required authority, protected data or operation, and the path to a consequential use. Inspect actual callers, validation order, failure handling and existing negative assertions. Follow only affected boundaries:

- For identity and commands, separate token verification, active Catalog membership, current action/scope permission checks, namespace authority and tenant isolation. Actor role arrays and browser hints never grant authority. For policy changes, check direct/group paths, current replay/continuation, protected final administration and changes observed through peer API processes; known policy-store outages must not become invalid-token rejection. Check stale revisions and failure atomicity; provider groups do not create Catalog assignments. Use database changes (`.agents/skills/database-changes/SKILL.md`) when an authorized fix changes SQL, RLS or grants.
- For outbound requests, trace URL normalization, DNS/IP checks, redirects, credential origin binding, deadlines and response bounds through the shared fetcher. Operator-configured OIDC verification, the UI's fixed API/issuer transports and the remote CLI's origin-bound client have different contracts; do not replace them with metadata fetching or allow caller-selected destinations.
- For parsers, archives, signatures and cursors, establish the owning format, canonicalization and resource limits before constructing rejection cases. Preserve non-execution of Skill/Plugin content and distinguish structural validity, integrity and verified trust. Inspect error, log, metric and response projections for private evidence or secrets as well as the successful result.

Use the maintained mapping to find existing regression owners. A high coverage percentage, scanner pass or validation label does not prove a boundary safe. Do not create a second threat inventory or infer a vulnerability solely from a suspicious helper without tracing a reachable use.

## Reproduce and fix within scope

For each demonstrated finding, state the source location, realistic preconditions, input/path, observable impact and bounded reproduction. Separate confirmed defects from unresolved concerns and describe the reviewed scope when no issue is found. Use synthetic secrets and owned fixtures; retain enough redacted evidence to reproduce without exposing credentials or private records.

When implementation is authorized, make the smallest correction in the owning boundary and extend existing acceptance cases. Check rejection before forbidden contact or mutation, safe public errors and retained state/evidence where relevant. Use unit testing (`.agents/skills/unit-testing/SKILL.md`) for injected boundaries and functional testing (`.agents/skills/functional-testing/SKILL.md`) for real TLS, redirects, processes or PostgreSQL. Fake SQL and controlled transports do not qualify real database or network behavior.

## Verify and report

Use post-code verification (`.agents/skills/post-code-verification/SKILL.md`) for shared gates. Security-sensitive changes require `make security` plus focused adversarial tests; affected parser boundaries use `make test-fuzz` or their focused fuzz target under AGENTS.md. Dependency and image findings follow the applicable dependency procedures (`documentation/src/content/docs/reference/dependencies.md`) and build security checks (`documentation/src/content/docs/operations/security.md#build-security-checks`); a root scan does not replace UI full-lockfile checks. Record the tested source, advisory freshness, meaningful skips and unresolved findings without claiming penetration-test or deployment certification.

Handle suspected vulnerabilities through `SECURITY.md` and the incident runbook (`documentation/src/content/docs/operations/incident-response.md`). Prepare vulnerability reports locally. The maintainer submits external reports. Keep reporting private and retain evidence safely; do not send notifications, rotate credentials or change retained state without the applicable authorization. Update the maintained control/test mapping when the implemented protection changes.

The ZAP harness (`test/security/README.md`) supplies explicitly selected passive API/UI and active search/browse checks through `test-security-passive` and `test-security-active`. Preserve its owned internal network, exact rule/path dispositions, vulnerable/fixed calibrations and credential-free reports. Confirm findings independently; do not weaken product controls or broadly suppress rules to make the scanner pass.
