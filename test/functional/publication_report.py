"""Real Catalog delivery to the independent Go publication-report example."""

import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import sys

from core import mutate, snapshot
from federation import Client
from runner import DIGEST, Failure, read_document, write_document


TENANT = "00000000-0000-7000-8000-000000000001"
ORIGIN = "http://127.0.0.1:8080"
RECEIVER = "https://api:9443/webhook"
DATABASE = "publication_receiver"
SOURCE = "https://github.com/open-agentic-catalog/open-agentic-catalog/tenants/" + TENANT
PERMISSIONS = {"publication.read", "webhook.read", "webhook.read_evidence"}


def private_write(path, value):
    path.write_bytes(value)
    path.chmod(0o600)


def prepare(environment):
    if environment.get("CATALOG_FUNCTIONAL_PROFILE") != "webhooks":
        raise Failure("invalid-publication-report-profile")
    fixture = Path(environment["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"])
    inputs = fixture / "publication-report"
    inputs.mkdir(mode=0o755)
    inputs.chmod(0o755)
    (fixture / "publication-report-output").mkdir(mode=0o777)
    (fixture / "publication-report-output").chmod(0o777)
    for name in ("server.pem", "server-key.pem"):
        private_write(inputs / name, (fixture / name).read_bytes())
    private_write(inputs / "catalog-token", b"local-template-service-client")
    policy_file = fixture / "webhooks/policy.json"
    policy = read_document(policy_file)
    policy["privateProfiles"].append({"name": "publication-report", "origins": ["https://api:9443"],
        "addressRanges": [environment["CATALOG_FUNCTIONAL_SUBNET"]], "assignedTenantIds": [TENANT],
        "trustBundleFile": "/config/webhooks/ca.pem", "transportRef": "direct"})
    policy_file.chmod(0o600)
    write_document(policy_file, policy)
    policy_file.chmod(0o444)


def valid_evidence(value):
    fields = {"programDigest", "completedReceipts", "activationReport", "unpublishReport",
              "readerRestricted", "databaseIsolated", "fileMatchesDatabase"}
    return (isinstance(value, dict) and set(value) == fields
            and isinstance(value["programDigest"], str) and DIGEST.fullmatch(value["programDigest"]) is not None
            and type(value["completedReceipts"]) is int and value["completedReceipts"] == 2
            and all(value[field] is True for field in fields - {"programDigest", "completedReceipts"}))


def check_observation(saved, trigger, active):
    """Startup/reconciliation, another receipt or a stale state cannot qualify."""
    if not isinstance(saved, dict) or saved.get("refreshFailure"):
        return False
    report = saved.get("lastSuccessfulObservation")
    if not isinstance(report, dict) or set(report) != {"label", "observedAt", "tenant", "host", "active", "trigger"}:
        return False
    try:
        observed = datetime.datetime.fromisoformat(report["observedAt"])
    except (ValueError, TypeError):
        return False
    return (report.get("label") == "Last observed publication state"
            and report.get("tenant") == TENANT and report.get("host") == "127.0.0.1"
            and observed.utcoffset() is not None and report["trigger"] == trigger and report["active"] == active)


def report_sql(journey, statement):
    raw = journey.compose("exec", "-T", "postgres", "psql", "-X", "-q", "-U", "catalog", "-d", DATABASE,
        "-v", "ON_ERROR_STOP=1", "-Atc", "SET statement_timeout='5s'; " + statement, capture=True)
    return raw.decode().strip()


def init_arguments(database_file="receiver.dsn"):
    return ["init", "--origin", ORIGIN, "--allow-loopback-http", "--tenant", TENANT, "--host", "127.0.0.1",
            "--database-file", "/report-inputs/" + database_file]


def read_report(path):
    """Retry an absent report; malformed documents and other I/O errors fail."""
    try:
        saved = read_document(path)
    except FileNotFoundError:
        return None
    if not isinstance(saved, dict):
        raise Failure("invalid-publication-report")
    return saved


def qualify(journey):
    inputs = journey.fixture / "publication-report"
    output = journey.fixture / "publication-report-output/report.json"
    reader = Client(journey.base, "local-template-service-client")
    me, _ = reader.request("GET", "/api/v1/me")
    assert me["permissions"] == [] and me["availablePermissions"] == []
    reader.request("GET", "/api/v1/publication/snapshots?host=127.0.0.1&state=active&limit=2", expected=(403,))
    role, _ = mutate(journey.api, "POST", "/api/v1/access/roles", {
        "name": "Publication report reader", "permissions": sorted(PERMISSIONS),
        "reason": "Read publication and notification evidence only"}, 201)
    mutate(journey.api, "POST", "/api/v1/access/assignments", {"roleDefinitionId": role["id"],
        "membershipId": me["membershipId"], "reason": "Qualify the independent example reader"}, 201)
    me, _ = reader.request("GET", "/api/v1/me")
    assert set(me["permissions"]) == PERMISSIONS and set(me["availablePermissions"]) == PERMISSIONS
    assert not me["namespaceGrants"]
    reader.request("GET", "/api/v1/webhooks/reconciliation")
    body = {"name": "Publication report receiver", "url": RECEIVER,
            "eventTypes": ["publication.activated", "publication.unpublished"]}
    reader.request("POST", "/api/v1/webhooks", body, expected=(403,), headers={"Idempotency-Key": "report-reader-denied"})
    endpoint, _ = mutate(journey.api, "POST", "/api/v1/webhooks", body, 201)
    private_write(inputs / "signer.json", json.dumps({"secret": endpoint["secret"], "version": endpoint["endpoint"]["secretVersion"]}).encode())

    password = secrets.token_hex(24)
    journey.sql("CREATE ROLE publication_receiver LOGIN PASSWORD '" + password + "' NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOREPLICATION NOBYPASSRLS")
    journey.sql("CREATE DATABASE publication_receiver OWNER publication_receiver")
    journey.sql("REVOKE CONNECT ON DATABASE catalog FROM PUBLIC")
    journey.sql("REVOKE CONNECT ON DATABASE publication_receiver FROM PUBLIC")
    assert journey.sql("SELECT has_database_privilege('publication_receiver','catalog','CONNECT')::int + has_database_privilege('catalog_api','publication_receiver','CONNECT')::int + has_database_privilege('catalog_worker','publication_receiver','CONNECT')::int") == "0"
    for filename, database in (("receiver.dsn", DATABASE), ("forbidden.dsn", "catalog")):
        private_write(inputs / filename, ("postgres://publication_receiver:" + password + "@postgres:5432/" + database + "?sslmode=disable").encode())
    journey.compose("run", "--rm", "--no-deps", "publication-report", *init_arguments())
    try:
        journey.compose("run", "--rm", "--no-deps", "publication-report", *init_arguments("forbidden.dsn"))
    except Failure as error:
        if str(error) != "child-failed":
            raise
    else:
        raise Failure("receiver-can-initialize-catalog-database")
    assert journey.sql("SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename LIKE 'publication_receiver_%'") == "0"
    assert report_sql(journey, "SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename LIKE 'publication_receiver_%'") == "3"

    journey.compose("--profile", "publication-report", "up", "--no-deps", "--force-recreate", "--detach", "publication-report")
    try:
        def initial():
            saved = read_report(output)
            if saved is None:
                return False
            observed = saved.get("lastSuccessfulObservation")
            return (isinstance(observed, dict) and "trigger" not in observed and saved.get("lossCountsObservedAt")
                    and not saved.get("refreshFailure"))
        journey.wait("publication-report-startup", initial)
        assert report_sql(journey, "SELECT count(*) FROM publication_receiver_inbox") == "0"
        activated = snapshot(journey.api)
        page, _ = reader.request("GET", "/api/v1/publication/snapshots?host=127.0.0.1&state=active&limit=2")
        assert len(page["items"]) == 1 and page["items"][0]["id"] == activated["id"] and not page.get("nextCursor")
        active = {field: page["items"][0][field] for field in ("id", "tenantId", "host", "state", "generation", "audience", "etag", "createdAt")}
        for action, expected in (("publication.activated", active), ("publication.unpublished", None)):
            if action == "publication.unpublished":
                mutate(journey.api, "POST", "/api/v1/publication:unpublish", {"host": "127.0.0.1", "reason": "Qualify notification-triggered report withdrawal"})
            event_id = journey.sql("SELECT id FROM outbox_events WHERE tenant_id='" + TENANT + "' AND subject='" + activated["id"] + "' AND data->>'action'='" + action + "' ORDER BY created_at DESC LIMIT 1")
            assert re.fullmatch(r"[a-f0-9-]{36}", event_id)
            payload = json.loads(journey.sql("SELECT data FROM outbox_events WHERE tenant_id='" + TENANT + "' AND id='" + event_id + "'"))
            trigger = {"id": event_id, "source": SOURCE, "action": action, "targetId": activated["id"]}
            if payload.get("digest"):
                trigger["snapshotStateDigest"] = payload["digest"]
            journey.delivery(endpoint, event_id)
            def completed():
                row = json.loads(report_sql(journey, "SELECT json_build_object('done',EXISTS(SELECT 1 FROM publication_receiver_inbox WHERE source='" + SOURCE + "' AND id='" + event_id + "' AND event='" + json.dumps(trigger) + "'::jsonb AND completed_at IS NOT NULL),'report',last_success,'failure',failure) FROM publication_receiver_report WHERE singleton"))
                if not row["done"]:
                    return False
                saved = read_report(output)
                if saved is None:
                    return False
                assert not row["failure"] and check_observation({"lastSuccessfulObservation": row["report"]}, trigger, expected)
                return check_observation(saved, trigger, expected) and saved["lastSuccessfulObservation"] == row["report"]
            journey.wait("publication-report-activation" if action == "publication.activated" else "publication-report-unpublish", completed)
        assert report_sql(journey, "SELECT count(*) FROM publication_receiver_inbox WHERE completed_at IS NOT NULL") == "2"
    finally:
        journey.compose("--profile", "publication-report", "stop", "--timeout", "20", "publication-report")
    evidence = {"programDigest": "sha256:" + hashlib.sha256((inputs / "receiver").read_bytes()).hexdigest(),
                "completedReceipts": 2, "activationReport": True, "unpublishReport": True,
                "readerRestricted": True, "databaseIsolated": True, "fileMatchesDatabase": True}
    assert valid_evidence(evidence)
    return evidence


if __name__ == "__main__":
    if sys.argv[1:] != ["prepare"]:
        raise SystemExit("Select publication-report fixture preparation.")
    prepare(os.environ)
