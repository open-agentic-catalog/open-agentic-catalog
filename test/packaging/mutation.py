"""Bounded, explicit mutation measurement of reviewed Go unit packages."""

import argparse
from collections import Counter
from contextlib import contextmanager
import hashlib
import json
import os
from pathlib import Path
import re
import selectors
import shlex
import signal
import subprocess
import sys
import tempfile
import time

sys.path.insert(0, str(Path(__file__).resolve().parent))
from modules import stage  # noqa: E402
import property as properties  # noqa: E402

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "functional"))
from runner import Failure, command, digest_files  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
SCOPES = {
    "./internal/publication": ("internal/publication/limits.go",),
    "./internal/platform/config": ("internal/platform/config/federation_policy.go", "internal/platform/config/webhook_policy.go"),
    "./catalog-ui/internal/web": ("catalog-ui/internal/web/session.go", "catalog-ui/internal/web/transport.go"),
    "./internal/webhook": ("internal/webhook/retry.go", "internal/webhook/signing.go"),
    "./internal/audit": ("internal/audit/export.go",),
    "./internal/adapter": ("internal/adapter/skill_package.go",),
    "./internal/catalog": ("internal/catalog/urn.go", "internal/catalog/search_cursor.go", "internal/catalog/semantic_cursor.go"),
    "./internal/platform/httpserver": ("internal/platform/httpserver/admin_pagination.go",),
    "./internal/platform/fetcher": ("internal/platform/fetcher/fetcher.go",),
    "./internal/platform/authn": ("internal/platform/authn/authn.go",),
    "./internal/jsondoc": ("internal/jsondoc/jsondoc.go",),
    "./internal/identity": ("internal/identity/identity.go", "internal/identity/access.go"),
    "./internal/platform/idempotency": ("internal/platform/idempotency/store.go",),
}
CONFIG = {"json_output": True, "skip_without_test": False, "skip_with_build_tags": False,
          "enable_mutators": ["expression/comparison", "conditional/negated", "expression/logical"]}
LIMIT = 8 << 20
TEST_SECONDS = 10
HOOK_SECONDS = 60
NATIVE_SECONDS = 90
RUN_SECONDS = 600
MAX_MUTANTS = 1024
BUCKETS = {"killed": "caught", "escaped": "survived", "skipped": "timeout", "errored": "error", "notCovered": "uncovered"}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def unique(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, "duplicate JSON key")
        result[key] = value
    return result


def decode(raw):
    def invalid(_):
        raise ValueError("non-finite JSON number")
    return json.loads(raw, object_pairs_hook=unique, parse_constant=invalid)


def regular(path, base):
    path = Path(os.path.abspath(path))
    require(path.is_relative_to(base), "path outside owned directory")
    require(not path.is_symlink() and not any(p.is_symlink() for p in path.parents), "symlinked input")
    require(path.is_file(), "missing regular file")
    return path


def read(path):
    path = regular(Path(path), Path(path).absolute().parent)
    with path.open("rb") as stream:
        raw = stream.read(LIMIT + 1)
    require(len(raw) <= LIMIT, "input byte limit")
    return raw


def write_json(path, data):
    raw = (json.dumps(data, sort_keys=True, indent=2, allow_nan=False) + "\n").encode()
    require(len(raw) <= LIMIT, "report byte limit")
    with Path(path).open("xb") as stream:
        stream.write(raw)


def environment(source, toolchain):
    # Keep dependency/cache/host prerequisites, never runtime or test selection.
    keep = ("PATH", "HOME", "TMPDIR", "TEMP", "TMP", "GOPATH", "GOCACHE", "GOMODCACHE",
            "GOPROXY", "GONOPROXY", "GOPRIVATE", "SSL_CERT_FILE", "SSL_CERT_DIR",
            "HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY")
    env = {key: source[key] for key in keep if key in source}
    env.update(GOENV="off", GOTOOLCHAIN=toolchain, GOFLAGS="-mod=readonly", GOWORK="off",
               GOPACKAGESDRIVER="off", CGO_ENABLED="0", GOTELEMETRY="off", GOSUMDB="sum.golang.org",
               PYTHONOPTIMIZE="0", PYTHONDONTWRITEBYTECODE="1", LC_ALL="C", TZ="UTC")
    return env


@contextmanager
def working(directory):
    old = Path.cwd()
    os.chdir(directory)
    try:
        yield
    finally:
        os.chdir(old)


def run(args, env, directory, seconds=60):
    with working(directory):
        return command(args, env, seconds, limit=LIMIT, capture=True)


def tool_environment(go, toolchain):
    env = environment(os.environ, toolchain)
    # Toolchain acquisition writes progress to stderr. Complete it before the
    # structured env query consumed through the shared combined-output helper.
    run([go, "version"], env, ROOT)
    values = decode(run([go, "env", "-json", "GOROOT", "GOVERSION", "GOHOSTOS", "GOHOSTARCH"], env, ROOT))
    require(values["GOVERSION"] == toolchain, "unexpected Go toolchain")
    require((values["GOHOSTOS"], values["GOHOSTARCH"]) == ("linux", "amd64"), "mutation workflow currently qualifies Linux amd64 hosts only")
    go = str(Path(values["GOROOT"]) / "bin/go")
    env.update(PATH=str(Path(go).parent) + os.pathsep + env.get("PATH", ""),
               GOOS=values["GOHOSTOS"], GOARCH=values["GOHOSTARCH"])
    return go, env


def acquire(go, env, directory):
    pin = decode(read(Path(__file__).with_name("mutation-tool.json")))
    require(set(pin) == {"module", "version", "sum", "revision"}, "invalid tool pin")
    require(pin["module"] == "github.com/quality-gates/mutago/v2" and
            re.fullmatch(r"v\d+\.\d+\.\d+", pin["version"]) and
            re.fullmatch(r"h1:[A-Za-z0-9+/]{43}=", pin["sum"]) and
            re.fullmatch(r"[0-9a-f]{40}", pin["revision"]), "invalid tool identity")
    directory.mkdir()
    run([go, "install", pin["module"] + "/cmd/mutago@" + pin["version"]],
        dict(env, GOBIN=str(directory)), directory, 300)
    tool = directory / "mutago"
    metadata = run([go, "version", "-m", str(tool)], env, directory).decode()
    require(f": {env['GOTOOLCHAIN']}\n" in metadata and
            f"\tpath\t{pin['module']}/cmd/mutago\n" in metadata and
            f"\tmod\t{pin['module']}\t{pin['version']}\t{pin['sum']}\n" in metadata,
            "installed tool identity mismatch")
    # Compiler build information binds the actual linked module closure.
    digest = hashlib.sha256()
    require(tool.stat().st_size <= 128 << 20, "tool binary size limit")
    with tool.open("rb") as stream:
        while block := stream.read(65536):
            digest.update(block)
    return tool, dict(pin, binarySHA256=digest.hexdigest(), buildInfo=metadata)


def classify(raw, package, code, baseline=False, property_tests=(), property_checks=properties.DEFAULT_CHECKS):
    """Conservative Go 1.27 event classification; package failure alone is not a catch."""
    require(len(raw) <= LIMIT and raw.endswith(b"\n"), "missing/truncated Go events")
    runs, terminal_tests, package_terminal, output = set(), {}, [], []
    starts = 0
    for line in raw.splitlines():
        e = decode(line)
        require(isinstance(e, dict) and isinstance(e.get("Action"), str), "invalid Go event")
        action = e["Action"]
        require(action in {"start", "run", "pause", "cont", "pass", "fail", "skip", "output", "build-output", "build-fail"}, "unknown Go action")
        if action.startswith("build-") or e.get("FailedBuild"):
            return "error", []
        require(e.get("Package") == package, "unexpected Go package")
        require(not package_terminal, "event after package completion")
        require(starts == 1 or action == "start", "event before package start")
        if "Output" in e:
            require(isinstance(e["Output"], str), "invalid Go output")
            output.append(e["Output"])
        test = e.get("Test")
        require(test is None or isinstance(test, str) and bool(test), "invalid Go test identity")
        if action == "start":
            require(not test and starts == 0, "invalid package start")
            starts += 1
        elif action == "run":
            require(test and test not in runs and not package_terminal, "duplicate/invalid test start")
            runs.add(test)
        elif action in {"pass", "fail", "skip"}:
            if test:
                require(test in runs and test not in terminal_tests and not package_terminal, "invalid test completion")
                terminal_tests[test] = action
            else:
                package_terminal.append(action)
    require(starts == 1 and len(package_terminal) == 1, "missing/duplicate package completion")
    text = "".join(output)
    failed = sorted(name for name, action in terminal_tests.items() if action == "fail")
    if any(line.startswith("signal: ") for line in text.splitlines()):
        return "error", failed
    if code == 1 and package_terminal == ["fail"] and "panic: test timed out after " in text:
        return "timeout", failed
    if property_tests:
        try:
            properties.validate_events(raw, package, property_tests, property_checks, code != 0)
        except (ValueError, TypeError):
            return "error", failed
    if code == 0 and package_terminal == ["pass"] and runs and set(terminal_tests) == runs and not failed:
        if baseline and "pass" not in terminal_tests.values():
            return "error", []
        return ("passed" if baseline else "survived"), []
    if not baseline and code == 1 and package_terminal == ["fail"] and failed:
        return "caught", failed
    return "error", failed


def selected_sources(listed, files, root):
    require(isinstance(listed, dict) and not listed.get("Error") and not listed.get("Incomplete"), "failed Go discovery")
    require(isinstance(listed.get("ImportPath"), str) and listed["ImportPath"], "missing package identity")
    selected = {str(Path(files[0]).parent / name) for name in listed.get("GoFiles", [])}
    require(set(files) <= selected and not listed.get("CgoFiles"), "source outside unit build selection")
    require(listed.get("TestGoFiles") or listed.get("XTestGoFiles"), "package has no unit tests")
    sources = {}
    for name in files:
        raw = read(regular(root / name, root))
        require(not re.search(rb"(?m)^// Code generated .* DO NOT EDIT\.$", raw), "generated mutation source")
        sources[name] = raw.decode("utf-8")
    return sources


def native_rows(data, sources, inventory=False):
    require(isinstance(data, dict) and data.get("sources") == sources, "native source inventory mismatch")
    require(set(data) <= {"stats", "mutatorStats", "sources", *BUCKETS}, "unexpected native report field")
    rows, ids = {}, set()
    counts = {}
    for bucket, status in BUCKETS.items():
        values = data.get(bucket)
        if values is None:
            values = []
        require(isinstance(values, list), "invalid native bucket")
        counts[bucket] = len(values)
        for row in values:
            require(isinstance(row, dict) and set(row) == {"checksum", "mutator", "diff", "processOutput"}, "invalid native row")
            identifier, meta, diff = row["checksum"], row["mutator"], row["diff"]
            require(isinstance(identifier, str) and re.fullmatch(r"[0-9a-f]{32}", identifier) and identifier not in ids, "duplicate/invalid native ID")
            require(isinstance(meta, dict) and set(meta) == {"mutatorName", "originalFilePath", "originalStartLine"}, "invalid mutation metadata")
            name, line = meta["originalFilePath"], meta["originalStartLine"]
            require(isinstance(name, str) and name in sources and type(line) is int and 1 <= line <= len(sources[name].splitlines()), "invalid mutation location")
            require(meta["mutatorName"] in CONFIG["enable_mutators"], "unexpected operator")
            require(isinstance(diff, str) and diff.startswith("--- Original\n+++ New\n@@ "), "missing native diff")
            require(isinstance(row["processOutput"], str), "invalid native diagnostics")
            key = (name, sha(diff.encode()))
            require(key not in rows, "duplicate native edit")
            require(status == "survived" if inventory else status in {"caught", "survived", "timeout"}, "unexplained native outcome")
            ids.add(identifier)
            rows[key] = dict(id=identifier, file=name, line=line, operator=meta["mutatorName"],
                             diff=diff, diffSHA256=key[1], status="unassessed" if inventory else status)
    require(0 < len(rows) <= MAX_MUTANTS, "empty/excessive mutation inventory")
    stats = data.get("stats")
    require(isinstance(stats, dict), "missing native totals")
    for bucket, count in counts.items():
        field = {"killed": "killedCount", "escaped": "escapedCount", "skipped": "skippedCount", "errored": "errorCount", "notCovered": "notCoveredCount"}[bucket]
        require(type(stats.get(field)) is int and stats[field] == count, "native count mismatch")
    require(type(stats.get("totalMutantsCount")) is int and stats["totalMutantsCount"] == len(rows), "native total mismatch")
    return rows


def reconcile(data, records, sources, previous=None):
    rows = native_rows(data, sources, inventory=previous is None)
    require(len(records) == len(rows), "missing/extra execution records")
    seen = set()
    for r in records:
        require(isinstance(r, dict) and set(r) == {"file", "sourceSHA256", "mutantSHA256", "diffSHA256", "status", "failedTests"}, "invalid hook record")
        require(isinstance(r["file"], str), "invalid hook filename")
        require(all(isinstance(r[k], str) and re.fullmatch(r"[a-f0-9]{64}", r[k]) for k in ("sourceSHA256", "mutantSHA256", "diffSHA256")), "invalid hook digest")
        key = (r["file"], r["diffSHA256"])
        require(key in rows and key not in seen, "unknown/duplicate hook identity")
        require(r["sourceSHA256"] == sha(sources[key[0]].encode()) and r["status"] == rows[key]["status"], "hook source/status mismatch")
        require(isinstance(r["failedTests"], list) and all(isinstance(v, str) and v for v in r["failedTests"]), "invalid test evidence")
        require(r["status"] != "caught" or r["failedTests"], "catch without test evidence")
        rows[key].update(mutantSHA256=r["mutantSHA256"], failedTests=r["failedTests"])
        seen.add(key)
    if previous is not None:
        require(set(rows) == set(previous), "incomplete mutant inventory")
        for key, row in rows.items():
            require(all(row[k] == previous[key][k] for k in ("id", "file", "line", "operator", "mutantSHA256")), "mutation changed between passes")
        require(any(r["status"] in {"caught", "survived"} for r in rows.values()), "zero assessed mutants")
    return rows


def capture_child(args, env, seconds=HOOK_SECONDS):
    """Drain a hook's Go/diff child inside the outer launcher's owned group."""
    proc = subprocess.Popen(args, env=env, stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
    deadline, raw = time.monotonic() + seconds, bytearray()
    try:
        with selectors.DefaultSelector() as selector:
            selector.register(proc.stdout, selectors.EVENT_READ)
            while selector.get_map():
                remaining = deadline - time.monotonic()
                require(remaining > 0, "hook infrastructure deadline")
                for key, _ in selector.select(min(remaining, .1)):
                    block = os.read(key.fileobj.fileno(), 65536)
                    if not block:
                        selector.unregister(key.fileobj)
                    raw.extend(block)
                    require(len(raw) <= LIMIT, "hook output byte limit")
            proc.wait(timeout=max(.001, deadline - time.monotonic()))
        return proc.returncode, bytes(raw)
    finally:
        if proc.poll() is None:
            proc.kill()
        proc.wait(timeout=5)
        proc.stdout.close()


def interrupt(_signum, _frame):
    raise KeyboardInterrupt


def hook(config_path):
    """Internal entry point; the parent command owns the entire process group."""
    require(os.getpgrp() in {os.getpid(), os.getppid()}, "hook needs an invocation-owned process group")
    config = decode(read(config_path))
    root, evidence = Path(config["root"]), Path(config["evidence"])
    mode = config["mode"]
    require(mode in {"baseline", "inventory", "execute"}, "invalid hook mode")
    record = None
    path = evidence / "result.json"
    try:
        args = [config["go"], "test", "-json", "-vet=off", "-tags=minikube", "-count=1", f"-timeout={TEST_SECONDS}s"]
        if mode != "baseline":
            original = regular(Path(os.environ["MUTATE_ORIGINAL"]).absolute(), root)
            changed = regular(Path(os.environ["MUTATE_CHANGED"]).absolute(), Path(config["temporary"]))
            name = str(original.relative_to(root))
            require(name in config["sources"] and os.environ["MUTATE_PACKAGE"] == config["package"], "hook source/package mismatch")
            source, mutant = read(original), read(changed)
            require(sha(source) == config["sources"][name], "hook original changed")
            code, diff = capture_child(["diff", "--label=Original", "--label=New", "-u", str(original), str(changed)], os.environ, 5)
            require(code == 1 and diff.startswith(b"--- Original\n+++ New\n@@ "), "diff prerequisite failed")
            identity = sha(name.encode() + b"\0" + diff)
            path = evidence / (identity + ".result.json")
            # Claim first: a repeated native invocation must not overwrite evidence.
            with (evidence / (identity + ".claim")).open("xb"):
                pass
            record = dict(file=name, sourceSHA256=sha(source), mutantSHA256=sha(mutant),
                          diffSHA256=sha(diff), status="unassessed", failedTests=[])
            if mode == "inventory":
                write_json(path, record)
                return 1
            overlay = evidence / (identity + ".overlay.json")
            write_json(overlay, {"Replace": {str(original): str(changed)}})
            args.append("-overlay=" + str(overlay))
        else:
            identity = "baseline"
        property_config = config.get("properties")
        if property_config:
            require(properties.corpus(root, property_config["selector"]) == property_config["corpus"], "property corpus changed")
        code, raw = capture_child([*args, config["package"]], os.environ)
        (evidence / (identity + ".events.jsonl")).write_bytes(raw)
        if property_config:
            require(properties.corpus(root, property_config["selector"]) == property_config["corpus"], "property corpus changed")
        status, failed = classify(raw, config["package"], code, baseline=mode == "baseline",
                                  property_tests=property_config["tests"] if property_config else ())
        if record is None:
            record = {"status": status, "failedTests": failed}
        else:
            record.update(status=status, failedTests=failed)
        write_json(path, record)
        return {"passed": 0, "caught": 0, "survived": 1, "timeout": 2}.get(status, 3)
    except (OSError, ValueError, KeyError, TypeError, subprocess.TimeoutExpired, KeyboardInterrupt):
        # No partial/malformed execution may continue and become a finding. The
        # outer command's finally block reaps this exclusively owned group.
        if record is not None and not path.exists():
            record.update(status="error", failedTests=[])
            write_json(path, record)
        os.killpg(os.getpgrp(), signal.SIGKILL)
        return 3


def measure(root, output, files, package, go, env, tool, seconds=RUN_SECONDS):
    """One structured baseline, then upstream inventory and full mutation passes."""
    deadline = time.monotonic() + seconds
    # Source identity is recorded separately; snapshots must not inherit VCS
    # stamping from a checkout or unrelated parent of the temporary directory.
    property_settings = properties.settings(str(properties.DEFAULT_CHECKS), "1")
    selected = dict(properties.environment(env, property_settings),
                    GOFLAGS="-mod=readonly -buildvcs=false -tags=minikube -count=1")
    selector = "./" + str(Path(files[0]).parent)
    # Fetch the complete test dependency closure without executing tests. This
    # keeps ordinary download progress out of subsequent structured Go events.
    run([go, "list", "-deps", "-test", selector], selected, root)
    listed = decode(run([go, "list", "-json", selector], selected, root))
    sources = selected_sources(listed, files, root)
    require(listed["ImportPath"] == package, "unexpected discovered package")
    property_tests = properties.names(run([go, "test", "-list=^TestProperty", selector], selected, root), required=False)
    if property_tests:
        pin = decode(run([go, "list", "-m", "-json", properties.MODULE], selected, root))
        require(pin.get("Version") == properties.VERSION and not pin.get("Replace"), "unexpected Rapid pin")
    property_config = dict(settings=property_settings, selector=selector, tests=property_tests,
                           corpus=properties.corpus(root, selector))
    write_json(output / "properties.json", property_config)
    temporary = root / ".mutation-tmp"
    temporary.mkdir()
    selected["TMPDIR"] = str(temporary)
    config_path = output / "native-config.json"
    write_json(config_path, CONFIG)
    # The native --exec parser splits on spaces. A relative launcher preserves
    # paths containing spaces without adding another executable-selection API.
    launcher = root / ".mutation-hook"
    with launcher.open("x") as stream:
        stream.write("#!/bin/sh\nexec " + shlex.join([sys.executable, "-I", "-B", str(Path(__file__).with_name("mutation_exec.py"))]) + "\n")
    launcher.chmod(0o700)
    previous = None
    for mode in ("baseline", "inventory", "execute"):
        phase = output / mode
        phase.mkdir()
        hook_config = phase / "config.json"
        write_json(hook_config, dict(root=str(root), evidence=str(phase), mode=mode, go=go,
                                    temporary=str(temporary), package=package,
                                    sources={name: sha(raw.encode()) for name, raw in sources.items()}, properties=property_config))
        phase_env = dict(selected, CATALOG_MUTATION_HOOK=str(hook_config))
        if mode == "baseline":
            args = [sys.executable, "-I", "-B", str(Path(__file__).with_name("mutation_exec.py"))]
        else:
            require(not (root / "report.json").exists(), "stale native report")
            args = [str(tool), "--config=" + str(config_path), "--workers=1", f"--exec-timeout={NATIVE_SECONDS}",
                    "--exec=./.mutation-hook", "--baseline=" + str(output / "unused-baseline.json"), "--no-diffs", *files]
        raw = run(args, phase_env, root, min(HOOK_SECONDS + 5 if mode == "baseline" else seconds, deadline - time.monotonic()))
        (phase / "tool.log").write_bytes(raw)
        if mode == "baseline":
            require(decode(read(phase / "result.json"))["status"] == "passed", "unqualified baseline")
            continue
        report = root / "report.json"
        data_raw = read(report)
        (phase / "native.json").write_bytes(data_raw)
        report.unlink()
        records = [decode(read(p)) for p in sorted(phase.glob("*.result.json"))]
        previous = reconcile(decode(data_raw), records, sources, previous)
    return sorted(previous.values(), key=lambda row: (row["file"], row["line"], row["id"]))


def source_identity(root):
    env = environment(os.environ, "local")
    paths = run(["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard"], env, root).decode().rstrip("\0").split("\0")
    require(paths and all(p and not Path(p).is_absolute() and ".." not in Path(p).parts for p in paths), "invalid source inventory")
    with working(root):
        return sorted(set(paths)), digest_files(set(paths))


def execute(options, root=ROOT):
    require(options.package in SCOPES, "select exactly one reviewed MUTATION_PACKAGE: " + ", ".join(SCOPES))
    require(os.name == "posix", "mutation requires POSIX process groups")
    go, env = tool_environment(options.go, options.toolchain)
    output = root / "output" / "mutation"
    require(not any(p.is_symlink() for p in (output, *output.parents)), "symlinked output directory")
    output.mkdir(parents=True, exist_ok=True)
    directory = Path(tempfile.mkdtemp(prefix="run-", dir=output))
    print("Mutation evidence: " + str(directory), flush=True)
    started = time.monotonic()
    try:
        paths, identity = source_identity(root)
        with tempfile.TemporaryDirectory(prefix="work-", dir=directory) as temporary:
            work = Path(temporary)
            snapshot = work / "source"
            # The staging helper already removes inherited Git discovery inputs.
            stage(root, snapshot)
            with working(snapshot):
                require(digest_files(paths) == identity, "staged source identity mismatch")
            require(source_identity(root)[1] == identity, "source changed during staging")
            tool, tool_info = acquire(go, env, work / "bin")
            print("Running complete package baseline, inventory and mutants: " + options.package, flush=True)
            findings = measure(snapshot, directory, SCOPES[options.package],
                               "github.com/open-agentic-catalog/open-agentic-catalog/" + options.package[2:], go, env, tool)
            with working(snapshot):
                require(digest_files(paths) == identity, "mutation modified staged source")
            require(source_identity(root)[1] == identity, "source changed during measurement")
        measured = Counter(row["status"] for row in findings)
        counts = {status: measured[status] for status in ("caught", "survived", "timeout")}
        report = dict(formatVersion=1, complete=True, sourceIdentity=identity, package=options.package,
                      files=list(SCOPES[options.package]), tags=["minikube"], config=CONFIG, tool=tool_info,
                      properties=decode(read(directory / "properties.json")),
                      go=options.toolchain, python=sys.version.split()[0], elapsedSeconds=round(time.monotonic() - started, 3), counts=counts,
                      mutants=findings, limits=dict(testSeconds=TEST_SECONDS, hookSeconds=HOOK_SECONDS,
                      nativeSeconds=NATIVE_SECONDS, runSeconds=RUN_SECONDS, acquisitionSeconds=300, bytes=LIMIT))
        write_json(directory / "report.tmp", report)
        (directory / "report.tmp").replace(directory / "report.json")
        print(f"Mutation findings: {counts.get('caught', 0)} caught, {counts.get('survived', 0)} survived, {counts.get('timeout', 0)} timed out; {len(findings)} total")
        for row in findings:
            if row["status"] != "caught":
                print(f"  {row['status']}: {row['file']}:{row['line']} {row['operator']} [{row['id']}]")
        print("Report: " + str(directory / "report.json"))
        return directory
    except (OSError, ValueError, Failure, KeyboardInterrupt):
        write_json(directory / "failure.json", {"complete": False, "message": "Measurement failed; no validated report was published."})
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--toolchain", required=True)
    args = parser.parse_args()
    args.package = os.environ.get("MUTATION_PACKAGE", "")
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, interrupt)
    try:
        execute(args)
    except (OSError, ValueError, Failure, KeyboardInterrupt) as error:
        print("Mutation measurement failed: " + (str(error) or "interrupted"), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
