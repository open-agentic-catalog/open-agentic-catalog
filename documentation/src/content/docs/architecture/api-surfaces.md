---
title: "API surfaces"
---

The checked-in [OpenAPI 3.1 contract](https://spec.openapis.org/oas/v3.1.0.html) defines every implemented API-listener operation. The separate worker listener exposes operations only. This page explains those boundaries; the HTTP API reference renders schemas directly from the checked-in contract.

## Listener boundaries

### Control API

`/api/v1` supports authoring, administration, reads, publication, federation, Jobs, audit and enabled webhooks. Requests require a bearer token and exact active Catalog membership. Current scoped permissions, applicable OAuth ceilings and tenant isolation govern each operation.

### Catalog MCP

Optional `/mcp` provides six read-only Control tools through stateless HTTP. It authenticates each request with the existing Catalog verifier and current membership. Exact tool reads share Control authorization, projection and pagination. OAuth scope checks apply per tool before reads; the adapter bounds responses and preserves JSON-RPC errors and HTTP authentication challenges.

The route uses the API listener, request deadline and configured public origin. It retains no MCP sessions or caller credentials. It refuses streaming methods, unbounded output and ambiguous headers. OIDC deployments also expose public `/.well-known/oauth-protected-resource/mcp` metadata. Development mode advertises no issuer. See the [MCP guide](../guides/mcp-server.mdx#connect-over-http).

### Discovery aliases

The ARD and AI Catalog documents under `/.well-known/` select an exact configured host. Its mode is disabled, anonymous or authenticated. That host binds the tenant, audience, publisher domain and active immutable ARD or AI Catalog snapshot.

### ARD Registry

`/ard/v1` provides pinned Search, Explore and List behavior. The host mode protects the active snapshot and limits federation to its approved topology.

### Operations

API `/livez`, `/readyz`, `/version`, `/metrics` and served OpenAPI are unauthenticated and contain no product data. They share the API listener and Service. Restrict their ingress or routing as required by the deployment.

The worker separately serves `/startupz`, health, status, version and metrics. Its operations port is excluded from the product Service and Ingress. See [observability](../operations/observability.mdx) for exact routes and network requirements.

OpenAPI describes all implemented runtime profiles. Webhook routes exist only when webhooks are enabled. Disabled deployments return the ordinary unknown-route problem and require no webhook key, store, sender or receiver. Check `GET /api/v1/capabilities` before using optional operations.

The running API serves its exact embedded YAML at unauthenticated `GET /openapi/control-v1.yaml`. Import that URL or the checked-in file into a compatible viewer or client tool. The API does not bundle Swagger UI.

## Common HTTP rules

### Command safety

Every mutating control request requires `Idempotency-Key`. Changes to existing revisioned targets also require `If-Match`. Replays check current authorization before returning retained bytes.

### Strict input

Closed control objects reject unknown or wrongly cased fields, duplicate keys, trailing values, invalid media types and over-limit bodies. They reject malformed UTF-8, unpaired surrogate escapes and nulls in non-nullable fields. Standards payloads preserve extensions only at declared boundaries. See [API input rules](../reference/api-conventions.md) for exact behavior.

### Stable errors

Control APIs use [RFC 9457 problem details](https://www.rfc-editor.org/rfc/rfc9457.html), stable service codes and `X-Request-ID`. ARD endpoints retain the pinned `errorCode` and `message` format.

### Authorization

Token verification, active membership, current scoped permission, applicable OAuth ceiling and tenant isolation remain separate checks. Signing in does not grant a role or permission to execute a resource.

### Pagination

Control API cursors are opaque, signed and bound to the caller, purpose, filters and page size. ARD Search/List tokens instead bind the exact host, tenant, audience, operation, request and snapshot generation. Both expire after 15 minutes and recheck current access on each page.

Search and snapshot traversal reject stale bound generations. Mutable administration pages use database keysets and can reflect concurrent changes. Start again for a fresh view; pagination does not retain a database snapshot across requests.

### Compatibility

`/api/v1` uses path-major versioning. Field meaning, defaults, ordering, authorization, idempotency and error semantics must not change silently.

## Operation reference

Use the HTTP API section for generated operations. Every documentation build checks that each OpenAPI operation renders. The source contract and API conventions define schemas, headers, authorization and pagination.

[Authoritative OpenAPI](../../../../../api/openapi/control-v1.yaml) - Exact paths, methods, parameters, bodies, responses, schemas and authentication.

[API conventions](../reference/api-conventions.md) - Identity, request rules, idempotency, errors, cursors and publication.

[API asset guide](../../../../../api/README.md) - Embedded contract, event schema, examples and validation.
