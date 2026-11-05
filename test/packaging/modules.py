#!/usr/bin/env python3
"""Check module tidiness against current source, excluding ignored development output."""

import argparse
import os
from pathlib import Path
import shutil
import subprocess
import tempfile


ROOT = Path(__file__).resolve().parents[2]


def stage(root, destination):
    """Copy tracked and new non-ignored files, including unstaged edits and embeds."""
    environment = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    result = subprocess.run(
        ["git", "-C", str(root), "ls-files", "--cached", "--others", "--exclude-standard", "-z"],
        env=environment, check=True, capture_output=True, timeout=30,
    )
    for name in set(result.stdout.split(b"\0")) - {b""}:
        relative = Path(os.fsdecode(name))
        source = root / relative
        if source.is_symlink() or any(parent.is_symlink() for parent in source.parents if parent != root):
            raise ValueError(f"Module check refuses symlinked source: {relative}")
        if not source.is_file():
            continue
        target = destination / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, target)


def main():
    """Run Go's read-only tidy comparison in a temporary source checkout."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--go", default="go", help="Go executable")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="catalog-module-check-") as directory:
        stage(ROOT, Path(directory))
        environment = dict(os.environ, GOWORK="off")
        return subprocess.run([args.go, "mod", "tidy", "-diff"], cwd=directory,
                              env=environment, check=False, timeout=300).returncode


if __name__ == "__main__":
    raise SystemExit(main())
