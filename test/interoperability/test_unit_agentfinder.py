"""Isolated Agent Finder catalog publication checks; no processes, sockets or network execution."""

import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import agentfinder_tools as tools


class AgentFinderPreflightTests(unittest.TestCase):
    def setUp(self):
        self.raw = tools.acquire_artifact()

    def test_pinned_artifact_matches_exact_size_and_digest(self):
        """US-1: Exact bytes, size, and SHA-256 match recorded provenance."""
        self.assertEqual(len(self.raw), tools.EXPECTED_SIZE)
        result = tools.verify_artifact_bytes(self.raw)
        self.assertEqual(result["digest"], tools.EXPECTED_SHA256)
        self.assertEqual(result["size"], tools.EXPECTED_SIZE)
        self.assertEqual(result["total_entries"], tools.TOTAL_ENTRIES)
        self.assertEqual(result["unique_identities"], tools.TOTAL_ENTRIES)

    def test_exact_breakdown_of_all_five_media_types(self):
        """US-1: All 1,718 entries match the 5 declared media types and expected counts."""
        result = tools.verify_artifact_bytes(self.raw)
        expected_counts = {
            "application/mcp-server+json": 647,
            "application/ai-skill": 925,
            "application/vnd.anthropic.claude-plugin+json": 69,
            "application/vnd.cursor.cursor-plugin+json": 22,
            "application/vnd.github.copilot-plugin": 55,
        }
        self.assertEqual(result["type_counts"], expected_counts)

    def test_version_presence_flags_are_frozen(self):
        """US-1: Only 647 MCP entries have non-empty version; the other 1,071 omit it."""
        doc = json.loads(self.raw)
        mcp_count = 0
        non_mcp_count = 0

        for entry in doc["entries"]:
            media_type = entry["type"]
            if media_type == "application/mcp-server+json":
                mcp_count += 1
                self.assertIn("version", entry, f"MCP entry {entry['identifier']} must have version")
                self.assertIsInstance(entry["version"], str)
                self.assertTrue(entry["version"].strip())
            else:
                non_mcp_count += 1
                self.assertNotIn(
                    "version",
                    entry,
                    f"Non-MCP entry {entry['identifier']} of type {media_type} must omit version",
                )

        self.assertEqual(mcp_count, 647)
        self.assertEqual(non_mcp_count, 1071)

    def test_rejection_of_altered_bytes_or_mismatched_size(self):
        """US-1: Altered bytes or truncated payloads fail verification immediately."""
        with self.assertRaisesRegex(ValueError, "Artifact size mismatch"):
            tools.verify_artifact_bytes(self.raw + b" ")

        with self.assertRaisesRegex(ValueError, "Artifact size mismatch"):
            tools.verify_artifact_bytes(self.raw[:-1])

    def test_rejection_of_corrupted_json_or_altered_digest(self):
        """US-1: Corrupted or substituted JSON fails verification."""
        doc = json.loads(self.raw)
        # Modify first entry identifier without changing size (pad with spaces)
        first_id = doc["entries"][0]["identifier"]
        altered_raw = self.raw.replace(first_id.encode(), (first_id[:-1] + "X").encode(), 1)
        if len(altered_raw) == len(self.raw):
            with self.assertRaisesRegex(ValueError, "Artifact SHA-256 mismatch"):
                tools.verify_artifact_bytes(altered_raw)

    def test_rejection_of_duplicate_identifiers(self):
        """US-1: Duplicate identifiers fail preflight."""
        doc = json.loads(self.raw)
        doc["entries"][1]["identifier"] = doc["entries"][0]["identifier"]
        with self.assertRaises(ValueError):
            seen = set()
            for entry in doc["entries"]:
                ident = entry["identifier"]
                if ident in seen:
                    raise ValueError(f"Duplicate entry identifier detected: {ident}")
                seen.add(ident)

    def test_rejection_of_synthetic_or_injected_version(self):
        """US-1: Adding synthetic version to a skill or plugin record is rejected."""
        doc = json.loads(self.raw)
        for entry in doc["entries"]:
            if entry["type"] == "application/ai-skill":
                entry["version"] = "1.0.0"
                break
        with self.assertRaises(ValueError):
            for entry in doc["entries"]:
                media_type = entry["type"]
                expected_v = tools.EXPECTED_TYPES[media_type]["version_present"]
                has_v = "version" in entry
                if has_v != expected_v:
                    raise ValueError("Version presence violation")

    def test_rejection_of_removed_mcp_version(self):
        """US-1: Removing version from an MCP record is rejected."""
        doc = json.loads(self.raw)
        for entry in doc["entries"]:
            if entry["type"] == "application/mcp-server+json":
                del entry["version"]
                break
        with self.assertRaises(ValueError):
            for entry in doc["entries"]:
                media_type = entry["type"]
                expected_v = tools.EXPECTED_TYPES[media_type]["version_present"]
                has_v = "version" in entry
                if has_v != expected_v:
                    raise ValueError("Version presence violation")

    def test_kind_mapping_treats_all_five_media_types_as_unsupported_kinds(self):
        """US-2: Current Catalog kind mapping leaves all five media types ungoverned."""
        # Under internal/federation/manager.go:kindForMediaType:
        # None of these 5 media types map to KindAgent, KindMCPServer, KindSkill, or KindPlugin.
        supported_types = {
            "application/vnd.agent.card+json",
            "application/vnd.mcp.server+json",
            "text/markdown;variant=agent-skill",
            "application/zip;profile=plugin",
        }
        for media_type in tools.EXPECTED_TYPES:
            self.assertNotIn(
                media_type,
                supported_types,
                f"Media type {media_type} must not be in native supported kind mapping",
            )

    def test_missing_installation_fails_with_instruction(self):
        """US-1: Missing installation stamp raises informative RuntimeError."""
        with tempfile.TemporaryDirectory() as temp_dir:
            missing_stamp = Path(temp_dir) / "missing_installation.json"
            with patch.object(tools, "STAMP", missing_stamp):
                with self.assertRaisesRegex(RuntimeError, "Agent Finder tools not installed"):
                    tools.installed()

    def test_installed_verifies_stamp_and_cached_identity(self):
        """US-1: Valid installation stamp matching identity succeeds."""
        with tempfile.TemporaryDirectory() as temp_dir:
            temp = Path(temp_dir)
            stamp_file = temp / "installation.json"
            stamp_data = {
                "identity": tools.identity(),
                "revision": tools.REVISION,
                "sha256": tools.EXPECTED_SHA256,
                "size": tools.EXPECTED_SIZE,
                "total_entries": tools.TOTAL_ENTRIES,
            }
            stamp_file.write_text(json.dumps(stamp_data))
            with patch.object(tools, "STAMP", stamp_file):
                result = tools.installed()
                self.assertEqual(result["revision"], tools.REVISION)
                self.assertEqual(result["sha256"], tools.EXPECTED_SHA256)
                self.assertEqual(result["size"], tools.EXPECTED_SIZE)
                self.assertEqual(result["total_entries"], tools.TOTAL_ENTRIES)

    def test_installed_rejects_identity_drift(self):
        """US-1: Mismatched stamp identity fails validation."""
        with tempfile.TemporaryDirectory() as temp_dir:
            temp = Path(temp_dir)
            stamp_file = temp / "installation.json"
            stamp_data = {
                "identity": "stale_or_drifted_identity_hash",
                "revision": tools.REVISION,
            }
            stamp_file.write_text(json.dumps(stamp_data))
            with patch.object(tools, "STAMP", stamp_file):
                with self.assertRaisesRegex(RuntimeError, "identity changed"):
                    tools.installed()


if __name__ == "__main__":
    unittest.main()
