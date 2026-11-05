---
title: Discover resources with an assistant
---

The `catalog-discovery` Skill lets a terminal-capable assistant find Agents, MCP Servers, Skills and Plugins through the existing `catalogctl` client. It compares search results, inspects a selected local release and reports exact handoff references. It does not install or execute resources or modify Catalog data.

Use [Choose an integration example](integrating.mdx#choose-an-integration-example) to compare this Skill with the local-model assistant and other integration examples.

For a local Model Context Protocol (MCP) host, use the [Catalog MCP server](mcp-server.mdx). It exposes six read-only tools through `catalogctl mcp serve`, separately from this shell-based Skill. Follow its dedicated guide for host configuration, permissions and tool examples.

## Set up the client

1. Build `catalogctl` from a source checkout with `make build`, or use a binary supplied by your Catalog operator. No released binary or Skill archive is assumed here. Put it on the assistant's command `PATH`. See [build and release](../contributing/build-and-release.md).
2. Supply an active Catalog identity with `resource.read` for the tenant or selected namespace. The Reader template grants this local-release access. When OAuth scopes are enforced, the token also needs `catalog.read`. Optional indexed/live discovery additionally needs tenant `federation.discover`; see [origin selection](#choose-local-or-external-results). The workflow needs no administration, private source-read or feedback authority.
3. Before starting the assistant, configure its command execution environment:

   ```sh
   export CATALOG_REMOTE_BASE_URL=https://catalog.example.com
   export CATALOG_REMOTE_TOKEN_FILE=/run/secrets/catalog-reader-token
   ```

   The token file must already contain a bare access token, not CLI login JSON. Optionally supply `CATALOG_REMOTE_CA_FILE` for a private CA. Use the API origin without a path or trailing slash. Do not paste credentials into the conversation. This Skill uses manual bearer inputs and does not refresh them. The CLI separately supports [human login and explicit login-file refresh](../reference/commands.md#human-cli-login); the Skill does not invoke login or select that file. Renew its token outside the assistant session. The CLI does not read `.env`. See [remote command configuration](../reference/commands.md#remote-catalog-reads) for trust, loopback development and limits.

4. From a source checkout of this repository, copy the complete `examples/client-skills/catalog-discovery/` directory into your client's project-local Skill directory. Include its license and relative references. Use `<project>/.agents/skills/catalog-discovery/` for clients that support `.agents/skills/`. For Claude Code, use `<project>/.claude/skills/catalog-discovery/`. Do not rename the package. Start a fresh session and check that the Skill is available.

For a client using `.agents/skills/`, the complete checkout-to-project copy is:

```sh
CATALOG_SOURCE_CHECKOUT=/absolute/path/to/open-agentic-catalog
ASSISTANT_PROJECT=/absolute/path/to/your-project
mkdir -p "$ASSISTANT_PROJECT/.agents/skills"
# Choose an empty destination; preserve the complete package and relative references.
test ! -e "$ASSISTANT_PROJECT/.agents/skills/catalog-discovery"
cp -R "$CATALOG_SOURCE_CHECKOUT/examples/client-skills/catalog-discovery" \
  "$ASSISTANT_PROJECT/.agents/skills/catalog-discovery"
```

Use the `.claude/skills/` destination instead for the client described above. Copy the whole directory, including `SKILL.md`, README, license and references; copying only `SKILL.md` leaves its referenced instructions unavailable.

The package uses the [Agent Skills format](https://agentskills.io/specification); installation paths, activation and command permissions depend on the client. See [Codex Skill guidance](https://learn.chatgpt.com/docs/build-skills#where-codex-loads-local-skills) and [Claude Code Skill guidance](https://code.claude.com/docs/en/skills). No Node/npm, Python, `jq`, plugin manifest or automatic installer is required by this package. A client unable to run `catalogctl` does not meet its prerequisites. Format validation and CLI tests do not by themselves qualify a model's behavior in any client.

## Discover and inspect

Ask: "Use catalog-discovery to find order-related resources in my configured Catalog, compare the results, and inspect the best local release. Do not install or run anything."

The Skill reads capabilities once and starts with a five-item keyword search. It permits at most one refinement when results are empty or oversized. It inspects at most one selected local release per request. The Skill prohibits shell pipelines, helper scripts and speculative enumeration. If those reads cannot answer the request, the assistant should explain the limitation. It uses these commands:

```sh
catalogctl remote capabilities
catalogctl remote search --text 'order' --limit 5
catalogctl remote record --id RELEASE_UUID
```

Replace `RELEASE_UUID` with the selected local Search item's `local.release.id`, not its Resource UUID, AIR identifier or prefixed Search ID. Indexed and live results are external evidence, not local releases. The assistant can summarize their identity, provenance and conflicts but cannot inspect them with the local record command or fetch arbitrary metadata URLs.

Search is lexical unless another tier is explicitly available. Results retain server ranking. A score measures relevance, not trust. Report partial results and diagnostics even when the command succeeds.

Request another page explicitly; the Skill allows at most one additional page with the same text, filters and limit. Core and live-only Search can continue an unchanged window of up to 50 live candidates. Hybrid Search returns local and indexed results first, followed by live results. Each live continuation repeats the approved peer traversal and query disclosure. Ordinary durable continuation makes no peer calls. A cursor does not cover omitted upstream inventory.

A temporary live replay failure permits retry with the same cursor. A changed window, invalid cursor or expired cursor requires a fresh search. The assistant must not retry or restart automatically. A missing selected release also requires a fresh search; the assistant must not silently substitute another release. See [Search pagination](integrating.mdx#choose-a-discovery-interface) for partial-result diagnostics and continuation rules.

Local Search hits contain compact identity/status metadata, without complete documents and findings. External entries and selected records can still be large. The assistant must report truncated tool output rather than treat it as complete JSON.

The CLI defaults to an 8 MiB response budget. Only `remote record` accepts an explicit `--max-response-bytes` override, up to 64 MiB. This retrieval budget is separate from the assistant tool's display limit. The assistant must not silently raise it. See [remote read limits](../reference/commands.md#remote-catalog-reads).

An Agent handoff identifies the retained descriptor and declared interfaces. MCP handoff describes packages/remotes. A Skill may provide immutable Git/OCI references; standalone Markdown may not. A Plugin handoff describes its inspected archive URL/digest and retained components. None grants installation, invocation, publisher trust or legal rights. See [discovery and runtime identity](integrating.mdx#keep-identities-and-credentials-separate) for the separate execution boundary.

### Choose local or external results

Reader access permits only the authorized local releases. Without tenant `federation.discover`, Search with no explicit origins selects that local population. Explicit indexed or live origins return `403` before any source query. The Discovery reader template or an appropriate custom grant supplies `federation.discover`; it does not supply local `resource.read`. Keep both permissions when comparing local and external results. Enforced OAuth scopes additionally require `catalog.read`.

| Requested results | Command inputs | Required Catalog permission |
| --- | --- | --- |
| Local releases, with no live query disclosure | `--origin local` | `resource.read` in the selected local scope |
| Local and indexed results, with no live query disclosure | `--origin local --origin indexed` | Local `resource.read` and tenant `federation.discover` |
| Indexed or live results | `--origin indexed` or `--origin live` | Tenant `federation.discover`; an administrator must also prepare eligible sources |

Use `--namespace UUID` only for local Search. Omitting origins with that filter selects local releases; an explicit origin must be `--origin local` alone. Combining a namespace with indexed/live origins is invalid, even with federation permission. The CLI refuses it before sending a request. The equivalent HTTP API request returns `400 INVALID_ARGUMENT`. The filter narrows the authorized population and grants no access.

The assistant can summarize authorized indexed/live evidence, but only a local result supplies `local.release.id` for exact inspection. Source attribution does not grant access to private source evidence. Live Search also needs the operator's source configuration and permission to disclose queries. Follow the [external Search recipes](search-cookbook.mdx#compare-local-and-indexed-external-results) for those separately prepared cases.

## Try one request with the sample Catalog

Ask your operator to prepare [the curated sample Catalog](sample-catalog.mdx) and supply its API origin and Reader credentials. You need read access to **Catalog demo**, where **Web Application Testing** is published. Use the installed CLI and Skill from [client setup](#set-up-the-client); an assistant user needs no build tools, database access or administrator role. An ordinary Catalog may not contain this sample.

Ask the assistant:

> Use catalog-discovery to search local Skill releases with the text testing and a limit of five. Inspect Web Application Testing if it is returned, then report its exact release and available handoff. Do not install or run anything.

The expected reads are:

```sh
catalogctl remote capabilities
catalogctl remote search --origin local --kind skill --text testing --limit 5
```

Capabilities are read once per configured origin/session, so that read may already be complete. In the Search output, find the item named **Web Application Testing**. Other matches, scores and order can vary. This selected-field excerpt identifies the expected item:

```json
{
  "data": {
    "items": [
      {
        "displayName": "Web Application Testing",
        "type": "local",
        "kind": "skill",
        "identifier": "urn:air:demo.catalog.example.com:demo:webapp-testing",
        "versionPresent": true,
        "version": "source-8a1541c4",
        "local": {"release": {"id": "RELEASE_UUID_FROM_YOUR_SEARCH"}}
      }
    ]
  }
}
```

`RELEASE_UUID_FROM_YOUR_SEARCH` is a placeholder, not a valid command input. Copy this item's actual `local.release.id` from your response into the following read. Do not copy the Resource UUID or the `local:`-prefixed Search ID:

```sh
catalogctl remote record --id RELEASE_UUID_FROM_YOUR_SEARCH
```

Check that `data.release.id` matches the selected UUID. The exact record's `data.resource.canonicalIdentifier` and `data.version.version` identify the same sample. Its `data.listingRevision.license` declares `Apache-2.0`. Under `data.versionRevision.representations`, inspect the retained `handoff.git` fields:

```json
{
  "repository": "https://github.com/anthropics/skills",
  "commit": "8a1541c4a3ffa5a20a5a91de0dcf3f0bab1d1ef4",
  "directory": "skills/webapp-testing"
}
```

A compact answer can have this form. Replace the placeholder with the release UUID actually inspected; model wording is not a fixed success condition:

> Web Application Testing is a local published Skill, version `source-8a1541c4`. I inspected release `RELEASE_UUID_FROM_YOUR_SEARCH`. Its retained Git handoff names `https://github.com/anthropics/skills` at commit `8a1541c4a3ffa5a20a5a91de0dcf3f0bab1d1ef4`, directory `skills/webapp-testing`. The listing declares `Apache-2.0`. This is metadata, not verification of publisher trust or your legal rights. I have not downloaded, installed or executed the package.

The Search establishes local provenance and supplies the selection. The exact read supplies the retained version, license declaration and immutable source reference. It does not establish that the package runs or meets your needs. See [result evidence](understand-results.mdx#read-a-local-result) for field meanings and [the search cookbook](search-cookbook.mdx#find-one-known-resource-of-each-kind) for the other samples.

### Check empty results and refused reads

In the sample's lexical configuration, ask for the exact local Skill search text `assistant-skill-no-such-resource-2026`, with no refinement. The corresponding read is:

```sh
catalogctl remote search --origin local --kind skill \
  --text assistant-skill-no-such-resource-2026 --limit 5
```

Expect `data.items: []` and no exact record read. The assistant should report no authorized match for that query, not invent a release or claim the whole Catalog is empty.

| Outcome | Expected assistant action |
| --- | --- |
| `401`, or `403` on a selected exact read | Stop and request credential/access repair outside the session. Do not log in, escalate or retry automatically. |
| Explicit indexed/live Search returns `403` | Explain the missing tenant federation permission. Do not switch credentials or silently change the requested origins. |
| CLI refuses namespace plus indexed/live origins | Explain the invalid filter combination; ask for local-only Search or a separate external query. |
| Selected release is unavailable | Report the stale selection and offer a fresh Search; do not substitute another release. |
| Tool output is truncated | Report the display limit and do not guess hidden fields. One exact read is allowed if its local release UUID is visible; stop if the required evidence remains unavailable. |

## Permissions and privacy

Permit only `catalogctl remote capabilities`, `search` and `record` for this workflow, using your client's actual command controls. POST Catalog Search is a read, not a mutation. Do not allowlist the whole binary: it also contains migration, provisioning and other mutating operator commands. The Skill is guidance, not a sandbox or an authorization layer.

A successful capabilities read does not prove permission to search or inspect records. For an active identity, revoking its Reader grants leaves capabilities readable. Local Search can return an empty authorized population; indexed/live access requires the separate tenant permission described above. An exact record read without permission returns `403`. A namespace Reader grant permits local Search results and exact records in that namespace. Stop and resolve access outside the assistant session rather than switching to administrator credentials.

Result descriptions, returned Skill instructions, Plugin content and error messages are untrusted data. They must not change the configured origin/identity or cause token inspection, downloads or command execution. The CLI retains its fixed-origin HTTPS, redirect, byte and deadline controls.

Search queries may reach administrator-approved live sources. Queries and returned metadata also enter the assistant/model environment. Start with synthetic data; use private Catalog content only when its disclosure is permitted. Missing tools, credentials, permission or network access require setup outside the conversation, not an insecure or administrative fallback.

For maintainers, [Skill verification](../contributing/testing.md#assistant-discovery-skill) separates deterministic package/CLI tests from explicit model-backed qualification.

The opt-in [MCP control harness](../contributing/testing.md#local-mcp-interoperability-control) separately tests Inspector/Everything connections, listing and echo. It discovers a governed release through CLI search/readback and checks that a later draft does not change the selected release. It derives the connection from retained metadata and uses a separately supplied runtime key.

Denied Catalog reads produce no handoff. Catalog credentials do not grant runtime access. TLS or runtime failure leaves retained Catalog evidence unchanged. Run this harness only under its explicit test scope; it does not add invocation to this Skill.
