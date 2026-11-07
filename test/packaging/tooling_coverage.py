"""Configure and publish native Python tooling coverage; never run tests."""

import argparse
import hashlib
import importlib
import importlib.metadata
import json
import os
from pathlib import Path, PurePosixPath
import re
import subprocess
import tempfile
import tomllib

ROOT = Path(__file__).resolve().parents[2]
DATA_LIMIT = 256 << 20
CONFIG_NAME = "coverage.ini"
RECEIPT_NAME = "receipt.json"
REPORT_NAME = "coverage.xml"


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def tracked_paths():
    raw = subprocess.run(
        ["git", "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
        cwd=ROOT, check=True, capture_output=True, timeout=30,
    ).stdout
    return raw.decode().split("\0")


def source_files():
    result = {}
    for name in sorted(set(filter(None, tracked_paths()))):
        path = PurePosixPath(name)
        if not name.startswith("test/") or path.suffix != ".py":
            continue
        if path.is_absolute() or ".." in path.parts or any(char in name for char in "\\\n\r"):
            raise ValueError("invalid Python source path")
        # Match the test/ Python subset of sonarqube.selected's source snapshot.
        if (any(part.startswith(".") or part in {"node_modules", "dist", "coverage", "output"}
                for part in path.parts) or name.startswith("test/clientcompat/go/generated/")):
            continue
        source = ROOT / name
        if source.is_symlink() or not source.resolve().is_relative_to(ROOT.resolve()):
            raise ValueError("Python sources must be regular repository files")
        if not source.exists():  # Match the current source snapshot after unstaged deletion.
            continue
        if not source.is_file() or source.stat().st_size > 16 << 20:
            raise ValueError("Python source exceeds the regular-file bound")
        result[name] = digest(source)
    if not result:
        raise ValueError("empty Python tooling source inventory")
    return result


def coverage_tool():
    dev = tomllib.loads((ROOT / "pyproject.toml").read_text())["dependency-groups"]["dev"]
    pins = [item.removeprefix("coverage==") for item in dev if item.startswith("coverage==")]
    if len(pins) != 1 or not re.fullmatch(r"[0-9]+\.[0-9]+\.[0-9]+", pins[0]):
        raise ValueError("shared development tools require one exact coverage pin")
    try:
        version = importlib.metadata.version("coverage")
    except importlib.metadata.PackageNotFoundError:
        raise ValueError("install the locked coverage tool with make dev-tools-install") from None
    if version != pins[0]:
        raise ValueError("coverage version differs; run make dev-tools-install")
    module = importlib.import_module("coverage")
    if module.__version__ != version:
        raise ValueError("coverage module and distribution disagree")
    return module, version


def output_path(value):
    path = ROOT / value
    base = ROOT / "output"
    if (base.is_symlink() or path.is_symlink()
            or not path.resolve().is_relative_to(base.resolve())):
        raise ValueError("select a coverage run inside repository output")
    return path.resolve()


def atomic_json(path, value):
    with tempfile.NamedTemporaryFile(mode="w", encoding="utf-8", dir=path.parent, delete=False) as target:
        temporary = Path(target.name)
        try:
            json.dump(value, target, indent=2)
            target.write("\n")
            target.close()
            temporary.replace(path)
        finally:
            temporary.unlink(missing_ok=True)


def prepare(output, owners):
    _, version = coverage_tool()
    if not owners or len(set(owners)) != len(owners) or any(
        not re.fullmatch(r"[a-z][a-z0-9-]*", owner) or not (ROOT / "test" / owner).is_dir()
        for owner in owners
    ):
        raise ValueError("select unique existing Python unit owners")
    sources = source_files()
    directory = output_path(output)
    directory.mkdir(mode=0o700, parents=True, exist_ok=False)
    (directory / "data").mkdir()
    (directory / "owners").mkdir()
    config = (
        "[run]\nbranch = True\nparallel = True\ndata_file = ${COVERAGE_FILE?}\ninclude =\n"
        + "".join("    " + str(ROOT / name) + "\n" for name in sources)
        + "omit =\n    */test_*.py\n[report]\ninclude_namespace_packages = True\nskip_empty = False\n"
    )
    (directory / CONFIG_NAME).write_text(config)
    atomic_json(directory / "manifest.json", {
        "schemaVersion": 1, "profile": "python-unit", "sourceRoots": ["test"],
        "sourceFiles": sources, "owners": owners, "coverageVersion": version,
        "configSha256": digest(directory / CONFIG_NAME),
    })
    return directory


def manifest(directory):
    value = json.loads((directory / "manifest.json").read_text())
    _, version = coverage_tool()
    if (value.get("schemaVersion") != 1 or value.get("profile") != "python-unit"
            or value.get("sourceRoots") != ["test"] or value.get("coverageVersion") != version
            or value.get("configSha256") != digest(directory / CONFIG_NAME)):
        raise ValueError("Python coverage run configuration changed")
    if (directory / RECEIPT_NAME).exists():
        raise ValueError("Python coverage run is already published")
    return value


def read_native(path, sources):
    if path.is_symlink() or not path.is_file() or not 0 < path.stat().st_size <= DATA_LIMIT:
        raise ValueError("native Python coverage data is missing or exceeds its bound")
    module, _ = coverage_tool()
    data = module.CoverageData(basename=str(path))
    data.read()
    measured = set(data.measured_files())
    admitted = {str((ROOT / name).resolve()) for name in sources}
    if not measured <= admitted or measured and not data.has_arcs():
        raise ValueError("native Python coverage data has an unexpected source or mode")
    return {"path": str(path.relative_to(ROOT)), "sha256": digest(path)}


def record_owner(output, owner):
    directory = output_path(output)
    value = manifest(directory)
    if owner not in value["owners"]:
        raise ValueError("unexpected Python unit owner")
    target = directory / "owners" / (owner + ".json")
    if target.exists():
        raise ValueError("Python unit owner was already recorded")
    files = sorted((directory / "data").glob(".coverage." + owner + ".*"))
    if not files:
        raise ValueError("Python unit owner produced no native coverage data")
    records = [read_native(path, value["sourceFiles"]) for path in files]
    atomic_json(target, {"owner": owner, "status": "passed", "data": records})


def finish(output):
    directory = output_path(output)
    value = manifest(directory)
    if source_files() != value["sourceFiles"]:
        raise ValueError("Python sources changed during coverage collection")
    expected = {owner + ".json" for owner in value["owners"]}
    if {path.name for path in (directory / "owners").iterdir()} != expected:
        raise ValueError("Python unit owner collection is incomplete")
    records = []
    for owner in value["owners"]:
        record = json.loads((directory / "owners" / (owner + ".json")).read_text())
        if record.get("owner") != owner or record.get("status") != "passed" or not record.get("data"):
            raise ValueError("Python unit owner did not complete")
        for entry in record["data"]:
            path = ROOT / entry["path"]
            if path.parent != directory / "data" or not path.name.startswith(".coverage." + owner + "."):
                raise ValueError("unexpected Python owner data path")
            if read_native(path, value["sourceFiles"]) != entry:
                raise ValueError("native Python coverage data changed after owner completion")
            records.append(path)
    if set(records) != set((directory / "data").iterdir()) or len(records) != len(set(records)):
        raise ValueError("unrecorded native Python coverage data")
    module, version = coverage_tool()
    # Explicit morfs let the standard reporter find statements in unexecuted
    # namespace modules. Coverage.py owns every executable line and hit value.
    morfs = [str(ROOT / name) for name in value["sourceFiles"] if not PurePosixPath(name).name.startswith("test_")]
    with tempfile.TemporaryDirectory(prefix=".report-", dir=directory) as temporary:
        temporary = Path(temporary)
        previous = Path.cwd()
        try:
            os.chdir(ROOT)
            combined = module.Coverage(config_file=False, data_file=str(temporary / ".coverage"), branch=True)
            combined.set_option("report:include_namespace_packages", True)
            combined.combine(data_paths=[str(path) for path in records], strict=True, keep=True)
            combined.xml_report(morfs=morfs, outfile=str(temporary / REPORT_NAME), skip_empty=False)
        finally:
            os.chdir(previous)
        if source_files() != value["sourceFiles"]:
            raise ValueError("Python sources changed during report generation")
        report = directory / REPORT_NAME
        (temporary / REPORT_NAME).replace(report)
    receipt = {
        "schemaVersion": 1, "status": "passed", "kind": "python", "profile": "python-unit",
        "sourceRoots": ["test"], "sourceFiles": value["sourceFiles"],
        "reports": [{"format": "cobertura", "path": str(report.relative_to(ROOT)), "sha256": digest(report)}],
        "tool": {"name": "coverage", "version": version},
    }
    atomic_json(directory / RECEIPT_NAME, receipt)
    return directory / RECEIPT_NAME


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest="command", required=True)
    start = commands.add_parser("prepare")
    start.add_argument("--output", required=True)
    start.add_argument("--owners", nargs="+", required=True)
    record = commands.add_parser("record-owner")
    record.add_argument("--output", required=True)
    record.add_argument("--owner", required=True)
    end = commands.add_parser("finish")
    end.add_argument("--output", required=True)
    args = parser.parse_args()
    if args.command == "prepare":
        print(prepare(args.output, args.owners))
    elif args.command == "record-owner":
        record_owner(args.output, args.owner)
    else:
        print(finish(args.output))


if __name__ == "__main__":
    main()
