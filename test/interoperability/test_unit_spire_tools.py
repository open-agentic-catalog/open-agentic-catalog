"""Check the SPIRE fixture's offline archive admission using inert file contents."""
import hashlib
import io
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from spire import tools


class SpireArchives(unittest.TestCase):
    def test_only_verified_release_tools_are_extracted_without_acquisition(self):
        with tempfile.TemporaryDirectory() as directory:
            cache = Path(directory)
            archives = []
            for prefix, names in (("spire", ("spire-agent", "spire-server")),
                                  ("spire-extras", ("oidc-discovery-provider",))):
                path = cache / f"{prefix}-{tools.VERSION}-linux-amd64-musl.tar.gz"
                with tarfile.open(path, "w:gz") as archive:
                    for name in (*names, "unselected"):
                        content = name.encode()
                        member = tarfile.TarInfo(f"{prefix}-{tools.VERSION}/bin/{name}")
                        member.size = len(content)
                        archive.addfile(member, io.BytesIO(content))
                archives.append(path)
            pins = tuple(hashlib.sha256(path.read_bytes()).hexdigest() for path in archives)
            with patch.object(tools, "CACHE", cache), patch.dict(tools.DIGESTS, amd64=pins), \
                    patch.object(tools.subprocess, "run", side_effect=AssertionError("unexpected acquisition")):
                dest = cache / "verified"
                dest.mkdir()
                tools.archives("amd64", destination=dest)
                names = {"spire-agent", "spire-server", "oidc-discovery-provider"}
                self.assertEqual({file.name: file.read_bytes() for file in dest.iterdir()},
                                 {name: name.encode() for name in names})
                for path, selected in zip(archives, ("spire-agent", "oidc-discovery-provider"), strict=True):
                    with self.subTest(archive=path.name):
                        original = path.read_bytes()
                        path.write_bytes(original + b"changed")
                        dest = cache / selected
                        dest.mkdir()
                        with self.assertRaisesRegex(ValueError, "Missing or invalid " + path.name):
                            tools.archives("amd64", destination=dest)
                        self.assertFalse((dest / selected).exists())
                        path.write_bytes(original)
