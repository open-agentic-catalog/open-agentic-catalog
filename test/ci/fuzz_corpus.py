"""Retain bounded synthetic counterexamples from maintained Go fuzz packages."""

import argparse
import os
from pathlib import Path
import re
import stat
import subprocess

ROOT = Path(__file__).resolve().parents[2]
PACKAGES = (
    "cmd/catalogctl", "internal/draftfile", "internal/artwork", "internal/jsondoc",
    "internal/platform/authn",
    "internal/platform/cliauth", "internal/audit", "internal/catalog",
    "internal/identity", "internal/platform/httpserver",
    "internal/platform/fetcher", "internal/adapter", "internal/publication",
    "internal/standards/anstrust", "internal/standards", "internal/federation",
)
MAX_FILE_BYTES = 1 << 20
MAX_TOTAL_BYTES = 16 << 20


def checked_directory(path):
    """Refuse redirected ancestors and existing non-directory report paths."""
    for parent in (path, *path.parents):
        if parent.is_symlink() or (parent.exists() and not parent.is_dir()):
            raise ValueError("Unsafe fuzz corpus directory")


def collect(root, paths, output):
    """Copy admitted files into ignored reports without replacing existing files."""
    root = root.absolute()
    checked_directory(root)
    output = root / output
    try:
        relative_output = output.relative_to(root / "output")
    except ValueError as error:
        raise ValueError("Fuzz corpus reports must stay under output/") from error
    if not relative_output.parts or ".." in relative_output.parts:
        raise ValueError("Unsafe fuzz corpus output path")
    checked_directory(output)
    copied = []
    total = 0
    for name in paths:
        path = Path(name)
        if path.is_absolute() or ".." in path.parts:
            raise ValueError("Unsafe fuzz counterexample path")
        package = next((item for item in PACKAGES if path.is_relative_to(Path(item) / "testdata/fuzz")), None)
        if package is None:
            continue
        tail = path.relative_to(Path(package) / "testdata/fuzz")
        source = root / path
        if (len(tail.parts) != 2 or not re.fullmatch(r"[0-9a-f]{16,64}", source.name)
                or any(parent.is_symlink() for parent in source.parents)
                or not stat.S_ISREG(source.lstat().st_mode)
                or source.stat().st_size > MAX_FILE_BYTES):
            raise ValueError("Unexpected fuzz counterexample file")
        with source.open("rb") as stream:
            data = stream.read(MAX_FILE_BYTES + 1)
        total += len(data)
        if (len(data) > MAX_FILE_BYTES or not data.startswith(b"go test fuzz v1\n")
                or total > MAX_TOTAL_BYTES):
            raise ValueError("Unexpected or excessive fuzz counterexamples")
        target = output / path
        checked_directory(target.parent)
        target.parent.mkdir(parents=True, exist_ok=True)
        with target.open("xb") as stream:
            stream.write(data)
        copied.append(path.as_posix())
    return copied


def main():
    """Select only untracked, nonignored Git paths; never execute corpus contents."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    paths = subprocess.run(["git", "ls-files", "--others", "--exclude-standard", "-z"],
                           cwd=ROOT, check=True, capture_output=True, timeout=30).stdout.split(b"\0")
    copied = collect(ROOT, [os.fsdecode(name) for name in paths if name], args.output)
    print(f"Retained {len(copied)} synthetic fuzz counterexamples.")


if __name__ == "__main__":
    main()
