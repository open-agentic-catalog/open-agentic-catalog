# JavaScript and TypeScript SDK

The ESM package `@open-agentic-catalog/client` provides typed methods for the complete [Catalog API](../../api/openapi/control-v1.yaml). It ships JavaScript with TypeScript declarations and uses the [Apache License 2.0](../../LICENSE).

Supported Node ranges are `>=22.23.4 <23` and `>=24.20.0 <25`. Maintainer tooling uses pinned Node 24.22.1 and npm 11.19.1. Consumers do not need npm 11. Type declarations are qualified with TypeScript 5.9.3. Modern browsers need Fetch, Request, Response, ReadableStream, AbortController and ES2022. The check qualifies Chromium only. Browser consumers need a bundler or import map for `openapi-fetch`.

## Install the SDK

Maintainer-published GitHub Releases provide SDK downloads. Follow the [download and checksum instructions](../../documentation/src/content/docs/contributing/build-and-release.md#download-release-packages) before installation. Packages are not published to ecosystem registries. Source installation remains available below.

No registry release is published. A maintainer builds the local package from this repository:

```sh
make sdk-tools-install SDK_LANGUAGE=typescript
make sdk-package SDK_LANGUAGE=typescript
```

Run each command separately. Installation acquires the pinned tools; packaging uses those installed inputs. See [tool prerequisites](../../documentation/src/content/docs/contributing/testing.md#javascripttypescript-sdk-verification). `make sdk-check SDK_LANGUAGE=typescript` separately qualifies generation, tests and the installed artifact.

Install the actual tarball in your consumer project:

```sh
npm install /absolute/path/to/open-agentic-catalog-client-0.1.0.tgz \
  --ignore-scripts --no-audit --no-fund
```

Replace the path and version with the artifact reported by the package target. Normal npm installation can download runtime dependencies. Your project must use ESM, for example through `"type": "module"` in its package.json. TypeScript projects can use `module` and `moduleResolution` set to `NodeNext`.

## Make your first request

Save this example as `catalog-example.mjs` in your consumer project. It is valid JavaScript and TypeScript. Supply `CATALOG_SDK_ORIGIN` and `CATALOG_SDK_TOKEN` through the process environment. The token needs an active Catalog membership.

<!-- sdk-example: first-request -->

```ts
import { createCatalogClient } from "@open-agentic-catalog/client";

const origin = process.env.CATALOG_SDK_ORIGIN;
const token = process.env.CATALOG_SDK_TOKEN;
if (!origin || !token) throw new Error("Supply the Catalog origin and access token");
const client = createCatalogClient({
  origin,
  token,
  // allowLoopbackHTTP: true, // Only for an explicitly selected local API.
});
const result = await client.api.GET("/api/v1/capabilities");
if (result.response.status !== 200 || !result.data) {
  throw new Error(`Capabilities returned HTTP ${result.response.status}`);
}
console.log("Catalog API connection succeeded");
```

Run the file with Node:

```sh
node catalog-example.mjs
```

This request reads capability metadata without changing Catalog data. TypeScript projects need their usual Node type declarations for `process.env`. In browser code, supply the configured origin and token directly instead of using Node's `process.env`.

## Connect and read discovery

For a complete Node application, use the [published discovery example](../../examples/typescript-ard-list/README.md). It lists an active ARD publication, validates response fields and reports complete or incomplete traversal. Its guide covers local package installation, anonymous and authenticated audiences, private trust and bounded output.

Use the configured client for ARD discovery:

```ts
const result = await client.api.GET("/.well-known/ard.json");
if (result.response.status !== 200 || !result.data) {
  throw new Error(`Discovery returned HTTP ${result.response.status}`);
}
```

Use `origin` without a path, query, fragment or embedded credentials. For local tests, explicitly set `allowLoopbackHTTP: true` and use a literal loopback address. Set `token` or `tokenProvider`, never both. Omit both for anonymous discovery. The provider runs once per request; it does not refresh or obtain tokens automatically. The SDK never logs response bodies or credentials.

For changing credentials, supply `tokenProvider: async (signal) => { signal.throwIfAborted(); return readCurrentToken(); }` instead of `token`. Your application defines `readCurrentToken` and returns the selected token or `undefined`. The UI's login cookie is not an SDK credential.

Browser calls require the same origin or operator-configured CORS at the API or its gateway. This package adds no server CORS, login or cookie session. Native Fetch owns TLS verification and its header transport limits. Configure private trust through your Node process or browser trust store. There is no insecure TLS option. The SDK omits cookies, refuses redirects and supplies no proxy configuration. Node deployments must not replace global Fetch with an ambient-proxy adapter. An optional second factory argument injects a trusted Fetch implementation for tests; the caller owns that implementation's TLS, proxy and retry behavior.

For direct cross-origin access, configure the backend's exact `http.corsOrigins` or `CATALOG_HTTP_CORS_ORIGINS`; see [HTTP configuration](../../documentation/src/content/docs/reference/configuration.mdx#configuration-groups). The Catalog allows its supported methods and command headers, and exposes response metadata such as `ETag`, `Location`, `X-Request-ID`, `Retry-After`, `Idempotency-Replayed` and `Content-Digest`. If a gateway owns CORS, preserve those allowed request and exposed response headers. A header can reach the browser but remain hidden from JavaScript without this policy. CORS does not grant Catalog permissions.

Keep browser access tokens in application-controlled memory. Do not embed a service credential or OAuth client secret in a browser bundle. A browser login requires an application-owned authentication flow; the SDK does not borrow the Catalog UI session.

## Limits and cancellation

| Setting | Default | Scope |
| --- | --- | --- |
| `timeoutMs` | 30000 | Token provider, HTTP request and body acquisition. |
| `requestBytes` | 16 MiB | Encoded request bytes before credentials or transmission. |
| `responseBytes` | 8 MiB | Streamed response bytes before generated parsing. |
| `headerBytes` | 32 KiB | Parsed header check; native Fetch retains its own transport limit. |

Byte budgets accept positive integers up to 1 GiB. A call can set `responseBytes` for a larger exact read. A call's `timeoutMs` can shorten the configured deadline. Pass `signal` to cancel. Calls retain independent credentials, timers and limits. No method automatically retries, follows Location, polls Jobs or fetches another page.

Use an application-owned controller when a view closes or a user cancels a read:

<!-- sdk-example: cancellable-read -->

```ts
import type { CatalogClient } from "@open-agentic-catalog/client";

export async function readCapabilities(client: CatalogClient, signal: AbortSignal) {
  const result = await client.api.GET("/api/v1/capabilities", {
    signal,
    timeoutMs: 5_000,
  });
  if (result.response.status !== 200 || !result.data) {
    throw new Error(`Capabilities returned HTTP ${result.response.status}`);
  }
  return result.data;
}
```

Call `readCapabilities(client, controller.signal)` with `const controller = new AbortController()`. Call `controller.abort()` to cancel. Handle `SDKError` codes `cancelled` and `deadline_exceeded` separately from HTTP refusals. Cancellation of a command does not establish that its server-side effects stopped.

JavaScript cannot interrupt synchronous serializers, parsers or token providers. Elapsed-time checks refuse late transmission or body completion when execution resumes. Requests are buffered. Response bodies are fully bounded before parsing, including `parseAs: "stream"`; this option returns an in-memory stream.

Browser decompression is controlled by Fetch. An exposed unexpected Content-Encoding is refused, and streamed decoded bytes remain bounded when a browser hides that header. Browser security can hide headers or reject manual redirects; such exchanges produce a safe transport failure.

## Responses and raw evidence

Check `response.status` before using `data` or `error`. Control errors use RFC 9457 Problem fields; ARD errors use `errorCode` and `message`. These remain distinct typed alternatives. Read ETag, Location, X-Request-ID, Retry-After and Idempotency-Replayed from `response.headers`. They do not trigger automatic actions. Bodyless 204 and 304 need no JSON. `SDKError` contains a fixed `code` and available status/request ID; it never includes dependency causes or private response bodies.

Valid JSON error responses return `error` and `response`; they do not throw `SDKError`. A 429 or 503 response does not trigger a retry, even with Retry-After. Network failures, exceeded limits and malformed JSON throw `SDKError`. `parseAs` selects successful response decoding; error responses still require JSON. Do not log private fields from a returned error body.

For example, distinguish a transport or decoding failure from an HTTP refusal:

```ts
import { SDKError } from "@open-agentic-catalog/client";

try {
  const result = await client.api.GET("/api/v1/capabilities");
  if (result.response.status !== 200 || !result.data) {
    throw new Error(`Capabilities returned HTTP ${result.response.status}`);
  }
} catch (failure) {
  if (!(failure instanceof SDKError)) throw failure;
  console.error(`SDK failure: ${failure.code}; HTTP status: ${failure.status}`);
}
```

`failure.status` and `failure.requestID` are `undefined` when unavailable. A valid HTTP 401 or 403 follows the status check. Your application must decide whether to stop or reconcile an uncertain command; this example adds no retry.

Malformed JSON after a write does not prove that the write failed. Preserve the original command key, body and revision binding for recovery. Use `raw()` from the outset when you need exact response bytes. Do not repeat a mutation just to obtain its response bytes.

Typed JSON uses JavaScript numbers and is not lossless evidence or full JSON Schema validation. Conditional schemas cannot enforce every rule at compile time. Submit original bytes for arbitrary native documents and extensions. This function sends an existing complete CreateVersion envelope:

<!-- sdk-example: create-version-bytes -->

```ts
import type { CatalogClient } from "@open-agentic-catalog/client";

export async function createVersionFromBytes(
  client: CatalogClient,
  resourceID: string,
  originalJSONBytes: Uint8Array,
  commandKey: string,
) {
  const result = await client.raw(
    `/api/v1/resources/${encodeURIComponent(resourceID)}/versions`,
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Idempotency-Key": commandKey,
      },
      body: originalJSONBytes,
    },
  );
  if (result.status !== 201) {
    throw new Error(`Version creation returned HTTP ${result.status}`);
  }
  return result;
}
```

`originalJSONBytes` must contain the complete CreateVersion envelope, not a bare native card. `raw()` returns status, headers, exact `bytes` and strict UTF-8 `text()`. `rawBytes(response)` copies bytes from a typed response. An oversized response fails entirely; it is never truncated. Received inline JSON is not original artifact bytes. Preserve the [document digest scopes](../../documentation/src/content/docs/guides/integrating.mdx#export-native-documents).

Read a readable VersionRevision without parsing or reserializing its response:

<!-- sdk-example: read-revision-bytes -->

```ts
import type { CatalogClient } from "@open-agentic-catalog/client";

export async function readRevisionBytes(client: CatalogClient, revisionID: string) {
  const result = await client.raw(
    `/api/v1/version-revisions/${encodeURIComponent(revisionID)}`,
    { responseBytes: 32 * 1024 ** 2 },
  );
  if (result.status !== 200) {
    throw new Error(`Revision read returned HTTP ${result.status}`);
  }
  return result.bytes;
}
```

The 32 MiB override applies only to this read. Keep the default when it is sufficient. Node callers can load the original request bytes with `readFile` from `node:fs/promises`. Browser callers can use `new Uint8Array(await file.arrayBuffer())`. Do not parse those bytes into a JavaScript object before a lossless submission.

## Pagination and commands

Each Search call returns one page, including partial results, diagnostics and an opaque continuation. Submit the cursor explicitly with the same query, filters, ordering and credentials. Do not reuse it after changing these inputs.

### Read a bounded sequence of pages

This helper reads up to three pages of local results. It retains complete page metadata and returns any remaining cursor. It does not claim to read the whole Catalog:

<!-- sdk-example: search-local-pages -->

```ts
import type { CatalogClient, components } from "@open-agentic-catalog/client";

export async function searchLocalPages(
  client: CatalogClient,
  signal: AbortSignal,
  cursor?: string,
) {
  const pages: components["schemas"]["CatalogSearchResponse"][] = [];
  let nextCursor = cursor;
  for (let count = 0; count < 3; count++) {
    signal.throwIfAborted();
    const result = await client.api.POST("/api/v1/catalog/search", {
      signal,
      body: {
        text: "weather",
        origins: ["local"],
        limit: 10,
        ...(nextCursor ? { cursor: nextCursor } : {}),
      },
    });
    if (result.response.status !== 200 || !result.data) {
      throw new Error(`Search returned HTTP ${result.response.status}`);
    }
    pages.push(result.data);
    nextCursor = result.data.nextCursor || undefined;
    if (!nextCursor) break;
  }
  return { pages, nextCursor };
}
```

Inspect each page's `partial` and `diagnostics` before presenting its results. An empty item list does not end traversal when a cursor remains. To continue this same traversal, call the helper with the returned cursor and the same identity. A cursor rejection requires a fresh search; do not mix restarted results into the previous sequence. This example selects local results; explicit indexed or live selection also requires federation discovery authority.

### Read the selected local release

Search result IDs start with `local:`, `indexed:` or `live:`. They are not Catalog record UUIDs. Use the local release UUID for governed detail:

<!-- sdk-example: read-local-result -->

```ts
import type { CatalogClient, components } from "@open-agentic-catalog/client";

export async function readLocalResult(
  client: CatalogClient,
  item: components["schemas"]["CatalogSearchResult"],
) {
  if (item.type !== "local" || !item.local) {
    throw new Error("Select a local result for a governed release read");
  }
  const result = await client.api.GET(
    "/api/v1/catalog/resources/{catalogRecordId}",
    { params: { path: { catalogRecordId: item.local.release.id } } },
  );
  if (result.response.status !== 200 || !result.data) {
    throw new Error(`Catalog detail returned HTTP ${result.response.status}`);
  }
  return result.data;
}
```

| Result type | Detail boundary |
| --- | --- |
| `local` | Read the governed release through `item.local.release.id`, as above. |
| `indexed` | Read `/api/v1/catalog/indexed-record` with `identifier`, `versionPresent` and `version` when present. This returns current external metadata, not local governance history. |
| `live` | Use the returned `item.live.entry` as transient metadata. It has no local release UUID. |

The server rechecks access on every detail read. Search visibility does not authorize execution or installation of the selected resource. See [client handoffs](../../documentation/src/content/docs/guides/integrating.mdx#choose-a-discovery-interface) for discovery boundaries.

### Send a deliberate command

Use the integration guide's complete [Search function](../../documentation/src/content/docs/guides/integrating.mdx#search-one-page-with-javascript-or-typescript), [draft creation function](../../documentation/src/content/docs/guides/integrating.mdx#create-a-resource-with-javascript-or-typescript) and [exact-byte read](../../documentation/src/content/docs/guides/integrating.mdx#read-exact-bytes-with-javascript-or-typescript). Typed calls use the HTTP method and exact OpenAPI path, including path placeholders:

```ts
const page = await client.api.POST("/api/v1/catalog/search", {
  body: { text: "weather", origins: ["local"], limit: 10 },
});
const draft = await client.api.POST("/api/v1/resources", {
  params: { header: { "Idempotency-Key": commandKey } },
  body: { namespaceId, kind: "agent", canonicalIdentifier: "urn:air:catalog.example.com:demo:agent" },
});
```

Check each result status. Creating a resource does not publish it. Use the required permissions and OAuth scopes. Supply the original `Idempotency-Key`, body and `If-Match` for safe command replay. A lost response leaves the outcome uncertain. Completed commands support replay for 24 hours from reservation; pending reservations have different recovery rules. See [API conventions](../../documentation/src/content/docs/reference/api-conventions.md#request-rules).

For a namespace update, supply its UUID, current ETag and a retained command key. Choose a different display name; an unchanged patch returns HTTP 400:

<!-- sdk-example: update-namespace -->

```ts
import type { CatalogClient } from "@open-agentic-catalog/client";

export async function updateNamespace(
  client: CatalogClient,
  namespaceId: string,
  currentETag: string,
  commandKey: string,
) {
  const updated = await client.api.PATCH("/api/v1/namespaces/{namespaceId}", {
    params: {
      path: { namespaceId },
      header: { "Idempotency-Key": commandKey, "If-Match": currentETag },
    },
    body: { displayName: "Examples", reason: "Update namespace name" },
  });
  if (updated.response.status !== 200 || !updated.data) {
    throw new Error(`Namespace update returned HTTP ${updated.response.status}`);
  }
  return updated;
}
```

Retain `updated.data` and `updated.response.headers.get("ETag")` from the returned result. A 412 response requires a new read and an explicit decision. Do not replace its validator and retry automatically. For uncertain delivery, retain the original key, ETag and body together. Replaying with changed bindings is a different command, not recovery.

## Find an operation or model

Select a method and exact path from the [HTTP API](../../api/openapi/control-v1.yaml). Generated names are available through the package's type exports:

<!-- sdk-example: operation-types -->

```ts
import type { components, operations, paths } from "@open-agentic-catalog/client";

export type SearchInput = components["schemas"]["CatalogSearch"];
export type SearchResult = components["schemas"]["CatalogSearchResult"];
export type SearchOperation = operations["searchCatalog"];
export type SearchRoute = paths["/api/v1/catalog/search"]["post"];
```

These declarations check request and response shapes at compile time. They do not validate incoming JSON or establish permission. The API remains the authority for conditional fields, authorization and lifecycle transitions. Functions above use TypeScript annotations; remove type-only imports and annotations for plain JavaScript.

Per-call fetch, base URL, serializer, middleware and Request overrides are refused at runtime. Configure the factory once. Do not use the upstream unmanaged `openapi-fetch` client for these guarantees. SDK installation needs only runtime packages; generation, tests and build tools remain development dependencies. See [JavaScript/TypeScript SDK verification](../../documentation/src/content/docs/contributing/testing.md#javascripttypescript-sdk-verification). This package uses root VERSION as its release base. The local package name does not establish npm ownership. CommonJS is not provided. Return to the [SDK overview](../README.md) to compare languages.
