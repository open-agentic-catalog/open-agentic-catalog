"""Admission for existing required native test selections, without a new reporter."""

import argparse
import json
from pathlib import Path
import re
import subprocess
import sys
import unittest


def cases(suite):
    """Flatten native suites without replacing their individual case identities."""
    for item in suite:
        if isinstance(item, unittest.TestSuite):
            yield from cases(item)
        else:
            yield item


def python_suite(directory, pattern):
    """Use native discovery and require each selected test file to own a case."""
    directory = Path(directory).resolve()
    loader = unittest.TestLoader()
    suite = loader.discover(str(directory), pattern=pattern)
    if loader.errors:
        raise RuntimeError("Python test discovery failed:\n" + "\n".join(loader.errors))
    tests = list(cases(suite))
    selected = {path.stem for path in directory.glob(pattern) if path.is_file()}
    observed = {test.__class__.__module__.split(".")[-1] for test in tests}
    if not tests or not selected or selected - observed:
        raise RuntimeError("No unit tests discovered or a required test file has no cases")
    return suite, len(tests)


def run_python(directory, pattern):
    """Execute the discovered native cases and require successful outcomes."""
    suite, expected = python_suite(directory, pattern)
    result = unittest.TextTestRunner(verbosity=2).run(suite)
    if (not result.wasSuccessful() or result.skipped or result.expectedFailures
            or result.unexpectedSuccesses or result.testsRun != expected):
        raise RuntimeError("Required Python tests failed, skipped or did not complete")


def go_discovery(raw):
    """Native go test -list -json identities, restricted to executable tests."""
    required = set()
    for row in raw.splitlines():
        event = json.loads(row)
        name = event.get("Output", "").strip()
        if re.fullmatch(r"Test[^\s/]*", name):
            required.add((event["Package"], name))
    if not required:
        raise RuntimeError("No required Go tests discovered")
    return required


def require_go(raw, required):
    """Require a run and passing terminal event for every declared top-level name."""
    started, passed, packages = set(), set(), set()
    for row in raw.splitlines():
        event = json.loads(row)
        action, package, name = event.get("Action"), event.get("Package"), event.get("Test")
        required_name = name and any(n == name.split("/")[0] and (p is None or p == package) for p, n in required)
        if action == "fail" or action == "skip" and required_name:
            raise RuntimeError("Required Go test failed or skipped: " + (name or package or "unknown package"))
        if required_name:
            identity = (package, name)
            if action == "run":
                if identity in started or package in packages:
                    raise RuntimeError("Required Go test has duplicate or contradictory execution: " + name)
                started.add(identity)
            elif action == "pass":
                if identity not in started or identity in passed or package in packages:
                    raise RuntimeError("Required Go test missing or incomplete: " + name)
                if any(p == package and child.startswith(name + "/") and (p, child) not in passed for p, child in started):
                    raise RuntimeError("Required Go child test missing or incomplete: " + name)
                passed.add(identity)
        elif action == "pass" and not name:
            if package in packages or any(p == package and (p, n) not in passed for p, n in started):
                raise RuntimeError("Required Go package missing or incomplete")
            packages.add(package)
    if not required:
        raise RuntimeError("No required Go tests selected")
    for package, name in required:
        matches = {(p, n) for p, n in started if n == name and (package is None or p == package)}
        if len(matches) != 1 or not matches <= passed or not {p for p, _ in matches} <= packages:
            raise RuntimeError("Required Go test missing or incomplete: " + name)


def run_go(command, required, *, cwd=None, env=None, timeout=600):
    """Preserve native diagnostics and require both native exit and complete events."""
    result = subprocess.run([str(arg) for arg in command], cwd=cwd, env=env,
                            capture_output=True, text=True, timeout=timeout, check=False)
    # Keep the native diagnostics available even when completion admission fails.
    sys.stdout.write(result.stdout)
    sys.stderr.write(result.stderr)
    if result.returncode:
        raise RuntimeError("Required Go selection failed")
    require_go(result.stdout, required)
    return result.stdout


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="kind", required=True)
    python = sub.add_parser("python")
    python.add_argument("--directory", default=".")
    python.add_argument("--pattern", default="test_unit_*.py")
    go = sub.add_parser("go")
    go.add_argument("--required", action="append", required=True)
    go.add_argument("command", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    if args.kind == "python":
        run_python(args.directory, args.pattern)
    else:
        command = args.command[1:] if args.command[:1] == ["--"] else args.command
        if len(command) < 3 or command[1] != "test" or "-json" not in command:
            raise RuntimeError("Expected native go test -json command")
        run_go(command, {(None, name) for name in args.required})


if __name__ == "__main__":
    main()
