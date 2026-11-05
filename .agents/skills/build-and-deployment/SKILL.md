---
name: build-and-deployment
description: Maintain Open Agentic Catalog builds, configuration delivery, images and Compose, or start and manage the local API/UI/docs environment. Use for build/deployment changes and local startup; chart qualification uses helm-and-minikube, and dependency selection uses dependency maintenance.
---

# Build and deployment

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md`, the relevant build procedures (`documentation/src/content/docs/contributing/build-and-release.md`) and deployment architecture (`documentation/src/content/docs/architecture/deployment.mdx`). Check implementation status (`CONTEXT.md`) for the runnable boundary. Preserve plan-only, review-only and unit-only scope. Select the configuration, Make, packaging or local-session path affected by the request; a local build does not authorize distribution, pipeline creation or a shared-environment rollout.

## Start and manage a local development session

Follow local development (`documentation/src/content/docs/guides/local-development.mdx`). Inspect existing listeners/services, the selected `COMPOSE_PROJECT` and workflow configuration before starting or rebuilding. Reuse a healthy requested environment, preserve `.env` and retained volumes, and account for Compose's migration/provisioning steps. Do not print secret-bearing configuration or reset data to resolve a startup failure.

Use `make run` for the in-memory API/worker, or `make compose-up LOCAL_UI=on` for the retained Compose environment with UI when requested. Select monitoring/model overlays only when needed; starting the embedding fixture does not enable semantic configuration or apply its optional schema. Separate `make ui-dev` serves the source-built UI against the configured local API. Use the documented locked prerequisites, not new global tooling.

For direct Docker without Compose, follow Docker with PostgreSQL (`documentation/src/content/docs/operations/docker.mdx`). Deliver selected non-secret settings through explicit `-e` or Docker-compatible `--env-file` inputs and mount each role's credentials separately. Do not pass Make's workflow `.env` wholesale. Validate owner access across forced RLS and extension-function grant authority before publishing a database setup recipe.

`make compose-up` builds and validates target API/worker configuration before stopping API then worker; existing migration/provisioning dependencies gate restart, and the launcher waits for healthy services. Raw `docker compose up` does not supply this drain sequence. Follow coordinated maintenance (`documentation/src/content/docs/operations/maintenance.mdx#coordinated-release-deployment`) for release ordering and failure recovery; do not restart a predecessor automatically after committed migrations.

For the documentation server (`documentation/README.md`), install its separate locked dependencies when needed, run `make docs-build`, then `make docs-preview` with the same `DOCS_BASE`. Preview serves existing output and does not rebuild it. Check the actual bound ports and base paths; verify API readiness, Compose service health and requested UI/docs pages before reporting URLs. A listening port or UI readiness alone does not prove backend authentication or an OIDC login works.

Keep requested development servers running at handoff and report their URLs, selected project and stop commands. Stop only owned/requested processes. `make compose-stop` retains containers/volumes; `make compose-down` removes that project's containers/network but retains data. `make compose-reset` deletes data and needs authorization for the exact disposable project. Smoke and demo seeding mutate records; startup alone does not request them. Disposable verification fixtures retain their own cleanup. An unchanged environment-start request needs health checks, not every post-code gate.

## Carry configuration to the consuming role

Use configuration and credentials (`.agents/skills/configuration-and-credentials/SKILL.md`) when changing runtime precedence, capability activation or credential loading/rotation. This workflow owns the build and launcher delivery; the per-consumer contract owns activation and replacement semantics.

For a setting change, trace its actual consumer through the typed loader (`internal/platform/config/README.md`), defaults, strict decoding, cross-field/role validation, explicit environment allowlist and safe diagnostic projection. Update affected sample profiles, the configuration reference (`documentation/src/content/docs/reference/configuration.mdx`), `.env.example` when relevant, and the Compose/Helm wiring that delivers it. Keep secrets in scoped deployment inputs and verify both enabled and disabled optional-capability behavior.

Make may load a selected workflow env file; binaries never load `.env`. `CATALOG_ENV_FILE=/dev/null` skips that file, not all inherited process variables. Preserve target-specific exports, dedicated migration credentials and chart-owned settings. Use `make config-check` with an explicit profile/role for static validation; it does not qualify database roles, dependency connectivity or a running deployment. Diagnostics construct role dependencies and need an explicitly selected environment.

Keep API, worker and migration authority separate. Preserve worker-only operations ports, liveness without dependency contact, required-role readiness and bounded drain. Optional UI, monitoring and degraded downstream services must retain their declared dependency boundaries. An actual SQL/migration change also uses database changes (`.agents/skills/database-changes/SKILL.md`); configuration wiring alone does not grant migration authority.

## Preserve build and Make contracts

Before editing a Make workflow, inspect callers and existing packaging/Make tests (`test/packaging`). Preserve target names, aliases, prerequisites, scoped variables, env-file isolation, exit behavior and cleanup ownership. Reuse the maintained target and fixture rather than adding a wrapper or another runner. Do not execute destructive or operator targets merely to enumerate them.

For images, use the canonical Containerfile and keep its compatibility copy and ignore-file pair aligned. Preserve pinned acquisition, static stripped non-root runtime artifacts, embedded roots, licenses and scoped build context. Dependency/toolchain selection follows dependency maintenance (`.agents/skills/dependency-maintenance/SKILL.md`); application and optional UI artifacts remain independent.

The optional UI image packages the static manual in a separate build stage at `/app/assets/docs/`, served at the fixed `/docs/` route. Keep React/manual hashes and browser-supplier evidence separate. Preserve the restricted context, network-disabled render and docs-only CSP. Ordinary source tests need no manual build; backend images remain independent. Use the existing UI image inspection/SBOM/scan gates rather than another inventory system.

Record source revision plus relevant working-tree changes, platform, explicit build metadata and the exact local image ID. Reuse artifact inspection (`documentation/src/content/docs/contributing/build-and-release.md#inspect-and-test`): `make container-inspect` examines an existing image without execution; `make container-verify` rebuilds, inspects and executes it. Use inspection for a cross-built image the local engine cannot run. A local image ID is not a published manifest/index digest, and a commit label does not prove a clean checkout. Select matching UI inspection, license/SBOM and chart packaging procedures only for affected artifacts.

## Qualify and hand off

Use post-code verification (`.agents/skills/post-code-verification/SKILL.md`) and AGENTS.md's additional-gate matrix. Reuse existing configuration units and Make contracts before relevant real-boundary tests. `make compose-check` renders without starting containers. Use Helm and Minikube (`.agents/skills/helm-and-minikube/SKILL.md`) for chart values/templates, migration-hook ordering, lint/schema/package checks and cluster scenario selection. Neither static evidence nor cross-build inspection proves runtime behavior.

Run the affected owned Compose or Minikube scenario for changed packaging, probes, session, topology or shutdown behavior. Follow the owning harness prerequisites and cleanup, avoiding concurrent network changes or retained-data resets. Unit-only scope leaves those checks unrun. Metadata-only changes may reuse applicable runtime evidence as allowed by AGENTS.md; do not require an unrelated platform/environment matrix.

Report configuration and artifact impact, exact checks, tested platforms, cleanup and unrun scope. Update the owning deployment/runbook and implementation status when behavior changes. Keep packages local. The maintainer handles all external publication and uploads.

Managed builds use root VERSION and test/packaging/versioning.py for one bounded version/full commit/source-state identity. Follow the build policy (`documentation/src/content/docs/contributing/build-and-release.md#application-version-and-release-checks`); preserve binary/label/Compose/chart agreement and maintainer-created signed release tags.
