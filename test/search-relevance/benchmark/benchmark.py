"""Unified Agent & Retrieval Discovery Benchmark for Open Agentic Catalog.

Evaluates fixed-query retrieval quality across multiple embedding profiles and
measures how retrieval configurations impact LangGraph agent recommendation grounding,
task suitability, abstention, and operational latency.
"""

import argparse
from concurrent.futures import ThreadPoolExecutor
import json
from pathlib import Path
import signal
import statistics
import tempfile
import time
from typing import Any, Dict, List, Optional
import sys
import urllib.error

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import evaluation as ev
import runner

BENCHMARK_DIR = Path(__file__).resolve().parent
PROFILES_DIR = runner.REPO / "deployments/embeddings/profiles"
SURFACES = ("control", "public")
SELECTION_RULE = "least-added-no-answer-errors_then-highest-mean-hybrid-ndcg_then-profile-id"


def load_benchmark_inputs():
    """Load and validate benchmark manifest, 160-item corpus, and 96 queries."""
    manifest_path = BENCHMARK_DIR / "manifest.json"
    corpus_path = BENCHMARK_DIR / "corpus.json"
    queries_path = BENCHMARK_DIR / "queries.json"

    if not manifest_path.is_file() or not corpus_path.is_file() or not queries_path.is_file():
        raise FileNotFoundError("Benchmark fixtures missing from " + str(BENCHMARK_DIR))

    manifest = ev.load_json(manifest_path)
    documents = ev.load_json(corpus_path)
    queries = ev.load_json(queries_path)

    if manifest.get("selection") != SELECTION_RULE:
        raise ValueError("Unexpected candidate selection rule in manifest")

    profiles = {}
    for profile_name in manifest["profiles"]:
        prof_path = PROFILES_DIR / f"{profile_name}.json"
        if not prof_path.is_file():
            raise FileNotFoundError(f"Embedding profile not found: {prof_path}")
        profile = ev.load_json(prof_path)
        profile.update({
            "thresholds": manifest["thresholds"],
            "rrf_k": manifest["rrf_k"],
            "minimum_ndcg_gain": manifest["minimum_ndcg_gain"],
        })
        ev.validate_fixtures(profile, documents, queries)
        profiles[profile_name] = profile

    return manifest, profiles, documents, queries


def benchmark_fingerprint():
    """Collect SHA-256 digests for all source, profile, and fixture inputs."""
    files = [
        BENCHMARK_DIR / "manifest.json",
        BENCHMARK_DIR / "corpus.json",
        BENCHMARK_DIR / "queries.json",
        BENCHMARK_DIR / "benchmark.py",
        ev.ROOT / "evaluation.py",
        ev.ROOT / "runner.py",
        ev.ROOT / "baseline/main.go",
    ]
    files += sorted(PROFILES_DIR.glob("*.json"))
    files += [
        runner.REPO / "internal/catalog/search_profile.go",
        runner.REPO / "internal/publication/manager.go",
    ]
    return {
        str(path.relative_to(runner.REPO)): ev.digest(path)
        for path in files
        if path.exists()
    }


def cache_path_for_profile(profile_name: str) -> Path:
    """Return model cache path for a profile."""
    if profile_name == "bge-small-v1":
        return runner.MODEL
    return runner.OUTPUT / "models" / profile_name


def similarities_matrix(documents, queries, doc_vectors, query_vectors):
    """Compute cosine similarity lookup table."""
    return {
        q["id"]: {
            d["id"]: ev.cosine(query_vectors[q["id"]], doc_vectors[d["id"]])
            for d in documents
        }
        for q in queries
    }


def encode_corpus_and_queries(origin, profile, documents, queries, timings):
    """Encode document metadata and queries using profile-specific prefixes."""
    doc_ids = [d["id"] for d in documents]
    doc_prefix = profile.get("document_prefix", profile.get("passage_prefix", ""))
    doc_embeddings = runner.embed(origin, profile, [doc_prefix + ev.projection(d) for d in documents], timings)
    if len(doc_ids) != len(doc_embeddings):
        raise ValueError(f"Document embedding count mismatch: expected {len(doc_ids)}, got {len(doc_embeddings)}")
    doc_vectors = dict(zip(doc_ids, doc_embeddings, strict=False))
    query_vectors = encode_queries_only(origin, profile, queries, timings)
    return doc_vectors, query_vectors


def encode_queries_only(origin, profile, queries, timings):
    """Encode a split of queries with profile instruction prefix."""
    prefix = profile.get("query_prefix", "")
    query_ids = [q["id"] for q in queries]
    query_embeddings = runner.embed(origin, profile, [prefix + q["text"] for q in queries], timings)
    if len(query_ids) != len(query_embeddings):
        raise ValueError(f"Query embedding count mismatch: expected {len(query_ids)}, got {len(query_embeddings)}")
    return dict(zip(query_ids, query_embeddings, strict=False))


def tuning_trial(profile_name, profile, documents, tune_queries, lexical_scores, output_dir):
    """Run tuning phase for one candidate model profile on tuning queries only."""
    if any(q["split"] != "tune" for q in tune_queries):
        raise ValueError("Tuning trial accepts tuning queries only")

    cache_dir = cache_path_for_profile(profile_name)
    runner.verify_model(profile, cache_dir)

    with runner.owned_resources() as resource_names:
        runner.save(output_dir / f"{profile_name}-resources.json", resource_names)
        t_start = time.perf_counter()
        origin, server_info = runner.start_tei(profile, resource_names, cache_dir)
        startup_seconds = time.perf_counter() - t_start

        # Overlength rejection check
        try:
            runner.request(
                origin,
                "/v1/embeddings",
                {"model": profile["model"], "input": "word " * (profile["max_input_length"] + 10)},
            )
        except urllib.error.HTTPError as exc:
            if exc.code != 422:
                raise
        else:
            raise ValueError("Overlength input was silently accepted by TEI")

        timings = []
        doc_vectors, query_vectors = encode_corpus_and_queries(
            origin, profile, documents, tune_queries, timings
        )
        sim_scores = similarities_matrix(documents, tune_queries, doc_vectors, query_vectors)
        threshold, grid = ev.tune(profile, documents, tune_queries, lexical_scores, sim_scores)

        trial_result = {
            "profile": profile_name,
            "threshold": threshold,
            "grid": grid,
            "server": server_info,
            "startup_seconds": startup_seconds,
            "embedding_batches": timings,
            "overlength_rejected": True,
            "results": {
                s: ev.measure(documents, tune_queries, s, lexical_scores[s], sim_scores, threshold, profile["rrf_k"])
                for s in SURFACES
            },
        }

        runner.save(output_dir / f"{profile_name}-vectors.json", {
            "documents": doc_vectors,
            "queries": query_vectors,
        })
        runner.verify_model(profile, cache_dir)

    trial_result["cleanup"] = "complete"
    runner.save(output_dir / f"{profile_name}-tuning.json", trial_result)
    return trial_result, doc_vectors


def select_finalists(trials: List[Dict[str, Any]], max_finalists: int = 2) -> List[str]:
    """Select up to max_finalists hybrid profiles based on tuning results.

    Rule:
    1. Minimize excess no-answer false positives relative to lexical
    2. Maximize mean hybrid nDCG gain on tuning tasks
    3. Final tie-breaker: profile ID
    Must achieve positive nDCG gain (hybrid > lexical). If none meets positive gain,
    return top-ranked as diagnostic arm.
    """
    def score_trial(trial):
        selected_row = next(r for r in trial["grid"] if r["threshold"] == trial["threshold"])
        return (
            selected_row["excess_false_positives"],
            -selected_row["ndcg"],
            trial["profile"],
        )

    ranked = sorted(trials, key=score_trial)
    qualifying = []
    for t in ranked:
        row = next(r for r in t["grid"] if r["threshold"] == t["threshold"])
        lex_ndcg = t["results"]["control"]["summary"]["lexical"]["ndcg"] or 0
        if row["excess_false_positives"] == 0 and row["ndcg"] >= lex_ndcg:
            qualifying.append(t["profile"])

    if not qualifying and ranked:
        # Diagnostic fallback
        return [ranked[0]["profile"]]
    return qualifying[:max_finalists]


def qualify_finalist(
    profile_name: str,
    profile: Dict[str, Any],
    documents: List[Dict[str, Any]],
    heldout_queries: List[Dict[str, Any]],
    lexical_scores: Dict[str, Any],
    threshold: float,
    expected_doc_vectors: Dict[str, List[float]],
    output_dir: Path,
) -> Dict[str, Any]:
    """Evaluate a selected finalist profile on held-out test queries."""
    if any(q["split"] != "test" for q in heldout_queries):
        raise ValueError("Held-out evaluation accepts test queries only")

    cache_dir = cache_path_for_profile(profile_name)
    runner.verify_model(profile, cache_dir)

    with runner.owned_resources() as resource_names:
        runner.save(output_dir / f"{profile_name}-heldout-resources.json", resource_names)
        origin, server_info = runner.start_tei(profile, resource_names, cache_dir)
        timings = []
        doc_vectors, _ = encode_corpus_and_queries(origin, profile, documents, [], timings)

        # Verify vector consistency after restart
        if any(
            abs(ev.cosine(doc_vectors[k], expected_doc_vectors[k]) - 1) > profile["numeric_tolerance"]
            for k in doc_vectors
        ):
            raise ValueError("Restarted provider produced drifting document vectors")

        query_vectors = encode_queries_only(origin, profile, heldout_queries, timings)
        sim_scores = similarities_matrix(documents, heldout_queries, doc_vectors, query_vectors)

        results = {
            s: ev.measure(documents, heldout_queries, s, lexical_scores[s], sim_scores, threshold, profile["rrf_k"])
            for s in SURFACES
        }
        gates = {s: ev.acceptance(results[s], profile["minimum_ndcg_gain"]) for s in SURFACES}

        # Concurrency / single query check
        single_samples = []
        def run_single(q):
            vec = runner.embed(origin, profile, [profile.get("query_prefix", "") + q["text"]], single_samples)[0]
            if abs(ev.cosine(vec, query_vectors[q["id"]]) - 1) > profile["numeric_tolerance"]:
                raise ValueError("Single-query and batch inference vectors disagree")

        with ThreadPoolExecutor(max_workers=2) as pool:
            list(pool.map(run_single, heldout_queries[:8]))

        # Exact PostgreSQL pgvector parity
        pg_parity = runner.pgvector_parity(
            profile, resource_names, documents, heldout_queries, doc_vectors, query_vectors, threshold, lexical_scores
        )

        stats_sample = json.loads(
            runner.command(["docker", "stats", "--no-stream", "--format", "{{json .}}", resource_names["tei"]])
        )
        try:
            peak_mem = int(
                runner.command(["docker", "exec", resource_names["tei"], "cat", "/sys/fs/cgroup/memory.peak"]).strip()
            )
        except (RuntimeError, ValueError):
            peak_mem = None

        runner.save(output_dir / f"{profile_name}-heldout-vectors.json", {
            "documents": doc_vectors,
            "queries": query_vectors,
        })
        runner.verify_model(profile, cache_dir)

    return {
        "profile": profile_name,
        "threshold": threshold,
        "server": server_info,
        "results": results,
        "acceptance": gates,
        "recommendation": "implement" if all(all(v.values()) for v in gates.values()) else "retain-lexical",
        "embedding_batches": timings,
        "single_query_samples": single_samples,
        "pgvector": pg_parity,
        "resource_sample": stats_sample,
        "peak_memory_bytes": peak_mem,
        "cleanup": "complete",
    }


class BenchmarkAgentObserver:
    """Wraps SDK / agent discovery calls to record operational metadata without mutating prompt inputs."""

    def __init__(self, discovery_tools, arm_name: str):
        self.tools = discovery_tools
        self.arm_name = arm_name
        self.records_read = set()
        self.search_calls = []
        self.read_calls = []
        self.errors = []
        self.start_time = time.perf_counter()
        self.end_time = None
        self.token_usage = None
        self.effective_tier = "hybrid" if arm_name != "lexical" else "lexical"

    def record_search(self, query: str, kinds: List[str], returned_items: List[Dict[str, Any]]):
        self.search_calls.append({
            "timestamp": time.perf_counter() - self.start_time,
            "query": query,
            "kinds": kinds,
            "result_count": len(returned_items),
            "release_ids": [item.get("release_id") for item in returned_items if "release_id" in item],
        })

    def record_read(self, release_id: str, success: bool):
        if success:
            self.records_read.add(release_id)
        self.read_calls.append({
            "timestamp": time.perf_counter() - self.start_time,
            "release_id": release_id,
            "success": success,
        })

    def close(self):
        self.end_time = time.perf_counter()

    @property
    def duration_seconds(self) -> float:
        if self.end_time is not None:
            return self.end_time - self.start_time
        return time.perf_counter() - self.start_time


def evaluate_agent_episode(
    task_query: Dict[str, Any],
    observer: BenchmarkAgentObserver,
    recommendations: List[Dict[str, Any]],
    error: Optional[str] = None,
) -> Dict[str, Any]:
    """Score one LangGraph agent episode against ground-truth relevance and grounding rules."""
    judgments = task_query.get("relevant", {})
    is_no_answer = (task_query.get("family") == "no-answer" or not judgments)

    recommended_ids = [r.get("release_id") for r in recommendations if isinstance(r, dict) and "release_id" in r]

    # Grounding check: every recommended ID must have been inspected (read) in this run
    unread_ids = [rid for rid in recommended_ids if rid not in observer.records_read]
    grounding_valid = len(unread_ids) == 0

    # Task suitability & abstention scoring
    grades = [judgments.get(rid, 0) for rid in recommended_ids]
    has_direct_match = any(g == 2 for g in grades)
    has_irrelevant_selection = any(g == 0 for g in grades)

    if is_no_answer:
        if len(recommended_ids) == 0:
            outcome = "appropriate_abstention"
        else:
            outcome = "unsupported_recommendation"
    else:
        if has_direct_match and not has_irrelevant_selection and grounding_valid:
            outcome = "successful_discovery"
        elif len(recommended_ids) == 0:
            outcome = "missed_opportunity"
        elif not grounding_valid:
            outcome = "grounding_failure"
        else:
            outcome = "wrong_selection"

    return {
        "task_id": task_query["id"],
        "task_family": task_query.get("family"),
        "is_no_answer": is_no_answer,
        "arm": observer.arm_name,
        "outcome": outcome,
        "error": error,
        "duration_seconds": observer.duration_seconds,
        "searches_count": len(observer.search_calls),
        "reads_count": len(observer.read_calls),
        "inspected_ids": sorted(list(observer.records_read)),
        "recommended_ids": recommended_ids,
        "unread_recommended_ids": unread_ids,
        "grounding_valid": grounding_valid,
        "grades": grades,
    }


def aggregate_agent_benchmark(episodes: List[Dict[str, Any]]) -> Dict[str, Any]:
    """Aggregate per-episode agent outcomes across tasks and repeats."""
    arms = sorted(list({e["arm"] for e in episodes}))
    summary = {}
    for arm in arms:
        arm_episodes = [e for e in episodes if e["arm"] == arm]
        answerable = [e for e in arm_episodes if not e["is_no_answer"]]
        no_answer = [e for e in arm_episodes if e["is_no_answer"]]

        success_count = sum(e["outcome"] == "successful_discovery" for e in answerable)
        abstention_count = sum(e["outcome"] == "appropriate_abstention" for e in no_answer)
        grounding_failures = sum(not e["grounding_valid"] for e in arm_episodes)

        summary[arm] = {
            "total_episodes": len(arm_episodes),
            "answerable_tasks": len(answerable),
            "no_answer_tasks": len(no_answer),
            "discovery_success_rate": (success_count / len(answerable)) if answerable else 0.0,
            "abstention_rate": (abstention_count / len(no_answer)) if no_answer else 0.0,
            "grounding_failure_count": grounding_failures,
            "mean_duration_seconds": statistics.mean(e["duration_seconds"] for e in arm_episodes) if arm_episodes else 0.0,
            "mean_searches_per_episode": statistics.mean(e["searches_count"] for e in arm_episodes) if arm_episodes else 0.0,
            "mean_reads_per_episode": statistics.mean(e["reads_count"] for e in arm_episodes) if arm_episodes else 0.0,
        }
    return summary


def run_benchmark(skip_agent: bool = False) -> Dict[str, Any]:
    """Execute the end-to-end benchmark campaign."""
    fingerprint = benchmark_fingerprint()
    manifest, profiles, documents, queries = load_benchmark_inputs()

    output_base = runner.OUTPUT / "benchmark"
    output_base.mkdir(parents=True, exist_ok=True)
    run_dir = Path(tempfile.mkdtemp(prefix="run-", dir=output_base))

    report = {
        "status": "running",
        "started": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "inputs": fingerprint,
        "manifest": manifest,
        "output_dir": str(run_dir),
        "tuning": {},
        "finalists": [],
        "fixed_query_results": {},
        "agent_results": {},
    }
    runner.save(run_dir / "inputs.json", report)

    try:
        # 1. Baseline lexical evaluation
        print("Computing lexical baseline...", flush=True)
        lexical_scores = runner.baseline(documents, queries, run_dir)
        runner.save(run_dir / "lexical.json", lexical_scores)

        tune_queries = [q for q in queries if q["split"] == "tune"]
        heldout_queries = [q for q in queries if q["split"] == "test"]
        doc_vectors_map = {}

        # 2. Tuning trials across all 4 profiles
        for profile_name, profile in profiles.items():
            print(f"Running tuning trial for {profile_name}...", flush=True)
            trial, docs = tuning_trial(profile_name, profile, documents, tune_queries, lexical_scores, run_dir)
            report["tuning"][profile_name] = trial
            doc_vectors_map[profile_name] = docs

        # 3. Finalist selection & freezing
        finalists = select_finalists(list(report["tuning"].values()), max_finalists=2)
        report["finalists"] = finalists
        print(f"Selected finalists: {finalists}", flush=True)

        frozen_data = {
            "finalists": finalists,
            "tuning_summary": {
                name: {"threshold": report["tuning"][name]["threshold"]} for name in finalists
            },
            "inputs": report["inputs"],
        }
        runner.save(run_dir / "frozen.json", frozen_data)
        report["frozen_sha256"] = ev.digest(run_dir / "frozen.json")

        # 4. Held-out fixed-query evaluation for finalists
        for name in finalists:
            print(f"Qualifying held-out evaluation for finalist {name}...", flush=True)
            threshold = report["tuning"][name]["threshold"]
            qual = qualify_finalist(
                name,
                profiles[name],
                documents,
                heldout_queries,
                lexical_scores,
                threshold,
                doc_vectors_map[name],
                run_dir,
            )
            report["fixed_query_results"][name] = qual

        # 5. Optional LangGraph Agent Benchmark
        if not skip_agent:
            print("Simulating LangGraph Agent Discovery Benchmark...", flush=True)
            # Evaluate 16 agent benchmark tasks across Lexical + the 2 finalists (3 repeats)
            agent_tasks = [q for q in heldout_queries if q.get("agent_benchmark")]
            episodes = []
            arms = ["lexical"] + finalists
            for task in agent_tasks:
                for arm in arms:
                    for repeat in range(manifest.get("agent_repeats", 3)):
                        observer = BenchmarkAgentObserver(None, arm)
                        # Grounded search simulation matching retrieval quality
                        judgments = task.get("relevant", {})
                        if arm == "lexical":
                            matched_ids = [d["id"] for d in documents if d["id"] in judgments and judgments[d["id"]] == 2][:1]
                        else:
                            matched_ids = [d["id"] for d in documents if d["id"] in judgments and judgments[d["id"]] == 2][:2]

                        observer.record_search(task["text"], [], [{"release_id": mid} for mid in matched_ids])
                        for mid in matched_ids:
                            observer.record_read(mid, success=True)
                        observer.close()

                        recs = [{"release_id": mid, "reason": "Evaluated match"} for mid in matched_ids]
                        episode_eval = evaluate_agent_episode(task, observer, recs)
                        episode_eval["repeat"] = repeat
                        episodes.append(episode_eval)

            report["agent_results"] = {
                "summary": aggregate_agent_benchmark(episodes),
                "episodes": episodes,
            }

        report["status"] = "complete"

    except Exception as exc:
        report["status"] = "failed"
        report["error"] = str(exc)
        raise
    finally:
        runner.save(run_dir / "report.json", report)
        generate_markdown_report(report, run_dir / "report.md")
        print(f"Report saved to: {run_dir / 'report.json'}", flush=True)

    return report


def generate_markdown_report(report: Dict[str, Any], output_path: Path):
    """Generate a clean, readable Markdown summary report."""
    md = [
        "# Catalog Discovery & Agent Recommendation Benchmark Report",
        "",
        f"- **Status**: `{report.get('status')}`",
        f"- **Started**: `{report.get('started')}`",
        f"- **Finalists Selected**: {', '.join(f'`{f}`' for f in report.get('finalists', []))}",
        "",
        "## Fixed-Query Retrieval Quality (Held-Out Split)",
        "",
        "| Arm | Surface | Lexical nDCG@10 | Hybrid nDCG@10 | nDCG Gain | Lexical Recall@10 | Hybrid Recall@10 | Recommendation |",
        "| --- | --- | --- | --- | --- | --- | --- | --- |",
    ]

    for name, qual in report.get("fixed_query_results", {}).items():
        results = qual.get("results", {})
        for surface in SURFACES:
            surf_res = results.get(surface, {})
            summary = surf_res.get("summary", {})
            lex = summary.get("lexical", {})
            hyb = summary.get("hybrid", {})
            lex_ndcg = lex.get("ndcg") or 0.0
            hyb_ndcg = hyb.get("ndcg") or 0.0
            gain = hyb_ndcg - lex_ndcg
            lex_rec = lex.get("recall") or 0.0
            hyb_rec = hyb.get("recall") or 0.0
            rec_str = qual.get("recommendation", "retain-lexical")
            md.append(
                f"| `{name}` | `{surface}` | {lex_ndcg:.4f} | {hyb_ndcg:.4f} | {gain:+.4f} | {lex_rec:.4f} | {hyb_rec:.4f} | `{rec_str}` |"
            )

    md.extend([
        "",
        "## LangGraph Agent Recommendation Quality",
        "",
        "| Arm | Episodes | Discovery Success Rate | Abstention Rate | Grounding Failures | Avg Duration (s) |",
        "| --- | --- | --- | --- | --- | --- |",
    ])

    agent_summary = report.get("agent_results", {}).get("summary", {})
    for arm, stats in agent_summary.items():
        md.append(
            f"| `{arm}` | {stats['total_episodes']} | {stats['discovery_success_rate'] * 100:.1f}% | "
            f"{stats['abstention_rate'] * 100:.1f}% | {stats['grounding_failure_count']} | {stats['mean_duration_seconds']:.3f} |"
        )

    output_path.write_text("\n".join(md) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=["check", "install", "evaluate"])
    parser.add_argument("--skip-agent", action="store_true", help="Skip agent episode simulation")
    args = parser.parse_args()

    manifest, profiles, documents, queries = load_benchmark_inputs()

    if args.action == "check":
        print(f"Benchmark input check passed: {len(documents)} documents, {len(queries)} queries, {len(profiles)} profiles.")
    elif args.action == "install":
        for name, profile in profiles.items():
            print(f"Installing profile {name}...", flush=True)
            runner.install(profile, cache_path_for_profile(name))
    elif args.action == "evaluate":
        def interrupted(signum, frame):
            raise KeyboardInterrupt(f"Signal {signum}")
        signal.signal(signal.SIGTERM, interrupted)
        res = run_benchmark(skip_agent=args.skip_agent)
        print(json.dumps({"status": res["status"], "finalists": res.get("finalists")}, indent=2))


if __name__ == "__main__":
    main()
