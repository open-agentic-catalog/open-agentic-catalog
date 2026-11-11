# Reviewed upstream resource fixtures

These are inert inputs for the [native adapter tests](../../README.md). Scripts, notebook cells, resource endpoints and instructions are data. Do not execute them or follow their URLs during metadata validation.

## Sources and terms

Every upstream file keeps its original bytes. Git attributes disable line-ending conversion for this corpus, including the C# producer's CRLF bytes. All selected Git files have mode `100644`; archive tests preserve `0644`, including the non-executable Python helper. [checksums.txt](checksums.txt) records SHA-256 for every retained input and license. Tests require the complete file set, not only matching files that happen to remain.

| Local path | Immutable source | Scope and terms | Assertion |
| --- | --- | --- | --- |
| `openai/jupyter-notebook/` | [OpenAI Skills, `49f948faa9258a0c61caceaf225e179651397431`, `skills/.curated/jupyter-notebook/`](https://github.com/openai/skills/tree/49f948faa9258a0c61caceaf225e179651397431/skills/.curated/jupyter-notebook) | Complete twelve-file subtree; package-local [Apache-2.0 license](openai/jupyter-notebook/LICENSE.txt). | Quoted frontmatter, notebook and artwork bytes, local references, exact package inventory and immutable Git handoff. |
| `huggingface/hf-cli/SKILL.md` | [Hugging Face Skills, `25910b3d0ba31a2ba102f901cdc9e008c278a69c`, `skills/hf-cli/SKILL.md`](https://github.com/huggingface/skills/blob/25910b3d0ba31a2ba102f901cdc9e008c278a69c/skills/hf-cli/SKILL.md) | Standalone metadata; repository [Apache-2.0 license](huggingface/LICENSE). | Exact 1,008-character description and unchanged Unicode Markdown. No complete-package claim. |
| `a2a-dotnet/ResearcherAgent.cs` | [A2A .NET, `8d3a083091f730c05c7962eb6caf65e75d51c587`, `samples/AgentServer/ResearcherAgent.cs`](https://github.com/a2aproject/a2a-dotnet/blob/8d3a083091f730c05c7962eb6caf65e75d51c587/samples/AgentServer/ResearcherAgent.cs) | Source evidence; repository [Apache-2.0 license](a2a-dotnet/LICENSE). | Reviewed derivation below; the C# producer is never compiled or run. |
| `playwright/server.json` | [Microsoft Playwright MCP, `e436f64eb7e1904b6c302da0997a2243496d191e`, `server.json`](https://github.com/microsoft/playwright-mcp/blob/e436f64eb7e1904b6c302da0997a2243496d191e/server.json) | Unchanged distribution document; repository [Apache-2.0 license](playwright/LICENSE). | Exact npm package, concrete version and stdio transport. |
| `context7/server.json` | [Context7, `c5e34a01799bce82534eb0ed74bb16b025d75f47`, `server.json`](https://github.com/upstash/context7/blob/c5e34a01799bce82534eb0ed74bb16b025d75f47/server.json) | Unchanged distribution document; repository [MIT license](context7/LICENSE). | npm/stdio and HTTP distributions, optional secret descriptors, inert icon URL. |
| `context7/plugin/` | [Context7, the same commit, `plugins/agent-plugins/context7/`](https://github.com/upstash/context7/tree/c5e34a01799bce82534eb0ed74bb16b025d75f47/plugins/agent-plugins/context7) | Complete five-file subtree; package-local [MIT license](context7/plugin/LICENSE). | Version `1.0.2`, exact inventory, one Skill and one MCP declaration; `/mcp/oauth` remains distinct from Registry `/mcp`. |

The reviewed source trees contain no root `NOTICE` or `THIRD_PARTY_NOTICES.md` for these selections. The copied licenses retain their original attribution. Terms apply to these selected files, not their declared runtime dependencies.

## Agent Card derivation

[researcher-card.json](a2a-dotnet/researcher-card.json) is a Catalog-authored static derivation of `ResearcherAgent.GetAgentCard`, not captured native JSON. C# property names map to the pinned A2A ProtoJSON names. The only parameter, `agentUrl`, is `https://agents.example.com/researcher`, an inert reserved-domain address. Name, description, version, interface binding/version, input/output modes, streaming/push declarations and all Skill fields match the producer. Explicit `pushNotifications: false` preserves the declaration even though ProtoJSON can omit default scalars. No signing, authenticated extended-card or wire-interoperability claim follows from this fixture.

## Verification and limits

Run the ordinary targets from the repository root:

```sh
make test-unit
make check verify CATALOG_ENV_FILE=/dev/null
```

Run `test-unit` separately; it bypasses local Make environment files. `make test` or `make test-functional-local` also runs both corpus layers. `make test-e2e` selects only the existing `TestEndToEnd` journey and excludes this corpus scenario. See the [testing guide](../../../../documentation/src/content/docs/contributing/testing.md#reviewed-upstream-resource-fixtures) for prerequisites and aggregate selection.

For focused iteration, run the two layers separately with the repository Go toolchain and module dependencies installed:

```sh
go test ./internal/adapter -run '^TestReviewedUpstream' -shuffle=on -count=1
go test -tags=functional ./internal/integration -run '^TestReviewedUpstreamGovernanceAndPublication$' -shuffle=on -count=1
```

The [adapter units](../../upstream_corpus_test.go) validate the source checksums, complete inventories, native schemas and service semantic results offline. ZIP and tar.gz containers are test-authored; their member bytes and executable flags match the source. The tests also account for the Jupyter Skill's local references without crawling external links.

The [functional scenario](../../../integration/upstream_corpus_functional_test.go) uses a disposable in-memory Catalog and the same corpus. It checks authoring, validation, immutable readback, review/release and publication eligibility. Inline JSON follows the Control API's deterministic encoding; Markdown and package members retain their exact bytes. Only the owned Plugin archive URL is supplied through a fake fetch port; resource URLs never execute. Functional evidence does not establish PostgreSQL persistence, independent consumer loading or native handoff execution.

With their separate pinned tools installed, run `make sdk-check` for all four SDKs, then `make vscode-check` and `make test-vscode` separately. The [SDK harness](../../../../test/sdk/README.md) checks actual typed HTTP readback from a second owned Catalog, including source fields, exact Markdown, package inventories, retained Plugin documents and tenant denial. The [extension harness](../../../../test/vscode/README.md) also checks the installed VSIX's paging, detail and native evidence editors. Both reuse this input tree without executing its scripts, following its runtime URLs or installing its packages. These metadata checks do not qualify native source consumers or .NET wire serialization.

Package-only Skills remain private handoffs. Registry metadata is not a Server Card. Plugins retain the existing generic archive projection. License, malware, secrets, SBOM, provenance and broad trust assessments remain `not_assessed`; source attribution and schema success confer no execution authority. These fixtures are not embedded in sample data, application images or SDKs, and the existing sample profiles are unchanged.

## Update the corpus

1. Select an immutable source commit and review its applicable licenses, attribution and notices. Do not use a moving branch or live endpoint as a test input.
2. Review the complete selected subtree and Git file modes. Copy exact upstream bytes, including supporting files for a complete-package case. Keep standalone metadata cases explicitly labeled.
3. Review expected native fields independently of the adapter output. Update the source table, any documented derivation, checksums and corresponding test expectations together.
4. Run both focused commands above. Then run `make test-unit` separately and `make check verify CATALOG_ENV_FILE=/dev/null`.

The inventory test requires every retained input and license in `checksums.txt`; only this guide and the checksum file itself are excluded. Confirm additions and removals against the reviewed source tree before updating hashes. Keep test-authored archive containers distinct from upstream release artifacts. Do not format copied files, execute their code, follow declared endpoints or raise admission limits to obtain a pass. Preserve synthetic refusal tests for malformed and unsupported inputs.
