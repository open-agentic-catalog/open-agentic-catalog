"""Explicit local-model acquisition and disposable relevance qualification.

No Catalog endpoint or database DSN is accepted. All runtime destinations are
allocated by this process; the only network acquisition belongs to install.
"""

import argparse
from concurrent.futures import ThreadPoolExecutor
import contextlib
import ipaddress
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid

import evaluation as ev

REPO = ev.ROOT.parent.parent
OUTPUT = REPO / "output/search-relevance"
MODEL = OUTPUT / "model"
OWNED_ORIGINS = set()


def command(args, *, timeout=120, data=None):
    """Run an explicit argv with bounded duration, never through a shell."""
    result = subprocess.run(args, cwd=REPO, input=data, capture_output=True, text=True, timeout=timeout, check=False)
    if result.returncode:
        raise RuntimeError(f"command failed: {' '.join(args[:2])}: {result.stderr[-2000:]}")
    return result.stdout


def save(path, value):
    """Write a complete report without accepting nonfinite JSON numbers."""
    path.write_text(json.dumps(value, indent=2, allow_nan=False) + "\n")


def verify_inputs(report):
    """Reject mixed-source evidence while allowing unrelated documentation edits."""
    for base, values in ((ev.ROOT, report["inputs"]), (REPO, report["scorer_inputs"])):
        if any(ev.digest(base/name) != expected for name, expected in values.items()):
            raise ValueError("evaluation inputs changed during the run")


def model_path(model, name):
    """Reject symlinked cache parents before any acquisition or model mount."""
    path = model / name
    if any(parent.is_symlink() for parent in (path, *path.parents)) or not path.resolve().is_relative_to(model.resolve()):
        raise ValueError("model cache/file cannot use symlinks or escape its root")
    return path


def verify_model(manifest, model=None):
    """Check every pinned local input before granting it to the model server."""
    model = MODEL if model is None else model
    model_path(model, ".")
    actual = {str(path.relative_to(model)) for path in model.rglob("*") if path.is_file() or path.is_symlink()}
    if actual != set(manifest["files"]):
        raise ValueError("model cache file inventory differs from the pinned manifest")
    for name, expected in manifest["files"].items():
        path = model_path(model, name)
        if not path.is_file() or ev.digest(path) != expected:
            raise ValueError(f"missing or changed pinned model file: {name}; use the install target for the selected evaluation profile")


def install(manifest, model=None, *, postgres=True):
    """Download only fixed-revision inputs and digest-addressed images."""
    model = MODEL if model is None else model
    model_path(model, ".")
    model.mkdir(parents=True, exist_ok=True)
    for name, expected in manifest["files"].items():
        path = model_path(model, name)
        path.parent.mkdir(parents=True, exist_ok=True)
        if path.is_file() and not path.is_symlink() and ev.digest(path) == expected:
            continue
        if path.exists():
            raise ValueError(f"existing model file differs: {name}; inspect it before replacement")
        url = f"https://huggingface.co/{manifest['model']}/resolve/{manifest['revision']}/{name}"
        print(f"Downloading pinned {name}", flush=True)
        with tempfile.NamedTemporaryFile(dir=path.parent, prefix="download-") as temporary:
            command(["curl", "--fail", "--location", "--silent", "--show-error", "--proto", "=https", "--proto-redir", "=https", "--max-time", "300", "--max-filesize", "150000000", "--output", temporary.name, url], timeout=310)
            if ev.digest(temporary.name) != expected:
                raise ValueError(f"download checksum mismatch: {name}")
            # Same-filesystem exclusive final creation avoids replacing an input.
            os.link(temporary.name, path)
            path.chmod(0o644)
    verify_model(manifest, model)
    images = [manifest["tei_image"]]
    if postgres:
        images.append(manifest["postgres_image"])
    for image in images:
        print(f"Pulling {image}", flush=True)
        command(["docker", "pull", image], timeout=600)


class NoRedirect(urllib.request.HTTPRedirectHandler):
    """An owned inference endpoint must not redirect requests elsewhere."""

    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def request(origin, path, payload=None):
    """Contact only loopback test servers or registered fixture-owned addresses."""
    loopback = origin.startswith("http://127.0.0.1:") and origin.removeprefix("http://127.0.0.1:").isdigit()
    if not loopback and origin not in OWNED_ORIGINS:
        raise ValueError("expected owned inference origin")
    req = urllib.request.Request(origin + path, data=None if payload is None else json.dumps(payload).encode(), headers={"Content-Type": "application/json"})
    opener = urllib.request.build_opener(urllib.request.ProxyHandler({}), NoRedirect())
    with opener.open(req, timeout=30) as response:
        data = response.read(4 * 1024 * 1024 + 1)
        if len(data) > 4 * 1024 * 1024:
            raise ValueError("overlong inference response")
        return json.loads(data) if data else None


@contextlib.contextmanager
def finish_cleanup_before_interrupt():
    """Defer cancellation during bounded cleanup, then stop subsequent work."""
    received = []
    def interrupted(signum, frame):
        received.append(signum)
    handlers = {sig: signal.getsignal(sig) for sig in (signal.SIGINT, signal.SIGTERM)}
    try:
        for sig in handlers:
            signal.signal(sig, interrupted)
        yield
    finally:
        for sig, handler in handlers.items():
            signal.signal(sig, handler)
    if received:
        raise KeyboardInterrupt(f"signal {received[0]} during cleanup")


@contextlib.contextmanager
def owned_resources():
    """Clean only exact invocation-owned names, including failed startup paths."""
    prefix = "catalog-relevance-" + uuid.uuid4().hex[:16]
    names = {"network": prefix, "tei": prefix + "-tei", "pg": prefix + "-pg"}
    errors = []
    try:
        yield names
    finally:
        OWNED_ORIGINS.clear()
        # Child cleanup commands have separate process groups: a launcher-wide
        # SIGTERM must not kill docker rm while this process defers cancellation.
        with finish_cleanup_before_interrupt():
            for name in (names["tei"], names["pg"]):
                try:
                    result = subprocess.run(["docker", "rm", "--force", "--volumes", name], capture_output=True, text=True, timeout=30, check=False, start_new_session=True)
                    if result.returncode and "No such container" not in result.stderr:
                        errors.append(result.stderr.strip())
                except (OSError, subprocess.TimeoutExpired) as exc:
                    errors.append(str(exc))
            try:
                result = subprocess.run(["docker", "network", "rm", names["network"]], capture_output=True, text=True, timeout=30, check=False, start_new_session=True)
                if result.returncode and "not found" not in result.stderr:
                    errors.append(result.stderr.strip())
            except (OSError, subprocess.TimeoutExpired) as exc:
                errors.append(str(exc))
            if errors:
                raise RuntimeError("owned fixture cleanup failed: " + "; ".join(errors))


def start_tei(manifest, names, model=None):
    """Start the pinned CPU model offline, with explicit input/resource limits."""
    model = MODEL if model is None else model
    command(["docker", "network", "create", "--internal", names["network"]])
    command(["docker", "run", "-d", "--pull=never", "--name", names["tei"], "--network", names["network"],
             "--cpus", "4", "--memory", "4g", "--memory-swap", "4g", "--pids-limit", "256",
             "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--read-only", "--tmpfs", "/tmp:rw,nosuid,nodev,size=256m",
             "-e", "OMP_NUM_THREADS=4", "-e", "MKL_NUM_THREADS=4", "-e", "HF_HUB_OFFLINE=1",
             "--mount", f"type=bind,src={model},dst=/model,readonly", manifest["tei_image"],
             "--model-id", "/model", "--served-model-name", manifest["model"], "--revision", manifest["revision"],
             "--dtype", "float32", "--pooling", manifest.get("pooling", "cls"), "--auto-truncate=false", "--max-concurrent-requests", "16",
             "--max-batch-tokens", "4096", "--max-client-batch-size", "8", "--tokenization-workers", "2"])
    networks = json.loads(command(["docker", "inspect", "--format", "{{json .NetworkSettings.Networks}}", names["tei"]]))
    address = ipaddress.IPv4Address(networks[names["network"]]["IPAddress"])
    if not address.is_private or address.is_loopback or address.is_unspecified:
        raise ValueError("unexpected fixture network address")
    origin = f"http://{address}:80"
    OWNED_ORIGINS.add(origin)
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        try:
            info = request(origin, "/info")
            if info["served_model_name"] != manifest["model"] or info["model_sha"] != manifest["revision"] or info["model_dtype"] != "float32" or info["max_input_length"] != manifest.get("max_input_length", 512) or info["auto_truncate"] or info["model_type"] != {"embedding": {"pooling": manifest.get("pooling", "cls")}} or info["version"] != "1.9.4":
                raise ValueError("served model configuration mismatch")
            return origin, info
        except (urllib.error.URLError, TimeoutError, ConnectionError):
            if command(["docker", "inspect", "--format", "{{.State.Running}}", names["tei"]]).strip() != "true":
                raise RuntimeError("model exited: " + command(["docker", "logs", "--tail", "30", names["tei"]])) from None
            time.sleep(0.25)
    raise RuntimeError("model startup timeout")


def embed(origin, manifest, texts, timings):
    """Encode bounded batches and verify the actual float32 response contract."""
    result = []
    for offset in range(0, len(texts), 8):
        batch = texts[offset:offset+8]
        started = time.perf_counter()
        response = request(origin, "/v1/embeddings", {"model": manifest["model"], "input": batch, "encoding_format": "float"})
        timings.append({"inputs": len(batch), "seconds": time.perf_counter()-started, "tokens": response["usage"]["prompt_tokens"]})
        items = response["data"]
        if response["model"] != manifest["model"] or not isinstance(items, list) or len(items) != len(batch) or any(type(item["index"]) is not int for item in items) or sorted(item["index"] for item in items) != list(range(len(batch))):
            raise ValueError("embedding response identity/count/index mismatch")
        result.extend(ev.vector(item["embedding"], manifest["dimensions"]) for item in sorted(items, key=lambda x: x["index"]))
    return result


def baseline(documents, queries, directory):
    """Build the Go bridge and measure unchanged production scorer output."""
    binary = directory / "baseline"
    command(["go", "build", "-o", str(binary), "./test/search-relevance/baseline"])
    fields = ("id", "identifier", "name", "description", "tags", "representativeQueries")
    payload = {"documents": [{k: d[k] for k in fields} for d in documents], "queries": [{"id": q["id"], "text": q["text"]} for q in queries]}
    return json.loads(command([str(binary)], data=json.dumps(payload)))


def pgvector_parity(manifest, names, documents, queries, embeddings, query_vectors, threshold, lexical):
    """Compare exact SQL distances, filtered identities and facets; no app schema."""
    command(["docker", "run", "-d", "--pull=never", "--name", names["pg"], "--network", "none", "--cpus", "2", "--memory", "1g",
             "-e", "POSTGRES_HOST_AUTH_METHOD=trust", "--tmpfs", "/var/lib/postgresql:rw,nosuid,nodev,size=512m", manifest["postgres_image"]])
    for _ in range(120):
        try:
            command(["docker", "exec", names["pg"], "pg_isready", "-h", "127.0.0.1", "-U", "postgres"], timeout=5)
            break
        except RuntimeError:
            time.sleep(0.25)
    else:
        raise RuntimeError("parity database startup timeout")
    def sql(text):
        return command(["docker", "exec", "-i", names["pg"], "psql", "-h", "127.0.0.1", "-X", "-qAt", "-v", "ON_ERROR_STOP=1", "-U", "postgres"], data=text)
    def literal(text):
        return "'" + str(text).replace("'", "''") + "'"
    def vec(value):
        return literal(json.dumps(value)) + "::vector"
    dimensions = manifest["dimensions"]
    if type(dimensions) is not int or not 1 <= dimensions <= 16000:
        raise ValueError("invalid SQL vector dimension")
    sql(f"CREATE EXTENSION vector; CREATE TABLE documents (id text PRIMARY KEY, kind text, source text, public boolean, embedding vector({dimensions}));\n" + "\n".join(
        f"INSERT INTO documents VALUES ({literal(d['id'])},{literal(d['kind'])},{literal(d['source'])},{str(d['public']).lower()},{vec(embeddings[d['id']])});" for d in documents))
    max_error = 0.0
    # Reuse a real query vector with extra scope restrictions; these are SQL
    # correctness cases, not new held-out relevance judgments.
    scope_cases = [dict(queries[0], source=source) for source in ("local", "indexed")]
    for q in queries + scope_cases:
        for surface in ("control", "public"):
            conditions = ["true" if surface == "control" else "public"]
            if q.get("kind"):
                conditions.append("kind="+literal(q["kind"]))
            if q.get("source"):
                conditions.append("source="+literal(q["source"]))
            rows = json.loads(sql("SELECT coalesce(json_agg(t),'[]') FROM (SELECT id,kind,1-(embedding <=> " + vec(query_vectors[q["id"]]) + ") AS similarity FROM documents WHERE " + " AND ".join(conditions) + " ORDER BY similarity DESC, id) t;"))
            scope = ev.eligible(documents, q, surface)
            if {r["id"] for r in rows} != {d["id"] for d in scope}:
                raise ValueError("pgvector eligibility differs")
            expected = {d["id"]: ev.cosine(embeddings[d["id"]], query_vectors[q["id"]]) for d in scope}
            for row in rows:
                error = abs(row["similarity"]-expected[row["id"]])
                max_error = max(max_error, error)
                if error > manifest["numeric_tolerance"]:
                    raise ValueError("pgvector cosine differs")
            # Require exact membership at the frozen cutoff, not just close distances.
            actual_ids = {r["id"] for r in rows if r["similarity"] >= threshold}
            expected_ids = {i for i, score in expected.items() if score >= threshold}
            if actual_ids != expected_ids:
                raise ValueError("pgvector threshold membership differs")
            for left, right in zip(rows, rows[1:], strict=False):
                if expected[left["id"]] + manifest["numeric_tolerance"] < expected[right["id"]]:
                    raise ValueError("pgvector ranking differs outside numeric ties")
                if left["similarity"] == right["similarity"] and left["id"] > right["id"]:
                    raise ValueError("pgvector exact tie ordering differs")
            for kind in ev.KINDS:
                if sum(r["id"] in actual_ids and r["kind"] == kind for r in rows) != sum(d["id"] in expected_ids and d["kind"] == kind for d in scope):
                    raise ValueError("pgvector full-set facet differs")
            # Ask SQL for full lexical-OR-semantic counts, independently of the
            # returned page or Python's cosine/membership calculation.
            lex_ids = [d["id"] for d in scope if lexical[surface][q["id"]][d["id"]] > 0]
            lexical_sql = "id IN (" + ",".join(literal(i) for i in lex_ids) + ")" if lex_ids else "false"
            matched = "(" + lexical_sql + " OR 1-(embedding <=> " + vec(query_vectors[q["id"]]) + ") >= " + str(threshold) + ")"
            matched_set = json.loads(sql("WITH matched AS (SELECT id,kind FROM documents WHERE " + " AND ".join(conditions+[matched]) + ") SELECT json_build_object('ids',(SELECT coalesce(json_agg(id ORDER BY id),'[]') FROM matched),'facets',(SELECT coalesce(json_object_agg(kind,n),'{}') FROM (SELECT kind,count(*) AS n FROM matched GROUP BY kind) t));"))
            expected_union = expected_ids | set(lex_ids)
            expected_facets = {kind: sum(d["kind"] == kind and d["id"] in expected_union for d in scope) for kind in ev.KINDS}
            if matched_set["ids"] != sorted(expected_union) or matched_set["facets"] != {kind: n for kind, n in expected_facets.items() if n}:
                raise ValueError("SQL full hybrid membership/facets differ")
    first = [1.0] + [0.0]*383
    other = [0.0, 1.0] + [0.0]*382
    edge = json.loads(sql("SELECT json_build_object('identical',1-("+vec(first)+" <=> "+vec(first)+"),'orthogonal',1-("+vec(first)+" <=> "+vec(other)+"));"))
    if edge != {"identical": 1, "orthogonal": 0}:
        raise ValueError("exact cosine boundary differs")
    extension = sql("SELECT extversion FROM pg_extension WHERE extname='vector';").strip()
    server_version = sql("SHOW server_version_num;").strip()
    if extension != "0.8.7" or int(server_version)//10000 != 18:
        raise ValueError("unexpected PostgreSQL/pgvector baseline")
    return {"queries": len(queries), "extra_scope_cases": len(scope_cases), "exact_boundary_cases": edge, "surfaces": 2, "max_cosine_error": max_error, "extension": extension, "postgres_version": server_version, "approximate_indexes": False}


def evaluate(manifest, documents, queries):
    """Freeze tuning before held-out inference and retain failures as evidence."""
    verify_model(manifest)
    OUTPUT.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="run-", dir=OUTPUT))
    report = {"status": "running", "started": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
              "source": command(["git", "rev-parse", "HEAD"]).strip(),
              "inputs": {name: ev.digest(ev.ROOT/name) for name in ("manifest.json", "corpus.json", "queries.json", "evaluation.py", "runner.py", "baseline/main.go")},
              "model": manifest["model"], "revision": manifest["revision"], "trial_limits": {"cpus": 4, "memory_gib": 4}, "output": str(directory)}
    report["scorer_inputs"] = {path: ev.digest(REPO/path) for path in ("internal/catalog/search_profile.go", "internal/publication/manager.go")}
    save(directory/"inputs.json", report)
    try:
        lexical = baseline(documents, queries, directory)
        with owned_resources() as names:
            report["resources"] = names
            save(directory/"resources.json", names)
            started = time.perf_counter()
            origin, report["server"] = start_tei(manifest, names)
            report["startup_seconds"] = time.perf_counter()-started
            timings = []
            try:
                request(origin, "/v1/embeddings", {"model": manifest["model"], "input": "word "*600})
            except urllib.error.HTTPError as exc:
                if exc.code != 422:
                    raise
                report["overlength_rejected"] = True
            else:
                raise ValueError("overlength input was silently accepted")
            embeddings = dict(zip((d["id"] for d in documents), embed(origin, manifest, [ev.projection(d) for d in documents], timings), strict=True))
            tuning = [q for q in queries if q["split"] == "tune"]
            heldout = [q for q in queries if q["split"] == "test"]
            vectors = dict(zip((q["id"] for q in tuning), embed(origin, manifest, [manifest["query_prefix"]+q["text"] for q in tuning], timings), strict=True))
            similarities = {q["id"]: {d["id"]: ev.cosine(vectors[q["id"]], embeddings[d["id"]]) for d in documents} for q in tuning}
            threshold, grid = ev.tune(manifest, documents, tuning, lexical, similarities)
            frozen = {"threshold": threshold, "rrf_k": manifest["rrf_k"], "minimum_ndcg_gain": manifest["minimum_ndcg_gain"], "tuning": grid, "inputs": report["inputs"]}
            save(directory/"frozen.json", frozen)
            report["frozen_sha256"] = ev.digest(directory/"frozen.json")
            vectors.update(zip((q["id"] for q in heldout), embed(origin, manifest, [manifest["query_prefix"]+q["text"] for q in heldout], timings), strict=True))
            similarities.update({q["id"]: {d["id"]: ev.cosine(vectors[q["id"]], embeddings[d["id"]]) for d in documents} for q in heldout})
            report["threshold"] = threshold
            report["results"] = {s: ev.measure(documents, heldout, s, lexical[s], similarities, threshold, manifest["rrf_k"]) for s in ("control", "public")}
            report["acceptance"] = {s: ev.acceptance(report["results"][s], manifest["minimum_ndcg_gain"]) for s in report["results"]}
            report["recommendation"] = "implement" if all(all(v.values()) for v in report["acceptance"].values()) else "retain-lexical"
            report["embedding_batches"] = timings
            # Single uncached requests provide query latency separate from bulk indexing.
            single = []
            def single_query(q):
                result = embed(origin, manifest, [manifest["query_prefix"]+q["text"]], single)[0]
                if abs(ev.cosine(result, vectors[q["id"]])-1) > manifest["numeric_tolerance"]:
                    raise ValueError("concurrent single/batch inference disagrees")
            with ThreadPoolExecutor(max_workers=2) as pool:
                list(pool.map(single_query, heldout[:8]))
            report["single_query_samples"] = single
            report["query_concurrency"] = 2
            report["resource_sample"] = json.loads(command(["docker", "stats", "--no-stream", "--format", "{{json .}}", names["tei"]]))
            try:
                report["peak_memory_bytes"] = int(command(["docker", "exec", names["tei"], "cat", "/sys/fs/cgroup/memory.peak"]).strip())
            except (RuntimeError, ValueError):
                report["peak_memory_bytes"] = None
            report["pgvector"] = pgvector_parity(manifest, names, documents, queries, embeddings, vectors, threshold, lexical)
            verify_inputs(report)
            verify_model(manifest)
            save(directory/"vectors.json", {"documents": embeddings, "queries": vectors})
            save(directory/"lexical.json", lexical)
            report["status"] = "complete"
        report["cleanup"] = "complete"
    except BaseException as exc:
        report["status"] = "failed"
        report["error"] = str(exc)
        raise
    finally:
        save(directory/"report.json", report)
        print(f"Report: {directory / 'report.json'}", flush=True)
    return report


def main():
    """Keep install, service-free checks and real execution separate."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "check", "evaluate"))
    args = parser.parse_args()
    manifest, documents, queries = ev.fixtures()
    if args.action == "install":
        install(manifest)
    elif args.action == "check":
        print(command([sys.executable, "-m", "unittest", "discover", "-s", str(ev.ROOT), "-p", "test_*.py"]))
        print(command(["go", "test", "./test/search-relevance/baseline", "-shuffle=on", "-count=1"]))
    else:
        def interrupted(signum, frame):
            raise KeyboardInterrupt(f"signal {signum}")
        signal.signal(signal.SIGTERM, interrupted)
        result = evaluate(manifest, documents, queries)
        print(json.dumps({"status": result["status"], "recommendation": result["recommendation"], "acceptance": result["acceptance"]}, indent=2))


if __name__ == "__main__":
    main()
