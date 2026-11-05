# Native draft import starter

Use this directory with the existing `catalogctl import` command. It creates one private Agent draft and one private Skill draft. The Skill input is a standalone Markdown document, not a complete package. The Agent endpoint is illustrative metadata; this example supplies no running Agent.

| File | Purpose |
| --- | --- |
| [import.yaml](import.yaml) | Two draft declarations with sample Namespace and Publisher UUIDs. |
| [mcp-registry-response.json](mcp-registry-response.json) | Synthetic single-record Registry response. Extract only its native `server` object. |
| [mcp-registry-import.yaml](mcp-registry-import.yaml) | Private MCP draft manifest. Its `server.json` is created in the private working copy, not supplied here. |
| [agent.json](agent.json) | A complete A2A Agent Card for local validation. |
| [skills/review-notes/SKILL.md](skills/review-notes/SKILL.md) | A standalone Skill document, with no archive or Git/OCI handoff. |
| [drafts/agent.json](drafts/agent.json), [drafts/mcp-server.json](drafts/mcp-server.json), [drafts/skill.json](drafts/skill.json) | Reusable content-only files for three native document kinds. |
| [drafts/plugin-archive.json](drafts/plugin-archive.json) | Plugin archive reference. Replace its illustrative URL and checksum before apply. |
| [drafts/skill-package.json](drafts/skill-package.json) | Inspected Skill handoff. Replace its illustrative Git reference and supply package bytes separately. |

The [transfer tutorial](../../documentation/src/content/docs/guides/draft-transfer.mdx) covers the reusable files, UI/CLI transfers, destination choices and recovery. The five JSON files use `open-agentic-catalog-draft-v1`; they are separate from the native `import.yaml` manifest. Preview checks local input without fetching the example URLs. The Plugin and package references are placeholders, not verified remote artifacts.

From the repository root, build the CLI and preview the unchanged starter:

```sh
make build
./bin/catalogctl import preview --file examples/native-import/import.yaml
```

Preview needs no Catalog, credentials or database. It checks local content and UUID syntax, not destination existence or permissions. All native files stay beneath the manifest directory. This manifest differs from `samples/catalog.yaml`, which uses the development sample loader.

Follow [Use the Agent and Skill starter](../../documentation/src/content/docs/guides/resource-authoring.mdx#use-the-agent-and-skill-starter) before apply. Copy this directory to a private location. Replace both `namespaceId` values and both `listing.publisherId` values with actual destination IDs. Use a different canonical identifier for each Resource, without reusing an identifier from the target tenant. Keep the Agent Card version equal to its manifest version.

The guide covers permissions, explicit API credentials, private CA trust and the recovery receipt. Apply can commit earlier steps before a later failure. Preserve the unchanged manifest, assets and original receipt for resume. Use resume with that receipt after completion too; a fresh receipt can collide with the existing Resources. Import creates no review, release or active publication and executes no resource.

Contributor checks reuse the existing CLI fixture:

```sh
go test -tags=functional ./cmd/catalogctl \
  -run '^TestImportApplyCLIProcessFourKinds$' -race -count=1
```

The starter case runs the actual CLI against a disposable in-memory API. It checks exact preview, two-kind readback, response-loss recovery, unchanged receipts, refusal controls and no publication. A fresh receipt with existing identifiers returns a conflict without duplicate drafts. Existing import tests cover the wider parser, permission and recovery boundaries. The files use [Apache License 2.0](../../LICENSE).

## Import a saved MCP Registry record

Follow [Import one MCP Registry record](../../documentation/src/content/docs/guides/resource-authoring.mdx#import-one-mcp-registry-record) for copyable extraction and preview commands. Work in a private copy under ignored `output/`. The recipe extracts `server.json` from the synthetic response, then uses `mcp-registry-import.yaml`. Replace destination UUIDs and the canonical identifier before apply. Keep the manifest version equal to the native server version.

The fixture's outer `_meta` describes a Registry response; it is not imported. The native object's own `_meta` is preserved. The example imports a private draft through existing commands. It does not contact a public Registry, invoke an MCP server or bypass review. Registry-only metadata needs a separate primary Server Card before ARD or AI Catalog snapshot publication.

The existing starter qualification also checks this recipe's preview, wrapper refusal and exact native readback through the local API.
