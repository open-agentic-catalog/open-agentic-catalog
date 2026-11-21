"""Verify that setup only publishes a checksum-matched controller export."""
import hashlib
import io
from pathlib import Path
import subprocess
import tarfile
from tempfile import TemporaryDirectory
import unittest
from unittest.mock import patch

from fixtures.flux import install


class ManifestCacheTest(unittest.TestCase):
    def test_valid_cached_manifest_needs_no_cli(self):
        with TemporaryDirectory() as tmp, patch.object(install.subprocess, "run") as run:
            cache = Path(tmp)
            manifest = b"cached export"
            (cache / "controllers.yaml").write_bytes(manifest)
            install.prepare_manifest(cache, {"controllerManifestSha256": hashlib.sha256(manifest).hexdigest()})
            run.assert_not_called()

    def test_mismatch_preserves_previous_manifest(self):
        package = io.BytesIO()
        with tarfile.open(fileobj=package, mode="w:gz") as archive:
            archive.addfile(tarfile.TarInfo("flux"), io.BytesIO())
        raw = package.getvalue()
        for valid_archive in (False, True):
            with self.subTest(valid_archive=valid_archive), TemporaryDirectory() as tmp:
                cache = Path(tmp)
                manifest = cache / "controllers.yaml"
                manifest.write_bytes(b"previous export")
                pins = {"version": "v1", "cliArchiveSha256": hashlib.sha256(raw).hexdigest() if valid_archive else "bad",
                        "controllerManifestSha256": hashlib.sha256(b"expected export").hexdigest()}

                def run(args, **kwargs):
                    if args[0] == "gh":
                        (Path(args[-1]) / "flux_1_linux_amd64.tar.gz").write_bytes(raw)
                        return subprocess.CompletedProcess(args, 0)
                    self.assertTrue(valid_archive, "an unverified binary was executed")
                    return subprocess.CompletedProcess(args, 0, stdout=b"unexpected export")

                with patch.object(install.subprocess, "run", side_effect=run), self.assertRaisesRegex(ValueError, "checksum mismatch"):
                    install.prepare_manifest(cache, pins)
                self.assertEqual(list(cache.iterdir()), [manifest])
                self.assertEqual(manifest.read_bytes(), b"previous export")
