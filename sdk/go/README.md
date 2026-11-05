# Go SDK

This source module provides every operation in the [Catalog OpenAPI contract](../../api/openapi/control-v1.yaml), including anonymous ARD discovery. It requires Go 1.27.3 or later. The module path is `github.com/open-agentic-catalog/open-agentic-catalog/sdk/go`; its Go package name is `catalogclient`.

## Install the SDK

For a complete consumer application, use the [Go webhook publication report](../../examples/publication-report/README.md). It accepts signed notifications into its own PostgreSQL inbox and refreshes a local report through current SDK reads. Its database and dependencies remain separate from the SDK and Catalog.

No remote module release is available yet. In your own module, add a local replacement for this checkout:

```sh
go mod edit -require=github.com/open-agentic-catalog/open-agentic-catalog/sdk/go@v0.1.0
go mod edit -replace=github.com/open-agentic-catalog/open-agentic-catalog/sdk/go=/absolute/path/to/open-agentic-catalog/sdk/go
```

Add the SDK import from the example below before running `go mod tidy`. Otherwise, Go removes the unused requirement. Run these commands in your consumer module, not in the Catalog repository root. Normal module resolution can download runtime dependencies.

Consumer builds need the declared runtime modules, not the generator, application module or test harness. The SDK uses the [Apache License 2.0](../../LICENSE). Its release base and OpenAPI `info.version` follow root `VERSION`, currently `0.1.0`. The `/api/v1` route prefix identifies the API family independently of the application release version. See [application versioning](../../documentation/src/content/docs/contributing/build-and-release.md#application-version-and-release-checks). Future Go submodule tags use `sdk/go/v<version>`; these commands create no tags.

## Make your first request

Save this program as `main.go` in your consumer module. Supply `CATALOG_SDK_ORIGIN` and `CATALOG_SDK_TOKEN` through the process environment. The token needs an active Catalog membership.

```go
package main

import (
    "context"
    "fmt"
    "net/http"
    "os"

    catalogclient "github.com/open-agentic-catalog/open-agentic-catalog/sdk/go"
)

func main() {
    if err := readCapabilities(context.Background()); err != nil {
        fmt.Fprintln(os.Stderr, "Catalog capabilities request failed")
        os.Exit(1)
    }
}

func readCapabilities(ctx context.Context) error {
    client, err := catalogclient.New(catalogclient.Config{
        Origin: os.Getenv("CATALOG_SDK_ORIGIN"),
        Token: os.Getenv("CATALOG_SDK_TOKEN"),
        // AllowLoopbackHTTP: true, // Only for an explicitly selected local API.
    })
    if err != nil {
        return err
    }
    defer client.Close()

    response, err := client.GetCapabilitiesWithResponse(ctx)
    if err != nil {
        return err
    }
    if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
        return fmt.Errorf("capabilities returned HTTP %d", response.StatusCode())
    }
    fmt.Println("Catalog API connection succeeded")
    return nil
}
```

After you add this file, run these commands from your consumer module:

```sh
go mod tidy
go run .
```

The program reads capability metadata without changing Catalog data. Its error output is deliberately brief. The error-handling section below shows how to classify a failure safely.

## Create a client and read discovery

Use `New` for the maintained transport. Omit both token settings for anonymous discovery. Authenticated discovery still follows the configured host audience and current backend permissions. This helper uses an already configured client:

```go
func readManifest(ctx context.Context, client *catalogclient.SDK) error {
    response, err := client.GetARDManifestWithResponse(ctx, nil)
    if err != nil {
        return err
    }
    if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
        return fmt.Errorf("manifest returned HTTP %d", response.StatusCode())
    }
    // response.Body retains the bounded HTTP bytes; JSON200 is the typed view.
    return nil
}
```

Supply an HTTPS origin without a path, query, fragment or embedded credentials. Empty `?` and `#` suffixes are also refused. For a local development server, explicitly set `AllowLoopbackHTTP: true` and use a literal loopback IP, such as `http://127.0.0.1:8080`. Plain HTTP to a hostname is refused.

For authenticated calls, set `Token` or `TokenProvider`, never both. The provider receives the request context and runs once for each request. It can read your current credential; the SDK does not acquire or refresh tokens. Honor cancellation in providers and custom request readers. Synchronize shared provider state if you make concurrent calls. An empty provider token sends an anonymous request.

The client ignores proxy, cookie and credential environment settings. It follows no redirects and sends configured credentials only to its selected origin. Request editors cannot change the origin or Host header. The client removes credential, cookie and compression headers before it applies its settings, including differently cased keys.

For private TLS, set `RootCAs` to an explicit `*x509.CertPool`. The SDK clones that pool and retains certificate and hostname verification. A nil pool uses the Go system roots, including the platform's trust environment settings. There is no insecure TLS option.

The generated `NewClient`, `NewClientWithResponses`, `WithHTTPClient` and mutable generated client fields are low-level escape hatches. They can bypass the maintained transport. Direct `Parse...Response` helpers also require a previously bounded response. Use `New` and its operation methods for the guarantees below.

## Limits and response ownership

| Setting | Default | Meaning |
| --- | --- | --- |
| `Timeout` | 30 seconds | Whole HTTP exchange and response acquisition, bounded further by the caller's context. |
| `RequestBytes` | 16 MiB | Buffered request body limit, before transmission. |
| `ResponseBytes` | 8 MiB | Response body limit, before generated JSON parsing. |
| `HeaderBytes` | 32 KiB | Response header limit enforced by the HTTP transport. |

Zero selects the default. Negative values fail configuration. Body and header budgets cannot exceed 1 GiB. These client budgets do not increase the server's configured limits. Request preparation, typed decoding and caller-supplied readers/providers are synchronous work; the timeout does not interrupt arbitrary caller code or impose a CPU deadline on JSON decoding.

Use `WithResponseLimit(ctx, bytes)` to select a larger finite response budget for one exact read. It accepts 1 byte through 1 GiB and leaves the client's default unchanged. An over-limit body fails the whole call; the SDK does not return a truncated page.

The SDK closes the network response body after bounded acquisition, including failure paths. Raw operation methods return an in-memory body that the caller must close. `WithResponse` methods close that body after parsing and retain its bytes in `Body` when decoding succeeds. Choose a raw method first if you need to inspect malformed response bytes. Call `Close` when the SDK is no longer needed to release idle connections. Existing calls remain owned by their contexts.

If you supply a file or custom request reader, close it in your code. Request construction or an editor can fail before the transport receives that reader.

The client requests identity encoding and refuses an unexpected compressed response. It does not buffer an unbounded decompressed body.

## Handle responses and errors

A nil Go error means the exchange and selected decoding succeeded. It does not mean the HTTP operation succeeded. Check `StatusCode()` and the documented response branch.

- Control failures use `ApplicationproblemJSONDefault`, with the service's RFC 9457 problem code.
- ARD failures use `JSONDefault`, with `errorCode` and `message`.
- Read `HTTPResponse.Header` for `X-Request-ID`, ETag, Location and Retry-After. A Location header does not trigger another request.
- Handle bodyless 204 and 304 responses by status. They do not require a JSON body.

Transport and codec failures return `*Error` with a fixed diagnostic code. When available, it also carries `StatusCode` and `RequestID`. Use `errors.Is` for `context.Canceled` and `context.DeadlineExceeded`. Default error formatting excludes URLs, tokens, private bodies and dependency causes. Response bodies and problem messages remain caller-owned private data; do not log them indiscriminately. The SDK emits no logs.

Use this helper for a Go error. It prints fixed SDK diagnostics without a dependency cause or response body:

```go
import (
    "errors"
    "fmt"
    "io"

    catalogclient "github.com/open-agentic-catalog/open-agentic-catalog/sdk/go"
)

func reportSDKFailure(output io.Writer, err error) {
    if err == nil {
        return
    }
    var failure *catalogclient.Error
    if errors.As(err, &failure) {
        fmt.Fprintf(output, "SDK failure: %s; HTTP status: %d\n", failure.Code, failure.StatusCode)
        return
    }
    fmt.Fprintln(output, "Catalog call failed")
}
```

Pass `os.Stderr` as the output. A zero status means no HTTP response status is available. A valid HTTP 401 or 403 can instead return a response with no Go error. When `ApplicationproblemJSONDefault` is non-nil, inspect its `Code`. Read `HTTPResponse.Header.Get("X-Request-ID")` for operator correlation. Check both paths; neither a transport failure nor malformed JSON proves that a write did not commit.

## Preserve native evidence

Generated types support ordinary control fields. Their arbitrary JSON maps can round integers above JavaScript's safe-integer range through Go's `float64` decoding. Generated union types also do not enforce every OpenAPI conditional rule. Typed decoding is not full JSON Schema validation or lossless evidence.

Use a generated `WithBody` method with the original JSON bytes for arbitrary native documents and extension maps. A raw response, or a typed response's `Body`, preserves the received HTTP bytes. For example:

```go
response, err := client.CreateResourceVersionWithBodyWithResponse(
    ctx, resourceID,
    &catalogclient.CreateResourceVersionParams{IdempotencyKey: commandKey},
    "application/json", bytes.NewReader(originalJSON),
)
```

Check `err`, require HTTP 201 and retain the returned revision identity. This path preserves large JSON numbers, unknown fields and explicit null/false/empty values during transport. The server still applies its validation and normalization rules. Inline JSON responses are not original artifact bytes; preserve the [export digest scopes](../../documentation/src/content/docs/guides/integrating.mdx#export-native-documents).

## Search and open a local result

This helper reads at most three pages of local published releases. Each page contains at most ten results. Pass `nil` for the initial cursor:

```go
import (
    "context"
    "fmt"
    "net/http"

    catalogclient "github.com/open-agentic-catalog/open-agentic-catalog/sdk/go"
)

func searchLocalPages(ctx context.Context, client *catalogclient.SDK, cursor *string) ([]catalogclient.CatalogSearchResponse, *string, error) {
    text, limit := "documentation", 10
    origins := []catalogclient.CatalogSearchOrigins{catalogclient.CatalogSearchOriginsLocal}
    query := catalogclient.CatalogSearch{Text: &text, Limit: &limit, Origins: &origins}
    pages := make([]catalogclient.CatalogSearchResponse, 0, 3)
    next := cursor
    for range 3 {
        query.Cursor = next
        response, err := client.SearchCatalogWithResponse(ctx, query)
        if err != nil {
            return pages, next, err
        }
        if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
            return pages, next, fmt.Errorf("Search returned HTTP %d", response.StatusCode())
        }
        page := *response.JSON200
        pages = append(pages, page)
        next = page.NextCursor
        if next == nil || *next == "" {
            return pages, nil, nil
        }
    }
    return pages, next, nil
}

func readLocalResult(ctx context.Context, client *catalogclient.SDK, item catalogclient.CatalogSearchResult) (*catalogclient.CatalogRecord, error) {
    if item.Type != catalogclient.CatalogSearchResultTypeLocal || item.Local == nil {
        return nil, fmt.Errorf("Select a local published result")
    }
    response, err := client.GetCatalogRecordWithResponse(ctx, item.Local.Release.Id)
    if err != nil {
        return nil, err
    }
    if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
        return nil, fmt.Errorf("Catalog record returned HTTP %d", response.StatusCode())
    }
    return response.JSON200, nil
}
```

Inspect `Partial` and `Diagnostics` on every returned page. A non-nil returned cursor means the three-page budget ended before traversal completed. Retain it for the next explicit call. An error can also return earlier pages and the cursor for the failed call. Do not treat either case as a complete inventory.

Keep the query, filters, page size and identity unchanged during continuation. A short or empty page can still have a cursor. If a cursor expires or the result generation changes, offer a fresh search instead of silently restarting.

Pass a local page item to `readLocalResult` for its complete released record. The required ID is `item.Local.Release.Id`, not the resource UUID or Search result ID. The function rejects indexed and live items. Their evidence and source provenance have separate [discovery contracts](../../documentation/src/content/docs/guides/integrating.mdx#interpret-results-before-handing-them-off); do not turn a returned URL into an automatic credentialed request.

Use a caller deadline when several requests form one interaction. For example, derive a context with `context.WithTimeout(parent, 10*time.Second)` and `defer cancel()`. It bounds the traversal as well as each SDK call; import the standard `time` package for that example.

## Page and write explicitly

Each call fetches one page. Pass the returned opaque cursor with the same query, filters and credentials. Inspect partial-result flags and diagnostics; an incomplete peer traversal is not a complete inventory. The client neither enumerates pages nor polls accepted Jobs automatically.

Use the integration guide's complete [Search function](../../documentation/src/content/docs/guides/integrating.mdx#search-one-page-with-go) and [draft creation function](../../documentation/src/content/docs/guides/integrating.mdx#create-a-resource-with-go). The Search example explicitly selects local releases. API operation IDs become Go method names, with `WithResponse` for typed results; for example, `searchCatalog` becomes `SearchCatalogWithResponse`.

For draft commands, supply the caller's `Idempotency-Key`. Updates also require the current ETag as `If-Match`:

```go
response, err := client.CreateNamespaceWithResponse(ctx,
    &catalogclient.CreateNamespaceParams{IdempotencyKey: commandKey},
    catalogclient.CreateNamespaceJSONRequestBody{
        Slug: "examples", DisplayName: "Examples",
    })
```

Require HTTP 201 and retain the returned ID and ETag. Explicit replay must reuse the exact body, key and preconditions. The SDK does not invent keys, rebase a conflict or retry a command after an uncertain outcome. Its transport prevents automatic mutation replay even on a reused connection. Safe reads can use Go's ordinary connection recovery; no application retry loop is added.

For a namespace update, supply its UUID and current ETag from the previous read. Retain a new command key for this update. Choose a different display name; an unchanged patch returns HTTP 400:

```go
response, err := client.UpdateNamespaceWithResponse(ctx, namespaceID,
    &catalogclient.UpdateNamespaceParams{
        IdempotencyKey: commandKey, IfMatch: currentETag,
    },
    catalogclient.UpdateNamespaceJSONRequestBody{
        DisplayName: &displayName, Reason: "Update namespace name",
    })
```

Check `err` and require HTTP 200 before using `JSON200`. Read the new ETag from `HTTPResponse.Header`. A 412 response requires a new read and an explicit decision; do not replace the validator and retry automatically.

Completed commands have a [24-hour replay window](../../documentation/src/content/docs/reference/api-conventions.md#request-rules) from reservation. A key is not safe for indefinite replay. Pending reservations have different recovery rules. Use the supported reconciliation procedure when the result remains uncertain.

## Find an operation

Go methods use exported OpenAPI operation IDs. Typed methods add `WithResponse`; raw JSON request alternatives add `WithBody`. The [OpenAPI contract](../../api/openapi/control-v1.yaml) owns permissions, fields and response branches.

| Task | Typed Go method |
| --- | --- |
| Read current access | `GetCurrentAccessWithResponse` |
| Search one page | `SearchCatalogWithResponse` |
| Read a released Catalog record | `GetCatalogRecordWithResponse` |
| Create a resource draft | `CreateResourceWithResponse` |
| Read a VersionRevision | `GetVersionRevisionWithResponse` |
| Read the ARD manifest | `GetARDManifestWithResponse` |

From a consumer module with the dependency installed, inspect a method's signature:

```sh
go doc github.com/open-agentic-catalog/open-agentic-catalog/sdk/go.SDK
go doc github.com/open-agentic-catalog/open-agentic-catalog/sdk/go.ClientWithResponses.SearchCatalogWithResponse
```

See [Go SDK verification](../../documentation/src/content/docs/contributing/testing.md#go-sdk-verification) for generation, unit/functional selection, independent consumers and dependency checks. Return to the [SDK overview](../README.md) to compare languages.
