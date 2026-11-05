"""Report admission and owned supervisor controls without processes or listeners."""

import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from common import Result
import mcp_runner
import mcp_stdio as s
import mcp_tools as tools
import peer_runner


def client_report():
    return {"client": "mcp@2.3.0", "protocol": "2025-11-25",
            "server": {"name": "open-agentic-catalog-control", "version": "dev"},
            "cases": dict.fromkeys(s.CASES, True), "childrenExited": True, "peerUnchanged": True}


class Admission(unittest.TestCase):
    def test_success_requires_every_group_and_clean_native_exit(self):
        data = client_report()
        self.assertEqual(s.validate_result(Result(0, json.dumps(data), ""), ["private-token"]), data)
        for field, value in (("protocol", "2026-07-28"), ("client", "another-client"),
                             ("childrenExited", False), ("peerUnchanged", False),
                             ("server", {"name": "wrong", "version": "dev"}),
                             ("cases", {name: True for name in s.CASES if name != "eof"}),
                             ("cases", dict(data["cases"], cancellation=False)),
                             ("cases", dict(data["cases"], extra=True))):
            with self.subTest(field=field, value=value), self.assertRaises(AssertionError):
                s.validate_result(Result(0, json.dumps(dict(data, **{field: value})), ""), [])
        for result in (Result(1, json.dumps(data), ""), Result(0, json.dumps(data), "native warning"),
                       Result(0, json.dumps(dict(data, secret="private-token")), "")):
            with self.subTest(result=result.code), self.assertRaises(AssertionError):
                s.validate_result(result, ["private-token"])
        with self.assertRaises(ValueError):
            s.validate_result(Result(0, "not JSON", ""), [])


class Supervisor(unittest.TestCase):
    def exercise(self, directory, *, failure=None, network_cleanup=False, container_cleanup=None, network_creation=None, source_changes=False):
        """Inject the entire process edge; file writes remain invocation-local."""
        (directory / "config").mkdir()
        (directory / "config/local.json").write_text((s.ROOT / "config/local.json").read_text())
        calls = []
        def docker(*args, **_):
            calls.append(args)
            if args[:2] == ("network", "create") and network_creation:
                raise network_creation
            if args[:2] == ("network", "rm") and network_cleanup:
                if isinstance(network_cleanup, Exception):
                    raise network_cleanup
                return Result(1, "", "owned network busy")
            if args[0] == "inspect":
                return Result(0, json.dumps([{"State": {"Running": False, "ExitCode": 0}}]), "")
            if args[:2] == ("start", "-a"):
                return Result(0, json.dumps(client_report()), "")
            return Result(0, "", "")
        def build(*_):
            return {"catalogctl": "compiled-identity"}
        def create(*args, **kwargs):
            if failure:
                raise failure
            return Result(0, "owned-id", "")
        source = iter(["before", "after"] if source_changes else ["before", "before"])
        with patch.object(s, "ROOT", directory), patch.object(s, "local_daemon", return_value="amd64"), \
             patch.object(s.platform, "system", return_value="Linux"), \
             patch.object(s.platform, "machine", return_value="x86_64"), \
             patch.object(tools, "OUTPUT", directory), \
             patch.object(s.mcp_catalog_tools, "installed", return_value={"image": "owned-image"}), \
             patch.object(s, "fingerprint", side_effect=lambda *_: next(source)), \
             patch.object(mcp_runner, "build", side_effect=build), \
             patch.object(mcp_runner, "isolation", return_value={}), \
             patch.object(s, "run", return_value=Result(0, "", "")), \
             patch.object(s, "docker", side_effect=docker), \
             patch.object(tools, "create", side_effect=create), \
             patch.object(tools, "remove", side_effect=container_cleanup) as remove, \
             patch.object(peer_runner, "addresses", return_value=["172.31.0.10"]), \
             patch.object(s, "wait_ready"), patch.object(s, "setup", return_value={"localReleases": 12}):
            if failure or network_cleanup or container_cleanup or network_creation or source_changes:
                with self.assertRaises((RuntimeError, OSError, KeyboardInterrupt, AssertionError)):
                    s.check()
            else:
                s.check()
        report = json.loads(next(directory.glob("stdio-*/report.json")).read_text())
        self.assertFalse(list(directory.glob("stdio-*/private")))
        remove.assert_called_once_with(report["containers"])
        self.assertTrue(any(call[:2] == ("network", "rm") for call in calls))
        return report

    def test_success_binds_all_groups_and_cleanup(self):
        with tempfile.TemporaryDirectory() as name:
            report = self.exercise(Path(name))
        self.assertTrue(report["success"] and report["cleanup"])
        self.assertEqual(report["phase"], "complete")
        self.assertEqual(report["client"]["cases"], dict.fromkeys(s.CASES, True))

    def test_failure_and_interruption_remove_registered_names_and_private_files(self):
        for failure in (RuntimeError("fixture failure"), KeyboardInterrupt(), tools.CreationUnconfirmed("unconfirmed")):
            with self.subTest(failure=type(failure).__name__), tempfile.TemporaryDirectory() as name:
                report = self.exercise(Path(name), failure=failure)
                self.assertEqual(len(report["containers"]), 1, "creation must be registered before interruption")
                self.assertFalse(report["success"])
                self.assertEqual(report["cleanup"], not isinstance(failure, tools.CreationUnconfirmed))

    def test_network_cleanup_failure_cannot_pass(self):
        with tempfile.TemporaryDirectory() as name:
            report = self.exercise(Path(name), network_cleanup=True)
        self.assertFalse(report["cleanup"] or report["success"])

    def test_cleanup_exception_cannot_report_success(self):
        for component in ("network", "container"):
            for failure in (RuntimeError("Docker deadline"), OSError("Docker unavailable")):
                with self.subTest(component=component, failure=type(failure).__name__), tempfile.TemporaryDirectory() as name:
                    report = self.exercise(Path(name), **{component + "_cleanup": failure})
                    self.assertFalse(report["cleanup"])
                    self.assertFalse(report["success"])

    def test_unconfirmed_network_creation_preserves_exact_owned_name(self):
        with tempfile.TemporaryDirectory() as name:
            report = self.exercise(Path(name), network_creation=RuntimeError("Docker deadline"))
        self.assertTrue(report["creation_unconfirmed"])
        self.assertFalse(report["cleanup"] or report["success"])
        self.assertIn("network", report)
        self.assertRegex(report["network"]["name"], r"^catalog-stdio-[a-f0-9]{12}-network$")
        self.assertNotIn("internal", report["network"], "failed creation cannot qualify isolation")

    def test_changed_source_cannot_pass(self):
        with tempfile.TemporaryDirectory() as name:
            report = self.exercise(Path(name), source_changes=True)
        self.assertTrue(report["cleanup"])
        self.assertFalse(report["success"])
        self.assertIn("inputs changed", report["error"])
