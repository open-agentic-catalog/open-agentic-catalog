from pathlib import Path
import tempfile
import unittest
from unittest import mock
import failover
import runner


class FailoverTests(unittest.TestCase):
    def test_credentials_are_generated_only_for_owned_failover_profiles(self):
        base = {"CATALOG_FUNCTIONAL_COMMIT": "a" * 40, "CATALOG_FUNCTIONAL_PROFILE": "failover-abrupt"}
        first = runner.compose_environment(base, "a" * 12, failover.Path("/tmp/owned-fixture"))
        second = runner.compose_environment(base, "b" * 12, failover.Path("/tmp/owned-fixture"))
        self.assertRegex(first["CATALOG_FUNCTIONAL_REPLICATION_PASSWORD"], r"^[0-9a-f]{48}$")
        self.assertNotEqual(first["CATALOG_FUNCTIONAL_REPLICATION_PASSWORD"], second["CATALOG_FUNCTIONAL_REPLICATION_PASSWORD"])
        self.assertEqual(first["COMPOSE_PROFILES"], "")
    def test_cleanup_checks_for_leftovers_even_when_compose_succeeds(self):
        for leftover in ("container", "volume", "network", None):
            with tempfile.TemporaryDirectory() as directory, mock.patch.object(Path, "cwd", return_value=Path(directory)):
                instance = runner.Runner({}, "failover-abrupt")
                instance.environment = runner.compose_environment({"CATALOG_FUNCTIONAL_COMMIT": "a" * 40}, instance.suffix, Path(directory))
                instance.compose += ["-f", "test/functional/failover.compose.yaml", "--profile", "failover", "-p", instance.project]
                calls = []
                def execute(arguments, _environment, _seconds, **_kwargs):
                    calls.append(arguments)
                    return b"owned-leftover" if leftover and arguments[1:3] == [leftover, "ls"] else b""
                with mock.patch.object(runner, "command", side_effect=execute):
                    self.assertEqual(instance.cleanup(), leftover is None)
                self.assertIn("failover", calls[0])
                self.assertTrue(all("label=com.docker.compose.project=" + instance.project in args
                                    for args in calls if args[1:3] in (["container", "ls"], ["volume", "ls"], ["network", "ls"])))
