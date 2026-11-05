# Immutable publication snapshots

Publication constructs audience-scoped discovery snapshots from eligible Catalog releases. Candidate construction and public activation are separate transitions; readers serve retained active documents rather than reconstructing public state from mutable drafts.

| File | Responsibility |
| --- | --- |
| [types.go](types.go) | Discovery entries, snapshot state, audience and query contracts. |
| [manager.go](manager.go) | Candidate generation, validation, activation, rollback and discovery reads. |
| [limits.go](limits.go) | Construction budgets, document integrity and bounded artifact references. |
| [store.go](store.go) | Immutable candidate persistence and atomic active-pointer transitions. |
| [audit.go](audit.go) | Candidate and lifecycle evidence with public notification references. |

Construction, activation and rollback use the same bounded eligibility projection. Snapshot row identity, host, audience, generation, timestamps and retained documents must agree. The worker can generate candidates; API authority owns activation and withdrawal.

Snapshot summary selection accepts an optional exact `candidate`, `active` or `retired` state. Memory and PostgreSQL stores apply it before the descending generation/ID limit, including when newer candidates hide an active generation from the first unfiltered page. Omission preserves the all-state inventory. HTTP continuations bind the state and host to current authorization; explicit active lookup uses `state=active&limit=1`. Neither a summary read nor a content ETag establishes later activity. Exact full reads and lifecycle authorization remain unchanged.

Run `go test ./internal/publication -shuffle=on -count=1`. Pinned discovery contracts are documented in [profiles](../../schemas/PROFILES.md).

Structured metadata filters and Explore facets share scalar projection. Valid JSON null contributes no comparable value or bucket; a literal `<nil>` string remains comparable. Local snapshot filtering requires a matching value, while live Search checks only actually disclosed optional fields and still admits omitted projection terms. [Disclosed-filter units](disclosed_filter_test.go) use the pinned EntryFields schema and exact-number decoding to distinguish null, strings, boolean values, precise numbers and omission. [Public projection units](../platform/httpserver/public_queries_test.go) qualify static snapshot Search/Explore; [live functional contracts](../platform/httpserver/ard_live_validation_functional_test.go) compose publication and federation with an injected peer transport, preserving healthy results and private diagnostics.

[Hybrid Explore functional coverage](semantic_explore_functional_test.go) connects the real embedding HTTP client to an owned loopback provider and reuses the immutable snapshot and deterministic similarity fixture. It checks full-set facets against explicit Search identities across pagination, filter/cutoff enforcement, provider failure and recovery, and absence of mutable-record reads. This qualifies HTTP transport and public projection; PostgreSQL vectors and model relevance have separate tests.

Run `go test -tags=functional ./internal/publication -run '^TestPublicHybrid' -race -shuffle=on -count=3` for these exchanges and the existing isolated hybrid assertions. `make test-functional-local` and `make verify` include the functional selection; unit targets exclude the listener.

Reviewed Listing license text uses the existing compact `metadata.license` projection; category labels use the Dublin Core subject IRI. [Listing projection units](listing_projection_test.go) check exact Unicode limits, filter/facet parity, continuation binding and immutable active reads across a projection change. Regenerate candidates that no longer match current eligibility; never rewrite retained snapshots or native documents.

[Native field projection](native_fields.go) supports nine public A2A/MCP Server Card paths, including A2A card input/output defaults and native skill tags. Query-local values are decoded once per inline document and reused for filters, hybrid preselection and facets. Media types and expected scalar shapes gate interpretation; URL-only records gain no facts. [Native units](native_fields_test.go) cover exact values, sparse/empty/null distinctions, array deduplication, independent interfaces/skills, card defaults versus skill overrides, cancellation, encoded bounds and retained immutability. The [standards reference](../../documentation/src/content/docs/reference/standards.mdx#native-card-filters-and-facets) owns client examples and limits.

Core automatic Search uses [bounded live composition](live_search_page.go) with exact whole-snapshot local precedence. Local offsets count emitted local results only. A live overflow token works even when local results are exhausted. Its expiry remains fixed; exhausted live windows return ordinary local continuation. Hybrid automatic Search preserves exact durable order before a distinct live tail, including a token after the final durable page.

`SearchResult.TrustManifest` preserves unverified declarations from admitted live peers as raw JSON. It is not part of the local publication `Entry`, and peers cannot attach their claims to a winning local result. The HTTP merger emits lowercase `trustManifest` for either supported input spelling without combining duplicate claims. [Projection units](../platform/httpserver/public_live_projection_test.go) and the existing live functional contracts check preservation, disclosure and continuation.

Ordinary discovery does not verify signatures or request receipts. Separately enabled [ANS registration assessments](../../documentation/src/content/docs/operations/federation.mdx#request-an-ans-registration-assessment) verify selected registration evidence on explicit request. They do not verify Agent Card signatures or establish broad trust, software safety or permission to execute a resource.
