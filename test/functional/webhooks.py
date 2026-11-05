#!/usr/bin/env python3
"""Fixed enabled-webhook journey inside the existing invocation-owned stack."""

import base64
from concurrent.futures import ThreadPoolExecutor
import datetime
import hashlib
import hmac
import json
import os
from pathlib import Path
import re
import secrets
import shlex
import tempfile
import time
import urllib.request
import uuid

import webhook_crash
import publication_report

from federation import Client, key
from journeys import header
from http_client import fixture_tls_context, ClientFailure, request
from postgres_archive import dump_archive, temporary_archive
from operations import COMPONENTS, check_process_identity, diagnostics
from runner import (Failure, WEBHOOK_CASES, WEBHOOK_STATES, command, record_scenario_failure,
                    valid_webhook_database, valid_webhook_failure, write_document)


TENANT = "00000000-0000-7000-8000-000000000001"
ORIGIN = "https://federation-fixture:8443"


def worker_restart_phase(value, ready):
    # Historical delivery loss can keep webhook_delivery degraded even after
    # the dispatcher recovers. Check the supervised components independently.
    components = value["components"]
    if (value["role"] != "worker" or value["state"] != "running" or value["ready"] is not ready
            or value["dependencies"] != {"postgresql": ready}
            or set(components) != COMPONENTS | {"webhook_delivery"}
            or components["operations_server"] != "running"):
        return False
    if ready:
        return all(components[name] == "running" for name in COMPONENTS)
    return all(components[name] == "degraded" for name in ("job_worker", "outbox_processor"))


def check_retry_window(delivery):
    """Check persisted scheduling under this fixture's five-second ceiling.

    Its 429 receiver hint is one second, below the entire jitter window.
    Formula/dispersion tests remain isolated Go units; this checks the worker,
    configuration, PostgreSQL timestamps and API projection together.
    """
    delay = (datetime.datetime.fromisoformat(delivery["nextAttemptAt"])
             - datetime.datetime.fromisoformat(delivery["updatedAt"]))
    if (delivery["state"] != "waiting" or delivery["attempt"] not in (1, 2)
            or delivery["responseStatus"] != 429
            or not datetime.timedelta(seconds=2.5) <= delay <= datetime.timedelta(seconds=5)):
        raise Failure("persisted-webhook-retry-window-invalid")


class WaitFailure(Failure):
    """A closed last observation that survives the scenario/report boundary."""

    def __init__(self, observation):
        if not valid_webhook_failure(observation):
            raise Failure("invalid-webhook-observation")
        super().__init__("webhook-wait-failed")
        self.observation = dict(observation)


def observation_error(error):
    """Classify known client failures without copying free-form exception text."""
    if isinstance(error, AssertionError):
        match = re.fullmatch(r"functional federation request returned HTTP ([1-5][0-9]{2}) \((?:DEPENDENCY_UNAVAILABLE|REQUEST_DEADLINE_EXCEEDED|INTERNAL|unclassified)\)", str(error))
        return ("http", int(match[1])) if match else ("assertion", None)
    if isinstance(error, ClientFailure):
        message = str(error)
        if message in ("functional HTTP transport failed", "functional HTTP deadline"):
            return "transport", None
        if message in ("functional response contract mismatch", "functional response validator failed",
                       "functional response validator unavailable", "functional response contract limit"):
            return "contract", None
        return "invalid-response", None
    return "observation", None


class Journey:
    def __init__(self, environment):
        self.env = dict(environment)
        suffix = self.env["CATALOG_FUNCTIONAL_SUFFIX"]
        if not re.fullmatch(r"[0-9a-f]{12}", suffix):
            raise Failure("invalid-functional-owner")
        self.project = "open-agentic-catalog-functional-" + suffix
        self.fixture = Path(self.env["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"])
        self.state = Path(self.env["CATALOG_FUNCTIONAL_STATE_DIRECTORY"])
        self.env.update(IMAGE="open-agentic-catalog:functional-" + suffix, VERSION="functional",
                        COMPOSE_PROGRESS="quiet",
                        COMMIT=self.env["CATALOG_FUNCTIONAL_COMMIT"],
                        CATALOG_COMPOSE_HTTP_PORT=self.env["CATALOG_FUNCTIONAL_HTTP_PORT"],
                        CATALOG_COMPOSE_POSTGRES_PORT="0",
                        CATALOG_COMPOSE_FIXTURE_PORT=self.env["CATALOG_FUNCTIONAL_FIXTURE_PORT"],
                        CATALOG_COMPOSE_SUBNET=self.env["CATALOG_FUNCTIONAL_SUBNET"],
                        CATALOG_COMPOSE_FEDERATION_POLICY_FILE=str(self.fixture / "policy.json"),
                        CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY=str(self.fixture / "trust"),
                        CATALOG_WORKER_HOUSEKEEPING_INTERVAL="1m",
                        CATALOG_FUNCTIONAL_WEBHOOK_HOUSEKEEPING_INTERVAL="1h",
                        CATALOG_WORKER_TERMINAL_JOB_RETENTION="24h",
                        CATALOG_WORKER_ACKNOWLEDGED_OUTBOX_RETENTION="1h")
        self.base = self.env["CATALOG_FUNCTIONAL_BASE_URL"]
        self.receiver_url = self.env["CATALOG_FUNCTIONAL_FIXTURE_URL"]
        for value, scheme in ((self.base, "http"), (self.receiver_url, "https")):
            if not re.fullmatch(scheme + r"://127\.0\.0\.1:[0-9]{1,5}", value):
                raise Failure("nonlocal-functional-listener")
        self.api = Client(self.base, "local-admin")
        self.other = Client(self.base, "other-tenant-admin")
        self.tls = fixture_tls_context(str(self.fixture / "trust" / "functional-ca.pem"))
        self.receiver = Client(self.receiver_url, ssl_context=self.tls)
        self.completed = []
        self.crash_evidence = None
        self.report_evidence = None

    def compose(self, *arguments, enabled=True, capture=False, limit=8 << 20):
        argv = shlex.split(self.env.get("CATALOG_FUNCTIONAL_COMPOSE_COMMAND", "docker compose"))
        argv += ["--env-file", "/dev/null", "-f", "compose.yaml", "-f", "test/functional/compose.yaml"]
        if enabled:
            argv += ["-f", "test/functional/webhooks.compose.yaml"]
        argv += ["-p", self.project, *arguments]
        return command(argv, self.env, 90, limit=limit, capture=capture)

    def sql(self, statement):
        return self.compose("exec", "-T", "postgres", "psql", "--username", "catalog", "--dbname", "catalog", "--set", "ON_ERROR_STOP=1", "--tuples-only", "--no-align", "--command", statement, capture=True).decode().strip()

    def observation(self, label, expected=None):
        value = {"case": WEBHOOK_CASES[len(self.completed)], "wait": label,
                 "expectedState": expected, "observedState": "unobserved",
                 "attempt": None, "status": None, "category": "timeout"}
        if not valid_webhook_failure(value):
            raise Failure("invalid-webhook-observation")
        return value

    def wait(self, label, callback, seconds=45, observation=None):
        # In-flight HTTP, validation and Compose calls retain their own bounds.
        # Never accept their result after this polling budget has expired.
        observation = observation if observation is not None else self.observation(label)
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            try:
                value = callback()
                observation.update(category="timeout", status=None)
            except WaitFailure:
                raise
            except Exception as error:
                category, status = observation_error(error)
                observation.update(category=category, status=status)
                # Only startup/restart readiness may retry a known unavailable
                # listener. Steady-state delivery reads must succeed normally.
                transient = label == "api-ready" and (
                    category == "http" and status == 503 or
                    isinstance(error, ClientFailure) and str(error) == "functional HTTP transport failed")
                if not transient:
                    raise WaitFailure(observation) from None
                value = None
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                break
            if value:
                return value
            time.sleep(min(0.1, remaining))
        raise WaitFailure(observation)

    def ready(self, *, restarting=False):
        self.wait("api-ready" if restarting else "api-health", lambda: self.api.request("GET", "/readyz", retry_unavailable=restarting)[0])
        for path in ("livez", "readyz"):
            self.compose("exec", "-T", "worker", "/usr/local/bin/catalogctl", "probe", "--url", "http://127.0.0.1:9090/" + path)

    def record(self, name):
        self.completed.append(name)
        evidence = {"completed": self.completed}
        if self.crash_evidence is not None:
            evidence["crash"] = self.crash_evidence
        if self.report_evidence is not None:
            evidence["publicationReport"] = self.report_evidence
        write_document(self.state / "webhooks.json", evidence)
        print("Webhook case passed: " + name, flush=True)

    def record_failure(self, error):
        if isinstance(error, WaitFailure):
            try:
                evidence = {"completed": self.completed, "failure": error.observation}
                if self.crash_evidence is not None:
                    evidence["crash"] = self.crash_evidence
                if self.report_evidence is not None:
                    evidence["publicationReport"] = self.report_evidence
                write_document(self.state / "webhooks.json", evidence)
            except (OSError, Failure):
                pass  # Reporting must not replace the failure or skip cleanup.

    def configure(self, endpoint, status=204, delay=0):
        self.receiver.request("POST", "/webhooks/control", {"endpointId": endpoint["endpoint"]["id"], "secret": endpoint["secret"], "version": endpoint["endpoint"]["secretVersion"], "status": status, "delayMillis": delay}, expected=(204,))

    def stats(self):
        return self.receiver.request("GET", "/webhooks/state")[0]

    def namespace(self, client=None):
        return (client or self.api).request("POST", "/api/v1/namespaces", {"slug": "webhook-" + uuid.uuid4().hex, "displayName": "Webhook qualification"}, expected=(201,), headers={"Idempotency-Key": key("namespace")})[0]

    def register(self, client=None, url=ORIGIN + "/webhooks/events"):
        return (client or self.api).request("POST", "/api/v1/webhooks", {"name": "Owned receiver", "url": url, "eventTypes": ["namespace.created"]}, expected=(201,), headers={"Idempotency-Key": key("register")})[0]

    def management(self, endpoint):
        """Read and edit the first endpoint without retrieving another secret."""
        path = "/api/v1/webhooks/" + endpoint["endpoint"]["id"]
        listed, _ = self.api.request("GET", "/api/v1/webhooks?limit=1")
        assert [item["id"] for item in listed["items"]] == [endpoint["endpoint"]["id"]], "webhook list omitted owned endpoint"
        current, headers = self.api.request("GET", path)
        # Creation returns the service timestamp; PostgreSQL stores
        # microseconds. Compare instants at stored precision, not JSON text.
        def persisted(value):
            return {key: datetime.datetime.fromisoformat(item.replace("Z", "+00:00"))
                    if key in ("createdAt", "updatedAt") else item for key, item in value.items()}
        assert persisted(current) == persisted(endpoint["endpoint"]), "webhook exact read differs from creation"
        command_headers = {"Idempotency-Key": key("webhook-update"), "If-Match": header(headers, "ETag")}
        body = {"name": "Updated owned receiver"}
        changed, _ = self.api.request("PATCH", path, body, headers=command_headers)
        replay, _ = self.api.request("PATCH", path, body, headers=command_headers)
        retained, _ = self.api.request("GET", path)
        assert changed == replay == retained, "webhook update replay or readback differs"
        assert retained["name"] == body["name"] and retained["revision"] == current["revision"] + 1, "webhook update not persisted"
        assert retained["secretVersion"] == current["secretVersion"] and "secret" not in retained, "metadata edit rotated or disclosed secret"
        return {**endpoint, "endpoint": retained}

    def test(self, endpoint, expected=(200,)):
        return self.api.request("POST", "/api/v1/webhooks/" + endpoint["endpoint"]["id"] + ":test", {}, expected=expected, headers={"Idempotency-Key": key("test"), "If-Match": '"rev-' + str(endpoint["endpoint"]["revision"]) + '"'})[0]

    def event_id(self, namespace):
        identifier = namespace["id"]
        if not re.fullmatch(r"[a-f0-9-]{36}", identifier):
            raise Failure("invalid-namespace-identity")
        return self.sql("SELECT id FROM outbox_events WHERE tenant_id='" + namespace["tenantId"] + "' AND subject='" + identifier + "' ORDER BY created_at DESC LIMIT 1")

    def delivery(self, endpoint, event_id, state="succeeded", client=None, label="delivery", *, attempt=None):
        observation = self.observation(label, state)
        def read():
            observation.update(observedState="unobserved", attempt=None)
            page = (client or self.api).request("GET", "/api/v1/webhooks/" + endpoint["endpoint"]["id"] + "/deliveries?limit=100")[0]
            item = next((item for item in page["items"] if item["eventId"] == event_id), None)
            if item is None:
                observation.update(observedState="missing")
                return None
            if (item["state"] not in WEBHOOK_STATES or type(item["attempt"]) is not int
                    or not 0 <= item["attempt"] <= 10000):
                raise ClientFailure("functional JSON response invalid")
            observation.update(observedState=item["state"], attempt=item["attempt"])
            if item["state"] == state and (attempt is None or item["attempt"] == attempt):
                return item
            if item["state"] in ("succeeded", "dead_letter"):
                observation.update(category="unexpected-terminal")
                raise WaitFailure(observation)
            return None
        try:
            return self.wait(label, read, observation=observation)
        except WaitFailure as error:
            if error.observation["observedState"] == "missing":
                database = self.delivery_diagnostic(endpoint, event_id)
                if database is not None:
                    error.observation["database"] = database
            raise

    def delivery_diagnostic(self, endpoint, event_id):
        """One read-only snapshot for the exact owned pair; never retain IDs or SQL errors."""
        identity = endpoint["endpoint"]
        tenant, endpoint_id = identity.get("tenantId"), identity.get("id")
        if not all(isinstance(value, str) and re.fullmatch(r"[0-9a-f]{8}-(?:[0-9a-f]{4}-){3}[0-9a-f]{12}", value)
                   for value in (tenant, endpoint_id, event_id)):
            return None
        try:
            raw = self.sql("BEGIN READ ONLY; SET LOCAL statement_timeout='2s'; SELECT json_build_object("
                           "'eventExists',event.id IS NOT NULL,'endpointExists',endpoint.id IS NOT NULL,"
                           "'eventPredatesEndpoint',event.created_at<endpoint.created_at,"
                           "'eventAcknowledged',event.published_at IS NOT NULL,"
                           "'eventLeased',event.lease_expires_at>clock_timestamp(),"
                           "'deliveryExists',delivery.id IS NOT NULL,'endpointActive',endpoint.status='active',"
                           "'eventTypeMatches',endpoint.event_types @> ARRAY['*']::text[] OR endpoint.event_types @> ARRAY[event.event_type]) "
                           "FROM (SELECT 1) seed "
                           "LEFT JOIN outbox_events event ON event.tenant_id='" + tenant + "' AND event.id='" + event_id + "' "
                           "LEFT JOIN webhook_endpoints endpoint ON endpoint.tenant_id='" + tenant + "' AND endpoint.id='" + endpoint_id + "' "
                           "LEFT JOIN webhook_deliveries delivery ON delivery.tenant_id='" + tenant + "' AND delivery.endpoint_id='" + endpoint_id + "' AND delivery.event_id='" + event_id + "'; COMMIT;")
            if len(raw) > 4096:
                return "unavailable"
            lines = [line for line in raw.splitlines() if line.startswith("{")]
            value = json.loads(lines[0]) if len(lines) == 1 else None
            return value if valid_webhook_database(value) else "unavailable"
        except Exception:
            # Optional evidence must not replace the original delivery failure.
            return "unavailable"

    def mutation(self, endpoint, state="succeeded", client=None, label="delivery"):
        namespace = self.namespace(client)
        event_id = self.event_id(namespace)
        return namespace, self.delivery(endpoint, event_id, state, client, label)

    def reconciliation(self, client=None):
        return (client or self.api).request("GET", "/api/v1/webhooks/reconciliation")[0]

    def worker_body(self, path):
        address = self.compose("port", "worker", "9090", capture=True).decode().strip()
        if not re.fullmatch(r"127\.0\.0\.1:[0-9]+", address):
            raise Failure("nonlocal-worker-listener")
        status, _, body = request(urllib.request.Request("http://" + address + path))
        if status != 200:
            raise Failure("worker-observation-failed")
        return body

    def process_identities(self):
        identifiers = [self.compose("ps", "-q", service, capture=True).decode().strip() for service in ("api", "worker")]
        if not all(re.fullmatch(r"[0-9a-f]{64}", identifier) for identifier in identifiers):
            raise Failure("missing-recovery-process")
        engine = shlex.split(self.env.get("CATALOG_FUNCTIONAL_ENGINE_COMMAND", "docker"))
        raw = command(engine + ["inspect", "--format", "[{{json .Id}},{{.State.Pid}},{{json .State.StartedAt}},{{.RestartCount}}]", *identifiers], self.env, 10, limit=4096, capture=True)
        return [json.loads(line) for line in raw.splitlines()]

    def database_recovery(self, endpoint):
        before = self.process_identities()
        effective = self.compose("exec", "-T", "worker", "/usr/local/bin/catalogctl", "diagnostics", "--config", "/config/config.json", "--role", "worker", capture=True)
        diagnostics(json.loads(effective), self.env["CATALOG_FUNCTIONAL_COMMIT"], "worker")
        self.compose("stop", "--timeout", "10", "postgres")
        self.wait("database-down", lambda: worker_restart_phase(json.loads(self.worker_body("/status")), False))
        self.api.request("GET", "/readyz", expected=(503,))
        self.api.request("GET", "/livez")
        self.worker_body("/livez")
        self.compose("start", "postgres")
        self.ready(restarting=True)
        self.wait("dispatcher-recovered", lambda: worker_restart_phase(json.loads(self.worker_body("/status")), True))
        namespace, delivered = self.mutation(endpoint)
        if delivered["state"] != "succeeded" or self.api.request("GET", "/api/v1/namespaces/" + namespace["id"])[0]["id"] != namespace["id"]:
            raise Failure("fresh-webhook-recovery-failed")
        check_process_identity(before, self.process_identities())
        self.record("database-outage-and-dispatcher-recovery")

    def send_reference(self, endpoint, event_id, at):
        raw = json.dumps({"specversion": "1.0", "id": event_id, "source": "https://github.com/open-agentic-catalog/open-agentic-catalog/tenants/" + TENANT, "type": "com.open.agentic.catalog.namespace.created.v1", "time": at, "data": {"action": "namespace.created", "targetType": "namespace", "targetId": str(uuid.uuid4())}}, separators=(",", ":")).encode()
        stamp = str(int(time.time()))
        signature = hmac.new(endpoint["secret"].encode(), stamp.encode() + b"." + raw, hashlib.sha256).hexdigest()
        headers = {"Content-Type": "application/cloudevents+json", "X-Catalog-Timestamp": stamp, "X-Catalog-Webhook-ID": endpoint["endpoint"]["id"], "X-Catalog-Signature": "v1,kid=" + str(endpoint["endpoint"]["secretVersion"]) + ",sha256=" + signature}
        status, _, _ = request(urllib.request.Request(self.receiver_url + "/webhooks/events", data=raw, headers=headers, method="POST"), self.tls)
        if status != 204:
            raise Failure("receiver-contract-failed")

    def rotate(self, endpoint, emergency=False):
        return self.api.request("POST", "/api/v1/webhooks/" + endpoint["endpoint"]["id"] + ":rotate-secret", {"revokePrevious": emergency}, headers={"Idempotency-Key": key("rotate"), "If-Match": '"rev-' + str(endpoint["endpoint"]["revision"]) + '"'})[0]

    def replace_key(self, endpoint):
        self.compose("stop", "--timeout", "20", "api", "worker")
        directory = self.fixture / "webhooks"
        old = (directory / "encryption.key").read_bytes()
        for name, content in (("archived.key", old), ("replacement.key", base64.b64encode(secrets.token_bytes(32))), ("third.key", base64.b64encode(secrets.token_bytes(32)))):
            (directory / name).write_bytes(content)
            (directory / name).chmod(0o444)
        # The backup and restore stay inside this disposable PostgreSQL
        # container. No production backup topology or external storage exists.
        with temporary_archive(self.compose, "postgres") as archive:
            dump_archive(self.compose, "postgres", archive)
            self.compose("exec", "-T", "postgres", "createdb", "-U", "catalog", "catalog_webhook_restore")
            self.compose("exec", "-T", "postgres", "pg_restore", "-U", "catalog", "-d", "catalog_webhook_restore", archive)
        args = ["repair", "webhook-secrets", "--config", "/config/config.json", "--runtimes-stopped", "--tenant", TENANT, "--reason", "Owned webhook key qualification", "--old-key-file", "/config/webhooks/archived.key", "--new-key-file", "/config/webhooks/replacement.key", "--limit", "100"]
        result = json.loads(self.compose("run", "--rm", "--no-deps", "migrate", *args, capture=True))
        if not result.get("changed"):
            raise Failure("key-replacement-did-not-change")
        repeated = json.loads(self.compose("run", "--rm", "--no-deps", "migrate", *args, capture=True))
        if repeated.get("changed") != 0:
            raise Failure("key-replacement-not-idempotent")
        restore_env = ["run", "--rm", "--no-deps", "--env", "CATALOG_DATABASE_DSN=postgres://catalog:catalog@postgres:5432/catalog_webhook_restore?sslmode=disable", "migrate"]
        wrong = list(args)
        wrong[wrong.index("/config/webhooks/archived.key")] = "/config/webhooks/replacement.key"
        wrong[wrong.index("--new-key-file") + 1] = "/config/webhooks/third.key"
        try:
            self.compose(*restore_env, *wrong)
        except Failure:
            pass
        else:
            raise Failure("old-backup-accepted-new-key")
        self.compose(*restore_env, *args)
        descriptor, pending = tempfile.mkstemp(dir=directory, prefix=".replacement-")
        with os.fdopen(descriptor, "wb") as output:
            output.write((directory / "replacement.key").read_bytes())
        os.chmod(pending, 0o444)
        os.replace(pending, directory / "encryption.key")
        # Only tenant A has retained endpoints at this stage.
        self.compose("up", "--detach", "--no-deps", "--force-recreate", "api", "worker")
        self.ready(restarting=True)
        self.mutation(endpoint)

    def qualify_retention(self, endpoint):
        # Finish startup housekeeping before creating the two retained rows.
        # The overlay puts its next cycle beyond the whole scenario deadline.
        self.wait("initial-housekeeping", lambda: re.search(
            rb'^open_agentic_catalog_housekeeping_cycles_total\{outcome="succeeded"\} [1-9][0-9]*$',
            self.worker_body("/metrics"), re.MULTILINE))
        synthetic = self.test(endpoint)
        _, retained = self.mutation(endpoint)
        identifiers = [synthetic["id"], retained["id"]]
        if len(set(identifiers)) != 2 or not all(re.fullmatch(r"[a-f0-9-]{36}", value) for value in identifiers):
            raise Failure("invalid-delivery-identity")
        selected = "','".join(identifiers)
        # A terminal recipient delivery is observable before its parent outbox
        # acknowledgement commits. Retention requires the latter, so leave the
        # worker running until that exact same-tenant parent is unleased.
        self.wait("delivery-parent-acknowledged", lambda: self.sql("SELECT count(*) FROM webhook_deliveries d JOIN outbox_events e ON e.tenant_id=d.tenant_id AND e.id=d.event_id AND e.event_type=d.event_type WHERE d.tenant_id='" + TENANT + "' AND d.id='" + retained["id"] + "' AND e.published_at IS NOT NULL AND e.lease_owner IS NULL AND e.lease_expires_at IS NULL") == "1")
        self.compose("stop", "--timeout", "20", "worker")
        marked = self.sql("WITH marked AS (UPDATE webhook_deliveries SET cleanup_after=clock_timestamp()+interval '500 milliseconds' WHERE tenant_id='" + TENANT + "' AND id IN ('" + selected + "') AND cleanup_after IS NULL RETURNING id) SELECT count(*) FROM marked")
        if marked != "2":
            raise Failure("delivery-cleanup-fixture-not-marked")
        self.wait("delivery-markers-elapsed", lambda: self.sql("SELECT count(*) FROM webhook_deliveries WHERE tenant_id='" + TENANT + "' AND id IN ('" + selected + "') AND cleanup_after<=clock_timestamp()") == "2")
        # Cleanup uses bounded SKIP LOCKED batches, and database time can move
        # after the expiry observation. One startup sweep is not a guarantee.
        # Only this phase uses a short cadence; keep the normal setup isolated.
        interval_key = "CATALOG_FUNCTIONAL_WEBHOOK_HOUSEKEEPING_INTERVAL"
        previous = self.env.get(interval_key)
        self.env[interval_key] = "1m"
        try:
            self.compose("up", "--detach", "--no-deps", "--force-recreate", "worker")
        finally:
            if previous is None:
                self.env.pop(interval_key, None)
            else:
                self.env[interval_key] = previous
        self.ready()
        self.wait("delivery-cleanup", lambda: self.sql("SELECT count(*) FROM webhook_deliveries WHERE tenant_id='" + TENANT + "' AND id IN ('" + selected + "')") == "0", seconds=125)
        if self.sql("SELECT count(*) FROM audit_events WHERE tenant_id='" + TENANT + "' AND target_id IN ('" + selected + "')") != "2":
            raise Failure("delivery-cleanup-lost-audit")
        self.record("prospective-delivery-retention")

    def run(self):
        self.api.request("GET", "/api/v1/webhooks/reconciliation", expected=(404,))
        self.compose("stop", "--timeout", "20", "api", "worker", enabled=False)
        self.compose("up", "--detach", "--no-deps", "--force-recreate", "api", "worker")
        self.ready(restarting=True)
        endpoint = self.register()
        endpoint = self.management(endpoint)
        self.configure(endpoint)
        namespace, delivery = self.mutation(endpoint)
        if delivery["eventId"] not in self.stats()["ids"]:
            raise Failure("worker-signature-not-verified")
        # Qualify the diagnostic against a known successful pair in real PostgreSQL.
        diagnostic = self.delivery_diagnostic(endpoint, delivery["eventId"])
        if (not valid_webhook_database(diagnostic) or not all(diagnostic[field] for field in
                ("eventExists", "endpointExists", "deliveryExists", "endpointActive", "eventTypeMatches"))
                or diagnostic["eventPredatesEndpoint"] is not False):
            raise Failure("webhook-database-diagnostic-control-failed")
        absent = self.delivery_diagnostic(endpoint, str(uuid.uuid4()))
        if (not valid_webhook_database(absent) or absent["eventExists"] is not False
                or absent["deliveryExists"] is not False or absent["eventPredatesEndpoint"] is not None):
            raise Failure("webhook-database-diagnostic-absent-control-failed")
        self.other.request("GET", "/api/v1/webhooks/" + endpoint["endpoint"]["id"], expected=(404,))
        Client(self.base, "local-contributor").request("GET", "/api/v1/webhooks/reconciliation", expected=(403,))
        self.record("signed-delivery-and-authorization")

        before = self.stats()
        self.send_reference(endpoint, delivery["eventId"], datetime.datetime.now(datetime.timezone.utc).isoformat())
        self.send_reference(endpoint, str(uuid.uuid4()), "2000-01-01T00:00:00Z")
        after = self.stats()
        if after["duplicates"] != before["duplicates"] + 1 or after["outOfOrder"] <= before["outOfOrder"]:
            raise Failure("deduplication-or-ordering-failed")
        current = self.api.request("GET", "/api/v1/namespaces/" + namespace["id"])[0]
        if current["revision"] != namespace["revision"]:
            raise Failure("notification-replaced-current-state")
        self.record("duplicate-and-out-of-order-reconciliation")

        self.configure(endpoint, status=429)
        _, waiting = self.mutation(endpoint, "waiting", label="throttle-waiting")
        check_retry_window(waiting)
        self.wait("persisted-retry-metric", lambda: re.search(
            rb"^open_agentic_catalog_webhook_retry_delay_seconds_count [1-9][0-9]*$",
            self.worker_body("/metrics"), re.MULTILINE))
        self.ready()
        metrics = self.worker_body("/metrics").decode()
        status = json.loads(self.worker_body("/status"))
        if "open_agentic_catalog_webhook_waiting_retries 1" not in metrics or status["components"].get("webhook_delivery") != "degraded":
            raise Failure("receiver-failure-not-observable")
        second = self.delivery(endpoint, waiting["eventId"], "waiting", label="throttle-waiting", attempt=2)
        check_retry_window(second)
        self.configure(endpoint)
        retried = self.delivery(endpoint, waiting["eventId"], label="retry-recovery")
        if (retried["attempt"] != 3 or retried["id"] != second["id"] or retried.get("nextAttemptAt") is not None
                or datetime.datetime.fromisoformat(retried["updatedAt"]) < datetime.datetime.fromisoformat(second["nextAttemptAt"])):
            raise Failure("retry-not-persisted")
        self.configure(endpoint, status=401)
        self.mutation(endpoint, "dead_letter", label="terminal-response")
        if not re.search(rb"^open_agentic_catalog_webhook_retry_delay_seconds_count 2$", self.worker_body("/metrics"), re.MULTILINE):
            raise Failure("terminal-or-successful-delivery-counted-as-retry")
        if self.reconciliation()["deliveryLossCount"] < 1:
            raise Failure("terminal-loss-not-reported")
        before = self.reconciliation()["deliveryLossCount"]
        self.configure(endpoint, status=503)
        _, exhausted = self.mutation(endpoint, "dead_letter", label="retry-exhaustion")
        if exhausted["attempt"] != 3 or self.reconciliation()["deliveryLossCount"] != before + 1:
            raise Failure("retry-exhaustion-not-reconciled")
        self.configure(endpoint)
        self.record("retry-throttle-loss-and-readiness")

        self.database_recovery(endpoint)

        rotated = self.rotate(endpoint)
        if self.test(rotated)["state"] != "succeeded":
            raise Failure("routine-overlap-failed")
        endpoint = rotated
        self.configure(endpoint)
        revoked = self.rotate(endpoint, emergency=True)
        if self.test(revoked)["state"] != "dead_letter":
            raise Failure("emergency-key-was-not-revoked")
        endpoint = revoked
        self.configure(endpoint)
        if self.test(endpoint)["state"] != "succeeded":
            raise Failure("new-signing-key-failed")
        self.record("routine-and-emergency-signing-rotation")

        self.configure(endpoint, delay=700)
        with ThreadPoolExecutor(max_workers=1) as executor:
            first = executor.submit(self.test, endpoint)
            self.wait("test-active", lambda: self.stats()["active"] == 1)
            self.test(endpoint, expected=(503,))
            if first.result(timeout=5)["state"] != "succeeded":
                raise Failure("admitted-test-failed")
        self.configure(endpoint)
        self.record("api-test-admission")

        self.crash_evidence = webhook_crash.qualify(self, endpoint)
        self.record("worker-loss-and-lease-recovery")

        self.replace_key(endpoint)
        self.record("encryption-key-replacement-and-old-backup")

        other_endpoint = self.register(self.other)
        self.configure(other_endpoint)
        self.configure(endpoint, delay=300)
        self.compose("up", "--detach", "--no-deps", "--scale", "worker=2", "worker")
        for _ in range(8):
            self.namespace()
        started = time.monotonic()
        self.mutation(other_endpoint, client=self.other)
        if time.monotonic() - started > 15:
            raise Failure("competing-tenant-progress-failed")
        for _ in range(8):
            self.namespace()
        self.wait("two-worker-drain", lambda: self.reconciliation()["pendingEvents"] == 0)
        if self.stats()["maxActive"] != 2:
            raise Failure("serial-worker-concurrency-bound-failed")
        self.configure(endpoint)
        self.compose("up", "--detach", "--no-deps", "--scale", "worker=1", "worker")
        self.record("two-workers-and-competing-tenant")

        self.compose("stop", "--timeout", "20", "worker")
        self.sql("INSERT INTO outbox_events(id,tenant_id,event_type,subject,data,created_at) SELECT uuidv7(),'" + TENANT + "','com.open.agentic.catalog.resource.created.v1','00000000-0000-7000-8000-000000000099',jsonb_build_object('action','resource.created','targetType','resource','targetId','00000000-0000-7000-8000-000000000099'),clock_timestamp() FROM generate_series(1,1000)")
        before = self.reconciliation()["capacityLossCount"]
        lost = self.namespace()
        if self.reconciliation()["capacityLossCount"] != before + 1:
            raise Failure("capacity-loss-not-reported")
        self.api.request("GET", "/api/v1/namespaces/" + lost["id"])
        self.compose("start", "worker")
        self.ready()
        self.wait("capacity-drain", lambda: self.reconciliation()["pendingEvents"] == 0)
        if self.event_id(lost) in self.stats()["ids"]:
            raise Failure("capacity-retired-event-was-delivered")
        self.record("capacity-and-current-state-reconciliation")

        self.qualify_retention(endpoint)

        self.report_evidence = publication_report.qualify(self)
        self.record("publication-report-example")

        self.configure(endpoint, status=503)
        _, waiting = self.mutation(endpoint, "waiting")
        self.api.request("DELETE", "/api/v1/webhooks/" + endpoint["endpoint"]["id"], expected=(204,), headers={"Idempotency-Key": key("disable"), "If-Match": '"rev-' + str(endpoint["endpoint"]["revision"]) + '"'})
        self.wait("disabled-endpoint-drain", lambda: self.reconciliation()["pendingEvents"] == 0)
        if "open_agentic_catalog_webhook_waiting_retries 0" not in self.worker_body("/metrics").decode():
            raise Failure("abandoned-waiting-row-remained-dispatchable")
        self.delivery(endpoint, waiting["eventId"], "waiting")
        self.compose("stop", "--timeout", "20", "api", "worker")
        self.compose("up", "--detach", "--no-deps", "--force-recreate", "api", "worker", enabled=False)
        self.ready(restarting=True)
        self.api.request("GET", "/api/v1/webhooks/reconciliation", expected=(404,))
        disabled = self.namespace(self.other)
        if self.sql("SELECT count(*) FROM outbox_events WHERE tenant_id='" + disabled["tenantId"] + "' AND subject='" + disabled["id"] + "' AND published_at IS NOT NULL") != "1":
            raise Failure("disabled-write-left-notification")
        self.compose("stop", "--timeout", "20", "api", "worker", enabled=False)
        self.compose("up", "--detach", "--no-deps", "--force-recreate", "api", "worker")
        self.ready(restarting=True)
        self.mutation(other_endpoint, client=self.other)
        if self.event_id(disabled) in self.stats()["ids"]:
            raise Failure("reenabled-worker-backfilled-disabled-period")
        self.compose("stop", "--timeout", "20", "api", "worker")
        self.compose("up", "--detach", "--no-deps", "--force-recreate", "api", "worker", enabled=False)
        self.ready(restarting=True)
        self.record("endpoint-disable-and-default-off-transition")


def main():
    journey = None
    try:
        journey = Journey(os.environ)
        journey.run()
        return 0
    except Exception as error:
        if journey is not None:
            journey.record_failure(error)
        record_scenario_failure("webhooks", error)
        # Never include exceptions, HTTP data, secrets or subprocess output.
        print("Enabled webhook qualification failed; see bounded case progress.", flush=True)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
