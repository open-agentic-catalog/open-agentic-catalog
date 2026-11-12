"""Explicit source-bound acquisition and preflight checks for GitHub Agent Finder Catalog."""

import hashlib
import json
from pathlib import Path
import shutil
import urllib.request

from runner import ROOT, fingerprint

HERE = Path(__file__).resolve().parent
INPUTS = HERE / "agentfinder"
OUTPUT = ROOT / "output/interoperability/agentfinder"
STAMP = OUTPUT / "installation.json"
PROVENANCE_FILE = INPUTS / "provenance.json"

PROVENANCE = json.loads(PROVENANCE_FILE.read_text())
REVISION = PROVENANCE["revision"]
EXPECTED_SHA256 = PROVENANCE["artifact_sha256"]
EXPECTED_SIZE = PROVENANCE["artifact_size"]
TOTAL_ENTRIES = PROVENANCE["inventory"]["total_entries"]
EXPECTED_TYPES = PROVENANCE["inventory"]["types"]

RAW_ARTIFACT_URL = f"https://raw.githubusercontent.com/{PROVENANCE['repository']}/{REVISION}/ai-catalog.json"
LICENSE_URL = f"https://raw.githubusercontent.com/{PROVENANCE['repository']}/{REVISION}/LICENSE"


def identity():
    """Invalidate installation when inputs or tools change."""
    return fingerprint(
        [
            INPUTS / "provenance.json",
            INPUTS / "LICENSE",
            INPUTS / "README.md",
            HERE / "agentfinder_tools.py",
            ROOT / "go.mod",
            ROOT / "go.sum",
        ]
    )


def verify_artifact_bytes(raw: bytes):
    """Verify exact bytes, SHA-256 digest, entry identities, type counts, and version presence."""
    if len(raw) != EXPECTED_SIZE:
        raise ValueError(
            f"Artifact size mismatch: expected {EXPECTED_SIZE} bytes, got {len(raw)}"
        )

    digest = hashlib.sha256(raw).hexdigest()
    if digest != EXPECTED_SHA256:
        raise ValueError(
            f"Artifact SHA-256 mismatch: expected {EXPECTED_SHA256}, got {digest}"
        )

    try:
        document = json.loads(raw)
    except json.JSONDecodeError as err:
        raise ValueError(f"Artifact is not valid JSON: {err}") from err

    if document.get("specVersion") != "1.0":
        raise ValueError(
            f"Unexpected specVersion: expected '1.0', got {document.get('specVersion')!r}"
        )

    entries = document.get("entries")
    if not isinstance(entries, list):
        raise ValueError("Document has no valid 'entries' array")

    if len(entries) != TOTAL_ENTRIES:
        raise ValueError(
            f"Entry count mismatch: expected {TOTAL_ENTRIES}, got {len(entries)}"
        )

    seen_identifiers = set()
    type_counts = {}
    version_violations = []

    for idx, entry in enumerate(entries):
        if not isinstance(entry, dict):
            raise ValueError(f"Entry at index {idx} is not an object")

        identifier = entry.get("identifier")
        if not identifier or not isinstance(identifier, str):
            raise ValueError(f"Entry at index {idx} lacks a non-empty string identifier")

        if identifier in seen_identifiers:
            raise ValueError(f"Duplicate entry identifier detected: {identifier}")
        seen_identifiers.add(identifier)

        media_type = entry.get("type")
        if not media_type or not isinstance(media_type, str):
            raise ValueError(f"Entry {identifier} lacks a non-empty string type")

        if media_type not in EXPECTED_TYPES:
            raise ValueError(
                f"Entry {identifier} has unexpected media type: {media_type!r}"
            )

        type_counts[media_type] = type_counts.get(media_type, 0) + 1
        expected_version_present = EXPECTED_TYPES[media_type]["version_present"]
        has_version = "version" in entry

        if has_version != expected_version_present:
            version_violations.append(
                f"Entry {identifier} (type {media_type}): version present={has_version}, expected={expected_version_present}"
            )
        elif has_version and (not isinstance(entry["version"], str) or not entry["version"].strip()):
            version_violations.append(
                f"Entry {identifier} has empty or non-string version: {entry.get('version')!r}"
            )

    if version_violations:
        raise ValueError(
            f"Version presence violations ({len(version_violations)}):\n"
            + "\n".join(version_violations[:5])
        )

    for media_type, spec in EXPECTED_TYPES.items():
        count = type_counts.get(media_type, 0)
        if count != spec["count"]:
            raise ValueError(
                f"Type count mismatch for {media_type}: expected {spec['count']}, got {count}"
            )

    return {
        "digest": digest,
        "size": len(raw),
        "total_entries": len(entries),
        "unique_identities": len(seen_identifiers),
        "type_counts": type_counts,
    }


def download_file(url: str, expected_sha256: str = None, timeout: int = 30) -> bytes:
    """Download a file with timeout and optional digest verification."""
    # Attempt gh if available
    gh_cli = shutil.which("gh")
    if gh_cli:
        try:
            # e.g., gh api repos/github/agentfinder-catalog/contents/ai-catalog.json?ref=...
            pass
        except Exception:
            pass

    req = urllib.request.Request(url, headers={"User-Agent": "OpenAgenticCatalog-InteropCheck/1.0"})
    with urllib.request.urlopen(req, timeout=timeout) as response:
        data = response.read(16 * 1024 * 1024)

    if expected_sha256:
        actual_sha = hashlib.sha256(data).hexdigest()
        if actual_sha != expected_sha256:
            raise ValueError(f"Downloaded file digest mismatch: expected {expected_sha256}, got {actual_sha}")
    return data


def acquire_artifact():
    """Acquire the pinned ai-catalog.json artifact and verify its integrity."""
    for path in (INPUTS / "ai-catalog.json", OUTPUT / "ai-catalog.json"):
        if path.exists():
            raw = path.read_bytes()
            try:
                verify_artifact_bytes(raw)
                return raw
            except Exception:
                pass

    # Fetch from repository
    OUTPUT.mkdir(parents=True, exist_ok=True)
    raw = download_file(RAW_ARTIFACT_URL, EXPECTED_SHA256)
    verify_artifact_bytes(raw)
    (OUTPUT / "ai-catalog.json").write_bytes(raw)
    return raw


def install():
    """Preflight check, artifact verification, and installation stamp generation."""
    OUTPUT.mkdir(parents=True, exist_ok=True)
    raw = acquire_artifact()
    stats = verify_artifact_bytes(raw)

    license_path = INPUTS / "LICENSE"
    if not license_path.exists():
        license_data = download_file(LICENSE_URL, PROVENANCE["files"]["LICENSE"])
        license_path.write_bytes(license_data)

    stamp_data = {
        "identity": identity(),
        "revision": REVISION,
        "sha256": stats["digest"],
        "size": stats["size"],
        "total_entries": stats["total_entries"],
        "type_counts": stats["type_counts"],
    }
    STAMP.write_text(json.dumps(stamp_data, indent=2) + "\n")
    print(f"Agent Finder qualification prerequisites installed successfully: {stats['total_entries']} entries verified.")


def installed():
    """Verify that installation is valid and matches the input identity."""
    if not STAMP.exists():
        raise RuntimeError("Agent Finder tools not installed; run 'make agentfinder-tools-install'")
    data = json.loads(STAMP.read_text())
    if data.get("identity") != identity():
        raise RuntimeError("Agent Finder tools identity changed; run 'make agentfinder-tools-install'")
    cached_path = INPUTS / "ai-catalog.json"
    if not cached_path.exists():
        raise RuntimeError("Agent Finder artifact missing; run 'make agentfinder-tools-install'")
    verify_artifact_bytes(cached_path.read_bytes())
    return data


if __name__ == "__main__":
    install()
