"""Capture public signed evidence from unchanged, owned ANS V1/V2 producers."""

import argparse
import base64
from datetime import datetime, timezone
import hashlib
import json
from pathlib import Path
import tempfile
import time

from native import Native, csr, seed_definition


def entry_bytes(raw):
    """Preserve the selected native entry's original JSON bytes."""
    text = raw.decode("utf-8")
    start = text.index('"results"') + len('"results"')
    start = text.index("[", start) + 1
    start += len(text[start:]) - len(text[start:].lstrip())
    entry, end = json.JSONDecoder().raw_decode(text, start)
    if not isinstance(entry, dict) or json.loads(text)["results"] != [entry]:
        raise ValueError("capture requires exactly one native entry")
    return text[start:end].encode("utf-8")


def capture(api, settings):
    directory = Path(settings["output"])
    rows = []
    for version in ("V1", "V2"):
        definition = seed_definition("trust-" + version.lower(), "MCP")
        base = "/v1/agents" if version == "V1" else "/v2/ans/agents"
        with tempfile.TemporaryDirectory(prefix="csr-", dir=directory) as temporary:
            body = {**definition, "identityCsrPEM": csr(definition["agentHost"], Path(temporary), "identity"),
                    "serverCsrPEM": csr(definition["agentHost"], Path(temporary), "server")}
            pending = api.document("ra", base + ("/register" if version == "V1" else ""), body, auth=True, expected=202)
        agent = pending["agentId"]
        api.document("ra", base + "/" + agent + "/verify-acme", {}, auth=True, expected=202)
        api.document("ra", base + "/" + agent + "/verify-dns", {}, auth=True, expected=202)
        rows.append((version, definition, agent))
    deadline = time.monotonic() + 90
    while True:
        feed = api.document("ra", "/v1/agents/events?limit=100")
        if {row[2] for row in rows}.issubset({item["agentId"] for item in feed["items"]}):
            break
        if time.monotonic() >= deadline:
            raise TimeoutError("native ANS feed did not acknowledge registrations")
        time.sleep(0.5)
    # Wait for the native checkpoint, then capture exact public bytes.
    while True:
        checkpoint = api.document("tl", "/v1/log/checkpoint")
        if checkpoint["logSize"] >= 2:
            break
        if time.monotonic() >= deadline:
            raise TimeoutError("native ANS checkpoint did not cover registrations")
        time.sleep(0.5)
    reports = []
    for version, definition, agent in rows:
        while True:
            raw = api.request("finder", "/v1/search", {"query": {"text": definition["agentDisplayName"]}, "federation": "none", "pageSize": 1})
            if len(json.loads(raw)["results"]) == 1:
                break
            if time.monotonic() >= deadline:
                raise TimeoutError("Finder did not project captured registration")
            time.sleep(0.5)
        entry = entry_bytes(raw)
        prefix = "/v1/agents/" + agent
        receipt = api.request("tl", prefix + "/receipt")
        note = api.request("tl", "/checkpoint")
        history = api.request("tl", "/v1/log/checkpoint/history?fromSize=2&toSize=2&limit=1")
        token = api.request("tl", prefix + "/status-token")
        bundle = {"profile": "ans-registration-v1", **{key: base64.b64encode(value).decode("ascii") for key, value in
                  (("entry", entry), ("receipt", receipt), ("checkpoint", note), ("checkpointHistory", history), ("statusToken", token))}}
        name = version.lower() + ".json"
        encoded = (json.dumps(bundle, indent=2) + "\n").encode()
        (directory / name).write_bytes(encoded)
        reports.append({"file": name, "schemaVersion": version, "agentId": agent, "sha256": hashlib.sha256(encoded).hexdigest(),
                        "expected": {"signatures": "valid", "inclusion": "valid", "target": "valid", "status": "ACTIVE"}})
    policy = {"profile": "ans-registration-v1", "registrationAuthority": "ans-ra-local", "logOrigin": "owned-ans",
              "evidenceOrigin": settings["tl_origin"], "publisherDomains": [row[1]["agentHost"] for row in rows],
              "registrationKeys": [{"id": "ans-ra-signer", "file": "ra.pem"}], "logKeyFiles": ["tl.pem"]}
    (directory / "policy.json").write_text(json.dumps(policy, indent=2) + "\n")
    manifest = {"status": "captured", "capturedAt": datetime.now(timezone.utc).isoformat(), "inputs": settings["inputs"],
                "fixtures": reports, "requests": api.captures, "scope": "synthetic local registration; no public domain-ownership proof"}
    (directory / "provenance.json").write_text(json.dumps(manifest, indent=2) + "\n")
    return manifest


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--settings", required=True)
    arguments = parser.parse_args()
    configuration = json.loads(Path(arguments.settings).read_text())
    print(json.dumps(capture(Native(configuration), configuration)))
