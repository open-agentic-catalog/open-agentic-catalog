"""Report and enforce pinned Go function reachability without running tests."""

import argparse
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import sys
import tempfile


ROOT = Path(__file__).resolve().parents[2]
TOOL = "golang.org/x/tools/cmd/deadcode"
MAINS = {"cmd/catalogd", "cmd/catalogctl", "catalog-ui/cmd/catalog-ui"}
# Static source selections, independent of the host executing the analyzer.
SCOPES = (
    ("production-amd64", "amd64", "netgo,osusergo", False),
    ("production-arm64", "arm64", "netgo,osusergo", False),
    ("unit", "amd64", "minikube", True),
    ("functional", "amd64", "functional", True),
    ("combined", "amd64", "functional,integration,helm,minikube", True),
)


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON key: " + key)
        result[key] = value
    return result


def decode(raw):
    return json.loads(raw, object_pairs_hook=unique_object)


def text(value):
    return isinstance(value, str) and bool(value.strip()) and not any(ord(c) < 32 for c in value)


def package_path(value, module):
    return text(value) and value.startswith(module + "/") and all(
        part not in ("", ".", "..") for part in value.split("/")
    )


def parse_findings(raw, module):
    """A clean upstream report is null; missing or corrupt output must fail."""
    data = decode(raw)
    if data is None:
        return {}
    if not isinstance(data, list):
        raise ValueError("expected a package array or null")
    findings, packages = {}, set()
    for package in data:
        if not isinstance(package, dict) or set(package) != {"Name", "Path", "Funcs"}:
            raise ValueError("invalid package report")
        path = package["Path"]
        if not package_path(path, module) or not text(package["Name"]) or path in packages:
            raise ValueError("invalid or duplicate report package")
        packages.add(path)
        if not isinstance(package["Funcs"], list) or not package["Funcs"]:
            raise ValueError("empty or invalid function list")
        for function in package["Funcs"]:
            if not isinstance(function, dict) or set(function) != {"Name", "Position", "Generated", "Marker"}:
                raise ValueError("invalid function report")
            name, position = function["Name"], function["Position"]
            if not text(name) or not isinstance(position, dict) or set(position) != {"File", "Line", "Col"}:
                raise ValueError("invalid function identity or position")
            filename = position["File"]
            if not text(filename) or PurePosixPath(filename).is_absolute() or ".." in filename.split("/") or not filename.endswith(".go"):
                raise ValueError("invalid source filename")
            if any(type(position[key]) is not int or position[key] < 1 for key in ("Line", "Col")):
                raise ValueError("invalid source coordinates")
            if function["Generated"] is not False or function["Marker"] is not False:
                raise ValueError("unexpected generated or marker finding")
            key = (path, name)
            if key in findings:
                raise ValueError("duplicate function finding")
            findings[key] = f"{filename}:{position['Line']}:{position['Col']}"
    return findings


def parse_exceptions(raw, module):
    data = decode(raw)
    if not isinstance(data, list):
        raise ValueError("exceptions must be an array")
    exceptions = {}
    scopes = {scope[0] for scope in SCOPES}
    for entry in data:
        if not isinstance(entry, dict) or set(entry) != {"package", "symbol", "scopes", "reason"}:
            raise ValueError("invalid exception entry")
        if not package_path(entry["package"], module) or not text(entry["symbol"]) or not text(entry["reason"]):
            raise ValueError("exception requires an exact identity and reason")
        if any(c in entry["package"] + entry["symbol"] for c in "*?[]"):
            raise ValueError("wildcard exceptions are forbidden")
        if not isinstance(entry["scopes"], list) or not entry["scopes"]:
            raise ValueError("exception needs explicit scopes")
        for scope in entry["scopes"]:
            if not isinstance(scope, str) or scope not in scopes:
                raise ValueError("unknown exception scope")
            key = (scope, entry["package"], entry["symbol"])
            if key in exceptions:
                raise ValueError("duplicate exception")
            exceptions[key] = entry["reason"]
    return exceptions


def review(reports, exceptions):
    """Keep production and test identities separate; line movement is harmless."""
    seen, unreviewed, lines = set(), [], []
    if set(reports) != {scope[0] for scope in SCOPES}:
        raise ValueError("incomplete analysis scopes")
    for scope, _, _, _ in SCOPES:
        findings = reports[scope]
        lines.append(f"{scope}: {len(findings)} finding(s)")
        for (package, symbol), position in sorted(findings.items()):
            key = (scope, package, symbol)
            seen.add(key)
            reason = exceptions.get(key)
            lines.append(f"  {package}.{symbol} ({position})" + (f" [retained: {reason}]" if reason else " [unreviewed]"))
            if reason is None:
                unreviewed.append(key)
    stale = sorted(set(exceptions) - seen)
    for scope, package, symbol in stale:
        lines.append(f"stale exception: {scope} {package}.{symbol}")
    return "\n".join(lines), len(unreviewed) + len(stale)


def run(command, root, env, timeout=180, allow_stderr=False):
    result = subprocess.run(command, cwd=root, env=env, capture_output=True, text=True, timeout=timeout, check=False)
    if result.returncode:
        raise ValueError(f"{Path(command[0]).name} {command[1]} failed ({result.returncode}): {result.stderr.strip()}")
    # go list can exit zero on unmatched patterns, with only a warning.
    if result.stderr.strip() and not allow_stderr:
        raise ValueError("unexpected tool diagnostic: " + result.stderr.strip())
    return result.stdout


def discover(raw, module, production):
    packages, mains = set(), set()
    for line in raw.splitlines():
        parts = line.split("\t")
        if len(parts) != 3:
            raise ValueError("invalid package discovery record")
        path, name, owner = parts
        if not package_path(path, module) or owner != module or path in packages or not text(name):
            raise ValueError("invalid or duplicate discovered package")
        packages.add(path)
        if name == "main":
            mains.add(path.removeprefix(module + "/"))
    if not packages or (production and mains != MAINS):
        raise ValueError("empty package selection or unexpected application mains")
    return sorted(packages)


def analyze(options, root=ROOT):
    env = dict(os.environ, GOTOOLCHAIN=options.toolchain, GOWORK="off", GOFLAGS="-mod=readonly", GOPACKAGESDRIVER="off", CGO_ENABLED="0", GOTELEMETRY="off")
    host = run([options.go, "env", "GOHOSTOS", "GOHOSTARCH"], root, env).splitlines()
    if len(host) != 2 or any(not re.fullmatch(r"[a-z0-9]+", value) for value in host):
        raise ValueError("invalid host tool platform")
    host_env = dict(env, GOOS=host[0], GOARCH=host[1])
    actual_go = run([options.go, "env", "GOVERSION"], root, host_env).strip()
    if actual_go != options.toolchain:
        raise ValueError("unexpected Go toolchain: " + actual_go)
    roots = (options.application_root, options.source_root)
    for selection in roots:
        if not selection or len(set(selection)) != len(selection) or any(
            not re.fullmatch(r"\./[A-Za-z0-9_/-]+/\.\.\.", item) or ".." in item[:-4].split("/") for item in selection
        ):
            raise ValueError("invalid or empty owned package roots")
    output = root / "output"
    output.mkdir(exist_ok=True)
    reports, descriptions = {}, []
    # A private install avoids global/PATH state and cross-process cache races.
    # Go's module/build caches make subsequent installs cheap; nothing is shipped.
    with tempfile.TemporaryDirectory(prefix="dead-code-", dir=output) as directory:
        install_env = dict(host_env, GOBIN=directory)
        run([options.go, "install", TOOL + "@" + options.version], root, install_env, timeout=300, allow_stderr=True)
        binary = str(Path(directory) / ("deadcode.exe" if host[0] == "windows" else "deadcode"))
        metadata = run([options.go, "version", "-m", binary], root, host_env)
        if f"\tmod\tgolang.org/x/tools\t{options.version}\t" not in metadata or f": {actual_go}\n" not in metadata or f"\tpath\t{TOOL}\n" not in metadata:
            raise ValueError("installed analyzer version mismatch")
        for scope, arch, tags, tests in SCOPES:
            target_env = dict(env, GOOS="linux", GOARCH=arch)
            selected = options.source_root if tests else options.application_root
            listed = run([options.go, "list", "-tags=" + tags, "-f={{.ImportPath}}\t{{.Name}}\t{{if .Module}}{{.Module.Path}}{{end}}", *selected], root, target_env)
            packages = discover(listed, options.module, not tests)
            command = [binary, "-json", "-filter=^" + re.escape(options.module) + "(?:/|$)", "-tags=" + tags]
            if tests:
                command.append("-test")
            reports[scope] = parse_findings(run(command + packages, root, target_env), options.module)
            descriptions.append(f"{scope}: linux/{arch} CGO_ENABLED=0 tags={tags} test={str(tests).lower()} packages={len(packages)} roots={' '.join(selected)}")
    return reports, [f"deadcode {options.version}; Go {actual_go}; host {host[0]}/{host[1]}", *descriptions]


def main(arguments=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("report", "check"))
    parser.add_argument("--go", required=True)
    parser.add_argument("--toolchain", required=True)
    parser.add_argument("--version", required=True)
    parser.add_argument("--module", required=True)
    parser.add_argument("--application-root", action="append", required=True)
    parser.add_argument("--source-root", action="append", required=True)
    options = parser.parse_args(arguments)
    try:
        exceptions = parse_exceptions((ROOT / "test/packaging/dead_code_exceptions.json").read_text(), options.module)
        reports, configuration = analyze(options)
        output, failures = review(reports, exceptions)
        print("\n".join(configuration) + "\n\n" + output)
        if options.mode == "check" and failures:
            print(f"Dead-code check failed: {failures} unreviewed finding(s) or stale exception(s).", file=sys.stderr)
            return 1
    except (OSError, ValueError, subprocess.TimeoutExpired) as error:
        print("Dead-code analysis failed: " + str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
