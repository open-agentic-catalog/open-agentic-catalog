"""Local image archive admission and same-source release evidence. Never publishes."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import subprocess

import admission

ROOT = admission.ROOT
PRODUCTS = ("backend", "ui")
PLATFORMS = ("linux/amd64", "linux/arm64")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")


def digest(path):
    value = hashlib.sha256()
    with path.open("rb") as stream:
        for block in iter(lambda: stream.read(1 << 20), b""):
            value.update(block)
    return value.hexdigest()


def run(args):
    return subprocess.check_output(args, cwd=ROOT, timeout=900, text=True).strip()


def image_identity(raw, value):
    """Bind the exact engine object to declared product, platform and build inputs."""
    if (value["product"] not in PRODUCTS or value["platform"] not in PLATFORMS
            or not DIGEST.fullmatch(value["imageId"]) or raw.get("Id") != value["imageId"]
            or raw.get("Os", "") + "/" + raw.get("Architecture", "") != value["platform"]):
        raise ValueError("Candidate image/platform mismatch")
    labels = raw.get("Config", {}).get("Labels", {})
    expected = {"org.opencontainers.image.version": value["build"]["version"],
                "org.opencontainers.image.revision": value["build"]["commit"],
                "io.open-agentic-catalog.source-state": value["build"]["sourceState"],
                "org.opencontainers.image.title": "Open Agentic Catalog" + (" UI" if value["product"] == "ui" else "")}
    if any(labels.get(key) != entry for key, entry in expected.items()):
        raise ValueError("Candidate build labels mismatch")


def inventory(directory):
    files = {}
    total = 0
    for path in sorted(directory.rglob("*")):
        name = path.relative_to(directory).as_posix()
        if path.is_symlink() or any(part.startswith(".") for part in Path(name).parts) or not re.fullmatch(r"[A-Za-z0-9_./+-]{1,512}", name):
            raise ValueError("Unsafe candidate file")
        if path.is_dir():
            continue
        if not path.is_file():
            raise ValueError("Nonregular candidate file")
        if name == "candidate.json":
            continue
        total += path.stat().st_size
        if len(files) >= 20000 or total > 4 * 1024 ** 3 or path.stat().st_size > 3 * 1024 ** 3:
            raise ValueError("Candidate exceeds bounds")
        files[name] = digest(path)
    return files


def read_candidate(filename, *, identity=None, product=None, platform=None):
    if filename.is_symlink() or not filename.is_file() or filename.stat().st_size > 4 << 20:
        raise ValueError("Missing candidate descriptor")
    value = json.loads(filename.read_text())
    if value.get("schemaVersion") != 1 or value.get("product") not in PRODUCTS or value.get("platform") not in PLATFORMS:
        raise ValueError("Invalid candidate descriptor")
    if (product and value["product"] != product) or (platform and value["platform"] != platform):
        raise ValueError("Wrong candidate selection")
    if identity is not None and value["source"] != identity:
        raise ValueError("Candidate source mismatch")
    if (value["build"]["commit"] != value["source"]["commit"]
            or value["build"]["sourceState"] not in {"clean", "modified"}
            or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9.+-]{0,127}", value["build"]["version"])):
        raise ValueError("Candidate build identity mismatch")
    if inventory(filename.parent) != value["files"] or "image.tar" not in value["files"]:
        raise ValueError("Candidate archive or evidence changed")
    if not any(name.endswith(".spdx.json") for name in value["files"]):
        raise ValueError("Missing candidate SPDX evidence")
    summary = json.loads(admission.regular(filename.parent, "scan-summary.json").read_text())
    if summary.get("outcome") != "passed" or summary.get("imageId") != value["imageId"] or summary.get("platform") != value["platform"]:
        raise ValueError("Missing exact-image scan summary")
    if value["checks"] != {"inspection": "success", "sbom": "success", "scan": "success"}:
        raise ValueError("Incomplete candidate checks")
    return value


def seal(directory, image, product, platform, scan_directory):
    if product not in PRODUCTS or platform not in PLATFORMS or not re.fullmatch(r"[a-z0-9][a-z0-9./:_-]{0,200}", image):
        raise ValueError("Invalid candidate selection")
    if directory.is_symlink() or not directory.is_dir() or (directory / "candidate.json").exists() or (directory / "image.tar").exists():
        raise ValueError("Select a fresh owned candidate directory")
    before = admission.source_identity(ROOT)
    build = json.loads(run(["python3", "-B", "test/packaging/versioning.py", "metadata"]))
    raw = json.loads(run(["docker", "image", "inspect", image]))[0]
    value = {"schemaVersion": 1, "product": product, "platform": platform, "image": image,
             "imageId": raw["Id"], "source": before, "build": build,
             "checks": {"inspection": "success", "sbom": "success", "scan": "success"}}
    image_identity(raw, value)
    inspection = json.loads((directory / "inspection.json").read_text())
    if inspection["imageId"] != value["imageId"] or inspection["platform"] != platform:
        raise ValueError("Missing exact-image inspection")
    scans = list(scan_directory.rglob("run.json"))
    if len(scans) != 1:
        raise ValueError("Missing exact-image scan")
    scan = json.loads(scans[0].read_text())
    if scan.get("outcome") != "passed" or scan.get("image", {}).get("imageId") != value["imageId"]:
        raise ValueError("Candidate scan did not pass")
    summary = {"outcome": "passed", "imageId": value["imageId"], "platform": platform,
               "toolVersion": scan["toolVersion"], "findings": scan["findings"]}
    (directory / "scan-summary.json").write_text(json.dumps(summary, sort_keys=True) + "\n")
    run(["docker", "image", "save", "--output", str(directory / "image.tar"), image])
    if admission.source_identity(ROOT) != before:
        raise ValueError("Candidate source changed")
    image_identity(json.loads(run(["docker", "image", "inspect", image]))[0], value)
    value["files"] = inventory(directory)
    (directory / "candidate.json").write_text(json.dumps(value, sort_keys=True, indent=2) + "\n")
    read_candidate(directory / "candidate.json", identity=before)
    return value


def admit(filename, product, *, load=False):
    value = read_candidate(filename, identity=admission.source_identity(ROOT), product=product, platform="linux/amd64")
    if load:
        run(["docker", "image", "load", "--input", str(filename.parent / "image.tar")])
    # Resolve by immutable engine ID. Never trust a tag after archive admission.
    image_identity(json.loads(run(["docker", "image", "inspect", value["imageId"]]))[0], value)
    return value


def release_results(source, results):
    expected = {"source", "security", "clients", "charts", "candidates", "runtime"}
    if set(results) != expected or any(result != "success" for result in results.values()):
        raise ValueError("Incomplete release qualification")
    return {"schemaVersion": 1, "source": source, "checks": results, "result": "success"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    selected = sub.add_parser("seal")
    selected.add_argument("--directory", type=Path, required=True)
    selected.add_argument("--image", required=True)
    selected.add_argument("--scan-directory", type=Path, required=True)
    selected.add_argument("--product", choices=PRODUCTS, required=True)
    selected.add_argument("--platform", choices=PLATFORMS, required=True)
    for name in ("admit", "load"):
        selected = sub.add_parser(name)
        selected.add_argument("--backend", type=Path, required=True)
        selected.add_argument("--ui", type=Path)
    selected = sub.add_parser("results")
    selected.add_argument("--results", required=True)
    selected.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    if args.command == "seal":
        seal(args.directory.absolute(), args.image, args.product, args.platform, args.scan_directory)
    elif args.command in {"admit", "load"}:
        value = {"backend": admit(args.backend.absolute(), "backend", load=args.command == "load")}
        if args.ui:
            value["ui"] = admit(args.ui.absolute(), "ui", load=args.command == "load")
            if value["ui"]["build"] != value["backend"]["build"]:
                raise ValueError("Candidate builds differ")
        print(json.dumps(value, sort_keys=True))
    else:
        value = release_results(admission.source_identity(ROOT), json.loads(args.results))
        args.output.parent.mkdir(parents=True, exist_ok=True)
        args.output.write_text(json.dumps(value, sort_keys=True) + "\n")


if __name__ == "__main__":
    main()
