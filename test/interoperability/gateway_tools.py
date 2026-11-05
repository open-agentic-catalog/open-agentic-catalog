"""Acquire and verify exact test-only Gateway inputs without deploying a service."""

import argparse
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import platform
import sys
import tarfile
import tempfile

from agntcy_tools import environment, fingerprints, image_identity
from common import run
import runner as shared

ASSETS = shared.HERE / "gateway"
TOOLS = shared.ROOT / ".tools/gateway"
LINKS = {
    "docs/" + name + ".md": "../" + target
    for name, target in (
        ("CODE_OF_CONDUCT", "CODE_OF_CONDUCT.md"),
        ("CONTRIBUTING", "CONTRIBUTING.md"),
        ("LICENSE", "LICENSE"),
        ("NOTICE", "NOTICE"),
        ("SECURITY", "SECURITY.md"),
    )
}
NATIVE_ASSETS = [
    "scripts/init-mongodb-ce.py",
    "docker/nginx_rev_proxy_http_only.conf",
    "docker/nginx_rev_proxy_http_and_https.conf",
    "auth_server/oauth2_providers.yml",
    "pyproject.toml",
]


def command(*args, **kwargs):
    return run(list(args), env=environment(), **kwargs)


def read_lock():
    return json.loads((ASSETS / "lock.json").read_text())


def tree_digest(source):
    """Include unexpected files and reviewed documentation links in source identity."""
    if source.is_symlink() or not source.is_dir():
        raise ValueError("Gateway source must be a real directory")
    digest = hashlib.sha256()
    for path in sorted(
        source.rglob("*"), key=lambda item: item.relative_to(source).as_posix()
    ):
        name = path.relative_to(source).as_posix()
        if path.is_symlink():
            if LINKS.get(name) != os.readlink(path):
                raise ValueError("unreviewed Gateway source link")
            digest.update(
                name.encode() + b"\0link\0" + os.readlink(path).encode() + b"\0"
            )
        elif path.is_file():
            digest.update(
                name.encode() + b"\0" + hashlib.sha256(path.read_bytes()).digest()
            )
        elif not path.is_dir():
            raise ValueError("unsupported Gateway source entry")
    return digest.hexdigest()


def extract(raw, destination, expected):
    """Gateway alone has five internal documentation links and a larger source bundle."""
    if len(raw) > 48 * 1024 * 1024:
        raise ValueError("Gateway archive exceeds the acquisition bound")
    with tarfile.open(fileobj=io.BytesIO(raw), mode="r:gz") as archive:
        members, expanded = [], 0
        for item in archive:
            members.append(item)
            expanded += item.size
            if len(members) > 3000 or expanded > 80 * 1024 * 1024:
                raise ValueError(
                    "Gateway expanded source exceeds the acquisition bound"
                )
        if not members:
            raise ValueError("Gateway archive is empty")
        prefixes, names = set(), set()
        for item in members:
            path = PurePosixPath(item.name)
            if (
                path.is_absolute()
                or ".." in path.parts
                or "\\" in item.name
                or not path.parts
            ):
                raise ValueError("unsafe Gateway archive path")
            prefixes.add(path.parts[0])
            name = PurePosixPath(*path.parts[1:]).as_posix()
            if name in names or item.size > 8 * 1024 * 1024:
                raise ValueError("duplicate or oversized Gateway source entry")
            names.add(name)
            if item.issym():
                if LINKS.get(name) != item.linkname:
                    raise ValueError("unreviewed Gateway archive link")
            elif not (item.isdir() or item.isfile()):
                raise ValueError("unsupported Gateway archive entry")
        if len(prefixes) != 1:
            raise ValueError("Gateway archive has multiple roots")
        archive.extractall(destination, filter="data")
        source = destination / next(iter(prefixes))
        if tree_digest(source) != expected:
            raise ValueError("Gateway source differs from the exact pin")
        return source


# The container executes metadata inspection only: no entrypoint, model, service or network.
INVENTORY = """
import hashlib, importlib.metadata as m, json, pathlib, subprocess
packages = sorted([{'name':d.metadata['Name'],'version':d.version,
 'license':d.metadata.get('License-Expression') or d.metadata.get('License'),
 'requires':d.metadata.get_all('Requires-Dist',[]),
 'license_files':{str(p):hashlib.sha256(d.locate_file(p).read_bytes()).hexdigest()
  for p in (d.files or []) if ('license' in p.name.lower() or 'copying' in p.name.lower()) and d.locate_file(p).is_file()},
 'classifiers':d.metadata.get_all('Classifier',[])} for d in m.distributions()],key=lambda d:d['name'].lower())
source = {str(p.relative_to('/app')):hashlib.sha256(p.read_bytes()).hexdigest()
 for root in ['registry','auth_server'] for p in pathlib.Path('/app',root).rglob('*.py')}
print(json.dumps({'packages':packages,'source':source,
 'assets':{p:hashlib.sha256(pathlib.Path('/app',p).read_bytes()).hexdigest() for p in ASSETS},
 'entrypoint':hashlib.sha256(pathlib.Path('/app/registry-entrypoint.sh').read_bytes()).hexdigest(),
 'os_packages':subprocess.check_output(['dpkg-query','-W','-f=${Package}=${Version}\\n'],text=True),
 'os_licenses':{str(p):hashlib.sha256(p.read_bytes()).hexdigest() for p in pathlib.Path('/usr/share/doc').glob('*/copyright')}}))
"""


def inspect():
    if platform.system() != "Linux" or platform.machine() not in ("x86_64", "amd64"):
        raise ValueError("Gateway qualification supports Linux AMD64 only")
    shared.local_daemon()
    lock, source = read_lock(), TOOLS / "source"
    if tree_digest(source) != lock["source_sha256"]:
        raise ValueError("Gateway source changed; restore the pinned inputs")
    images = {
        name: image_identity(reference) for name, reference in lock["images"].items()
    }
    auth_reference = "open-agentic-catalog:gateway-auth-" + lock["revision"][:12]
    auth = json.loads(command("docker", "image", "inspect", auth_reference).stdout)[0]
    if auth["Os"] != "linux" or auth["Architecture"] != "amd64":
        raise ValueError("Gateway auth image platform differs")
    auth_bytes = json.loads(
        command(
            "docker",
            "run",
            "--rm",
            "--network=none",
            "--read-only",
            "--entrypoint",
            "/app/.venv/bin/python",
            auth_reference,
            "-B",
            "-c",
            "import hashlib,json,pathlib; print(json.dumps({str(p.relative_to('/app')):hashlib.sha256(p.read_bytes()).hexdigest() for p in [*pathlib.Path('/app').glob('*.py'),pathlib.Path('/app/auth-entrypoint.sh'),*pathlib.Path('/app/providers').rglob('*.py'),*pathlib.Path('/app/observability').rglob('*.py')]}))",
        ).stdout
    )
    expected_auth = {
        str(p.relative_to(source / "auth_server")): hashlib.sha256(
            p.read_bytes()
        ).hexdigest()
        for p in (source / "auth_server").rglob("*.py")
    }
    expected_auth["auth-entrypoint.sh"] = hashlib.sha256(
        (source / "docker/auth-entrypoint.sh").read_bytes()
    ).hexdigest()
    if auth_bytes != expected_auth:
        raise ValueError("Gateway auth image source differs")
    images["auth"] = {"id": auth["Id"], "reference": auth_reference}
    metadata = json.loads(
        command("docker", "image", "inspect", lock["images"]["registry"]).stdout
    )[0]
    if (
        metadata["Config"]["Labels"]["org.opencontainers.image.revision"]
        != lock["revision"]
    ):
        raise ValueError("Gateway image source revision differs")
    inventory = json.loads(
        command(
            "docker",
            "run",
            "--rm",
            "--network=none",
            "--read-only",
            "--entrypoint",
            "/app/.venv/bin/python",
            lock["images"]["registry"],
            "-B",
            "-c",
            INVENTORY.replace(
                "ASSETS",
                repr(
                    NATIVE_ASSETS
                    + [
                        str(p.relative_to(source))
                        for p in sorted((source / "scripts").glob("*.json"))
                    ]
                ),
            ),
            timeout=30,
        ).stdout
    )
    expected = {
        str(path.relative_to(source)): hashlib.sha256(path.read_bytes()).hexdigest()
        for root in ("registry", "auth_server")
        for path in (source / root).rglob("*.py")
    }
    if (
        inventory["source"] != expected
        or inventory["assets"]
        != {
            p: hashlib.sha256((source / p).read_bytes()).hexdigest()
            for p in inventory["assets"]
        }
        or inventory["entrypoint"]
        != hashlib.sha256(
            (source / "docker/registry-entrypoint.sh").read_bytes()
        ).hexdigest()
    ):
        raise ValueError(
            "Gateway image does not contain the exact native source and entrypoint"
        )
    if (
        not any(p["name"] == "jsonschema" for p in inventory["packages"])
        or len(inventory["packages"]) < 100
    ):
        raise ValueError("Gateway installed dependency closure is incomplete")
    closure = hashlib.sha256(json.dumps(inventory, sort_keys=True).encode()).hexdigest()
    return {
        "revision": lock["revision"],
        "source_sha256": lock["source_sha256"],
        "images": images,
        "closure_sha256": closure,
        "python_packages": len(inventory["packages"]),
        "auth_build_sha256": hashlib.sha256(
            (ASSETS / "Containerfile.auth").read_bytes()
        ).hexdigest(),
    }, inventory


def install():
    if platform.system() != "Linux" or platform.machine() not in ("x86_64", "amd64"):
        raise ValueError("Gateway qualification supports Linux AMD64 only")
    shared.local_daemon()
    TOOLS.mkdir(parents=True, exist_ok=True)
    if TOOLS.is_symlink():
        raise ValueError("Gateway tools directory must not be a symlink")
    lock = read_lock()
    if not (TOOLS / "source").exists():
        raw = command(
            "gh",
            "api",
            "repos/" + lock["repository"] + "/tarball/" + lock["revision"],
            binary=True,
            limit=48 * 1024 * 1024,
            timeout=180,
        ).stdout
        with tempfile.TemporaryDirectory(prefix="acquire-", dir=TOOLS) as temporary:
            extract(raw, Path(temporary), lock["source_sha256"]).rename(
                TOOLS / "source"
            )
    if tree_digest(TOOLS / "source") != lock["source_sha256"]:
        raise ValueError("Gateway installed source differs")
    for reference in lock["images"].values():
        command("docker", "pull", "--platform=linux/amd64", reference, timeout=600)
    command(
        "docker",
        "build",
        "--pull=false",
        "--network=none",
        "--file",
        str(ASSETS / "Containerfile.auth"),
        "--tag",
        "open-agentic-catalog:gateway-auth-" + lock["revision"][:12],
        str(TOOLS / "source"),
        timeout=120,
    )
    identity, inventory = inspect()
    (TOOLS / "closure.json").write_text(json.dumps(inventory, indent=2) + "\n")
    (TOOLS / "installation.json").write_text(json.dumps(identity, indent=2) + "\n")
    print(json.dumps(identity))


def check():
    actual, inventory = inspect()
    if actual != json.loads(
        (TOOLS / "installation.json").read_text()
    ) or inventory != json.loads((TOOLS / "closure.json").read_text()):
        raise ValueError("Gateway installation changed; run make gateway-tools-install")
    print(json.dumps(actual))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "check", "fingerprint"))
    args = parser.parse_args()
    try:
        if not __debug__:
            raise ValueError("Gateway tools require enabled Python assertions")
        {"install": install, "check": check, "fingerprint": fingerprints}[args.action]()
    except (
        RuntimeError,
        OSError,
        ValueError,
        KeyError,
        TypeError,
        tarfile.TarError,
    ) as error:
        print("Gateway tools: " + str(error), file=sys.stderr)
        sys.exit(1)
