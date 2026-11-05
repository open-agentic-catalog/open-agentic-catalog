# Local artifact tooling

`image.py` inspects an existing local image, executes native version checks when requested, extracts its included license files or generates a Trivy SPDX SBOM. Its default backend profile stays strict; `--profile ui` checks the separate UI host and delegates build-bound evidence to `ui.py`. That evidence partitions React assets and the required static manual. Both inventories must match actual files and notices from the selected image. The SBOM and scan include frontend production dependencies and reviewed manual browser suppliers, keeping build-only packages separate. The `ui-container-inspect` and `ui-sbom` targets select `UI_IMAGE`, while backend targets select `IMAGE`.

Standalone SBOM validation compares each extracted binary's linked module versions and compiler standard library with its native SPDX application graph. The qualified converter uses `APPLICATION CONTAINS main module` and `main module DEPENDS_ON package` relationships. Package source information binds each node to its binary path. Duplicate module identities in different application nodes remain valid. Missing or empty main-module versions remain valid when the binary reports `(devel)` or an empty version. Validation runs before standards, frontend and manual supplements, without an advisory database or current-checkout dependency inventory.

An SBOM records components; it does not replace notices, advisory checks, provenance evidence or signatures. Backend and UI images have separate inventories. SDK packages, VSIX files, charts and external infrastructure images retain their own component boundaries.

`test_unit_spdx.py` checks exact versions, required relationships, malformed graphs and preserved native content without external tools. `test_functional_spdx.py` runs the real CLI and a converter subprocess double, with injected image extraction and final identity lookup. It checks rejection before publication, exclusive output, image drift and scratch cleanup for both profiles. Actual engine and pinned Trivy qualification remain separate native checks.

`charts.py` stages and validates the application and independent monitoring chart archives, reusing `test/monitoring/helm.py` for shared assets. Commands use bounded subprocesses and temporary staging and do not publish, pull substitute images, change a cluster or prune shared resources.

Use the public Make targets and [build/release guide](../../documentation/src/content/docs/contributing/build-and-release.md). `packaging_functional_test.go` runs Python input-validation, mocked subprocess and Make-scoping regressions in the normal Go test gate. Real Helm packaging tests live in `test/helm/test_functional_packaging.py` and run only through `make helm-package-test`, also included in `make helm-test` and `make helm-check`.

Installing Helm does not change default test selection. Actual image checks require the selected local engine and tools; static cross-architecture inspection does not execute target binaries or replace native compatibility tests.

`test_functional_make.py` checks UI lint and opt-in dependency-check targets, scanner selection, local/image Node alignment, the shared Go source-root inventory, explicit Helm target wiring, coverage-profile consumers and database/manual-session credential isolation. Disposable Make probes verify grouped default/explicit help without workflow execution, complete public target listings, Compose embedding-profile exports, env-file bypass, fixed Go selections, assertion-enabled Python execution despite environment or Make overrides, discovery/execution failures and atomic profile preservation.

Recursive `demo-up` fixtures retain the real Make boundary with inert child recipes. They check exact environment/file/command-line selections, spaced workflow paths, literal metacharacters, optional-service inputs and explicit build identities. Environment/file runtime credentials reach only Compose; the launcher does not copy them into recursive Make arguments or output. Workflow-path wildcard refusals prove that neither the literal selected file nor a matching alternative is loaded before child work. Origin/file refusal, first-failure stopping and readiness-before-loading assertions require no Docker or Catalog. The separately selected [curated workflow](../demo/README.md#complete-curated-developer-workflow) qualifies the real environment-selected launcher.

A tiny temporary Go module verifies actual build-tag selection and rejection of persisted flag overrides; no application build, scanner or database runs. Go lint/test/coverage scans must include the UI host and examples, but not Go files bundled in npm dependencies or generated output.

The [local OIDC tutorial](../../examples/tutorials/local-oidc/README.md) owns its private preparation and project-pinned Compose launcher. `test_unit_tutorial_credentials.py` and `test_unit_tutorial_compose.py` check protected files, prepared metadata and fixed selectors without processes. `test_functional_tutorial_compose.py` qualifies native launcher refusal/exit behavior, actual daemon-free Compose rendering under conflicting ambient inputs, and partial/fresh OpenSSL preparation in owned temporary directories. Actual-tool cases report explicit skips when Compose or OpenSSL is unavailable. They start no containers; the manual's complete setup/recovery requires separate owned execution.

`clean.py` owns the explicit build/dependency cleanup inventory behind `make clean` and `make clean-dry-run`. It validates the checkout and every target before removal or permission changes, preserving retained reports and data. The inventory includes `examples/langgraph/.venv/` and checkout-local Gitleaks, AGNTCY, Gateway and ANS inputs; other personal environments remain outside it. `test_functional_clean.py` uses disposable Git repositories for both public recipes. Independent literal-path assertions cover the five installations, ordinary child symlinks and the ANS read-only module cache. Unreadable-target/subtree and mount controls require refusal before any earlier valid target changes. Real user/mount-namespace qualification covers bind-mounted targets, ancestors and descendant directories/files; unavailable namespace support is a skip, not a qualified mount run.

These tests never clean the working checkout; they run in the existing packaging test gate.

Normal cleanup includes the root `httpserver.test` binary and prunes only empty build parents. Isolated units select parents without writes or Git. Public Make fixtures verify unchanged previews, child-first removal, preserved local state and repeated cleanup. Another test creates a file after preflight and confirms that cleanup preserves it.

Independent npm inventory checks derive maintained projects from tracked `package.json` files. Literal fixtures verify all seven dependency directories and `bin/examples/publication-report` through both public Make targets. They check unchanged previews, preserved package inputs, repeated cleanup and extension-only exclusions.

`make clean-output` and `make clean-output-dry-run` use a separate output scope in the same helper. Unknown output, runtime inputs, models and nested Git repositories remain in place. Selected siblings around nested repositories can be removed. The [cleanup guide](../../documentation/src/content/docs/contributing/build-and-release.md#clean-generated-output-separately) lists the exact removable and preserved locations.

`test_unit_clean.py` checks one complete preflight under the checkout lock, read-only selection, input preservation, literal paths and guard failures without Git or subprocesses. `test_functional_clean_output.py` checks both output Make targets in disposable Git repositories. It covers complete deletion, unchanged previews, nested repositories, child links, source and permission refusal, lock contention and all four targets' Make isolation. All four targets also run from checkout paths with trailing spaces and newlines. Real namespace controls qualify five output bind scenarios and ten refusals. These are separate from normal cleanup's existing mount campaign.

`modules.py` runs the tidy comparison for `make module-check` in a temporary copy of tracked and non-ignored new source, including unstaged edits and embedded assets. This prevents Go from discovering stale imports inside ignored reports or generated development output. It leaves the original checkout and module files unchanged, requires Git and Python, and rejects symlinked source.

`test_functional_modules.py` verifies source selection and preservation with a disposable repository.

`go_licenses.py` owns `make license-check`: the pinned analyzer's complete test-inclusive license report retains the general permissive allowlist plus the exact Rapid v1.3.0/MPL-2.0 test-only exception. Module metadata rejects replacements and other versions; non-test source and both shipped Linux architecture closures must exclude Rapid. Missing discovery, malformed reports, unknown/transitive forbidden licenses and tool failures fail the gate.

`test_unit_go_licenses.py` tests pure policy decisions; `test_functional_go_licenses.py` exercises actual Make/process wiring with a controlled Go executable. Actual analyzer qualification is separate from these controlled-tool fixtures. No application dependency is installed by the checker; ordinary Go caches may acquire its pinned analyzer.

`unit_coverage.py` validates and summarizes the single application profile produced by `make coverage-unit`. `test_unit_coverage.py` checks unique-block weighting, zero-hit retention, missing packages, conflicting rows and exact aggregate/critical-package floor failures. The shared comparison accepts plain decimal percentages from 0 through 100 and uses rational arithmetic, independently of rounded display output.

The unit aggregate and each shared critical package default to 90 percent. The existing Make regressions additionally use a disposable Go module to verify actual instrumentation, untested files and executable packages, tag/flag isolation, failure propagation and preservation of the last successful profile.

`application_coverage.py` reuses that atomic-profile parser for process counter conversion, local process merging and the fresh `make coverage-full` application report. The full target preserves separate unit/local/database/process profiles, compares the functional supervisor's source digest before and after the suites, rejects incomplete or incompatible inputs and enforces `FULL_COVERAGE_MIN` (95 percent by default) plus the fixed 80 percent per executable application file. Its `check` action also enforces the existing broad-local (90), core (93), critical-local-package (90) and database-package (95) floors with the shared exact arithmetic.

Pure reconciliation tests live in `test_unit_application_coverage.py`; CLI/Make/cleanup tests live in `test_functional_application_coverage.py` and the existing Make fixture. The Make controls reject rounded false passes, calibrate default/overridden thresholds through real recursive Make, and preserve successful outputs after threshold failures. Native Go instrumentation and disposable PostgreSQL are qualified by the owning process and full-coverage targets, not by the stub regressions.

Coverage producers keep temporary output under `coverage/.staging/`. Process controls interrupt each real recipe, then send another TERM while cleanup is active. Cleanup ignores repeated HUP, INT and TERM until it removes the owned scratch. The controls also preserve earlier successful output on failure and distinguish SIGKILL, which leaves scratch for later cleanup.

`dead_code.py` implements `make dead-code-report` and `make dead-code-check`. It builds the pinned analyzer for the host in an invocation-owned directory under ignored `output/`, verifies its build metadata, then loads the shared Make application/source roots for two production architectures and three test-inclusive selections. It rejects incomplete discovery, unexpected executable roots and invalid reports.

The report displays every finding; the check also enforces `dead_code_exceptions.json`, whose exact symbol/scope entries require reasons and must remain present in that scope. Neither command runs application binaries or test bodies. See [unreachable-function analysis](../../documentation/src/content/docs/contributing/testing.md#unreachable-function-analysis) for acquisition, offline use and review boundaries.

`test_unit_dead_code.py` tests pure report, discovery and exception-policy decisions. `test_functional_dead_code.py` runs bounded local launcher and Make fixtures, including target failure propagation; its stubs need no analyzer download. When changing the analyzer pin or its assumptions, explicitly qualify the actual binary after a version-qualified local installation:

```sh
mkdir -p output/deadcode-qualification/bin
GOTOOLCHAIN=go1.27.3 GOWORK=off GOFLAGS=-mod=readonly CGO_ENABLED=0 \
  GOBIN="$PWD/output/deadcode-qualification/bin" \
  go install golang.org/x/tools/cmd/deadcode@v0.51.0
python3 -B test/packaging/dead_code_qualification.py \
  --tool output/deadcode-qualification/bin/deadcode \
  --toolchain go1.27.3 --version v0.51.0
```

Install for the host, without target `GOOS`/`GOARCH` overrides. The qualification verifies version metadata and exercises executable roots, orphan packages, dependency tests, functional tags, generated reporting, unsupported comment suppression, clean `null`, exit-zero findings and load failures in a disposable module. It also includes an invalid local clone outside selected roots.

This is an explicit functional tool check, not an auto-discovered unit or an additional framework.

`data_checks.py` implements `json-check` and the discovered portion of `yaml-check`. Its Git inventory includes tracked and non-ignored new working files, omits worktree deletions and rejects symlinks; NUL-delimited paths preserve spaces and newlines. JSON includes the pinned JSON-LD context without style or schema rules.

YAML keeps Control OpenAPI under `api-yaml-check`, excludes the two Helm template directories for rendered validation, and applies syntax/duplicate checks without style to vendored upstream YAML. Parser/policy units and disposable Git/linter regressions use the native `test_unit_*` and `test_functional_*` selections.

`ard_conformance.py` checks the pinned Python dependency and schema, then invokes the unchanged upstream CLI with the same interpreter and requires explicit schema-success evidence for manifest/publisher mode. Registry mode retains its protocol-only assertions. Install the root locked development group explicitly with `make dev-tools-install`, as described in the [testing guide](../../documentation/src/content/docs/contributing/testing.md#schema-validation-and-ard-conformance). This places the pinned `jsonschema` dependency in `.tools/dev`.

The normal regressions use controlled dependencies/CLI fixtures and do not require `jsonschema`. For actual tool, schema-failure and loopback mode qualification, run `.tools/dev/bin/python -B test/packaging/ard_conformance_qualification.py`. An alternative interpreter must have the pinned `jsonschema` version installed. The qualification uses that interpreter for the unchanged upstream CLI and cleans its own temporary files and listener. No application binary or database is needed.

The loopback fixture serves one schema-valid MCP entry. It checks the checker's exact `weather forecast tools` query, MCP Server Card filter, `federation: none` and page size two. All three Registry routes must return HTTP 200. The served identifier/type and native result count must match the one expected MCP.

Separate omissions of score, source and identifier must each produce the intended native error. A valid empty response passes the native protocol check but fails the same assertion that requires nonempty result evidence. The qualification prints each wrapper exit and preserves native output, including deliberate failures. It does not invoke the declared MCP endpoint.

`registry_result_evidence` checks the selected HTTP-success markers and exact positive native count. Callers must also require a successful checker exit. Generic Registry mode still accepts valid empty results; its protocol-only behavior is unchanged. Isolated helper units need no optional checker dependency, subprocess or listener. Actual qualification does not establish every URI/type rule, sparse shape, authentication flow or continuation case.

## Property testing

`property.py` owns `make test-property`, selecting `./internal/jsondoc`, `./internal/identity`, `./internal/catalog`, `./internal/platform/config`, `./internal/audit` and `./internal/platform/idempotency`, and shares Rapid configuration, corpus fingerprinting and completion validation with `mutation.py`. It reuses the bounded process runner in `test/interoperability/common.py`. Existing `test-tooling-unit` and packaging functional discovery select `test_unit_property.py` and `test_functional_property.py`; these controls use synthetic events and child fixtures without acquiring Rapid.

See the [testing guide](../../documentation/src/content/docs/contributing/testing.md#structured-go-properties) for target settings, evidence and direct replay.

Qualify the actual pinned library and mutation integration explicitly after runner or dependency changes:

```sh
python3 -B test/packaging/property_qualification.py --toolchain go1.27.3
python3 -B test/packaging/mutation_qualification.py --toolchain go1.27.3
```

The first owns a disposable module and verifies shrinking to a known counterexample, seed/file replay, the corrected regression, and rejection of missing/malformed replay, zero checks, short mode and deadline-reduced success. Its explicit-file control disables random checks before and after correcting the assertion, so random generation cannot substitute for artifact replay. This qualification-only control does not permit zero checks in the public target.

The mutation qualification additionally verifies Rapid assertion catches and rejects generator starvation and ignored corpus baselines. Both remove owned temporary fixtures. Go caches retain normal downloaded dependencies.

These process/tool checks are functional; the application properties remain isolated units.

## Mutation testing

`mutation.py`, the private `mutation_exec.py` hook and `mutation-tool.json` own `make test-mutation`. Follow the [testing guide](../../documentation/src/content/docs/contributing/testing.md#mutation-testing-and-assertion-strength) for prerequisites, all accepted package commands, fixed source/operator selection, report paths, bounds and exit semantics. Tool acquisition is part of the target and verifies the installed version, module checksum and Go build metadata; no ignored prototype file is needed.

Pure event/report/configuration helpers are tested in `test_unit_mutation.py` and included in `make test-tooling-unit`. `test_functional_mutation.py` runs real Make, file and child-process fixtures without downloading the mutation tool; it belongs to the existing packaging functional wrapper. Run both focused selections with:

```sh
python3 -B -m unittest discover -s test/packaging -p 'test_*mutation.py'
```

After changing the runner or tool pin, run the explicit actual-tool controls with the same toolchain selected by Make:

```sh
python3 -B test/packaging/mutation_qualification.py --toolchain go1.27.3
```

This command acquires the pinned tool into an owned temporary directory and tests strong/weak assertions, failed/skipped baselines, filename-independent tests and embedded fixtures, generated/tagged/child-package exclusions, compiler errors, panic, timeout, missing executor and signal failure. It removes its owned tools and fixtures. The process fixtures separately verify cancellation/deadline cleanup and output limits.

Then run each public Make selection from a clean source checkout and inspect its new report; mocks alone do not qualify an upstream tool. Keep reports, logs, reviewed results and limits under ignored `output/`.

Keep tool changes out of root `go.mod`/`go.sum` and application images. Update the exact version/checksum/revision together and review the complete linked dependency/license inventory and binary advisories under the [tool policy](../../documentation/src/content/docs/reference/dependencies.md#mutation-tool); do not broaden the service Go license allowlist. Requalify event/report formats before accepting a tool or Go upgrade.

Preserve source staging, complete package test selection, all-skipped-baseline rejection and exact native/hook reconciliation.

## Local SonarQube

The optional [SonarQube harness](../sonarqube/README.md) owns pinned image acquisition, loopback startup, source/coverage ingestion, status and retained-data shutdown. Its `test_unit_sonarqube.py` decisions join the native unit selection; `test_functional_sonarqube.py` exercises Make isolation and a real local HTTP boundary in the packaging functional suite. Actual image/analyzer/persistence qualification remains an explicit workflow, separate from those native regressions.

## Coverage tools and local image scans

`coverage-tools/` locks the two optional coverage reporters separately from application dependencies. `coverage_tools.py` verifies installed Go command/compiler/module identities before reporting. Use `make coverage-tools-install`, then `make coverage-html`; `GOCOV` and `GOCOV_HTML` select installed executables.

Pure metadata tests and an actual Make/process failure fixture preserve the previous HTML report and remove staging when conversion fails.

`trivy.json` is the shared exact version for packaging, source security and UI lockfile scanning. `trivy.py` qualifies that tool, freezes a fresh advisory database, validates complete native reports (including each Go standard-library version, main module and linked dependency) and retains private scan evidence. `image.py` resolves/inspects the existing local artifact once and rechecks its selection before publishing SBOM/scan success.

`container-scan` and `ui-container-scan` reuse this boundary. The UI scan adds the production npm inventory extracted from the same image through its supplemented SPDX package URLs. See [local image scanning](../../documentation/src/content/docs/contributing/build-and-release.md#scan-local-images) for database acquisition, supported overrides, evidence, exit semantics and limitations.

`test_unit_trivy.py` exercises policy, metadata, inventory completeness, private evidence and cleanup with injected commands; `test_functional_trivy.py` exercises native failure/timeout/output bounds, missing-image rejection and Make report preservation. Existing Make fixtures verify both image selectors and target failure propagation. Ordinary selections do not install Trivy, start Docker or download databases.

Actual-tool controls are opt-in. Tool/platform/report changes need affected backend/UI image inspection, SBOM and scan qualification; preserve two-architecture evidence separately from narrower option-only checks. A stub-only pass does not establish scanner compatibility.

To qualify image-metadata secret detection, select a clean, locally built backend image, the pinned Trivy executable and an already fresh advisory database:

```sh
CATALOG_TEST_IMAGE_SECRETS=open-agentic-catalog:dev \
CATALOG_TEST_TRIVY=/absolute/path/to/trivy \
python3 -B -m unittest discover -s test/packaging -p test_functional_trivy.py \
  -k test_actual_image_metadata_secret_and_clean_control
```

This Docker-only control reads the image's build identity, scans its immutable ID, creates an owned local base alias and a disposable metadata-only derivative, then scans that derivative. It requires identical filesystem layers, clean success and a native `github-pat` synthetic-secret finding with failure status, without exposing the synthetic value on terminal output. Temporary reports/context and both owned tags are removed; normal images and running services are untouched.

It never submits a token to a provider, pulls a target image or executes the application. Both controls require zero High/Critical vulnerabilities, so a newly disclosed vulnerability in the selected base must be resolved before qualification. Docker's local builder may retain its normal cache containing this deliberately invalid synthetic value; do not use real credentials or prune shared caches as test cleanup.

`--image-config-scanners secret` is required and recorded only for image scans, not SPDX/development dependency scans. The native control proves this metadata path; built-in exclusions, binary/size limits and previously deleted-layer detection remain outside that proof. Current `scratch` runtime stages inherit no builder layers and need no stripping/squashing.

The local password setup uses `test_unit_local_auth.py` for isolated topology decisions. `test_functional_local_auth.py` exercises native account preparation, single-Catalog Compose rendering, input reuse/refusal, TLS renewal and loopback port checks. These tests start no containers. Select an owned deployment for actual OIDC login and retained-state checks.
