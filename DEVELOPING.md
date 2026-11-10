# Developing locally

This is the contributor entry point for building and testing a checkout. Read [application context](CONTEXT.md) for a self-contained orientation, use the [README quick start](README.md#quick-start) to try the service, [Contributing](CONTRIBUTING.md) for coding and review conventions, and the [full manual](documentation/README.md) for detailed procedures.

## Choose your workflow

Run Make commands from the repository root with GNU Make. Managed builds and local workflows use Python 3.12-3.14, Git and curl. Install the additional tools for your workflow below. The backend does not require the UI or documentation toolchain.

| Workflow | Start here |
| --- | --- |
| Backend development | Go 1.27.3. `make run` starts the in-memory API/worker; `make build` writes `catalogd` and `catalogctl` to `bin/`. No Python packages are needed to compile. |
| Persistent local environment | Docker with Compose v2 and BuildKit. `make compose-up` starts PostgreSQL and the backend; add `LOCAL_UI=on` for the UI and bundled manual. No host Go or Node.js is needed. |
| Direct Docker | Build with Docker/BuildKit, or use a supplied image. No host Go is needed with an explicit build platform. Follow the [PostgreSQL procedure](documentation/src/content/docs/operations/docker.mdx) for separate roles and file inputs. |
| UI development | The backend toolchain plus Node.js 22.23.4 and npm 12.2.2, recorded in `catalog-ui/.nvmrc` and `catalog-ui/package.json`. Run `make ui-install`, then `make ui-dev` against a backend on port 8080. See the [UI developer guide](catalog-ui/README.md). |
| SDKs and client examples | Follow the [SDK overview](sdk/README.md) for the selected Go, Python, TypeScript or Rust toolchain and isolated package checks. The [LangGraph example](examples/langgraph/README.md) has its own optional Python environment and explicit local model setup. |
| Local SonarQube | Follow [setup, login and scanning](documentation/src/content/docs/contributing/testing.md#optional-local-sonarqube-dashboard). Separate Make targets start the local dashboard, ingest source/coverage and stop it while retaining history. |
| Documentation | Node.js 22.23.4 and npm 12.2.2. Run `make docs-install`, then `make docs-build` and `make docs-preview`. See [documentation authoring](documentation/src/content/docs/contributing/documentation.mdx). |

These are run/build prerequisites, not the complete lint-tool inventory. Start with the [testing strategy and quality checks](documentation/src/content/docs/contributing/testing.md#start-here) for test layers, lint tools, schema/conformance checks, coverage, browser/deployment gates and performance tests. The [build guide](documentation/src/content/docs/contributing/build-and-release.md) covers container inspection, cross-compilation, licenses and chart packaging.

For local overrides, copy `.env.example` to `.env` only if the file does not already exist. Make loads it; the binaries do not. Sample runtime profiles live in [config/](config/README.md).

Validate the selected profile with `make config-check`, and follow the [configuration reference](documentation/src/content/docs/reference/configuration.mdx) for supported settings and secret inputs.

`make demo-seed` loads sample resources through the API. The default curated profile needs a configured local Plugin HTTPS fixture. Select `DEMO_SAMPLE_SET=minimal` for samples that need no archive server. Use `make smoke` for the executable API walkthrough. Both commands create data. Follow the [sample and cleanup guide](documentation/src/content/docs/guides/sample-catalog.mdx) for fresh instances, saved receipts and extending an existing dataset.

## Check a change

Install the prerequisites in the [testing guide](documentation/src/content/docs/contributing/testing.md), the separate API tools with `make validation-tools-install`, actionlint with `make workflow-tools-install` and shared Python tools with `make dev-tools-install`. Include the pinned [Ruff executable](documentation/src/content/docs/contributing/testing.md#python-linting). For backend code changes, run:

```sh
make test-unit
make check verify
```

Run `test-unit` separately, or substitute `coverage-unit` when measuring the same isolated selection. An explicit unit-only task uses isolated selections and requested linters; the broad functional gate remains separate.

`check` runs source, API, documentation and configuration linters. `verify` requires the explicitly installed [ARD Python validator](documentation/src/content/docs/contributing/testing.md#schema-validation-and-ard-conformance) and runs the service-free Go regression gate, including race detection, coverage checks, standards conformance and binary checks. They do not launch PostgreSQL or a deployed Catalog, and neither runs the separate frontend or documentation-site gates.

For documentation-only changes, use `make check`, which includes `docs-check`, plus the site gate below when manual content changes. Choose other checks for the boundary you changed:

| Change or task | Check |
| --- | --- |
| Isolated Go package units | `go test ./internal/identity -shuffle=on -count=1`, substituting the affected package. |
| Go package units and local functional tests | `go test -tags=functional ./cmd/catalogctl -shuffle=on -count=1`, substituting the affected package. |
| UI behavior | `make test-ui` after `make ui-install` and the one-time `make ui-browser-install`. Use `make ui-test-unit` for isolated tests, `make ui-test` for local functional fixtures, and `make ui-check` for lint/type checks. |
| UI appearance or dependencies | Use `make ui-visual` for appearance or dependency changes. Use `make ui-deps-check` for local dependency scans. `make ui-deps-audit` submits dependency metadata to the registry and remains maintainer-only. Follow [UI dependency maintenance](documentation/src/content/docs/reference/dependencies.md#ui-dependency-maintenance) for update and packaging checks. |
| Reader documentation | `make docs-site-check` after `make docs-install`; `make docs-check` remains the source-only gate. |
| Local workflow files | Use `make workflow-check` for syntax. Follow [local act setup and qualification](test/ci/README.md#local-workflow-commands) for the fixed local selections. Uploads remain disabled under act. |
| PostgreSQL behavior | `make test-integration` uses an isolated disposable database. |
| Optional hybrid search | `make embeddings-check` is service-free. After explicit `make embeddings-install`, use `make test-semantic` for real-model Compose/browser/1k checks and `make test-minikube-semantic` for the replicated Helm fixture. See [setup and limits](documentation/src/content/docs/operations/hybrid-search.mdx). |
| Packaged API workflows | `make test-functional` owns and cleans separate default-off, webhook and OIDC Compose stacks. |
| Helm or deployment assets | `make deploy-check` validates charts and manifests without a cluster; select relevant Minikube scenarios from the [testing guide](documentation/src/content/docs/contributing/testing.md). |
| API or generated clients | Follow [API validation](test/validation/README.md) and [Go-client compatibility](test/clientcompat/README.md); install their locked tools explicitly and run their Make targets separately. |

The manual also covers [UI packaging and login tests](documentation/src/content/docs/contributing/testing.md#optional-browser-client), [performance testing](documentation/src/content/docs/guides/performance-and-monitoring.md), coverage reports, security checks and optional pre-commit hooks. Use those focused guides rather than running every deployment or scale scenario for every edit.

### Where tests belong

Keep Go package tests beside their source in `*_test.go` files. Untagged tests are isolated units. Tests that open listeners, execute subprocesses or compose application workflows use the `functional` build tag, including workflows backed by memory stores.

Real database tests remain beside the adapter with the `integration` build tag; cross-package memory workflows live in `internal/integration/`. Root `test/` holds broader harnesses and their own unit and functional tests; service-free does not mean unit-only.

`make test` delegates to `make test-functional-local`, running untagged and functional-tagged tests without a pre-existing deployment. `make test-unit` runs isolated Go tests across the owned roots, including Minikube helpers without a cluster. Use `make test-tooling-unit` for isolated Python helpers and the owning npm unit scripts for Node tooling.

Run `test-unit` alone; it bypasses workflow env files. `make coverage-unit` runs the same Go selection with application-only instrumentation, writes `coverage/unit.out` after validation and enforces its own coverage floor. Run it separately too.

See [test placement and selection](documentation/src/content/docs/contributing/testing.md#test-placement-and-selection) for the current boundary and fixture conventions.

## Repository map

| Path | Contents |
| --- | --- |
| [cmd/](cmd/README.md) | API/worker entry points and remote/operator commands. |
| [internal/](internal/README.md) | Domain packages, platform adapters and in-process workflows. |
| [catalog-ui/](catalog-ui/README.md) | React client, Go authentication host, separate image and UI tests. |
| [sdk/](sdk/README.md) | Independent Go, Python, TypeScript and Rust clients, with generated bindings and bounded transports. |
| [extensions/vscode/](extensions/vscode/README.md) | Optional SDK-based discovery and resource-consumption extension. |
| [api/](api/README.md) | OpenAPI, event schemas and request examples. |
| [config/](config/README.md) | Runtime configuration profiles and local policies. |
| [migrations/](migrations/README.md) | Embedded forward-only PostgreSQL migrations. |
| [schemas/](schemas/README.md) | Pinned upstream standards, checksums and embedded profile inventory. Service-owned HTTP/event schemas live in `api/`. |
| [samples/](samples/README.md) | Configurable development inventory, native assets, provenance and repeat-load rules. |
| [deployments/](deployments/README.md) | Helm charts, container compatibility files and deployment assets. |
| [test/](test/README.md) | Functional, deployment, performance and tooling harnesses. |
| [examples/](examples/README.md) | Client and integration examples. |
| [documentation/](documentation/README.md) | Starlight manual, diagrams and site tooling. |
| Optional local `todo/` | Gitignored active work, pending decisions, deferred proposals and closed tasks. |

Use [application context](CONTEXT.md#implemented-features-and-defaults) for current capabilities and [the architecture guide](documentation/src/content/docs/architecture/overview.mdx) for design context. Local trackers are optional and are not distributed with the source. When present, follow `todo/README.md` when moving tasks: preserve full entries, IDs and evidence, update links and leave no duplicate definitions. Keep detailed procedures in the manual and package responsibilities in their colocated READMEs.

Builds and tests write generated files under ignored paths such as `bin/`, `coverage/` and `output/`. Do not commit credentials, local configuration or raw test output. Reviewed fixtures and visual baselines are maintained test inputs; follow the [baseline review procedure](catalog-ui/tests/browser/snapshots/README.md) when updating screenshots.

Keep retained development data separate from disposable test fixtures; `make compose-reset` deletes development data volumes.

The cleanup targets require Linux and a readable `/proc/self/mountinfo` inventory.

Use `make clean-dry-run` to preview known build/dependency paths and `make clean` to remove them, including standard coverage files/scratch, Python caches, npm dependencies, UI/docs output, VS Code tools/staging and conventional Rust targets. The inventory includes the owned LangGraph environment and local Gitleaks, AGNTCY, Gateway and ANS installations. Both targets refuse mounts and unreadable subtrees before removal or permission changes. Stop processes using those files first; the writer lock covers participating VS Code workflows only. Retained reports, configuration, personal virtual environments, model downloads and application data are preserved; custom paths must stay outside the explicit deletion inventory. See [cleanup scope, the LangGraph exception and reinstall commands](documentation/src/content/docs/contributing/build-and-release.md#clean-local-builds-and-dependencies).

Use `make clean-output-dry-run` to preview disposable output, then `make clean-output` to remove it. These separate targets preserve runtime inputs, models, unknown output and nested Git repositories. The [output cleanup guide](documentation/src/content/docs/contributing/build-and-release.md#clean-generated-output-separately) lists exact locations and preservation rules.

Application build identity and explicit VERSION bump commands are documented in the [build guide](documentation/src/content/docs/contributing/build-and-release.md#application-version-and-release-checks). Use `make version` to show the version, commit and source state.

The optional VS Code client has its own [Makefile](extensions/vscode/Makefile): run `make -C extensions/vscode help` for build, watch, package, verification and scoped cleanup. Build and editor-test installation are separate. Follow [extension development](documentation/src/content/docs/contributing/build-and-release.md#optional-vs-code-package) for SDK prerequisites, F5/source maps and local VSIX qualification.
