"""Acquire one unchanged configured-source registry with its original frozen lock."""

import base64
import hashlib
import json
import shutil
import tempfile
import urllib.request
from pathlib import Path

from common import run
import runner as shared

ROOT, HERE = shared.ROOT, shared.HERE
INPUTS = HERE / "ard-registry"
PIN = json.loads((INPUTS / "inputs.json").read_text())
OUTPUT = ROOT / "output/interoperability/ard-registry"
STAMP = OUTPUT / "tools.json"
IMAGE = "open-agentic-catalog-ard-registry:" + PIN["revision"][:12]


def digest(raw):
    """Identify exact original source/lock bytes without timestamp metadata."""
    return hashlib.sha256(raw).hexdigest()


def tool_identity():
    """Invalidate the installation after changes to acquisition or build inputs."""
    return shared.fingerprint([INPUTS / "inputs.json", INPUTS / "Containerfile", Path(__file__)])


def verify_package(raw):
    """Verify the package-manager tarball before giving it to the container build."""
    algorithm, expected = PIN["pnpm"]["integrity"].split("-", 1)
    if algorithm != "sha512" or base64.b64encode(hashlib.sha512(raw).digest()).decode() != expected:
        raise ValueError("pnpm archive integrity mismatch")


def installed():
    """Require the selected local image and recorded closure; never acquire implicitly."""
    try:
        value = json.loads(STAMP.read_text())
    except (OSError, ValueError) as error:
        raise RuntimeError("ARD registry tools missing; run make ard-registry-tools-install") from error
    if value.get("toolIdentity") != tool_identity() or value.get("revision") != PIN["revision"]:
        raise RuntimeError("ARD registry tools are stale; run make ard-registry-tools-install")
    actual = shared.docker("image", "inspect", "--format={{.Id}}", IMAGE).stdout.strip()
    if actual != value.get("imageId"):
        raise RuntimeError("ARD registry image differs from installed identity")
    if digest((OUTPUT / "licenses.json").read_bytes()) != value.get("licensesDigest"):
        raise RuntimeError("ARD registry dependency notices changed")
    return value


def install():
    """Explicitly download public inputs and build the isolated native peer image."""
    shared.local_daemon()
    OUTPUT.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="install-", dir=OUTPUT) as name:
        directory = Path(name)
        source = directory / "source"
        raw = run(["gh", "api", "--method", "GET", "repos/" + PIN["repository"] + "/tarball/" + PIN["revision"]],
                  binary=True, timeout=90, limit=8 << 20).stdout
        shared.extract_source(raw, source, PIN["sourceDigest"])
        if digest((source / "pnpm-lock.yaml").read_bytes()) != PIN["lockDigest"]:
            raise ValueError("peer pnpm lock changed")
        package = json.loads((source / "package.json").read_text())
        if package["license"] != PIN["license"] or package["engines"]["node"] != ">=20":
            raise ValueError("peer license/runtime declaration changed")
        with urllib.request.urlopen(PIN["pnpm"]["url"], timeout=45) as response:
            archive = response.read((8 << 20) + 1)
        if len(archive) > 8 << 20:
            raise ValueError("pnpm download exceeds size bound")
        verify_package(archive)
        (directory / "pnpm.tgz").write_bytes(archive)
        shutil.copyfile(INPUTS / "Containerfile", directory / "Containerfile")
        node_image = next(line.split()[1] for line in (INPUTS / "Containerfile").read_text().splitlines() if line.startswith("FROM "))
        if shared.docker("image", "inspect", node_image, check=False).code:
            shared.docker("pull", node_image, timeout=300)
        result = shared.docker("build", "-f", str(directory / "Containerfile"), "-t", IMAGE,
                               str(directory), timeout=600, check=False)
        (OUTPUT / "install.log").write_text(result.stdout + result.stderr)
        if result.code:
            raise RuntimeError("peer installation build failed; see " + str(OUTPUT / "install.log"))
        version = shared.docker("run", "--rm", "--pull=never", "--network=none", "--entrypoint=node", IMAGE, "--version").stdout.strip()
        if version != "v" + PIN["node"]:
            raise RuntimeError("native Node version differs")
        versions = json.loads(shared.docker("run", "--rm", "--pull=never", "--network=none", "--entrypoint=cat", IMAGE,
                                            "/peer-tool-versions.json").stdout)
        if versions != {"node": version, "pnpm": PIN["pnpm"]["version"]}:
            raise RuntimeError("native build tool versions differ")
        retained_lock = shared.docker("run", "--rm", "--pull=never", "--network=none", "--entrypoint=cat", IMAGE,
                                     "/peer/pnpm-lock.yaml", binary=True).stdout
        if digest(retained_lock) != PIN["lockDigest"]:
            raise RuntimeError("peer build changed the original frozen lock")
        licenses = shared.docker("run", "--rm", "--pull=never", "--network=none", "--entrypoint=cat", IMAGE,
                                 "/peer-dependency-licenses.json", timeout=45).stdout
        closure = json.loads(licenses)
        if not closure or any(k in ("UNKNOWN", "UNLICENSED") for k in closure):
            raise RuntimeError("review unresolved peer dependency licenses")
        (OUTPUT / "licenses.json").write_text(licenses)
        image_id = shared.docker("image", "inspect", "--format={{.Id}}", IMAGE).stdout.strip()
        STAMP.write_text(json.dumps(dict(toolIdentity=tool_identity(), revision=PIN["revision"], imageId=image_id,
            node=version, pnpm=PIN["pnpm"]["version"], sourceDigest=PIN["sourceDigest"], lockDigest=PIN["lockDigest"],
            license=package["license"], licensesDigest=digest(licenses.encode())), indent=2) + "\n")
    print("Installed unchanged ARD registry " + PIN["revision"] + "; " + image_id)


if __name__ == "__main__":
    install()
