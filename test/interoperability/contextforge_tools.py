"""Explicit installation of the trusted ContextForge image, separate from Catalog dependencies."""

import base64
import hashlib
import json
from pathlib import Path
import uuid

from common import run
from runner import ROOT, docker, fingerprint, local_daemon
import mcp_catalog_tools
import mcp_tools

INPUTS = Path(__file__).resolve().parent / "contextforge"
OUTPUT = ROOT / "output/interoperability/contextforge"
STAMP = OUTPUT / "installation.json"
INVENTORY = r'''
import importlib.metadata as m, json, subprocess
packages = []
for d in m.distributions():
    packages.append({'name': d.metadata['Name'], 'version': d.version,
                     'license': d.metadata.get('License-Expression') or d.metadata.get('License') or
                     '; '.join(c for c in d.metadata.get_all('Classifier', []) if c.startswith('License ::')),
                     'license_files': sorted(str(f) for f in d.files or [] if 'license' in str(f).lower() or 'copying' in str(f).lower())})
rpm = subprocess.run(['rpm', '-qa', '--qf', '%{NAME}\t%{VERSION}-%{RELEASE}\t%{LICENSE}\n'],
                     check=True, capture_output=True, text=True, timeout=30)
print(json.dumps({'python': sorted(packages, key=lambda p:p['name'].lower()), 'rpm': sorted(rpm.stdout.splitlines())}))
'''


def pins():
    value = json.loads((INPUTS / "inputs.json").read_text())
    assert value["repository"] == "IBM/mcp-context-forge"
    assert value["image"] == "ghcr.io/ibm/mcp-context-forge"
    assert value["platform"] == "linux/amd64" and value["minimumReleaseAgeDays"] == 0
    for key in ("index", "manifest"):
        assert len(value[key]) == 71 and value[key].startswith("sha256:")
        int(value[key][7:], 16)
    return value


def identity():
    return fingerprint([INPUTS / "inputs.json", INPUTS / "UPSTREAM-LICENSE", Path(__file__)])


def image_reference(value):
    return value["image"] + ":" + value["tag"] + "@" + value["index"]


def validate_release(value, release):
    assert release["tag_name"] == value["tag"], "New stable ContextForge release: review and update inputs.json first"
    assert not release["draft"] and not release["prerelease"], "ContextForge selection is not stable"


def validate_index(value, index):
    matches = [m for m in index["manifests"] if m.get("platform", {}).get("os") == "linux" and m.get("platform", {}).get("architecture") == "amd64"]
    assert len(matches) == 1 and matches[0]["digest"] == value["manifest"], "ContextForge AMD64 manifest drift"


def install():
    if not __debug__:
        raise RuntimeError("ContextForge installation requires Python assertions")
    assert local_daemon() == "amd64", "ContextForge execution is selected only for Linux AMD64"
    value = pins()
    release = json.loads(run(["gh", "api", "repos/" + value["repository"] + "/releases/latest"], timeout=30).stdout)
    validate_release(value, release)
    revision = run(["gh", "api", "repos/" + value["repository"] + "/commits/" + value["tag"], "--jq", ".sha"], timeout=30).stdout.strip()
    assert revision == value["releaseRevision"], "ContextForge release source drift"
    license_data = json.loads(run(["gh", "api", "repos/" + value["repository"] + "/contents/LICENSE?ref=" + value["imageRevision"]], timeout=30).stdout)
    assert base64.b64decode(license_data["content"]) == (INPUTS / "UPSTREAM-LICENSE").read_bytes(), "ContextForge source license drift"
    mcp_tools.installed()
    mcp_catalog_tools.installed()
    OUTPUT.mkdir(parents=True, exist_ok=True)
    reference = image_reference(value)
    validate_index(value, json.loads(docker("buildx", "imagetools", "inspect", "--raw", reference, timeout=30).stdout))
    result = docker("pull", "--platform", value["platform"], reference, timeout=900, limit=8*1024*1024, check=False)
    (OUTPUT / "install.log").write_text(result.stdout + result.stderr)
    assert result.code == 0, "ContextForge pull failed; inspect the installation log"
    info = json.loads(docker("image", "inspect", reference).stdout)[0]
    assert info["Architecture"] == "amd64" and info["Os"] == "linux"
    assert info["Config"]["Labels"]["org.opencontainers.image.revision"] == value["imageRevision"]
    name = "catalog-contextforge-inventory-" + uuid.uuid4().hex[:12]
    try:
        inventory = docker("run", "--rm", "--name", name, "--pull=never", "--network=none", "--read-only",
                           "--cap-drop=ALL", "--security-opt=no-new-privileges", "--memory=512m", "--pids-limit=64",
                           "--entrypoint=python3", reference, "-c", INVENTORY, timeout=90, limit=4*1024*1024).stdout
    finally:
        docker("rm", "-f", name, check=False, timeout=15)
    parsed = json.loads(inventory)
    assert parsed["python"] and parsed["rpm"], "ContextForge artifact inventory is incomplete"
    (OUTPUT / "artifact-inventory.json").write_text(inventory)
    scan = OUTPUT / "image-review.json"
    result = run(["trivy", "image", "--image-src", "docker", "--platform", value["platform"], "--offline-scan",
                  "--skip-db-update", "--skip-java-db-update", "--skip-check-update", "--disable-telemetry", "--skip-version-check",
                  "--scanners", "vuln,license", "--license-full", "--list-all-pkgs", "--format", "json",
                  "--output", str(scan), info["Id"]], timeout=900, limit=2*1024*1024, check=False)
    (OUTPUT / "scan.log").write_text(result.stdout + result.stderr)
    assert result.code == 0 and json.loads(scan.read_text()).get("Results"), "ContextForge image review failed"
    stamp = dict(identity=identity(), image=info["Id"], reference=reference, pins=value,
                 inventory=hashlib.sha256(inventory.encode()).hexdigest(), scan=hashlib.sha256(scan.read_bytes()).hexdigest())
    temporary = OUTPUT / "installation.tmp"
    temporary.write_text(json.dumps(stamp, indent=2) + "\n")
    temporary.replace(STAMP)
    print("ContextForge installed for local testing: " + value["version"] + " " + info["Id"])


def installed():
    try:
        stamp = json.loads(STAMP.read_text())
        assert stamp["identity"] == identity() and stamp["pins"] == pins()
        assert stamp["reference"] == image_reference(pins())
        assert hashlib.sha256((OUTPUT / "artifact-inventory.json").read_bytes()).hexdigest() == stamp["inventory"]
        assert hashlib.sha256((OUTPUT / "image-review.json").read_bytes()).hexdigest() == stamp["scan"]
        assert docker("image", "inspect", stamp["reference"], "--format", "{{.Id}}").stdout.strip() == stamp["image"]
        return stamp
    except (OSError, ValueError, KeyError, AssertionError, RuntimeError) as error:
        raise RuntimeError("Run make contextforge-tools-install first (missing or stale installation)") from error


if __name__ == "__main__":
    install()
