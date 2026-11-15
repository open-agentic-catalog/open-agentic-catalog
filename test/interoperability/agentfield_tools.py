"""Explicit acquisition of the unchanged native AgentField consumer, never on tests."""

import hashlib
import json
from pathlib import Path
import shutil
import tempfile
import uuid

from common import run
import runner as shared

INPUTS = shared.HERE / "agentfield"
OUTPUT = shared.ROOT / "output/interoperability/agentfield"
STAMP = OUTPUT / "tools.json"
ARTIFACTS = ("build.txt", "dependency-licenses.csv", "licenses.tar")


def pins():
    return json.loads((INPUTS / "inputs.json").read_text())


def identity():
    return shared.fingerprint([INPUTS / "inputs.json", INPUTS / "Containerfile",
                               INPUTS / "UPSTREAM-LICENSE", Path(__file__), shared.HERE / "runner.py"])


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def installed():
    """Require exact local native source, image and notices, with no downloads."""
    try:
        value = json.loads(STAMP.read_text())
        assert value["toolIdentity"] == identity() and value["source"] == pins(), "AgentField input drift"
        assert isinstance(value["artifacts"], dict) and set(value["artifacts"]) == set(ARTIFACTS), "AgentField evidence inventory incomplete"
        actual = shared.docker("image", "inspect", value["image"], "--format={{.Id}}").stdout.strip()
        assert actual == value["imageId"], "AgentField image drift"
        for name, expected in value["artifacts"].items():
            assert digest(OUTPUT / name) == expected, "AgentField build evidence drift"
        assert pins()["revision"] in (OUTPUT / "build.txt").read_text(), "native revision missing"
        return value
    except (OSError, AssertionError, RuntimeError, ValueError, KeyError, TypeError) as error:
        raise RuntimeError("Run make agentfield-tools-install first (missing or stale tools)") from error


def install():
    """Download public source/modules, then build a confined execution-only image."""
    shared.local_daemon()
    selected = pins()
    before = identity()
    python_image = next(line.split()[1] for line in (shared.HERE / "Containerfile").read_text().splitlines() if line.startswith("FROM python:"))
    if shared.docker("image", "inspect", python_image, check=False).code:
        shared.docker("pull", python_image, timeout=300)
    OUTPUT.mkdir(parents=True, exist_ok=True)
    image = "open-agentic-catalog-agentfield:" + before[:20]
    with tempfile.TemporaryDirectory(prefix="install-", dir=OUTPUT) as name:
        directory = Path(name)
        raw = run(["gh", "api", "--method", "GET", "repos/" + selected["repository"] + "/tarball/" + selected["revision"]],
                  timeout=180, limit=64 << 20, binary=True).stdout
        source = directory / "source"
        shared.extract_source(raw, source, selected["sourceDigest"], max_members=4000,
                              max_member_bytes=8 << 20, max_bytes=96 << 20)
        assert (source / "LICENSE").read_bytes() == (INPUTS / "UPSTREAM-LICENSE").read_bytes(), "native license drift"
        for filename, key in (("go.mod", "goModDigest"), ("go.sum", "goSumDigest")):
            assert digest(source / "control-plane" / filename) == selected[key], "native module lock drift"
        shutil.copyfile(INPUTS / "Containerfile", directory / "Containerfile")
        result = shared.docker("build", "--progress=plain", "-f", str(directory / "Containerfile"), "--build-arg", "REVISION=" + selected["revision"],
                               "--build-arg", "MOD_DIGEST=" + selected["goModDigest"], "--build-arg", "SUM_DIGEST=" + selected["goSumDigest"],
                               "-t", image, str(directory), timeout=1200, limit=16 << 20, check=False)
        (OUTPUT / "install.log").write_text(result.stdout + result.stderr)
        assert result.code == 0, "AgentField build failed; see output/interoperability/agentfield/install.log"
    name = "open-agentic-catalog-agentfield-install-" + uuid.uuid4().hex[:12]
    try:
        shared.docker("create", "--name", name, "--network=none", "--entrypoint=/unused", image)
        for filename in ("build.txt", "dependency-licenses.csv"):
            shared.docker("cp", name + ":/" + filename, str(OUTPUT / filename))
        # Preserve actual linked dependency notices, without publishing the peer.
        licenses = shared.docker("cp", name + ":/licenses", "-", binary=True, limit=8 << 20).stdout
        assert licenses, "missing AgentField dependency notices"
        (OUTPUT / "licenses.tar").write_bytes(licenses)
    finally:
        shared.docker("rm", "-f", name, check=False)
    assert identity() == before, "AgentField installation inputs changed"
    value = dict(source=selected, toolIdentity=before, image=image,
                 imageId=shared.docker("image", "inspect", image, "--format={{.Id}}").stdout.strip(),
                 artifacts={name: digest(OUTPUT / name) for name in ARTIFACTS})
    STAMP.write_text(json.dumps(value, indent=2) + "\n")
    installed()
    print("Installed unchanged AgentField " + selected["revision"])


if __name__ == "__main__":
    install()
