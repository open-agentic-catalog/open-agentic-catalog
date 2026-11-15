# Controlled federation

Federation inspects administrator-selected sources, retains provenance and indexes complete source snapshots. External observations remain external until an authorized client explicitly adopts one through the Catalog lifecycle.

| File | Responsibility |
| --- | --- |
| [scope.go](scope.go) | Exact source exclusions, AIR normalization and adoption commit fencing. |
| [types.go](types.go) | Source policy, inspection, snapshot, observation and discovery contracts. |
| [manager.go](manager.go) | Inspection, onboarding, administration, synchronization and bounded indexed/live discovery. |
| [live_search.go](live_search.go) | Request-local peer paging, shared work limits, source rechecks and final result merging. |
| [sync_retrieval.go](sync_retrieval.go) | Retrieval descriptors, conditional requests, pagination and retained retrieval evidence. |
| [observation.go](observation.go) | Observation normalization, provenance binding and integrity validation. |
| [snapshot_comparison.go](snapshot_comparison.go) | Authorized comparison of exact complete occurrence memberships, with bounded compact pages and explicit duplicate keys. |
| [control_filters.go](control_filters.go) | Bounded optional license/topic projection for indexed repositories, live Control admission and restore comparison. |
| [store.go](store.go) | Persistence ports and the in-memory snapshot/search implementation. |
| [audit.go](audit.go) | Action-specific evidence and observation-change summaries. |
| [maintenance.go](maintenance.go) | Scheduled synchronization and freshness maintenance through durable Jobs. |

Observation pages, indexed source Search and group contributions are metadata reads: they preserve exact digests and `rawEntryAvailable` without returning retained raw/normalized buffers. Occurrence-history parent checks use the same metadata boundary. Exact observation reads, raw export, adoption and synchronization still load their retained evidence. Group previews keep the fixed 100-contribution cap and explicit truncation; occurrence summaries for selected contributions use one bounded store call, with exact counts and latest tuples from the same read snapshot. Missing selected summaries fail the group; conflicts are still evaluated beyond the displayed prefix.

Indexed repositories derive exact license strings and normalized literal topics from retained normalized entries. Missing or unusable fields remain unavailable independently; unfiltered observations remain eligible. The [repair runbook](../../documentation/src/content/docs/operations/search-projection-repair.mdx) defines bounds and upgrade handling. Control Search and browser filters apply across selected origins. Live admission checks exact license and normalized all-category matching before counts or page selection. It omits lossy ARD topic preselection and uses only locally resolvable contexts. Missing or unusable declarations produce bounded insufficient-evidence diagnostics; known nonmatches do not fail the peer. Public ARD forwarding remains unchanged.

A failed or partial sync cannot withdraw the last complete snapshot. Source endpoint/profile changes require the administrative inspection workflow. All remote content and JSON-LD contexts use policy-controlled fetching; source URLs, query disclosure, audiences, origins and fan-out are not client-selected topology.

Live native `data` must be a JSON object when present. Its object-shape check preserves numeric tokens without conversion. Public Search compares only the [selected native paths](../../documentation/src/content/docs/reference/standards.mdx#native-card-filters-and-facets); unrelated native values remain opaque.

Complete ARD ingestion and sparse live Search apply the same Trust Manifest member types and declared publisher alignment. Either supported spelling works alone; both together are invalid. Invalid live entries produce sanitized partial-result diagnostics without removing healthy results. Invalid indexed entries remain private compatibility evidence without a normalized search projection. Validation preserves raw bytes and framework extensions, makes no evidence/key requests and establishes no cryptographic trust. Existing observations are unchanged; synchronize an affected source to reassess current entries.

Both entry paths also validate present media-type syntax and exact `@id`/`identifier` agreement. Sparse Search still permits omitted name, type and delivery fields. Unfamiliar valid types and their parameters are accepted without registration checks or additional fetches. Invalid present values omit only that entry, preserving healthy entries from the same peer and local publication.

Public ARD Search emits valid manifests from eligible live peers as lowercase `trustManifest`. The Search-only raw field preserves unknown framework values and exact numbers; Control evidence retains the original entry and spelling. Existing tenant, host, audience and anonymous-disclosure rules still apply. Local identity precedence and peer deduplication never combine claims. The raw-entry replay fingerprint already fences trust changes, while a transient outage permits retry with the same cursor.

Live Search defaults to one request per peer. `WithLiveSearchPages` accepts one through five; runtime configuration supplies the operator's choice. Extra pages reuse the query, endpoint, deadline and one credential/policy fence. First requests to selected peers and approved referrals take priority over extra pages. Pages share an 8 MiB body allowance per peer. Stop on limits, token loops, failures or access changes; never forward continuation tokens through redirects. Keep candidates by source until final admission, then reuse existing ranking and duplicate rules. See [configuration](../../documentation/src/content/docs/reference/configuration.mdx#live-peer-pages) for operational limits and later-client-page behavior.

Isolated traversal tests use static fetch results to check exact nested membership, URL/redirect/content deduplication and per-entry retrieval provenance. Adoption tests preserve retained evidence, require an explicit local version for unversioned observations and bind local overrides to the selected occurrence without a manager-side fetch. Real transport and the Catalog adoption lifecycle have separate owners.

Run isolated tests with `go test ./internal/federation -shuffle=on -count=1`. Static fetch fixtures check paging, scheduling, source/credential fences, byte limits and shared deadlines. Add `-tags=functional` for owned HTTPS discovery, token-bearing redirect refusal, response/accumulated byte limits and nested synchronization through the production router and hardened fetcher. These tests retain the in-memory evidence store; real PostgreSQL behavior has its separate integration gate. See [pinned profiles](../../schemas/PROFILES.md) for protocol versions and [application context](../../CONTEXT.md#implemented-features-and-defaults) for current boundaries.

Automatic source admission receives the deployment Job creation limit through `NewMaintainer`; it retains existing bounded selection, source-revision deduplication and atomic admission. Source 429/503 responses preserve bounded seconds/date `Retry-After` hints through the runtime adapter. The Job worker owns minimum-plus-jitter scheduling, classification and exhaustion; fetching and live discovery add no retry loop. See the [Job retry policy](../../documentation/src/content/docs/reference/configuration.mdx#durable-job-retry-policy).

## Exclude source entries

`scope.excludedIdentifiers` omits up to 1,000 exact identifiers from one tenant's source, across all versions and kinds. Each identifier allows 512 Unicode code points. Valid AIR identifiers retain ASCII syntax and the separate 512-byte parser limit. They use canonical publisher casing; namespaces and names remain case-sensitive. Other identifiers match exactly. Duplicate normalized values, blank values, surrounding whitespace and NUL are rejected. Omitted and empty lists have the same meaning. A positive scope filter remains required; exclusions always take precedence.

Current policy applies before indexed selection, counts, grouping and pagination, and after each bounded live response. Both Catalog Search and observation Search use it. Normal policy omissions are healthy results, including an empty result. Administrative observation, raw and occurrence reads retain evidence. New adoption checks the source before preparation and fences the final commit against a concurrent source edit. Existing local adoptions are unchanged.

Exclusions remain local. They are not forwarded to peers or added to published ARD manifests. Each approved referral source applies its own policy. They do not prevent enumeration, nested retrieval or direct client calls to a referred peer. Clearing exclusions restores eligible retained current entries; absent entries need a complete synchronization or a new live query.

Core client continuation requests a fixed 50-candidate window and [replay evidence](live_replay.go). The proof includes successful empty peers, actual fetch policy/credential revisions, source configuration, ordered content and traversal disposition. Per-request receipt/trace values and opaque peer tokens are excluded. Unsafe traversal has no replay proof; healthy first results remain usable.

## Explicit registration assessments

Optional `trustVerificationPolicy` selects a startup-loaded ANS authority policy. Assessment commands bind exact current indexed evidence or one bounded direct live query. Private administrator results preserve native media/delivery variants and expose original-byte digest and source revision. Public Search retains its logical-resource deduplication and unchanged ARD contract.

The worker verifies owned leases and source/policy/credential generations before each credential-free evidence request. Result reads distinguish historical checks from current applicability. Assessment creates no source snapshot, adopted resource or public notification. See [assessment operations](../../documentation/src/content/docs/operations/federation.mdx#request-an-ans-registration-assessment).
