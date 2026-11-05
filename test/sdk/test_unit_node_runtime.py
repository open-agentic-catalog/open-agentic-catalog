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
