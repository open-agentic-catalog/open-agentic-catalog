---
name: ui-development
description: Implement Open Agentic Catalog React interactions and Go UI-host session or forwarding behavior. Use for views, forms, navigation, accessibility, browser state or host changes; dependency-only updates and test-only runs use their existing workflows.
---

# UI development

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md`, the relevant UI architecture (`documentation/src/content/docs/architecture/catalog-ui.mdx`) and UI guide (`catalog-ui/README.md`). Preserve plan-only, review-only and unit-only scope. The optional UI has its own build and host; backend development does not require its toolchain.

## Follow the interaction and its authority

Establish the user's task, actual backend capability, current screen/route and relevant workflow coverage (`catalog-ui/tests/README.md#workflow-matrix`). Trace the existing view, shared controls, typed client and server contract before adding code. Display actual results and capability states; discovery and validation do not imply trust, installation or execution. Backend authentication, tenant/permission/scope checks and lifecycle rules remain authoritative after every UI permission hint.

Read only the affected owners: screens and shared controls (`catalog-ui/src/views/README.md`), session/navigation shell (`catalog-ui/src/shell/README.md`), API client (`catalog-ui/src/api/README.md`) or Go host (`catalog-ui/internal/web/README.md`). A host-only change need not redesign a screen. Use API contract changes (`.agents/skills/api-contract-changes/SKILL.md`) for a changed backend contract; do not implement missing domain behavior in the browser or host.

## Reuse the existing client and host boundaries

- Keep workflow-specific queries and validation in the owning screen. Reuse workspace context, authoring controls, semantic styles and inline icons. Preserve lazy routes and bounded in-memory pages instead of adding a UI framework, generic CRUD layer or full-catalog cache.
- Regenerate API declarations through the existing UI package command after an OpenAPI change; do not edit generated types. Type generation is not runtime validation. Keep exact retained bytes and lossless raw-evidence paths, including authenticated downloads and their digest checks.
- Keep the host-selected manual destination separate from resource metadata links: only the exact `/docs/` fallback or a safe HTTPS override is valid. Preserve one Help link, accessible new-tab behavior and independent API reference. Ordinary source fixtures need no manual build; packaged Compose/OIDC gates own actual-manual qualification.
- Extend the existing session and consumed-response predicates before new external values drive rendering, navigation or commands. Check nested consumed fields and documented success/partial-result discriminators; retain useful results with valid partial-failure diagnostics. Keep opaque evidence and raw downloads lossless rather than introducing another whole-schema validator. A malformed successful write response leaves an uncertain outcome, not proof that the write failed.
- Capture a command's target, body and ETag before confirmation. Explicit retries after uncertain outcomes reuse that input and idempotency key; the command helper does not freeze mutable form inputs. Do not introduce automatic mutation retries. Stale revisions require recovery from current state without silently overwriting edits.
- Preserve identity invalidation and discard stale asynchronous responses. Private drafts and one-time secrets stay in memory; browser storage holds only non-sensitive appearance preferences. Fetch private evidence on demand, not for every list row.
- In the host, keep encrypted sessions, absolute expiry, current backend access checks, OIDC state/PKCE and cookie/origin controls intact. API and issuer transports have fixed destinations, separate trust, byte/deadline bounds and body cleanup. Browser requests cannot supply upstream URLs or bearer overrides; forwarding stays within the embedded product contract. The host has no database or worker authority. Use security review (`.agents/skills/security-review/SKILL.md`) for substantive changes to these controls.

## Complete and verify the affected behavior

For changed interactions, cover initial/loading, empty, failed and successful reads, permission/session loss and relevant conflict/uncertain-command recovery. Preserve unsaved-input handling, explicit refresh and bounded paging. Check keyboard operation, labels, visible focus, focus restoration and responsive light/dark appearance using the existing native controls and styles. Scale cases to the changed interaction rather than repeating every browser journey in a unit test.

Let ordinary UI prose use the available layout width and wrap naturally. Source formatting must not impose presentation breaks. Avoid arbitrary paragraph caps and manual breaks; preserve usable container bounds, supplied paragraphs and data/code formatting. For layout regressions, compare rendered text with available container space in the real browser, including narrow and enlarged-text cases. Documentation-site reading measures are separate.

Use unit testing (`.agents/skills/unit-testing/SKILL.md`) for isolated client/component/host assertions and functional testing (`.agents/skills/functional-testing/SKILL.md`) for actual browser, API or issuer exchanges. Reuse the maintained browser harness (`catalog-ui/tests/browser/README.md`) and workflow matrix to avoid duplicating lower-layer assertions. Generic Playwright tooling is optional; it does not replace the repository fixtures or require another dependency.

Select final checks through post-code verification (`.agents/skills/post-code-verification/SKILL.md`). `make test-ui` already includes lint/types/generated drift, frontend coverage, Node and Go host tests, an isolated build and browser journeys. Go-host changes also need the shared Go gates. For explicit unit-only work, select isolated gates from the unit-testing skill; run `make ui-test-unit` alone when selecting the complete UI unit suite and report real-boundary checks as unrun. Appearance changes require `make ui-visual`; update baselines only for reviewed intentional differences. Packaging/login changes add the maintained Compose/OIDC gates, and deployment/session changes use the applicable Helm/replica scenario under AGENTS.md.

Use installed UI tools and owned fixtures, run browser gates sequentially and preserve retained development services/data. Report affected interactions, checks, meaningful skips and remaining limits. Update the workflow matrix or user/host guide when the tested behavior changes; an operation inventory or screenshot alone is not proof of a complete workflow.

For host diagnostics, reuse the injected process logger and `diagnostics.go` outcome owner. Preserve fixed route families, bounded API/issuer warnings, expected-authentication DEBUG classification and separate validated local/upstream IDs. Do not forward browser correlation headers or collect response bodies, cookies, OAuth values or raw dependency errors. Test filtering/privacy alongside real issuer/API success and failure controls; the observability runbook (`documentation/src/content/docs/operations/observability.mdx#ui-host-events`) defines recovery limits.

Use the current finite permission projection rather than checking built-in role names. `availablePermissions` enables navigation only; an exact action combines tenant/exact-namespace permissions and its OAuth ceiling. My access and assignment provenance remain bounded and policy-bound. Custom role/group administration uses the existing protected `access.manage` API; preserve explicit companions, built-in immutability, overlap explanations and manual refresh after policy changes. Namespace-only workflows must work without a tenant Reader grant.
