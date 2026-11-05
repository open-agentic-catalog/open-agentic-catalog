---
name: debugging-and-performance
description: Diagnose Open Agentic Catalog failures or intermittent behavior, and maintain service metrics, diagnostics and tracing. Use for root-cause or instrumentation work; measured performance baselines and optimization use performance-testing-and-optimization.
---

# Debugging and performance

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md` and the relevant troubleshooting path (`documentation/src/content/docs/operations/troubleshooting.md`). Preserve diagnosis-only, review-only and unit-only scope. Establish the failure or requested telemetry contract before changing code, settings or workloads. Use performance testing and optimization (`.agents/skills/performance-testing-and-optimization/SKILL.md`) for load measurements, PostgreSQL query/index tuning and measured code optimization; this skill owns general diagnosis and instrumentation maintenance.

## Establish identity and evidence

Record the source revision and working-tree changes, exact binary/image, profile, process role, relevant safe configuration differences, operation, time window and reproduction. Distinguish expected behavior from the observed failure using the owning contract and existing assertions. A later passing retry is evidence of intermittency, not an explanation of the earlier failure.

Use observability (`documentation/src/content/docs/operations/observability.mdx`) to select bounded logs, safe request IDs, fixed metrics and role-specific diagnostics. `catalogctl diagnostics` constructs a new role instance and may open dependencies; it does not inspect an existing process's memory or lifecycle. Its redacted configuration can still expose deployment topology. Select the environment explicitly and inspect evidence before sharing; never dump tokens, DSNs, private documents or unrestricted responses.

Separate startup/drain state, required PostgreSQL readiness and optional component degradation. Check snapshot availability before interpreting zero backlog, and distinguish missing series from measured zero. Correlate pool waits, SQL classes, application latency, worker progress and deployment resource signals only as needed. Do not add private/high-cardinality metric labels or make a collector/downstream outage into a liveness failure.

## Test a focused hypothesis

Trace actual callers and failure paths in the owning package. Find the smallest existing assertion or owned fixture that can distinguish a suspected cause from alternatives. Use deterministic inputs, injectable clocks and bounded synchronization; do not alter the host clock, disable history guards, relax authorization or weaken an assertion to reproduce or hide a failure.

Use unit testing (`.agents/skills/unit-testing/SKILL.md`) for isolated decisions and functional testing (`.agents/skills/functional-testing/SKILL.md`) for real transport, SQL or process behavior. Keep original failed evidence and assertions. When a fix is authorized, correct the owning boundary and add or extend the missing regression case without duplicating a complete journey. An SQL/schema fix uses database changes (`.agents/skills/database-changes/SKILL.md`); supported retained-state operations use recovery and repair (`.agents/skills/recovery-and-repair/SKILL.md`). Diagnosis alone does not apply migrations, repair records or rewrite evidence.

## Maintain instrumentation when requested

Read ADR-0021 (`documentation/src/content/docs/architecture/decisions/0021-logging-observability-and-audit.md`) for the implemented logging policy and operational limits. Review command stderr, HTTP fallbacks and owned goroutines as well as structured call sites. Never pass wrapped dependency errors or panic values through a formatter and call that redaction. Preserve error identity internally and classify safe fields at their output boundary; debug verbosity has the same privacy limits. Recovery is goroutine-local, and a worker panic must not become an ordinary retry or successful completion.

Trace the signal from the owning observer through metrics/diagnostics (`internal/platform/observability/README.md`) or tracing (`internal/platform/tracing/README.md`), its HTTP/export surface and actual dashboard/query consumer. Reuse fixed vocabularies and bounded collectors. Update affected schema/tests, runbook and shared monitoring assets together; changing a metric name, type, label or unit can break a query even when the endpoint still responds. Distinguish unavailable snapshots from measured zero, and do not sum replica gauges describing the same database as independent work.

Keep labels/span attributes allowlisted and free of tenant/resource IDs, raw paths, queries, SQL, secrets, documents and raw error text. Use domain tracing ports, process-owned providers and the dedicated endpoint-bound exporter. Preserve linked asynchronous attempts, replay's original creation cause, incoming-context validation and no trace-header propagation to federation/artifact/webhook destinations. Telemetry remains optional, bounded and independent of readiness; sampled traces and submitted-export counters are not audit or storage guarantees.

Extend existing tests for actual rendered signals, rejection of unsafe values, concurrency, disabled behavior and collector failure. Use `make test-monitoring` for affected collection/dashboard workflows, `make test-tracing` for real export/causality and the Helm skill's tracing scenario for cross-replica wiring. Select only affected layers through post-code verification; explicit unit-only scope uses injected collectors/transports and leaves real export unrun. Do not introduce a required collector or new telemetry framework for a missing signal.

## Verify and report the result

Use post-code verification (`.agents/skills/post-code-verification/SKILL.md`) after an authorized fix. Report the demonstrated cause, correction and regression evidence, or a precise unresolved hypothesis with the missing observation. Include comparable before/after measurements where applicable, cleanup, unrun boundaries and limitations. Update the owning engineering record and tracker without turning a local timing result into an SLA or suppressing unexplained intermittent failures.

For safe terminal projections, use `internal/safeerr` only for fixed reviewed messages and operation names. Preserve private causes internally; do not pass their text to `New` or `Wrap`. The command/HTTP diagnostic helpers sanitize owned output. Owned background panic recovery must return a fatal marker, join cleanup and remain fatal under simultaneous cancellation or another dependency failure; fatal runtime and dependency-owned goroutine limits remain explicit.

Backend/UI logging defaults to INFO structured text. Select startup `logLevel`/`logFormat` through the owning configuration path; backend overrides are `CATALOG_LOG_LEVEL`/`CATALOG_LOG_FORMAT`, and UI launchers use independent `CATALOG_UI_LOG_LEVEL`/`CATALOG_UI_LOG_FORMAT`. Restart after changes. Configuration reports describe selected inputs, not another process or an injected handler. Keep operator runtime logs on stderr and service logs on stdout; both formats retain the same privacy boundary.
