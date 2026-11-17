# Declared tool fixtures

These synthetic documents use the private `open-agentic-catalog-mcp-tools-1` profile. They are metadata representations attached to an MCP VersionRevision, alongside its unchanged primary definition. No tool or endpoint is invoked.

Release a server named Document Utilities with listing text "Document processing" and `tool-only.json`. Ordinary Search for `pdf tables` must miss; Search with `includeMcpTools: true`, `origins: [local]` must return that server once, identify `extract_pdf_tables` and bind its declaration to the selected release/revision. Metadata-only readers must not gain the tool-only match.

Saving `unpublished-edit.json` as a new draft must leave the released match unchanged. That draft needs independent validation/review before replacing discovery. Omitting the metadata representation means not supplied; `empty.json` declares an empty list. Neither state establishes runtime capability.

`FuzzMCPToolDeclaration`, selected by `make test-fuzz`, checks malformed UTF-8, accepted declaration bounds and JSON codec stability with native Go fuzzing. It starts no server and makes no network request.

## Attributed dataset-derived example

`dataset-derived.json` retains only the tool names and descriptions from the
first two rows (`app_get`, `app_list`) of Neuronto's
[Verified MCP Tools](https://huggingface.co/datasets/AgenticResourceDiscovery/verified-mcp-tools),
`tools.jsonl` at snapshot `8356c9079b9ac3229ff7acc0b02287644109afc2`.
The dataset is licensed under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
Credit: Neuronto Agentic Resource Discovery Index and the publisher-declared
inference.sh tool descriptions credited by that dataset.

Changes: projected `tool_name` to `name` and `tool_description` to `description`;
removed schemas, endpoint, timestamps and collection fields; wrapped the two
entries in the Catalog's private declaration format. Original source identifier:
`urn:air:ac.inference.sh:mcp:mcp`; publisher `ac.inference.sh`.

These are historical observations, used only as inert parser/matching fixtures.
They supply no upstream version, current capability, Catalog approval or runtime
authority. Tests never contact the publisher. Any Catalog release around these
rows is explicitly synthetic. The regular suite needs no dataset download.
