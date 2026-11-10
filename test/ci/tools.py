"""Explicit prerequisites and validation for Linux verification workflows.

Application tools keep their own installers and native locks. Raw archives are
checked against retained reviewed digests before extraction or execution.
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


def archive_checksum(name, version, filename):
    """Require the reviewed bytes to match the owning version and exact asset."""
    pin = PINS.get("archives", {}).get(name, {})
    checksum = pin.get("sha256", "")
    if pin.get("version") != version or pin.get("filename") != filename or not isinstance(checksum, str) or not re.fullmatch(r"[a-f0-9]{64}", checksum):
        raise ValueError("no reviewed archive for " + name + " " + version + " " + filename)
    return checksum


def checked_asset(url, expected, destination):
    """Check retained expectations; never obtain trust from a new manifest."""
    if not isinstance(expected, str) or not re.fullmatch(r"[a-f0-9]{64}", expected):
        raise ValueError("missing reviewed archive checksum")
    download(url, destination)
    with destination.open("rb") as source:
        if hashlib.file_digest(source, "sha256").hexdigest() != expected:
            raise ValueError("release checksum mismatch")


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
    checksum = archive_checksum("go", version, filename)
    target = OWNED / ("go-" + version)
    if target.exists() or target.is_symlink():
        raise ValueError("owned Go installation exists; use a fresh job rather than overwrite it")
    with tempfile.TemporaryDirectory(prefix="go-acquire-", dir=OWNED) as directory:
        temporary = Path(directory)
        archive = temporary / filename
        checked_asset("https://go.dev/dl/" + filename, checksum, archive)
        unpack(archive, temporary)
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
    checksum = json.loads((ROOT / "test/sdk/node-runtimes.json").read_text()).get(version, {}).get(filename, "")
    if not re.fullmatch(r"[a-f0-9]{64}", checksum):
        raise ValueError("no reviewed Node archive for " + version)
    target = OWNED / filename.removesuffix(".tar.gz")
    if target.exists() or target.is_symlink():
        raise ValueError("owned Node installation exists; use a fresh job")
    aliases = OWNED / "bin"
    if any((aliases / name).exists() or (aliases / name).is_symlink() for name in ("npm", "npx")):
        raise ValueError("owned npm command exists; use a fresh job")
    with tempfile.TemporaryDirectory(prefix="node-acquire-", dir=OWNED) as directory:
        temporary = Path(directory)
        archive = temporary / filename
        checked_asset(base + filename, checksum, archive)
        unpack(archive, temporary)
        (temporary / target.name).rename(target)
    publish_path(target / "bin")
    actual_node = run(["node", "--version"], capture=True).strip()
    if actual_node != "v" + version:
        raise ValueError("Node version mismatch")
    # Keep separately selected npm outside the reviewed Node distribution.
    sys.path.insert(0, str(ROOT / "test/sdk"))
    import node_runtime  # noqa: E402
    executable = node_runtime.install_npm(target / "bin/node", expected_npm, OWNED, env=dict(os.environ))
    aliases.mkdir(exist_ok=True)
    for name, entry in (("npm", executable), ("npx", executable.with_name("npx-cli.js"))):
        alias = aliases / name
        if alias.exists() or alias.is_symlink():
            raise ValueError("owned npm command exists; use a fresh job")
        alias.symlink_to(entry)
    publish_path(aliases)
    actual_npm = run(["npm", "--version"], capture=True).strip()
    if actual_npm != expected_npm:
        raise ValueError("npm version mismatch")
    record_tool("node", actual_node)
    record_tool("npm", actual_npm)


def python_install(profile):
    venv = OWNED / "python"
    if venv.exists() or venv.is_symlink():
        raise ValueError("owned Python bootstrap exists; use a fresh environment rather than skip hash verification")
    uv = tomllib.loads((ROOT / "pyproject.toml").read_text())["tool"]["uv"]["required-version"]
    env = {key: value for key, value in os.environ.items() if not key.upper().startswith("PIP_") and key not in ("PYTHONPATH", "PYTHONHOME", "VIRTUAL_ENV")}
    env.update(PIP_CONFIG_FILE=os.devnull, PIP_NO_INPUT="1")
    try:
        run([sys.executable, "-I", "-m", "venv", venv], env=env)
        run([venv / "bin/python", "-I", "-m", "pip", "install", "--disable-pip-version-check",
             "--index-url", "https://pypi.org/simple", "--require-hashes", "--only-binary=:all:",
             "-r", ROOT / "test/ci/uv/requirements.txt"], env=env)
        installed = run([venv / "bin/python", "-I", "-c", "import importlib.metadata; print(importlib.metadata.version('uv'))"], capture=True, env=env)
        if installed.strip() != uv.removeprefix("=="):
            raise ValueError("uv bootstrap metadata differs from pyproject.toml")
        actual_uv = run([venv / "bin/uv", "--version"], capture=True, env=env)
        if actual_uv.split()[:2] != ["uv", uv.removeprefix("==")]:
            raise ValueError("uv version mismatch")
    except BaseException:
        if venv.is_dir() and not venv.is_symlink():
            shutil.rmtree(venv)
        raise
    publish_path(venv / "bin")
    record_tool("uv", actual_uv)


def markdown_install():
    """Install the maintained native CLI lock without resolving a new graph."""
    project = ROOT / "test/ci/markdown"
    package = json.loads((project / "package.json").read_text())
    if not package.get("private") or package.get("dependencies", {}).get("markdownlint-cli") != PINS["markdownlint-cli"]:
        raise ValueError("Markdown CLI pin differs from its maintained manifest")
    lock = json.loads((project / "package-lock.json").read_text())
    for name, item in lock["packages"].items():
        if name and (not item.get("resolved", "").startswith("https://registry.npmjs.org/") or not item.get("integrity", "").startswith("sha512-")):
            raise ValueError("Markdown CLI lock has an unexpected source or missing integrity")
    OWNED.mkdir(parents=True, exist_ok=True)
    target = OWNED / "markdown"
    if target.exists() or target.is_symlink():
        raise ValueError("owned Markdownlint installation exists; select a fresh owned destination")
    env = {key: value for key, value in os.environ.items() if not key.lower().startswith(("npm_config_", "node_"))}
    with tempfile.TemporaryDirectory(prefix="markdown-acquire-", dir=OWNED) as directory:
        stage = Path(directory)
        for name in ("package.json", "package-lock.json"):
            shutil.copyfile(project / name, stage / name)
        empty = stage / "empty.npmrc"
        empty.write_text("")
        env.update(NPM_CONFIG_USERCONFIG=os.devnull, NPM_CONFIG_GLOBALCONFIG=str(empty),
                   NPM_CONFIG_REGISTRY="https://registry.npmjs.org/", NPM_CONFIG_IGNORE_SCRIPTS="true",
                   NPM_CONFIG_AUDIT="false", NPM_CONFIG_FUND="false", NPM_CONFIG_UPDATE_NOTIFIER="false")
        run(["npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund"], cwd=stage, env=env)
        command = stage / "node_modules/.bin/markdownlint"
        actual = run([command, "--version"], capture=True, env=env)
        if actual.strip() != PINS["markdownlint-cli"]:
            raise ValueError("Markdown CLI version mismatch")
        stage.rename(target)
    publish_path(target / "node_modules/.bin")
    record_tool("markdownlint-cli", actual)


def binary_install(name):
    if name == "compose" and os.environ.get("GITHUB_ACTIONS") != "true":
        raise ValueError("Compose plugin installation is restricted to an explicitly selected hosted job")
    version = PINS[name] if name != "trivy" else json.loads((ROOT / "test/packaging/trivy.json").read_text())["version"]
    if name == "shellcheck":
        filename = "shellcheck-v" + version + ".linux.x86_64.tar.xz"
        base = "https://github.com/koalaman/shellcheck/releases/download/v" + version + "/"
        url, member = base + filename, "shellcheck-v" + version + "/shellcheck"
    elif name == "hadolint":
        filename = "hadolint-linux-x86_64"
        base = "https://github.com/hadolint/hadolint/releases/download/v" + version + "/"
        url, member = base + filename, None
    elif name == "helm":
        filename = "helm-v" + version + "-linux-amd64.tar.gz"
        url, member = "https://get.helm.sh/" + filename, "linux-amd64/helm"
    elif name == "compose":
        filename = "docker-compose-linux-x86_64"
        base = "https://github.com/docker/compose/releases/download/v" + version + "/"
        url, member = base + filename, None
    elif name == "trivy":
        filename = "trivy_" + version + "_Linux-64bit.tar.gz"
        base = "https://github.com/aquasecurity/trivy/releases/download/v" + version + "/"
        url, member = base + filename, "trivy"
    else:
        raise ValueError("unknown binary tool")
    checksum = archive_checksum(name, version, filename)
    destination = Path.home() / ".docker/cli-plugins/docker-compose" if name == "compose" else OWNED / "bin" / name
    if destination.exists() or destination.is_symlink():
        raise ValueError("tool destination exists; use a fresh job rather than overwrite it")
    with tempfile.TemporaryDirectory(prefix=name + "-acquire-", dir=OWNED) as directory:
        temporary = Path(directory)
        archive = temporary / filename
        checked_asset(url, checksum, archive)
        if member:
            unpack(archive, temporary)
        source = temporary / member if member else archive
        destination.parent.mkdir(parents=True, exist_ok=True)
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
        markdown_install()
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
