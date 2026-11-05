---
title: "ADR-0011: Core and optional semantic search tiers"
---

- Status: Accepted

## Question

Does a complete Catalog require semantic embeddings, who enables them, and may clients choose the ranking infrastructure for each request?

## Context

Catalog discovery must continue to work for exact identifiers, names, ordinary text, filters, facets, and deterministic browsing. Embeddings can improve task-oriented discovery when a query uses different words from the resource metadata, but they introduce a model endpoint, pgvector, dimensions, data disclosure, cost, latency, quality evaluation, and reindexing when a model changes.

ARD requires a Search result to carry a relevance score; it does not mandate embeddings, vector storage, or a particular ranking algorithm. A mandatory embedding provider would therefore add an avoidable runtime dependency without being required for interoperability.

The deployment boundary is already established by ADR-0004: Compose supplies a pgvector-capable image and creates the extension, the maintained Minikube development dependency profile reuses that capable image but creates only core `pgcrypto`, and production SRE supplies a pgvector-capable PostgreSQL 18 service only when semantic search is enabled.

## Decision

The service provides core lexical retrieval and optional hybrid lexical/semantic retrieval. Both use the existing Search and Explore APIs; the deployment selects the embedding provider and model, not each caller:

| Tier | Capability | Required dependency |
| --- | --- | --- |
| `core` | Exact identifier and name matching, PostgreSQL lexical search, structured filters, facets, deterministic List and browse, and bounded federation | PostgreSQL 18 with `pgcrypto` |
| `semantic` | Core plus versioned document and query embeddings, vector retrieval, and evaluated deterministic score fusion | Core dependencies, pgvector, and one configured embedding provider and model |

Core is a complete supported Catalog capability for local development, testing, and production. It does not depend on an embedding provider. Query eligibility and projection-integrity checks still apply. Semantic search is an optional deployment capability, not a separate product edition and not a requirement for ARD compatibility.

An enabled deployment configures one active embedding endpoint, model ID/version evidence, dimension and versioned SearchProfile, with bounded requests, concurrency, timeouts and scoped credentials/private trust. Deployment policy can exclude tenants from both metadata embedding and semantic query processing; no per-tenant model or new tenant-settings API is required. Production hosting, region and data-disclosure policy are deployment choices.

### Provider API and deployment boundary

Use a narrow OpenAI-compatible embeddings HTTP adapter, not a BGE/MiniLM allowlist or a provider SDK framework. The endpoint may be an independently operated local model service or a hosted API. Catalog sends text and the configured model identifier to one exact operator-selected URL, normally `/v1/embeddings`, and validates indexed float vectors. Private path prefixes are permitted; URL credentials, query strings and redirects are not.

Expected dimensions are required configuration; optional request-side dimension reduction is sent only for a qualified supporting model. Preserve bounded input/output, model-specific preprocessing and deterministic normalization; never pad, truncate or mix incompatible vectors. See the [OpenAI contract](https://developers.openai.com/api/reference/resources/embeddings/methods/create).

The portable adapter does not require `/models`, TEI `/info`, model weight hashes or a tokenizer API. Local TEI qualification verifies pinned files and loaded settings; hosted qualification records the available model/version evidence and operator-controlled profile identity without claiming that a model-name response proves immutable weights. Configured request/response aliases must be explicit. Other compatible endpoints are selectable within the documented protocol and operational bounds, but are not automatically quality-qualified by the local examples. No hosted account or external service is required for default tests.

The API embeds queries. Workers embed metadata asynchronously. PostgreSQL stores vectors and generation state. Model execution stays in a separate service with no Catalog database access. Multiple identical model replicas may sit behind a stable Service or load balancer and scale independently of API/worker replicas. No sticky sessions, per-query provider routing or automatic failover to a different model are selected.

Separate query and indexing concurrency bounds prevent unlimited work; their aggregate effect depends on replica count.

An embedding-profile change requires replacement-generation indexing even when dimensions match. Configure only one endpoint/model at a time, including upgrades. Queries can temporarily use lexical search while workers build the replacement vector generation. No concurrent old/new endpoint configuration, ensemble, reranker or automatic model selection is required.

Use a dedicated endpoint-bound adapter inside the hardened fetch boundary, with separate token/CA inputs, no proxy/redirect credential forwarding and no widening of federation permissions. HTTPS is the default. Explicit internal HTTP/private-address permission is scoped to this endpoint and documented as a plaintext risk; it never authorizes cloud metadata or other blocked destinations. Provider error bodies, query text, vectors and secrets stay out of ordinary diagnostics and telemetry.

Invalid schema, profile, or configuration fails startup. A temporary endpoint outage permits reported core degradation and does not cause a liveness restart loop. A new generation cannot activate without a successful provider/profile check.

### Optional local packaging

Use upstream digest-pinned [Text Embeddings Inference (TEI)](https://huggingface.co/docs/text-embeddings-inference/quick_tour) with separately acquired, verified model files; do not put weights, Python or a model server into Catalog/UI images. `compose.embeddings.yaml` is selected with `LOCAL_EMBEDDINGS=on`. Starting this local model is separate from enabling semantic search and applying owner-run optional database setup; startup never downloads weights, resets data or adds a core readiness dependency. The same pinned model inputs feed relevance and packaged tests.

The application Helm chart configures an endpoint. It mounts credentials and trust files into API and worker Pods only when semantic search is enabled. It does not install a model Deployment, companion chart, Operator or GPU infrastructure. The owned Minikube scenario deploys a separate TEI Deployment/Service with verified preloaded weights and tests multiple identical provider replicas. Operators can use their chosen compatible local service or remote endpoint. See [hybrid-search setup](../../operations/hybrid-search.mdx) for configuration, migrations and lifecycle.

### Search behavior

Ordinary clients submit the same Search request in either tier. They cannot choose an embedding provider, model, vector index, ranking weights, or core-only execution for a request. Deterministic automation uses exact identifiers, structured filters, List, or browse. Capabilities report the active tier, non-sensitive SearchProfile version, index generation, and degraded state.

When semantic search is enabled but the embedding provider is unavailable, a new query falls back to the versioned core lexical profile. The Catalog remains ready when core dependencies are healthy, while capabilities, metrics, and alerts report semantic degradation. Missing document embeddings remain queued for bounded retry. Signed cursors fail stale if the ranking generation changes. Continuation never switches algorithms or profiles between pages.

One bounded embedding represents each distinct eligible metadata projection: display name, description, tags and representative queries, with deterministic field order and query/document preprocessing. Name and description are trimmed; tags are sorted and deduplicated; representative-query order is preserved. Indexed observations use the identifier when display name is absent or empty; current published ARD entries require display names. There is no separate capability-text input. Do not chunk or crawl artifacts. Source credentials, artifact bodies, scripts, feedback, audit records, endpoint headers, user identity, tenant identifiers, roles, groups and unrelated policy/context are excluded as input fields.

Listing metadata and query text may themselves be private: using them at the configured endpoint requires deployment disclosure policy and must honor tenant opt-out, not a repeated end-user prompt. The field allowlist does not redact sensitive strings inside allowed fields. It also does not guarantee the provider's deletion or retention behavior. The [disclosure contract](../../operations/hybrid-search.mdx#data-sent-to-the-provider) defines current inputs and no-call boundaries.

Use only metadata visible to the corresponding search population. A public snapshot must not reuse an embedding derived from private fields of the same resource. Query instructions, pooling, normalization and token handling belong to the versioned input profile; server-side default prompts must not duplicate Catalog prefixes or apply a query-only instruction to documents. Qualify the loaded model/version evidence separately from its response name: a compatible embeddings server may accept a requested model name without selecting or verifying that model.

Embedding vectors are rebuildable derived projections, not authoritative Catalog records or portable evidence. Every vector is bound to the exact source document digest, provider, model ID, available model-version evidence, declared embedding-profile revision, dimension, input-template revision, and index generation. The profile revision does not assert access to a hosted provider's immutable weights.

A change to the model, dimension, or embedding input builds a replacement vector generation. Incompatible processes serve lexical results. Activate a compatible generation atomically only after integrity and policy checks and accounting for every eligible member. Accounting includes reused vectors and failed inputs. Rejected inputs remain keyword-only. No perfect-relevance score is required.

Rollback restores the previous configured profile and revalidates retained state, rebuilding when necessary. Ranking/cutoff-only changes reuse compatible vectors and invalidate continuations rather than rebuilding embeddings.

Token/CA rotation and an endpoint move serving the same qualified embedding profile are transport changes, not reasons to regenerate vectors. Keep credentials in deployment inputs, not stored generations. During a rolling configuration change, each API/worker must use the profile bound to its query/indexing generation; a mismatch permits lexical degradation or recoverable indexing, never mixed-profile comparison or writes.

Ranking behavior lives in a versioned SearchProfile. The core profile is a code-defined version with focused regression fixtures, not a database entity or dynamic profile-control subsystem. Exact authorized identifier matches take precedence with score 100 in the hybrid profile; retain the existing core scorers unchanged.

A semantic profile or material ranking change requires an evaluation report for Agents, MCP Servers, Skills, and Plugins. Record gains and limitations against the lexical baseline. Exact matches, filters, authorization and deterministic tie-breaking remain strict implementation checks; no universal model-quality score is required. Relevance never incorporates trust, rights, approval, popularity, freshness, commercial terms or execution authorization.

### Matching, ranking and scope

Retrieve lexical and semantic candidates independently; reranking lexical hits alone cannot recover missed synonyms. An eligible entry matches through the existing lexical criteria or a model-specific semantic similarity threshold. Apply this same matching predicate to Search and Explore within their respective populations, then order matches using evaluated reciprocal rank fusion (RRF). RRF combines rank positions rather than incomparable raw scores, but does not establish that an answer exists. Select the similarity threshold and fusion parameters with reviewed tuning judgments and verify no-answer behavior on held-out questions.

Authenticated Search covers eligible local releases and approved indexed observations. Its Explore remains local-only. Public ARD Search and Explore use only the active host/audience-scoped snapshot, with vectors bound to immutable content rather than modifying published evidence. Preserve provenance, local precedence and conflict resolution before fusion; duplicate observations must not accumulate source votes. Local and indexed results require coherent combined ranks, not independently numbered lists treated as globally comparable. Live federation remains bounded and separate: do not re-embed transient responses or assume remote scores are calibrated.

Count facets and totals over the full matched set. A result page or arbitrary nearest-neighbor shortlist is insufficient. Start with exact scoped vector comparisons in PostgreSQL and measure search, totals and facets together. Approximate indexes require demonstrated latency/resource need and filtered-recall evidence against that exact baseline; RLS does not prove approximate retrieval quality. Preserve bounded database work and deadlines rather than hydrating an entire tenant collection in Go. If complete semantic matching exceeds a measured work limit, report degradation or the documented limit rather than returning incomplete counts as exact.

Retain precise internal ranking positions separately from rounded 0-100 wire scores. Stable ties and cursors bind query, authorization scope, data generation and ranking profile; never rescale each page's best result to 100 or silently switch profiles during continuation. A display score is not a confidence percentage. Disabled mode preserves current core behavior, and text-free List/browse/Explore requires no model call.

### Indexing and qualification

Use PostgreSQL and existing durable Jobs for bounded, idempotent indexing with leases and retries. Provider calls occur outside interactive transactions. Newly published entries remain lexically searchable while embeddings are pending; publication does not wait for a model. Current tenant/source/release/snapshot eligibility gates every result immediately, even if obsolete-vector cleanup happens later. API and worker replicas share the database-backed generation and job state; no replica-local authoritative index or separate scheduler is added. Coordinate compatible model/profile configuration across replicas before atomic activation.

Keep core migration and startup paths usable without the vector extension. Semantic enablement requires explicit operator provisioning and exact API/worker privileges for its optional schema and extension operations. A development image containing pgvector does not prove either migration portability or runtime access. Before runtime qualification, compare exact PostgreSQL retrieval with the relevance runner's vectors, including filtered membership, threshold boundaries and full-set facets; a small offline relevance calculation alone does not qualify SQL behavior.

The maintained local runtime fixture uses TEI with the pinned MiniLM profile. BGE remains an optional comparison profile. Pin the image, model/tokenizer revision and preprocessing before execution. These English fixtures do not mandate a production model or establish multilingual quality. Use reviewed non-private examples across all four resource kinds, with separate tuning and held-out questions. Fake providers test integration behavior, not relevance quality.

Keep model downloads/setup and evaluation in explicit local tooling outside default unit/lint gates. An optional Compose model fixture can qualify the selected endpoint; Helm configures an operator-supplied endpoint without requiring a model Deployment. The Catalog binary/image contains no model runtime or Python toolchain. No separate vector database, Redis, broker, LLM reranker, query rewriting, fine-tuning or mandatory hosted subscription is selected.

Relevance evaluation records benefits and limitations. Relevance errors alone do not prevent runtime implementation or activation. Model comparison and tuning remain optional evaluation activities, separate from strict runtime correctness checks.

### Relevance limits

Local English evaluations show task-oriented retrieval gains and no-answer false positives. Similarity and rank fusion do not reliably enforce negation or capability restrictions. Tested errors include read-only versus write requests and labeling-only resources returned for image-generation requests. A relevance score does not prove that a resource can perform the requested task.

Retain profile-specific measurements and failed cases in local evaluation reports. Exact PostgreSQL comparisons must agree with reference vectors and matching sets; relevance quality remains a separate result. Do not recast failed judgments as passing or reuse inspected held-out questions as fresh validation. These limits do not impose a universal quality threshold for activation.

## Why this decision

- A useful and interoperable Catalog remains deployable with PostgreSQL alone.
- Users receive the best search capability enabled by their deployment without understanding search infrastructure.
- Provider neutrality preserves standalone operation and avoids binding the Catalog model to one vendor.
- Lexical fallback prevents an optional model outage from disabling discovery.
- Versioned, evaluated ranking makes search changes and relevance limitations inspectable without promising perfect answers.
- Versioned derived state prevents mixed-model comparisons; lexical search keeps discovery available during a single-model rebuild.

## Consequences

- Enabled semantic search requires compatible pgvector and one operational embedding provider configuration. Core deployments require neither.
- Compose includes pgvector and the maintained Minikube profile uses the same capable image without creating the extension, but neither needs an embedding model for the core profile. Service-free tests use deterministic fakes and owned HTTP fixtures; explicit semantic Compose/Minikube qualification uses a real pinned local model. Hosted qualification is separately authorized, never a default gate.
- Fusion parameters and similarity cutoffs need relevance evidence, not arbitrary fixed weights or per-page normalization.
- Provider failure degrades ranking quality, not Catalog availability, while core PostgreSQL remains healthy.
- Metrics cover embedding operations, latency, indexing backlog and degradation without logging query text or private embedding input. Operation counts include local validation and pre-dispatch cancellation; duration includes admission waiting. They do not measure provider requests or provider-only execution time. The adapter ignores provider token usage and does not collect cost. [ADR-0021](0021-logging-observability-and-audit.md#operational-measurements-and-provider-evidence) defines these measurement limits.
- Search-profile changes require an evaluation report and controlled activation even when no API schema changes.

## Rejected alternatives

### Require semantic search in every deployment

Rejected because ARD does not require it, core search remains useful, and the extra provider would unnecessarily complicate local, self-managed, and privacy-constrained deployments.

### Let each client choose core or semantic execution

Rejected because ranking consistency and infrastructure policy belong to the service. Exact and deterministic API operations already cover automation that must avoid relevance ranking.

### Select a mandatory embedding vendor

Rejected because the core service does not need one and production deployments may have different provider, region, residency, and cost requirements. The portable adapter and evidence contract are fixed; the enabled deployment selects the compatible endpoint and model.

### Activate fixed ranking weights without evaluation

Rejected because plausible-looking weights are not quality evidence and may regress exact discovery or one resource kind.

## Verification

The checks below are maintained acceptance requirements. Keep build-specific results under ignored `output/`, not in this architectural decision.

- Core search, filters, facets, List, browse, federation, and ARD conformance pass with no pgvector extension and no embedding configuration.
- Enabled startup rejects missing/incompatible pgvector, schema or model/dimension/profile configuration. Temporary provider outages produce semantic degradation rather than failing otherwise healthy core readiness; fallback never hides invalid enablement.
- Evaluation reports aggregate and per-kind nDCG/Recall, no-answer errors and limitations separately from functional results. Preserve original failed experiments. Exact-match precedence, authorization, filters, numerical retrieval and deterministic ordering remain strict correctness checks; model relevance errors are not an automatic implementation or activation veto.
- Provider timeout, outage, rate limit, dimensional mismatch, partial embedding coverage, model change, single-profile rebuild, atomic activation, rollback, and deletion are tested.
- A semantic outage produces a reported lexical fallback while core search remains usable; signed cursors fail stale on an unsafe generation change, and continuation never changes the selected ranking profile.
- Tests prove excluded fields and identities never enter provider requests, logs, metrics, traces, caches, or persisted embedding input.
- Capabilities report the active tier, SearchProfile, generation, coverage, and degradation honestly without exposing provider credentials or sensitive query data.
- Test full-set facets, exact and semantic-only matches, no-answer cutoffs, duplicate-source neutrality, precise pagination, tenant opt-out and generation activation across multiple replicas. Document measured limits using the existing scale harness; its synthetic terms are not relevance judgments.

## References

- [ARD Search and Explore](https://github.com/ards-project/ard-spec/blob/aa3e598bb7752a9175897823234311216acfa864/spec/ard.md#53-the-search-api): embedding processing is informative; Search/Explore share relevance criteria and facets cover the full matched set. This is the pinned v0.91 profile used by the service.
- [pgvector filtering and multitenancy](https://github.com/pgvector/pgvector/blob/efa08fda9ec485d80292d0487a77939c087dedcc/README.md#filtering) and [hybrid retrieval](https://github.com/pgvector/pgvector/blob/efa08fda9ec485d80292d0487a77939c087dedcc/README.md#hybrid-search): exact retrieval, approximate-filter recall and rank fusion.
- [TEI endpoint contract](https://huggingface.co/docs/text-embeddings-inference/quick_tour) and [BGE model card](https://huggingface.co/BAAI/bge-small-en-v1.5): local inference and candidate limits/preprocessing. Execution pins belong to the evaluation inputs.
