# Two-profile relevance comparison

This follow-up retains the original 80-record corpus and adds 86 reviewed queries, equally divided between tuning and held-out evaluation. Each split includes task descriptions, short names, kind filters, partial/ambiguous requests, exact identifiers, four close-but-incompatible requests and two unrelated no-answer requests. Grade 1 denotes partial usefulness; grade 2 denotes a direct match.

Judgments follow the resource descriptions, not model output. Task families, including negative intents, are disjoint between splits. Previously inspected SQL-write and automatic-merge failures inform tuning, not fresh held-out evidence.

Exact identifiers are stable regression cases and may have appeared in the original experiment.

```sh
make search-relevance-check
make search-relevance-compare-install
make search-relevance-compare
```

The [shared profiles](../../../deployments/embeddings/profiles/README.md) select separately pinned BGE-small and MiniLM models. Both candidates see only tuning queries before selection. Each chooses its own cutoff from `experiment.json`; selection minimizes additional no-answer errors, then maximizes mean hybrid nDCG across both surfaces, then breaks ties by profile ID.

RRF remains fixed at 60. Write and hash `frozen.json` before sending any held-out text to the selected model. The other candidate receives no held-out inference.

Do not select a different candidate or cutoff after inspecting the report.

The selected candidate restarts with the same pinned files and must reproduce document embeddings before any held-out query is sent. It then runs held-out inference and exact PostgreSQL parity. Input fingerprints bracket fixture loading and are rechecked before freezing and completion, so an edit while inputs are being read cannot produce a successful mixed-source report.

The acceptance gate preserves the original nDCG/Recall/no-answer/exact-identity requirements and explicitly checks per-kind nDCG/Recall regressions. Execution success is distinct from quality: `recommendation: retain-lexical` records this experiment's original criteria, not a current runtime implementation veto. ADR-0011 separates those relevance findings from strict runtime correctness requirements.

An unchanged repeat checks numerical reproducibility, not new quality evidence or another opportunity to tune.

The corpus remains small, synthetic and English; repeated delivery-kind descriptions are correlated. Fresh queries are not a new independent corpus or evidence of generalization to new domains. Reports retain per-query rankings and grades for review.

No hosted calls, existing database, retained Compose project or development cluster are used. At most one model service runs at a time; inference is offline. Each stage records exact resource names before startup and removes its own containers/network on success, failure or interruption.

See the [parent harness guide](../README.md) for CPU/Docker prerequisites, resource limits and uncatchable-interruption cleanup.

Ignored output is under `output/search-relevance/comparison-v2-*/`. BGE reuses `output/search-relevance/model/`; MiniLM uses `output/search-relevance/models/minilm-l6-v1/`. Installation verifies reusable files and refuses changed files; inference verifies hashes again before and after use. Reports include profile/scorer/source hashes, server settings, selected threshold, tuning and held-out metrics, SQL parity, query timings and resource samples. No model cache is automatically deleted.
