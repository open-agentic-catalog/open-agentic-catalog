"""Prepare the pinned Agentic Resource Registry and fixture images before offline execution."""

import hashlib
import json
import os
import shutil
from pathlib import Path
import subprocess
import tarfile
import tempfile

import yaml

from agentfinder_tools import runner_image
from runner import docker, local_daemon

ROOT = Path(__file__).resolve().parents[2]
HERE = Path(__file__).resolve().parent
OUTPUT = ROOT / "output/interoperability/agentic-registry"
REVISION = "d587d73df6bbdde4be2adcf4f834dca89c2902d9"
ARCHIVE = "df45ba4f75e53d2379faf0aba730e59c5339c7e8bc7b20127a664657e2ddad1e"
PEER = "oac-agentic-registry:" + REVISION
FIXTURE = "oac-agentic-registry:fixture"
OVERLAY = HERE / "agentic-registry/compose.yaml"


def support_images():
    services = yaml.safe_load(OVERLAY.read_text())["services"]
    catalog_postgres = yaml.safe_load((ROOT / "compose.yaml").read_text())["services"]["postgres"]["image"]
    return [services[name]["image"] for name in ("arr-db", "arr-redis", "arr-clickhouse")] + [catalog_postgres, runner_image()]


def installed():
    path = OUTPUT / "prepared.json"
    if not path.is_file():
        raise ValueError("Run make agentic-registry-tools-install first")
    receipt = json.loads(path.read_text())
    assert set(receipt["images"]) == {*support_images(), PEER, FIXTURE}, "incomplete prepared image inventory"
    assert (receipt["revision"], receipt["archive"], receipt["platform"]) == (REVISION, ARCHIVE, local_daemon())
    for image, identity in receipt["images"].items():
        assert docker("image", "inspect", "--format", "{{.Id}}", image).stdout.strip() == identity, "prepared image changed: " + image
    return receipt


def install():
    platform = local_daemon()
    OUTPUT.mkdir(parents=True, exist_ok=True)
    receipt_path = OUTPUT / "prepared.json"
    archive = Path(os.environ.get("ARR_SOURCE_ARCHIVE", OUTPUT / "peer.tar.gz")).resolve()
    if not archive.exists() and "ARR_SOURCE_ARCHIVE" not in os.environ:
        with tempfile.TemporaryDirectory(dir=OUTPUT) as stage, (Path(stage) / "peer.tar.gz").open("w+b") as tmp:
            subprocess.run(["gh", "api", "--method", "GET", "repos/AgenticResourceRegistry/AgenticResourceRegistry/tarball/" + REVISION],
                           stdout=tmp, check=True, timeout=180)
            tmp.seek(0)
            assert hashlib.file_digest(tmp, "sha256").hexdigest() == ARCHIVE, "archive digest differs"
            Path(tmp.name).replace(archive)
    with archive.open("rb") as stream:
        assert hashlib.file_digest(stream, "sha256").hexdigest() == ARCHIVE, "archive digest differs"
    images = support_images()
    for image in images:
        if docker("image", "inspect", image, check=False).code:
            docker("pull", image, timeout=600)
    with tempfile.TemporaryDirectory(dir=OUTPUT) as directory:
        with tarfile.open(archive) as src:
            src.extractall(directory, filter="data")
        tree, = Path(directory).iterdir()
        assert "Apache License" in (tree / "LICENSE").read_text()
        shutil.copyfile(tree / "LICENSE", OUTPUT / "peer-LICENSE")
        shutil.copytree(tree / "LICENSES", OUTPUT / "peer-licenses", dirs_exist_ok=True)
        subprocess.run(["docker", "build", "-f", str(tree / "docker/Dockerfile.api"), "-t", PEER, str(tree)], check=True, timeout=1800)
    subprocess.run(["docker", "build", "-f", "test/functional/Containerfile", "-t", FIXTURE, "."], check=True, timeout=600)
    identities = {image: docker("image", "inspect", "--format", "{{.Id}}", image).stdout.strip() for image in [*images, PEER, FIXTURE]}
    python = docker("run", "--rm", "--pull=never", "--network=none", "--entrypoint=/app/.venv/bin/python", PEER, "-c",
                    "import importlib.metadata as m,json,platform;print(json.dumps([platform.python_version(),"
                    "sorted((d.metadata['Name'],d.version,d.metadata.get('License-Expression') or (d.metadata.get('License') or '').split('\\n')[0][:120]) for d in m.distributions())]))").stdout
    packages = docker("run", "--rm", "--pull=never", "--network=none", "--entrypoint=dpkg-query", PEER, "-W").stdout
    (OUTPUT / "python-packages.json").write_text(python)
    (OUTPUT / "os-packages.tsv").write_text(packages)
    with tempfile.TemporaryDirectory(dir=OUTPUT) as stage:
        receipt = Path(stage) / receipt_path.name
        receipt.write_text(json.dumps(dict(platform=platform, revision=REVISION, archive=ARCHIVE, images=identities), indent=2) + "\n")
        receipt.replace(receipt_path)
    installed()


if __name__ == "__main__":
    if not __debug__:
        raise RuntimeError("preparation requires assertions")
    install()
