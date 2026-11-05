"""Small owned portable packages and their exact expected contents."""

import copy
import json

NAME = "owned-plugin"
SKILL = "owned-skill"
DESCRIPTION = "Inspect invoices without executing commands."
BODY = "Read references/guide.txt. Never run scripts/sentinel.py."
URL = "http://127.0.0.1:17654/mcp"
MEDIA = "application/agent-plugins+zip"
PROFILE = "agent-plugins-1.0.0-1fc1b627"
TAG = "independent-plugin-handoff"
IDENTIFIER = "urn:air:catalog.example.com:plugins:" + NAME
MANIFEST = {
    "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
    "name": NAME,
    "version": "1.0.0",
    "description": "Owned portable Plugin",
    "license": "Apache-2.0",
    "extensions": {"net.catalog.fixture": {"entry_command": ["inert"]}},
}
MCP = {
    "$schema": "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json",
    "mcpServers": {"owned": {"type": "streamable-http", "url": URL}},
}
DIRECT = (
    "valid",
    "malformed",
    "missing-schema",
    "unsupported-schema",
    "invalid-name",
    "component-recovery",
)
REFUSALS = ("digest", "size", "missing", "invalid", "wrong-tenant")


def files(case="valid", *, later=False):
    manifest = copy.deepcopy(MANIFEST)
    mcp = copy.deepcopy(MCP)
    if later:
        manifest["version"] = "1.0.1"
    if case == "missing-schema":
        del manifest["$schema"]
    if case == "unsupported-schema":
        manifest["$schema"] = (
            "https://agent-plugins.org/schemas/99.0.0/plugin.schema.json"
        )
    if case == "invalid-name":
        manifest["name"] = "invalid name"
    if case == "component-recovery":
        mcp["mcpServers"]["invalid"] = {"type": "streamable-http", "url": 17}
    result = {
        "plugin.json": ("{" if case == "malformed" else json.dumps(manifest)).encode(),
        "mcp.json": json.dumps(mcp).encode(),
        "skills/"
        + SKILL
        + "/SKILL.md": f"---\nname: {SKILL}\ndescription: {DESCRIPTION}\n---\n{BODY}\n".encode(),
        "skills/" + SKILL + "/references/guide.txt": b"Later unpublished reference.\n"
        if later
        else b"Original retained reference.\n",
        "skills/"
        + SKILL
        + "/scripts/sentinel.py": b"from pathlib import Path\nPath('/tmp/plugin-executed').write_text('unexpected')\n",
        "net.catalog.fixture/opaque.txt": b"Never interpreted by this client.\n",
    }
    return result


def prepare_direct(root):
    for case in DIRECT:
        directory = root / case
        for name, body in files(case).items():
            path = directory / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(body)
        # A portable root must win over the SDK's legacy format fallback.
        legacy = directory / ".claude-plugin/plugin.json"
        legacy.parent.mkdir()
        legacy.write_text(json.dumps({"name": "legacy-decoy", "version": "9.9.9"}))


def write_dataset(root):
    """JSON is a YAML subset; use one local dataset with exact file-backed assets."""
    resources = []
    for key, later in (("original-plugin", False), ("later-plugin", True)):
        for name, body in files(later=later).items():
            path = root / key / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_bytes(body)
        resources.append(
            dict(
                key=key,
                kind="plugin",
                identifier="urn:air:catalog.example.com:plugins:" + key,
                name=key,
                description="Owned Plugin qualification",
                namespace="plugins",
                publisher="owned",
                origin="synthetic",
                version="1.0.1" if later else "1.0.0",
                state="published",
                representation=dict(mediaType=MEDIA, packageDirectory=key),
                listing=dict(
                    license="Apache-2.0",
                    visibility="public",
                    tags=[TAG],
                    representativeQueries=["Find owned Plugin", "Find portable Plugin"],
                ),
            )
        )
    dataset = dict(
        format=1,
        dataset="plugin-qualification",
        revision="1",
        namespaces=[dict(key="plugins", displayName="Owned Plugins")],
        publishers=[
            dict(
                key="owned",
                displayName="Owned publisher",
                domains=["catalog.example.com"],
            )
        ],
        profiles={"qualification": [r["key"] for r in resources]},
        resources=resources,
    )
    (root / "catalog.yaml").write_text(json.dumps(dataset, indent=2) + "\n")
