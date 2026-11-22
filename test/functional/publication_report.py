"""Real Catalog delivery to the independent Go publication-report example."""

import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import sys
import tempfile

from core import mutate, snapshot
from federation import Client
from journeys import header
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


def install_signer(inputs, endpoint):
    with tempfile.NamedTemporaryFile(mode="w", dir=inputs, delete=False) as pending:
        json.dump({"secret": endpoint["secret"], "version": endpoint["endpoint"]["secretVersion"]}, pending)
    os.replace(pending.name, inputs / "signer.json")


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
              "readerRestricted", "databaseIsolated", "fileMatchesDatabase", "pendingUnpublishRecovered",
              "missedActivationReconciled"}
    return (isinstance(value, dict) and set(value) == fields
            and isinstance(value["programDigest"], str) and DIGEST.fullmatch(value["programDigest"]) is not None
            and type(value["completedReceipts"]) is int and value["completedReceipts"] == 3
            and all(value[field] is True for field in fields - {"programDigest", "completedReceipts"}))


def check_observation(saved, trigger, active):
    """Match the API state and distinguish a named receipt from reconciliation."""
    if not isinstance(saved, dict) or saved.get("refreshFailure"):
        return False
    report = saved.get("lastSuccessfulObservation")
    fields = {"label", "observedAt", "tenant", "host", "active"}
    if trigger is not None:
        fields.add("trigger")
    if not isinstance(report, dict) or set(report) != fields:
        return False
    try:
        observed = datetime.datetime.fromisoformat(report["observedAt"])
    except (ValueError, TypeError):
        return False
    return (report.get("label") == "Last observed publication state"
            and report.get("tenant") == TENANT and report.get("host") == "127.0.0.1"
            and observed.utcoffset() is not None and report.get("trigger") == trigger and report["active"] == active)


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


def publication_trigger(journey, snapshot_id, action):
    event = json.loads(journey.sql(
        "SELECT json_build_object('id',id,'digest',data->>'digest') FROM outbox_events "
        f"WHERE tenant_id='{TENANT}' AND subject='{snapshot_id}' AND data->>'action'='{action}'"))
    assert re.fullmatch(r"[a-f0-9-]{36}", event["id"])
    trigger = {"id": event["id"], "source": SOURCE, "action": action, "targetId": snapshot_id}
    if event["digest"]:
        trigger["snapshotStateDigest"] = event["digest"]
    return trigger


def receipt_report(journey, trigger):
    # A successful delivery already acknowledged this durable receipt.
    row = json.loads(report_sql(journey,
        "SELECT json_build_object('event',i.event,'done',i.completed_at IS NOT NULL,"
        "'report',r.last_success,'failure',r.failure) "
        "FROM publication_receiver_inbox i CROSS JOIN publication_receiver_report r "
        f"WHERE r.singleton AND i.source='{SOURCE}' AND i.id='{trigger['id']}'"))
    assert row["event"] == trigger, "receiver retained a different event"
    return row


def completed_report(journey, output, trigger, active, label):
    def completed():
        row = receipt_report(journey, trigger)
        if not row["done"]:
            return None
        assert not row["failure"] and check_observation({"lastSuccessfulObservation": row["report"]}, trigger, active)
        saved = read_report(output)
        if check_observation(saved, trigger, active) and saved["lastSuccessfulObservation"] == row["report"]:
            return row["report"]
        return None
    return journey.wait(label, completed)


def current_snapshot(reader, snapshots, snapshot_id):
    page, _ = reader.request("GET", snapshots)
    assert len(page["items"]) == 1 and page["items"][0]["id"] == snapshot_id and not page.get("nextCursor")
    return {field: page["items"][0][field] for field in ("id", "tenantId", "host", "state", "generation", "audience", "etag", "createdAt")}


def qualify_missed_activation(journey, endpoint, reader, snapshots, output):
    previous = read_report(output)
    before, _ = reader.request("GET", "/api/v1/webhooks/reconciliation")
    rotated = journey.rotate(endpoint, emergency=True)
    assert rotated["endpoint"]["secretVersion"] == endpoint["endpoint"]["secretVersion"] + 1
    published = snapshot(journey.api)
    active = current_snapshot(reader, snapshots, published["id"])
    trigger = publication_trigger(journey, published["id"], "publication.activated")
    rejected = journey.delivery(rotated, trigger["id"], "dead_letter", label="terminal-response", attempt=1)
    assert rejected["responseStatus"] == 401 and not rejected.get("nextAttemptAt")
    after, _ = reader.request("GET", "/api/v1/webhooks/reconciliation")
    assert after["deliveryLossCount"] == before["deliveryLossCount"] + 1
    assert read_report(output) == previous, "refused notification changed the rendered report"
    assert json.loads(report_sql(journey, "SELECT last_success FROM publication_receiver_report WHERE singleton")) == previous["lastSuccessfulObservation"]

    # Restart with the old signer still installed. Only an authorized current-state read can recover this activation.
    journey.compose("--profile", "publication-report", "restart", "--timeout", "20", "publication-report")
    def reconciled():
        saved = read_report(output)
        if check_observation(saved, None, active) and saved["deliveryLossCount"] == after["deliveryLossCount"]:
            return saved
        return None
    saved = journey.wait("publication-report-missed-activation", reconciled)
    stored = json.loads(report_sql(journey,
        "SELECT json_build_object('observation',last_success,'loss',delivery_loss,'failure',failure) "
        "FROM publication_receiver_report WHERE singleton"))
    assert stored == {"observation": saved["lastSuccessfulObservation"], "loss": after["deliveryLossCount"], "failure": ""}
    assert report_sql(journey, f"SELECT count(*) FROM publication_receiver_inbox WHERE source='{SOURCE}' AND id='{trigger['id']}'") == "0"

    install_signer(journey.fixture / "publication-report", rotated)
    mutate(journey.api, "POST", "/api/v1/publication:unpublish",
           {"host": "127.0.0.1", "reason": "Verify the replacement signing key"})
    withdrawal = publication_trigger(journey, published["id"], "publication.unpublished")
    delivered = journey.delivery(rotated, withdrawal["id"])
    assert delivered["responseStatus"] == 204
    completed_report(journey, output, withdrawal, None, "publication-report-unpublish")


def qualify(journey):
    inputs = journey.fixture / "publication-report"
    output = journey.fixture / "publication-report-output/report.json"
    reader = Client(journey.base, "local-template-service-client")
    snapshots = "/api/v1/publication/snapshots?host=127.0.0.1&state=active&limit=2"
    me, _ = reader.request("GET", "/api/v1/me")
    assert me["permissions"] == [] and me["availablePermissions"] == []
    reader.request("GET", snapshots, expected=(403,))
    role, _ = mutate(journey.api, "POST", "/api/v1/access/roles", {
        "name": "Publication report reader", "permissions": sorted(PERMISSIONS),
        "reason": "Read publication and notification evidence only"}, 201)
    assignments = "/api/v1/access/assignments"
    reader_grant = {"roleDefinitionId": role["id"], "membershipId": me["membershipId"],
                    "reason": "Read publication and notification evidence"}
    grant, grant_headers = mutate(journey.api, "POST", assignments, reader_grant, 201)
    me, _ = reader.request("GET", "/api/v1/me")
    assert set(me["permissions"]) == PERMISSIONS and set(me["availablePermissions"]) == PERMISSIONS
    assert not me["namespaceGrants"]
    reader.request("GET", "/api/v1/webhooks/reconciliation")
    body = {"name": "Publication report receiver", "url": RECEIVER,
            "eventTypes": ["publication.activated", "publication.unpublished"]}
    reader.request("POST", "/api/v1/webhooks", body, expected=(403,), headers={"Idempotency-Key": "report-reader-denied"})
    endpoint, _ = mutate(journey.api, "POST", "/api/v1/webhooks", body, 201)
    install_signer(inputs, endpoint)

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
        active = current_snapshot(reader, snapshots, activated["id"])
        activation = publication_trigger(journey, activated["id"], "publication.activated")
        journey.delivery(endpoint, activation["id"])
        last_good = completed_report(journey, output, activation, active, "publication-report-activation")

        mutate(journey.api, "POST", assignments + "/" + grant["id"] + ":revoke",
               {"reason": "Temporarily remove the receiver's read access"}, etag=header(grant_headers, "ETag"))
        reader.request("GET", snapshots, expected=(403,))
        mutate(journey.api, "POST", "/api/v1/publication:unpublish",
               {"host": "127.0.0.1", "reason": "Qualify notification-triggered report withdrawal"})
        withdrawal = publication_trigger(journey, activated["id"], "publication.unpublished")
        journey.delivery(endpoint, withdrawal["id"])

        def pending_after_refusal():
            row = receipt_report(journey, withdrawal)
            assert not row["done"] and row["report"] == last_good, "failed read completed or replaced the report"
            assert row["failure"] in ("", "catalog_read_failed"), "unexpected refresh failure"
            saved = read_report(output)
            return (row["failure"] == "catalog_read_failed" and saved is not None
                    and saved.get("refreshFailure") == "catalog_read_failed"
                    and saved["lastSuccessfulObservation"] == last_good)
        journey.wait("publication-report-read-refused", pending_after_refusal)

        mutate(journey.api, "POST", assignments, reader_grant, 201)
        page, _ = reader.request("GET", snapshots)
        assert page["items"] == [] and not page.get("nextCursor")
        completed_report(journey, output, withdrawal, None, "publication-report-unpublish")
        qualify_missed_activation(journey, endpoint, reader, snapshots, output)
        assert report_sql(journey, "SELECT count(*),count(completed_at) FROM publication_receiver_inbox") == "3|3"
    finally:
        journey.compose("--profile", "publication-report", "stop", "--timeout", "20", "publication-report")
    evidence = {"programDigest": "sha256:" + hashlib.sha256((inputs / "receiver").read_bytes()).hexdigest(),
                "completedReceipts": 3, "activationReport": True, "unpublishReport": True,
                "readerRestricted": True, "databaseIsolated": True, "fileMatchesDatabase": True,
                "pendingUnpublishRecovered": True, "missedActivationReconciled": True}
    assert valid_evidence(evidence)
    return evidence


if __name__ == "__main__":
    if sys.argv[1:] != ["prepare"]:
        raise SystemExit("Select publication-report fixture preparation.")
    prepare(os.environ)
