"""Service-free fixture validation, exact retrieval and relevance measurements."""

import hashlib
import json
import math
import re
import statistics
import struct
from pathlib import Path

ROOT = Path(__file__).resolve().parent
KINDS = {"agent", "mcp_server", "skill", "plugin"}


def load_json(path):
    """Reject duplicate object keys instead of silently changing frozen inputs."""
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError(f"duplicate JSON key: {key}")
            result[key] = value
        return result
    def nonfinite(value):
        raise ValueError(f"nonfinite JSON number: {value}")
    return json.loads(Path(path).read_text(), object_pairs_hook=unique, parse_constant=nonfinite)


def digest(path):
    """Hash exact bytes, including downloaded weights, with bounded memory."""
    value = hashlib.sha256()
    with Path(path).open("rb") as stream:
        for block in iter(lambda: stream.read(1024 * 1024), b""):
            value.update(block)
    return value.hexdigest()


def fixtures():
    """Load the reviewed corpus and validate split, identity and judgments."""
    manifest = load_json(ROOT / "manifest.json")
    documents, queries = load_json(ROOT / "corpus.json"), load_json(ROOT / "queries.json")
    validate_fixtures(manifest, documents, queries)
    return manifest, documents, queries


def validate_fixtures(manifest, documents, queries):
    """Apply the same judgment/split checks to original and versioned trials."""
    validate_manifest(manifest)
    if not 1 <= len(documents) <= 200 or not 1 <= len(queries) <= 200:
        raise ValueError("unbounded or empty fixture")
    ids = {d["id"] for d in documents}
    if len(ids) != len(documents) or len({d["identifier"] for d in documents}) != len(documents):
        raise ValueError("duplicate document identity")
    if {d["kind"] for d in documents} != KINDS:
        raise ValueError("all four kinds are required")
    for d in documents:
        if type(d["public"]) is not bool or d["source"] not in {"local", "indexed"} or (d["public"] and d["source"] != "local"):
            raise ValueError("invalid source/public population")
        if not all(isinstance(d[k], str) and d[k] for k in ("id", "identifier", "name", "description", "family")):
            raise ValueError("invalid document text")
    if len({q["id"] for q in queries}) != len(queries):
        raise ValueError("duplicate query")
    splits = {name: set() for name in ("tune", "test")}
    texts = {name: set() for name in splits}
    representatives = {text.casefold() for d in documents for text in d["representativeQueries"]}
    for q in queries:
        if not isinstance(q["id"], str) or not q["id"] or q["split"] not in splits or not q["text"].strip() or len(q["text"]) > 500:
            raise ValueError("invalid query or split")
        texts[q["split"]].add(" ".join(q["text"].casefold().split()))
        if "exact" in q and type(q["exact"]) is not bool:
            raise ValueError("exact marker must be boolean")
        if q.get("kind") and q["kind"] not in KINDS:
            raise ValueError("invalid kind")
        if q.get("source") and q["source"] not in {"local", "indexed"}:
            raise ValueError("invalid source")
        if not q["rationale"] or any(k not in ids or type(v) is not int or v not in (1, 2) for k, v in q["relevant"].items()):
            raise ValueError("invalid judgments")
        if q["family"] != "no-answer":
            if not q["relevant"]:
                raise ValueError("answerable query has no judgments")
            splits[q["split"]].add(q["family"])
        elif q["relevant"]:
            raise ValueError("no-answer query has relevant documents")
        if q["split"] == "test" and q["text"].casefold() in representatives:
            raise ValueError("held-out query copied from representative inputs")
        scoped = {d["id"]: d for d in eligible(documents, q, "control")}
        if not set(q["relevant"]).issubset(scoped):
            raise ValueError("judgment contradicts explicit kind/source filter")
        if q.get("exact") and (len(q["relevant"]) != 1 or scoped[next(iter(q["relevant"]))]["identifier"] != q["text"]):
            raise ValueError("exact query needs its actual identifier as the sole relevant identity")
    if splits["tune"] & splits["test"]:
        raise ValueError("intent family crosses tuning/test boundary")
    if texts["tune"] & texts["test"]:
        raise ValueError("query text crosses tuning/test boundary")
    for split in splits:
        if {q.get("kind") for q in queries if q["split"] == split and q.get("kind")} != KINDS:
            raise ValueError("each split needs explicit queries for all four kinds")


def validate_manifest(manifest):
    """Reject unsafe acquisition paths and malformed numeric evaluation settings."""
    if not re.fullmatch(r"[A-Za-z0-9][\w.-]*/[A-Za-z0-9][\w.-]*", manifest["model"]) or not re.fullmatch(r"[0-9a-f]{40}", manifest["revision"]):
        raise ValueError("model acquisition requires a fixed repository and revision")
    for name, checksum in manifest["files"].items():
        path = Path(name)
        if path.is_absolute() or ".." in path.parts or len(checksum) != 64 or any(c not in "0123456789abcdef" for c in checksum):
            raise ValueError("invalid pinned model file")
    if type(manifest["dimensions"]) is not int or not 1 <= manifest["dimensions"] <= 16000 or type(manifest["rrf_k"]) is not int or manifest["rrf_k"] <= 0:
        raise ValueError("invalid vector or fusion profile")
    if manifest.get("pooling", "cls") not in {"cls", "mean"} or type(manifest.get("max_input_length", 512)) is not int or not 1 <= manifest.get("max_input_length", 512) <= 4096:
        raise ValueError("invalid local model preprocessing")
    thresholds = manifest["thresholds"]
    if not thresholds or thresholds != sorted(set(thresholds)) or any(type(x) not in (int, float) or not math.isfinite(x) or not 0 <= x <= 1.01 for x in thresholds):
        raise ValueError("invalid cutoff grid")
    if not 0 < manifest["minimum_ndcg_gain"] <= 1 or not 0 < manifest["numeric_tolerance"] < 0.001:
        raise ValueError("invalid acceptance criteria")
    for key in ("tei_image", "postgres_image"):
        parts = manifest[key].split("@sha256:")
        if len(parts) != 2 or len(parts[1]) != 64 or any(c not in "0123456789abcdef" for c in parts[1]):
            raise ValueError("image must have immutable SHA-256 digest")


def projection(document):
    """Versioned metadata-only template; identity and visibility never leak in."""
    return "\n".join((
        "Name: " + document["name"].strip(),
        "Description: " + document["description"].strip(),
        "Tags: " + ", ".join(sorted(set(document["tags"]))),
        "Representative queries: " + "; ".join(document["representativeQueries"]),
    ))


def vector(values, dimensions):
    """Require normalized finite float32 vectors without silently repairing them."""
    if not isinstance(values, list) or len(values) != dimensions:
        raise ValueError("embedding dimension mismatch")
    if any(type(x) not in (int, float) or not math.isfinite(x) for x in values):
        raise ValueError("nonfinite or nonnumeric embedding")
    try:
        result = [struct.unpack("f", struct.pack("f", x))[0] for x in values]
    except (OverflowError, struct.error) as exc:
        raise ValueError("float32 overflow") from exc
    norm = math.sqrt(sum(x*x for x in result))
    if not math.isfinite(norm) or abs(norm - 1) > 0.001:
        raise ValueError("embedding must have unit norm")
    return result


def cosine(left, right):
    """Reference cosine using float32 inputs and full precision accumulation."""
    if len(left) != len(right) or not left:
        raise ValueError("incompatible vectors")
    denominator = math.sqrt(sum(x*x for x in left) * sum(x*x for x in right))
    if not denominator:
        raise ValueError("zero vector")
    return sum(a*b for a, b in zip(left, right, strict=True)) / denominator


def eligible(documents, query, surface):
    """Apply the evaluation's explicit population and filters before retrieval."""
    return [d for d in documents if
            (surface == "control" or d["public"])
            and (not query.get("kind") or d["kind"] == query["kind"])
            and (not query.get("source") or d["source"] == query["source"])]


def tie(document, surface):
    """Mirror the scorers' stable identity/name tie order for single versions."""
    return ((document["name"].lower(), document["id"]) if surface == "control"
            else (document["identifier"], "1.0.0", document["kind"]))


def rankings(documents, query, surface, lexical, similarities, threshold, k):
    """Retrieve independently, then fuse ranks without duplicate source votes."""
    by_id = {d["id"]: d for d in eligible(documents, query, surface)}
    if len({d["identifier"] for d in by_id.values()}) != len(by_id):
        raise ValueError("resolve duplicate identities before retrieval")
    def key(ident):
        return tie(by_id[ident], surface)
    lex = sorted((i for i in by_id if lexical[i] > 0), key=lambda i: (-lexical[i], key(i)))
    sem = sorted((i for i in by_id if similarities[i] >= threshold), key=lambda i: (-similarities[i], key(i)))
    fused = {}
    for sequence in (lex, sem):
        for rank, ident in enumerate(sequence, 1):
            fused[ident] = fused.get(ident, 0) + 1 / (k + rank)
    exact = {i for i, d in by_id.items() if query["text"] == d["identifier"]}
    hybrid = sorted(fused, key=lambda i: (i not in exact, -fused[i], key(i)))
    return {"lexical": lex, "semantic": sem, "hybrid": hybrid}


def metrics(ranked, judgments):
    """nDCG uses 2^grade-1 gain; Recall counts each relevant identity once."""
    if len(set(ranked)) != len(ranked):
        raise ValueError("duplicate ranked identity")
    if not judgments:
        return {"ndcg": None, "recall": None, "false_positive": bool(ranked)}
    def dcg(grades):
        return sum((2**grade-1) / math.log2(rank+2) for rank, grade in enumerate(grades))
    top = ranked[:10]
    ideal = dcg(sorted(judgments.values(), reverse=True)[:10])
    return {"ndcg": dcg([judgments.get(i, 0) for i in top]) / ideal,
            "recall": sum(i in judgments for i in top) / len(judgments), "false_positive": False}


def summarize(rows):
    """Keep no-answer denominators separate from answerable retrieval metrics."""
    answerable = [r for r in rows if r["ndcg"] is not None]
    return {"queries": len(rows), "answerable": len(answerable),
            "no_answer": len(rows)-len(answerable),
            "ndcg": statistics.mean(r["ndcg"] for r in answerable) if answerable else None,
            "recall": statistics.mean(r["recall"] for r in answerable) if answerable else None,
            "false_positives": sum(r["false_positive"] for r in rows)}


def measure(documents, queries, surface, lexical, similarities, threshold, k):
    """Return aggregate, per-kind and per-query evidence for one frozen split."""
    rows = []
    for query in queries:
        scope = eligible(documents, query, surface)
        ids = {d["id"] for d in scope}
        judgments = {i: grade for i, grade in query["relevant"].items() if i in ids}
        ranks = rankings(documents, query, surface, lexical[query["id"]], similarities[query["id"]], threshold, k)
        rows.append({"id": query["id"], "kind": query.get("kind", "all"), "exact": query.get("exact", False),
                     "judgments": judgments, "rankings": ranks,
                     "metrics": {method: metrics(rank, judgments) for method, rank in ranks.items()}})
    summary = {method: summarize([r["metrics"][method] for r in rows]) for method in ("lexical", "semantic", "hybrid")}
    per_kind = {kind: {method: summarize([r["metrics"][method] for r in rows if r["kind"] == kind]) for method in summary} for kind in sorted(KINDS)}
    exact_failures = [r["id"] for r in rows if r["exact"] and r["judgments"] and (not r["rankings"]["hybrid"] or r["rankings"]["hybrid"][0] not in r["judgments"])]
    return {"summary": summary, "per_kind": per_kind, "exact_failures": exact_failures, "queries": rows}


def tune(manifest, documents, queries, lexical, similarities):
    """Choose one shared cutoff on tuning only; reject held-out input outright."""
    if not queries or any(q["split"] != "tune" for q in queries):
        raise ValueError("tuning accepts only the tuning split")
    choices = []
    for threshold in manifest["thresholds"]:
        values = [measure(documents, queries, s, lexical[s], similarities, threshold, manifest["rrf_k"])["summary"] for s in ("control", "public")]
        excess = sum(max(0, v["hybrid"]["false_positives"]-v["lexical"]["false_positives"]) for v in values)
        quality = statistics.mean(v["hybrid"]["ndcg"] for v in values)
        choices.append({"threshold": threshold, "excess_false_positives": excess, "ndcg": quality})
    # Avoid inventing answers first, then maximize relevance, preferring the
    # stricter cutoff on exact ties. 1.01 is the deliberate lexical-only option.
    selected = min(choices, key=lambda x: (x["excess_false_positives"], -x["ndcg"], -x["threshold"]))
    return selected["threshold"], choices


def acceptance(result, minimum_gain):
    """Apply predeclared criteria without turning a quality failure into success."""
    lex, hybrid = result["summary"]["lexical"], result["summary"]["hybrid"]
    return {"useful_gain": hybrid["ndcg"]-lex["ndcg"] >= minimum_gain,
            "recall_preserved": hybrid["recall"] >= lex["recall"],
            "no_answer_preserved": hybrid["false_positives"] <= lex["false_positives"],
            "exact_preserved": not result["exact_failures"]}
