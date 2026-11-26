#!/usr/bin/env python3
"""Deliver one committed reusable draft with an operator-installed catalogctl."""
import argparse
import contextlib
import fcntl
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
from urllib.parse import urlsplit

# CatalogDraftFile and unpacked Skill limits. The example accepts one optional Skill directory.
DRAFT_BYTES = 64 << 20
FILE_BYTES = 16 << 20
PACKAGE_BYTES = 64 << 20
FILES = 1024
SHA = re.compile(r"(?:[0-9a-f]{40}|[0-9a-f]{64})")
ENV = {"PATH": os.defpath, "LC_ALL": "C", "GIT_NO_REPLACE_OBJECTS": "1",
       "GIT_NO_LAZY_FETCH": "1", "GIT_TERMINAL_PROMPT": "0", "GIT_ALLOW_PROTOCOL": ""}


class Refusal(Exception):
    """A diagnostic safe to show in shared job logs."""


def require(condition, message):
    if not condition:
        raise Refusal(message)


def git(repo, *args, limit=4096):
    command = ["git", "--no-replace-objects", "--literal-pathspecs", "-C", repo, *args]
    with subprocess.Popen(command, env=ENV, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL) as process:
        data = process.stdout.read(limit + 1)
        if len(data) > limit:
            process.kill()
            raise Refusal("selected Git objects exceed the example limits")
        require(process.wait() == 0, "required Git ref, commit or file is unavailable")
        return data


def relative(name):
    parts = name.split("/")
    require(len(name.encode()) <= 1024 and all(p and p not in (".", "..") and p.lower() != ".git"
            for p in parts) and name.isprintable() and not any(c in name for c in "\\:"),
            "invalid repository-relative input path")
    return Path(name)


def sha256(data):
    return hashlib.sha256(data).hexdigest()


def encoded(value):
    return (json.dumps(value, sort_keys=True, separators=(",", ":")) + "\n").encode()


def stage(args, dest):
    draft = relative(args.file)
    paths = [str(draft)]
    package = None
    if args.package_directory:
        index, sep, name = args.package_directory.partition("=")
        require(sep and index in map(str, range(1, 17)), "use --package-directory N=relative/path")
        package = draft.parent / relative(name)
        require(package != draft and draft not in package.parents and package not in draft.parents,
                "draft and package must be separate inputs")
        paths.append(str(package))
    # Read only the selected subtrees. Check sizes and modes before requesting any blob.
    tree = git(args.repo, "ls-tree", "-r", "-l", "-z", args.commit, "--", *paths,
               limit=(FILES + 1) * 1200)
    records = []
    size_sum = 0
    for entry in tree.split(b"\0"):
        if not entry:
            continue
        header, raw_path = entry.split(b"\t", 1)
        mode, kind, oid, size_text = header.split()
        path = relative(raw_path.decode())
        require(kind == b"blob" and mode in (b"100644", b"100755"), "selected input is not a regular Git file")
        size = int(size_text)
        if path == draft:
            require(size <= DRAFT_BYTES, "draft exceeds its byte limit")
        else:
            require(package and package in path.parents and len(path.relative_to(package).parts) <= 16,
                    "invalid Skill directory or path depth")
            size_sum += size
            require(size <= FILE_BYTES and size_sum <= PACKAGE_BYTES, "Skill exceeds its byte limits")
        records.append((path, mode, oid.decode(), size))
        require(len(records) <= FILES + 1, "too many selected files")
    require(any(p == draft for p, *_ in records), "draft is absent from selected commit")
    require(not package or len(records) > 1, "selected Skill directory is empty or missing")
    manifest = {}
    for path, mode, oid, size in records:
        data = git(args.repo, "cat-file", "blob", oid, limit=size)
        require(len(data) == size and not data.startswith(b"version https://git-lfs.github.com/spec/v1"),
                "incomplete input or unresolved Git LFS pointer")
        target = dest / path
        target.parent.mkdir(parents=True, exist_ok=True)
        executable = mode == b"100755"
        with target.open("xb") as out:
            out.write(data)
            os.fchmod(out.fileno(), 0o700 if executable else 0o600)
            if args.action == "apply":
                out.flush()
                os.fsync(out.fileno())
        manifest[str(path)] = [size, executable, sha256(data)]
    if args.action == "apply":
        for folder in sorted({p for name in manifest for p in Path(name).parents}, key=lambda p: -len(p.parts)):
            sync_directory(dest / folder)
    return sha256(encoded(manifest))


def input_digest(directory, draft):
    """Recheck bytes, modes and membership, including unexpected empty directories."""
    require(directory.is_dir() and not directory.is_symlink(), "original staged inputs are missing")
    require((directory / draft).is_file(), "original draft is missing")
    manifest = {}
    directories = set()
    size_sum = 0

    def visit(folder):
        nonlocal size_sum
        for path in folder.iterdir():
            rel = str(relative(path.relative_to(directory).as_posix()))
            info = path.lstat()
            if stat.S_ISDIR(info.st_mode):
                require(len(path.relative_to(directory).parts) <= 512 and len(directories) < 18000,
                        "unexpected staged directory")
                directories.add(rel)
                visit(path)
                continue
            size_sum += info.st_size
            require(stat.S_ISREG(info.st_mode) and len(manifest) <= FILES
                    and info.st_size <= (DRAFT_BYTES if rel == draft else FILE_BYTES)
                    and size_sum <= DRAFT_BYTES + PACKAGE_BYTES, "unexpected staged file")
            with path.open("rb") as stream:
                digest = hashlib.file_digest(stream, "sha256").hexdigest()
            manifest[rel] = [info.st_size, bool(info.st_mode & 0o111), digest]
    visit(directory)
    parents = {str(p) for name in manifest for p in Path(name).parents if p != Path(".")}
    require(directories == parents, "staged directory membership changed")
    return sha256(encoded(manifest))


def read_private_input(path, limit):
    require(stat.S_ISREG(path.lstat().st_mode), "expected a regular input file")
    with path.open("rb") as stream:
        data = stream.read(limit + 1)
    require(len(data) <= limit, "retained input exceeds its byte limit")
    return data


def admit(args):
    require(args.ref.startswith("refs/heads/"), "select the approved full branch ref")
    git(args.repo, "check-ref-format", args.ref)
    origin = git(args.repo, "config", "--local", "--get-all", "remote.origin.url").decode().rstrip("\n")
    require(origin == args.source, "origin differs from the operator-selected repository")
    if args.push_event:
        event = json.loads(read_private_input(args.push_event, 25 << 20))
        require(event.get("deleted") is False and event.get("ref") == args.ref
                and event.get("after") == args.commit
                and event.get("repository", {}).get("html_url", "") + ".git" == args.source,
                "push did not admit this repository, ref and commit")
    else:
        tip = git(args.repo, "rev-parse", "--verify", args.ref).decode().strip()
        require(tip == args.commit, "fresh delivery requires the exact approved branch tip")


def sync_directory(path):
    fd = os.open(path, os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def save(path, data):
    with tempfile.NamedTemporaryFile(dir=path.parent, delete=False) as temp:
        temp.write(data)
        temp.flush()
        os.fsync(temp.fileno())
    os.replace(temp.name, path)
    sync_directory(path.parent)


def flags(args):
    result = []
    for key in ("namespace_id", "resource_id", "listing_id", "publisher_id", "package_directory"):
        value = getattr(args, key)
        if value:
            result.extend(["--" + key.replace("_", "-"), value])
    if args.content_only:
        result.append("--content-only")
    return result


def invoke(args, action, directory, extra=(), lock=()):
    cmd = [args.cli, "import", action, "--file", directory / args.file, *flags(args), *extra]
    # Do not inherit bearer tokens, login caches, destination overrides or proxy settings.
    env = {"LC_ALL": "C"}
    if os.environ.get("GOCOVERDIR"):
        env["GOCOVERDIR"] = os.environ["GOCOVERDIR"]
    return subprocess.run(cmd, env=env, capture_output=True, pass_fds=lock, check=False)


def preview(args):
    with tempfile.TemporaryDirectory() as tmp:
        directory = Path(tmp)
        expected = stage(args, directory)
        require(input_digest(directory, args.file) == expected, "staged preview differs from the commit")
        result = invoke(args, "preview", directory)
        if result.stdout:
            # catalogctl's bounded preview omits command bodies and package bytes.
            sys.stdout.buffer.write(result.stdout)
        require(result.returncode == 0, "offline preview failed; check draft, destination and package arguments")


def deliver(args):
    for url in (args.source, args.base_url):
        parsed = urlsplit(url)
        require(parsed.hostname and parsed.scheme in ("https", "http") and parsed.username is None
                and parsed.password is None and not parsed.fragment and not parsed.query,
                "source and destination must be credential-free URLs")
    root = args.state_dir.absolute()
    info = root.lstat()
    require(root == root.resolve() and stat.S_ISDIR(info.st_mode) and info.st_uid == os.getuid()
            and stat.S_IMODE(info.st_mode) == 0o700, "use an existing owner-only state directory (0700)")
    require(not root.is_relative_to(args.repo.resolve()), "keep private state outside the Git checkout")
    require(args.token_file.is_absolute() and (not args.ca_file or args.ca_file.is_absolute()),
            "supply absolute credential and trust file paths")
    require(args.token_file.is_file() and (not args.ca_file or args.ca_file.is_file()),
            "supply readable mounted credential and trust files before starting delivery")
    for path in (args.token_file, args.ca_file, args.push_event):
        require(path is None or not path.resolve().is_relative_to(args.repo.resolve()),
                "keep online credentials, trust and push evidence outside the source repository")
    with contextlib.ExitStack() as stack:
        lock = os.open(root, os.O_DIRECTORY)
        stack.callback(os.close, lock)
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        pending = root / "pending"
        active = read_private_input(pending, 65).decode().strip() if os.path.lexists(pending) else None
        require(active in (None, args.commit), "another commit has an unresolved delivery on this binding")
        record = root / args.commit
        inputs = record / "inputs"
        private = record / "delivery.json"
        receipt = record / "receipt.json"
        selection = {"source": args.source, "ref": args.ref, "commit": args.commit, "file": args.file,
                     "origin": args.base_url, "arguments": flags(args)}
        if args.action == "apply":
            require(not record.exists() and active is None, "delivery already attempted; inspect state and use resume")
            admit(args)
            # Prepare privately before admitting a mutation. A crash after pending is saved fails closed.
            with tempfile.TemporaryDirectory(dir=root) as tmp:
                prep = Path(tmp)
                staged = prep / "inputs"
                staged.mkdir()
                digest = stage(args, staged)
                require(input_digest(staged, args.file) == digest, "staged inputs differ from the commit")
                checked = invoke(args, "preview", staged)
                require(checked.returncode == 0, "local preview refused delivery")
                build = subprocess.run([args.cli, "version"], capture_output=True, env={}, check=True)
                save(prep / "delivery.json", encoded(dict(selection, inputDigest=digest, cli=json.loads(build.stdout))))
                save(pending, (args.commit + "\n").encode())
                prep.rename(record)
                sync_directory(root)
        else:
            require(record.is_dir() and not record.is_symlink() and private.is_file() and receipt.is_file(),
                    "missing original delivery or receipt; stop and reconcile the earlier attempt")
            require(active == args.commit or (record / "complete").is_file(), "missing original pending state")
        metadata = json.loads(read_private_input(private, 1 << 20))
        require(all(metadata[k] == v for k, v in selection.items()), "source or destination selection changed")
        require(input_digest(inputs, args.file) == metadata["inputDigest"], "original input bytes or modes changed")
        extra = ["--base-url", args.base_url, "--token-file", args.token_file, "--state-file", receipt]
        if args.ca_file:
            extra.extend(["--ca-file", args.ca_file])
        result = invoke(args, args.action, inputs, extra, (lock,))
        if result.stdout:
            save(record / ("result.json" if result.returncode == 0 else "last-failure.json"), result.stdout)
        save(record / "diagnostic.txt", result.stderr)
        require(result.returncode == 0, "import incomplete; inspect private result and diagnostic, then recover")
        require(json.loads(result.stdout)["disposition"] == "complete", "CLI did not acknowledge completion")
        save(record / "complete", b"complete\n")
        if pending.exists():
            pending.unlink()
            sync_directory(root)
        print("Draft delivery verified. Acknowledged revisions are in the private result.json.")


def main():
    parser = argparse.ArgumentParser(description=__doc__, allow_abbrev=False)
    parser.add_argument("action", choices=("preview", "apply", "resume"))
    parser.add_argument("--repo", type=Path, required=True)
    parser.add_argument("--commit", required=True)
    parser.add_argument("--file", required=True)
    parser.add_argument("--cli", type=Path, required=True)
    for name in ("namespace-id", "resource-id", "listing-id", "publisher-id", "package-directory"):
        parser.add_argument("--" + name)
    parser.add_argument("--content-only", action="store_true")
    parser.add_argument("--source", help="approved origin URL, exactly as configured on the trusted clone")
    parser.add_argument("--ref", help="approved full branch ref")
    parser.add_argument("--push-event", type=Path, help="trusted runner push event for fresh admission")
    parser.add_argument("--state-dir", type=Path)
    parser.add_argument("--base-url")
    parser.add_argument("--token-file", type=Path)
    parser.add_argument("--ca-file", type=Path)
    args = parser.parse_args()
    require(SHA.fullmatch(args.commit), "expected the full lower-case Git commit hash")
    # Without destination flags catalogctl also accepts native batch manifests,
    # whose Resource and Publisher destinations are selected by the file.
    require(args.namespace_id or args.resource_id, "select a destination Namespace or exact Resource")
    require(args.cli.is_absolute(), "supply the absolute path to the trusted catalogctl")
    require(not args.cli.resolve().is_relative_to(args.repo.resolve()), "load the CLI outside the resource repository")
    require(not args.push_event or args.action == "apply", "push evidence is only for fresh delivery")
    relative(args.file)
    if args.action != "resume":
        bare = git(args.repo, "rev-parse", "--is-bare-repository") == b"true\n"
        root = git(args.repo, "rev-parse", "--absolute-git-dir" if bare else "--show-toplevel")
        require(Path(os.fsdecode(root.removesuffix(b"\n"))) == args.repo.resolve(),
                "select the root of the Git repository")
        require(git(args.repo, "cat-file", "-t", args.commit) == b"commit\n", "select an exact commit")
    if args.action == "preview":
        require(not any((args.token_file, args.ca_file, args.state_dir, args.base_url, args.push_event)),
                "preview accepts no online inputs")
        preview(args)
    else:
        require(all((args.ref, args.source, args.base_url, args.token_file, args.state_dir)),
                "online delivery requires source, ref, base URL, credential file and private state")
        deliver(args)


if __name__ == "__main__":
    os.umask(0o077)
    try:
        main()
    except BlockingIOError:
        sys.exit("Another delivery holds this destination lock.")
    except Refusal as error:
        sys.exit(str(error))
    except (OSError, ValueError, KeyError, TypeError, AttributeError, subprocess.SubprocessError):
        sys.exit("Cannot read inputs, run the trusted tools or verify private state; inspect the selected files.")
