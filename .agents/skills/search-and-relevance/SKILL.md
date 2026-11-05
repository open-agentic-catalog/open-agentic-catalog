---
name: search-and-relevance
description: Investigate or change Open Agentic Catalog lexical/hybrid search, indexing, ranking profiles and relevance evaluation. Use for missing or incorrect results, search semantics or model-quality comparisons; latency-only investigations and routine test execution use their existing workflows.
---

# Search and relevance

Work from the repository root. Resolve instruction paths from that root. Read `AGENTS.md`, the Catalog ownership guide (`internal/catalog/README.md`), current implementation status (`CONTEXT.md`) and the affected hybrid-search procedure (`documentation/src/content/docs/operations/hybrid-search.mdx`). Preserve review-only, plan-only and unit-only scope. Use current code/contracts for runtime behavior; historical relevance experiments do not define whether an optional capability is implemented.

## Identify the result population and failure

Record a bounded synthetic reproduction: surface, actor/tenant or public audience, filters, submitted query, response tier/fallback, generation and continuation state. Keep private queries and evidence out of logs and public reports. Distinguish missing eligibility, stale derived state, ranking quality, cursor rejection, provider failure and latency before changing a scorer.

Trace the actual domain, PostgreSQL and publication paths. Authenticated Search includes eligible local/indexed results; Control Explore covers local releases; public Search/Explore read the active audience-scoped snapshot. Core Search can continue an unchanged bounded live window. Hybrid Search preserves its precise durable order before a distinct live tail. Live-only Search pages without durable queries or embeddings. Live results are not an embedding corpus. Use federation development (`.agents/skills/federation-development/SKILL.md`) only when source participation or synchronization changes.

Apply authorization and explicit filters before matching. Preserve exact-identity behavior, deterministic ordering and deduplication. Enforce selection bounds before hydration. Preserve full durable match totals and local facet counts. Live counts describe the returned page. Do not trade those guarantees for a higher quality score or silently truncate a successful hybrid result.

Cursor bindings include caller/query, generation and ranking/vector identity: an existing hybrid continuation cannot silently switch to lexical ranking during an outage. Successful empty hybrid results remain empty; fresh-query fallback has its explicit contract.

## Coordinate a runtime change

Reuse the existing scorer, repository projections, embedding client and index Jobs. Metadata projection stays bounded and excludes private/raw artifact content. Preserve one configured endpoint/model, public/control index separation, generation/profile/lease fencing and complete-generation activation. Inference stays outside transactions. Distinguish permanent item rejection, retryable provider outages and terminal authentication/invalid-response failures that require operator correction; new metadata remains available to lexical search while indexing catches up.

Derived vectors never become authoritative records. Use the indexing and recovery procedure (`documentation/src/content/docs/operations/hybrid-search.mdx#indexing-and-recovery`) for an authorized rebuild, preserving command replay and current Job controls. Do not delete retained data to force indexing or treat a model fixture being started as schema/runtime enablement.

Coordinate changed contracts with API contract changes (`.agents/skills/api-contract-changes/SKILL.md`), SQL with database changes (`.agents/skills/database-changes/SKILL.md`), and worker behavior with durable workflows (`.agents/skills/durable-workflows/SKILL.md`) only where affected. Configuration, UI-reported tier/fallback, public headers and deployment wiring must agree with actual behavior. A quality-only experiment need not change production configuration.

## Evaluate relevance without contaminating the result

Use the maintained comparison procedure (`test/search-relevance/comparison-v2/README.md`) and pinned model profiles (`deployments/embeddings/profiles/README.md`). Reuse actual Go scorer bridges and reviewed fixtures; do not create a second lexical implementation. Judge relevance from the task and resource descriptions independently of returned neighbors. Preserve explicit filters, exact identifiers, graded relevance, per-kind results and no-answer cases.

Keep tuning and held-out task families separate. Freeze and hash the selected candidate/parameters before held-out inference; do not choose a different model or cutoff after inspecting its results. An unchanged repeat measures reproducibility, not new quality evidence. Report the small synthetic corpus's limits, per-query regressions and nDCG/Recall/no-answer outcomes rather than only an aggregate score.

Execution success and a quality recommendation are separate. Read both, along with input/model/scorer fingerprints and cleanup status. A historical `retain-lexical` recommendation is not a veto on the implemented optional tier; integrity, filter/authorization, continuation and SQL parity remain strict. Latency/capacity measurement belongs to performance testing and optimization (`.agents/skills/performance-testing-and-optimization/SKILL.md`).

## Select verification for the changed boundary

Use post-code verification (`.agents/skills/post-code-verification/SKILL.md`) and extend existing assertions without duplicating complete journeys. `make embeddings-check` and `make search-relevance-check` cover service-free selections, which must not be assumed pure-unit. Explicit unit-only work uses the isolated/native selections from unit testing (`.agents/skills/unit-testing/SKILL.md`).

Use real PostgreSQL gates for SQL matching/counts and generation transitions, `make test-embeddings` for the HTTP adapter with its real local model, `make test-semantic` for composed indexing/search/UI/fallback, and the Helm skill's semantic scenario for affected cluster wiring. Relevance comparisons use `make search-relevance-compare-install` and `make search-relevance-compare` following their prerequisites; use these only for the selected experiment. Cached model acquisition and hosted inference are different scopes, and the maintained fixtures use owned offline inference.

Report correctness, relevance and performance evidence separately with source/profile identity, failures, unrun layers and fixture cleanup. Preserve reusable verified model caches and retained development environments. Update the owning runbook or measured record, without treating statement coverage or one model score as proof of search completeness.
