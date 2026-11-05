# Documentation navigation shapes

These local SVGs supply decorative masks for manual sidebar links. [The authoring guide](../../content/docs/contributing/documentation.mdx#navigation-icons) explains selection, rendering and verification. Do not add accessible titles to these masks; the native link text names each destination. Technical diagrams use separate accessible components.

All shapes except `map.svg` are exact shape markup from Starlight 0.42.0's [built-in icons](https://github.com/withastro/starlight/blob/%40astrojs/starlight%400.42.0/packages/starlight/src/components-internals/Icons.ts). The enclosing SVG adds the common 24-unit view box. Preserve their [MIT license](LICENSE); the built site's third-party notices also retain the installed Starlight license. `map.svg` is project-owned artwork. Project source uses [Apache License 2.0](../../../../LICENSE).

The stylesheet includes only these selected local files. Vite resolves them at build time and bundles their masks without an external request. Add a shape only when an existing family does not fit the task. Keep its 24-unit view box and visual weight consistent with the existing set.
