"""Install immutable, independent ANS tools; execution never acquires prerequisites."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import sys
import tempfile
import uuid

from agntcy_tools import command, fingerprints
import runner as shared

ASSETS = shared.HERE / "ans"
TOOLS = shared.ROOT / ".tools/ans"


def lock():
    return json.loads((ASSETS / "lock.json").read_text())


def digest_tree(directory):
    if directory.is_symlink() or not directory.is_dir():
        raise ValueError("ANS input must be a real directory")
    digest = hashlib.sha256()
    for path in sorted(directory.rglob("*"), key=lambda p: p.relative_to(directory).as_posix()):
        if path.is_symlink() or not (path.is_file() or path.is_dir()):
            raise ValueError("ANS input contains a link or special file")
        if path.is_file():
            digest.update(path.relative_to(directory).as_posix().encode() + b"\0" + hashlib.sha256(path.read_bytes()).digest())
    return digest.hexdigest()


def image_tag():
    return "open-agentic-catalog:ans-" + lock()["revision"][:12]


def inspect():
    if platform.system() != "Linux" or platform.machine() not in ("x86_64", "amd64"):
        raise ValueError("ANS qualification supports Linux AMD64 only")
    shared.local_daemon()
    pins = lock()
    source = TOOLS / "source"
    if digest_tree(source) != pins["source_sha256"]:
        raise ValueError("ANS source differs from its exact pin")
    if (source / "LICENSE").read_bytes() != (ASSETS / "LICENSE").read_bytes():
        raise ValueError("ANS source license differs")
    image = json.loads(command("docker", "image", "inspect", image_tag()).stdout)[0]
    if (image["Os"], image["Architecture"]) != ("linux", "amd64") or image["Config"]["Labels"]["org.opencontainers.image.revision"] != pins["revision"]:
        raise ValueError("ANS image identity differs")
    binaries = {}
    for name in pins["components"]:
        binary = TOOLS / "artifacts" / ("ans-" + name)
        version = command(str(binary), "--version").stdout.strip()
        if pins["revision"] not in version or "qualification" not in version:
            raise ValueError("ANS binary build stamp differs")
        binaries[name] = {"sha256": hashlib.sha256(binary.read_bytes()).hexdigest(), "version": version}
    return {"revision": pins["revision"], "source_sha256": pins["source_sha256"],
            "inputs_sha256": shared.fingerprint((ASSETS / "Containerfile", ASSETS / "lock.json", ASSETS / "LICENSE", shared.HERE / "ans_tools.py")),
            "artifacts_sha256": digest_tree(TOOLS / "artifacts"), "binaries": binaries,
            "images": {"native": {"reference": image_tag(), "id": image["Id"]}}}


def install():
    shared.local_daemon()
    TOOLS.mkdir(parents=True, exist_ok=True)
    if TOOLS.is_symlink():
        raise ValueError("ANS tools must not be a symlink")
    pins, source = lock(), TOOLS / "source"
    if not source.exists():
        raw = command("gh", "api", "repos/" + pins["repository"] + "/tarball/" + pins["revision"], binary=True, limit=8 << 20, timeout=180).stdout
        shared.extract_source(raw, source, pins["source_sha256"])
    if digest_tree(source) != pins["source_sha256"]:
        raise ValueError("ANS source differs from its pin")
    command("docker", "pull", "--platform=linux/amd64", pins["compiler"], timeout=600)
    modules = TOOLS / "modules"
    modules.mkdir(exist_ok=True)
    if modules.is_symlink() or shutil.disk_usage(TOOLS).free < 8 << 30:
        raise ValueError("ANS installation requires real disk inputs and at least 8 GiB free")
    name = "ans-acquire-" + uuid.uuid4().hex[:16]
    with tempfile.TemporaryDirectory(prefix="acquire-", dir=TOOLS) as temporary:
        acquisition = Path(temporary)
        for filename in ("go.mod", "go.sum"):
            shutil.copyfile(source / filename, acquisition / filename)
        try:
            command("docker", "run", "--rm", "--name", name, "--network=host", "--user", str(os.getuid()) + ":" + str(os.getgid()),
                    "--mount", "type=bind,source=" + str(acquisition) + ",target=/src",
                    "--mount", "type=bind,source=" + str(modules) + ",target=/modules", "--workdir=/src",
                    "--env=GOMODCACHE=/modules", "--env=GOTOOLCHAIN=local", "--env=GOCACHE=/modules/.build",
                    "--entrypoint=go", pins["compiler"], "mod", "download", "all", timeout=600)
        finally:
            removed = command("docker", "rm", "-f", name, check=False)
            if removed.code and "No such container" not in removed.stderr:
                raise RuntimeError("ANS acquisition container cleanup failed: " + name)
    with tempfile.TemporaryDirectory(prefix="build-", dir=TOOLS) as temporary:
        context = Path(temporary)
        shutil.copytree(source, context / "source")
        shutil.copytree(modules, context / "modules")
        shutil.copyfile(ASSETS / "Containerfile", context / "Containerfile")
        args = ["docker", "build", "--platform=linux/amd64", "--pull=false", "--file", str(context / "Containerfile"), "--build-arg", "REVISION=" + pins["revision"]]
        command(*args, "--network=none", "--target=artifacts", "--output=type=local,dest=" + str(TOOLS / "artifacts"), str(context), timeout=1200)
        command(*args, "--network=none", "--target=runtime", "--label", "org.opencontainers.image.revision=" + pins["revision"], "--tag", image_tag(), str(context), timeout=300)
    identity = inspect()
    (TOOLS / "installation.json").write_text(json.dumps(identity, indent=2) + "\n")
    print(json.dumps(identity))


def check():
    identity = inspect()
    if identity != json.loads((TOOLS / "installation.json").read_text()):
        raise ValueError("ANS installation changed; run make ans-tools-install")
    print(json.dumps(identity))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "check", "fingerprint"))
    try:
        if not __debug__:
            raise ValueError("ANS qualification requires enabled assertions")
        {"install": install, "check": check, "fingerprint": fingerprints}[parser.parse_args().action]()
    except (OSError, RuntimeError, ValueError, KeyError) as error:
        print("ANS tools: " + str(error), file=sys.stderr)
        sys.exit(1)
