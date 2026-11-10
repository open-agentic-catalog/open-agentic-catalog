"""Pinned runtime acquisition controls with injected responses, no real network."""
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch
import subprocess

import node_runtime as runtime


class NodeRuntimeUnits(unittest.TestCase):
    def test_unsupported_host_stops_before_network(self):
        with patch.object(runtime.platform, "system", return_value="Unsupported"), patch.object(runtime.urllib.request, "urlopen") as acquire:
            with self.assertRaisesRegex(RuntimeError, "No reviewed Node"):
                runtime.install("24.22.1", "/unused")
            acquire.assert_not_called()

    def test_checksum_failure_does_not_activate_runtime_and_cleans_staging(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(runtime.platform, "system", return_value="Linux"), patch.object(runtime.platform, "machine", return_value="x86_64"), patch.object(runtime.urllib.request, "urlopen", return_value=io.BytesIO(b"untrusted archive")):
            destination = Path(directory)
            with self.assertRaisesRegex(RuntimeError, "checksum mismatch"):
                runtime.install("24.22.1", destination)
            self.assertEqual(list(destination.iterdir()), [])

    def test_verified_archive_installs_once_without_changing_personal_paths(self):
        archive = io.BytesIO()
        filename = "node-v24.21.0-linux-x64.tar.gz"
        with tarfile.open(fileobj=archive, mode="w:gz") as bundle:
            for name in runtime.EXECUTABLES:
                member = tarfile.TarInfo(filename.removesuffix(".tar.gz") + "/" + name)
                member.size = 4
                member.mode = 0o755
                bundle.addfile(member, io.BytesIO(b"node"))
        payload = archive.getvalue()
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "node-runtimes.json").write_text(json.dumps({"24.21.0": {filename: hashlib.sha256(payload).hexdigest()}}))
            with patch.object(runtime, "HERE", root), patch.object(runtime.platform, "system", return_value="Linux"), patch.object(runtime.platform, "machine", return_value="x86_64"), patch.object(runtime.urllib.request, "urlopen", return_value=io.BytesIO(payload)) as acquire:
                result = runtime.install("24.21.0", root / "installed")
                self.assertEqual(result.read_bytes(), b"node")
                self.assertEqual(runtime.install("24.21.0", root / "installed"), result)
                acquire.assert_called_once()
                result.write_bytes(b"tampered")
                with self.assertRaisesRegex(RuntimeError, "drift"):
                    runtime.install("24.21.0", root / "installed")

    def test_npm_selection_rejects_nonexact_values_before_acquisition(self):
        with patch.object(runtime.subprocess, "run") as acquire:
            for version in ("latest", "^12.2.1", "12.2.1-beta.1", "12.2.1\n", "../12.2.1"):
                with self.subTest(version=version), self.assertRaisesRegex(RuntimeError, "exact stable"):
                    runtime.install_npm("/unused/bin/node", version, "/unused", env={})
            acquire.assert_not_called()

    def test_npm_installs_separately_reuses_receipt_and_refuses_drift(self):
        def acquire(command, **options):
            work = Path(command[command.index("--prefix") + 1])
            package = work / "node_modules/npm"
            (package / "bin").mkdir(parents=True)
            (package / "package.json").write_text(json.dumps({"version": "12.2.1"}))
            (package / "bin/npm-cli.js").write_text("npm entrypoint")
            (work / "package-lock.json").write_text("locked integrity")
        with tempfile.TemporaryDirectory() as directory, patch.object(runtime.subprocess, "run", side_effect=acquire) as install:
            root = Path(directory)
            node = root / "reviewed-node/bin/node"
            target = root / "npm"
            selected = runtime.install_npm(node, "12.2.1", target, env={"NPM_CONFIG_AUDIT": "false"})
            self.assertEqual(selected, target / "npm-12.2.1/node_modules/npm/bin/npm-cli.js")
            command = install.call_args.args[0]
            self.assertEqual(command[:3], [str(node), str(node.parent.parent / "lib/node_modules/npm/bin/npm-cli.js"), "install"])
            for flag in ("--ignore-scripts", "--no-audit", "--no-fund", "--save-exact"):
                self.assertIn(flag, command)
            self.assertNotIn("--global", command)
            self.assertEqual(command[-1], "npm@12.2.1")
            self.assertFalse(node.parent.parent.exists())
            self.assertEqual(runtime.install_npm(node, "12.2.1", target, env={}), selected)
            install.assert_called_once()
            selected.write_text("changed")
            with self.assertRaisesRegex(RuntimeError, "npm installation drift"):
                runtime.install_npm(node, "12.2.1", target, env={})
            install.assert_called_once()

    def test_failed_npm_acquisition_cleans_staging_without_activation(self):
        with tempfile.TemporaryDirectory() as directory, patch.object(runtime.subprocess, "run", side_effect=subprocess.CalledProcessError(1, "npm")):
            target = Path(directory)
            with self.assertRaises(subprocess.CalledProcessError):
                runtime.install_npm("/unused/bin/node", "12.2.1", target, env={})
            self.assertEqual(list(target.iterdir()), [])
