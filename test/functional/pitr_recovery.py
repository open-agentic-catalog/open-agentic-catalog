"""Historical PostgreSQL recovery using the existing Catalog restore assertions."""

import hashlib
import os
import re
import sys
import time

import backup_ratings
from backup_restore import BackupRestore, fingerprint
from pitr import WAL, backup_wal_range, lsn_number, recovery_settings, require
from runner import Failure, decode, record_scenario_failure, write_document


STAGES = ("base-backup", "baseline", "target-capture", "later-changes", "missing-wal", "restore-target", "runtime")


def missing_wal_failure(log, segment):
    """A normal archive miss after success is not an unreachable recovery target."""
    return (isinstance(log, bytes) and isinstance(segment, str) and WAL.fullmatch(segment) is not None
            and segment.encode() in log and b"No such file or directory" in log
            and b"FATAL" in log and b"recovery ended before configured recovery target was reached" in log)


def valid_evidence(value):
    fields = {"targetName", "sourceTimeline", "recoveredTimeline", "backupWALEnd", "targetLSN", "replayLSN",
              "requiredSegment", "requiredWALDigest", "systemIdentifier", "postBackupChangeRetained", "laterChangeAbsent",
              "missingWALRejected", "targetPaused", "sourceStopped", "historicalRecoveryTested"}
    if not isinstance(value, dict) or set(value) != fields:
        return False
    try:
        return (isinstance(value["targetName"], str) and re.fullmatch(r"catalog-pitr-[0-9a-f]{12}", value["targetName"]) is not None
                and type(value["sourceTimeline"]) is int and type(value["recoveredTimeline"]) is int
                and 1 <= value["sourceTimeline"] < value["recoveredTimeline"] <= 0xFFFFFFFF
                and lsn_number(value["backupWALEnd"]) < lsn_number(value["targetLSN"]) == lsn_number(value["replayLSN"])
                and isinstance(value["requiredSegment"], str) and WAL.fullmatch(value["requiredSegment"]) is not None
                and int(value["requiredSegment"][:8], 16) == value["sourceTimeline"]
                and isinstance(value["requiredWALDigest"], str) and re.fullmatch(r"sha256:[0-9a-f]{64}", value["requiredWALDigest"]) is not None
                and isinstance(value["systemIdentifier"], str) and re.fullmatch(r"[0-9]{1,20}", value["systemIdentifier"]) is not None
                and all(value[key] is True for key in ("postBackupChangeRetained", "laterChangeAbsent", "missingWALRejected",
                                                       "targetPaused", "sourceStopped", "historicalRecoveryTested")))
    except (Failure, ValueError):
        return False


class PITRRecovery(BackupRestore):
    def __init__(self, environment):
        require(environment.get("CATALOG_FUNCTIONAL_PROFILE") == "pitr-recovery", "invalid-pitr-recovery-profile")
        super().__init__(environment)
        self.point = "catalog-pitr-" + self.env["CATALOG_FUNCTIONAL_SUFFIX"]
        self.end = time.monotonic() + 900
        self.pitr = {}

    def stage(self, name):
        require(name in STAGES, "invalid-pitr-stage")
        write_document(self.state / "pitr-stage.json", {"stage": name})

    def tools(self, script, capture=False):
        return self.compose("exec", "-T", "wal-tools", "bash", "-ec", script, capture=capture)

    def baseline(self):
        self.phase("baseline")
        self.stage("base-backup")
        self.compose("up", "--no-deps", "--detach", "wal-tools")
        self.tools("install -d -m 700 -o postgres -g postgres /backup/base /restored/data /negative/data /missing-archive; "
                   "test -z \"$(ls -A /backup/base)\"; "
                   "exec gosu postgres pg_basebackup -D /backup/base -F plain -X stream --checkpoint=fast --manifest-checksums=SHA256")
        self.tools("exec gosu postgres pg_verifybackup /backup/base")
        timeline = int(self.sql("SELECT timeline_id FROM pg_control_checkpoint()"))
        manifest_raw = self.tools("cat /backup/base/backup_manifest", capture=True)
        _, end = backup_wal_range(decode(manifest_raw), timeline)
        size = int(self.tools("du -sb /backup/base | cut -f1", capture=True))
        require(0 < size <= 512 << 20, "pitr-base-backup-size")
        self.evidence.update(backupManifestDigest="sha256:" + hashlib.sha256(manifest_raw).hexdigest(), backupBytes=size)
        self.pitr.update(targetName=self.point, sourceTimeline=timeline, backupWALEnd=end,
                         systemIdentifier=self.sql("SELECT system_identifier FROM pg_control_system()"))
        self.stage("baseline")
        super().baseline()
        self.marker_a = decode(self.replay_value)["id"]

    def capture(self):
        super().capture()
        self.stage("target-capture")
        lsn = self.sql("SELECT pg_create_restore_point('" + self.point + "')")
        require(lsn_number(lsn) > lsn_number(self.pitr["backupWALEnd"]), "pitr-target-not-after-backup")
        segment = self.sql("SELECT pg_walfile_name('" + lsn + "'::pg_lsn)")
        require(WAL.fullmatch(segment) is not None, "invalid-pitr-required-wal")
        self.pitr.update(targetLSN=lsn, requiredSegment=segment)
        self.evidence["inventoryDigest"] = fingerprint(self.before_inventory)
        self.evidence["bindingDigest"] = fingerprint({"backup": self.evidence["backupManifestDigest"], "target": self.pitr,
            "inventory": self.before_inventory, "audit": self.manifests, "plan": self.plan, "profiles": self.profiles,
            "databaseSettings": self.database_properties, "ratings": self.rating_expectations})

    def dump(self):
        self.phase("dump")
        self.stage("later-changes")
        self.later_rating_state = backup_ratings.later_changes(self, self.rating_seed)
        self.compose("up", "--no-deps", "--detach", "--wait", "--wait-timeout", "60", "api")
        later, _ = self.api.request("POST", "/api/v1/namespaces", {"slug": "after-pitr-" + self.env["CATALOG_FUNCTIONAL_SUFFIX"],
                                   "displayName": "Committed after the recovery point"}, (201,),
                                  {"Idempotency-Key": self.point + "-later"})
        self.marker_b = later["id"]
        self.compose("stop", "--timeout", "20", "api")
        self.stopped("api", "worker", "peer-archives")
        segment = self.pitr["requiredSegment"]
        self.sql("SELECT pg_switch_wal()")
        end = min(self.end, time.monotonic() + 45)
        while self.sql("SELECT count(*) FROM pg_ls_dir('/archive') AS f WHERE f='" + segment + "'") != "1":
            require(time.monotonic() < end, "pitr-archive-timeout")
            time.sleep(0.2)
        digest = self.tools("sha256sum /archive/" + segment, capture=True).decode("ascii").split()[0]
        require(re.fullmatch(r"[0-9a-f]{64}", digest) is not None, "invalid-pitr-archive-digest")
        self.pitr["requiredWALDigest"] = "sha256:" + digest
        self.compose("stop", "--timeout", "20", "postgres")
        self.stopped("postgres", "api", "worker")
        self.pitr["sourceStopped"] = True
        (self.inputs / "pitr.conf").write_text(recovery_settings(self.point, self.pitr["sourceTimeline"]), encoding="ascii")
        (self.inputs / "pitr.conf").chmod(0o444)
        self.tools("test ! -e /backup/base/standby.signal; "
                   "test ! -e /backup/base/pg_wal/" + segment + "; "
                   "for destination in /restored/data /negative/data; do "
                   "test ! -e \"$destination/PG_VERSION\"; "
                   "gosu postgres cp -a /backup/base/. \"$destination/\"; "
                   "cat /inputs/pitr.conf >> \"$destination/postgresql.auto.conf\"; "
                   "touch \"$destination/recovery.signal\"; chown postgres:postgres \"$destination/recovery.signal\"; "
                   "gosu postgres pg_verifybackup \"$destination\"; done; "
                   "cp -a /archive/. /missing-archive/; rm -- /missing-archive/" + segment)

    def restore(self):
        self.phase("restore")
        self.stage("missing-wal")
        self.stopped("api", "worker", "postgres", "restored")
        self.compose("up", "--no-deps", "--detach", "negative")
        end = min(self.end, time.monotonic() + 45)
        while True:
            raw = self.compose("ps", "--all", "--format", "json", "negative", capture=True)
            state = decode(raw)
            if isinstance(state, list):
                require(len(state) == 1, "pitr-negative-container-missing")
                state = state[0]
            if state.get("State") == "exited":
                require(type(state.get("ExitCode")) is int and state["ExitCode"] != 0, "pitr-missing-wal-exited-successfully")
                break
            require(time.monotonic() < end, "pitr-negative-recovery-timeout")
            time.sleep(0.2)
        log = self.tools("cat /negative/data/logs/pitr.log", capture=True)
        require(missing_wal_failure(log, self.pitr["requiredSegment"]), "pitr-missing-wal-failure-unconfirmed")
        self.pitr["missingWALRejected"] = True
        self.stopped("api", "worker", "postgres", "restored", "negative")
        require(self.receiver.request("GET", "/state")[0] == self.fixture_state and self.stats() == self.receiver_state,
                "pitr-negative-side-effects")

        self.stage("restore-target")
        self.compose("up", "--no-deps", "--detach", "--wait", "--wait-timeout", "60", "restored")
        self.writer = "restored"
        end = min(self.end, time.monotonic() + 45)
        while self.sql("SELECT pg_is_in_recovery() AND pg_get_wal_replay_pause_state()='paused'") != "t":
            require(time.monotonic() < end, "pitr-target-not-paused")
            time.sleep(0.2)
        replay = self.sql("SELECT pg_last_wal_replay_lsn()")
        require(lsn_number(replay) == lsn_number(self.pitr["targetLSN"]), "pitr-wrong-recovery-point")
        require(int(self.sql("SELECT timeline_id FROM pg_control_checkpoint()")) == self.pitr["sourceTimeline"], "pitr-wrong-source-timeline")
        require(self.sql("SELECT system_identifier FROM pg_control_system()") == self.pitr["systemIdentifier"], "pitr-wrong-cluster")
        require(self.sql("SELECT count(*) FROM namespaces WHERE id='" + self.marker_a + "'") == "1", "pitr-change-a-missing")
        require(self.sql("SELECT count(*) FROM namespaces WHERE id='" + self.marker_b + "'") == "0", "pitr-change-b-retained")
        self.pitr.update(replayLSN=replay, targetPaused=True, postBackupChangeRetained=True, laterChangeAbsent=True)
        self.sql("SELECT pg_wal_replay_resume()")
        end = min(self.end, time.monotonic() + 45)
        while self.sql("SELECT pg_is_in_recovery()") != "f":
            require(time.monotonic() < end, "pitr-recovery-not-complete")
            time.sleep(0.2)
        timeline = int(self.sql("SELECT substring(pg_walfile_name(pg_current_wal_lsn()),1,8)"), 16)
        require(timeline > self.pitr["sourceTimeline"], "pitr-recovered-timeline-not-created")
        self.pitr["recoveredTimeline"] = timeline
        require(self.ctl("migrate", "plan", "--config", "/config/config.json") == self.plan, "pitr-migration-plan-mismatch")
        applied = self.ctl("migrate", "apply", "--config", "/config/config.json", "--api-role", "catalog_api", "--worker-role", "catalog_worker")
        require(not applied["applied"] and len(applied["runtimePrivileges"]) == 2
                and all(item["compliant"] is True for item in applied["runtimePrivileges"]), "pitr-runtime-grants-mismatch")
        self.manifest_version = applied["runtimePrivileges"][0]["manifestVersion"]
        require(self.ctl("profiles", "verify") == self.profiles and self.database_settings() == self.database_properties,
                "pitr-restored-settings-mismatch")
        self.evidence["postgresVersions"] = [self.version, int(self.sql("SHOW server_version_num"))]

    def runtime(self):
        self.stage("runtime")
        super().runtime()
        self.api.request("GET", "/api/v1/namespaces/" + self.marker_b, expected=(404,))
        self.pitr["historicalRecoveryTested"] = True
        require(valid_evidence(self.pitr), "pitr-invalid-evidence")
        write_document(self.state / "pitr-recovery.json", self.pitr)


if __name__ == "__main__":
    try:
        require(not sys.argv[1:], "invalid-pitr-recovery-command")
        PITRRecovery(os.environ).run()
    except Exception as error:
        record_scenario_failure("pitr_recovery", error)
        raise SystemExit("Historical recovery failed; inspect the report phase.") from None
