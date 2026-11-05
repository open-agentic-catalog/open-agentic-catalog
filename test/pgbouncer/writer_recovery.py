"""Reuse the maintained fenced-promotion control in the owned pooler fixture."""
import hashlib
import json
import os
import re
from pathlib import Path
import sys
import time
import urllib.request


ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(ROOT / "test" / "functional"))
from failover import Failover, wait_for  # noqa: E402
from runner import command, Failure  # noqa: E402
import http_client  # noqa: E402
from abrupt import validate_held_input  # noqa: E402



class WriterRecovery(Failover):
    def __init__(self, environment):
        self.env = dict(environment)
        project = self.env["CATALOG_TEST_PGBOUNCER_PROJECT"]
        if not re.fullmatch(r"oac-pgbouncer-[0-9a-f]{12}", project):
            raise Failure("invalid-pooler-fixture")
        self.launcher = ["docker", "compose", "--env-file", "/dev/null", "-f",
                         self.env["CATALOG_TEST_PGBOUNCER_COMPOSE"], "-p", project]
        self.selection = self.env.get("CATALOG_TEST_PGBOUNCER_WRITER_LOSS", "planned")
        if self.selection not in ("planned", "abrupt"):
            raise Failure("invalid-writer-loss-selection")
        self.profile = "failover-" + self.selection
        self.writer = "postgres"
        self.evidence = {}
        self.end = time.monotonic() + 90
        self.base = self.env["CATALOG_TEST_PGBOUNCER_API"]
        self.listeners = [self.base]
        # The API route is created by the owned process fixture, never ambient.
        from urllib.parse import urlsplit
        address = urlsplit(self.base)
        if address.scheme != "http" or address.hostname != "127.0.0.1" or address.path:
            raise Failure("invalid-pooler-api")

    def phase(self, value):
        self.evidence["phase"] = value

    def compose(self, *arguments, capture=False):
        return command([*self.launcher, *arguments], self.env, 30, capture=capture)

    def sql(self, query, target=None):
        writer = self.writer if target is None else target
        result = self.compose("exec", "-T", writer, "psql", "-XAt", "-v", "ON_ERROR_STOP=1",
                              "-U", "catalog", "-d", "catalog_test", "-c", query, capture=True).decode().strip()
        if query == "SELECT pg_promote(true, 30)" and writer == "standby" and result == "t":
            # Infrastructure owns the stable writer endpoint. PgBouncer must not
            # choose among read-only/writable servers or elect a writer itself.
            container = self.compose("ps", "-q", "standby", capture=True).decode().strip()
            if not re.fullmatch(r"[0-9a-f]{64}", container):
                raise Failure("invalid-owned-standby")
            if self.selection == "abrupt":
                # Keep the new writer private until the parent holds its retry
                # barrier. No runtime can reclaim this Job through the old alias.
                marker = Path(self.env["CATALOG_TEST_PGBOUNCER_WRITER_FILE"])
                marker.with_suffix(".promoted").write_text("promoted", encoding="utf-8")
                ready = marker.with_suffix(".route-ready")
                wait_for(lambda: ready.exists() and ready.read_text(encoding="utf-8") == "ready", 30)
            network = self.env["CATALOG_TEST_PGBOUNCER_PROJECT"] + "_default"
            command(["docker", "network", "disconnect", network, container], self.env, 10)
            command(["docker", "network", "connect", "--alias", "standby", "--alias", "writer", "--alias", "wrong-name", network, container], self.env, 10)
            if self.selection == "abrupt":
                marker.with_suffix(".routed").write_text("routed", encoding="utf-8")
                resumed = marker.with_suffix(".resumed")
                wait_for(lambda: resumed.exists() and resumed.read_text(encoding="utf-8") == "resumed", 75)

        return result

    def health(self, status):
        for base in self.listeners:
            live, _, _ = http_client.request(urllib.request.Request(base+"/livez"))
            ready, _, _ = http_client.request(urllib.request.Request(base+"/readyz"))
            if live != 200 or ready != status:
                return False
        if self.selection == "abrupt":
            self.evidence["degradedListeners" if status == 503 else "recoveredListeners"] = len(self.listeners)
        return True

    def held_state(self, held, target=None):
        tenant, reservation, job, outbox = (held[key] for key in ("tenant", "reservation", "job", "outbox"))
        raw = self.sql(f"""SELECT json_build_object(
          'reservation', (SELECT count(*)=1 FROM idempotency_keys WHERE tenant_id='{tenant}' AND reservation_id='{reservation}' AND atomic_response_status IS NULL AND released_at IS NULL),
          'job', (SELECT json_build_object('attempt',attempt,'owner',lease_owner,'live',lease_expires_at>clock_timestamp()) FROM jobs WHERE tenant_id='{tenant}' AND id='{job}' AND state='running'),
          'outbox', (SELECT to_jsonb(e)-ARRAY['published_at','lease_owner','lease_expires_at','cleanup_after','dispatch_after'] FROM outbox_events e WHERE tenant_id='{tenant}' AND id='{outbox}' AND published_at IS NULL AND lease_owner IS NULL))""", target)
        value = json.loads(raw)
        job_value = value.get("job")
        if value.get("reservation") is not True:
            raise Failure("missing-held-reservation")
        if value.get("outbox") != held["outboxDocument"]:
            raise Failure("changed-held-outbox")
        if (not isinstance(job_value, dict) or job_value.get("attempt") != 1
                or not job_value.get("owner") or job_value.get("live") is not True):
            raise Failure("missing-held-job-lease")
        return job_value["owner"]

    def blocked_roles(self):
        return self.sql("""SELECT count(DISTINCT a.usename) FROM pg_stat_activity a
          WHERE a.usename IN ('catalog_api','catalog_worker') AND EXISTS
          (SELECT 1 FROM pg_stat_activity b WHERE b.application_name='pooled-abrupt-barrier'
          AND b.pid=ANY(pg_blocking_pids(a.pid)))""") == "2"

    def qualify(self):
        held = None
        if self.selection == "abrupt":
            held = validate_held_input(sys.stdin.read(65537))
            self.listeners = held["listeners"]
            if self.listeners[0] != self.base:
                raise Failure("mismatched-owned-api")
            self.phase("held-primary")
            owner = self.held_state(held)
            if not self.blocked_roles():
                raise Failure("runtime-work-not-blocked")
            self.evidence.update(selection="abrupt", heldOnPrimary=True, processes=held["processes"],
                                 reservation=held["reservation"], job=held["job"], outbox=held["outbox"], jobOwner=owner,
                                 jobAttempt=1, outboxSHA256="sha256:" + hashlib.sha256(json.dumps(
                                     held["outboxDocument"], sort_keys=True, separators=(",", ":")).encode()).hexdigest())
        barrier = self.sql("SELECT pg_current_wal_flush_lsn()")
        # The selected completed or held state must reach the physical replica
        # before fencing. Asynchronous data loss is excluded.
        if not re.fullmatch(r"[0-9A-F]+/[0-9A-F]+", barrier):
            raise Failure("invalid-replication-barrier")
        wait_for(lambda: self.sql(f"SELECT pg_last_wal_replay_lsn() >= '{barrier}'::pg_lsn", "standby") == "t", 30)
        if held is not None:
            self.phase("held-standby")
            if self.held_state(held, "standby") != owner or self.held_state(held) != owner or not self.blocked_roles():
                raise Failure("held-work-changed-before-writer-loss")
            self.evidence.update(heldOnStandby=True, barrier=barrier,
                                 replayedBarrier=self.sql("SELECT pg_last_wal_replay_lsn()", "standby"))
        self.promote()  # Existing stop/fence, unready, promote, recover control.
        if self.evidence["phase"] != "recovery" or self.writer != "standby":
            raise Failure("pooler-promotion-incomplete")
        Path(self.env["CATALOG_TEST_PGBOUNCER_WRITER_FILE"]).write_text("standby", encoding="utf-8")
        if held is not None:
            old_writer = self.compose("ps", "--all", "-q", "postgres", capture=True).decode().strip()
            if not re.fullmatch(r"[0-9a-f]{64}", old_writer):
                raise Failure("invalid-old-writer")
            exit_code = command(["docker", "inspect", "--format", "{{.State.ExitCode}}", old_writer], self.env, 10, capture=True).decode().strip()
            if exit_code != "137":
                raise Failure("writer-not-killed-abruptly")
            self.evidence.update(writerFenced=True, promoted=True, oldWriterExitCode=137)
            path = Path(self.env["CATALOG_TEST_PGBOUNCER_EVIDENCE_FILE"])
            with path.open("x", encoding="utf-8") as output:
                path.chmod(0o600)
                json.dump(self.evidence, output, sort_keys=True)


if __name__ == "__main__":
    recovery = None
    try:
        recovery = WriterRecovery(os.environ)
        recovery.qualify()
    except Exception as error:
        # Fixed stage and classification only; never print SQL, credentials or
        # subprocess exception contents.
        phase = recovery.evidence.get("phase", "setup") if recovery else "setup"
        reason = str(error) if str(error) in {
            "missing-held-reservation", "changed-held-outbox", "missing-held-job-lease",
            "runtime-work-not-blocked", "held-work-changed-before-writer-loss",
        } else type(error).__name__
        raise SystemExit(f"Owned pooled writer recovery failed: {phase}: {reason}") from None
