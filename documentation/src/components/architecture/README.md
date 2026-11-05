# Technical SVG diagrams

Each `.astro` component draws one architecture boundary or workflow using native SVG and [shared styles](../../styles/architecture.css). Its title and description explain the drawing to assistive technology; marker and label IDs must be unique and self-contained.

Embed components in the relevant MDX page, or through the content map's explicit diagram projection markers. Update expected diagram counts in [the output checker](../../../scripts/check.mjs) when adding or removing an embedding. Avoid a second prose explanation inside component source when the adjacent page already owns it.

`DiagramCard.astro` emits a repeated native SVG rectangle and its text. The owning component still supplies the diagram geometry, labels and connectors. Figures use a 640-unit canvas and labels of at least 16 units. Figures fit the available reading column when it is at least 560 CSS pixels wide. Narrower columns retain that minimum; the focusable wrapper contains horizontal panning at any viewport width. Keep independent branches on separate paths and put connector labels in clear gaps, not over another card.

Follow the [diagram authoring guidance](../../content/docs/contributing/documentation.mdx) and run `make docs-site-check`. Check light/dark themes and narrow layouts when changing SVG geometry or shared styles.

## Canonical architecture views

| Component | Reader question | Owning placement |
| --- | --- | --- |
| `SystemContext.astro` | Who uses the Catalog, and which external systems interact with it? | [System context](../../content/docs/architecture/overview.mdx). It treats the Catalog as one software system. |
| `WelcomeDeployment.astro` | Which logical applications and data stores serve a PostgreSQL deployment? | [Logical applications and data](../../content/docs/architecture/deployment.mdx#logical-applications-and-data), shared with the homepage. Browser React and the Go host remain separate. |
| `Components.astro` | How do backend layers depend on contracts and make runtime calls? | [Components and command flow](../../content/docs/architecture/components.mdx). Exact package names remain in its ownership table. |
| `ComposeDeployment.astro` | Which local containers, ports and startup conditions does Compose use? | [Compose architecture](../../content/docs/architecture/deployment.mdx#docker-compose), linked from the local quickstart. |
| `Deployment.astro` | Which workloads belong to a Helm release, and which infrastructure stays external? | [Helm architecture](../../content/docs/architecture/deployment.mdx#kubernetes-through-helm), shared with the [operator guide](../../content/docs/operations/deployment.mdx#helm-runtime-topology). |

Solid connectors show runtime requests or storage access. Dotted `svg-connector-dependency` paths show non-network dependencies, such as construction or startup conditions; label their meaning in each view. Purple dashed reference paths represent recorded metadata without a fetch. Optional nodes name their optional status. Label group frames by software scope or deployment ownership; do not use an unlabelled frame for both.

Reuse existing workflow components for details. `CommandPath.astro` owns the HTTP command flow; `EmbeddingBoundary.astro` owns inference and derived-vector boundaries. `TelemetryPaths.astro` and `CoordinatedUpgrade.astro` own telemetry and maintenance sequencing. `TargetContext.astro` remains a conditional target view in the design chapter, not a claim that every depicted integration runs.
