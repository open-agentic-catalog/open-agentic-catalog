# Pinned standards validation

This package validates protocol documents using the immutable inputs embedded by [schemas](../../schemas/README.md). Runtime validation must not download a newer schema or silently select a different profile.

| File | Responsibility |
| --- | --- |
| [jsonschema.go](jsonschema.go) | Embedded JSON Schema compilation and ARD/MCP validation. |
| [jsonld.go](jsonld.go) | ARD JSON-LD expansion, compaction and query IRI resolution with bounded context retrieval. |
| [ard_subject.go](ard_subject.go) | Selected literal topic projection using the peer entry context without remote retrieval. |
| [ai_catalog.go](ai_catalog.go) | AI Catalog envelope, entry and producer-level semantic rules. |
| [plugin.go](plugin.go) | Agent Plugins schemas and the pinned name-pattern implementation. |

Schema validity is separate from publisher trust, local governance and invocation authorization. JSON-LD context fetching is a caller-supplied policy boundary. Preserve explicit format/profile evidence when interpreting discovery input; Agent Plugins is not an ARD profile.

[JSON-LD unit tests](jsonld_unit_test.go) check identity/trust normalization, exact number preservation, query-context rejection and failed-fetch budgeting using static documents and controlled fetch functions.

[Context-version regressions](jsonld_version_unit_test.go) accept numeric JSON-LD 1.1 declarations through inline, array, scoped and controlled remote contexts. They reject unsupported versions and wrong types, preserve caller-owned query contexts, and verify exact native numbers and JSON literals. The processor adapter converts context controls only; `UseNumber` remains enabled for opaque evidence.

[AI Catalog unit tests](ai_catalog_test.go) derive typed-field expectations from the pinned 1.0 prose and CDDL. Optional typed fields reject null when present; empty string arrays and opaque artifact/extension JSON remain valid. Consumer/producer version and host checks are tested independently. An opaque local identifier can be retained without trust metadata, but cannot establish the publisher-domain binding required for an entry Trust Manifest. Structural checks do not verify signatures or confer publisher trust.

Run `go test ./internal/standards -shuffle=on -count=1` and the profile gates described in [pinned profiles](../../schemas/PROFILES.md) when changing standards behavior.

[Topic and IRI units](ard_subject_test.go) cover equivalent full IRIs, dotted prefixes, literal path tails, sparse declarations and unsupported or protected contexts. Sparse Search result interpretation does not require complete-entry normalization.

[Present-field regressions](guardrails_test.go) keep complete-entry and sparse Search media-type/node-identity checks aligned without adding required fields to Search. Valid unfamiliar parameterized types, exact matching identifiers and opaque extension numbers remain intact. The shared EntryFields/query-context validator stays structural; no registration or identity URL is fetched. [Federation units](../federation/live_result_test.go) and [public HTTP cases](../platform/httpserver/ard_live_validation_functional_test.go) separately assert invalid-entry omission, sanitized partial signals, healthy local/peer survival and existing trust disclosure.
