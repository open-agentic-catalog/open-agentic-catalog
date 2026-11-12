"""Qualified Trivy execution and fail-closed local artifact scan evidence."""
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import tempfile
import tomllib

VERSION = json.loads(Path(__file__).with_suffix(".json").read_text())["version"]
SEVERITIES = {"UNKNOWN", "LOW", "MEDIUM", "HIGH", "CRITICAL"}


def environment():
    """Ambient policy, remote-server and suppression settings cannot narrow scans."""
    return {k: v for k, v in os.environ.items() if not k.upper().startswith("TRIVY_") or k in ("TRIVY_CACHE_DIR", "TRIVY_PODMAN_HOST")}


def execute(command, *, cwd=None, env=None, seconds=300):
    """Bound waits and captured output; never expose scanner stderr or secrets."""
    with tempfile.TemporaryFile() as stdout, tempfile.TemporaryFile() as stderr:
        result = subprocess.run(command, cwd=cwd, env=env, stdout=stdout, stderr=stderr, timeout=seconds, check=False)
        if result.returncode or max(stdout.tell(), stderr.tell()) > 8 << 20:
            raise ValueError("scanner command failed or exceeded its output limit")
        stdout.seek(0)
        return stdout.read()


def qualify(command, *, cwd=None, env=None):
    info = json.loads(execute(command + ["--version", "--format", "json"], cwd=cwd, env=env, seconds=30))
    if not isinstance(info, dict) or info.get("Version") != VERSION:
        raise ValueError("install the qualified Trivy " + VERSION)
    help_text = execute(command + ["image", "--help"], cwd=cwd, env=env, seconds=30).decode()
    if any(flag not in help_text for flag in ("--image-src", "--platform", "--offline-scan", "--skip-db-update", "--list-all-pkgs", "--image-config-scanners")):
        raise ValueError("Trivy lacks required local image capabilities")
    return info


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def database_metadata(value, now):
    """The selected advisory snapshot must be current through its next update."""
    if value.get("Version") != 2:
        raise ValueError("unsupported vulnerability database")
    times = [datetime.fromisoformat(value[k].replace("Z", "+00:00")) for k in ("UpdatedAt", "NextUpdate", "DownloadedAt")]
    if any(t.tzinfo is None for t in times) or not times[0] < times[1] or now >= times[1] or (times[0] - now).total_seconds() > 300:
        raise ValueError("vulnerability database is stale or invalid; explicitly update it before scanning")
    return value


def snapshot_database(cache, destination):
    """Freeze one database for every phase; concurrent cache updates cannot mix results."""
    source = Path(cache) / "db"
    if any(p.is_symlink() or not p.is_file() for p in (source / "metadata.json", source / "trivy.db")):
        raise ValueError("missing regular vulnerability database; explicitly download it before scanning")
    raw = (source / "metadata.json").read_bytes()
    metadata = database_metadata(json.loads(raw), datetime.now(timezone.utc))
    destination.mkdir(parents=True)
    before = (source / "trivy.db").stat()
    shutil.copyfile(source / "trivy.db", destination / "trivy.db")
    after = (source / "trivy.db").stat()
    changed = any(getattr(before, field) != getattr(after, field) for field in ("st_dev", "st_ino", "st_size", "st_mtime_ns"))
    if not (destination / "trivy.db").stat().st_size or changed or (source / "metadata.json").read_bytes() != raw:
        raise ValueError("vulnerability database changed while copying")
    (destination / "metadata.json").write_bytes(raw)
    return {"metadata": metadata, "sha256": digest(destination / "trivy.db")}


def report(path):
    if path.is_symlink() or not path.is_file() or not 0 < path.stat().st_size <= 64 << 20:
        raise ValueError("missing or oversized scanner report")
    value = json.loads(path.read_bytes())
    if not isinstance(value, dict) or value.get("SchemaVersion") != 2 or value.get("Trivy", {}).get("Version") != VERSION or not isinstance(value.get("Results"), list):
        raise ValueError("invalid scanner report")
    return value


def findings(value):
    """No suppression list: every High/Critical vulnerability or secret fails."""
    counts = {"vulnerabilities": 0, "secrets": 0}
    for item in value["Results"]:
        if not isinstance(item, dict) or not isinstance(item.get("Target"), str) or not item["Target"]:
            raise ValueError("invalid scan result")
        for field, identity, counter in (("Vulnerabilities", "VulnerabilityID", "vulnerabilities"), ("Secrets", "RuleID", "secrets")):
            entries = item.get(field, [])
            if not isinstance(entries, list):
                raise ValueError("invalid finding inventory")
            for entry in entries:
                if not isinstance(entry, dict) or not isinstance(entry.get(identity), str) or not entry[identity] or entry.get("Severity") not in SEVERITIES:
                    raise ValueError("invalid scanner finding")
                if entry["Severity"] in ("HIGH", "CRITICAL"):
                    counts[counter] += 1
    return counts


def development_dependencies(root, local_sources=None):
    """Project exact locked PyPI versions; uv's installer is pinned separately."""
    packages = tomllib.loads((root / "uv.lock").read_text())["package"]
    required = tomllib.loads((root / "pyproject.toml").read_text())["tool"]["uv"]["required-version"]
    if not isinstance(required, str) or not required.startswith("==") or not packages:
        raise ValueError("missing exact development dependency inventory")
    expected = {("uv", required[2:])}
    local = {}
    for package in packages:
        if (local_sources or {}).get(package["name"]) == package.get("source"):
            if package["name"] in local:
                raise ValueError("duplicate local dependency identity")
            local[package["name"]] = package["source"]
            continue
        if package.get("source") != {"registry": "https://pypi.org/simple"}:
            raise ValueError("development lock must contain reviewed PyPI packages")
        expected.add((package["name"], package["version"]))
    if local != (local_sources or {}) or len(expected) != len(packages) - len(local) + 1:
        raise ValueError("duplicate development dependency identity")
    for name, version in expected:
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9._-]{0,127}", name) or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.!+_-]{0,127}", version):
            raise ValueError("unsafe development dependency identity")
    return expected


def development_report(value, expected):
    """A successful scan must actually inventory the entire locked closure."""
    results = value["Results"]
    if len(results) != 1 or results[0].get("Type") != "pip" or not expected:
        raise ValueError("missing development dependency scan")
    packages = results[0].get("Packages", [])
    actual = {(package.get("Name"), package.get("Version")) for package in packages}
    if actual != expected or len(packages) != len(expected):
        raise ValueError("incomplete development dependency scan")
    if any(findings(value).values()):
        raise ValueError("High/Critical development dependency vulnerabilities")


def scan_development_dependencies(command, root, local_sources=None):
    """Use a transient requirements projection because dependency-only uv is unsupported."""
    expected = development_dependencies(root, local_sources)
    with tempfile.TemporaryDirectory(prefix="catalog-development-scan-") as directory:
        private = Path(directory)
        inputs = private / "inputs"
        inputs.mkdir()
        (inputs / "requirements.txt").write_text("".join(name + "==" + version + "\n" for name, version in sorted(expected)))
        (private / "config.yaml").write_text("{}\n")
        (private / "ignore").write_text("")
        target = private / "report.json"
        execute(command + ["fs", "--config", str(private / "config.yaml"), "--ignorefile", str(private / "ignore"),
                           "--disable-telemetry", "--skip-version-check", "--offline-scan",
                           "--scanners", "vuln", "--severity", "UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL",
                           "--format", "json", "--list-all-pkgs", "--output", str(target), str(inputs)],
                cwd=private, env=environment())
        development_report(report(target), expected)
    return len(expected)


def go_inventory(binary):
    """Interpret linked modules and stdlib from the exact extracted Go binary."""
    lines = binary["goBuildInfo"].splitlines()
    dependencies = [line.split("\t") for line in lines if line.startswith("\tdep\t")]
    modules = [line.split("\t") for line in lines if line.startswith("\tmod\t")]
    compiler = lines[0].split()[-1] if lines and lines[0].split() else ""
    if (not dependencies or any(len(parts) < 4 or not parts[2] or not parts[3] for parts in dependencies)
            or not compiler.startswith("go") or len(compiler) <= 2
            or len(modules) != 1 or len(modules[0]) < 4 or not modules[0][2]):
        raise ValueError("incomplete Go build identity")
    main = (modules[0][2], "" if modules[0][3] == "(devel)" else modules[0][3])
    return main, {tuple(parts[2:4]) for parts in dependencies} | {("stdlib", "v" + compiler[2:]), main}


def spdx_go_inventory(document, inspected):
    """Check the qualified converter's per-application Go package relationships."""
    if not isinstance(document, dict):
        raise ValueError("missing SPDX Go inventory")
    packages, relationships = document.get("packages"), document.get("relationships")
    if document.get("spdxVersion") != "SPDX-2.3" or not isinstance(packages, list) or not isinstance(relationships, list):
        raise ValueError("missing SPDX Go inventory")
    nodes, edges = {}, {}
    for package in packages:
        if not isinstance(package, dict) or not isinstance(package.get("SPDXID"), str) or not package["SPDXID"] or package["SPDXID"] in nodes:
            raise ValueError("ambiguous SPDX package identity")
        nodes[package["SPDXID"]] = package
    for relation in relationships:
        if not isinstance(relation, dict):
            raise ValueError("invalid SPDX relationship")
        kind = relation.get("relationshipType")
        if kind not in ("CONTAINS", "DEPENDS_ON"):
            continue
        source, target = relation.get("spdxElementId"), relation.get("relatedSpdxElement")
        if not isinstance(source, str) or not source or not isinstance(target, str) or not target:
            raise ValueError("invalid SPDX Go relationship")
        edges.setdefault((source, kind), set()).add(target)

    def identity(package):
        name, version = package.get("name"), package.get("versionInfo", "")
        if not isinstance(name, str) or not name or not isinstance(version, str):
            raise ValueError("invalid SPDX Go package identity")
        return name, version

    def children(identifier, kind):
        targets = edges.get((identifier, kind), set())
        if not targets.issubset(nodes):
            raise ValueError("unresolved SPDX Go relationship")
        return [nodes[target] for target in targets]

    for name, binary in inspected["binaries"].items():
        main, required = go_inventory(binary)
        path = "usr/local/bin/" + name
        applications = [p for p in packages if p.get("name") == path and p.get("primaryPackagePurpose") == "APPLICATION"]
        if len(applications) != 1:
            raise ValueError("missing or ambiguous SPDX Go application: " + name)
        roots = [p for p in children(applications[0]["SPDXID"], "CONTAINS") if identity(p) == main]
        if not roots:
            raise ValueError("missing SPDX main module for: " + name)
        actual = set()
        for root in roots:
            for package in [root, *children(root["SPDXID"], "DEPENDS_ON")]:
                if package.get("sourceInfo") != "package found in: " + path:
                    raise ValueError("SPDX Go package belongs to another application: " + name)
                actual.add(identity(package))
        if not required.issubset(actual):
            raise ValueError("SPDX Go dependency inventory mismatch: " + name)


def image_report(value, inspected):
    metadata = value.get("Metadata", {})
    config = metadata.get("ImageConfig", {})
    if value.get("ArtifactType") != "container_image" or value.get("ArtifactName") != inspected["imageId"] or metadata.get("ImageID") != inspected["imageId"] or config.get("os", "") + "/" + config.get("architecture", "") != inspected["platform"]:
        raise ValueError("scanner image identity/platform mismatch")
    for name, binary in inspected["binaries"].items():
        results = [r for r in value["Results"] if r.get("Target") == "usr/local/bin/" + name and r.get("Type") == "gobinary"]
        if len(results) != 1 or not isinstance(results[0].get("Packages"), list):
            raise ValueError("missing scanned Go binary inventory")
        packages = {(p["Name"], p.get("Version", "")) for p in results[0]["Packages"]}
        _, expected = go_inventory(binary)
        if not expected.issubset(packages):
            raise ValueError("scanned Go dependency inventory mismatch")
    return findings(value)


def frontend_report(value, inventory):
    packages = {(p["Name"], p["Version"]) for r in value["Results"] if r.get("Type") == "node-pkg" for p in r.get("Packages", [])}
    if packages != {(p["name"], p["version"]) for p in inventory["packages"]}:
        raise ValueError("missing or mismatched image-owned frontend inventory")
    return findings(value)


def scan(options, inspected, directory, check_identity, make_sbom, inventory):
    """Inspect once, scan only that image ID, then reject changed caller selection."""
    output = Path(options.output)
    output.mkdir(parents=True, exist_ok=True)
    evidence = Path(tempfile.mkdtemp(prefix="scan-", dir=output)).resolve()
    metadata = {"schemaVersion": 1, "outcome": "error", "profile": options.profile, "image": inspected,
                "scanners": ["vuln", "secret"], "failureSeverities": ["HIGH", "CRITICAL"], "disposition": "no local suppressions"}
    status = 2
    old_umask = os.umask(0o077)
    try:
        command = shlex.split(options.trivy)
        engine = shlex.split(options.engine)
        source = Path(engine[0]).name
        if len(engine) != 1 or source not in ("docker", "podman"):
            raise ValueError("scanning requires one docker or podman executable; select its connection through environment")
        metadata["imageSource"] = source
        env = environment()
        metadata["phase"] = "tool-qualification"
        qualify(command, cwd=evidence, env=env)
        metadata["toolVersion"] = VERSION
        metadata["phase"] = "database-snapshot"
        cache = env.get("TRIVY_CACHE_DIR", str(Path.home() / ".cache/trivy"))
        with tempfile.TemporaryDirectory(prefix="catalog-trivy-") as temporary:
            private = Path(temporary)
            metadata["database"] = snapshot_database(cache, private / "cache/db")
            env["TRIVY_CACHE_DIR"] = str(private / "cache")
            (evidence / "config.yaml").write_text("{}\n")
            (evidence / "ignore").write_text("")
            (evidence / "secret.yaml").write_text("{}\n")
            (private / "modules").mkdir()
            common = ["--config", str(evidence / "config.yaml"), "--ignorefile", str(evidence / "ignore"),
                      "--offline-scan", "--skip-db-update", "--skip-java-db-update", "--disable-telemetry", "--skip-version-check",
                      "--list-all-pkgs", "--format", "json", "--exit-code", "0",
                      "--severity", "UNKNOWN,LOW,MEDIUM,HIGH,CRITICAL", "--timeout", "5m"]
            metadata["configuration"] = {"ambientTrivyPolicy": "removed", "databaseUpdate": False, "offlineDependencyIdentification": True,
                                         "config": {}, "ignore": [], "secretConfig": {}, "imageConfigScanners": ["secret"], "wasmModules": [], "timeoutSeconds": 300}
            metadata["phase"] = "image-scan"
            target = evidence / "image.json"
            execute(command + ["image", *common, "--skip-check-update", "--module-dir", str(private / "modules"), "--secret-config", str(evidence / "secret.yaml"), "--image-src", source,
                               "--platform", options.platform, "--scanners", "vuln,secret", "--image-config-scanners", "secret", "--output", str(target), inspected["imageId"]], cwd=evidence, env=env)
            counts = image_report(report(target), inspected)
            metadata["findings"] = {"image": counts}
            if options.profile == "ui":
                metadata["phase"] = "frontend-scan"
                sbom = make_sbom(options, directory, inspected)
                sbom_path = evidence / "ui.spdx.json"
                sbom_path.write_text(json.dumps(sbom) + "\n")
                target = evidence / "frontend.json"
                execute(command + ["sbom", *common, "--scanners", "vuln", "--output", str(target), str(sbom_path)], cwd=evidence, env=env)
                metadata["findings"]["frontend"] = frontend_report(report(target), inventory(directory))
                metadata["frontendSBOMSha256"] = digest(sbom_path)
            metadata["phase"] = "identity-recheck"
            if check_identity(options) != inspected["imageId"]:
                raise ValueError("selected image changed during scan")
            database_metadata(metadata["database"]["metadata"], datetime.now(timezone.utc))
            status = int(any(n for group in metadata["findings"].values() for n in group.values()))
            metadata["outcome"] = "findings" if status else "passed"
    except (ValueError, KeyError, TypeError, AttributeError, OSError, subprocess.SubprocessError):
        # Private native reports contain evidence; terminal diagnostics stay fixed.
        metadata["outcome"] = "error"
    finally:
        try:
            (evidence / "run.json").write_text(json.dumps(metadata, indent=2) + "\n")
        finally:
            os.umask(old_umask)
    print(f"Local image scan: {metadata['outcome']}. Evidence: {evidence}")
    return status


if __name__ == "__main__":
    try:
        selected = shlex.split(os.environ.get("PACKAGING_TRIVY", "trivy"))
        qualify(selected, env=environment())
    except (ValueError, KeyError, TypeError, OSError, subprocess.SubprocessError):
        raise SystemExit("Trivy qualification failed; install the documented pinned version and select TRIVY.") from None
    print("Qualified Trivy " + VERSION)
    try:
        root = Path(__file__).resolve().parents[2]
        count = scan_development_dependencies(selected, root, {"catalog-dev-tools": {"virtual": "."}})
        sdk_count = scan_development_dependencies(selected, root / "test/sdk/python", {
            "catalog-python-sdk-tools": {"virtual": "."},
            "open-agentic-catalog": {"directory": "../../../sdk/python"},
        })
    except (ValueError, KeyError, TypeError, AttributeError, OSError, subprocess.SubprocessError):
        raise SystemExit("Locked development dependency scan failed; inspect the lock and scanner prerequisites.") from None
    print(f"Locked development dependencies scanned: shared {count}, Python SDK {sdk_count}; no High/Critical findings.")
