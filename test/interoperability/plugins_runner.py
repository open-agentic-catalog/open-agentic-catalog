"""Explicit pinned portable Plugin loading and governed immutable archive handoff."""

import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import sys
import tempfile
import time
import traceback
import uuid

from common import run
from runner import (
    CATALOG_INPUTS,
    ROOT,
    build_binaries,
    docker,
    fingerprint,
    local_daemon,
)
from skills_catalog import same_retained
import plugins_archive as archive
from plugins_catalog import Catalog
import plugins_contract as contract
import plugins_data as data
from plugins_http import ORIGIN
import plugins_tools as tools


def extra_binaries(directory, go, arch):
    environment = {
        k: os.environ[k]
        for k in ("PATH", "HOME", "GOCACHE", "GOMODCACHE")
        if k in os.environ
    }
    environment.update(
        CGO_ENABLED="0",
        GOOS="linux",
        GOARCH=arch,
        GOTOOLCHAIN="local",
        GOENV="off",
        GOWORK="off",
        GOFLAGS="-mod=readonly",
        GOPROXY="off",
        GOSUMDB="off",
        GOPRIVATE="",
        GONOPROXY="",
        GONOSUMDB="",
    )
    for name, package in (
        ("prepare", "./test/demo"),
        ("archive", "./test/interoperability/pluginfixture"),
    ):
        run(
            [
                go,
                "build",
                "-trimpath",
                "-ldflags=-s -w",
                "-o",
                str(directory / name),
                package,
            ],
            env=environment,
            timeout=120,
        )


def check():
    arch = local_daemon()
    assert arch == "amd64", "Plugin tooling is qualified on Linux amd64"
    stamp = tools.installed()
    inputs = [
        *CATALOG_INPUTS,
        ROOT / "Makefile",
        tools.HERE,
        ROOT / "samples",
        ROOT / "test/demo",
        ROOT / "test/demofixture",
    ]
    source = fingerprint(inputs)
    run_id = uuid.uuid4().hex[:12]
    report_dir = tools.OUTPUT / run_id
    report_dir.mkdir(parents=True, mode=0o700)
    private = Path(tempfile.mkdtemp(prefix="catalog-plugins-"))
    report = dict(
        run_id=run_id,
        source=source,
        tools=stamp,
        success=False,
        cleanup=False,
        native=[],
        refusals={},
        containers=[],
        phase="setup",
        private_directory=str(private),
    )
    names = report["containers"]
    secrets = []

    def save():
        raw = json.dumps(report, indent=2)
        for secret in secrets:
            raw = raw.replace(secret, "[redacted]")
        (report_dir / "report.json").write_text(raw + "\n")

    def interrupted(*_args):
        raise TimeoutError("Plugin qualification interrupted or timed out")

    signals = (signal.SIGINT, signal.SIGTERM, signal.SIGALRM)
    old = {s: signal.signal(s, interrupted) for s in signals}
    try:
        signal.alarm(600)
        save()
        print("Plugin report:", report_dir / "report.json", flush=True)
        paths = {
            n: private / n
            for n in (
                "commands",
                "fixture",
                "packages",
                "dataset",
                "prepared",
                "catalog",
                "trust",
            )
        }
        for path in paths.values():
            path.mkdir(mode=0o700)
        build_binaries(paths["commands"], stamp["go"], arch)
        extra_binaries(paths["commands"], stamp["go"], arch)
        report["binaries"] = {
            p.name: hashlib.sha256(p.read_bytes()).hexdigest()
            for p in paths["commands"].iterdir()
        }
        for name in ("plugins_native.py", "plugins_http.py"):
            shutil.copyfile(tools.HERE / name, paths["fixture"] / name)
        data.prepare_direct(paths["packages"])
        prefix = "catalog-plugins-" + run_id

        def native(case):
            name = prefix + "-" + case
            names.append(name)
            tools.create(
                name,
                stamp["image"],
                [(paths["packages"], "/packages"), (paths["fixture"], "/fixture")],
                args=("/fixture/plugins_native.py", "/packages/" + case),
            )
            report["phase"] = "native-" + case
            save()
            result = docker(
                "start", "--attach", name, timeout=90, limit=65536, check=False
            )
            state = json.loads(docker("inspect", name).stdout)[0]["State"]
            assert (
                not state["Running"]
                and state["ExitCode"] == result.code
                and not state["OOMKilled"]
            )
            row = dict(
                case=case, exit=result.code, stdout=result.stdout, stderr=result.stderr
            )
            report["native"].append(row)
            contract.native(result, case, "/packages/" + case)
            tools.remove([name])

        for case in data.DIRECT:
            native(case)
        report["isolation"] = True
        report["phase"] = "prepare"
        save()
        data.write_dataset(paths["dataset"])
        # Preparation runs locally with private files; no network or retained sample state.
        run(
            [
                str(paths["commands"] / "prepare"),
                "--prepare",
                "--data-file",
                str(paths["dataset"] / "catalog.yaml"),
                "--sample-set",
                "qualification",
                "--directory",
                str(paths["prepared"]),
                "--archive-origin",
                ORIGIN,
            ],
            timeout=30,
        )
        shutil.copyfile(paths["prepared"] / "cert.pem", paths["trust"] / "cert.pem")
        allowlist = json.loads((paths["prepared"] / "allowlist.json").read_text())
        bodies = {
            e["path"]: (
                paths["prepared"]
                / "objects"
                / (e["digest"].removeprefix("sha256:") + ".zip")
            ).read_bytes()
            for e in allowlist["entries"]
        }
        original, later = bodies["/original-plugin.zip"], bodies["/later-plugin.zip"]
        assert original != later
        report["later_archive_distinct"] = True
        server = prefix + "-archive"
        names.append(server)
        tools.create(
            server,
            stamp["image"],
            [
                (paths["commands"], "/commands"),
                (paths["prepared"], "/prepared"),
                (paths["trust"], "/trust"),
                (paths["fixture"], "/fixture"),
            ],
            entrypoint="/commands/archive",
            args=("--directory", "/prepared"),
        )
        docker("start", server)

        def fetch(url):
            return docker(
                "exec",
                server,
                "python",
                "-B",
                "/fixture/plugins_http.py",
                url,
                timeout=15,
                limit=128 * 1024,
            )

        expires = time.monotonic() + 15
        while True:
            try:
                fetched = base64.b64decode(
                    fetch(ORIGIN + "/original-plugin.zip").stdout.strip(), validate=True
                )
                assert fetched == original
                break
            except RuntimeError:
                if time.monotonic() >= expires:
                    raise
                time.sleep(0.1)

        def observations():
            return json.loads(fetch(ORIGIN + "/observations").stdout)

        # A live peer in another owned namespace must be unreachable from the native namespace.
        isolated = prefix + "-isolation"
        names.append(isolated)
        probe = "import errno,socket; s=socket.socket(); s.settimeout(2); code=s.connect_ex(('127.0.0.1',9443)); s.close(); assert code==errno.ECONNREFUSED; print('isolated')"
        tools.create(isolated, stamp["image"], args=("-c", probe))
        assert (
            docker("start", "--attach", isolated, timeout=5).stdout.strip()
            == "isolated"
        )
        assert (
            json.loads(docker("inspect", isolated).stdout)[0]["State"]["ExitCode"] == 0
        )
        assert (
            base64.b64decode(fetch(ORIGIN + "/original-plugin.zip").stdout) == original
        )
        report["network_denial"] = True
        catalog_name = prefix + "-catalog"
        catalog = Catalog(paths["catalog"], catalog_name)
        catalog.configuration()
        secrets.extend(catalog.tokens.values())
        config = json.loads((paths["catalog"] / "config.json").read_text())
        config["demoArchives"] = dict(
            origin=ORIGIN,
            trustBundleFile="/trust/cert.pem",
            allowlistFile="/policy/allowlist.json",
        )
        catalog.write("config.json", json.dumps(config))
        policy = private / "policy"
        policy.mkdir()
        shutil.copyfile(paths["prepared"] / "allowlist.json", policy / "allowlist.json")
        names.append(catalog_name)
        tools.create(
            catalog_name,
            stamp["image"],
            [
                (paths["commands"], "/commands"),
                (paths["catalog"], "/catalog"),
                (paths["fixture"], "/fixture"),
                (paths["trust"], "/trust"),
                (policy, "/policy"),
            ],
            network="container:" + server,
            entrypoint="/commands/catalogd",
            args=("all", "--config", "/catalog/config.json"),
        )
        docker("start", catalog_name)
        expires = time.monotonic() + 20
        while docker(
            "exec",
            catalog_name,
            "/commands/catalogctl",
            "probe",
            "--url",
            "http://127.0.0.1:8080/readyz",
            timeout=5,
            check=False,
        ).code:
            if time.monotonic() >= expires:
                raise RuntimeError("Catalog readiness deadline")
            time.sleep(0.1)
        report["phase"] = "governance"
        save()
        expected, listing = catalog.publish(original, ORIGIN + "/original-plugin.zip")
        selected = catalog.discover(expected, original, ORIGIN + "/original-plugin.zip")
        representation = selected["versionRevision"]["representations"][0]
        report["selection"] = dict(
            expected, snapshot=catalog.snapshot, digest=representation["contentDigest"]
        )
        catalog.draft(expected, listing, later, ORIGIN + "/later-plugin.zip")
        same_retained(
            selected,
            catalog.discover(expected, original, ORIGIN + "/original-plugin.zip"),
        )
        report["immutable"] = True
        # The actual download is selected by the verified record, not hard-coded as the outcome.
        before = len(observations())
        received = base64.b64decode(
            fetch(representation["url"]).stdout.strip(), validate=True
        )
        assert len(observations()) == before + 1
        target = paths["packages"] / "governed"
        files, dirs = archive.materialize(received, representation, target)
        assert {p: b for p, (b, _) in files.items()} == data.files()
        native("governed")
        archive.unchanged(target, files, dirs)
        report["governed"] = True
        for case in ("digest", "size"):
            changed = (
                bytes([received[0] ^ 1]) + received[1:]
                if case == "digest"
                else received[:-1]
            )
            destination = paths["packages"] / case
            count = len(report["native"])
            try:
                archive.materialize(changed, representation, destination)
            except archive.IntegrityError as error:
                assert str(error) == "archive " + case + " mismatch"
            else:
                raise AssertionError("corrupt archive consumed")
            assert not destination.exists() and len(report["native"]) == count
            report["refusals"][case] = True
        for case in ("missing", "invalid", "wrong-tenant"):
            count = len(report["native"])
            before = observations()
            catalog.denial(case, expected["release"])
            assert observations() == before and len(report["native"]) == count
            report["refusals"][case] = True
        same_retained(
            selected,
            catalog.discover(expected, original, ORIGIN + "/original-plugin.zip"),
        )
        report["retained"] = report["no_execution"] = True
        report["archive_reads"] = observations()
        docker("stop", "--time", "25", catalog_name, timeout=30)
        assert (
            json.loads(docker("inspect", catalog_name).stdout)[0]["State"]["ExitCode"]
            == 0
        )
        report["source_unchanged"] = source == fingerprint(inputs)
    except BaseException as error:
        report["error_type"] = type(error).__name__
        report["error_frames"] = [
            dict(file=Path(f.filename).name, line=f.lineno, function=f.name)
            for f in traceback.extract_tb(error.__traceback__)
        ]
        report["creation_unconfirmed"] = isinstance(error, tools.CreationUnconfirmed)
        raise
    finally:
        signal.alarm(0)
        blocked = signal.pthread_sigmask(
            signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM}
        )
        try:
            try:
                tools.remove(names)
            finally:
                shutil.rmtree(private)
            report["cleanup"] = not private.exists() and not report.get(
                "creation_unconfirmed", False
            )
        finally:
            save()
            for s, handler in old.items():
                signal.signal(s, handler)
            signal.pthread_sigmask(signal.SIG_SETMASK, blocked)
    contract.complete(report)
    report["success"] = True
    report["phase"] = "complete"
    save()
    print("Independent Plugin loading passed:", run_id)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "check"))
    args = parser.parse_args()
    try:
        if args.action == "install":
            signals = (signal.SIGINT, signal.SIGTERM, signal.SIGALRM)

            def interrupted(*_args):
                raise TimeoutError("Plugin installation interrupted or timed out")

            previous = {s: signal.signal(s, interrupted) for s in signals}
            try:
                signal.alarm(900)
                tools.install()
            finally:
                signal.alarm(0)
                for sig, handler in previous.items():
                    signal.signal(sig, handler)
        else:
            check()
    except (Exception, KeyboardInterrupt) as error:
        print(
            "Plugin qualification failed ("
            + type(error).__name__
            + "); inspect the private report/installation log.",
            file=sys.stderr,
        )
        raise SystemExit(1) from None


if __name__ == "__main__":
    if not __debug__:
        print("Plugin qualification requires Python assertions", file=sys.stderr)
        raise SystemExit(2)
    main()
