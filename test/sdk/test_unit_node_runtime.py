"""Pinned runtime acquisition controls with injected responses, no real network."""
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import node_runtime as runtime


def npm_fixture(root, *, extra=None):
    """Create inert reviewed archive bytes, never a real executable."""
    archive = io.BytesIO()
    entries = {"package/package.json": '{"version":"12.2.2"}',
               "package/bin/npm-cli.js": "npm entrypoint", "package/bin/npx-cli.js": "npx entrypoint"}
    entries.update(extra or {})
    with tarfile.open(fileobj=archive, mode="w:gz") as bundle:
        for name, value in entries.items():
            member = tarfile.TarInfo(name)
            member.size = len(value.encode())
            bundle.addfile(member, io.BytesIO(value.encode()))
    payload = archive.getvalue()
    asset = {"version": "12.2.2", "filename": "npm-12.2.2.tgz",
             "url": "https://registry.npmjs.org/npm/-/npm-12.2.2.tgz", "sha256": hashlib.sha256(payload).hexdigest()}
    pins = root / "pins.json"
    pins.write_text(json.dumps({"archives": {"npm": asset}}))
    response = io.BytesIO(payload)
    response.geturl = lambda: asset["url"]
    return pins, asset, response


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
        with patch.object(runtime.urllib.request, "urlopen") as acquire:
            for version in ("latest", "^12.2.1", "12.2.1-beta.1", "12.2.1\n", "../12.2.1"):
                with self.subTest(version=version), self.assertRaisesRegex(RuntimeError, "exact stable"):
                    runtime.install_npm("/unused/bin/node", version, "/unused", env={})
            acquire.assert_not_called()

    def test_npm_installs_separately_reuses_receipt_and_refuses_drift(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pins, asset, response = npm_fixture(root)
            node = root / "reviewed-node/bin/node"
            node.parent.mkdir(parents=True)
            node.write_bytes(b"reviewed Node")
            target = root / "npm"
            with patch.object(runtime, "NPM_PINS", pins), patch.object(runtime.urllib.request, "urlopen", return_value=response) as install:
                selected = runtime.install_npm(node, "12.2.2", target, env={})
                self.assertEqual(selected, target / "npm-12.2.2/node_modules/npm/bin/npm-cli.js")
                self.assertEqual(node.read_bytes(), b"reviewed Node")
                self.assertFalse((selected.parents[3] / "package-lock.json").exists())
                receipt = selected.parents[3] / ".catalog-npm.json"
                self.assertEqual(json.loads(receipt.read_text())["archive"], asset)
                self.assertEqual(runtime.install_npm(node, "12.2.2", target, env={}), selected)
                install.assert_called_once_with(asset["url"], timeout=60)
                for path in (selected, selected.with_name("npx-cli.js")):
                    original = path.read_bytes()
                    path.write_text("changed")
                    with self.subTest(path=path), self.assertRaisesRegex(RuntimeError, "npm installation drift"):
                        runtime.install_npm(node, "12.2.2", target, env={})
                    path.write_bytes(original)
                asset["sha256"] = "0" * 64
                pins.write_text(json.dumps({"archives": {"npm": asset}}))
                with self.assertRaisesRegex(RuntimeError, "npm installation drift"):
                    runtime.install_npm(node, "12.2.2", target, env={})
                install.assert_called_once()

    def test_failed_npm_acquisition_cleans_staging_without_activation(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pins, _, _ = npm_fixture(root)
            target = root / "target"
            with patch.object(runtime, "NPM_PINS", pins), patch.object(runtime.urllib.request, "urlopen", side_effect=OSError("download failed")):
                with self.assertRaisesRegex(OSError, "download failed"):
                    runtime.install_npm("/unused/bin/node", "12.2.2", target, env={})
            self.assertEqual(list(target.iterdir()), [])

    def test_missing_or_inconsistent_npm_pin_stops_before_download(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pins, asset, _ = npm_fixture(root)
            for change in ({"version": "12.2.1"}, {"filename": "../npm.tgz"}, {"sha256": "not-a-hash"},
                           {"url": "https://other.example/npm.tgz"}, {"sha256": None}):
                pins.write_text(json.dumps({"archives": {"npm": dict(asset, **change)}}))
                with self.subTest(change=change), patch.object(runtime, "NPM_PINS", pins), patch.object(runtime.urllib.request, "urlopen") as acquire:
                    with self.assertRaisesRegex(RuntimeError, "No reviewed npm"):
                        runtime.install_npm("/unused/bin/node", "12.2.2", root / "target", env={})
                    acquire.assert_not_called()
                    self.assertFalse((root / "target").exists())
            pins.unlink()
            with patch.object(runtime, "NPM_PINS", pins), patch.object(runtime.urllib.request, "urlopen") as acquire:
                with self.assertRaisesRegex(RuntimeError, "No reviewed npm"):
                    runtime.install_npm("/unused/bin/node", "12.2.2", root / "target", env={})
                acquire.assert_not_called()

    def test_wrong_npm_bytes_fail_before_extraction(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pins, _, response = npm_fixture(root)
            response.write(b"substituted archive")
            response.seek(0)
            target = root / "target"
            target.mkdir()
            retained = target / "retained"
            retained.write_text("retained")
            with patch.object(runtime, "NPM_PINS", pins), patch.object(runtime.urllib.request, "urlopen", return_value=response), patch.object(runtime.tarfile, "open") as unpack:
                with self.assertRaisesRegex(RuntimeError, "npm distribution checksum mismatch"):
                    runtime.install_npm("/unused/bin/node", "12.2.2", target, env={})
                unpack.assert_not_called()
            self.assertEqual(list(target.iterdir()), [retained])
            self.assertEqual(retained.read_text(), "retained")

    def test_npm_refuses_escaping_archive_and_redirected_destination(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            pins, _, response = npm_fixture(root, extra={"package/../escape": "untrusted"})
            outside = root / "outside"
            outside.mkdir()
            (outside / "retained").write_text("retained")
            with patch.object(runtime, "NPM_PINS", pins), patch.object(runtime.urllib.request, "urlopen", return_value=response):
                with self.assertRaisesRegex(RuntimeError, "unexpected archive path"):
                    runtime.install_npm("/unused/bin/node", "12.2.2", root / "target", env={})
            self.assertFalse((root / "target/escape").exists())
            self.assertEqual(list((root / "target").iterdir()), [])
            (root / "redirected").symlink_to(outside, target_is_directory=True)
            with patch.object(runtime, "NPM_PINS", pins), patch.object(runtime.urllib.request, "urlopen") as acquire:
                with self.assertRaisesRegex(RuntimeError, "redirected path"):
                    runtime.install_npm("/unused/bin/node", "12.2.2", root / "redirected", env={})
                acquire.assert_not_called()
            self.assertEqual((outside / "retained").read_text(), "retained")
