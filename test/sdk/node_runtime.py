"""Acquire reviewed Node distributions only during explicit tool installation."""
import hashlib
import json
from pathlib import Path
import platform
import re
import tarfile
import tempfile
import urllib.request

HERE = Path(__file__).resolve().parent
NPM_PINS = HERE.parent / "ci/pins.json"
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
    try:
        asset = json.loads(NPM_PINS.read_text())["archives"]["npm"]
        filename = "npm-" + version + ".tgz"
        if (asset["version"] != version or asset["filename"] != filename
                or asset["url"] != "https://registry.npmjs.org/npm/-/" + filename
                or not isinstance(asset["sha256"], str)
                or not re.fullmatch(r"[a-f0-9]{64}", asset["sha256"])):
            raise ValueError()
    except (OSError, ValueError, KeyError, TypeError):
        raise RuntimeError("No reviewed npm distribution for the selected version") from None
    destination = Path(destination)
    target = destination / ("npm-" + version)
    owned = [destination, target]
    repository = HERE.parents[1]
    if repository in destination.absolute().parents:
        owned += [ancestor for ancestor in destination.absolute().parents if ancestor.is_relative_to(repository)]
    if any(path.is_symlink() or (path.exists() and not path.is_dir()) for path in owned):
        raise RuntimeError("npm installation destination must be an owned directory, not a redirected path")
    executable = Path("node_modules/npm/bin/npm-cli.js")
    inventory = ("node_modules/npm/package.json", str(executable), "node_modules/npm/bin/npx-cli.js")

    def hashes(directory):
        if any((directory / name).is_symlink() for name in inventory):
            raise ValueError("redirected npm executable")
        return {"archive": asset, "files": {
            name: hashlib.sha256((directory / name).read_bytes()).hexdigest() for name in inventory}}

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
        archive = Path(temporary) / filename
        with urllib.request.urlopen(asset["url"], timeout=60) as response, archive.open("wb") as output:
            if not response.geturl().startswith("https://"):
                raise RuntimeError("npm distribution redirected outside HTTPS")
            total = 0
            while chunk := response.read(1024 * 1024):
                total += len(chunk)
                if total > 32 * 1024 * 1024:
                    raise RuntimeError("npm distribution exceeds acquisition bound")
                output.write(chunk)
        if hashlib.sha256(archive.read_bytes()).hexdigest() != asset["sha256"]:
            raise RuntimeError("npm distribution checksum mismatch")
        work = Path(temporary) / "work"
        modules = work / "node_modules"
        modules.mkdir(parents=True)
        with tarfile.open(archive) as bundle:
            members = bundle.getmembers()
            if len(members) > 10000 or sum(member.size for member in members) > 128 * 1024 * 1024:
                raise RuntimeError("npm distribution exceeds expanded bound")
            if any(not Path(member.name).parts or Path(member.name).parts[0] != "package"
                   or ".." in Path(member.name).parts for member in members):
                raise RuntimeError("npm distribution has an unexpected archive path")
            bundle.extractall(modules, filter="data")
        (modules / "package").rename(modules / "npm")
        actual = json.loads((work / "node_modules/npm/package.json").read_text())
        if actual["version"] != version:
            raise RuntimeError("npm version mismatch")
        (work / ".catalog-npm.json").write_text(json.dumps(hashes(work), sort_keys=True) + "\n")
        work.rename(target)
    return target / executable
