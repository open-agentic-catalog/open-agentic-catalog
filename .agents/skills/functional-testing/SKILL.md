---
name: functional-testing
description: Write, review and run functional tests in Open Agentic Catalog, including local HTTP/process fixtures, composed workflows, PostgreSQL integration and browser or deployment qualification. Use to preserve real boundary assertions when splitting mixed tests; isolated unit-only improvements use unit-testing.
---

# Functional testing

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md`, the test authoring and selection guide (`documentation/src/content/docs/contributing/testing.md#writing-tests`), and the README for the affected harness. Inspect the implementation, owning contract and existing assertions before changing or adding a scenario.

## Select the boundary

Classify everything the test executes, including imports, `TestMain`, helpers and cleanup. Real listeners, HTTP/TLS exchanges, subprocesses and composed application workflows are functional, including workflows backed by memory stores. Real SQL, browsers, tools and deployments use their existing explicit gates. Contract assertions, generated properties, race detection, fuzzing and coverage are techniques, not separate isolation layers.

Keep local Go functional cases beside their source with `//go:build functional`; keep inert shared helpers available to the selections that use them. PostgreSQL tests retain `integration`, Helm retains `helm`, and Minikube socket helpers require `minikube && functional`. Python uses `test_functional_*.py`, and Node tooling uses `*.functional.test.mjs`. Do not enable all tags indiscriminately or move every functional test into a mirrored source tree.

## Implement the missing assertion

Use the smallest existing fixture that qualifies the behavior. Preserve real transport/process/database assertions, including meaningful failure paths. For database changes, retain migration, exact-role, tenant/RLS, transaction and lifecycle checks required by AGENTS.md. A fake transaction cannot replace SQL execution evidence. Avoid repeating complete journeys when the existing scenario can assert the missing outcome.

Trace how failures cross the actual boundary: partial commits, retries, cancellation, stale state or changed authorization may violate the contract even when individual calls succeed. Choose the relevant failure scenario and assert retained state and forbidden side effects as well as responses. Establish a valid control before fault injection, and use the failure investigation rules (`documentation/src/content/docs/contributing/testing.md#writing-tests`) to distinguish product defects from fixture or environment failures.

Own loopback listeners, temporary directories, processes and disposable environments. Use bounded readiness checks and deadlines, capture useful redacted diagnostics, and clean only invocation-owned resources on success and failure. Never reset retained development data or point a mutating smoke test at a shared service without the required authorization. Functional-test scope does not authorize unrelated hosted services, peer implementations or publication. Run packaged browser gates serially without concurrent Compose/Minikube network changes.

## Run the owning gate

| Scope | Existing gate |
| --- | --- |
| Units plus local Go functional cases and native wrappers | `make test-functional-local` (`make test` delegates here) |
| Broad local race and coverage | `make test-race`; `make coverage-check coverage-core-check` |
| Composed in-memory end-to-end workflow | `make test-e2e` |
| Real PostgreSQL | `make test-integration` or `make coverage-postgres-check` for an instrumented run |
| UI local fixtures and Go host exchanges | `make ui-test` |
| Actual UI browser journeys | `make test-ui`; use `test-ui-compose` or `test-ui-oidc` for affected packaged/login behavior |
| Packaged API workflows | `make test-functional` |
| Generated Go client or API tools | `make client-go-check` or `make api-validation-test` |
| Independent Catalog publication and federation across all four resource kinds | `make test-federation-peers`; follow the owned peer fixture (`test/functional/README.md`) |
| Independent immutable Skill consumption or native Plugin loading | Separately selected `make test-interoperability-skills` or `make test-interoperability-plugins` |
| Helm or Minikube harness contracts without a cluster | `make helm-check` or `make test-minikube-contract` |

Choose only relevant gates; this table is not a mandate to run every environment. Use post-code-verification (`.agents/skills/post-code-verification/SKILL.md`) to coordinate these gates with lint, units and dead-code checks after changes. Read the owning runbook for explicit installation, model, cluster, recovery or interoperability scopes. Reuse documented prerequisites and existing launchers rather than adding another test framework. Tagged compilation and a passing local suite do not qualify an unexecuted external environment.

For selected independent clients, resource consumers or Catalog peers, use interoperability qualification (`.agents/skills/interoperability-qualification/SKILL.md`). It routes HF, Directory, MCP, A2A, Skill, Plugin and two-Catalog scenarios to their maintained guides and explicit tool prerequisites. Keep direct native controls, exact immutable handoffs, denial before forbidden traffic, peer limitations and owned cleanup separate from helper-test results. Ordinary helper units must not import or execute independent tools; a functional test request does not authorize hosted calls or an unrelated peer deployment.

When splitting tests, reconcile old assertions, discovery, fixture ownership, fuzz/benchmark selection and opt-in cases. Execute each changed selection, check unexpected skips and retain required real boundaries. Run the applicable source/lint gates in AGENTS.md. Report commands, prerequisites, passed/failed/skipped outcomes, cleanup and any unrun scope. Keep broad local, database, frontend and API-operation coverage distinct; do not add overlapping percentages or label them unit coverage. Keep raw evidence under ignored `output/`. Reviewed results may use the optional private records described in `AGENTS.md`. Keep unresolved actionable findings in the owning tracker.

Mutation-runner Make/process regressions belong to the packaging functional selection. Runner/tool changes also require the explicit real-tool controls in the packaging guide (`test/packaging/README.md#mutation-testing`); stub-only reports do not qualify the tool. Keep the application packages measured by `test-mutation` isolated units.

Property-runner Make/process and license-gate fixtures belong to the packaging functional selection. Changes to the Rapid pin or runner also require the explicit real-tool qualification in the property testing guide (`test/packaging/README.md#property-testing`), including shrinking, replay and incomplete-run rejection. The application properties exercised by `test-property` remain isolated units.

Formal-runner process/Make fixtures live in `test/formal/` and run through the ordinary functional wrapper without Java. For changes to the helper binding, contract, runner or verifier pin, additionally execute `make test-formal` with installed tools and inspect the source proof, three semantic failure controls and four tool calibrations. Follow the formal harness (`test/formal/README.md`); acquisition remains explicit through `formal-tools-install`, and parser failures or weak-contract success are not application proof evidence.

Run `make test-api-generated` after changes to its harness or selected API boundaries, with explicitly installed tools. These include Namespace operations, Search/browse, Resource/Version lists and Resource/Version creation. Follow the generated API guide (`test/apigenerated/README.md`): preserve unchanged OpenAPI input, successful controls, serialization-aware negatives, strict error/state assertions, native report completeness, bounded owned fixtures and cleanup. Requalify actual-tool calibrations when changing the pin or runner. Record genuine defects separately from generator limitations; ordinary unit tests must not acquire or execute Schemathesis.

For selected dynamic security scanning, use `make test-security-passive` and `make test-security-active` after explicit `zap-tools-install`. Follow the ZAP harness (`test/security/README.md`): require actual vulnerable/fixed calibrations, observed authenticated API/UI exchanges, completed rules and passive processing, exact finding dispositions and owned cleanup. Never point the scanner at retained environments or infer TLS/OIDC qualification from the development fixture.

For Playwright source measurements, use `make ui-coverage-e2e UI_E2E_PROFILE=local|compose|oidc|semantic` with explicitly installed UI tools/Chromium. Follow the browser coverage protocol (`catalog-ui/tests/README.md#browser-driven-source-coverage`): preserve per-document checkpoints, await collector readiness for additional pages, flush before closing, and reject missing contributions, skipped cases or cleanup failure.

Run profiles serially and keep their Istanbul results separate from component/Go coverage. Private instrumented assets do not replace normal-image qualification. Do not add final-page-only V8 collection, saved auth state or sensitive trace output.

Combine only successful compatible run directories with `make ui-coverage-e2e-report UI_E2E_REPORTS='...'`. Review its explicit profile list, weighted union, `files.txt` and `gaps.json`; a subset is not all-profile qualification. The semantic selection reuses the full owned MiniLM supervisor, including 1,000-release qualification, and requires its pinned offline prerequisites. It supplements ordinary `test-semantic`. Keep public rebuilds before outage continuation capture and wait for accepted Jobs; preserve exact provider-outage versus stale-cursor expectations.

For configurable demo data or loading changes, select `make test-demo-catalog` with the explicitly installed UI/A2A/MCP tools following the owned workflow (`test/demo/README.md#complete-curated-developer-workflow`). It exercises copied YAML, governed PostgreSQL loading, browser discovery, Catalog-selected native calls, append-only extension and outage recovery. Preserve its unique project/ports/private inputs, exact cleanup and retained developer data. Hosted destinations remain metadata only; package-only Skill handoff is not execution. Keep existing minimal browser/model fixtures and full interoperability denial controls.
