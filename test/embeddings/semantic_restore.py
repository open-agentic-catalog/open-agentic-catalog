"""Small semantic backup preparation, using the existing supervisor and helpers."""

import hashlib
import importlib.util
import json
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[2]


def helper(name):
    spec = importlib.util.spec_from_file_location("semantic_restore_" + name, ROOT / "test/functional" / (name + ".py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


archive_tools = helper("postgres_archive")
pages = helper("restore_pages")
TABLES = ("catalog_semantic_schema", "semantic_generations", "semantic_vectors", "semantic_members")


def validate_baseline(expected, hybrid, lexical):
    """A nonempty, exact semantic-only match is required before the dump."""
    if (len(expected) != 3 or not hybrid or not hybrid.items() <= expected.items() or lexical):
        raise RuntimeError("semantic-restore-baseline-missing")


def prepare(run, compose, environment, directory, baseline):
    """Stop the source, restore an owned destination and verify before startup."""
    def execute(*args, capture=False, limit=1 << 20):
        try:
            output = run(compose + list(args), environment, timeout=90)
        except Exception:
            raise RuntimeError("semantic-restore-command-failed") from None
        raw = output.encode()
        if len(raw) > limit:
            raise RuntimeError("semantic-restore-command-output-limit")
        return raw if capture else None

    def sql(statement, service="postgres", database="catalog"):
        return execute("exec", "-T", service, "psql", "-X", "-U", "catalog", "-d", database,
                       "-v", "ON_ERROR_STOP=1", "-Atc", "SET statement_timeout='15s'; " + statement,
                       capture=True).decode().strip().removeprefix("SET\n")

    def ctl(service, *args):
        return json.loads(execute("run", "--rm", "--no-deps", "-e",
            "CATALOG_DATABASE_DSN=postgres://catalog:catalog@" + service + ":5432/catalog?sslmode=disable",
            "-e", "CATALOG_SEMANTIC_SEARCH=" + environment["CATALOG_SEMANTIC_SEARCH"],
            "migrate", *args, capture=True))

    def stopped(*services):
        for service in services:
            if execute("ps", "--status", "running", "-q", service, capture=True).strip():
                raise RuntimeError("semantic-restore-source-not-stopped")

    def inventory(service):
        result = {}
        for table in TABLES:
            # Fixed table names, bounded fixture rows and exact vector bytes.
            rows = json.loads(sql("SELECT json_build_array(count(*),encode(sha256(convert_to("
                "coalesce(string_agg(to_jsonb(v)::text,E'\\n' ORDER BY to_jsonb(v)::text),''),'UTF8')),'hex')) "
                "FROM (SELECT * FROM public." + table + " LIMIT 10001) v", service))
            if not 0 < rows[0] <= 10000 or not re.fullmatch(r"[0-9a-f]{64}", rows[1]):
                raise RuntimeError("semantic-restore-invalid-inventory")
            result[table] = {"rows": rows[0], "digest": "sha256:" + rows[1]}
        return result

    validate_baseline(baseline["expected"], baseline["hybrid"], baseline["lexical"])
    execute("stop", "--timeout", "20", "api", "worker")
    stopped("api", "worker")
    before = inventory("postgres")
    versions = json.loads(sql("SELECT json_build_object('postgres',current_setting('server_version_num'),"
        "'vector',(SELECT extversion FROM pg_extension WHERE extname='vector'))"))
    tenants = json.loads(sql("SELECT json_agg(id ORDER BY id) FROM tenants"))
    if not tenants or len(tenants) > 10:
        raise RuntimeError("semantic-restore-invalid-tenants")
    plan = ctl("postgres", "migrate", "plan", "--semantic", "--config", "/config/config.json")
    dump = Path(directory) / "semantic-backup.dump"
    with archive_tools.temporary_archive(execute, "postgres", error_type=RuntimeError) as archive:
        archive_tools.dump_archive(execute, "postgres", archive)
        size = int(execute("exec", "-T", "postgres", "stat", "-c", "%s", archive, capture=True))
        if not 0 < size <= pages.MAX_DUMP:
            raise RuntimeError("semantic-restore-dump-limit")
        execute("cp", "postgres:" + archive, str(dump))
    dump.chmod(0o600)
    raw = dump.read_bytes()
    if len(raw) != size:
        raise RuntimeError("semantic-restore-dump-size-mismatch")
    execute("stop", "--timeout", "20", "postgres")
    stopped("api", "worker", "postgres")
    execute("up", "--no-deps", "--detach", "--wait", "--wait-timeout", "60", "restored")
    if sql("SELECT count(*) FROM pg_database WHERE datname='catalog'", "restored", "postgres") != "0":
        raise RuntimeError("semantic-restore-destination-not-empty")
    sql("CREATE ROLE catalog_api LOGIN PASSWORD 'catalog-api' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS; "
        "CREATE ROLE catalog_worker LOGIN PASSWORD 'catalog-worker' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION BYPASSRLS",
        "restored", "postgres")
    execute("exec", "-T", "restored", "createdb", "-U", "catalog", "--template", "template0", "--owner", "catalog", "catalog")
    sql("GRANT CONNECT ON DATABASE catalog TO catalog_api,catalog_worker; CREATE EXTENSION vector", "restored")
    with archive_tools.temporary_archive(execute, "restored", error_type=RuntimeError) as archive:
        execute("cp", str(dump), "restored:" + archive)
        execute("exec", "-T", "restored", "pg_restore", "-U", "catalog", "-d", "catalog", "--single-transaction", archive)
    if ctl("restored", "migrate", "plan", "--semantic", "--config", "/config/config.json") != plan:
        raise RuntimeError("semantic-restore-migration-plan-changed")
    applied = ctl("restored", "migrate", "apply", "--semantic", "--config", "/config/config.json",
                  "--api-role", "catalog_api", "--worker-role", "catalog_worker")
    privileges = applied["runtimePrivileges"]
    if applied["applied"] or len(privileges) != 2 or not all(p["compliant"] is True for p in privileges):
        raise RuntimeError("semantic-restore-privileges-invalid")
    after = inventory("restored")
    if after != before or versions != json.loads(sql("SELECT json_build_object('postgres',current_setting('server_version_num'),"
        "'vector',(SELECT extversion FROM pg_extension WHERE extname='vector'))", "restored")):
        raise RuntimeError("semantic-restore-derived-state-changed")
    point = "semantic-restore-preparation"
    counts = []
    for tenant in tenants:
        diagnostic = {}
        def read(cursor):
            args = ["restore", "verify", "--config", "/config/config.json", "--tenant", tenant,
                    "--recovery-point", point, "--api-role", "catalog_api", "--worker-role", "catalog_worker", "--limit", str(pages.PAGE_LIMIT)]
            if cursor:
                args += ["--cursor", cursor]
            value = ctl("restored", *args)
            diagnostic.update({key: value.get(key) for key in ("formatVersion", "checkSetVersion", "schemaVersion", "privilegeManifestVersion", "traversal", "integrity", "processed")})
            diagnostic["findings"] = [item.get("code") for item in value.get("findings", [])]
            return value
        try:
            count, _ = pages.verify_chain(read, tenant, point, plan["core"][-1]["version"], privileges[0]["manifestVersion"], minimum_pages=1)
        except ValueError:
            raise RuntimeError("semantic-restore-verification-refused: " + json.dumps(diagnostic, sort_keys=True)) from None
        counts.append(count)
    if max(counts) < 2:
        raise RuntimeError("semantic-restore-pagination-not-exercised")
    stopped("api", "worker", "postgres")
    return {"stage": "restored-before-runtime", "sourceStopped": True, "destinationVerified": True,
            "dumpBytes": size, "dumpDigest": "sha256:" + hashlib.sha256(raw).hexdigest(),
            "semanticTables": after, "versions": versions, "verificationPages": counts,
            "runtimePrivileges": privileges, "baseline": baseline}


def verify_search(origin, baseline, request, control_page):
    """Bind restored results and permission denials to the pre-backup fixture."""
    actual = {}
    for label, token in (("administrator", "local-admin"), ("reader", "local-reader")):
        page, _ = request(origin, "/api/v1/catalog/search", baseline["query"], token=token)
        found = control_page(page, len(baseline["hybrid"]), len(baseline["hybrid"]))
        if found != baseline["hybrid"] or page.get("nextCursor") or page["profile"] != baseline["profile"]:
            raise RuntimeError("semantic-restore-search-identities-changed")
        actual[label] = found
    for token in ("local-template-service-client", "other-tenant-admin"):
        page, _ = request(origin, "/api/v1/catalog/search", baseline["query"], token=token)
        if page.get("items") != [] or page.get("durableTotal") != 0 or page.get("nextCursor") or page.get("partial"):
            raise RuntimeError("semantic-restore-denied-search-leaked")
    page, _ = request(origin, "/api/v1/catalog/search", {"text": "order", "limit": 3})
    if control_page(page, 3, 3) != baseline["expected"] or page.get("nextCursor"):
        raise RuntimeError("semantic-restore-complete-membership-changed")
    return actual


def verify_documents(origin, baseline, request):
    """Compare authoritative API records and immutable publication evidence."""
    for path, saved in baseline["documents"].items():
        value, headers = request(origin, path)
        etag = next((value for name, value in headers.items() if name.lower() == "etag"), "")
        if value != saved["document"] or etag != saved["etag"]:
            raise RuntimeError("semantic-restore-authoritative-document-changed")
        if path.startswith("/api/v1/resources/") or path.startswith("/api/v1/catalog/resources/"):
            request(origin, path, token="other-tenant-admin", expected=404)


def verify_reindex_job(job, identifier):
    """A succeeded HTTP request alone does not prove a completed index rebuild."""
    if (job.get("id") != identifier or job.get("kind") != "semantic.reindex" or job.get("state") != "succeeded"
            or job.get("result") != {"type": "semantic_index", "generationId": identifier, "members": 3}):
        raise RuntimeError("semantic-restore-reindex-not-complete")


def resume(run, compose, environment, report, origin, request, wait_until, control_page):
    """Start only verified restored runtimes, then test the supported rebuild."""
    if report.get("stage") != "restored-before-runtime" or report.get("destinationVerified") is not True or report.get("sourceStopped") is not True:
        raise RuntimeError("semantic-restore-not-verified")
    restored_compose = list(compose)
    restored_compose[-2:-2] = ["-f", "test/embeddings/semantic_restored_runtime.compose.yaml"]

    def stopped_source():
        if run(restored_compose + ["ps", "--status", "running", "-q", "postgres"], environment).strip():
            raise RuntimeError("semantic-restore-original-restarted")

    def start(service):
        stopped_source()
        run(restored_compose + ["up", "--no-deps", "--force-recreate", "--detach", "--wait", "--wait-timeout", "60", service], environment)
        stopped_source()

    baseline = report["baseline"]
    start("api")
    # The original worker stays stopped until restored vectors answer real queries.
    if run(restored_compose + ["ps", "--status", "running", "-q", "worker"], environment).strip():
        raise RuntimeError("semantic-restore-worker-started-before-search")
    before = verify_search(origin, baseline, request, control_page)
    verify_documents(origin, baseline, request)
    request(origin, "/api/v1/catalog:reindex", {"reason": "Verify restored semantic index"},
            token="local-reader", key="semantic-restore-reader-refusal", expected=403)
    start("worker")
    job, _ = request(origin, "/api/v1/catalog:reindex", {"reason": "Rebuild restored semantic index"},
                     key="semantic-restore-rebuild-control", expected=202)
    if not isinstance(job.get("id"), str) or not re.fullmatch(r"[0-9a-f-]{36}", job["id"]) or job.get("kind") != "semantic.reindex":
        raise RuntimeError("semantic-restore-reindex-receipt-invalid")
    completed = wait_until(lambda: request(origin, "/api/v1/jobs/" + job["id"])[0],
                           lambda value: value.get("state") in ("succeeded", "failed", "cancelled"))
    verify_reindex_job(completed, job["id"])
    status, _ = request(origin, "/api/v1/capabilities")
    index = status["search"].get("indexing", {})
    if index != {"state": "ready", "jobId": job["id"], "indexed": 3, "failed": 0}:
        raise RuntimeError("semantic-restore-rebuilt-generation-not-active")
    after = verify_search(origin, baseline, request, control_page)
    verify_documents(origin, baseline, request)
    stopped_source()
    return {**report, "stage": "restored-search-and-reindex-complete", "searchBeforeWorker": before,
            "searchAfterReindex": after, "reindexJob": completed["id"], "authoritativeDocumentsUnchanged": True}


def cleanup(run, compose, environment):
    """Attempt each owned cleanup even if another cleanup action fails."""
    errors = []
    actions = [compose + ["--profile", "semantic-restore", "down", "--volumes", "--remove-orphans"],
               ["docker", "image", "rm", environment["IMAGE"]]]
    project = compose[-1]
    for args in actions:
        try:
            run(args, environment, timeout=120)
        except Exception:
            errors.append("remove-owned-resources")
    for kind in ("container", "volume", "network"):
        try:
            if run(["docker", kind, "ls"] + (["--all"] if kind == "container" else []) + ["--quiet", "--filter", "label=com.docker.compose.project=" + project], environment).strip():
                errors.append("owned-resources-remain")
        except Exception:
            errors.append("inspect-owned-resources")
    if errors:
        raise RuntimeError("semantic-restore-cleanup-failed: " + ",".join(sorted(set(errors))))
