"""Verify bundled publisher inputs; acquire only the explicitly selected runner image."""

from collections import Counter
import hashlib
import json
from pathlib import Path

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[1]
INPUTS = HERE / "agentfinder"
PROVENANCE = json.loads((INPUTS / "provenance.json").read_text())
REVISION = PROVENANCE["revision"]
EXPECTED_SHA256 = PROVENANCE["artifact_sha256"]
EXPECTED_SIZE = PROVENANCE["artifact_size"]
TOTAL_ENTRIES = PROVENANCE["inventory"]["total_entries"]
EXPECTED_TYPES = PROVENANCE["inventory"]["types"]


def unique_object(pairs):
    """Reject ambiguous JSON before a dictionary could hide a repeated member."""
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValueError("duplicate JSON member: " + key)
        result[key] = value
    return result


def inventory(document):
    """Validate publisher identities/version presence independently of Catalog output."""
    if not isinstance(document, dict) or document.get("specVersion") != "1.0":
        raise ValueError("unexpected publication envelope")
    entries = document.get("entries")
    if not isinstance(entries, list) or len(entries) != TOTAL_ENTRIES:
        raise ValueError("entry count mismatch")
    seen, counts = set(), Counter()
    for entry in entries:
        if not isinstance(entry, dict):
            raise ValueError("entry must be an object")
        identifier, media = entry.get("identifier"), entry.get("type")
        if not isinstance(identifier, str) or not identifier.startswith("urn:air:") or media not in EXPECTED_TYPES:
            raise ValueError("invalid publisher identity or media type")
        present = "version" in entry
        version = entry.get("version")
        if present != EXPECTED_TYPES[media]["version_present"] or present and (not isinstance(version, str) or not version.strip()):
            raise ValueError("version presence or value changed")
        identity = (identifier, present, version)
        if identity in seen:
            raise ValueError("duplicate identifier/version identity")
        seen.add(identity)
        if not isinstance(entry.get("url"), str) or not entry["url"].startswith("https://") or "data" in entry:
            raise ValueError("expected URL-backed publisher entry")
        counts[media] += 1
    if counts != {media: spec["count"] for media, spec in EXPECTED_TYPES.items()}:
        raise ValueError("media type counts changed")
    return entries


def verify_artifact_bytes(raw):
    """Require the selected immutable bytes before inventory interpretation."""
    if len(raw) != EXPECTED_SIZE:
        raise ValueError("artifact size mismatch")
    digest = hashlib.sha256(raw).hexdigest()
    if digest != EXPECTED_SHA256:
        raise ValueError("artifact SHA-256 mismatch")
    document = json.loads(raw.decode("utf-8"), object_pairs_hook=unique_object)
    entries = inventory(document)
    return {"digest": digest, "size": len(raw), "total_entries": len(entries),
            "unique_identities": len(entries), "type_counts": dict(Counter(e["type"] for e in entries))}


def installed():
    """Bundled inputs are prerequisites, never silently downloaded or repaired."""
    for name in ("ai-catalog.json", "LICENSE"):
        path = INPUTS / name
        if not path.is_file():
            raise RuntimeError("missing bundled Agent Finder input: " + name)
        if hashlib.sha256(path.read_bytes()).hexdigest() != PROVENANCE["files"][name]:
            raise ValueError("bundled Agent Finder digest mismatch: " + name)
    return verify_artifact_bytes((INPUTS / "ai-catalog.json").read_bytes())


def runner_image():
    """Reuse the interoperability toolchain's digest-pinned standard Python base."""
    source = (HERE / "Containerfile").read_text()
    return next(line.split()[1] for line in source.splitlines() if line.startswith("FROM python:"))


def install():
    """Verify bundled inputs and explicitly acquire the pinned runner when absent."""
    from runner import docker, local_daemon
    result = installed()
    local_daemon()
    image = runner_image()
    if docker("image", "inspect", image, check=False).code:
        docker("pull", image, timeout=300)
    print(f"Verified bundled Agent Finder publication: {result['total_entries']} entries; pinned Python runner image ready.")


if __name__ == "__main__":
    install()
