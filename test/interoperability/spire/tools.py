"""Verified Linux binaries from the Apache-2.0 SPIRE release; test use only."""
import argparse
import hashlib
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile

HERE = Path(__file__).resolve().parent
sys.path.insert(0, str(HERE.parent))
from runner import local_daemon  # noqa: E402

CACHE = HERE.parent / ".tools/spire"
VERSION = "1.16.0"
DIGESTS = {
    "amd64": ("7bcf5e167151a1fc58293768462b44e155ba8784214582aefa6adf18c633d1d5",
              "b2ee7828549e46a333c5ba83541457214d1f6d886000168231aed086236137d5"),
    "arm64": ("ba2804905a27ca72b0478ae790b580ea26233abafac7e480bbaf54152d5c2c5b",
              "95bd0838be0aa96cebc0c71fe5683ecd63f751ad4ece57f71d001c26e4a1773e"),
}


def verified(path, digest):
    if not path.is_file():
        return False
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest() == digest


def archives(arch, *, install=False, destination=None):
    for prefix, names, digest in zip(("spire", "spire-extras"), (("spire-agent", "spire-server"),
                                     ("oidc-discovery-provider",)), DIGESTS[arch], strict=True):
        filename = f"{prefix}-{VERSION}-linux-{arch}-musl.tar.gz"
        path = CACHE / filename
        if not verified(path, digest):
            if not install:
                raise ValueError(f"Missing or invalid {filename}; run make spire-tools-install")
            CACHE.mkdir(parents=True, exist_ok=True)
            with tempfile.TemporaryDirectory(dir=CACHE) as staging:
                subprocess.run(["gh", "release", "download", "v" + VERSION, "--repo", "spiffe/spire",
                                "--pattern", filename, "--dir", staging], check=True, timeout=300)
                download = Path(staging) / filename
                if not verified(download, digest):
                    raise ValueError(f"SPIRE checksum mismatch for {filename}")
                download.replace(path)
        if destination:
            with tarfile.open(path) as archive:
                for name in names:
                    member = archive.getmember(f"{prefix}-{VERSION}/bin/{name}")
                    if not member.isfile():
                        raise ValueError(f"Expected regular file for {name}")
                    with archive.extractfile(member) as source, (destination / name).open("xb") as binary:
                        shutil.copyfileobj(source, binary)
                    (destination / name).chmod(0o555)


if __name__ == "__main__":
    argparse.ArgumentParser(description=__doc__).parse_args()
    archives(local_daemon(), install=True)
