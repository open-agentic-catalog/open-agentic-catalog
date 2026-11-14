#!/usr/bin/env python3
"""Prove owned orphan repair/replay, queued Job cancellation and failed-Job retry."""

from concurrent.futures import ThreadPoolExecutor
import hashlib
import os
from pathlib import Path
import re
import shlex
import time

from core import mutate
from federation import Client, key, wait_job
from http_client import fixture_tls_context, ClientFailure, decode
from journeys import audit_once, header, namespace_state
from runner import record_scenario_failure, Failure, command


TENANT = "00000000-0000-7000-8000-000000000001"


class Operators:
    def __init__(self, environment):
        self.env = dict(environment)
        self.suffix = self.env["CATALOG_FUNCTIONAL_SUFFIX"]
        if not re.fullmatch(r"[0-9a-f]{12}", self.suffix):
            raise Failure("invalid-functional-owner")
        self.project = "open-agentic-catalog-functional-" + self.suffix
        fixture = Path(self.env["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"])
        self.env.update(IMAGE=self.env.get("CATALOG_FUNCTIONAL_CANDIDATE_IMAGE", "open-agentic-catalog:functional-" + self.suffix),
                        VERSION=self.env.get("CATALOG_FUNCTIONAL_VERSION", "functional"),
                        SOURCE_STATE=self.env.get("CATALOG_FUNCTIONAL_SOURCE_STATE", "unknown"), COMPOSE_PROGRESS="quiet",
                        COMMIT=self.env["CATALOG_FUNCTIONAL_COMMIT"], CATALOG_COMPOSE_HTTP_PORT=self.env["CATALOG_FUNCTIONAL_HTTP_PORT"],
                        CATALOG_COMPOSE_POSTGRES_PORT="0", CATALOG_COMPOSE_SUBNET=self.env["CATALOG_FUNCTIONAL_SUBNET"],
                        CATALOG_COMPOSE_FEDERATION_POLICY_FILE=str(fixture / "policy.json"),
                        CATALOG_COMPOSE_FEDERATION_TRUST_DIRECTORY=str(fixture / "trust"))
        base = self.env["CATALOG_FUNCTIONAL_BASE_URL"]
        if not re.fullmatch(r"http://127\.0\.0\.1:[0-9]{1,5}", base):
            raise Failure("nonlocal-functional-listener")
        self.api = Client(base, "local-admin")
        self.lock_name = "r02-repair-" + self.suffix

    def compose(self, *args, capture=False):
        argv = shlex.split(self.env.get("CATALOG_FUNCTIONAL_COMPOSE_COMMAND", "docker compose"))
        return command(argv + ["--env-file", "/dev/null", "-f", "compose.yaml", "-f", "test/functional/compose.yaml", "-p", self.project, *args], self.env, 45, capture=capture, limit=1 << 20)

    def sql(self, statement):
        return self.compose("exec", "-T", "postgres", "psql", "--username", "catalog", "--dbname", "catalog", "--set", "ON_ERROR_STOP=1", "--tuples-only", "--no-align", "--command", statement, capture=True).decode().strip()

    def repair(self, action, *args):
        return decode(self.compose("run", "--rm", "--no-deps", "--entrypoint", "/usr/local/bin/catalogctl", "migrate",
                                   "repair", "idempotency", action, "--config", "/config/config.json", "--tenant", TENANT, *args, capture=True))

    def remote(self, action, *args, token="local-reader"):
        # Invoke the shipped binary against its owned API, not a host build.
        # An unusable server-config input must not affect this HTTP-only client.
        result = decode(self.compose("exec", "-T", "--env", "CATALOG_REMOTE_TOKEN=" + token,
                                     "--env", "CATALOG_DATABASE_DSN_FILE=/does-not-exist",
                                     "api", "/usr/local/bin/catalogctl", "remote", action,
                                     "--base-url", "http://127.0.0.1:8080", *args, capture=True))
        assert result["meta"]["requestId"] and "data" in result, "remote envelope incomplete"
        return result["data"]

    def remote_reads(self, job):
        self.remote("capabilities")
        page = self.remote("search", "--limit", "1", "--tag", "functional-" + self.suffix)
        assert not page["partial"] and len(page["items"]) == 1 and page.get("nextCursor"), "remote Search page incomplete"
        item = page["items"][0]
        assert item["type"] == "local", "remote fixture lost local result"
        record = self.remote("record", "--id", item["local"]["release"]["id"])
        for section in ("resource", "version", "versionRevision", "listing", "listingRevision", "release", "evidence"):
            assert record[section]["id"] == item["local"][section]["id"], "remote record identity differs from Search"
        assert record["versionRevision"]["contentSetDigest"] == item["local"]["versionRevision"]["contentSetDigest"]
        assert record["versionRevision"]["representations"], "exact record lost retained documents"
        assert "representations" not in item["local"]["versionRevision"] and "findings" not in item["local"]["evidence"]
        for field in ("displayName", "description", "tags"):
            assert record["listingRevision"][field] == item[field], "remote record display differs from Search"
        assert record["listingRevision"]["publisherId"] == item["publisher"]
        later = self.remote("search", "--limit", "1", "--tag", "functional-" + self.suffix, "--cursor", page["nextCursor"])
        assert later["items"] and later["items"][0]["id"] != item["id"], "remote cursor duplicated first page"
        retained = self.remote("job", "--id", job["id"], token="local-admin")
        assert retained["id"] == job["id"] and retained["state"] == job["state"], "remote Job status mismatch"
        # Reader metadata discovery is not authority for private Job-family results.
        try:
            self.remote("job", "--id", job["id"])
        except Failure as error:
            if str(error) != "child-failed":
                raise
        else:
            raise Failure("remote-reader-job-result-accepted")
        for action, identifier in (("record", record["release"]["id"]), ("job", job["id"])):
            try:
                self.remote(action, "--id", identifier, token="other-tenant-admin")
            except Failure as error:
                if str(error) != "child-failed":
                    raise
            else:
                raise Failure("remote-cross-tenant-read-accepted")

    def source_comparison(self, source_id, before_id):
        current, _ = self.api.request("GET", "/api/v1/federation/sources/" + source_id)
        after_id = current["lastCompleteSnapshotId"]
        assert before_id != after_id, "operator comparison did not retain two synchronizations"
        path = f"/api/v1/federation/snapshots/{before_id}/comparison?otherSnapshotId={after_id}&limit=100"
        flags = ("--before-snapshot-id", before_id, "--after-snapshot-id", after_id, "--limit", "100")
        all_entries, _ = self.api.request("GET", path + "&view=all")
        actual = self.remote("source-compare", *flags, "--view", "all", token="local-admin")
        assert actual == all_entries and not actual.get("nextCursor"), "shipped comparison differs from saved API membership"
        assert actual["sourceId"] == source_id and actual["before"]["id"] == before_id and actual["after"]["id"] == after_id, "comparison changed its selected snapshots"
        assert actual["items"] and all(row["change"] in ("unchanged", "multiple") for row in actual["items"]), "unchanged upstream acquired historical content changes"
        for row in actual["items"]:
            for side, snapshot_id in (("before", before_id), ("after", after_id)):
                if side in row:
                    assert row[side]["occurrence"]["snapshotId"] == snapshot_id, "comparison lost exact occurrence attribution"
        changes, _ = self.api.request("GET", path + "&view=changes")
        assert self.remote("source-compare", *flags, token="local-admin") == changes, "CLI default view did not select changes"
        assert all(row["change"] == "multiple" for row in changes["items"]), "changes view invented a content change"
        for token, status in (("local-reader", 403), ("other-tenant-admin", 404)):
            Client(self.api.base_url, token).request("GET", path, expected=(status,))
            try:
                self.remote("source-compare", *flags, token=token)
            except Failure as error:
                if str(error) != "child-failed":
                    raise
            else:
                raise Failure("remote-source-comparison-disclosure-accepted")
        retained, _ = self.api.request("GET", "/api/v1/federation/sources/" + source_id)
        assert retained["lastCompleteSnapshotId"] == after_id and retained["revision"] == current["revision"], "comparison changed source authority"

    def wait(self, callback, seconds=30):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            try:
                if callback():
                    return
            except (ClientFailure, AssertionError):
                pass
            time.sleep(0.05)
        raise Failure("operator-wait-timeout")

    def jobs(self):
        # The earlier housekeeping qualification deliberately removes its own
        # automatic source. Own a new manual source for operator/adoption work;
        # do not depend on another journey's mutable fixture surviving.
        inspection, _ = mutate(self.api, "POST", "/api/v1/federation/source-inspections", {
            "url": "https://federation-fixture:8443/.well-known/ard.json", "networkProfile": "functional-fixture"}, 201)
        source, headers = mutate(self.api, "POST", "/api/v1/federation/sources", {
            "inspectionId": inspection["id"], "displayName": "Owned operator source", "retrievalMode": "hybrid",
            "live": {"enabled": True, "queryPrivacyAcknowledged": True},
            "referrals": {"enabled": True, "maxHops": 1},
            "discoveryAssignments": [{"host": "private.catalog.test", "audience": "authenticated"}],
            "scope": {"kinds": ["agent"]}, "updateMode": "manual_review", "networkProfile": "functional-fixture",
            "reason": "Owned operator and adoption qualification"}, 201)
        accepted, _ = mutate(self.api, "POST", "/api/v1/federation/sources/" + source["id"] + ":sync", {}, 202, header(headers, "ETag"))
        wait_job(self.api, accepted["job"]["id"], {"succeeded"})
        first_source, _ = self.api.request("GET", "/api/v1/federation/sources/" + source["id"])
        first_snapshot_id = first_source["lastCompleteSnapshotId"]
        self.compose("stop", "--timeout", "20", "worker")
        try:
            _, headers = self.api.request("GET", "/api/v1/federation/sources/" + source["id"])
            accepted, _ = mutate(self.api, "POST", "/api/v1/federation/sources/" + source["id"] + ":sync", {}, 202, header(headers, "ETag"))
            job = accepted["job"]
            path = "/api/v1/jobs/" + job["id"]
            current, _ = self.api.request("GET", path)
            assert current["state"] == "queued", "stopped worker unexpectedly claimed Job"
            cancelled, _ = mutate(self.api, "POST", path + ":cancel", {"reason": "Owned queued Job cancellation"})
            retained, _ = self.api.request("GET", path)
            assert cancelled["state"] == "cancelled" and retained["state"] == "cancelled", "Job cancellation not persisted"
            self.remote_reads(retained)
            self.api.request("POST", path + ":retry", {"reason": "Cancelled Jobs are not failures"}, (409,), {"Idempotency-Key": key("cancelled-retry")})
            audit_once(self.api, "job.cancellation_requested", "job", job["id"])
            # A permanent upstream HTTP refusal creates a real failed Job
            # without modifying its retained source revision or payload.
            fixture = Path(self.env["CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY"])
            origin = Client("https://127.0.0.1:" + self.env["CATALOG_COMPOSE_FIXTURE_PORT"], ssl_context=fixture_tls_context(fixture / "trust/functional-ca.pem"))
            origin.request("POST", "/control", {"mode": "denied"})
            _, headers = self.api.request("GET", "/api/v1/federation/sources/" + source["id"])
            pending, _ = mutate(self.api, "POST", "/api/v1/federation/sources/" + source["id"] + ":sync", {}, 202, header(headers, "ETag"))
            # Compare the retained PostgreSQL timestamp, not the enqueue
            # response's higher-precision in-process timestamp.
            scheduled, _ = self.api.request("GET", "/api/v1/jobs/" + pending["job"]["id"])
            assert scheduled["state"] == "queued" and scheduled["attempt"] == 0 and scheduled["attemptLimit"] == 7, "terminal-refusal fixture was not queued with the configured budget"
        finally:
            self.compose("start", "worker")
        failed = wait_job(self.api, pending["job"]["id"], {"failed", "dead_letter"})
        assert (failed["state"] == "failed" and failed["attempt"] == 1 and failed["attemptLimit"] == 7
                and failed["failureClass"] == "authentication" and failed["notBefore"] == scheduled["notBefore"]), "terminal hinted response acquired a retry or lost its configured budget"
        assert self.remote("job", "--id", failed["id"], token="local-admin")["state"] == failed["state"], "failed Job retrieval did not succeed"
        origin.request("POST", "/control", {"mode": "initial"})
        retried, _ = mutate(self.api, "POST", "/api/v1/jobs/" + failed["id"] + ":retry", {"reason": "Qualify retained operator retry intent"}, 202)
        assert retried["parentJobId"] == failed["id"] and retried["id"] != failed["id"] and retried["attemptLimit"] == failed["attemptLimit"], "retry lost parent linkage or attempt limit"
        recovered = wait_job(self.api, retried["id"], {"succeeded"})
        assert recovered["attempt"] == 1 and recovered["attemptLimit"] == 7 and not recovered.get("failureClass") and not recovered.get("safeError"), "replacement Job did not recover cleanly"
        retained, _ = self.api.request("GET", "/api/v1/jobs/" + failed["id"])
        assert retained == failed, "successful retry rewrote its terminal parent"
        audit_once(self.api, "job.retried", "job", failed["id"])
        self.source_comparison(source["id"], first_snapshot_id)

    def orphan(self):
        body = {"slug": "repair-" + self.suffix, "displayName": "Owned orphan retry"}
        headers = {"Idempotency-Key": "repair-" + self.suffix}
        def request():
            return self.api.request("POST", "/api/v1/namespaces", body, (201,), headers)
        with ThreadPoolExecutor(max_workers=2) as workers:
            pending = None
            lock = workers.submit(self.sql, "SET application_name='" + self.lock_name + "'; BEGIN; LOCK TABLE namespaces IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(15); COMMIT")
            try:
                self.wait(lambda: self.sql("SELECT count(*) FROM pg_locks WHERE relation='namespaces'::regclass AND mode='AccessExclusiveLock' AND granted") == "1", 5)
                pending = workers.submit(request)
                self.wait(lambda: self.sql("SELECT count(*) FROM pg_stat_activity WHERE usename='catalog_api' AND wait_event_type='Lock'") == "1", 2)
                self.compose("kill", "--signal", "SIGKILL", "api")
            finally:
                self.sql("SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name='" + self.lock_name + "' AND pid<>pg_backend_pid()")
            for future in (lock, pending):
                if future is None:
                    continue
                try:
                    future.result(timeout=20)
                except (Failure, ClientFailure):
                    pass
        inventory = self.repair("list")
        digest = "sha256:" + hashlib.sha256(headers["Idempotency-Key"].encode()).hexdigest()
        matches = [item for item in inventory["items"] if item["idempotencyKeyDigest"] == digest]
        assert len(matches) == 1 and matches[0]["protocolVersion"] == 2 and matches[0]["repairable"], "current orphan not repairable"
        reservation = matches[0]["reservationId"]
        args = ("--reservation", reservation, "--reason", "Owned process-interrupted command")
        plan = self.repair("plan", *args)
        assert plan["eligible"] and plan["state"] == "interrupted" and not plan["active"], "unsafe repair plan"
        try:
            self.repair("apply", *args, "--plan-digest", "sha256:" + "0" * 64)
        except Failure as error:
            if str(error) != "child-failed":
                raise
        else:
            raise Failure("mismatched-repair-plan-accepted")
        applied = self.repair("apply", *args, "--plan-digest", plan["planDigest"])
        assert applied["applied"] and applied["auditEventId"], "repair not applied"
        retained = self.repair("plan", *args)
        assert not retained["eligible"] and retained["state"] == "released", "released reservation remained eligible"
        try:
            self.repair("apply", *args, "--plan-digest", plan["planDigest"])
        except Failure as error:
            if str(error) != "child-failed":
                raise
        else:
            raise Failure("stale-repair-plan-accepted")
        self.compose("start", "api")
        self.wait(lambda: self.api.request("GET", "/readyz", retry_unavailable=True)[0])
        audit_once(self.api, "idempotency.reservation_released", "idempotency_reservation", reservation)
        created, _ = request()
        replay, _ = request()
        assert created == replay, "repaired retry did not replay"
        retained, _ = self.api.request("GET", "/api/v1/namespaces/" + created["id"])
        assert namespace_state(retained) == namespace_state(created), "repaired command not persisted"
        audit_once(self.api, "namespace.created", "namespace", created["id"])


if __name__ == "__main__":
    try:
        journey = Operators(os.environ)
        journey.jobs()
        journey.orphan()
        print("Packaged Job cancellation and current-protocol orphan/repair/retry passed.")
    except Exception as error:
        record_scenario_failure("operators", error)
        raise
