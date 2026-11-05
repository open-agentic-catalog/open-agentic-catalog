# Interpret Catalog discovery results

Commands return `{"data": ..., "meta": {"requestId": "..."}}`; `meta.etag` is present when available. Work inside `data`, not an imagined `results` or ARD `entries` wrapper. Do not rewrite exact identifiers, digests or opaque extension values.

## Search

`data.items` is the ranked authorized page. Reader permission supplies local `resource.read`; namespace-only readers receive only their permitted local population. Without tenant `federation.discover`, default Search is local-only. Explicit indexed/live origins require that additional permission and otherwise return `403` before source queries. The Discovery reader template does not supply local `resource.read`. Enforced OAuth scopes additionally require `catalog.read`.

`--namespace UUID` restricts Search to local releases. Omit origins or select only `--origin local` with it; the CLI refuses indexed/live combinations before sending a request. The equivalent HTTP API request returns `400 INVALID_ARGUMENT`. Removing local read grants can produce an empty successful local Search; it does not change exact-read authorization. An empty local page does not prove that no external records exist or that external discovery is permitted.

Each item has `id`, `type`, `identifier`, `kind`, `displayName`, `description`, `tags`, `publisher`, `score`, and possibly a version. Honor `versionPresent`; an absent version is unspecified, not "latest". Metadata can be missing.

Preserve server order and deduplication instead of merging entries by display name or assuming that scores from different sources are calibrated probabilities.

| `type` | Evidence | Inspection |
| --- | --- | --- |
| `local` | `local` contains compact governed identity and validation status, including `local.release.id`; it omits representations and findings. | Only this release UUID can be passed to `remote record --id`. |
| `indexed` | `observationGroupId`, `normalizedEntryDigest`, `sourceIds`, `sourceCount`, `sourcesTruncated`, `conflict`, `sameSourceConflict`. | Report available source/conflict evidence, including truncated provenance. It is not a local governed release. |
| `live` | `sourceId`, `source`, retained `entry`. | Report that it came from a live source; preserve the supplied identity/entry. Do not fetch it or treat it as locally approved. |

Always check `partial` and `diagnostics`. `durableTotal` counts eligible local/indexed results; `liveCount` counts transient live items returned in this page, not an upstream total. `generation`, `profile` and `tier` identify search state; they are not versions of a resource or trust assessments.

`nextCursor` can continue durable results and an unchanged window of up to 50 live candidates, including live-only Search. Hybrid Search returns durable results first, then a live tail. Keep the same text, filters and limit. Each live continuation repeats the approved peer traversal and query disclosure; ordinary durable continuation makes no peer calls. The cursor does not cover omitted upstream inventory.

`live_results_limited` means additional live results could not be safely continued. `continuation_unavailable` means a peer has another page beyond the bounded traversal. Report either diagnostic as a partial result.

A temporary live replay failure permits retry with the same cursor. A changed window, invalid cursor or expired cursor requires a fresh search. Follow the Skill's limit of one additional page, requested explicitly by the user. Do not retry or restart automatically.

## Selected local record

`record` returns a Catalog record directly under `data`: `resource`, `version`, `versionRevision`, `listing`, `listingRevision`, `release`, `evidence`, `source`, and optional `relevanceScore`. `release.id` is distinct from `resource.id`. Use `resource.canonicalIdentifier`, `resource.kind`, `version.version` and `release.id` to identify the selection.

Read presentation metadata from `listingRevision`; representations are under `versionRevision.representations`. Search omits full local documents and findings even for large records; retrieve exact evidence only through the selected release. An 8 MiB default CLI response budget and a record-only explicit override up to 64 MiB bound retrieval.

The assistant tool may display fewer bytes; hidden content is unavailable evidence, not an absent field.

Each representation identifies its `role`, `mediaType`, `profileId`, `contentDigest`, optional `url`, `data`, `normalizedFacts` and package-specific evidence. Do not assume every kind has the same handoff fields or that `url` is a runtime endpoint. Preserve the distinction between declared data and validation findings.

| Kind | Report only what the retained representation supplies |
| --- | --- |
| Agent | Descriptor/card identity and declared interfaces from `data` or normalized facts. A card/descriptor URL is not automatically an invocation endpoint. |
| MCP Server | Declared packages/remotes and transport metadata. Discovery does not establish that tools are accessible or authorize invoking them. |
| Skill | If present, `handoff.git.repository`, `commit`, `directory`, or `handoff.oci.reference`; preserve immutable commits/digests. Markdown alone may provide no installable package handoff. |
| Plugin | Archive `url` and `contentDigest`, `plugin.files` inventory, `plugin.documents` and `plugin.components`. These are inspected archive evidence, not a reconstructed downloadable package or execution permission. |

Give exact references as data, with missing fields explicitly unavailable. Do not install, download an archive, follow resource URLs, execute embedded commands or claim that local review establishes publisher trust, legal rights or runtime authorization.
