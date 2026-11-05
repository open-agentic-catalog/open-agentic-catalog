"""Explicit, local-only act setup and fixed workflow dispatch."""

import argparse
from contextlib import contextmanager
from datetime import UTC, datetime, timedelta
import fcntl
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import secrets
import shlex
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time

import local_reports
import tools


ROOT = Path(__file__).resolve().parents[2]
OWNED = ROOT / ".tools/ci/act"
PIN = tools.PINS["act"]
SOCKET = "/var/run/docker.sock"
LABEL = "io.open-agentic-catalog.workflow-local"
RUNNER_RECIPE = Path(__file__).with_name("Containerfile.act")
# Exact jobs and inputs, never caller-provided workflow or command text.
SELECTIONS = {
    "lint": ("quality.yml", "source-local", "lint", False, 90),
    "unit": ("quality.yml", "isolated-unit", "unit", False, 45),
    "ui": ("quality.yml", "ui", "ui", False, 60),
    "docs": ("quality.yml", "documentation", "docs", False, 45),
    "charts": ("images.yml", "charts", None, True, 60),
    "image": ("images.yml", "images", None, True, 120),
    "security": ("extended.yml", "extended", "source-security", False, 180),
    "postgres": ("functional.yml", "qualification", "coverage-postgres-check", True, 180),
}
PROBE = """set -eu
test "$(. /etc/os-release; printf '%s' "$VERSION_ID")" = 24.04
for tool in bash git make cc openssl tar xz unzip node docker; do command -v "$tool" >/dev/null; done
python3.12 -I -c 'import ensurepip, ssl, venv; print("Python venv and TLS prerequisites available")'
node -e 'if (Number(process.versions.node.split(".")[0]) < 24) process.exit(1); console.log(process.version)'
docker buildx version
"""


def selection(name, product="backend", *, scan=True, release_candidate=False):
    if name not in SELECTIONS or product not in {"backend", "ui"}:
        raise ValueError("select a fixed local workflow and backend or ui image product")
    workflow, job, suite, docker, minutes = SELECTIONS[name]
    if type(scan) is not bool or type(release_candidate) is not bool:
        raise ValueError("workflow boolean inputs require true or false")
    if (name != "image" and not scan) or (workflow != "images.yml" and release_candidate):
        raise ValueError("scan and release-candidate inputs apply only to image/chart selections")
    inputs = {"upload_artifacts": "false"}
    if suite:
        inputs["suite"] = suite
    if workflow == "images.yml":
        inputs.update(scan="true" if name == "image" and scan else "false", release_candidate=str(release_candidate).lower())
    matrix = ["product:" + product, "arch:amd64"] if name == "image" else []
    return workflow, job, inputs, matrix, docker, minutes * 60


def boolean(value):
    if value not in {"true", "false"}:
        raise ValueError("workflow boolean inputs require exactly true or false")
    return value == "true"


def environment(directory):
    """Do not inherit credentials, service URLs, Git overrides or user config."""
    return {
        "PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
        "HOME": str(directory / "home"), "XDG_CONFIG_HOME": str(directory / "config"),
        "XDG_CACHE_HOME": str(directory / "cache"), "XDG_DATA_HOME": str(directory / "data"),
        "DOCKER_CONFIG": str(directory / "docker"), "DOCKER_HOST": "unix://" + SOCKET,
        "TMPDIR": str(directory / "tmp"), "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8",
        "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_TERMINAL_PROMPT": "0",
        "GIT_OPTIONAL_LOCKS": "0",
        "GOTELEMETRY": "off", "ASTRO_TELEMETRY_DISABLED": "1", "NPM_CONFIG_AUDIT": "false",
        "NPM_CONFIG_FUND": "false", "PYTHONDONTWRITEBYTECODE": "1", "PYTHONOPTIMIZE": "0",
    }


def command(arguments, *, env, cwd, capture=True, timeout=60, check=True):
    return subprocess.run([str(value) for value in arguments], env=env, cwd=cwd, check=check,
                          capture_output=capture, text=True, timeout=timeout)


def git(root, *arguments, env):
    return command(["git", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false",
                    "-C", root, *arguments], env=env, cwd=root).stdout


def source_identity(root, env):
    actual = Path(git(root, "rev-parse", "--show-toplevel", env=env).removesuffix("\n")).resolve()
    if actual != root.resolve():
        raise ValueError("local workflows require the repository root")
    if git(root, "status", "--porcelain=v1", "-z", "--untracked-files=all", env=env):
        raise ValueError("commit staged, unstaged and nonignored untracked changes before running a local workflow")
    commit = git(root, "rev-parse", "HEAD", env=env).strip()
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("local workflows require a committed source identity")
    return commit


def copy_source(root, destination, commit, env):
    command(["git", "-c", "core.hooksPath=/dev/null", "clone", "--quiet", "--no-local",
             "--no-checkout", "--no-recurse-submodules", "--", root, destination], env=env, cwd=root)
    git(destination, "remote", "remove", "origin", env=env)
    shutil.rmtree(destination / ".git/hooks", ignore_errors=True)
    git(destination, "checkout", "--quiet", "--detach", commit, env=env)
    if source_identity(root, env) != commit or source_identity(destination, env) != commit:
        raise ValueError("source changed while preparing the disposable checkout")


@contextmanager
def private_directory(cleanup=None):
    # Do not inherit a user-selected RAM-backed or redirected artifact root.
    directory = Path(tempfile.mkdtemp(prefix="catalog-workflow-local-", dir="/tmp"))
    try:
        env = environment(directory)
        for name in ("home", "config", "cache", "data", "docker", "tmp", "invoke", "actions"):
            (directory / name).mkdir(mode=0o700)
        (directory / "tmp/home").mkdir(mode=0o700)
        yield directory, env
    finally:
        if cleanup is not None and cleanup.get("retain"):
            raise ValueError("workflow cleanup failed; retained owned directory " + str(directory))
        try:
            shutil.rmtree(directory)
        except OSError as error:
            if cleanup is not None:
                cleanup["remainingDirectory"] = str(directory)
            raise ValueError("cannot remove owned workflow directory " + str(directory)) from error


@contextmanager
def serialized():
    descriptor = os.open(OWNED, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError("another local workflow setup or execution is active") from None
        yield
    finally:
        os.close(descriptor)


def require_platform():
    if platform.system() != "Linux" or platform.machine() not in {"x86_64", "AMD64"}:
        raise ValueError("local workflows currently require Linux AMD64")
    if sys.version_info[:2] != (3, 12):
        raise ValueError("local workflows require Python 3.12")


def require_age(now):
    for key in ("publishedAt", "runnerCreatedAt"):
        if datetime.fromisoformat(PIN[key].replace("Z", "+00:00")) > now - timedelta(days=14):
            raise ValueError("the selected act tool or runner does not meet the 14-day rule")


def check_act(env, cwd):
    executable = OWNED / "act"
    receipt = OWNED / "installation.json"
    if executable.is_symlink() or receipt.is_symlink() or not executable.is_file() or not receipt.is_file():
        raise ValueError("act is missing; run make workflow-local-tools-install explicitly")
    installed = json.loads(receipt.read_text())
    if (installed.get("archiveSha256") != PIN["sha256"] or installed.get("version") != PIN["version"]
            or installed.get("binarySha256") != hashlib.sha256(executable.read_bytes()).hexdigest()):
        raise ValueError("act installation is stale or changed; rerun workflow-local-tools-install")
    actual = command([executable, "--version"], env=env, cwd=cwd).stdout.strip()
    if actual != "act version " + PIN["version"]:
        raise ValueError("installed act version differs from the selected pin")
    return executable


def docker(arguments, env, cwd, **kwargs):
    return command(["docker", "--host", "unix://" + SOCKET, *arguments], env=env, cwd=cwd, **kwargs)


def runner_reference():
    recipe = hashlib.sha256(RUNNER_RECIPE.read_bytes()).hexdigest()
    return "open-agentic-catalog-act:" + PIN["version"] + "-" + str(os.getuid()) + "-" + str(os.getgid()) + "-" + recipe[:12]


def check_runner(env, cwd):
    installed = json.loads((OWNED / "installation.json").read_text())
    expected = {"baseRunner": PIN["runner"], "runnerRecipeSha256": hashlib.sha256(RUNNER_RECIPE.read_bytes()).hexdigest(),
                "runnerUID": os.getuid(), "runnerGID": os.getgid(), "runnerReference": runner_reference()}
    if any(installed.get(key) != value for key, value in expected.items()):
        raise ValueError("local runner setup is stale; run make workflow-local-tools-install explicitly")
    result = docker(["image", "inspect", runner_reference()], env, cwd, check=False)
    if result.returncode:
        raise ValueError("pinned runner image is missing; run make workflow-local-tools-install explicitly")
    images = json.loads(result.stdout)
    if (len(images) != 1 or images[0].get("Architecture") != "amd64" or images[0].get("Os") != "linux"
            or images[0].get("Id") != installed.get("runnerId")
            or images[0].get("Config", {}).get("User") != str(os.getuid()) + ":" + str(os.getgid())):
        raise ValueError("local runner identity or platform differs from the selected pin")
    return images[0]["Id"]


def install():
    require_platform()
    if os.getuid() == 0 or os.getgid() == 0:
        raise ValueError("local workflow setup requires a non-root host user and group")
    require_age(datetime.now(UTC))
    for path in (ROOT / ".tools", ROOT / ".tools/ci", OWNED):
        if path.is_symlink():
            raise ValueError("refusing redirected workflow tool directory")
        path.mkdir(exist_ok=True)
    if shutil.disk_usage(ROOT).free < 8 << 30:
        raise ValueError("local workflow setup needs 8 GiB free; do not prune unrelated data")
    with serialized(), private_directory() as (directory, env):
        archive = directory / "act_Linux_x86_64.tar.gz"
        tools.download("https://github.com/nektos/act/releases/download/v" + PIN["version"] + "/" + archive.name, archive)
        if hashlib.sha256(archive.read_bytes()).hexdigest() != PIN["sha256"]:
            raise ValueError("act release archive checksum mismatch")
        unpacked = directory / "unpacked"
        unpacked.mkdir()
        tools.unpack(archive, unpacked)
        for name in ("act", "LICENSE"):
            source = unpacked / name
            if source.is_symlink() or not source.is_file():
                raise ValueError("act archive lacks its regular executable or license")
            destination = OWNED / name
            if destination.is_symlink():
                raise ValueError("refusing redirected workflow tool file")
            shutil.copyfile(source, destination)
            destination.chmod(0o755 if name == "act" else 0o644)
        receipt = {"version": PIN["version"], "archiveSha256": PIN["sha256"],
                   "binarySha256": hashlib.sha256((OWNED / "act").read_bytes()).hexdigest()}
        receipt_path = OWNED / "installation.json"
        if receipt_path.is_symlink():
            raise ValueError("refusing redirected workflow installation receipt")
        receipt_path.write_text(json.dumps(receipt, indent=2) + "\n")
        check_act(env, directory / "invoke")
        docker(["pull", "--platform", "linux/amd64", PIN["runner"]], env, directory, capture=False, timeout=1200)
        result = docker(["run", "--rm", "--pull=never", "--network=none", "--read-only",
                         "--entrypoint", "/bin/bash", PIN["runner"], "-c", PROBE], env, directory)
        docker(["build", "--pull=false", "--network=none", "--build-arg", "BASE=" + PIN["runner"],
                "--build-arg", "LOCAL_UID=" + str(os.getuid()), "--build-arg", "LOCAL_GID=" + str(os.getgid()),
                "--file", RUNNER_RECIPE, "--tag", runner_reference(), directory / "invoke"], env, directory, capture=False, timeout=1200)
        metadata = json.loads(docker(["image", "inspect", runner_reference()], env, directory).stdout)[0]
        receipt.update(baseRunner=PIN["runner"], runnerId=metadata["Id"], runnerReference=runner_reference(),
                       runnerUID=os.getuid(), runnerGID=os.getgid(), runnerRecipeSha256=hashlib.sha256(RUNNER_RECIPE.read_bytes()).hexdigest(),
                       prerequisites=result.stdout.strip())
        docker(["run", "--rm", "--pull=never", "--network=none", "--tmpfs", "/run/sudo",
                "--entrypoint", "/bin/bash", runner_reference(), "-ceu", "test \"$(id -u)\" = \"$1\"; sudo -n true", "--", str(os.getuid())], env, directory)
        receipt_path.write_text(json.dumps(receipt, indent=2) + "\n")
        check_runner(env, directory)
        print(json.dumps(receipt, indent=2))


def arguments(name, product, directory, commit, run_id, *, scan=True, release_candidate=False, docker_gid=None):
    local_reports.run_identifier(run_id)
    workflow, job, inputs, matrix, needs_docker, _ = selection(name, product, scan=scan, release_candidate=release_candidate)
    source = directory / "source"
    result = [str(OWNED / "act"), "workflow_dispatch", "--workflows", str(source / ".github/workflows" / workflow),
              "--directory", str(source), "--job", job, "--platform", "ubuntu-24.04=" + runner_reference(),
              "--container-architecture", "linux/amd64", "--pull=false", "--bind", "--rm",
              "--no-cache-server", "--artifact-server-path", "", "--action-cache-path", str(directory / "actions"),
              "--secret-file", "/dev/null", "--var-file", "/dev/null", "--env-file", "/dev/null",
              "--input-file", "/dev/null", "--secret", "GITHUB_TOKEN=", "--concurrent-jobs", "1",
              "--container-daemon-socket", SOCKET if needs_docker else "-", "--network", "host"]
    for key, value in inputs.items():
        result += ["--input", key + "=" + value]
    for item in matrix:
        result += ["--matrix", item]
    settings = {"ACT": "true", "GITHUB_REPOSITORY": "open-agentic-catalog/open-agentic-catalog",
                "GITHUB_REPOSITORY_OWNER": "open-agentic-catalog", "GITHUB_SHA": commit,
                "GITHUB_RUN_ID": run_id, "GITHUB_RUN_ATTEMPT": "1", "GOTELEMETRY": "off",
                "ASTRO_TELEMETRY_DISABLED": "1", "NPM_CONFIG_AUDIT": "false", "NPM_CONFIG_FUND": "false",
                "PYTHONDONTWRITEBYTECODE": "1", "PYTHONOPTIMIZE": "0", "TMPDIR": str(directory / "tmp")}
    settings.update(HOME=str(directory / "tmp/home"), XDG_CACHE_HOME=str(directory / "tmp/home/.cache"),
                    DOCKER_CONFIG=str(directory / "tmp/home/.docker"))
    for key, value in settings.items():
        result += ["--env", key + "=" + value]
    options = ["--label", LABEL + "=" + run_id, "--mount",
               "type=bind,src=" + str(directory / "tmp") + ",dst=" + str(directory / "tmp")]
    if needs_docker:
        if not isinstance(docker_gid, int) or isinstance(docker_gid, bool) or docker_gid < 0:
            raise ValueError("Docker selections require the host socket group")
        options += ["--group-add", str(docker_gid)]
    result += ["--container-options", shlex.join(options)]
    return result


def execute(arguments_, env, cwd, timeout, resources=None):
    deadline = time.monotonic() + timeout
    process = subprocess.Popen(arguments_, env=env, cwd=cwd, start_new_session=True)
    previous = {}
    def interrupted(signum, _frame):
        raise InterruptedError("local workflow interrupted by signal " + str(signum))
    try:
        for signum in (signal.SIGINT, signal.SIGTERM):
            previous[signum] = signal.signal(signum, interrupted)
        if resources is not None:
            resources.deadline = deadline
        while True:
            if resources is not None:
                resources.observe()
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise subprocess.TimeoutExpired(arguments_, timeout)
            try:
                code = process.wait(timeout=min(0.5, remaining))
                if time.monotonic() >= deadline:
                    return {"status": "timed_out", "exitCode": code}
                return {"status": "passed" if code == 0 else "failed", "exitCode": code}
            except subprocess.TimeoutExpired:
                if time.monotonic() >= deadline:
                    raise
    except (InterruptedError, subprocess.TimeoutExpired) as error:
        return {"status": "interrupted" if isinstance(error, InterruptedError) else "timed_out", "exitCode": None}
    except (OSError, ValueError, subprocess.SubprocessError):
        return {"status": "failed", "exitCode": None, "failureStage": "supervision"}
    finally:
        for signum, handler in previous.items():
            signal.signal(signum, handler)
        if resources is not None:
            resources.deadline = None
        if process.poll() is None:
            # Give the harness inside the owned runner time to execute its traps.
            if resources is not None:
                try:
                    resources.signal_jobs()
                except (OSError, ValueError, subprocess.SubprocessError):
                    resources.errors.append("runner-interruption")
            else:
                os.killpg(process.pid, signal.SIGINT)
            try:
                process.wait(timeout=30)
            except subprocess.TimeoutExpired:
                os.killpg(process.pid, signal.SIGINT)
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    os.killpg(process.pid, signal.SIGKILL)
                    process.wait(timeout=10)


class DockerResources:
    """Track the single selected runner and its actual volume identities."""

    def __init__(self, env, directory, run_id, image_tag=None):
        self.env, self.directory, self.run_id = env, directory, run_id
        self.image_tag = image_tag
        self.baseline = set()
        self.owned_cache = False
        self.parent = None
        self.volumes = {}
        self.images = []
        self.errors = []
        self.deadline = None
        self.prepared = False
        self.active = False
        self.reason = "owned Docker cleanup failed"

    def call(self, args, **kwargs):
        if self.deadline is not None:
            remaining = self.deadline - time.monotonic()
            if remaining <= 0:
                raise subprocess.TimeoutExpired(args, 0)
            kwargs["timeout"] = min(kwargs.get("timeout", 60), remaining)
        return docker(args, self.env, self.directory, **kwargs)

    def prepare(self):
        if self.image_tag and self.call(["image", "ls", "--filter", "reference=" + self.image_tag, "--format", "{{.ID}}"]).stdout.strip():
            raise ValueError("refusing a pre-existing local workflow image tag")
        self.baseline = set(self.call(["volume", "ls", "--format", "{{.Name}}" ]).stdout.splitlines())
        if any(name.startswith("act-") and name != "act-toolcache" for name in self.baseline):
            raise ValueError("pre-existing act job volumes must be handled before execution")
        if self.call(["ps", "-a", "--filter", "label=" + LABEL + "=" + self.run_id, "--format", "{{.ID}}"]).stdout.strip():
            raise ValueError("pre-existing Docker resources use this workflow run identifier")
        self.prepared = True
        self.owned_cache = "act-toolcache" not in self.baseline
        if self.owned_cache:
            self.call(["volume", "create", "--label", LABEL + "=" + self.run_id, "act-toolcache"])
            metadata = json.loads(self.call(["volume", "inspect", "act-toolcache"]).stdout)
            if len(metadata) != 1 or (metadata[0].get("Labels") or {}).get(LABEL) != self.run_id:
                raise ValueError("act tool-cache ownership changed; do not remove the existing volume")

    def observe(self):
        if self.parent is not None:
            return
        ids = self.call(["ps", "-a", "--filter", "label=" + LABEL + "=" + self.run_id, "--format", "{{.ID}}"]).stdout.splitlines()
        for identifier in ids:
            result = self.call(["inspect", identifier], check=False)
            if result.returncode:
                continue  # A short-lived, already-removed container needs no cleanup.
            metadata = json.loads(result.stdout)[0]
            mounts = metadata.get("Mounts", [])
            source = str(self.directory / "source")
            if not any(item.get("Type") == "bind" and item.get("Source") == source and item.get("Destination") == source for item in mounts):
                continue
            if (metadata.get("Config", {}).get("Labels") or {}).get(LABEL) != self.run_id:
                raise ValueError("runner container ownership changed")
            name = metadata["Name"].removeprefix("/")
            if not name.startswith("act-"):
                raise ValueError("unexpected runner container name")
            self.parent = identifier
            for item in mounts:
                volume = item.get("Name")
                if item.get("Type") == "volume" and volume in {name, name + "-env"}:
                    if volume in self.baseline:
                        raise ValueError("runner reused a pre-existing job volume")
                    self.volumes[volume] = json.loads(self.call(["volume", "inspect", volume]).stdout)[0]

    def signal_jobs(self):
        self.observe()
        if self.parent is not None:
            # This fixed command signals only processes in the owned PID namespace.
            self.call(["exec", self.parent, "/bin/bash", "-c", "kill -TERM -1"], check=False)

    def cleanup(self):
        try:
            self.observe()
            ids = self.call(["ps", "-a", "--filter", "label=" + LABEL + "=" + self.run_id, "--format", "{{.ID}}"]).stdout.splitlines()
            for identifier in ids:
                if not re.fullmatch(r"[0-9a-f]{12,64}", identifier):
                    raise ValueError("invalid invocation-owned container identity")
                self.call(["rm", "--force", identifier])
            current = set(self.call(["volume", "ls", "--format", "{{.Name}}" ]).stdout.splitlines())
            for name, original in self.volumes.items():
                if name in current:
                    actual = json.loads(self.call(["volume", "inspect", name]).stdout)[0]
                    if actual != original:
                        raise ValueError("job volume identity changed; refusing cleanup")
                    self.call(["volume", "rm", name])
            if self.owned_cache and "act-toolcache" in current:
                metadata = json.loads(self.call(["volume", "inspect", "act-toolcache"]).stdout)
                if len(metadata) != 1 or (metadata[0].get("Labels") or {}).get(LABEL) != self.run_id:
                    raise ValueError("act tool-cache ownership changed during execution; refusing cleanup")
                self.call(["volume", "rm", "act-toolcache"])
            unknown = {name for name in current - self.baseline - self.volumes.keys() if name.startswith("act-") and name != "act-toolcache"}
            if unknown:
                raise ValueError("unidentified act volumes remain; refusing unrelated deletion")
        except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
            self.errors.append("docker-resource-cleanup")
            if isinstance(error, ValueError):
                self.reason = str(error)
        try:
            if self.image_tag and self.active:
                present = self.call(["image", "ls", "--filter", "reference=" + self.image_tag, "--format", "{{.ID}}"]).stdout.strip()
                if present:
                    result = self.call(["image", "inspect", self.image_tag])
                    metadata = json.loads(result.stdout)[0]
                    self.images.append({"tag": self.image_tag, "id": metadata["Id"], "os": metadata["Os"], "architecture": metadata["Architecture"]})
                    self.call(["image", "rm", self.image_tag])
        except (OSError, ValueError, KeyError, subprocess.SubprocessError):
            self.errors.append("image-tag-cleanup")
        if self.errors:
            raise ValueError(self.reason)


@contextmanager
def docker_resources(env, cwd, run_id, image_tag=None, *, resources=None):
    if resources is None:
        resources = DockerResources(env, cwd, run_id, image_tag)
    try:
        resources.prepare()
        resources.active = True
        yield resources
    finally:
        if resources.prepared:
            resources.cleanup()


def repair_ownership(directory, env):
    def failed(error):
        raise error
    try:
        needs_repair = False
        for parent, directories, files in os.walk(directory, onerror=failed, followlinks=False):
            for name in [".", *directories, *files]:
                metadata = (Path(parent) / name).lstat()
                if metadata.st_uid != os.getuid() or (stat.S_ISDIR(metadata.st_mode) and metadata.st_mode & 0o700 != 0o700):
                    needs_repair = True
                    break
            if needs_repair:
                break
    except PermissionError:
        needs_repair = True
    if needs_repair:
        docker(["run", "--rm", "--pull=never", "--network=none", "--read-only", "--user", "0:0",
                "--cap-drop=ALL", "--cap-add=CHOWN", "--cap-add=DAC_OVERRIDE", "--cap-add=FOWNER",
                "--mount", "type=bind,src=" + str(directory) + ",dst=/owned", "--entrypoint", "/bin/bash",
                PIN["runner"], "-ceu", 'chmod -R u+rwX /owned; chown -hR --no-dereference "$1:$2" /owned',
                "--", str(os.getuid()), str(os.getgid())], env, directory, timeout=120)


def run(name, product, *, scan=True, release_candidate=False):
    require_platform()
    selected = selection(name, product, scan=scan, release_candidate=release_candidate)
    if not OWNED.is_dir() or OWNED.is_symlink():
        raise ValueError("local tools are missing; run make workflow-local-tools-install explicitly")
    destination = None
    result = {"execution": {"status": "setup_failed", "exitCode": None}, "cleanup": "passed", "cleanupErrors": [],
              "reportStatus": "partial", "files": [], "missingReports": [], "images": []}
    cleanup = {}
    resources = None
    with serialized():
        try:
            with private_directory(cleanup) as (directory, env):
                check_act(env, directory / "invoke")
                commit = source_identity(ROOT, env)
                runner = check_runner(env, directory)
                existing = docker(["ps", "-a", "--format", "{{.Names}}"], env, directory).stdout.splitlines()
                if any(value.startswith("act-") for value in existing):
                    raise ValueError("pre-existing act containers must finish before local workflow execution")
                copy_source(ROOT, directory / "source", commit, env)
                run_id = str(10**17 + secrets.randbelow(8 * 10**17))
                destination = local_reports.create_destination(ROOT, run_id)
                result.update(runId=run_id, commit=commit, selection=name, product=product, inputs=selected[2], runnerId=runner)
                argv = arguments(name, product, directory, commit, run_id, scan=scan, release_candidate=release_candidate,
                                 docker_gid=Path(SOCKET).stat().st_gid if selected[4] else None)
                image = ("open-agentic-catalog" + ("-ui" if product == "ui" else "") + "-ci:" + run_id + "-1-amd64") if name == "image" else None
                print("Local workflow " + name + "; commit " + commit + "; run " + run_id, flush=True)
                # Keep setup/cleanup evidence even when __enter__ fails before yielding.
                resources = DockerResources(env, directory, run_id, image)
                try:
                    with docker_resources(env, directory, run_id, image, resources=resources) as resources:
                        result["execution"] = execute(argv, env, directory / "invoke", selected[5], resources)
                finally:
                    if resources is not None:
                        result["images"] = resources.images
                        result["cleanupErrors"].extend(resources.errors)
                    if result["cleanupErrors"]:
                        cleanup.update(retain=True, remainingDirectory=str(directory))
                    else:
                        try:
                            repair_ownership(directory, env)
                        except (OSError, ValueError, subprocess.SubprocessError):
                            result["cleanupErrors"].append("workspace-ownership")
                    try:
                        result["files"], result["missingReports"] = local_reports.collect(directory / "source", destination, name, product, run_id)
                        result["reportStatus"] = "partial" if result["missingReports"] else "complete"
                    except local_reports.CollectionError as error:
                        result["files"], result["missingReports"] = error.files, error.missing
                        result["reportStatus"] = "failed"
                        print("Report collection failed: " + str(error), file=sys.stderr)
                    except (OSError, ValueError) as error:
                        result["reportStatus"] = "failed"
                        message = str(error) if isinstance(error, ValueError) else "cannot read a selected report"
                        print("Report collection failed: " + message, file=sys.stderr)
        except (OSError, ValueError, subprocess.SubprocessError) as error:
            if destination is None:
                raise
            message = str(error) if isinstance(error, ValueError) else "required subprocess or filesystem operation failed"
            print("Local workflow failed: " + message, file=sys.stderr)
            if cleanup.get("remainingDirectory"):
                result["cleanupErrors"].append("workspace-removal")
                result["remainingDirectory"] = cleanup["remainingDirectory"]
        result["cleanup"] = "failed" if result["cleanupErrors"] else "passed"
        if name == "image" and result["execution"]["status"] == "passed" and not result["images"]:
            result["execution"] = {"status": "failed", "exitCode": None, "failureStage": "missing-built-image"}
        passed = result["execution"]["status"] == "passed" and result["cleanup"] == "passed" and result["reportStatus"] == "complete"
        result["status"] = "passed" if passed else "failed"
        local_reports.write_result(destination, result)
        print("Local reports: " + str(destination), flush=True)
        print("Execution: " + result["execution"]["status"] + "; reports: " + result["reportStatus"] + "; cleanup: " + result["cleanup"], flush=True)
        if result.get("remainingDirectory"):
            print("Retained owned work: " + result["remainingDirectory"], file=sys.stderr)
        if passed:
            return 0
        return result["execution"]["exitCode"] if result["execution"]["exitCode"] not in (None, 0) else 1


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("install")
    sub.add_parser("list")
    sub.add_parser("run").add_argument("selection", choices=tuple(SELECTIONS))
    args = parser.parse_args()
    try:
        if args.command == "install":
            install()
        elif args.command == "list":
            for name in SELECTIONS:
                workflow, job, inputs, _, _, _ = selection(name)
                print("workflow-local-" + name + ": " + workflow + " / " + job + " / " + json.dumps(inputs, sort_keys=True))
        else:
            return run(args.selection, os.environ.get("WORKFLOW_LOCAL_PRODUCT", "backend"),
                       scan=boolean(os.environ.get("WORKFLOW_LOCAL_SCAN", "true")),
                       release_candidate=boolean(os.environ.get("WORKFLOW_LOCAL_RELEASE_CANDIDATE", "false")))
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        # Subprocess stdout/stderr may contain fixture diagnostics. Do not repeat it here.
        message = "required subprocess failed" if isinstance(error, subprocess.CalledProcessError) else str(error)
        print("Local workflow failed: " + message, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
