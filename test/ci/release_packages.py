"""Package retained CLI bytes and stage a tested VSIX. Never publishes."""

import argparse
import gzip
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import sys
import tarfile
import tempfile
from types import SimpleNamespace

import admission
import candidate

ROOT = admission.ROOT
EDITOR_PHASES = {"toolingFunctional", "localFunctional", "packagedDesktop", "linuxRemoteAndBrowser", "linuxMcpRunners", "cleanup"}


def fresh_output(directory):
    relative = directory.relative_to(ROOT)
    if not relative.parts or relative.parts[0] != "output" or ".." in relative.parts:
        raise ValueError("Package output must be an owned output directory")
    current = ROOT
    for part in relative.parts[:-1]:
        current /= part
        if current.is_symlink() or (current.exists() and not current.is_dir()):
            raise ValueError("Redirected package output")
        current.mkdir(exist_ok=True)
    directory.mkdir()


def archive_cli(extracted, output, version, platform):
    """Write a regular-file-only archive with the existing image notices."""
    if platform not in candidate.PLATFORMS or not version or any(c not in "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ._+-" for c in version):
        raise ValueError("Invalid CLI archive identity")
    files = [extracted / "catalogctl", extracted / "licenses", *sorted((extracted / "licenses").rglob("*"))]
    if not (extracted / "licenses/project/LICENSE").is_file():
        raise ValueError("Missing CLI project license")
    if any(p.is_symlink() or not (p.is_dir() or p.is_file()) for p in files):
        raise ValueError("Redirected CLI package input")
    files = [p for p in files if p.is_file()]
    if len(files) > 20000 or sum(p.stat().st_size for p in files) > 512 << 20:
        raise ValueError("CLI archive exceeds bounds")
    path = output / ("catalogctl-" + version + "-" + platform.replace("/", "-") + ".tar.gz")
    with path.open("xb") as raw, gzip.GzipFile(fileobj=raw, mode="wb", mtime=0, filename="") as compressed, tarfile.open(fileobj=compressed, mode="w|") as archive:
        for file in files:
            item = tarfile.TarInfo(file.relative_to(extracted).as_posix())
            item.size, item.mode = file.stat().st_size, 0o555 if item.name == "catalogctl" else 0o644
            with file.open("rb") as source:
                archive.addfile(item, source)
    return path


def unpack_cli(artifact, extracted, directory):
    """Check every archive payload against inspected input before native use."""
    expected = {"catalogctl": extracted / "catalogctl"}
    expected.update({p.relative_to(extracted).as_posix(): p for p in (extracted / "licenses").rglob("*") if p.is_file()})
    if (not expected["catalogctl"].is_file() or "licenses/project/LICENSE" not in expected
            or any(p.is_symlink() for p in expected.values())):
        raise ValueError("Invalid inspected CLI input")
    directory.mkdir()
    with tarfile.open(artifact, "r:gz") as archive:
        members = archive.getmembers()
        if len(members) != len(expected) or {m.name for m in members} != set(expected):
            raise ValueError("CLI archive members mismatch")
        for member in members:
            source = expected[member.name]
            mode = 0o555 if member.name == "catalogctl" else 0o644
            if not member.isreg() or member.mode != mode or member.size != source.stat().st_size:
                raise ValueError("CLI archive type, mode or size mismatch")
            # Only independently enumerated inspected paths can be written;
            # never ask tarfile to resolve an archive-controlled link or path.
            target = directory / member.name
            target.parent.mkdir(parents=True, exist_ok=True)
            digest = hashlib.sha256()
            with archive.extractfile(member) as payload, target.open("xb") as destination:
                for block in iter(lambda: payload.read(1 << 20), b""):
                    digest.update(block)
                    destination.write(block)
            if digest.hexdigest() != candidate.digest(source):
                raise ValueError("CLI archive payload mismatch: " + member.name)
            target.chmod(mode)
    return directory / "catalogctl"


def smoke_cli(executable, build):
    version = json.loads(subprocess.check_output([str(executable), "version"], timeout=30))
    if any(version.get(key) != build[key] for key in ("version", "commit", "sourceState")):
        raise ValueError("Unpacked CLI version mismatch")
    subprocess.run([str(executable), "--help"], check=True, capture_output=True, timeout=30)


def cli_package(directory, platform, *, image=None, descriptor=None):
    source = admission.source_identity(ROOT)
    if platform not in candidate.PLATFORMS or bool(image) == bool(descriptor):
        raise ValueError("Select one local image or retained backend candidate")
    if descriptor:
        value = candidate.read_candidate(descriptor, identity=source, product="backend", platform=platform)
        candidate.run(["docker", "image", "load", "--input", str(descriptor.parent / "image.tar")])
        image, build = value["imageId"], value["build"]
    else:
        build = json.loads(candidate.run([sys.executable, "-B", "test/packaging/versioning.py", "metadata"]))
    # Reuse the maintained static ELF, image-label and notice inspection.
    sys.path.insert(0, str(ROOT / "test/packaging"))
    try:
        import image as image_tools
    finally:
        sys.path.pop(0)
    options = SimpleNamespace(engine="docker", go="go", image=image, platform=platform, profile="backend", action="inspect",
                              version=build["version"], commit=build["commit"], source_state=build["sourceState"])
    fresh_output(directory)
    try:
        with tempfile.TemporaryDirectory(prefix="catalog-cli-package-") as temporary:
            extracted = Path(temporary)
            inspected = image_tools.inspect(options, extracted)
            archive = archive_cli(extracted, directory, build["version"], platform)
            executable = unpack_cli(archive, extracted, extracted / "unpacked")
            if platform == "linux/amd64":
                smoke_cli(executable, build)
        receipt = {"kind": "cli", "source": source, "build": build, "platform": platform,
                   "imageId": inspected["imageId"], "artifact": archive.name, "sha256": candidate.digest(archive),
                   "releaseCandidate": descriptor is not None, "result": "success"}
        if admission.source_identity(ROOT) != source:
            raise ValueError("CLI package source changed")
        (directory / "cli.json").write_text(json.dumps(receipt, sort_keys=True) + "\n")
        return receipt
    except BaseException:
        shutil.rmtree(directory)
        raise


def stage_editor(directory, expected_source):
    source = admission.source_identity(ROOT)
    if source != expected_source:
        raise ValueError("Editor test source changed")
    report = json.loads(admission.regular(ROOT, "output/vscode/qualification.json").read_text())
    manifest = json.loads(admission.regular(ROOT, "output/artifacts/vscode/manifest.json").read_text())
    sdk = admission.sdk_manifest(ROOT, "typescript")
    phases = report.get("phases", {})
    version = (ROOT / "VERSION").read_text().strip()
    name = "open-agentic-catalog-" + version + ".vsix"
    if (report.get("scope") != "all" or report.get("status") != "passed" or set(phases) != EDITOR_PHASES
            or any(v != "passed" for v in phases.values()) or report.get("package") != manifest
            or manifest.get("releaseBase") != version or manifest.get("artifact") != name):
        raise ValueError("Missing complete current packaged-editor tests")
    for path, expected in (manifest["sources"] | manifest["inputs"]).items():
        if admission.digest(admission.regular(ROOT, path)) != expected:
            raise ValueError("Editor source or SDK input changed")
    sdk_name, sdk_hash = next(iter(sdk["artifacts"].items()))
    if manifest["inputs"].get("output/artifacts/sdk/typescript/" + sdk_name) != sdk_hash:
        raise ValueError("Editor SDK differs from the tested SDK")
    artifact = admission.regular(ROOT, "output/artifacts/vscode/" + name)
    if candidate.digest(artifact) != manifest.get("sha256"):
        raise ValueError("Tested VSIX changed")
    fresh_output(directory)
    try:
        shutil.copyfile(artifact, directory / name)
        receipt = {"kind": "editor", "source": source, "releaseBase": version, "artifact": name,
                   "sha256": manifest["sha256"], "sdkSha256": sdk_hash, "result": "success", "cleanup": "passed"}
        if candidate.digest(directory / name) != receipt["sha256"] or admission.source_identity(ROOT) != source:
            raise ValueError("Editor handoff changed")
        (directory / "editor.json").write_text(json.dumps(receipt, sort_keys=True) + "\n")
        return receipt
    except BaseException:
        shutil.rmtree(directory)
        raise


def check_editor(directory):
    """Own a fresh test result and bind it to the complete CI source tree."""
    before = admission.source_identity(ROOT)
    (ROOT / "output/vscode/qualification.json").unlink(missing_ok=True)
    subprocess.run(["make", "test-vscode"], cwd=ROOT, check=True, timeout=5400)
    return stage_editor(directory, before)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    cli = sub.add_parser("cli")
    cli.add_argument("--platform", choices=candidate.PLATFORMS, required=True)
    selected = cli.add_mutually_exclusive_group(required=True)
    selected.add_argument("--image")
    selected.add_argument("--candidate", type=Path)
    cli.add_argument("--output", type=Path, required=True)
    editor = sub.add_parser("editor")
    editor.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.command == "editor":
        check_editor(args.output.absolute())
    else:
        cli_package(args.output.absolute(), args.platform, image=args.image, descriptor=args.candidate)


if __name__ == "__main__":
    main()
