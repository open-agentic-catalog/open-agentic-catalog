"""Explicit local Control API and LangGraph discovery-quality campaign."""

import argparse
import asyncio
import json
import os
from pathlib import Path
import signal
import statistics
import sys
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import evaluation as ev
import runner
from benchmark import protocol

BENCHMARK_DIR = Path(__file__).resolve().parent
PROFILES_DIR = runner.REPO / "deployments/embeddings/profiles"


def load_benchmark_inputs():
    manifest = ev.load_json(BENCHMARK_DIR / "manifest.json")
    documents = ev.load_json(BENCHMARK_DIR / "corpus.json")
    queries = ev.load_json(BENCHMARK_DIR / "queries.json")
    profiles = {}
    for name in protocol.PROFILES:
        value = ev.load_json(PROFILES_DIR / (name + ".json"))
        value.update({key: manifest[key] for key in ("thresholds", "rrf_k", "minimum_ndcg_gain")})
        profiles[name] = value
    protocol.validate(manifest, profiles, documents, queries)
    return manifest, profiles, documents, queries


def input_fingerprint():
    files = [BENCHMARK_DIR / name for name in ("manifest.json", "corpus.json", "queries.json")]
    files += [PROFILES_DIR / (name + ".json") for name in protocol.PROFILES]
    files += [runner.REPO / "test/langgraph/model.json", runner.REPO / "examples/langgraph/uv.lock"]
    return {str(p.relative_to(runner.REPO)): ev.digest(p) for p in files}


def save_report(report, directory):
    """Persist a failed terminal receipt if a non-JSON observation escapes."""
    try:
        runner.save(directory / "report.json", report)
    except (TypeError, ValueError) as exc:
        runner.save(directory / "report.json", {"status": "failed", "selection": report["selection"],
                    "qualified_for_recommendation": False, "error": "invalid report observation", "cleanup": report.get("cleanup", "pending")})
        raise ValueError("invalid report observation") from exc


def require_inputs(expected, frozen=None, frozen_digest=None):
    if input_fingerprint() != expected:
        raise ValueError("benchmark inputs changed during execution")
    if frozen is not None and ev.digest(frozen) != frozen_digest:
        raise ValueError("frozen experiment changed during execution")


def similarities(documents, queries, document_vectors, query_vectors):
    return {q["id"]: {d["id"]: ev.cosine(query_vectors[q["id"]], document_vectors[d["id"]]) for d in documents} for q in queries}


def encode(origin, profile, documents, queries):
    samples = []
    vectors = runner.embed(origin, profile, [profile.get("document_prefix", "") + ev.projection(d) for d in documents], samples)
    query_vectors = runner.embed(origin, profile, [profile.get("query_prefix", "") + q["text"] for q in queries], samples)
    return dict(zip([d["id"] for d in documents], vectors, strict=True)), dict(zip([q["id"] for q in queries], query_vectors, strict=True)), samples


def query_measurement(fixture, queries):
    ranks, samples = {}, []
    for query in queries:
        ranks[query["id"]], observation = fixture.search(query)
        samples.append(observation)
    result = protocol.measure(queries, ranks)
    result["samples"] = samples
    exact = {q["id"] for q in queries if q.get("exact")}
    values = sorted(s["seconds"] for s in samples if s["query"] not in exact)
    result["api_latency"] = {"samples": len(values), "median_seconds": statistics.median(values),
                             "p95_nearest_rank_seconds": values[max(0, (95 * len(values) + 99)//100 - 1)],
                             "scope": "serial warm non-exact first-page API requests; setup excluded"}
    return result


def require_source(fixture):
    if fixture.helpers.performance.source_identity(fixture.environment) != fixture.source:
        raise ValueError("campaign source changed during execution")


def candidate_trial(fixture, name, profile, manifest, documents, tuning, lexical, directory):
    from benchmark import runtime
    launch = fixture.configure(name, .2)
    settings = {**fixture.settings, "endpoint": fixture.model_origin + "/v1/embeddings", "allowedAddressPrefixes": ["127.0.0.1/32"]}
    qualification = json.loads(runner.command([str(directory / "qualify")], data=json.dumps(settings), timeout=120))
    if qualification["dimensions"] != profile["dimensions"] or not all(qualification[x] for x in ("batchOrdering", "repeatStable", "overlengthRejected")):
        raise ValueError("actual production embedding adapter qualification failed")
    doc_vectors, query_vectors, samples = encode(fixture.model_origin, profile, documents, tuning)
    matrix = similarities(documents, tuning, doc_vectors, query_vectors)
    selected, grid, baseline = protocol.tuning_grid(manifest, documents, tuning, lexical["control"], matrix)
    configured = fixture.configure(name, selected["threshold"])
    measured = query_measurement(fixture, tuning)
    for query, actual in zip(tuning, measured["queries"], strict=True):
        expected = ev.rankings(documents, query, "control", lexical["control"][query["id"]], matrix[query["id"]], selected["threshold"], 60)["hybrid"][:10]
        if actual["ranked"] != expected:
            runner.save(directory / (name + "-parity-failure.json"), {"query": query, "expected": expected, "actual": actual})
            raise ValueError("offline tuning differs from real Control API rankings: " + name + "/" + query["id"])
    runtime.runner.verify_model(profile, runtime.cache(name))
    runner.save(directory / (name + "-vectors.json"), {"documents": doc_vectors, "tuningQueries": query_vectors})
    return {"profile": name, "selected": selected, "grid": grid, "lexical": baseline,
            "tuning_api": measured, "qualification": qualification, "server": fixture.server_info,
            "initial_launch": launch, "selected_launch": configured, "embedding_batches": samples,
            "resource_sample": fixture.resources(), "storage_bytes": fixture.storage()}


def quality(lexical, candidate, minimum, seed):
    baseline, result = lexical["summary"], candidate["summary"]
    passed = result["ndcg"] - baseline["ndcg"] >= minimum and result["recall"] >= baseline["recall"] and result["false_positives"] <= baseline["false_positives"]
    base = {q["id"]: q for q in lexical["queries"]}
    pairs = [(q["family"], q["metrics"]["ndcg"] - base[q["id"]]["metrics"]["ndcg"]) for q in candidate["queries"] if q["stratum"] == "answerable"]
    return {"recommendation": "candidate-benefit-on-this-fixture" if passed else "retain-lexical",
            "ndcg_gain": result["ndcg"] - baseline["ndcg"], "criteria_met": passed,
            "paired_family_ndcg": protocol.paired_interval(pairs, seed)}


def render(report, path):
    lines = ["# Local Catalog discovery benchmark", "", "Status: `" + report["status"] + "`. Selection: `" + report["selection"] + "`.", "",
             "Real API results; primary scores exclude exact, partial-only and no-answer cases.", "",
             "| Arm | nDCG@10 | Recall@10 | No-answer errors | Decision |", "| --- | --- | --- | --- | --- |"]
    for name, value in report.get("fixed_query", {}).items():
        row = value["summary"]
        decision = report.get("quality", {}).get(name, {}).get("recommendation", "baseline") if report.get("qualified_for_recommendation") else "provisional"
        lines.append(f"| {name} | {row['ndcg']:.4f} | {row['recall']:.4f} | {row['false_positives']} / {row['no_answer']} | {decision} |")
    lines += ["", "| Arm | API median / p95 seconds | Active vector payload bytes | Model process peak bytes |",
              "| --- | --- | --- | --- |"]
    for name, value in report.get("fixed_query", {}).items():
        timing = value["api_latency"]
        storage = value["storage_bytes"].get("active_profile", {}).get("embedding_payload_bytes")
        memory = value["resources"]["process_lifetime_peak_bytes"]["embeddings"]
        lines.append(f"| {name} | {timing['median_seconds']:.4f} / {timing['p95_nearest_rank_seconds']:.4f} | {storage} | {memory} |")
    lines += ["", "| Agent arm | Episodes | Direct-match success | No-answer abstention | Search pages core / hybrid | Errors |", "| --- | --- | --- | --- | --- | --- |"]
    for name, value in report.get("agent", {}).items():
        row = value["summary"]
        direct = f"{row['discovery_success_rate']:.1%} of {row['answerable_episodes']}" if row['discovery_success_rate'] is not None else "not measured"
        empty = f"{row['abstention_rate']:.1%} of {row['no_answer_episodes']}" if row['abstention_rate'] is not None else "not measured"
        tiers = row["search_pages_by_tier"]
        lines.append(f"| {name} | {row['episodes']} | {direct} | {empty} | {tiers['core']} / {tiers['hybrid']} | {row['errors']} |")
    for name, value in report.get("quality", {}).items():
        interval = value["paired_family_ndcg"]
        lines += ["", f"{name}: paired nDCG difference {interval['mean_difference']}; 95% family interval {interval['ci95']} ({interval['families']} families)."]
    if report.get("error"):
        lines += ["", "Failure: " + report["error"]]
    lines += ["", "Small synthetic English/local corpus: 40 correlated task families, not a general model leaderboard.",
              "Timing samples are report-only. Repeated episodes are aggregated within task families.",
              "Only a complete full selection with successful cleanup can support the campaign's conclusions.", ""]
    path.write_text("\n".join(lines))


def run_benchmark(mode="full"):
    from benchmark import runtime
    for key in list(os.environ):
        if key.startswith(("LANGSMITH_", "LANGCHAIN_")):
            os.environ.pop(key)
    os.environ.update(LANGSMITH_TRACING="false", LANGCHAIN_TRACING_V2="false")
    before = input_fingerprint()
    manifest, profiles, documents, queries = load_benchmark_inputs()
    require_inputs(before)
    pin = ev.load_json(runner.REPO / "test/langgraph/model.json")
    prerequisites = runtime.preflight(profiles, pin)
    output = runner.OUTPUT / "benchmark"
    output.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="campaign-", dir=output))
    report = {"status": "running", "selection": mode, "qualified_for_recommendation": False, "stage": "setup", "inputs": before, "manifest": manifest, "reasoning_model": pin,
              "images": prerequisites["images"], "environment": {k: v for k, v in prerequisites.items() if k != "images"}, "output": str(directory), "tuning": {}, "fixed_query": {}, "agent": {}, "quality": {}}
    save_report(report, directory)
    try:
        with runtime.catalog_fixture(directory, manifest, profiles, pin, documents) as fixture:
            report["source"] = fixture.source
            if fixture.helpers.performance.source_identity(fixture.environment) != fixture.source:
                raise ValueError("source changed during build or fixture setup")
            report["application_image"] = fixture.command(["docker", "image", "inspect", "--format={{.Id}}", fixture.environment["IMAGE"]]).strip()
            report["fixture"] = {"project": fixture.project, "namespace": fixture.inventory["namespaceId"], "records": len(documents)}
            tuning = [q for q in queries if q["split"] == "tune"]
            heldout = [q for q in queries if q["split"] == "test"]
            lexical = runner.baseline(documents, tuning, directory)
            fixture.configure("lexical")
            baseline = query_measurement(fixture, tuning)
            for query in baseline["queries"]:
                expected = ev.rankings(documents, next(q for q in tuning if q["id"] == query["id"]), "control", lexical["control"][query["id"]],
                                       {d["id"]: 0 for d in documents}, 1.01, 60)["lexical"][:10]
                if query["ranked"] != expected:
                    raise ValueError("real lexical API differs from the production Go scorer bridge")
            report["controls"] = fixture.controls(documents)
            for name in (protocol.PROFILES if mode == "full" else ("bge-small-v1",)):
                report.update(stage="tuning", active_arm=name)
                print("Tuning and qualifying real API:", name, flush=True)
                report["tuning"][name] = candidate_trial(fixture, name, profiles[name], manifest, documents, tuning, lexical, directory)
                require_inputs(before)
                require_source(fixture)
                save_report(report, directory)
            from benchmark import agent_benchmark
            invoice = next(q for q in tuning if q["id"] == "tune-invoice-task")
            control = asyncio.run(agent_benchmark.run_episode(fixture, invoice, pin, manifest["episode_seconds"], agent_benchmark.ScriptedModel()))
            if control["outcome"] != "successful_discovery" or control["tool_calls"] != 2 or control["catalog_requests"] < 2:
                raise ValueError("actual API/scripted graph observation control failed")
            report["scripted_observation_control"] = control
            report["tuning_exact_control"] = fixture.exact_controls([{**invoice, "text": documents[0]["identifier"], "relevant": {documents[0]["id"]: 2}, "exact": True}])
            finalists = protocol.select_finalists(list(report["tuning"].values()))
            frozen = {"inputs": before, "source": report["source"], "profiles": {k: v["selected"] for k, v in report["tuning"].items()},
                      "agent_finalists": finalists, "agent_tasks": [q["id"] for q in heldout if q.get("agent_benchmark")],
                      "reasoner": pin, "manifest": manifest}
            require_inputs(before)
            require_source(fixture)
            runner.save(directory / "frozen.json", frozen)
            frozen_digest = ev.digest(directory / "frozen.json")
            report["frozen_sha256"] = frozen_digest
            if mode == "full":
                for name in ("lexical", *protocol.PROFILES):
                    report.update(stage="held-out-api", active_arm=name)
                    print("Held-out real API:", name, flush=True)
                    cutoff = None if name == "lexical" else report["tuning"][name]["selected"]["threshold"]
                    launch = fixture.configure(name, cutoff)
                    require_source(fixture)
                    measurement = query_measurement(fixture, heldout)
                    measurement["exact_controls"] = fixture.exact_controls([q for q in heldout if q.get("exact")])
                    measurement["launch"] = launch
                    measurement["resources"] = fixture.resources()
                    measurement["storage_bytes"] = fixture.storage()
                    report["fixed_query"][name] = measurement
                    if name != "lexical":
                        report["quality"][name] = quality(report["fixed_query"]["lexical"], measurement, manifest["minimum_ndcg_gain"], manifest["bootstrap_seed"])
                    require_inputs(before, directory / "frozen.json", frozen_digest)
                    require_source(fixture)
                    save_report(report, directory)
                fixture.start_reasoner()
                agent_benchmark.verify_model(fixture, pin)
                tasks = [q for q in heldout if q.get("agent_benchmark")]
                report["agent_selection"] = finalists
                report["agent_warmup"] = {}
                for name in ["lexical", *finalists["profiles"]]:
                    report.update(stage="agent", active_arm=name)
                    print("Real LangGraph episodes:", name, flush=True)
                    fixture.configure(name, None if name == "lexical" else report["tuning"][name]["selected"]["threshold"])
                    warm = asyncio.run(agent_benchmark.run_episode(fixture, invoice, pin, manifest["episode_seconds"]))
                    fixture.require_reasoner()
                    if warm["model_calls"] < 1 or warm["inference_http_attempts"] < 1:
                        raise ValueError("actual model invocation/HTTP observation control failed")
                    report["agent_warmup"][name] = warm
                    episodes = []
                    for task in tasks:
                        for repeat in range(manifest["agent_repeats"]):
                            fixture.guard()
                            print(name, task["id"], repeat + 1, flush=True)
                            row = asyncio.run(agent_benchmark.run_episode(fixture, task, pin, manifest["episode_seconds"]))
                            fixture.require_reasoner()
                            row["repeat"] = repeat
                            episodes.append(row)
                            runner.save(directory / (name + "-episodes.json"), episodes)
                    report["agent"][name] = {"episodes": episodes, "summary": protocol.aggregate_agent(episodes, tasks, manifest["agent_repeats"]), "resources": fixture.resources()}
                    require_inputs(before, directory / "frozen.json", frozen_digest)
                    require_source(fixture)
                    save_report(report, directory)
                baseline_episodes = {(r["task"], r["repeat"]): r for r in report["agent"]["lexical"]["episodes"]}
                report["paired_agent_discovery"], report["paired_agent_abstention"] = {}, {}
                for name in finalists["profiles"]:
                    pairs = [(r["family"], int(r["outcome"] == "successful_discovery") - int(baseline_episodes[r["task"], r["repeat"]]["outcome"] == "successful_discovery"))
                             for r in report["agent"][name]["episodes"] if r["stratum"] == "answerable"]
                    report["paired_agent_discovery"][name] = protocol.paired_interval(pairs, manifest["bootstrap_seed"])
                    empty_pairs = [(r["family"], int(r["outcome"] == "appropriate_abstention") - int(baseline_episodes[r["task"], r["repeat"]]["outcome"] == "appropriate_abstention"))
                                   for r in report["agent"][name]["episodes"] if r["stratum"] == "no_answer"]
                    report["paired_agent_abstention"][name] = protocol.paired_interval(empty_pairs, manifest["bootstrap_seed"])
            if fixture.helpers.performance.source_identity(fixture.environment) != report["source"]:
                raise ValueError("campaign source changed during execution")
            report["configurations"] = fixture.events
        report["cleanup"] = "complete"
        require_inputs(before, directory / "frozen.json", frozen_digest)
        for name, profile in profiles.items():
            runner.verify_model(profile, runtime.cache(name))
        runtime.verify_reasoner(pin)
        report["status"] = "complete"
        report["qualified_for_recommendation"] = mode == "full"
        report["stage"] = "complete"
    except BaseException as exc:
        report["status"] = "failed"
        report["error"] = "interrupted" if isinstance(exc, KeyboardInterrupt) else str(exc)
        raise
    finally:
        if (directory / "cleanup.json").is_file():
            report["cleanup"] = ev.load_json(directory / "cleanup.json")["status"]
        save_report(report, directory)
        render(report, directory / "report.md")
        print("Report:", directory / "report.json", flush=True)
    return report


def install(uv, python):
    from benchmark import runtime
    runner.command(["make", "langgraph-tools-install", "UV=" + uv, "DEV_PYTHON=" + python], timeout=600)
    _, profiles, _, _ = load_benchmark_inputs()
    for name, profile in profiles.items():
        runner.install(profile, runtime.cache(name))
    pin = ev.load_json(runner.REPO / "test/langgraph/model.json")
    runner.command(["docker", "pull", pin["runtimeImage"]], timeout=600)
    runtime.install_reasoner(pin)
    print("Pinned inputs ready. Reasoning cache is verified; existing cache/data were preserved.")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("check", "install", "evaluate"))
    parser.add_argument("--mode", choices=("full", "smoke"), default="full")
    parser.add_argument("--uv", default="uv")
    parser.add_argument("--python", default="python3")
    args = parser.parse_args()
    manifest, profiles, documents, queries = load_benchmark_inputs()
    if args.action == "check":
        print(f"Benchmark input check passed: {len(documents)} documents, {len(queries)} queries, {len(profiles)} profiles.")
    elif args.action == "install":
        install(args.uv, args.python)
    else:
        def interrupted(signum, frame):
            raise KeyboardInterrupt("SIGTERM")
        previous = signal.signal(signal.SIGTERM, interrupted)
        try:
            run_benchmark(args.mode)
        finally:
            signal.signal(signal.SIGTERM, previous)


if __name__ == "__main__":
    main()
