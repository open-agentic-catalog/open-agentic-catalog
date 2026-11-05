"""Read-only syntax checks for current repository JSON and standalone YAML."""

import argparse
import json
import os
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
CHARTS = (
    "deployments/helm/open-agentic-catalog/templates/",
    "deployments/helm/open-agentic-catalog-monitoring/templates/",
)


def unique_object(pairs):
    """Reject duplicate names at every object depth without printing their content."""
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON object key")
        result[key] = value
    return result


def reject_constant(_value):
    """Reject Python JSON extensions such as NaN and Infinity."""
    raise ValueError("non-standard JSON numeric constant")


def validate_json(raw):
    """Validate UTF-8 JSON syntax without evaluating or rounding numeric tokens."""
    # This is syntax validation, not numeric evaluation. Retaining numeric tokens
    # avoids rounding, floating-point overflow and Python's integer digit limit.
    json.loads(raw.decode("utf-8"), object_pairs_hook=unique_object,
               parse_constant=reject_constant, parse_int=str, parse_float=str)


def yaml_scope(name):
    """Separate maintained style, immutable syntax and existing template/API owners."""
    if name.startswith(CHARTS) or name == "api/openapi/control-v1.yaml":
        return None  # Helm rendering and the existing api-yaml-check own these.
    return "upstream" if name.startswith("schemas/upstream/") else "maintained"


def source_files(root, suffixes):
    """List current non-ignored data files safely, preserving edits and deletions."""
    # Worktree checks include staged and unstaged edits and non-ignored new files.
    # Git's index/environment must not redirect inventory to another repository.
    environment = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    result = subprocess.run(
        ["git", "-C", str(root), "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
        env=environment, check=True, capture_output=True, timeout=30,
    )
    files = []
    for name in sorted(set(result.stdout.split(b"\0")) - {b""}):
        relative = Path(os.fsdecode(name))
        if relative.suffix not in suffixes:
            continue
        source = root / relative
        if source.is_symlink() or any(parent.is_symlink() for parent in source.parents if parent != root):
            raise ValueError(f"refusing symlinked data file: {str(relative)!r}")
        if not source.exists():
            continue  # An unstaged deletion is absent from the current worktree.
        if not source.is_file():
            raise ValueError(f"not a regular data file: {str(relative)!r}")
        files.append(relative)
    return files


def check_json(root):
    """Validate every current JSON input and report failures without file contents."""
    files = source_files(root, {".json", ".jsonld"})
    failures = []
    for relative in files:
        try:
            validate_json((root / relative).read_bytes())
        except (ValueError, RecursionError) as error:
            # Do not print document contents or duplicate key names.
            if isinstance(error, UnicodeDecodeError):
                reason = "invalid UTF-8"
            elif isinstance(error, json.JSONDecodeError):
                reason = f"{error.msg} at line {error.lineno}, column {error.colno}"
            else:
                reason = str(error)
            failures.append(f"{str(relative)!r}: {reason}")
    if failures:
        raise ValueError("Invalid JSON:\n" + "\n".join(failures))
    print(f"JSON syntax checked: {len(files)} files (including JSON-LD).")


def check_yaml(root, executable):
    """Apply existing yamllint policy with a separate syntax-only upstream scope."""
    files = source_files(root, {".yaml", ".yml"})
    for scope in ("maintained", "upstream"):
        selected = [str(root / path) for path in files if yaml_scope(path.as_posix()) == scope]
        if not selected:
            continue
        # Upstream bytes are immutable. Check syntax/duplicates without imposing
        # the Catalog's formatting conventions on the vendored specification.
        config = ["-c", str(root / ".yamllint.yml")] if scope == "maintained" else [
            "-d", '{"rules": {"key-duplicates": "enable"}}',
        ]
        subprocess.run([executable, *config, "--", *selected], check=True, timeout=60)
        print(f"YAML {scope} checked: {len(selected)} files.")


def main():
    """Dispatch explicit read-only checks and propagate discovery/tool failures."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("kind", choices=("json", "yaml"))
    parser.add_argument("--yamllint", default="yamllint")
    args = parser.parse_args()
    try:
        if args.kind == "json":
            check_json(ROOT)
        else:
            check_yaml(ROOT, args.yamllint)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"Data check failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
