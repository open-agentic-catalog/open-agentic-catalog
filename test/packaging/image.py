"""Inspect local images, extract licenses, generate SBOMs and scan exact artifacts."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import struct
import subprocess
import tempfile
import trivy
import ui

DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")
IDENTITY = re.compile(r"[A-Za-z0-9][A-Za-z0-9._+-]{0,127}\Z")
MACHINES = {"linux/amd64": 62, "linux/arm64": 183}
SAMPLE_NOTICES = (
    "artwork/README.md", "first-party/catalog-discovery/LICENSE",
    "packages/catalog-discovery-tools/NOTICE.txt", "packages/frontend-toolkit/NOTICE.txt",
    "packages/web-testing-toolkit/NOTICE.txt", "upstream/skills/brand-guidelines/LICENSE.txt",
    "upstream/skills/frontend-design/LICENSE.txt", "upstream/skills/webapp-testing/LICENSE.txt",
)


def run(argv, seconds=60):
    """No shell, registry fallback, or unbounded subprocess waits."""
    result = subprocess.run(argv, check=True, capture_output=True, timeout=seconds)
    return result.stdout


def executable(path, platform, go):
    """Inspect ELF and Go metadata without executing a possibly foreign binary."""
    raw = path.read_bytes()
    if len(raw) < 64 or raw[:6] != b"\x7fELF\x02\x01":
        raise ValueError("expected little-endian ELF64")
    fields = struct.unpack_from("<HHIQQQIHHHHHH", raw, 16)
    if fields[1] != MACHINES[platform]:
        raise ValueError("binary architecture mismatch")
    phoff, shoff, phsize, phcount, shsize, shcount, names_index = fields[4], fields[5], fields[8], fields[9], fields[10], fields[11], fields[12]
    if phsize != 56 or shsize != 64 or not 0 < shcount < 4096 or names_index >= shcount:
        raise ValueError("invalid ELF tables")
    if phoff + phcount * phsize > len(raw) or shoff + shcount * shsize > len(raw):
        raise ValueError("truncated ELF tables")
    if any(struct.unpack_from("<I", raw, phoff + i * phsize)[0] in (2, 3) for i in range(phcount)):
        raise ValueError("binary requires dynamic loading")
    table = struct.unpack_from("<IIQQQQIIQQ", raw, shoff + names_index * shsize)
    names = raw[table[4]:table[4] + table[5]]
    for i in range(shcount):
        section = struct.unpack_from("<IIQQQQIIQQ", raw, shoff + i * shsize)
        if section[0] >= len(names):
            raise ValueError("invalid ELF section name")
        name = names[section[0]:].split(b"\0", 1)[0]
        if name == b".symtab" or name.startswith((b".debug", b".zdebug")):
            raise ValueError("binary retains debug/symbol tables")
    metadata = run(go + ["version", "-m", str(path)]).decode()
    settings = {line.split("\t", 2)[2] for line in metadata.splitlines() if line.startswith("\tbuild\t")}
    expected = {"CGO_ENABLED=0", "GOOS=linux", "GOARCH=" + platform.split("/")[1], "-trimpath=true", "-tags=netgo,osusergo"}
    expected.add("GOAMD64=v1" if platform == "linux/amd64" else "GOARM64=v8.0")
    # Trimpath intentionally omits linker flags. Runtime version commands check
    # linker-injected version/commit; cross-architecture inspection checks labels.
    if not expected.issubset(settings) or "\tdep\tgolang.org/x/crypto/x509roots/fallback\t" not in metadata:
        raise ValueError("binary build metadata mismatch")
    if run(go + ["tool", "buildid", str(path)]).strip():
        raise ValueError("binary retains linker build ID")
    return metadata


def image_settings(value, platform, version, commit, profile="backend", source_state="unknown"):
    if source_state not in ("clean", "modified", "unknown"):
        raise ValueError("invalid source state")
    if profile not in ("backend", "ui"):
        raise ValueError("invalid image inspection profile")
    if not DIGEST.fullmatch(value.get("Id", "")) or value.get("Os") + "/" + value.get("Architecture") != platform:
        raise ValueError("image identity/platform mismatch")
    config = value["Config"]
    settings = {"User": "65532:65532", "WorkingDir": "/var/lib/open-agentic-catalog",
                "Entrypoint": ["/usr/local/bin/catalogd"], "Cmd": ["api"], "StopSignal": "SIGTERM"}
    if profile == "ui":
        settings.update(WorkingDir="/", Entrypoint=["/usr/local/bin/catalog-ui"], Cmd=["--config", "/config/config.json"])
    for name, expected in settings.items():
        actual = config.get(name)
        # An omitted OCI working directory and explicit root are equivalent.
        if profile == "ui" and name == "WorkingDir" and actual in (None, ""):
            actual = "/"
        if actual != expected:
            raise ValueError("image runtime setting mismatch: " + name)
    if set(config.get("ExposedPorts", {})) != ({"8080/tcp"} if profile == "ui" else {"8080/tcp", "9090/tcp"}):
        raise ValueError("image ports mismatch")
    labels = config.get("Labels", {})
    expected = {"org.opencontainers.image.version": version, "org.opencontainers.image.revision": commit,
                "io.open-agentic-catalog.source-state": source_state,
                "org.opencontainers.image.title": "Open Agentic Catalog",
                "org.opencontainers.image.source": "https://github.com/open-agentic-catalog/open-agentic-catalog"}
    if profile == "ui":
        expected.update({"org.opencontainers.image.title": "Open Agentic Catalog UI", "org.opencontainers.image.licenses": "Apache-2.0"})
    if any(labels.get(k) != v for k, v in expected.items()):
        raise ValueError("image labels mismatch")
    return value["Id"]


def check_licenses(directory, profile="backend"):
    if directory.is_symlink() or not directory.is_dir() or directory.stat().st_mode & 0o005 != 0o005:
        raise ValueError("license root is not a readable/traversable directory for the runtime user")
    for name in ("project/LICENSE", "go/LICENSE", "frontend/inventory.json" if profile == "ui" else "upstream/checksums.txt"):
        if not (directory / name).is_file() or not (directory / name).read_bytes():
            raise ValueError("missing packaged license input: " + name)
    if not list((directory / "dependencies").rglob("LICENSE*")):
        raise ValueError("missing dependency licenses")
    if profile == "backend":
        # The existing profile manifest identifies every embedded upstream
        # notice. Inspect the image's copy without requiring its source checkout.
        upstream_notices = 0
        for line in (directory / "upstream/checksums.txt").read_text().splitlines():
            digest, name = line.split("  ", 1)
            path = Path(name)
            if not re.fullmatch(r"[a-f0-9]{64}", digest) or not path.parts or path.is_absolute() or ".." in path.parts or path.parts[0] != "upstream":
                raise ValueError("invalid upstream checksum entry")
            if re.match(r"(?i)licen[cs]e|notice|copying", path.name) or "LICENSES" in path.parts:
                upstream_notices += 1
                if hashlib.sha256((directory / path).read_bytes()).hexdigest() != digest:
                    raise ValueError("packaged upstream notice mismatch: " + name)
        if not upstream_notices:
            raise ValueError("missing upstream licenses")
        for name in SAMPLE_NOTICES:
            if not (directory / "samples" / name).is_file() or not (directory / "samples" / name).read_bytes().strip():
                raise ValueError("missing embedded sample notice: " + name)
    for path in directory.rglob("*"):
        if path.is_symlink() or (not path.is_file() and not path.is_dir()):
            raise ValueError("unexpected license file type")
        if path.is_dir() and path.stat().st_mode & 0o005 != 0o005:
            raise ValueError("license directory is not readable/traversable by the runtime user")
        if path.is_file() and not path.stat().st_mode & 0o004:
            raise ValueError("license file is not readable by the runtime user")


def supplement_sbom(document, directory, identifier, platform):
    """Add the embedded bundle that binary scanners cannot identify themselves."""
    if document.get("spdxVersion") != "SPDX-2.3" or not document.get("packages"):
        raise ValueError("missing SPDX package inventory")
    names = [p.get("name", "") for p in document["packages"]]
    required = {"usr/local/bin/catalogd", "usr/local/bin/catalogctl", "github.com/open-agentic-catalog/open-agentic-catalog", "github.com/jackc/pgx/v5"}
    if not required.issubset(names):
        raise ValueError("SBOM did not identify Catalog Go dependencies")
    bundle = directory / "upstream/checksums.txt"
    bundle_id = "SPDXRef-CatalogEmbeddedStandards"
    document["packages"].append({"SPDXID": bundle_id, "name": "catalog-embedded-standards",
                                "downloadLocation": "NOASSERTION", "filesAnalyzed": False,
                                "licenseConcluded": "NOASSERTION", "licenseDeclared": "NOASSERTION",
                                "copyrightText": "NOASSERTION",
                                "versionInfo": "manifest-sha256:" + hashlib.sha256(bundle.read_bytes()).hexdigest(),
                                "sourceInfo": "Embedded upstream bundle; exact file hashes and license texts are in /licenses/upstream/."})
    document.setdefault("relationships", []).append({"spdxElementId": document["SPDXID"], "relationshipType": "DESCRIBES", "relatedSpdxElement": bundle_id})
    document["comment"] = "Local image " + identifier + "; platform " + platform + ". Embedded bundle licenses accompany the image."


def verify_commands(engine, identifier, version, commit, source_state="unknown"):
    """Matching output never hides a failed command or engine exit."""
    common = engine + ["run", "--rm", "--pull=never", "--network=none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true"]
    if run(common + [identifier, "version"]).decode().strip() != f"open-agentic-catalog {version} ({commit}) [{source_state}]":
        raise ValueError("catalogd version mismatch")
    value = json.loads(run(common + ["--entrypoint", "/usr/local/bin/catalogctl", identifier, "version"]))
    if value.get("version") != version or value.get("commit") != commit or value.get("sourceState") != source_state:
        raise ValueError("catalogctl version mismatch")


def local_identity(options):
    engine = shlex.split(options.engine)
    values = json.loads(run(engine + ["image", "inspect", options.image]))
    if not isinstance(values, list) or len(values) != 1:
        raise ValueError("expected one local image")
    return image_settings(values[0], options.platform, options.version, options.commit, getattr(options, "profile", "backend"), getattr(options, "source_state", "unknown"))


def inspect(options, directory):
    profile = getattr(options, "profile", "backend")
    engine, go = shlex.split(options.engine), shlex.split(options.go)
    identifier = local_identity(options)
    container = run(engine + ["container", "create", "--platform", options.platform, "--pull=never", "--network=none", identifier]).decode().strip()
    if not re.fullmatch(r"[0-9a-f]{64}", container):
        raise ValueError("invalid inspection container identity")
    try:
        binaries = {}
        for name in (("catalog-ui",) if profile == "ui" else ("catalogd", "catalogctl")):
            path = directory / name
            run(engine + ["container", "cp", container + ":/usr/local/bin/" + name, str(path)])
            if path.is_symlink() or path.stat().st_mode & 0o005 != 0o005:
                raise ValueError("binary is not runtime-readable/executable")
            metadata = executable(path, options.platform, go)
            binaries[name] = {"sha256": hashlib.sha256(path.read_bytes()).hexdigest(), "goBuildInfo": metadata}
        run(engine + ["container", "cp", container + ":/licenses", str(directory / "licenses")])
        check_licenses(directory / "licenses", profile)
        if profile == "ui":
            run(engine + ["container", "cp", container + ":/app/assets", str(directory / "assets")])
            ui.inventory(directory)
            ui.documentation_inventory(directory)
        if options.action == "verify":
            verify_commands(engine, identifier, options.version, options.commit, getattr(options, "source_state", "unknown"))
        return {"imageId": identifier, "platform": options.platform, "version": options.version, "commit": options.commit, "sourceState": getattr(options, "source_state", "unknown"),
                "binaries": binaries, "licenses": "included", "execution": options.action == "verify"}
    finally:
        run(engine + ["container", "rm", container])


def make_sbom(options, directory, inspected):
    engine = shlex.split(options.engine)
    source = Path(engine[0]).name
    if len(engine) != 1 or source not in ("docker", "podman"):
        raise ValueError("SBOM requires one docker or podman local image source")
    command, env = shlex.split(options.trivy), trivy.environment()
    with tempfile.TemporaryDirectory(prefix="catalog-sbom-") as temporary:
        private = Path(temporary)
        trivy.qualify(command, cwd=private, env=env)
        (private / "config.yaml").write_text("{}\n")
        (private / "modules").mkdir()
        draft = private / "sbom.json"
        trivy.execute(command + ["image", "--config", str(private / "config.yaml"), "--image-src", source,
                                "--platform", options.platform, "--format", "spdx-json", "--offline-scan",
                                "--skip-db-update", "--skip-java-db-update", "--skip-check-update", "--skip-version-check",
                                "--module-dir", str(private / "modules"), "--output", str(draft), inspected["imageId"]], cwd=private, env=env)
        if draft.is_symlink() or not 0 < draft.stat().st_size <= 64 << 20:
            raise ValueError("missing or oversized SBOM")
        document = json.loads(draft.read_bytes())
        if not isinstance(document, dict) or document.get("name") != inspected["imageId"]:
            raise ValueError("SBOM image identity mismatch")
        trivy.spdx_go_inventory(document, inspected)
        if options.profile == "ui":
            ui.supplement(document, directory, inspected["imageId"], options.platform)
        else:
            supplement_sbom(document, directory / "licenses", inspected["imageId"], options.platform)
        return document


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("inspect", "verify", "licenses", "sbom", "scan"))
    parser.add_argument("--profile", choices=("backend", "ui"), default="backend")
    options = parser.parse_args()
    for name, default in {"engine": "docker", "go": "go", "trivy": "trivy", "image": "open-agentic-catalog:dev", "platform": "linux/amd64",
                          "version": "dev", "commit": "unknown", "source_state": "unknown", "output": "output/artifacts"}.items():
        setattr(options, name, os.environ.get("PACKAGING_" + name.upper(), default))
    try:
        if options.profile == "ui" and options.action == "verify":
            raise ValueError("UI runtime verification uses test-ui-compose; select inspect or sbom")
        if options.source_state not in ("clean", "modified", "unknown"):
            raise ValueError("invalid source state")
        if options.platform not in MACHINES or not all(IDENTITY.fullmatch(v) for v in (options.version, options.commit)):
            raise ValueError("invalid platform/version/commit")
        if not options.image or options.image.startswith("-") or any(c.isspace() for c in options.image):
            raise ValueError("invalid image reference")
        with tempfile.TemporaryDirectory(prefix="catalog-image-") as temporary:
            directory = Path(temporary)
            report = inspect(options, directory)
            if options.action == "scan":
                parser.exit(trivy.scan(options, report, directory, local_identity, make_sbom, ui.scan_inventory))
            if options.action in ("inspect", "verify"):
                print(json.dumps(report, indent=2))
                return
            output = Path(options.output)
            output.mkdir(parents=True, exist_ok=True)
            stem = options.platform.replace("/", "-") + "-" + report["imageId"][7:19]
            if options.action == "licenses":
                destination = output / ("licenses-" + stem)
                if destination.exists():
                    raise ValueError("license output already exists; select another ARTIFACT_OUTPUT")
                # Stage on the destination filesystem before the final rename.
                with tempfile.TemporaryDirectory(prefix=".licenses-", dir=output) as staged:
                    shutil.copytree(directory / "licenses", Path(staged) / "licenses")
                    os.rename(Path(staged) / "licenses", destination)
            else:
                destination = output / ("sbom-" + stem + ".spdx.json")
                if destination.exists():
                    raise ValueError("SBOM output already exists; select another ARTIFACT_OUTPUT")
                with tempfile.TemporaryDirectory(prefix=".sbom-", dir=output) as staged:
                    draft = Path(staged) / "sbom.json"
                    document = make_sbom(options, directory, report)
                    if local_identity(options) != report["imageId"]:
                        raise ValueError("selected image changed during SBOM generation")
                    draft.write_text(json.dumps(document, indent=2) + "\n")
                    draft.chmod(0o600)
                    os.link(draft, destination)  # exclusive publication; never overwrite caller output
            print(destination)
    except (ValueError, KeyError, TypeError, OSError, struct.error, subprocess.SubprocessError) as error:
        parser.exit(1, "Local image operation failed: " + str(error) + "\n")


if __name__ == "__main__":
    main()
