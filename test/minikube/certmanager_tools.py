"""Acquire checksum-bound local cert-manager test inputs without deploying them."""

import argparse
import base64
import hashlib
import json
from pathlib import Path
import platform
import subprocess

ROOT = Path(__file__).resolve().parents[2]
PINS = ROOT / "test/minikube/fixtures/cert-manager/pins.json"
TOOLS = ROOT / ".tools/minikube-cert-manager"


def command(*args):
    """Run a selected acquisition command with a finite deadline."""
    return subprocess.run(args, cwd=ROOT, check=True, capture_output=True,
                          text=True, timeout=300).stdout


def verify(path, digest):
    """Reject changed inputs before any acquired executable runs."""
    if hashlib.sha256(path.read_bytes()).hexdigest() != digest:
        raise ValueError(f"checksum mismatch: {path.name}")


def install():
    """Keep all downloaded artifacts and licenses in the ignored tool cache."""
    if platform.system() != "Linux" or platform.machine() not in ("amd64", "x86_64"):
        raise ValueError("cert-manager qualification supports Linux AMD64 only")
    pins = json.loads(PINS.read_text())
    TOOLS.mkdir(parents=True, exist_ok=True)
    for chart in pins["charts"].values():
        path = TOOLS / chart["file"]
        if not path.exists():
            command("helm", "pull", chart["oci"], "--version", chart["version"],
                    "--destination", str(TOOLS))
        verify(path, chart["sha256"])
    cli = pins["cmctl"]
    for name, digest in [(cli["file"], cli["sha256"]),
                         (cli["file"] + ".spdx.sbom", cli["sbomSha256"])]:
        path = TOOLS / name
        if not path.exists():
            command("gh", "release", "download", cli["version"], "--repo", "cert-manager/cmctl",
                    "--pattern", name, "--dir", str(TOOLS))
        verify(path, digest)
    (TOOLS / cli["file"]).chmod(0o755)
    for repo, source in pins["sources"].items():
        path = TOOLS / source["file"]
        if not path.exists():
            document = json.loads(command("gh", "api", f"repos/{repo}/contents/{source['path']}?ref={source['revision']}"))
            path.write_bytes(base64.b64decode(document["content"], validate=False))
        verify(path, source["sha256"])
    for pin in pins["images"].values():
        command("docker", "pull", "--platform=linux/amd64", pin["reference"])
    print("Pinned cert-manager inputs cached; no cluster was started")


if __name__ == "__main__":
    argparse.ArgumentParser(description=__doc__).parse_args()
    install()
