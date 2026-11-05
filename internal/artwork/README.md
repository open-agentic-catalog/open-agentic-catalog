# Listing artwork boundary

This domain package validates presentation descriptors and transient PNG/JPEG bytes. It has no HTTP client, origin policy, persistence, cache, image transformation or hosting authority. The runtime adapter supplies the existing hardened fetcher with exact operator-selected media origin/profile bindings and empty credentials.

`Seal` accepts explicit authoring inputs, requires author rights declarations, fetches bounded bytes and returns immutable SHA-256/type/size/dimension facts. `Deliver` rechecks bytes against those facts; mutable native icons instead remain current display hints. Every rights assessment remains `not_assessed`.

`DecodeInput` and `DecodeDescriptor` enforce closed, duplicate-safe JSON. `Extract` recognizes the canonical absolute IRI as an expanded JSON-LD `@json` literal; `ExtractAI` recognizes it in the AI Catalog extension map. Unknown future versions are omitted. Invalid optional artwork must be omitted by the caller without rejecting otherwise valid standards evidence.

Core tests are isolated units using generated raster bytes and a narrow fake fetcher. Schema/JSON-LD tests use the real pinned normalization and checked-in extension schema. Runtime and integration functional tests separately qualify owned TLS, tenant/private trust, credential isolation and independent publisher/consumer snapshot exchange. Run `go test ./internal/artwork`; bounded fuzz targets are `FuzzDecodeArtworkInput` and `FuzzExtractArtworkJSONLiteral`.
