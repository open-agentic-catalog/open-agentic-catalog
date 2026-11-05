"""Bounded immutable local Git materialization; never clone, checkout or execute."""

import hashlib
import io
import os
from pathlib import PurePosixPath
import re
import zipfile

from common import run

REPOSITORY = "https://git.example.com/owned/skills.git"
NAME = "owned-skill"
DIRECTORY = "skills/" + NAME
DESCRIPTION = "Inspect invoices & explain <results> safely."
MARKDOWN = f'''---
name: {NAME}
description: {DESCRIPTION}
license: Apache-2.0
metadata:
  author: owned-fixture
---
# Instructions
Read references/guide.txt. Never execute scripts/sentinel.py.
'''
FILES = {"SKILL.md": (MARKDOWN.encode(), False), "references/guide.txt": (b"Exact immutable reference.\n", False),
         "scripts/sentinel.py": (b"from pathlib import Path\nPath('/tmp/skill-executed').write_text('unexpected')\n", True)}


def digest(raw):
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def git(repo, *args, limit=1024*1024):
    env = {"PATH": os.environ["PATH"], "HOME": str(repo.parent), "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8",
           "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null", "GIT_CONFIG_SYSTEM": "/dev/null",
           "GIT_NO_REPLACE_OBJECTS": "1", "GIT_TERMINAL_PROMPT": "0", "GIT_OPTIONAL_LOCKS": "0",
           "GIT_AUTHOR_NAME": "Owned fixture", "GIT_AUTHOR_EMAIL": "fixture@example.invalid",
           "GIT_COMMITTER_NAME": "Owned fixture", "GIT_COMMITTER_EMAIL": "fixture@example.invalid",
           "GIT_AUTHOR_DATE": "2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE": "2026-01-01T00:00:00Z"}
    return run(["git", "--no-replace-objects", "-C", str(repo), "-c", "core.hooksPath=/dev/null",
                "-c", "commit.gpgsign=false", *args], env=env, timeout=15, limit=limit, binary=True).stdout


def seed(repo):
    repo.mkdir()
    git(repo, "init", "--object-format=sha1", "--initial-branch=fixture")
    for path, (body, executable) in FILES.items():
        target = repo / DIRECTORY / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(body)
        target.chmod(0o755 if executable else 0o644)
    git(repo, "add", "--", DIRECTORY)
    git(repo, "commit", "-m", "owned immutable Skill")
    commit = git(repo, "rev-parse", "HEAD").decode().strip()
    return {"repository": REPOSITORY, "commit": commit, "directory": DIRECTORY}


def advance(repo):
    (repo / DIRECTORY / "references/guide.txt").write_text("Unpublished branch content.\n")
    git(repo, "add", "--", DIRECTORY)
    git(repo, "commit", "-m", "later unpublished Skill")
    return git(repo, "rev-parse", "HEAD").decode().strip()


def archive(files):
    out = io.BytesIO()
    with zipfile.ZipFile(out, "w", compression=zipfile.ZIP_DEFLATED) as zipped:
        for path, (body, executable) in sorted(files.items()):
            info = zipfile.ZipInfo(NAME + "/" + path, (2026, 1, 1, 0, 0, 0))
            info.create_system = 3
            info.external_attr = (0o100755 if executable else 0o100644) << 16
            zipped.writestr(info, body)
    return out.getvalue()


def safe_path(path):
    value = PurePosixPath(path)
    if not path or value.is_absolute() or any(p in (".", "..") for p in path.split("/")) or "\\" in path or "\0" in path or str(value) != path or len(path.encode()) > 1024 or len(value.parts) > 17:
        raise ValueError("unsafe Skill file path")
    return value


def entries(raw):
    if not raw or not raw.endswith(b"\0"):
        raise ValueError("incomplete Git tree")
    result = []
    for row in raw[:-1].split(b"\0"):
        head, path = row.split(b"\t", 1)
        mode, kind, oid = head.decode("ascii").split(" ")
        path = path.decode("utf-8", errors="strict")
        safe_path(path)
        if mode not in ("100644", "100755") or kind != "blob" or not re.fullmatch(r"[a-f0-9]{40}", oid):
            raise ValueError("nonregular Git tree entry")
        result.append((path, oid, mode == "100755"))
    if len(result) > 1024 or len({p for p, _, _ in result}) != len(result):
        raise ValueError("invalid Git file count")
    return result


def materialize(repo, locator, destination):
    if locator["repository"] != REPOSITORY or not re.fullmatch(r"[a-f0-9]{40}", locator["commit"]):
        raise ValueError("unmapped repository or invalid commit")
    directory = str(safe_path(locator["directory"]))
    if PurePosixPath(directory).name != NAME:
        raise ValueError("wrong Skill root")
    commit = locator["commit"]
    if git(repo, "cat-file", "-t", commit) != b"commit\n":
        raise ValueError("handoff is not a commit")
    tree = entries(git(repo, "ls-tree", "-r", "-z", commit + ":" + directory))
    destination.mkdir()
    files = {}
    total = 0
    for path, oid, executable in tree:
        size = int(git(repo, "cat-file", "-s", oid))
        if size < 0 or size > 16*1024*1024 or total + size > 64*1024*1024:
            raise ValueError("Skill byte bound exceeded")
        body = git(repo, "cat-file", "blob", oid, limit=16*1024*1024)
        if len(body) != size:
            raise ValueError("Git blob size mismatch")
        total += size
        target = destination / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(body)
        target.chmod(0o755 if executable else 0o644)
        files[NAME + "/" + path] = (body, executable)
    return files


def verify_inventory(files, representation, zipped):
    package = representation["package"]
    if representation["contentDigest"] != digest(zipped) or representation["size"] != len(zipped):
        raise ValueError("original archive identity mismatch")
    assert package["structureState"] == "passed" and package["rootDirectory"] == NAME, "retained package identity mismatch"
    assert package["fileCount"] == len(files) and package["expandedSize"] == sum(len(b) for b, _ in files.values()), "retained package totals mismatch"
    expected = {p: (len(body), digest(body), executable) for p, (body, executable) in files.items()}
    actual = {p["path"]: (p["size"], p["digest"], p["executable"]) for p in package["files"]}
    assert len(actual) == len(package["files"]) and actual == expected, "retained file inventory mismatch"
    assert all(v == "not_assessed" for v in package["assessments"].values()), "retained assessment mismatch"


def read_directory(root):
    files = {}
    total = 0
    for path in sorted(root.rglob("*")):
        if path.is_symlink():
            raise ValueError("materialized link")
        if path.is_dir():
            continue
        relative = str(safe_path(str(path.relative_to(root))))
        info = path.stat()
        if not path.is_file() or info.st_size > 16*1024*1024 or total + info.st_size > 64*1024*1024 or len(files) >= 1024:
            raise ValueError("invalid materialized file")
        body = path.read_bytes()
        total += len(body)
        files[NAME + "/" + relative] = (body, bool(info.st_mode & 0o111))
    return files
