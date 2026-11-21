"""Acquire the pinned upstream non-HA installation, CLI and Linux AMD64 images."""

import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile

PINS = json.loads(Path(__file__).with_name("pins.json").read_text())
CACHE = Path(".tools/minikube-argocd")


def verify(path, digest):
    if not path.is_file():
        return False
    with path.open("rb") as data:
        return hashlib.file_digest(data, "sha256").hexdigest() == digest


def prepare(check=False):
    if platform.system() != "Linux" or platform.machine() not in ("amd64", "x86_64"):
        raise ValueError("Argo CD qualification requires Linux AMD64")
    files = (("install.yaml", "manifestSHA256"), ("argocd", "cliSHA256"))
    if check:
        if not os.access(CACHE / "argocd", os.X_OK) or not all(verify(CACHE / name, PINS[key]) for name, key in files):
            raise ValueError("run make argocd-tools-install to prepare the verified CLI and manifest")
    else:
        acquire_files(files)
    preload = prepare_preload(check)
    for image in PINS["images"].values():
        result = subprocess.run(
            [
                "docker",
                "image",
                "inspect",
                "--format",
                "{{.Os}}/{{.Architecture}}",
                image,
            ],
            capture_output=True,
            text=True,
            check=False,
            timeout=30,
        )
        if result.returncode or result.stdout.strip() != "linux/amd64":
            if check:
                raise ValueError("run make argocd-tools-install for " + image)
            subprocess.run(
                ["docker", "pull", "--platform=linux/amd64", image],
                check=True,
                timeout=600,
            )
    return preload


def prepare_preload(check):
    pin = PINS["preload"]
    cached = CACHE / pin["name"]
    override = os.environ.get("CATALOG_MINIKUBE_PRELOAD_ARCHIVE")
    path = Path(override) if override else cached
    if check or override:
        if path.name != pin["name"] or not verify(path, pin["sha256"]):
            raise ValueError(
                "missing or incorrect Kubernetes preload; run make argocd-tools-install or provide the pinned archive"
            )
    if check:
        return path
    if verify(cached, pin["sha256"]):
        return cached
    with tempfile.TemporaryDirectory(dir=CACHE) as tmp:
        fetched = Path(tmp) / pin["name"]
        if override:
            shutil.copyfile(path, fetched)
        else:
            subprocess.run(
                [
                    "curl",
                    "--fail",
                    "--silent",
                    "--show-error",
                    "--location",
                    "--proto",
                    "=https",
                    "--output",
                    str(fetched),
                    pin["url"],
                ],
                check=True,
                timeout=600,
            )
        if not verify(fetched, pin["sha256"]):
            raise ValueError("checksum mismatch for Kubernetes preload")
        fetched.replace(cached)
    return cached


def acquire_files(files):
    CACHE.mkdir(exist_ok=True, parents=True)
    with tempfile.TemporaryDirectory(dir=CACHE) as tmp:
        for name, key in files:
            if verify(CACHE / name, PINS[key]):
                if name == "argocd":
                    (CACHE / name).chmod(0o700)
                continue
            path = Path(tmp) / name
            if name == "argocd":
                subprocess.run(
                    [
                        "gh",
                        "release",
                        "download",
                        PINS["version"],
                        "--repo",
                        "argoproj/argo-cd",
                        "--pattern",
                        "argocd-linux-amd64",
                        "--output",
                        str(path),
                    ],
                    check=True,
                    timeout=300,
                )
                path.chmod(0o700)
            else:
                with path.open("wb") as output:
                    subprocess.run(
                        [
                            "gh",
                            "api",
                            "-H",
                            "Accept: application/vnd.github.raw+json",
                            "repos/argoproj/argo-cd/contents/manifests/install.yaml?ref=" + PINS["commit"],
                        ],
                        stdout=output,
                        check=True,
                        timeout=180,
                    )
            if not verify(path, PINS[key]):
                raise ValueError(f"checksum mismatch for {name}")
            path.replace(CACHE / name)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true", help="verify all inputs offline")
    args = parser.parse_args()
    if args.check:
        import yaml  # Repo developer Python provides the manifest reader.

        if not callable(yaml.safe_load_all):
            raise SystemExit("PyYAML is required to read Kubernetes manifests")
    archive = prepare(check=args.check)
    if args.check:
        print(archive.resolve())
