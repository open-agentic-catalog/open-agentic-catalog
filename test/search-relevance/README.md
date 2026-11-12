# Search relevance evaluation

This harness compares the unchanged Control and public lexical scorers with a pinned local BGE encoder and reciprocal rank fusion. It evaluates discovery quality, not API throughput, production isolation or semantic runtime support. Optional semantic retrieval has separate runtime qualification and configuration requirements.

See the [search design](../../documentation/src/content/docs/architecture/decisions/0011-search-and-embedding-tiers.md) and [performance guide](../../documentation/src/content/docs/guides/performance-and-monitoring.md).

The original commands below retain the fixed BGE experiment. The separate [two-profile comparison](comparison-v2/README.md) uses pinned BGE/MiniLM profiles, fresh judgments and tuning-only candidate selection through `make search-relevance-compare-install` and `make search-relevance-compare`. Shared acquisition, inference and SQL helpers remain in this harness.

These relevance experiments remain offline and separate from endpoint/runtime qualification. The implemented `make test-embeddings`, `make test-semantic` and `make test-minikube-semantic` cover the production HTTP adapter and enabled Compose/Helm runtime; see [embedding endpoint tests](../embeddings/README.md).

The separate [real API and LangGraph campaign](benchmark/README.md) uses a fresh local-release corpus and explicit `search-relevance-benchmark` targets. Its helper units remain model-free; the live campaign is opt-in.

## Run

From the repository root:

```sh
make search-relevance-check
make search-relevance-install
make search-relevance-evaluate
```

`check` requires Python 3.12+ and the repository Go toolchain, but no Docker, database, model or network access. Its Python regressions also run in the default Go suite. `install` explicitly downloads checksum-pinned model files with curl and pulls digest-pinned images; it does not install Python packages.

Repeating installation verifies/reuses matching files and rejects changed files rather than overwriting them.

`evaluate` requires a local Linux AMD64 Docker engine whose internal bridge is reachable from the host, installed images/model files, and available CPU/memory. TEI gets four CPUs and four GiB; the disposable PostgreSQL check gets two CPUs and one GiB. The CPU model is BAAI/bge-small-en-v1.5, English, float32, 384 dimensions, CLS pooling and normalized embeddings.

This is a tested candidate, not a multilingual or production sizing claim.

The model reads verified local files on an internal Docker network with no published port or inference-time Hub access. The runner registers the exact container address, rejects redirects and ignores proxy environment variables. No user-supplied endpoint, token or database DSN is accepted.

PostgreSQL has no network connection or published port; psql runs inside that disposable container. Its test-only trust authentication cannot reach a shared database.

Each invocation creates unique container/network names and temporary PostgreSQL storage. Normal completion, setup failure, SIGINT and SIGTERM clean only those resources. A cleanup error fails the run.

An uncatchable process/host failure can leave resources: inspect the exact names in the run's `resources.json`, written before startup, before removing them; never prune Docker or reset development Compose volumes. Existing app, UI and docs servers are not used or stopped.

Reports, the small Go bridge binary and vectors remain under ignored `output/search-relevance/run-*/`. The verified model cache at `output/search-relevance/model/` is deliberately reusable, not leftover database state. No automatic cache purge or report deletion occurs.

Review those exact paths before deleting retained local evidence. Evaluation exits successfully when the experiment completes, even if its quality recommendation is `retain-lexical`; infrastructure, validation and parity failures exit nonzero. Read `acceptance` and `recommendation`, not only the process exit status.

## Inputs and interpretation

| File | Purpose |
| --- | --- |
| [manifest.json](manifest.json) | Immutable model/tokenizer file hashes, TEI/PostgreSQL image digests, input profile, threshold grid, RRF constant and acceptance target. |
| [corpus.json](corpus.json) | 80 authored non-private records, 20 per resource kind; 60 local/20 indexed, with 40 public entries. Descriptive synthetic fixtures, not sampled customer traffic. |
| [queries.json](queries.json) | 20 tuning and 40 held-out scenarios, split by task family; relevance judgments and rationales, exact identifiers, kind restrictions and eight held-out no-answer requests. |
| [baseline/](baseline/README.md) | Calls the actual Go scorers. Runtime Search/Explore use the same public scoring function; no alternate keyword implementation is introduced. |
| [evaluation.py](evaluation.py) | Metadata projection, exact cosine, eligibility, deterministic ranking, RRF, metrics and tuning. |
| [runner.py](runner.py) | Explicit acquisition, owned CPU/SQL fixtures, frozen inputs, execution and cleanup. |
| [comparison.py](comparison.py) and [comparison-v2/](comparison-v2/README.md) | Two-profile tuning, candidate selection, frozen held-out qualification and comparison-specific inputs. |

Judgments are maintained review inputs: absent IDs mean irrelevant, grade 1 means useful and grade 2 means directly relevant. The initial corpus uses direct task matches; the metric tests also exercise grade 1. Each task family offers four delivery kinds, so this deliberately small fixture has correlated descriptions.

Relevance results are directional evidence, not broad real-world or multilingual validation. Review any new corpus/query judgments independently of model output; do not turn returned neighbors into expected answers.

Document embeddings use ordered name, description, sorted unique tags and representative queries. No identifiers, private context, feedback or raw artifacts enter the document template. Queries get the BGE retrieval prefix once; documents do not.

TEI automatic truncation is disabled. Startup checks model configuration and file hashes, then a real overlength request must return 422. Inputs must fit the pinned 512-token limit, including instructions and special tokens; changing truncation or field selection requires another versioned evaluation.

Both surfaces use independent lexical and semantic candidates. Control includes local/indexed records; public includes only the fixture's published local subset. Kind/source restrictions apply before ranking.

The corpus has unique canonical identities; duplicate-source rows are rejected rather than awarded extra votes. This is not a test of production source conflict resolution, authorization, RLS or snapshot lifecycle.

The only tuning parameter is a shared similarity cutoff from the checked-in grid; RRF uses the frozen constant 60. Tuning first minimizes additional no-answer false positives, then maximizes mean hybrid nDCG, preferring a stricter cutoff on ties. The 1.01 candidate deliberately means lexical-only.

`frozen.json` is written before held-out inference. Held-out results cannot select or change parameters. Fixture validation rejects normalized query-text overlap across splits, mislabeled exact identities and judgments that contradict explicit filters.

Each report includes fixture, harness and owning-scorer hashes, rechecked before completion. Unexpected model-cache files also fail validation; unrelated documentation edits do not invalidate an experiment.

Metrics use nDCG@10 with gain `2^grade - 1` and logarithmic discount, plus Recall@10 over all eligible judged-relevant identities. No-answer queries have no nDCG/Recall value and are counted separately. Per-kind summaries cover explicitly kind-filtered questions; their small denominators are reported.

The original experiment gate requires at least 0.05 absolute nDCG improvement on both surfaces, no aggregate Recall regression, no additional no-answer false positives and preserved exact-identity precedence. Preserve its output and review per-kind behavior; these historical quality criteria do not block the optional runtime implementation under ADR-0011. Numerical parity, fixture integrity and cleanup remain strict harness checks.

Reports include full ranked IDs and judgments for successes/regressions, batch token counts/timing, separate uncached single-query samples at concurrency two, startup time and one Docker resource sample. Container-cgroup peak memory is reported when available; otherwise it is explicitly null, not inferred from the point-in-time sample. These timings are not application end-to-end latency.

Repeated single/batch inference must agree numerically.

## Exact PostgreSQL parity

The SQL fixture uses the same float32 vectors, `vector(384)` and cosine distance, with no HNSW/IVFFlat index or Catalog migrations. It checks both populations, extra local/indexed restrictions, empty public scope, distances within `1e-5`, threshold membership, SQL-returned neighbor order outside numeric ties, deterministic ID ordering for exact ties, exact identical/orthogonal boundaries and full lexical-OR-semantic SQL identities and facet counts. It waits for final TCP readiness rather than the image's temporary initialization socket.

Numerical near-ties are not evidence that one retrieval algorithm is more relevant.

The experiment runs this small check even if relevance fails, to distinguish a quality result from a faulty vector baseline. SQL parity does not qualify production privileges, RLS, generation activation, Jobs or API continuation. Those belong to the separate runtime phase. Larger application latency/indexing tests should reuse the existing [performance harness](../performance/README.md), not expand this fixture into another load-testing framework.
