#!/usr/bin/env python3
"""Remove an explicit inventory of disposable checkout artifacts, never deployments."""

import argparse
from contextlib import contextmanager
import fcntl
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys


ROOT = Path(__file__).resolve().parents[2]
MOUNTINFO = Path("/proc/self/mountinfo")
# Normal cleanup keeps reports, models and runtime inputs under output/ intact.
# Add new build locations deliberately; ignore rules are not a deletion policy.
FILE_TARGETS = (
    "bin/catalogd", "bin/catalogctl", "bin/catalog-ui", "bin/examples/publication-report", "bin/examples/mcp-scan-receiver",
    "httpserver.test",
    "coverage/default.out", "coverage/core.out", "coverage/postgres.out",
    "coverage/unit.out", "coverage/index.html",
    "coverage/process-postgres.out", "coverage/full.out", "coverage/full.txt",
    "coverage/publication-report-unit.out", "coverage/mcp-scanner-receiver-unit.out",
    "extensions/vscode/THIRD_PARTY_NOTICES.txt",
)
TARGETS = FILE_TARGETS + (
    "node_modules", "catalog-ui/node_modules", "documentation/node_modules",
    "test/validation/node_modules", "sdk/typescript/node_modules", "test/ci/markdown/node_modules",
    "test/interoperability/mcp/node_modules", "examples/typescript-ard-list/node_modules",
    "test/interoperability/pi-ard/runtime/node_modules",
    "catalog-ui/dist", "documentation/dist",
    "documentation/.astro", "documentation/src/content/docs/generated",
    "documentation/public/generated", "test/validation/.tools",
    "test/clientcompat/.tools", "test/clientcompat/go/generated", "test/sdk/.tools", "test/formal/.tools", "test/apigenerated/.tools", "test/performance/.tools", "test/mcp-scanner/.tools",
    "output/artifacts", "coverage/.staging", ".ruff_cache", ".mypy_cache", ".hypothesis", ".tools/dev", ".tools/ci",
    "examples/langgraph/.venv", ".tools/gitleaks", ".tools/agntcy", ".tools/gateway", ".tools/ans",
    ".tools/minikube-cert-manager", ".tools/minikube-vault", ".tools/documentation-pdf",
    "output/documentation-pdf",
    "extensions/vscode/node_modules", "extensions/vscode/dist", "test/vscode/.tools",
    "sdk/rust/target", "test/sdk/rust/consumer/target",
    "examples/rust-catalog-report/target",
    # Known investigation-local caches; keep sibling reports and runtime inputs.
    "output/n88/go-build-cache", "output/n88/trivy-cache",
    "output/deadcode-research/n16-rebuild/go-cache",
)
VSCODE_TARGETS = ("extensions/vscode/THIRD_PARTY_NOTICES.txt", "extensions/vscode/node_modules", "extensions/vscode/dist", "extensions/vscode/.tools", "test/vscode/.tools", "output/artifacts/vscode", "output/vscode/staging")
TARGETS += ("extensions/vscode/.tools", "output/vscode/staging")
EMPTY_PARENTS = (".tools", "bin/examples", "bin", "documentation/public")

OUTPUT_TARGETS = tuple(dict.fromkeys((
    "output/review", "output/sdk", "output/mutation", "output/ui-mutation", "output/property",
    "output/playwright", "output/coverage", "output/verification", "output/ui-dependencies",
    *(name for name in TARGETS if Path(name).parts[0] == "output"),
)))
OUTPUT_PRESERVED = {
    "demo": "demo receipts, archives and runtime inputs",
    "ui-compose": "UI runtime inputs", "monitoring": "monitoring inputs and credentials",
    "sonarqube": "SonarQube state and credentials", "backup": "backup data", "backups": "backup data",
    "native-import": "native import inputs and receipts", "interoperability": "tool installations and state",
    "langgraph-model": "model inputs and state", "search-relevance": "model inputs and state",
    "performance": "performance state and retained measurements",
}


def display_path(path):
    """Escape control and non-ASCII characters without reading file contents."""
    value = str(path)
    return value if all(32 <= ord(char) < 127 for char in value) else json.dumps(value, ensure_ascii=True)


@contextmanager
def checkout_guard(root):
    """Serialize extension writers, including cleanup and separate Make processes."""
    descriptor = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        try:
            fcntl.flock(descriptor, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            raise ValueError("Another VS Code build, watch, install or test is active; stop it before retrying") from None
        yield
    finally:
        os.close(descriptor)


def git(root, *arguments):
    """Inspect this checkout's real index, independent of caller Git overrides."""
    environment = {key: value for key, value in os.environ.items() if not key.startswith("GIT_")}
    return subprocess.run(
        ["git", "-C", str(root), *arguments], env=environment,
        check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30,
    ).stdout


def mounted_paths():
    """Read the kernel mount inventory, including same-filesystem file binds."""
    if sys.platform != "linux":
        raise ValueError("Cleanup requires Linux /proc/self/mountinfo mount inspection")
    result = set()
    # A missing/unreadable/malformed inventory must never downgrade to is_mount:
    # that heuristic misses same-filesystem bind mounts, including mounted files.
    for line in MOUNTINFO.read_bytes().splitlines():
        before, separator, after = line.partition(b" - ")
        fields = before.split()
        if not separator or len(fields) < 6 or len(after.split()) < 3:
            raise ValueError("Cannot safely inspect the kernel mount inventory")
        raw = re.sub(rb"\\([0-7]{3})", lambda match: bytes([int(match[1], 8)]), fields[4])
        path = Path(os.fsdecode(raw))
        if not path.is_absolute():
            raise ValueError("Cannot safely inspect the kernel mount inventory")
        result.add(path)
    if not result:
        raise ValueError("Cannot safely inspect the kernel mount inventory")
    return result


def inspect_tree(target, mounts):
    """Inspect every child without following links; any traversal error aborts."""
    pending = [target]
    while pending:
        directory = pending.pop()
        with os.scandir(directory) as entries:
            for entry in entries:
                if entry.name == ".git":
                    raise ValueError(f"Refusing nested repository: {target}")
                metadata = entry.stat(follow_symlinks=False)
                if stat.S_ISLNK(metadata.st_mode):
                    continue
                child = Path(entry.path)
                if child in mounts or child.is_mount():
                    raise ValueError(f"Refusing nested mount: {target}")
                if stat.S_ISDIR(metadata.st_mode):
                    pending.append(child)


def make_directories_writable(target):
    """Permit removal of validated read-only caches without chmod through links."""
    flags = os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW

    def visit(descriptor):
        metadata = os.fstat(descriptor)
        if not metadata.st_mode & stat.S_IWUSR:
            os.fchmod(descriptor, stat.S_IMODE(metadata.st_mode) | stat.S_IWUSR)
        with os.scandir(descriptor) as entries:
            for entry in entries:
                if entry.is_dir(follow_symlinks=False):
                    child = os.open(entry.name, flags, dir_fd=descriptor)
                    try:
                        visit(child)
                    finally:
                        os.close(child)

    descriptor = os.open(target, flags)
    try:
        visit(descriptor)
    finally:
        os.close(descriptor)


def inspect_target(root, relative, mounts, *, tracked=(), file_target=False):
    """Check literal ancestors, source ownership and the selected target type."""
    target = root / relative
    metadata = None
    for part in (*reversed(relative.parents), relative):
        entry = root / part
        try:
            metadata = entry.lstat()
        except FileNotFoundError:
            return None
        if stat.S_ISLNK(metadata.st_mode) or (entry != root and (entry in mounts or entry.is_mount())):
            raise ValueError(f"Refusing symlink or mount: {display_path(part)}")
    if any(source == relative or relative in source.parents for source in tracked):
        raise ValueError(f"Refusing tracked content: {display_path(relative)}")
    expected = stat.S_ISREG(metadata.st_mode) if file_target else stat.S_ISDIR(metadata.st_mode)
    if not expected:
        raise ValueError(f"Refusing unexpected file type: {display_path(relative)}")
    return target


def output_tree(root, relative, mounts):
    """Inspect a report tree, then partition it around opaque Git repositories."""
    preserved = {}
    pending = [relative]
    while pending:
        directory = pending.pop()
        with os.scandir(root / directory) as entries:
            children = list(entries)
        if any(entry.name == ".git" for entry in children):
            preserved[directory] = "nested Git repository"
            continue
        for entry in children:
            metadata = entry.stat(follow_symlinks=False)
            if stat.S_ISLNK(metadata.st_mode):
                continue  # Unlink only the child link, never its destination.
            child = Path(entry.path)
            if child in mounts or child.is_mount():
                raise ValueError(f"Refusing nested mount: {display_path(root / relative)}")
            if stat.S_ISDIR(metadata.st_mode):
                pending.append(child.relative_to(root))
            elif not stat.S_ISREG(metadata.st_mode):
                raise ValueError(f"Refusing unexpected file type: {display_path(child.relative_to(root))}")
    if not preserved:
        return [relative], preserved
    ancestors = {parent for path in preserved for parent in path.parents}
    targets = []
    pending = [relative]
    while pending:
        directory = pending.pop()
        if directory in preserved:
            continue
        with os.scandir(root / directory) as entries:
            children = list(entries)
        if any(entry.name == ".git" for entry in children):
            raise ValueError("Cleanup inputs changed during inspection; stop writers and retry")
        for entry in children:
            child = Path(entry.path).relative_to(root)
            if child in preserved:
                continue
            if child in ancestors:
                pending.append(child)
            else:
                targets.append(child)
    return targets, preserved


def output_plan(root, tracked, mounts):
    """Select only known output, retaining unknown entries without traversing them."""
    output = Path("output")
    if inspect_target(root, output, mounts) is None:
        return [], {}
    inventory = {Path(name) for name in OUTPUT_TARGETS}
    prefixes = {parent for path in inventory for parent in path.parents if parent != Path(".")}
    targets, preserved = [], {}
    pending = [output]
    while pending:
        directory = pending.pop()
        with os.scandir(root / directory) as entries:
            children = sorted(entries, key=lambda entry: entry.name)
        if any(entry.name == ".git" for entry in children):
            preserved[directory] = "nested Git repository"
            continue
        for entry in children:
            child = directory / entry.name
            if directory == output and entry.name in OUTPUT_PRESERVED:
                preserved[child] = OUTPUT_PRESERVED[entry.name]
            elif child in inventory:
                inspect_target(root, child, mounts, tracked=tracked)
                selected, kept = output_tree(root, child, mounts)
                targets.extend(selected)
                preserved.update(kept)
            elif child in prefixes:
                inspect_target(root, child, mounts)
                pending.append(child)
            elif directory == output and entry.name.endswith(".log"):
                inspect_target(root, child, mounts, tracked=tracked, file_target=True)
                targets.append(child)
            else:
                preserved[child] = "outside output cleanup inventory"
    return sorted(set(targets)), preserved


def empty_parent_targets(root, removals, tracked, mounts):
    """Select build parents only when planned child removal leaves them empty."""
    selected = set(removals)
    result = []
    for name in EMPTY_PARENTS:
        relative = Path(name)
        if any(source == relative or relative in source.parents for source in tracked):
            continue
        target = inspect_target(root, relative, mounts)
        if target is None:
            continue
        with os.scandir(target) as entries:
            empty = all(Path(entry.path).relative_to(root) in selected for entry in entries)
        if empty:
            selected.add(relative)
            result.append(relative)
    return result


def plan(root, *, scope="all"):
    """Preflight every target before any deletion, rejecting source and redirected paths."""
    root = root.resolve()
    # Git appends one newline; any other whitespace belongs to the path.
    actual = Path(os.fsdecode(git(root, "rev-parse", "--show-toplevel").removesuffix(b"\n"))).resolve()
    if root != actual:
        raise ValueError("Cleanup must use the repository root")
    tracked = {Path(os.fsdecode(name)) for name in git(root, "ls-files", "--cached", "-z").split(b"\0") if name}
    mounts = mounted_paths()
    if scope == "output":
        return output_plan(root, tracked, mounts)
    if scope not in ("all", "vscode"):
        raise ValueError("Unknown cleanup scope")
    candidates = set(TARGETS if scope == "all" else VSCODE_TARGETS)
    # Python bytecode lives beside maintained sources. Never scan arbitrary
    # ignored directories such as retained output, models or virtual environments.
    for source in tracked:
        selected = scope == "all" or source.parts[:2] in (("extensions", "vscode"), ("test", "vscode"))
        if selected and source.suffix == ".py" and source.parts[:2] != ("schemas", "upstream"):
            candidates.add(str(source.parent / "__pycache__"))
    result = []
    for name in sorted(candidates):
        relative = Path(name)
        target = inspect_target(root, relative, mounts, tracked=tracked, file_target=name in FILE_TARGETS)
        if target is None:
            continue
        if target.is_dir():
            inspect_tree(target, mounts)
        result.append(relative)
    if scope == "all":
        result.extend(empty_parent_targets(root, result, tracked, mounts))
    return result, {}


def clean(root, *, dry_run=False, scope="all"):
    """Print the validated inventory and remove only those paths when requested."""
    root = root.resolve()
    with checkout_guard(root):
        targets, preserved = plan(root, scope=scope)
        for relative, reason in sorted(preserved.items()):
            print(f"Preserving {display_path(relative)}: {reason}", flush=True)
        for relative in targets:
            print(f"{'Would remove' if dry_run else 'Removing'} {display_path(relative)}", flush=True)
            if not dry_run:
                target = root / relative
                if scope == "all" and str(relative) in EMPTY_PARENTS:
                    target.rmdir()
                elif stat.S_ISDIR(target.lstat().st_mode):
                    make_directories_writable(target)
                    shutil.rmtree(target)
                else:
                    target.unlink()
    if not targets:
        print("No known output artifacts to remove." if scope == "output" else "No known build artifacts or dependencies to remove.")
    return targets


def main():
    """Use the script's own checkout; callers cannot supply a deletion root."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dry-run", action="store_true", help="validate and list without deleting")
    parser.add_argument("--scope", choices=("all", "vscode", "output"), default="all")
    args = parser.parse_args()
    try:
        clean(ROOT, dry_run=args.dry_run, scope=args.scope)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f"Cleanup failed: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
