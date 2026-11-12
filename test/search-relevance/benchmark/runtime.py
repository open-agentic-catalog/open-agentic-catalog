"""Concrete owned Compose fixture, reusing semantic setup and native seed assets."""

import contextlib
import importlib.util
import json
import os
import platform
from pathlib import Path
import re
import secrets
import shutil
import sys
import time
import uuid
import urllib.request

import evaluation as ev
import runner

ROOT = runner.REPO
HERE = Path(__file__).resolve().parent
REASONER_CACHE = ROOT / "output/langgraph-model"


def semantic_helpers():
    # Reuse actual command/readiness/source/cleanup helpers, not the full pilot.
    sys.path.insert(0, str(ROOT / "test/embeddings"))
    spec = importlib.util.spec_from_file_location("benchmark_semantic_fixture", ROOT / "test/embeddings/semantic.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def verify_reasoner(pin):
    """Verify the selected Ollama manifest and its actual content-addressed closure."""
    manifest = REASONER_CACHE / "models/manifests/registry.ollama.ai/library/qwen2.5/7b"
    runner.model_path(REASONER_CACHE, str(manifest.relative_to(REASONER_CACHE)))
    if not manifest.is_file():
        raise ValueError("missing pinned reasoning cache; follow the local-model installation guide")
    document = ev.load_json(manifest)
    if ev.digest(manifest) != pin["digest"]:
        # Ollama's current cache can retain the original manifest as an OCI
        # layer. Verify that exact pinned manifest and identical model closure.
        layers = [d for d in document["layers"] if d["digest"] == "sha256:" + pin["digest"]]
        if len(layers) != 1:
            raise ValueError("reasoning cache does not contain the pinned manifest")
        original = runner.model_path(REASONER_CACHE, "models/blobs/sha256-" + pin["digest"])
        if not original.is_file() or ev.digest(original) != pin["digest"]:
            raise ValueError("pinned reasoning manifest blob differs")
        pinned = ev.load_json(original)
        if document["config"] != pinned["config"] or [d for d in document["layers"] if d != layers[0]] != pinned["layers"]:
            raise ValueError("reasoning model closure differs from pinned manifest")
        document = pinned
    for descriptor in [document["config"], *document["layers"]]:
        digest = descriptor["digest"]
        if not re.fullmatch(r"sha256:[a-f0-9]{64}", digest):
            raise ValueError("invalid reasoning model blob")
        path = runner.model_path(REASONER_CACHE, "models/blobs/" + digest.replace(":", "-"))
        if not path.is_file() or path.stat().st_size != descriptor["size"] or ev.digest(path) != digest.removeprefix("sha256:"):
            raise ValueError("missing or changed reasoning model blob")


def cache(name):
    return runner.MODEL if name == "bge-small-v1" else runner.OUTPUT / "models" / name


def install_reasoner(pin):
    """Use the documented Ollama pull only in explicit acquisition, preserving caches."""
    manifest = REASONER_CACHE / "models/manifests/registry.ollama.ai/library/qwen2.5/7b"
    if manifest.exists():
        verify_reasoner(pin)
        return
    runner.model_path(REASONER_CACHE, ".")
    REASONER_CACHE.mkdir(parents=True, exist_ok=True, mode=0o700)
    name = "catalog-quality-acquire-" + uuid.uuid4().hex[:16]
    try:
        runner.command(["docker", "run", "-d", "--pull=never", "--name", name,
                        "--user", f"{os.getuid()}:{os.getgid()}", "--cpus", "4", "--memory", "8g",
                        "--cap-drop", "ALL", "--security-opt", "no-new-privileges",
                        "-e", "HOME=/model", "-e", "OLLAMA_MODELS=/model/models", "-e", "OLLAMA_HOST=0.0.0.0:11434",
                        "-e", "OLLAMA_NO_CLOUD=1", "--mount", f"type=bind,src={REASONER_CACHE},dst=/model", pin["runtimeImage"]])
        deadline = time.monotonic() + 60
        while True:
            try:
                runner.command(["docker", "exec", name, "ollama", "list"], timeout=10)
                break
            except RuntimeError:
                if time.monotonic() >= deadline:
                    raise
                time.sleep(.25)
        runner.command(["docker", "exec", name, "ollama", "pull", pin["model"]], timeout=3600)
        verify_reasoner(pin)
    finally:
        runner.command(["docker", "rm", "-f", "--volumes", name], timeout=90)


def preflight(profiles, pin):
    """Refuse missing offline inputs before creating any database or container."""
    if not (ROOT / "examples/langgraph/.venv/bin/python").is_file():
        raise ValueError("install the locked LangGraph environment with the benchmark install target")
    if Path(sys.prefix).resolve() != (ROOT / "examples/langgraph/.venv").resolve():
        raise ValueError("run evaluation through make search-relevance-benchmark and its locked environment")
    if shutil.disk_usage(ROOT).free < 16 << 30:
        raise ValueError("benchmark requires 16 GiB free disk; retained data and caches are preserved")
    info = json.loads(runner.command(["docker", "info", "--format", "{{json .}}"], timeout=30))
    if info["OSType"] != "linux" or info["Architecture"] not in ("x86_64", "amd64"):
        raise ValueError("this pinned CPU campaign requires a Linux AMD64 Docker engine; no implicit emulation")
    if info["MemTotal"] < 16 << 30:
        raise ValueError("benchmark requires a Docker engine with 16 GiB RAM")
    for name, profile in profiles.items():
        runner.verify_model(profile, cache(name))
    verify_reasoner(pin)
    images = {pin["runtimeImage"]} | {p[key] for p in profiles.values() for key in ("tei_image", "postgres_image")}
    return {"images": {image: runner.command(["docker", "image", "inspect", "--format={{.Id}}", image]).strip() for image in sorted(images)},
            "engine": {key: info[key] for key in ("OSType", "Architecture", "NCPU", "MemTotal", "ServerVersion")},
            "host": {"system": platform.system(), "machine": platform.machine(), "processor": platform.processor() or None},
            "tools": {"python": sys.version.split()[0], "go": runner.command(["go", "version"]).strip()},
            "framework": json.loads(runner.command([str(ROOT / "examples/langgraph/.venv/bin/python"), "test/langgraph/inventory.py"]))}


class CatalogFixture:
    """One empty database, fixed released identities, serial model configuration."""

    def __init__(self, directory, manifest, profiles, pin):
        self.helpers = semantic_helpers()
        self.directory, self.manifest, self.profiles, self.pin = directory, manifest, profiles, pin
        self.project = "catalog-quality-" + uuid.uuid4().hex[:16]
        self.environment = {k: os.environ[k] for k in ("PATH", "HOME", "DOCKER_HOST", "DOCKER_CONFIG", "XDG_RUNTIME_DIR") if k in os.environ}
        self.environment.update(CATALOG_ENV_FILE="/dev/null", GOTOOLCHAIN="local", GOWORK="off", GOENV="off", GOPROXY="off", GOSUMDB="off", GOTELEMETRY="off")
        self.config = ev.load_json(ROOT / "config/postgres.json")
        self.config["auth"]["developmentTokens"]["benchmark-reader"] = {
            **self.config["auth"]["developmentTokens"]["local-reader"], "subject": "benchmark-reader"}
        self.config_file = directory / "runtime.json"
        # Bootstrap uses the standard config; only API/worker mount this reader.
        self.config_file.write_text(json.dumps(self.config))
        self.ports = {k: self.helpers.available_port() for k in ("api", "database", "embedding", "reasoner")}
        self.origin = f"http://127.0.0.1:{self.ports['api']}"
        self.model_origin = f"http://127.0.0.1:{self.ports['embedding']}"
        self.reasoner_origin = f"http://127.0.0.1:{self.ports['reasoner']}"
        self.subnet = f"172.29.{secrets.randbelow(256)}.0/24"
        self.environment.update(IMAGE="open-agentic-catalog:quality-" + self.project.removeprefix("catalog-quality-"), VERSION="quality-test",
                                COMMIT=runner.command(["git", "rev-parse", "HEAD"]).strip(), SOURCE_STATE="modified",
                                CATALOG_COMPOSE_HTTP_PORT=str(self.ports["api"]), CATALOG_COMPOSE_POSTGRES_PORT=str(self.ports["database"]),
                                CATALOG_COMPOSE_SUBNET=self.subnet, CATALOG_WEBHOOKS_ENABLED="false", CATALOG_BENCHMARK_CONFIG=str(self.config_file),
                                CATALOG_BENCHMARK_EMBEDDING_PORT=str(self.ports["embedding"]), CATALOG_BENCHMARK_REASONER_PORT=str(self.ports["reasoner"]),
                                CATALOG_BENCHMARK_REASONER_IMAGE=pin["runtimeImage"], CATALOG_BENCHMARK_REASONER_CACHE=str(REASONER_CACHE),
                                CATALOG_BENCHMARK_UID=str(os.getuid()), CATALOG_BENCHMARK_GID=str(os.getgid()),
                                CATALOG_WORKER_LEASE_DURATION="60s", CATALOG_HTTP_REQUEST_TIMEOUT="20s", CATALOG_HTTP_WRITE_TIMEOUT="45s", CATALOG_HTTP_SHUTDOWN_TIMEOUT="45s", CATALOG_SEMANTIC_SEARCH='{"enabled":false}')
        self.compose = ["docker", "compose", "--env-file", "/dev/null", "-f", "compose.yaml", "-f", "compose.embeddings.yaml",
                        "-f", "test/embeddings/semantic.compose.yaml", "-f", "test/search-relevance/benchmark/runtime.compose.yaml", "-p", self.project]
        self.profile_environment("minilm-l6-v1")
        self.deadline = time.monotonic() + manifest["run_seconds"]
        self.inventory = None
        self.context = None
        self.events = []
        self.source = self.helpers.performance.source_identity(self.environment)
        resolved = directory / "compose.resolved.json"
        runner.save(resolved, json.loads(self.command(self.compose + ["--profile", "agent", "config", "--format", "json"])))
        resolved.chmod(0o600)
        stop = ["docker", "compose", "--env-file", "/dev/null", "-f", str(resolved), "-p", self.project, "--profile", "agent", "down", "--volumes", "--remove-orphans"]
        runner.save(directory / "resources.json", {"project": self.project, "ports": self.ports, "image": self.environment["IMAGE"],
                    "cacheRetained": str(REASONER_CACHE), "stopCommand": stop, "removeImageCommand": ["docker", "image", "rm", self.environment["IMAGE"]]})

    def guard(self):
        if time.monotonic() >= self.deadline:
            raise TimeoutError("benchmark total execution budget exhausted")

    def model_guard(self):
        self.guard()
        if not self.command(self.compose + ["ps", "--status", "running", "--quiet", "embeddings"]).strip():
            raise ValueError("owned embedding server exited before qualification")

    def command(self, args, seconds=180):
        self.guard()
        return self.helpers.command(args, self.environment, timeout=min(seconds, max(1, int(self.deadline-time.monotonic()))))

    def profile_environment(self, name):
        profile = self.profiles[name]
        self.environment.update(CATALOG_EMBEDDING_IMAGE=profile["tei_image"], CATALOG_EMBEDDING_MODEL=profile["model"],
                                CATALOG_EMBEDDING_REVISION=profile["revision"], CATALOG_EMBEDDING_POOLING=profile["pooling"],
                                CATALOG_EMBEDDING_MODEL_DIRECTORY=str(cache(name)))

    def start(self, documents):
        self.command(["go", "build", "-o", str(self.directory / "seed"), "./test/search-relevance/benchmark/seed"], 240)
        self.command(["go", "build", "-o", str(self.directory / "qualify"), "./test/embeddings/qualify"], 240)
        self.command(self.compose + ["build", "migrate"], self.manifest["startup_seconds"])
        self.command(self.compose + ["up", "-d", "--no-build", "--wait", "--wait-timeout", str(self.manifest["startup_seconds"]), "postgres", "semantic-setup", "migrate", "provision", "api", "worker"], self.manifest["startup_seconds"])
        self.wait_ready()
        # Reuse the native stdin boundary and bounded command helper.
        self.inventory = json.loads(runner.command([str(self.directory / "seed")], data=json.dumps({"port": self.ports["database"], "documents": documents}), timeout=660))
        if set(self.inventory["records"]) != {d["id"] for d in documents}:
            raise ValueError("seeded corpus identity mismatch")
        for d in documents:
            if self.inventory["records"][d["id"]]["projection"] != ev.projection(d):
                raise ValueError("offline projection differs from sealed Go metadata")
        runner.save(self.directory / "inventory.json", self.inventory)
        self.by_release = {v["releaseId"]: k for k, v in self.inventory["records"].items()}
        self.kind_by_release = {self.inventory["records"][d["id"]]["releaseId"]: d["kind"] for d in documents}
        if len(self.by_release) != len(documents):
            raise ValueError("duplicate real release identity")

    def request(self, path, body=None, token="benchmark-reader", key="", expected=200):
        self.guard()
        return self.helpers.request(self.origin, path, body, token=token, key=key, expected=expected)

    def wait_ready(self):
        return self.helpers.wait_until(lambda: self.request("/api/v1/capabilities", token="local-admin")[0],
                                       lambda x: bool(x.get("search")), seconds=self.manifest["startup_seconds"], guard=self.guard)

    def configure(self, name, cutoff=None):
        self.guard()
        started = time.perf_counter()
        self.context = None
        if name == "lexical":
            settings = {"enabled": False}
            self.command(self.compose + ["stop", "embeddings"])
        else:
            self.profile_environment(name)
            profile = self.profiles[name]
            runner.verify_model(profile, cache(name))
            settings = {"enabled": True, "endpoint": "http://embeddings:80/v1/embeddings", "model": profile["model"],
                        "profileRevision": ev.digest(ROOT / "deployments/embeddings/profiles" / (name + ".json")),
                        "dimensions": profile["dimensions"], "similarityCutoff": cutoff, "queryPrefix": profile.get("query_prefix", ""),
                        "documentPrefix": profile.get("document_prefix", ""), "batchSize": 8, "maxInputBytes": 32768,
                        "queryTimeout": "10s", "batchTimeout": "20s", "allowHttp": True, "allowedAddressPrefixes": [self.subnet]}
            self.command(self.compose + ["up", "-d", "--no-build", "--no-deps", "embeddings"], self.manifest["startup_seconds"])
            info = self.helpers.wait_until(lambda: runner.request(self.model_origin, "/info"), lambda x: bool(x), seconds=self.manifest["startup_seconds"], guard=self.model_guard)
            if (info["served_model_name"] != profile["model"] or info["model_sha"] != profile["revision"]
                    or info["model_dtype"] != "float32" or info["auto_truncate"] or info["version"] != "1.9.4"
                    or info["max_input_length"] != profile["max_input_length"] or info["model_type"] != {"embedding": {"pooling": profile["pooling"]}}):
                raise ValueError("actual embedding server differs from pinned profile")
            self.server_info = info
        self.environment["CATALOG_SEMANTIC_SEARCH"] = json.dumps(settings)
        self.command(self.compose + ["up", "-d", "--no-build", "--no-deps", "--force-recreate", "--wait", "--wait-timeout", str(self.manifest["startup_seconds"]), "api", "worker"], self.manifest["startup_seconds"])
        self.wait_ready()
        if name != "lexical":
            def index_status():
                result = self.request("/api/v1/capabilities", token="local-admin")[0]
                status = result["search"].get("indexing", {})
                if status.get("state") == "failed" or status.get("failed", 0):
                    raise ValueError("owned semantic index rejected the common-fit corpus; inspect the run's Jobs and logs")
                return result
            ready = self.helpers.wait_until(index_status,
                     lambda x: x["search"].get("indexing", {}).get("state") == "ready", seconds=self.manifest["index_seconds"], guard=self.guard)
            status = ready["search"]["indexing"]
            self.generation_job = str(uuid.UUID(status["jobId"]))
            if status["failed"] or status["indexed"] != self.inventory["indexedReleases"]:
                raise ValueError("incomplete common-fit semantic generation")
        self.arm = name
        self.settings = settings
        probe = {"id": "ready", "text": "benchmark readiness probe", "kind": None}
        self.search(probe)
        result = {"arm": name, "settings": settings, "setup_seconds": time.perf_counter()-started,
                  "setup_scope": "cache verification, model/runtime startup and index readiness",
                  "response_context": self.context, "containers": self.container_state()}
        self.events.append(result)
        return result

    def container_state(self):
        names = self.command(self.compose + ["ps", "--quiet"]).splitlines()
        return [{"id": value["Id"], "image": value["Image"], "restarts": value["RestartCount"], "oom": value["State"]["OOMKilled"]}
                for value in json.loads(self.command(["docker", "inspect", *names]))]

    def observe_page(self, page, model_free=False, kinds=()):
        tier = "core" if self.arm == "lexical" or model_free else "hybrid"
        if (page["tier"] != tier or page.get("fallbackReason") or page.get("partialIndex") or page["partial"] or page["liveCount"]
                or len({x["id"] for x in page["items"]}) != len(page["items"])):
            raise ValueError("unexpected tier, fallback, incomplete index or repeated results")
        if (tier == "core" and page["profile"] != "core-lexical-v1") or (tier == "hybrid" and re.fullmatch(r"sha256:[a-f0-9]{64}", page["profile"]) is None):
            raise ValueError("unexpected actual ranking profile")
        for item in page["items"]:
            if item["type"] != "local" or item["local"]["release"]["id"] not in self.by_release:
                raise ValueError("forbidden or unmapped identity disclosed by Catalog")
            release = item["local"]["release"]["id"]
            if item["id"] != "local:" + release or item["kind"] != self.kind_by_release[release]:
                raise ValueError("Catalog result identity or retained kind mismatch")
            if kinds and item["kind"] not in kinds:
                raise ValueError("Catalog ignored an explicit kind filter")
        if not model_free:
            context = {k: page[k] for k in ("tier", "profile", "generation")}
            if self.context is None:
                self.context = context
            elif context != self.context:
                raise ValueError("ranking/profile/generation changed during arm")

    def search(self, query, token="benchmark-reader"):
        body = {"text": query["text"], "origins": ["local"], "namespaceId": self.inventory["namespaceId"], "limit": 10}
        if query.get("kind"):
            body["kinds"] = [query["kind"]]
        started = time.perf_counter()
        page, _ = self.request("/api/v1/catalog/search", body, token=token)
        duration = time.perf_counter()-started
        self.observe_page(page, query.get("exact", False), [query["kind"]] if query.get("kind") else ())
        ranked = [self.by_release[x["local"]["release"]["id"]] for x in page["items"]]
        return ranked, {"query": query["id"], "seconds": duration, "response": page}

    def controls(self, documents):
        for token in ("other-tenant-admin", "local-template-service-client"):
            page, _ = self.request("/api/v1/catalog/search", {"origins": ["local"], "limit": 10}, token=token)
            if page["items"] or page["durableTotal"]:
                raise ValueError("forbidden tenant/grantless disclosure")
        self.request("/api/v1/catalog/resources/" + self.inventory["excluded"]["releaseId"], expected=403)
        first = self.inventory["records"][documents[0]["id"]]["releaseId"]
        self.request("/api/v1/catalog/resources/" + first, token="other-tenant-admin", expected=404)
        page, _ = self.request("/api/v1/catalog/search", {"text": "urn:air:catalog.example.test:benchmark:draft", "origins": ["local"]})
        if page["items"]:
            raise ValueError("unreleased resource entered discovery")
        return {"other_tenant": "denied", "grantless": "denied", "excluded_namespace_exact_read": "denied", "draft": "excluded"}

    def start_reasoner(self):
        self.command(self.compose + ["--profile", "agent", "up", "-d", "--no-deps", "reasoner"], self.manifest["startup_seconds"])
        self.helpers.wait_until(lambda: runner.request(self.reasoner_origin, "/api/tags"), lambda x: bool(x.get("models")),
                                seconds=self.manifest["startup_seconds"], guard=self.guard)
        self.reasoner_reference = self.reasoner_state()
        return self.reasoner_origin

    def reasoner_state(self):
        identifier = self.command(self.compose + ["--profile", "agent", "ps", "--all", "--quiet", "reasoner"]).strip()
        if not identifier:
            raise ValueError("owned reasoning service is missing")
        value = json.loads(self.command(["docker", "inspect", identifier]))[0]
        if not value["State"]["Running"] or value["State"]["OOMKilled"]:
            raise ValueError("owned reasoning service stopped or exhausted memory")
        return {"id": value["Id"], "started": value["State"]["StartedAt"], "restarts": value["RestartCount"]}

    def require_reasoner(self):
        if self.reasoner_state() != self.reasoner_reference:
            raise ValueError("reasoning service restarted during the campaign")

    def resources(self):
        ids = self.command(self.compose + ["ps", "--quiet"]).splitlines()
        sample = [json.loads(line) for line in self.command(["docker", "stats", "--no-stream", "--format", "{{json .}}", *ids]).splitlines()]
        peaks = {}
        for service in ("embeddings", "reasoner"):
            try:
                peaks[service] = int(self.command(self.compose + ["exec", "-T", service, "cat", "/sys/fs/cgroup/memory.peak"]).strip())
            except (RuntimeError, ValueError):
                peaks[service] = None
        return {"point_samples": sample, "process_lifetime_peak_bytes": peaks, "limits": {"embedding_cpus": 4, "embedding_memory_gib": 4, "reasoner_cpus": 4, "reasoner_memory_gib": 8}}

    def storage(self):
        # Whole-table overhead is cumulative, not a per-model storage claim.
        sql = "SELECT json_build_object('vectors',pg_total_relation_size('semantic_vectors'),'members',pg_total_relation_size('semantic_members'),'generations',pg_total_relation_size('semantic_generations'))"
        result = {"all_profiles_relation_bytes": json.loads(self.command(self.compose + ["exec", "-T", "postgres", "psql", "-U", "catalog", "-d", "catalog", "-At", "-c", sql]).strip())}
        if self.arm != "lexical":
            job = str(uuid.UUID(self.generation_job))
            sql = ("SELECT json_build_object('profile',g.profile,'dimensions',g.dimensions,'vectors',count(v.*),'embedding_payload_bytes',coalesce(sum(pg_column_size(v.embedding)),0)) "
                   "FROM semantic_generations g LEFT JOIN semantic_vectors v ON v.tenant_id=g.tenant_id AND v.profile=g.profile AND v.disclosure=g.disclosure "
                   "WHERE g.id='" + job + "'::uuid AND g.scope='control' GROUP BY g.profile,g.dimensions")
            result["active_profile"] = json.loads(self.command(self.compose + ["exec", "-T", "postgres", "psql", "-U", "catalog", "-d", "catalog", "-At", "-c", sql]).strip())
        return result

    def query_operations(self):
        req = urllib.request.Request(self.origin + "/metrics", headers={"Authorization": "Bearer local-admin"})
        with urllib.request.build_opener(urllib.request.ProxyHandler({}), self.helpers.NoRedirect()).open(req, timeout=10) as response:
            raw = response.read((2 << 20) + 1)
        if len(raw) > 2 << 20:
            raise ValueError("over-limit owned metric response")
        values = re.findall(r'^open_agentic_catalog_embedding_request_duration_seconds_count\{operation="query",outcome="[a-z_]+"\} ([0-9.]+)$', raw.decode(), re.MULTILINE)
        if self.arm != "lexical" and not values:
            raise ValueError("query embedding operation metric is unavailable")
        return sum(float(x) for x in values)

    def exact_controls(self, queries):
        before_state = self.container_state()
        before = self.query_operations()
        for query in queries:
            ranked, _ = self.search(query)
            if not ranked or ranked[0] not in query["relevant"]:
                raise ValueError("exact AIR identity was not preserved")
        after = self.query_operations()
        if before != after or before_state != self.container_state():
            raise ValueError("exact AIR control invoked embeddings or process state changed")
        return {"queries": len(queries), "query_embedding_operations_added": after-before, "container_state_unchanged": True}

    def cleanup(self):
        # Cleanup has its own finite budget even after the campaign expires.
        errors = []
        try:
            logs = self.helpers.command(self.compose + ["--profile", "agent", "logs", "--no-color", "--tail", "150"], self.environment, timeout=30)
            (self.directory / "container.log").write_text(logs)
        except Exception:
            pass
        for args in (self.compose + ["--profile", "agent", "down", "--volumes", "--remove-orphans"],):
            try:
                self.helpers.command(args, self.environment, timeout=120)
            except Exception as exc:
                errors.append(str(exc))
        try:
            self.helpers.command(["docker", "image", "inspect", self.environment["IMAGE"]], self.environment, timeout=15)
        except Exception as exc:
            if "no such image" not in str(exc).lower():
                errors.append(str(exc))
        else:
            try:
                self.helpers.command(["docker", "image", "rm", self.environment["IMAGE"]], self.environment, timeout=30)
            except Exception as exc:
                errors.append(str(exc))
        for kind in ("container", "volume", "network"):
            try:
                args = ["docker", "ps", "-a", "--quiet"] if kind == "container" else ["docker", kind, "ls", "--quiet"]
                remaining = self.helpers.command(args + ["--filter", "label=com.docker.compose.project=" + self.project], self.environment, timeout=15)
                if remaining.strip():
                    errors.append("owned " + kind + " remained after cleanup")
            except Exception as exc:
                errors.append(str(exc))
        runner.save(self.directory / "cleanup.json", {"status": "failed" if errors else "complete", "errors": errors})
        if errors:
            raise RuntimeError("owned benchmark cleanup failed: " + "; ".join(errors))


@contextlib.contextmanager
def catalog_fixture(directory, manifest, profiles, pin, documents):
    fixture = CatalogFixture(directory, manifest, profiles, pin)
    try:
        fixture.start(documents)
        yield fixture
    finally:
        with runner.finish_cleanup_before_interrupt():
            fixture.cleanup()
