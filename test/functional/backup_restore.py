#!/usr/bin/env python3
"""Logical restore and shared Catalog recovery checks in the owned stack.

Raw dumps, audit files and row data never enter the public report. SQL inventory
hashing is bounded to this small synthetic fixture, not a production backup API.
"""

import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import sys
import time

import audit
import authorization_recovery
import backup_ratings
import restore_pages
from core import exact_record, mutate, records
from federation import Client, sync, wait_job, wait_until
from http_client import decode
from journeys import header, namespace_state
from postgres_archive import dump_archive, temporary_archive
from peers import prepare_plugin_archives
from runner import DIGEST, Failure, command, read_document, record_scenario_failure, write_document
from webhooks import Journey, TENANT


OTHER = "00000000-0000-7000-8000-000000000002"
TENANTS = (TENANT, OTHER)
PHASES = ("baseline", "capture", "dump", "restore", "verify", "runtime", "complete")
PAGE_LIMIT = restore_pages.PAGE_LIMIT
MAX_PAGES = restore_pages.MAX_PAGES
MAX_DUMP = restore_pages.MAX_DUMP
ALLOWED_FINDINGS = restore_pages.ALLOWED_FINDINGS
FINDING_DOMAINS = restore_pages.FINDING_DOMAINS


def fingerprint(value):
    return "sha256:" + hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def require_search_baseline(results):
    """Equal empty responses are not evidence of recovered indexed Search."""
    if (len(results) != 2 or any(result.get("partial") is not False or result.get("tier") != "core" for result in results)
            or len(results[0]["items"]) != 3 or any(item["type"] != "local" for item in results[0]["items"])
            or not any(item["type"] == "indexed" for item in results[1]["items"])):
        raise Failure("backup-search-baseline-missing")


def valid_evidence(value, *, physical=False):
    """Only complete bounded, non-sensitive conclusions can become a pass."""
    digest_field, size_field = ("backupManifestDigest", "backupBytes") if physical else ("dumpDigest", "dumpBytes")
    fields = {digest_field, "inventoryDigest", "bindingDigest", size_field, "tenantCount",
              "verificationPages", "auditRows", "postgresVersions", "sourceStopped",
              "retainedState", "replay", "jobRecovered", "signedDelivery", "policyRetained", "tenantSettingsRetained",
              "pluginRetained", "personalItemsRetained", "ratingsRetained", "archiveStopped"}
    return (isinstance(value, dict) and set(value) == fields
            and all(isinstance(value[k], str) and DIGEST.fullmatch(value[k]) for k in (digest_field, "inventoryDigest", "bindingDigest"))
            and type(value[size_field]) is int and 0 < value[size_field] <= (512 << 20 if physical else MAX_DUMP)
            and type(value["tenantCount"]) is int and value["tenantCount"] == 2
            and all(isinstance(value[k], list) and len(value[k]) == 2
                    and all(type(n) is int and lo <= n <= hi for n in value[k])
                    for k, lo, hi in (("verificationPages", 2, MAX_PAGES), ("auditRows", 1, 1000),
                                      ("postgresVersions", 180000, 189999)))
            and value["postgresVersions"][0] == value["postgresVersions"][1]
            and all(value[k] is True for k in ("sourceStopped", "retainedState", "replay", "jobRecovered", "signedDelivery", "policyRetained", "tenantSettingsRetained",
                                               "pluginRetained", "personalItemsRetained", "ratingsRetained", "archiveStopped")))


def verify_chain(read_page, tenant, recovery_point, schema, manifest):
    """Keep the existing functional supervisor's classified error contract."""
    return restore_pages.verify_chain(read_page, tenant, recovery_point, schema, manifest, error_type=Failure)


class BackupRestore(Journey):
    """Reuse the owned webhook/API clients, with a fixed fresh-database overlay."""

    def __init__(self, environment):
        if environment.get("CATALOG_FUNCTIONAL_PROFILE") not in ("backup-restore", "pitr-recovery"):
            raise Failure("invalid-backup-profile")
        super().__init__(environment)
        self.env.update(CATALOG_FUNCTIONAL_BACKUP_DATABASE="postgres", CATALOG_FUNCTIONAL_WEBHOOK_ENABLED="true", COMPOSE_PROFILES="")
        self.writer = "postgres"
        self.end = time.monotonic() + 600
        self.point = "logical-backup-" + self.env["CATALOG_FUNCTIONAL_SUFFIX"]
        self.evidence = {}
        # The private supervisor directory protects these host files. Only this
        # child directory is mounted for the non-root operator binary; the dump
        # and unrelated state/credentials are never included in that mount.
        self.inputs = self.state / "backup-inputs"
        self.inputs.mkdir(mode=0o755)

    def phase(self, value):
        if value not in PHASES:
            raise Failure("invalid-backup-phase")
        write_document(self.state / "backup-phase.json", {"phase": value})

    def compose(self, *arguments, capture=False, limit=1 << 20):
        argv = shlex.split(self.env.get("CATALOG_FUNCTIONAL_COMPOSE_COMMAND", "docker compose"))
        argv += ["--env-file", "/dev/null", "-f", "compose.yaml", "-f", "test/functional/compose.yaml",
                 "-f", "test/functional/webhooks.compose.yaml", "-f", "test/functional/backup_restore.compose.yaml"]
        if self.env["CATALOG_FUNCTIONAL_PROFILE"] == "pitr-recovery":
            argv += ["-f", "test/functional/pitr_recovery.compose.yaml"]
        return command(argv + ["-p", self.project, *arguments], self.env,
                       min(90, self.end - time.monotonic()), capture=capture, limit=limit)

    def sql(self, statement, server=None, database="catalog"):
        return self.compose("exec", "-T", server or self.writer, "psql", "-X", "-U", "catalog", "-d", database,
                            "-v", "ON_ERROR_STOP=1", "-Atc", "SET statement_timeout='15s'; " + statement, capture=True).decode().strip().removeprefix("SET\n")

    def ctl(self, *arguments):
        raw = self.compose("run", "--rm", "--no-deps", "-e",
                           "CATALOG_DATABASE_DSN=postgres://catalog:catalog@" + self.writer + ":5432/catalog?sslmode=disable",
                           "-v", str(self.inputs) + ":/backup:ro", "migrate", *arguments, capture=True)
        return decode(raw)

    def stopped(self, *services):
        for service in services:
            if self.compose("ps", "--status", "running", "-q", service, capture=True).strip():
                raise Failure("backup-runtime-not-stopped")

    def database_settings(self):
        """Compare database-level metadata absent from a no-create dump."""
        return self.sql("SELECT jsonb_build_object('owner',pg_get_userbyid(datdba),'encoding',encoding,"
            "'locale',datlocale,'collate',datcollate,'ctype',datctype,'acl',datacl,"
            "'settings',(SELECT coalesce(jsonb_agg(jsonb_build_object('role',pg_get_userbyid(setrole),"
            "'settings',setconfig) ORDER BY setrole),'[]') FROM pg_db_role_setting s WHERE setdatabase=d.oid)) "
            "FROM pg_database d WHERE datname='catalog'")

    def inventory(self):
        tenants = json.loads(self.sql("SELECT coalesce(jsonb_agg(id ORDER BY id),'[]') FROM tenants"))
        if tenants != list(TENANTS):
            raise Failure("backup-tenant-inventory-mismatch")
        names = self.sql("SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename").splitlines()
        if not 1 <= len(names) <= 150 or any(not re.fullmatch(r"[a-z_][a-z0-9_]*", name) for name in names):
            raise Failure("backup-table-inventory-limit")
        tables = {}
        for name in names:
            selection = '(SELECT * FROM public."' + name + '" LIMIT 10001) v'
            count, size = json.loads(self.sql("SELECT json_build_array(count(*),coalesce(sum(octet_length(to_jsonb(v)::text)),0)) FROM " + selection))
            if count > 10000 or size > MAX_DUMP:
                raise Failure("backup-fixture-inventory-limit")
            digest = self.sql("SELECT encode(sha256(convert_to(coalesce(string_agg(to_jsonb(v)::text,E'\\n' ORDER BY to_jsonb(v)::text),''),'UTF8')),'hex') FROM " + selection)
            if not re.fullmatch(r"[0-9a-f]{64}", digest):
                raise Failure("backup-invalid-table-digest")
            tables[name] = {"rows": count, "digest": digest}
        return {"tenants": tenants, "tables": tables}

    def baseline(self):
        self.phase("baseline")
        self.retained = [exact_record(self.api, item) for item in records(self.api)]
        assert len(self.retained) == 3 and {v["resource"]["kind"] for v in self.retained} == {"agent", "mcp_server", "skill"}
        self.populate_retained_inputs()
        self.rating_seed = backup_ratings.prepare(self)
        self.other_namespace = self.namespace(self.other)
        # Store distinct exact overrides in both tenants before the backup.
        self.tenant_settings = []
        for client, overrides in ((self.api, {"automaticSourceSyncEnabled": False,
                "automaticSourceSyncInterval": "1m30.123456789s", "newJobMaxAttempts": 2}),
                (self.other, {"automaticSourceSyncInterval": "2h", "newJobMaxAttempts": 9})):
            _, headers = client.request("GET", "/api/v1/tenant-settings")
            saved, reply = mutate(client, "POST", "/api/v1/tenant-settings", {
                "overrides": overrides, "reason": "Retain tenant policy through owned backup"},
                etag=header(headers, "ETag"))
            assert saved["revision"] == 1 and saved["overrides"]["newJobMaxAttempts"] == overrides["newJobMaxAttempts"]
            self.tenant_settings.append((client, saved, header(reply, "ETag")))
        self.other_resources = []
        for kind in ("agent", "mcp_server", "skill"):
            resource, _ = mutate(self.other, "POST", "/api/v1/resources", {
                "namespaceId": self.other_namespace["id"], "kind": kind,
                "canonicalIdentifier": "urn:air:catalog.example.com:backup:" + kind + "-" + self.env["CATALOG_FUNCTIONAL_SUFFIX"]}, 201)
            self.other_resources.append((resource["id"], self.other.request("GET", "/api/v1/resources/" + resource["id"])[0]))
        inspection, _ = mutate(self.api, "POST", "/api/v1/federation/source-inspections", {
            "url": "https://federation-fixture:8443/.well-known/ard.json", "networkProfile": "functional-fixture"}, 201)
        source, _ = mutate(self.api, "POST", "/api/v1/federation/sources", {
            "inspectionId": inspection["id"], "displayName": "Backup fixture source", "retrievalMode": "indexed",
            "scope": {"kinds": ["agent"]}, "updateMode": "manual_review", "networkProfile": "functional-fixture",
            "reason": "Owned logical restore"}, 201)
        initial = sync(self.api, source["id"], source["revision"], "backup-initial")
        wait_job(self.api, initial["job"]["id"], {"succeeded"})
        assert int(self.sql("SELECT count(*) FROM observations")) > 0
        # Retain one live and one cleared feedback record across the real dump/restore.
        self.feedback_records = []
        for terminal in (False, True):
            receipt, response = mutate(self.api, "POST", "/api/v1/feedback", {
                "resourceId": self.retained[0]["resource"]["id"], "category": "other",
                "comment": "Synthetic backup feedback"}, 201)
            path = "/api/v1/feedback/" + receipt["id"]
            if terminal:
                mutate(self.api, "POST", path + ":remove", {}, etag=header(response, "ETag"))
            current, _ = self.api.request("GET", path)
            self.feedback_records.append((path, current))
        self.endpoint = self.register()
        self.configure(self.endpoint)
        completed = self.namespace()
        self.verified_delivery(self.event_id(completed))
        # Ordinary writes cease after this capture except the explicitly queued
        # command and audit exports. No clock manipulation or lease rewriting.
        self.compose("stop", "--timeout", "20", "worker")
        self.stopped("worker")
        source, _ = self.api.request("GET", "/api/v1/federation/sources/" + source["id"])
        queued = sync(self.api, source["id"], source["revision"], "backup-queued")
        self.job = queued["job"]["id"]
        assert self.api.request("GET", "/api/v1/jobs/" + self.job)[0]["state"] == "queued"
        self.replay_body = {"slug": "backup-" + self.env["CATALOG_FUNCTIONAL_SUFFIX"], "displayName": "Retained command"}
        self.replay_headers = {"Idempotency-Key": self.point}
        self.replay_value, self.replay_response_headers = self.api.request(
            "POST", "/api/v1/namespaces", self.replay_body, (201,), self.replay_headers, decode_body=False)
        assert header(self.replay_response_headers, "ETag") == '"rev-1"'
        created = decode(self.replay_value)
        assert created["revision"] == 1 and all(created[key] == value for key, value in self.replay_body.items())
        self.policy = authorization_recovery.prepare(self.api, created["id"], "backup-" + self.env["CATALOG_FUNCTIONAL_SUFFIX"])
        self.pending_event = self.event_id(created)
        self.public = audit.request(self.base, "GET", "/.well-known/ard.json", "local-admin")
        assert self.public[0] == 200 and header(self.public[1], "ETag") == '"' + hashlib.sha256(self.public[2]).hexdigest() + '"'
        self.searches = [{"tags": ["functional-" + self.env["CATALOG_FUNCTIONAL_SUFFIX"]], "limit": 20},
                         {"text": "order", "limit": 20}]
        self.search_results = [self.api.request("POST", "/api/v1/catalog/search", q)[0] for q in self.searches]
        require_search_baseline(self.search_results)

    def populate_retained_inputs(self):
        """Create real API-owned records; never seed sealed rows through SQL."""
        origin = read_document(self.fixture / "archive-origin.json")["origin"]
        entries = read_document(self.fixture / "archives/allowlist.json")["entries"]
        assert len(entries) == 1 and entries[0]["path"] == "/restore-plugin.zip"
        archive = entries[0]
        resource, _ = mutate(self.api, "POST", "/api/v1/resources", {
            "namespaceId": self.retained[0]["resource"]["namespaceId"], "kind": "plugin",
            "canonicalIdentifier": "urn:air:catalog.example.com:backup:plugin-" + self.env["CATALOG_FUNCTIONAL_SUFFIX"]}, 201)
        created, _ = mutate(self.api, "POST", "/api/v1/resources/" + resource["id"] + "/versions", {
            "version": "1.0.0", "representations": [{"role": "primary", "mediaType": "application/agent-plugins+zip",
                "url": origin + archive["path"], "expectedDigest": archive["digest"]}]}, 201)
        revision = created["revision"]
        assert created["evidence"]["state"] == "complete" and len(revision["representations"]) == 1
        representation = revision["representations"][0]
        assert representation["validationState"] == "valid" and representation["contentDigest"] == archive["digest"]
        assert representation["url"] == origin + archive["path"]
        inspection = representation["plugin"]
        expected_documents = {path: (self.fixture / "archives/restore-plugin" / path).read_text(encoding="utf-8")
                              for path in ("plugin.json", "mcp.json", "skills/review-repository/SKILL.md")}
        assert len(inspection["documents"]) == 3
        assert {doc["path"]: doc["content"] for doc in inspection["documents"]} == expected_documents
        assert {(item["kind"], item["name"], item["path"]) for item in inspection["components"]} == {
            ("skill", "review-repository", "skills/review-repository/SKILL.md"), ("mcp_server", "tools", "mcp.json")}
        assert len(inspection["files"]) == 3
        assert {item["path"]: (item["size"], item["digest"]) for item in inspection["files"]} == {
            path: (len(content.encode()), "sha256:" + hashlib.sha256(content.encode()).hexdigest())
            for path, content in expected_documents.items()}
        self.plugin_reads = []
        for path in ("/api/v1/resources/" + resource["id"], "/api/v1/resource-versions/" + created["version"]["id"],
                     "/api/v1/version-revisions/" + revision["id"]):
            value, headers = self.api.request("GET", path)
            self.plugin_reads.append((path, value, header(headers, "ETag")))
        # Creation can return nanoseconds; PostgreSQL reads retain microseconds.
        # Normalize only this setup comparison. Restored reads remain exact.
        def stored(value):
            return {key: datetime.datetime.fromisoformat(item) if key in ("createdAt", "updatedAt") else item
                    for key, item in value.items()}
        for actual, expected in ((self.plugin_reads[0][1], resource), (self.plugin_reads[1][1], created["version"]),
                                 (self.plugin_reads[2][1]["revision"], revision)):
            assert stored(actual) == stored(expected)
        assert self.plugin_reads[2][1]["evidence"] == created["evidence"]
        self.personal_items = []
        for body in ({"type": "search", "label": "Restore search", "search": {
                "text": "order", "origins": ["local"], "kinds": ["agent"], "tags": ["functional-" + self.env["CATALOG_FUNCTIONAL_SUFFIX"]]}},
                {"type": "resource", "label": "Restore reference", "reference": {"origin": "local",
                 "releaseId": self.retained[0]["release"]["id"], "versionRevisionId": self.retained[0]["versionRevision"]["id"]}}):
            receipt, headers = mutate(self.api, "POST", "/api/v1/personal/items", body, 201)
            path = "/api/v1/personal/items/" + receipt["id"]
            value, reply = self.api.request("GET", path)
            assert all(value[key] == expected for key, expected in body.items())
            assert value["revision"] == 1 and value["id"] == receipt["id"]
            assert value["createdAt"] and value["updatedAt"]
            validator = header(reply, "ETag")
            assert validator and validator == header(headers, "ETag")
            self.personal_items.append((path, value, validator))
        self.verify_retained_inputs()

    def verify_retained_inputs(self):
        """Require populated records and exact bytes/metadata, including owner isolation."""
        assert len(self.plugin_reads) == 3 and len(self.personal_items) == 2
        assert {value["type"] for _, value, _ in self.personal_items} == {"search", "resource"}
        stranger = Client(self.base, "local-reader")
        for path, expected, validator in self.plugin_reads + self.personal_items:
            actual, headers = self.api.request("GET", path)
            assert actual == expected and header(headers, "ETag") == validator, "restore changed retained input"
            self.other.request("GET", path, expected=(404,))
            if path.startswith("/api/v1/personal/"):
                assert header(headers, "Cache-Control") == "no-store"
                stranger.request("GET", path, expected=(404,))

    def capture(self):
        self.phase("capture")
        self.rating_expectations = backup_ratings.capture(self, self.rating_seed)
        self.rating_capture_digest = backup_ratings.digest(self.rating_expectations)
        self.manifests, self.audit_rows = [], []
        # One server-derived range, fixed before either export, covers all
        # committed fixture audit rows but excludes export's own evidence.
        bounds = json.loads(self.sql("SELECT json_build_array(min(occurred_at)-interval '1 second',max(occurred_at)+interval '1 microsecond') FROM audit_events"))
        before = datetime.datetime.fromisoformat(bounds[1])
        audit.wait_for_server_cutoff(self.base, before)
        for index, (tenant, token) in enumerate(zip(TENANTS, ("local-admin", "other-tenant-admin"), strict=True)):
            body = {"occurredFrom": bounds[0], "occurredBefore": bounds[1], "reason": "Owned logical backup comparison"}
            status, _, raw = audit.request(self.base, "POST", "/api/v1/audit-exports", token, body, self.point + "-audit-" + str(index))
            assert status == 200
            exported = decode(raw)
            count = exported["manifest"]["rowCount"]
            assert 1 <= count <= 1000 and exported["manifest"]["tenantId"] == tenant
            expected = int(self.sql("SELECT count(*) FROM audit_events WHERE tenant_id='" + tenant + "' AND occurred_at >= '" + bounds[0] + "' AND occurred_at < '" + bounds[1] + "'"))
            assert count == expected
            path = self.inputs / ("audit-" + str(index) + ".json")
            path.write_bytes(raw)
            path.chmod(0o444)
            assert self.ctl("audit", "verify-export", "--file", "/backup/" + path.name)["valid"] is True
            manifest = self.ctl("audit", "bind-manifest", "--file", "/backup/" + path.name, "--recovery-point", self.point)
            manifest_path = self.inputs / ("manifest-" + str(index) + ".json")
            write_document(manifest_path, manifest)
            manifest_path.chmod(0o444)
            self.manifests.append(manifest)
            self.audit_rows.append(count)
        self.compose("stop", "--timeout", "20", "api")
        self.stopped("api", "worker")
        self.compose("stop", "--timeout", "20", "peer-archives")
        self.stopped("peer-archives")
        self.plan = self.ctl("migrate", "plan", "--config", "/config/config.json")
        assert self.plan and all(row["applied"] is True for row in self.plan)
        self.profiles = self.ctl("profiles", "verify")
        self.before_inventory = self.inventory()
        self.fixture_state = self.receiver.request("GET", "/state")[0]
        self.receiver_state = self.stats()
        self.version = int(self.sql("SHOW server_version_num"))
        self.database_properties = self.database_settings()

    def dump(self):
        self.phase("dump")
        with temporary_archive(self.compose, "postgres") as archive:
            dump_archive(self.compose, "postgres", archive)
            size = int(self.compose("exec", "-T", "postgres", "stat", "-c", "%s", archive, capture=True))
            if not 0 < size <= MAX_DUMP:
                raise Failure("backup-dump-size")
            self.archive = self.state / "catalog-backup.dump"
            self.compose("cp", "postgres:" + archive, str(self.archive))
        self.archive.chmod(0o600)
        with self.archive.open("rb") as archive:
            raw = archive.read(MAX_DUMP + 1)
        if len(raw) != size:
            raise Failure("backup-copied-dump-size")
        self.evidence.update(dumpBytes=size, dumpDigest="sha256:" + hashlib.sha256(raw).hexdigest(),
                             inventoryDigest=fingerprint(self.before_inventory))
        self.evidence["bindingDigest"] = fingerprint({"dump": self.evidence["dumpDigest"], "inventory": self.before_inventory,
            "manifests": self.manifests, "plan": self.plan, "profiles": self.profiles,
            "version": read_document(self.state / "version.json"), "configuration": Path("config/postgres.json").read_text(),
            "policy": read_document(self.fixture / "policy.json"), "databaseSettings": self.database_properties,
            "ratings": self.rating_expectations, "recoveryPoint": self.point})
        self.later_rating_state = backup_ratings.later_changes(self, self.rating_seed)
        self.compose("stop", "--timeout", "20", "postgres")
        self.stopped("postgres", "api", "worker")

    def restore(self):
        self.phase("restore")
        self.compose("up", "--no-deps", "--detach", "--wait", "--wait-timeout", "60", "restored")
        self.writer = "restored"
        assert self.sql("SELECT count(*) FROM pg_database WHERE datname='catalog'", database="postgres") == "0"
        # Cluster roles are not in pg_dump. Reuse reviewed fixture attributes,
        # not an unfiltered pg_dumpall import or elevated runtime identity.
        self.sql("CREATE ROLE catalog_api LOGIN PASSWORD 'catalog-api' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS; "
                 "CREATE ROLE catalog_worker LOGIN PASSWORD 'catalog-worker' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION BYPASSRLS", database="postgres")
        self.compose("exec", "-T", "restored", "createdb", "-U", "catalog", "--template", "template0", "--owner", "catalog", "catalog")
        self.sql("GRANT CONNECT ON DATABASE catalog TO catalog_api,catalog_worker")
        with temporary_archive(self.compose, "restored") as archive:
            self.compose("cp", str(self.archive), "restored:" + archive)
            self.compose("exec", "-T", "restored", "pg_restore", "-U", "catalog", "-d", "catalog", "--single-transaction", archive)
        assert self.ctl("migrate", "plan", "--config", "/config/config.json") == self.plan
        applied = self.ctl("migrate", "apply", "--config", "/config/config.json", "--api-role", "catalog_api", "--worker-role", "catalog_worker")
        assert not applied["applied"]
        privileges = applied["runtimePrivileges"]
        assert len(privileges) == 2 and all(p["compliant"] is True for p in privileges)
        self.manifest_version = privileges[0]["manifestVersion"]
        assert self.ctl("profiles", "verify") == self.profiles
        assert self.database_settings() == self.database_properties
        self.evidence["postgresVersions"] = [self.version, int(self.sql("SHOW server_version_num"))]

    def verify(self):
        self.phase("verify")
        self.stopped("api", "worker", "postgres")
        if self.inventory() != self.before_inventory:
            raise Failure("backup-restored-inventory-mismatch")
        pages, found = [], set()
        for tenant in TENANTS:
            def read_page(cursor):
                args = ["restore", "verify", "--config", "/config/config.json", "--tenant", tenant,
                        "--recovery-point", self.point, "--api-role", "catalog_api", "--worker-role", "catalog_worker", "--limit", str(PAGE_LIMIT)]
                if cursor:
                    args += ["--cursor", cursor]
                return self.ctl(*args)
            count, findings = verify_chain(read_page, tenant, self.point, self.plan[-1]["version"], self.manifest_version)
            pages.append(count)
            found.update(findings)
        assert "RESTORE_ACTIONABLE_JOB_RETAINED" in found
        for index, manifest in enumerate(self.manifests):
            compared = self.ctl("audit", "compare-restore", "--config", "/config/config.json", "--file",
                                "/backup/manifest-" + str(index) + ".json", "--recovery-point", self.point)
            if (compared.get("valid") is not True or compared.get("rowCount") != self.audit_rows[index]
                    or compared.get("eventsDigest") != manifest["manifest"]["eventsDigest"]):
                raise Failure("backup-audit-mismatch")
        assert self.inventory() == self.before_inventory
        assert self.receiver.request("GET", "/state")[0] == self.fixture_state and self.stats() == self.receiver_state
        self.rating_owner_verified = backup_ratings.verify_owner(
            self, self.rating_seed, self.rating_expectations, self.rating_capture_digest)
        self.evidence.update(tenantCount=2, verificationPages=pages, auditRows=self.audit_rows, retainedState=True)

    def verified_delivery(self, event_id):
        """Wait for retained work, not for failed HTTP/contract checks to disappear.

        A dead-letter result needs investigation, not more polling. Distinct
        call sites survive in the supervisor's redacted failure location.
        """
        def read():
            page, _ = self.api.request("GET", "/api/v1/webhooks/" + self.endpoint["endpoint"]["id"] + "/deliveries?limit=100")
            delivery = next((item for item in page["items"] if item["eventId"] == event_id), None)
            if delivery and delivery["state"] == "dead_letter":
                raise Failure("backup-delivery-dead-letter")
            return delivery if delivery and delivery["state"] == "succeeded" else None
        return wait_until("backup fixture webhook delivery", read)

    def runtime(self):
        self.phase("runtime")
        if getattr(self, "rating_owner_verified", False) is not True:
            raise Failure("backup-ratings-not-verified")
        self.stopped("postgres", "api", "worker")
        self.env["CATALOG_FUNCTIONAL_BACKUP_DATABASE"] = "restored"
        # Never follow Compose's provision/migrate dependencies after restore.
        self.compose("up", "--no-deps", "--force-recreate", "--detach", "--wait", "--wait-timeout", "60", "api")
        assert [exact_record(self.api, item) for item in records(self.api)] == self.retained
        for path, expected in self.feedback_records:
            assert self.api.request("GET", path)[0] == expected, "restore changed retained or cleared feedback"
            self.other.request("GET", path, expected=(404,))
        for value in self.retained:
            path = "/api/v1/catalog/resources/" + value["release"]["id"]
            assert self.api.request("GET", path)[0] == value
            self.other.request("GET", path, expected=(404,))
        retained, _ = self.other.request("GET", "/api/v1/namespaces/" + self.other_namespace["id"])
        assert namespace_state(retained) == namespace_state(self.other_namespace)
        for identifier, resource in self.other_resources:
            path = "/api/v1/resources/" + identifier
            assert self.other.request("GET", path)[0] == resource
            self.api.request("GET", path, expected=(404,))
        self.api.request("GET", "/api/v1/namespaces/" + self.other_namespace["id"], expected=(404,))
        for client, saved, validator in self.tenant_settings:
            current, headers = client.request("GET", "/api/v1/tenant-settings")
            assert current.get("tenantId") == saved.get("tenantId"), "restore changed policy tenant"
            assert current.get("revision") == saved.get("revision"), "restore changed policy revision"
            assert current.get("overrides") == saved.get("overrides"), "restore changed policy overrides"
            assert current.get("updatedAt") == saved.get("updatedAt"), "restore changed policy time"
            assert current.get("deploymentDefaults") == saved.get("deploymentDefaults"), "restore changed policy fallbacks"
            assert current.get("effective") == saved.get("effective"), "restore changed effective policy"
            assert current == saved, "restore changed policy fields"
            assert header(headers, "ETag") == validator, "restore changed policy validator"
        public = audit.request(self.base, "GET", "/.well-known/ard.json", "local-admin")
        assert public[0] == 200 and public[2] == self.public[2] and header(public[1], "ETag") == '"' + hashlib.sha256(self.public[2]).hexdigest() + '"'
        for query, expected in zip(self.searches, self.search_results, strict=True):
            actual, _ = self.api.request("POST", "/api/v1/catalog/search", query)
            assert actual["items"] == expected["items"] and actual["partial"] is False
        before_replay = self.inventory()
        assert before_replay == self.before_inventory
        replay, headers = self.api.request("POST", "/api/v1/namespaces", self.replay_body, (201,), self.replay_headers, decode_body=False)
        assert replay == self.replay_value and header(headers, "Idempotency-Replayed") == "true"
        assert header(headers, "ETag") == '"rev-1"'
        assert self.inventory() == before_replay
        authorization_recovery.verify(self.api, self.policy)
        self.stopped("postgres", "peer-archives")
        self.verify_retained_inputs()
        backup_ratings.verify_api(self, self.rating_seed, self.rating_expectations,
                                  self.rating_capture_digest, self.later_rating_state)
        self.evidence.update(pluginRetained=True, personalItemsRetained=True, ratingsRetained=True)
        self.compose("up", "--no-deps", "--force-recreate", "--detach", "--wait", "--wait-timeout", "60", "worker")
        self.ready()
        recovered = wait_job(self.api, self.job, {"succeeded"})
        assert recovered["attemptLimit"] == 2, "restore rewrote an admitted Job budget"
        delivery = self.verified_delivery(self.pending_event)
        assert delivery["state"] == "succeeded"
        assert self.stats()["invalid"] == 0 and self.stats()["accepted"] > self.receiver_state["accepted"]
        assert self.sql("SELECT count(DISTINCT usename) FROM pg_stat_activity WHERE usename IN ('catalog_api','catalog_worker')") == "2"
        self.stopped("postgres", "peer-archives")
        self.evidence.update(sourceStopped=True, archiveStopped=True, replay=True, jobRecovered=True, signedDelivery=True, policyRetained=True, tenantSettingsRetained=True)
        if not valid_evidence(self.evidence, physical=self.env["CATALOG_FUNCTIONAL_PROFILE"] == "pitr-recovery"):
            raise Failure("backup-invalid-evidence")
        write_document(self.state / "backup-restore.json", self.evidence)
        self.phase("complete")

    def run(self):
        self.baseline()
        self.capture()
        self.dump()
        self.restore()
        self.verify()
        self.runtime()


if __name__ == "__main__":
    try:
        if sys.argv[1:] == ["prepare"] and os.environ.get("CATALOG_FUNCTIONAL_PROFILE") in ("backup-restore", "pitr-recovery"):
            prepare_plugin_archives(os.environ, (("restore-plugin", "1.0.0"),), include_mcp=True)
        elif not sys.argv[1:]:
            BackupRestore(os.environ).run()
        else:
            raise Failure("invalid-backup-command")
    except Exception as error:
        record_scenario_failure("backup_restore", error)
        raise SystemExit("Logical backup recovery failed; inspect the report phase.") from None
