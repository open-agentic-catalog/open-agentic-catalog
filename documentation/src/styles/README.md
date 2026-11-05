# Documentation styles

`architecture.css` styles the technical SVG components and their scrollable containers. It uses theme-aware colors and shared node, connector and text classes; keep geometry local to the drawing that owns it.

The [ClientComparison component](../components/ClientComparison.astro) owns the homepage's five-column table styles. It fits the desktop reading column; narrow screens retain Starlight's scrollable table layout.

`openapi.css` uses theme colors for required API fields, property labels and type tabs. It also restores the syntax palette's background for API examples. These rules apply only to generated API components.

Run `make docs-site-check` after edits and inspect diagrams in both themes and at narrow widths. Catalog UI application styles are maintained separately in `catalog-ui/`.
