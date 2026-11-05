"""Acquire reviewed Node distributions only during explicit tool installation."""
import hashlib
import json
from pathlib import Path
import platform
import tarfile
import tempfile
import urllib.request

HERE = Path(__file__).resolve().parent
EXECUTABLES = ("bin/node", "lib/node_modules/npm/bin/npm-cli.js")


def install(version, destination):
    system = {"Linux": "linux", "Darwin": "darwin"}.get(platform.system())
    arch = {"x86_64": "x64", "aarch64": "arm64", "arm64": "arm64"}.get(platform.machine())
    filename = f"node-v{version}-{system}-{arch}.tar.gz"
    checksum = json.loads((HERE / "node-runtimes.json").read_text())[version].get(filename)
    if not checksum:
        raise RuntimeError("No reviewed Node distribution for this development host")
    destination = Path(destination)
    target = destination / filename.removesuffix(".tar.gz")
    if (target / "bin/node").is_file():
        receipt = target / ".catalog-runtime.json"
        try:
            hashes = json.loads(receipt.read_text())
            if set(hashes) != set(EXECUTABLES) or any(hashlib.sha256((target / name).read_bytes()).hexdigest() != hashes[name] for name in EXECUTABLES):
                raise ValueError()
        except (OSError, ValueError, TypeError):
            raise RuntimeError("Node installation drift; remove only this owned runtime and reinstall") from None
        return target / "bin/node"
    destination.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="node-acquire-", dir=destination) as temporary:
        archive = Path(temporary) / filename
        with urllib.request.urlopen(f"https://nodejs.org/dist/v{version}/{filename}", timeout=60) as response, archive.open("wb") as output:
            total = 0
            while chunk := response.read(1024 * 1024):
                total += len(chunk)
                if total > 100 * 1024 * 1024:
                    raise RuntimeError("Node distribution exceeds acquisition bound")
                output.write(chunk)
        if hashlib.sha256(archive.read_bytes()).hexdigest() != checksum:
            raise RuntimeError("Node distribution checksum mismatch")
        with tarfile.open(archive) as bundle:
            if sum(member.size for member in bundle.getmembers()) > 1024 ** 3:
                raise RuntimeError("Node distribution exceeds expanded bound")
            bundle.extractall(temporary, filter="data")
        unpacked = Path(temporary) / target.name
        (unpacked / ".catalog-runtime.json").write_text(json.dumps({name: hashlib.sha256((unpacked / name).read_bytes()).hexdigest() for name in EXECUTABLES}))
        unpacked.rename(target)
    return target / "bin/node"
