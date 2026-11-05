"""Finite local workflow reports; never copy runner state or arbitrary output."""

import hashlib
import json
import os
from pathlib import Path
import re
import stat


FILE_LIMIT = 64 << 20
COVERAGE_LIMIT = 256 << 20
TOTAL_LIMIT = 256 << 20
QUALITY = {"lint": "source-local", "unit": "isolated-unit", "ui": "ui", "docs": "documentation"}


class CollectionError(ValueError):
    """Preserve completed copies when a later selected report fails."""

    def __init__(self, message, files, missing):
        super().__init__(message)
        self.files = list(files)
        self.missing = list(missing)


def run_identifier(value):
    if not re.fullmatch(r"[0-9]{18}", value):
        raise ValueError("invalid local workflow run identifier")
    return value


def plan(name, product, run_id):
    """Return exact required files and finite optional package patterns."""
    run_identifier(run_id)
    prefix = "output/ci/" + run_id + "-1/"
    if name in QUALITY:
        files = [prefix + "quality/" + QUALITY[name] + "/summary.md"]
        if name == "unit":
            files.append("coverage/unit.out")
        elif name == "ui":
            files.extend(("output/coverage/catalog-ui/coverage-summary.json", "output/coverage/catalog-ui/lcov.info"))
        return files, []
    if name == "security":
        return [prefix + "extended/source-security/summary.txt"], []
    if name == "postgres":
        return [prefix + "functional/coverage-postgres-check/summary.txt", "coverage/postgres.out"], []
    if name == "charts":
        base = prefix + "charts/"
        return [base + "reports/summary.txt"], [(base, r"open-agentic-catalog-[0-9][0-9A-Za-z.+-]*\.tgz", 1),
                                               (base, r"open-agentic-catalog-monitoring-[0-9][0-9A-Za-z.+-]*\.tgz", 1)]
    if name == "image" and product in {"backend", "ui"}:
        base = prefix + "images/" + product + "/amd64/"
        return [base + "reports/summary.txt"], [(base + "sbom/", r"[A-Za-z0-9_.-]+\.spdx\.json", 1)]
    raise ValueError("unsupported local workflow report selection")


def checked_path(root, relative, *, directory=False, missing=False):
    """Reject redirected ancestors and nonregular selected files before reading."""
    relative = Path(relative)
    if relative.is_absolute() or not relative.parts or any(part in {"..", "."} for part in relative.parts):
        raise ValueError("unsafe workflow report path")
    current = root
    for index, part in enumerate(relative.parts):
        current = current / part
        try:
            metadata = current.lstat()
        except FileNotFoundError:
            if missing:
                return None
            raise ValueError("missing workflow report path") from None
        expected_directory = index < len(relative.parts) - 1 or directory
        if stat.S_ISLNK(metadata.st_mode) or not (stat.S_ISDIR(metadata.st_mode) if expected_directory else stat.S_ISREG(metadata.st_mode)):
            raise ValueError("unsafe workflow report file or ancestor")
    return current


def create_destination(root, run_id):
    run_identifier(run_id)
    current = root
    for part in ("output", "artifacts", "workflows"):
        current = current / part
        try:
            current.mkdir(mode=0o700)
        except FileExistsError:
            pass
        if current.is_symlink() or not current.is_dir():
            raise ValueError("refusing redirected workflow report destination")
    destination = current / run_id
    destination.mkdir(mode=0o700)  # Never reuse an earlier invocation's reports.
    return destination


def collect(source, destination, name, product, run_id):
    files, missing = [], []
    try:
        expected, packages = plan(name, product, run_id)
        selected = list(expected)
        for parent, pattern, count in packages:
            directory = checked_path(source, parent, directory=True, missing=True)
            matches = [] if directory is None else sorted(path.name for path in directory.iterdir() if re.fullmatch(pattern, path.name))
            if len(matches) > count:
                raise ValueError("unexpected number of workflow packages")
            if len(matches) < count:
                missing.append(parent + "<packages>")
            selected.extend(parent + name for name in matches)
        total = 0
        for relative in selected:
            path = checked_path(source, relative, missing=True)
            if path is None:
                missing.append(relative)
                continue
            descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
            try:
                file_limit = COVERAGE_LIMIT if relative in {"coverage/unit.out", "coverage/postgres.out"} else FILE_LIMIT
                metadata = os.fstat(descriptor)
                if not stat.S_ISREG(metadata.st_mode) or metadata.st_size > file_limit:
                    raise ValueError("workflow report exceeds its file limit or is not regular")
                with os.fdopen(descriptor, "rb", closefd=False) as stream:
                    content = stream.read(file_limit + 1)
            finally:
                os.close(descriptor)
            total += len(content)
            if len(content) > file_limit or total > TOTAL_LIMIT:
                raise ValueError("workflow reports exceed their byte budget")
            target = destination / "reports" / relative
            target.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
            created = False
            try:
                with target.open("xb") as output:
                    created = True
                    output.write(content)
                target.chmod(0o600)
            except OSError:
                if created:
                    target.unlink(missing_ok=True)
                raise
            files.append({"path": "reports/" + relative, "bytes": len(content), "sha256": hashlib.sha256(content).hexdigest()})
        return files, missing
    except (OSError, ValueError) as error:
        message = str(error) if isinstance(error, ValueError) else "cannot copy a selected report"
        raise CollectionError(message, files, missing) from None


def write_result(destination, result):
    path = destination / "run.json"
    temporary = destination / "run.json.tmp"
    with temporary.open("x") as output:
        json.dump(result, output, indent=2, sort_keys=True)
        output.write("\n")
    temporary.chmod(0o600)
    temporary.replace(path)
