# Catalog Discovery & Agent Recommendation Benchmark

This benchmark evaluates search relevance and agent recommendation quality across Open Agentic Catalog (OAC) resources:

1. **Fixed-Query Retrieval Evaluation**: Compares Lexical baseline search against 4 pinned embedding profiles (`bge-small-v1`, `minilm-l6-v1`, `granite-278m-v1`, and `multilingual-e5-large-v1`) over a 160-item synthetic corpus (40 Agents, 40 MCP Servers, 40 Skills, 40 Plugins).
2. **LangGraph Agent Recommendation Evaluation**: Measures whether top-performing hybrid retrieval finalists help a local tool-calling reasoning agent (`qwen2.5:7b`) recommend appropriate, inspected releases and properly abstain when no matching capability exists.
3. **Operational Metrics**: Measures embedding computation time, API response latency, and pgvector storage requirements.

## Directory Structure

| File | Description |
| --- | --- |
| `manifest.json` | Benchmark configuration: model profiles, valid threshold grid, selection rule, and budgets. |
| `corpus.json` | 160 synthetic catalog resources (40 per kind), balanced across local/indexed and public/private scopes. |
| `queries.json` | 96 task scenarios split into 32 `tune` and 64 `test` queries with graded relevance judgments (0, 1, 2) and 16 agent benchmark tasks. |
| `benchmark.py` | Benchmark runner implementing serial profile tuning, finalist selection, held-out evaluation, agent episode simulation, and report generation. |
| `generate_fixtures.py` | Deterministic fixture generator for corpus and query splits. |
| `test_unit_benchmark.py` | Unit tests for fixture validation, split isolation, finalist selection, agent grounding checks, and end-to-end pipeline execution. |

## Running the Benchmark

### 1. Check Fixtures & Validation (No Docker / Model required)

```sh
python3 test/search-relevance/benchmark/benchmark.py check
python3 -m unittest discover -s test/search-relevance/benchmark -p "test_*.py"
```

### 2. Install Pinned Model Profiles

```sh
python3 test/search-relevance/benchmark/benchmark.py install
```

### 3. Run Benchmark Evaluation

```sh
python3 test/search-relevance/benchmark/benchmark.py evaluate
```

Results and reports will be saved to `output/search-relevance/benchmark/run-*/`:

- `report.json`: Complete machine-readable results
- `report.md`: Human-readable summary table
- `frozen.json`: Hashed parameters and finalist selections
- `lexical.json`: Baseline lexical scores
- `vectors.json`: Derived document and query embeddings
