"""Install the independent dual-protocol MCP client without changing other MCP tools."""

import base64
from datetime import datetime, timedelta, timezone
import hashlib
import json
from pathlib import Path
import tempfile
import urllib.request
import uuid

from common import run
from runner import docker, fingerprint, local_daemon
import mcp_tools

INPUTS = mcp_tools.INPUTS / "catalog"
OUTPUT = mcp_tools.OUTPUT / "catalog-tools"
STAMP = OUTPUT / "installation.json"
INVENTORY = r'''
import hashlib, importlib.metadata as m, json
from pathlib import Path
import mcp, mcp_types
h = hashlib.sha256()
for module in (mcp, mcp_types):
    root = Path(module.__file__).parent
    for path in sorted(root.rglob('*')):
        if path.is_file() and '__pycache__' not in path.parts:
            h.update((module.__name__ + '/' + str(path.relative_to(root))).encode()
                     + b'\0' + hashlib.sha256(path.read_bytes()).digest())
print(json.dumps({'sdk_source_sha256': h.hexdigest(), 'packages': sorted([
    {'name': d.metadata['Name'], 'version': d.version,
     'license': d.metadata.get('License-Expression') or d.metadata.get('License') or
     '; '.join(v for v in d.metadata.get_all('Classifier', []) if v.startswith('License ::'))}
    for d in m.distributions()], key=lambda d: d['name'].lower())}))
'''


def identity():
    return fingerprint([INPUTS, Path(__file__), mcp_tools.HERE / "mcp_tools.py"])


def provenance():
    return {name: json.loads((INPUTS / (name + "-provenance.json")).read_text()) for name in ("mcp", "mcp-types")}


def validate_metadata(expected, actual, now):
    """Admit exact reviewed wheels only after the minimum release age."""
    assert actual["info"]["version"] == expected["version"], "MCP client version drift"
    wheels = {f["filename"]: f for f in actual["urls"]}
    for wanted in expected["wheels"]:
        found = wheels[wanted["filename"]]
        assert found["url"] == wanted["url"] and found["digests"]["sha256"] == wanted["digests"]["sha256"], "MCP wheel identity drift"
        assert found["upload_time_iso_8601"] == wanted["upload_time_iso_8601"], "MCP wheel publication drift"
        published = datetime.fromisoformat(found["upload_time_iso_8601"].replace("Z", "+00:00"))
        assert published + timedelta(days=14) <= now, "MCP client release is too recent"
        assert "sha256:" + wanted["digests"]["sha256"] in (INPUTS / "requirements.txt").read_text(), "MCP wheel is absent from lock"


def install():
    local_daemon()
    pins = provenance()
    for name, pin in pins.items():
        with urllib.request.urlopen("https://pypi.org/pypi/" + name + "/" + pin["version"] + "/json", timeout=30) as response:
            raw = response.read(1_048_577)
        assert len(raw) <= 1_048_576, "MCP registry metadata exceeds its bound"
        validate_metadata(pin, json.loads(raw), datetime.now(timezone.utc))
    sdk = pins["mcp"]
    ref = json.loads(run(["gh", "api", "repos/" + sdk["repository"] + "/git/ref/tags/v" + sdk["version"]], timeout=30).stdout)
    assert ref["object"]["sha"] == sdk["revision"], "MCP SDK source revision drift"
    evidence = json.loads(run(["gh", "api", "repos/" + sdk["repository"] + "/contents/LICENSE?ref=" + sdk["revision"]], timeout=30).stdout)
    assert base64.b64decode(evidence["content"]) == (INPUTS / "SDK-LICENSE").read_bytes(), "MCP SDK license drift"
    OUTPUT.mkdir(parents=True, exist_ok=True)
    before = identity()
    tag = "open-agentic-catalog-mcp-client:" + before[:20]
    result = docker("build", "--progress=plain", "-t", tag, "-f", str(INPUTS / "Containerfile"), str(INPUTS), timeout=900, limit=16*1024*1024, check=False)
    (OUTPUT / "install.log").write_text(result.stdout + result.stderr)
    assert result.code == 0, "MCP client build failed; inspect its private install log"
    image = docker("image", "inspect", tag, "--format", "{{.Id}}").stdout.strip()
    name = "catalog-mcp-client-install-" + uuid.uuid4().hex[:12]
    try:
        mcp_tools.create(name, "--network=none", image, "-c", INVENTORY)
        inventory = docker("start", "--attach", name, timeout=30, limit=2*1024*1024).stdout
    finally:
        mcp_tools.remove([name])
    packages = json.loads(inventory)["packages"]
    for name, pin in pins.items():
        assert any(p["name"] == name and p["version"] == pin["version"] for p in packages), "MCP installed version drift"
    assert before == identity(), "MCP client inputs changed during installation"
    (OUTPUT / "dependencies.json").write_text(inventory)
    stamp = {"identity": before, "image": image, "packages": pins,
             "inventory_sha256": hashlib.sha256(inventory.encode()).hexdigest()}
    with tempfile.NamedTemporaryFile(mode="w", dir=OUTPUT, delete=False) as temporary:
        json.dump(stamp, temporary, indent=2)
        temporary.write("\n")
        path = Path(temporary.name)
    path.replace(STAMP)
    print("Independent MCP client installed: " + image)


def installed():
    try:
        stamp = json.loads(STAMP.read_text())
        assert stamp["identity"] == identity(), "stale inputs"
        assert stamp["packages"] == provenance(), "provenance drift"
        assert stamp["image"].startswith("sha256:"), "invalid image"
        assert hashlib.sha256((OUTPUT / "dependencies.json").read_bytes()).hexdigest() == stamp["inventory_sha256"], "inventory drift"
        assert docker("image", "inspect", stamp["image"], "--format", "{{.Id}}").stdout.strip() == stamp["image"], "image drift"
        return stamp
    except (OSError, AssertionError, KeyError, TypeError, ValueError, RuntimeError) as error:
        raise RuntimeError("Run make mcp-catalog-tools-install first (missing or stale client)") from error
