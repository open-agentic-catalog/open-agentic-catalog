# Catalog domain

This package owns local resource authoring, immutable revisions, review, release state and authenticated Catalog reads. Infrastructure implements its ports; domain code must not import database repositories or HTTP handlers.

| Files | Responsibility |
| --- | --- |
| [types.go](types.go), [repository.go](repository.go) | Canonical records, transition inputs, persistence and dependency contracts. |
| [service.go](service.go), [adoption.go](adoption.go) | Authorization, local lifecycle commands, validation and explicit adoption of external observations. |
| [history.go](history.go), [memory_history.go](memory_history.go) | Bounded revision and release history reads. |
| [unified_search.go](unified_search.go), [live_search_page.go](live_search_page.go), [live_cursor.go](live_cursor.go), [search_cursor.go](search_cursor.go), [search_profile.go](search_profile.go) | Local/federated result composition, bounded live continuation, signed tokens and deterministic lexical ranking. |
| [semantic.go](semantic.go), [semantic_index.go](semantic_index.go), [semantic_indexer.go](semantic_indexer.go), [semantic_search.go](semantic_search.go) | Bounded metadata projection, RRF60 ranking, resumable indexing, administrator commands and optional hybrid retrieval. PostgreSQL owns eligible populations and exact counts; inference runs outside transactions. |
| [memory_repository.go](memory_repository.go) | In-memory lifecycle and evidence implementation for development and tests. |
| [digest.go](digest.go), [external_retrieval.go](external_retrieval.go), [skill_package.go](skill_package.go), [plugin.go](plugin.go) | Immutable representation and validation-evidence integrity. |
| [audit_support.go](audit_support.go), [notification.go](notification.go) | Safe audit export projections and notification state. |
| [errors.go](errors.go), [urn.go](urn.go), [url.go](url.go), [transition_time.go](transition_time.go), [tracing.go](tracing.go) | Domain error, identifier, reference, time and private tracing contracts. |

Repository mutations bind tenant, expected revision and required audit/outbox evidence. Published evidence is immutable. Search indexes and cursors do not become record authority. URL syntax validation here does not authorize fetching; external retrieval must use the injected hardened fetch boundary.

Run `go test ./internal/catalog -shuffle=on -count=1`. PostgreSQL implementations and real database checks live under [platform/database](../platform/database/README.md); composed workflows live under [integration](../integration/README.md).

Isolated service tests check external-fetch evidence against the requested URL, observed bytes, current policy and caller budget, including byte ownership and safe failure classification. Publisher and release tests use memory state and narrow mutation counters to verify rejection before writes and valid replacement eligibility; they do not qualify real retrieval or PostgreSQL transactions.

Authenticated Search accepts `license` and `categories` across selected local, indexed and live origins. Explore covers local Listing declarations only. License uses a trimmed, case-sensitive exact declaration up to 256 Unicode characters. All selected categories match after trimming/lowercasing, with at most 16 terms of 80 characters. Missing values do not match. Omitted Search origins use the caller's authorized population. Namespace filters still require local origin. External filters use bounded license strings and literal Dublin Core subjects; missing evidence does not match. Memory and SQL selection apply filters before ranking, totals, facets and hydration. Cursor bindings include normalized filters. These Control fields do not expose public ARD native paths or assess license rights.

Indexed repository ports also support these filters over bounded retained ARD declarations. This persistence support does not change public admission. The [projection repair guide](../../documentation/src/content/docs/operations/search-projection-repair.mdx) describes unavailable evidence, normalization, upgrade requirements and current limits.

Core mixed-origin Search uses [flat live cursors](live_cursor.go) and [bounded composition](live_search_page.go). A fixed 50-candidate window is replayed while live results remain. Whole matching durable identity projections suppress remote duplicates outside the current page. Tokens preserve one initial 15-minute expiry and the 4,096-byte Search limit. Hybrid pages preserve exact durable order before a distinct live tail. Live-only paging makes no durable or embedding calls.
