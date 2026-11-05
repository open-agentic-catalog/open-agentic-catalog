# Documentation source

`content.config.ts` registers the Starlight content collection. Authored manual pages live in [content/docs](content/docs/index.mdx); their routes and inclusion are controlled by [the content map](../content-map.json).

[Components](components/README.md) supply reusable SVG diagrams. [Styles](styles/README.md) keep those diagrams readable across themes and viewport sizes.

Do not add directory README files inside `content/docs`: Starlight treats Markdown there as reader pages. Use the existing section landing pages instead. See the [documentation guide](content/docs/contributing/documentation.mdx) for source placement and verification.
