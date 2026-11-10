"""Resolve build identity and perform explicit, local-only version/tool operations."""

import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tomllib

ROOT = Path(__file__).resolve().parents[2]
UV_VERSION = "0.13.6"
TOOL_PINS = {"ruff": "0.17.3", "jsonschema": "4.26.0", "bump-my-version": "1.6.7", "coverage": "7.17.0", "yamllint": "1.38.0"}
BASE = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-rc\.([1-9][0-9]*))?\Z")
IDENTITY = re.compile(r"[A-Za-z0-9][A-Za-z0-9._+-]{0,127}\Z")
SHA = re.compile(r"(?:[0-9a-f]{40}|[0-9a-f]{64})\Z")


def environment():
    """Keep process tools independent of caller Git, uv and bump configuration."""
    return {key: value for key, value in os.environ.items()
            if not key.startswith(("GIT_", "UV_", "BUMPVERSION_"))
            and key not in ("VIRTUAL_ENV", "PYTHONPATH", "PYTHONHOME")}


def run(arguments, root, *, env=None):
    """Run a bounded argv-only tool and retain errors privately."""
    result = subprocess.run(arguments, cwd=root, env=environment() if env is None else env,
                            capture_output=True, text=True, timeout=120, check=False)
    if result.returncode:
        raise ValueError("tool command failed; check prerequisites and local repository state")
    return result.stdout.strip()


def base_version(root):
    """Read the sole release base, rejecting whitespace and partial matches."""
    value = (root / "VERSION").read_text(encoding="ascii")
    if not value.endswith("\n") or not BASE.fullmatch(value[:-1]) or len(value) > 100:
        raise ValueError("VERSION must contain one numeric or rc.N version and a final newline")
    return value[:-1]


def source(root):
    """Inspect only this checkout, never a parent or an overridden Git work tree."""
    if not (root / ".git").exists():
        return "unknown", "unknown", ()
    actual = Path(run(["git", "rev-parse", "--show-toplevel"], root)).resolve()
    if actual != root.resolve():
        raise ValueError("Git root does not match source root")
    commit = run(["git", "rev-parse", "--verify", "HEAD"], root)
    if not SHA.fullmatch(commit):
        raise ValueError("invalid Git commit identity")
    status = run(["git", "status", "--porcelain", "--untracked-files=normal"], root)
    tags = run(["git", "tag", "--points-at", "HEAD"], root).splitlines()
    return commit, "modified" if status else "clean", tuple(tags)


def metadata(root, version=None, commit=None):
    """Resolve once; explicit metadata is never evidence of a released source tree."""
    for value in (version, commit):
        if value is not None and not IDENTITY.fullmatch(value):
            raise ValueError("build overrides must be 1-128 safe identifier characters")
    base = base_version(root)
    actual, state, tags = source(root)
    overridden = version is not None or commit is not None
    if overridden:
        state = "unknown"
    effective = version
    if effective is None:
        effective = base
        if overridden or state != "clean" or "v" + base not in tags:
            effective += (".dev" if "-" in base else "-dev")
            if actual != "unknown":
                effective += "+g" + actual[:12]
    return {"version": effective, "commit": commit if commit is not None else actual, "sourceState": state}


def check(root):
    """Check configuration parity without installing or modifying anything."""
    base = base_version(root)
    config = tomllib.loads((root / "pyproject.toml").read_text())
    expected = [f"{name}=={pin}" for name, pin in TOOL_PINS.items()]
    if config.get("project") is not None or config["dependency-groups"]["dev"] != expected:
        raise ValueError("shared dev group must match the reviewed exact tool pins")
    if config["tool"]["uv"]["required-version"] != "==" + UV_VERSION:
        raise ValueError("uv pin differs from the qualified installer")
    if tomllib.loads((root / "ruff.toml").read_text())["required-version"] != "==" + TOOL_PINS["ruff"]:
        raise ValueError("Ruff configuration and dependency pin disagree")
    bump = config["tool"]["bumpversion"]
    if "current_version" in bump or any(bump.get(key, True) for key in ("commit", "tag", "push", "allow_dirty")):
        raise ValueError("bump configuration must not duplicate VERSION or enable repository mutation")
    if bump["files"] != bump_files():
        raise ValueError("bump files must match VERSION, OpenAPI and the derived package metadata")
    openapi_version_check(root, base)
    sdk_version_check(root, base)


def openapi_version_check(root, base):
    """Read canonical info metadata without adding a bootstrap YAML dependency.

    The API gate validates YAML. This check keeps the two-space version field
    used by the bump rule unique and equal to the application release base.
    """
    document = (root / "api/openapi/control-v1.yaml").read_text()
    blocks = re.findall(r"(?m)^info:\n((?:[ \t][^\n]*\n|\n)*)", document)
    versions = re.findall(r"(?m)^  version: ([^\n]+)$", blocks[0]) if len(blocks) == 1 else []
    if versions != [base] or document.count("\n  version: " + base + "\n") != 1:
        raise ValueError("OpenAPI info.version must match VERSION in the canonical two-space metadata block")


def bump_files():
    """Exact metadata edits; never replace dependency versions accidentally."""
    rules = [{"filename": "VERSION", "search": "{current_version}", "replace": "{new_version}"}]
    for filename, pattern in (
        ("api/openapi/control-v1.yaml", '\n  version: {version}\n'),
        ("sdk/rust/Cargo.toml", 'name = "open-agentic-catalog"\nversion = "{version}"'),
        ("sdk/rust/Cargo.lock", 'name = "open-agentic-catalog"\nversion = "{version}"'),
        ("test/sdk/rust/consumer/Cargo.toml", 'open-agentic-catalog = {{ path = "../../../../sdk/rust", version = "{version}" }}'),
        ("test/sdk/rust/consumer/Cargo.lock", 'name = "open-agentic-catalog"\nversion = "{version}"'),
        ("examples/rust-catalog-report/Cargo.toml", 'open-agentic-catalog = {{ path = "../../sdk/rust", version = "{version}" }}'),
        ("examples/rust-catalog-report/Cargo.lock", 'name = "open-agentic-catalog"\nversion = "{version}"'),
        ("sdk/python/pyproject.toml", 'version = "{version}"'),
        ("test/sdk/python/uv.lock", 'name = "open-agentic-catalog"\nversion = "{version}"'),
        ("examples/langgraph/uv.lock", 'name = "open-agentic-catalog"\nversion = "{version}"'),
        ("sdk/typescript/package.json", '"name": "@open-agentic-catalog/client",\n  "version": "{version}"'),
        ("sdk/typescript/package-lock.json", '"name": "@open-agentic-catalog/client",\n  "version": "{version}"'),
        ("sdk/typescript/package-lock.json", '"name": "@open-agentic-catalog/client",\n      "version": "{version}"'),
        ('extensions/vscode/package.json', '"version": "{version}",\n  "publisher": "open-agentic-catalog"'),
        ('extensions/vscode/package-lock.json', '"name": "open-agentic-catalog",\n  "version": "{version}"'),
        ('extensions/vscode/package-lock.json', '"name": "open-agentic-catalog",\n      "version": "{version}"'),
        ('extensions/vscode/package.json', 'open-agentic-catalog-client-{version}.tgz'),
        ('extensions/vscode/package-lock.json', 'open-agentic-catalog-client-{version}.tgz'),
        ('extensions/vscode/package-lock.json', '"node_modules/@open-agentic-catalog/client": {{\n      "version": "{version}"'),
    ):
        rule = {"filename": filename, "search": pattern.replace("{version}", "{current_version}"),
                "replace": pattern.replace("{version}", "{new_version}")}
        if filename in ("sdk/python/pyproject.toml", "test/sdk/python/uv.lock", "examples/langgraph/uv.lock"):
            rule["serialize"] = ["{major}.{minor}.{patch}{pre}{rc}", "{major}.{minor}.{patch}"]
        rules.append(rule)
    return rules


def sdk_version_check(root, base):
    """Root VERSION owns the release base; package formats are derived copies."""
    if tomllib.loads((root / "sdk/rust/Cargo.toml").read_text())["package"]["version"] != base:
        raise ValueError("Rust SDK version must match VERSION")
    for filename in ("sdk/rust/Cargo.lock", "test/sdk/rust/consumer/Cargo.lock", "examples/rust-catalog-report/Cargo.lock"):
        packages = tomllib.loads((root / filename).read_text())["package"]
        if [p["version"] for p in packages if p["name"] == "open-agentic-catalog"] != [base]:
            raise ValueError("Rust SDK lock version must match VERSION")
    consumer = tomllib.loads((root / "test/sdk/rust/consumer/Cargo.toml").read_text())
    if consumer["dependencies"]["open-agentic-catalog"]["version"] != base:
        raise ValueError("Rust SDK consumer version must match VERSION")
    report = tomllib.loads((root / "examples/rust-catalog-report/Cargo.toml").read_text())
    if report["dependencies"]["open-agentic-catalog"]["version"] != base:
        raise ValueError("Rust report SDK version must match VERSION")
    python = base.replace("-rc.", "rc")
    if tomllib.loads((root / "sdk/python/pyproject.toml").read_text())["project"]["version"] != python:
        raise ValueError("Python SDK version must match VERSION (PEP 440 rcN mapping)")
    locked = tomllib.loads((root / "test/sdk/python/uv.lock").read_text())["package"]
    if [p["version"] for p in locked if p["name"] == "open-agentic-catalog"] != [python]:
        raise ValueError("Python SDK lock version must match VERSION")
    example_lock = tomllib.loads((root / "examples/langgraph/uv.lock").read_text())["package"]
    if [p["version"] for p in example_lock if p["name"] == "open-agentic-catalog"] != [python]:
        raise ValueError("LangGraph example SDK lock version must match VERSION")
    package = json.loads((root / "sdk/typescript/package.json").read_text())
    lock = json.loads((root / "sdk/typescript/package-lock.json").read_text())
    if any(value != base for value in (package["version"], lock["version"], lock["packages"][""]["version"])):
        raise ValueError("TypeScript SDK package/lock versions must match VERSION")

    package = json.loads((root / "extensions/vscode/package.json").read_text())
    lock = json.loads((root / "extensions/vscode/package-lock.json").read_text())
    local_sdk = lock["packages"]["node_modules/@open-agentic-catalog/client"]
    if any(value != base for value in (package["version"], lock["version"], lock["packages"][""]["version"], local_sdk["version"])):
        raise ValueError("VS Code package/lock versions must match VERSION")
    suffix = "open-agentic-catalog-client-" + base + ".tgz"
    if not all(value.endswith(suffix) for value in (package["dependencies"]["@open-agentic-catalog/client"], lock["packages"][""]["dependencies"]["@open-agentic-catalog/client"], local_sdk["resolved"])):
        raise ValueError("VS Code SDK artifact version must match VERSION")


def release(root, version=None, commit=None):
    """Require the actual clean matching tagged tree, not build overrides."""
    if version is not None or commit is not None:
        raise ValueError("release-check does not accept build overrides")
    base = base_version(root)
    actual, state, tags = source(root)
    if state != "clean" or actual == "unknown" or "v" + base not in tags:
        raise ValueError("release-check requires clean source at the exact v<VERSION> tag")
    return metadata(root)


def private_environment_path(root):
    """Reject redirected environments before invoking an installer."""
    for path in (root / ".tools", root / ".tools/dev"):
        if path.is_symlink() or path.is_mount() or (path.exists() and not path.is_dir()):
            raise ValueError(".tools/dev must be an owned directory, not a symlink or mount")
    target = root / ".tools/dev"
    if target.exists() and not (target / "pyvenv.cfg").is_file():
        raise ValueError("incomplete .tools/dev; inspect it before removing and reinstalling")
    return target


def install(root, uv, python):
    """Acquire the locked dev group only; never auto-download an interpreter."""
    check(root)
    target = private_environment_path(root)
    if run([uv, "--version"], root).split()[:2] != ["uv", UV_VERSION]:
        raise ValueError("uv " + UV_VERSION + " is required; set UV to the pinned executable")
    python = shutil.which(python)
    if python is None:
        raise ValueError("DEV_PYTHON must select an installed Python 3.12-3.14 interpreter")
    interpreter = json.loads(run([python, "-I", "-c", "import sys,json; print(json.dumps(list(sys.version_info[:2])))"], root))
    if not [3, 12] <= interpreter < [3, 15]:
        raise ValueError("DEV_PYTHON must select an installed Python 3.12-3.14 interpreter")
    env = environment()
    env["UV_PROJECT_ENVIRONMENT"] = str(target)
    cutoff = tomllib.loads((root / "pyproject.toml").read_text())["tool"]["uv"]["exclude-newer"]
    run([uv, "sync", "--no-config", "--project", str(root), "--locked", "--only-group", "dev",
         "--python", python, "--no-python-downloads", "--no-build", "--exclude-newer", cutoff], root, env=env)
    verify_tools(target / "bin/python", root)
    print("Shared development tools installed in .tools/dev")


def verify_tools(python, root):
    """Verify all direct pins using the actual selected interpreter."""
    code = "import importlib.metadata,json; print(json.dumps({n:importlib.metadata.version(n) for n in " + repr(list(TOOL_PINS)) + "}))"
    if json.loads(run([str(python), "-I", "-c", code], root)) != TOOL_PINS:
        raise ValueError("wrong development tool versions; run make dev-tools-install")


def bump(root, executable, part, dry_run):
    """Update explicit release files without committing, tagging or pushing."""
    check(root)
    current = base_version(root)
    _, state, _ = source(root)
    if state != "clean":
        raise ValueError("version bumps require a clean Git checkout")
    if run([executable, "--version"], root) != "bump-my-version, version " + TOOL_PINS["bump-my-version"]:
        raise ValueError("bump-my-version 1.6.7 required; run make dev-tools-install")
    arguments = [executable, "bump", "--config-file", str(root / "pyproject.toml"),
                 "--current-version", current, "--no-commit", "--no-tag", "--no-push-tag", "--no-allow-dirty"]
    if part in ("major", "minor", "patch"):
        arguments.append(part)
    elif part == "rc":
        match = BASE.fullmatch(current)
        number = int(match[4]) + 1 if match[4] else 1
        arguments += ["--new-version", current.split("-", 1)[0] + f"-rc.{number}"]
    elif part == "final" and "-rc." in current:
        arguments += ["--new-version", current.split("-", 1)[0]]
    else:
        raise ValueError("PART must be major, minor, patch, rc or final (from rc.N)")
    if dry_run:
        arguments.append("--dry-run")
    print(run(arguments, root))
    print(("Preview from " if dry_run else "VERSION is now ") + (current if dry_run else base_version(root)))


def main():
    """Fixed checkout entrypoint; fixture tests call functions with owned roots."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("metadata", "tokens", "check", "release", "install", "bump", "preview"))
    parser.add_argument("--version")
    parser.add_argument("--commit")
    parser.add_argument("--uv", default="uv")
    parser.add_argument("--python", default="python3")
    parser.add_argument("--bump", default=str(ROOT / ".tools/dev/bin/bump-my-version"))
    parser.add_argument("--part", default="patch")
    args = parser.parse_args()
    try:
        if args.action in ("metadata", "tokens", "release"):
            value = release(ROOT, args.version, args.commit) if args.action == "release" else metadata(ROOT, args.version, args.commit)
            print(" ".join(value.values()) if args.action == "tokens" else json.dumps(value))
        elif args.action == "check":
            check(ROOT)
        elif args.action == "install":
            install(ROOT, args.uv, args.python)
        else:
            bump(ROOT, args.bump, args.part, args.action == "preview")
    except (OSError, ValueError, KeyError, subprocess.SubprocessError) as error:
        print(f"Version tooling: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
