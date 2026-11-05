# Control OpenAPI

[control-v1.yaml](control-v1.yaml) is the OpenAPI 3.1 source for the API listener, including authenticated administration, host-bound discovery, health and contract delivery. The Go runtime embeds these exact bytes; the manual and generated-client checks use the same file. Do not maintain another generated contract copy.

Update operations alongside routes, authorization, request/response types and positive/negative contract tests. The worker's separate operations listener is documented in [observability](../../documentation/src/content/docs/operations/observability.mdx), not this product contract.

Run `make api-check` and `make api-contract-check`. After contract changes, also run `make client-go-check`, `make ui-client-check` and `make docs-site-check` to check generated consumers and rendered API documentation. See [API validation](../README.md#validate-a-change) for explicit tool installation and baseline compatibility checks.
