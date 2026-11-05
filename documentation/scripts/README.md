# Documentation build scripts

These Node modules prepare and verify the Starlight manual. Run them through the root Make targets so preparation, rendering and output checks happen in order; see the [authoring guide](../src/content/docs/contributing/documentation.mdx).

| File | Responsibility |
| --- | --- |
| `pdf.mjs`, `pdf.css` | Validate complete sidebar/API coverage, namespace anchors and SVG references, and assemble an expanded print document. |
| `pdf-export.mjs` | Export the sealed site through the documentation-owned browser and loopback assets. Preserve the prior PDF on failure. |
| `pdf.unit.test.mjs`, `pdf.functional.mjs` | Keep isolated transformations separate from explicit real PDF, native inspection and failure/cleanup tests. |
| `site.mjs` | Shared content-map validation, source projections, download allowlist and repository-to-site link rewriting. Bounded `manual-link` comments add registered manual links to site projections while offline package Help retains readable section names. |
| `prepare.mjs` | Recreates owned projections and Astro cache, checks source inclusion and copies reviewed downloads. |
| `notices.mjs`, `notices.unit.test.mjs` | Check locked metadata, preserve nested license files and require notice texts for reviewed browser suppliers. Keep the complete build inventory separate. |
| `bundle.mjs`, `bundle.unit.test.mjs` | Seal final static files, hosting base and inline CSP hashes. Reject hidden files, symlinks, excessive inventories and conflicting policies. |
| `openapi-anchors.mjs` | Repairs the pinned renderer's repeated response-header anchors before search indexing. |
| `content-focus.mjs`, `content-focus.unit.test.mjs` | Add missing keyboard-focus attributes to rendered manual tables, code blocks and API paths. Preserve all other bytes and existing focus choices. |
| `navigation.mjs`, `navigation.unit.test.mjs` | Validate local decorative masks and rendered sidebar names, icon assignments and API exclusions. |
| `diagrams.mjs` | Checks SVG accessibility and diagram-local references; rejects external references, event handlers, scripts, HTML labels and image elements. |
| `check.mjs` | Builds root and non-root output and verifies pages, navigation, assets, anchors, operation coverage and selected content components. It checks tab labels/panels, card destinations, status explanations, titled asides, ordered Steps and visible common requirements. Rendered/copied examples remain exact; package paths match the maintained sample, and illustrative host inputs keep searchable filenames. |
| `site.unit.test.mjs` | Isolated tests of projection, link, inclusion and diagram rules. |
| `content.functional.test.mjs` | Actual installed-site Markdown/MDX callout rendering in a private temporary copy. Also executes the displayed Gitleaks recipe with local command doubles to verify failure stops, staging cleanup and conditional PATH selection. |
| `site.functional.test.mjs` | Actual preparation subprocess and repeated-output checks. |
| `preview.mjs`, `preview-download.mjs`, `preview.functional.test.mjs` | Foreground, loopback-only Astro preview of existing output. The exact built LICENSE download bypasses Astro's extensionless-file trailing-slash refusal; all other requests retain framework routing. Functional controls cover root/base paths, GET/HEAD and refusal of unlisted paths. |

Use `npm --prefix documentation run test:unit` for isolated script tests. Use `make docs-site-check` after changing these modules; its existing check script runs both test layers before validating the built site. It does not publish the site or require a Catalog deployment. Generated content, `dist/`, `.astro/` and installed npm packages are not maintained source.

`licenses/pagefind-default-ui-1.5.2.txt` retains Pagefind's upstream MIT license verbatim. The published default UI package omits this text. Its source is [LICENSE at commit bf17396721be637cc67c8ed7ead1dc7b8ac43d96](https://github.com/Pagefind/pagefind/blob/bf17396721be637cc67c8ed7ead1dc7b8ac43d96/LICENSE), selected by tag `v1.5.2`. The same revision owns [the default UI package](https://github.com/Pagefind/pagefind/blob/bf17396721be637cc67c8ed7ead1dc7b8ac43d96/pagefind_ui/default/package.json). Review the text and version binding when updating that dependency. Builds read this local file without an upstream request.

Run `make docs-pdf-browser-install` explicitly after `make docs-install`. Run `make docs-build`, then `make docs-pdf`. The exporter never installs tools, modifies the completed build or publishes artifacts. Run `make test-docs-pdf` separately with installed `pdftotext`, `pdfinfo` and `mutool`; its filename excludes it from ordinary site checks. See the [PDF procedure](../src/content/docs/contributing/documentation.mdx#export-the-complete-manual-to-pdf) for deadlines, reports, cleanup and limitations.
