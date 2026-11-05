# API examples

The [example index](../README.md#reviewed-examples) describes each JSON file and its prerequisites. Files are embedded as reviewed assets, not executed by the service.

- `agent-lifecycle.json` is an annotated sequence: send each `body` to its `method` and `path`, supplying the indicated identity, idempotency key and current ETag. Replace saved-ID placeholders with actual responses.
- `https-representation.json` and `federation-source.json` contain separately named request bodies plus prerequisites. Do not send their surrounding description or instructions to an API endpoint.
- `audit-export-request.json` is a direct request body; replace its filters and illustrative time range.
- `hybrid-search.json` contains named Search/reindex bodies, live/hybrid continuation bodies and core, hybrid, indexing and outage response examples. Copy the actual opaque `nextCursor` and preserve the request criteria. Reindex is a separate command with an idempotency key. It requires tenant-level `operations.reindex` and, when OAuth scopes are enforced, `tenant.admin`. Resource read permission alone does not admit it.
- `ard-manifest.json` and the three `*webhook-delivery.json` files are output examples. Webhook signatures cover the actual delivered bytes; these files have no signature headers.

Example URLs are not hosted services. Use a disposable Catalog for workflows that create records or activate snapshots. For runnable Agent, MCP Server and Skill examples, use the [local walkthrough](../../documentation/src/content/docs/guides/local-development.mdx). The [Plugin guide](../../documentation/src/content/docs/guides/agent-plugins.mdx) covers complete HTTPS archives and the same review/publication workflow without duplicating the lifecycle template here.

Run `make api-check` for unambiguous JSON and request-schema checks, `make api-contract-check` for HTTP and event checks, and `make verify` for the composed capabilities example and pinned ARD conformance fixtures. These gates do not contact the example URLs or execute the annotated templates.
