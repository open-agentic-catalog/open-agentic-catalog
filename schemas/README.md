# Standards schemas and specifications

`upstream/` contains third-party files copied from the standards projects that Catalog implements: specifications, schemas, licenses and selected test fixtures. Keeping reviewed copies in this repository is called vendoring. These are not schemas generated from our database or API, and they are not user-supplied Catalog entries.

Catalog uses these files to validate resource metadata and check standards compatibility. Go embeds the bundle into the binaries at build time, so a normal build or deployment does not need to download the standards again or mount this directory. The generated A2A binding is also checked in; ordinary builds do not need the Protobuf compiler.

## Where the files come from

| Local directory under `upstream/` | Source project | Selected material |
| --- | --- | --- |
| `ard/` | [ards-project/ard-spec](https://github.com/ards-project/ard-spec) | Agentic Resource Discovery specifications, schemas, JSON-LD context and conformance fixtures/tooling. |
| `ai-catalog/` | [Agent-Card/ai-catalog](https://github.com/Agent-Card/ai-catalog) | AI Catalog specification, example and Trust Manifest threat model. |
| `a2a/` | [a2aproject/A2A](https://github.com/a2aproject/A2A) | Agent2Agent Protobuf definitions used for Agent Card validation. |
| `mcp/` | [modelcontextprotocol/registry](https://github.com/modelcontextprotocol/registry) | MCP Registry `server.json` schema and reference documentation. |
| `mcp/` | [modelcontextprotocol/ext-server-card](https://github.com/modelcontextprotocol/ext-server-card) | The separate experimental MCP Server Card schema and documentation. |
| `agent-skills/` | [agentskills/agentskills](https://github.com/agentskills/agentskills) | Agent Skills package and `SKILL.md` specification. |
| `agent-plugins/` | [agentplugins/agent-plugins-spec](https://github.com/agentplugins/agent-plugins-spec) | Agent Plugins specification, manifest schema and MCP configuration schema. |

These are selected files, not full clones of those repositories. Each versioned subdirectory is pinned to an exact Git commit: for example, `ard-0.91-aa3e598` identifies the selected ARD version and abbreviated source commit. A profile is the named standards version and validation behavior that Catalog supports. [PROFILES.md](PROFILES.md#active-profiles) records the full commits, [original source paths](PROFILES.md#vendored-source-paths), licenses and compatibility details; use those pins rather than the repositories' moving default branches when comparing files.

Upstream files are retrieved through the GitHub CLI (`gh`) at the recorded commit. For example, this prints the selected ARD specification without modifying the checkout:

```sh
gh api 'repos/ards-project/ard-spec/contents/spec/ard.md?ref=aa3e598bb7752a9175897823234311216acfa864' \
  -H 'Accept: application/vnd.github.raw+json'
```

Third-party files retain their upstream licenses. Our Go embedding/verification code and the maintenance guides alongside `upstream/` are maintained here; they are not downloaded standards.

## Directory contents

| Path | Contents |
| --- | --- |
| [upstream/](upstream) | Versioned ARD, AI Catalog, A2A, MCP, Agent Skills and Agent Plugins inputs with retained licenses and relevant fixtures/tooling. |
| [checksums.txt](checksums.txt) | SHA-256 manifest for the exact vendored files. |
| [embed.go](embed.go), [embed_test.go](embed_test.go) | Embedded bundle, profile inventory and checksum verification. |

Service-owned HTTP and event contracts live separately under [api/](../api/README.md). Generated A2A Go bindings live under [internal/a2apb/](../internal/a2apb); their regeneration toolchain is recorded in the profile reference.

## Validation and authority

The bundle retains selected profiles, licenses, reference documents and test fixtures; `make profiles` reports profile identities and the artifact count. [checksums.txt](checksums.txt) lists the individual artifacts. Not every embedded file is a runtime validator:

| Input within its profile | Use in the service |
| --- | --- |
| ARD `spec/schemas/ard-entry.schema.json` | Entry validation and its `#/$defs/ArdManifest` definition for hosted manifests, followed by Go semantic checks. |
| ARD `spec/schemas/ard.context.jsonld` | Pinned base context for JSON-LD processing. |
| ARD `spec/schemas/ai-catalog.schema.json`, CDDL, predecessor prose and upstream OpenAPI | Retained reference inputs, not runtime validators or the service HTTP contract. |
| AI Catalog `ai-catalog.md` | Structural and semantic rules implemented in Go, separately from the ARD repository's retained AI Catalog schema. |
| A2A `a2a.proto` | Source for the generated Agent Card binding; runtime validation uses strict ProtoJSON and Go semantic checks. |
| MCP Registry `server.schema.json` and Server Card `schema.json#/$defs/ServerCard` | Separate schema validators for distinct metadata formats. |
| Agent Skills `specification.mdx` | Rules implemented by the bounded Markdown/frontmatter and archive inspectors; no upstream JSON Schema is supplied. |
| Agent Plugins `schemas/1.0.0/plugin.schema.json` and `mcp.schema.json` | Manifest and MCP configuration validation, followed by bounded component and archive inspection. |

[internal/standards/](../internal/standards) owns schema compilation, JSON-LD and discovery-document semantics. [internal/adapter/](../internal/adapter) owns native representation and package validation. Upstream examples and the ARD CLI are test evidence, not additional validation authorities.

`make profiles` verifies local embedded bytes against the manifest without contacting upstream. It reports the selected profile identities, artifact count and deterministic bundle digest; it does not establish that a pin is the newest upstream revision or prove protocol conformance. Schema identifiers containing an upstream URL do not select a moving runtime schema: validators load the checked-in bytes. Source-declared remote JSON-LD contexts use the separate bounded safe-fetch policy.

Use these checks from the repository root:

```sh
make profiles
go test ./schemas ./internal/standards ./internal/adapter ./internal/federation -shuffle=on -count=1
make conformance-fixture
make docs-check
make check
```

`make conformance-fixture` exercises pinned ARD examples, actual ingestion and service-owned protocol cases, then runs the unchanged upstream CLI. It needs no running Catalog or external provider. `make verify` includes profile and fixture checks in the broad local gate. That gate includes local HTTP, process and composed functional tests; it is separate from isolated unit testing. When publication or discovery output changes, also run the applicable functional tests and `make conformance-live` against a deliberately selected disposable local service with an active publication; the live gate reads that publication and does not create one.

## Do these files need updating?

Not for ordinary development, builds or deployments. There is no automatic schema update or scheduled refresh requirement. A new upstream release is a reason to review its changes, not to overwrite this bundle. Review an upgrade when supporting a newer format or interoperability requirement, or when an upstream correction or security issue affects the selected inputs. Changes to unrelated upstream guides or automation do not by themselves require a profile switch.

Replacing a schema can change which documents Catalog accepts or rejects. Supporting a new standard may also require adapter, API, publication or configuration changes; downloading a file alone does not implement that support. A local bug fix against the existing standard normally belongs in our validator and its tests, not in the upstream file.

## How to update a profile

1. Compare the selected inputs with a proposed upstream commit and identify changes to validation, normalization, publication and retained evidence. Retrieve upstream content through `gh` at that exact commit, not an unpinned `main` URL.
2. Add the selected files in a new versioned directory with a new profile ID. Preserve original bytes, source-path mappings and applicable licenses. Do not edit existing vendored files or add local READMEs inside pinned directories; existing profile IDs must never acquire different meanings.
3. Update [PROFILES.md](PROFILES.md), the inventory in [embed.go](embed.go), affected adapter/schema paths, configuration defaults/examples and compatibility tests together. For A2A source changes, regenerate the binding using the recorded toolchain and annotation inputs.
4. Regenerate and review [checksums.txt](checksums.txt) for the complete resulting bundle. Do not re-hash an unexplained modification to make verification pass. The manifest paths are relative to `schemas/`.
5. Run `make check` and `make verify`, plus the applicable functional and live-conformance checks described above when behavior changes. Update the standards documentation for the supported version and any compatibility limitations; run `make docs-site-check` when changing the published profile or reference pages.

If `make profiles` fails during ordinary work, inspect the reported file and Git diff first. A checksum failure means the local bundle and its manifest disagree; it does not mean a newer upstream download is required.

`extensions/ratings-v1.schema.json` defines the optional Catalog-owned aggregate extension. It contains publisher-bound count/sum data, not individual opinions or an upstream ARD rating standard. The runtime applies the same closed shape and semantic target checks.
