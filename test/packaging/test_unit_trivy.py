"""Local scan identity, inventory and failure policy without tool execution."""
import argparse
import copy
from datetime import datetime, timezone
import io
import json
import os
from pathlib import Path
import tempfile
import tomllib
import unittest
from unittest.mock import Mock, patch

import trivy
from packaging_test_helpers import IDENTIFIER


def inspection():
    return {"imageId": IDENTIFIER, "platform": "linux/arm64", "binaries": {
        "catalogd": {"goBuildInfo": "catalogd: go1.27.0\n\tmod\tlocal-main-module\t(devel)\t\n\tdep\texample.test/library\tv1.0.0\th1:x\n"}}}


def native_report():
    return {"SchemaVersion": 2, "Trivy": {"Version": trivy.VERSION}, "ArtifactType": "container_image",
            "ArtifactName": IDENTIFIER, "Metadata": {"ImageID": IDENTIFIER, "ImageConfig": {"os": "linux", "architecture": "arm64"}},
            "Results": [{"Target": "usr/local/bin/catalogd", "Type": "gobinary", "Packages": [{"Name": "example.test/library", "Version": "v1.0.0"}, {"Name": "local-main-module"}, {"Name": "stdlib", "Version": "v1.27.0"}]}]}


def database():
    return {"Version": 2, "UpdatedAt": "2026-09-28T00:00:00Z", "NextUpdate": "2026-09-30T00:00:00Z", "DownloadedAt": "2026-09-28T01:00:00Z"}


class PolicyTests(unittest.TestCase):
    def test_sdk_python_lock_projects_all_registry_packages_and_only_known_local_sources(self):
        root = Path(__file__).resolve().parents[2] / "test/sdk/python"
        local = {"catalog-python-sdk-tools": {"virtual": "."}, "open-agentic-catalog": {"directory": "../../../sdk/python"}}
        expected = trivy.development_dependencies(root, local)
        packages = tomllib.loads((root / "uv.lock").read_text())["package"]
        self.assertEqual(len(expected), len(packages) - 2 + 1)
        self.assertIn(("httpx2", "2.16.0"), expected)
        self.assertIn(("openapi-generator-cli", "7.26.0"), expected)
        self.assertFalse({name for name, _ in expected}.intersection(local))
        with self.assertRaisesRegex(ValueError, "reviewed PyPI"):
            trivy.development_dependencies(root)
        changed = dict(local, **{"open-agentic-catalog": {"directory": "/unreviewed"}})
        with self.assertRaisesRegex(ValueError, "reviewed PyPI"):
            trivy.development_dependencies(root, changed)

    def test_development_scan_uses_private_complete_inputs_and_no_suppressions(self):
        expected = {("ruff", "0.16.7"), ("uv", "0.13.4")}
        seen = []
        def scanner(arguments, **options):
            private = options["cwd"]
            seen.append(private)
            self.assertEqual((private / "inputs/requirements.txt").read_text(), "ruff==0.16.7\nuv==0.13.4\n")
            self.assertEqual((private / "ignore").read_text(), "")
            self.assertEqual((private / "config.yaml").read_text(), "{}\n")
            self.assertIn("--offline-scan", arguments)
            self.assertIn("--list-all-pkgs", arguments)
            self.assertIn("--disable-telemetry", arguments)
            self.assertIn("--skip-version-check", arguments)
            self.assertNotIn("--image-config-scanners", arguments)
            value = {"SchemaVersion": 2, "Trivy": {"Version": trivy.VERSION}, "Results": [{"Target": "requirements.txt", "Type": "pip", "Packages": [{"Name": n, "Version": v} for n, v in sorted(expected)]}]}
            Path(arguments[arguments.index("--output") + 1]).write_text(json.dumps(value))
        with patch.object(trivy, "development_dependencies", return_value=expected), patch.object(trivy, "execute", side_effect=scanner):
            self.assertEqual(trivy.scan_development_dependencies(["/selected/scanner"], Path("/source")), 2)
        self.assertFalse(seen[0].exists(), "private projection must be removed")

    def test_development_lock_inventory_and_safe_projection(self):
        root = Path(__file__).resolve().parents[2]
        expected = trivy.development_dependencies(root)
        self.assertEqual(len(expected), len(tomllib.loads((root / "uv.lock").read_text())["package"]) + 1)
        self.assertIn(("uv", "0.13.6"), expected)
        config = '[tool.uv]\nrequired-version = "==0.13.4"\n'
        with tempfile.TemporaryDirectory() as directory:
            private = Path(directory)
            (private / "pyproject.toml").write_text(config)
            for name, version, source in [("bad\\nname", "1.0", "https://pypi.org/simple"), ("x", "1.0\\nother", "https://pypi.org/simple"), ("x", "1.0", "https://unreviewed.invalid/simple")]:
                (private / "uv.lock").write_text('[[package]]\nname = "' + name + '"\nversion = "' + version + '"\nsource = {registry = "' + source + '"}\n')
                with self.subTest(name=name, version=version, source=source), self.assertRaises(ValueError):
                    trivy.development_dependencies(private)

    def test_development_scan_requires_every_identity_and_rejects_findings(self):
        expected = {("ruff", "0.16.7"), ("uv", "0.13.4")}
        value = {"Results": [{"Target": "requirements.txt", "Type": "pip", "Packages": [{"Name": name, "Version": version} for name, version in sorted(expected)]}]}
        trivy.development_report(value, expected)
        for change in [{"Results": []}, {"Results": value["Results"] * 2}, {"Results": [{**value["Results"][0], "Type": "uv"}]}, {"Results": [{**value["Results"][0], "Packages": []}]}]:
            with self.assertRaises(ValueError):
                trivy.development_report(change, expected)
        for severity in ("LOW", "MEDIUM", "HIGH", "CRITICAL", "invalid"):
            changed = copy.deepcopy(value)
            changed["Results"][0]["Vulnerabilities"] = [{"VulnerabilityID": "controlled-finding", "Severity": severity}]
            if severity in ("LOW", "MEDIUM"):
                trivy.development_report(changed, expected)
            else:
                with self.assertRaises(ValueError):
                    trivy.development_report(changed, expected)

    def test_version_and_required_capabilities(self):
        help_text = b"--image-src --platform --offline-scan --skip-db-update --list-all-pkgs --image-config-scanners"
        for version, help_output, succeeds in [(trivy.VERSION, help_text, True), ("0.66.0", help_text, False), (trivy.VERSION, b"--platform", False),
                                               (trivy.VERSION, help_text.replace(b" --image-config-scanners", b""), False)]:
            with self.subTest(version=version, help=help_output), patch.object(trivy, "execute", side_effect=[json.dumps({"Version": version}).encode(), help_output]):
                if succeeds:
                    self.assertEqual(trivy.qualify(["selected"])["Version"], trivy.VERSION)
                else:
                    with self.assertRaises(ValueError):
                        trivy.qualify(["selected"])
        with patch.object(trivy, "execute", return_value=b"[]"), self.assertRaises(ValueError):
            trivy.qualify(["selected"])

    def test_environment_retains_database_location_and_local_engine_connection(self):
        with patch.dict(os.environ, {"TRIVY_CACHE_DIR": "/cache", "TRIVY_SERVER": "https://remote", "TRIVY_SKIP_FILES": "*", "trivy_severity": "LOW", "DOCKER_HOST": "unix:///owned", "TRIVY_PODMAN_HOST": "/owned/podman.sock"}, clear=True):
            self.assertEqual(trivy.environment(), {"TRIVY_CACHE_DIR": "/cache", "DOCKER_HOST": "unix:///owned", "TRIVY_PODMAN_HOST": "/owned/podman.sock"})

    def test_database_freshness_has_an_exact_boundary(self):
        now = datetime(2026, 9, 29, tzinfo=timezone.utc)
        self.assertEqual(trivy.database_metadata(database(), now), database())
        for changes in [{"Version": 1}, {"NextUpdate": now.isoformat()}, {"UpdatedAt": "2026-09-30T00:00:00Z"}, {"DownloadedAt": "2026-09-28T01:00:00"}, {"UpdatedAt": "invalid"}]:
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                trivy.database_metadata(database() | changes, now)

    def test_image_report_requires_exact_artifact_and_complete_binary_dependencies(self):
        self.assertEqual(trivy.image_report(native_report(), inspection()), {"vulnerabilities": 0, "secrets": 0})
        mutations = [lambda r: r.update(ArtifactName="mutable-tag"), lambda r: r["Metadata"].update(ImageID="wrong"),
                     lambda r: r["Metadata"]["ImageConfig"].update(architecture="amd64"), lambda r: r.update(Results=[]),
                     lambda r: r["Results"][0].update(Packages=[]), lambda r: r["Results"][0]["Packages"][0].pop("Version"), lambda r: r["Results"].append(copy.deepcopy(r["Results"][0])),
                     lambda r: r["Results"][0]["Packages"].pop(), lambda r: r["Results"][0]["Packages"][-1].update(Version="v1.26.0"),
                     lambda r: r["Results"][0]["Packages"].pop(1)]
        for mutate in mutations:
            value = native_report()
            mutate(value)
            with self.assertRaises(ValueError):
                trivy.image_report(value, inspection())
        for build_info in ("", "catalogd: go1.27.0\n", "catalogd: go1.27.0\n\tmod\tlocal-main-module\n\tdep\texample.test/library\tv1.0.0\th1:x\n"):
            inspected = inspection()
            inspected["binaries"]["catalogd"]["goBuildInfo"] = build_info
            with self.assertRaises(ValueError):
                trivy.image_report(native_report(), inspected)

    def test_all_high_and_critical_findings_fail_and_lower_severities_remain_visible(self):
        value = native_report()
        value["Results"][0].update(Vulnerabilities=[{"VulnerabilityID": "CVE-fixture", "Severity": severity} for severity in trivy.SEVERITIES],
                                  Secrets=[{"RuleID": "synthetic", "Severity": "HIGH"}])
        self.assertEqual(trivy.findings(value), {"vulnerabilities": 2, "secrets": 1})
        for entries in [None, {}, [{"VulnerabilityID": "x", "Severity": "new"}], [{"Severity": "HIGH"}]]:
            value["Results"][0]["Vulnerabilities"] = entries
            with self.assertRaises(ValueError):
                trivy.findings(value)

    def test_frontend_inventory_is_exact_and_nonempty(self):
        inventory = {"packages": [{"name": "@example/ui", "version": "1.2.3"}]}
        value = {"Results": [{"Type": "node-pkg", "Target": "image", "Packages": [{"Name": "@example/ui", "Version": "1.2.3"}]}]}
        self.assertEqual(trivy.frontend_report(value, inventory), {"vulnerabilities": 0, "secrets": 0})
        value["Results"][0]["Packages"][0]["Version"] = "1.2.4"
        with self.assertRaises(ValueError):
            trivy.frontend_report(value, inventory)
        with self.assertRaises(ValueError):
            trivy.frontend_report({"Results": []}, inventory)

    def test_report_missing_malformed_empty_wrong_version_and_symlink(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "report.json"
            with self.assertRaises(ValueError):
                trivy.report(path)
            for raw in ["", "{", "[]", "{}", json.dumps(native_report() | {"Results": None}), json.dumps(native_report() | {"Trivy": {"Version": "old"}})]:
                path.write_text(raw)
                with self.assertRaises(ValueError):
                    trivy.report(path)
            path.write_text(json.dumps(native_report()))
            self.assertEqual(trivy.report(path), native_report())
            link = path.with_name("link")
            link.symlink_to(path)
            with self.assertRaises(ValueError):
                trivy.report(link)

    def test_database_snapshot_missing_symlink_and_change_are_rejected(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(trivy, "database_metadata", side_effect=lambda value, now: value):
            root = Path(directory)
            with self.assertRaises(ValueError):
                trivy.snapshot_database(root, root / "snapshot")
            source = root / "db"
            source.mkdir()
            (source / "metadata.json").write_text(json.dumps(database()))
            (source / "trivy.db").write_bytes(b"owned database")
            result = trivy.snapshot_database(root, root / "snapshot")
            self.assertEqual(result["sha256"], trivy.digest(source / "trivy.db"))
            self.assertEqual((root / "snapshot/trivy.db").read_bytes(), b"owned database")
            def changing_copy(src, dest):
                Path(dest).write_bytes(b"old")
                (source / "metadata.json").write_text("{}")
            with patch.object(trivy.shutil, "copyfile", side_effect=changing_copy), self.assertRaises(ValueError):
                trivy.snapshot_database(root, root / "changed")
            (source / "trivy.db").unlink()
            (source / "trivy.db").symlink_to(root / "snapshot/trivy.db")
            with self.assertRaises(ValueError):
                trivy.snapshot_database(root, root / "symlink")

    def test_scan_orchestration_keeps_private_evidence_and_rejects_incomplete_work(self):
        for scenario, expected in [("clean", 0), ("finding", 1), ("config-finding", 1), ("config-malformed", 2), ("error", 2), ("drift", 2), ("ui", 0), ("ui-missing", 2)]:
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as directory:
                options = argparse.Namespace(output=directory, profile="ui" if scenario.startswith("ui") else "backend", trivy="/tools/trivy", engine="docker", platform="linux/arm64")
                value = native_report()
                if scenario == "finding":
                    value["Results"][0]["Secrets"] = [{"RuleID": "synthetic", "Severity": "CRITICAL"}]
                if scenario.startswith("config-"):
                    value["Results"].append({"Target": "image config", "Class": "secret", "Type": "config", "Secrets": [
                        {"RuleID": "synthetic", "Severity": "HIGH" if scenario == "config-finding" else "invalid"}]})
                calls = []
                def execute(command, **kwargs):
                    calls.append((command, kwargs))
                    self.assertIn("--disable-telemetry", command)
                    self.assertIn("--skip-version-check", command)
                    if scenario == "error":
                        raise ValueError("private sentinel")
                    output = value
                    if command[1] == "sbom":
                        self.assertNotIn("--skip-check-update", command)
                        self.assertNotIn("--module-dir", command)
                        self.assertNotIn("--image-config-scanners", command)
                        self.assertEqual(command[command.index("--scanners") + 1], "vuln")
                        if scenario == "ui":
                            output = value | {"Results": [{"Type": "node-pkg", "Target": "Node.js", "Packages": [{"Name": "react", "Version": "1"}]}]}
                    Path(command[command.index("--output") + 1]).write_text(json.dumps(output))
                def snapshot(cache, target):
                    target.mkdir(parents=True)
                    return {"metadata": database(), "sha256": "a" * 64}
                identity = Mock(return_value="changed" if scenario == "drift" else IDENTIFIER)
                with patch.object(trivy, "qualify"), patch.object(trivy, "snapshot_database", side_effect=snapshot), patch.object(trivy, "database_metadata"), patch.object(trivy, "execute", side_effect=execute), patch("sys.stdout", new_callable=io.StringIO) as terminal:
                    self.assertEqual(trivy.scan(options, inspection(), Path(directory), identity, Mock(return_value={}), Mock(return_value={"packages": [{"name": "react", "version": "1"}]})), expected)
                    self.assertNotIn("private sentinel", terminal.getvalue())
                evidence = next(Path(directory).glob("scan-*"))
                self.assertEqual(evidence.stat().st_mode & 0o777, 0o700)
                self.assertEqual((evidence / "run.json").stat().st_mode & 0o777, 0o600)
                metadata = json.loads((evidence / "run.json").read_text())
                self.assertEqual(metadata["outcome"], {0: "passed", 1: "findings", 2: "error"}[expected])
                command, kwargs = calls[0]
                self.assertEqual(command[-1], IDENTIFIER)
                self.assertEqual(command[command.index("--image-src") + 1], "docker")
                self.assertEqual(command[command.index("--platform") + 1], "linux/arm64")
                self.assertEqual(command[command.index("--scanners") + 1], "vuln,secret")
                self.assertEqual(command[command.index("--image-config-scanners") + 1], "secret")
                self.assertEqual(metadata["configuration"]["imageConfigScanners"], ["secret"])
                if scenario == "config-finding":
                    self.assertEqual(metadata["findings"]["image"], {"vulnerabilities": 0, "secrets": 1})
                self.assertIn("--skip-db-update", command)
                self.assertFalse(Path(kwargs["env"]["TRIVY_CACHE_DIR"]).exists())
                if scenario in ("clean", "finding", "config-finding", "drift", "ui"):
                    identity.assert_called_once_with(options)
