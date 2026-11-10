"""Explicit acquisition and immutable identity for execution-only MCP tools."""

import base64
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import tempfile
import urllib.request

from common import run
from runner import ROOT, docker, fingerprint, local_daemon

HERE = Path(__file__).resolve().parent
INPUTS = HERE / "mcp"
OUTPUT = ROOT / "output/interoperability/mcp"
STAMP = OUTPUT / "installation.json"
INSPECTOR = "/tools/node_modules/@modelcontextprotocol/inspector/clients/launcher/build/index.js"
EVERYTHING = "/tools/node_modules/@modelcontextprotocol/server-everything/dist/index.js"
# Enumerate embedded package manifests as well as the exposed npm lock graph.
INVENTORY = r'''const fs=require('node:fs'),path=require('node:path');let out=[];
function walk(p){for(const d of fs.readdirSync(p,{withFileTypes:true})){
 const f=path.join(p,d.name);if(d.isDirectory())walk(f);
 else if(d.isFile()&&d.name==='package.json'){const m=JSON.parse(fs.readFileSync(f));
 if(m.name&&m.version)out.push({path:f,name:m.name,version:m.version,license:m.license??null,
 scripts:Object.fromEntries(Object.entries(m.scripts??{}).filter(([k])=>['preinstall','install','postinstall','prepare'].includes(k)))});}}}
walk('/tools/node_modules');console.log(JSON.stringify(out.sort((a,b)=>a.path.localeCompare(b.path))));'''


def identity():
    return fingerprint([*(INPUTS / name for name in ("Containerfile", "package.json", "package-lock.json", "provenance.json", "Everything-LICENSE")), HERE / "mcp_tools.py", ROOT / "test/ci/pins.json"])


def source_revision(package, expected):
    """Keep a reviewed source pin separate from an absent registry gitHead."""
    if "gitHead" not in expected:
        raise ValueError("MCP registry gitHead presence is unrecorded: " + package)
    revision = expected["gitHead"]
    reviewed = expected.get("sourceRevision")
    if revision is None:
        revision = reviewed
    elif reviewed is not None and reviewed != revision:
        raise ValueError("MCP source revision disagrees with registry gitHead: " + package)
    if not isinstance(revision, str) or len(revision) != 40 or any(c not in "0123456789abcdef" for c in revision):
        raise ValueError("MCP immutable source revision is missing or invalid: " + package)
    return revision


def validate_registry_identity(package, expected, metadata):
    """Require the reviewed registry fields, including gitHead's exact absence."""
    name, version = package.rsplit("@", 1)
    dist = metadata.get("dist", {})
    if (metadata.get("name") != name or metadata.get("version") != version
            or ("gitHead" in metadata) != (expected["gitHead"] is not None)
            or metadata.get("gitHead") != expected["gitHead"]
            or dist.get("integrity") != expected["dist.integrity"]
            or dist.get("tarball") != expected["dist.tarball"]):
        raise RuntimeError("registry identity drift: " + package)


def validate_lock():
    npm = json.loads((ROOT / "test/ci/pins.json").read_text())["archives"]["npm"]
    package = json.loads((INPUTS / "package.json").read_text())
    recipe = (INPUTS / "Containerfile").read_text()
    expected = "ADD --checksum=sha256:" + npm["sha256"] + " " + npm["url"] + " /tmp/npm.tgz"
    if (package.get("packageManager") != "npm@" + npm["version"] or expected not in recipe.splitlines()
            or 'test "$(npm --version)" = "' + npm["version"] + '"' not in recipe):
        raise ValueError("MCP npm distribution differs from the reviewed pin")
    lock = json.loads((INPUTS / "package-lock.json").read_text())
    pins = json.loads((INPUTS / "provenance.json").read_text())
    for package, expected in pins.items():
        name, version = package.rsplit("@", 1)
        source_revision(package, expected)
        actual = lock["packages"]["node_modules/" + name]
        if (expected["version"] != version or (actual["version"], actual["integrity"], actual["resolved"])
                != (version, expected["dist.integrity"], expected["dist.tarball"])):
            raise ValueError("MCP package provenance mismatch: " + name)
    for path, package in lock["packages"].items():
        if path and (not package.get("resolved", "").startswith("https://registry.npmjs.org/") or not package.get("integrity", "").startswith("sha512-")):
            raise ValueError("unlocked or unexpected npm source: " + path)
    return pins


def safe_args(name):
    if os.getuid() == 0:
        raise RuntimeError("run MCP qualification as a non-root developer")
    args = ["--name", name, "--pull=never", "--read-only", "--cap-drop=ALL",
            "--security-opt=no-new-privileges", "--pids-limit=128", "--memory=512m", "--cpus=2",
            "--tmpfs=/tmp:rw,noexec,nosuid,size=32m", "--user", f"{os.getuid()}:{os.getgid()}",
            "--log-driver=local", "--log-opt=max-size=1m", "--log-opt=max-file=1", "--log-opt=compress=false"]
    for key in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"):
        args += ["--env", key + "="]
    return args


class CreationUnconfirmed(RuntimeError):
    """The daemon may finish creating a container after the CLI fails."""


def create(name, *arguments):
    """Finish bounded creation before delivering interruption; avoid a late-created orphan."""
    previous = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM, signal.SIGALRM})
    unconfirmed = False
    try:
        return docker("create", *safe_args(name), *arguments, timeout=30, limit=2*1024*1024)
    except (RuntimeError, OSError) as error:
        unconfirmed = True
        raise CreationUnconfirmed("MCP container creation unconfirmed: " + name) from error
    finally:
        try:
            signal.pthread_sigmask(signal.SIG_SETMASK, previous)
        except (KeyboardInterrupt, TimeoutError) as interruption:
            if unconfirmed:
                raise CreationUnconfirmed("MCP container creation unconfirmed: " + name) from interruption
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
        raise RuntimeError("owned MCP container cleanup failed")


def install():
    local_daemon()
    pins = validate_lock()
    OUTPUT.mkdir(parents=True, exist_ok=True)
    # Validate immutable registry evidence; npm ci subsequently verifies archive SRI.
    for package, expected in pins.items():
        with urllib.request.urlopen("https://registry.npmjs.org/" + package.replace("@", "%40", 1).replace("@", "/", 1), timeout=30) as response:
            raw = response.read(2 * 1024 * 1024 + 1)
        if len(raw) > 2 * 1024 * 1024:
            raise RuntimeError("registry metadata bound exceeded")
        metadata = json.loads(raw)
        validate_registry_identity(package, expected, metadata)
    sources = {"@modelcontextprotocol/inspector": ("modelcontextprotocol/inspector", "package.json"),
               "@modelcontextprotocol/server-everything": ("modelcontextprotocol/servers", "src/everything/package.json"),
               "@modelcontextprotocol/sdk": ("modelcontextprotocol/typescript-sdk", "package.json")}
    for package, expected in pins.items():
        name = package.rsplit("@", 1)[0]
        repository, path = sources[name]
        revision = source_revision(package, expected)
        source = json.loads(run(["gh", "api", f"repos/{repository}/contents/{path}?ref={revision}"], limit=128*1024).stdout)
        manifest = json.loads(base64.b64decode(source["content"]))
        if manifest.get("name") != name or manifest.get("version") != expected["version"]:
            raise RuntimeError("upstream source/package version mismatch")
    revision = pins["@modelcontextprotocol/server-everything@2026.8.31"]["gitHead"]
    evidence = json.loads(run(["gh", "api", f"repos/modelcontextprotocol/servers/contents/LICENSE?ref={revision}"], limit=128*1024).stdout)
    if base64.b64decode(evidence["content"]) != (INPUTS / "Everything-LICENSE").read_bytes():
        raise RuntimeError("Everything source license mismatch")
    with tempfile.TemporaryDirectory(prefix="install-", dir=OUTPUT) as temporary:
        directory = Path(temporary)
        for filename in ("Containerfile", "package.json", "package-lock.json", "Everything-LICENSE"):
            shutil.copyfile(INPUTS / filename, directory / filename)
        tag = "open-agentic-catalog-mcp:" + identity()[:20]
        print("Installing locked MCP tools (downloads allowed)...", flush=True)
        result = docker("build", "--progress=plain", "-t", tag, "-f", str(directory / "Containerfile"), str(directory), timeout=900, limit=16*1024*1024, check=False)
        (OUTPUT / "install.log").write_text(result.stdout + result.stderr)
        if result.code:
            raise RuntimeError("MCP installation failed; see output/interoperability/mcp/install.log")
        image = docker("image", "inspect", tag, "--format", "{{.Id}}").stdout.strip()
        import uuid
        name = "catalog-mcp-install-" + uuid.uuid4().hex[:12]
        try:
            create(name, "--network=none", image, "-e", INVENTORY)
            inventory = docker("start", "--attach", name, timeout=30, limit=2*1024*1024).stdout
        finally:
            remove([name])
        packages = json.loads(inventory)
        for package in pins:
            name, version = package.rsplit("@", 1)
            if not any(p["path"] == "/tools/node_modules/" + name + "/package.json" and p["version"] == version for p in packages):
                raise RuntimeError("installed MCP package mismatch: " + name)
        (OUTPUT / "dependencies.json").write_text(inventory)
        stamp = {"identity": identity(), "image": image, "packages": pins,
                 "inventory_sha256": hashlib.sha256(inventory.encode()).hexdigest()}
        (directory / "installation.json").write_text(json.dumps(stamp, indent=2) + "\n")
        (directory / "installation.json").replace(STAMP)
    print("MCP tools installed: " + image)


def installed():
    validate_lock()
    try:
        stamp = json.loads(STAMP.read_text())
        if stamp["identity"] != identity() or not stamp["image"].startswith("sha256:"):
            raise ValueError("stale stamp")
        if hashlib.sha256((OUTPUT / "dependencies.json").read_bytes()).hexdigest() != stamp["inventory_sha256"]:
            raise ValueError("inventory drift")
        if docker("image", "inspect", stamp["image"], "--format", "{{.Id}}").stdout.strip() != stamp["image"]:
            raise ValueError("image drift")
    except (OSError, KeyError, ValueError, RuntimeError) as error:
        raise RuntimeError("Run make mcp-tools-install first (missing or stale tools)") from error
    return stamp
