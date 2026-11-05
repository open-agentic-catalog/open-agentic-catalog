"""Explicit prerequisites and validation for Linux verification workflows.

Application tools keep their own installers and locks. Binary downloads verify
the publisher's checksum for the exact release. Those fetched checksum manifests
are not an independently pinned supply-chain attestation.
"""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import tomllib
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
OWNED = ROOT / ".tools/ci"
PINS = json.loads(Path(__file__).with_name("pins.json").read_text())
# Resource admission budgets; adjust them against measured runner requirements.
DISK_GIB = {"unit": 4, "quality": 8, "ui": 6, "docs": 6, "go": 8,
            "python": 8, "typescript": 10, "rust": 20, "images": 10,
            "charts": 6, "functional": 12, "functional-ui": 12,
            "coverage": 12, "security": 10, "formal": 4}
SOURCE_TOOLS = {"quality"}
DOCKER = {"quality", "images", "charts", "functional", "functional-ui", "coverage", "security"}
NODE_PROJECT = {"quality": "documentation", "docs": "documentation", "ui": "catalog-ui",
                "functional-ui": "catalog-ui", "security": "catalog-ui"}


def run(arguments, *, capture=False, env=None, cwd=ROOT):
    return subprocess.run([str(value) for value in arguments], cwd=cwd, env=env,
                          text=True, capture_output=capture, check=True,
                          timeout=1200).stdout


def record_tool(name, version):
    """Emit only bounded tool version text, never a process environment."""
    print(json.dumps({"tool": name, "actualVersion": version.strip()[:2048]}))


def preflight(profile):
    if platform.system() != "Linux" or platform.machine() not in {"x86_64", "AMD64"}:
        raise ValueError("verification workflows require a Linux AMD64 runner")
    if sys.version_info[:2] != (3, 12):
        raise ValueError("select the installed Python 3.12 interpreter explicitly")
    required = ["git", "openssl", "tar"]
    if profile in {"unit", "quality", "coverage", "go", "python", "typescript", "rust", "functional", "functional-ui"}:
        required.append("cc")
    if profile in DOCKER:
        required.append("docker")
    if profile in {"go", "python", "typescript", "rust", "formal"}:
        required.append("gh")
    if profile in {"python", "formal"}:
        java_home = os.environ.get("JAVA_HOME_21_X64")
        if not java_home or not (Path(java_home) / "bin/java").is_file():
            raise ValueError("the selected workflow requires installed JAVA_HOME_21_X64")
        os.environ["JAVA_HOME"] = java_home
        os.environ["PATH"] = str(Path(java_home) / "bin") + os.pathsep + os.environ["PATH"]
        required.append("java")
    if profile == "rust":
        required.append("rustup")
    missing = [name for name in required if shutil.which(name) is None]
    if missing:
        raise ValueError("missing system prerequisites: " + ", ".join(missing))
    if profile in {"python", "formal"}:
        actual_java = run(["java", "--version"], capture=True)
        if not re.match(r"(?:openjdk|java)\s+21(?:[.\s+-]|$)", actual_java):
            raise ValueError("select the installed Java 21 runtime explicitly")
        record_tool("java", actual_java)
    available = shutil.disk_usage(ROOT).free
    memory = int(Path("/proc/meminfo").read_text().split("MemAvailable:", 1)[1].split()[0]) * 1024
    resource_budget(profile, available, memory)
    print(json.dumps({"profile": profile, "platform": platform.platform(),
                      "python": platform.python_version(), "freeDiskBytes": available,
                      "availableMemoryBytes": memory, "runnerImage": os.environ.get("ImageVersion", "unreported")}))
    if profile in DOCKER:
        record_tool("docker-client", run(["docker", "--version"], capture=True))


def resource_budget(profile, disk_bytes, memory_bytes):
    if disk_bytes < DISK_GIB[profile] * (1 << 30) or memory_bytes < (4 if profile in {"rust", "formal"} else 2) * (1 << 30):
        raise ValueError("insufficient disk/memory; use a runner with sufficient capacity, do not prune or tune the host")


def download(url, destination, limit=512 << 20):
    if not url.startswith("https://"):
        raise ValueError("tool acquisition requires HTTPS")
    with urllib.request.urlopen(url, timeout=90) as response, destination.open("wb") as output:
        if not response.geturl().startswith("https://"):
            raise ValueError("tool acquisition redirected outside HTTPS")
        total = 0
        while chunk := response.read(1 << 20):
            total += len(chunk)
            if total > limit:
                raise ValueError("tool acquisition exceeded its size limit")
            output.write(chunk)


def checked_asset(url, manifest_url, destination):
    """Match an exact release asset, never a latest release or unchecked binary."""
    with tempfile.TemporaryDirectory(prefix="checksums-", dir=OWNED) as directory:
        manifest = Path(directory) / "checksums"
        download(manifest_url, manifest, 4 << 20)
        expected = manifest_checksum(manifest.read_text(), destination.name)
        download(url, destination)
        with destination.open("rb") as source:
            if hashlib.file_digest(source, "sha256").hexdigest() != expected:
                raise ValueError("release checksum mismatch")


def manifest_checksum(value, filename):
    matches = []
    for line in value.splitlines():
        parts = line.split()
        if parts and re.fullmatch(r"[a-fA-F0-9]{64}", parts[0]):
            if len(parts) == 1 or parts[-1].lstrip("*").removeprefix("./") == filename:
                matches.append(parts[0].lower())
    if len(matches) != 1:
        raise ValueError("release checksum manifest has no unique exact asset")
    return matches[0]


def unpack(archive, destination):
    with tarfile.open(archive) as bundle:
        if sum(member.size for member in bundle.getmembers()) > 2 << 30:
            raise ValueError("tool archive exceeds its expanded size bound")
        bundle.extractall(destination, filter="data")


def publish_path(path):
    if os.environ.get("GITHUB_PATH"):
        with Path(os.environ["GITHUB_PATH"]).open("a") as output:
            output.write(str(path) + "\n")
    os.environ["PATH"] = str(path) + os.pathsep + os.environ["PATH"]


def go_install():
    selected = re.search(r"^go ([0-9.]+)$", (ROOT / "go.mod").read_text(), re.MULTILINE)
    if selected is None:
        raise ValueError("no exact repository Go version")
    version = selected[1]
    filename = "go" + version + ".linux-amd64.tar.gz"
    with tempfile.TemporaryDirectory(prefix="go-acquire-", dir=OWNED) as directory:
        temporary = Path(directory)
        releases = temporary / "releases.json"
        download("https://go.dev/dl/?mode=json&include=all", releases, 16 << 20)
        checksums = [item["sha256"] for release in json.loads(releases.read_text())
                     if release["version"] == "go" + version for item in release["files"]
                     if item["filename"] == filename]
        if len(checksums) != 1 or not re.fullmatch(r"[a-f0-9]{64}", checksums[0]):
            raise ValueError("official Go release metadata lacks the selected toolchain")
        archive = temporary / filename
        download("https://go.dev/dl/" + filename, archive)
        with archive.open("rb") as source:
            if hashlib.file_digest(source, "sha256").hexdigest() != checksums[0]:
                raise ValueError("Go toolchain checksum mismatch")
        unpack(archive, temporary)
        target = OWNED / ("go-" + version)
        if target.exists():
            raise ValueError("owned Go installation exists; use a fresh job rather than overwrite it")
        (temporary / "go").rename(target)
    publish_path(target / "bin")
    actual = run([target / "bin/go", "version"], capture=True, env=dict(os.environ, GOTOOLCHAIN="local"))
    if actual.split()[:3] != ["go", "version", "go" + version]:
        raise ValueError("Go toolchain version mismatch")
    record_tool("go", actual)


def node_install(project):
    directory = ROOT / project
    version = (directory / ".nvmrc").read_text().strip()
    expected_npm = json.loads((directory / "package.json").read_text())["packageManager"].removeprefix("npm@")
    if not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", version):
        raise ValueError("Node selection must be an exact version")
    filename = "node-v" + version + "-linux-x64.tar.gz"
    base = "https://nodejs.org/dist/v" + version + "/"
    with tempfile.TemporaryDirectory(prefix="node-acquire-", dir=OWNED) as directory:
        temporary = Path(directory)
        archive = temporary / filename
        checked_asset(base + filename, base + "SHASUMS256.txt", archive)
        unpack(archive, temporary)
        target = OWNED / filename.removesuffix(".tar.gz")
        if target.exists():
            raise ValueError("owned Node installation exists; use a fresh job")
        (temporary / target.name).rename(target)
    publish_path(target / "bin")
    actual_node = run(["node", "--version"], capture=True).strip()
    if actual_node != "v" + version:
        raise ValueError("Node version mismatch")
    if run(["npm", "--version"], capture=True).strip() != expected_npm:
        # npm has a separate owning packageManager pin; distributions can bundle another version.
        run(["npm", "install", "--global", "--prefix", target, "--ignore-scripts", "--no-audit", "--no-fund", "npm@" + expected_npm])
    actual_npm = run(["npm", "--version"], capture=True).strip()
    if actual_npm != expected_npm:
        raise ValueError("npm version mismatch")
    record_tool("node", actual_node)
    record_tool("npm", actual_npm)


def python_install(profile):
    venv = OWNED / "python"
    run([sys.executable, "-m", "venv", venv])
    uv = tomllib.loads((ROOT / "pyproject.toml").read_text())["tool"]["uv"]["required-version"]
    requirements = ["uv" + uv]
    if profile in SOURCE_TOOLS:
        requirements += ["yamllint==" + PINS["yamllint"], "PyYAML==" + PINS["pyyaml"], "pathspec==" + PINS["pathspec"]]
    # Direct tool versions are pinned; registries supply distribution metadata.
    run([venv / "bin/python", "-m", "pip", "install", "--disable-pip-version-check", "--only-binary=:all:", *requirements])
    publish_path(venv / "bin")
    actual_uv = run([venv / "bin/uv", "--version"], capture=True)
    if actual_uv.split()[:2] != ["uv", uv.removeprefix("==")]:
        raise ValueError("uv version mismatch")
    record_tool("uv", actual_uv)
    if profile in SOURCE_TOOLS:
        actual_yaml = run([venv / "bin/yamllint", "--version"], capture=True)
        if actual_yaml.strip() != "yamllint " + PINS["yamllint"]:
            raise ValueError("yamllint version mismatch")
        record_tool("yamllint", actual_yaml)


def binary_install(name):
    if name == "compose" and os.environ.get("GITHUB_ACTIONS") != "true":
        raise ValueError("Compose plugin installation is restricted to an explicitly selected hosted job")
    version = PINS[name] if name != "trivy" else json.loads((ROOT / "test/packaging/trivy.json").read_text())["version"]
    if name == "shellcheck":
        filename = "shellcheck-v" + version + ".linux.x86_64.tar.xz"
        base = "https://github.com/koalaman/shellcheck/releases/download/v" + version + "/"
        url, manifest, member = base + filename, None, "shellcheck-v" + version + "/shellcheck"
    elif name == "hadolint":
        filename = "hadolint-linux-x86_64"
        base = "https://github.com/hadolint/hadolint/releases/download/v" + version + "/"
        url, manifest, member = base + filename, base + "checksums.sha256", None
    elif name == "helm":
        filename = "helm-v" + version + "-linux-amd64.tar.gz"
        url, manifest, member = "https://get.helm.sh/" + filename, "https://get.helm.sh/" + filename + ".sha256sum", "linux-amd64/helm"
    elif name == "compose":
        filename = "docker-compose-linux-x86_64"
        base = "https://github.com/docker/compose/releases/download/v" + version + "/"
        url, manifest, member = base + filename, base + "checksums.txt", None
    else:
        filename = "trivy_" + version + "_Linux-64bit.tar.gz"
        base = "https://github.com/aquasecurity/trivy/releases/download/v" + version + "/"
        url, manifest, member = base + filename, base + "trivy_" + version + "_checksums.txt", "trivy"
    with tempfile.TemporaryDirectory(prefix=name + "-acquire-", dir=OWNED) as directory:
        temporary = Path(directory)
        archive = temporary / filename
        if name == "shellcheck":
            # ShellCheck publishes no checksum asset. Retain the exact release digest.
            download(url, archive)
            with archive.open("rb") as source:
                if hashlib.file_digest(source, "sha256").hexdigest() != PINS["shellcheckSha256"]:
                    raise ValueError("ShellCheck release checksum mismatch")
        else:
            checked_asset(url, manifest, archive)
        if member:
            unpack(archive, temporary)
        source = temporary / member if member else archive
        destination = OWNED / "bin" / name
        if name == "compose":
            destination = Path.home() / ".docker/cli-plugins/docker-compose"
        destination.parent.mkdir(parents=True, exist_ok=True)
        if destination.exists():
            raise ValueError("tool destination exists; use a fresh job rather than overwrite it")
        shutil.copyfile(source, destination)
        destination.chmod(0o755)
    command = ["docker", "compose", "version", "--short"] if name == "compose" else [destination, "version" if name == "helm" else "--version"]
    actual = run(command, capture=True)
    if (name == "shellcheck" and not re.search(r"(?m)^version: " + re.escape(version) + r"$", actual.strip())) or version not in actual:
        raise ValueError("installed " + name + " version mismatch")
    record_tool(name, actual)


def actionlint_install():
    (OWNED / "bin").mkdir(parents=True, exist_ok=True)
    env = {key: value for key, value in os.environ.items() if not key.startswith(("GO", "CGO_"))}
    go_version = re.search(r"^go ([0-9.]+)$", (ROOT / "go.mod").read_text(), re.MULTILINE)
    if go_version is None:
        raise ValueError("no exact repository Go version")
    env.update(GOBIN=str(OWNED / "bin"), GOSUMDB="sum.golang.org", GOPROXY="https://proxy.golang.org",
               GOTOOLCHAIN="go" + go_version[1], GOENV="off", GOWORK="off", GOFLAGS="-trimpath", GOTELEMETRY="off")
    run(["go", "install", PINS["actionlint"]["module"] + "@v" + PINS["actionlint"]["version"]], env=env)
    actionlint_check(validate=False)


def actionlint_check(*, validate=True):
    executable = OWNED / "bin/actionlint"
    if not executable.is_file():
        raise ValueError("actionlint is missing; run make workflow-tools-install explicitly")
    if validate and shutil.which("shellcheck") is None:
        raise ValueError("ShellCheck is missing; install it explicitly before workflow-check")
    # `go install` can leave the CLI's ldflags-owned display version as devel.
    # The exact checksum-verified module identity remains in Go build metadata.
    env = dict(os.environ, GOTOOLCHAIN="local", GOENV="off", GOWORK="off")
    metadata = run(["go", "version", "-m", executable], capture=True, env=env)
    module = PINS["actionlint"]["module"].removesuffix("/cmd/actionlint")
    expected = r"\tmod\t" + re.escape(module) + r"\tv" + re.escape(PINS["actionlint"]["version"]) + r"\th1:[A-Za-z0-9+/=]+"
    if not re.search(expected, metadata):
        raise ValueError("actionlint version differs from test/ci/pins.json")
    record_tool("actionlint-module", module + " v" + PINS["actionlint"]["version"])
    if validate:
        workflows = sorted(path for path in (ROOT / ".github/workflows").iterdir()
                           if path.suffix in {".yml", ".yaml"})
        if not workflows:
            raise ValueError("no workflow files found")
        run([executable, "-color", "-shellcheck=shellcheck", *workflows])


def install(profile):
    preflight(profile)
    OWNED.mkdir(parents=True, exist_ok=True)
    (OWNED / "bin").mkdir(exist_ok=True)
    publish_path(OWNED / "bin")
    if profile in {"python", "formal"}:
        publish_path(Path(os.environ["JAVA_HOME"]) / "bin")
        if os.environ.get("GITHUB_ENV"):
            with Path(os.environ["GITHUB_ENV"]).open("a") as output:
                output.write("JAVA_HOME=" + os.environ["JAVA_HOME"] + "\n")
    go_install()
    python_install(profile)
    if profile in NODE_PROJECT:
        node_install(NODE_PROJECT[profile])
    if profile in SOURCE_TOOLS:
        binary_install("shellcheck")
    if profile in SOURCE_TOOLS or profile == "docs":
        run(["npm", "install", "--prefix", OWNED / "markdown", "--ignore-scripts", "--no-audit", "--no-fund", "--before=" + PINS["dependencyCutoff"], "--save-exact", "markdownlint-cli@" + PINS["markdownlint-cli"]])
        publish_path(OWNED / "markdown/node_modules/.bin")
        actual_markdown = run(["markdownlint", "--version"], capture=True)
        if actual_markdown.strip() != PINS["markdownlint-cli"]:
            raise ValueError("Markdown CLI version mismatch")
        record_tool("markdownlint-cli", actual_markdown)
    if profile in SOURCE_TOOLS:
        binary_install("hadolint")
        actionlint_install()
    if profile in {"images", "security"}:
        binary_install("trivy")
    if profile in {"images", "charts"}:
        binary_install("hadolint")
    if profile == "charts":
        binary_install("helm")
    if profile in DOCKER - {"charts", "security"}:
        binary_install("compose")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    for command in ("preflight", "install"):
        sub.add_parser(command).add_argument("--profile", choices=tuple(DISK_GIB), required=True)
    sub.add_parser("actionlint-install")
    sub.add_parser("actionlint-check")
    args = parser.parse_args()
    try:
        if args.command == "preflight":
            preflight(args.profile)
        elif args.command == "install":
            install(args.profile)
        elif args.command == "actionlint-install":
            actionlint_install()
        else:
            actionlint_check()
    except (ValueError, OSError, subprocess.SubprocessError, KeyError) as error:
        print("Workflow prerequisites failed: " + str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
