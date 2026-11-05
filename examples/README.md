# Integration examples

This directory contains small integration examples for the Catalog's public contracts.

Use the manual's [integration example chooser](../documentation/src/content/docs/guides/integrating.mdx#choose-an-integration-example) to compare prerequisites, credentials, discovery scope and persistent state before selecting a guide.

| Directory | Purpose |
| --- | --- |
| [client-skills/](client-skills/README.md) | Portable read-only Catalog discovery instructions for a terminal-capable assistant using the installed CLI. |
| [langgraph/](langgraph/README.md) | Read-only local-model assistant with two LangGraph tools backed by the Python SDK: Search local releases and inspect selected exact records. |
| [rust-catalog-report/](rust-catalog-report/README.md) | Standalone Rust SDK CLI that searches authorized local releases, verifies exact records and prints a bounded Markdown inventory. |
| [publication-report/](publication-report/README.md) | Standalone Go SDK HTTPS receiver with a durable PostgreSQL inbox, current publication-state report and periodic reconciliation. |
| [typescript-ard-list/](typescript-ard-list/README.md) | Standalone Node client using the TypeScript SDK for published-only ARD List, bounded metadata output and explicit complete or incomplete traversal. |
| [native-import/](native-import/README.md) | Copyable Agent Card, standalone Skill document and manifest for offline preview and explicit draft creation through the existing CLI. |
| [tutorials/local-oidc/](tutorials/local-oidc/README.md) | Local password, OIDC and two-Catalog tutorial inputs; explicit preparation creates separate private state outside the checkout. |
| [webhookreceiver/](webhookreceiver/README.md) | Go signature-verification helper and HTTP handler outline. The README explains the durable acceptance, CloudEvents validation and deduplication the receiving application must implement. |
| [mcp-scanner/](mcp-scanner/README.md) | Optional offline YARA scan and signed-webhook receiver for explicitly mapped local MCP definitions. Reports stay private; native YARA qualification remains pending. |

Governed resource metadata and package inputs live in [samples/](../samples/README.md); follow the [sample Catalog guide](../documentation/src/content/docs/guides/sample-catalog.mdx) to load them. Reviewed HTTP request and notification payloads live in [api/examples/](../api/examples) and are indexed in the [API asset guide](../api/README.md). Executable smoke and hostile upstream fixtures belong to [test/](../test/README.md), not this integration-example directory.

Keep examples small, bounded and consistent with the owning contract. Discovery never grants permission to install or execute a resource, and webhook notifications do not replace authorized current-state reads. Run `go test ./examples/webhookreceiver -shuffle=on -count=1` after receiver changes and follow the [webhook runbook](../documentation/src/content/docs/operations/webhooks.mdx) for protocol and key requirements.
