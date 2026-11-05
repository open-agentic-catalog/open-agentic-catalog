---
name: unit-testing
description: Write, review and improve isolated unit tests and unit coverage in Open Agentic Catalog. Use for Go, frontend and native tooling units or for extracting isolated cases from mixed tests. Real server, subprocess, database and browser fixtures belong to functional testing.
---

# Unit testing

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md` and the test authoring and selection guide (`documentation/src/content/docs/contributing/testing.md#writing-tests`), then inspect the owning implementation, contract and existing tests. Use the checked-in API or pinned standards profile when expected behavior depends on it; a reference implementation or an uncovered branch alone is not the contract.

## Choose and implement a case

- Trace fixture execution through initializers, `TestMain`, helpers and cleanup. Units must not start listeners, call real network/DNS, spawn subprocesses, execute application binaries, or use real databases, browsers or providers. Normal test compilation and runner workers are allowed. Composed application workflows remain functional even with memory stores.
- Use local inputs, temporary files, HTTP recorders, controlled transports and existing fake stores or scripted transactions. An inert `exec.Command` is different from `Run`, `Start` or `Output`; `httptest.NewServer` is functional. A Go wrapper launching a Python unit suite is itself functional; run those Python units natively.
- Identify a missing behavior before adding a test. Extend existing cases when they already own the contract. Assert output, classified errors, state or absence of unintended calls; use valid controls for rejection cases. Do not duplicate workflows, assert accidental implementation details or add speculative production seams for coverage.
- Trace a plausible contract violation before designing the case. Construct expected values independently of the code under test; a round trip alone can hide matching defects. Prove the unchanged fixture succeeds, then isolate the intended invalid condition so another guard cannot mask it. Follow the failure investigation rules (`documentation/src/content/docs/contributing/testing.md#writing-tests`) before changing expectations.
- Use injected clocks/identifiers and deterministic synchronization. Keep Go units colocated and normally untagged. Python units use `test_unit_*.py`; Node tooling units use `*.unit.test.mjs`; React units use the existing Vitest/jsdom setup. Shared helpers must not introduce duplicate discovery.
- For timer-driven goroutines, follow the synctest guidance (`documentation/src/content/docs/contributing/testing.md#writing-tests`) and existing worker lifecycle tests. Keep fixtures and cancellation/release defers inside the bubble, establish quiescence before assertions, advance injected service clocks explicitly, and assert completion separately from quiescence. Convert existing cases without duplicate wrappers; keep real I/O in functional tests.

## Verify with existing gates

Run focused tests while editing, then the applicable complete selection from the root:

| Scope | Gate |
| --- | --- |
| All owned Go units | `make test-unit` |
| Application Go unit coverage | `make coverage-unit` |
| Python helper units | `make test-tooling-unit` |
| Frontend, UI Node helpers and UI Go host units | `make ui-test-unit` |
| Documentation Node units | `npm --prefix documentation run test:unit` |
| API-validation Node units | `npm --prefix test/validation run test:unit` |

Run each isolated Make target separately. They bypass workflow env files; preserve their fixed Go selections and assertion-enabled Python execution. The Go unit target includes isolated Minikube helpers through the existing `minikube` tag, without creating a cluster.

Do not replace the complete target with a function-name allowlist. The generated-client module has separate unit selection inside `make client-go-check`, which also performs functional/tool checks; it is not a pure-unit gate.

Use post-code-verification (`.agents/skills/post-code-verification/SKILL.md`) to select applicable lint, dead-code, race and broader checks after changes. If the user limits execution to units, report additional functional verification as unrun. When moving tests, reconcile assertions and discovery and verify both affected layers within the authorized scope; do not silently drop or mock away a real boundary.

Report commands, results, meaningful gaps and covered/total application statements when measuring coverage. Keep `coverage/unit.out` distinct from broad local, PostgreSQL and frontend reports. Include zero-hit application/database code, preserve the aggregate and each shared critical-package floor and atomic profile publication, and compare identical selections and denominators. Uncovered code is not evidence that it can be deleted. Keep raw reports under ignored `output/`. Reviewed measurements may use the optional private records described in `AGENTS.md`, not this skill.

For the reviewed packages, use the separate `make test-mutation MUTATION_PACKAGE=...` target after relevant source/assertion changes. Follow mutation assessment (`documentation/src/content/docs/contributing/testing.md#mutation-testing-and-assertion-strength`): add assertions only for contract-relevant survivors, preserve complete package tests, and keep timeouts/tool failures distinct from test catches. This tool execution measures isolated application units; it does not replace ordinary unit/coverage gates.

For structured generated tests, follow Go properties (`documentation/src/content/docs/contributing/testing.md#structured-go-properties`). Use the pinned Rapid dependency, bounded independent generators and one `rapid.Check` per top-level `TestProperty...`; keep ordinary example/fuzz assertions. `make test-property` runs alone and verifies requested counts. Fix genuine findings with minimized regressions, and keep replay artifacts synthetic and reviewed.

The explicit formal gate (`test/formal/README.md`) supplements the Job retry helper's ordinary tests. `make test-formal` proves selected arithmetic obligations; it does not execute application units or increase statement coverage. Preserve exact schedule/jitter and worker assertions. Runner helper units remain in `make test-tooling-unit`; actual verifier execution belongs to the separate installed-tool target.

For changes to UI command helpers or JSON presentation, preserve existing examples and the independent generated literal/token oracle. The bounded fast-check property belongs to ordinary UI units; use `ui-test-property` for explicit stress/replay. `ui-test-mutation` runs the complete owning Vitest files with fixed property inputs. Follow `catalog-ui/tests/assertion-strength.md`: inspect exact survivor dispositions, distinguish tool errors from assertion failures, and never treat a mutation score as source coverage. Install dependencies explicitly with `ui-install`.
