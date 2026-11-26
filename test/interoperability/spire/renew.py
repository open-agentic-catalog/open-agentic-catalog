"""Deployment example: project one attested JWT-SVID into Catalog B's read-only source binding."""
import base64
from datetime import datetime, timezone
import fcntl
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import time
import uuid

DIRECTORY = Path("/credentials")
BINDING = "catalog-a-reader.json"
SUBJECT = "spiffe://workloads.test/source-reader"
AUDIENCE = "open-agentic-catalog"
ISSUER = "https://identity.test:8447"
ORIGIN = "https://catalog-a.test:8443"
TENANT = "00000000-0000-7000-8000-000000000002"
PREFIX = BINDING.removesuffix(".json") + "-"
LEAF = re.compile(re.escape(PREFIX) + r"([0-9]{1,12})-[a-f0-9]{32}\.token")


def acquire():
    result = subprocess.run(["/opt/spire-agent", "api", "fetch", "jwt", "-socketPath", "/workload/api.sock",
                             "-spiffeID", SUBJECT, "-audience", AUDIENCE, "-output", "json"],
                            capture_output=True, check=False, timeout=8)
    if result.returncode:
        raise ValueError("Workload API unavailable")
    replies = json.loads(result.stdout)
    if isinstance(replies, list) and all(isinstance(reply, dict) for reply in replies):
        # The Agent also emits a separate JWT bundle response. Only SVID responses carry credentials.
        match [reply["svids"] for reply in replies if "svids" in reply]:
            case [[{"spiffe_id": identity, "svid": str(value)}]] if identity == SUBJECT:
                return value
    raise ValueError("Unexpected workload identity")


def lifetime(token, now):
    # The acquisition channel is the registered workload's local Agent. A verifies the signature.
    if not isinstance(token, str) or len(token) > 16384 or not re.fullmatch(r"[\w-]+\.[\w-]+\.[\w-]+", token, re.ASCII):
        raise ValueError("Invalid compact credential")
    payload = token.split(".")[1]
    claims = json.loads(base64.urlsafe_b64decode(payload + "=" * (-len(payload) % 4)))
    if not isinstance(claims, dict):
        raise ValueError("Unexpected workload claims")
    expiry = claims.get("exp")
    if (claims.get("iss") != ISSUER or claims.get("sub") != SUBJECT or claims.get("aud") not in (AUDIENCE, [AUDIENCE])
            or type(expiry) is not int or not now + 3 < expiry <= 253402300799):
        raise ValueError("Unexpected workload claims or insufficient lifetime")
    return expiry


def binding(revision, expiry):
    return {"schemaVersion": 1, "tenantId": TENANT, "origin": ORIGIN, "type": "bearer",
            "revision": revision, "expiresAt": datetime.fromtimestamp(expiry, timezone.utc).isoformat(),
            "secretFile": revision + ".token"}


def refresh(directory=DIRECTORY, fetch=acquire, clock=time.time):
    """Serialize writers, stage a complete leaf, then commit with one rename on the same filesystem."""
    fd = os.open(directory, os.O_RDONLY | os.O_DIRECTORY)
    try:
        fcntl.flock(fd, fcntl.LOCK_EX)
        active = directory / BINDING
        current = ""
        active_leaf = None
        if active.exists():
            metadata = json.loads(active.read_bytes())
            current = metadata.get("secretFile") if isinstance(metadata, dict) else None
            active_leaf = LEAF.fullmatch(current) if isinstance(current, str) else None
            if active_leaf is None:
                raise ValueError("Unexpected active credential filename")
        for path in directory.iterdir():
            match = LEAF.fullmatch(path.name)
            if match and path.name != current and int(match[1]) + 30 < clock():
                path.unlink()
        # A single fixed temporary metadata path belongs only to this locked writer.
        staged = directory / ".catalog-a-reader.next"
        staged.unlink(missing_ok=True)
        token = fetch()
        expiry = lifetime(token, clock())
        if active_leaf:
            if expiry < int(active_leaf[1]):
                return None
            if (metadata == binding(current.removesuffix(".token"), expiry) and (directory / current).is_file()
                    and (directory / current).read_bytes() == token.encode()):
                return None
        revision = PREFIX + str(expiry) + "-" + uuid.uuid4().hex
        leaf = directory / (revision + ".token")
        metadata = binding(revision, expiry)
        for path, data in ((leaf, token.encode()), (staged, json.dumps(metadata).encode())):
            with path.open("xb") as stream:
                stream.write(data)
                stream.flush()
                os.fchmod(stream.fileno(), 0o440)
                os.fsync(stream.fileno())
        os.fsync(fd)
        os.replace(staged, active)
        os.fsync(fd)
        return revision
    finally:
        os.close(fd)


def main():
    os.umask(0o077)
    while True:
        try:
            revision = refresh()
            if revision:
                print("Activated " + revision, flush=True)
        except (OSError, ValueError, LookupError, subprocess.SubprocessError):
            print("Credential refresh failed; check the Agent and binding directory", file=sys.stderr, flush=True)
        time.sleep(2)


if __name__ == "__main__":
    main()
