# Packaged performance workloads

This directory measures the application through owned local Compose fixtures. It is not a generic PostgreSQL benchmark, production sizing tool or load generator for arbitrary external endpoints. The [performance guide](../../documentation/src/content/docs/guides/performance-and-monitoring.md) defines settings, phases, measurements and cleanup.

| Files | Responsibility |
| --- | --- |
| [runner.py](runner.py) | Invocation ownership, Compose lifecycle, observation deadlines, process continuity, evidence validation and abandoned-run cleanup. |
| [compose.yaml](compose.yaml) | Disposable workload-specific service inputs. |
| [main.go](main.go), [scenarios.go](scenarios.go) | Bounded HTTP workload client, response validation and measurements. |
| [scenarios.py](scenarios.py) | Fixture setup and scenario orchestration used by the supervisor. |
| [scale.py](scale.py), [scale.go](scale.go) | Closed scale cases, correctness oracles, capacity controls and restart checks. |
| [scalefixture/](scalefixture/README.md), [seed/](seed/README.md) | Deterministic metadata and governed, exact-role setup helper. |
| `*_test.go`, `test_*.py` | Client/report and supervisor safety, failure, sampling and qualification regressions; the Go wrapper includes Python tests. |

Use these Make entry points from the repository root:

| Target | Purpose |
| --- | --- |
| `make test-load-smoke` | Short owned Search diagnostic with supported duration/concurrency inputs. |
| `make test-load` | Local/indexed/public read phases and bounded mixed mutations. |
| `make test-public-delivery` | Separate 1,001-entry full/conditional public delivery trial with fixed settings. |
| `make test-scale PERF_ITEMS=1000` | Fresh 1k, 10k or 100k searchable-item fixture; 16 timed read cases, three Plugin preflight checks and mixed writes. Default size is 1k. |
| `make test-soak` | Finite one-hour mixed profile with bounded writes and two readers. |
| `make test-monitoring` | Owned collector/provisioning qualification using the same workload infrastructure. |
| `make perf-clean PERF_RUN_ID=<id>` | Explicit recovery of a validated abandoned run, not general Docker cleanup. |

The separately selected [PgBouncer qualification](../pgbouncer/README.md) reuses this directory's workload client for three short matched direct/pooled public-local Search pairs. Its fixed bounds and aggressive connection-churn settings, exact response equality, error checks, source/image identity and cleanup belong to `make test-pgbouncer`; results remain under `output/pgbouncer/`, separate from the packaged performance reports. Ordinary load/scale targets retain their direct PostgreSQL fixtures.

`go test -tags=functional ./test/performance/... -shuffle=on -count=1` runs service-free regressions, not a measured container trial. Run public load targets as single Make goals; they ignore developer env files and accept only their documented inputs. Reports under ignored `coverage/performance/` retain identity, measured phases, failures and cleanup; incomplete work must not become passing evidence.

Job polling allows 30 seconds on a monotonic clock. All polls for one Job share this allowance. Only `queued`, `running` and `waiting` continue polling. A `succeeded` response must complete before the deadline. The `failed`, `cancelled` and `dead_letter` states stop the workflow immediately. Malformed or unknown states fail explicitly; request errors retain their original classification.

Polling sleeps use at most the remaining allowance. The deadline does not cancel an active HTTP request; existing transport bounds still apply. Isolated regressions use controlled clocks and inert request doubles. Process regressions and the owned load trial verify the surrounding workflow separately.

Failed HTTP requests retain only status counts and two fixed cursor classifications. `400-stale-generation` identifies the exact stale-generation problem. `400-invalid-live-cursor` identifies the exact invalid-or-expired live-cursor problem; it does not distinguish signature, binding, shape or clock rejection. Every such response still fails the workload. Unknown problem details remain plain status counts; reports do not copy their text. The Python report validator accepts only the fixed vocabulary. Diagnose clock-related refusals with the original token and bounded observations; do not replace cursors or change host time to obtain a pass.

Never prune shared Docker resources or use retained development data as a benchmark fixture.

The scale seed uses a temporary container beside its owned PostgreSQL process, with the same backend image and exact database roles. It prepares two drafts at a time, joins preparation, then publishes both serially. The [seed guide](seed/README.md) defines its limits, ownership labels and fixed SQL-cost counters. The helper exits before timed readers start. Setup failures retain their own stage; `failureCommand` identifies only a failed source-connect or mixed-workload command.

Scale validation binds local/indexed, exact-identifier and continuation cases to the fixture's expected result population, as well as checking identities, counts, ordering, tenant scope and provenance shape. Mixed cases retain both populations. This independent check runs before accepting the baseline; later digest comparisons detect drift but cannot prove that a consistently wrong baseline was correct.

The fixture oracle remains outside timed request sampling; existing digest checks still run per sample.

Compare timing with serial matched runs and the same source, fixtures and resource limits. Query-count regressions and exact-role SQL plans live in [the database package](../../internal/platform/database/README.md), exposed through `make test-integration`, `make bench-search` and `make bench-search-plans`. The plan benchmark uses 45/100/1,000 local records and a separate 10,000-observation indexed fixture; it also compares warmed local reads with cached versus request-planned execution, including parameter encoding and hydration.

The plan benchmark uses a retained reader fixture and initializes current collection permissions before each probe. It reuses the scale collector's argument lists and validates their arity against PostgreSQL. Failed probes release their owned prepared statements after rollback, before the connection returns to its pool. A real PostgreSQL regression verifies failure followed by a successful different statement on the same connection.

It is not a full HTTP scale trial. `make bench` separately measures Go runtime metric collection/exposition. Constant round trips, successful short trials and profiler samples answer different questions and are not capacity promises.

Local Search and Explore use bound unnamed extended-protocol queries, avoiding named generic-plan reuse under normal auto planning without changing session settings. Local `page` plans include the combined exact count and bounded page selection; standalone local `count` plans are reference diagnostics, not a second runtime statement. Explore shares one matching pass across the exact total and requested facet buckets, materializing only requested value arrays.

Indexed count/page remain separate, and an exact zero indexed count skips page selection. The [measured baseline](../../documentation/src/content/docs/guides/performance-and-monitoring.md#measured-baseline) summarizes current qualification; keep source identities, results and limitations with local run evidence.

Indexed selection disables JIT only within its transaction. The plan benchmark compares default execution with transaction-local JIT disabled and records compiled function counts. Native scale probes apply the same indexed policy as runtime queries.

Failed correctness reads record the HTTP status and an allowlisted service code. Exact cursor refusals distinguish invalid live cursors from stale generations. Reports exclude arbitrary response details and private values.

The PostgreSQL fixture has one CPU, 512 MiB total memory and an explicit 256 MiB shared-memory ceiling for parallel queries; the shared-memory ceiling is not additional reserved memory. The report verifies and records the Docker limit. A failed seeded trial also records fixed error classifications from a bounded PostgreSQL log tail before cleanup.

No SQL or log text is retained; false or unavailable classifications do not prove that a failure never occurred.

Add `PERF_SQL_PLANS=on` to `make test-scale` for bounded local and indexed count/page diagnostics on that actual fixture, after HTTP measurements and before cleanup. The supervisor builds an integration-tagged test binary before measurement and runs only `TestPostgresScalePlans`; ordinary unit/integration runs skip it. Eighty-six custom/generic probes cover browse, common, rare, absent, exact-identifier and kind-filter queries in both repositories, plus local `Scale Local` first/third-page comparisons and the combined kind/tag facet query. Eight additional local page probes compare publisher ascending/descending on first and real third pages. They use the production ordering expression and exact text cursor key. Default count probes remain shared. Twenty probes measure the actual Overview statements. They cover Resources, releases, indexed identities, reviews and Sources, including sparse Namespace access. The existing query deadline and plan-output bounds remain unchanged.

The mixed scale phase binds its expected population through `mixedScaleItems`. Only the first seven generated indexed identities require two distinct sources. Other identities, ordinary load and soak retain their single-source checks. Mixed totals can grow only by the planned number of publications.

Before timed traffic, fourteen GET requests check twelve dashboard summaries, the sparse reader's first `/me` page and its Resource summary. Independent owner counts supply the expected populations. Each response must fit within 128 KiB. The dashboard checks make no commands or live-source queries. Plugin preflight checks select local, indexed and combined Plugin populations. All sixteen existing timed cases remain selected.

The third-page boundary comes from traversing two real 20-item pages in one read-only snapshot using the same score/sort/identity position as the HTTP cursor; it is not an OFFSET approximation. SQL deadlines are five seconds, continuation setup is bounded to 30 seconds, and the helper deadline is 390 seconds. Reports retain plan node statistics, not expressions, SQL, documents, cursor values or credentials.

Plan probes resolve the fixture's retained `local-reader` membership and initialize its collection policy in each read-only transaction. They use the same API role and permissions as the HTTP reader. Empty license and category filters remain explicit parameters. An isolated regression compares diagnostic argument counts with the current count, page and facet statements.

Preparation and argument-count drift fail collection; the helper compares separate arguments with the actual prepared parameter descriptor. Executed query failures remain visible as `query-failed`; collected plans do not turn a failed workload into a pass. Handled interruption skips diagnostics.

The report allows at most 512 KiB, 320 nodes per plan, 32 levels below the root and 128 children per node. Zero counters, empty relation names and empty child lists are omitted. An omitted counter means zero; every node and nonzero measurement remains present. Current indexed page plans reach 260 nodes. This diagnostic bound does not change application limits or performance budgets.

If collection fails, inspect `sqlPlans.failure`. Fixed codes distinguish helper failure, deadline, output size, report shape and plan bounds. Only an allowlisted stage escapes a failed helper; raw stdout, stderr and parser errors are not retained. A failed SQL-plan phase makes the overall trial incomplete even when HTTP measurements pass.

The binary is removed by normal and abandoned-run cleanup.

Scale checks reject empty or repeated Source IDs before measurement. Only seven exact fixture identifiers expect two distinct contributors. Isolated tests check all four kind populations at each supported size. The 100k population test does not qualify a deployed 100k Catalog.

## Matched Go benchmark comparison

```sh
make benchstat-tools-install
make bench
# Run again serially on the same otherwise idle machine after the proposed change.
make bench
make bench-compare BENCH_BASELINE=coverage/performance/bench-OLD/report.json BENCH_CANDIDATE=coverage/performance/bench-NEW/report.json
```

Installation explicitly acquires `golang.org/x/perf/cmd/benchstat` at `v0.0.0-20261009192801-be2c69fb417e` (BSD-3-Clause); comparison never downloads it. Both targets run alone and ignore deployment env files. `make clean` includes the private tool directory but preserves reports.

`bench` records ten samples each for runtime metric collection and exposition, with source identity, Go build settings, kernel/architecture, CPU model, affinity, visible cgroup CPU/memory limits and exact arguments. Version-2 successful reports with matching environments, matching benchmark `GOMAXPROCS` values and complete finite measurements are required; historical reports without metadata are rejected. Go omits the benchmark suffix when `GOMAXPROCS=1`; the comparison recognizes this form.

Different source revisions are allowed. The wrapper checks installed module identity and retains raw input/output and both source identities in an owned comparison directory.

Benchstat reports estimates, confidence intervals and statistical comparisons. It does not enforce a timing threshold, qualify capacity, prove an improvement or account for unobserved host contention, frequency scaling and inherited infrastructure limits. Use serial matched trials on the same idle host and interpret effect size alongside code/profile evidence.

Identical metadata is necessary, not proof that the host was equally idle. The [upstream guide](https://pkg.go.dev/golang.org/x/perf/cmd/benchstat) recommends at least ten samples; SQL plans, HTTP load and browser timings remain separate measurements.

Load and soak reuse the same exact API/worker/PostgreSQL identity, start-time, restart and OOM checks as scale. The mode-neutral `containerBaseline` is captured after intentional setup; samples and finalization must agree. Collection windows cover leading/trailing gaps as well as adjacent observations. Injected-clock tests reject readiness/drain success at or after the allowance; supervisor units prove continuity checks are wired into load and soak and preserve cleanup on failure. Native functional wrappers and actual owned trials qualify the real boundaries separately.

## Publisher-sort preparation

The scale preflight includes four `publisher-asc-first`, `publisher-asc-third`, `publisher-desc-first` and `publisher-desc-third` cases. Third-page cursors come from two real 20-item HTTP pages with the same order. The client requires the fixture-derived identities, totals, local tenant evidence and publisher name. A missing or reordered entry fails before timing. The default 16 timed cases remain unchanged.

Sparse-access checks traverse all 179 generated local results in the selected Namespace at 1k. Each order must match the independent fixture identifiers. Another tenant must receive no results. Existing exact-role tests cover multiple publisher names, both directions and stale-generation refusal after a publisher rename.

Fixture v2 remains unchanged. At 1k, `Scale Local` matches 897 generated releases across all four kinds. They share `Scale publisher`; publisher ties retain ascending display-name and ID ordering in both directions. This tests repeated local publisher lookup cost, not diverse publisher cardinalities or indexed sorting performance.

The publisher-sort experiment uses two serial 1k trials on unchanged source. Each trial compares default, publisher ascending and publisher descending on first and third pages. Use two readers, five seconds of warm-up and 30 seconds per phase. Retain existing request, SQL, resource and cleanup bounds. Require zero errors and exact results. Report timings without a new latency threshold. Run at most one additional 10k pair only if the 1k pair cannot resolve the suspected cost. No index or cache change is selected.

Run `make test-scale PERF_ITEMS=1000 PERF_SQL_PLANS=on` to qualify the preparation on an owned database. It executes the existing timed inventory, the new correctness preflight and 86 plans. These timings are qualification evidence, not the selected six-phase baseline/repeat experiment.

Run the matched publisher-sort workload twice, serially:

```sh
make test-scale PERF_ITEMS=1000 PERF_SCALE_WORKLOAD=publisher-sort PERF_SQL_PLANS=on
make test-scale PERF_ITEMS=1000 PERF_SCALE_WORKLOAD=publisher-sort PERF_SQL_PLANS=on
```

The explicit selector runs six timed read cases instead of the standard sixteen. It retains all 23 preflight cases, sparse-access checks, monitoring, mixed writes, 86 optional plans and owned cleanup. The selector fixes two readers, five-second warm-up and 30-second phases. It accepts 1k or 10k only. Other selectors or settings fail before setup. Reports record `scaleWorkload` to distinguish these trials from the standard profile.

Child report schema 3 records `successfulResponseBytes` separately for warm-up and measured phases. This counts encoded response-body bytes for successful requests only, excluding headers, transport overhead and failed responses. Historical schema-2 reports do not supply this counter.

Use parent monotonic `phaseWindows` when selecting metric samples. Include only samples collected wholly inside a phase. Samples include warm-up traffic; phase counter deltas are approximate and include observer requests. A wall-clock step disables wall-clock alignment, not these monotonic brackets. Do not add inclusive plan-node buffers or derive physical I/O from shared-block reads.

The recorded matched 1k trials measure a 4-8 ms mean overhead for publisher ordering. Both runs pass correctness, operational bounds and cleanup. Plans attribute the extra work to 897 cached publisher lookups. No production SQL or index change is selected.

## Public snapshot delivery

Run `make test-public-delivery` twice, serially, for a matched baseline and repeat. Each trial uses two closed-loop readers, five seconds of warm-up and sixty seconds of measurement per phase. Settings are fixed. Do not run other builds, tests or dashboard queries during measurement.

The owned fixture adds 997 small, independently reviewed public MCP Server Cards to the three smoke releases. The resulting manifest has exactly 1,001 unique entries, including the Registry entry. Existing scale/public cases retain their four-entry smoke manifest. This workload measures delivery, not ingestion or all resource-kind distributions.

Before timing, the fixture verifies activation, withdrawal, refusal of obsolete snapshot membership and rollback to an eligible large snapshot. The restored document and ETag must match exactly. It checks unknown hosts, authenticated-host access, cross-tenant refusal and audience-specific ETags for full and conditional reads. All metadata remains local. No resource endpoint is invoked.

The full `200` phase is `workload`; the empty-body `304` phase is `readScenarios.public-conditional`. Separate `phaseWindows` bind samples to each phase. Both child reports retain the large manifest's digest and entry count. `successfulResponseBytes` counts encoded bodies only; valid 304 samples contribute zero. The report also records exact manifest bytes/ETag, CPU, memory, database work and pool waits. Headers and network overhead are not included in body-byte totals.

Use the existing resource, monitoring, source-identity and cleanup checks. Public response reads remain bounded by the selected encoded size and the 16 MiB publication limit. Timings have no new latency threshold and do not establish offered-load capacity or a production SLA. Raw reports remain under ignored `coverage/performance/<run-id>/`.

The recorded matched 1,001-entry comparison selects no product change. Full-response means are 213-217 ms; conditional means are 207-213 ms. All four phases have zero request failures. Conditional requests save body bytes, but still load and validate the complete retained snapshot before the ETag check. Sampled API CPU remains around one core in both modes. This does not establish a reliable latency improvement, production capacity or a cache requirement.
