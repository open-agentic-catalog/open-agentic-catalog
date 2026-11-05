"""Install a pinned HF client and supervise disposable, network-isolated qualification."""

import argparse
import hashlib
import io
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import signal
import sys
import tarfile
import tempfile
import time
import uuid

from common import run
from client_contract import DISPOSITIONS, check_cases, redact

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent
OUTPUT = ROOT / "output/interoperability"
STAMP = OUTPUT / "installation.json"
REVISION = "49c927439fcaa8f210cfd42186c0641acef579fa"
SOURCE_DIGEST = "9ea4ddf816e8f66bfddb2228cee550054e2ca65358abef10684819fd6a76b815"
TOOL_INPUTS = (HERE / "Containerfile", HERE / "tools.txt", HERE / "tools.in", ROOT / "go.mod", ROOT / "go.sum")
CATALOG_INPUTS = tuple(ROOT / name for name in
                       ("go.mod", "go.sum", "cmd", "internal", "api", "schemas", "migrations", "samples", "config/local.json"))
EXPECTED_CASES = set(DISPOSITIONS)


def fingerprint(paths):
    """Bind sorted path names and bytes, without timestamps or generated evidence."""
    digest = hashlib.sha256()
    for root in paths:
        for path in sorted(root.rglob("*") if root.is_dir() else [root]):
            if "__pycache__" in path.relative_to(ROOT).parts or path.suffix in (".pyc", ".pyo"):
                continue
            if path.is_file():
                digest.update(str(path.relative_to(ROOT)).encode() + b"\0")
                digest.update(hashlib.sha256(path.read_bytes()).digest())
    return digest.hexdigest()


def tool_identity():
    """Invalidate installations when any immutable source or build input changes."""
    return hashlib.sha256((REVISION + SOURCE_DIGEST + fingerprint(TOOL_INPUTS)).encode()).hexdigest()


def extract_source(raw, destination, expected_digest=None):
    """Extract bounded regular files only; never trust tar paths, links or permissions."""
    digest = hashlib.sha256()
    files = {}
    total = 0
    with tarfile.open(fileobj=io.BytesIO(raw), mode="r:gz") as archive:
        prefix = None
        for index, member in enumerate(archive):
            if index >= 3000 or member.size > 4 * 1024 * 1024:
                raise ValueError("source archive bound exceeded")
            path = PurePosixPath(member.name)
            if path.is_absolute() or ".." in path.parts or not path.parts:
                raise ValueError("unsafe source archive path")
            prefix = prefix or path.parts[0]
            if path.parts[0] != prefix:
                raise ValueError("multiple source archive roots")
            if member.isdir():
                continue
            if not member.isfile() or len(path.parts) < 2:
                raise ValueError("source archive contains a link or special file")
            relative = PurePosixPath(*path.parts[1:])
            if str(relative) in files:
                raise ValueError("duplicate source archive path")
            total += member.size
            if total > 20 * 1024 * 1024:
                raise ValueError("expanded source archive bound exceeded")
            with archive.extractfile(member) as stream:
                files[str(relative)] = stream.read()
    for relative, content in sorted(files.items()):
        digest.update(relative.encode() + b"\0" + hashlib.sha256(content).digest())
    actual = digest.hexdigest()
    if actual != (SOURCE_DIGEST if expected_digest is None else expected_digest):
        raise ValueError("source tree digest mismatch: " + actual)
    for relative, content in files.items():
        path = destination / relative
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(content)
    return actual


def docker(*args, **kwargs):
    """Use Docker only as the explicitly local fixture launcher."""
    return run(["docker", *args], **kwargs)


def local_daemon():
    """Reject remote Docker endpoints and non-Linux engines before any mutation."""
    context = os.environ.get("DOCKER_CONTEXT")
    endpoint = None if context else os.environ.get("DOCKER_HOST")
    if not endpoint:
        args = ["context", "inspect"] + ([context] if context else [])
        info = json.loads(docker(*args).stdout)
        endpoint = info[0]["Endpoints"]["docker"]["Host"]
    if not endpoint.startswith("unix://"):
        raise RuntimeError("interoperability requires a local Unix-socket Docker engine")
    platform = docker("info", "--format", "{{.OSType}}/{{.Architecture}}").stdout.strip()
    arch = {"linux/x86_64": "amd64", "linux/aarch64": "arm64", "linux/amd64": "amd64", "linux/arm64": "arm64"}.get(platform)
    if not arch:
        raise RuntimeError("unsupported test engine platform: " + platform)
    return arch


def install():
    """Fetch reviewed inputs through gh, build locally and stamp an inspected image ID."""
    local_daemon()
    OUTPUT.mkdir(parents=True, exist_ok=True)
    go_version = next(line.split()[1] for line in (ROOT / "go.mod").read_text().splitlines() if line.startswith("go "))
    environment = dict(os.environ, GOTOOLCHAIN="go" + go_version, GOENV="off", GOWORK="off", GOFLAGS="")
    goroot = run(["go", "env", "GOROOT"], env=environment).stdout.strip()
    go = str(Path(goroot) / "bin/go")
    run([go, "mod", "download"], env=environment, timeout=300)
    with tempfile.TemporaryDirectory(prefix="install-", dir=OUTPUT) as name:
        directory = Path(name)
        raw = run(["gh", "api", f"repos/huggingface/hf-discover/tarball/{REVISION}"],
                  timeout=120, limit=16 * 1024 * 1024, binary=True).stdout
        extract_source(raw, directory / "source")
        for path in TOOL_INPUTS[:3]:
            shutil.copyfile(path, directory / path.name)
        tag = "open-agentic-catalog-hf-interop:" + tool_identity()[:20]
        print("Building pinned HF client (dependency downloads allowed)...", flush=True)
        result = docker("build", "--progress=plain", "-f", str(directory / "Containerfile"),
                        "-t", tag, str(directory), timeout=900, limit=16 * 1024 * 1024, check=False)
        (OUTPUT / "install.log").write_text(result.stdout + result.stderr)
        if result.code:
            raise RuntimeError("HF client installation failed; see output/interoperability/install.log")
        image = docker("image", "inspect", tag, "--format", "{{.Id}}").stdout.strip()
        version = installation_probe(image, "hf-discover", ["--version"]).stdout.strip()
        if version != "hf-discover 1.3.7":
            raise RuntimeError("installed client version mismatch")
        help_text = installation_probe(image, "hf-discover", ["search", "--help"]).stdout
        if "--registry-url" not in help_text:
            raise RuntimeError("installed Search command lacks the selected endpoint option")
        from peer_runner import image_preflight
        peer_capabilities = image_preflight(image)
        inventory = installation_probe(image, "python", ["-c", "import importlib.metadata as m,json; print(json.dumps(sorted([{'name':d.metadata['Name'],'version':d.version,'license':d.metadata.get('License-Expression') or d.metadata.get('License') or '; '.join(v for v in d.metadata.get_all('Classifier',[]) if v.startswith('License ::'))} for d in m.distributions()],key=lambda d:d['name'])))"]).stdout
        (OUTPUT / "dependencies.json").write_text(inventory)
        stamp = {"identity": tool_identity(), "image": image, "go": go, "source_revision": REVISION,
                 "source_digest": SOURCE_DIGEST, "version": version, "peer_capabilities": peer_capabilities,
                 "inventory_sha256": hashlib.sha256(inventory.encode()).hexdigest()}
        temporary = directory / "installation.json"
        temporary.write_text(json.dumps(stamp, indent=2) + "\n")
        temporary.replace(STAMP)
    print("Installed " + image)


def installed_image():
    """Fail closed on stale, missing or malformed installs instead of downloading."""
    try:
        stamp = json.loads(STAMP.read_text())
        if not isinstance(stamp, dict) or stamp.get("identity") != tool_identity():
            raise ValueError("stale stamp")
        if not isinstance(stamp.get("image"), str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", stamp["image"]):
            raise ValueError("invalid image ID")
        if not isinstance(stamp.get("go"), str) or not Path(stamp["go"]).is_file():
            raise ValueError("stale stamp")
        image = stamp["image"]
        if docker("image", "inspect", image, "--format", "{{.Id}}").stdout.strip() != image:
            raise ValueError("image mismatch")
        return stamp
    except (OSError, ValueError, KeyError, TypeError, RuntimeError) as error:
        raise RuntimeError("Run make interoperability-tools-install first") from error


def configuration():
    """Reuse the maintained local profile with two isolated audience bindings."""
    value = json.loads((ROOT / "config/local.json").read_text())
    value["http"]["address"] = "127.0.0.1:8080"
    value["http"]["publicBaseUrl"] = "http://127.0.0.1:8080"
    public = value["discovery"]["hosts"][0]
    value["discovery"]["hosts"] = [public, dict(public, host="localhost", mode="authenticated")]
    return value


def safety_args(name):
    """Apply runtime bounds independently of the third-party HTTP implementation."""
    flags = ["--name", name, "--pull=never", "--read-only", "--cap-drop=ALL",
            "--security-opt=no-new-privileges", "--pids-limit=128", "--memory=512m",
            "--cpus=2", "--user=65532:65532",
            "--log-driver=none", "--env=HF_HUB_OFFLINE=1", "--env=HF_HUB_DISABLE_TELEMETRY=1"]
    # Docker's config.json can inject proxies even when the shell environment is clean.
    for key in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"):
        flags.append("--env=" + key + "=")
    return flags


def installation_probe(image, entrypoint, arguments):
    """Keep version/help/inventory execution offline and clean up even on a timeout."""
    name = "open-agentic-catalog-hf-install-" + uuid.uuid4().hex[:12]
    try:
        return docker("run", *safety_args(name), "--network=none", "--entrypoint=" + entrypoint,
                      image, *arguments, timeout=60, limit=1024 * 1024)
    finally:
        result = docker("rm", "-f", name, check=False)
        if result.code and "no such" not in result.stderr.lower():
            raise RuntimeError("failed to clean installation probe " + name)


def check_report(value, code):
    """Reject empty, truncated or contradictory successful reports from the child."""
    expected = EXPECTED_CASES
    if not isinstance(value, dict) or "setup_error" in value:
        raise RuntimeError("client fixture setup failed: " + str(value))
    cases = value.get("cases")
    passed = check_cases(cases, expected)
    if (type(value.get("passed")) is not int or type(value.get("total")) is not int
            or value["passed"] != passed or value["total"] != len(expected)
            or code not in (0, 1) or (code == 0) != (passed == len(expected))):
        raise RuntimeError("contradictory client qualification result")
    if value.get("client_version") != "hf-discover 1.3.7":
        raise RuntimeError("qualification used an unexpected client version")


def navigation_preflight(image):
    """Probe existing installations offline; a missing command must fail before setup."""
    help_text = installation_probe(image, "hf-discover", ["navigate", "--help"]).stdout
    for option in ("--max-depth", "--max-registries", "--max-per-source", "--timeout", "--token", "--json"):
        if option not in help_text:
            raise RuntimeError("installed navigation command lacks " + option)
    code = ("from discover.cli import _registry_search; "
            "from discover.navigation import navigate,search_registry; "
            "from discover.models import SearchRequest,SearchQuery; "
            "from pydantic import ValidationError; from typer import BadParameter; "
            "assert all(callable(f) for f in (_registry_search,navigate,search_registry)); "
            "print('navigation-capabilities-ok')")
    result = installation_probe(image, "python", ["-B", "-c", code])
    if result.stdout.strip() != "navigation-capabilities-ok":
        raise RuntimeError("installed navigation capability probe failed")


def cached_go():
    """Resolve the exact repository compiler offline, not an older PATH launcher."""
    version = next(line.split()[1] for line in (ROOT / "go.mod").read_text().splitlines() if line.startswith("go "))
    environment = {key: os.environ[key] for key in ("PATH", "HOME", "GOCACHE", "GOMODCACHE") if key in os.environ}
    environment.update(GOTOOLCHAIN="local", GOENV="off", GOWORK="off", GOFLAGS="",
                       GOPROXY="off", GOSUMDB="off", GOTELEMETRY="off",
                       GOPRIVATE="", GONOPROXY="", GONOSUMDB="")
    selected = json.loads(run(["go", "env", "-json", "GOROOT", "GOVERSION", "GOMODCACHE",
                               "GOHOSTOS", "GOHOSTARCH"], env=environment, timeout=10).stdout)
    # Automatic switching can attempt checksum verification even for a cached
    # toolchain. Inspect the launcher locally, then execute the exact cached
    # compiler directly; qualification never acquires or verifies tools online.
    root = Path(selected["GOROOT"])
    if selected["GOVERSION"] != "go" + version:
        root = Path(selected["GOMODCACHE"]) / (
            f"golang.org/toolchain@v0.0.1-go{version}.{selected['GOHOSTOS']}-{selected['GOHOSTARCH']}")
    go = root / "bin/go"
    if not go.is_file():
        raise RuntimeError("Cached Go executable is unavailable; install the repository toolchain first")
    if run([str(go), "env", "GOVERSION"], env=environment, timeout=10).stdout.strip() != "go" + version:
        raise RuntimeError("Cached Go toolchain must match the repository go directive")
    return str(go)


def build_binaries(build, go, arch):
    """Build the current static commands from cached modules without downloads."""
    environment = {key: os.environ[key] for key in ("PATH", "HOME", "GOCACHE", "GOMODCACHE") if key in os.environ}
    environment.update(CGO_ENABLED="0", GOOS="linux", GOARCH=arch, GOENV="off", GOFLAGS="-mod=readonly",
                       GOPROXY="off", GOSUMDB="off", GOTOOLCHAIN="local", GOWORK="off",
                       GOPRIVATE="", GONOPROXY="", GONOSUMDB="")
    for command in ("catalogd", "catalogctl"):
        run([go, "build", "-trimpath", "-ldflags=-s -w", "-o", str(build / command),
             "./cmd/" + command], env=environment, timeout=300)


def build_catalog(build, stamp, arch, config, image):
    """Build the current static commands offline into an invocation-owned image."""
    build_binaries(build, stamp["go"], arch)
    (build / "config.json").write_text(json.dumps(config))
    (build / "Containerfile").write_text("FROM scratch\nCOPY catalogd catalogctl /\nCOPY config.json /config.json\nUSER 65532:65532\nENTRYPOINT [\"/catalogd\"]\n")
    docker("build", "--network=none", "-f", str(build / "Containerfile"), "-t", image, str(build), timeout=120)


def check():
    """Build current Catalog code, run all client cases, retain results and clean owned resources."""
    arch = local_daemon()
    stamp = installed_image()
    navigation_preflight(stamp["image"])
    before = fingerprint(CATALOG_INPUTS)
    name = "open-agentic-catalog-hf-" + uuid.uuid4().hex[:12]
    OUTPUT.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="run-", dir=OUTPUT))
    directory.chmod(0o755)
    build = directory / "build"
    build.mkdir()
    network, server, client, image = name + "-net", name + "-catalog", name + "-client", name + ":fixture"
    harness_before = fingerprint((HERE, ROOT / "Makefile"))
    report = {"catalog_commit": run(["git", "-C", str(ROOT), "rev-parse", "HEAD"]).stdout.strip(),
              "harness_sha256": harness_before, "started_utc": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
              "catalog_source_sha256": before, "client": stamp, "status": "setup-failed",
              "owned": dict(network=network, server=server, client=client, image=image)}
    cleanup_errors = []
    try:
        # Mount one non-secret diagnostic file, never the report directory or a RAM disk.
        progress = directory / "progress"
        progress.mkdir(mode=0o700)
        phase = progress / "phase.json"
        phase.write_text('{}\n')
        phase.chmod(0o666)  # The private parent excludes other host users; container UID differs.
        build_catalog(build, stamp, arch, configuration(), image)
        docker("network", "create", "--internal", network)
        docker("run", "-d", *safety_args(server), "--network", network, image,
               "all", "--config", "/config.json")
        for _ in range(60):
            probe = docker("exec", server, "/catalogctl", "probe", "--url", "http://127.0.0.1:8080/readyz", check=False)
            if probe.code == 0:
                break
            time.sleep(0.25)
        else:
            raise RuntimeError("disposable Catalog did not become ready")
        smoke = docker("exec", server, "/catalogctl", "smoke", "--run-id", "hf-interop", timeout=180)
        (directory / "smoke.json").write_text(smoke.stdout)
        result = docker("run", *safety_args(client), "--network", "container:" + server,
                        "--mount", "type=bind,source=" + str(HERE) + ",target=/harness,readonly",
                        "--mount", "type=bind,source=" + str(phase) + ",target=/phase.json",
                        "--entrypoint=python", stamp["image"], "-B", "/harness/qualification.py",
                        timeout=600, check=False)
        (directory / "client-stderr.txt").write_text(redact(result.stderr)[:6000])
        try:
            report["qualification"] = json.loads(redact(result.stdout))
        except ValueError as error:
            raise RuntimeError("client runner did not return a JSON report") from error
        if result.stdout != redact(result.stdout):
            raise RuntimeError("client runner emitted a synthetic credential")
        check_report(report["qualification"], result.code)
        report["status"] = "passed" if result.code == 0 else "qualification-failed"
        if before != fingerprint(CATALOG_INPUTS) or harness_before != fingerprint((HERE, ROOT / "Makefile")):
            raise RuntimeError("Catalog or harness source changed during qualification")
    except KeyboardInterrupt:
        report.update(error="qualification interrupted", status="interrupted")
    except (RuntimeError, OSError, ValueError) as error:
        report["error"] = redact(str(error))
        report["status"] = "setup-failed"
    finally:
        for args in (("rm", "-f", client), ("rm", "-f", server), ("network", "rm", network), ("image", "rm", image)):
            try:
                result = docker(*args, check=False)
                if result.code and "no such" not in result.stderr.lower() and "not found" not in result.stderr.lower():
                    cleanup_errors.append(" ".join(args))
            except (RuntimeError, OSError) as error:
                cleanup_errors.append(str(error))
        if cleanup_errors:
            report["cleanup_errors"] = cleanup_errors
            report["status"] = "cleanup-failed"
        # This exact directory was created above; never remove retained reports or shared caches.
        try:
            shutil.rmtree(build)
        except OSError as error:
            report["cleanup_errors"] = [*cleanup_errors, str(error)]
            report["status"] = "cleanup-failed"
        (directory / "result.json").write_text(json.dumps(report, indent=2) + "\n")
    print("Report: " + str(directory / "result.json"))
    print("Status: " + report["status"])
    return int(report["status"] != "passed")


def main():
    """Expose explicit install/check commands without a caller-selectable product target."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("install", "check", "peer"))
    args = parser.parse_args()
    if not __debug__:
        raise RuntimeError("interoperability requires Python assertions; unset PYTHONOPTIMIZE")
    os.chdir(ROOT)
    signal.signal(signal.SIGTERM, interrupted)
    if args.command == "install":
        install()
        return 0
    if args.command == "peer":
        from peer_runner import check as check_peer
        return check_peer()
    return check()


def interrupted(_signum, _frame):
    """Let SIGTERM follow the same scoped cleanup path as Ctrl-C."""
    raise KeyboardInterrupt()


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (RuntimeError, OSError, ValueError, KeyboardInterrupt) as error:
        print(str(error), file=sys.stderr)
        sys.exit(2)
