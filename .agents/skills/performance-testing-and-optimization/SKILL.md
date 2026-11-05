---
name: performance-testing-and-optimization
description: Measure and improve Open Agentic Catalog latency, throughput, allocations and PostgreSQL query/index costs using existing workloads, dashboards, traces and profiles. Use for performance baselines, bottleneck investigations or measured optimization; search relevance and general failure diagnosis use their owning workflows.
---

# Performance testing and optimization

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md`, the performance guide (`documentation/src/content/docs/guides/performance-and-monitoring.md`) and harness ownership rules (`test/performance/README.md`).

Check current implementation status (`CONTEXT.md`) and the performance guide before selecting a workload. Consult optional private measurements described in `AGENTS.md` when available. Keep harness reports in their documented ignored locations, including `coverage/performance/`. Use `output/` for supplemental evidence. Do not treat historical results as a current baseline.

Preserve measurement-only, review-only and unit-only scope. Measurement or diagnosis alone does not authorize changes to retained databases.

## Establish the question and baseline

Identify the operation, population, suspected bottleneck and desired improvement before choosing a workload. Record source revision/diff, binary/image, PostgreSQL version, profile, role/tenant context, data distribution, concurrency, warm-up, duration, resource limits and instrumentation. Select an owned disposable fixture for seed/load/DDL experiments; never redirect these harnesses to retained data or arbitrary endpoints. Existing authorization remains valid for its selected scope.

Reuse the smallest maintained measurement that answers the question:

| Question | Starting point |
| --- | --- |
| Production UI assets, request work and browser timings | `make ui-performance`; uninstrumented private production build, real local memory fixtures, explicit asset/work budgets and report-only durations. |
| Runtime metric collection/exposition overhead | `make bench` followed by matched `bench-compare`; focused Go instrumentation benchmarks, not a whole-service benchmark. |
| Search hydration, allocations or round trips | Existing exact-role query-count assertions and `make bench-search`; use focused Go profiles with the guide's private fixture/profile handling when needed. |
| PostgreSQL selection/count/facet planning | `make bench-search-plans`; inspect reported plan measurements, not setup-inclusive enclosing benchmark `ns/op`. |
| Short HTTP baseline or mixed read/write behavior | `make test-load-smoke` or `make test-load`, respectively. |
| Optional session-pooler overhead and connection churn | `make test-pgbouncer`; matched exact-role SQL reads and three alternating direct/pooled Search pairs with the existing workload client. Follow its fixed fixture/budget, identical-result/error assertions and report-only timings; this is not a saturation or production-sizing trial. |
| Public snapshot delivery and conditional-read cost | `make test-public-delivery`; fixed 1,001-entry manifest, separate 200/304 phases and lifecycle/access checks. Preserve the original four-entry benchmark. |
| Data-size effects and actual-fixture plans | `make test-scale PERF_ITEMS=1000 PERF_SQL_PLANS=on`; select larger supported sizes only for a justified question and budget. Plans run after timed readers stop. |
| Collector behavior/cost or long-lived growth | `make test-monitoring` or the fixed finite `make test-soak`; match workload controls for overhead comparisons. |

For UI measurements follow the performance guide (`documentation/src/content/docs/guides/performance-and-monitoring.md#ui-performance-budgets`). Keep cold/warm contexts, source/build identity and complete samples intact; no routing, coverage instrumentation or saved authentication state. `UI_PERFORMANCE_TIMEOUT_SCALE` adjusts harness deadlines only, not application behavior or byte/work budgets. Read the successful version-1 report after cleanup; failed or partial runs are not baselines. Keep UI measurements separate from ordinary `test-ui`, browser source coverage and deployment/load qualifications.

Run isolated measurement targets as single Make goals and comparable trials serially. Database benchmark `-dsn` alternatives require an explicitly owned empty disposable database. Hold workload, host/cache conditions and instrumentation constant across baseline and candidate trials; record variance and every intentional difference. Do not edit any repository file, including documentation, during source-bound trials. Build, seed, browse dashboards and run EXPLAIN separately from comparable timed phases.

These closed-loop readers report achieved throughput, not offered-rate capacity. A small pass does not qualify a larger dataset or an unrepresented resource kind.

Install benchstat only through `make benchstat-tools-install`. Compare successful version-2 reports with `make bench-compare BENCH_BASELINE=... BENCH_CANDIDATE=...`; the harness checks exact tool identity, matching observed environments and ten complete samples per benchmark. Different source revisions are allowed. Historical reports without metadata are not comparable. Treat statistical significance as evidence to investigate alongside effect size and repeatability, never proof of a code regression or an automatic timing gate.

## Follow the bottleneck through telemetry and code

Use the dashboard interpretation guide (`documentation/src/content/docs/guides/performance-and-monitoring.md#interpretation-and-troubleshooting`) and observability runbook (`documentation/src/content/docs/operations/observability.mdx`). Select the correct data source, deployment, role, instance and time window. Correlate HTTP latency/errors, pool acquisition/waits, SQL operation duration/counts, CPU/memory/GC, worker progress and database I/O/WAL/temp observations.

Inspect the actual dashboard queries (`deployments/monitoring/dashboards/catalog.json`) when a panel is ambiguous. Missing/unavailable samples are not healthy zeros; do not sum shared-database worker gauges across replicas. Bucket quantiles are estimates, and heap, container working set and RSS differ.

For requested visual review, use available browser tooling against the selected dashboard outside comparable timed phases; `test-monitoring` checks provisioning and query results, not browser rendering.

Trace the request from router/handler through domain and repository into actual SQL, hydration and encoding; include outbound calls or asynchronous attempts only where involved. Use sampled traces to locate expensive spans, then inspect their owning code. SQL spans omit query text and pool acquisition; map fixed operation classes to repository calls and use pool metrics separately. Job/outbox attempts link to creating traces rather than forming one continuous synchronous request. Missing sampled spans do not prove no work occurred.

Use the existing local trace viewer (`test/tracing/README.md`) only when needed, preserving private inputs and controlled sampling. Keep private profiles in ignored output; process profiles can include fixture setup. Do not add a public pprof endpoint for diagnosis.

## Diagnose PostgreSQL before proposing an index

Use the database package guidance (`internal/platform/database/README.md`) and existing plan fixtures with the exact runtime role, tenant context, RLS, parameter types, predicates, ordering and transaction snapshot. An owner/superuser plan can bypass the policy being measured. Examine realistic cardinality/selectivity, statistics freshness, first/continuation pages, common/rare/absent terms and relevant filters. Distinguish selection/count work from hydration and network cost; a bounded page or fixed query count can still scan expensive matching sets.

Read [PostgreSQL 18 EXPLAIN](https://www.postgresql.org/docs/18/using-explain.html) for estimates versus actual rows/loops, buffer work, sort/hash spills, planning and execution. Reuse bounded `EXPLAIN (ANALYZE, BUFFERS, TIMING OFF, FORMAT JSON)` diagnostics: ANALYZE executes the query, adds overhead and does not measure full client latency. Do not sum inclusive parent/child costs or equate buffer reads with physical disk I/O. Keep sensitive SQL, expressions and parameters out of shared reports. Local Search/Explore use unnamed parameter-bound execution; compare the real path and relevant custom/generic diagnostics without forcing a global planner setting. Maintain representative fixture statistics through the existing owner setup.

The maintained fixture already samples bounded database/WAL counters. If the selected deployment already provides [pg_stat_statements](https://www.postgresql.org/docs/18/pgstatstatements.html), use authorized bounded deltas with reset/window identity to find expensive query classes. It is not a repository prerequisite. Do not silently install/preload it, restart PostgreSQL, reset shared statistics or broaden application grants for diagnosis. Lock/activity evidence likewise uses the selected observer authority and must not disclose other tenants' SQL.

## Implement and qualify the smallest supported improvement

Choose the demonstrated cause: repeated queries, excessive hydration, query shape, index access, contention or application allocations. Compare existing indexes and constraint dependencies before adding one. Match key order, sort direction, expressions/operator classes and partial predicates to actual queries; justify covering columns and index method. Tenant-scoped reads and bounded cross-tenant worker selection have different access patterns. A sequential scan or zero observed index scans alone is not a defect or permission to drop an index.

For an index change, use database changes (`.agents/skills/database-changes/SKILL.md`): add a forward migration, review privileges only where authority changes, and preserve RLS, constraints, retained history and checksums. The migration runner (`internal/platform/database/migrate.go`) executes each migration in a transaction; PostgreSQL [CREATE INDEX CONCURRENTLY](https://www.postgresql.org/docs/18/sql-createindex.html) cannot run there. Use the supported migration/drain procedure and document locking, build/storage cost and rollback boundary; a separate online migration facility is not implied. Authoring/testing a migration does not authorize applying it to a retained deployment.

Change one coherent cause, then repeat matched trials and check read latency/work alongside write, synchronization, publication, storage/WAL and maintenance costs where affected. Preserve exact totals/facets, ranking, pagination, tenant denial, snapshot consistency and mutation evidence. Use existing real PostgreSQL tests and post-code verification (`.agents/skills/post-code-verification/SKILL.md`), extending meaningful missing assertions rather than duplicating journeys or freezing incidental full plan trees. Keep wall-clock observations separate from deterministic regression assertions; do not lower correctness or workload gates to pass. Search-quality changes additionally use search and relevance (`.agents/skills/search-and-relevance/SKILL.md`); telemetry changes use debugging and performance (`.agents/skills/debugging-and-performance/SKILL.md`).

Report the hypothesis, code/query path, measured cause, candidate change, comparable before/after results, correctness checks, tradeoffs and remaining uncertainty. Read the authoritative final report: incomplete workload or failed cleanup is not a pass. Do not average percentiles, invent exact percentiles from buckets or claim production capacity/SLA from a local fixture.

Clean only invocation-owned resources; `make perf-clean` is for a validated abandoned performance run, while database benchmarks use their own integration cleanup. Preserve reports in the harness's native ignored location. Reviewed findings may use the optional private records described in `AGENTS.md` after measurement finishes. Under unit-only scope, leave database/process/load qualification explicitly unrun.
