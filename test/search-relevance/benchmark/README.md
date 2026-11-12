# Local discovery-quality campaign

This opt-in functional benchmark measures authenticated Control Search and the existing LangGraph assistant against one disposable Catalog. The [manual procedure](../../../documentation/src/content/docs/guides/discovery-quality.mdx) owns setup, interpretation and reviewed results.

```sh
make search-relevance-benchmark-check
make search-relevance-benchmark-install
make search-relevance-benchmark BENCHMARK_MODE=smoke
make search-relevance-benchmark
```

Run each target separately from the repository root. Installation uses the existing locked LangGraph environment and pinned model/image installers. It downloads several GiB. Evaluation verifies cached files, then builds the application and fixture commands. It performs no inference-time model downloads or hosted model calls. The full campaign needs a Linux AMD64 Docker engine, 16 GiB Docker memory, 16 GiB free disk and the repository Go/Python toolchains. Use the repository's documented pinned uv for installation.

The service-free check uses native helper tests. `make test-tooling-unit` also collects those units. Graph/callback units use `make test-langgraph-unit` and its installed framework environment. Neither unit selection starts a server, database or model. The smoke selection starts real PostgreSQL/API/worker/BGE services, checks tuning parity and runs a scripted graph over actual SDK HTTP calls. It does not run held-out model-quality evaluation.

The full selection tunes all four embedding profiles before freezing cutoffs and agent finalists. It measures lexical plus every embedding arm through the real API. It then runs one pinned local Qwen model through the unchanged graph for lexical and up to two tuning-selected finalists. No-answer, partial-only and exact controls retain separate denominators. A failed quality criterion can correctly recommend lexical search; failed execution or cleanup cannot support a recommendation.

The corpus contains 160 synthetic records, 40 per kind, grouped into 40 correlated capability families. All primary records are local tenant-visible releases. The fixed reader has only their namespace grant. An excluded namespace, another tenant, a grantless identity and an unreleased Resource provide disclosure controls. Representations reuse existing native test assets; this tests metadata discovery, not resource execution or actual capability delivery. Questions and judgments are reviewed fixture inputs, not model-generated truth.

| Source | Ownership |
| --- | --- |
| `manifest.json` | Versioned roster, split/episode selection, selection rule and finite budgets. |
| `corpus.json`, `queries.json`, `generate_fixtures.py` | Reviewed metadata, independently phrased tasks, family split and labels. Generation is explicit, never part of measurement. |
| `protocol.py`, `test_unit_benchmark.py` | Pure validation, metrics, selection, episode accounting and isolated regressions. |
| `benchmark.py` | Explicit install/check/run selection, freeze, actual measurements and reports. |
| `runtime.py`, `runtime.compose.yaml` | Invocation-owned Compose lifecycle, exact runtime settings, identity mapping and scoped cleanup. |
| `seed/` | Native functional fixture command. Seeds only an empty owned database through exact-role governance and independent review. |
| `agent_benchmark.py` | Actual SDK and standard callback observations around the existing graph. Labels enter scoring after graph execution. |
| `../../langgraph/test_unit_benchmark.py` | Framework-specific observer/graph tests with injected SDK operations. |

Reports are written to ignored `output/search-relevance/benchmark/campaign-*/`. `report.json` contains execution, retrieval, agent and resource evidence. `frozen.json` records the pre-held-out selection. The inventory binds fixture keys to real release UUIDs. Per-query responses, episode files, vectors, setup identities and cleanup evidence remain local. Reports retain no hidden reasoning. Final recommendation reasons are bounded output from the normal example.

Normal completion, setup failure, SIGINT and SIGTERM remove only the random campaign project, its database volume and unique application image. Verified model caches and retained development services remain. An uncatchable host/process failure can leave resources. Read that invocation's `resources.json` before cleanup; use only its recorded project and image. Never prune Docker or remove a development volume.

This campaign preserves the meaning and inputs of the original offline experiments. Their temporary-table parity checks do not substitute for this campaign's actual Catalog/API/index evidence. Public, indexed-source, multilingual and hosted-provider comparisons remain separate selections.
