"""Go SDK archive tests without a compiler, database or subprocess."""

import gzip
import hashlib
import io
from pathlib import Path
import tarfile
import tempfile
import unittest

import go_archive as g


class GoArchiveTests(unittest.TestCase):
    def fixture(self, root):
        sdk = root / "sdk"
        sdk.mkdir()
        for name in ("client.go", "go.mod", "go.sum", "README.md", "LICENSE", "NOTICE"):
            (sdk / name).write_text(name)
        (sdk / ".env").write_text("must not ship")
        (sdk / "target").mkdir()
        (sdk / "target/private").write_text("must not ship")
        return sdk

    def test_reproducible_archive_and_complete_extracted_consumer(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            sdk = self.fixture(root)
            first = g.build(sdk, "0.1.0-rc.1", root)
            destination = root / "consumer"
            destination.mkdir()
            extracted = g.extract(first, sdk, destination)
            self.assertEqual({p.name for p in extracted.iterdir()}, {"client.go", "go.mod", "go.sum", "README.md", "LICENSE", "NOTICE"})
            checksum = hashlib.sha256(first.read_bytes()).hexdigest()
            first.unlink()
            second = g.build(sdk, "0.1.0-rc.1", root)
            self.assertEqual(hashlib.sha256(second.read_bytes()).hexdigest(), checksum)
            for name in ("../escape", "0.1.0/escape", "0.1.0+wrong"):
                with self.assertRaises(ValueError):
                    g.build(sdk, name, root)

    def test_symlink_and_tampered_archive_refuse_before_consumer_writes(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            sdk = self.fixture(root)
            (sdk / "link.go").symlink_to(sdk / "client.go")
            with self.assertRaises(ValueError):
                g.sources(sdk)
            (sdk / "link.go").unlink()
            archive = g.build(sdk, "0.1.0", root)
            (sdk / "client.go").write_text("tampered!")
            destination = root / "consumer"
            destination.mkdir()
            with self.assertRaises(ValueError):
                g.extract(archive, sdk, destination)
            self.assertEqual(list(destination.iterdir()), [])
            for malicious in ("../escape", "open-agentic-catalog-go-0.1.0/link.go"):
                with archive.open("wb") as raw, gzip.GzipFile(fileobj=raw, mode="wb") as compressed, tarfile.open(fileobj=compressed, mode="w|") as bundle:
                    item = tarfile.TarInfo(malicious)
                    item.size = 1
                    bundle.addfile(item, io.BytesIO(b"x"))
                with self.assertRaises(ValueError):
                    g.extract(archive, sdk, destination)
                self.assertEqual(list(destination.iterdir()), [])
