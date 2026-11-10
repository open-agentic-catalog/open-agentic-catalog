"""Pure Pi harness decisions: no native imports, listeners, processes or installation."""

import copy
from contextlib import redirect_stdout
from datetime import datetime, timezone
import importlib.util
import io
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import pi_ard as pi


def lock_fixture():
    return json.loads((pi.INPUTS / "runtime/package-lock.json").read_text())


def evidence():
    return {
        "readOnlyRoot": True,
        "network": "loopback-only",
        "externalConnectErrno": 101,
        "memoryMax": 2147483648,
        "cpuCores": 2,
        "tasksMax": 256,
        "swapMax": 0,
        "fileLimit": 8388608,
        "openFiles": 256,
        "native": {
            "activeTools": ["ard_catalog", "ard_install", "ard_search"],
            "fetchUnchanged": True,
            "dispatcherInitialized": True,
            "dispatcherPackage": "undici",
            "dispatcherVersion": "8.12.3",
            "dispatcherStableAfterImport": True,
            "offline": True,
            "scriptedTurn": True,
            "localConfig": True,
            "toolCalls": 0,
            "searchPages": 0,
            "confirms": 0,
            "artifactFiles": 0,
            "httpTimeoutMs": 15000,
            "httpBytes": 1000000,
            "nativeLoopbackRequests": 2,
        },
    }


class PiUnits(unittest.TestCase):
    def test_reviewed_lock_age_integrity_and_license(self):
        lock = lock_fixture()
        self.assertEqual(len(pi.validate_lock(lock, datetime(2026, 11, 10, 3, tzinfo=timezone.utc))), 123)
        for key, value in (
            ("integrity", ""),
            ("resolved", "https://other.invalid/"),
            ("license", "unknown"),
        ):
            wrong = copy.deepcopy(lock)
            wrong["packages"]["node_modules/undici"][key] = value
            with self.subTest(key=key), self.assertRaises(ValueError):
                pi.validate_lock(wrong, datetime(2026, 11, 10, 3, tzinfo=timezone.utc))
        with self.assertRaises(ValueError):
            pi.validate_lock(lock, datetime(2026, 10, 25, tzinfo=timezone.utc))

    def test_scan_requires_complete_inventory_and_no_findings(self):
        pairs = {("undici", "8.12.3")}
        finding = {
            "VulnerabilityID": "CVE-2026-109383",
            "PkgName": "undici",
            "InstalledVersion": "8.10.2",
        }
        report = {
            "SchemaVersion": 2,
            "Results": [{"Type": "npm", "Packages": [{"Name": "undici", "Version": "8.12.3"}], "Vulnerabilities": []}],
        }
        self.assertEqual(pi.validate_scan(report, pairs), [])
        for change in ({"Packages": []}, {"Vulnerabilities": [finding]}, {"Vulnerabilities": [finding | {"VulnerabilityID": "unknown"}]}):
            wrong = copy.deepcopy(report)
            wrong["Results"][0].update(change)
            with self.assertRaises(ValueError):
                pi.validate_scan(wrong, pairs)
        with self.assertRaises(ValueError):
            pi.validate_scan(report | {"Results": []}, pairs)

    def test_age_exceptions_bind_exact_packages_and_preserve_other_age_limits(self):
        lock = lock_fixture()
        now = datetime(2026, 11, 10, 3, tzinfo=timezone.utc)
        pi.validate_lock(lock, now)
        wrong = copy.deepcopy(lock)
        wrong["packages"]["node_modules/minimatch"]["integrity"] = "sha512-AAAA"
        with self.assertRaisesRegex(ValueError, "approved package"):
            pi.validate_lock(wrong, now)
        for mutation, message in (
            (lambda pin: pin.update(ageExceptions=[]), "14-day rule"),
            (lambda pin: pin["releaseDates"].pop("undici@8.12.3"), "incomplete"),
            (lambda pin: pin["releaseDates"].update({"undici@8.12.3": "2026-11-02T20:00:00Z"}), "14-day rule"),
            (lambda pin: pin["releaseDates"].update({"minimatch@11.0.3": "2026-11-11T00:00:00Z"}), "unpublished"),
        ):
            pin = copy.deepcopy(pi.PIN)
            mutation(pin)
            with self.subTest(message=message), patch.object(pi, "PIN", pin), self.assertRaisesRegex(ValueError, message):
                pi.validate_lock(lock, now)

    def test_native_evidence_requires_exact_types_and_all_controls(self):
        value = evidence()
        pi.validate_result(value)
        for key, replacement in (
            ("toolCalls", False),
            ("activeTools", ["ard_search", "bash"]),
            ("offline", False),
            ("fetchUnchanged", False),
            ("localConfig", False),
            ("confirms", 1),
        ):
            wrong = copy.deepcopy(value)
            wrong["native"][key] = replacement
            with self.subTest(key=key), self.assertRaises(ValueError):
                pi.validate_result(wrong)
        for key in value:
            wrong = copy.deepcopy(value)
            del wrong[key]
            with self.subTest(key=key), self.assertRaises(ValueError):
                pi.validate_result(wrong)
        with self.assertRaises(ValueError):
            pi.validate_result(value | {"memoryMax": True})
        with self.assertRaises(ValueError):
            pi.validate_result(value | {"privateToken": "must refuse"})

    def test_scope_ownership_and_namespace_bounds(self):
        command = pi.scope_command("oac-pi-123456abcdef.service", ["bwrap"])
        for expected in (
            "--uid=" + str(pi.os.getuid()),
            "MemoryMax=2147483648",
            "MemorySwapMax=0",
            "RuntimeMaxSec=180",
            "CPUQuota=200%",
            "TasksMax=256",
            "KillMode=control-group",
            "TimeoutStopSec=5",
        ):
            self.assertIn(expected, command)
        with self.assertRaises(ValueError):
            pi.scope_command("other.service", [])
        with self.assertRaises(ValueError):
            pi.stop_scope("other.service")
        sandbox = pi.sandbox(Path("/owned"), "prepare")
        for expected in (
            "--clearenv",
            "--unshare-net",
            "--unshare-pid",
            "--unshare-user",
            "--die-with-parent",
            "--remount-ro",
        ):
            self.assertIn(expected, sandbox)
        self.assertNotIn("/home", sandbox)
        self.assertNotIn("/root", sandbox)
        self.assertNotIn("/var/run/docker.sock", sandbox)
        env = pi.environment(Path("/private"))
        self.assertEqual(env["HOME"], "/private")
        self.assertEqual(env["npm_config_ignore_scripts"], "true")
        self.assertEqual(env["npm_config_audit"], "false")

    def test_host_home_or_docker_visibility_refuses_before_network_or_limit_changes(self):
        spec = importlib.util.spec_from_file_location("pi_confined", pi.INPUTS / "confined.py")
        confined = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(confined)

        class RouteCheckReached(Exception):
            pass

        for exposed in (None, "/home", "/root", "/var/run/docker.sock"):
            with self.subTest(exposed=exposed), patch.dict(confined.os.environ, {}, clear=True), \
                    patch.object(confined.os, "statvfs", return_value=SimpleNamespace(f_flag=confined.os.ST_RDONLY)), \
                    patch.object(confined.os, "getuid", return_value=1000), \
                    patch.object(confined.Path, "exists", autospec=True, side_effect=lambda p: str(p) == exposed), \
                    patch.object(confined.Path, "read_text", side_effect=RouteCheckReached) as read, \
                    patch.object(confined.socket, "socket") as connect, \
                    patch.object(confined.resource, "setrlimit") as limits:
                if exposed is None:
                    with self.assertRaises(RouteCheckReached):
                        confined.isolation()
                    read.assert_called_once()
                else:
                    with self.assertRaisesRegex(RuntimeError, "unexpected host authority"):
                        confined.isolation()
                    read.assert_not_called()
                connect.assert_not_called()
                limits.assert_not_called()

    def test_installation_rejects_changed_evidence_tools_and_missing_stamp(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            tools, node = root / "tools", root / "node"
            (tools / "source").mkdir(parents=True)
            (tools / "runtime").mkdir()
            (tools / "runtime/package-lock.json").write_text("owned runtime lock")
            (node / "bin").mkdir(parents=True)
            (node / "bin/node").write_text("owned unit fixture, never executed")
            (tools / "source/code").write_text("original")
            (tools / "Pi-MIT-LICENSE").write_text("owned fixture")
            (tools / "inventory.json").write_text("[]")
            stamp = {
                "revision": pi.PIN["revision"],
                "version": pi.PIN["version"],
                "hostVersion": pi.PIN["hostVersion"],
                "inputs": pi.sha(pi.INPUTS / "inputs.json"),
                "toolDigest": pi.tree_digest(tools / "source"),
                "runtimeDigest": pi.tree_digest(tools / "runtime"),
                "nodeDigest": pi.sha(node / "bin/node"),
                "hostLicenseDigest": pi.sha(tools / "Pi-MIT-LICENSE"),
                "reviewFiles": {"inventory.json": pi.sha(tools / "inventory.json")},
            }
            (tools / "installation.json").write_text(json.dumps(stamp))
            with patch.object(pi, "TOOLS", tools), patch.object(pi, "NODE", node), patch.object(pi, "prerequisites"):
                self.assertEqual(pi.installed(), stamp)
                (tools / "inventory.json").write_text("changed")
                with self.assertRaisesRegex(ValueError, "acquisition evidence changed"):
                    pi.installed()
                (tools / "inventory.json").write_text("[]")
                (tools / "runtime/package-lock.json").write_text("changed")
                with self.assertRaisesRegex(ValueError, "installation changed"):
                    pi.installed()
                (tools / "runtime/package-lock.json").write_text("owned runtime lock")
                (tools / "source/code").write_text("changed")
                with self.assertRaisesRegex(ValueError, "installation changed"):
                    pi.installed()
                (tools / "installation.json").unlink()
                with self.assertRaises(FileNotFoundError):
                    pi.installed()

    def test_cleanup_records_failure_and_preserves_live_state(self):
        with tempfile.TemporaryDirectory() as temporary:
            private = Path(temporary) / "owned"
            private.mkdir()
            report = {}
            with patch.object(pi, "stop_scope", side_effect=RuntimeError("still active")):
                with self.assertRaises(RuntimeError):
                    pi.cleanup_run(private, "owned", report)
            self.assertTrue(private.exists())
            self.assertEqual(report["cleanup"], "failed")
            with patch.object(pi, "stop_scope"):
                pi.cleanup_run(private, "owned", report)
            self.assertFalse(private.exists())
            self.assertEqual(report["cleanup"], "passed")

    def test_private_setup_failure_still_cleans_and_records_the_owned_case(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            private = root / "private"
            private.mkdir()
            output = root / "reports"
            output.mkdir()
            original_mkdir = Path.mkdir

            def mkdir(path, *args, **kwargs):
                if path == private / "home":
                    raise OSError("owned setup failure")
                return original_mkdir(path, *args, **kwargs)

            with (
                patch.object(pi, "OUTPUT", output),
                patch.object(pi, "installed", return_value={}),
                patch.object(pi, "fingerprint", return_value="owned source"),
                patch.object(pi.tempfile, "mkdtemp", return_value=str(private)),
                patch.object(pi, "stop_scope") as stop,
                patch.object(pi.signal, "alarm"),
                patch.object(Path, "mkdir", mkdir),
                redirect_stdout(io.StringIO()),
            ):
                with self.assertRaisesRegex(OSError, "owned setup failure"):
                    pi.prepare()
            stop.assert_called_once()
            self.assertFalse(private.exists())
            report = json.loads(next(output.glob("run-*/report.json")).read_text())
            self.assertEqual(report["status"], "failed")
            self.assertEqual(report["cleanup"], "passed")
            self.assertEqual(report["privateDirectory"], str(private))
            self.assertEqual(report["controls"], {})

    def test_tool_fingerprint_catches_mutation_and_escaping_links(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "code").write_text("before")
            digest = pi.tree_digest(root)
            (root / "code").write_text("after")
            self.assertNotEqual(pi.tree_digest(root), digest)
            (root / "link").symlink_to("code")
            pi.tree_digest(root)
            (root / "link").unlink()
            (root / "link").symlink_to("/etc/passwd")
            with self.assertRaises(ValueError):
                pi.tree_digest(root)

    def test_diagnostics_reject_links_and_oversized_files(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            private, output = root / "private", root / "report"
            private.mkdir()
            output.mkdir()
            log = private / "catalog.log"
            log.write_bytes(b"owned diagnostics")
            pi.retain_campaign_logs(private, output)
            self.assertEqual((output / "catalog.log").read_bytes(), b"owned diagnostics")
            log.unlink()
            (root / "outside").write_bytes(b"must not be copied")
            log.symlink_to(root / "outside")
            with self.assertRaisesRegex(ValueError, "symlink"):
                pi.retain_campaign_logs(private, output)
            log.unlink()
            log.write_bytes(b"x" * (pi.PIN["bounds"]["outputBytes"] + 1))
            with self.assertRaisesRegex(ValueError, "output limit"):
                pi.retain_campaign_logs(private, output)
