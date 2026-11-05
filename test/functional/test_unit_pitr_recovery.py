"""Historical-recovery evidence and workflow decisions without processes or SQL."""

import copy
import tempfile
from pathlib import Path
import unittest
from unittest import mock

import backup_restore
from backup_restore_test_helpers import evidence
import pitr_recovery as pitr
import runner


def physical_evidence():
    return {"targetName": "catalog-pitr-012345abcdef", "sourceTimeline": 1, "recoveredTimeline": 2,
            "backupWALEnd": "0/3000120", "targetLSN": "0/4000090", "replayLSN": "0/4000090",
            "requiredSegment": "000000010000000000000004", "requiredWALDigest": "sha256:" + "c" * 64,
            "systemIdentifier": "123456789", "postBackupChangeRetained": True, "laterChangeAbsent": True,
            "missingWALRejected": True, "targetPaused": True, "sourceStopped": True, "historicalRecoveryTested": True}


class PITRRecoveryTests(unittest.TestCase):
    def test_historical_evidence_requires_exact_target_chronology_and_both_controls(self):
        value = physical_evidence()
        self.assertTrue(pitr.valid_evidence(value))
        changes = {"sourceTimeline": True, "recoveredTimeline": 1, "backupWALEnd": "0/5000000", "replayLSN": "0/4000091",
                   "requiredSegment": "000000020000000000000004", "targetName": "other", "systemIdentifier": "secret",
                   "requiredWALDigest": "wrong", "extra": True}
        changes.update({key: False for key in value if type(value[key]) is bool})
        for key, change in changes.items():
            with self.subTest(key=key):
                changed = dict(value, **{key: change})
                self.assertFalse(pitr.valid_evidence(changed))
        for key in value:
            changed = copy.deepcopy(value)
            del changed[key]
            self.assertFalse(pitr.valid_evidence(changed))

    def test_missing_wal_requires_the_requested_segment_and_failure_before_target(self):
        segment = physical_evidence()["requiredSegment"]
        log = ("cp: cannot stat '/archive/" + segment + "': No such file or directory\n"
               "FATAL: recovery ended before configured recovery target was reached\n").encode()
        self.assertTrue(pitr.missing_wal_failure(log, segment))
        for bad in (b"", log.replace(b"FATAL", b"LOG"), log.replace(b"before configured recovery target was reached", b"after completion"),
                    log.replace(segment.encode(), b"000000010000000000000005"), log.replace(b"No such file or directory", b"Permission denied")):
            self.assertFalse(pitr.missing_wal_failure(bad, segment))
        self.assertFalse(pitr.missing_wal_failure(log, "../../private"))

    def test_common_restore_evidence_keeps_physical_and_logical_formats_distinct(self):
        value = evidence()
        self.assertTrue(backup_restore.valid_evidence(value))
        self.assertFalse(backup_restore.valid_evidence(value, physical=True))
        value["backupManifestDigest"] = value.pop("dumpDigest")
        value["backupBytes"] = 64 << 20
        del value["dumpBytes"]
        self.assertTrue(backup_restore.valid_evidence(value, physical=True))
        self.assertFalse(backup_restore.valid_evidence(value))
        value["backupBytes"] = (512 << 20) + 1
        self.assertFalse(backup_restore.valid_evidence(value, physical=True))

    def test_profile_uses_existing_backup_assertions_and_cannot_pass_without_pitr_evidence(self):
        self.assertEqual(runner.selected_steps("pitr-recovery"), runner.selected_steps("backup-restore"))
        value = evidence()
        value["backupManifestDigest"] = value.pop("dumpDigest")
        value["backupBytes"] = value.pop("dumpBytes")
        for native in (None, physical_evidence()):
            report = {"profile": "pitr-recovery", "backupRestore": value, "pitrRecovery": native,
                      "images": {"api": "owned"}, "servedVersion": {"version": "functional"}, "smoke": {"retained": True}}
            result = runner.finalize(report, list(runner.BACKUP_STEPS), "", True)
            self.assertEqual(result, native is not None)
            self.assertEqual(report["qualification"], "postgres-point-in-time-recovery")

    def test_target_capture_occurs_after_the_final_existing_inventory(self):
        journey = object.__new__(pitr.PITRRecovery)
        journey.point, journey.pitr = "catalog-pitr-012345abcdef", {"backupWALEnd": "0/30", "sourceTimeline": 1}
        journey.evidence, journey.before_inventory = {"backupManifestDigest": "digest"}, {"target": True}
        journey.manifests, journey.plan, journey.profiles = [], [], {}
        journey.database_properties, journey.rating_expectations = {}, {}
        events = []
        def sql(query):
            events.append("target" if "pg_create_restore_point" in query else "segment")
            return "0/40" if "pg_create_restore_point" in query else "000000010000000000000004"
        with mock.patch.object(backup_restore.BackupRestore, "capture", side_effect=lambda: events.append("final-inventory")), \
             mock.patch.object(journey, "stage"), mock.patch.object(journey, "sql", side_effect=sql):
            journey.capture()
        self.assertEqual(events, ["final-inventory", "target", "segment"])
        self.assertEqual(journey.pitr["targetLSN"], "0/40")

    def test_native_checkpoint_failures_remain_closed_source_locations(self):
        with tempfile.TemporaryDirectory() as directory:
            runner.write_document(Path(directory) / "scenario-failure.json", {"scenario": "pitr_recovery", "lines": [200]})
            self.assertEqual(runner.read_scenario_failure(Path(directory), "pitr-recovery"), {"scenario": "pitr_recovery", "lines": [200]})

    def test_unconfirmed_missing_wal_failure_never_starts_the_positive_destination(self):
        journey = object.__new__(pitr.PITRRecovery)
        journey.end, journey.pitr = float("inf"), physical_evidence()
        with mock.patch.object(journey, "phase"), mock.patch.object(journey, "stage"), mock.patch.object(journey, "stopped"), \
             mock.patch.object(journey, "compose", side_effect=[b"", b'{"State":"exited","ExitCode":1}']) as compose, \
             mock.patch.object(journey, "tools", return_value=b"FATAL: unrelated configuration error"):
            with self.assertRaisesRegex(pitr.Failure, "pitr-missing-wal-failure-unconfirmed"):
                journey.restore()
        self.assertEqual(compose.call_count, 2)
        self.assertEqual(compose.call_args_list[0].args[-1], "negative")

    def test_wrong_replay_point_cannot_resume_recovery_or_start_the_catalog(self):
        journey = object.__new__(pitr.PITRRecovery)
        journey.end, journey.pitr = float("inf"), physical_evidence()
        journey.fixture_state, journey.receiver_state = {"requests": 0}, {"accepted": 0}
        journey.receiver = mock.Mock()
        journey.receiver.request.return_value = (journey.fixture_state, {})
        segment = journey.pitr["requiredSegment"]
        log = ("cp: " + segment + ": No such file or directory\nFATAL: recovery ended before configured recovery target was reached").encode()
        with mock.patch.object(journey, "phase"), mock.patch.object(journey, "stage"), mock.patch.object(journey, "stopped"), \
             mock.patch.object(journey, "stats", return_value=journey.receiver_state), mock.patch.object(journey, "tools", return_value=log), \
             mock.patch.object(journey, "compose", side_effect=[b"", b'{"State":"exited","ExitCode":1}', b""]) as compose, \
             mock.patch.object(journey, "sql", side_effect=["t", "0/4000091"]) as sql:
            with self.assertRaisesRegex(pitr.Failure, "pitr-wrong-recovery-point"):
                journey.restore()
        self.assertEqual(sql.call_count, 2)
        self.assertIn("pg_get_wal_replay_pause_state()='paused'", sql.call_args_list[0].args[0])
        self.assertEqual([c.args[-1] for c in compose.call_args_list if c.args[0] == "up"], ["negative", "restored"])


if __name__ == "__main__":
    unittest.main()
