# Generated-client consumer tests

This separate Go module contains handwritten consumer assertions for the generated Control API client. Tests cover request encoding, response decoding, authentication, raw evidence and representative workflows using owned in-process fixtures. Hybrid-search codec checks validate the maintained disabled/enabled/indexing/outage examples and preserve optional partial-index fields, including explicit false, through generated round trips without calling a model.

The generated client is created in an invocation-owned working directory, not maintained here. Use `make client-go-tools-install` followed by `make client-go-check` from the repository root; direct root `go test ./...` does not select this nested module. See the [compatibility harness guide](../README.md) for tool locks and generation boundaries.
