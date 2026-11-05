"""Run the pinned, unchanged ARD CLI with mandatory schema-validation evidence."""

import argparse
import importlib.metadata
import json
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parents[2]
PROFILE = ROOT / "schemas/upstream/ard/ard-0.91-aa3e598"
CLI = PROFILE / "conformance/bin/conformance-test"
SCHEMA = PROFILE / "spec/schemas/ard-entry.schema.json"
JSONSCHEMA_VERSION = "4.26.0"
ANSI = re.compile(r"\x1b\[[0-9;]*m")


def prerequisite():
    """Require the selected dependency and valid local schema before CLI execution."""
    try:
        from jsonschema import Draft202012Validator, SchemaError
        from jsonschema.validators import validator_for
        if importlib.metadata.version("jsonschema") != JSONSCHEMA_VERSION:
            raise ValueError("unsupported jsonschema version")
    except (ImportError, importlib.metadata.PackageNotFoundError, ValueError) as error:
        raise ValueError(
            "Install shared tools with make dev-tools-install and use .tools/dev/bin/python "
            f"(jsonschema=={JSONSCHEMA_VERSION}, Draft 2020-12 required)."
        ) from error
    schema = json.loads(SCHEMA.read_text(encoding="utf-8"))
    if validator_for(schema) is not Draft202012Validator:
        raise ValueError("pinned ARD schema must use Draft 2020-12")
    try:
        Draft202012Validator.check_schema(schema)
    except SchemaError as error:
        raise ValueError("pinned ARD schema is invalid") from error
    for name in ("ArdManifest", "ArdEntry"):
        if name not in schema.get("$defs", {}):
            raise ValueError(f"pinned ARD schema is missing {name}")


def schema_evidence(mode, output):
    """Require explicit pinned-CLI evidence for modes that validate manifests."""
    if mode == "registry":
        return True  # Upstream registry mode checks protocol shapes, not schemas.
    lines = ANSI.sub("", output).splitlines()
    skip_prefixes = (
        "  ⚠ Python 'jsonschema' package not installed.",
        "  ⚠ Schema file not found at ",
        "  ⚠ Failed to read ARD entry schema:",
        "  ⚠ Failed to run JSON Schema validator:",
    )
    if any(line.startswith(skip_prefixes) for line in lines):
        return False
    return (
        sum(line == "  ✓ Manifest validates against ArdManifest." for line in lines) == 1
        and sum(bool(re.fullmatch(r"  ✓ All [0-9]+ entries validate against ArdEntry\.", line)) for line in lines) == 1
    )


def registry_result_evidence(output, expected_count):
    """Require nonempty native probe evidence for an explicitly owned fixture."""
    if type(expected_count) is not int or expected_count < 1:
        return False
    lines = ANSI.sub("", output).splitlines()
    routes = ("GET /agents", "POST /search", "POST /explore")
    counts = [match.group(1) for line in lines
              if (match := re.fullmatch(r"  ✓ POST /search returned valid results list \(([0-9]+) items\)\.", line))]
    return (all(lines.count(f"  ✓ {route} responded successfully with 200 OK.") == 1 for route in routes)
            and counts == [str(expected_count)])


def main():
    """Preserve upstream failure/output while rejecting silently skipped validation."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("check", "manifest", "publisher", "registry"))
    parser.add_argument("arguments", nargs=argparse.REMAINDER)
    args = parser.parse_args()
    try:
        prerequisite()
        if args.mode == "check":
            if args.arguments:
                raise ValueError("check takes no arguments")
            return 0
        result = subprocess.run(
            [sys.executable, str(CLI), args.mode, *args.arguments],
            capture_output=True, text=True, timeout=60, check=False,
        )
        sys.stdout.write(result.stdout)
        sys.stderr.write(result.stderr)
        if result.returncode:
            return 1
        if not schema_evidence(args.mode, result.stdout):
            raise ValueError("ARD CLI did not confirm complete manifest and entry schema validation")
        return 0
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"ARD conformance failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
