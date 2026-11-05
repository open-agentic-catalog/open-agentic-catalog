"""Bounded consumer ZIP verification and materialization; no package execution."""

import hashlib
import io
from pathlib import PurePosixPath
import stat
import zipfile

MAX_ARCHIVE = 64 * 1024
MAX_EXPANDED = 64 * 1024 * 1024
MAX_FILE = 16 * 1024 * 1024
MAX_FILES = 1024


class IntegrityError(ValueError):
    """An expected archive size or digest mismatch before extraction."""


def digest(raw):
    return "sha256:" + hashlib.sha256(raw).hexdigest()


def verify_archive(raw, representation):
    if len(raw) != representation["size"]:
        raise IntegrityError("archive size mismatch")
    if digest(raw) != representation["contentDigest"]:
        raise IntegrityError("archive digest mismatch")
    if not raw or len(raw) > MAX_ARCHIVE:
        raise ValueError("archive byte bound")


def safe_path(name):
    path = PurePosixPath(name)
    if (
        not name
        or path.is_absolute()
        or str(path) != name
        or len(name.encode()) > 1024
        or len(path.parts) > 17
        or any(p in ("", ".", "..") or ":" in p for p in name.split("/"))
        or "\\" in name
        or any(ord(c) < 32 for c in name)
    ):
        raise ValueError("unsafe ZIP path")
    return path


def decode(raw):
    if not raw or len(raw) > MAX_ARCHIVE:
        raise ValueError("archive byte bound")
    result, directories, seen = {}, set(), set()
    total = 0
    with zipfile.ZipFile(io.BytesIO(raw)) as archive:
        members = archive.infolist()
        if len(members) > MAX_FILES * 2:
            raise ValueError("archive entry count")
        for member in members:
            if member.filename != member.orig_filename:
                raise ValueError("noncanonical ZIP filename")
            name = member.filename[:-1] if member.is_dir() else member.filename
            path = safe_path(name)
            mode = member.external_attr >> 16
            if name.casefold() in seen or member.flag_bits & 1:
                raise ValueError("duplicate or encrypted ZIP entry")
            seen.add(name.casefold())
            if member.is_dir():
                if stat.S_IFMT(mode) not in (0, stat.S_IFDIR):
                    raise ValueError("nonregular directory")
                directories.add(name)
            else:
                if (
                    stat.S_IFMT(mode) not in (0, stat.S_IFREG)
                    or member.file_size > MAX_FILE
                    or len(result) >= MAX_FILES
                ):
                    raise ValueError("nonregular or oversized ZIP member")
                with archive.open(member) as stream:
                    body = stream.read(
                        min(MAX_FILE, MAX_EXPANDED - total, len(raw) * 100 - total) + 1
                    )
                total += len(body)
                if (
                    len(body) != member.file_size
                    or len(body) > MAX_FILE
                    or total > min(MAX_EXPANDED, len(raw) * 100)
                ):
                    raise ValueError("expanded ZIP byte bound")
                result[name] = (body, bool(mode & 0o111))
            directories.update(str(p) for p in path.parents if str(p) != ".")
    if "plugin.json" not in result or set(result) & directories:
        raise ValueError("missing portable manifest or path conflict")
    folded = [p.casefold() for p in [*result, *directories]]
    if len(folded) != len(set(folded)):
        raise ValueError("ambiguous package path")
    return result, directories


def verify_inventory(files, directories, representation):
    package = representation["plugin"]
    assert package["rootDirectory"] == "", "unexpected Plugin root"
    assert set(package.get("directories", [])) == directories, (
        "retained directory mismatch"
    )
    assert len(set(package.get("directories", []))) == len(
        package.get("directories", [])
    )
    expected = {p: (len(b), digest(b), x) for p, (b, x) in files.items()}
    actual = {
        p["path"]: (p["size"], p["digest"], p["executable"]) for p in package["files"]
    }
    assert len(actual) == len(package["files"]) and actual == expected, (
        "retained file mismatch"
    )
    assert package["expandedSize"] == sum(len(b) for b, _ in files.values()), (
        "retained size mismatch"
    )
    documents = {
        p: b.decode()
        for p, (b, _) in files.items()
        if p in ("plugin.json", "mcp.json", "skills/owned-skill/SKILL.md")
    }
    actual_docs = {d["path"]: d["content"] for d in package["documents"]}
    assert len(actual_docs) == len(package["documents"]) and actual_docs == documents, (
        "retained document mismatch"
    )
    assert package["assessments"] == dict.fromkeys(
        ("malware", "secrets", "license", "sbom", "provenance"), "not_assessed"
    ), "missing or changed assessment status"
    components = {(c["kind"], c["name"], c["path"]) for c in package["components"]}
    assert len(package["components"]) == 2 and components == {
        ("skill", "owned-skill", "skills/owned-skill/SKILL.md"),
        ("mcp_server", "owned", "mcp.json"),
    }
    assert all(not c.get("findings") for c in package["components"])


def materialize(raw, representation, destination):
    verify_archive(raw, representation)
    files, directories = decode(raw)
    verify_inventory(files, directories, representation)
    destination.mkdir()
    for path in sorted(directories):
        (destination / path).mkdir(parents=True, exist_ok=True)
    for path, (body, executable) in files.items():
        target = destination / path
        target.write_bytes(body)
        target.chmod(0o555 if executable else 0o444)
    unchanged(destination, files, directories)
    return files, directories


def unchanged(root, files, directories):
    actual, dirs = {}, set()
    for path in root.rglob("*"):
        info = path.lstat()
        name = str(path.relative_to(root))
        if stat.S_ISDIR(info.st_mode):
            dirs.add(name)
        else:
            assert stat.S_ISREG(info.st_mode) and info.st_size <= MAX_FILE, (
                "changed file type/size"
            )
            actual[name] = (path.read_bytes(), bool(info.st_mode & 0o111))
    assert actual == files and dirs == directories, "consumer changed verified files"
