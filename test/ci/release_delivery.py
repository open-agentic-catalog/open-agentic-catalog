"""Assemble exact qualified release assets. External delivery is a separate maintainer job."""

import argparse
import gzip
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import tempfile

import admission
import candidate

ROOT = admission.ROOT
SUITES = ("api", "ui-compose", "ui-oidc")


def runtime_result(suite, backend, ui, output):
    if suite not in SUITES:
        raise ValueError("Invalid runtime selection")
    values = {"backend": candidate.admit(backend, "backend")}
    if suite != "api":
        if ui is None:
            raise ValueError("Missing UI candidate")
        values["ui"] = candidate.admit(ui, "ui")
    value = {"suite": suite, "source": admission.source_identity(ROOT), "result": "success",
             "images": {key: entry["imageId"] for key, entry in values.items()}}
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(json.dumps(value, sort_keys=True) + "\n")


def chart_result(directory):
    files = sorted(directory.glob("*.tgz"))
    if (len(files) != 2 or not any(path.name.startswith("open-agentic-catalog-monitoring-") for path in files)
            or not any(path.name.startswith("open-agentic-catalog-") and "monitoring-" not in path.name for path in files)):
        raise ValueError("Missing maintained chart packages")
    value = {"source": admission.source_identity(ROOT), "result": "success",
             "files": {path.name: candidate.digest(path) for path in files}}
    (directory / "qualification.json").write_text(json.dumps(value, sort_keys=True) + "\n")


def assemble(directory, results, output):
    source = admission.source_identity(ROOT)
    value = candidate.release_results(source, results)
    images = {}
    assets = {}
    expected_hashes = {}
    for descriptor in sorted(directory.rglob("candidate.json")):
        image = candidate.read_candidate(descriptor, identity=source)
        key = image["product"] + "-" + image["platform"].split("/")[1]
        if key in images:
            raise ValueError("Duplicate release candidate")
        images[key] = image
        for name in ["candidate.json", *image["files"]]:
            relative = "images/" + key + "/" + name
            assets[relative] = descriptor.parent / name
            expected_hashes[relative] = candidate.digest(descriptor) if name == "candidate.json" else image["files"][name]
    if set(images) != {product + "-" + arch for product in candidate.PRODUCTS for arch in ("amd64", "arm64")}:
        raise ValueError("Incomplete release platform inventory")
    builds = {json.dumps(image["build"], sort_keys=True) for image in images.values()}
    if len(builds) != 1:
        raise ValueError("Release build identities differ")
    runtime = {}
    for path in directory.rglob("runtime-*.json"):
        receipt = json.loads(path.read_text())
        suite = receipt["suite"]
        required = {"backend": images["backend-amd64"]["imageId"]}
        if suite != "api":
            required["ui"] = images["ui-amd64"]["imageId"]
        if (suite not in SUITES or suite in runtime or receipt["source"] != source
                or receipt["result"] != "success" or receipt["images"] != required):
            raise ValueError("Invalid runtime qualification")
        runtime[suite] = receipt
        assets["evidence/" + path.name] = path
        expected_hashes["evidence/" + path.name] = candidate.digest(path)
    if set(runtime) != set(SUITES):
        raise ValueError("Missing runtime qualification")
    clients = {}
    charts = None
    for path in directory.rglob("qualification.json"):
        receipt = json.loads(path.read_text())
        if receipt.get("language") in admission.LANGUAGES:
            language = receipt["language"]
            manifest_path = path.with_name("manifest.json")
            if (language in clients or receipt["result"] != "success" or receipt["commit"] != source["commit"]
                    or receipt["sourceSha256"] != source["sourceSha256"] or receipt["manifestSha256"] != candidate.digest(manifest_path)):
                raise ValueError("Invalid SDK qualification")
            manifest = json.loads(manifest_path.read_text())
            expected_suffixes = {"go": [], "python": [".gz", ".whl"], "typescript": [".tgz"], "rust": [".crate"]}
            if (manifest["language"] != language or manifest["releaseBase"] != (ROOT / "VERSION").read_text().strip()
                    or manifest["openapiSha256"] != admission.digest(ROOT / "api/openapi/control-v1.yaml")
                    or sorted(Path(name).suffix for name in manifest["artifacts"]) != expected_suffixes[language]
                    or not manifest["sources"] or not manifest["tools"]):
                raise ValueError("Incomplete SDK package set")
            for name, checksum in manifest["sources"].items():
                if admission.digest(admission.regular(ROOT, name)) != checksum:
                    raise ValueError("SDK source inventory changed")
            for name, checksum in manifest["artifacts"].items():
                package = admission.regular(path.parent, name)
                if candidate.digest(package) != checksum:
                    raise ValueError("Changed qualified SDK package")
                assets["sdk/" + language + "/" + name] = package
                expected_hashes["sdk/" + language + "/" + name] = checksum
            assets["sdk/" + language + "/manifest.json"] = manifest_path
            assets["sdk/" + language + "/qualification.json"] = path
            expected_hashes["sdk/" + language + "/manifest.json"] = receipt["manifestSha256"]
            expected_hashes["sdk/" + language + "/qualification.json"] = candidate.digest(path)
            clients[language] = receipt
        elif "files" in receipt:
            if charts is not None or receipt["source"] != source or receipt["result"] != "success" or len(receipt["files"]) != 2:
                raise ValueError("Invalid chart qualification")
            for name, checksum in receipt["files"].items():
                package = admission.regular(path.parent, name)
                if not name.endswith(".tgz") or candidate.digest(package) != checksum:
                    raise ValueError("Changed qualified chart")
                assets["charts/" + name] = package
                expected_hashes["charts/" + name] = checksum
            charts = receipt
    if set(clients) != set(admission.LANGUAGES) or charts is None:
        raise ValueError("Missing client or chart qualification")
    if output.exists() or output.is_symlink():
        raise ValueError("Select a new release output")
    output.mkdir(parents=True)
    try:
        for name, source_file in assets.items():
            target = output / name
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source_file, target)
            if candidate.digest(target) != expected_hashes[name]:
                raise ValueError("Qualified asset changed during staging")
        evidence_path = output / "release-evidence.tar.gz"
        with evidence_path.open("wb") as raw, gzip.GzipFile(fileobj=raw, mode="wb", mtime=0, filename="") as compressed, tarfile.open(fileobj=compressed, mode="w|") as archive:
            for name in sorted(assets):
                if name.endswith("/image.tar"):
                    continue
                file = output / name
                item = tarfile.TarInfo(name)
                item.size = file.stat().st_size
                item.mode = 0o644
                with file.open("rb") as stream:
                    archive.addfile(item, stream)
        assets["release-evidence.tar.gz"] = evidence_path
        expected_hashes["release-evidence.tar.gz"] = candidate.digest(evidence_path)
        value.update(build=json.loads(next(iter(builds))), images={key: image["imageId"] for key, image in images.items()}, runtime=runtime,
                     files={name: expected_hashes[name] for name in sorted(assets)})
        (output / "release.json").write_text(json.dumps(value, sort_keys=True, indent=2) + "\n")
        (output / "SHA256SUMS").write_text("".join(checksum + "  " + name + "\n" for name, checksum in value["files"].items())
                                            + candidate.digest(output / "release.json") + "  release.json\n")
    except BaseException:
        shutil.rmtree(output)
        raise
    return value


def delivery_plan(directory, environment):
    """Refuse all external delivery without exact maintainer admission."""
    if environment.get("ACT") or environment.get("GITHUB_ACTIONS") != "true" or environment.get("RELEASE_PUBLISH") != "true":
        raise ValueError("External delivery requires the protected maintainer job")
    value = json.loads((directory / "release.json").read_text())
    expected = "refs/tags/v" + value["build"]["version"]
    if (value["result"] != "success" or value["build"]["sourceState"] != "clean" or environment.get("GITHUB_REF") != expected
            or environment.get("GITHUB_SHA") != value["source"]["commit"]):
        raise ValueError("Delivery tag/source mismatch")
    if value["source"] != admission.source_identity(ROOT):
        raise ValueError("Delivery checkout differs from the qualified source")
    expected_checksums = "".join(checksum + "  " + name + "\n" for name, checksum in value["files"].items())
    expected_checksums += candidate.digest(directory / "release.json") + "  release.json\n"
    if admission.regular(directory, "SHA256SUMS").read_text() != expected_checksums:
        raise ValueError("Delivery checksums changed")
    for name, checksum in value["files"].items():
        if candidate.digest(admission.regular(directory, name)) != checksum:
            raise ValueError("Delivery artifact changed")
    return value


def registry_manifest(reference):
    """Return an existing manifest or a confirmed not-found response, never an auth error."""
    result = subprocess.run(["docker", "buildx", "imagetools", "inspect", "--raw", reference],
                            capture_output=True, text=True, timeout=120, check=False)
    if result.returncode:
        if "manifest unknown" in result.stderr.lower() or (reference.lower() + ": not found") in result.stderr.lower():
            return None
        raise ValueError("Cannot inspect the delivery destination")
    return json.loads(result.stdout)


def registry_digest(reference):
    raw = subprocess.check_output(["docker", "buildx", "imagetools", "inspect", "--format", "{{json .Manifest}}", reference],
                                  timeout=120, text=True)
    value = json.loads(raw).get("digest", "")
    if not candidate.DIGEST.fullmatch(value):
        raise ValueError("Missing registry manifest digest")
    return value


def existing_member(manifest, image_id):
    if manifest is not None and manifest.get("config", {}).get("digest") != image_id:
        raise ValueError("Destination already contains a different platform image")


def existing_index(manifest, members):
    if manifest is None:
        return
    actual = {(item.get("platform", {}).get("os"), item.get("platform", {}).get("architecture")): item.get("digest")
              for item in manifest.get("manifests", [])}
    expected = {("linux", arch): digest for arch, digest in members.items()}
    if actual != expected or len(manifest.get("manifests", [])) != 2:
        raise ValueError("Destination already contains a different platform index")


def existing_assets(version, paths):
    """Permit a partial-delivery rerun only when existing release assets match."""
    repository = "open-agentic-catalog/open-agentic-catalog"
    raw = subprocess.check_output(["gh", "release", "view", "v" + version, "--repo", repository, "--json", "assets"],
                                  text=True, timeout=120)
    names = {item["name"] for item in json.loads(raw)["assets"]}
    with tempfile.TemporaryDirectory(prefix="catalog-release-assets-") as temporary:
        for path in paths:
            if path.name not in names:
                continue
            subprocess.run(["gh", "release", "download", "v" + version, "--repo", repository, "--pattern", path.name,
                            "--dir", temporary], check=True, timeout=300)
            if candidate.digest(Path(temporary) / path.name) != candidate.digest(path):
                raise ValueError("Release already contains a different asset")


def publish(directory):
    value = delivery_plan(directory, os.environ)
    # This function is selected only by the protected hosted job. Never used by
    # ordinary builds, act or local verification. Preserve partial progress.
    record = {"source": value["source"], "completed": [], "result": "incomplete"}
    destination = directory / "delivery-result.json"

    def operation(args, label):
        subprocess.run(args, check=True, timeout=900)
        record["completed"].append(label)
        destination.write_text(json.dumps(record, sort_keys=True) + "\n")

    version = value["build"]["version"]
    release_assets = [directory / "release.json", directory / "SHA256SUMS", directory / "release-evidence.tar.gz"]
    release_assets += [directory / name for name in value["files"] if name.startswith(("charts/", "sdk/")) and name.endswith((".tgz", ".crate", ".whl", ".tar.gz"))]
    if len({path.name for path in release_assets}) != len(release_assets):
        raise ValueError("Release asset basename collision")
    # Confirm the maintainer prepared the release, and refuse changed existing
    # assets before pushing any container layer.
    registry = "ghcr.io/open-agentic-catalog/"
    try:
        existing_assets(version, release_assets)
        for product in candidate.PRODUCTS:
            references = []
            members = {}
            for arch in ("amd64", "arm64"):
                key = product + "-" + arch
                image = value["images"][key]
                operation(["docker", "image", "load", "--input", str(directory / "images" / key / "image.tar")], "load-" + key)
                tag = registry + ("open-agentic-catalog" if product == "backend" else "catalog-ui") + ":" + version + "-" + arch
                existing_member(registry_manifest(tag), image)
                operation(["docker", "image", "tag", image, tag], "tag-" + key)
                operation(["docker", "image", "push", tag], "push-" + key)
                manifest = registry_manifest(tag)
                existing_member(manifest, image)
                if manifest is None:
                    raise ValueError("Pushed platform manifest is unavailable")
                digest = registry_digest(tag)
                members[arch] = digest
                references.append(tag.split(":")[0] + "@" + digest)
                record.setdefault("platformDigests", {})[key] = digest
            target = registry + ("open-agentic-catalog" if product == "backend" else "catalog-ui") + ":" + version
            existing_index(registry_manifest(target), members)
            operation(["docker", "buildx", "imagetools", "create", "--tag", target, *references], "index-" + product)
            existing_index(registry_manifest(target), members)
            record.setdefault("indexDigests", {})[product] = registry_digest(target)
            destination.write_text(json.dumps(record, sort_keys=True) + "\n")
        # No release creation or guessed notes. The maintainer prepares the
        # matching GitHub release; this job only attaches qualified assets.
        operation(["gh", "release", "upload", "v" + version, "--repo", "open-agentic-catalog/open-agentic-catalog", "--clobber", *map(str, release_assets)], "release-assets")
        record["result"] = "success"
    finally:
        destination.write_text(json.dumps(record, sort_keys=True) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    runtime = sub.add_parser("runtime-result")
    runtime.add_argument("--suite", choices=SUITES, required=True)
    runtime.add_argument("--backend", type=Path, required=True)
    runtime.add_argument("--ui", type=Path)
    runtime.add_argument("--output", type=Path, required=True)
    charts = sub.add_parser("charts")
    charts.add_argument("--directory", type=Path, required=True)
    assembly = sub.add_parser("assemble")
    assembly.add_argument("--directory", type=Path, required=True)
    assembly.add_argument("--results", required=True)
    assembly.add_argument("--output", type=Path, required=True)
    delivery = sub.add_parser("publish")
    delivery.add_argument("--directory", type=Path, required=True)
    args = parser.parse_args()
    if args.command == "runtime-result":
        runtime_result(args.suite, args.backend, args.ui, args.output)
    elif args.command == "charts":
        chart_result(args.directory)
    elif args.command == "assemble":
        assemble(args.directory, json.loads(args.results), args.output)
    else:
        publish(args.directory)


if __name__ == "__main__":
    main()
