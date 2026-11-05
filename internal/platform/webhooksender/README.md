# Webhook HTTP transport

[sender.go](sender.go) implements one webhook delivery attempt through the [hardened fetcher](../fetcher/README.md). It posts the supplied CloudEvent bytes, rejects redirects, and returns only the HTTP status and Retry-After value. The [webhook domain](../../webhook) owns signatures, delivery claims, retries and durable evidence.

Private route selection uses exact operator-configured tenant/origin pairs. API clients cannot select network profiles, credentials or trust roots. `NewWithPolicy` rejects HTTP test routing and ambiguous assignments; transport and policy failures return errors for domain classification.

Run `go test ./internal/platform/webhooksender -shuffle=on -count=1` for payload/status mapping, route selection and policy rejection with controlled fetchers. Add `-tags=functional` to include the real HTTPS receiver's signature, redirect, response-bound, tenant and private-trust checks. See the [operations overview](../../../documentation/src/content/docs/operations/overview.md) for the linked webhook procedures and deployment prerequisites.
