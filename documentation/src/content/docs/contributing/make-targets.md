---
title: Make targets and development tools
---

Run Make workflows from the repository root of a source checkout. Build and test tools are prerequisites for these workflows. An installed CLI user follows the [CLI reference](../reference/commands.md) and does not need every development tool.

This page indexes maintained workflows. Their owning build, testing and operations guides state prerequisites, authority and cleanup. Make targets build and verify local artifacts. External publication and uploads remain maintainer-only.

## Maintained Make workflows

Run `make` or `make help` to list public commands in workflow groups. Section headings use emoji and descriptive text; target names remain copyable ASCII and redirected output contains no color escapes. The root Makefile follows the same section order, with a source index, scoped configuration blocks and nearby `.PHONY` declarations. Maintainers add public commands to an existing `##@` section with an inline `##` description; internal supervisor workflows remain outside public help.

Use the [testing strategy and quality checks](../contributing/testing.md#start-here) to choose between source linting, isolated units, local or packaged functional tests, schema validation, standards conformance, coverage and performance measurement. It identifies each layer's prerequisites and the checks included in aggregate targets.

Use `make coverage-full` alone to run fresh unit, local functional, database and application-process coverage, enforce the existing suite floors and require at least 80 percent combined statement coverage per Go application file. Its report is `coverage/full.txt`. For the focused real PostgreSQL CLI/API/worker boundary, use `make test-process-postgres` or instrument it with `make coverage-process-postgres-profile`. These commands own disposable database fixtures; see [coverage accounting](../contributing/testing.md#coverage-accounting).

Use `make test` or `make test-functional-local` for the broad local Go suite, including untagged units, functional-tagged cases, tooling tests and composed in-memory workflows. `make test-unit` runs isolated Go tests across owned roots, including Minikube helpers through their existing tag without a cluster. `make test-tooling-unit` selects isolated Python helpers in their owning directories; Node projects expose their own unit scripts. Run that target alone; it bypasses `.env` before inclusion. Neither selection needs PostgreSQL or Helm. `make verify` runs the broad local suite with race detection and separately with coverage, plus static, conformance and binary checks.

Use `make helm-package-test` for the focused real-Helm packager tests or `make helm-check` for the complete cluster-free chart gate.

The [reviewed upstream resource corpus](testing.md#reviewed-upstream-resource-fixtures) uses the existing unit and local functional targets. It needs no separate installer, download step or running service. `test-e2e` selects only the existing `TestEndToEnd` journey, so use `test` or the documented focused command for the corpus publication scenario.

Run `make workflow-local-tools-install` to install the pinned local act tools. Run `make workflow-local-list` to list fixed selections without downloads or containers. Execute each target alone. Jobs require clean committed source and always disable external uploads. The [local runner guide](../../../../../test/ci/README.md#local-workflow-commands) describes isolation, cleanup and current qualification limits.

Image jobs accept `WORKFLOW_LOCAL_SCAN=true|false`. Image and chart jobs accept `WORKFLOW_LOCAL_RELEASE_CANDIDATE=true|false`. The defaults enable image scanning and disable candidate checks. Local reports stay in `output/artifacts/workflows/<run-id>/`; `run.json` records execution, report collection and cleanup separately.

Run `make coverage-unit` alone for isolated application Go coverage in `coverage/unit.out`, with weighted totals, file/package summaries and its own regression floor. Broad local reports remain `coverage/default.out`, `coverage/core.out` and `coverage/index.html`; PostgreSQL and frontend profiles remain separate.

`make help` is the complete target index. The [testing guide](../contributing/testing.md) explains each verification boundary; the [local walkthrough](../guides/local-development.mdx) covers build/run/smoke; [performance and monitoring](../guides/performance-and-monitoring.md) covers benchmark, load, soak, SQL plans and owned cleanup. The [documentation guide](../contributing/documentation.mdx) covers documentation installation, builds, previews and verification. No docs target starts the application, PostgreSQL, Compose or Kubernetes.

Use GNU Make from the repository root. Pass overrides after the target, for example `make build VERSION=0.1.0 COMMIT=unknown` or `make helm-check HELM=/path/to/helm`. Add `CATALOG_ENV_FILE=/dev/null` to skip the optional workflow file. That file contains trusted Make syntax, not runtime dotenv input; only load files you trust. Its filename and parent directories must not contain `*`, `?` or an opening `[`. See [local binaries and container packaging](../contributing/build-and-release.md) for tool requirements, output locations and cleanup.

`make clean-dry-run` lists validated disposable build/dependency paths; `make clean` removes them, including npm dependencies, UI/docs output and the owned LangGraph, Gitleaks, AGNTCY, Gateway and ANS installations. Both targets refuse mounts and unreadable subtrees before removal or permission changes. These targets ignore env files and must run alone. Stop processes using those paths first; configuration, retained reports, personal virtual environments and application data remain untouched. See [cleanup scope and the LangGraph environment exception](../contributing/build-and-release.md#clean-local-builds-and-dependencies).

`make demo-up` preserves the selected workflow file and supported project, deployment and sample inputs through its child targets. Environment and workflow-file selections follow the same precedence as other runtime targets; explicit command-line assignments take priority. Follow the [sample setup procedure](../guides/sample-catalog.mdx#start-the-catalog-with-compose-and-load-sample-data) to select matching origins, ports and a private receipt before loading data.

The [Helm monitoring guide](../operations/helm-monitoring.mdx) covers `make monitoring-package`, the explicit install/status/access/uninstall commands and `make test-minikube-monitoring`. These operate on the independent companion chart, not the application chart or the Compose `LOCAL_MONITORING` selector.

For tracing, `make test-tracing` and `make test-minikube-tracing` run isolated export and cross-process causality tests. `make trace-viewer-up` starts a separate local Jaeger viewer without starting Catalog; `make trace-viewer-down` removes that viewer and discards its in-memory traces. See [tracing operations](../operations/observability.mdx#distributed-tracing) for application configuration and [local viewing](../operations/observability.mdx#view-traces-locally) for the development commands, ports and cleanup.

With installed browser tools, `make test-monitoring MONITORING_BROWSER=on` also checks native Grafana selectors, data and an owned import/update. `make test-tracing TRACING_VIEWER=on TRACING_BROWSER=on` follows the actual exported Job link in a separately started owned Jaeger viewer. Both browser phases remain optional; neither monitoring nor tracing becomes an application readiness dependency. See the [monitoring harness](../../../../../test/monitoring/README.md) and [tracing harness](../../../../../test/tracing/README.md) for evidence and cleanup.

`make test-tutorial-federation-oidc` qualifies the prepared local two-Catalog Keycloak tutorial, including explicit source credential replacement and a new complete synchronization. It requires the documented images and free tutorial ports. `make test-mcp-scanner-receiver-fuzz FUZZ_TIME=10s` selects only the independent receiver's event fuzzer, without a scanner, database or network. These are separate opt-in campaigns; see the [functional harness](../../../../../test/functional/README.md) and [receiver harness](../../../../../test/mcp-scanner/README.md).

### Scoped ZAP checks

`make zap-tools-install` downloads the pinned execution-only image. Run `make test-security-passive` or `make test-security-active` separately to build owned PostgreSQL/API/UI fixtures, qualify the scanner, inspect selected traffic and clean up. The [harness guide](../../../../../test/security/README.md) documents Linux/Docker/Playwright prerequisites, exact active scope, limits and failure policy. These targets bypass workflow env files and never accept a shared target URL.

## Optional documentation PDF

| Target | Action |
| --- | --- |
| `docs-pdf-browser-install` | Download the browser selected by the installed documentation Playwright package into `.tools/documentation-pdf/`. |
| `docs-pdf` | Export the existing completed manual to `output/documentation-pdf/catalog-manual.pdf`, without changing the site. |
| `test-docs-pdf` | Run real PDF regression and cleanup tests with the installed browser and native inspection tools. |

Follow the [PDF procedure](documentation.mdx#export-the-complete-manual-to-pdf) for prerequisites and output limits. These targets do not publish artifacts or become prerequisites of ordinary site builds.

## Version and shared development tools

`make dev-tools-install` synchronizes only the locked Python dev group into `.tools/dev`. `version` prints effective build identity; `version-check` validates the release base and pin/configuration parity. `version-bump-dry-run PART=patch` previews and `version-bump PART=patch` updates `VERSION` and derived SDK and extension release metadata in a clean checkout. Supported parts are `major`, `minor`, `patch`, `rc` and `final`. `release-check` requires clean actual source at the exact matching tag and rejects metadata overrides. Installation, bump and release checks run alone; none commits, tags, pushes or publishes. Follow [the build guide](../contributing/build-and-release.md#application-version-and-release-checks) for uv bootstrap, release ordering, fixture overrides and development provenance.

## API development checks

Use `make validation-tools-install` once before `make api-check` or the aggregate `make check`. `api-contract-check` runs the existing HTTP/event component tests; `api-validation-test` qualifies the installed lint/comparison tools. `api-diff` and `api-breaking-check` require an explicit `API_BASE=/absolute/path/to/baseline.yaml`. See [API validation](../contributing/testing.md#api-validation) for prerequisites, rule scope and input limits. These commands do not start or change a Catalog deployment.

For generated Go consumers, run `make client-go-tools-install` once, then `make client-go-check` to regenerate, compile and exercise representative reads/commands without a database. `make client-go-generate` writes an ignored inspection copy. These targets run separately, ignore deployment environment files and do not replace the CLI transport. See [Go-client compatibility](../contributing/testing.md#generated-go-client-compatibility) for the raw JSON and bounded-transport requirements.

The maintained [Go SDK](../../../../../sdk/go/README.md), [Python SDK](../../../../../sdk/python/README.md), [JavaScript/TypeScript SDK](../../../../../sdk/typescript/README.md) and [Rust SDK](../../../../../sdk/rust/README.md) share these targets. `SDK_LANGUAGE=go|python|typescript|rust|all` selects the scope; `all` is the default. Run each target alone. Deployment environment files are ignored.

The [Go publication report](../../../../../examples/publication-report/README.md) adds `webhook-report-build` and `webhook-report-check` for its separate module. The check includes Go SDK qualification, isolated example units and the existing analyzers. `test-webhook-report` runs its actual HTTPS process with an owned PostgreSQL fixture. `test-webhook-report-dsn` requires an explicitly selected disposable database and an already built example binary. These targets do not change retained Catalog data.

| Target | Effect |
| --- | --- |
| `make sdk-tools-install UV=/path/to/uv-0.13.6` | Install all SDKs' pinned tools and cached test dependencies. Only this target acquires packages. |
| `make sdk-generate` | Regenerate the selected checked-in source from the complete OpenAPI. |
| `make sdk-check` | Check drift, types, lint, licenses, units, local HTTP/TLS and clean installed-package API calls. |
| `make sdk-package` | Build local artifacts without generation, tests or publication. |

Append `SDK_LANGUAGE=go`, `python`, `typescript` or `rust` for one language. Go-only selection needs no Python SDK, Node, Rust or browser installation. Missing or stale tools fail without skipping a language. Python installation requires pinned uv 0.13.6. Packages and hash manifests use `output/artifacts/sdk/`; Rust produces a `.crate`, while Go remains a source module. See [combined SDK qualification](../contributing/testing.md#combined-sdk-qualification) for prerequisites, test boundaries and coverage interpretation.

Independent A2A handoff qualification uses `make a2a-tools-install`, then `make test-interoperability-a2a`, separately. It runs the official SDK client and unchanged Hello World executor over private HTTPS on owned loopback without external egress. It builds an owned in-memory Catalog, governs and publishes an Agent Card, then qualifies exact CLI-selected invocation, immutable evidence and Catalog/TLS/outage denials. See [A2A control testing](../contributing/testing.md#local-a2a-interoperability-control) for pins, prerequisites and reports.

Install and test each MCP selection separately:

| Installer | Test target and boundary |
| --- | --- |
| `make mcp-catalog-tools-install` | `make test-interoperability-mcp-catalog` uses the pinned Python client for Catalog Control tools over stdio and stateless HTTP. See [Catalog MCP qualification](testing.md#catalog-control-tools-over-stdio). |
| `make mcp-tools-install` | `make test-interoperability-mcp` uses Inspector/Everything and a disposable memory Catalog on owned loopback. It checks discovery into an explicit MCP connection. See [MCP control testing](testing.md#local-mcp-interoperability-control). |
| `make adk-tools-install` | `make test-interoperability-adk` qualifies native Google ADK without a model. `make test-interoperability-adk-cleanup` adds repeatable failure/SIGTERM controls. See [ADK testing](testing.md#native-adk-mcp-qualification). |

Independent HF qualification uses `make interoperability-tools-install`, then either `make test-interoperability` for clients or `make test-interoperability-hf` for owned HTTPS peers. Run each target separately. The client target creates an isolated in-memory Catalog. The peer target runs the real API and worker against owned HTTPS peers. Neither requires an HF account or calls a hosted HF catalog. See [independent client testing](../contributing/testing.md#independent-hugging-face-interoperability) for prerequisites, reports and cleanup.

## Optional LangGraph example

The [SDK-based discovery assistant](../../../../../examples/langgraph/README.md) is a separate Python client. It searches local governed releases and inspects selected exact records. It does not install or execute resources.

| Command | Effect |
| --- | --- |
| `make langgraph-tools-install UV=/path/to/uv-0.13.6` | Install the example's locked optional environment. No model is downloaded or started. |
| `make langgraph-demo` | Ask the configured local model with explicit SDK origin, bearer credentials and `LANGGRAPH_QUESTION`. |
| `make test-langgraph-unit` | Run isolated graph/tool units and example source coverage. |
| `make test-langgraph` | Add real SDK HTTP exchanges with an owned Catalog and scripted model. |
| `make test-langgraph-model LANGGRAPH_MODEL_ORIGIN=http://127.0.0.1:11434` | Qualify tools with an explicitly prepared local model and an owned Catalog. |

Run each target separately. Root Go tests and SDK package checks do not execute the optional framework. See [test boundaries](../contributing/testing.md#optional-langgraph-discovery-example) for prerequisites and interpretation. The example guide owns model setup, limits, credential handling and cleanup.

## Structured data and conformance checks

`make json-check` parses current JSON/JSON-LD with strict UTF-8 and duplicate-key rejection. `make yaml-check` lints maintained standalone YAML and syntax-checks immutable upstream YAML; Helm owns rendered templates. Both are included in `make check`. See [data-check scope](../contributing/testing.md#checked-in-json-and-yaml) for current-worktree selection and exclusions.

`make conformance-prerequisite` checks the selected Python validator and pinned ARD schema without contacting a service. `conformance-fixture`, `conformance-live` and the packaged functional workflow enforce it and use the same guarded upstream CLI. Follow [ARD conformance](../contributing/testing.md#schema-validation-and-ard-conformance) for explicit installation, mode coverage and selected-service prerequisites.

## Workflow file checks

Run `make workflow-tools-install` explicitly to acquire the pinned actionlint executable under `.tools/ci/bin/`. Run `make workflow-check` separately to check the GitHub Actions files and shell commands. Both targets bypass local Make env files and reject mixed goals. The check requires installed ShellCheck and the repository Go toolchain; it never downloads tools.

`make check` includes workflow validation. `make verify` and ordinary unit tests do not. See [GitHub Actions](../contributing/testing.md#github-actions) for manual workflow selection. These Make targets check local files; they do not dispatch a workflow or publish artifacts.

## Go unreachable-function checks

`make dead-code-report` prints all findings and their retained reasons across the shipped application and test selections. `make dead-code-check` also fails for unreviewed findings or invalid/stale exceptions and runs through `make lint`, `make check` and `make verify`. Both can acquire the pinned host analyzer through the Go cache; neither starts a Catalog, database or test environment. See [unreachable-function analysis](../contributing/testing.md#unreachable-function-analysis) for exact scopes, offline prerequisites and finding review.

## Go property testing

Run `make test-property` alone for the maintained isolated Rapid properties. `PROPERTY_CHECKS` defaults to 1,000 (maximum 10,000); `PROPERTY_SEED` defaults to 1. The target rejects incomplete generated runs and prints its retained evidence directory. The current selection covers JSON validation, authorization equivalence, lexical/hybrid cursors, federation-policy normalization, audit exports and memory-idempotency sequences; ordinary complete unit/local/race/coverage targets also execute its properties with bounded defaults. See [structured properties](../contributing/testing.md#structured-go-properties) for replay, limits and interpretation.

## Go mutation measurement

Run `make test-mutation MUTATION_PACKAGE=./internal/jsondoc` alone to measure assertion strength in one reviewed Go unit package. The other accepted selections are `./internal/identity`, `./internal/platform/idempotency`, `./internal/platform/fetcher`, `./internal/platform/authn`, `./internal/catalog`, `./internal/platform/httpserver`, `./internal/audit`, `./internal/adapter`, `./catalog-ui/internal/web`, `./internal/webhook`, `./internal/publication` and `./internal/platform/config`.

The target acquires its pinned tool. It requires a passing test run on unchanged source before running every selected mutation. It prints the fresh JSON report path. It is explicitly invoked and is not an aggregate prerequisite or mutation-score gate. See [mutation testing](../contributing/testing.md#mutation-testing-and-assertion-strength) for prerequisites, fixed files/operators, limits, outcomes and review instructions.

## Generated API pilot

Run `make schemathesis-tools-install` to acquire the isolated pinned Python tooling. Run `make test-api-generated` to build and test an owned PostgreSQL-backed Catalog. These separate targets ignore deployment environment files. `SCHEMATHESIS_SEED=2 make test-api-generated` selects a repeatable alternate seed. The original pilot covers eight operations, with generated examples for seven. A separate authoring selection generates Resource and Version creation requests and checks their retained state. See [generated API testing](../contributing/testing.md#generated-api-testing) for prerequisites, limits and reports.

## Functional API coverage

`make functional-coverage FUNCTIONAL_REPORTS='output/functional/run-<id>/report.json ...'` reads explicit functional reports and prints a JSON coverage summary. It does not run tests or select reports automatically. Compatible source/build/contract identities are required; failed or incomplete runs and malformed evidence produce a nonzero exit. See [operation reporting](../contributing/testing.md#read-functional-operation-reports) for denominators, dispositions and unobserved paths.

## Local SonarQube

Run each target separately; these opt-in targets bypass Catalog workflow env files and do not join the default gates. Follow the [testing guide](../contributing/testing.md#optional-local-sonarqube-dashboard) for prerequisites, licensing and coverage interpretation.

| Target | Effect |
| --- | --- |
| `sonarqube-tools-install` | Acquire pinned Community Build and scanner images. |
| `sonarqube-up` | Start a loopback-only local dashboard; `SONAR_PORT` defaults to 19000. |
| `sonarqube-scan` | Ingest current source; `SONAR_GO_COVERAGE`, `SONAR_JS_COVERAGE` and `SONAR_TOOLING_REPORTS` explicitly select coverage inputs. |
| `sonarqube-status` | Check the owned server's pinned version/readiness and print its URL. |
| `sonarqube-credentials` | Display the saved dashboard URL, username and password in a private terminal; works without a running server. |
| `sonarqube-down` | Stop the owned service/network, retaining analysis volumes and private credentials. |
| `coverage-tooling-python` | Instrument the existing owner-isolated Python units and publish native XML plus a source-bound receipt. |
| `coverage-tooling-node` | Measure native scripts with `TOOLING_NODE_PROFILE=ui-unit`, `docs-unit` or the complete packaged `compose` selection. |

The default dashboard is `http://127.0.0.1:19000/dashboard?id=open-agentic-catalog`. See [SonarQube login](../contributing/testing.md#log-in-to-sonarqube) for the generated admin password and [coverage imports](../contributing/testing.md#scan-and-import-coverage) for the complete commands.

## UI checks

`make ui-dead-code-report` reports unused files, exports and dependencies in both the full and production browser scopes with the locally installed pinned Knip; `make ui-dead-code-check` enforces both scopes and is included in `ui-check`. Both fail on tool/configuration errors and never install tools or delete code. Install through `make ui-install`; see [UI dead-code analysis](../contributing/testing.md#ui-dead-code-analysis) for scope and review rules. The UI Go host remains in `dead-code-check`.

`make ui-deps-check` scans the complete UI lockfile with installed Trivy (`TRIVY=/path/to/trivy` selects an executable). The separate `make ui-deps-audit` sends dependency metadata to npm's configured registry and is maintainer-only. Agents leave that external submission unrun. Both include development dependencies, fail on high/critical advisories or tool/report errors, and retain private reports under `output/ui-dependencies/`. They require no installed UI packages, build or running service and do not modify manifests. See [UI dependency maintenance](../reference/dependencies.md#ui-dependency-maintenance) for scope, error handling and updates.

For the optional UI, run `make ui-lint` for read-only JavaScript/TypeScript, React hook, typed promise, selected test-framework and formatting checks, `make ui-check` for all frontend static checks, and `make test-ui` for static checks, unit tests and browser journeys including rendered accessibility. `make ui-visual` separately compares the reviewed screenshot baselines; `make ui-visual-update` explicitly regenerates them for image review, never as a routine pass/fail gate. Install pinned tools with `make ui-install` and acquire Chromium with `make ui-browser-install`; see [UI testing](../contributing/testing.md#optional-browser-client).

Build its separate image with `make ui-container-build`. `make ui-container-inspect` checks an existing `UI_IMAGE` without rebuilding or executing it; `make ui-container-scan` scans the selected image, its Go host and its retained frontend inventory with the qualified Trivy tool and a fresh database. `make ui-sbom` adds local SPDX output with the Go host and build-bound production JavaScript inventory. Supply the matching `CONTAINER_PLATFORM`, `VERSION` and `COMMIT`; see [UI image packaging](../contributing/build-and-release.md#optional-ui-image).

## Optional staged checks

For local staged-file checks, use `make hooks-install`, `make pre-commit-check` and `make hooks-uninstall` as described in [optional pre-commit checks](../contributing/testing.md#optional-pre-commit-checks). They are development commands, independent of `catalogctl` and server configuration.

## Frontend assertion strength and benchmark comparison

After `ui-install`, run `make ui-test-property` for seeded JSON properties or `make ui-test-mutation` for the two reviewed frontend helpers. Each runs alone and bypasses workflow env files. Property stress/replay uses `UI_PROPERTY_RUNS`, `UI_PROPERTY_SEED` and `UI_PROPERTY_PATH`; mutation inputs remain fixed. See [frontend assertion strength](../contributing/testing.md#frontend-properties-and-mutation).

`make benchstat-tools-install` explicitly installs the pinned comparison tool. `make bench` records ten samples per runtime benchmark; compare two successful matched reports with `make bench-compare BENCH_BASELINE=... BENCH_CANDIDATE=...`. These are separate, report-only measurement commands; see [benchmark comparison](../contributing/testing.md#benchmark-comparison).
