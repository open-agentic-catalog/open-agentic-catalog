---
name: api-contract-changes
description: Change Open Agentic Catalog HTTP routes, request or response semantics, API authorization and error contracts together with OpenAPI and affected clients. Use for contract changes; running existing API tests or changing UI presentation alone uses the existing workflows.
---

# API contract changes

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md`, API assets (`api/README.md`) and the relevant API conventions (`documentation/src/content/docs/reference/api-conventions.md`). Preserve plan-only, review-only and test-execution limits. Use the checked-in Control OpenAPI (`api/openapi/control-v1.yaml`) as the implemented API-listener contract.

## Trace the operation

Inspect the route registration, handler and middleware, owning domain operation, request/response types, existing assertions and actual clients. Establish the operation's authentication, Catalog role, namespace and tenant boundaries separately. Public discovery reads active host/audience-scoped snapshots; it must not expose mutable or private state.

The worker-only listener has its own operations contract (`internal/platform/operations/README.md`); do not add its routes to Control OpenAPI. For ARD or another standards representation, establish the requirement from pinned profiles (`schemas/PROFILES.md`) before changing its wire shape. Control errors use RFC 9457 problem details; ARD Registry errors retain their protocol envelope. Neither a similar route nor a reference implementation replaces the owning contract.

## Update the contract and its consumers together

- Keep route, method, operation ID, headers, parameters, body, responses and authorization aligned with OpenAPI. For protected routes, reconcile the finite action/scope mapping and reviewed authorization inventory; role labels, capability hints and broad token scopes are not authority. Preserve strict duplicate/unknown/trailing/over-limit rejection for service control objects and the deliberately extensible raw-evidence boundaries. Follow the current pre-release/released compatibility policy in AGENTS.md rather than inventing aliases.
- Apply idempotency to mutating commands and optimistic concurrency to revision-changing operations according to their contracts. Replays must reauthorize. Keep errors safe, classified and correlated; rejection must not disclose another tenant's object or commit unintended state.
- Update source assets under `api/`, affected examples/guides, and the clients that actually consume the change. `api/embed.go` embeds those assets directly. Use existing generation commands for generated clients/types; preserve lossless raw-response paths and do not hand-edit generated output. Update event vocabulary/schema/examples when a public event changes.
- Check emitted success and partial-response discriminators against OpenAPI, generated clients and the UI's consumed-value predicates. A generated type does not validate an incoming value. Preserve useful results accompanying documented partial-failure diagnostics, while rejecting malformed consumed fields and unsupported discriminators.

Use unit testing (`.agents/skills/unit-testing/SKILL.md`) for isolated decisions and direct handlers. Use functional testing (`.agents/skills/functional-testing/SKILL.md`) for real transport or composed workflows. Extend existing cases with successful output and relevant rejection, authorization, tenant, replay and stale-revision assertions. Check the error envelope and state/evidence effects, not just the status code. Preserve existing assertions without duplicating entire journeys.

## Verify the affected boundary

Use post-code verification (`.agents/skills/post-code-verification/SKILL.md`) for the shared source, unit and local gates. `make check` already includes `api-check`; `make api-contract-check` supplies focused HTTP/event contracts. Contract changes also use `make client-go-check` and `make ui-client-check` with their documented installed prerequisites. API validator/launcher changes add `make api-validation-test`; an explicit contract baseline can use `make api-diff` or `make api-breaking-check` following the validation guide (`test/validation/README.md`).

Regenerate each affected Go, Python, TypeScript or Rust SDK through the SDK harness (`test/sdk/README.md`). Select each affected language with `SDK_LANGUAGE` when running `make sdk-generate` and `make sdk-check` with explicitly installed tools. The raw Go compatibility client does not qualify the independent SDK packages.

Apply AGENTS.md's additional gates to affected lifecycle, SQL, standards or browser behavior. In particular, HTTP/lifecycle changes need the disposable running-service smoke path; discovery-output changes need applicable conformance evidence. Smoke mutates data, so use an invocation-owned or explicitly selected disposable service. Unit-only execution leaves those gates reported as unrun, not qualified by mocks.

Report the resulting behavior, contract/client alignment, executed checks and remaining limits. Update implementation status only when the runnable boundary materially changes. A passing schema or generated client does not prove authorization, transport or workflow correctness.

Run `make test-api-generated` after changes to its harness or selected API boundaries, with explicitly installed tools. These include Namespace operations, Search/browse, Resource/Version lists and Resource/Version creation. Follow the generated API guide (`test/apigenerated/README.md`): preserve unchanged OpenAPI input, successful controls, serialization-aware negatives, strict error/state assertions, native report completeness, bounded owned fixtures and cleanup. Requalify actual-tool calibrations when changing the pin or runner. Record genuine defects separately from generator limitations; ordinary unit tests must not acquire or execute Schemathesis.
