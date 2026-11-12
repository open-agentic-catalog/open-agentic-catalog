"""Pure campaign validation, selection and scoring, independent of live tooling."""

import math
import random
import statistics

import evaluation as ev

SELECTION_RULE = "control-nonexact_no-added-errors_ndcg_recall_stricter-cutoff_profile-id"
PROFILES = ("bge-small-v1", "minilm-l6-v1", "granite-278m-v1", "multilingual-e5-large-v1")


def stratum(query):
    if query.get("exact"):
        return "exact"
    if not query["relevant"]:
        return "no_answer"
    return "answerable" if 2 in query["relevant"].values() else "partial_only"


def validate(manifest, profiles, documents, queries):
    """Validate the actual API/protocol limits before any infrastructure work."""
    if manifest.get("version") != 2 or manifest.get("selection") != SELECTION_RULE:
        raise ValueError("unsupported benchmark protocol")
    if tuple(manifest.get("profiles", ())) != PROFILES or set(profiles) != set(PROFILES):
        raise ValueError("the predeclared four-profile roster is required")
    bounds = {"agent_repeats": (1, 5), "agent_task_count": (8, 24), "episode_seconds": (30, 600),
              "run_seconds": (600, 43200), "startup_seconds": (60, 1800), "index_seconds": (60, 1800)}
    for key, (low, high) in bounds.items():
        if type(manifest.get(key)) is not int or not low <= manifest[key] <= high:
            raise ValueError("invalid bounded protocol setting: " + key)
    if manifest["rrf_k"] != 60 or manifest["surface"] != "control-local-english":
        raise ValueError("campaign must use the current Control/local RRF profile")
    grid = manifest["thresholds"]
    if (not isinstance(grid, list) or not 2 <= len(grid) <= 25 or len(set(grid)) != len(grid)
            or any(type(x) not in (int, float) or not math.isfinite(x) or not -1 <= x <= 1 for x in grid)):
        raise ValueError("invalid runtime cutoff grid")
    if not 0 < manifest["minimum_ndcg_gain"] <= 1:
        raise ValueError("invalid minimum gain")
    if len(documents) != 160 or len(queries) != 96:
        raise ValueError("the reviewed first-edition population is 160 records/96 queries")
    names = [d["name"].casefold() for d in documents]
    if len(set(names)) != len(names):
        raise ValueError("unique normalized names are required for repeatable tie ordering")
    if any(d["source"] != "local" or d["public"] for d in documents):
        raise ValueError("core corpus contains only tenant-visible local releases")
    for profile in profiles.values():
        ev.validate_fixtures(profile, documents, queries)
    families = {s: {q["intentFamily"] for q in queries if q["split"] == s} for s in ("tune", "test")}
    if families["tune"] & families["test"]:
        raise ValueError("intent family crosses tuning/test split")
    for q in queries:
        if len(q["text"].encode("utf-8")) > 500 or "\0" in q["text"]:
            raise ValueError("query exceeds the Control UTF-8 contract")
        if q.get("category") == "partial" and stratum(q) != "partial_only":
            raise ValueError("partial-only task has contradictory judgments")
    for split in ("tune", "test"):
        kinds = {q.get("kind") for q in queries if q["split"] == split and q.get("kind")}
        if kinds != ev.KINDS or not all(any(q["split"] == split and stratum(q) == s for q in queries)
                                        for s in ("answerable", "no_answer", "partial_only")):
            raise ValueError("split lacks required task strata or kind controls")
    tasks = [q for q in queries if q.get("agent_benchmark")]
    if (len(tasks) != manifest["agent_task_count"] or any(q["split"] != "test" or q.get("exact") for q in tasks)
            or {q.get("kind") for q in tasks if q.get("kind")} != ev.KINDS
            or not {"answerable", "no_answer", "partial_only"} <= {stratum(q) for q in tasks}):
        raise ValueError("agent subset must contain held-out four-kind, empty and partial tasks")


def metrics(ranked, judgments):
    """Reuse graded nDCG, with explicit Recall@5 as well as Recall@10."""
    value = ev.metrics(ranked, judgments)
    value["recall5"] = sum(x in judgments for x in ranked[:5]) / len(judgments) if judgments else None
    return value


def summarize(rows):
    """Never mix exact, partial-only or empty tasks into primary quality."""
    direct = [x for x in rows if x["stratum"] == "answerable"]
    empty = [x for x in rows if x["stratum"] == "no_answer"]
    partial = [x for x in rows if x["stratum"] == "partial_only"]
    return {"queries": len(rows), "answerable": len(direct), "no_answer": len(empty),
            "partial_only": len(partial), "exact": sum(x["stratum"] == "exact" for x in rows),
            "ndcg": statistics.mean(x["metrics"]["ndcg"] for x in direct) if direct else None,
            "recall": statistics.mean(x["metrics"]["recall"] for x in direct) if direct else None,
            "recall5": statistics.mean(x["metrics"]["recall5"] for x in direct) if direct else None,
            "candidate_at_5": statistics.mean(x["direct_at_5"] for x in direct) if direct else None,
            "candidate_at_10": statistics.mean(x["direct_at_10"] for x in direct) if direct else None,
            "false_positives": sum(bool(x["ranked"]) for x in empty),
            "partial_ndcg": statistics.mean(x["metrics"]["ndcg"] for x in partial) if partial else None}


def measure(queries, rankings):
    if set(rankings) != {q["id"] for q in queries}:
        raise ValueError("missing or extra fixed-query measurement")
    rows = []
    for q in queries:
        ranked = rankings[q["id"]]
        rows.append({"id": q["id"], "family": q["intentFamily"], "kind": q.get("kind", "all"),
                     "category": q["category"], "stratum": stratum(q), "ranked": ranked,
                     "metrics": metrics(ranked, q["relevant"]),
                     "direct_at_5": any(q["relevant"].get(x) == 2 for x in ranked[:5]),
                     "direct_at_10": any(q["relevant"].get(x) == 2 for x in ranked[:10])})
    return {"summary": summarize(rows), "queries": rows,
            "per_kind": {k: summarize([x for x in rows if x["kind"] == k]) for k in sorted(ev.KINDS | {"all"})},
            "per_category": {c: summarize([x for x in rows if x["category"] == c]) for c in sorted({q["category"] for q in queries})}}


def tuning_grid(manifest, documents, queries, lexical, similarities):
    if any(q["split"] != "tune" for q in queries):
        raise ValueError("tuning accepts tuning queries only")
    baseline = measure(queries, {q["id"]: ev.rankings(documents, q, "control", lexical[q["id"]], similarities[q["id"]], 1.01, 60)["lexical"] for q in queries})
    rows = []
    for threshold in manifest["thresholds"]:
        ranked = {q["id"]: ev.rankings(documents, q, "control", lexical[q["id"]], similarities[q["id"]], threshold, 60)["hybrid"] for q in queries}
        result = measure(queries, ranked)["summary"]
        rows.append({"threshold": threshold, "excess_false_positives": max(0, result["false_positives"] - baseline["summary"]["false_positives"]),
                     "ndcg": result["ndcg"], "recall": result["recall"]})
    selected = min(rows, key=lambda r: (r["excess_false_positives"], -r["ndcg"], -r["recall"], -r["threshold"]))
    return selected, rows, baseline


def select_finalists(trials):
    def key(t):
        r = t["selected"]
        # Raw cosine cutoffs are not comparable across different encoders.
        return r["excess_false_positives"], -r["ndcg"], -r["recall"], t["profile"]
    ranked = sorted(trials, key=key)
    qualified = [t["profile"] for t in ranked if t["selected"]["excess_false_positives"] == 0
                 and t["selected"]["ndcg"] > t["lexical"]["summary"]["ndcg"]]
    return {"profiles": qualified[:2] or [ranked[0]["profile"]], "diagnostic": not bool(qualified)}


def episode(task, evidence, recommendations, error=None):
    """Labels are used only after the graph has returned; errors cannot succeed."""
    ids = [r["release_id"] for r in recommendations]
    known = set(evidence["found"]) & set(evidence["read"])
    grounded = len(set(ids)) == len(ids) and set(ids) <= known and error != "model_recommended_unread_resource"
    grades = [task["relevant"].get(i, 0) for i in ids]
    category = stratum(task)
    if error == "model_recommended_unread_resource":
        outcome = "grounding_failure"
    elif error == "model_did_not_search":
        outcome = "did_not_search"
    elif error in {"deadline_exceeded", "inference_deadline_exceeded", "model_turn_limit_exceeded", "request_budget_exhausted"}:
        outcome = "budget_exhausted"
    elif error:
        outcome = "agent_error"
    elif not evidence["searched"]:
        outcome = "did_not_search"
    elif not grounded:
        outcome = "grounding_failure"
    elif not ids and evidence["incomplete"]:
        outcome = "incomplete_search"
    elif category == "no_answer":
        outcome = "appropriate_abstention" if not ids else "unsupported_recommendation"
    elif category == "partial_only":
        outcome = "partial_usefulness" if ids and all(grades) else "partial_abstention" if not ids else "wrong_selection"
    elif 2 in grades and all(grades):
        outcome = "successful_discovery"
    else:
        outcome = "missed_opportunity" if not ids else "wrong_selection"
    return {"task": task["id"], "family": task["intentFamily"], "stratum": category,
            "outcome": outcome, "error": error, "grounded": grounded, "recommended": ids,
            "grades": grades, "item_precision": sum(g > 0 for g in grades) / len(grades) if grades else None,
            "candidate_available": any(task["relevant"].get(i) == 2 for i in evidence["found"]), **evidence}


def paired_interval(pairs, key, samples=2000):
    """Paired family bootstrap: correlated variants/repeats count as one unit."""
    families = {}
    for family, difference in pairs:
        families.setdefault(family, []).append(difference)
    means = [statistics.mean(x) for _, x in sorted(families.items())]
    if len(means) < 2:
        return {"families": len(means), "mean_difference": statistics.mean(means) if means else None, "ci95": None}
    rng = random.Random(key)
    values = sorted(statistics.mean(rng.choices(means, k=len(means))) for _ in range(samples))
    return {"families": len(means), "mean_difference": statistics.mean(means),
            "ci95": [values[int(.025 * samples)], values[min(samples-1, int(.975 * samples))]], "bootstrap_samples": samples}


def aggregate_agent(rows, tasks, repeats):
    expected = {(q["id"], i) for q in tasks for i in range(repeats)}
    if len(rows) != len(expected) or {(r["task"], r["repeat"]) for r in rows} != expected:
        raise ValueError("agent campaign has missing, duplicate or extra episodes")
    direct = [r for r in rows if r["stratum"] == "answerable"]
    empty = [r for r in rows if r["stratum"] == "no_answer"]
    return {"episodes": len(rows), "tasks": len(tasks), "answerable_episodes": len(direct), "no_answer_episodes": len(empty),
            "partial_only_episodes": sum(r["stratum"] == "partial_only" for r in rows),
            "discovery_success_rate": sum(r["outcome"] == "successful_discovery" for r in direct) / len(direct) if direct else None,
            "abstention_rate": sum(r["outcome"] == "appropriate_abstention" for r in empty) / len(empty) if empty else None,
            "errors": sum(r["error"] is not None for r in rows), "grounding_failures": sum(not r["grounded"] for r in rows),
            "error_codes": {code: sum(r["error"] == code for r in rows) for code in sorted({r["error"] for r in rows if r["error"]})},
            "search_pages_by_tier": {tier: sum(call["operation"] == "search" and call.get("response", {}).get("tier") == tier
                                               for row in rows for call in row.get("sdk_calls", [])) for tier in ("core", "hybrid")},
            "outcomes": {k: sum(r["outcome"] == k for r in rows) for k in sorted({r["outcome"] for r in rows})}}
