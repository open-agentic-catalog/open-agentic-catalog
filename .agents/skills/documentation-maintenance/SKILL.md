---
name: documentation-maintenance
description: Maintain Open Agentic Catalog documentation for users, contributors, developers, architects and operators, including engineering records, trackers and site tooling. Use for documentation work; architecture decisions use change planning and application changes use their owning workflows.
---

# Documentation maintenance

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md`, the documentation index (`documentation/README.md`) and the relevant authoring guidance (`documentation/src/content/docs/contributing/documentation.mdx`). Preserve review-only, plan-only and unit-only scope. Choose the reader and authoritative source before editing; documentation work does not itself authorize changing application behavior or publishing the site.

Treat usage, contribution, architecture, development, testing, deployment and operations pages as documentation alongside reference material, records and trackers. Apply the same writing and link standards throughout. The published manual serves multiple audiences. Site inclusion identifies publication location; it does not make developer documentation secondary or exclude repository-only documents from a requested complete review. Preserve useful evidence and chronology in records, but remove conversational history and repetitive session recaps there too.

## Establish the facts and owning source

Check runnable behavior against implementation status (`CONTEXT.md`), owning code/contracts, Make recipes and actual verification evidence. Keep accepted design, implemented capability, deferred proposals and measured results distinct. A command's presence, passing static check or target architecture does not prove an operational workflow works.

Edit an existing guide or runbook where it owns the procedure. Authored manual pages belong under `documentation/src/content/docs/`; architecture and ADRs belong in its `architecture/` tree. Root entry points and colocated package guides link to those authorities.

API and pinned-format projections derive from their original contracts, so update the source rather than a generated copy. Use change planning (`.agents/skills/change-planning/SKILL.md`) for an architecture decision; maintaining an ADR does not create an implementation requirement by itself.

Follow the authoring guide's writing style (`documentation/src/content/docs/contributing/documentation.mdx#writing-style`). Preserve ASCII and one-line paragraphs. Authored manual pages use one frontmatter title without a duplicate H1. Preserve the existing header format in root documents and skills. Verify relative paths and section anchors from the source location.

## Write clear technical documentation

Apply ASD-STE100 Simplified Technical English principles where practical. These software-documentation guidelines do not establish full compliance with its controlled dictionary and rules.

- Aim for 20 words or fewer per instruction and 25 per descriptive sentence. Give one instruction per sentence. Start a procedural step with a verb.
- Prefer active voice and simple present, past or future tense. Name the actor when responsibility matters. Retain articles such as "a" and "the".
- Prefer a simple verb to an unnecessary "-ing" construction. Keep established technical terms such as "listing" and "pooling".
- Use a consistent name and meaning for each concept. Explain required technical terms and acronyms at first use. Avoid noun chains longer than three words.
- Keep each paragraph to one topic and six sentences or fewer. Use lists or tables for parallel choices and complex conditions.
- Write "do not" for a prohibition. Preserve exact identifiers, code, standard names and contractual words such as MUST or SHOULD.
- Remove filler, repeated disclaimers and remarks such as "as you can see" or "this is important". Do not remove prerequisites, limits, failure modes or uncertainty to shorten text.
- Write current capabilities and useful procedures, not a work-session journal. Keep dated checks, source comparisons and measurements in engineering records. Keep plans and task IDs in trackers.
- Distinguish a standard's requirement from a Catalog rule and a test observation. Link to the specification for requirements. Use pinned profiles for version claims; announcements and reference implementations are supporting evidence.
- Link named standards, tools and reference implementations where readers need more detail, including names in tables. Prefer official specifications, project documentation or source repositories. Put the link at the relevant name, not only in a source list. Use versioned specifications or pinned sources for exact version claims. Check destinations and redirects, use descriptive labels, and avoid repeated links that distract from the text. Use `gh` for GitHub verification.

Treat sentence lengths as review targets, not an automatic gate. Exact technical terms, code and URLs need separate judgment. After rewriting, compare the result with the source facts. Check that conditions, required actions, permissions and failure consequences retain their meaning. Read the page as a user who has no conversation or backlog context.

## Maintain documentation-site structure when affected

Register authored pages in the content map (`documentation/content-map.json`) and ensure each route appears once in the appropriate sidebar (`documentation/astro.config.mjs`), including existing autogenerated navigation. Preserve routes when converting Markdown to MDX, reconcile incoming links, and update exact `omitSections` references when their headings change. Generated projections, build output, dependencies and repository-only records never become authored site content.

Use existing accessible SVG components and styles for diagrams, with their rendered-count checks. Use reviewed synthetic-data screenshots and the existing download allowlist when a workflow needs an image. Follow the authoring guide instead of adding another diagram/page system or public asset copy. Consult site tooling (`documentation/scripts/README.md`) only when changing projection, navigation, search or build behavior, and dependency maintenance (`.agents/skills/dependency-maintenance/SKILL.md`) when its toolchain changes.

Give each diagram one reader question and keep it beside the owning procedure. Check arrows, actor authority, stored versus temporary state and stop conditions against code/contracts. Reuse existing drawings instead of duplicating topology or lifecycle maps. Keep exact settings and exceptions in tables or captions. Native SVG is the selected workflow path; Mermaid fences are not rendered. Preserve static local references, title/description, theme styling, contained keyboard scrolling and source/site checks. When converting Markdown to MDX, preserve routes, anchors and examples, reconcile source links and lint component names, and replace or remove Markdown-only comments.

Follow the content-component conventions (`documentation/src/content/docs/contributing/documentation.mdx#content-components`) when presentation needs improvement. Use native Starlight components selectively; keep required steps, permissions and safety visible. FileTree is excluded from search, so retain required filenames in adjacent prose. JSX links need the hosting base; the Markdown transformer does not rewrite their `href` values. Preserve exact code and clipboard content, including shell comments and heredocs, and check used disclosure controls in the browser.

Use one destination per LinkCard and keep comparison tables that need several links. Format existing statuses with badges and keep their explanations; do not imply trust or runtime health. Place illustrative file trees after their inputs are introduced. Include required source conversions, content-map/incoming-link updates, narrow lint allowances and rendered assertions with each owning content change.

## Reconcile tracking and evidence

When local `todo/` exists, follow tracker maintenance (`todo/README.md`). The directory is gitignored and optional; do not require or create it for documentation work. Active and deferred work have separate files; completed scope moves in full to `todo/done.md`, preserving IDs, acceptance criteria, links, disposition and unresolved limits. Use unchecked active/deferred items and checked closed items. For partial completion, move only the completed subitems and keep the remaining parent scope explicit. Update the queue and incoming links without duplicating full task definitions. Keep public documentation independent of these local files.

If local `continue.md` exists, keep it a concise handoff to the current sequence. It is gitignored and optional. Update `CONTEXT.md` when the runnable boundary changes. Reviewed local notes are optional context; do not record private workspace locations in repository files. Keep raw logs and generated reports under ignored `output/`. If local notes are unavailable, report results in the response without blocking verification. Notes and trackers stay outside the content map, site assets and search. A later passing run does not erase an unexplained earlier failure.

Keep `CONTEXT.md` self-contained and compact, reconciling affected summaries with the owning code, contracts, configuration, workflows and active queue. Preserve explicit source and measurement baselines without treating old evidence as a new run. It remains repository-only and does not replace those authorities. Link closed history from the handoff and tracker index instead of duplicating task narratives or successive coverage totals.

## Verify the changed boundary

Use post-code verification (`.agents/skills/post-code-verification/SKILL.md`) for source gates: `make check` includes `docs-check`; focused tracker/link iteration can use `make docs-check`. Reader-site content or tooling changes also require `make docs-site-check` after the documented dependencies are installed. Skills, trackers and repository-only records alone do not need a site rebuild or application regression suite.

After navigation, theme, search or tooling changes, inspect built root and non-root output for keyboard controls, responsive layout, local search, links and console/network errors. Build and preview with the same `DOCS_BASE`; preview does not rebuild changed sources. Reuse maintained checks and owned previews without introducing a new browser framework or disrupting retained servers. Changes to operator procedures may need targeted execution in an owned disposable fixture; do not run a mutating command merely because it appears in a guide. Report unrun qualification under an explicit unit-only limit.

Report the owning pages/records updated, reconciled links or task moves, checks and remaining limits. Publication, CI/CD, staging and commits retain their separate authorization boundaries.
