"""Bounded Go SDK source archives; no compiler, transport or publication."""

import gzip
from pathlib import Path
import re
import shutil
import tarfile


def sources(sdk):
    files = [*sorted(sdk.glob("*.go")), *(sdk / name for name in ("go.mod", "go.sum", "README.md", "LICENSE", "NOTICE"))]
    if not list(sdk.glob("*.go")) or any(p.is_symlink() or not p.is_file() for p in files):
        raise ValueError("Go SDK source inventory is incomplete or redirected")
    return files


def build(sdk, version, directory):
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+(?:-rc\.[1-9][0-9]*)?", version):
        raise ValueError("Invalid SDK archive version")
    prefix = "open-agentic-catalog-go-" + version
    output = directory / (prefix + ".tar.gz")
    with output.open("xb") as raw, gzip.GzipFile(fileobj=raw, mode="wb", mtime=0, filename="") as compressed, tarfile.open(fileobj=compressed, mode="w|") as archive:
        for path in sources(sdk):
            data = path.read_bytes()
            if len(data) > 32 << 20:
                raise ValueError("Go SDK source exceeds archive bound")
            item = tarfile.TarInfo(prefix + "/" + path.name)
            item.size, item.mode = len(data), 0o644
            with path.open("rb") as source:
                archive.addfile(item, source)
    return output


def extract(archive_path, sdk, directory):
    """Check the entire generated inventory before writing consumer files."""
    expected = {p.name: p.read_bytes() for p in sources(sdk)}
    prefix = archive_path.name.removesuffix(".tar.gz")
    with tarfile.open(archive_path, "r:gz") as archive:
        members = archive.getmembers()
        if (len(members) != len(expected) or len({m.name for m in members}) != len(members)
                or any(not m.isfile() or m.name != prefix + "/" + Path(m.name).name
                       or Path(m.name).name not in expected or m.size != len(expected[Path(m.name).name]) for m in members)):
            raise ValueError("Go SDK archive inventory differs from source")
        for member in members:
            if archive.extractfile(member).read() != expected[Path(member.name).name]:
                raise ValueError("Go SDK archive bytes differ from source")
        destination = directory / prefix
        destination.mkdir()
        for member in members:
            with archive.extractfile(member) as source, (destination / Path(member.name).name).open("xb") as output:
                shutil.copyfileobj(source, output)
    return destination
