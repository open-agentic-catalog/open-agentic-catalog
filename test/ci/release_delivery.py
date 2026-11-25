"""Assemble exact qualified release assets. External delivery is a separate maintainer job."""

import argparse
import gzip
import hashlib
import json
import os
import re
from pathlib import Path
import shlex
import shutil
import subprocess
import tarfile
import tempfile

import admission
import candidate
import release_standalone

ROOT = admission.ROOT
CHART_REGISTRY = "ghcr.io/open-agentic-catalog/charts"
SOURCE_ANNOTATION = "org.opencontainers.image.source"
SOURCE_URL = "https://github.com/" + admission.REPOSITORY
SUITES = ("api", "ui-compose", "ui-oidc")


def sdk_downloads(version):
    python = version.replace("-rc.", "rc")
    return {
        "go": {f"open-agentic-catalog-go-{version}.tar.gz"},
        "python": {f"open_agentic_catalog-{python}-py3-none-any.whl", f"open_agentic_catalog-{python}.tar.gz"},
        "typescript": {f"open-agentic-catalog-client-{version}.tgz"},
        "rust": {f"open-agentic-catalog-{version}.crate"},
    }


def downloadable(name):
    parts = Path(name).parts
    return ((len(parts) == 3 and parts[0] == "sdk" and parts[1] in admission.LANGUAGES
             and name.endswith((".tgz", ".crate", ".whl", ".tar.gz")))
            or (len(parts) == 2 and parts[0] == "charts" and name.endswith(".tgz"))
            or (len(parts) == 2 and parts[0] in {"cli", "standalone"} and name.endswith(".tar.gz"))
            or (len(parts) == 2 and parts[0] == "editor" and name.endswith(".vsix")))


def public_assets(directory, value):
    """One flat download inventory; internal image archives remain internal."""
    names = ["release.json", "release-evidence.tar.gz", *(name for name in value["files"] if downloadable(name))]
    if len({Path(name).name for name in names}) != len(names):
        raise ValueError("Release asset basename collision")
    return {Path(name).name: admission.regular(directory, name) for name in names}


def public_checksums(directory, value):
    return checksums(public_assets(directory, value))


def checksums(assets):
    return "".join(candidate.digest(path) + "  " + name + "\n" for name, path in sorted(assets.items()))


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


def release_charts():
    charts = {}
    for name in ("open-agentic-catalog", "open-agentic-catalog-monitoring"):
        metadata = (ROOT / "deployments/helm" / name / "Chart.yaml").read_text()
        versions = [line.removeprefix("version: ").strip() for line in metadata.splitlines() if line.startswith("version: ")]
        if len(versions) != 1:
            raise ValueError("Missing or duplicate release chart version")
        version = versions[0]
        charts[f"{name}-{version}.tgz"] = (name, version)
    return charts


def chart_result(directory):
    files = sorted(directory.glob("*.tgz"))
    expected = release_charts()
    if {path.name for path in files} != expected.keys():
        raise ValueError("Missing maintained chart packages")
    app_version = (ROOT / "VERSION").read_text().strip()
    helm = shlex.split(os.environ.get("HELM", "helm"))
    for path in files:
        name, version = expected[path.name]
        metadata = subprocess.check_output(helm + ["show", "chart", str(path.resolve())], text=True, timeout=30).splitlines()
        if not {f"name: {name}", f"version: {version}", f"appVersion: {app_version}"}.issubset(metadata):
            raise ValueError(f"Packaged chart {path.name} must declare {name} {version} with appVersion {app_version}")
        annotations = re.search(r"^annotations:\n((?:  [^\n]*\n)+)", "\n".join(metadata) + "\n", re.MULTILINE)
        if not annotations or f"  {SOURCE_ANNOTATION}: {SOURCE_URL}" not in annotations[1].splitlines():
            raise ValueError(f"Packaged chart {path.name} must identify its source repository")
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
    sdk_hashes = {}
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
            if (manifest["language"] != language or manifest["releaseBase"] != (ROOT / "VERSION").read_text().strip()
                    or manifest["openapiSha256"] != admission.digest(ROOT / "api/openapi/control-v1.yaml")
                    or set(manifest["artifacts"]) != sdk_downloads(manifest["releaseBase"])[language]
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
            sdk_hashes[language] = set(manifest["artifacts"].values())
        elif "files" in receipt:
            if charts is not None or receipt["source"] != source or receipt["result"] != "success" or set(receipt["files"]) != release_charts().keys():
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
    editors = list(directory.rglob("editor.json"))
    if len(editors) != 1:
        raise ValueError("Missing or duplicate tested editor")
    editor = editors[0]
    receipt = json.loads(admission.regular(directory, editor.relative_to(directory).as_posix()).read_text())
    base = (ROOT / "VERSION").read_text().strip()
    if (set(receipt) != {"kind", "source", "releaseBase", "artifact", "sha256", "sdkSha256", "result", "cleanup"}
            or receipt["kind"] != "editor" or receipt["source"] != source or receipt["releaseBase"] != base
            or receipt["result"] != "success" or receipt["cleanup"] != "passed"
            or receipt["sdkSha256"] not in sdk_hashes["typescript"]
            or receipt["artifact"] != "open-agentic-catalog-" + base + ".vsix"):
        raise ValueError("Invalid tested editor handoff")
    editor_file = admission.regular(directory, (editor.parent.relative_to(directory) / receipt["artifact"]).as_posix())
    if candidate.digest(editor_file) != receipt["sha256"]:
        raise ValueError("Tested editor bytes changed")
    assets["editor/" + receipt["artifact"]] = editor_file
    expected_hashes["editor/" + receipt["artifact"]] = receipt["sha256"]
    assets["evidence/editor.json"] = editor
    expected_hashes["evidence/editor.json"] = candidate.digest(editor)
    cli_platforms = set()
    cli_archives = {}
    for path in directory.rglob("cli.json"):
        receipt = json.loads(admission.regular(directory, path.relative_to(directory).as_posix()).read_text())
        platform = receipt.get("platform")
        if platform not in candidate.PLATFORMS or platform in cli_platforms:
            raise ValueError("Missing, duplicate or unexpected CLI platform")
        arch = platform.split("/")[1]
        expected_name = "catalogctl-" + base + "-linux-" + arch + ".tar.gz"
        if (set(receipt) != {"kind", "source", "build", "platform", "imageId", "artifact", "sha256", "releaseCandidate", "result"}
                or receipt["kind"] != "cli" or receipt["source"] != source or receipt["build"] != images["backend-" + arch]["build"]
                or receipt["releaseCandidate"] is not True or receipt["result"] != "success"
                or receipt["imageId"] != images["backend-" + arch]["imageId"] or receipt["artifact"] != expected_name):
            raise ValueError("Invalid retained CLI package")
        artifact = admission.regular(directory, (path.parent.relative_to(directory) / expected_name).as_posix())
        if candidate.digest(artifact) != receipt["sha256"]:
            raise ValueError("CLI package changed")
        cli_platforms.add(platform)
        cli_archives[arch] = (path.parent, receipt, assets["images/backend-" + arch + "/inspection.json"])
        assets["cli/" + expected_name] = artifact
        expected_hashes["cli/" + expected_name] = receipt["sha256"]
        assets["evidence/cli-" + arch + ".json"] = path
        expected_hashes["evidence/cli-" + arch + ".json"] = candidate.digest(path)
    if cli_platforms != set(candidate.PLATFORMS):
        raise ValueError("Missing CLI packages")
    for name, (path, checksum) in release_standalone.qualified_files(directory, source, images, cli_archives).items():
        assets[name] = path
        expected_hashes[name] = checksum
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
                if name.endswith("/image.tar") or downloadable(name):
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
        (output / "SHA256SUMS").write_text(public_checksums(output, value))
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
    intent = admission.release_intent(environment, value["build"]["version"])
    if not intent["publish"]:
        raise ValueError("Release publication is disabled")
    if (value["result"] != "success" or value["build"]["sourceState"] != "clean" or environment.get("GITHUB_REF") != expected
            or environment.get("GITHUB_SHA") != value["source"]["commit"]):
        raise ValueError("Delivery tag/source mismatch")
    if value["source"] != admission.source_identity(ROOT):
        raise ValueError("Delivery checkout differs from the qualified source")
    for name, checksum in value["files"].items():
        if candidate.digest(admission.regular(directory, name)) != checksum:
            raise ValueError("Delivery artifact changed")
    if admission.regular(directory, "SHA256SUMS").read_text() != public_checksums(directory, value):
        raise ValueError("Delivery checksums changed")
    return value


def registry_bytes(reference):
    """Return an existing manifest or a confirmed not-found response, never an auth error."""
    result = subprocess.run(["docker", "buildx", "imagetools", "inspect", "--raw", reference],
                            capture_output=True, timeout=120, check=False)
    if result.returncode:
        error = result.stderr.decode(errors="replace").strip().removeprefix("ERROR: ")
        # Match the complete Buildx diagnostic. A denial or transport error may also mention a missing manifest.
        missing = {"manifest unknown", "manifest unknown: manifest unknown"}
        if result.returncode == 1 and (error == f"{reference}: not found" or error.removeprefix(reference + ": ") in missing):
            return None
        raise ValueError(f"Cannot inspect the delivery destination {reference}")
    return result.stdout


def parse_registry_manifest(raw, reference):
    manifest = json.loads(raw)
    if not isinstance(manifest, dict):
        raise ValueError(f"Expected a registry manifest object at {reference}")
    return manifest


def registry_manifest(reference):
    raw = registry_bytes(reference)
    return parse_registry_manifest(raw, reference) if raw is not None else None


def chart_tag(version):
    # OCI tags use an underscore for the SemVer build separator.
    return version.replace("+", "_")


def chart_receipt(reference, checksum):
    raw = registry_bytes(reference)
    if raw is None:
        return None
    manifest = parse_registry_manifest(raw, reference)
    layers = manifest.get("layers", [])
    if (manifest.get("schemaVersion") != 2 or manifest.get("config", {}).get("mediaType") != "application/vnd.cncf.helm.config.v1+json"
            or len(layers) != 1 or layers[0].get("mediaType") != "application/vnd.cncf.helm.chart.content.v1.tar+gzip"
            or layers[0].get("digest") != "sha256:" + checksum or manifest.get("annotations", {}).get(SOURCE_ANNOTATION) != SOURCE_URL):
        raise ValueError(f"Registry chart {reference} does not match the qualified archive or its source")
    return {"archiveSha256": checksum, "manifestDigest": "sha256:" + hashlib.sha256(raw).hexdigest()}


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
    """Inspect the prepared draft; never delete an asset to retry an upload."""
    repository = admission.REPOSITORY
    raw = subprocess.check_output(["gh", "release", "view", "v" + version, "--repo", repository, "--json", "tagName,isDraft,assets"],
                                  text=True, timeout=120)
    release = json.loads(raw)
    if release.get("tagName") != "v" + version or type(release.get("isDraft")) is not bool:
        raise ValueError("Prepared release identity differs")
    names = {item["name"] for item in release["assets"]}
    if len(names) != len(release["assets"]) or names.difference(path.name for path in paths):
        raise ValueError("Unexpected or duplicate release asset")
    missing = []
    with tempfile.TemporaryDirectory(prefix="catalog-release-assets-") as temporary:
        for path in paths:
            if path.name not in names:
                missing.append(path)
                continue
            if not path.is_file():
                raise ValueError("Final release metadata exists before its registry inputs")
            subprocess.run(["gh", "release", "download", "v" + version, "--repo", repository, "--pattern", path.name,
                            "--dir", temporary], check=True, timeout=300)
            if candidate.digest(Path(temporary) / path.name) != candidate.digest(path):
                raise ValueError("Release already contains a different asset")
    return {"draft": release["isDraft"], "missing": missing}


def destination_commit(version, expected_tag):
    """Refuse a replacement tag even when it points to the same commit."""
    prefix = "repos/" + admission.REPOSITORY + "/git/"
    if not admission.SHA.fullmatch(expected_tag):
        raise ValueError("Missing admitted release tag object")
    value = json.loads(subprocess.check_output(["gh", "api", prefix + "ref/tags/v" + version], text=True, timeout=120))["object"]
    if value.get("type") != "tag" or value.get("sha") != expected_tag:
        raise ValueError("Remote release tag differs from admitted tag")
    target = json.loads(subprocess.check_output(["gh", "api", prefix + "tags/" + expected_tag], text=True, timeout=120))["object"]
    if target.get("type") != "commit" or not admission.SHA.fullmatch(target.get("sha", "")):
        raise ValueError("Release tag must directly identify a commit")
    return target["sha"]


def image_target(product, version, arch=None):
    return "ghcr.io/open-agentic-catalog/" + ("open-agentic-catalog" if product == "backend" else "catalog-ui") + ":" + version + ("-" + arch if arch else "")


def registry_preflight(value):
    """Read every destination before any write, refusing conflicting versions."""
    members, digests, indexes = {}, {}, {}
    version = value["build"]["version"]
    for product in candidate.PRODUCTS:
        expected = {}
        for arch in ("amd64", "arm64"):
            key = product + "-" + arch
            target = image_target(product, version, arch)
            manifest = registry_manifest(target)
            existing_member(manifest, value["images"][key])
            members[key] = manifest is not None
            if manifest is not None:
                digests[key] = registry_digest(target)
                expected[arch] = digests[key]
        index = registry_manifest(image_target(product, version))
        if index is not None:
            if len(expected) != 2:
                raise ValueError("Existing index has missing platform tags")
            existing_index(index, expected)
        indexes[product] = index is not None
    return members, digests, indexes


def publish(directory):
    value = delivery_plan(directory, os.environ)
    with tempfile.TemporaryDirectory(prefix="catalog-release-public-") as temporary:
        deliver(directory, value, Path(temporary))


def deliver(directory, value, public_directory):
    # This function is selected only by the protected hosted job. Never used by
    # ordinary builds, act or local verification. Preserve partial progress.
    record = {"source": value["source"], "completed": [], "result": "incomplete"}
    destination = directory / "delivery-result.json"

    def operation(args, label):
        subprocess.run(args, check=True, timeout=900)
        record["completed"].append(label)
        destination.write_text(json.dumps(record, sort_keys=True) + "\n")

    version = value["build"]["version"]
    assets = public_assets(directory, value)
    assets["release.json"] = public_directory / "release.json"
    release_assets = [*assets.values(), public_directory / "SHA256SUMS"]

    def write_metadata(index_digests, platform_digests):
        public = {**value, "indexDigests": index_digests, "platformDigests": platform_digests}
        assets["release.json"].write_text(json.dumps(public, sort_keys=True, indent=2) + "\n")
        (public_directory / "SHA256SUMS").write_text(checksums(assets))

    # Confirm the maintainer prepared the release, and refuse changed existing
    # assets before pushing any container layer.
    try:
        if destination_commit(version, os.environ.get("RELEASE_TAG_OBJECT", "")) != value["source"]["commit"]:
            raise ValueError("Remote release tag no longer matches approved source")
        present, digests, indexes = registry_preflight(value)
        missing_charts = {}
        record["charts"] = {}
        for filename, (name, chart_version) in release_charts().items():
            reference = f"{CHART_REGISTRY}/{name}:{chart_tag(chart_version)}"
            path = "charts/" + filename
            archive = directory / path
            # Helm automatically uploads an adjacent provenance file as another OCI layer.
            if os.path.lexists(str(archive) + ".prov"):
                raise ValueError(f"Unexpected provenance file beside qualified chart {filename}")
            checksum = value["files"][path]
            receipt = chart_receipt(reference, checksum)
            if receipt:
                record["charts"][reference] = receipt
            else:
                missing_charts[reference] = (archive, checksum)
        images_complete = all(indexes.values()) and all(present.values())
        if images_complete:
            record["indexDigests"] = {product: registry_digest(image_target(product, version)) for product in candidate.PRODUCTS}
            record["platformDigests"] = dict(digests)
            write_metadata(record["indexDigests"], digests)
        release = existing_assets(version, release_assets)
        if not release["draft"]:
            if release["missing"] or missing_charts or not images_complete:
                raise ValueError("Published release is incomplete; no writes are permitted")
            record.update(result="success", releaseState="published", delivery="verified-existing")
            return
        for product in candidate.PRODUCTS:
            references = []
            members = {}
            for arch in ("amd64", "arm64"):
                key = product + "-" + arch
                image = value["images"][key]
                tag = image_target(product, version, arch)
                if not present[key]:
                    operation(["docker", "image", "load", "--input", str(directory / "images" / key / "image.tar")], "load-" + key)
                    operation(["docker", "image", "tag", image, tag], "tag-" + key)
                    operation(["docker", "image", "push", tag], "push-" + key)
                    manifest = registry_manifest(tag)
                    existing_member(manifest, image)
                    if manifest is None:
                        raise ValueError("Pushed platform manifest is unavailable")
                    digests[key] = registry_digest(tag)
                digest = digests[key]
                members[arch] = digest
                references.append(tag.split(":")[0] + "@" + digest)
                record.setdefault("platformDigests", {})[key] = digest
            target = image_target(product, version)
            if not indexes[product]:
                operation(["docker", "buildx", "imagetools", "create", "--tag", target, *references], "index-" + product)
            index = registry_manifest(target)
            if index is None:
                raise ValueError("Pushed platform index is unavailable")
            existing_index(index, members)
            verified_digest = registry_digest(target)
            if images_complete and verified_digest != record["indexDigests"][product]:
                raise ValueError("Registry index changed during verification")
            record.setdefault("indexDigests", {})[product] = verified_digest
            destination.write_text(json.dumps(record, sort_keys=True) + "\n")
        for reference, (archive, checksum) in missing_charts.items():
            operation(["helm", "push", str(archive), "oci://" + CHART_REGISTRY], "push-" + archive.name)
            receipt = chart_receipt(reference, checksum)
            if receipt is None:
                raise ValueError(f"Pushed chart {reference} is unavailable")
            record["charts"][reference] = receipt
            destination.write_text(json.dumps(record, sort_keys=True) + "\n")
        # No release creation or guessed notes. The maintainer prepares the
        # matching GitHub release; this job only attaches qualified assets.
        if not images_complete:
            write_metadata(record["indexDigests"], digests)
            release = existing_assets(version, release_assets)
            if not release["draft"]:
                raise ValueError("Prepared draft changed during delivery")
        if release["missing"]:
            operation(["gh", "release", "upload", "v" + version, "--repo", admission.REPOSITORY, *map(str, release["missing"])], "release-assets")
        final = existing_assets(version, release_assets)
        if not final["draft"] or final["missing"]:
            raise ValueError("Prepared draft changed or release asset upload is incomplete")
        record.update(result="success", releaseState="draft", delivery="images-pushed-files-in-draft")
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
