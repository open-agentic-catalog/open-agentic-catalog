# Wellknown indexed-import assessment

## Decision

Complete indexed import from [Wellknown's ARD registry](https://wellknown.network/docs/ard) is not qualified for the reviewed inventory. One bounded native List read reported 41,122 members. That requires at least 412 pages at Catalog's page size of 100. Catalog permits at most 32 List pages per synchronization, before other retrievals consume its aggregate budget. The reported inventory also exceeds the independent 10,000-entry ceiling.

This completes the feasibility assessment with an over-budget outcome. It does not establish a Catalog defect, complete import or native artifact compatibility. Catalog maintainers own the decision and its revisit conditions. The selected discovery profile, synchronization bounds and source authority remain unchanged. No new importer, dependency or hosted test target is selected.

## Reviewed interface and permission

The review used Catalog commit `37612e9902abd31ccfdde67235b996cf4666c6a3` and the public service on November 13, 2026. Wellknown advertises ARD v0.91 with Registry base `https://wellknown.network/ard`. Its [documentation](https://wellknown.network/docs/ard) describes Search, Explore and newest-first List. These claims do not establish compatibility of every operation.

Direct downloads of [OpenAPI](https://wellknown.network/openapi.json) and [machine-readable terms](https://wellknown.network/api/v1/terms) returned HTTP 200. The reviewed OpenAPI 3.1.0 document describes 23 vendor API paths. It links the ARD surfaces but does not define ARD List operations or their success schema. The vendor API is not an alternative import path selected by this assessment.

The [terms](https://wellknown.network/terms), version `2026-09-23`, permit attributed caching, re-serving and paged mirroring within service limits. Observed and derived values require attribution to Wellknown. Declared values remain third-party content. These permissions do not license referenced packages or authorize resource execution. Retained captures identify Wellknown, the source URL and the reviewed terms version. Captured records are not redistributed as maintained test fixtures.

## Native observation

The assessment made one native inventory request. It did not follow the returned token.

| Item | Observation |
| --- | --- |
| Request | `GET https://wellknown.network/ard/agents?pageSize=1` with `Accept: application/json`. |
| Request bounds | No credentials, redirects or retries; 15-second deadline and 1 MiB transfer cap. |
| Response | HTTP 200, `application/json`, 980 bytes. |
| Envelope | One `items` member, integer `total: 41122` and nonempty `pageToken: "1"`. |
| Advertised rate fields | `RateLimit-Limit: 30`, `RateLimit-Remaining: 29`, `RateLimit-Reset: 60`, with matching `X-RateLimit-*` fields. |
| Sample entry | Declares `application/mcp-server-card+json`, points to a Wellknown `record.json` URL and carries `metadata.wellknown`. |

The total is a provider-reported value from one response, not an independently enumerated count. It is sufficient to stop this assessment's complete-import attempt under current bounds. The token's value is opaque; it does not select a Catalog offset convention. No terminal-page behavior or complete native schema validation is established.

The rate fields are consistent with the [API guide's](https://wellknown.network/docs/api) anonymous 30-per-minute policy. They do not establish shared-route buckets, keyed quotas or admission limits for every ARD operation. The known capacity mismatch requires no additional requests or account. No payment, credential acquisition or rate-limit bypass occurred.

The [record format](https://wellknown.network/docs/records) separates publisher declarations, derived values, provider observations and provenance. The sampled URL follows that wrapper design. Its bytes were not fetched. A recognized entry media type does not prove that the referenced document is a native MCP Server Card, that its endpoint works or that its claims are verified.

## Catalog limits and completion semantics

The owning sources are [synchronization retrieval](../../internal/federation/sync_retrieval.go) and the [federation manager](../../internal/federation/manager.go).

| Bound | Current Catalog behavior |
| --- | --- |
| List page size | Requests 100 entries per page. |
| List page count | At most 32 pages. |
| Aggregate retrievals | At most 32 per synchronization, including other retrievals and conditional fallbacks. |
| Retrieval deadline | 30 seconds per synchronization. |
| Decoded data | At most 64 MiB per synchronization. |
| Enumerated members | Independent 10,000-entry ceiling. |

The page/request limits allow at most 3,200 List members before other work reduces that capacity. Scope filters apply after enumeration. Narrowing kinds or identifiers does not request a smaller upstream List. The three-entry self-manifest describes Wellknown's own services; importing it would not import the registry inventory.

Source admission is separate from a worker synchronization. Normal inspection can send a fixed read-only Search probe and a one-entry List probe. Any later hosted campaign must count those requests in its overall budget. They do not consume a different worker run's 32-retrieval allowance. This assessment did not run Catalog admission.

List enumeration checks totals, duplicate members, repeated tokens, continuation after a declared total and work limits. A successful terminal traversal does not prove that every page represents one frozen inventory. The selected ARD profile provides no such guarantee. Newest-first membership can change between pages.

A complete inspected Registry List can make missing previous members stale in the current view while preserving history. An incomplete run cannot replace the last complete view. Missing membership is not proof of publisher withdrawal. No prefix of the hosted inventory is accepted as a complete snapshot by this assessment.

## Qualification outcomes

| Boundary | Outcome |
| --- | --- |
| Native initial List read | HTTP response and initial envelope observed within the selected bounds. |
| Complete indexed import | Not qualified: the reported inventory exceeds existing limits. No Source, Job, snapshot or observation was created. |
| Catalog admission and Search/Explore exchange | Not run. List availability alone does not establish these interfaces. |
| Terminal tokens, complete entry compatibility and hosted paging consistency | Not assessed beyond the initial page. No continuation was followed. |
| Referenced artifact validity, invocation and trust | Unassessed. No listed artifact or runtime was contacted. |
| Hosted lifecycle and reverse replication | Not run. No submission, crawl trigger or Catalog publication to Wellknown occurred. |
| Local failure preservation | Existing federation tests cover paging/accounting refusals and retention of the last complete view. These local controls are not hosted-import evidence. |

Ordinary indexed ingestion retains source-owned metadata and provenance. It does not fetch every entry's resource artifact, adopt a local Resource or turn provider observations into Catalog approval. Per-entry compatibility, enumeration completeness and artifact validity remain separate results.

## Verification and revisit conditions

Use the existing `make test-unit` selection for federation decoder, budget, consistency and last-complete-view controls. Run `make check` for source/documentation contracts and `make docs-site-check` for the public compatibility reference. There is no new executable or hosted target to install. These checks do not reproduce a complete Wellknown import.

The decisive size evidence already reaches the assessment's stop condition. No disposable Catalog, database, cluster or browser fixture was required. No hosted data was imported and no retained Catalog state was changed. Additional traversal would not qualify the reported inventory under the reviewed bounds.

Revisit complete-import qualification when a useful native inventory fits the unchanged limits and its permitted request policy is clear. Then use normal administrator inspection and connection in an owned API/worker/PostgreSQL fixture, with indexed/manual retrieval and live/referrals off. Require nonempty independent API and database membership reads, exact source/Job/snapshot/occurrence provenance, native completion checks and owned cleanup before claiming success.

Any larger-scale synchronization requirement needs its own design and verification. It is not selected merely to make this peer pass. A missing prerequisite, a provider limitation and a Catalog defect require different dispositions. None justifies a truncated replica, payload converter or unverified compatibility claim.
