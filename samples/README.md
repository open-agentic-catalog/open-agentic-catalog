# Development sample inventory

[catalog.yaml](catalog.yaml) is the single default inventory. The `minimal` and `expanded` profiles preserve the original synthetic content exactly. The separate `curated` profile adds reviewed projects and opt-in reachable peers.

API loading defaults to `curated`; `minimal`/`expanded` remain explicit selections. These examples are discovery metadata, not installed or invoked resources. Native Cards are exact files under `cards/`; complete Plugin trees live under `packages/`.

Original standalone Skills use YAML literal strings; curated complete Skill trees use bounded file maps. The Go embedding packages these same files for standalone CLI defaults; there is no second Go-authored dataset.

For your own new drafts, use [`catalogctl import`](../documentation/src/content/docs/guides/resource-authoring.mdx#apply-and-resume-a-native-draft). Its separate `format: 1` manifest references existing namespace/publisher UUIDs and local native assets. It creates drafts through the API with a private recovery receipt. It does not use sample profiles, approve or publish content. Reuse the exact files under `cards/` where appropriate; the import manifest is not `catalog.yaml`.

## Check and prepare

```sh
make demo-check
make demo-check DEMO_SAMPLE_SET=minimal
make demo-check DEMO_DATA_FILE=/absolute/path/to/catalog.yaml DEMO_STATE_FILE=/path/to/receipt.json
make demo-archives-prepare DEMO_ARCHIVE_DIR=output/demo/archives
make demo-archives
```

`demo-check` validates every definition and profile locally using the loader and existing native adapters, then prints selected keys, namespace, lifecycle, origin classification, warnings and execution digests. An optional receipt comparison reports candidate additions and retained keys as **unverified hints**; it cannot establish current API authority or authorize adoption/deletion. It makes no API or runtime requests, starts no server, and creates no TLS or receipt state. `DEMO_STATE_FILE` defaults to the existing demo receipt; missing receipts are ordinary fresh previews.

`DEMO_DATA_FILE` selects the same YAML/native assets for preview, preparation and API loading. `make demo-seed` passes that file to `catalogctl seed-demo --data-file`; an omitted CLI file uses the identical embedded default. Each invocation freezes and validates its selected bytes before governed API writes. `curated` is the developer default; `minimal` and `expanded` remain explicit profiles. Editing a source asset affects a rebuilt standalone binary; custom preview/preparation reads the selected files at invocation. Neither action updates an existing published Resource.

## Manifest contract

| Field | Meaning |
| --- | --- |
| `format` | Integer `1`; the local manifest format, not a standards version. |
| `dataset`, `revision` | Dataset key and nonempty revision string. Comments and ordering do not change selected execution identity. |
| `namespaces` | Unique `key` (the slug) and `displayName`; no tenant override. |
| `publishers` | Unique `key`, `displayName`, normalized publication `domains`. |
| `profiles` | Named lists of unique resource keys; definitions occur once, without overrides. |
| `resources[].key` | Stable lowercase hyphenated key, at most 64 characters. |
| `kind`, `name`, `description` | One supported kind and bounded display metadata. |
| `namespace`, `publisher` | References to declared keys. |
| `identifier`, `version` | Exact AIR identifier under a declared publisher domain, and bounded version string. |
| `state` | Only `published`, `draft`, `pending_review`; no programmable commands. |
| `origin` | `synthetic`, `adapted`, `upstream`; attribution, not trust or invocation proof. |
| `listing` | `tags`, optional `categories`, `representativeQueries` (two to five for published public samples), `license`, `visibility`, optional `documentationUrl`; API authority remains checked during loading. |
| `representation` | `mediaType` and exactly one body source below. The loader supplies the primary role. |

Small native text/JSON bodies use either `inline: |` (a YAML literal scalar) or `file: cards/example.json`. The latter is relative to the selected manifest's directory and preserves exact bytes. A native Plugin uses `mediaType: application/agent-plugins+zip` and `packageDirectory: packages/example`, whose root contains `plugin.json`. Alternatively, `files` maps archive paths to one `inline` or `file` body each. Directory and file-map sources cannot coexist; body sources cannot coexist with a package. There are no remote/recursive includes, aliases, merge keys, custom tags, environment substitutions, templates or executable hooks.

For complete Skills, use `mediaType: application/agent-skills+zip` with a native `packageDirectory` named for the Skill root, or a `files` inventory including that root prefix. Include `SKILL.md` and all referenced files plus a real immutable `handoff.git` (`repository`, full `commit`, `directory`) and/or `handoff.oci.reference`. A directory is packaged under its basename; explicit file maps already include the root. Existing adapter validation checks the root against the handoff. Loading validates files and metadata without executing scripts or fetching Git/OCI. Curation must independently establish truthful source/licence/handoff evidence.

Paths must remain beneath the manifest directory, with no absolute paths, traversal, backslashes, links or special files. Limits are fixed local authoring ceilings: 2 MiB YAML, 32 levels/20,000 YAML nodes, 64 Resources, 8 namespaces/publishers/profiles each, 1 MiB per asset, 128 regular files per package, 16 path segments, 8 MiB total native plan bytes and a 2 MiB encoded create-Version request. This last bound includes base64 and JSON overhead and matches the default backend HTTP limit; a smaller configured server limit remains authoritative. Plugin delivery additionally remains at 64 KiB per archive. Existing native adapter/schema limits also apply. Unsupported or oversized content is rejected, never truncated.

## Category labels and existing catalogs

See [the sample Catalog guide](../documentation/src/content/docs/guides/sample-catalog.mdx#category-labels-and-existing-catalogs) for the maintained reader procedure.

## Prepared archive identity

Preparation validates the complete input before publishing anything. It creates/reuses private TLS in the explicitly selected `DEMO_ARCHIVE_DIR`, writes immutable `objects/<sha256>.zip` bytes and atomically replaces `allowlist.json` only after all admitted objects are complete. Archives are deterministic ZIPs with sorted paths, stored bytes, read-only member permissions and a 1980 timestamp. Filesystem source paths/inline choice are excluded from execution digests; exact native bytes, metadata, lifecycle and resolved Plugin origin are included.

A nonblocking exclusive lock prevents concurrent preparation. Existing paths are retained even when another selected profile omits them; they may never be rebound to new bytes. Invalid existing publications fail without repair or replacement; interrupted unreferenced staging files are not admitted. Repeated identical preparation preserves archive and TLS identities. Additive preparation may add new paths; changing a published package requires a new path and ordinary new-version governance, never overwriting the original URL. This is local fixture delivery, not artifact hosting.

The listener reads and verifies the whole prepared allowlist/object set before serving a frozen in-memory snapshot. It serves only anonymous exact GET paths; arbitrary files, queries and credentials are rejected. API/worker load only the bounded allowlist and public CA through `demoArchives.allowlistFile` and `trustBundleFile`, mounted read-only by `compose.demo.yaml`.

The private TLS key and archive directory belong only to the fixture. Allowlist entries bind fixed development tenant, exact path, SHA-256 and byte size into the hardened fetch policy together with origin/trust. Missing/mismatched input fails closed.

Restart fixture and API/worker after adding admitted paths. Changed origin/trust/allowlist invalidates current-policy external evidence for future publication; it does not erase the active snapshot or retained history. Existing authorized API revalidation is required before republishing affected external records; explicit extension preflights exact bytes/trust and revalidates receipt-owned published Plugin revisions under the current policy before constructing a replacement snapshot.

Production/OIDC/Helm remain unable to enable this route; disabled mode reads neither file.

The [configuration reference](../documentation/src/content/docs/reference/configuration.mdx#development-demo-archives) owns operational settings. The [local development guide](../documentation/src/content/docs/guides/local-development.mdx#local-plugin-archive-fixtures) and [fixture harness](../test/demo/README.md) explain startup and owned qualification. The maintained `make test-demo-catalog` gate verifies the complete owned developer workflow.

## Curated real projects

See [the sample Catalog guide](../documentation/src/content/docs/guides/sample-catalog.mdx#curated-real-projects) for the maintained reader procedure.

<a id="start-and-test-the-two-local-peers"></a>

See [Start and test the two local peers](../documentation/src/content/docs/guides/sample-catalog.mdx#start-and-test-the-two-local-peers).

<a id="source-only-agent-setup"></a>

See [Source-only Agent setup](../documentation/src/content/docs/guides/sample-catalog.mdx#source-only-agent-setup).

<a id="hosted-connections-context7-and-aws"></a>

See [Hosted connections: Context7 and AWS](../documentation/src/content/docs/guides/sample-catalog.mdx#hosted-connections-context7-and-aws).

## Load, extend and recover

See [the sample Catalog guide](../documentation/src/content/docs/guides/sample-catalog.mdx#receipts-and-reruns) for the maintained reader procedure.

## Qualify the complete developer experience

After installing the existing UI/A2A/MCP prerequisites, run `make test-demo-catalog` alone. The [owned qualification guide](../test/demo/README.md#complete-curated-developer-workflow) describes real PostgreSQL loading, browser discovery/handoff, Catalog-selected native calls, YAML-only extension, immutable evidence/unrelated-release preservation and archive/peer outage recovery. It uses a copied dataset and a unique disposable project; it never changes your retained developer Catalog. Its outcomes are workflow assertions, not statement coverage.

## Optional owned artwork fixture

`--sample-set artwork` selects one synthetic Agent with an owned PNG icon and JPEG preview. Existing `minimal`, `expanded`, and `curated` selections are unchanged. These geometric CC0-1.0 fixtures demonstrate media delivery; they are not real product screenshots. The existing owned HTTPS fixture serves `/artwork-icon.png` and `/artwork-screen.jpg`; `--archive-origin` substitutes only its exact default origin in this profile.

Before loading this optional profile, the operator must explicitly configure `listingMedia.allowedOrigins` for the selected fixture origin and its existing private `networkProfile`, including the current tenant assignment and trusted fixture CA in federation policy. Empty media bindings deny fetching. The sample and CLI never grant network authority or change runtime settings. Run the normal governed seed workflow only against an explicitly selected owned development Catalog; no hosted images are contacted.
