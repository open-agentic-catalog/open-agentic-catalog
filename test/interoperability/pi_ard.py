"""Explicit acquisition and bounded, local native Pi session preparation."""

import argparse
import base64
from datetime import datetime, timedelta, timezone
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import shlex
import signal
import subprocess
import tempfile
import time
import uuid

from common import run
from runner import ROOT, HERE, extract_source, fingerprint, cached_go, build_binaries, CATALOG_INPUTS

INPUTS = HERE / "pi-ard"
OUTPUT = ROOT / "output/interoperability/pi-ard"
TOOLS = OUTPUT / "tools"
PIN = json.loads((INPUTS / "inputs.json").read_text())
NODE = ROOT / ".tools/ci" / ("node-v" + PIN["node"] + "-linux-x64")
HARNESS_INPUTS = (
    INPUTS,
    HERE / "pi_ard.py",
    HERE / "pi_campaign.py",
    HERE / "common.py",
    HERE / "runner.py",
    ROOT / "Makefile",
)
LICENSES = {
    "MIT",
    "Apache-2.0",
    "BSD-3-Clause",
    "ISC",
    "0BSD",
    "Unlicense",
    "BlueOak-1.0.0",
}


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def save(path, value):
    path.write_text(json.dumps(value, indent=2) + "\n")
    path.chmod(0o600)


def tree_digest(directory):
    """Bind installed files and links; refuse links outside the tool tree."""
    digest = hashlib.sha256()
    for path in sorted(directory.rglob("*")):
        if path.is_symlink():
            if not path.resolve().is_relative_to(directory.resolve()):
                raise ValueError("tool symlink escapes installation")
            data = os.readlink(path).encode()
        elif path.is_file():
            data = path.read_bytes()
        else:
            continue
        digest.update(str(path.relative_to(directory)).encode() + b"\0" + hashlib.sha256(data).digest())
    return digest.hexdigest()


def environment(home):
    return {
        "PATH": str(NODE / "bin") + ":/usr/bin:/bin",
        "HOME": str(home),
        "LANG": "C.UTF-8",
        "npm_config_userconfig": "/dev/null",
        "npm_config_globalconfig": str(home / "npmrc"),
        "npm_config_audit": "false",
        "npm_config_fund": "false",
        "npm_config_ignore_scripts": "true",
    }


def prerequisites():
    if sys_optimized() or platform.system() != "Linux" or platform.machine() != "x86_64" or os.getuid() == 0:
        raise RuntimeError("Pi preparation requires assertions and non-root Linux AMD64")
    for name in ("bwrap", "sudo", "systemd-run", "systemctl"):
        if not shutil.which(name):
            raise RuntimeError("missing Pi preparation tool: " + name)
    if run([str(NODE / "bin/node"), "--version"]).stdout.strip() != "v" + PIN["node"]:
        raise RuntimeError("install the pinned repository Node toolchain first")
    if run([str(NODE / "bin/npm"), "--version"]).stdout.strip() != PIN["npm"]:
        raise RuntimeError("npm differs from the reviewed pin")


def sys_optimized():
    import sys

    return sys.flags.optimize


def validate_lock(lock, now):
    if lock.get("lockfileVersion") != 3 or len(lock.get("packages", {})) != PIN["packagePaths"] + 1:
        raise ValueError("Pi lock inventory differs")
    exceptions = {(item["name"], item["version"]): item for item in PIN["ageExceptions"]}
    pairs = set()
    for key, value in lock["packages"].items():
        if not key:
            continue
        name = key.rsplit("node_modules/", 1)[1]
        if not value.get("resolved", "").startswith("https://registry.npmjs.org/") or not re.fullmatch(
            r"sha512-[A-Za-z0-9+/]+=*", value.get("integrity", "")
        ):
            raise ValueError("unreviewed Pi package source or integrity")
        if value.get("license") not in LICENSES:
            raise ValueError("unreviewed Pi package license")
        pair = (name, value["version"])
        if pair in exceptions and value["integrity"] != exceptions[pair]["integrity"]:
            raise ValueError("Pi age exception does not match the approved package")
        pairs.add(pair)
    if len(pairs) != PIN["packageVersions"] or not exceptions.keys() <= pairs:
        raise ValueError("Pi package/version closure differs")
    if now - datetime.fromisoformat(PIN["published"].replace("Z", "+00:00")) < timedelta(days=14):
        raise ValueError("Pi source does not meet the 14-day rule")
    dates = PIN["releaseDates"]
    if set(dates) != {name + "@" + version for name, version in pairs}:
        raise ValueError("incomplete Pi package release dates")
    for pair in pairs:
        published = datetime.fromisoformat(dates["@".join(pair)].replace("Z", "+00:00"))
        if published > now:
            raise ValueError("Pi inputs name an unpublished package")
        if now - published < timedelta(days=14) and pair not in exceptions:
            raise ValueError("Pi dependency does not meet the 14-day rule")
    return pairs


def validate_scan(value, pairs):
    if value.get("SchemaVersion") != 2 or len(value.get("Results", [])) != 1:
        raise ValueError("incomplete Pi advisory report")
    result = value["Results"][0]
    actual = {(p["Name"], p["Version"]) for p in result.get("Packages", [])}
    if result.get("Type") != "npm" or not pairs <= actual:
        raise ValueError("Pi scan omits locked dependencies")
    findings = result.get("Vulnerabilities", [])
    if findings:
        raise ValueError("Pi dependency finding requires review before execution")
    return findings


def install():
    prerequisites()
    if TOOLS.exists():
        raise RuntimeError("Pi tool directory exists; inspect and remove only that directory before explicit reinstall")
    TOOLS.mkdir(parents=True, mode=0o700)
    source, home = TOOLS / "source", TOOLS / "home"
    home.mkdir()
    (home / "npmrc").write_text("")
    raw = run(
        ["gh", "api", "repos/" + PIN["repository"] + "/tarball/" + PIN["revision"]],
        binary=True,
        timeout=90,
    ).stdout
    extract_source(raw, source, PIN["sourceDigest"])
    for name, key in (
        ("package-lock.json", "lockDigest"),
        ("LICENSE", "licenseDigest"),
        ("NOTICE", "noticeDigest"),
    ):
        if sha(source / name) != PIN[key]:
            raise ValueError("Pi source input differs: " + name)
    runtime = TOOLS / "runtime"
    runtime.mkdir()
    for name, key in (("package.json", "runtimeManifestDigest"), ("package-lock.json", "runtimeLockDigest")):
        if sha(INPUTS / "runtime" / name) != PIN[key]:
            raise ValueError("Pi runtime input differs: " + name)
        shutil.copyfile(INPUTS / "runtime" / name, runtime / name)
    pairs = validate_lock(
        json.loads((runtime / "package-lock.json").read_text()),
        datetime.now(timezone.utc),
    )
    license_data = run(
        [
            "gh",
            "api",
            "repos/" + PIN["hostRepository"] + "/contents/LICENSE?ref=" + PIN["hostRevision"],
            "--jq",
            ".content",
        ]
    ).stdout
    host_license = TOOLS / "Pi-MIT-LICENSE"
    host_license.write_bytes(base64.b64decode(license_data))
    if sha(host_license) != PIN["hostLicenseDigest"]:
        raise ValueError("Pi host license differs from reviewed source")
    scanner = ROOT / ".tools/ci/bin/trivy"
    version = json.loads(run([str(scanner), "--version", "--format=json"]).stdout)
    if version["Version"] != json.loads((ROOT / "test/packaging/trivy.json").read_text())["version"]:
        raise RuntimeError("install the pinned scanner first")
    cache = TOOLS / "advisory-cache"
    scanner_env = {key: value for key, value in os.environ.items() if not key.upper().startswith("TRIVY_")}
    scanner_config = TOOLS / "scanner.yaml"
    scanner_config.write_text("{}\n")
    scanner_ignore = TOOLS / "scanner.ignore"
    scanner_ignore.write_text("")
    run(
        [
            str(scanner),
            "image",
            "--config",
            str(scanner_config),
            "--cache-dir",
            str(cache),
            "--download-db-only",
            "--disable-telemetry",
        ],
        timeout=180,
        env=scanner_env,
    )
    scan = TOOLS / "advisories.json"
    run(
        [
            str(scanner),
            "fs",
            "--config",
            str(scanner_config),
            "--ignorefile",
            str(scanner_ignore),
            "--cache-dir",
            str(cache),
            "--scanners",
            "vuln",
            "--include-dev-deps",
            "--list-all-pkgs",
            "--skip-db-update",
            "--skip-java-db-update",
            "--offline-scan",
            "--disable-telemetry",
            "--format",
            "json",
            "--output",
            str(scan),
            str(runtime),
        ],
        timeout=90,
        env=scanner_env,
    )
    findings = validate_scan(json.loads(scan.read_text()), pairs)
    result = run(
        [
            str(NODE / "bin/npm"),
            "--prefix",
            str(runtime),
            "ci",
            "--ignore-scripts",
            "--no-audit",
            "--no-fund",
        ],
        env=environment(home),
        timeout=240,
    )
    (TOOLS / "acquisition.log").write_text(result.stdout + result.stderr)
    if sha(source / "package-lock.json") != PIN["lockDigest"]:
        raise ValueError("acquisition changed the native lock")
    if sha(runtime / "package-lock.json") != PIN["runtimeLockDigest"]:
        raise ValueError("acquisition changed the reviewed runtime lock")
    # Preserve the upstream source and development lock. The sandbox mounts
    # the separately locked host packages at the normal Node resolution path.
    (source / "node_modules").mkdir()
    inventory = []
    lock = json.loads((runtime / "package-lock.json").read_text())
    for path, package in lock["packages"].items():
        if not path:
            continue
        manifest = runtime / path / "package.json"
        if not manifest.is_file():
            if package.get("optional"):
                continue
            raise ValueError("missing installed Pi package: " + path)
        actual = json.loads(manifest.read_text())
        if actual["version"] != package["version"] or actual["name"] != path.rsplit("node_modules/", 1)[1]:
            raise ValueError("installed Pi package identity differs")
        inventory.append(
            {
                "path": path,
                "name": actual["name"],
                "version": actual["version"],
                "license": actual.get("license"),
                "lifecycleScriptsDisabled": True,
            }
        )
    save(TOOLS / "inventory.json", inventory)
    save(
        TOOLS / "notices.json",
        {
            str(p.relative_to(runtime)): sha(p)
            for p in runtime.rglob("*")
            if p.is_file() and p.name.lower().startswith(("license", "notice", "copyright"))
        },
    )
    save(
        TOOLS / "installation.json",
        {
            "version": PIN["version"],
            "revision": PIN["revision"],
            "hostVersion": PIN["hostVersion"],
            "inputs": sha(INPUTS / "inputs.json"),
            "nodeDigest": sha(NODE / "bin/node"),
            "toolDigest": tree_digest(source),
            "runtimeDigest": tree_digest(runtime),
            "hostLicenseDigest": sha(host_license),
            "findings": findings,
            "scanner": {"Version": version["Version"]},
            "advisoryDatabase": json.loads((cache / "db/metadata.json").read_text()),
            "reviewFiles": {
                name: sha(TOOLS / name)
                for name in ("inventory.json", "notices.json", "advisories.json", "Pi-MIT-LICENSE")
            },
            "installedUTC": datetime.now(timezone.utc).isoformat(),
        },
    )
    print(
        "Pi tools acquired with lifecycle scripts and registry audits disabled. Native preparation is a separate target."
    )


def installed():
    prerequisites()
    stamp = json.loads((TOOLS / "installation.json").read_text())
    if (
        stamp["revision"] != PIN["revision"]
        or stamp["version"] != PIN["version"]
        or stamp["hostVersion"] != PIN["hostVersion"]
        or stamp["inputs"] != sha(INPUTS / "inputs.json")
        or stamp["toolDigest"] != tree_digest(TOOLS / "source")
        or stamp["runtimeDigest"] != tree_digest(TOOLS / "runtime")
        or stamp["nodeDigest"] != sha(NODE / "bin/node")
        or stamp["hostLicenseDigest"] != sha(TOOLS / "Pi-MIT-LICENSE")
    ):
        raise ValueError("Pi installation changed; explicit review and reinstall required")
    if any(sha(TOOLS / name) != value for name, value in stamp["reviewFiles"].items()):
        raise ValueError("Pi acquisition evidence changed; review and reinstall required")
    return stamp


def scope_command(unit, command):
    if not re.fullmatch(r"oac-pi-[a-f0-9]{12}\.service", unit):
        raise ValueError("unowned Pi unit name")
    limits = shlex.split(
        "-p RuntimeMaxSec=180 -p MemoryMax=2147483648 -p MemorySwapMax=0 "
        "-p CPUQuota=200% -p TasksMax=256 -p KillMode=control-group -p TimeoutStopSec=5"
    )
    return [
        "sudo",
        "-n",
        "systemd-run",
        "--quiet",
        "--wait",
        "--pipe",
        "--collect",
        "--uid=" + str(os.getuid()),
        "--gid=" + str(os.getgid()),
        "--unit=" + unit,
        *limits,
        *command,
    ]


def sandbox(private, mode):
    args = shlex.split(
        "bwrap --unshare-user --unshare-ipc --unshare-pid --unshare-net --unshare-uts "
        "--die-with-parent --new-session --clearenv "
        "--ro-bind /usr /usr --ro-bind /lib /lib --ro-bind /lib64 /lib64 "
        "--symlink usr/bin /bin --proc /proc --dev /dev "
        "--ro-bind /sys/fs/cgroup /sys/fs/cgroup --ro-bind /etc/ld.so.cache /etc/ld.so.cache"
    )
    for source, target in ((NODE, "/node"), (TOOLS / "source", "/pi"), (INPUTS, "/harness")):
        args += ["--ro-bind", str(source), target]
    args += ["--ro-bind", str(TOOLS / "runtime/node_modules"), "/pi/node_modules"]
    args += ["--bind", str(private), "/state", "--bind", str(private / "tmp"), "/tmp"]
    args += shlex.split(
        "--chdir /state --setenv PATH /node/bin:/usr/bin:/bin --setenv HOME /state/home "
        "--setenv LANG C.UTF-8 --setenv PYTHONDONTWRITEBYTECODE 1"
    )
    return [*args, "--remount-ro", "/", "/usr/bin/python3", "-B", "/harness/confined.py", mode]


def stop_scope(unit):
    if not re.fullmatch(r"oac-pi-[a-f0-9]{12}\.service", unit):
        raise ValueError("unowned Pi unit name")
    group = run(
        ["sudo", "-n", "systemctl", "show", unit, "--property=ControlGroup", "--value"],
        check=False,
    ).stdout.strip()
    run(["sudo", "-n", "systemctl", "stop", unit], check=False, timeout=15)
    state = run(["sudo", "-n", "systemctl", "is-active", unit], check=False).stdout.strip()
    if state not in ("inactive", "failed", "unknown"):
        raise RuntimeError("Pi unit remains active")
    path = Path("/sys/fs/cgroup") / group.lstrip("/") / "cgroup.procs"
    if group and path.exists() and path.read_text().strip():
        raise RuntimeError("Pi child remains in its cgroup")


def validate_result(value):
    expected = {
        "readOnlyRoot": True,
        "network": "loopback-only",
        "externalConnectErrno": 101,
        "memoryMax": 2147483648,
        "cpuCores": 2,
        "tasksMax": 256,
        "swapMax": 0,
        "fileLimit": 8388608,
        "openFiles": 256,
    }
    if (
        type(value) is not dict
        or set(value) != {*expected, "native"}
        or any(type(value[k]) is not type(v) or value[k] != v for k, v in expected.items())
    ):
        raise ValueError("incomplete Pi isolation evidence")
    native = value["native"]
    required = {
        "activeTools",
        "fetchUnchanged",
        "dispatcherInitialized",
        "dispatcherPackage",
        "dispatcherVersion",
        "dispatcherStableAfterImport",
        "offline",
        "scriptedTurn",
        "localConfig",
        "toolCalls",
        "searchPages",
        "confirms",
        "artifactFiles",
        "httpTimeoutMs",
        "httpBytes",
        "nativeLoopbackRequests",
    }
    expected_native = {
        "activeTools": ["ard_catalog", "ard_install", "ard_search"],
        "fetchUnchanged": True,
        "dispatcherInitialized": True,
        "dispatcherPackage": "undici",
        "dispatcherVersion": PIN["dispatcherVersion"],
        "dispatcherStableAfterImport": True,
        "offline": True,
        "scriptedTurn": True,
        "localConfig": True,
        "toolCalls": 0,
        "searchPages": 0,
        "confirms": 0,
        "artifactFiles": 0,
        "httpTimeoutMs": 15000,
        "httpBytes": 1000000,
        "nativeLoopbackRequests": 2,
    }
    if (
        type(native) is not dict
        or set(native) != required
        or any(type(native[k]) is not type(v) or native[k] != v for k, v in expected_native.items())
    ):
        raise ValueError("incomplete native Pi preparation")


def cleanup_run(private, unit, report):
    previous = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM, signal.SIGALRM})
    try:
        stop_scope(unit)
        shutil.rmtree(private)
        if private.exists():
            raise RuntimeError("Pi private directory remains")
        report["cleanup"] = "passed"
    except BaseException:
        report["cleanup"] = "failed"
        raise
    finally:
        signal.pthread_sigmask(signal.SIG_SETMASK, previous)


def retain_campaign_logs(private, directory):
    """Retain bounded regular diagnostics after the owned service stops."""
    for name in ("catalog.log", "pi-one.log", "pi-two.log", "observations.json", "native-campaign.json"):
        path = private / name
        if path.is_symlink():
            raise ValueError("Pi diagnostic symlink is not permitted")
        if path.is_file():
            if path.stat().st_size > PIN["bounds"]["outputBytes"]:
                raise ValueError("Pi diagnostic exceeds its output limit")
            (directory / name).write_bytes(path.read_bytes())


def prepare(campaign=False):
    stamp = installed()
    source = fingerprint(HARNESS_INPUTS)
    directory = OUTPUT / ("run-" + uuid.uuid4().hex[:12])
    directory.mkdir(mode=0o700)
    report = {
        "status": "incomplete",
        "cleanup": "pending",
        "tools": stamp,
        "source": source,
        "controls": {},
    }
    catalog_source = fingerprint(CATALOG_INPUTS) if campaign else None
    report["stage"] = "catalog-campaign" if campaign else "preparation"
    if campaign:
        report["catalogSource"] = catalog_source

    def interrupted(_number, _frame):
        raise RuntimeError("Pi preparation interrupted")

    handlers = {
        number: signal.signal(number, interrupted) for number in (signal.SIGINT, signal.SIGTERM, signal.SIGALRM)
    }
    signal.alarm(PIN["bounds"]["seconds"])
    print("Pi preparation report: " + str(directory / "report.json"), flush=True)
    try:
        modes = (
            ("campaign",)
            if campaign
            else (
                "prepare",
                "tool-budget",
                "page-budget",
                "missing-config",
                "failure",
                "timeout",
                "output",
                "interruption",
            )
        )
        for mode in modes:
            unit = "oac-pi-" + uuid.uuid4().hex[:12] + ".service"
            private = Path(tempfile.mkdtemp(prefix="catalog-pi-"))
            try:
                report.update(cleanup="pending", unit=unit, privateDirectory=str(private))
                save(directory / "report.json", report)
                (private / "tmp").mkdir()
                (private / "home").mkdir()
                if campaign:
                    go = cached_go()
                    build_binaries(private, go, "amd64")
                    save(private / "config.json", json.loads((ROOT / "config/local.json").read_text()))
                    report["build"] = {
                        "go": run([go, "version"]).stdout.strip(),
                        "catalogd": sha(private / "catalogd"),
                        "catalogctl": sha(private / "catalogctl"),
                    }
                command = scope_command(unit, sandbox(private, mode))
                if mode == "interruption":
                    with (directory / "interruption.log").open("w") as output:
                        child = subprocess.Popen(
                            command,
                            stdout=output,
                            stderr=output,
                            start_new_session=True,
                        )
                        try:
                            end = time.monotonic() + 30
                            while not (private / "ready").exists() and child.poll() is None and time.monotonic() < end:
                                time.sleep(0.05)
                            if not (private / "ready").exists():
                                raise RuntimeError("native interruption control did not start")
                            os.killpg(child.pid, signal.SIGINT)
                            child.wait(timeout=10)
                            if child.returncode == 0:
                                raise RuntimeError("interrupted native control reported success")
                        finally:
                            if child.poll() is None:
                                os.killpg(child.pid, signal.SIGKILL)
                                child.wait(timeout=5)
                else:
                    try:
                        result = run(
                            command,
                            timeout=10 if mode == "timeout" else 180,
                            limit=PIN["bounds"]["outputBytes"],
                            check=False,
                        )
                    except RuntimeError as error:
                        expected_error = {
                            "timeout": "subprocess deadline exceeded",
                            "output": "subprocess output limit exceeded",
                        }
                        if (
                            mode not in expected_error
                            or str(error) != expected_error[mode]
                            or not (private / "ready").exists()
                        ):
                            raise
                    else:
                        (directory / (mode + ".log")).write_text(result.stdout + result.stderr)
                        if mode in ("timeout", "output") or (result.code == 0) != (
                            mode not in ("missing-config", "failure")
                        ):
                            raise RuntimeError("Pi preparation/control returned an unexpected status: " + mode)
                        if mode == "campaign":
                            from pi_campaign import validate_campaign

                            value = json.loads((private / "campaign.json").read_text())
                            validate_campaign(value)
                            report["campaign"] = value
                        elif mode == "prepare":
                            value = json.loads((private / "result.json").read_text())
                            validate_result(value)
                            report["native"] = value
                        elif mode in ("tool-budget", "page-budget"):
                            value = json.loads((private / "budget.json").read_text())
                            if value != {
                                "blocked": True,
                                "executed": 1 if mode == "tool-budget" else 3,
                            }:
                                raise ValueError("native budget control failed")
                        elif mode in ("missing-config", "failure"):
                            if not (private / ("missing-refused" if mode == "missing-config" else "ready")).exists():
                                raise ValueError("failure occurred before its intended control")
                report["controls"][mode] = "passed"
            finally:
                try:
                    if campaign:
                        stop_scope(unit)
                        retain_campaign_logs(private, directory)
                finally:
                    cleanup_run(private, unit, report)
        if fingerprint(HARNESS_INPUTS) != source:
            raise ValueError("Pi preparation sources changed during execution")
        if campaign and (fingerprint(CATALOG_INPUTS) != catalog_source or installed() != stamp):
            raise ValueError("Pi campaign inputs changed during execution")
        report["status"] = "passed"
        report["cleanup"] = "passed"
    except BaseException:
        report["status"] = "failed"
        raise
    finally:
        signal.alarm(0)
        save(directory / "report.json", report)
        for number, handler in handlers.items():
            signal.signal(number, handler)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "prepare", "check"))
    selected = parser.parse_args()
    if selected.action == "install":
        install()
    else:
        prepare(campaign=selected.action == "check")
