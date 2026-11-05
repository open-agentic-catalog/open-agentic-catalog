"""Explicit source-bound installation and owned offline Plugin containers."""

import io
import tarfile
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import tempfile

from common import run
from runner import ROOT, docker, fingerprint, local_daemon

HERE = Path(__file__).resolve().parent
INPUTS = HERE / "plugins"
OUTPUT = ROOT / "output/interoperability/plugins"
STAMP = OUTPUT / "installation.json"
INVENTORY = """
import hashlib, importlib.metadata as m, json
root = m.distribution('openhands-sdk').locate_file('openhands/sdk')
print(json.dumps({'source': {str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest()
 for p in root.rglob('*') if p.is_file() and '__pycache__' not in p.parts}, 'version': m.version('openhands-sdk'),
 'packages': sorted([{'name': d.metadata['Name'], 'version': d.version,
 'license': d.metadata.get('License-Expression') or d.metadata.get('License') or
 '; '.join(v for v in d.metadata.get_all('Classifier', []) if v.startswith('License ::'))}
 for d in m.distributions()], key=lambda d: d['name'].lower())}))
"""


def identity():
    return fingerprint(
        [
            *(
                INPUTS / p
                for p in (
                    "Containerfile",
                    "requirements.in",
                    "requirements.txt",
                    "constraints.txt",
                    "provenance.json",
                    "LICENSE",
                )
            ),
            HERE / "plugins_tools.py",
            ROOT / "go.mod",
            ROOT / "go.sum",
        ]
    )


class CreationUnconfirmed(RuntimeError):
    """A lost daemon reply cannot establish successful cleanup."""


def create(name, image, mounts=(), entrypoint=None, args=(), network="none"):
    """Record names before calling; deferred signals preserve ownership on interruption."""
    if os.getuid() == 0:
        raise RuntimeError("run Plugin qualification as a non-root developer")
    if network != "none" and not network.startswith("container:catalog-plugins-"):
        raise ValueError("invalid owned Plugin namespace")
    if network != "none":
        peer = network.removeprefix("container:")
        info = json.loads(docker("inspect", peer).stdout)[0]
        assert (
            info["Name"] == "/" + peer and info["HostConfig"]["NetworkMode"] == "none"
        )
        network = "container:" + info["Id"]
    command = [
        "create",
        "--name",
        name,
        "--pull=never",
        "--network=" + network,
        "--read-only",
        "--cap-drop=ALL",
        "--security-opt=no-new-privileges",
        "--pids-limit=256",
        "--memory=2g",
        "--cpus=2",
        "--user",
        f"{os.getuid()}:{os.getgid()}",
        "--tmpfs=/tmp:rw,noexec,nosuid,size=128m",
        "--log-driver=local",
        "--log-opt=max-size=1m",
        "--log-opt=max-file=1",
        "--log-opt=compress=false",
    ]
    for source, target in mounts:
        command += ["--mount", f"type=bind,src={source},dst={target},readonly"]
    if entrypoint:
        command += ["--entrypoint", entrypoint]
    previous = signal.pthread_sigmask(
        signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM, signal.SIGALRM}
    )
    uncertain = False
    try:
        docker(*command, image, *args, timeout=30)
    except (RuntimeError, OSError) as error:
        uncertain = True
        raise CreationUnconfirmed("Plugin container creation unconfirmed") from error
    finally:
        try:
            signal.pthread_sigmask(signal.SIG_SETMASK, previous)
        except (KeyboardInterrupt, TimeoutError) as error:
            if uncertain:
                raise CreationUnconfirmed(
                    "Plugin container creation unconfirmed"
                ) from error
            raise
    info = json.loads(docker("inspect", name, timeout=10).stdout)[0]
    host = info["HostConfig"]
    assert (
        host["NetworkMode"] == network
        and host["ReadonlyRootfs"]
        and not host["Privileged"]
    )
    assert host["Memory"] == 2 * 1024**3 and host["NanoCpus"] == 2 * 10**9
    assert host["PidsLimit"] == 256
    assert set(host["Tmpfs"]) == {"/tmp"} and set(host["Tmpfs"]["/tmp"].split(",")) == {
        "rw",
        "noexec",
        "nosuid",
        "size=128m",
    }, "unexpected temporary filesystem limits"
    assert host["CapDrop"] == ["ALL"] and "no-new-privileges" in host["SecurityOpt"]
    assert (
        not host["PortBindings"]
        and info["Config"]["User"] == f"{os.getuid()}:{os.getgid()}"
    )
    assert {
        (m["Source"], m["Destination"]) for m in info["Mounts"] if m["Type"] == "bind"
    } == {(str(s), t) for s, t in mounts}
    assert all(not m["RW"] for m in info["Mounts"] if m["Type"] == "bind")


def remove(names):
    failed = False
    for name in reversed(names):
        try:
            result = docker("rm", "--force", name, timeout=20, check=False)
            if result.code and "No such container" not in result.stderr:
                failed = True
            remaining = docker(
                "ps", "-aq", "--filter", "name=^/" + name + "$", timeout=10
            )
            failed = failed or bool(remaining.stdout.strip())
        except (RuntimeError, OSError):
            failed = True
    if failed:
        raise RuntimeError("owned Plugin container cleanup failed")


def inventory_matches(inventory, pins):
    expected = {
        p.removeprefix("openhands-sdk/openhands/sdk/"): digest
        for p, digest in pins["files"].items()
        if p.startswith("openhands-sdk/openhands/sdk/")
        and (p.endswith((".py", ".j2", ".json", "/py.typed")))
    }
    if inventory["source"] != expected or inventory["version"] != pins["version"]:
        raise ValueError("installed reference source/version differs from pin")


def install():
    if local_daemon() != "amd64":
        raise ValueError("Plugin qualification is locked for Linux amd64")
    pins = json.loads((INPUTS / "provenance.json").read_text())
    version = next(
        line.split()[1]
        for line in (ROOT / "go.mod").read_text().splitlines()
        if line.startswith("go ")
    )
    environment = dict(
        os.environ, GOTOOLCHAIN="go" + version, GOENV="off", GOWORK="off", GOFLAGS=""
    )
    goroot = run(["go", "env", "GOROOT"], env=environment, timeout=120).stdout.strip()
    go = str(Path(goroot) / "bin/go")
    run([go, "mod", "download"], env=environment, timeout=300)
    OUTPUT.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="install-", dir=OUTPUT) as scratch:
        directory = Path(scratch)
        raw = run(
            ["gh", "api", f"repos/{pins['repository']}/tarball/{pins['revision']}"],
            timeout=120,
            limit=16 * 1024 * 1024,
            binary=True,
        ).stdout
        unpack_source(raw, pins, directory / "source")
        assert (directory / "source/LICENSE").read_bytes() == (
            INPUTS / "LICENSE"
        ).read_bytes()
        for name in ("Containerfile", "requirements.txt"):
            shutil.copyfile(INPUTS / name, directory / name)
        tag = "open-agentic-catalog-plugins:" + identity()[:20]
        result = docker(
            "build",
            "--progress=plain",
            "-t",
            tag,
            str(directory),
            "-f",
            str(directory / "Containerfile"),
            timeout=900,
            limit=8 * 1024 * 1024,
            check=False,
        )
        (OUTPUT / "install.log").write_text(result.stdout + result.stderr)
        if result.code:
            raise RuntimeError(
                "Plugin installation failed; see output/interoperability/plugins/install.log"
            )
        image = docker(
            "image", "inspect", tag, "--format", "{{.Id}}", timeout=10
        ).stdout.strip()
        name = "catalog-plugins-install-" + directory.name
        try:
            create(name, image, args=("-c", INVENTORY))
            result = docker("start", "--attach", name, timeout=90)
            state = json.loads(docker("inspect", name).stdout)[0]["State"]
            assert not state["Running"] and state["ExitCode"] == 0
            inventory = json.loads(result.stdout)
            inventory_matches(inventory, pins)
        finally:
            remove([name])
        (OUTPUT / "dependencies.json").write_text(result.stdout)
        stamp = dict(
            identity=identity(),
            image=image,
            go=go,
            sources=pins,
            image_bytes=int(
                docker("image", "inspect", image, "--format", "{{.Size}}").stdout
            ),
            inventory_sha256=hashlib.sha256(result.stdout.encode()).hexdigest(),
        )
        path = directory / "installation.json"
        path.write_text(json.dumps(stamp, indent=2) + "\n")
        path.replace(STAMP)
    print("Installed unchanged openhands-sdk:", image)


def installed():
    stamp = json.loads(STAMP.read_text())
    if stamp["identity"] != identity() or not stamp["image"].startswith("sha256:"):
        raise ValueError("run make plugin-tools-install: missing or stale tools")
    raw = (OUTPUT / "dependencies.json").read_bytes()
    assert hashlib.sha256(raw).hexdigest() == stamp["inventory_sha256"]
    inventory_matches(
        json.loads(raw), json.loads((INPUTS / "provenance.json").read_text())
    )
    assert (
        docker("image", "inspect", stamp["image"], "--format", "{{.Id}}").stdout.strip()
        == stamp["image"]
    )
    return stamp


def unpack_source(raw, pins, destination):
    """Extract only exact reviewed regular files; never use tar extraction helpers."""
    found = set()
    with tarfile.open(fileobj=io.BytesIO(raw), mode="r:gz") as archive:
        for member in archive:
            path = "/".join(member.name.split("/")[1:])
            if path not in pins["files"]:
                continue
            if (
                path in found
                or not member.isfile()
                or member.size > 8 * 1024 * 1024
                or ".." in path.split("/")
                or path.startswith("/")
            ):
                raise ValueError("invalid pinned source member")
            body = archive.extractfile(member).read(8 * 1024 * 1024 + 1)
            if hashlib.sha256(body).hexdigest() != pins["files"][path]:
                raise ValueError("pinned source checksum mismatch")
            target = destination / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(body)
            found.add(path)
    if found != set(pins["files"]):
        raise ValueError("incomplete pinned source")
