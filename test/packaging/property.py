"""Bounded Rapid property runs and shared mutation completion checks."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import sys
import tempfile

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "interoperability"))
from common import run  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]
PACKAGES = ("./internal/jsondoc", "./internal/identity", "./internal/catalog", "./internal/platform/config", "./internal/audit", "./internal/platform/idempotency", "./internal/jobs", "./internal/platform/demodata")
MODULE = "pgregory.net/rapid"
VERSION = "v1.3.0"
DEFAULT_CHECKS = 100
PREFIX = "TestProperty"
SUCCESS = re.compile(r"\[rapid\] OK, passed ([0-9]+) tests \(")
INVALID = re.compile(r"\[rapid\] (?:only generated|ignoring fail file|fail file .* is no longer valid|flaky test|invalid value)")
LIMIT = 8 << 20


def require(condition, message):
    if not condition:
        raise ValueError(message)


def settings(checks="1000", seed="1"):
    for name, value, maximum in (("checks", checks, 10000), ("seed", seed, (1 << 64)-1)):
        require(isinstance(value, str) and re.fullmatch(r"[0-9]{1,20}", value) and
                1 <= int(value) <= maximum, "invalid property " + name)
    return dict(module=MODULE, version=VERSION, checks=int(checks), seed=int(seed),
                shrinkTime="1s", failfileWrites=False)


def environment(source, config):
    env = {key: value for key, value in source.items() if not key.startswith("RAPID_")}
    env.update(RAPID_CHECKS=str(config["checks"]), RAPID_SEED=str(config["seed"]),
               RAPID_SHRINKTIME=config["shrinkTime"], RAPID_NOFAILFILE="1",
               GOFLAGS="-mod=readonly", GOWORK="off", GOPACKAGESDRIVER="off",
               GOTELEMETRY="off", PYTHONOPTIMIZE="0")
    return env


def names(raw, required=True):
    values = [line for line in raw.decode().splitlines() if re.fullmatch(r"TestProperty\w*", line)]
    require(len(set(values)) == len(values), "duplicate property discovery")
    require(values or not required, "no properties discovered in selected package")
    return sorted(values)


def corpus(root, selector):
    """Freeze files Rapid may replay; NOFAILFILE prevents writes, not reads."""
    base = root / selector.removeprefix("./") / "testdata" / "rapid"
    require(not any(p.is_symlink() for p in (base, *base.parents)), "symlinked property corpus")
    result, size = {}, 0
    if not base.exists():
        return result
    require(base.is_dir(), "property corpus is not a directory")
    for path in sorted(base.rglob("*")):
        require(not path.is_symlink(), "symlinked property corpus")
        if path.is_dir():
            continue
        require(path.is_file(), "nonregular property corpus")
        size += path.stat().st_size
        require(size <= LIMIT and len(result) < 1000, "property corpus limit")
        result[str(path.relative_to(root))] = hashlib.sha256(path.read_bytes()).hexdigest()
    return result


def validate_events(raw, package, expected, checks, failed_run=False):
    """Reject incomplete passes or invalid Rapid evidence, including during mutants."""
    output, terminals, runs, package_end, started = {}, {}, set(), [], False
    for line in raw.splitlines():
        event = json.loads(line)
        require(isinstance(event, dict) and event.get("Package") == package, "unexpected property event package")
        action, test = event.get("Action"), event.get("Test")
        require(action in {"start", "run", "pause", "cont", "output", "pass", "fail", "skip"}, "unknown property event")
        require(test is None or isinstance(test, str), "invalid property identity")
        require(not package_end, "property event after package completion")
        if action == "start":
            require(not started and test is None, "duplicate property package start")
            started = True
        require(started, "property event before package start")
        if action == "output":
            value = event.get("Output")
            require(isinstance(value, str), "invalid property output")
            require(not INVALID.search(value) and "(cached)" not in value, "invalid or cached property evidence")
            if test:
                output[test] = output.get(test, "") + value
        if test and test.startswith(PREFIX):
            require(test in expected, "undiscovered property executed")
            if action == "run":
                require(test not in runs, "duplicate property execution")
                runs.add(test)
            elif action in {"pass", "fail", "skip"}:
                require(test in runs and test not in terminals, "invalid property completion")
                require(action != "skip", "property was skipped")
                terminals[test] = action
        elif test is None and action in {"pass", "fail", "skip"}:
            package_end.append(action)
    require(package_end == (["fail"] if failed_run else ["pass"]), "missing or invalid package completion")
    for test in expected:
        text = output.get(test, "")
        require(not INVALID.search(text), "invalid property replay or generator evidence")
        if failed_run and terminals.get(test) != "pass":
            # A genuine assertion/panic may stop execution before later properties.
            continue
        require(terminals.get(test) == "pass", "missing property: " + test)
        require(SUCCESS.findall(text) == [str(checks)], "incomplete property checks: " + test)


def source_digest(root, selector):
    """Identify selected package source and module pins, not a whole-repository snapshot."""
    paths = [root / "go.mod", root / "go.sum", *sorted((root / selector).glob("*.go"))]
    digest = hashlib.sha256()
    for path in paths:
        require(path.is_file() and not path.is_symlink(), "missing or symlinked property source")
        raw = path.read_bytes()
        require(len(raw) <= LIMIT, "property source limit")
        digest.update(str(path.relative_to(root)).encode() + b"\0" + hashlib.sha256(raw).digest())
    return digest.hexdigest()


def execute(go, toolchain, config, root=ROOT, selectors=PACKAGES):
    require(os.name == "posix", "property execution requires POSIX process groups")
    env = environment(os.environ, config)
    env["GOTOOLCHAIN"] = toolchain
    output = root / "output" / "property"
    require(not any(p.is_symlink() for p in (output, *output.parents)), "symlinked property output")
    output.mkdir(parents=True, exist_ok=True)
    evidence = Path(tempfile.mkdtemp(prefix="run-", dir=output))
    print("Property evidence: " + str(evidence), flush=True)
    old = Path.cwd()
    try:
        os.chdir(root)
        version = run([go, "env", "GOVERSION"], env=env).stdout.strip()
        require(version == toolchain, "unexpected Go toolchain")
        pin = json.loads(run([go, "list", "-m", "-json", MODULE], env=env).stdout)
        require(pin.get("Version") == VERSION and not pin.get("Replace"), "unexpected Rapid module")
        reports = []
        for index, selector in enumerate(selectors):
            package = run([go, "list", "-tags=minikube", "-f", "{{.ImportPath}}", selector], env=env).stdout.strip()
            require(package and "\n" not in package, "ambiguous property package")
            before_source = source_digest(root, selector)
            discovered = run([go, "test", "-tags=minikube", "-count=1", "-timeout=2m", "-list=^TestProperty", selector], env=env, binary=True)
            expected = names(discovered.stdout)
            before = corpus(root, selector)
            result = run([go, "test", "-json", "-tags=minikube", "-count=1", "-timeout=2m",
                          "-run=^TestProperty", selector], timeout=180, env=env, check=False, binary=True)
            (evidence / f"{index}.events.jsonl").write_bytes(result.stdout)
            (evidence / f"{index}.stderr.txt").write_text(result.stderr)
            require(corpus(root, selector) == before, "property corpus changed during execution")
            require(source_digest(root, selector) == before_source, "property source changed during execution")
            validate_events(result.stdout, package, expected, config["checks"], result.code != 0)
            require(result.code == 0, "property assertion or process failed; inspect retained events")
            reports.append(dict(package=package, tests=expected, checksPerProperty=config["checks"],
                                corpus=before, packageSourceSHA256=before_source))
        report = dict(complete=True, go=version, property=config, moduleSum=pin.get("Sum"), packages=reports)
        (evidence / "report.json").write_text(json.dumps(report, indent=2) + "\n")
        print(f"Passed {sum(len(p['tests']) for p in reports)} properties, {config['checks']} checks each; seed {config['seed']}.")
        return report
    except BaseException:
        (evidence / "failure.json").write_text(json.dumps(dict(complete=False, property=config)) + "\n")
        raise
    finally:
        os.chdir(old)


def interrupt(_signum, _frame):
    raise KeyboardInterrupt


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--toolchain", required=True)
    args = parser.parse_args()
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, interrupt)
    try:
        config = settings(os.environ.get("PROPERTY_CHECKS", "1000"), os.environ.get("PROPERTY_SEED", "1"))
        execute(args.go, args.toolchain, config)
    except (OSError, ValueError, RuntimeError, KeyboardInterrupt) as error:
        print("Property verification failed: " + (str(error) or "interrupted"), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
