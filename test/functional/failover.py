#!/usr/bin/env python3
"""Bounded physical promotion checks inside an invocation-owned Compose stack."""

from concurrent.futures import ThreadPoolExecutor
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import time
import urllib.request

from core import mutate
import authorization_recovery
from federation import Client
import http_client
from journeys import audit_once, header, namespace_state
from operators import Operators, TENANT
from runner import Failure, command, write_document


PHASES = ("replication", "baseline", "blocked-work", "replicated-barrier", "writer-loss",
          "no-writer", "promotion", "recovery", "repair", "verification", "complete")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")
LSN = re.compile(r"[0-9A-F]{1,8}/[0-9A-F]{1,8}\Z")


def valid_evidence(value, profile):
    """Reject partial/extra report fields; never promote a partial run to pass."""
    fields = {"scenario", "replication", "barrier", "replayedBarrier", "postgresVersions",
              "processDigests", "writerChanged", "completedReplay", "pendingRepair",
              "jobRecovered", "retainedPublicState", "policyRetained", "baselineLost", "recoverySeconds"}
    return (profile in ("failover-planned", "failover-abrupt") and isinstance(value, dict) and set(value) == fields
            and value["scenario"] == profile and value["replication"] == "asynchronous-physical"
            and all(isinstance(value[k], str) and LSN.fullmatch(value[k]) for k in ("barrier", "replayedBarrier"))
            and lsn_number(value["replayedBarrier"]) >= lsn_number(value["barrier"])
            and isinstance(value["postgresVersions"], list) and len(value["postgresVersions"]) == 2
            and all(type(v) is int and 180000 <= v < 190000 for v in value["postgresVersions"])
            and value["postgresVersions"][0] == value["postgresVersions"][1]
            and isinstance(value["processDigests"], dict) and set(value["processDigests"]) == {"api", "worker"}
            and all(isinstance(v, str) and DIGEST.fullmatch(v) for v in value["processDigests"].values())
            and all(value[k] is True for k in ("writerChanged", "completedReplay", "retainedPublicState", "policyRetained"))
            and value["pendingRepair"] is (profile == "failover-abrupt")
            and value["jobRecovered"] is (profile == "failover-abrupt")
            and type(value["baselineLost"]) is int and value["baselineLost"] == 0
            and type(value["recoverySeconds"]) in (int, float) and 0 <= value["recoverySeconds"] <= 180)


def lsn_number(value):
    upper, lower = value.split("/")
    return (int(upper, 16) << 32) + int(lower, 16)


def wait_for(callback, seconds=45):
    """Accept timely truthy observations only; unexpected errors fail now."""
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        value = callback()
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            break
        if value:
            return value
        time.sleep(min(0.1, remaining))
    raise Failure("failover-phase-timeout")


class Failover(Operators):
    def __init__(self, environment):
        super().__init__(environment)
        self.profile = environment["CATALOG_FUNCTIONAL_PROFILE"]
        if self.profile not in ("failover-planned", "failover-abrupt"):
            raise Failure("invalid-failover-profile")
        self.state = Path(environment["CATALOG_FUNCTIONAL_STATE_DIRECTORY"])
        self.writer = "postgres"
        self.engine = shlex.split(environment.get("CATALOG_FUNCTIONAL_ENGINE_COMMAND", "docker"))
        self.end = time.monotonic() + 300
        self.evidence = {"scenario": self.profile, "replication": "asynchronous-physical"}

    def phase(self, value):
        if value not in PHASES:
            raise Failure("invalid-failover-phase")
        write_document(self.state / "failover-phase.json", {"phase": value})

    def compose(self, *args, capture=False):
        argv = shlex.split(self.env.get("CATALOG_FUNCTIONAL_COMPOSE_COMMAND", "docker compose"))
        return command(argv + ["--env-file", "/dev/null", "-f", "compose.yaml", "-f", "test/functional/compose.yaml",
                               "-f", "test/functional/failover.compose.yaml", "-p", self.project, *args],
                       self.env, min(90, self.end - time.monotonic()), capture=capture, limit=1 << 20)

    def sql(self, statement, server=None):
        return self.compose("exec", "-T", server or self.writer, "psql", "-X", "-U", "catalog", "-d", "catalog",
                            "-v", "ON_ERROR_STOP=1", "-Atc", statement, capture=True).decode().strip()

    def repair(self, action, *args):
        return http_client.decode(self.compose("run", "--rm", "--no-deps", "-e",
            "CATALOG_DATABASE_DSN=postgres://catalog:catalog@standby:5432/catalog?sslmode=disable&connect_timeout=2",
            "--entrypoint", "/usr/local/bin/catalogctl", "migrate", "repair", "idempotency", action,
            "--config", "/config/config.json", "--tenant", TENANT, *args, capture=True))

    def identity(self, service):
        identifier = self.compose("ps", "--all", "-q", service, capture=True).decode().strip()
        if not re.fullmatch(r"[0-9a-f]{64}", identifier):
            raise Failure("failover-container-identity")
        raw = command(self.engine + ["inspect", "--format", "{{json .State}}", identifier], self.env, 10, capture=True)
        state = http_client.decode(raw)
        if not state["Running"] or state["Restarting"]:
            raise Failure("failover-process-not-running")
        # Container ID, process PID and start time detect manual and automatic restarts.
        return "sha256:" + hashlib.sha256(json.dumps([identifier, state["Pid"], state["StartedAt"]]).encode()).hexdigest()

    def health(self, expected):
        for base in (self.api.base_url, self.worker_url):
            live, _, _ = http_client.request(urllib.request.Request(base + "/livez"))
            ready, _, _ = http_client.request(urllib.request.Request(base + "/readyz"))
            if live != 200 or ready not in (200, 503):
                raise Failure("failover-invalid-health")
            if ready != expected:
                return False
        return True

    def setup(self):
        self.phase("replication")
        password = self.env["CATALOG_FUNCTIONAL_REPLICATION_PASSWORD"]
        if not re.fullmatch(r"[0-9a-f]{48}", password):
            raise Failure("invalid-replication-credential")
        self.sql("CREATE ROLE catalog_replication LOGIN REPLICATION PASSWORD '" + password + "'")
        # Only this owned database's replication identity can use this rule.
        self.compose("exec", "-T", "postgres", "bash", "-ec",
                     "printf '\nhost replication catalog_replication all scram-sha-256\n' >> \"$PGDATA/pg_hba.conf\"")
        self.sql("SELECT pg_reload_conf()")
        self.compose("up", "--no-deps", "--detach", "standby")
        # pg_isready is the only startup polling that expects child failure.
        def standby_ready():
            try:
                self.compose("exec", "-T", "standby", "pg_isready", "-h", "127.0.0.1", "-U", "catalog", "-d", "catalog")
                return True
            except Failure as error:
                if str(error) != "child-failed":
                    raise
                return False
        wait_for(standby_ready, 90)
        assert self.sql("SELECT pg_is_in_recovery()", "standby") == "t"
        assert self.sql("SHOW synchronous_standby_names") == ""
        # Accepting SQL connections does not establish that the WAL sender
        # has reached streaming state. Observe that separate prerequisite.
        wait_for(lambda: self.sql("SELECT count(*) FROM pg_stat_replication WHERE usename='catalog_replication' AND state='streaming' AND sync_state='async'") == "1")
        self.evidence["postgresVersions"] = [int(self.sql("SHOW server_version_num", name)) for name in ("postgres", "standby")]
        assert self.evidence["postgresVersions"][0] == self.evidence["postgresVersions"][1]
        address = self.compose("port", "worker", "9090", capture=True).decode().strip()
        if not re.fullmatch(r"127\.0\.0\.1:[0-9]{1,5}", address):
            raise Failure("nonlocal-worker-listener")
        self.worker_url = "http://" + address
        self.processes = {name: self.identity(name) for name in ("api", "worker")}

    def baseline(self):
        self.phase("baseline")
        self.headers = {"Idempotency-Key": "failover-complete-" + self.suffix}
        self.body = {"slug": "complete-" + self.suffix, "displayName": "Writer recovery baseline"}
        self.completed, self.completed_headers = self.api.request("POST", "/api/v1/namespaces", self.body, (201,), self.headers, decode_body=False)
        assert header(self.completed_headers, "ETag") == '"rev-1"'
        created = http_client.decode(self.completed)
        assert all(created[name] == value for name, value in self.body.items()) and created["revision"] == 1
        self.policy = authorization_recovery.prepare(self.api, created["id"], "failover-" + self.suffix)
        self.public, self.public_headers = Client(self.api.base_url).request("GET", "/.well-known/ard.json", decode_body=False)
        assert header(self.public_headers, "ETag") == '"' + hashlib.sha256(self.public).hexdigest() + '"'
        # Exclude acknowledgement/lease fields: they legitimately progress.
        self.outbox = self.sql("SELECT coalesce(jsonb_agg(v ORDER BY id),'[]') FROM (SELECT id,tenant_id,event_type,subject,data,created_at FROM outbox_events ORDER BY id LIMIT 1001) v")
        assert 0 < len(json.loads(self.outbox)) <= 1000

    def barrier(self):
        self.phase("replicated-barrier")
        lsn = self.sql("SELECT pg_current_wal_flush_lsn()")
        if not LSN.fullmatch(lsn):
            raise Failure("invalid-wal-barrier")
        wait_for(lambda: self.sql("SELECT pg_last_wal_replay_lsn() >= '" + lsn + "'::pg_lsn", "standby") == "t")
        self.evidence.update(barrier=lsn, replayedBarrier=self.sql("SELECT pg_last_wal_replay_lsn()", "standby"))

    def promote(self):
        self.phase("writer-loss")
        started = time.monotonic()
        if self.profile == "failover-abrupt":
            self.compose("kill", "--signal", "SIGKILL", "postgres")
        else:
            # SIGINT is PostgreSQL fast shutdown. An exec'd pg_ctl would itself
            # be killed when the PID-1 server exits, giving a misleading error.
            self.compose("kill", "--signal", "SIGINT", "postgres")
        wait_for(lambda: not self.compose("ps", "--status", "running", "-q", "postgres", capture=True).strip(), 30)
        self.phase("no-writer")
        wait_for(lambda: self.health(503))
        self.phase("promotion")
        assert self.sql("SELECT pg_promote(true, 30)", "standby") == "t"
        self.writer = "standby"
        assert self.sql("SELECT pg_is_in_recovery()") == "f"
        self.phase("recovery")
        wait_for(lambda: self.health(200), 60)
        self.evidence["recoverySeconds"] = round(time.monotonic() - started, 3)

    def blocked(self, executor):
        self.phase("blocked-work")
        inspection, _ = mutate(self.api, "POST", "/api/v1/federation/source-inspections", {
            "url": "https://federation-fixture:8443/.well-known/ard.json", "networkProfile": "functional-fixture"}, 201)
        source, headers = mutate(self.api, "POST", "/api/v1/federation/sources", {
            "inspectionId": inspection["id"], "displayName": "Writer recovery source", "retrievalMode": "indexed",
            "scope": {"kinds": ["agent"]}, "updateMode": "manual_review", "networkProfile": "functional-fixture",
            "reason": "Owned writer recovery"}, 201)
        # SHARE ROW EXCLUSIVE blocks the selected writes but permits baseline
        # SELECTs and does not replay an ACCESS EXCLUSIVE lock on the standby.
        lock = executor.submit(self.sql, "SET application_name='failover-barrier'; BEGIN; LOCK TABLE namespaces,source_retrieval_states IN SHARE ROW EXCLUSIVE MODE; SELECT pg_sleep(90); ROLLBACK")
        wait_for(lambda: self.sql("SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a USING(pid) WHERE a.application_name='failover-barrier' AND l.mode='ShareRowExclusiveLock' AND l.granted") == "2")
        accepted, _ = mutate(self.api, "POST", "/api/v1/federation/sources/" + source["id"] + ":sync", {}, 202, header(headers, "ETag"))
        self.job_id = accepted["job"]["id"]
        self.pending_headers = {"Idempotency-Key": "failover-pending-" + self.suffix}
        self.pending_body = {"slug": "pending-" + self.suffix, "displayName": "Interrupted writer command"}
        pending = executor.submit(self.api.request, "POST", "/api/v1/namespaces", self.pending_body, (503,), self.pending_headers)
        wait_for(lambda: self.blocked_count() == "2", 30)
        self.reservation = self.sql("SELECT reservation_id FROM idempotency_keys WHERE tenant_id='" + TENANT + "' AND idempotency_key='" + self.pending_headers["Idempotency-Key"] + "' AND atomic_response_status IS NULL")
        if not re.fullmatch(r"[0-9a-f-]{36}", self.reservation):
            raise Failure("missing-pending-reservation")
        return lock, pending

    def blocked_count(self):
        return self.sql("SELECT count(DISTINCT a.usename) FROM pg_stat_activity a WHERE a.usename IN ('catalog_api','catalog_worker') AND EXISTS (SELECT 1 FROM pg_stat_activity b WHERE b.application_name='failover-barrier' AND b.pid=ANY(pg_blocking_pids(a.pid)))")

    def recover_command(self, pending):
        # Wait for the original server execution to unwind before repair.
        problem, _ = pending.result(timeout=70)
        assert problem["code"] == "DEPENDENCY_UNAVAILABLE"
        self.phase("repair")
        problem, _ = self.api.request("POST", "/api/v1/namespaces", self.pending_body, (503,), self.pending_headers)
        assert problem["code"] == "DEPENDENCY_UNAVAILABLE"
        plan = self.repair("plan", "--reservation", self.reservation, "--reason", "Original request ended after fenced writer loss")
        assert plan["state"] == "interrupted" and plan["eligible"] and not plan["active"]
        applied = self.repair("apply", "--reservation", self.reservation, "--reason", plan["reason"], "--plan-digest", plan["planDigest"])
        assert applied["applied"]
        created, created_headers = self.api.request("POST", "/api/v1/namespaces", self.pending_body, (201,), self.pending_headers, decode_body=False)
        assert header(created_headers, "ETag") == '"rev-1"'
        replay, headers = self.api.request("POST", "/api/v1/namespaces", self.pending_body, (201,), self.pending_headers, decode_body=False)
        assert replay == created and header(headers, "Idempotency-Replayed") == "true"
        assert header(headers, "ETag") == '"rev-1"'
        created = http_client.decode(created)
        assert all(created[name] == value for name, value in self.pending_body.items()) and created["revision"] == 1
        retained, headers = self.api.request("GET", "/api/v1/namespaces/" + created["id"])
        assert namespace_state(retained) == namespace_state(created) and header(headers, "ETag") == '"rev-1"'
        audit_once(self.api, "namespace.created", "namespace", created["id"])
        audit_once(self.api, "idempotency.reservation_released", "idempotency_reservation", self.reservation)
        def recovered():
            job, _ = self.api.request("GET", "/api/v1/jobs/" + self.job_id)
            if job["state"] in ("failed", "dead_letter", "cancelled"):
                raise Failure("failover-job-terminal-failure")
            return job if job["state"] == "succeeded" else None
        job = wait_for(recovered, 75)
        assert job["attempt"] >= 2

    def verify(self):
        self.phase("verification")
        authorization_recovery.verify(self.api, self.policy)
        replay, headers = self.api.request("POST", "/api/v1/namespaces", self.body, (201,), self.headers, decode_body=False)
        assert replay == self.completed and header(headers, "Idempotency-Replayed") == "true"
        assert header(headers, "ETag") == '"rev-1"'
        replay = http_client.decode(replay)
        retained, headers = self.api.request("GET", "/api/v1/namespaces/" + replay["id"])
        assert namespace_state(retained) == namespace_state(replay) and header(headers, "ETag") == '"rev-1"'
        Client(self.api.base_url, "other-tenant-admin").request("GET", "/api/v1/namespaces/" + replay["id"], expected=(404,))
        audit_once(self.api, "namespace.created", "namespace", replay["id"])
        public, headers = Client(self.api.base_url).request("GET", "/.well-known/ard.json", decode_body=False)
        assert public == self.public and header(headers, "ETag") == header(self.public_headers, "ETag")
        retained = {row["id"]: row for row in json.loads(self.sql("SELECT coalesce(jsonb_agg(v ORDER BY id),'[]') FROM (SELECT id,tenant_id,event_type,subject,data,created_at FROM outbox_events ORDER BY id LIMIT 1001) v"))}
        assert all(retained.get(row["id"]) == row for row in json.loads(self.outbox))
        wait_for(lambda: self.sql("SELECT count(*) FROM outbox_events WHERE published_at IS NULL") == "0")
        assert self.processes == {name: self.identity(name) for name in ("api", "worker")}
        assert self.sql("SELECT count(DISTINCT usename) FROM pg_stat_activity WHERE usename IN ('catalog_api','catalog_worker')") == "2"
        self.evidence.update(processDigests=self.processes, writerChanged=True, completedReplay=True,
                             pendingRepair=self.profile == "failover-abrupt", jobRecovered=self.profile == "failover-abrupt",
                             retainedPublicState=True, policyRetained=True, baselineLost=0)
        if not valid_evidence(self.evidence, self.profile):
            raise Failure("invalid-failover-evidence")
        write_document(self.state / "failover.json", self.evidence)
        self.phase("complete")

    def run(self):
        # This child alone uses a longer bounded HTTP budget for held requests.
        http_client.REQUEST_SECONDS = 90
        self.setup()
        self.baseline()
        with ThreadPoolExecutor(max_workers=2) as executor:
            lock = pending = None
            if self.profile == "failover-abrupt":
                lock, pending = self.blocked(executor)
            self.barrier()
            if pending is not None:
                assert not pending.done() and not lock.done() and self.blocked_count() == "2"
                assert self.sql("SELECT count(*) FROM idempotency_keys WHERE reservation_id='" + self.reservation + "' AND atomic_response_status IS NULL", "standby") == "1"
                assert self.sql("SELECT state FROM jobs WHERE id='" + self.job_id + "'", "standby") == "running"
            self.promote()
            if pending is not None:
                try:
                    lock.result(timeout=10)
                except Failure as error:
                    if str(error) != "child-failed":
                        raise
                else:
                    raise Failure("barrier-ended-before-writer-loss")
                self.recover_command(pending)
        self.verify()


if __name__ == "__main__":
    try:
        Failover(os.environ).run()
    except Exception:
        # The supervisor retains the closed phase, not raw SQL/HTTP/credentials.
        raise SystemExit("Writer recovery failed; inspect the report phase.") from None
