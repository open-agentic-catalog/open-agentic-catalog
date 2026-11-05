"""Prepare an owned PostgreSQL base backup and WAL archive, without restoring."""

import os
from pathlib import Path
import re
import secrets
import shlex
import signal
import tempfile
import time

from runner import Failure, command, decode, digest_files, write_document


SOURCES = ("Makefile", "test/functional/runner.py", "test/functional/pitr.py",
           "test/functional/pitr.compose.yaml", "test/functional/pitr_archive.sh", "test/functional/pitr_pg_hba.conf", "deployments/postgres/init.sql")
IMAGE = "pgvector/pgvector:pg18@sha256:2358fcba361ed2233a5ed81b5fe4ca779ccb304120ce531a3bf51c0ed7e2bc11"
PROJECT = re.compile(r"open-agentic-catalog-pitr-[0-9a-f]{12}\Z")
WAL = re.compile(r"[0-9A-F]{24}\Z")
LSN = re.compile(r"[0-9A-F]{1,8}/[0-9A-F]{1,8}\Z")
COMPLETED = ["isolated-source", "verified-base-backup", "post-backup-wal-archived", "recovery-settings-staged", "corrupt-backup-rejected"]


def require(condition, code):
    if not condition:
        raise Failure(code)


def lsn_number(value):
    require(isinstance(value, str) and LSN.fullmatch(value), "invalid-pitr-lsn")
    high, low = value.split("/")
    return (int(high, 16) << 32) + int(low, 16)


def backup_wal_range(manifest, timeline):
    ranges = manifest.get("WAL-Ranges") if isinstance(manifest, dict) else None
    require(isinstance(ranges, list) and len(ranges) == 1 and isinstance(ranges[0], dict), "invalid-pitr-backup-wal")
    entry = ranges[0]
    require(type(entry.get("Timeline")) is int and entry["Timeline"] == timeline, "pitr-backup-timeline-mismatch")
    start, end = entry.get("Start-LSN"), entry.get("End-LSN")
    require(lsn_number(start) <= lsn_number(end), "pitr-backup-wal-order")
    return start, end


def recovery_settings(name, timeline):
    """Only owned names and exact source timelines can enter PostgreSQL config."""
    require(isinstance(name, str) and re.fullmatch(r"catalog-pitr-[0-9a-f]{12}", name), "invalid-pitr-target")
    require(type(timeline) is int and 1 <= timeline <= 0xFFFFFFFF, "invalid-pitr-timeline")
    return ("restore_command = 'cp /archive/%f %p'\n"
            f"recovery_target_name = '{name}'\nrecovery_target_timeline = '{timeline}'\n"
            "recovery_target_action = 'pause'\nhot_standby = on\narchive_mode = off\n"
            "primary_conninfo = ''\nprimary_slot_name = ''\n")


def compose_environment(base, inputs, password):
    """Do not inherit Compose selections, PG connections or Catalog credentials."""
    environment = {key: base[key] for key in ("PATH", "HOME", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "XDG_RUNTIME_DIR") if key in base}
    environment.update(CATALOG_PITR_INPUTS=str(inputs), CATALOG_PITR_PASSWORD=password,
                       COMPOSE_PROFILES="", COMPOSE_DISABLE_ENV_FILE="true")
    return environment


def owned_inventory(engine, environment, project, seconds):
    require(isinstance(project, str) and PROJECT.fullmatch(project), "invalid-pitr-project")
    result = {}
    for kind, args in (("containers", ["ps", "-aq"]), ("volumes", ["volume", "ls", "-q"]),
                       ("networks", ["network", "ls", "-q"])):
        result[kind] = command(engine + args + ["--filter", "label=com.docker.compose.project=" + project],
                               environment, seconds(), capture=True, limit=65536).splitlines()
    return result


class Preparation:
    def __init__(self, base, directory, suffix):
        require(re.fullmatch(r"[0-9a-f]{12}", suffix) is not None, "invalid-pitr-run")
        self.base, self.directory = base, Path(directory)
        self.project = "open-agentic-catalog-pitr-" + suffix
        self.target = "catalog-pitr-" + suffix
        self.engine = shlex.split(base.get("CATALOG_FUNCTIONAL_ENGINE_COMMAND", "docker"))
        self.compose = shlex.split(base.get("CATALOG_FUNCTIONAL_COMPOSE_COMMAND", "docker compose"))
        require(bool(self.engine) and bool(self.compose), "invalid-pitr-command")
        self.compose += ["--env-file", "/dev/null", "-f", "test/functional/pitr.compose.yaml", "-p", self.project]
        self.environment = None
        self.claimed = False
        self.deadline = time.monotonic() + 600
        self.report = {"formatVersion": 1, "runId": suffix, "profile": "pitr-prepare", "status": "incomplete",
                       "qualification": "preparation-only", "historicalRecoveryTested": False,
                       "restoredDatabaseStarted": False, "cleanup": "not-run", "completed": []}

    def seconds(self):
        return min(90, self.deadline - time.monotonic())

    def call(self, *args, capture=False):
        return command(self.compose + list(args), self.environment, self.seconds(), capture=capture)

    def sql(self, query):
        return self.call("exec", "-T", "--user", "postgres", "source", "psql", "-X", "-U", "catalog", "-d", "catalog",
                         "-qAt", "-v", "ON_ERROR_STOP=1", "-c", query, capture=True).decode("ascii").strip()

    def tools(self, script, capture=False):
        return self.call("exec", "-T", "tools", "bash", "-ec", script, capture=capture)

    def phase(self, name):
        self.report["phase"] = name
        write_document(self.directory / "report.json", self.report)
        print("PITR preparation: " + name, flush=True)

    def prepare(self, inputs):
        self.environment = compose_environment(self.base, inputs, secrets.token_hex(24))
        require(not any(owned_inventory(self.engine, self.environment, self.project, self.seconds).values()), "pitr-project-already-exists")
        self.claimed = True
        self.source_digest = digest_files(SOURCES)
        self.report["sourceDigest"] = self.source_digest
        self.phase("source-start")
        config = decode(self.call("config", "--format", "json", capture=True))
        require({service["image"] for service in config["services"].values()} == {IMAGE}, "pitr-image-drift")
        require(not any(service.get("ports") for service in config["services"].values())
                and config["networks"]["default"].get("internal") is True, "pitr-network-not-isolated")
        self.call("--profile", "tools", "up", "--detach", "--wait", "--wait-timeout", "60", "source", "tools")
        self.report["image"] = IMAGE
        image_id = command(self.engine + ["image", "inspect", IMAGE, "--format", "{{.Id}}"], self.environment,
                           self.seconds(), capture=True).decode("ascii").strip()
        require(re.fullmatch(r"sha256:[0-9a-f]{64}", image_id) is not None, "invalid-pitr-image-id")
        self.report["imageId"] = image_id
        version = int(self.sql("SHOW server_version_num"))
        require(180000 <= version < 190000, "pitr-postgres-major")
        require(self.sql("SHOW wal_level") == "replica" and self.sql("SHOW archive_mode") == "on", "pitr-archiving-disabled")
        timeline = int(self.sql("SELECT timeline_id FROM pg_control_checkpoint()"))
        require(1 <= timeline <= 0xFFFFFFFF, "invalid-pitr-timeline")
        self.report.update(postgresVersion=version, sourceTimeline=timeline)
        self.report["completed"].append("isolated-source")

        self.phase("base-backup")
        self.tools("install -d -m 700 -o postgres -g postgres /backup/base /restored/data; "
                   "test -z \"$(ls -A /backup/base)\"; "
                   "exec gosu postgres pg_basebackup -D /backup/base -F plain -X stream --checkpoint=fast --manifest-checksums=SHA256")
        self.tools("exec gosu postgres pg_verifybackup /backup/base")
        manifest = self.tools("sha256sum /backup/base/backup_manifest", capture=True).decode("ascii").split()[0]
        require(re.fullmatch(r"[0-9a-f]{64}", manifest) is not None, "invalid-pitr-manifest")
        self.report["backupManifestDigest"] = "sha256:" + manifest
        start, end = backup_wal_range(decode(self.tools("cat /backup/base/backup_manifest", capture=True)), timeline)
        self.report.update(backupWALStart=start, backupWALEnd=end)
        self.report["completed"].append("verified-base-backup")

        self.phase("post-backup-archive")
        lsn = self.sql("SELECT pg_create_restore_point('" + self.target + "')")
        require(LSN.fullmatch(lsn) is not None, "invalid-pitr-target-lsn")
        require(lsn_number(lsn) > lsn_number(end), "pitr-target-not-after-backup")
        segment = self.sql("SELECT pg_walfile_name('" + lsn + "'::pg_lsn)")
        require(WAL.fullmatch(segment) is not None, "invalid-pitr-wal-name")
        self.sql("SELECT pg_switch_wal()")
        deadline = min(self.deadline, time.monotonic() + 45)
        while self.sql("SELECT count(*) FROM pg_ls_dir('/archive') AS f WHERE f='" + segment + "'") != "1":
            require(time.monotonic() < deadline, "pitr-archive-timeout")
            time.sleep(0.2)
        archive_hash = self.call("exec", "-T", "source", "sha256sum", "/archive/" + segment, capture=True).decode("ascii").split()[0]
        require(re.fullmatch(r"[0-9a-f]{64}", archive_hash) is not None, "invalid-pitr-wal-digest")
        self.report.update(targetName=self.target, targetLSN=lsn, archivedSegment=segment, archivedSegmentDigest="sha256:" + archive_hash)
        self.call("exec", "-T", "source", "bash", "-ec",
                  "cp /archive/" + segment + " /tmp/pitr-retry; "
                  "/bin/sh /fixture/archive.sh /tmp/pitr-retry " + segment + "; "
                  "printf x >> /tmp/pitr-retry; "
                  "if /bin/sh /fixture/archive.sh /tmp/pitr-retry " + segment + "; then exit 1; fi; "
                  "rm /tmp/pitr-retry")
        unchanged = self.call("exec", "-T", "source", "sha256sum", "/archive/" + segment, capture=True).decode("ascii").split()[0]
        require(unchanged == archive_hash, "pitr-archive-overwritten")
        self.report["archiveRetryVerified"] = True
        self.report["completed"].append("post-backup-wal-archived")

        self.phase("stage-recovery")
        settings = recovery_settings(self.target, timeline)
        (inputs / "recovery.conf").write_text(settings, encoding="ascii")
        # The volume is private to this invocation; PostgreSQL must read this file.
        (inputs / "recovery.conf").chmod(0o444)
        self.tools("test ! -e /backup/base/standby.signal; "
                   "test ! -e /restored/data/PG_VERSION; "
                   "gosu postgres cp -a /backup/base/. /restored/data/; "
                   "cat /inputs/recovery.conf >> /restored/data/postgresql.auto.conf; "
                   "touch /restored/data/recovery.signal; chown postgres:postgres /restored/data/recovery.signal; "
                   "exec gosu postgres pg_verifybackup /restored/data")
        observed = self.tools("exec gosu postgres postgres -D /restored/data -C recovery_target_name", capture=True).decode("ascii").strip()
        require(observed == self.target, "pitr-staged-target-mismatch")
        for key, expected in (("recovery_target_timeline", str(timeline)), ("recovery_target_action", "pause"),
                              ("restore_command", "cp /archive/%f %p"), ("primary_conninfo", ""),
                              ("primary_slot_name", ""), ("archive_mode", "off")):
            require(self.tools("exec gosu postgres postgres -D /restored/data -C " + key, capture=True).decode("ascii").strip() == expected,
                    "pitr-staged-setting-mismatch")
        self.report["completed"].append("recovery-settings-staged")

        self.phase("backup-negative-control")
        self.tools("printf x >> /restored/data/global/pg_control; "
                   "if gosu postgres pg_verifybackup /restored/data > /tmp/verify.log 2>&1; then exit 1; fi; "
                   "grep -F 'global/pg_control' /tmp/verify.log | grep -E 'size|checksum' > /dev/null; "
                   "cp /backup/base/global/pg_control /restored/data/global/pg_control; "
                   "gosu postgres pg_verifybackup /restored/data; exec gosu postgres pg_verifybackup /backup/base")
        self.report["completed"].append("corrupt-backup-rejected")
        containers = [decode(line) for line in self.call("ps", "--all", "--format", "json", capture=True).splitlines()]
        require(len(containers) == 2 and {item["Service"] for item in containers} == {"source", "tools"},
                "pitr-unexpected-service-started")
        require(digest_files(SOURCES) == self.source_digest, "source-changed-during-run")

    def cleanup(self):
        if not self.claimed:
            return True
        self.deadline = time.monotonic() + 90
        try:
            self.call("--profile", "*", "down", "--volumes", "--remove-orphans", "--timeout", "10")
            return not any(owned_inventory(self.engine, self.environment, self.project, self.seconds).values())
        except (Failure, OSError):
            return False


def run_prepare(base=None):
    base = dict(os.environ if base is None else base)
    suffix = secrets.token_hex(6)
    directory = Path("output/functional/run-" + suffix)
    directory.mkdir(parents=True, mode=0o700)
    preparation = Preparation(base, directory, suffix)
    def interrupt(_number, _frame):
        raise KeyboardInterrupt
    previous = {number: signal.signal(number, interrupt) for number in (signal.SIGINT, signal.SIGTERM)}
    try:
        with tempfile.TemporaryDirectory(prefix=".pitr-inputs-", dir=directory) as temporary:
            # Docker bind mounts need a traversable parent; only synthetic config is mounted.
            inputs = Path(temporary).resolve()
            inputs.chmod(0o755)
            try:
                preparation.prepare(inputs)
                require(preparation.report["completed"] == COMPLETED, "missing-pitr-preparation-evidence")
                preparation.report["status"] = "passed"
            except KeyboardInterrupt:
                preparation.report.update(status="failed", failureCode="interrupted")
            except (Failure, OSError, ValueError, KeyError) as error:
                code = str(error) if isinstance(error, Failure) else "pitr-preparation-failed"
                preparation.report.update(status="failed", failureCode=code)
            finally:
                for number in previous:
                    signal.signal(number, signal.SIG_IGN)
                clean = preparation.cleanup()
                preparation.report["cleanup"] = "passed" if clean else "failed"
                if not clean:
                    preparation.report["status"] = "failed"
                    preparation.report.setdefault("failureCode", "pitr-cleanup-failed")
                write_document(directory / "report.json", preparation.report)
    finally:
        for number, handler in previous.items():
            signal.signal(number, handler)
    print(f"PITR preparation {preparation.report['status']}: {directory / 'report.json'}", flush=True)
    return 0 if preparation.report["status"] == "passed" else 1
