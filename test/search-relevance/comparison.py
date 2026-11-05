"""Compare local profiles on tuning data before qualifying one frozen candidate.

The original experiment stays independently runnable. Shared acquisition,
offline inference, metric math and SQL parity live in runner/evaluation; this
module owns only the versioned two-candidate experiment and selection boundary.
"""

import argparse
from concurrent.futures import ThreadPoolExecutor
import json
from pathlib import Path
import signal
import tempfile
import time
import urllib.error

import evaluation as ev
import runner

EXPERIMENT = ev.ROOT / "comparison-v2"
PROFILES = runner.REPO / "deployments/embeddings/profiles"
SURFACES = ("control", "public")
SELECTION = "least-added-no-answer-errors_then-highest-mean-hybrid-ndcg_then-profile-id"


def inputs():
    """Validate profiles, preserved corpus and fresh family-separated judgments."""
    experiment = ev.load_json(EXPERIMENT / "experiment.json")
    if experiment["version"] != 2 or experiment["profiles"] != ["bge-small-v1", "minilm-l6-v1"] or experiment["selection"] != SELECTION:
        raise ValueError("unexpected comparison definition")
    if ev.digest(ev.ROOT / "corpus.json") != experiment["corpus_sha256"]:
        raise ValueError("the first corpus must remain unchanged")
    documents = ev.load_json(ev.ROOT / "corpus.json")
    queries = ev.load_json(EXPERIMENT / "queries.json")
    profiles = {}
    for name in experiment["profiles"]:
        profile = ev.load_json(PROFILES / (name + ".json"))
        profile.update({key: experiment[key] for key in ("thresholds", "rrf_k", "minimum_ndcg_gain")})
        ev.validate_fixtures(profile, documents, queries)
        profiles[name] = profile
    validate_followup(queries, ev.load_json(ev.ROOT / "queries.json"))
    return experiment, profiles, documents, queries


def validate_followup(queries, original):
    """Include hard negatives in split isolation, not just answerable families."""
    families = {split: {q["intentFamily"] for q in queries if q["split"] == split} for split in ("tune", "test")}
    if families["tune"] & families["test"]:
        raise ValueError("negative intent family crosses tuning/test boundary")
    original_text = {" ".join(q["text"].casefold().split()) for q in original if not q.get("exact")}
    if any(not q.get("exact") and " ".join(q["text"].casefold().split()) in original_text for q in queries):
        raise ValueError("follow-up copied an inspected question")
    for split in families:
        selected = [q for q in queries if q["split"] == split]
        for category in ("task", "short", "filtered", "hard-negative", "unrelated", "partial", "exact"):
            if not any(q["category"] == category for q in selected):
                raise ValueError("missing comparison category")
        if not any(1 in q["relevant"].values() for q in selected):
            raise ValueError("partial relevance needs graded judgments")


def cache(name):
    """Reuse the original BGE cache without copying or altering pinned inputs."""
    if name == "bge-small-v1":
        return runner.MODEL
    if name != "minilm-l6-v1":
        raise ValueError("unknown local evaluation profile")
    return runner.OUTPUT / "models" / name


def fingerprint():
    """Bind the experiment to exact source/profile/scorer bytes, not Git alone."""
    files = [ev.ROOT / name for name in ("manifest.json", "corpus.json", "queries.json", "evaluation.py", "runner.py", "comparison.py", "baseline/main.go")]
    files += [EXPERIMENT / "experiment.json", EXPERIMENT / "queries.json"]
    files += sorted(PROFILES.glob("*.json"))
    files += [runner.REPO / name for name in ("internal/catalog/search_profile.go", "internal/publication/manager.go")]
    return {str(path.relative_to(runner.REPO)): ev.digest(path) for path in files}


def similarities(documents, queries, document_vectors, query_vectors):
    """Compute exact similarities once, independent of cutoff or ranking."""
    return {q["id"]: {d["id"]: ev.cosine(query_vectors[q["id"]], document_vectors[d["id"]]) for d in documents} for q in queries}


def encode(origin, profile, documents, queries, timings):
    """Embed metadata and the specified split only, using profile-specific text."""
    doc_vectors = dict(zip((d["id"] for d in documents), runner.embed(origin, profile, [ev.projection(d) for d in documents], timings), strict=True))
    query_vectors = encode_queries(origin, profile, queries, timings)
    return doc_vectors, query_vectors


def encode_queries(origin, profile, queries, timings):
    """Apply the selected query preprocessing only after its prerequisites pass."""
    return dict(zip((q["id"] for q in queries), runner.embed(origin, profile, [profile["query_prefix"] + q["text"] for q in queries], timings), strict=True))


def reject_overlength(origin, profile):
    """Require actual rejection beyond each model's own declared token limit."""
    try:
        runner.request(origin, "/v1/embeddings", {"model": profile["model"], "input": "word " * (profile["max_input_length"] + 10)})
    except urllib.error.HTTPError as exc:
        if exc.code == 422:
            return
        raise
    raise ValueError("overlength input was silently accepted")


def tuning_trial(name, profile, documents, queries, lexical, directory):
    """Run and clean one candidate; held-out queries are forbidden here."""
    if not queries or any(q["split"] != "tune" for q in queries):
        raise ValueError("candidate trial accepts tuning only")
    runner.verify_model(profile, cache(name))
    with runner.owned_resources() as names:
        runner.save(directory / (name + "-resources.json"), names)
        started = time.perf_counter()
        origin, server = runner.start_tei(profile, names, cache(name))
        startup = time.perf_counter() - started
        reject_overlength(origin, profile)
        timings = []
        docs, vectors = encode(origin, profile, documents, queries, timings)
        scores = similarities(documents, queries, docs, vectors)
        threshold, grid = ev.tune(profile, documents, queries, lexical, scores)
        result = {"profile": name, "threshold": threshold, "grid": grid, "server": server, "startup_seconds": startup,
                  "embedding_batches": timings, "overlength_rejected": True,
                  "results": {s: ev.measure(documents, queries, s, lexical[s], scores, threshold, profile["rrf_k"]) for s in SURFACES}}
        runner.save(directory / (name + "-vectors.json"), {"documents": docs, "queries": vectors})
        runner.verify_model(profile, cache(name))
    result["cleanup"] = "complete"
    runner.save(directory / (name + "-tuning.json"), result)
    return result, docs


def select_candidate(trials):
    """Select from tuning summaries only; break exact quality ties by profile ID."""
    def key(trial):
        selected = next(row for row in trial["grid"] if row["threshold"] == trial["threshold"])
        return selected["excess_false_positives"], -selected["ndcg"], trial["profile"]
    return min(trials, key=key)["profile"]


def qualify(name, profile, documents, heldout, lexical, threshold, expected_docs, directory):
    """Evaluate only the frozen winner, including restart and exact SQL checks."""
    if not heldout or any(q["split"] != "test" for q in heldout):
        raise ValueError("qualification accepts held-out only")
    runner.verify_model(profile, cache(name))
    with runner.owned_resources() as names:
        runner.save(directory / "heldout-resources.json", names)
        origin, server = runner.start_tei(profile, names, cache(name))
        timings = []
        docs, _ = encode(origin, profile, documents, [], timings)
        if any(abs(ev.cosine(docs[key], expected_docs[key]) - 1) > profile["numeric_tolerance"] for key in docs):
            raise ValueError("restarted provider changed document vectors")
        vectors = encode_queries(origin, profile, heldout, timings)
        scores = similarities(documents, heldout, docs, vectors)
        results = {s: ev.measure(documents, heldout, s, lexical[s], scores, threshold, profile["rrf_k"]) for s in SURFACES}
        gates = {s: ev.acceptance(results[s], profile["minimum_ndcg_gain"]) for s in SURFACES}
        # Check kind regressions explicitly instead of leaving them as a caveat.
        for surface in SURFACES:
            gates[surface]["kinds_preserved"] = all(
                values["hybrid"]["ndcg"] >= values["lexical"]["ndcg"] and values["hybrid"]["recall"] >= values["lexical"]["recall"]
                for values in results[surface]["per_kind"].values() if values["lexical"]["answerable"])
        single = []
        def single_query(query):
            vector = runner.embed(origin, profile, [profile["query_prefix"] + query["text"]], single)[0]
            if abs(ev.cosine(vector, vectors[query["id"]]) - 1) > profile["numeric_tolerance"]:
                raise ValueError("single/batch inference differs")
        with ThreadPoolExecutor(max_workers=2) as pool:
            list(pool.map(single_query, heldout[:8]))
        result = {"server": server, "results": results, "acceptance": gates, "embedding_batches": timings,
                  "single_query_samples": single, "query_concurrency": 2,
                  "recommendation": "implement" if all(all(v.values()) for v in gates.values()) else "retain-lexical"}
        result["pgvector"] = runner.pgvector_parity(profile, names, documents, heldout, docs, vectors, threshold, lexical)
        result["resource_sample"] = json.loads(runner.command(["docker", "stats", "--no-stream", "--format", "{{json .}}", names["tei"]]))
        try:
            result["peak_memory_bytes"] = int(runner.command(["docker", "exec", names["tei"], "cat", "/sys/fs/cgroup/memory.peak"]).strip())
        except (RuntimeError, ValueError):
            result["peak_memory_bytes"] = None
        runner.save(directory / "heldout-vectors.json", {"documents": docs, "queries": vectors})
        runner.verify_model(profile, cache(name))
    result["cleanup"] = "complete"
    return result


def evaluate():
    """Freeze both the selected model and cutoff before any held-out inference."""
    input_hashes = fingerprint()
    experiment, profiles, documents, queries = inputs()
    if fingerprint() != input_hashes:
        raise ValueError("comparison inputs changed while loading")
    runner.OUTPUT.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="comparison-v2-", dir=runner.OUTPUT))
    report = {"status": "running", "started": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "inputs": input_hashes,
              "source": runner.command(["git", "rev-parse", "HEAD"]).strip(), "experiment": experiment, "tuning": {}}
    runner.save(directory / "inputs.json", report)
    try:
        lexical = runner.baseline(documents, queries, directory)
        runner.save(directory / "lexical.json", lexical)
        tuning, heldout = ([q for q in queries if q["split"] == split] for split in ("tune", "test"))
        document_vectors = {}
        for name, profile in profiles.items():
            trial, docs = tuning_trial(name, profile, documents, tuning, lexical, directory)
            report["tuning"][name] = trial
            document_vectors[name] = docs
        name = select_candidate(list(report["tuning"].values()))
        threshold = report["tuning"][name]["threshold"]
        frozen = {"profile": name, "threshold": threshold, "manifest": profiles[name], "inputs": report["inputs"]}
        if fingerprint() != report["inputs"]:
            raise ValueError("comparison inputs changed before freezing")
        runner.save(directory / "frozen.json", frozen)
        report["frozen_sha256"] = ev.digest(directory / "frozen.json")
        report.update(profile=name, threshold=threshold)
        report.update(qualify(name, profiles[name], documents, heldout, lexical, threshold, document_vectors[name], directory))
        if fingerprint() != report["inputs"] or ev.digest(directory / "frozen.json") != report["frozen_sha256"]:
            raise ValueError("comparison inputs or frozen selection changed")
        report["status"] = "complete"
    except BaseException as exc:
        report["status"] = "failed"
        report["error"] = str(exc)
        raise
    finally:
        runner.save(directory / "report.json", report)
        print(f"Report: {directory / 'report.json'}", flush=True)
    return report


def main():
    """Keep model acquisition explicit and remote services outside this trial."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "evaluate"))
    args = parser.parse_args()
    _, profiles, _, _ = inputs()
    if args.action == "install":
        for name, profile in profiles.items():
            runner.install(profile, cache(name))
    else:
        def interrupted(signum, frame):
            raise KeyboardInterrupt(f"signal {signum}")
        signal.signal(signal.SIGTERM, interrupted)
        report = evaluate()
        print(json.dumps({key: report[key] for key in ("status", "profile", "threshold", "recommendation", "acceptance")}, indent=2))


if __name__ == "__main__":
    main()
