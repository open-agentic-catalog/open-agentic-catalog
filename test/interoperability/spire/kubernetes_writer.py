"""Publish JWT-SVIDs into the qualification's one pre-created Kubernetes Secret."""
import base64
import http.client
import json
from pathlib import Path
import ssl
import subprocess
import sys
import time
import uuid

import renew

ACCESS = Path("/kube-api")
SECRET = "/api/v1/namespaces/open-agentic-catalog-e2e-peer/secrets/catalog-a-reader"


def update(secret, token, now):
    expiry = renew.lifetime(token, now)
    data = secret.get("data", {})
    encoded = base64.b64encode(token.encode()).decode()
    fields = {name: None for name in data if (match := renew.LEAF.fullmatch(name)) and int(match[1]) + 30 < now}
    activate = True
    if renew.BINDING in data:
        active = json.loads(base64.b64decode(data[renew.BINDING], validate=True))
        leaf = active.get("secretFile") if isinstance(active, dict) else None
        match = renew.LEAF.fullmatch(leaf) if isinstance(leaf, str) else None
        if not match:
            raise ValueError("Invalid active binding")
        activate = expiry >= int(match[1]) and (
            active != renew.binding(leaf.removesuffix(".token"), expiry) or data.get(leaf) != encoded)
    if activate:
        revision = renew.PREFIX + str(expiry) + "-" + uuid.uuid4().hex
        metadata = json.dumps(renew.binding(revision, expiry)).encode()
        fields.update({renew.BINDING: base64.b64encode(metadata).decode(), revision + ".token": encoded})
    if not fields:
        return None
    return {"metadata": {"resourceVersion": secret["metadata"]["resourceVersion"]}, "data": fields}


def refresh():
    client = http.client.HTTPSConnection("kubernetes.default.svc", timeout=8,
                                        context=ssl.create_default_context(cafile=str(ACCESS / "ca.crt")))
    headers = {"Authorization": "Bearer " + (ACCESS / "token").read_text().strip()}
    try:
        client.request("GET", SECRET, headers=headers)
        response = client.getresponse()
        if response.status != 200:
            raise ValueError("Secret read refused")
        patch = update(json.loads(response.read()), renew.acquire(), time.time())
        if patch:
            client.request("PATCH", SECRET, json.dumps(patch), headers | {"Content-Type": "application/merge-patch+json"})
            if client.getresponse().status != 200:
                raise ValueError("Secret update refused")
    finally:
        client.close()


if __name__ == "__main__":
    while True:
        try:
            refresh()
        except (OSError, ValueError, KeyError, http.client.HTTPException, subprocess.SubprocessError):
            print("Source binding refresh failed", file=sys.stderr, flush=True)
        time.sleep(3)
