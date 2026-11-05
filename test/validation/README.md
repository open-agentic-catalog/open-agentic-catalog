# Local API validation

This directory adds read-only OpenAPI linting and explicit-baseline compatibility checks. The authoritative input remains `api/openapi/control-v1.yaml`; no generator, application server, database or container is involved. Existing Go API and handler tests remain the implementation-contract checks.

## Install and run

Use Node.js 22.13+ within major 22, npm 10+ (tested with 10.9.9), the repository Go toolchain and yamllint. Run from the repository root:

```sh
make validation-tools-install
make api-check
make api-contract-check
make api-validation-test
```

Installation downloads the exact npm lockfile with lifecycle scripts disabled and builds oasdiff from the isolated Go module. It records lockfile and executable hashes in ignored `.tools/`. Install explicitly after changing tool locks.

The validation launcher rejects missing or stale tools with an installation instruction. `api-lint` also runs the installed ESLint through its owning npm script. Neither check downloads tools or silently skips validation.

Initial downloads use the normal npm/Go caches. Neither the service's Go dependencies nor UI/documentation dependencies change.

The directory's dependency-free `go.mod` is a traversal boundary for root Go commands: npm packages can contain unrelated Go implementations. It adds no Go source or dependencies. The actual oasdiff module remains in `tools/`; root formatting separately prunes `node_modules` so checks never rewrite installed packages.

| Target | Purpose |
| --- | --- |
| `api-yaml-check` | Existing yamllint policy, including duplicate mapping keys. |
| `api-validate` | Existing Go API specification, example and convention tests. |
| `api-contract-check` | Existing local functional HTTP router/behavior and event schema tests, including owned in-process HTTP listeners; no database or deployed-service scan. |
| `api-lint` | YAML check, API-tooling ESLint, local-input preflight and configured Redocly lint. |
| `api-check` | Go API validation plus API/source lint; also part of `make check`. |
| `api-validation-test` | Real pinned-tool positive/negative fixtures and launcher safety tests; run when changing tooling, rules or pins. |
| `api-diff` | Semantic change report against an explicit baseline; changes alone do not fail. |
| `api-breaking-check` | Fail when oasdiff reports an error-level breaking change. |

`make verify` retains its Go/component/workflow boundary and does not require this npm installation. API-only Make goals ignore `.env` and `CATALOG_ENV_FILE` before parsing. They can be combined with each other, but not with unrelated goals; installation must run alone.

Normal aggregate targets retain their existing Make environment-file behavior: use `make check CATALOG_ENV_FILE=/dev/null` to omit local settings.

## Compare an explicit baseline

Supply an absolute path to a local OpenAPI 3.1.0 file representing the contract to compare against:

```sh
make api-diff API_BASE=/absolute/path/to/baseline.yaml
make api-breaking-check API_BASE=/absolute/path/to/baseline.yaml
```

The candidate is always the checked-in Control OpenAPI. No release, Git revision, remote URL or merge base is guessed or fetched. Both inputs must pass preflight and lint.

Output includes their SHA-256 digests and the semantic changes. The breaking gate uses `--fail-on ERR`; report and tool failures remain nonzero. Shells see Make's own nonzero exit convention, not necessarily the child tool's numeric status.

Comparisons use `--flatten-params` to account for inherited path parameters while preserving operation-level overrides. This changes only the comparison model in memory, not either file. No `allOf` flattening, schema downgrade, auto-upgrade or automatic compatibility fix is performed.

A breaking report calls for review; it does not independently determine the project's pre-release or released compatibility policy.

## Rule and input boundaries

Redocly 2.52.1 uses `spec` plus the reviewed rules in `redocly.yaml`: operations, parameters, references, examples, tags, summaries and security references. Unused components and cross-category component names are warnings. The rules do not require every response to list a concrete 4xx in addition to the shared default Problem response, require unconditional `If-Match` on conditionally revisioned commands, or reject the documented anonymous/conditional discovery security alternatives.

Domain-specific API conventions remain in Go tests.

Example checks allow additional properties by default, as JSON Schema does; explicit `additionalProperties: false` and `unevaluatedProperties: false` still reject extras. `resolve.doNotResolveExamples` prevents literal `$ref` keys in examples from being fetched. Unknown rule names fail the launcher preflight, since the pinned CLI can silently accept them.

Local built-in rules only: no remote presets, custom plugins or decorators.

The preflight accepts one duplicate-key-free UTF-8 YAML/JSON document, at most 2 MiB, with local JSON Pointer references. It rejects YAML aliases/tags, non-JSON keys/numbers, external references/examples, reference-base identifiers, dynamic references and unrecognized dialects. These are supported-tooling input restrictions, not claims that every rejected feature is invalid OpenAPI.

Standard OAS 3.1 and JSON Schema 2020-12 dialect identifiers are recognized without retrieving them. Schema annotations and example values are data, not retrieval instructions. Parsing preserves large integers for inspection and passes original bytes to tools.

Validator subprocesses run in an owned temporary working directory with a minimal environment, no runtime credentials or ambient proxy/Node loader settings, and telemetry/update checks disabled. oasdiff additionally receives `--allow-external-refs=false`. Each process has a 30-second deadline and a combined 2-MiB output bound. Input and configuration hashes are checked for concurrent edits; rerun on stable files if they change.

Only invocation-owned temporary files are removed. These are local-tool safeguards, not an OS sandbox for executing hostile plugins or validating arbitrary changing files.

## JavaScript source checks

`npm --prefix test/validation run lint` checks maintained `.mjs` modules, configuration and tests with pinned ESLint, recommended JS correctness rules and Node ES-module globals. It includes new files and excludes `node_modules/`, `.tools/` and the Go `tools/` module. It does not format, fix, cache or execute checked source; inline rule overrides are disabled.

The command invokes the local executable directly, so missing tools fail without downloading or falling back to a global linter. It does not require oasdiff or the installation stamp. `api-lint` invokes this command once before Redocly, so `api-check` and `make check` include source lint.

Native unit tests and `make verify` remain independent.

## Unit selection

After installing dependencies, run `npm --prefix test/validation run test:unit` for parsing, bounded regular-file reads and CLI rejection before execution. `input.functional.test.mjs` retains the FIFO/process tests; `tools.functional.test.mjs` retains actual validator qualification. `make api-validation-test` runs every `*.test.mjs`, with the same pinned tools and failure handling.

No real validator is required by the unit script.

## Qualification and limitations

`lint.functional.test.mjs` qualifies the owning npm command against disposable valid/invalid files, ES-module globals, generated-file exclusions, inline suppression attempts, unchanged source and missing tools. It runs with the other functional checks through `make api-validation-test`; the unit script does not launch ESLint or npm.

The Go `api-validate` gate also protects editor interoperability: parameter components must define their fields directly, the maintained Schema Object samples use plural `examples`, and body-bearing commands cannot use GET, HEAD or DELETE. HTTP and UI regressions separately verify lifecycle action routes, required preconditions and replay. These checks complement the selected linter; no Swagger runtime dependency is added to the maintained toolchain.

The tests exercise valid 3.1 nullable types, numeric exclusive bounds, recursive references, reference siblings, open and closed objects and literal example references. Negative cases check required structure, operation IDs, path/duplicate parameters, unresolved references, invalid examples and configuration/input failures. Comparison cases cover removed operations, request narrowing/expansion, response narrowing/expansion, inherited required parameters, operation overrides and equivalent inline schemas.

Launcher tests cover environment-file isolation, file bounds, missing tools, exit status, deadlines, output limits and source preservation.

Two pinned-Redocly limitations have explicit reproductions: a referenced boolean `false` schema is incorrectly rejected by structural validation; an example integer above 2^53 can round into a declared bound and incorrectly pass. These are not suppressed or represented as conformance successes. They do not occur in the current linted contract.

Future use requires reviewing the tool limitation, not rewriting valid schemas or disabling structural checks globally. Go client serialization compatibility remains a separate concern.

Refresh pins deliberately, reinstall, run the real-tool corpus and validate the unchanged authoritative input. Review both the npm lock and Redocly's bundled `THIRD_PARTY_NOTICES`, plus the isolated Go module closure. See the [dependency policy](../../documentation/src/content/docs/reference/dependencies.md#api-validation-tools) for execution-only licenses and audit limits. Do not patch installed dependencies or upload contracts to a hosted validator.
