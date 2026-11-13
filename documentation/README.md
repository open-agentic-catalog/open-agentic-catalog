# Documentation project

Project documentation covers usage, contribution, architecture, development, testing, deployment and operations. The published [manual](src/content/docs/index.mdx) serves users, developers, architects and operators. Engineering records, trackers and colocated guides also belong to the documentation. Apply the same writing standards to all of these documents. Start with the [authoring and build guide](src/content/docs/contributing/documentation.mdx) for Node/npm prerequisites, Make targets, root/base-path builds, testing, source placement and dependency review.

From the repository root, run `make docs-install`, `make docs-build`, then `make docs-preview`. The preview is loopback-only and serves `documentation/dist/`. `make docs-site-check` runs JavaScript lint and Astro diagnostics before native tests and root/non-root builds; `make docs-check` remains the independent Markdown and repository contract gate. These Make commands do not publish the site. The separately enabled Pages workflow is maintainer-operated.

The optional `make docs-pdf` exports a completed build to `output/documentation-pdf/catalog-manual.pdf`. Follow the [PDF procedure](src/content/docs/contributing/documentation.mdx#export-the-complete-manual-to-pdf) for explicit browser installation and verification. Ordinary site builds need no browser.

The optional UI image builds this manual in a separate stage and serves it publicly at `/docs/`. It ships only static output and notices. Backend builds and ordinary unit tests remain independent. See [UI image delivery](src/content/docs/contributing/build-and-release.md#optional-ui-image) for file evidence, CSP, caching and exact-image checks.

`content-map.json` explicitly lists every authored or derived reader page. Generated projections and build output are ignored. Architecture, detailed design and ADRs are authored in the manual; API and colocated contracts retain their own authority. Optional local trackers under `todo/`, research and test records are not site inputs. Generated reader pages do not display source-file or editing notices.

The manual's sidebar starts with Get started (including the existing-Catalog reader and populated sample guides), Use the Catalog (including Search and result evidence) and Build integrations. Deploy and operate, Architecture and Develop and contribute follow. Reference is last, grouping the CLI reference, API conventions, downloadable contracts/examples and generated HTTP API. The [manual homepage](src/content/docs/index.mdx#choose-your-starting-point) links to starting pages for each section. Navigation changes preserve routes and include every manual page exactly once.

**Build integrations > SDKs** contains the [overview](../sdk/README.md), [Go](../sdk/go/README.md), [Python](../sdk/python/README.md), [JavaScript/TypeScript](../sdk/typescript/README.md) and [Rust](../sdk/rust/README.md) guides. The content map publishes these source READMEs at `sdk/overview/`, `sdk/go/`, `sdk/python/`, `sdk/typescript/` and `sdk/rust/`. The [client integration guide](src/content/docs/guides/integrating.mdx) retains shared workflows and complete examples.

**Build integrations > LangGraph discovery assistant** publishes the [Python example](../examples/langgraph/README.md). It uses the Python SDK and a local tool-calling model to find governed releases. The guide explains setup, permissions, bounds and separate scripted and real-model checks.

**Build integrations > Rust Catalog report** publishes the [standalone Rust example](../examples/rust-catalog-report/README.md). It searches authorized local releases, verifies exact records and prints a bounded Markdown inventory. The guide covers credentials, private trust, filters, complete/partial output and a curated-sample query.

**Build integrations > Go webhook publication report** publishes the [Go receiver application](../examples/publication-report/README.md). It stores accepted notifications in its own PostgreSQL inbox and refreshes a local report through the Go SDK. The guide covers HTTPS, minimal grants, bounds, signer rotation and reconciliation.

**Build integrations > TypeScript published discovery** publishes the [Node application](../examples/typescript-ard-list/README.md). It uses ARD List without private Resource reads or entry-URL requests. The guide covers local SDK installation, audiences, trust, response checks, pagination and exit statuses.

**Build integrations > VS Code client** publishes the [extension guide](../extensions/vscode/README.md), including VSIX installation, first connection, credentials, browsing, filters, document inspection, bookmarks and all four resource workflows. It also covers troubleshooting, uninstall controls, current limits and the SDK-first contributor build sequence. The same guide and screenshots ship in offline extension Help.

**Develop and contribute > Build and release** covers [VS Code development setup, source ownership and packaging](src/content/docs/contributing/build-and-release.md#optional-vs-code-package). **Tests and quality checks** explains [extension test placement, Make targets, coverage and installed-editor reports](src/content/docs/contributing/testing.md#optional-vs-code-extension).

**Build integrations > Catalog MCP server** contains the [MCP guide](src/content/docs/guides/mcp-server.mdx). It covers local stdio and optional stateless HTTP setup, the six read-only discovery tools, permissions, pagination, limits and troubleshooting. It includes [Google ADK qualification without a model](src/content/docs/guides/mcp-server.mdx#use-google-adk-without-a-model). This interface connects to the Catalog API; it does not invoke cataloged MCP Servers.

**Build integrations > Connect with ContextForge** contains the [setup tutorial](src/content/docs/guides/contextforge.mdx). It covers isolated setup, native registrations, Catalog MCP proxying, governed virtual-server handoff, the Admin UI, separate credentials and scoped cleanup. It also explains the connection settings for an existing Catalog and ContextForge deployment.

**Deploy and operate > Install and configure > Optional Vault secret delivery** contains the [Vault tutorial](src/content/docs/operations/vault.md). It covers the isolated lab, existing Vault/VSO setup, exact policies, synchronized Secrets, activation, rotation, recovery and scoped removal. Native Kubernetes Secrets remain the default.

**Deploy and operate > Authentication and sign-in** contains the [shared setup](src/content/docs/operations/sign-in.md) and provider guides for Keycloak, Microsoft Entra ID, Auth0, Okta, [IBM Security Verify](src/content/docs/operations/oidc-ibm-security-verify.md) and Amazon Cognito. The guides distinguish locally qualified Keycloak flows from hosted-provider instructions and current token/client restrictions.

**Deploy and operate > Install and configure > Managed container platforms** contains the [IBM Cloud](src/content/docs/operations/ibm-code-engine.mdx), [Azure](src/content/docs/operations/azure-container-apps.mdx) and [Google Cloud](src/content/docs/operations/google-cloud-run.mdx) procedures. They use managed container hosts and PostgreSQL services. They describe required compatibility checks and CLI commands; they do not claim tested cloud deployments.

Root [CONTEXT.md](../CONTEXT.md) consolidates application context for repository readers. An optional local `continue.md` contains the working handoff; it is gitignored and is not required by documentation tooling. Keep summaries aligned with the owning sources below. Recorded coverage or qualification is not evidence of a fresh test run.

**Develop and contribute** includes [GitHub Actions selections, prerequisites and reports](src/content/docs/contributing/testing.md#github-actions), plus [image builds and release-candidate checks](src/content/docs/contributing/build-and-release.md#github-actions). The [workflow directory guide](../.github/WORKFLOWS.md) links the source files and their tool ownership.

**Use the Catalog > Move drafts between Catalogs** contains the [transfer tutorial](src/content/docs/guides/draft-transfer.mdx). It covers both UI/CLI directions, five downloadable examples, destination permissions, package inputs, new Versions and interrupted imports.

## Find the authoritative source

| Need | Source |
| --- | --- |
| Load the app's working context in one file | Root [CONTEXT.md](../CONTEXT.md), a consolidated overview with explicit source and measurement baselines. |
| Run and build | Root [README](../README.md), [Makefile](../Makefile) and [build guide](src/content/docs/contributing/build-and-release.md). |
| Current behavior | [application context](../CONTEXT.md#implemented-features-and-defaults). |
| Design philosophy | [Design principles](src/content/docs/architecture/design.mdx): operational simplicity, infrastructure ownership, configuration, bounded recovery and evidence-driven changes. |
| Architecture and decisions | [Architecture pages and diagrams](src/content/docs/architecture/overview.mdx), [ADRs](src/content/docs/architecture/decisions/index.md) and [design principles](src/content/docs/architecture/design.mdx); root [architecture](../DESIGN.md) is the repository entry point. |
| HTTP contracts and examples | [OpenAPI](../api/openapi/control-v1.yaml), [API assets](../api/README.md) and [API conventions](src/content/docs/reference/api-conventions.md). |
| SDK installation and use | [SDK overview](../sdk/README.md) and the colocated language READMEs; the manual publishes these guides without a second editable copy. |
| Assistant and IDE access through MCP | [Catalog MCP server](src/content/docs/guides/mcp-server.mdx), the owning guide for `catalogctl mcp serve`. |
| Configuration and deployment | [Configuration reference](src/content/docs/reference/configuration.mdx), [sample profiles](../config/README.md) and [operations index](src/content/docs/operations/overview.md). |
| Logging, traces and audit | [Current observability behavior and limits](src/content/docs/operations/observability.mdx#logs-and-trace-correlation), [audit authority](src/content/docs/operations/audit.md) and [logging strategy](src/content/docs/architecture/decisions/0021-logging-observability-and-audit.md). Diagnostic safety, owned panic containment, startup logging controls and backend signal consistency are implemented; UI-host dependency/session events and audit-preservation verification are complete under accepted ADR-0021. |
| Monitoring tools and dashboard | [Monitoring with Prometheus and Grafana](src/content/docs/operations/helm-monitoring.mdx) covers existing collectors and the optional Helm chart. See [local setup](src/content/docs/guides/performance-and-monitoring.md#monitor-the-persistent-development-stack), the [Grafana walkthrough and screenshot](src/content/docs/guides/performance-and-monitoring.md#read-the-grafana-dashboard) and [Prometheus target inspection](src/content/docs/guides/performance-and-monitoring.md#inspect-prometheus-collection). Traces use the separate [Jaeger workflow](src/content/docs/operations/observability.mdx#view-traces-locally). |
| Standards and formats | [Standards reference](src/content/docs/reference/standards.mdx) and [pinned profiles](../schemas/PROFILES.md). |
| Local code-quality dashboard | [SonarQube setup](src/content/docs/contributing/testing.md#optional-local-sonarqube-dashboard), [login](src/content/docs/contributing/testing.md#log-in-to-sonarqube) and [scan/coverage imports](src/content/docs/contributing/testing.md#scan-and-import-coverage). |
| Testing and measured results | [Testing pyramid and quality map](src/content/docs/contributing/testing-map.mdx), [testing strategy](src/content/docs/contributing/testing.md), [functional harness](../test/functional/README.md) and [UI matrix](../catalog-ui/tests/README.md). Raw reports stay under ignored `output/`; reviewed notes may use the optional private workspace described in [AGENTS.md](../AGENTS.md). |
| Local work tracking, when present | Gitignored `todo/` contains active, deferred and closed tasks, plus local maintenance rules. It is optional and excluded from the site. |
| Contribution and security | Root [contribution guide](../CONTRIBUTING.md), [agent rules](../AGENTS.md), [security policy](../SECURITY.md) and [incident response](src/content/docs/operations/incident-response.md). |

## Directory layout

| Path | Contents |
| --- | --- |
| [src/content/docs/](src/content/docs/index.mdx) | Authored reader guides, references and runbooks. Do not put directory-index READMEs here: each Markdown file is a site page and must be registered in the content map. |
| [src/content.config.ts](src/content.config.ts), [astro.config.mjs](astro.config.mjs) | Starlight content loading, navigation, plugins and static build configuration. |
| [scripts/](scripts) | Source projection and link policy (`prepare.mjs`, `site.mjs`), notice generation, OpenAPI anchor repair, bounded preview and output checks/tests. Use the Make targets rather than invoke pieces out of order. |
| [src/assets/navigation/](src/assets/navigation), [src/styles/navigation.css](src/styles/navigation.css) | Decorative local SVG masks for manual sidebar links; mapping stays in the Starlight configuration. |
| [src/styles/content.css](src/styles/content.css) | Shared manual content styles, including callout title wrapping for narrow columns and enlarged text. |
| [assets/](assets) | Maintained source assets: the favicon, reviewed Catalog UI screenshots and the live Grafana dashboard illustration. |
| [src/components/architecture/](src/components/architecture), [src/styles/architecture.css](src/styles/architecture.css) | Accessible technical SVGs for architecture and in-context workflow diagrams, with shared responsive light/dark styling. |
| [content-map.json](content-map.json) | Explicit source-to-route mapping and projection/exclusion rules. |
| [package.json](package.json), [package-lock.json](package-lock.json), [.nvmrc](.nvmrc) | Documentation-only toolchain and locked dependencies. |
| [eslint.config.mjs](eslint.config.mjs), [tsconfig.json](tsconfig.json) | Separate JavaScript lint and strict Astro/TypeScript source scopes. |

`src/content/docs/generated/` and `public/generated/` are disposable build projections, `dist/` is static output, `.astro/` is the build cache, and `node_modules/` contains installed dependencies. They are ignored and must not be hand-edited or committed. Directory guides elsewhere in the repository remain repository navigation; adding a README does not automatically add a published manual page.
