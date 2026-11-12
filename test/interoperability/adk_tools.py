"""Install a locked, execution-only ADK adapter and own its local containers."""

import base64
import hashlib
import json
import os
from pathlib import Path
import re
import signal
import tempfile
import uuid

from common import run
from runner import ROOT, docker, fingerprint, local_daemon
from mcp_tools import remove

HERE = Path(__file__).resolve().parent
INPUTS = HERE / "adk"
OUTPUT = ROOT / "output/interoperability/adk"
STAMP = OUTPUT / "installation.json"
INVENTORY = r'''
import hashlib, importlib.metadata as m, json
from pathlib import Path
import google.adk
root = Path(google.adk.__file__).parent
paths = ('tools/mcp_tool/mcp_toolset.py', 'tools/mcp_tool/mcp_tool.py',
         'tools/mcp_tool/mcp_session_manager.py', 'tools/base_authenticated_tool.py')
print(json.dumps({'source_files': {'google/adk/' + p: hashlib.sha256((root/p).read_bytes()).hexdigest() for p in paths},
 'packages': sorted([{'name': d.metadata['Name'], 'version': d.version,
 'license': d.metadata.get('License-Expression') or d.metadata.get('License') or
 '; '.join(v for v in d.metadata.get_all('Classifier', []) if v.startswith('License ::'))}
 for d in m.distributions()], key=lambda d: d['name'].lower())}))
'''


def identity():
    """Bind the exact installer, locks, upstream provenance and notices."""
    return fingerprint([*(INPUTS / name for name in ("Containerfile", "requirements.in", "requirements.txt", "provenance.json", "ADK-LICENSE", "MCP-LICENSE")), Path(__file__)])


def pins():
    """Read the reviewed ADK/MCP source and wheel selection."""
    return json.loads((INPUTS / "provenance.json").read_text())


def safe_args(name):
    """Confine native execution without host credentials, ports or RAM scratch."""
    if os.getuid() == 0:
        raise RuntimeError("Run ADK qualification as a non-root developer")
    args = ["--name", name, "--pull=never", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges",
            "--pids-limit=128", "--memory=512m", "--cpus=2", "--user", f"{os.getuid()}:{os.getgid()}",
            "--log-driver=local", "--log-opt=max-size=1m", "--log-opt=max-file=1", "--log-opt=compress=false"]
    for key in ("HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"):
        args += ["--env", key + "="]
    return args


class CreationUnconfirmed(RuntimeError):
    """A daemon creation may complete after the bounded client has failed."""


def create(name, *arguments):
    """Defer interruption until a registered container creation has completed."""
    mask = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM, signal.SIGALRM})
    uncertain = False
    try:
        return docker("create", *safe_args(name), *arguments, timeout=30, limit=2*1024*1024)
    except (OSError, RuntimeError) as error:
        uncertain = True
        raise CreationUnconfirmed("ADK container creation unconfirmed") from error
    finally:
        try:
            signal.pthread_sigmask(signal.SIG_SETMASK, mask)
        except (KeyboardInterrupt, TimeoutError) as error:
            if uncertain:
                raise CreationUnconfirmed("ADK container creation unconfirmed") from error
            raise


def verify_inventory(inventory, expected):
    """Require native adapter bytes and every selected direct package version."""
    assert inventory["source_files"] == expected["adk"]["source_files"], "ADK adapter source differs from reviewed source"
    versions = {p["name"].lower().replace("_", "-"): p["version"] for p in inventory["packages"]}
    assert versions["google-adk"] == expected["adk"]["version"] and versions["mcp"] == expected["mcp"]["version"], "ADK/MCP version drift"
    assert versions["mcp-types"] == expected["mcp"]["version"], "MCP types version drift"


def install():
    """Explicitly acquire immutable inputs and record an inspected offline image."""
    local_daemon()
    selected = pins()
    adk = selected["adk"]
    tag = json.loads(run(["gh", "api", f"repos/{adk['repository']}/git/ref/tags/v{adk['version']}"], timeout=30).stdout)
    assert tag["object"]["type"] == "commit" and tag["object"]["sha"] == adk["revision"], "ADK release source drift"
    license_data = json.loads(run(["gh", "api", f"repos/{adk['repository']}/contents/LICENSE?ref={adk['revision']}"], timeout=30).stdout)
    assert base64.b64decode(license_data["content"]) == (INPUTS / "ADK-LICENSE").read_bytes(), "ADK license drift"
    lock = (INPUTS / "requirements.txt").read_text()
    assert "sha256:" + adk["wheel_sha256"] in lock, "ADK wheel absent from lock"
    for wheel in selected["mcp"]["wheels"]:
        assert "sha256:" + wheel["digests"]["sha256"] in lock, "MCP wheel absent from lock"
    OUTPUT.mkdir(parents=True, exist_ok=True)
    before = identity()
    tag_name = "open-agentic-catalog-adk:" + before[:20]
    result = docker("build", "--progress=plain", "-t", tag_name, "-f", str(INPUTS / "Containerfile"), str(INPUTS), timeout=900, limit=16*1024*1024, check=False)
    (OUTPUT / "install.log").write_text(result.stdout + result.stderr)
    assert result.code == 0, "ADK installation failed; see output/interoperability/adk/install.log"
    image = docker("image", "inspect", tag_name, "--format", "{{.Id}}").stdout.strip()
    name = "catalog-adk-install-" + uuid.uuid4().hex[:12]
    try:
        create(name, "--network=none", image, "-c", INVENTORY)
        result = docker("start", "--attach", name, timeout=45, limit=2*1024*1024)
        assert not result.stderr.strip(), "ADK inventory emitted unexpected diagnostics"
        raw = result.stdout
    finally:
        remove([name])
    verify_inventory(json.loads(raw), selected)
    assert identity() == before, "ADK installation inputs changed"
    (OUTPUT / "dependencies.json").write_text(raw)
    stamp = {"identity": before, "image": image, "sources": selected, "inventory_sha256": hashlib.sha256(raw.encode()).hexdigest()}
    with tempfile.NamedTemporaryFile(mode="w", dir=OUTPUT, delete=False) as temporary:
        json.dump(stamp, temporary, indent=2)
        temporary.write("\n")
        path = Path(temporary.name)
    path.replace(STAMP)
    print("ADK tools installed: " + image)


def installed():
    """Refuse missing/stale tools without installing or contacting registries."""
    try:
        stamp = json.loads(STAMP.read_text())
        assert stamp["identity"] == identity() and stamp["sources"] == pins(), "ADK installation drift"
        assert re.fullmatch(r"sha256:[0-9a-f]{64}", stamp["image"]), "invalid ADK image identity"
        raw = (OUTPUT / "dependencies.json").read_bytes()
        assert hashlib.sha256(raw).hexdigest() == stamp["inventory_sha256"], "ADK inventory drift"
        verify_inventory(json.loads(raw), pins())
        assert docker("image", "inspect", stamp["image"], "--format", "{{.Id}}").stdout.strip() == stamp["image"], "ADK image drift"
        return stamp
    except (OSError, AssertionError, ValueError, KeyError, TypeError, RuntimeError) as error:
        raise RuntimeError("Run make adk-tools-install first (missing or stale tools)") from error
