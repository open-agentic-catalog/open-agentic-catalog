"""Acquire reviewed Node distributions only during explicit tool installation."""
import hashlib
import json
from pathlib import Path
import platform
import re
import subprocess
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


def install_npm(node, version, destination, *, env):
    """Acquire an exact npm separately, preserving reviewed Node distributions."""
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", version):
        raise RuntimeError("npm selection must be an exact stable version")
    node, destination = Path(node), Path(destination)
    target = destination / ("npm-" + version)
    executable = Path("node_modules/npm/bin/npm-cli.js")
    inventory = ("package-lock.json", "node_modules/npm/package.json", str(executable))

    def hashes(directory):
        return {name: hashlib.sha256((directory / name).read_bytes()).hexdigest() for name in inventory}

    if target.exists():
        try:
            package = json.loads((target / "node_modules/npm/package.json").read_text())
            if package["version"] != version or json.loads((target / ".catalog-npm.json").read_text()) != hashes(target):
                raise ValueError()
        except (OSError, ValueError, KeyError, TypeError):
            raise RuntimeError("npm installation drift; remove only this owned npm directory and reinstall") from None
        return target / executable
    destination.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="npm-acquire-", dir=destination) as temporary:
        work = Path(temporary) / "package"
        work.mkdir()
        (work / "package.json").write_text(json.dumps({"name": "catalog-npm-tool", "private": True}))
        bundled = node.parent.parent / "lib/node_modules/npm/bin/npm-cli.js"
        subprocess.run([str(node), str(bundled), "install", "--prefix", str(work), "--ignore-scripts",
                        "--no-audit", "--no-fund", "--save-exact", "npm@" + version],
                       env=env, check=True, timeout=300)
        actual = json.loads((work / "node_modules/npm/package.json").read_text())
        if actual["version"] != version:
            raise RuntimeError("npm version mismatch")
        (work / ".catalog-npm.json").write_text(json.dumps(hashes(work), sort_keys=True) + "\n")
        work.rename(target)
    return target / executable
