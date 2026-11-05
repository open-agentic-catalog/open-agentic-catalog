"""Acquire and verify the pinned, test-only Directory deployment inputs."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import sys
import tempfile

from agntcy_lab import file_digest, read_json
from common import run
import runner as shared

ROOT = shared.ROOT
ASSETS = shared.HERE / "agntcy"
TOOLS = ROOT / ".tools/agntcy" / read_json(ASSETS / "deployment.json")["release"]


def environment():
    """Keep acquisition and inspection independent of caller Git and cluster settings."""
    result = {key: os.environ[key] for key in ("PATH", "HOME", "LANG", "DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG") if key in os.environ}
    return result


def command(*args, **kwargs):
    return run(list(args), env=environment(), **kwargs)


def source_identity(source, revision):
    """Require every extracted byte and path, including unexpected extra files."""
    if source.is_symlink() or not source.is_dir():
        raise ValueError("Directory source must be a real directory")
    digest = hashlib.sha256()
    for path in sorted(source.rglob("*"), key=lambda item: item.relative_to(source).as_posix()):
        if path.is_symlink():
            raise ValueError("Directory source contains a symlink")
        if path.is_file():
            digest.update(path.relative_to(source).as_posix().encode() + b"\0" + hashlib.sha256(path.read_bytes()).digest())
    if digest.hexdigest() != read_json(ASSETS / "deployment.json")["source_sha256"]:
        raise ValueError("Directory source differs from the pinned revision " + revision)
    return file_digest(source / "LICENSE.md")


def image_identity(reference):
    document = json.loads(command("docker", "image", "inspect", reference, limit=1024 * 1024).stdout)[0]
    repository = reference.split("@", 1)[0].split(":", 1)[0]
    digest = reference.split("@", 1)[1]
    if (document["Os"] != "linux" or document["Architecture"] != "amd64"
            or not any(item.endswith("@" + digest) and item.split("@", 1)[0].removeprefix("docker.io/") == repository.removeprefix("docker.io/")
                       for item in document.get("RepoDigests", []))):
        raise ValueError("Directory image platform or repository digest differs from its pin")
    return {"id": document["Id"], "reference": reference, "os": "linux", "architecture": "amd64"}


def require_platform():
    if platform.system() != "Linux" or platform.machine() not in ("x86_64", "amd64"):
        raise ValueError("Directory deployment inputs qualify Linux AMD64 only")


def inspect():
    """Inspection performs no downloads, builds or cluster mutations."""
    require_platform()
    shared.local_daemon()
    lock = read_json(ASSETS / "deployment.json")
    source_license = source_identity(TOOLS / "source", lock["revision"])
    for name, digest in (("dirctl-linux-amd64", lock["cli_sha256"]), ("dir-" + lock["release"] + ".tgz", lock["chart_sha256"])):
        if file_digest(TOOLS / name) != digest:
            raise ValueError("Directory artifact checksum mismatch: " + name)
    cli = TOOLS / "dirctl-linux-amd64"
    if command(str(cli), "version").stdout.strip() != lock["cli_version"]:
        raise ValueError("Directory CLI version differs from its pin")
    dependencies = command("go", "version", "-m", str(cli)).stdout
    return {"release": lock["release"], "revision": lock["revision"], "cli_sha256": lock["cli_sha256"], "chart_sha256": lock["chart_sha256"],
            "license_sha256": source_license, "cli_dependencies_sha256": hashlib.sha256(dependencies.encode()).hexdigest(),
            "images": {name: image_identity(reference) for name, reference in lock["images"].items()}}


def install():
    """Explicit acquisition never starts Directory or changes a Kubernetes context."""
    require_platform()
    shared.local_daemon()
    TOOLS.mkdir(parents=True, exist_ok=True)
    if TOOLS.is_symlink():
        raise ValueError("Directory tool directory must not be a symlink")
    lock = read_json(ASSETS / "deployment.json")
    source = TOOLS / "source"
    if not source.exists():
        raw = command("gh", "api", "repos/agntcy/dir/tarball/" + lock["revision"], binary=True, limit=16 * 1024 * 1024, timeout=180).stdout
        with tempfile.TemporaryDirectory(prefix="source-", dir=TOOLS) as temporary:
            staged = Path(temporary) / "source"
            shared.extract_source(raw, staged, read_json(ASSETS / "deployment.json")["source_sha256"])
            staged.rename(source)
    source_identity(source, lock["revision"])
    cli = TOOLS / "dirctl-linux-amd64"
    if not cli.exists():
        command("gh", "release", "download", lock["release"], "--repo", "agntcy/dir", "--pattern", cli.name,
                "--dir", str(TOOLS), timeout=180)
    cli.chmod(0o755)
    chart = TOOLS / ("dir-" + lock["release"] + ".tgz")
    if not chart.exists():
        command("helm", "pull", "oci://ghcr.io/agntcy/dir/helm-charts/dir", "--version", lock["release"],
                "--destination", str(TOOLS), timeout=180)
    # Refuse changed binaries/charts before acquiring the larger image closure.
    for path, digest in ((cli, lock["cli_sha256"]), (chart, lock["chart_sha256"])):
        if file_digest(path) != digest:
            raise ValueError("Directory artifact checksum mismatch: " + path.name)
    for reference in lock["images"].values():
        command("docker", "pull", "--platform=linux/amd64", reference, timeout=600)
    dependencies = command("go", "version", "-m", str(cli)).stdout
    (TOOLS / "cli-dependencies.txt").write_text(dependencies)
    (TOOLS / "LICENSE.md").write_bytes((source / "LICENSE.md").read_bytes())
    identity = inspect()
    (TOOLS / "installation.json").write_text(json.dumps(identity, indent=2) + "\n")
    print(json.dumps(identity))


def check():
    actual = inspect()
    if actual != read_json(TOOLS / "installation.json"):
        raise ValueError("Directory installation changed; run make agntcy-tools-install")
    print(json.dumps(actual))


def fingerprints():
    """Bind current build, chart and harness files without including generated output."""
    print(json.dumps({"catalog": shared.fingerprint((*shared.CATALOG_INPUTS, ROOT / "VERSION", ROOT / "Containerfile")),
                      "harness": shared.fingerprint((ROOT / "test/minikube", shared.HERE, ROOT / "Makefile", ROOT / "test/functional/Containerfile", ROOT / "test/functional/fixture")),
                      "chart": shared.fingerprint((ROOT / "deployments/helm/open-agentic-catalog",))}))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "check", "fingerprint"))
    args = parser.parse_args()
    try:
        if not __debug__:
            raise ValueError("Directory tools require enabled Python assertions")
        if args.action == "install":
            install()
        elif args.action == "check":
            check()
        else:
            fingerprints()
    except (RuntimeError, OSError, ValueError, KeyError, TypeError) as error:
        print("Directory tools: " + str(error), file=sys.stderr)
        sys.exit(1)
