# Functional HTTP contract driver

This test-only Go command validates exchanges against the embedded Control OpenAPI contract. `main.go` handles bounded loopback requests, response matching and safe operation observations; `request_envelopes.go` supplies representative request-envelope probes. The Python functional journeys invoke the same binary for response validation and operation coverage.

Keep raw bodies, tokens and validator internals out of diagnostics. The driver may validate negative responses, but the calling journey still owns the expected status and business assertions. This is not a shipped API client.

Run `go test ./test/functional/contracts` for isolated parser and contract tests. Use the [functional harness](../README.md) for actual Compose exchanges; ordinary unit tests here do not require a running Catalog or database.
