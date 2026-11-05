"""Explicit direct and governed Skill consumption in owned offline containers."""

import argparse
import copy
import hashlib
import json
from pathlib import Path
import shutil
import signal
import sys
import traceback
import tempfile
import time
import uuid

from common import run
from runner import CATALOG_INPUTS, ROOT, build_binaries, docker, fingerprint, local_daemon
from skills_catalog import Catalog, same_retained
from skills_contract import DIRECT, NATIVE_REFUSALS, check_native, check_refusal, complete
import skills_git as package
import skills_tools as tools


def prepare_cases(root):
    for case in sorted(DIRECT):
        name = "wrong-parent" if case == "parent-mismatch" else package.NAME
        directory = root / case / name
        directory.mkdir(parents=True)
        text = package.MARKDOWN
        if case == "missing-document":
            continue
        if case == "malformed":
            text = "---\nname: [broken\n---\n"
        elif case == "missing-field":
            text = text.replace("name: " + package.NAME + "\n", "")
        elif case == "trim-name":
            text = text.replace("name: " + package.NAME, 'name: " ' + package.NAME + ' "')
        elif case == "nfkc-name":
            text = text.replace("name: " + package.NAME, "name: ｏｗｎｅｄ-skill")
        elif case == "empty-compatibility":
            text = text.replace("license:", 'compatibility: ""\nlicense:')
        elif case == "embedded-delimiter":
            text = text.replace(package.DESCRIPTION, '"Explain --- separators safely."')
        (directory / ("skill.md" if case == "lowercase" else "SKILL.md")).write_text(text)


def check():
    arch = local_daemon()
    stamp = tools.installed()
    source_inputs = [*CATALOG_INPUTS, ROOT / "Makefile", tools.HERE]
    source = fingerprint(source_inputs)
    run_id = uuid.uuid4().hex[:12]
    report_dir = tools.OUTPUT / run_id
    report_dir.mkdir(parents=True, mode=0o700)
    private = Path(tempfile.mkdtemp(prefix="catalog-skills-"))
    report = dict(run_id=run_id, source=source, tools=stamp, success=False, cleanup=False,
                  private_directory=str(private), direct={}, refusals={}, native=[], containers=[], phase="setup")
    names = report["containers"]
    secrets = []

    def save():
        raw = json.dumps(report, indent=2, ensure_ascii=False)
        for secret in secrets:
            raw = raw.replace(secret, "[redacted]")
        (report_dir / "report.json").write_text(raw + "\n")

    def interrupted(*_):
        raise TimeoutError("Skill qualification interrupted or timed out")

    old = {s: signal.signal(s, interrupted) for s in (signal.SIGINT, signal.SIGTERM, signal.SIGALRM)}
    try:
        signal.alarm(600)
        save()
        print("Skill report:", report_dir / "report.json", flush=True)
        commands, catalog_dir, fixture, packages = (private / name for name in ("commands", "catalog", "fixture", "packages"))
        for directory in (commands, catalog_dir, fixture, packages):
            directory.mkdir()
        go = stamp["go"]
        build_binaries(commands, go, arch)
        report["binaries"] = {name: hashlib.sha256((commands / name).read_bytes()).hexdigest() for name in ("catalogd", "catalogctl")}
        report["git"] = run(["git", "--version"], timeout=10).stdout.strip()
        report["go"] = run([go, "version"], timeout=10).stdout.strip()
        shutil.copyfile(tools.HERE / "skills_fixture.py", fixture / "skills_fixture.py")
        prefix = "catalog-skills-" + run_id
        consumer, catalog_name = prefix + "-consumer", prefix + "-catalog"
        prepare_cases(packages)
        names.append(consumer)
        tools.create(consumer, stamp["image"], [(packages, "/packages")], args=("-c", "import time; time.sleep(600)"))
        docker("start", consumer, timeout=10)
        report["isolation"] = True

        def native(command, path, *, valid=True):
            assert command in ("validate", "read-properties", "to-prompt") and path.startswith("/packages/")
            report["phase"] = "native-" + command
            save()
            result = docker("exec", consumer, "skills-ref", command, path, timeout=15, limit=65536, check=False)
            report["native"].append(dict(command=command, path=path, exit=result.code, stdout=result.stdout, stderr=result.stderr))
            check_native(result, command, path, valid=valid)
            return result

        for case in sorted(DIRECT):
            name = "wrong-parent" if case == "parent-mismatch" else package.NAME
            path = "/packages/" + case + "/" + name
            valid = case not in NATIVE_REFUSALS
            native("validate", path, valid=valid)
            if case in ("valid", "parent-mismatch"):
                native("read-properties", path)
                native("to-prompt", path)
            report["direct"][case] = True
        report["phase"] = "governance"
        save()
        catalog = Catalog(catalog_dir, catalog_name)
        catalog.configuration()
        secrets.extend(catalog.tokens.values())
        names.append(catalog_name)
        tools.create(catalog_name, stamp["image"], [(commands, "/commands"), (catalog_dir, "/catalog"), (fixture, "/fixture")],
                     entrypoint="/commands/catalogd", args=("all", "--config", "/catalog/config.json"))
        docker("start", catalog_name, timeout=10)
        expires = time.monotonic() + 20
        while docker("exec", catalog_name, "/commands/catalogctl", "probe", "--url", "http://127.0.0.1:8080/readyz", timeout=5, check=False).code:
            if time.monotonic() >= expires:
                raise RuntimeError("Catalog readiness deadline")
            time.sleep(0.1)
        repo = private / "repository"
        locator = package.seed(repo)
        archive = package.archive(package.FILES)
        expected, listing = catalog.publish(archive, locator)
        selected = catalog.discover(expected, archive, locator)
        representation = selected["versionRevision"]["representations"][0]
        report["selection"] = dict(expected, locator=locator, archive_digest=package.digest(archive))
        later = dict(locator, commit=package.advance(repo))
        later_files = dict(package.FILES, **{"references/guide.txt": (b"Unpublished branch content.\n", False)})
        catalog.draft(expected, listing, package.archive(later_files), later)
        same_retained(selected, catalog.discover(expected, archive, locator))
        report["immutable"] = True

        def consume(directory):
            package.verify_inventory(package.read_directory(directory), representation, archive)
            path = "/packages/" + str(directory.relative_to(packages))
            for command in ("validate", "read-properties", "to-prompt"):
                native(command, path)

        target = packages / "governed" / package.NAME
        target.parent.mkdir()
        package.materialize(repo, representation["handoff"]["git"], target)
        consume(target)
        report["governed"] = True
        for case in ("missing-commit", "missing-directory", "altered-file", "missing-file", "extra-file", "executable-mode"):
            destination = packages / case / package.NAME
            destination.parent.mkdir()
            changed = copy.deepcopy(locator)
            if case == "missing-commit":
                changed["commit"] = "f" * 40
            if case == "missing-directory":
                changed["directory"] = "absent/" + package.NAME
            before = len(report["native"])
            try:
                package.materialize(repo, changed, destination)
                if case == "altered-file":
                    (destination / "references/guide.txt").write_text("altered")
                elif case == "missing-file":
                    (destination / "references/guide.txt").unlink()
                elif case == "extra-file":
                    (destination / "extra.txt").write_text("extra")
                elif case == "executable-mode":
                    (destination / "scripts/sentinel.py").chmod(0o644)
                consume(destination)
            except (ValueError, RuntimeError, AssertionError) as error:
                check_refusal(case, error, before, len(report["native"]))
            else:
                raise AssertionError("bad handoff consumed: " + case)
            report["refusals"][case] = True
        for case in ("missing", "invalid", "wrong-tenant"):
            before = len(report["native"])
            catalog.denial(case, expected["release"])
            assert len(report["native"]) == before
            report["refusals"][case] = True
        same_retained(selected, catalog.discover(expected, archive, locator))
        report["retained"] = True
        package.verify_inventory(package.read_directory(target), representation, archive)
        sentinel = docker("exec", consumer, "python", "-B", "-c", "from pathlib import Path; assert not Path('/tmp/skill-executed').exists()", timeout=5)
        assert sentinel.code == 0
        report["no_execution"] = True
        docker("stop", "--time", "25", catalog_name, timeout=30)
        state = json.loads(docker("inspect", catalog_name).stdout)[0]["State"]
        assert not state["Running"] and state["ExitCode"] == 0
        report["source_unchanged"] = source == fingerprint(source_inputs)
    except BaseException as error:
        report["error_type"] = type(error).__name__
        report["error_frames"] = [{"file": Path(f.filename).name, "line": f.lineno, "function": f.name} for f in traceback.extract_tb(error.__traceback__)]
        report["creation_unconfirmed"] = isinstance(error, tools.CreationUnconfirmed)
        # Fixed stage plus native synthetic outputs suffice; dependency chains can contain credentials.
        raise
    finally:
        signal.alarm(0)
        previous = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
        try:
            try:
                tools.remove(names)
            finally:
                shutil.rmtree(private)
            report["cleanup"] = not private.exists() and not report.get("creation_unconfirmed", False)
        finally:
            save()
            for sig, handler in old.items():
                signal.signal(sig, handler)
            signal.pthread_sigmask(signal.SIG_SETMASK, previous)
    complete(report)
    report["success"] = True
    report["phase"] = "complete"
    save()
    print("Independent Skill consumption passed:", run_id)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("install", "check"))
    args = parser.parse_args()
    try:
        if args.action == "install":
            tools.install()
        else:
            check()
    except (Exception, KeyboardInterrupt) as error:
        print("Skill qualification failed (" + type(error).__name__ + "); inspect the private report/installation log.")
        raise SystemExit(1) from None


if __name__ == "__main__":
    if not __debug__:
        print("Skill qualification requires Python assertions", file=sys.stderr)
        raise SystemExit(2)
    main()
