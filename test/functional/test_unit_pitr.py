"""Isolated PITR preparation decisions; no commands, databases or listeners."""

import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import pitr
import runner


class PITRTests(unittest.TestCase):
    def test_recovery_settings_bind_one_target_and_disable_source_fallback(self):
        value = pitr.recovery_settings("catalog-pitr-012345abcdef", 7)
        for line in ("recovery_target_name = 'catalog-pitr-012345abcdef'", "recovery_target_timeline = '7'",
                     "recovery_target_action = 'pause'", "primary_conninfo = ''", "primary_slot_name = ''",
                     "restore_command = 'cp /archive/%f %p'", "archive_mode = off"):
            self.assertIn(line + "\n", value)
        for name in (None, "", "name'\nprimary_conninfo='host=elsewhere", "catalog-pitr-012345abcdef\n"):
            with self.subTest(name=name), self.assertRaises(pitr.Failure):
                pitr.recovery_settings(name, 1)
        for timeline in (True, "latest", 0, -1, 0x100000000):
            with self.subTest(timeline=timeline), self.assertRaises(pitr.Failure):
                pitr.recovery_settings("catalog-pitr-012345abcdef", timeline)

    def test_wal_comparison_is_numeric_and_bound_to_the_source_timeline(self):
        self.assertGreater(pitr.lsn_number("1/0"), pitr.lsn_number("0/FFFFFFFF"))
        self.assertGreater(pitr.lsn_number("0/A"), pitr.lsn_number("0/9"))
        value = {"WAL-Ranges": [{"Timeline": 1, "Start-LSN": "0/10", "End-LSN": "0/20"}]}
        self.assertEqual(pitr.backup_wal_range(value, 1), ("0/10", "0/20"))
        for bad in (None, "0/", "-1/0", "100000000/0", "0/100000000", "0/0\n"):
            with self.subTest(value=bad), self.assertRaises(pitr.Failure):
                pitr.lsn_number(bad)
        for value in ({}, {"WAL-Ranges": []}, {"WAL-Ranges": [None]},
                      {"WAL-Ranges": [{"Timeline": 2, "Start-LSN": "0/10", "End-LSN": "0/20"}]},
                      {"WAL-Ranges": [{"Timeline": True, "Start-LSN": "0/10", "End-LSN": "0/20"}]},
                      {"WAL-Ranges": [{"Timeline": 1, "Start-LSN": "0/20", "End-LSN": "0/10"}]}):
            with self.subTest(value=value), self.assertRaises(pitr.Failure):
                pitr.backup_wal_range(value, 1)

    def test_environment_cannot_redirect_compose_or_postgres(self):
        value = pitr.compose_environment({"PATH": "/tools", "HOME": "/client-home", "PGHOST": "shared",
            "PGSERVICEFILE": "/private", "COMPOSE_FILE": "other.yaml", "COMPOSE_PROFILES": "restore",
            "CATALOG_DATABASE_DSN": "secret", "CATALOG_PITR_PASSWORD": "inherited"}, Path("/owned"), "fresh")
        self.assertEqual(value, {"PATH": "/tools", "HOME": "/client-home", "CATALOG_PITR_INPUTS": "/owned",
                                "CATALOG_PITR_PASSWORD": "fresh", "COMPOSE_PROFILES": "", "COMPOSE_DISABLE_ENV_FILE": "true"})

    def test_inventory_refuses_unowned_labels_without_a_command(self):
        with mock.patch("pitr.command") as call:
            for project in (None, "", "open-agentic-catalog", "open-agentic-catalog-pitr-012345abcdef\n"):
                with self.subTest(project=project), self.assertRaises(pitr.Failure):
                    pitr.owned_inventory(["docker"], {}, project, lambda: 1)
            call.assert_not_called()

    def test_cleanup_never_removes_a_project_that_failed_the_ownership_check(self):
        fixture = pitr.Preparation({}, "/unused", "012345abcdef")
        with mock.patch("pitr.owned_inventory", return_value={"volumes": [b"preexisting"]}), mock.patch.object(fixture, "call") as call:
            with self.assertRaisesRegex(pitr.Failure, "pitr-project-already-exists"):
                fixture.prepare(Path("/unused"))
            self.assertTrue(fixture.cleanup())
            call.assert_not_called()

    def test_cleanup_checks_all_profiles_and_rejects_remaining_resources_or_inventory_errors(self):
        for remaining in ({"containers": [], "volumes": [], "networks": []}, {"volumes": [b"remaining"]}, pitr.Failure("inventory-failed")):
            fixture = pitr.Preparation({}, "/unused", "012345abcdef")
            fixture.claimed, fixture.environment = True, {}
            with mock.patch.object(fixture, "call") as call, mock.patch("pitr.owned_inventory") as inventory:
                if isinstance(remaining, Exception):
                    inventory.side_effect = remaining
                else:
                    inventory.return_value = remaining
                self.assertEqual(fixture.cleanup(), not isinstance(remaining, Exception) and not any(remaining.values()))
                call.assert_called_once_with("--profile", "*", "down", "--volumes", "--remove-orphans", "--timeout", "10")
        with mock.patch.object(fixture, "call", side_effect=pitr.Failure("down-failed")), mock.patch("pitr.owned_inventory") as inventory:
            self.assertFalse(fixture.cleanup())
            inventory.assert_not_called()

    def test_runner_dispatches_preparation_without_the_catalog_workflow(self):
        with mock.patch("sys.argv", ["runner.py", "run-pitr-prepare"]), mock.patch("pitr.run_prepare", return_value=7) as prepare, mock.patch("runner.run") as run:
            self.assertEqual(runner.main(), 7)
            prepare.assert_called_once_with()
            run.assert_not_called()

    def test_failures_interruptions_incomplete_evidence_and_cleanup_failure_cannot_pass(self):
        for error, complete, clean, expected in ((None, True, True, None), (pitr.Failure("backup-failed"), False, True, "backup-failed"),
                (KeyboardInterrupt(), False, True, "interrupted"), (None, False, True, "missing-pitr-preparation-evidence"),
                (None, True, False, "pitr-cleanup-failed")):
            with self.subTest(error=error, complete=complete, clean=clean), tempfile.TemporaryDirectory() as directory:
                old = Path.cwd()
                try:
                    os.chdir(directory)
                    with mock.patch("pitr.Preparation") as factory, mock.patch("pitr.signal.signal"), mock.patch("pitr.print"):
                        fixture = factory.return_value
                        fixture.prepare.side_effect, fixture.cleanup.return_value = error, clean
                        fixture.report = {"completed": list(pitr.COMPLETED) if complete else [], "status": "incomplete"}
                        self.assertEqual(pitr.run_prepare({}), 0 if expected is None else 1)
                        fixture.cleanup.assert_called_once_with()
                        self.assertEqual(fixture.report.get("failureCode"), expected)
                        self.assertEqual(fixture.report["cleanup"], "passed" if clean else "failed")
                        reports = list(Path("output/functional").glob("run-*/report.json"))
                        self.assertEqual(len(reports), 1)
                        self.assertEqual(runner.read_document(reports[0]), fixture.report)
                        self.assertFalse(list(reports[0].parent.glob(".pitr-inputs-*")))
                finally:
                    os.chdir(old)
