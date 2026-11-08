# Packaged functional harness

Install the pinned [ARD conformance Python prerequisite](../../documentation/src/content/docs/contributing/testing.md#schema-validation-and-ard-conformance) in the selected Make `PYTHON` environment before packaged workflows. The shared prerequisite fails before the internal workflow starts; the default profile's manifest, publisher and registry calls use the guarded official CLI.

The default-off ARD phase creates one independently reviewed weather MCP with its own publisher domain. It does not modify the shared smoke samples. A temporary immutable snapshot makes the checker's fixed query match that MCP. The phase checks the exact public identifier/type and requires all three Registry routes to return HTTP 200. Native Search must report exactly one result; manifest/publisher validation must cover every snapshot entry. All three native runs require successful exits and zero warnings.

`ard_checker.py` retains the fixture's Resource, revision, release and snapshot identities, exact query, manifest digest and native output digests. Public bytes, ETag and active snapshot identity must stay unchanged during checking. The phase retires its release and rolls back to the original snapshot before later workflows run. Historical fixture records remain in the invocation-owned database until cleanup. The declared MCP endpoint is never invoked.

The report's `ardConformance` field requires complete matching evidence before default-off can pass. A valid checkpoint survives a later phase failure and independent cleanup. Raw `ard-manifest.log`, `ard-publisher.log` and `ard-registry.log` stay beside the owning `report.json`. Empty results, another identity, zero native results, failed exits, changed warnings or an unstable publication cannot qualify. The generic `conformance-live` target remains read-only and does not create this fixture.

Optional OTLP tracing has a focused `make test-tracing` profile using the same supervisor and fixture. It checks real protobuf exports, private HTTPS/authentication, incoming parents, durable Job links, replay, original-process failure-counter increase and fresh accepted export after collector recovery, separate restarts and disablement. Its four-mode overhead report compares off, sampled, fully sampled and unavailable-collector HTTP reads without timing the response-contract subprocess. See [tracing tooling](../tracing/README.md). It supplements the three-profile `make test-functional` aggregate. Add `TRACING_VIEWER=on` to query actual retained handoff and replay spans in the separately started local Jaeger viewer. This also checks incorrect and correct installation/service filters. See the [owned viewer check](../tracing/README.md#query-a-real-job-handoff-in-jaeger) for setup, unchanged OTLP relay, evidence and cleanup.

The default-off profile validates authenticated `/api/v1/service-information` and private worker `/status` timing fields. Worker readiness, outage and recovery checks remain intact. The tracing profile compares API/worker execution IDs from those responses with real exported `service.instance.id` values. Static `/version` contracts remain unchanged; these reads do not provide startup history or node membership.

This directory tests the shipped API/worker processes and operator binaries in invocation-owned Compose stacks. Start from the repository root with the maintained Make targets; internal scripts and `test-functional-workflow` require supervisor-generated state and are not standalone entry points.

The subprocess supervisor drains stdout and stderr separately under one combined byte limit and deadline. Captured command results contain stdout only, so text or JSON operational diagnostics on stderr cannot corrupt CLI JSON. Raw diagnostics remain transient; failed children retain only the existing closed failure classifications. Process tests require safe terminal stage/code output while checking exit status, unchanged state and audit evidence independently.

The compiled-process fixture retains a bounded private log tail for startup classification. Failed assignment revocations report fixed problem and current-row observations without printing response text or credentials. Failures remain failures; diagnostics do not retry commands. Isolated helper tests check these projections without processes or databases. PgBouncer role-readiness tests require the selected real pooler gate.

Worker transition failures retain the phase and a bounded `failureLocation.workerStatus` snapshot in the existing report. It contains the last valid states, readiness, separately observed Job/outbox degradation, sample count and monotonic elapsed milliseconds. It excludes worker identities, timestamps, URLs, credentials and raw responses. Diagnostics make no extra request and preserve the 45-second transition budget.

The core journey also creates a Plugin draft, verifies exact readback, replay and tenant isolation, rejects invalid checksums and private archive URLs without persisting versions, accepts four-kind observation filters, then deletes the draft. Positive Plugin archive inspection uses the owned-TLS HTTP and PostgreSQL integration tests; container fixtures do not bypass the production anonymous-public HTTPS boundary or depend on an external archive host.

The colocated harness tests are different from running those stacks. `runner_functional_test.go` runs the local Python regressions during `make test` and `make verify`, using mocked subprocesses and test-owned HTTP fixtures without starting Compose or PostgreSQL. Real packaged workflows run only through the explicit targets below. See [test placement](../../documentation/src/content/docs/contributing/testing.md#test-placement-and-selection).

Private-CA clients share `http_client.fixture_tls_context`, which requires TLS 1.2 or newer while retaining certificate and hostname verification against the selected fixture CA. Missing or invalid trust fails without fallback. Isolated tests assert that client policy; the packaged profiles verify real private-CA exchanges. The peer `HostClient` preserves the base client's request options and copies caller headers before applying its configured Host. `retry_unavailable` only classifies readiness failures for operation accounting; callers still receive failed responses and own any polling.

Unexpected Search replies can retain fixed cursor/refusal categories in the existing HTTP failure diagnostic. Classification requires exact routes, statuses, service codes and reviewed messages. Reports never retain response text or tokens. Unknown errors remain unclassified. An unexpected reply still fails the journey.

| Target | Scope |
| --- | --- |
| `make test-functional` | All three profiles below, each with its own isolated stack and report. |
| `make test-functional-default-off` | Core governance, discovery, federation, credentials, operators, persistence and recovery with optional webhooks disabled. |
| `make test-functional-webhooks` | Enabled notification lifecycle, transport safety, retry, retention, key maintenance and real delivery to the publication-report example. |
| `make test-functional-oidc` | Real HTTPS service-account tokens and production dedicated/shared Catalog mappings using one reviewed independent provider. |
| `make test-backup-restore` | Full logical backup, fresh PostgreSQL restore, all-tenant verification and recovered API/worker behavior. Separate from the three-profile aggregate. |
| `make test-federation-peers` | Two populated four-kind Catalogs: indexed/live/referral discovery, reciprocal sources, adoption, scheduled lifecycle and recovery. Separate from the three-profile aggregate. |
| `make test-postgres-failover` | Separate planned and abrupt physical PostgreSQL writer changes; each scenario has fresh state and its own report. Not part of the three-profile aggregate. |

See [prerequisites and verification workflow](../../CONTRIBUTING.md), the [coverage inventory and profile selections](../../documentation/src/content/docs/contributing/testing.md#functional-coverage-inventory) and dated execution evidence. These container end-to-end targets build local images and acquire pinned dependencies. A passing main build can be tagged for release without rebuilding. A profile pass is not the full aggregate, every permutation or production certification.

The peer journey compares three complete indexed generations through its existing index and update phases. It checks retained raw entry bytes against their digests and preserves the exact earlier Snapshots and occurrence provenance after correction. Existing checks preserve unrelated local Resources, releases, publication and adopted Versions. Paused-sync refusal, exclusions, failed-sync recovery, tenant isolation and stale-preview checks remain separate assertions. Domain and PostgreSQL tests cover incomplete-generation preservation and stale-worker fencing. See [source correction](../../documentation/src/content/docs/operations/federation.mdx#correct-an-unwanted-successful-synchronization) for the operator procedure.

The owned API/worker overlay sets a two-second connection timeout in each runtime DSN and pool lifetime/idle/health timings to `45m`/`3m`/`15s`. The connection timeout bounds TCP attempts while PostgreSQL is stopped, so the outage check can observe supervisor retries within its existing observation window. Packaged API and worker diagnostics must report the pool timings. The process/PostgreSQL gate separately inspects actual composed pools at `2s`/`500ms`/`100ms`. Repository integration tests observe idle and lifetime recycling, active-transaction survival and replacement session admission.

The owned API/worker overlay sets `jobs.maxAttempts=7` and `jobs.retryCeiling=5.5s`. The federation journey checks limits on manual and automatic Jobs, retains its incomplete-pagination regression, then exercises real 429/503 replies with two-second hints. It observes at least the second failed attempt under the configured cap, preserves the complete view and follows the same Job to recovery after its stored due time. Header cases run after the original audit-count assertions; they do not replace them. The existing operator journey also sends a terminal 403 with a two-minute hint: the Job must fail on its first attempt without moving its due time. Explicit retry preserves the seven-attempt budget, recovers without stale failure evidence and leaves its terminal parent unchanged.

The overlay also sets worker dependency restart windows to 1.25s initial and 2.5s ceiling. The existing PostgreSQL outage phase observes Job/outbox degradation with live processes, then requires all worker components to recover and a newly submitted sync Job on the existing fixture source to succeed before housekeeping removes that source and before stack recreation. Narrow container/PID/start-time/restart-count comparisons reject process replacement masquerading as recovery. Worker diagnostics verify effective settings. The transitional status validator still permits degraded components after readiness returns; the phase check requires their later recovery. Isolated helper tests reject false recovery, replaced processes, incorrect settings and failed or mismatched Jobs.

Outage observations accumulate separately for the Job worker and outbox processor within the same continuously-not-ready phase. Independent jittered retries report running when callbacks reenter; their degraded backoffs need not coincide in one sample. Every sample must still pass the fixed status contract and required readiness state. Recovery requires one coherent healthy component sample plus completed fresh work and unchanged process identities; outage observations cannot establish recovery.

The OIDC outage control distinguishes unavailable verification from invalid credentials. An unknown signing-key ID requires a JWKS refresh. With the issuer stopped, this returns `503 DEPENDENCY_UNAVAILABLE`; the same token returns `401 UNAUTHENTICATED` after issuer recovery. A valid cached token remains usable inside the existing cache window. These checks do not extend that window or accept unverified tokens.

## Where behavior belongs

| Files | Responsibility |
| --- | --- |
| [runner.py](runner.py), root [Makefile](../../Makefile) | Fixed profile selection, owned environment, bounded process/output supervision, source identity, reports and independent cleanup; Make contains the private stack lifecycle recipe. |
| [failover.py](failover.py), [failover.compose.yaml](failover.compose.yaml) | Physical replication, controlled writer loss, unchanged-process recovery, retained replay, interrupted-command repair and leased-Job recovery. |
| [backup_restore.py](backup_restore.py), [backup_ratings.py](backup_ratings.py), [backup_restore.compose.yaml](backup_restore.compose.yaml), [test_unit_backup_restore.py](test_unit_backup_restore.py) | Whole-fixture logical backup/restore, read-only all-tenant verification, runtime recovery and failure-before-startup regressions. |
| [peers.py](peers.py), [peers.compose.yaml](peers.compose.yaml), [test_unit_peers.py](test_unit_peers.py), [fixture/peers.go](fixture/peers.go) | Independent Catalog federation, fixed-upstream discovery TLS frontend, bounded phase evidence and recovery regressions. |
| [coverage.json](coverage.json) | Shipped-surface classification, exact evidence references and justified lower-level dispositions; not a scenario interpreter or runtime pass report. |
| [operation_coverage.py](operation_coverage.py) | Bounded per-process operation counters and explicit compatible-report summaries using identities supplied by the Go contract driver. |
| [http_client.py](http_client.py), [response_contract.py](response_contract.py), [contracts/](contracts) | Bounded HTTP/JSON and method-aware Go OpenAPI validation. Exchanges go through stdin, not retained private response files. |
| [core.py](core.py), [journeys.py](journeys.py) | Shared mutations/readback, governance/publication and discovery journeys, replay/ETag and audit assertions. |
| [federation.py](federation.py), [credentials.py](credentials.py) | Source/snapshot/observation/adoption workflows and mounted bearer, API-key and mTLS transport with rotation, missing-binding refusal and recovery. |
| [operators.py](operators.py), [operations.py](operations.py) | Real protocol-v2 orphan repair, Job cancellation/manual retry and fixed private-worker status/version and diagnostics through dependency recovery. |
| [audit.py](audit.py), [deadline.py](deadline.py), [restore.sh](restore.sh), [housekeeping.sh](housekeeping.sh) | Existing bounded audit, work-deadline, stopped-runtime restore and class-specific cleanup qualification. |
| [webhooks.py](webhooks.py), [webhooks.compose.yaml](webhooks.compose.yaml) | Separate enabled-webhook profile and invocation-owned receiver inputs. |
| [oidc.py](oidc.py), [oidc-realm.json](oidc-realm.json), [compose.oidc.yaml](compose.oidc.yaml) | Independent provider scenario, non-secret realm template and single image-pin authority. The separate [oidc-trust.compose.yaml](oidc-trust.compose.yaml) remains the narrow controlled discovery/startup-refusal fixture. |
| [fixture/](fixture), [compose.yaml](compose.yaml), [compose.credentials.yaml](compose.credentials.yaml) | Owned TLS fixture and test-only mounts/ports layered on canonical Compose. Credential listeners are explicitly enabled by Compose and are not required by the shared Minikube fixture. |

The Minikube topology launcher reuses the federation client and builds the same response validator into its owned host temporary directory. It does not install Keycloak or add a second topology; Kubernetes-specific assertions stay under [test/minikube/](../minikube).

## API operation reports

Each functional profile retains `operationCoverage` in its existing `output/functional/run-<id>/report.json`. The Go contract driver derives the complete operation ID denominator from embedded OpenAPI and identifies actual method/template pairs; Python does not maintain a second route matcher. The existing inventory still owns coverage dispositions. Worker routes, fixture/provider/receiver traffic and CLI commands are not API operations.

Only the supervisor-selected API origin is automatically counted. Other Catalog origins, such as a second peer or restored instance, retain their existing assertions and contract validation but do not add counters unless explicitly selected by the test client. Direct HTTP probes outside the observed clients are also unmeasured. This is operation coverage for the selected listeners and clients, not a capture of every network exchange.

Summarize explicitly selected reports from the repository root:

```sh
make functional-coverage FUNCTIONAL_REPORTS='output/functional/run-<first-id>/report.json output/functional/run-<second-id>/report.json'
```

Replace the example paths with actual run reports. This read-only target does not build, run services, find a latest run or require all profiles. It prints JSON with the denominator, observed and expected-positive operation counts, per-operation negative evidence/dispositions, selected profile results and `packagedGaps`. A gap means no measured positive response for an applicable packaged operation in the selected reports; it does not by itself establish missing implementation.

The target returns nonzero for failed/incomplete runs, cleanup failure, malformed evidence or incompatible identities. A passing summary means its selected runs passed, not that every operation was covered. Disabled and lower-level-only operations remain in the total denominator.

Read the summary in this order:

1. Check `status` and each run's `eligible` field. A failed or incomplete run retains observations for diagnosis but contributes no passing evidence, even when combined with a passing run.
2. Inspect `packagedGaps`. Select the missing applicable profile or add a focused assertion to an existing journey; do not add requests solely to increase a count. Disabled webhooks are excluded from gaps when all selected profiles disable them, but remain in `totalOperations`. Lower-level-only operations retain their separate test evidence.
3. Compare `expectedPositiveOperations` with `observedOperations`. An expected 401 rejection is useful negative evidence, not proof that the operation succeeds for an authorized caller. Repeating the same operation, replaying a command or selecting the same run twice does not increase the unique-operation count.

The default-off/webhook/OIDC selection targets the core API operations, including authoring histories, review lists and bound evidence, current access and indexed bookmarks. Optional `reindexCatalog` requires the separate `make test-semantic` real-model qualification described in [embedding tests](../embeddings/README.md). With semantic search disabled, `expectedPositiveOperations` is smaller than `totalOperations`, and `packagedGaps` includes that operation. Inspect these fields explicitly: the summary command's exit status checks run validity, not coverage completeness, and the semantic report is separate evidence rather than an input to this summary. An empty gap list would not establish every workflow, input or authorization permutation. Focused profiles may legitimately omit operations exercised by another profile.

History checks use a tiny owned fixture and one-record pages, compare known IDs/digests and verify cross-tenant denial and cursor binding. The archive journey revalidates a submitted version and confirms the original submission still returns its original evidence, then publishes a replacement and checks both release histories. Current-access checks cover scoped grant/revocation, an active member without product roles and real OIDC scope ceilings. Indexed bookmarks require tenant-level `federation.discover`. They also require `catalog.read` when OAuth scopes are enforced.

The fixture checks source pause/resume/removal without exposing raw observations. Its development Reader bundle includes explicit companion assignments; an ordinary `reader` grant does not add them. The representative Go driver sends malformed raw JSON/media types, verifies repeated rejection without namespace creation, and checks valid charset-bearing creation and exact replay; the ordinary Python client continues to send valid JSON only.

The archive journey also starts with invalid Skill frontmatter. It checks persisted blocked evidence and exact archive identity, failed-package revalidation, cross-tenant denial and absence of partial inventory or transient bytes. A corrective revision preserves the failed history; a stale append cannot change the corrected candidate. A listing still referencing the failed revision cannot be submitted, even after that version has a corrected candidate. Repeated rejected submissions create no review records or listing changes. Explicitly updating the listing to the corrected revision then proceeds through the existing independent review, publication, replacement and retirement checks.

Only reports with matching source/worktree, contract/inventory and application build identities can be combined. Empty observation sets and missing or inconsistent served build identities are rejected, even if a report claims success. Configuration/profile identities remain separate; identical duplicate run IDs are counted once and conflicting duplicates fail. Reports must match the current checkout's contract and inventory. The three-profile `make test-functional` target still runs independent default-off, webhook and OIDC stacks; pass their explicit report paths to combine evidence. Peer, restore, tracing and writer-recovery reports are optional inputs, not automatic extra runs.

The namespace journey rejects a reused key with changed input or `If-Match`, then verifies the original response still replays and only one update/audit exists. The scoped-grant journey denies a previously successful command after revocation, restores the same authority and verifies exact replay without a second Resource creation. Core mutation checks require the replay header as well as identical bodies and ETags. Public unpublish checks send the old ETag and require 404, then verify conditional reads work again after explicit publication.

Each observation records a fixed step, operation ID, status, contract outcome, expected-outcome category and count. Status `0` denotes a transport failure with no complete HTTP response. `positive` means an expected 2xx/3xx response, including conditional 304; `negative` means an expected rejection, and `unchecked` means the collector was not supplied the calling assertion's expected status. Neither a 202 nor an exact replay proves a new completed mutation. Persisted outcomes, recovery and independent review are established by the owning journey assertions and completed steps. Failed runs retain partial observations but contribute no passing positive/negative evidence to the summary.

Private exchange bodies travel only on bounded stdin to the validator. Reports never retain raw paths, queries, headers, response bodies or credentials. Thread-safe per-process/step checkpoints are atomically replaced in supervisor-owned temporary state, then collected before cleanup, with limits of 128 streams, 4,096 distinct counter rows, 1,000,000 occurrences per row and the existing 1 MiB report bound. Shipped smoke/remote-CLI internals, shell probes and the publication-report SDK remain explicitly unobserved. Their assertions and inventory references do not create HTTP counters. Timed tracing and performance requests are not instrumented by this report.

The collector uses only supervisor-injected `CATALOG_FUNCTIONAL_OPERATION_COVERAGE`, `CATALOG_FUNCTIONAL_STATE_DIRECTORY`, `CATALOG_FUNCTIONAL_BASE_URL` and `CATALOG_FUNCTIONAL_CONTRACT_DRIVER`. These are internal test inputs, not application settings or supported direct-script setup. `FUNCTIONAL_REPORTS` is a target-only Make input; no `.env` default is needed. Fast counter, failure, isolation and aggregation regressions run with `go test -tags=functional ./test/functional/...`; no telemetry backend or browser runner is involved.

## Native PostgreSQL process tests

`make test-process-postgres` executes `TestPostgresProcesses` in `process_integration_test.go` with the `integration` tag. It builds temporary host CLI/API/worker executables, uses the existing `compose.integration.yaml` PostgreSQL 18 launcher and creates exact runtime roles using the maintained bootstrap fixture. No application container, browser, model or external issuer is required. The default local test selection excludes this file; tagged lint/compile checks validate it, and unit targets never execute it.

The test checks fresh migration plan/apply/reapply and exact manifests, development identity provisioning, realm suspension/reactivation, stale-revision rejection without state/audit changes, API and worker readiness, graceful SIGTERM and listener release. The compiled owner command also qualifies scoped-only/mixed grants, deterministic alias unions, stable active references and actor-type conflict refusal before any principal commit. An authenticated namespace survives API process replacement; another tenant cannot read it. Worker operations expose no product namespace route, and the worker database identity is rejected by API startup. Existing packaged/OIDC/repair/restore and repository integration suites retain their broader assertions.

After stopping the product processes, the same fixture checks bounded native restore verification, exact audit-range comparison, refusal to release a completed command reservation, and search repair with no work. It verifies retained state and generation values. The pooled writer-loss profiles use the explicit direct promoted-owner endpoint for these operator checks.

The native import case uses the compiled CLI and Contributor permissions. It imports two related Agents in separate namespaces, drops the second committed ListingRevision response and routes original-key replay to the other API process. The first Agent uses a 512-code-point opaque identifier and a 128-code-point version. Exact readback, relationship references, original resource/version replay and export preview preserve these Unicode values. Assertions require two of each created domain/evidence row, eight mutation-audit records, eight outbox records and the exact cross-namespace relationship. It also verifies large extension numbers through SQL and HTTP, complete no-write reruns, collision refusal, strict control decoding and zero review/release/snapshot creation.

After those draft-only assertions, the same fixture independently reviews and publishes one Agent for native export. The compiled CLI previews bounded local Browse results, saves frozen identities and exports the selected release as a batch. SQL and HTTP controls verify inline `jsonb` numeric precision and its changed notation, with separate retained-document and exported-file digests. A new manifest explicitly selects the destination namespace and publisher. Re-import preserves the complete native JSON as a new draft and creates no additional release. Memory-backed four-kind CLI tests and private TLS fault controls remain separate. Run `make test-process-postgres` for this real PostgreSQL boundary; ordinary unit tests never execute it.

The same process fixture exports a reusable JSON file through the compiled CLI. Exact VersionRevision and ListingRevision selectors use the private export API. Assertions compare delivered native text, numeric precision, canonical identity, Version label and Listing display name against the retained PostgreSQL record. The four-kind local functional test separately covers incomplete drafts, packages and revoked scoped access.

The local functional selection also runs the compiled CLI against the real memory-backed API for all four kinds in one manifest. It verifies retained native validation, complete Skill inventory and executable flags, immutable handoff metadata, exact HTTPS Plugin retrieval evidence, and ListingRevision bindings. Completed resume succeeds without archive availability or new mutations. Changed Plugin archive bytes are refused before a Version commits; the receipt preserves the created Resource and prevents a new mutation on resume. No review or release is created. Protocol fault fixtures separately cover blocked evidence, later-item interruption, zero writes for invalid later input or denied namespaces, unchanged whole-manifest intent and receipt/principal recovery limits. Isolated controlled-transport units reject incorrect retained Version, evidence, Listing and ListingRevision bindings and unavailable evidence without more writes or receipt changes. These tests do not contact hosted resources.

`make coverage-process-postgres-profile` runs the same scenario with Go-instrumented application binaries. Ordinary executions have separate counter directories; expected nonzero rejections must still emit counters, while unexpected exits, missing counters and cleanup failures fail the gate. The one deliberately SIGKILLed import invocation has no `GOCOVERDIR`: it cannot flush counters and contributes no process profile. Its native crash assertions still run, and its fresh-process resumes, refusals and orderly SIGTERM companion remain instrumented. This explicit test-only selection does not admit metadata-only or otherwise incomplete collector inputs. The profile at `coverage/process-postgres.out` covers the two command packages and runtime composition. It is distinct from `coverage/postgres.out`, which instruments the database package. `make coverage-full` collects both, plus fresh unit/local profiles, verifies source stability and reports their deduplicated application union with an 80 percent per-file minimum.

The internal `test-process-postgres-dsn` and `coverage-process-postgres-dsn-profile` targets require an explicitly disposable, fresh loopback `catalog_test` database through `CATALOG_TEST_DATABASE_DSN`; normally use the owning Compose targets. They deliberately reject an already migrated database. `CATALOG_PROCESS_COVERAGE_DIR` is launcher-owned test state, not application configuration. Do not run simultaneous coverage writers or use retained development services as fixtures.

### Live tenant settings qualification

The same `test-process-postgres` fixture checks the three tenant overrides through two compiled APIs and two workers. Settings writes use the actual HTTP API, private ETags, idempotency and current custom delegation. A separate tenant remains on deployment defaults. An owned loopback ARD manifest supports real source inspection, enrollment and automatic/manual synchronization.

Checks require unchanged process identities during live edits, new admissions with the committed attempt limit and unchanged retained Job budgets. Concurrent saves yield one success and one stale refusal. Revoked delegates cannot replay private results. Temporarily unavailable policy storage must refuse API admissions and degrade workers without creating fallback Jobs. Recovery must complete new work through the original workers. Separate planned API/worker replacements verify saved overrides, changed-fallback validators and reset inheritance.

The PostgreSQL suite also selects one eligible tenant behind 35 paused sources with a 32-source limit. It inspects the bounded query plan and measures sixteen exact policy reads. Timings are diagnostic evidence, not host-sensitive pass thresholds or production capacity claims. Existing first-save, waited-lock, cancellation and audit-rollback assertions remain in the repository suite.

## Selected PgBouncer qualification

`make test-pgbouncer` runs [the focused owned pooler fixture](../pgbouncer/README.md). It reuses `TestPostgresProcesses` with separate direct-owner and pooled runtime DSNs, two API/two worker processes and the unchanged current-authority assertions. It adds protocol, capacity, private-TLS, reset, cancellation and outage/writer-recovery controls. The ordinary `test-process-postgres` and dynamic-role integration matrix continue to use direct PostgreSQL. No general integration DSN is silently rerouted and real database/process cases remain functional.

## Two-Catalog federation

The client phase checks page-size-one traversal through Control and authenticated ARD Search, plus opaque live-only cursors through the packaged `catalogctl`. It publishes 25 temporary peer resources to cross the normal 24-item browser page size. Real browser interactions verify complete mixed-origin traversal, retained cards during a peer outage and identical-token retry. The phase repeats with real PostgreSQL hybrid ranking and a fixed-vector endpoint on the owned TLS fixture. Equal vectors check ordering and continuation, not model relevance. The separate MiniLM qualification owns relevance checks. The phase restores B's runtime settings, retires its temporary releases on A and confirms that live reads created no indexed observations. Final evidence requires both modes, CLI traversal and both browser runs.

Run `make test-federation-peers` with Go, Python 3, curl, Docker and Compose. Run `make ui-install ui-browser-install` to install the locked UI dependencies and Playwright Chromium. The client phase builds an invocation-owned UI host and browser assets; it does not restart a retained UI. The supervisor rebuilds one Catalog image and one discovery fixture image, then starts two independent API/worker pairs with separate PostgreSQL 18 volumes, exact runtime roles and deployment-local development identities. Its fixed TLS frontend routes A and B discovery requests to their actual APIs, preserving Host, response bytes, status and ETags. B uses invocation-generated CA trust and a tenant-assigned outbound profile. The private A control uses an origin-bound reader credential. No retained Catalog, identity provider, hosted peer or unrelated container is used.

Each Catalog publishes one native Agent, MCP Server, Skill and Plugin through separate author/reviewer/publisher identities, plus private and draft disclosure controls. Public releases declare an exact license and two category labels. Search and Explore check those declarations, full-IRI/dotted-prefix equivalence, all-kind membership and matched local facet counts. Live queries check forwarding and topic preservation through the peer's separate entry context. Identifiers, Catalog namespaces and publisher identities differ. Plugin preparation reuses the maintained native sample tree and existing archive tool; its static binary, private TLS inputs and exact allowlist are invocation-owned mounts, never added to the application image. The non-root HTTPS archive service has no host port. Development-only archive admission does not relax production fetching or enable this exception in Helm. Stopping the archive during indexed/live discovery proves those reads do not need to fetch or execute Plugin packages; explicit adoption restores it for normal bounded inspection.

The peer seed and live phases also qualify the six public native-card paths. Local Search and Explore use declared A2A skill IDs, streaming, bindings and versions, plus MCP transports and versions. Live queries check A2A streaming and MCP versions across the approved source. The assertions exclude URL-only Skill/Plugin facts, private controls and drafts. The fixture does not invoke native endpoints.

The ordered phases begin with startup, governed seeding, exact indexed entry content/provenance, unchanged synchronization, and mixed local/indexed pagination and filters. They then cover scope changes, sealed listing and Plugin version updates, withdrawal, and API outage and unpublication recovery. Later phases cover all-kind live/referral discovery, bounded live paging, client pagination, credentials, reciprocal federation, deliberate adoption, automatic refresh, pause/resume, removal and reconciliation. The final transfer phase checks reusable drafts through the UI and CLI. ARD Registry List pagination is traversed separately; manifest synchronization does not qualify Registry List ingestion. Registry self-entries are counted separately from native resources. B's original Resources, releases and public entries must survive ordinary federation unchanged.

Control declaration checks also traverse page-size-one local/indexed results, preserve counts and source provenance, and reject a differently cased license. Four-kind live checks require both normalized integration and kind categories. ARD queries keep their original exact-value filters. These checks reuse the ordered phases and owned peers.

The native Plugin archive binding is Catalog-supported; it is not a standardized ARD Plugin type or an installation capability.

Live queries require explicit disclosure acknowledgement and an authenticated host assignment. `none` searches only local publications; `referrals` returns local matches and an eligible Registry reference without an upstream Search contact. Referral participation is disabled before `auto` queries, whose upstream mode stays `none`. Safe counters record only fixed host/method/route/mode/status keys. They verify exact page counts and no recursive traversal.

The owned overlay sets B's `federation.maxLivePagesPerSource` to three and A's to one. Product defaults remain one. An explicit fixture page ceiling forces small upstream pages from A during this phase. The TLS frontend applies the same ceiling to each continuation request; actual Catalog A responses and signed tokens remain unchanged. The ceiling is removed before normal client/browser qualification. An unfiltered direct query establishes the first, second and third peer results. A temporary exact-identifier source scope excludes earlier results without changing the query sent to A. One downstream request must reach the allowed result on page two or three. ARD `auto` uses the approved authenticated host; private-network sources remain unavailable to anonymous discovery, with no outbound request.

The same later-page admission is checked through authenticated Catalog Search and the administrator diagnostic. Diagnostic replay returns the same response, creates no second audit and makes no additional peer request. A fixture control fails only A's continuation requests with HTTP 503. Earlier accepted peer entries and authorized local results must survive. A fresh query must begin without a token and recover after the fault is removed.

Page counters contain only fixed hosts, initial/continuation classes and status counts. They contain no query text, credentials or tokens. Source scopes and fault state are restored even after a failed assertion. Later Control pages must traverse every authorized durable and live result exactly once and preserve durable totals. Composite cursors replay the same bounded peer window. The authenticated baseline includes tenant-visible releases as well as public releases.

ARD and Control continuations preserve durable ordering and traverse one unchanged bounded live window. Signed cursors bind the query, current authority, generations and live-window digest. Peer failure during replay returns HTTP 503 without a page or replacement cursor. Retrying the identical token after recovery returns the same healthy page. Small upstream pages preserve useful results and `continuation_unavailable`/`partial: true` in the bounded administrator diagnostic. The latter remains an audited, idempotent command; replay must return the same response without another peer contact.

The client-pagination phase traverses one-item API pages and live-only pages through the packaged CLI. It temporarily publishes 25 additional Agent releases on A. The normal browser requests 24 cards per page, retains them during an actual peer 503 and retries the identical cursor after recovery. Combined-origin browser pages must contain every expected identity exactly once. No response interception or browser page-size override is used.

The phase then enables the existing semantic configuration on B, with owner-applied pgvector setup and a fixed three-dimensional provider in the owned HTTPS fixture. API and browser checks require hybrid ordering, complete continuation and recovery. The provider tests ordering and continuation, not model quality; use `make test-semantic` for the real-model selection. The phase restores B's configuration and worker cadence, retires its temporary releases through the API, republishes A and verifies that live reads created no observations.

Live results and diagnostics create no observations or local Resources. Reciprocal sources import only the other Catalog's native inventory, and regenerated publications exclude indexed observations.

Agent and Plugin adoption selects complete current occurrences through the API and produces private drafts with retained content/package evidence and immutable provenance. Reader attempts, including replay, are denied; successful same-key replay retains draft identities and exactly one originating audit and notification. Subsequent remote updates leave the captured durable revisions unchanged. No submission, release, installation or execution is implied.

All sources remain `manual_review` until the scheduled phase. The primary B source then uses the effective worker `1m` maintenance interval. Two worker-admitted complete Jobs must converge without an explicit `:sync` or source-policy revision change. A stale removal preview is rejected with the current ETag.

An explicit manual sync while paused is accepted asynchronously, then fails as `invalid_input` before a peer fetch or new snapshot. Pause is observed for eighty seconds (cadence, spread and rescan allowance), with no new admission/snapshot and a ready worker; resume must recover automatic/live participation. Returning to manual mode precedes a fresh preview/removal. Historical evidence and adopted drafts survive removal, while current indexed/live participation ends.

Removed source and parent-history routes return 404; exact snapshot/observation identities retain authorized evidence reads.

The scenario has a ten-minute work budget inside the twenty-minute supervisor bound. Ordinary waits allow sixty seconds; automatic waits allow 120 seconds, and the pause observation has a bounded 100-second polling window. Compose commands allow at most forty-five seconds. Callback results at or after either effective deadline are rejected.

A's random loopback port stays fixed during its API restart. Reports under `output/functional/run-<id>/report.json` retain validated phase progress, both native inventories, build/source/image identities, actual archive-tool/allowlist hashes, safe counters and owned cleanup. Startup progress reports no published kinds until governed seeding completes. Final qualification independently requires every ordered phase, exact A/B native inventories, bounded contact counters, eight compatible image identities, both archive-input hashes, collected operation observations, core/hybrid API/browser and live-only CLI pagination evidence and `peerIdentityVerified: true` after the final unchanged-source check.

The final `draft-transfer` phase reuses both PostgreSQL Catalogs and the private UI build. UI downloads feed CLI creation for all four kinds. CLI exports then feed UI Add version on the original Resources. Content-only Skill transfers preserve existing Listing state. Exact exports verify native content and metadata after both writes.

Imported Listing metadata uses an explicit destination Publisher; no submission, release or domain approval is inherited. One imported Agent follows independent review, release publication and explicit ARD snapshot activation. The active snapshot stays unchanged before that activation. The validated `peerDrafts` result requires both directions, four new Resources, four new Versions, content-only outcomes, publication and UI cleanup. This phase invokes no resource.

Missing or contradictory evidence and retained contract failures cannot qualify, even when the phase list is complete. Bodies, credentials and query text are excluded. Cleanup removes only invocation-owned containers, volumes, network, generated inputs and image aliases on success, failure or interruption.

The ordinary target remains strict. For a deliberate investigation after a response-schema failure, use:

```sh
CATALOG_FUNCTIONAL_PEER_INVESTIGATE=on make test-federation-peers
```

This mode continues only read-only Control Search schema mismatches, retaining failed operation observations and fixed `peerContractFailures` entries. Transport, mutation and semantic failures remain fatal. A run with any retained schema failure still exits unsuccessfully and cannot qualify the journey. Control OpenAPI and the UI consumer include the emitted `continuation_unavailable` diagnostic, with regression tests. Those failed runs remain unsuccessful evidence; the recorded rebuilt qualification and source-matched repeat passed all seventeen phases.

This opt-in gate supplements `make test-functional`; it is not PostgreSQL replication, a freshness SLA, a mixed-version upgrade, Kubernetes deployment qualification or resource execution. Packaged phase/operation accounting is separate from Go statement coverage and observes the selected Python HTTP clients, not every request to both origins. The CLI and browser assertions do not add operation counters or browser-source coverage.

The scope, live and adoption phases also qualify source exclusions across all four kinds. They check immediate indexed and live omission, healthy empty results, retained administrative bytes, clear-list recovery and blocked new adoption. A completed local adoption and its idempotent replay survive later source exclusion. PostgreSQL tests separately cover duplicate-source attribution and SQL paging.

### Fast peer checks

From the repository root, run isolated frontend and supervisor units, then local functional contract checks, without starting containers:

```sh
go test ./test/functional/fixture -race -count=1
make test-tooling-unit
go test -tags=functional ./test/functional/... -race -shuffle=on -count=1
```

This also runs the Python peer tests through `TestFunctionalSupervisor`; no additional Python test framework is needed. The checks cover fixed routing, Host/credential/validator preservation, cancellation, body bounds, failed-read cleanup, exact Search evidence, recovery, opaque pagination and ordered phase reporting. Polling tests use simulated time and never wait for the real sixty-second timeout. The frontend's 64 KiB request, 4 MiB response and four-second transport bounds are test-fixture limits, not replacements for the service's documented API limits.

Run `make test-federation-peers` to verify the actual publication-to-sync composition after changing it. On failure, inspect the report's `lastPhase`, `failureLocation`, optional `httpFailure` and `cleanup` fields. The outage phases deliberately create failed source snapshots; the overall run passes only after retaining membership and then completing a new successful enumeration. Do not interpret a retained observation or an earlier successful phase as proof that recovery completed.

Failed peer Search traversals can also retain `searchPageFailure`. This includes indexed paging when a source excludes specific identifiers. It identifies the Control or public surface, page number, completed pages and whether the failed request contained a continuation. Its optional HTTP diagnostic uses fixed categories, including separate binding, expiry and live-cursor refusals. A first-page failure has zero completed pages; do not assume every HTTP 400 concerns a continuation.

An optional `clock` object contains only two bounded differences in seconds. `responseDeltaSeconds` compares the failed response's Date with the preceding successful page's Date. `cursorExpiryMinusResponseDateSeconds` compares the submitted cursor's declared expiry with the failed response's Date. This diagnostic reads an opaque cursor only after failure; it does not authenticate the token. Reports retain no token bytes, query, response body or absolute date. Neither the comparison nor a passing repeat establishes a failure's cause by itself.

These diagnostics never retry or restart a request, relax a check or change the host clock. The Minikube peer helper emits the same validated page evidence in its bounded failure output. Diagnostic-write failures preserve the original failure and cleanup.

## Safe execution and maintenance

The local `test_functional_postgres_archive.py` fixture requires POSIX shell tools and writable `/tmp`. It allocates a private random root explicitly there so real local shell commands model the container's restricted archive-path format even when host `TMPDIR` contains dots or spaces. It verifies directory/file permissions, nested allocation, failure propagation, sentinel protection and cleanup without Docker. Production fixture archives continue to use container-side `mktemp` and the unchanged path guard.

Keep source frozen for a run: any tracked or non-ignored change invalidates its bound source identity. Python assertions must remain enabled; the supervisor refuses optimized execution and launchers explicitly set `PYTHONOPTIMIZE=0`. Do not bypass a failed assertion, expand a deadline merely to get a pass or replace a missing required unit with a successful partial report.

The supervisor owns randomly named projects, transient loopback ports, volumes, generated credentials and temporary image aliases. Do not point these scenarios at an existing deployment or supply another project's database, issuer, receiver or monitoring endpoint. Exact application fetch policy confines test destinations; this is not Docker firewall-level egress isolation. Private worker ports appear only in test overlays, not the product Service.

Reports under ignored `output/functional/run-<id>/report.json` retain bounded identities, selected units/cases, outcomes and cleanup. An optional `httpFailure` on a failed run preserves only the last recognized closed unexpected-response status/category from an uncaught client assertion, not its traceback or underlying cause. Successful supervised commands do not return a failure diagnostic. Never add tokens, DSNs, secrets, raw private documents, rendered credential configuration or unrestricted logs. For failure categories and exact-owned cleanup, follow the [testing guide](../../documentation/src/content/docs/contributing/testing.md#troubleshoot-failed-functional-runs-and-clean-up); preserve shared upstream images and unrelated developer data. Do not use a broad prune or `make compose-reset` to recover a run.

Failed core, credential, operator, federation, peer, webhook and backup/restore scenarios also retain `failureLocation`: the fixed scenario name and up to three source line numbers from that scenario's own frames, ordered from caller to failure. Match them to the report's source identity when investigating an assertion or timeout. This excludes absolute paths, exception messages, frame locals and response content; an absent location is not evidence of success.

Worker outage and recovery checks also retain `failureLocation.phase`: either `degraded` or `recovered`. Diagnostic-output checks and process comparisons omit this field. Each component-transition wait retains its 45-second monotonic deadline. Independent Job and outbox retries can enter degradation at different times; the outage check must observe both while PostgreSQL remains unavailable. Recovery requires healthy components, fresh successful work and unchanged API/worker process identities. A passing repeat does not explain a historical timeout. Keep worker transition timings and outcomes in local run reports.

Webhook polling failures also retain `webhookFailure`: fixed `case` and `wait` labels, `expectedState`, `observedState`, `attempt`, unexpected observation HTTP `status`, and a closed `category`. Unavailable fields are null; `unobserved` means no valid delivery observation was obtained, while `missing` means a successful list read did not contain the selected event. Categories distinguish timeout, incompatible terminal state, HTTP, transport, contract, invalid-response, assertion and other observation failures. The scenario writes only this last observation alongside its completed-case prefix; the supervisor validates both before copying them into the final report. No event/endpoint IDs, error messages or response content are retained. Diagnostic failure cannot replace the original test failure or bypass cleanup.

For example, this failure means the delivery exhausted or terminated before the test observed success. It does not identify the receiver's HTTP response: `status` describes an unexpected response to the observation request, so it is null here.

```json
{
  "case": "retry-throttle-loss-and-readiness",
  "wait": "retry-recovery",
  "expectedState": "succeeded",
  "observedState": "dead_letter",
  "attempt": 3,
  "status": null,
  "category": "unexpected-terminal"
}
```

The diagnostic is the last observation, not a history. A failed read clears any earlier delivery state/attempt; a valid subsequent observation clears an earlier HTTP error. When startup readiness exhausts its budget after repeated errors, the last error category remains visible rather than being replaced with `timeout`. A retained failure cannot turn into a passing report because the child returned zero, and cleanup failure is reported separately from the original test failure.

A missing-delivery failure also attempts one read-only database snapshot for the exact owned tenant, endpoint and event. Its optional `database` field contains only booleans/nulls: event/endpoint/delivery existence, event-before-registration ordering, acknowledgement, current lease, endpoint activity and event-type matching. No identities, timestamps, SQL or raw errors are retained. The query uses a two-second statement timeout within the existing bounded Compose invocation; unavailable evidence never replaces the original failure. Successful and absent-event controls qualify this diagnostic in every enabled-webhook run. A snapshot is an observation after failure, not a claim about the complete dispatch history.

Delivery waits poll valid missing/nonterminal states, fail immediately on an incompatible terminal state or failed API observation, and accept only results obtained before their 45-second monotonic deadline. Only API readiness waits around startup/restart retry recognized transport unavailability or HTTP 503. Sleeps are bounded by the remaining wait budget; an in-flight HTTP request, response-validator subprocess or Compose command retains its own existing bound and may overrun that polling budget, but a late success cannot pass. These are state observations, not locks on worker progress: an intermediate `waiting` row may advance while subsequent checks run. Keep runtime retry rules and receiver-failure assertions intact when investigating timing failures.

The throttle/recovery case checks persisted `updatedAt` and `nextAttemptAt` against the configured five-second ceiling's inclusive 2.5-5 second jitter window. The receiver's one-second hint cannot dominate that window. It waits for the second failed attempt before recovery, then requires success on attempt three, the same delivery identity, completion no earlier than the retained due time and no remaining retry timestamp. Exact hash endpoints and dispersion belong to the isolated Go tests; the packaged check qualifies configuration, worker scheduling, PostgreSQL retention and API projection together.

The database-outage case independently exercises the enabled dispatcher supervisor. It verifies the effective fractional restart settings, stops only the owned PostgreSQL container, observes Job/outbox degradation with API/worker liveness intact, then requires database and supervised-component recovery plus a newly committed namespace notification delivered successfully. API and worker container IDs, PIDs, start times and restart counts must remain unchanged. Historical dead-letter loss may keep the separate `webhook_delivery` signal degraded; that is not a failed dispatcher restart and is not cleared by this test. The existing default-off outage phase continues to qualify the drainer.

The webhook retention case waits for startup housekeeping before creating its two deliveries. Its overlay keeps the next housekeeping cycle outside the setup deadline; after stopping the worker, setup must assign both prospective cleanup markers and observe their expiry using the database clock. The cleanup phase then recreates the worker with the minimum supported one-minute housekeeping interval and allows two cycles plus five seconds for both deliveries to disappear while retaining their audit evidence. A bounded skip-locked sweep may defer rows, so the test does not require completion in the first startup cycle. It neither shortens an existing marker nor changes the host clock or production defaults.

Run timestamp-sensitive journeys on a host with stable, synchronized wall time. Webhook recipient selection requires the endpoint's creation time to be no later than the event's creation time. A backwards clock step between registration and mutation can exclude that fixture event and produce a missing-delivery timeout; a longer polling deadline cannot change its eligibility. Investigate the retained event/endpoint timestamps and database-clock observations before changing delivery behavior. Preserve failed evidence, correct the runner's time synchronization outside the test, and rerun; never rewrite event timestamps or count the failed attempt as passed.

Job retry checks also compare retained wall-clock times. After a backward clock step, retry scheduling can clamp `notBefore` to the previous `updatedAt`. The [memory](../../internal/jobs/clock_test.go) and [PostgreSQL](../../internal/platform/database/job_clock_integration_test.go) clock regressions cover this case. Preserve the failed run. A later pass or host probe does not explain an earlier failure.

Keycloak is upstream-acquired execution-only tooling under [ADR-0003](../../documentation/src/content/docs/architecture/decisions/0003-runtime-packaging-and-local-test-environments.md), not a Catalog runtime/chart/release dependency. Its mixed image licenses and known security findings remain explicit in the [review](../../documentation/src/content/docs/contributing/testing.md#functional-coverage-inventory). A pin update requires a fresh review, not just a YAML change. Exact malformed-provider, key-rotation and injected-clock cache matrices stay in verifier tests; no browser/password flow, Admin API simulator or deployment-issuer certification is implied.

When extending a journey, add only a missing composition boundary, update its inventory evidence and fixed selection where appropriate, and add focused helper/fixture regressions. Run `go test -tags=functional ./test/functional/... -shuffle=on -count=1`, `make check` and `make verify`; behavioral changes also run the affected packaged profile and the complete aggregate for shared boundaries. Update the dated evidence only after execution, preserving failures and explicitly unrun gates.

The helper safety tests include exact and excessive response/header limits before process launch, structured private stdin transport, malformed exchange rejection, closed launch/read/timeout failures and problem-status consistency even during replay. Keep these negative assertions when changing the validator; a successful subprocess exit alone is not a test of its input binding or redaction. Go invokes the Python unit tests through `TestFunctionalSupervisor`, so no additional test dependency or standalone coverage gate is needed.

## Publication-report example with real delivery

The `publication-report-example` case joins the real Catalog API, worker and compiled [Go example](../../examples/publication-report/README.md). It extends `make test-functional-webhooks`; it does not introduce another deployment target. The other twelve webhook cases keep their existing fault and recovery assertions.

The receiver uses a separate database and login on the owned PostgreSQL server. Its schema loads only there. The receiver cannot connect to the Catalog database, and the API/worker accounts cannot connect to its database. Its API identity has exactly `publication.read`, `webhook.read` and `webhook.read_evidence`. Endpoint registration and publication commands use separate administrator credentials.

The profile-only receiver shares the API's network namespace, so its SDK uses explicit loopback HTTP. The worker sends to `https://api:9443/webhook` with the fixture's private CA and exact origin policy. Input files have private modes and mount read-only; only the report output directory is writable. This arrangement belongs to the local test, not the production deployment.

The case first waits for startup reconciliation. It then activates a new snapshot and requires a succeeded delivery, the exact completed `(source, id)` receipt and a matching report. Unpublish requires a second receipt and an explicit `active: null` report. Both the receiver database and JSON file must identify the expected event and snapshot. The selected 24-hour reconciliation interval and exact report trigger prevent startup or periodic refresh from passing these checks.

The supervisor requires closed `webhookPublicationReport` evidence and the receiver container's image identity. Evidence contains the compiled program digest, two completed receipts and successful isolation, permission and report checks. It contains no credentials, event bodies or private report contents. Missing or contradictory evidence fails qualification. The separate example functional target retains its controlled-API fault tests.

## Selected-event worker crash control

The enabled-webhook profile's worker-loss case is implemented in [webhook_crash.py](webhook_crash.py) with the existing receiver's [bounded barrier](fixture/webhook_barrier.go). It stops only the owned worker to prepare one namespace event, arms the exact endpoint/source/event selection, then starts the worker. Only a valid signed request with that selection can satisfy acceptance. The receiver holds its response; an observed blocked attempt precedes SIGKILL. Container state must confirm exit 137, and PostgreSQL must retain the same unacknowledged event with an unexpired claim and no delivery outcome. A send timeout that already persisted a retry cannot pass this condition.

The barrier releases through an independent control request, request disconnection or a 30-second expiry; expiry fails instead of returning success. Explicit release runs on failure as well as success and preserves an earlier failure if its own connection fails. Supervisor cleanup remains independent. The worker stays stopped until the database clock reports lease expiry, then restarts without changing lease timestamps or runtime retry settings.

The replacement must produce a second correctly signed acceptance for the exact event, with identical payload digest, successful delivery evidence and eventual parent acknowledgement with both lease fields cleared. The lost send has no persisted outcome, so two receiver observations correspond to persisted delivery attempt one. Payload digests bind all immutable CloudEvent fields; signature timestamps may differ.

The originating namespace/revision, one originating audit and one outbox event must remain intact, as must a known other-tenant namespace and its singular audit. The API process remains unchanged. The private receiver retains one selection and at most eight digests/duplicate flags/attempt outcomes; no payload or signing secret enters its control report. The final supervisor report requires closed `webhookCrash` evidence containing selection/payload digests, two accepted attempts, exit status and completed checks.

Unrelated duplicates, missing evidence or cleanup failure cannot qualify recovery. These are at-least-once delivery assertions, not exactly-once receiver effects. Existing PostgreSQL stale-owner/lock-wait/acknowledgement-loss tests and the separately qualified same-Job topology takeover keep their distinct scope; this case does not duplicate them.

## Logical backup recovery

Run `make test-backup-restore` with the normal Docker/Compose, Go, Python 3 and curl prerequisites. The supervisor builds local images, generates private fixture inputs and owns both PostgreSQL instances; no caller-supplied database is accepted. The fresh destination uses the same pinned PostgreSQL 18 image, a separate volume and no host database port. The test supplements `make test-functional`, not its default three-profile selection.

The scenario retains three published kinds in tenant A, three draft kinds in tenant B, indexed source evidence, completed idempotency, encrypted webhook/delivery evidence and one queued source-sync Job. Workers and ordinary writes stop before two full bounded audit exports; the API then stops before `pg_dump -Fc`. A private capture binds the dump digest, every fixture table's row count/content digest, expected tenants, audit manifests, migration/profile identity and configuration. Raw host archives and audit files stay in the invocation's private temporary directory.

Each source/destination container allocates a separate `mktemp -d` directory with owner-only access; dumps are created with `umask 077`. The webhook key-recovery case uses the same helper. Container archives are removed after the operation, including failed dump, copy and restore attempts; cleanup errors fail the scenario. If the container becomes unavailable or the command budget expires, supervisor cleanup removes the owned stack.

Reports retain only safe digests, counts and conclusions.

Restoration creates the exact fixture roles and an empty `template0` database, then uses `pg_restore --single-transaction`. No Catalog bootstrap or migration creates missing state before loading the archive. The unchanged migration plan must pass before exact-grant reconciliation, which applies no migrations. Database ownership, locale, encoding, ACLs and database-specific settings are compared separately because a single-database dump without `--create` does not restore them. These fixed fixture inputs are not a general role/settings importer.

The drill saves distinct tenant overrides before capture. After restore, both tenants must retain their exact settings and validators. The queued Job keeps its two-attempt budget; restoring policy does not rewrite admitted work. The closed report requires `tenantSettingsRetained` as well as authorization policy evidence.

The API creates one unpublished Plugin from an owned HTTPS archive, plus one private saved search and one saved resource reference. The Plugin includes a manifest, a Skill document and an MCP component document. The fixture reuses the peer archive tool without starting the peer Catalog. Its exact origin, private trust and allowlist stay unchanged across runtime replacement. The archive server stops before the dump and remains stopped during restored reads. Archive bytes remain external; the database retains the inspected documents and inventory.

Restored Plugin resources, versions, evidence, documents and inventory must equal the captured API reads. Both saved items must retain their exact labels, criteria or references, revisions, timestamps and ETags. Another human in the same tenant and the other tenant cannot read those saved items. Cross-tenant Plugin reads also fail. The closed report requires `pluginRetained`, `personalItemsRetained` and `archiveStopped`; missing or false values cannot pass. It records archive tool and allowlist digests, without document bodies or private criteria.

The API also creates four opinions on one exact published Agent revision through the maintained Reader, Contributor, Reviewer and Publisher identities. One active opinion has an edit and sharing consent. Another remains private; the other two are withdrawn and removed by a moderator. Tenant administration explicitly enables one anonymous-audience sharing setting. One consented opinion is below the public-summary threshold, and existing publication bytes and ETags must remain unchanged.

Before the dump, the fixture requires one target, four current opinions, seven history rows and one sharing setting. Owner API records, ETags, local/shared totals and all four bounded SQL tables enter `state/rating-expectations.json` with mode `0600`. No tokens enter this capture. Its contents contribute to the existing backup binding digest. The file stays outside the operator input mount and public report.

Before API startup, bounded owner reads must match the protected capture, including all seven history rows. After startup, exact owner records, aggregates, moderator reads and rejected nonowner/cross-tenant reads must pass before the worker starts. The closed success report requires `ratingsRetained=true`.

After the dump, the source API records a withdrawal, a moderation decision, a consent change and a sharing change. The worker stays stopped. The older restore must exclude those changes. Source public snapshot bytes and ETags remain unchanged. Isolated tests reject missing/changed captures, API mismatches and `RESTORE_RATING_INVALID` findings. Scripted Go rows check finding classification and redaction; tests never corrupt real immutable history or disable database guards.

Before any restored runtime starts, every table must match the captured inventory, both tenant chains must start with an empty cursor and end complete, and both audit comparisons must match. Seven-row pages force continuation. Only the queued Job and pending internal/public outbox warnings are expected; any integrity failure, repairable drift, interrupted command or other warning fails. Verification must leave all table digests and source/receiver counters unchanged. Existing `restore.sh` corruption/repair cases and webhook wrong-key/partial-rotation tests remain separate.

The original database stays stopped. Only the API starts first, with an explicit restored-database DSN and `--no-deps`: retained reads, cross-tenant denial, public bytes/ETag, Search and an exactly replayed command must pass before worker startup. Search requires positive local and indexed results before the dump, not just equal empty responses afterward. Every fixture table must still match the captured inventory after read-only verification and after replay. Plugin revision reads create required audit evidence, so the fixture checks them afterward.

Replay compares exact response bytes, namespace ETags must be `"rev-1"`, and the public ETag must match the SHA-256 of the retained public bytes. Missing tags or equal decoded JSON with changed encoding cannot pass. Current custom roles, group/member relations, scoped assignments and a separately revoked direct edit grant must equal their captured rows. A member retains exact-namespace create authority, no tenant-wide create or revoked edit authority, and exact completed resource-command replay with one audit.

This uses the same bounded API control as writer recovery; no policy can be reconstructed by bootstrap. The restored worker then completes the queued Job and sends the retained event with a valid signature using the retained encryption key. No provisioning, mutating smoke, key rotation or source-database fallback can manufacture recovery.

The scenario's Compose commands share a ten-minute budget within the supervisor's twenty-minute workflow limit; cleanup has its own ninety-second budget. Individual commands, HTTP calls and waits are bounded. Each tenant has at most 200 verification pages; inventory scans allow at most 150 tables, 10,000 rows and 16 MiB of encoded data per table, with a 16 MiB archive ceiling. These are small-test safeguards, not service capacity limits. Reports include PostgreSQL/image/source identities, tenant/page/audit counts, dump/inventory/binding digests, runtime outcomes, closed failure phase/location and independently checked cleanup. Failed dump, restore, inventory, verification or audit comparison cannot advance to runtime startup.

On failure, inspect `status`, `failurePhase`, `failureLocation` and `cleanup` in the report printed by the target. Source line numbers refer to that run's recorded worktree, not necessarily a later checkout. A webhook may be absent, pending or waiting while the worker catches up; setup and restored-delivery checks poll those states for up to 45 seconds. A dead-letter result fails immediately, as does an HTTP, transport or response-contract error while reading delivery evidence.

Terminal delivery, failed observation and an unfinished wait have distinct call sites instead of being swallowed into a generic polling timeout. The report never includes the exception text, delivery body, endpoint secret or database dump. A passing rerun does not explain a previous failure; retain both reports and track unresolved actionable findings in the owning todo.

Service-free helper tests run through `go test -tags=functional ./test/functional` and `make verify`; for this profile alone, use `(cd test/functional && python3 -B -m unittest discover -s . -p 'test_*backup_restore.py' -v)`. They exercise rejected destinations, migration/inventory drift, all-tenant cursor binding, quiet dependencies, bounded dumps, delivery polling/failure distinctions and cleanup. They do not replace the real PostgreSQL round trip.

For deployment procedures and key prerequisites, use the [backup/restore runbook](../../documentation/src/content/docs/operations/backup-restore.mdx). This logical round trip does not qualify a deployment's WAL archive, PITR or HA routing.

## PostgreSQL backup and WAL preparation

For a complete historical Catalog recovery test, use `make test-postgres-pitr`, described below. The preparation target remains available as a smaller setup check.

Run this opt-in setup check with Docker Engine, Docker Compose and Python 3:

```sh
make test-postgres-pitr-prepare
```

The target uses the existing pinned PostgreSQL 18 image. It starts one private source and a tools container, with no published ports. It ignores workflow env files and inherited PostgreSQL or Compose selections. It builds no Catalog image and uses no retained database.

The check takes a plain-format `pg_basebackup` with streamed WAL and a SHA-256 backup manifest. It runs `pg_verifybackup`, creates a named restore point after the backup, and waits for that point's WAL segment in the archive. The manifest's WAL end must precede the named point. Archive retries accept identical bytes and reject conflicting bytes without overwriting the segment.

The tools container stages a separate copy with `recovery.signal`, an explicit target name and source timeline, and `recovery_target_action=pause`. It clears source connection and slot settings and disables archiving on the destination. It checks those settings through `postgres -C`, without starting the destination. A damaged copy must fail backup verification; the original backup must remain valid.

The source, backup and destination each use an invocation-owned, memory-backed volume capped at 512 MiB. The archive has a 128 MiB cap. Containers have memory and CPU limits. A persistent tools container keeps these volumes mounted between commands. Setup has a ten-minute deadline; individual commands allow at most 90 seconds. Archive polling allows 45 seconds. Cleanup has a separate 90-second budget.

Reports use `output/functional/run-<id>/report.json`. They contain source/image identities, backup and WAL digests, target/timeline information, completed checks and cleanup results. Raw backups and passwords stay outside reports. The report labels the result `preparation-only` and sets `historicalRecoveryTested` to `false`. This check does not restore Catalog data or prove that a database stops at the selected point.

Cleanup runs after success, failure and interruption. It removes all owned profile volumes and checks for remaining project-labelled containers, networks and volumes. Shared images and retained Compose services remain available. A failed inventory command or remaining resource fails cleanup.

Isolated decisions run through `make test-tooling-unit PYTHON_UNIT_DIRS=functional`. The ordinary functional wrapper also runs Make parsing controls. These checks do not start PostgreSQL. The explicit preparation target runs the real database and backup tools.

## PostgreSQL historical recovery

Run the complete drill with the ordinary packaged-functional prerequisites:

```sh
make test-postgres-pitr
```

The target builds the current Catalog and reuses the logical-restore fixture's publication, federation, webhook and two-tenant setup. It creates its own Compose project. Loopback-only ports and the fixture's exact fetch policy restrict requests to owned peers. Workflow env files cannot redirect the outer target or its nested Make invocation.

The test takes a verified physical base backup before normal API writes create change A. It stops ordinary writes and workers, captures the existing audit/read evidence, and inventories every table. It then creates a named PostgreSQL restore point. Further API writes create change B and change existing ratings. The source archives the required WAL and stops. No source volume or streaming connection supplies the restored databases.

Two separate destinations use the same base backup. The negative copy receives an archive with one required later WAL segment removed. The test refuses a bundled alternate copy. It requires a nonzero PostgreSQL exit and the specific unreachable-target error for that missing segment. API and worker remain stopped, and peer counters must remain unchanged.

The intact destination must reach an actual recovery pause, the exact target LSN, source timeline and cluster identity. SQL checks require A present and B absent. Only then does the test resume recovery onto a new timeline. It reconciles exact grants and reuses every tenant's restore-verifier pages, table digests and bound audit comparisons.

The API starts before the worker. Existing assertions verify current permissions, retained records, public bytes/ETag, command replay, personal data and older rating state. The worker then completes the retained Job and signed webhook delivery against owned peers. The original source stays stopped. No bootstrap or smoke command runs against the restored database.

Four memory-backed data volumes each have a 512 MiB cap. The intact and negative WAL archives each have a 128 MiB cap. A persistent tools container holds the restore inputs; stopping the original source releases its data. The supervisor allows 20 minutes, the recovery journey allows 15 minutes, and cleanup has a separate 90-second budget. No test result establishes production capacity or recovery objectives.

The report retains separate `backupRestore` and `pitrRecovery` sections under `output/functional/run-<id>/report.json`. Physical evidence names the backup manifest and physical byte count, without logical-dump fields. PITR evidence records the target/replayed LSNs, source/new timelines, required WAL digest, A/B results and negative-control outcome. The qualification is `postgres-point-in-time-recovery`. Passing requires current source/image identity, all existing recovery assertions, complete PITR evidence and independent cleanup.

`make test-backup-restore` and `make test-postgres-failover` keep their existing scopes. This opt-in local drill does not test a managed backup provider, backup scheduling, regional recovery or external key storage.

## Catalog Control MCP stdio checks

The ordinary local functional selection runs `TestMCPStdioAgainstCatalog`, `TestMCPStdioFrameRefusal` and `TestMCPStdioCancellationAndTermination` in `cmd/catalogctl`. They compile the real command and connect the official Go MCP SDK to its stdio transport. An owned memory-backed Catalog verifies four resource kinds, opaque continuation, exact release references and current namespace grants/revocation. Loopback controls verify input bounds, request cancellation, session reuse, SIGTERM drain, safe nonzero exit and EOF cleanup. A separate private-TLS fixture checks startup credential snapshots, serialized result limits and cancellation at the HTTP boundary.

`TestServeIndexedSelectionRefusalsAndEOF` connects the SDK to an owned loopback API. It checks absent, empty and nonempty indexed versions, exact query binding and retained digests. Fault controls assert specific denial, missing-record, upstream, redirect, malformed-response, credential-reflection and response-limit errors. Invalid arguments send no request. Redirects are not followed, a later valid call succeeds and idle EOF stops the server successfully.

Isolated units use injected HTTP transports for projections, refusals, cancellation, output limits and credential reflection. They verify complete-page refusal when one item is invalid and exact local-release selection without native instructions. These units never start a server or command. The six tools include local Explore facets and current-access pages, with private identity fields excluded and authorization-bound continuation. The independent client uses the separate `make test-interoperability-mcp-catalog` target. These tests do not execute discovered resources or change the existing Everything handoff.

## Remote CLI checks

The default-off profile's operator journey also invokes the shipped `catalogctl remote` capabilities, Search, record and Job commands from the API container. It checks paginated reads, release identity, cross-tenant rejection and retained cancelled/failed Jobs without database configuration. Focused Go tests cover TLS/private roots, redirects, cancellation, response limits, malformed JSON/problems and safe output. The client does not require a separate Compose service or Make target.

## PostgreSQL writer recovery

Run `make test-postgres-failover` from the repository root with Docker/Compose, Go, Python 3 and curl available through the existing Make tool settings. No running Catalog, Kubernetes cluster, external provider or caller-supplied DSN is needed. The target builds local Catalog and fixture images and runs `failover-planned` followed by `failover-abrupt`, each in a new owned stack. It stops on failure without retrying a scenario until green. Keep source unchanged during execution.

Both scenarios bootstrap migrations and exact roles once, then use `pg_basebackup` and asynchronous physical streaming between identical digest-pinned PostgreSQL 18 images. A generated replication credential belongs only to the fixture. API and worker use fixed role-specific multi-host DSNs with read/write selection and a two-second per-connection timeout. The overlay sets a 20-second API work deadline, 35-second write/shutdown bounds and 15-second worker leases; these are valid test settings, not new runtime defaults.

Standby startup, promotion, observation and cleanup are bounded. Polling accepts success only when the observation finishes strictly before its monotonic deadline, and sleeps never exceed the remaining budget. Unexpected observation errors fail immediately. This acceptance deadline does not preempt an in-flight subprocess or network call; those retain their own execution bounds.

Compose operations share a five-minute scenario budget, with individually bounded HTTP calls and waits. The existing supervisor bounds the complete build/workflow to 20 minutes and gives cleanup an independent 90-second budget.

The planned case sends PostgreSQL's fast-shutdown signal; the abrupt case kills only the primary container while a namespace command and source-sync Job are blocked by fixture-owned database locks. It verifies the committed baseline and pending/claimed work reached a WAL replay barrier before the fault. The retained policy control additionally compares custom roles, direct group membership, exact-namespace assignments and revoked edit history, checks current scoped permission decisions and byte-identical member-command replay with one audit. Both roles must remain live and report unready before promotion, then become ready and serve retained state through the same processes and DSNs.

Tests compare exact completed-command replay bytes and public manifest bytes, require namespace creation/replay/readback ETags to equal `"rev-1"`, and bind the baseline public ETag to the served bytes. Tenant denial, mutation audit, immutable outbox content and eventual acknowledgement remain independent checks. The abrupt case also verifies pending-command refusal, schema-owner repair, one retried mutation and completion of a later Job attempt. No API/worker restart, forced pool closure, host-clock change, failback or production HA controller is involved.

Each `output/functional/run-<id>/report.json` records its scenario, image/source identities, PostgreSQL versions, WAL barrier, process-identity digests, measured recovery interval, assertion outcomes and cleanup. `recoverySeconds` runs from initiating primary loss through both unchanged runtimes becoming ready, including deliberate no-writer observation and promotion. It excludes subsequent command repair, Job completion and final retained-state verification; it is not an end-to-end recovery objective. Failure retains a closed phase, never raw SQL, credentials or private responses.

Cleanup removes only that invocation's resources, including the profile-scoped standby, and verifies no owned container, volume or network remains. Missing phases, failed cleanup and incomplete evidence fail the target. Do not run the child script directly or use a broad prune to recover a run. The original qualification retained three consecutive complete runs as historical repeatability evidence.

A changed harness requires both current scenarios; repeat further only for a specific timing or reproducibility concern, not as automatic retries until green.

This verifies Catalog recovery for retained state, not zero-loss asynchronous replication or an operator's writer-routing/PITR configuration. No writes are deliberately raced beyond the known replication barrier to measure data loss. A retained pending command requires the existing [idempotency repair](../../documentation/src/content/docs/operations/idempotency-repair.mdx); reconnecting is not permission to release it automatically. The old writer stays stopped before promotion and repair, and the original request must finish before repair starts.

The core profile runs `feedback.py` through the packaged API: exact release context, own/admin visibility, stale ETags, address/reopen/edit/withdraw/remove, and durable text-free replay. The separate backup scenario retains both live and removed feedback across dump/restore. No external receiver, notification or feedback provider is involved.

## Generated API profile

`make test-api-generated` reuses this supervisor's build, smoke seeding, identity, operation accounting and cleanup with a separate `generated-api` profile. Install its isolated tools first through `make schemathesis-tools-install`. See [the pilot](../apigenerated/README.md) for eight selected operations, schema-based generation, nine calibration controls, native JUnit evidence and seed replay. The report compares both fixed selections and their omitted IDs against the independent Go/OpenAPI inventory. The loader cannot replace that inventory. The authoring section requires nine retained-state projections, 100 snapshots, four event checks and four SQL calibrations. See the pilot guide for exact limits and injected-write controls. The standard `test-functional` aggregate retains its existing default-off, webhook and OIDC profiles.

## Security scanner profiles

The `security-passive` and `security-active` profiles reuse this supervisor and smoke seed for [scoped ZAP checks](../security/README.md). Their internal network deliberately has no Internet route; Linux host drivers use owned container addresses instead of published ports. Existing profiles retain their port checks. Both profiles require scanner calibration, authenticated API/browser controls, complete native scan evidence and cleanup.

## Bounded Job wait evidence

The shared federation `wait_until` keeps its 45-second default and rejects success observed at or after the deadline. `wait_job` accepts the requested states, but immediately rejects any other terminal outcome. HTTP and response-contract failures propagate unchanged; they do not become polling timeouts.

On failure, the existing private state directory checkpoints only the last observed Job state, classified failure, attempt/limit, cancellation flag, elapsed milliseconds and, when available, the relative persisted scheduling delay. The supervisor validates this closed, bounded schema as `jobWaitFailure` before cleanup. It excludes IDs, results, error text, timestamps, tokens and credentials. Evidence capture is best effort and cannot replace the original failure or extend the wait. No extra API, lease disclosure, database query or worker probe is needed for this baseline. If recurrence requires lease evidence, inspect only the owned fixture before cleanup under a separately bounded diagnostic.

The helper regressions also run an actual failed child process, validate its checkpoint before real temporary-directory cleanup and preserve the original failure when a checkpoint path cannot be written. These process tests remain functional; polling and parser units use controlled clocks and inert clients.

Keep the concurrent-sync deduplication, same-Job completion and fresh conditional-304 assertions intact. A passing rerun does not establish the cause of an earlier timeout; retain the failed report and state the bounded investigation's result explicitly.

### Independent Catalogs in Minikube

Use `make test-minikube-peers` for two application-chart releases with separate PostgreSQL instances. The [owned cluster guide](../minikube/README.md#federation-between-two-independent-catalogs) describes its three-kind publication, synchronization, provenance, failure/recovery and process-replacement assertions. This is separate from the four-kind Compose/browser journey above.

`minikube_peers.py` reuses the existing publication and federation helpers. Its API exchanges use the compiled response-contract validator. Tokens enter through private stdin. Reports contain only fixed check labels and counts. `test_unit_minikube_peers.py` checks membership failures without a server, subprocess or database.

### Two-Catalog OIDC tutorial

Run `make test-tutorial-federation-oidc` separately to qualify the shipped two-Catalog Keycloak tutorial. It requires the selected local backend image, pinned Keycloak/PostgreSQL images, Docker Compose, OpenSSL and free loopback ports 8443, 8445 and 8447. It uses the tutorial's existing preparer, TLS frontend, configuration and protected service-token helper; it does not start browser UI services or contact a hosted issuer. The default private subnet is `172.29.246.0/24`; an overlap refuses before startup. The Python owner accepts explicit alternative subnet/frontend arguments when that default is unavailable.

The campaign independently reviews and publishes one Agent in A, synchronizes it into B with exact indexed provenance, exercises wrong audience/origin and current source-membership withdrawal, and retains the last complete snapshot after failed sync. Renewal externally reacquires the token and atomically publishes a complete new mounted generation. Source-test body success and its exact credential revision are mandatory; a fresh current Source ETag/revision binds the newly queued successful Job and complete current snapshot. Replacing credentials never revives the failed terminal Job.

Reports under `output/tutorial-oidc-federation/` contain selected input hashes, image identities, completed phases and cleanup outcome, without tokens or private response bodies. The owner removes only its unique project, volumes, network, generated frontend image and private inputs. Failed resource cleanup preserves private inputs and the exact owned stop command in the report. No retained development deployment is selected. Ordinary unit/local selections do not start this environment.
