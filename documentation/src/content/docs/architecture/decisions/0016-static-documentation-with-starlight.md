---
title: "ADR-0016: Static documentation with Astro Starlight"
---

- Status: Accepted

## Question

Which documentation system should present the service's guides, contracts, architecture and operations material without becoming an application dependency or creating a second source of truth?

## Context

The repository contains Markdown guides, architecture documentation, executable examples and an OpenAPI 3.1 contract. A separate documentation toolchain can provide navigation, search and API rendering without adding application dependencies.

## Decision

Make targets build, preview and validate the static site locally. Static output supports root and nested base paths. External hosting and publication remain maintainer-owned.

Use Astro Starlight to build a standalone static documentation site. Keep ordinary content predominantly repository-readable Markdown, use the standard theme with minimal overrides, and use its integrated Pagefind search with locally bundled assets. No hosted search account, analytics, CDN or external product is a baseline requirement.

Keep one documentation-local Node/npm project with a pinned supported Node version, reviewed generator/plugin versions and a committed npm lockfile. Use `npm ci` for clean locked installs and documented Make entry points for installation, preview, build and appropriate validation. Do not use unpinned package runners in maintained gates. The version pins, lockfile, Make targets and dependency licenses are documented in the [authoring guide](../../contributing/documentation.mdx). The full npm build tree includes non-permissive tool-only components and is not redistributed with the static site or Catalog artifacts. Review build tools and distributed JavaScript, CSS, fonts and other assets separately; the MIT core licenses do not approve every transitive dependency or replace the Catalog's [Apache-2.0 source license](../../../../../../LICENSE).

The documentation build remains separate from the Go module, backend image, ordinary backend startup and readiness. The generated static output can be hosted independently without a Node server. Preserve existing Markdown and contract audits; add site-specific build, link and browser gates without making deployment infrastructure necessary to edit or build documentation.

GitHub Actions includes a [manually run documentation check](../../contributing/testing.md#github-actions). This repository does not configure publication to a separate documentation website. The optional UI image builds the manual in its own stage and serves static output at `/docs/`. It includes no Node server or documentation build tools at runtime. Backend images and ordinary units remain independent. See [UI image delivery](../../contributing/build-and-release.md#optional-ui-image) for the build inventory, caching and verification boundary.

Keep one editable authority for each topic. Derive API presentation from `api/openapi/control-v1.yaml`. Use existing CLI, configuration and example sources instead of duplicate contracts.

The pinned starlight-openapi integration renders every operation read-only from the OpenAPI 3.1 contract. Local-reference preflight prevents remote schema resolution. A narrow tested build hook corrects repeated response-header anchors before search indexing. No request console is enabled.

Author architecture and ADRs under `documentation/src/content/docs/architecture/`. Embed technical SVG diagrams through small MDX components. Keep tables and explanations in the owning page. Avoid generator-specific syntax in ordinary prose.

Implementation status, backlog and handoff remain repository-only developer references. Local research and verification reports stay under ignored `output/`; do not publish or index them. Reader pages describe capabilities and limitations directly. Keep implemented behavior distinct from target behavior. Rewrite links to included sources as site routes. Do not retain a duplicate HTML explorer or old reader-path stubs.

Validate representative content: quickstart, API reference, long configuration table, recovery runbook and architecture view. Check authoring friction, existing root/relative links, title/frontmatter handling, search, responsive and keyboard behavior, and unintended network requests. This check determines whether the selected generator meets the implementation requirements. Report a material incompatibility for an explicit decision rather than silently replacing the selected generator or constructing a custom documentation framework.

## Alternatives considered

- Docusaurus: a credible option with built-in release-version snapshots and React customization. Starlight provides ordinary Markdown and integrated local search; Docusaurus's community local-search integration and default MDX parsing would require their own review. Multiple maintained documentation versions are not yet selected.
- Hugo with Hextra or Hugo Book: a simpler Go-based build is a real benefit, and Hextra can be used without Node. It does not automatically simplify API rendering, content migration, browser assets or versioned documentation. Sharing the service's language was not a sufficient reason to prefer it over the selected authoring experience. Docsy also illustrates that a Hugo theme can still require Node/CSS tooling.
- Another Markdown-first generator would require a separate API-rendering and search integration. No current requirement justifies that replacement.
- A custom Go documentation generator or broad custom theme: rejected because the repository needs verified content and navigation, not another maintained rendering system.

## Consequences

- Documentation maintainers take on a separate npm dependency tree, security/license reviews and coordinated Astro/Starlight/plugin upgrades. A lockfile controls installs but does not remove supply-chain risk or upgrade work.
- Keep customization small, pin inputs and test upgrades. Generator selection alone establishes no maintenance or performance guarantee.
- Ordinary authors should mostly edit Markdown and preview it through the documented workflow rather than write JavaScript. Some frontmatter, link and navigation work remains unavoidable.
- The manual ships in the optional UI image. The manually run GitHub Actions check validates it without publishing. Maintain one current documentation set. Translations, version archives and `file://` support require a demonstrated need. Build and search-index inclusion use an explicit content allowlist; trackers, internal evidence and test artifacts stay outside it. The maintainer selects and operates any external hosting.
- The local documentation workflow comprises the complete reader tree, verified references, runbooks and local source/site/browser maintenance gates. Normal development/CI consumes relevant documentation evidence without a separate release checklist. Publishing the site is not a local completion requirement.

## References

- [Starlight setup and maintenance status](https://starlight.astro.build/getting-started/), [Markdown authoring](https://starlight.astro.build/guides/authoring-content/) and [local search](https://starlight.astro.build/guides/site-search/).
- [Starlight license](https://github.com/withastro/starlight/blob/main/LICENSE), [Astro license](https://github.com/withastro/astro/blob/main/LICENSE) and [npm clean-install behavior](https://docs.npmjs.com/cli/v11/commands/npm-ci/).
- [Starlight OpenAPI integration](https://starlight-openapi.vercel.app/getting-started/), [Docusaurus versioning](https://docusaurus.io/docs/versioning), [search](https://docusaurus.io/docs/search) and [Markdown parsing](https://docusaurus.io/docs/markdown-features).
- [Hugo](https://gohugo.io/about/introduction/), [Hextra](https://imfing.github.io/hextra/) and [Docsy prerequisites](https://www.docsy.dev/docs/get-started/docsy-as-module/installation-prerequisites/).
