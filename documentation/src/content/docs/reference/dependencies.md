---
title: "Dependency and tooling policy"
---

This reference covers Go dependencies, the separate UI, API-validation and documentation toolchains, and optional external test and monitoring tools. Each has a separate license, update process and verification scope. Build-only tools do not automatically become shipped dependencies. The direct Go module inventory below explains why each module is present. `go.mod` remains authoritative for selected versions. The documentation contract checks that the inventory agrees with it.

Select stable direct releases that are at least 14 days old. Use one fixed UTC cutoff for each update. Record approved security exceptions with exact package names, versions, source identities and hashes. Keep the ordinary cutoff for other dependencies.

Follow the component-specific exceptions below.

## CI bootstrap and source tools

The existing [CI installer](../../../../../test/ci/tools.py) verifies raw archive bytes against reviewed SHA-256 records in [its pin inventory](../../../../../test/ci/pins.json). Node reuses [the existing host distribution pins](../../../../../test/sdk/node-runtimes.json). Go, Node and npm versions retain their owning manifests; checksum records bind the selected artifacts rather than becoming another version-selection source. Upstream metadata is reviewed during updates, not fetched as the ordinary install-time checksum authority.

Python development tools use root [pyproject.toml](../../../../../pyproject.toml) and [uv.lock](../../../../../uv.lock), installed through `make dev-tools-install`. The dev group includes yamllint; its PyYAML/pathspec versions and wheel hashes come from that lock. Only obtaining uv itself needs the small [pip bootstrap input](../../../../../test/ci/uv/requirements.txt). Existing installed uv can be supplied explicitly. Native pip/uv wheel selection handles Intel/ARM Macs and compatible Linux x64/ARM64 distributions without another package manager or per-host dependency files.

Repository Markdown/MDX linting uses the private [Markdownlint manifest](../../../../../test/ci/markdown/package.json) and [native npm lock](../../../../../test/ci/markdown/package-lock.json). This keeps source checks independent of the Astro site installation. The CLI remains 0.50.0. Its supported markdownlint 0.42.1 library satisfies the CLI's declared range and selects re2js 2.9.1. Ordinary installation uses `npm ci`, without lifecycle scripts, audits, funding requests, live transitive resolution or lock repair.

These exact security exceptions to the recorded 2026-10-27T02:11:38Z cutoff are reviewed:

| Package | Version | Published | Reason |
| --- | --- | --- | --- |
| [npm](https://github.com/npm/cli/blob/fd31aa66f0b715bbae1fc072d24fc7e8f23657ad/package.json) | 12.2.2 | 2026-11-04 | Its official bundled distribution updates Undici and removes three High advisory matches. |
| [markdownlint](https://github.com/DavidAnson/markdownlint/blob/bf7bd390f813485e46d0386af8e2d7f99781272f/package.json) | 0.42.1 | 2026-11-07 | Supported patch within CLI 0.50.0's range replaces its affected exact re2js dependency. |
| [re2js](https://github.com/le0pard/re2js/blob/5a112c6290dd6c9a10d6ccaa84ed38d9dd6ecfa2/package.json) | 2.9.1 | 2026-10-28 | Exact dependency selected by markdownlint 0.42.1; fixes CVE-2026-116795. |

Package links identify reviewed immutable source revisions. Exact archive identities remain in the pin inventory; the [native npm lock](../../../../../test/ci/markdown/package-lock.json) retains the Markdownlint/re2js artifact sources and integrity values. Keep the ordinary cutoff for unrelated packages. Do not apply blanket date exceptions, force incompatible overrides or downgrade dependencies.

The npm archive is acquired separately from the checksummed Node distribution. The shared host helper and UI/MCP build recipes verify the same official artifact. Review its bundled dependencies with the qualified Trivy's `rootfs` mode and current advisory data. Check the complete package inventory; a default filesystem scan does not inventory this lock-free installed distribution. This is a pin-review step, not a prerequisite for ordinary npm installation. The selected npm closure retains two Medium matches; Markdownlint retains one Medium and one Low match. These tool findings do not establish a Catalog runtime exploit or an all-severity clean result.

Actionlint retains exact version-suffixed Go installation with the native checksum database. It uses the upstream tool module graph without repository-specific transitive overrides. Its upstream x/sys v0.42.0 metadata matches [GO-2026-5024](https://pkg.go.dev/vuln/GO-2026-5024), affecting `golang.org/x/sys/windows.NewNTUnicodeString` before v0.44.0. The [GitHub mirror](https://github.com/advisories/GHSA-4vpj-hr3r-4gpg) rates the finding Low and labels its record unreviewed. The affected package is Windows-only; supported Linux/macOS tool execution does not include it. Retain that disposition and reassess upstream changes or new execution platforms without suppression.

Use the [CI update procedure](../../../../../test/ci/README.md#deliberate-input-updates) for pin and lock refresh. Hosted profiles and the local act harness remain Linux AMD64; developer prerequisites use ordinary native tools. Source checks retain their documented Go analyzer/module acquisition behavior. Hooks remain network-free. Existing Pi/Explorer harnesses keep their separate Linux/systemd requirements.

## Optional local Actions runner

Local workflow setup pins [act 0.2.89](https://github.com/nektos/act/releases/tag/v0.2.89), its archive checksum and one Linux AMD64 Ubuntu 24.04 base in `test/ci/pins.json`. Both downloaded artifacts meet the 14-day rule. Act and the upstream runner recipe use MIT; bundled Ubuntu and tool components retain their separate licenses. The local recipe uses the project license. It configures a non-root account and container sudo, without new packages. These execution tools remain outside the application Go module and shipped Catalog artifacts.

Setup retains act's license with its executable. Its receipt binds the base, local recipe checksum, host UID/GID and exact image ID. See [local workflow setup and qualification](../../../../../test/ci/README.md#local-workflow-commands) for isolation, checks and cleanup limits.

A stable tool can require an exact prerelease dependency. Review that upstream requirement and its existing use before advancing the dependency. Do not override incompatible ranges to force another version.

## Optional PgBouncer infrastructure

The application chart can provision a separately pulled PgBouncer image with `pgbouncer.enabled=true`; it defaults off and is not embedded in the Catalog image or imported Go dependencies. The selected digest lives in the [chart values](../../../../../deployments/helm/open-agentic-catalog/values.yaml), aligned with the two qualification fixtures. This is optional database-path infrastructure when enabled, rather than a development-only tool. PostgreSQL and its stable writer endpoint remain operator-supplied; migrations and other owner operations stay direct.

Upstream PgBouncer uses ISC and the container recipe MIT, while bundled OS packages include GPL and other licenses. Keep their notices and corresponding-source obligations when redistributing the independent image; this does not widen the Go license allowlist. Record exact source/image identities, both architecture inventories and execution limits with each local qualification. Review updates deliberately with image vulnerability/license scans and both `make test-pgbouncer` and `make test-minikube-pgbouncer`; align the chart/default and both fixtures rather than silently updating a mutable tag. The [pooling runbook](../operations/database-roles.mdx#connection-pooling-boundary) owns TLS, credentials, budgets and supported session mode.

## Optional local SonarQube tooling

The separately acquired Community Build and scanner images are digest-pinned in `test/sonarqube/images.json`, with the qualified version, licenses, resource requirements and update verification in the [harness guide](../../../../../test/sonarqube/README.md). The server/scanner use LGPL licensing; the bundled analyzers include Sonar Source-Available License components and the complete bundle is not entirely OSI open source. These execution-only tools are not application dependencies or release artifacts and do not widen the Go license allowlist. Pin updates require native regressions and actual local source/coverage ingestion, authentication, cleanup and retained-history qualification. No automatic image update or hosted integration is configured.

## License policy

The optional UI has a separate locked npm toolchain and [dependency/license inventory](#ui-runtime-and-test-inputs). Its Playwright/Testing Library ESLint plugins are MIT-licensed test tools; the axe browser accessibility integration is MPL-2.0 and test-only. Do not apply the Go import allowlist below to npm tooling or include test tools in the production JavaScript inventory.

`make license-check` uses `test/packaging/go_licenses.py` and pinned `go-licenses` v1.6.0 with the repository's pinned Go toolchain against the maintained Go source roots selected by `GO_SOURCE_PACKAGES` in the Makefile, with the `functional,integration,helm,minikube` build tags and test-only imports included. This covers the service, UI Go host, examples and harnesses without traversing Go examples bundled in npm dependencies or generated output. Isolated API-validation and generated-client modules have separate reviews below. The gate checks the imported transitive closure, not only the direct modules listed below. The launcher validates the tool's complete license report; it does not ignore the excepted library.

The target deliberately builds the tool with the repository toolchain because an older prebuilt binary can mistake a downloaded newer Go standard library for a third-party dependency.

The general approved [SPDX license identifiers](https://spdx.org/licenses/) are exactly `Apache-2.0`, `MIT`, `BSD-2-Clause`, and `BSD-3-Clause`. Unknown classifications and other licenses fail except for the reviewed Rapid test dependency below. This rejects GPL, AGPL, LGPL, and other copyleft or unreviewed licenses by default. A new identifier or dependency exception requires an explicit policy review.

The sole Go exception permits [Rapid](https://github.com/flyingmutant/rapid) `pgregory.net/rapid` **v1.3.0**, classified as **MPL-2.0**, in test imports only. Rapid is a pinned root-module test dependency for structured unit properties. The gate checks resolved module metadata, rejects other versions and replacements, and requires the matching license report.

All transitive dependencies retain the general allowlist. Rapid must be absent from non-test imports in the maintained source selection and from all three shipped commands under both Linux AMD64 and ARM64 release selections (`netgo,osusergo`). Checks use read-only module mode and disable workspace overrides. No global MPL allowance or blanket test exclusion is introduced.

This use is compatible with the project's Apache-2.0 license: Mozilla's [MPL FAQ, questions 5 and 11-13](https://www.mozilla.org/en-US/MPL/2.0/FAQ/), distinguishes use from distribution and permits combining separate Apache-licensed files with MPL code. Importing Rapid in independently authored tests does not relicense those files.

MPL remains the license of Rapid itself. Preserve upstream notices in downloaded copies; do not copy Rapid implementation into project files, vendor it into source releases, or distribute linked test binaries under this test-only exception. Such distribution needs a separate notice/source-availability review under [MPL sections 3.1-3.4](https://www.mozilla.org/en-US/MPL/2.0/).

Apache licensing is distinct from Apache Software Foundation project policy. The [ASF Category B policy](https://www.apache.org/legal/resolved.html#category-b) lists MPL-2.0 and restricts how covered works are bundled in ASF distributions. This exception is for externally downloaded development code absent from Catalog release artifacts, not blanket approval for bundling MPL source or claiming ASF project status.

Backend and UI image stages collect licenses from non-test command imports and copy only release outputs; dependency caches and test executables remain outside the final images. Recheck that boundary when packaging changes.

Project source uses [Apache License 2.0](../../../../../LICENSE). Third-party material retains its respective terms and notices.

The dependency gate excludes the main module and checks imported dependencies. It does not verify source headers or completeness of the distributed notice bundle; artifact packaging must include the applicable licenses and notices.

The gate covers Go code imported by the service and its tests. Standalone development analyzers are pinned build inputs and are not linked into release binaries. Vendored standards sources and their license files remain governed by the [profile inventory](../../../../../schemas/PROFILES.md), `schemas/checksums.txt`, and `make profiles` rather than this Go-module gate.

The [reviewed upstream resource fixtures](../contributing/testing.md#reviewed-upstream-resource-fixtures) retain their Apache-2.0 or MIT licenses beside immutable source inputs. Adapter tests check their checksums and complete file set. These inert files are not Go imports or executed dependencies; `license-check` does not review their terms. Preserve their attribution when distributing repository source. They are absent from final application images.

External development tools and test-container images have a separate policy. The Catalog executes these tools without importing them into its Go dependency graph. Before you adopt an external tool, retain:

- One exact version or artifact pin.
- Source licenses and the complete artifact license inventory for each supported execution platform.
- Security and architecture reviews.
- A documented acquisition and retention procedure.
- An explicit acceptance decision.

 Do not infer an image's complete license inventory from its source project's license or from a scan of only one platform manifest. An execution-only image may be obtained from upstream only when the owning policy accepts that use. Obtaining it does not make it a Catalog release artifact. Do not bundle, mirror or redistribute it without separate acceptance of those actions and all image components. Listing it in the Catalog release SBOM or describing it as a permissive dependency also requires acceptance covering those actions. ADR-0003 applies this rule to the independent OIDC test provider.

The Go allowlist does not cover external-tool execution; each tool requires its separate documented license and security review. The independent OIDC test provider's [two-platform license/security review](../contributing/testing.md#packaged-oidc-qualification) records the exact artifact identities and known findings. The image is not part of the Go allowlist or a Catalog release artifact.

## Go reachability analyzer

`make dead-code-report` and `make dead-code-check` pin `golang.org/x/tools/cmd/deadcode` v0.52.0, maintained by the Go tools project under BSD-3-Clause. It complements existing Staticcheck/golangci-lint checks by analyzing reachability from explicit executable and test roots. The launcher builds it with the repository Go toolchain for the host, verifies embedded module/toolchain metadata, and removes its invocation-owned binary after use. Go module/build caches retain ordinary downloaded inputs. It does not modify the application module or enter release images, SBOMs or notice bundles.

The reviewed linked module closure is `golang.org/x/tools` v0.52.0, `golang.org/x/mod` v0.42.0, `golang.org/x/sync` v0.24.0 and `golang.org/x/telemetry` v0.0.0-20261009151841-97e8ea77aa2a, all BSD-3-Clause, plus the Go standard library under its BSD license. Telemetry is disabled in the launcher environment. Check the complete imported-license inventory and built-binary vulnerabilities separately. Root `license-check` covers application imports and does not replace this tool review. Qualified host execution is Linux AMD64; Linux ARM64 is a static application-analysis selection, not an ARM tool-execution claim.

Refresh the exact pin deliberately with license/dependency and vulnerability review, the [actual-analyzer fixtures](../../../../../test/packaging/README.md), both report/check targets and the applicable source/functional gates. Do not silently substitute a globally installed or latest-version binary. See [testing prerequisites and bounds](../contributing/testing.md#unreachable-function-analysis) for first-use acquisition, offline caching, subprocess limits and analysis limitations.

## Independent ARD Explorer test tools

The optional [Explorer harness](../../../../../test/interoperability/ard-explorer/README.md) selects MIT-licensed source at `ba00c28ec268af379a5b7d06dd2a5f5c1d2fd821`. It retains the unchanged npm lock and verifies source and license hashes. Its npm packages declare MIT, Apache-2.0 and 0BSD licenses. Local installation records embedded manifests and notice-file hashes. It reuses the pinned UI Node, npm, Playwright and Chromium tools. Explorer remains an execution-only test client; it adds no Catalog runtime dependency or shipped files.

Installation disables npm lifecycle scripts and runs the reviewed upstream build explicitly. The locked platform package supplies esbuild without its postinstall script. The local advisory scan covers every locked package, including development tools. GHSA-67mh-4wv8-2f99 affects esbuild 0.24.2's development server, which this harness never starts. New findings require review; there is no global scanner exception. Keep source, license, age, advisory and confinement results in local reports under ignored `output/`.

## Independent HF client test tools

Independent client and local HTTPS peer qualification pins `hf-discover` 1.3.7 at commit `49c927439fcaa8f210cfd42186c0641acef579fa` (MIT), Python 3.13.17 by image digest and hash-locked uv/Hatchling build tools. The upstream runtime lock is retained unchanged. The installed Python inventory includes permissive licenses and MPL-2.0 components; these packages remain in a local test-only image, not the service runtime or Go license allowlist. Explicit installation records source/lock/image identity and installed versions. Peer qualification reuses the existing host jsonschema pin through the maintained conformance wrapper and requires OpenSSL for invocation-owned TLS certificates; no package is added to HF. Checks do not download tools or contact a hosted HF service. See [independent client testing](../contributing/testing.md#independent-hugging-face-interoperability) for the selected exchange and maintenance boundary.

## ADK interoperability tools

The [native ADK qualification](../../../../../test/interoperability/adk/README.md) owns `test/interoperability/adk/requirements.in` and its generated hash lock. It selects [Google ADK Python 2.13.0](https://github.com/google/adk-python/tree/d68ca857d5e3bab8e968d4e5f98ef028eb37dc87), MCP 2.3.0 and pip 26.2.2. The complete wheel closure uses the fixed `2026-10-29T00:00:00Z` cutoff. `provenance.json` retains the upstream revision, wheel identities and native adapter source hashes. `make adk-tools-install` verifies those inputs and notices, builds the digest-pinned Python image and records installed package/license metadata. Tests never acquire tools implicitly.

These are execution-only dependencies, outside application modules and shipped Catalog images. ADK includes model-client libraries, but the selected native tools do not create a model or make inference requests. Qualification disables automatic Google mTLS discovery and OTel export, uses explicit private-CA trust and confines execution to owned loopback. It does not require Google credentials. Preserve full dependency/license inventories and current advisory results separately from runtime compatibility and base-image OS qualification.

The harness documents [provenance, updates and the retained Medium OpenTelemetry advisory](../../../../../test/interoperability/adk/README.md#dependency-provenance-and-updates).

## A2A interoperability tools

`test/interoperability/a2a/requirements.txt` hash-locks the execution-only Python SDK 1.2.3 HTTP-server closure, Uvicorn and pip. The SDK release and all installed SDK source bytes are bound to commit `44b2f761a99123370237afe6c828f4cd2a50c402`; the unchanged Hello World executor comes from sample commit `6603ba3f2c31a7ef33e70b9d8b5b5f8be42ac9a3`. `a2a-tools-install` verifies their source digests and retained Apache-2.0 notices, installs wheels in a digest-pinned Python image and records the package/license inventory. `test-interoperability-a2a` uses that image offline for direct and governed handoff controls; current Catalog commands are built separately from cached Go modules. The direct control qualifies the sample's SDK 1.1.0 declaration with the selected SDK 1.2.3; no upstream protocol implementation is patched.

The installed Python distributions declare Apache, MIT, BSD, PSF and MPL-2.0 licenses, including certifi's unchanged certificate bundle. These are local execution-only tools, outside the application module, Go import allowlist and shipped Catalog images.

Review the complete installed inventory and current advisories when updating pins; a Python package advisory check does not assess bundled native libraries or base-image OS packages. Preserve exact locks/source identity and actual qualification. See the [A2A harness](../../../../../test/interoperability/a2a/README.md) and [testing scope](../contributing/testing.md#local-a2a-interoperability-control).

## MCP interoperability tools

`test/interoperability/mcp/package-lock.json` isolates Inspector 2.12.0, Everything 2026.8.31 and Everything's SDK 1.32.2 from application dependencies. The source/npm identities and archive integrity are pinned in `provenance.json`; installation uses the digest-pinned Node 22.23.4 image and disables all lifecycle scripts. The published selected executables need none of those scripts. `make mcp-tools-install` records the immutable image and installed package/license/script inventory, including embedded manifests. `make test-interoperability-mcp` uses these installed Inspector/Everything tools offline. See [MCP control testing](../contributing/testing.md#local-mcp-interoperability-control).

`make mcp-catalog-tools-install` installs a separate independent Python Catalog client. Its `requirements.in` pins `mcp==2.3.0`; `test/interoperability/mcp/catalog/mcp-provenance.json` owns its upstream source and wheel identity. A temporary process observer records the unchanged SDK child's exit; it does not rewrite SDK source or protocol bytes. `make test-interoperability-mcp-catalog` checks Control tools over both stdio and stateless HTTP with these installed inputs. It does not use the Inspector npm client. See [Catalog MCP qualification](../contributing/testing.md#catalog-control-tools-over-stdio) for acquisition, transport scope and reports.

Preserve the exact Everything source license retained beside the lock: its npm archive omits the file, and the source is transitioning from MIT to Apache-2.0, with CC-BY-4.0 documentation. The installed closure declares permissive licenses, MPL-2.0 and permissively selectable dual licenses; the included pino test fixture has no separate license field. These are local execution-only tools, outside the Go import allowlist and shipped Catalog images. npm audit assesses the exposed graph, not all embedded/native code or the base image; recheck provenance, notices, vulnerabilities and real qualification when updating pins. Ordinary source/unit checks neither acquire nor execute these tools.

The trusted upstream [ContextForge](https://github.com/IBM/mcp-context-forge) test image is exempt from the 14-day release-age rule. Select the latest stable official image during explicit installation. Record its release tag and immutable digest. Use that digest throughout qualification. This exception covers only the ContextForge image.

`make contextforge-tools-install` verifies [the reviewed inputs](../../../../../test/interoperability/contextforge/inputs.json), source license and Linux AMD64 manifest. It records Python and OS inventories and a local image scan. The [ContextForge harness](../../../../../test/interoperability/contextforge/README.md) describes acquisition, retention and isolated execution. Its image remains outside Catalog runtime packages and notices.

## ARD conformance Python prerequisite

The root pyproject dev group and uv.lock pin the MIT-licensed `jsonschema` 4.26.0 package required by the unchanged upstream ARD CLI. The maintained wrapper enforces this direct version and Draft 2020-12 support. Its normal dependencies are `attrs`, `jsonschema-specifications`, `referencing` and `rpds-py`; optional format extras are not selected. This is an explicit local test prerequisite, not a Go/runtime-image dependency. The common Python closure is fully locked alongside Ruff, bump-my-version and Apache-2.0-licensed coverage 7.17.0, installed only by `make dev-tools-install` with uv 0.13.6. Coverage reuses the version selected for the Python SDK and measures optional native tooling units. The lock retains conditional-platform packages and reviewed wheel hashes. The closure includes MIT, BSD-2-Clause/BSD-3-Clause, PSF-2.0 and Apache-2.0 licenses. The shared YAML tools additionally use GPL-3.0-or-later for yamllint, MPL-2.0 for pathspec and MIT for PyYAML; uv uses MIT OR Apache-2.0. These execution-only tools retain upstream notices and remain outside runtime images. The Go dependency allowlist is unchanged. Review the full lock's licenses and advisories on changes, not only the direct packages. The existing security target verifies complete scan inventory through a transient requirements projection because qualified Trivy cannot directly parse this dependency-only uv layout; no duplicate checked-in requirements file is maintained. Installation and actual-tool qualification are documented under [ARD conformance](../contributing/testing.md#schema-validation-and-ard-conformance).

## Python lint tool

Ruff 0.17.3 is an execution-only development tool with an [MIT source license](https://github.com/astral-sh/ruff/blob/0.17.3/LICENSE). Root `ruff.toml` enforces the exact version and the small stable rule selection. It is not imported by Python helpers, included in Catalog images or needed by Go verification. Preserve upstream notices in the local installation; this selection does not introduce a redistributed tool bundle or change the Go dependency allowlist. Checks use installed local binaries without network acquisition, source execution or automatic fixes. See [Python linting](../contributing/testing.md#python-linting) for isolated installation, scope and update verification.

## Coverage reporters and Trivy

`make coverage-tools-install` installs gocov v1.2.1 and gocov-html v1.4.0 from the isolated `test/packaging/coverage-tools/` module and its checksums, using the repository Go toolchain. Its compatible `x/tools` v0.52.0 closure replaces the reporter's older compiler-incompatible dependency. These MIT/BSD development tools do not enter application modules or images. Installation can acquire modules; rendering never installs them. Select the installed executables with `GOCOV` and `GOCOV_HTML` when their installation directory is outside `PATH`. Both installation and `coverage-html` verify command, compiler, module versions and checksums through Go build metadata. Rendering publishes only after both conversion and HTML generation succeed. Ordinary Go coverage targets and `go tool cover -html` need neither reporter.

`test/packaging/trivy.json` owns the exact qualified Trivy version, currently 0.76.1. Install the corresponding official release for the host and verify its published checksums; `TRIVY=/absolute/path/to/trivy` selects it without replacing another personal installation. Image/SBOM, UI lockfile and root security targets enforce the same pin. Image helpers also check required CLI capabilities. Acquisition remains explicit, with no universal tool installer or automatic upgrade. Trivy is Apache-2.0 licensed. Record the selected release, advisory review and exercised formats with each local qualification. Requalify the native reports and failure controls when changing the pin, rather than just editing the version file.

The [local artifact scan procedure](../contributing/build-and-release.md#scan-local-images) distinguishes image selection, database acquisition and advisory freshness. Root `make security` scans source dependencies, secrets and deployment configuration. `ui-deps-check` scans the complete UI development lockfile; image scans assess the shipped Go binaries and the UI's retained production inventory. Their scopes are complementary.

## UI runtime and test inputs

React and its DOM renderer are MIT-licensed browser dependencies. openapi-fetch is MIT-licensed. TypeScript, Vite, Vitest, Prettier and openapi-typescript are pinned build/test inputs, not runtime Node dependencies. The lockfile overrides the generator's js-yaml to patched 4.3.2. The Go-host dependencies appear in the direct Go inventory below. The UI image includes Go and runtime JavaScript dependency licenses/notices under `/licenses`. Use `make ui-sbom` for the image-bound Go and production JavaScript inventory.

The UI pins [jsdiff 9.0.0](https://github.com/kpdecker/jsdiff/tree/db0b12ace208da7fd741cf96d97f009347a9eaa8) (`diff` on npm), published April 13, 2026, under BSD-3-Clause. It has no transitive dependencies. Revision comparison uses bounded line and character differences on verified original text, without parsing or reformatting JSON. Its license is included in browser and image notices. The [UI guide](../../../../../catalog-ui/README.md#document-comparison-limits) describes work and rendering limits.

Knip 6.43.0 is the ISC-licensed development analyzer installed by `ui-install`. `knip.json` preserves browser/lazy entries, tests and external Make/Containerfile roots while detecting unused helpers and test-only application modules. Generated schema exports/types, the compiler-consumed JSX runtime exports and the framework-consumed Playwright configuration export are the reporting exceptions; their files remain analyzed. The JSX bridge binds React's original public functions and Fragment for compact production calls; it adds no package and preserves automatic lazy chunks. Vite development retains React's stock development runtime. Checks inspect imports without loading the guarded Playwright configuration. See [UI dead-code analysis](../contributing/testing.md#ui-dead-code-analysis) for review, limits and functional tool qualification; no automatic removal or runtime dependency is added.

ESLint 10.13.0, @eslint/js 10.0.1, typescript-eslint 8.72.1, eslint-plugin-react-hooks 7.1.1 and globals 17.13.0 are pinned MIT-licensed development tools. They add static JavaScript/TypeScript and hook checks without runtime instrumentation or React Compiler configuration. The [typescript-eslint setup](https://typescript-eslint.io/getting-started/) and [React hook rules](https://react.dev/reference/eslint-plugin-react-hooks) describe the maintained checks. ESLint requires Node 22.13+, within the pinned Node 22.23.4 build toolchain. Make-based Go analysis and coverage use explicit project source roots so Go examples bundled inside npm packages are not counted as Catalog code.

Playwright Test 1.64.1 is Apache-2.0 and `@types/node` is MIT; both are development-only lockfile inputs. The Chromium download is an upstream test tool with its own third-party notices, not a Catalog image asset or part of the shipped JavaScript. Installation is explicit, and ordinary builds require no browser download. See [Playwright fixtures](https://playwright.dev/docs/test-fixtures) for the setup/teardown model.

eslint-plugin-playwright 2.12.1 and eslint-plugin-testing-library 7.16.2 are MIT-licensed development tools compatible with ESLint 10. `@axe-core/playwright` 4.14.0 and its axe-core engine are MPL-2.0 test dependencies, injected only into owned browser-test pages. They are not imported by application source or included in runtime dependency notices. No production accessibility instrumentation or external scanning service is added.

`@vitest/coverage-v8` 5.0.5 matches Vitest and produces source-mapped coverage without adding instrumentation to production assets; see the [Vitest coverage guide](https://vitest.dev/guide/coverage.html). React Testing Library 16.3.3 and DOM Testing Library 10.4.2 provide DOM-oriented assertions; jsdom 30.1.3 provides the simulated DOM and supports the pinned Node 22.23.4 build toolchain. These MIT-licensed packages are test-only. Tests substitute bounded API responses, use controlled promises/timers, clean up rendered roots and never execute Catalog documents or contact external providers. jsdom does not prove layout, modal focus trapping, real navigation or OIDC interoperability; those remain Playwright/provider checks.

Use opt-in `make ui-deps-check` for a local full-lockfile advisory scan. External `make ui-deps-audit` submissions are maintainer-only. `ui-check` and `ui-test` remain separate local checks. Reports, scope, failure codes and the update procedure are documented in [UI dependency maintenance](#ui-dependency-maintenance).

## UI dependency maintenance

The UI pins Node 22.23.4 in `catalog-ui/.nvmrc` and uses separately pinned npm 12.2.2 locally and in the digest-pinned frontend build stage. `packageManager` records the selection but does not install or enforce it: verify `node --version` and `npm --version`. Node is not shipped in the scratch UI image. The documentation project owns a separate toolchain and lockfile.

The Knip closure, including optional native OXC bindings, declares MIT, ISC, BSD-3-Clause and 0BSD licenses. Its direct version and role are listed above.

These are UI analysis/build inputs, outside the Go import allowlist and production JavaScript inventory. Install with `make ui-install`, which uses `npm ci --ignore-scripts --no-audit --no-fund`. Checks use installed packages without acquiring tools. Review complete lockfile changes, platform bindings, license notices and both advisory reports when changing this pin. Qualify the actual analyzer fixtures and [two scan scopes](../contributing/testing.md#ui-dead-code-analysis); validate frontend image installation and production notices through `ui-container-build`, `ui-container-inspect` and `ui-sbom`. Host qualification and an image build do not imply execution on every optional binding platform.

Run the opt-in local scan from the repository root:

```sh
make ui-deps-check
```

`ui-deps-check` requires installed Trivy; `TRIVY=/absolute/path/to/trivy` selects a different executable. It scans the UI lockfile with development dependencies included, retains all vulnerability severities and verifies representative locked runtime/build/test packages before applying the HIGH/CRITICAL threshold through native report conversion. The exact Trivy version in `test/packaging/trivy.json` (currently 0.76.1) is required; another version fails before scanning.

Normal Trivy database freshness handling can download an update; a cached database still valid by Trivy's policy need not be downloaded again. `TRIVY_CACHE_DIR` is supported.

Other `TRIVY_*` settings, ambient config and ignore files are deliberately excluded so they cannot silently narrow the scan, suppress findings or switch to a remote scanner. No offline or suppression mode is exposed by this target.

The maintainer can separately run `make ui-deps-audit`. It sends dependency metadata to the configured registry. Agents must leave this external submission unrun. The target reads the lockfile with dev, optional and peer dependencies explicitly included even under ambient production settings. It retains all severities and fails on high/critical findings.

Neither target installs packages, executes lifecycle scripts or changes manifests. Neither needs `node_modules`, a browser, PostgreSQL or a running Catalog. Normal UI static/unit checks do not contact either advisory service.

Each run prints a unique private report directory under ignored `output/ui-dependencies/`. `run.json` records scope, the lockfile SHA-256, tool version, last phase, native command statuses and outcome; Trivy runs also record vulnerability-database metadata. `trivy.json` retains the full scanner report and `summary.txt` the threshold-filtered display; successful npm advisory requests retain `npm-audit.json`, including reports with findings. Exit 0 means the check passed, 1 means high/critical findings and 2 means a tool, report or input-integrity error. Trivy conversion internally reserves code 10 for findings to distinguish its operational errors. Review the phase and last command status when a run fails.

Raw stderr and invalid registry replies are not copied into diagnostics because they may contain credentials.

Missing tools, invalid reports and registry outages are failures, not clean scans; input changes during a run invalidate its result without reverting the files.

For reviewed updates, run the following inside `catalog-ui/`:

```sh
npm outdated --json --long
npm view react@19.3.1 engines peerDependencies license
npm explain js-yaml
```

`npm outdated` can exit nonzero when updates exist; a valid update report is informational, while a registry error must be investigated. With exact pins, `wanted` can equal the installed version despite a newer `latest` tag. Review selected releases, peers, engines, licenses and transitive changes; update exact pins deliberately and use `npm ci --ignore-scripts --no-audit --no-fund` for clean acquisition. Do not run `audit fix --force`, relax peers or automatically accept regenerated lockfiles or visual baselines. Retain TypeScript 5.9.3 while the API generator requires `^5.x`, and js-yaml 4.3.2 while the generator's Redocly dependency still pins 4.3.1. npm 12.2.2 is installed explicitly in the frontend build stages; native SDK/extension installers retain it separately from the checksummed Node distributions.

After selected updates, run the complete-lock local scan, `make test-ui` and `make ui-visual`. Inspect any changed screenshots. External registry audits remain maintainer-only.

For build/runtime/login changes, also run `make test-ui-compose test-ui-oidc`, rebuild the UI image, and use `make ui-container-inspect ui-sbom` with its matching image metadata. Preserve production dependency notices and lockfile/asset hashes. Update affected setup/command/test documentation and repeat source/site checks. Keep image/tool upgrades separate from changes to application behavior.

The [npm audit documentation](https://docs.npmjs.com/cli/v10/commands/npm-audit/) describes disclosure and failure thresholds; [Trivy's npm scope](https://trivy.dev/docs/v0.74/guide/coverage/language/nodejs/#npm) explains why development dependencies need explicit inclusion. These checks assess known advisories in the lockfile graph, not malicious packages, every bundled/native/browser binary or the Node/Alpine build image. npm and Trivy inventory counts differ and are not coverage percentages. They complement the existing root source/security scan and image checks; they do not replace license review or the build-bound UI SBOM.

## SDK dependency inventories

Each SDK owns its runtime requirements and locked maintainer tools. `sdk-check` inventories the selected tools and runtime licenses; package consumers need the runtime dependencies. The default selection checks all four SDKs. Root application imports, UI npm checks and SDK checks have separate scopes.

`make security` projects every registry package from the shared and Python SDK uv locks into temporary scanner inputs. It excludes only the exact named local SDK/tool projects, which have no PyPI advisory identity. Their locked third-party dependencies remain included. Trivy also scans the repository's nested Go/npm manifests. SDK Go imported licenses and full-lock npm advisories still use their explicit language checks. Scanner failure or stale advisory data is not a clean result. No scanner input is copied into a package.

## JavaScript/TypeScript SDK dependencies

The [ESM package](../../../../../sdk/typescript/README.md) owns [package.json](../../../../../sdk/typescript/package.json) and its lockfile. Runtime and declaration dependencies are MIT-licensed [openapi-fetch](https://openapi-ts.dev/openapi-fetch/) 0.17.0 and openapi-typescript-helpers 0.1.0. No React or UI-host dependency is required. Consumer installations do not need development dependencies.

MIT-licensed [openapi-typescript](https://openapi-ts.dev/introduction) 7.13.0 generates declarations from the complete unchanged OpenAPI. Apache-2.0 TypeScript 5.9.3 compiles ESM and declarations. Pinned ESLint, Prettier, Playwright and YAML are separate maintainer tools. The js-yaml 4.3.2 override preserves the reviewed generator fix. Install through `make sdk-tools-install SDK_LANGUAGE=typescript`; generation and checks use installed inputs. [Verification](../contributing/testing.md#javascripttypescript-sdk-verification) inventories the closure and tests the actual tarball. Root application Go and UI-specific npm scans do not replace this independent lockfile review. Scan this lock locally when inputs change. The external `npm --prefix sdk/typescript audit --package-lock-only --ignore-scripts` submission is maintainer-only.

## Python SDK dependencies

The independent [Python SDK](../../../../../sdk/python/pyproject.toml) declares [HTTPX2](https://github.com/pydantic/httpx2), Pydantic, python-dateutil and typing-extensions as consumer requirements. It supports Python 3.12-3.14. The maintainer [uv lock](../../../../../test/sdk/python/uv.lock) fixes their qualified versions separately from generator, build and test tools. The runtime closure uses MIT, BSD-3-Clause, Apache-2.0 and PSF-2.0 licenses. python-dateutil 2.9.0.post0 supplies Apache-2.0 or BSD-3-Clause terms. PSF-2.0 applies to typing-extensions; this Python-specific permissive license does not change the Go allowlist. No MPL package or certifi dependency is required.

[OpenAPI Generator](https://openapi-generator.tech/docs/generators/python/) 7.26.0 is an Apache-2.0 execution-only generator. Java, the generator and test/build tools are not consumer dependencies. The package includes its license and attribution for runtime template code. Local builds do not redistribute the generator JAR. [SDK verification](../contributing/testing.md#python-sdk-verification) checks runtime licenses and the complete installed tool inventory. Review current advisories separately whenever locks change; a root Go scan does not assess this Python closure.

## Optional LangGraph example dependencies

The [discovery assistant example](../../../../../examples/langgraph/README.md) owns a separate dependency-only `pyproject.toml` and `uv.lock`. It selects MIT-licensed [LangGraph](https://docs.langchain.com/oss/python/langgraph/overview) 1.2.18 and [langchain-ollama](https://docs.langchain.com/oss/python/integrations/chat/ollama) 1.1.0. The local Python SDK remains the maintained source dependency. Framework and model packages are absent from the SDK wheel, Catalog binaries and UI/backend images.

Explicit `make langgraph-tools-install` uses uv 0.13.6 and installed Python 3.12-3.14. The example records its release cutoff and exact security exceptions in `pyproject.toml`. Checks use installed inputs without automatic updates. Review the complete resolved closure when changing it. `test/langgraph/inventory.py` verifies installed versions against the lock and rejects unreviewed license metadata.

Most packages use permissive MIT, BSD, Apache or PSF terms. Two exact execution-only exceptions remain in this isolated example: certifi 2026.10.19 under MPL-2.0, and orjson 3.13.0 under `MPL-2.0 AND (Apache-2.0 OR MIT)`. Do not describe this environment as wholly permissive. These exceptions do not broaden the Go license allowlist or Python SDK consumer requirements. Preserve upstream notices; redistributing this environment or its dependency sources requires a separate review.

Model runtime and weight acquisition are separate prerequisites. The [example guide](../../../../../examples/langgraph/README.md) identifies the selected local runtime/model and resource limits. Neither model files nor the optional Python environment become application release artifacts. Use the [separate unit, HTTP and real-model targets](../contributing/testing.md#optional-langgraph-discovery-example) after dependency changes. License inventory is not an advisory scan or proof of model safety.

## Rust SDK dependencies

The [Rust crate](../../../../../sdk/rust/Cargo.toml) owns compatible dependency ranges and a qualified Cargo lock. Its runtime uses Serde/serde_json, Tokio, reqwest and Rustls with reviewed features and dependencies. Those reviewed runtime dependencies use permissive licenses. Platform trust and operator private roots remain enabled. Consumers own their Cargo resolution; a library lock does not constrain another application's graph.

[openapi-to-rust 0.23.0](https://github.com/gpu-cli/openapi-to-rust/tree/505fa248da127ddb384a1012928d92f3083f592e) is an MIT execution-only generator. The package retains its MIT notice. No generator, Java, application module or other Catalog SDK is a Rust runtime dependency. Guarded source repairs are maintained in `test/sdk/rust/generation.py`; the unchanged OpenAPI remains authoritative.

The SDK, generator, analyzer, extracted-package consumer and [standalone Rust report](../../../../../examples/rust-catalog-report/README.md) use separate checked-in locks. The report adds only Serde JSON and Tokio runtime/test features already selected by SDK qualification.

The separate `make sdk-rust-audit-tools-install` command builds [cargo-deny 0.20.2](https://github.com/EmbarkStudios/cargo-deny/tree/bca0dde53651ee946720e4540b5ce2610bec8f06) from its checksummed pinned source and reviewed lock. That lock includes supported fixes for its TLS and zstd dependencies. `make sdk-rust-audit` checks licenses, sources and advisories offline. Cargo-deny is not a prerequisite for normal installation, generation, builds, tests or packaging. Normal Rust qualification does not claim a clean dependency audit.

Its policy includes the report's build/dev dependencies and unpublished crate. Allowed additional permissive licenses include MIT-0, Unicode-3.0, Zlib and CDLA-Permissive-2.0 for the selected closure. An exact analyzer dependency allows Apache-2.0 WITH LLVM-exception. Source license files and package checksums are recorded alongside SPDX metadata. The Go policy is unchanged.

One informational exception covers `RUSTSEC-2024-0449`: the pinned generator's unsafe-libyaml is unmaintained. That CLI parses only the maintained local OpenAPI file. The exception cannot apply to SDK/consumer dependencies or a new vulnerability. Tool executable advisories exclude upstream dev-only tests; full tool license inventories retain dev/build packages. Known vulnerable Rustls and yanked ChaCha versions were replaced in qualified locks. No known SDK runtime advisory is suppressed.

Audit installation records the public RustSec revision and acquisition time separately from compiler/generator installation. Audits fail after seven days, or if data is missing or changed. Crates must meet the recorded two-week release cutoff and not be yanked in the installed registry snapshot. The [Rust tool manifest](../../../../../test/sdk/rust/tools.json) lists exact approved security exceptions with their versions, advisories, registry checksums and release times. Other dependencies retain the ordinary cutoff.

Normal installation records source checksums, release ages, yanks and license metadata. License allowlists and advisories are enforced by the separate audit. Offline checks do not establish registry changes after acquisition. Refresh compiler/generator tools with `make sdk-tools-install SDK_LANGUAGE=rust`. Refresh audit tools and data with `make sdk-rust-audit-tools-install`. Checks never update tools automatically.

Rust 1.99.1 supplies rustfmt, Clippy and matched LLVM tools. Coverage uses those native tools without another Cargo coverage dependency. Rust 1.88.0 is the separately qualified consumer floor. Frozen Cargo prevents acquisition and resolution; it does not sandbox build scripts or proc macros. Maintainers need a native C toolchain for the reviewed TLS closure. See [SDK verification](../contributing/testing.md#rust-sdk-verification) for installed-package tests and supported scope.

## API validation tools

The generator retains speakeasy-api/openapi v1.25.5 and its go-yit helper: newer overlay APIs use a different YAML Node type than oapi-codegen v2.8.1. The service and scanner receiver select segmentio/asm v1.2.0, the latest reviewed MIT-licensed release; v1.2.1 uses MIT-0, outside this Go allowlist.

The independent [Go SDK module](../../../../../sdk/go/go.mod) has a separate consumer dependency closure. It declares oapi-codegen runtime v1.8.0 and YAML v3.0.6, with go-jsonmerge v2.0.0 and UUID v1.6.0 indirectly. Their imported licenses are Apache-2.0, MIT or BSD-3-Clause. SDK verification applies the existing Go license policy; root application scans alone do not cover this module. It imports neither the application nor its generator. The SDK reuses the existing generator lock below and root analyzer pins through explicit `make sdk-tools-install SDK_LANGUAGE=go`. Review SDK advisories separately when its locks change. See [SDK verification](../contributing/testing.md#go-sdk-verification).

The API tooling's separate source lint uses ESLint 10.13.0, `@eslint/js` 10.0.1 and globals 17.13.0, all MIT-licensed. These are exact pinned npm development dependencies; their added lockfile entries declare MIT, Apache-2.0, BSD-2-Clause, BSD-3-Clause, ISC or BlueOak-1.0.0. They do not change the Redocly/oasdiff execution boundary or enter Catalog images. Documentation source lint owns the same direct pins in its independent project; neither project borrows the UI installation.

Generated Go-client qualification separately pins oapi-codegen v2.8.1 and its runtime v1.8.0 (both Apache-2.0), with the existing YAML v3.0.6 decoder (Apache-2.0/MIT). The generator and consumer have separate module locks under `test/clientcompat/`; their kin-openapi closures remain independent. The generator selects kin-openapi v0.152.0, `golang.org/x/mod` v0.42.0 and `golang.org/x/tools` v0.52.0. Its go-yit pin preserves the YAML v3 node API required by yaml-jsonpath. Recheck source, test and rebuilt-tool vulnerability results when these locks change. Earlier qualification results do not establish current security status. These local test-only modules do not change service dependencies, images or the Go import allowlist. Explicit `make client-go-tools-install` acquires the tools; checks use cached dependencies, local generation and owned loopback fixtures without contacting external Catalogs. See [client qualification](../contributing/testing.md#generated-go-client-compatibility) for usage, precision/transport limits and update gates.

The [API validation workflow](../contributing/testing.md#api-validation) uses Redocly CLI 2.71.1 (MIT), yaml 2.9.1 (ISC) and oasdiff v1.35.0 (Apache-2.0). These are local execution-only development tools, isolated under `test/validation/`; the npm lockfile and nested Go module fix their dependency inputs. They are not imported by service code, included in images or redistributed as Catalog artifacts. The Go import allowlist is unchanged. Install with `make validation-tools-install`; normal checks use the installed versions without acquisition.

Redocly bundles dependencies inside its npm package, so `package-lock.json` and `npm audit` alone do not inventory or assess that full closure. Review the installed `THIRD_PARTY_NOTICES` as well: the selected artifact lists Apache-2.0, MIT, ISC, BSD variants, BlueOak-1.0.0 and a dual MPL-2.0/Apache-2.0 component. Its `unknown` group names decko and stickyfill and supplies MIT license text; retain that upstream attribution/classification limitation rather than silently relabeling it. The local-tool execution scope does not authorize redistribution of that bundle.

The oasdiff imported closure is reviewed with the repository Go toolchain and pinned go-licenses; platform assembly remains outside source license discovery's recursive inspection.

## Selected documentation tooling

[ADR-0016](../architecture/decisions/0016-static-documentation-with-starlight.md) selects the implemented, isolated Starlight project in `documentation/`. The [authoring guide](../contributing/documentation.mdx) records exact Node/npm/Astro/Starlight/OpenAPI pins, the patched multipart helper, asset notices and verification. `package-lock.json` controls clean installs and retains the narrow form-data 4.0.6 override. Use current advisory data when you review updates.

The optional PDF exporter pins Playwright and playwright-core 1.64.1, both Apache-2.0, in the documentation lock. Its matching Chromium Headless Shell is installed explicitly under `.tools/documentation-pdf/`. The browser and host fonts have separate licenses. They remain local tools and are excluded from the static site and application images. Normal site builds/checks do not launch the browser. See the [PDF procedure](../contributing/documentation.mdx#export-the-complete-manual-to-pdf).

Technical diagrams use local native SVG components and the shared theme stylesheet. They add no renderer dependency, browser installation or client-side diagram script. Mermaid is not part of the locked documentation toolchain.

The full npm closure is not permissive: Sharp/libvips includes LGPL platform binaries and Lightning CSS is MPL-2.0. They are upstream build-only tools, excluded from browser output and Catalog images/charts, not additions to the Go allowlist or approved redistributed binaries. The site generates an exact lockfile license inventory and available installed-package license texts; normal updates require review and repeat source/site/browser checks. Publication and any redistribution of build tools require their own review. No hosting service, external search, analytics, runtime Node or product readiness dependency is introduced.

The documentation lock selects `http-cache-semantics` 4.3.0 through Astro. GitHub withdrew [GHSA-ch52-4w7c-c8xp / CVE-2026-93748](https://github.com/advisories/GHSA-ch52-4w7c-c8xp) on October 28, 2026, because the reported vulnerability is invalid. That withdrawal requires no dependency change.

The retained offline controls show a separate cache-rule discrepancy when `max-stale` permits reuse without required revalidation. Withdrawal does not change that behavior. Current Astro image builds use `storable()` and `timeToLive()`, not the lookup method `satisfiesWithoutRevalidation()`. These observations do not establish a Catalog vulnerability.

Use the primary advisory's current status when interpreting older scan reports. Follow the supported dependency policy for future updates; do not patch libraries locally or force an incompatible Astro/Starlight downgrade.

Zod 4.6.6 enforces declared collection limits before it validates elements. The documentation and MCP tool locks select this MIT-licensed patch. Both locks also select Undici 8.12.3 and fast-uri 3.1.9 for the reviewed advisory fixes.

## Optional local monitoring tools

Prometheus and Grafana are optional upstream tools, used by Compose fixtures and the independently installed [monitoring companion chart](../operations/helm-monitoring.mdx), not Catalog runtime dependencies. The chart packages configuration and image references, not the upstream images. This does not change the permissive Go import policy or authorize linking, bundling, mirroring or redistributing these images with Catalog artifacts.

`deployments/monitoring/images.json` is the single pin authority for Prometheus 3.15.1 and Grafana OSS 13.2.4. Both indexes provide linux/amd64 and linux/arm64. Runtime and browser qualification covers linux/amd64.

The Grafana pin was reviewed on November 1, 2026. Grafana 13.3.0 introduces [dashboard-browsing quota errors](https://github.com/grafana/grafana/issues/136271), so 13.2.4 remains selected. The retained pin passes the owned Compose, Minikube and browser checks. Local Docker caches may retain upstream layers after disposable test cleanup.

These two tools use a lightweight maintenance rule rather than the complete artifact-inventory/security-review gate described for other external test images above. Check the selected upstream licenses, use suitable latest stable releases with exact digest pins, and run affected `make test-monitoring` checks after updates. Check Grafana login, dashboard selectors and import/update in an owned browser fixture after a Grafana update. Check version/license information within two weeks of selecting or refreshing a pin; the release itself can be older. There is no separate security-review or exhaustive bundled-component inventory task, and no requirement to rebuild upstream products.

Known findings may inform ordinary updates. Optional local tools do not require a separate release approval process. A future redistribution or production-bundling requirement needs its own scope decision.

Prometheus uses [Apache-2.0](https://github.com/prometheus/prometheus/blob/v3.15.1/LICENSE). Grafana uses [AGPL-3.0](https://github.com/grafana/grafana/blob/v13.2.4/LICENSE). Whole images contain additional third-party components. Do not describe either source license as its complete image license.

Refresh platform inventories, vulnerability findings and scan limitations when you update the selected images. Do not treat a historical scan as production or redistribution approval. SRE chooses production monitoring independently.

<!-- markdownlint-disable MD033 -->
<a id="monitoring-artifact-completion-recheck-on-2026-09-07"></a>
<!-- markdownlint-enable MD033 -->

## Mutation tool

`test/packaging/mutation-tool.json` is the exact pin for the execution-only `mutago` v2.10.15 analyzer (`32a53dabf140e1e77df25598a14c971b2d6636b6`, MIT). `make test-mutation` builds it with the repository Go toolchain, verifies module checksum/build identity, executes it in an owned directory and removes the binary afterward. Go's ordinary module/build caches retain downloaded inputs. The qualified execution platform is Linux amd64 with CGO disabled; other hosts fail explicitly. The tool is not a service import, image component, release SBOM entry or distributed Catalog artifact. The Go import license allowlist is unchanged.

The reviewed linked closure contains MIT, BSD-2-Clause and BSD-3-Clause components, plus `termie/go-shutil` at `bcacb06fecae`, whose LICENSE contains nonstandard Python-or-MIT wording. Automated license classification reports that package as unknown; this is retained as a provenance limitation, not relabelled as an unqualified SPDX MIT result. This tool-specific decision accepts acquisition and local execution of the reviewed artifact only; bundling, mirroring or redistribution requires a separate review. Retain the complete package inventory, exact upstream wording, standard-library boundary and qualification evidence under ignored `output/`.

The pinned closure also includes `golang.org/x/mod` v0.39.0. Binary advisory review identifies GO-2026-6179 and GO-2026-6180 at module level, with neither affected package nor symbol linked into this tool; the tool imports `x/mod/semver`, not its sumdb verification packages. These findings are not described as patched. Tool acquisition uses the separately pinned Go toolchain. Recheck the installed artifact's full closure and current advisories when changing the pin or Go version, and run the [actual-tool controls](../../../../../test/packaging/README.md#mutation-testing) before acceptance. Counts and passing mutation results do not substitute for dependency review.

## Direct module inventory

| Module | Version | Upstream Go | License | Purpose and maintenance decision | Cancellation, streaming, and resource bounds |
| --- | --- | --- | --- | --- | --- |
| `pgregory.net/rapid` | `v1.3.0` | `1.23` | `MPL-2.0` | Test-only structured generators, shrinking and replay under the exact exception above; no nonstandard module requirements. | Bounded synthetic inputs; ordinary Make suites fix 100 checks, seed 1 and one-second shrinking; the explicit property target validates counts up to 10,000 and a two-minute package timeout. |
| `github.com/gorilla/securecookie` | `v1.1.2` | `1.20` | `BSD-3-Clause` | Optional UI host authenticated encrypted sessions; use the maintained codec instead of custom cryptography. | Mandatory encryption, immutable one-to-three-key ring, JSON serializer, absolute expiry and 3,600-byte encoded ceiling; no network or shared store. |
| `golang.org/x/oauth2` | `v0.37.0` | `1.26.0` | `BSD-3-Clause` | Optional UI authorization-code exchange and standard PKCE helpers. | Fixed issuer-origin client with cancellation, deadlines and bounded responses; no retained refresh token or automatic token source. |
| `github.com/go-jose/go-jose/v4` | `v4.1.6` | `1.24.0` | `Apache-2.0` | Offline ANS verification uses detached ES256 JWS; UI tests also use signed provider fixtures. | Bounded signed JSON and fixed algorithms, configured public keys and no network retrieval. OIDC remains in its existing verifier. |
| `github.com/go-logr/logr` | `v1.4.4` | `1.18` | `Apache-2.0` | Sanitized SDK diagnostics use its discard logger. | No I/O or background work; raw SDK messages are discarded. |
| `github.com/modelcontextprotocol/go-sdk` | `v1.8.0` | `1.25.0` | `Apache-2.0 AND MIT` | Official [Go MCP SDK](https://github.com/modelcontextprotocol/go-sdk/tree/v1.8.0) for `catalogctl mcp serve` over stdio and the optional stateless HTTP Control MCP endpoint. HTTP defaults off. Neither transport invokes discovered MCP Servers. | Input frames are bounded to 256 KiB. Closed arguments, API bytes, result bytes, concurrency and cancellable per-call deadlines have separate bounds. SDK diagnostics are discarded; operator credentials use the shared fixed-origin Control client. |
| `go.opentelemetry.io/otel` | `v1.47.0` | `1.26.0` | `Apache-2.0` | Official tracing attributes, status and sanitized diagnostic hook. | No global provider or propagator; fixed safe attributes and explicit startup. |
| `go.opentelemetry.io/otel/sdk` | `v1.47.0` | `1.26.0` | `Apache-2.0` | Process-owned sampling, resource identity and non-blocking bounded batch processor. | Finite queue, batch, attributes, links and flush; collector loss cannot block product work. |
| `go.opentelemetry.io/otel/trace` | `v1.47.0` | `1.26.0` | `Apache-2.0` | Official span and context contracts behind the domain tracing port. | Context-scoped synchronous propagation and one private durable cause; no baggage. |
| `go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp` | `v1.47.0` | `1.26.0` | `Apache-2.0` | Standard HTTP protobuf trace export and SDK retry semantics. | Isolated hardened client, verified TLS, fixed endpoint, no redirects/proxies, bounded requests/responses/deadlines and sanitized errors. |
| `go.opentelemetry.io/proto/otlp` | `v1.11.1` | `1.26.0` | `Apache-2.0` | Official OTLP wire bindings for exporter dependencies and capture assertions. | Decoding is bounded by test capture limits and the production response-size ceiling; generated bindings do not initiate I/O. |
| `github.com/coreos/go-oidc/v3` | `v3.21.0` | `1.25.0` | `Apache-2.0` | `internal/platform/authn` uses it only for configured-issuer discovery and JWK-backed access-token signature and claim verification. The service-owned boundary then resolves the verified issuer, subject, and optional organization key through Catalog identity state; token-carried tenant and role claims cannot grant authority. | Startup and request contexts cancel discovery and verification work. Tokens are bounded to 32 KiB; duplicate-key-safe discovery and JWK responses are bounded to 1 MiB; an HTTPS issuer cannot downgrade retrieval; redirects are bounded; rotation forces one refresh; and a known key can bridge provider outage for at most 15 minutes. |
| `github.com/getkin/kin-openapi` | `v0.152.0` | `1.25` | `MIT` | API and HTTP contract tests use it to parse and validate the embedded OpenAPI document and typed responses. Retain as a test-only contract dependency; it is not linked into release binaries. | Validation receives a context, external references are not enabled, and work is bounded by the checked-in contract and test response bytes. It performs no runtime network or streaming work. |
| `github.com/jackc/pgx/v5` | `v5.11.0` | `1.25.0` | `MIT` | `internal/platform/database` and the runtime composition root use it as the PostgreSQL driver and pool. Retain as the single production database client behind repository boundaries. | Repository calls accept contexts, pool configuration bounds connections, and row iterators are closed. Local and indexed Catalog search, eligible live-source selection, and administration pages are bounded in PostgreSQL. PostgreSQL is the only required external persistence stream. |
| `github.com/piprate/json-gold` | `v0.9.1` | `1.21` | `Apache-2.0` | `internal/standards` uses its JSON-LD 1.1 processor for ARD normalization and query-term resolution. Retain instead of maintaining a partial JSON-LD implementation. | The library receives in-memory documents and no raw HTTP client. Remote loading is possible only through the service callback, which uses the hardened fetch boundary, and normalization allows at most eight remote contexts plus existing fetch and document-size limits. |
| `github.com/santhosh-tekuri/jsonschema/v6` | `v6.0.3` | `1.21` | `Apache-2.0` | `internal/standards` uses it for Draft 2020-12 validation of pinned ARD and MCP schemas. Retain as the single runtime JSON Schema implementation. | Schemas are embedded, registered locally, and compiled once; external retrieval is not enabled. Validation is in-memory and bounded by service document limits, with no network or streaming interface. |
| `go.yaml.in/yaml/v3` | `v3.0.6` | `1.16` | `MIT` | `internal/adapter` uses it for typed Agent Skills Markdown frontmatter. Retain for the constrained YAML subset instead of writing a parser. | Parsing is in-memory with no external resolution or stream, and the representation plus extracted fields are bounded by service limits. Strict typed validation rejects unsupported structures after decoding. |
| `golang.org/x/crypto/x509roots/fallback` | `v0.0.0-20261020153306-9f2b28d4331a` | `1.26.0` | `BSD-3-Clause` | Both command entry points blank-import the maintained public root set required by the scratch image contract. Retain and update deliberately with public-HTTPS and binary contract evidence. | Initialization loads a fixed embedded root set; it performs no network operation, accepts no input stream, and has a build-bounded memory footprint. |
| `golang.org/x/net` | `v0.62.0` | `1.26.0` | `BSD-3-Clause` | `internal/federation` uses its HTML parser for inert discovery metadata. Retain the standards-aware parser instead of a custom HTML parser. | It parses only bytes already bounded and retrieved through the hardened fetcher, performs no network request itself, and discovery depth, links, entries, and response bytes have explicit limits. |
| `golang.org/x/sync` | `v0.25.0` | `1.26.0` | `BSD-3-Clause` | `internal/jobs` uses `errgroup` for worker supervision and cancellation. Retain the small concurrency primitive rather than duplicating coordination logic. | The derived context cancels sibling loops on failure; configured worker concurrency, claim batches, leases, and shutdown bounds cap goroutines and work. It carries no data stream. |
| `google.golang.org/genproto/googleapis/api` | `v0.0.0-20261026165753-2ffab32bce7e` | `1.26.0` | `Apache-2.0` | The pinned generated A2A binding imports Google API annotations from this module. Retain only as required by regenerated upstream protocol code. | It contributes generated descriptors and annotations only, performs no I/O, exposes no application stream, and is bounded by the checked-in protocol binding. |
| `google.golang.org/protobuf` | `v1.36.12` | `1.23` | `BSD-3-Clause` | `internal/adapter` and the pinned A2A binding use ProtoJSON and protobuf runtime descriptors. Retain as the canonical runtime for that generated contract. | ProtoJSON operates on in-memory representation bytes already capped by service request limits; it performs no network I/O and exposes no unbounded application stream. |
| `github.com/cyberphone/json-canonicalization` | `v0.0.0-20261011152517-32a48195b9b2` | `not declared` | `Apache-2.0` | RFC 8785 canonicalization for signed ANS event and envelope JSON. | In-memory bytes pass duplicate/Unicode/size checks first; ordinary ARD admission is unchanged. |
| `github.com/fxamacker/cbor/v2` | `v2.9.6` | `1.20` | `MIT` | Strict CBOR prevalidation before COSE verification. | Finite bytes, nesting, maps and arrays; duplicate keys and indefinite lengths rejected. |
| `github.com/ldclabs/cose` | `v1.4.0` | `1.25.0` | `MIT` | Attached COSE Sign1 verification for the fixed ANS profile. | P-256/ES256 and required protected fields checked before library calls; no embedded-key authority or I/O. |
| `github.com/transparency-dev/merkle` | `v0.0.2` | `1.19` | `Apache-2.0` | RFC 6962 SHA-256 inclusion verification. | At most 64 siblings against an independently authenticated checkpoint. |
| `github.com/google/uuid` | `v1.6.0` | `not declared` | `BSD-3-Clause` | Parse ANS subject identifiers before exact receipt-path matching. | Bounded in-memory input; no generated identifier or I/O. |

## Change procedure

Tracing uses the official OpenTelemetry SDK/exporter. Although the HTTP exporter brings gRPC-related modules transitively, Catalog exports traces only through OTLP/HTTP and does not enable ambient OpenTelemetry configuration.

For any direct or transitive dependency change, review `go.mod` and `go.sum`, update the applicable direct row, and run `make module-check`, `make license-check`, tests for the affected packages, `make check`, `make verify`, and `make security` with current vulnerability data. A transitive-only change does not add a row, but it must still pass the complete-closure license and vulnerability gates. Do not add a license exception to make a build pass; replace the dependency or obtain an explicit policy decision first.

## Generated API test tooling

Schemathesis 4.32.5 and its locked wheel closure are exact-version/hash locked in `test/apigenerated/tools.txt`. They are installed only by `schemathesis-tools-install`, used by the explicit PostgreSQL-backed `test-api-generated` pilot, and excluded from product build contexts and ordinary Go units.

The [pilot guide](../../../../../test/apigenerated/README.md#maintenance) records the license inventory, including execution-only MPL-2.0 certifi/Hypothesis, qualified Python/platform and pin-update procedure. Keep upstream notices in installed packages; this does not expand the shipped Go dependency allowlist.

## ZAP execution-only tooling

`test/security/tool.json` binds ZAP 2.17.0 to an immutable image digest and exact installed add-on inventory. Acquisition is explicit through `zap-tools-install`; scan targets do not update add-ons or pull a missing tool. The unmodified external image and its notices remain outside shipped artifacts and the Go license allowlist. See the [harness maintenance policy](../../../../../test/security/README.md#maintenance) for source-license attribution, image-closure limits and required requalification.

## Independent Skill reference tool

`test/interoperability/skills/provenance.json` pins unchanged skills-ref 0.1.0 source at Agent Skills commit `69ef37e9424c0a7ea9dd2293b559e43ec8176379`, matching the existing specification profile. Its separate hashed build/runtime lock and retained Apache-2.0 notice are test-only. `skill-tools-install` verifies every installed reader source file and records runtime package licenses in a digest-pinned Python image; packaging tools and pip are removed afterward. `test-interoperability-skills` executes offline without model access or package execution. The [harness guide](../../../../../test/interoperability/skills/README.md) owns exact prerequisites, license boundaries and dependency regeneration. This is independent from root Python development tools and application dependencies.

## Optional portable Plugin qualification tool

`make plugin-tools-install` installs an unchanged, source-verified OpenHands SDK in a separate pinned local test image. Its hash-locked dependency closure is isolated from application Go/npm dependencies. The loader runs offline with only owned read-only packages; no model, provider or proxy is invoked. The lock selects LiteLLM 1.103.4 within the SDK requirement to address CVE-2026-84377. The complete closure includes `lmnr-claude-code-proxy` 0.1.24 under proprietary internal-use terms; do not redistribute this test image as an open-source bundle. Root application license gates are unchanged. See the Plugin section of the [testing guide](../contributing/testing.md#independent-portable-plugin-loading) and its linked harness for pin, inventory and license details.

## Optional VS Code client and SDK runtime

The TypeScript SDK owns Node 24.22.1/npm 12.2.2 development and checksum-pinned compatibility runtimes. Its independent installer preserves the user's default Node and other npm projects. The extension build installer reuses that reviewed primary Node/npm pin in its own tool directory; it does not require the SDK maintainer's Go, compatibility runtimes or Chromium once a verified SDK archive exists. The optional [VS Code client](../../../../../extensions/vscode/README.md) consumes the locally packaged SDK. It does not copy generated declarations. Extension runtime code is excluded from backend and UI builds; the UI image packages only its published guide and screenshots.

The extension bundles the Apache-2.0 [A2A JavaScript SDK 1.3.0](https://github.com/a2aproject/a2a-js/tree/v1.3.0) client for explicit A2A 1.0 JSON-RPC conversations. Its optional Express/gRPC dependencies are not installed or shipped. The pinned unchanged Hello World executor is acquired only for local functional tests.

The extension also bundles MIT-licensed JOSE 6.2.13 for OIDC signature verification and Undici 8.12.3 for fixed connections, private trust and explicit proxies. The transport explicitly uses HTTP/1.1 for direct requests and both proxy hops. This preserves its response-header limits. The Catalog SDK is Apache-2.0; JOSE and Undici retain MIT notices.

The separate locked build/test closure includes official VS Code test and VSIX tools, TypeScript, esbuild, ESLint and Prettier. VS Code 1.143.1 is acquired only as an execution-only test host. It is not distributed in the extension.

VSCE also installs the build-only `@vscode/vsce-sign` 2.1.0 helper under Microsoft's VSCE-SIGN software terms. Those terms permit development and testing with Visual Studio products and services; this component is not permissively licensed. Local unsigned packaging does not invoke the helper or include it in the VSIX. Runtime notices and build/test license inventories therefore have separate scopes.

`vscode-tools-install` also acquires checksum-pinned [ORAS 1.3.6](https://github.com/oras-project/oras/releases/tag/v1.3.6) under Apache-2.0 for local OCI package tests. The installer retains its license and verifies the executable before qualification. Git remains a system test prerequisite. Neither executable ships in the VSIX. Users supply Git or ORAS separately for explicit Skill installation; activation does not acquire tools.

Scan the complete extension lock locally, including development packages. External npm registry audits remain maintainer-only. Build runtime notices from the actual bundle inputs. Lock changes require explicit installation, `vscode-check`, packaged local qualification and documentation checks. See [the harness](../../../../../test/vscode/README.md) for installation, artifact identity and unit/functional boundaries.
