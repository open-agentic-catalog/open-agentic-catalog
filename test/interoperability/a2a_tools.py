"""Explicit locked installation and bounded container ownership for A2A tools."""

import base64
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import tempfile
import uuid

from common import run
from runner import ROOT, docker, fingerprint, local_daemon

HERE = Path(__file__).resolve().parent
INPUTS = HERE / "a2a"
OUTPUT = ROOT / "output/interoperability/a2a"
STAMP = OUTPUT / "installation.json"
INVENTORY = """
import hashlib, importlib.metadata as m, json
from pathlib import Path
import a2a
root = Path(a2a.__file__).parent
h = hashlib.sha256()
for path in sorted(root.rglob('*')):
    if path.is_file() and '__pycache__' not in path.parts:
        name = 'a2a/' + str(path.relative_to(root))
        h.update(name.encode() + b'\\0' + hashlib.sha256(path.read_bytes()).digest())
print(json.dumps({'sdk_source_sha256': h.hexdigest(),
 'executor_sha256': hashlib.sha256(Path('/tools/agent_executor.py').read_bytes()).hexdigest(),
 'packages': sorted([{'name': d.metadata['Name'], 'version': d.version,
 'license': d.metadata.get('License-Expression') or d.metadata.get('License') or
 '; '.join(v for v in d.metadata.get_all('Classifier', []) if v.startswith('License ::'))}
 for d in m.distributions()], key=lambda d: d['name'].lower())}))
"""


def identity():
    return fingerprint([*(INPUTS / name for name in ("Containerfile", "requirements.in", "requirements.txt",
                        "provenance.json", "SDK-LICENSE", "Sample-LICENSE")), HERE / "a2a_tools.py"])


def pins():
    return json.loads((INPUTS / "provenance.json").read_text())


def safe_args(name):
    if os.getuid() == 0:
        raise RuntimeError("run A2A qualification as a non-root developer")
    args = ["--name", name, "--pull=never", "--read-only", "--cap-drop=ALL",
            "--security-opt=no-new-privileges", "--pids-limit=128", "--memory=512m", "--cpus=2",
            "--tmpfs=/tmp:rw,noexec,nosuid,size=32m", "--user", f"{os.getuid()}:{os.getgid()}",
            "--log-driver=local", "--log-opt=max-size=1m", "--log-opt=max-file=1", "--log-opt=compress=false"]
    for key in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"):
        args += ["--env", key + "="]
    return args


class CreationUnconfirmed(RuntimeError):
    """A timed-out daemon request might create the named container later."""


def create(name, *args):
    """Defer signals through creation so cleanup knows which names it owns."""
    old = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM, signal.SIGALRM})
    uncertain = False
    try:
        return docker("create", *safe_args(name), *args, timeout=30)
    except (RuntimeError, OSError) as error:
        uncertain = True
        raise CreationUnconfirmed("A2A container creation unconfirmed: " + name) from error
    finally:
        try:
            signal.pthread_sigmask(signal.SIG_SETMASK, old)
        except (KeyboardInterrupt, TimeoutError) as error:
            if uncertain:
                raise CreationUnconfirmed("A2A container creation unconfirmed: " + name) from error
            raise


def remove(names):
    failures = []
    for name in reversed(names):
        try:
            result = docker("rm", "-f", name, check=False, timeout=15)
            if result.code and "No such container" not in result.stderr:
                failures.append(name)
        except (RuntimeError, OSError):
            failures.append(name)
    if failures:
        raise RuntimeError("owned A2A container cleanup failed: " + ", ".join(failures))


def github_file(repository, revision, path):
    data = json.loads(run(["gh", "api", f"repos/{repository}/contents/{path}?ref={revision}"], timeout=60, limit=1024*1024).stdout)
    return base64.b64decode(data["content"], validate=False)


def install():
    """Download locked tools; compare every installed SDK source file to its reviewed tree."""
    local_daemon()
    expected = pins()
    sdk, sample = expected["sdk"], expected["executor"]
    tag = json.loads(run(["gh", "api", "repos/" + sdk["repository"] + "/git/ref/tags/v" + sdk["version"]]).stdout)
    if tag["object"]["type"] != "commit" or tag["object"]["sha"] != sdk["revision"]:
        raise RuntimeError("A2A SDK release/source mismatch")
    executor = github_file(sample["repository"], sample["revision"], sample["path"])
    if hashlib.sha256(executor).hexdigest() != sample["sha256"]:
        raise RuntimeError("A2A executor source drift")
    for source, filename in ((sdk, "SDK-LICENSE"), (sample, "Sample-LICENSE")):
        if github_file(source["repository"], source["revision"], "LICENSE") != (INPUTS / filename).read_bytes():
            raise RuntimeError("A2A upstream license drift")
    OUTPUT.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="install-", dir=OUTPUT) as temporary:
        directory = Path(temporary)
        for filename in ("Containerfile", "requirements.txt", "SDK-LICENSE", "Sample-LICENSE"):
            shutil.copyfile(INPUTS / filename, directory / filename)
        (directory / "agent_executor.py").write_bytes(executor)
        image_tag = "open-agentic-catalog-a2a:" + identity()[:20]
        result = docker("build", "--progress=plain", "-t", image_tag, "-f", str(directory / "Containerfile"), str(directory), timeout=900, limit=16*1024*1024, check=False)
        (OUTPUT / "install.log").write_text(result.stdout + result.stderr)
        if result.code:
            raise RuntimeError("A2A installation failed; see output/interoperability/a2a/install.log")
        image = docker("image", "inspect", image_tag, "--format", "{{.Id}}").stdout.strip()
        name = "catalog-a2a-install-" + uuid.uuid4().hex[:12]
        try:
            create(name, "--network=none", image, "-c", INVENTORY)
            raw = docker("start", "--attach", name, timeout=30, limit=1024*1024).stdout
        finally:
            remove([name])
        inventory = json.loads(raw)
        if inventory["sdk_source_sha256"] != sdk["source_sha256"] or inventory["executor_sha256"] != sample["sha256"]:
            raise RuntimeError("installed A2A source differs from reviewed source")
        if not any(p["name"] == "a2a-sdk" and p["version"] == sdk["version"] for p in inventory["packages"]):
            raise RuntimeError("installed A2A SDK version mismatch")
        (OUTPUT / "dependencies.json").write_text(raw)
        stamp = {"identity": identity(), "image": image, "sources": expected,
                 "inventory_sha256": hashlib.sha256(raw.encode()).hexdigest()}
        (directory / "installation.json").write_text(json.dumps(stamp, indent=2) + "\n")
        (directory / "installation.json").replace(STAMP)
    print("A2A tools installed: " + image)


def installed():
    try:
        stamp = json.loads(STAMP.read_text())
        if stamp["identity"] != identity() or not stamp["image"].startswith("sha256:"):
            raise ValueError("stale stamp")
        if hashlib.sha256((OUTPUT / "dependencies.json").read_bytes()).hexdigest() != stamp["inventory_sha256"]:
            raise ValueError("inventory drift")
        if docker("image", "inspect", stamp["image"], "--format", "{{.Id}}").stdout.strip() != stamp["image"]:
            raise ValueError("image drift")
    except (OSError, KeyError, ValueError, RuntimeError) as error:
        raise RuntimeError("Run make a2a-tools-install first (missing or stale tools)") from error
    return stamp
