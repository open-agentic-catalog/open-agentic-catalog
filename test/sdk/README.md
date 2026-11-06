# SDK generation and verification

Each SDK retains its own LICENSE and NOTICE. Local Python, npm and Rust package builds compare those files with the corresponding SDK source. A missing or changed notice fails packaging. The Go SDK source archive includes its own license and notice files. Dependencies installed separately by package managers are not bundled into these SDK archives. The VSIX separately includes notices for SDK code and libraries that its build bundles.

The Go selection maintains [sdk/go/](../../sdk/go/README.md). It uses the complete, unchanged [OpenAPI contract](../../api/openapi/control-v1.yaml). The [Python selection](#python-sdk) has independent tooling. The [TypeScript](#javascripttypescript-sdk) and [Rust](#rust-sdk) selections are also implemented. All targets accept `SDK_LANGUAGE=go|python|typescript|rust|all`; the default is `all`. Selections run Go, Python, TypeScript and Rust in that order and stop on the first failure. No language is silently skipped.

## Run the maintained targets

For all four SDKs, install their prerequisites below, then run each target separately:

```sh
make sdk-tools-install UV=/path/to/uv-0.13.4
make sdk-generate
make sdk-check
make sdk-package
```

Only installation acquires dependencies. Generation updates checked-in sources; checks compare without rewriting them. Packaging builds local artifacts without generation, test execution or publication. Missing or stale tools fail with explicit installation guidance. Do not combine these targets with other Make goals, including parallel goals.

Packages and `manifest.json` files use `output/artifacts/sdk/<language>/`. The Python outputs are a wheel and sdist; TypeScript produces an npm tarball. Rust produces a local `.crate`. Go produces a source-module archive and its source hash inventory. The all-language package target writes `output/artifacts/sdk/manifest.json` only after every build succeeds. It removes a previous combined manifest before starting, so a failed run cannot leave a stale combined success record. Individual artifacts can remain from earlier runs: compare their release, contract and input hashes, and require a successful command. Packaging alone does not qualify an artifact. Capture `sdk-check` output to retain native test and coverage summaries. The TypeScript check runs units, local HTTP/TLS, installed-package and browser workflows under all three pinned runtimes. The artifact manifest binds the package to its SDK source and OpenAPI inputs.

Successful `sdk-check` invocations also write `qualification.json` beside each language manifest. The receipt identifies the current source, run and exact manifest hash. A failed check removes its previous success receipt. Packaging alone never writes one. Set `SDK_RUN_ID` to a unique safe run identifier for a CI handoff; local checks generate one automatically.

Use `test/ci/admission.py stage-sdk` to retain the actual qualified packages. It verifies current source, contract, manifest, package hashes and run identity before copying a finite set. The destination must be a new directory under `output/artifacts/qualified-sdk/`. The Go check retains its source archive after an outside consumer extracts and tests it.

Root `VERSION` supplies package versions. Python uses PEP 440 (`0.2.0-rc.1` becomes `0.2.0rc1`); npm uses the unchanged SemVer base. `make version-check` checks package/lock parity. The existing bump tool updates only the release metadata, then `sdk-generate` refreshes version-bearing generated source. Reinstall tools after lock changes and rerun `sdk-check`. GitHub Release downloads distribute these packages without ecosystem publication or Go submodule tags. Local checks create no tags.

### Verify documentation examples

The language READMEs are also the published SDK guides. Existing SDK checks verify selected actual code blocks rather than a second copy of their functions:

| Language | Example verification |
| --- | --- |
| Go | Extract named complete helpers into the existing external consumer. Compile them and exercise bounded Search, local detail and safe errors with owned loopback HTTP controls. These calls are functional tests. |
| Python | Compile Python fences and execute selected helpers with the existing in-memory HTTPX transport. Check continuation, partial results, exact local IDs, draft commands and one Job read. These are isolated units. |
| JavaScript/TypeScript | Compile marked complete snippets with strict TypeScript. Execute selected helpers with injected Fetch for paging, cancellation, raw evidence and command headers. These are isolated units; compilation is a separate tool check. |
| Rust | Compile README examples through native doctests on both supported compilers. `no_run` examples do not contact their placeholder origins. Existing extracted-package fixtures separately run the discovery program locally. |

Keep complete example fences and their helper names intact when editing the guide. Missing selected helpers or TypeScript markers fail verification. These checks do not execute every illustrative fragment or prove a production deployment. No additional Make target or hosted service is required.

### Go selection

Use Go 1.27.3, Python 3.12-3.14, GNU Make and a C compiler for the race detector. Run these targets separately:

```sh
make sdk-tools-install SDK_LANGUAGE=go
make sdk-generate SDK_LANGUAGE=go
make sdk-check SDK_LANGUAGE=go
make sdk-package SDK_LANGUAGE=go
```

Installation acquires the pinned generator, module dependencies and analyzers. It also checks SDK module tidiness while downloads are enabled, to acquire dependencies needed by offline checks and external consumers. Installation does not change module locks. The other targets use installed inputs with Go module acquisition disabled. Missing tools, changed locks or changed analyzer pins require another explicit installation. Deployment `.env` settings are bypassed, and mixed SDK/deployment goals are rejected. Go-only checks need no Python SDK environment, Node or browser installation.

`sdk-generate` is a maintainer operation, not a prerequisite for consumers. Generated source is checked in. `sdk-check` generates twice in an owned directory, checks determinism and compares source without rewriting it. It verifies module tidiness, formatting, vet, Staticcheck, the root golangci-lint rules and imported dependency licenses. Analyzer versions and license policy come from the root Makefile. The generator and its configuration come from the existing locked [Go-client compatibility harness](../clientcompat/README.md).

## Generated diagnostics

The supported oapi-codegen template override changes only diagnostic handling. It sanitizes generated request/editor/marshaler failures and complete response-parser failures. This includes error returns emitted by the generator's response-unmarshal helper. Do not edit `client.gen.go` by hand.

The original API schema, models, operation inventory, response alternatives and raw methods remain intact. Independent schema and four-kind workflow assertions run from the existing compatibility suite against the SDK's generated types. Its standalone `client-go-*` targets still qualify the unchanged generator independently.

## Test boundaries

| Selection | Execution and assertions |
| --- | --- |
| `sdk/go/*_test.go`, untagged | Injected HTTP Doers, codecs, limits, safe diagnostics, explicit credentials, partial/error branches and command bindings. Open metadata codecs preserve literal keys and null/false/empty values. Concurrent calls retain separate credentials and response budgets. Marshal failures stop before credentials or transmission. No listeners, subprocesses or application imports. |
| `sdk/go/sdk_functional_test.go`, `functional` tag | Owned HTTP/TLS listeners, private CA/hostname refusal, credential replacement, redirects/proxies, deadlines, response headers and lost command responses on reused connections. Fixed-length and chunked body limits, truncated bodies, stream cleanup and later recovery are checked. HTTP 429/503 preserve response metadata without command retries. |
| Temporary compatibility module | Reused independent schema/four-kind, local/indexed/live, pagination, Jobs/private-result and raw-codec cases. Added assertions cover every current operation and real draft creation, exact-key replay, stale ETags, denial and precision. A committed command with a lost response requires state readback and explicit original-key replay; current reader credentials and changed-body reuse remain denied. Composed Catalog memory fixtures stay outside the SDK runtime module. |
| Temporary external module | A compiled SDK import and representative local HTTP call with only the SDK replacement. No application module, generator or remote module is required. |
| `test_unit_runner.py` | Isolated launcher tests with mocked process boundaries. |
| `test_functional_runner.py` | Actual Python/Make processes for target selection, environment independence, missing-tool refusal, fail-fast execution and stale combined-manifest removal. Failed non-install commands acquire nothing and leave source inputs unchanged. |

`make sdk-check SDK_LANGUAGE=go` runs these selections with race detection and shuffled Go tests. For a focused isolated iteration, run `go test -race -shuffle=on ./...` from `sdk/go/`. Add `-tags=functional` for its local HTTP/TLS tests. Use the repository Go toolchain. Launcher units also run through `make test-tooling-unit`; its process tests remain functional.

The check reports handwritten SDK coverage for isolated units and, separately, units plus local HTTP/TLS functional tests. Both reports exclude `client.gen.go` and show covered/total statements. The second selection includes units; do not add their totals or percentages. Equal percentages can still cover different transport behaviors.

The temporary compatibility and external consumers provide additional assertions outside those two coverage profiles. The raw Go package percentage includes generated operations. It is not backend coverage or an operation-completeness measure. Root application profiles exclude this nested module.

## Inputs, results and cleanup

The runtime module has its own `go.mod` and `go.sum`. Generator locks stay under `test/clientcompat/tools/`; composed fixture dependencies stay under `test/clientcompat/go/`. No application or generator dependency enters the SDK's runtime import closure. Check SDK advisories separately from root application scans whenever its locks change.

Installation records module inputs and tool hashes under ignored `test/sdk/.tools/`. Checks reject stale tools and input drift. Successful checks remove their owned staging directories under `output/sdk/`; failed checks retain the directory and print its path. Clean consumers use securely generated temporary directories outside the checkout and are removed on success and failure. External package installations do not inherit repository imports. The Go staging directory contains `unit.out`, `local.out` and their `*-handwritten.out` profiles until cleanup. Capture command output when you need to retain the printed summaries. `make clean` removes SDK tools and default packages under `output/artifacts/` while preserving SDK source, locks and retained reports elsewhere in `output/`. It does not stop other services.

Neither these commands nor consumer builds publish packages, create tags or change retained Catalog data. No Compose, database, Minikube or external peer is used.

## Python SDK

The independent [Python package](../../sdk/python/README.md) owns its `pyproject.toml`, typed sources and Apache license. Python 3.12-3.14 is supported. Maintainers also need Java 21, OpenSSL, the repository Go toolchain and pinned uv 0.13.4. Go compiles the reused composed Catalog fixture; it is not a Python SDK runtime dependency.

```sh
make sdk-tools-install SDK_LANGUAGE=python UV=/path/to/uv-0.13.4
make sdk-generate SDK_LANGUAGE=python
make sdk-check SDK_LANGUAGE=python
make sdk-package SDK_LANGUAGE=python
```

Installation alone acquires dependencies. `test/sdk/python/pyproject.toml` and `uv.lock` isolate OpenAPI Generator 7.26.0, the build frontend/backend, analyzers and tests. Root `pyproject.toml` stays shared tooling. The installer downloads digest-bound runtime wheels selected from the lock for the installed interpreter. It records package/license inventory and the qualified uv and Go executables with their hashes. Checks reject changed executables or Python/Go module locks. Go fixture modules are cached at explicit installation; checks use the recorded Go binary with toolchain and module acquisition disabled. No Go SDK analyzer installation is required for the Python selection.

The complete unchanged OpenAPI is the sole API input. The check generates twice, formats with pinned Ruff, rejects nondeterminism/drift and compares current operation IDs, methods, paths, response alternatives/media, named object fields and codec field preservation. Supported templates replace transport and the synchronous bridge, hide Pydantic call inputs and document managed timeout/authentication behavior. No generated source is hand-edited. One template adjustment preserves composed-model fields that the generator otherwise discards during decoding. Four conditional schemas use the maintained JSON-object container: `RepresentationInput`, `TrustAssessmentRequest`, `CatalogSearchResult` and `AuditDetailsV1`. Independent schema tests cover valid and invalid delivery, result-origin and principal alternatives against the unchanged contract. These containers preserve fields; they do not validate every schema constraint. Other generated unions retain their normal models and require independent controls.

A guarded generation step preserves extra values in six open models: both listing-extension objects, `ARDSearchResult`, its trust manifest, response referrals and `ARDEntry`. The first five explicitly allow additional properties; `ARDEntry` uses JSON Schema's default openness. Native Pydantic extra storage preserves literal metadata names such as `additional_properties`, including scalar and null values. Decoding preserves absent versus explicit-null `@context`. Template-marker drift fails generation. Independent codec tests check the original schemas and a closed composed object; the repair must not make closed schemas open. Do not edit generated models by hand.

The first generator candidate, openapi-python-client 0.29.1, omits artwork responses and audit models. Its HTTPX/certifi closure is also unsuitable for this SDK selection. Runtime license checks cover the installed SDK closure separately from execution-only generator/test tools. The closure includes permissive PSF-2.0 typing-extensions and Apache/BSD-licensed python-dateutil; the Go allowlist is unchanged. Advisory review is separate and needs current advisory data when locks change.

| Selection | Boundary |
| --- | --- |
| `test/sdk/python/tests/test_unit_*.py` | Injected transports, codecs, schema/operation inventory, bounds, credentials, provider failures/time accounting, cancellation and raw/typed distinctions. Negative cases assert no transport call. No listener, subprocess, DNS or database. |
| `test/sdk/python/tests/test_functional_*.py` | Owned HTTP/TLS listeners, OpenSSL, private CA/hostname checks, ambient proxy refusal, lost commands, body/header limits, whole-call deadlines and observed connection closure on cancellation. |
| `catalog_workflow.py` and reused Go fixture | Actual authenticated Catalog API, four resource kinds, native Agent evidence, two reviewed and published Agent releases, complete nonempty Search/Browse pages, exact extension readback, audit continuation, revision/replay conflicts, current-permission replay refusal, denied-write readback, cross-tenant denial and private Jobs. No Compose or PostgreSQL. |
| Wheel and sdist consumers | Build a direct wheel and an sdist independently, then rebuild another wheel from that sdist. Require identical wheel contents. Install both wheel paths offline in separate clean environments outside the checkout, then run isolated imports and real Catalog calls. Require the exact locked runtime closure without source links, generators or test tools. |
| `test_unit_python_runner.py` | Stdlib-only launcher units with mocked process/tool boundaries; included in `test-tooling-unit`. |

The Python check runs native units with handwritten SDK coverage, then appends local HTTP/TLS tests to a separate profile. Both omit generated source. The second total includes units; never add percentages. The composed Catalog and both installed consumers add assertions outside those profiles. These percentages have a Python SDK denominator and do not change Go application coverage.

The installed-package selection also runs the actual [service-client tutorial script](../../examples/tutorials/local-oidc/service-read.py). `python_service_read_functional_test.go.txt` reuses the composed Catalog with owned HTTPS and retained service grants. The script runs in Python's isolated mode through both wheel and sdist consumers, without SDK source paths. The service script and its fixture belong to the check's source inventory.

Identity, tenant-wide and namespace-only permission checks, scope denial, input failures and unrelated CA trust have explicit API-call assertions. Failed preflight checks prevent Search; failed trust prevents API requests. Injected OAuth claims exercise scope enforcement. Existing OIDC tests cover JWT verification separately.

`test_unit_service_read.py` also tests the script directly with temporary files and mocked SDK calls. These units start no listener, process or database. They cover malformed and deeply nested claims, identity and permission decisions, bounded local Search and fixed diagnostics. Actual process tests separately require exit `1` and the expected refusal message. A synthetic private detail in the scope-denial response verifies that the script does not print the body.

For isolated iteration, use `PYTHONPATH="$PWD/sdk/python/src" test/sdk/.tools/python/bin/python -B -m unittest discover -s test/sdk/python/tests -p 'test_unit_*.py'`. The functional pattern is `test_functional_*.py`. The SDK gate also runs Ruff, compile checks, maintained-source mypy, wheel/sdist builds, clean installation and the composed fixture. Ordinary source checks do not require these independent SDK tools. The generated tree is excluded only from root Ruff rules; generation drift, compilation, import, schema/operation controls and SDK transport tests remain mandatory.

Successful checks remove their invocation-owned output under `output/sdk/python-*`; failures retain it with a printed path. Capture output to retain coverage summaries. `make clean` already removes `test/sdk/.tools`, including Python tools/wheels, and preserves sources, locks and retained reports. Checks publish nothing, change no retained service or data and contact no external Catalog.

## JavaScript/TypeScript SDK

The independent [ESM package](../../sdk/typescript/README.md) ships JavaScript and TypeScript declarations. Maintainer tooling uses Node 24.22.1 and bundled npm 11.19.1, acquired by the explicit SDK installer under `test/sdk/.tools/`. It also acquires Node 22.23.4 and 24.20.0 for compatibility checks. Distributions have reviewed SHA-256 pins in `test/sdk/node-runtimes.json`; the SDK owns `.nvmrc`. Installation does not change your default Node. Maintainers also need OpenSSL and the repository Go toolchain. The SDK owns its npm manifest and lockfile. It does not use the UI installation.

```sh
make sdk-tools-install SDK_LANGUAGE=typescript
make sdk-generate SDK_LANGUAGE=typescript
make sdk-check SDK_LANGUAGE=typescript
make sdk-package SDK_LANGUAGE=typescript
```

Installation alone acquires the locked npm closure, runtime package metadata, Chromium and Go fixture modules. It disables npm lifecycle scripts. Tools use the SDK's node_modules; browser/cache/stamp files use ignored `test/sdk/.tools/typescript/`. Checks fail for missing or stale tools. Generation, compilation, packaging and consumer installation acquire no packages. Run each target alone; deployment environment files and inherited Node/npm overrides are excluded.

[openapi-typescript](https://openapi-ts.dev/introduction) 7.13.0 generates the complete unchanged OpenAPI. [openapi-fetch](https://openapi-ts.dev/openapi-fetch/) 0.17.0 supplies typed path/method serialization. The maintained wrapper excludes transport overrides. TypeScript 5.9.3 builds ESM JavaScript and declarations. The SDK has no React, UI-host or application runtime dependency. Its two consumer dependencies use MIT licenses; maintainer tools remain devDependencies. The generator's js-yaml override matches the reviewed UI pin.

| Selection | Scope |
| --- | --- |
| `client.unit.test.mjs` | Injected Fetch/streams, credential admission/recovery, exact bytes, body/header bounds, provider deadlines, cancellation, metadata, errors, partial pages and bodyless responses. Both success/error JSON decode failures and retry hints have explicit controls. No listener or subprocess. |
| `client.functional.test.mjs` | Owned HTTP/TLS, private CA and hostname refusal, unknown-length bounds, compression refusal, truncation, active-body deadlines, observed cancellation and lost writes with/without command keys. Actual 429/503 hints do not trigger retries; malformed JSON preserves safe metadata and later recovery. |
| Contract and compiler checks | Every operation, path, status, request/response media and named schema; valid typed calls and compile-time refusal of unknown routes, wrong methods, missing command headers and transport overrides. |
| Clean npm consumer | Lifecycle-free offline installation of the actual tarball with locked runtime versions; plain JavaScript import, exact file/dependency inventory and installed TypeScript declarations. |
| Reused Catalog fixture | Actual installed-package API calls for four resource kinds, two reviewed and published Agent releases, nonempty Search/Browse continuation, exact native evidence, replay, ETag/key conflicts, current-permission replay refusal, denied-write readback, tenant boundaries and private Jobs. Uses memory stores and Go race detection. |
| Owned Chromium smoke | Installed ESM modules, independent ARD controls and real Catalog Search continuation, detail reads and denied writes. A bounded test-only gateway supplies the reader token. Each browser fixture confirms a same-origin cookie exists before asserting its omission from SDK requests. Browser traffic is restricted to the owned origin. The SDK adds no server CORS or browser-login feature. |

The check generates twice, compares checked-in declarations without rewriting them, builds, lints and tests. It reports handwritten compiled JavaScript line, branch and function coverage from isolated units only. Generated declarations contain no executable statements and are excluded. This is separate from Python SDK statements, Go application coverage and UI/browser coverage.

Successful checks retain the qualified npm tarball and its hash manifest under `output/artifacts/sdk/typescript/`. Temporary consumers and listeners are cleaned. Failed checks retain their invocation directory under `output/sdk/typescript-*`. `make clean` removes SDK npm dependencies, owned tools and ordinary packages; it preserves checked-in sources, locks and retained reports. Neither checks nor installation restart retained services or contact external Catalogs. The combined selection and packaging-only workflow use the same installed tools and build paths.

### TypeScript published discovery example

The same gate qualifies [the standalone ARD List client](../../examples/typescript-ard-list/README.md) on all three pinned Node runtimes. It copies the actual `.mts` application beside the clean consumer's installed tarball. Type checking uses installed declarations. No SDK source link or additional runtime dependency enters that consumer.

The example's isolated tests live under `typescript/ard-example/`. They use injected Fetch and temporary token files without listeners or subprocesses. The runner selects them explicitly after package installation. Their coverage report measures the example separately from SDK coverage.

`typescript_ard_functional_test.go.txt` extends the composed Catalog fixture with active anonymous/authenticated publications and an unpublished draft. It runs the actual Node entrypoint through owned HTTPS. Native cases cover paging, audience access, page budgets, expired continuations and activation of a different generation. Local malformed-response controls cover decoding and runtime field validation. Both unit and process cases preserve incomplete output on failure. The fixture refuses private, entry and runtime requests.

Private CA inputs, credentials, listeners and temporary consumers belong to the invocation. OAuth claims exercise the existing scope ceiling; this is not another OIDC qualification. No Compose stack, browser campaign or retained data change is added for this example. Existing SDK browser checks remain unchanged.

## Rust SDK

The independent [Rust crate](../../sdk/rust/README.md) uses checked-in bindings from MIT-licensed openapi-to-rust 0.22.0. Generation targets the complete unchanged Control OpenAPI. The bindings include reusable draft export and its file models. The operation inventory checks every implemented API operation.

Guarded post-processing routes generated operations through the maintained bounded transport, preserves response metadata, decodes original JSON bytes, removes unmanaged streaming aliases and fixes closed objects and refusal-only operations. The corresponding buffered operation remains available. Independent codec assertions and the authoritative operation inventory supplement generation and compilation. Seven packaged-consumer cases validate both original and Rust-reserialized values against the unchanged OpenAPI schema. Some conditional models preserve JSON values; the SDK is not a JSON Schema validator.

Maintainers need Linux AMD64, Python 3.12+, rustup, Git, the GitHub CLI, a native C toolchain and the repository Go toolchain. Rustup must already be installed.

Run `gh auth status` to check the GitHub CLI account. Run `gh auth login` if the CLI has no authenticated account. The installer uses `gh api` to download public source archives. That command requires authentication even for public repositories. Do not put a token in a command or repository file.

Installation changes no personal default. The supported consumer floor is Rust 1.88.0; maintenance uses 1.99.0 with rustfmt, Clippy and matching `llvm-tools-preview`. The caller owns its Tokio runtime. No blocking, WASM or other-platform qualification is claimed.

```sh
make sdk-tools-install SDK_LANGUAGE=rust
make sdk-generate SDK_LANGUAGE=rust
make sdk-check SDK_LANGUAGE=rust
make sdk-package SDK_LANGUAGE=rust
```

Run each target separately. Normal installation acquires compiler and generator prerequisites, crates and registry metadata. GitHub acquisition uses `gh`. The installer records exact tool hashes, a release-age cutoff and separate SDK, generator, package-consumer and [Rust report](../../examples/rust-catalog-report/README.md) inventories. Inventories include source checksums and license metadata.

Normal checks use frozen locks and offline tools. They do not require cargo-deny or RustSec data and do not run a dependency audit.

Root VERSION updates SDK metadata, both consumer constraints and all three local SDK lock entries through the existing bump workflow.

Installation reuses a complete installed toolchain only when its compiler reports the exact selected version and every required component exists. Missing or mismatched compilers/components use explicit rustup acquisition. It builds the generator from its pinned, checksummed source archive and checked-in lock. Compiler and generator binary hashes enter the installation record. That record explicitly marks the separate dependency audit as `not_run`. The exact Rustls 0.23.46 security patch has a checksum/release-time-bound exception to the ordinary release-age cutoff; no advisory is waived. The [dependency policy](../../documentation/src/content/docs/reference/dependencies.md#rust-sdk-dependencies) explains the fixes and retained checks.

| Selection | Assertions |
| --- | --- |
| `sdk/rust/src/unit.rs` | Injected transport/provider decisions, validation before credentials, strict origins, finite bounds, safe diagnostics, command headers, exact extensions/numbers, closed controls, omission/null and codecs. Invalid UTF-8 is rejected without changing raw evidence. Paused-time tests verify shared credential/transport deadlines; concurrent calls keep credentials, budgets and cancellation separate. No servers, network, subprocesses or application execution. |
| Owned Go HTTP/TLS fixtures | Explicit private CA and hostname validation, poisoned ambient proxies, redirects, no retries, compression feature unification, exact bytes, fixed/chunked/header limits, whole-call timeout and observed connection closure after cancellation. An incomplete body fails, and the same client recovers. Anonymous typed ARD reads cover the reviewed manifest, 304, opaque continuation, empty facets and separate ARD errors, with independently schema-validated messages. |
| Composed in-memory Catalog | Four-kind drafts, exact native evidence, independent review and two releases, nonempty Search/Browse continuation, audit paging, private Jobs, tenant denials, current namespace grants and revocation, replay/conflict/stale validators and a committed command whose response is lost. A rejecting fetch port proves the workflow performs no external representation fetch. |
| Extracted `.crate` consumers | Consumers outside the checkout use only the extracted package and their own frozen graph on both compilers. They run the actual Catalog/TLS controls and compiled discovery example. No generator or Catalog application dependency enters the Rust package. |
| Standalone Rust report | Both compilers build and run its native isolated units; the maintained compiler runs rustfmt/strict Clippy. Actual executables use the existing Catalog and owned HTTP/TLS fixtures for Reader/namespace/tenant boundaries, paging, partial/empty continuation, failures, required fields, safe output and observed sixty-second cancellation. A separate composed API/worker/TLS-archive fixture loads the unchanged default curated data and compares all twelve report rows against exact records, with three releases per kind and testing/kind/empty queries. |

The Go composition is a functional test dependency, not a Rust runtime dependency. Static codec cases cover indexed/live result provenance; this selection does not deploy a federation peer or qualify a real PostgreSQL database. Existing service interoperability and database gates retain those boundaries.

Rust launcher units give temporary tools a matching temporary ownership root. They run without Cargo or downloads, including with a mounted temporary directory. Separate controls reject symlinks and mounts below that root.

The report's isolated tests live in `examples/rust-catalog-report/src/unit.rs`. Run `cargo test --manifest-path examples/rust-catalog-report/Cargo.toml --frozen` with installed tools for native-only iteration. `rust_report_functional_test.go.txt` is copied into the owned SDK fixture; `cmd/catalogctl/rust_report_functional_test.go` reuses curated preparation and skips without the selected `CATALOG_RUST_REPORT` executable. The complete Rust gate selects and executes both. These functional checks never connect to the retained Catalog or execute discovered resources.

For isolated iteration, use the installed compiler directly from `test/sdk/.tools/rust/rustup/toolchains/1.99.0-x86_64-unknown-linux-gnu/bin/`, with `CARGO_HOME` set to `test/sdk/.tools/rust/cargo` and its directory on PATH. Run `cargo test --manifest-path sdk/rust/Cargo.toml --frozen --lib`. That command runs only native isolated units. The shared launcher exposes no additional unit target.

The full gate checks deterministic generation, rustfmt, strict Clippy, both compilers, README doctests, examples and rustdoc. Native compiler LLVM tools report separate handwritten unit and unit-plus-local-functional line coverage. Generated bindings, tests and drivers are excluded. Combined coverage includes unit coverage; do not add percentages. `output/sdk/rust-coverage.json` records counts and exact source/tool identities. These Rust line counts are independent of Go statement coverage.

### Separate Rust dependency audit

The [Rust dependency policy](../../documentation/src/content/docs/reference/dependencies.md#rust-sdk-dependencies) owns licenses and advisories. Run these commands separately when you need the audit:

```sh
make sdk-rust-audit-tools-install
make sdk-rust-audit
```

The first command requires the normal Rust tools. It acquires the pinned cargo-deny source, builds the analyzer and refreshes public RustSec data. It records an independent installation identity. Installation does not imply a clean audit.

The second command checks SDK, consumer, report and tool dependencies offline. It includes build/dev licenses and unpublished crates. Tool advisory checks exclude upstream dev-only tests. A real MPL-2.0 unpublished control must fail. Missing or changed tools and advisory data older than seven days cause refusal without acquisition. Refresh with `make sdk-rust-audit-tools-install`.

`output/sdk/rust-audit.json` records each stage and the overall result. A failed audit returns a nonzero status without invalidating the normal compiler/generator installation. A previous success report is removed before each audit attempt. Normal SDK artifact records retain `dependencyAudit: not_run`; they never claim audit success.

The maintained analyzer lock includes supported zstd security backports. Run the separate audit after installation to check the selected dependencies against fresh advisory data. Normal generation, builds, tests and packaging remain independent of this audit. One informational exception applies only to the generator's unmaintained unsafe-libyaml dependency. No SDK vulnerability is waived.

Tools, sources and compiled caches stay under `test/sdk/.tools/rust/`. Successful staging is removed; failures print their owned `output/sdk/rust-*` path. Extracted consumers are removed on success and failure. `make clean` removes these tools, default artifacts and conventional Rust targets. The public Git advisory cache under `output/sdk-rust-advisories/` and retained coverage reports remain available. The commands publish nothing and leave retained applications unchanged.
