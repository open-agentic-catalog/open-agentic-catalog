from pathlib import Path
import ipaddress
import os
import tempfile
import unittest
from unittest import mock
import runner


class BackupTests(unittest.TestCase):
    def test_owned_environment_cannot_select_another_database_or_disable_webhooks(self):
        env = runner.compose_environment({"CATALOG_FUNCTIONAL_COMMIT": "a" * 40, "CATALOG_FUNCTIONAL_PROFILE": "backup-restore",
            "CATALOG_FUNCTIONAL_BACKUP_DATABASE": "shared", "CATALOG_FUNCTIONAL_WEBHOOK_ENABLED": "false", "COMPOSE_PROFILES": "unrelated",
            "CATALOG_FUNCTIONAL_ARCHIVE_IP": "external.invalid", "CATALOG_FUNCTIONAL_ARCHIVE_UID": "other",
            "CATALOG_FUNCTIONAL_ARCHIVE_GID": "other"},
            "a" * 12, Path("/tmp/owned-fixture"))
        self.assertEqual(env["CATALOG_FUNCTIONAL_BACKUP_DATABASE"], "postgres")
        self.assertEqual(env["CATALOG_FUNCTIONAL_WEBHOOK_ENABLED"], "true")
        self.assertEqual(env["COMPOSE_PROFILES"], "")
        self.assertEqual(env["CATALOG_FUNCTIONAL_ARCHIVE_IP"], str(ipaddress.ip_network(env["CATALOG_FUNCTIONAL_SUBNET"])[12]))
        self.assertEqual(env["CATALOG_FUNCTIONAL_ARCHIVE_UID"], str(os.getuid()))
        self.assertEqual(env["CATALOG_FUNCTIONAL_ARCHIVE_GID"], str(os.getgid()))
    def test_cleanup_includes_restored_profile_and_checks_exact_ownership(self):
        for leftover in ("container", "volume", "network", None):
            with tempfile.TemporaryDirectory() as directory, mock.patch.object(Path, "cwd", return_value=Path(directory)):
                instance = runner.Runner({}, "backup-restore")
                instance.environment = runner.compose_environment({"CATALOG_FUNCTIONAL_COMMIT": "a" * 40}, instance.suffix, Path(directory))
                instance.compose += ["-f", "test/functional/backup_restore.compose.yaml", "--profile", "backup-restore", "-p", instance.project]
                calls = []
                def execute(args, *_args, **_kwargs):
                    calls.append(args)
                    return b"owned-leftover" if leftover and args[1:3] == [leftover, "ls"] else b""
                with mock.patch.object(runner, "command", side_effect=execute):
                    self.assertEqual(instance.cleanup(), leftover is None)
                self.assertIn("backup-restore", calls[0])
                self.assertTrue(all("label=com.docker.compose.project=" + instance.project in args for args in calls
                                    if args[1:3] in (["container", "ls"], ["volume", "ls"], ["network", "ls"])))
