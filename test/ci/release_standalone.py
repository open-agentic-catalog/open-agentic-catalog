"""Build standalone release downloads and exercise their retained bytes on a native host."""

import argparse
import gzip
import hashlib
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import tempfile
from threading import Thread

import admission
import candidate

STANDALONE_RECEIPT = "standalone.json"
SKILL_FILES = ("LICENSE", "README.md", "SKILL.md", "references/README.md", "references/results.md")
PLATFORMS = ("linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64")


def programs(target):
    return ("catalogctl", "catalog-deploy", "catalogd") if target.startswith("linux/") else ("catalogctl", "catalog-deploy")


def filename(program, version, target):
    return f"{program}-{version}-{target.replace('/', '-')}.tar.gz"


def skill_archive(directory, version):
    root = admission.ROOT / "examples/client-skills/catalog-discovery"
    path = directory / f"catalog-discovery-skill-{version}.tar.gz"
    with path.open("xb") as raw, gzip.GzipFile(fileobj=raw, mode="wb", mtime=0, filename="") as compressed, tarfile.open(fileobj=compressed, mode="w|") as archive:
        for name in SKILL_FILES:
            source = admission.regular(root, name)
            member = tarfile.TarInfo("catalog-discovery/" + name)
            member.mode, member.size = 0o644, source.stat().st_size
            with source.open("rb") as payload:
                archive.addfile(member, payload)
    return path


def dependencies(executable):
    info = subprocess.check_output(["go", "version", "-m", executable], text=True, timeout=30)
    return {line.strip() for line in info.splitlines() if line.startswith(("\tdep\t", "\t=>\t"))}


def cross_build(program, target, build, destination):
    goos, arch = target.split("/")
    package = "./test/deployment" if program == "catalog-deploy" else "./cmd/catalogctl"
    flags = "-s -w -buildid="
    if program == "catalogctl":
        flags += " " + " ".join(f"-X main.{key}={build[key]}" for key in ("version", "commit", "sourceState"))
    subprocess.run(["go", "build", "-mod=readonly", "-trimpath", "-buildvcs=false", "-tags=netgo,osusergo",
                    "-ldflags", flags, "-o", destination, package], cwd=admission.ROOT, check=True, timeout=900,
                   env={**os.environ, "GOWORK": "off", "CGO_ENABLED": "0", "GOOS": goos, "GOARCH": arch})


def extra_archives(extracted, output, stamp, source, image_id, arch):
    from release_packages import archive_cli, unpack_cli

    version = stamp["version"]
    covered = dependencies(extracted / "catalogctl") | dependencies(extracted / "catalogd")
    assets = {}
    for system in ("linux", "darwin"):
        target = system + "/" + arch
        with tempfile.TemporaryDirectory(prefix="catalog-release-programs-") as temporary:
            inputs = Path(temporary)
            shutil.copytree(extracted / "licenses", inputs / "licenses")
            for program in programs(target):
                if system == "linux" and program == "catalogctl":
                    continue  # Already packaged directly from this image.
                binary = inputs / program
                if program == "catalogd":
                    shutil.copyfile(extracted / program, binary)
                else:
                    cross_build(program, target, stamp, binary)
                    if dependencies(binary) - covered:
                        raise ValueError("Linked dependency is absent from the qualified image notice inventory")
                verify_architecture(binary, target)
                archive = archive_cli(inputs, output, version, target, program)
                unpacked = unpack_cli(archive, inputs, inputs / ("checked-" + program), program)
                assets[archive.name] = {"sha256": candidate.digest(archive), "binarySha256": candidate.digest(unpacked)}
    if arch == "amd64":
        skill = skill_archive(output, version)
        assets[skill.name] = {"sha256": candidate.digest(skill)}
    receipt = {"source": source, "build": stamp, "imageId": image_id, "arch": arch, "files": assets}
    (output / STANDALONE_RECEIPT).write_text(json.dumps(receipt, sort_keys=True) + "\n")


def smoke_discovery(executable):
    entry = {"identifier": "urn:air:agent:fixture:reader", "type": "application/a2a+json", "displayName": "Release fixture", "url": "https://example.invalid/fixture"}
    calls = []

    class Publication(BaseHTTPRequestHandler):
        def do_GET(self):
            calls.append((self.path, self.headers.get("Authorization"), self.headers.get("Cookie")))
            self.send_response(200)
            self.send_header("Content-Type", "application/json")
            self.send_header("X-Request-ID", "release-fixture")
            self.end_headers()
            self.wfile.write(json.dumps({"entries": [entry]}).encode())

        def log_message(self, *_args):
            # Access logs would mix fixture traffic with the native checker's diagnostics.
            pass

    with HTTPServer(("127.0.0.1", 0), Publication) as peer:
        thread = Thread(target=peer.serve_forever)
        thread.start()
        try:
            result = json.loads(subprocess.check_output([executable, "discovery", "manifest", "--base-url",
                                f"http://127.0.0.1:{peer.server_port}"], timeout=30))
        finally:
            peer.shutdown()
            thread.join()
    if calls != [("/.well-known/ard.json", None, None)]:
        raise ValueError("Packaged CLI must read the manifest once without credentials")
    if result["meta"]["requestId"] != "release-fixture":
        raise ValueError("Packaged CLI did not preserve the discovery request ID")
    if result["data"]["entries"] != [entry]:
        raise ValueError("Packaged CLI discovery entry differs from the selected peer")


def smoke_credentials(executable, directory):
    import fcntl

    # A normal status read creates the private sibling lock without logging in.
    path = directory / "login.json"
    command = [executable, "auth", "status", "--file", path]
    status = json.loads(subprocess.check_output(command, timeout=15))
    if status["loggedIn"]:
        raise ValueError("Unexpected credentials in empty fixture")
    lock = path.with_suffix(".json.lock")
    if lock.stat().st_mode & 0o077:
        raise ValueError("Credential lock is accessible to another user")
    with lock.open("rb") as stream:
        fcntl.flock(stream, fcntl.LOCK_EX)
        result = subprocess.run([*command, "--timeout", "500ms"], capture_output=True, timeout=10)
        if result.returncode == 0 or b"waiting for credentials" not in result.stderr:
            raise ValueError("Packaged CLI did not report the held credential lock")
    path.write_text("disposable invalid credential")
    path.chmod(0o644)
    result = subprocess.run([executable, "auth", "logout", "--file", path], capture_output=True, timeout=15)
    if result.returncode == 0 or b"private owned regular file" not in result.stderr or path.read_text() != "disposable invalid credential":
        raise ValueError("Packaged CLI removed an unsafe credential file")


def verify_architecture(executable, target):
    with executable.open("rb") as stream:
        header = stream.read(20)
    system, arch = target.split("/")
    if system == "linux":
        valid = header[:6] == b"\x7fELF\x02\x01" and int.from_bytes(header[18:20], "little") == {"amd64": 62, "arm64": 183}[arch]
    else:
        valid = header[:4] == b"\xcf\xfa\xed\xfe" and int.from_bytes(header[4:8], "little") == {"amd64": 0x01000007, "arm64": 0x0100000C}[arch]
    if not valid:
        raise ValueError("Executable header differs from its selected architecture")


def smoke_server_version(executable, build):
    expected = f"open-agentic-catalog {build['version']} ({build['commit']}) [{build['sourceState']}]"
    if subprocess.check_output([executable, "version"], text=True, timeout=30).strip() != expected:
        raise ValueError("Wrong packaged server build")


def check_native(directory, target, output):
    machine = {"aarch64": "arm64", "arm64": "arm64", "x86_64": "amd64"}.get(platform.machine())
    if platform.system().lower() + "/" + str(machine) != target:
        raise ValueError("Selected release binaries require the matching native host")
    source = admission.source_identity(admission.ROOT)
    extra = json.loads(admission.regular(directory, STANDALONE_RECEIPT).read_text())
    cli = json.loads(admission.regular(directory, "cli.json").read_text())
    build = extra["build"]
    if (extra["source"] != source or cli["source"] != source or build != cli["build"] or extra["imageId"] != cli["imageId"]
            or build["commit"] != source["commit"] or build["sourceState"] not in {"clean", "modified"}):
        raise ValueError("Native release check selected inconsistent package inputs")
    version = build["version"]
    assets = {}
    with tempfile.TemporaryDirectory(prefix="catalog-native-release-") as temporary:
        root = Path(temporary).resolve()
        for program in programs(target):
            name = filename(program, version, target)
            archive = admission.regular(directory, name)
            digest = candidate.digest(archive)
            metadata = extra["files"].get(name)
            expected = cli["sha256"] if name == cli["artifact"] else metadata["sha256"]
            if digest != expected:
                raise ValueError("Native archive changed")
            with tarfile.open(archive, "r:gz") as bundle:
                member = bundle.getmember(program)
                if not member.isreg() or member.mode != 0o555 or not 0 < member.size < 512 << 20:
                    raise ValueError("Unexpected archived executable")
                executable = root / program
                with bundle.extractfile(member) as data, executable.open("xb") as file:
                    shutil.copyfileobj(data, file)
            verify_architecture(executable, target)
            executable.chmod(0o555)
            if metadata and candidate.digest(executable) != metadata["binarySha256"]:
                raise ValueError("Native executable changed")
            subprocess.run([executable, "--help"], capture_output=True, timeout=30, check=True)
            if program == "catalogctl":
                from release_packages import smoke_cli

                smoke_cli(executable, build)
                smoke_discovery(executable)
                smoke_credentials(executable, root)
            elif program == "catalogd":
                smoke_server_version(executable, build)
            assets[name] = digest
    output.parent.mkdir(parents=True, exist_ok=True)
    with output.open("x") as stream:
        json.dump({"source": source, "platform": target, "files": assets}, stream, sort_keys=True)


def archive_executable(path, program, notices):
    """Validate exactly one executable and the complete notice tree from its image."""
    expected = {program, *notices}
    seen = set()
    total = 0
    binary = None
    with tarfile.open(path, "r|gz") as archive:
        for member in archive:
            total += member.size
            if (member.name not in expected or member.name in seen or not member.isreg() or total > 512 << 20
                    or member.mode != (0o555 if member.name == program else 0o644)):
                raise ValueError("Invalid standalone archive member")
            seen.add(member.name)
            with archive.extractfile(member) as stream:
                checksum = hashlib.file_digest(stream, "sha256").hexdigest()
            if member.name == program:
                binary = checksum
            elif checksum != notices[member.name]:
                raise ValueError("Standalone notice differs from the qualified image")
    if seen != expected or "licenses/project/LICENSE" not in seen:
        raise ValueError("Incomplete standalone archive or notices")
    return binary


def verify_skill(path):
    expected = {"catalog-discovery/" + name: admission.regular(admission.ROOT / "examples/client-skills/catalog-discovery", name) for name in SKILL_FILES}
    with tarfile.open(path, "r|gz") as archive:
        for member in archive:
            source = expected.pop(member.name, None)
            if source is None or not member.isreg() or member.mode != 0o644 or member.size != source.stat().st_size:
                raise ValueError("Unexpected discovery Skill member")
            with archive.extractfile(member) as payload:
                if payload.read() != source.read_bytes():
                    raise ValueError("Discovery Skill differs from the source")
    if expected:
        raise ValueError("Incomplete discovery Skill")


def native_receipts(inputs, source):
    """Require exactly one matching native check per release platform."""
    native = {}
    evidence = {}
    for record in inputs.rglob("native-*.json"):
        value = json.loads(admission.regular(inputs, record.relative_to(inputs).as_posix()).read_text())
        target = value["platform"]
        if target in native or target not in PLATFORMS or value["source"] != source or set(value) != {"platform", "files", "source"}:
            raise ValueError("Invalid native release qualification")
        native[target] = value["files"]
        evidence["evidence/native-" + target.replace("/", "-") + ".json"] = (record, candidate.digest(record))
    if set(native) != set(PLATFORMS):
        raise ValueError("Missing native release qualification")
    return native, evidence


def qualified_files(inputs, source, images, cli_archives):
    """Independently derive the inventory and bind each native result to the tested bytes."""
    native, selected = native_receipts(inputs, source)

    for arch in ("amd64", "arm64"):
        directory, cli, inspection = cli_archives[arch]
        record = admission.regular(directory, STANDALONE_RECEIPT)
        value = json.loads(record.read_text())
        image = images["backend-" + arch]
        if (value["source"] != source or value["imageId"] != image["imageId"] or value["build"] != image["build"]
                or value["arch"] != arch or set(value) != {"source", "imageId", "build", "arch", "files"}):
            raise ValueError("Standalone source or image differs from the admitted build")
        inspected = json.loads(inspection.read_text())
        if inspected["imageId"] != image["imageId"] or inspected["platform"] != "linux/" + arch:
            raise ValueError("Standalone inspection differs from the admitted image")
        notices = {"licenses/" + name.split("/", 2)[2]: checksum for name, checksum in image["files"].items() if name.startswith("licenses/")}
        selected["evidence/standalone-" + arch + ".json"] = (record, candidate.digest(record))
        remaining = value["files"].copy()
        version = value["build"]["version"]
        for system in ("linux", "darwin"):
            target = system + "/" + arch
            observed = {}
            for program in programs(target):
                name = filename(program, version, target)
                path = admission.regular(directory, name)
                checksum = candidate.digest(path)
                observed[name] = checksum
                binary = archive_executable(path, program, notices)
                if system == "linux" and program in ("catalogctl", "catalogd") and inspected["binaries"][program]["sha256"] != binary:
                    raise ValueError("Linux download differs from the qualified image executable")
                if name == cli["artifact"]:
                    if checksum != cli["sha256"]:
                        raise ValueError("Linux CLI changed")
                    continue
                if remaining.pop(name, None) != {"sha256": checksum, "binarySha256": binary}:
                    raise ValueError("Standalone package differs from the build receipt")
                selected["standalone/" + name] = (path, checksum)
            if native[target] != observed:
                raise ValueError("Native result differs from the retained archives")
        if arch == "amd64":
            name = f"catalog-discovery-skill-{version}.tar.gz"
            path = admission.regular(directory, name)
            verify_skill(path)
            checksum = candidate.digest(path)
            if remaining.pop(name, None) != {"sha256": checksum}:
                raise ValueError("Discovery Skill archive changed")
            selected["standalone/" + name] = (path, checksum)
        if remaining:
            raise ValueError("Unexpected standalone release package")
    return selected


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--directory", type=Path, required=True)
    parser.add_argument("--platform", choices=PLATFORMS, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    check_native(args.directory, args.platform, args.output)


if __name__ == "__main__":
    main()
