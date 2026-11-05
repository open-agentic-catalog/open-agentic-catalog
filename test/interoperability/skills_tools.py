"""Explicit source-bound installation and owned offline Skill containers."""

import base64
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
INPUTS = HERE / "skills"
OUTPUT = ROOT / "output/interoperability/skills"
STAMP = OUTPUT / "installation.json"
INVENTORY = """
import hashlib, importlib.metadata as m, json
from pathlib import Path
import skills_ref
root = Path(skills_ref.__file__).parent
print(json.dumps({'source': {str(p.relative_to(root)): hashlib.sha256(p.read_bytes()).hexdigest()
 for p in root.rglob('*.py')}, 'version': m.version('skills-ref'),
 'packages': sorted([{'name': d.metadata['Name'], 'version': d.version,
 'license': d.metadata.get('License-Expression') or d.metadata.get('License') or
 '; '.join(v for v in d.metadata.get_all('Classifier', []) if v.startswith('License ::'))}
 for d in m.distributions()], key=lambda d: d['name'].lower())}))
"""


def identity():
    return fingerprint([*(INPUTS / p for p in ("Containerfile", "requirements.in", "requirements.txt", "provenance.json", "LICENSE")),
                        HERE / "skills_tools.py", ROOT / "go.mod", ROOT / "go.sum"])


class CreationUnconfirmed(RuntimeError):
    """A lost daemon reply cannot establish successful cleanup."""


def create(name, image, mounts=(), entrypoint=None, args=()):
    """Record names before calling; deferred signals preserve ownership on interruption."""
    if os.getuid() == 0:
        raise RuntimeError("run Skill qualification as a non-root developer")
    command = ["create", "--name", name, "--pull=never", "--network=none", "--read-only",
               "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=128", "--memory=512m",
               "--cpus=2", "--user", f"{os.getuid()}:{os.getgid()}", "--tmpfs=/tmp:rw,noexec,nosuid,size=32m",
               "--log-driver=local", "--log-opt=max-size=1m", "--log-opt=max-file=1", "--log-opt=compress=false"]
    for source, target in mounts:
        command += ["--mount", f"type=bind,src={source},dst={target},readonly"]
    if entrypoint:
        command += ["--entrypoint", entrypoint]
    previous = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM, signal.SIGALRM})
    uncertain = False
    try:
        docker(*command, image, *args, timeout=30)
    except (RuntimeError, OSError) as error:
        uncertain = True
        raise CreationUnconfirmed("Skill container creation unconfirmed") from error
    finally:
        try:
            signal.pthread_sigmask(signal.SIG_SETMASK, previous)
        except (KeyboardInterrupt, TimeoutError) as error:
            if uncertain:
                raise CreationUnconfirmed("Skill container creation unconfirmed") from error
            raise
    info = json.loads(docker("inspect", name, timeout=10).stdout)[0]
    host = info["HostConfig"]
    assert host["NetworkMode"] == "none" and host["ReadonlyRootfs"] and not host["Privileged"]
    assert host["CapDrop"] == ["ALL"] and "no-new-privileges" in host["SecurityOpt"]
    assert not host["PortBindings"] and info["Config"]["User"] == f"{os.getuid()}:{os.getgid()}"
    assert {(m["Source"], m["Destination"]) for m in info["Mounts"] if m["Type"] == "bind"} == {(str(s), t) for s, t in mounts}
    assert all(not m["RW"] for m in info["Mounts"] if m["Type"] == "bind")


def remove(names):
    failed = False
    for name in reversed(names):
        try:
            result = docker("rm", "--force", name, timeout=20, check=False)
            if result.code and "No such container" not in result.stderr:
                failed = True
            remaining = docker("ps", "-aq", "--filter", "name=^/" + name + "$", timeout=10)
            failed = failed or bool(remaining.stdout.strip())
        except (RuntimeError, OSError):
            failed = True
    if failed:
        raise RuntimeError("owned Skill container cleanup failed")


def inventory_matches(inventory, pins):
    expected = {p.removeprefix("skills-ref/src/skills_ref/"): digest for p, digest in pins["files"].items()
                if p.startswith("skills-ref/src/skills_ref/")}
    if inventory["source"] != expected or inventory["version"] != pins["version"]:
        raise ValueError("installed reference source/version differs from pin")


def install():
    local_daemon()
    pins = json.loads((INPUTS / "provenance.json").read_text())
    version = next(line.split()[1] for line in (ROOT / "go.mod").read_text().splitlines() if line.startswith("go "))
    environment = dict(os.environ, GOTOOLCHAIN="go" + version, GOENV="off", GOWORK="off", GOFLAGS="")
    goroot = run(["go", "env", "GOROOT"], env=environment, timeout=120).stdout.strip()
    go = str(Path(goroot) / "bin/go")
    run([go, "mod", "download"], env=environment, timeout=300)
    OUTPUT.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="install-", dir=OUTPUT) as scratch:
        directory = Path(scratch)
        for path, digest in pins["files"].items():
            result = run(["gh", "api", f"repos/{pins['repository']}/contents/{path}?ref={pins['revision']}"], timeout=60, limit=2*1024*1024)
            raw = base64.b64decode(json.loads(result.stdout)["content"])
            if hashlib.sha256(raw).hexdigest() != digest:
                raise ValueError("reference source checksum mismatch")
            target = directory / "source" / path
            target.parent.mkdir(parents=True, exist_ok=True)
            target.write_bytes(raw)
        assert (directory / "source/LICENSE").read_bytes() == (INPUTS / "LICENSE").read_bytes()
        for name in ("Containerfile", "requirements.txt"):
            shutil.copyfile(INPUTS / name, directory / name)
        tag = "open-agentic-catalog-skills:" + identity()[:20]
        result = docker("build", "--progress=plain", "-t", tag, str(directory), "-f", str(directory / "Containerfile"), timeout=600, limit=8*1024*1024, check=False)
        (OUTPUT / "install.log").write_text(result.stdout + result.stderr)
        if result.code:
            raise RuntimeError("Skill installation failed; see output/interoperability/skills/install.log")
        image = docker("image", "inspect", tag, "--format", "{{.Id}}", timeout=10).stdout.strip()
        name = "catalog-skills-install-" + directory.name
        try:
            create(name, image, args=("-c", INVENTORY))
            result = docker("start", "--attach", name, timeout=20)
            state = json.loads(docker("inspect", name).stdout)[0]["State"]
            assert not state["Running"] and state["ExitCode"] == 0
            inventory = json.loads(result.stdout)
            inventory_matches(inventory, pins)
        finally:
            remove([name])
        (OUTPUT / "dependencies.json").write_text(result.stdout)
        stamp = dict(identity=identity(), image=image, go=go, sources=pins,
                     inventory_sha256=hashlib.sha256(result.stdout.encode()).hexdigest())
        path = directory / "installation.json"
        path.write_text(json.dumps(stamp, indent=2) + "\n")
        path.replace(STAMP)
    print("Installed unchanged skills-ref:", image)


def installed():
    stamp = json.loads(STAMP.read_text())
    if stamp["identity"] != identity() or not stamp["image"].startswith("sha256:"):
        raise ValueError("run make skill-tools-install: missing or stale tools")
    raw = (OUTPUT / "dependencies.json").read_bytes()
    assert hashlib.sha256(raw).hexdigest() == stamp["inventory_sha256"]
    inventory_matches(json.loads(raw), json.loads((INPUTS / "provenance.json").read_text()))
    assert docker("image", "inspect", stamp["image"], "--format", "{{.Id}}").stdout.strip() == stamp["image"]
    return stamp
