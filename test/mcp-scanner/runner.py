"""Explicit installation and bounded, network-disabled scanner processes."""

import argparse
from contextlib import contextmanager
from datetime import datetime, timedelta, timezone
import importlib.util
import json
import os
from pathlib import Path
import platform
import re
import secrets
import selectors
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time


ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent
EXAMPLE = ROOT / "examples/mcp-scanner"
OWNED = HERE / ".tools"
PINS = json.loads((HERE / "pins.json").read_text())
LABEL = "io.open-agentic-catalog.mcp-scanner"
spec = importlib.util.spec_from_file_location("catalog_mcp_scan", EXAMPLE / "catalog_mcp_scan.py")
core = importlib.util.module_from_spec(spec)
spec.loader.exec_module(core)


class SetupError(ValueError):
    """Fixed first-party prerequisite messages, safe for the command line."""


def require_host():
    if platform.system() != "Linux" or platform.machine() not in {"x86_64", "AMD64"} or os.getuid() == 0:
        raise SetupError("Scanner commands require a non-root Linux AMD64 host.")


def fingerprint():
    paths = [HERE / name for name in ("pins.json", "requirements.in", "constraints.txt", "requirements.txt", "Containerfile", "runner.py")]
    paths += [EXAMPLE / name for name in ("catalog_mcp_scan.py", "worker.py")]
    paths += [EXAMPLE / "licenses/libyara-COPYING"]
    return core.digest(core.encoded({str(p.relative_to(ROOT)): core.digest(p.read_bytes()) for p in paths}))


def environment(directory):
    return {"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "HOME": str(directory),
            "DOCKER_CONFIG": str(directory / "docker"), "DOCKER_HOST": "unix:///var/run/docker.sock",
            "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8", "PYTHONDONTWRITEBYTECODE": "1", "GOTELEMETRY": "off"}


@contextmanager
def private_directory():
    with tempfile.TemporaryDirectory(prefix="catalog-mcp-scanner-", dir="/tmp") as name:
        directory = Path(name)
        (directory / "docker").mkdir(mode=0o700)
        yield directory, environment(directory)


def docker(arguments, env, *, seconds=30, capture=True):
    return subprocess.run(["docker", "--host", "unix:///var/run/docker.sock", *map(str, arguments)],
                          env=env, check=False, capture_output=capture, text=True, timeout=seconds)


def require_success(result):
    if result.returncode:
        raise ValueError("required local Docker operation failed")
    return result.stdout


def image_inspection(reference, env):
    data = json.loads(require_success(docker(["image", "inspect", reference], env)))
    if (not isinstance(data, list) or len(data) != 1 or not isinstance(data[0], dict)
            or data[0].get("Os") != "linux" or data[0].get("Architecture") != "amd64"
            or not isinstance(data[0].get("Id"), str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", data[0]["Id"])
            or not isinstance(data[0].get("Config"), dict)):
        raise ValueError("scanner image must be Linux AMD64")
    return data[0]


def require_installed(env):
    require_host()
    receipt = OWNED / "installed.json"
    if OWNED.is_symlink() or receipt.is_symlink() or not receipt.is_file():
        raise SetupError("Scanner tools are missing. Run make mcp-scanner-tools-install.")
    installed = core.decode(read_file(receipt, core.REPORT_LIMIT), core.REPORT_LIMIT)
    if not isinstance(installed, dict) or installed.get("fingerprint") != fingerprint() or installed.get("version") != PINS["scanner"]:
        raise SetupError("Scanner tools are stale. Run make mcp-scanner-tools-install again.")
    if not isinstance(installed.get("imageId"), str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", installed["imageId"]):
        raise ValueError("scanner image identity is missing")
    actual = image_inspection(installed["imageId"], env)
    if actual["Id"] != installed["imageId"] or actual.get("Config", {}).get("User") != "65532:65532":
        raise ValueError("scanner image identity or non-root configuration changed")
    if installed.get("rulesSha256") != PINS["rulesSha256"]:
        raise ValueError("scanner rules identity is missing")
    return installed


def install():
    require_host()
    published = datetime.fromisoformat(PINS["scannerPublishedAt"].replace("Z", "+00:00"))
    if published > datetime.now(timezone.utc) - timedelta(days=14) and not PINS["scannerAgeExceptionApproved"]:
        raise SetupError("MCP Scanner 4.8.7 needs approval for its exact 14-day age exception before installation.")
    if OWNED.is_symlink():
        raise ValueError("refusing redirected scanner installation")
    OWNED.mkdir(mode=0o700, exist_ok=True)
    with private_directory() as (directory, env):
        for name in ("Containerfile", "requirements.txt"):
            shutil.copy2(HERE / name, directory / name)
        for name in ("catalog_mcp_scan.py", "worker.py"):
            shutil.copy2(EXAMPLE / name, directory / name)
        shutil.copy2(EXAMPLE / "licenses/libyara-COPYING", directory / "libyara-COPYING")
        reference = "open-agentic-catalog-mcp-scanner:" + PINS["scanner"] + "-" + fingerprint()[:12]
        require_success(docker(["pull", "--platform", "linux/amd64", PINS["baseImage"]], env, seconds=600))
        require_success(docker(["build", "--pull=false", "--platform", "linux/amd64", "--tag", reference, directory], env, seconds=900, capture=False))
        image = image_inspection(reference, env)
        observed = image_identity(image["Id"], env)
        expected = dict(re.findall(r"(?m)^([A-Za-z0-9_.-]+)==([^ ;\\\n]+)", (HERE / "requirements.txt").read_text()))
        packages = observed.get("packages") if isinstance(observed, dict) else None
        if (not isinstance(packages, dict) or observed.get("version") != PINS["scanner"] or observed.get("rulesSha256") != PINS["rulesSha256"]
                or packages.keys() != expected.keys()
                or any(not isinstance(item, dict) or expected.get(name) != item.get("version") for name, item in packages.items())):
            raise ValueError("installed scanner package inventory differs from the hash lock")
        receipt = {**observed, "imageId": image["Id"], "imageReference": reference, "fingerprint": fingerprint()}
        write_report(OWNED / "installed.json", receipt, replace=True)
        print("Installed offline scanner " + receipt["version"] + "; image " + receipt["imageId"])


def read_file(path, limit):
    descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    try:
        info = os.fstat(descriptor)
        if not stat.S_ISREG(info.st_mode) or info.st_size > limit:
            raise ValueError("input must be a regular file within its byte limit")
        with os.fdopen(descriptor, "rb", closefd=False) as stream:
            content = stream.read(limit + 1)
    finally:
        os.close(descriptor)
    if len(content) > limit:
        raise ValueError("input exceeds its byte limit")
    return content


def write_report(path, value, *, replace=False):
    content = core.encoded(value) + b"\n"
    if len(content) > core.REPORT_LIMIT:
        raise ValueError("report exceeds its byte limit")
    path.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(prefix=".mcp-scan-", dir=path.parent, delete=False) as temporary:
        staged = Path(temporary.name)
        try:
            temporary.write(content)
            temporary.close()
            if replace:
                if path.is_symlink():
                    raise ValueError("refusing redirected report destination")
                staged.replace(path)
            else:
                os.link(staged, path)
        finally:
            staged.unlink(missing_ok=True)


def named_containers(name, env):
    ids = require_success(docker(["container", "ls", "--all", "--no-trunc", "--filter", "name=^/" + name + "$", "--format", "{{.ID}}"], env, seconds=10)).splitlines()
    if any(not re.fullmatch(r"[0-9a-f]{64}", value) for value in ids):
        raise ValueError("invalid container inventory")
    return ids


def cleanup_container(name, marker, env):
    try:
        ids = named_containers(name, env)
        if not ids:
            return False
        if len(ids) != 1:
            return True
        data = json.loads(require_success(docker(["container", "inspect", ids[0]], env, seconds=10)))
        if (not isinstance(data, list) or len(data) != 1 or not isinstance(data[0], dict)
                or data[0].get("Id") != ids[0] or data[0].get("Name") != "/" + name
                or not isinstance(data[0].get("Config"), dict)):
            return True
        labels = data[0]["Config"].get("Labels")
        if not isinstance(labels, dict) or labels.get(LABEL) != marker:
            return True
        require_success(docker(["rm", "--force", data[0]["Id"]], env, seconds=10))
        return bool(named_containers(name, env))
    except (ValueError, OSError, subprocess.SubprocessError):
        return True  # An unavailable engine is not evidence of cleanup.


def image_identity(image, env):
    marker = secrets.token_hex(12)
    name = "catalog-mcp-identity-" + marker
    if named_containers(name, env):
        raise ValueError("refusing an existing identity container")
    confirmed = False
    try:
        container_id = require_success(docker(["create", "--pull=never", "--name", name, "--label", LABEL + "=" + marker,
                                               "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
                                               "--memory=1g", "--pids-limit=128", image, "--identity"], env)).strip()
        if not re.fullmatch(r"[0-9a-f]{64}", container_id):
            raise ValueError("invalid identity container")
        confirmed = True
        raw = require_success(docker(["start", "--attach", container_id], env)).encode()
        return core.decode(raw, core.REPORT_LIMIT)
    finally:
        failed = cleanup_container(name, marker, env)
        if failed or not confirmed:
            raise ValueError("identity process or cleanup could not be confirmed")


def execute(image, directory, env, *, seconds=core.SCAN_SECONDS):
    require_host()
    marker = secrets.token_hex(12)
    name = "catalog-mcp-scan-" + marker
    if named_containers(name, env):
        raise ValueError("refusing a pre-existing scanner container")
    create_arguments = ["create", "--pull=never", "--name", name,
                 "--label", LABEL + "=" + marker, "--network=none", "--read-only", "--cap-drop=ALL",
                 "--security-opt=no-new-privileges", "--pids-limit=128", "--memory=1g", "--cpus=1",
                 "--user", str(os.getuid()) + ":" + str(os.getgid()),
                 "--mount", "type=bind,src=" + str(directory / "input") + ",dst=/input,readonly", image,
                 "--input", "/input/capture.json", "--context", "/input/context.json"]
    try:
        container_id = require_success(docker(create_arguments, env)).strip()
        if not re.fullmatch(r"[0-9a-f]{64}", container_id):
            raise ValueError("invalid created container identity")
    except (ValueError, OSError, subprocess.SubprocessError, KeyboardInterrupt) as error:
        cleanup_container(name, marker, env)
        # An interrupted creation can still finish at the daemon. Never report
        # its absence as confirmed cleanup, even when an immediate query is empty.
        return b"", 2, "scanner_interrupted" if isinstance(error, KeyboardInterrupt) else "scanner_creation_failed", True
    arguments = ["docker", "--host", "unix:///var/run/docker.sock", "start", "--attach", container_id]
    try:
        process = subprocess.Popen(arguments, env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, start_new_session=True)
    except OSError:
        return b"", 2, "scanner_process_failed", cleanup_container(name, marker, env)
    with process:
        failure = None
        deadline = time.monotonic() + seconds
        output = bytearray()
        sizes = {process.stdout: 0, process.stderr: 0}
        try:
            with selectors.DefaultSelector() as selected:
                for stream in sizes:
                    selected.register(stream, selectors.EVENT_READ)
                while selected.get_map():
                    if time.monotonic() >= deadline:
                        failure = "scanner_timeout"
                        break
                    for key, _events in selected.select(timeout=min(0.1, max(0, deadline - time.monotonic()))):
                        chunk = os.read(key.fileobj.fileno(), 65536)
                        sizes[key.fileobj] += len(chunk)
                        if sizes[key.fileobj] > core.REPORT_LIMIT:
                            failure = "scanner_output_limit"
                            break
                        if key.fileobj is process.stdout:
                            output.extend(chunk)
                        if not chunk:
                            selected.unregister(key.fileobj)
                    if failure:
                        break
                if not failure:
                    try:
                        process.wait(timeout=max(0.001, deadline - time.monotonic()))
                    except subprocess.TimeoutExpired:
                        failure = "scanner_timeout"
        except KeyboardInterrupt:
            failure = "scanner_interrupted"
        finally:
            if process.poll() is None:
                process.terminate()
                try:
                    process.wait(timeout=5)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)
            cleanup_failed = cleanup_container(name, marker, env)
        return bytes(output), process.returncode, failure, cleanup_failed


def scan(input_path, output_path, source, captured_at, context_path=None):
    content = read_file(input_path, core.INPUT_LIMIT)
    value = core.capture(content)
    context_bytes = read_file(context_path, core.CONTEXT_LIMIT) if context_path else b"{}"
    core.context(context_bytes)
    origin = core.attribution(source, captured_at)
    with private_directory() as (directory, env):
        installed = require_installed(env)
        base = core.report_base(value, content, context_bytes, origin, installed)
        if not value["tools"]:
            result = {**base, "status": "skipped", "cleanup": "passed"}
        else:
            (directory / "input").mkdir(mode=0o700)
            (directory / "input/capture.json").write_bytes(content)
            (directory / "input/context.json").write_bytes(context_bytes)
            try:
                raw, code, failure, cleanup_failed = execute(installed["imageId"], directory, env)
                try:
                    result = core.normalize(raw, value, base)
                except ValueError:
                    result = {**base, "error": "invalid_scanner_report"}
                if failure or code:
                    result.update(status="failed", error=failure or "scanner_process_failed")
            except (ValueError, OSError, subprocess.SubprocessError):
                result = {**base, "error": "scanner_process_failed"}
                cleanup_failed = True
            result["cleanup"] = "failed" if cleanup_failed else "passed"
            if cleanup_failed:
                result["status"] = "failed"
                result.setdefault("error", "scanner_cleanup_failed")
        if len(core.encoded(result)) + 1 > core.REPORT_LIMIT:
            result = {**base, "completedTools": result["completedTools"], "error": result.get("error", "scanner_report_limit"),
                      "reportError": "scanner_report_limit", "cleanup": result["cleanup"]}
        write_report(output_path, result)
        print("Offline scan: " + result["status"] + "; report: " + str(output_path))
        return core.exit_status(result)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="action", required=True)
    sub.add_parser("install")
    sub.add_parser("check")
    sub.add_parser("identity")
    selected = sub.add_parser("scan")
    selected.add_argument("--input", type=Path, default=os.environ.get("MCP_SCANNER_INPUT"))
    selected.add_argument("--output", type=Path, default=os.environ.get("MCP_SCANNER_OUTPUT"))
    selected.add_argument("--source", default=os.environ.get("MCP_SCANNER_SOURCE"))
    selected.add_argument("--captured-at", default=os.environ.get("MCP_SCANNER_CAPTURED_AT"))
    selected.add_argument("--context", type=Path, default=os.environ.get("MCP_SCANNER_CONTEXT") or None)
    args = parser.parse_args()
    try:
        if args.action == "install":
            install()
        elif args.action == "identity":
            with private_directory() as (_directory, env):
                installed = require_installed(env)
            selected = {key: installed[key] for key in ("version", "imageId", "rulesSha256")}
            selected["configurationSha256"] = core.digest(core.encoded(core.configuration(installed["rulesSha256"])))
            sys.stdout.buffer.write(core.encoded(selected) + b"\n")
        elif args.action == "scan":
            if not all((args.input, args.output, args.source, args.captured_at)):
                raise ValueError("set all required MCP_SCANNER inputs or CLI flags")
            return scan(args.input, args.output, args.source, args.captured_at, args.context)
        else:
            with private_directory() as (_directory, env):
                require_installed(env)
            command = [sys.executable, "-B", "-m", "unittest", "discover", "-s", str(HERE)]
            subprocess.run([*command, "-p", "test_unit_*.py"], check=True)
            subprocess.run([*command, "-p", "test_functional_*.py"], check=True,
                           env={**os.environ, "CATALOG_TEST_MCP_SCANNER": "1"})
    except SetupError as error:
        print(str(error), file=sys.stderr)
        return 2
    except (ValueError, OSError, subprocess.SubprocessError):
        print("Offline scanner command failed; check its input, installation and owned Docker resources.", file=sys.stderr)
        return 2
    return 0


def interrupted(_number, _frame):
    raise KeyboardInterrupt


if __name__ == "__main__":
    signal.signal(signal.SIGTERM, interrupted)
    raise SystemExit(main())
