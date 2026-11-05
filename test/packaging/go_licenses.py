"""Check imported Go licenses with one reviewed, test-only Rapid exception."""

import argparse
import csv
import io
import json
import os
from pathlib import Path
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[2]
RAPID = "pgregory.net/rapid"
RAPID_VERSION = "v1.3.0"
TAGS = "functional,integration,helm,minikube"
COMMANDS = ("./cmd/catalogd", "./cmd/catalogctl", "./catalog-ui/cmd/catalog-ui")


def packages(raw):
    """Decode Go's JSON object stream; missing discovery is not a clean scan."""
    result = []
    decoder = json.JSONDecoder()
    remaining = raw.strip()
    while remaining:
        value, end = decoder.raw_decode(remaining)
        if not isinstance(value, dict) or not isinstance(value.get("ImportPath"), str):
            raise ValueError("invalid Go dependency discovery")
        result.append(value)
        remaining = remaining[end:].lstrip()
    if not result:
        raise ValueError("empty Go dependency discovery")
    return result


def rapid_dependency(discovered, *, tests):
    """Approve only the unmodified module pin and only in the test closure."""
    present = False
    for package in discovered:
        module = package.get("Module") or {}
        path = package["ImportPath"].split(" ", 1)[0]
        if module.get("Path") != RAPID and path != RAPID and not path.startswith(RAPID + "/"):
            continue
        if not tests:
            raise ValueError("Rapid is forbidden in non-test dependencies")
        if module.get("Path") != RAPID or module.get("Version") != RAPID_VERSION or module.get("Replace"):
            raise ValueError("Rapid requires the reviewed v1.3.0 module without replacement")
        present = True
    return present


def check_report(raw, allowed, rapid_present):
    """Enforce every reported license, including unknown and transitive entries."""
    seen = set()
    saw_rapid = False
    for row in csv.reader(io.StringIO(raw), strict=True):
        if len(row) != 3 or not all(row) or row[0] in seen:
            raise ValueError("invalid or duplicate Go license report entry")
        package, _, license_name = row
        seen.add(package)
        if package == RAPID or package.startswith(RAPID + "/"):
            if not rapid_present or license_name != "MPL-2.0":
                raise ValueError("Rapid license report disagrees with the reviewed test dependency")
            saw_rapid = True
        elif license_name not in allowed:
            raise ValueError(f"unapproved Go license: {package}: {license_name}")
    if not seen or saw_rapid != rapid_present:
        raise ValueError("incomplete Go license report")


def run(command, environment):
    result = subprocess.run(command, cwd=ROOT, env=environment, capture_output=True,
                            text=True, check=False, timeout=300)
    if result.returncode:
        raise ValueError(result.stderr.strip() or "Go license command failed")
    return result.stdout


def check(args):
    # Ignore ambient build selections; analyze the same maintained closure as Make.
    environment = dict(os.environ, GOTOOLCHAIN=args.toolchain, GOWORK="off",
                       GOFLAGS="-mod=readonly", GOTELEMETRY="off")
    listing = [args.go, "list", "-deps", "-json=ImportPath,Module", "-tags=" + TAGS]
    discovered = packages(run([*listing, "-test", *args.source_root], environment))
    present = rapid_dependency(discovered, tests=True)
    if present:
        rapid_dependency(packages(run([*listing, *args.source_root], environment)), tests=False)
    # Always inspect release-only imports, even if the host test closure has no Rapid.
    for architecture in ("amd64", "arm64"):
        release_env = dict(environment, GOOS="linux", GOARCH=architecture, CGO_ENABLED="0")
        release = [args.go, "list", "-deps", "-json=ImportPath,Module",
                   "-tags=netgo,osusergo", *COMMANDS]
        rapid_dependency(packages(run(release, release_env)), tests=False)
    report = run([args.go, "run", "github.com/google/go-licenses@" + args.version,
                  "report", "--include_tests", "--ignore=" + args.module, *args.source_root],
                 dict(environment, GOFLAGS="-mod=readonly -tags=" + TAGS))
    check_report(report, set(args.allowed.split(",")), present)
    print("Go dependency licenses satisfy the policy (reviewed Rapid test-only exception).")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go")
    parser.add_argument("--toolchain", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--module", required=True)
    parser.add_argument("--allowed", required=True)
    parser.add_argument("--source-root", action="append", required=True)
    args = parser.parse_args()
    try:
        check(args)
    except (ValueError, csv.Error, OSError, subprocess.TimeoutExpired) as error:
        print(f"Go license check failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
