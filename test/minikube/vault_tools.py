"""Acquire the exact external Vault test inputs without creating a cluster."""

import argparse
import base64
import hashlib
import json
from pathlib import Path
import platform
import subprocess
import urllib.request

ROOT = Path(__file__).resolve().parents[2]
PINS = ROOT / "test/minikube/fixtures/vault/pins.json"
TOOLS = ROOT / ".tools/minikube-vault"


def command(*args):
    """Run explicit acquisition with bounded output and execution time."""
    return subprocess.run(args, cwd=ROOT, check=True, capture_output=True,
                          text=True, timeout=600).stdout


def verify(path, digest):
    """Reject changed artifacts before any cluster uses them."""
    if hashlib.sha256(path.read_bytes()).hexdigest() != digest:
        raise ValueError(f"checksum mismatch: {path.name}")


def install():
    """Retain charts, notices and image identities in the ignored tools cache."""
    if platform.system() != "Linux" or platform.machine() not in ("amd64", "x86_64"):
        raise ValueError("Vault qualification supports Linux AMD64 only")
    pins = json.loads(PINS.read_text())
    TOOLS.mkdir(parents=True, exist_ok=True)
    for chart in pins["charts"].values():
        path = TOOLS / chart["file"]
        if not path.exists():
            with urllib.request.urlopen(chart["url"], timeout=60) as response:
                raw = response.read(4 * 1024 * 1024 + 1)
            if len(raw) > 4 * 1024 * 1024:
                raise ValueError("chart archive exceeds acquisition bound")
            path.write_bytes(raw)
        verify(path, chart["sha256"])
    for repo, source in pins["sources"].items():
        path = TOOLS / source["file"]
        if not path.exists():
            document = json.loads(command("gh", "api", f"repos/{repo}/contents/{source['path']}?ref={source['revision']}"))
            path.write_bytes(base64.b64decode(document["content"]))
        verify(path, source["sha256"])
    receipt = {"pinsSha256": hashlib.sha256(PINS.read_bytes()).hexdigest(), "images": {}}
    for name, pin in pins["images"].items():
        command("docker", "pull", "--platform=linux/amd64", pin["reference"])
        image = json.loads(command("docker", "image", "inspect", pin["reference"]))[0]
        if image["Os"] != "linux" or image["Architecture"] != "amd64":
            raise ValueError("unexpected Vault image platform")
        receipt["images"][name] = image["Id"]
    (TOOLS / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    print("Pinned Vault charts, notices and images cached; no cluster was started")


if __name__ == "__main__":
    argparse.ArgumentParser(description=__doc__).parse_args()
    install()
