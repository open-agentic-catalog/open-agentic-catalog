---
name: dependency-maintenance
description: Review and update Open Agentic Catalog Go modules, npm packages, toolchains, analyzers and pinned build or test images. Use for dependency changes and advisory investigations; standards profile changes use standards conformance, and routine scanner execution uses post-code verification.
---

# Dependency maintenance

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md` and the affected section of the dependency policy (`documentation/src/content/docs/reference/dependencies.md`). Preserve review-only and unit-only scope. Establish a concrete reason for the selected update; a request to inspect advisories or versions does not authorize a blanket upgrade.

## Identify the owning dependency boundary

Locate the authoritative manifest, lockfile, tool pin or image digest and its actual consumers. Distinguish imported runtime code, test imports, build tools, bundled browser assets and external execution-only tools. The root Go module, UI npm project, documentation project, API-validation tools and generated Go-client modules have separate closures. A root module or source scan does not qualify all of them.

Inspect current overrides and compatibility constraints before selecting a version. Review official release notes, engines/peers, API or generated-output changes, advisories and transitive drift for the proposed revision. Use `gh` for GitHub operations.

Retain exact immutable image identities and review every supported execution platform when an external tool/image changes; a source license alone does not inventory its complete artifact.

A new release, changed advisory range or scanner `fixAvailable` flag is not proof of a behavioral fix. When sources disagree, compare the published implementation with the demonstrated vulnerable path and retain the dated evidence and uncertainty. Do not force an upgrade or suppress a finding merely to obtain a clean report.

Follow the policy for that boundary. The imported Go closure has a strict license allowlist; it is not the policy for npm tools or external test images. Separate permission to execute a tool from permission to distribute its files. Preserve upstream notices and unresolved inventory limitations. Route vendored format/profile changes to standards conformance (`.agents/skills/standards-conformance/SKILL.md`), including their provenance and checksum rules.

## Make the smallest coordinated update

Change the owning exact pins and regenerate only their lockfiles with the documented toolchain. Review the complete diff, including transitives, resolved sources and integrity fields. Do not hand-edit lockfile dependency graphs, use forced audit fixes, relax peer constraints or widen license/scan exceptions to make a check pass. Use an existing documented installation target; do not silently install the latest global analyzer or change personal tool configuration.

For Go changes, run `go mod tidy` only when imports or requirements change and reconcile both module files plus the applicable direct-dependency inventory. Preserve separate locks in nested tool modules. For UI packages, follow UI dependency maintenance (`documentation/src/content/docs/reference/dependencies.md#ui-dependency-maintenance`), including lifecycle-script-free acquisition and the documented reasons for overrides. Documentation tooling follows its authoring/dependency procedure (`documentation/src/content/docs/contributing/documentation.mdx#dependencies-and-distributed-assets`), with its own notices and build-only license boundary.

Coordinate toolchain/image pins with their actual Make, Containerfile, version checks and setup documentation; keep duplicate compatibility assets aligned. Use build and deployment (`.agents/skills/build-and-deployment/SKILL.md`) for affected packaging mechanics. A dependency-only update need not redesign UI screens or refactor application behavior.

## Verify the changed closure

Select shared checks through post-code verification (`.agents/skills/post-code-verification/SKILL.md`), adding the owning policy's gates:

- Root Go module changes need `make module-check`, `make license-check`, affected tests, ordinary source/local verification and `make security` with current advisory data. Nested API/client-tool modules require their own license/vulnerability review and compatibility checks; a root pass does not cover them.
- UI changes need local `make ui-deps-check`, plus the existing UI/browser and visual gates. Both advisory targets include development dependencies; inspect report scope and failures. `ui-deps-audit` submits dependency metadata to its registry; external registry audits are maintainer-only. Leave external registry audits unrun and report that limit. Tool or registry errors are not clean scans. Build/runtime/login changes also need affected packaged journeys and image inspection/SBOM evidence under the owning guide.
- Documentation or API-tool changes need their own installation, source/site or validator/client checks and artifact-notice review. An exposed npm lock graph can omit dependencies bundled inside a tool package. Image/tool changes need the relevant platform, acquisition and execution qualification rather than a root Go import check alone.

Retain tested source/lock/image identity, tool versions, advisory freshness, meaningful skips and remaining findings. Do not regenerate visual expectations or weaken tests solely to accept an upgrade. Under unit-only execution, report broader compatibility/package qualification as unrun. Record the update reason, compatibility/license impact and measured results; inventory, vulnerability scans and runtime tests provide different evidence.

Common Python development tools use the dependency-only pyproject dev group and uv.lock. Update pins/lock and review the complete license/advisory closure together. Install explicitly with `make dev-tools-install` and pinned uv. Specialized toolchains stay independent. See shared tools (`documentation/src/content/docs/contributing/build-and-release.md#shared-development-tools`).

For the Rust SDK, follow `test/sdk/README.md#rust-sdk`. Review SDK, tool and external-consumer Cargo locks separately. Normal installation records checksummed sources, license metadata and the release-age cutoff. Cargo-deny is a separate optional audit tool. Its explicit `sdk-rust-audit-tools-install` target records its own identity and a fresh RustSec revision; `sdk-rust-audit` checks licenses and advisories. Report audit failures separately from normal SDK qualification. Frozen offline checks do not update registry metadata. Preserve build/dev and unpublished-crate license checks, the exact generator-only informational exception and the separately tested compiler floor. Do not import a coverage wrapper when the pinned compiler supplies the required LLVM tools.
