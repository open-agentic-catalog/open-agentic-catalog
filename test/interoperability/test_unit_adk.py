"""Isolated ADK report, provenance and ownership checks; no native SDK imports."""

import copy
from contextlib import redirect_stdout
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import AsyncMock, patch

import adk_contract as contract
import adk_runner
import adk_tools
from adk import client as adk_client
from common import Result


def native_result():
    """Construct the documented complete child result independently of its driver."""
    return {"adk": "2.13.0", "mcp": "2.3.0", "cases": dict.fromkeys(contract.CASES, True),
            "closed": True, "sameToolsetRevocation": True, "phase": "complete",
            "schemas": {"server": {name: {"inputSchema": {"type": "object"}} for name in contract.TOOLS},
                        "adk": {name: {"parameters": {"type": "OBJECT"}} for name in contract.TOOLS}}}


class ADKContractTests(unittest.TestCase):
    """Reject false success, lost metadata and unobserved protocol controls."""

    def test_native_failure_projection_discloses_only_the_fixed_calibration_code(self):
        """Useful mismatch diagnostics must not expose arbitrary native error text."""
        for message, expected in (("native Search display changed", "search-display"), ("PRIVATE_SENTINEL", None)):
            with tempfile.TemporaryDirectory() as temporary, patch.object(adk_client, "PROGRESS", Path(temporary)), \
                    patch.object(adk_client, "run", new=AsyncMock(side_effect=AssertionError(message))):
                output = io.StringIO()
                with redirect_stdout(output):
                    self.assertEqual(adk_client.main(), 1)
                value = json.loads(output.getvalue())
                self.assertEqual(value.get("failureCheck"), expected)
                self.assertEqual(value["failure"], "AssertionError")
                self.assertNotIn("PRIVATE_SENTINEL", output.getvalue())

    def test_loopback_admission_distinguishes_disabled_tunnels_from_egress(self):
        """A DOWN kernel IPIP device is harmless; an UP external device is refused."""
        contract.check_network({"lo": 9})
        contract.check_network({"lo": 9, "tunl0": 128})
        for interfaces in ({}, {"lo": 8}, {"lo": 9, "eth0": 4099}, {"lo": 9, "tunl0": 129}, {"lo": 9, "eth0": True}):
            with self.assertRaises(AssertionError):
                contract.check_network(interfaces)

    def test_complete_native_report_and_failure_controls(self):
        """Require exact versions, every real case, lifecycle and process success."""
        versions = {"adk": "2.13.0", "mcp": "2.3.0"}
        value = native_result()
        self.assertEqual(contract.validate_result(Result(0, json.dumps(value), ""), ["PRIVATE_SENTINEL"], versions), value)
        mutations = [lambda v: v["cases"].pop("revoked-access"), lambda v: v["cases"].update(inventory=False),
                     lambda v: v["cases"].update(inventory=1), lambda v: v.update(closed=False),
                     lambda v: v.update(sameToolsetRevocation=False), lambda v: v.update(adk="0"),
                     lambda v: v.update(mcp="0"), lambda v: v.update(phase="setup"), lambda v: v.update(extra=True),
                     lambda v: v["schemas"].pop("server"), lambda v: v["schemas"]["adk"].pop("catalog_control_search"),
                     lambda v: v["schemas"]["server"].update(catalog_control_release={})]
        for mutate in mutations:
            changed = copy.deepcopy(value)
            mutate(changed)
            with self.subTest(changed=changed), self.assertRaises(AssertionError):
                contract.validate_result(Result(0, json.dumps(changed), ""), [], versions)
        for result in (Result(1, json.dumps(value), ""), Result(0, json.dumps(value), "PRIVATE_SENTINEL")):
            with self.assertRaises(AssertionError):
                contract.validate_result(result, ["PRIVATE_SENTINEL"], versions)

    def test_native_result_envelope_and_refusal_remain_distinct(self):
        """Empty successful data cannot stand in for a refused or changed result."""
        data = {"meta": {"requestId": "owned"}, "data": {"items": []}}
        result = {"content": [{"type": "text", "text": json.dumps(data)}], "structuredContent": data}
        self.assertEqual(contract.envelope(result), {"items": []})
        refused = {"isError": True, "content": [{"type": "text", "text": "api_not_found: Resource unavailable"}]}
        contract.refusal(refused, "api_not_found")
        for value in ({**result, "isError": True}, {**result, "structuredContent": {"data": "changed"}}, {**result, "error": "refused"}):
            with self.assertRaises(AssertionError):
                contract.envelope(value)
        for value in (result, {**refused, "structuredContent": {}}, {**refused, "content": [{"type": "text", "text": "other: failure"}]}):
            with self.assertRaises(AssertionError):
                contract.refusal(value, "api_not_found")

    def test_exact_release_retains_identity_metadata_and_content_references(self):
        """Bind independent API evidence and reject fabricated or executable content."""
        record = {name: {"id": name + "-id"} for name in ("resource", "version", "versionRevision", "listing", "listingRevision", "release", "evidence")}
        record["listingRevision"]["displayName"] = "Owned immutable listing"
        record["versionRevision"]["contentSetDigest"] = "sha256:" + "b"*64
        detail = copy.deepcopy(record)
        ref = {"id": "representation", "role": "primary", "mediaType": "application/json", "profileId": "owned-profile", "contentDigest": "sha256:" + "a"*64, "size": 27, "url": "https://example.invalid/metadata"}
        record["versionRevision"]["representations"] = [{**ref, "data": {"never": "disclosed"}}]
        detail["representations"] = [ref]
        contract.check_release(detail, record)
        for section, field, value in (("release", "id", "different"), ("listingRevision", "displayName", "different"), ("versionRevision", "contentSetDigest", "sha256:" + "c"*64)):
            changed = copy.deepcopy(detail)
            changed[section][field] = value
            with self.assertRaises(AssertionError):
                contract.check_release(changed, record)
        for update in ({"data": {}}, {"url": "https://elsewhere.invalid"}, {"contentDigest": "invalid"}, {"size": 28}):
            changed = copy.deepcopy(detail)
            changed["representations"][0].update(update)
            with self.assertRaises(AssertionError):
                contract.check_release(changed, record)

    def test_traffic_requires_real_negotiation_authentication_and_zero_resource_calls(self):
        """A green child report cannot substitute for independently observed HTTP."""
        value = {"requests": 25, "unexpected": 0, "sampling": False, "offered": ["2026-07-28"], "negotiated": ["2025-11-25"],
                 "methods": {"initialize": 6, "tools/list": 4}, "statuses": {"401": 2},
                 "tools": {"catalog_control_capabilities": 1, "catalog_control_access": 1, "catalog_control_search": 5,
                           "catalog_control_release": 4, "catalog_control_explore": 1}}
        contract.validate_traffic(value)
        for update in ({"unexpected": 1}, {"sampling": True}, {"requests": 0}, {"negotiated": []}, {"negotiated": ["other"]}, {"offered": ["other"]}, {"statuses": {}}, {"methods": {}}, {"tools": {}},
                       {"tools": {**value["tools"], "catalog_control_search": 3}}, {"tools": {**value["tools"], "catalog_control_access": True}}):
            with self.assertRaises(AssertionError):
                contract.validate_traffic({**value, **update})


class ADKOwnershipTests(unittest.TestCase):
    """Exercise ownership and installed-source admission through injected boundaries."""

    def test_native_checkpoints_preserve_partial_outcomes_without_private_inputs(self):
        """Completion, failure and interruption survive cleanup without native imports."""
        for failed in (False, True):
            with self.subTest(failed=failed), tempfile.TemporaryDirectory() as temporary:
                private = Path(temporary) / "private"
                progress = private / "progress"
                progress.mkdir(parents=True)
                value = {"phase": "known-search", "cases": {"inventory": True, "schemas": True},
                         "credential": "PRIVATE_SENTINEL", "schemas": {"private": "PRIVATE_SENTINEL"}}
                if failed:
                    value["failure"] = "AssertionError"
                with patch.object(adk_client, "PROGRESS", progress):
                    adk_client.checkpoint(value)
                    raw = (progress / "cases.json").read_text()
                    self.assertNotIn("PRIVATE_SENTINEL", raw)
                    self.assertFalse((progress / "cases.tmp").exists())
                    report = {"cleanup": False}
                    with patch.object(adk_runner.tools, "remove"):
                        adk_runner.cleanup([], private, report)
                self.assertTrue(report["cleanup"])
                self.assertFalse(private.exists())
                self.assertEqual(report["nativePhase"], "known-search")
                self.assertEqual(report["caseOutcomes"]["inventory"], "passed")
                self.assertEqual(report["caseOutcomes"]["schemas"], "passed")
                self.assertEqual(report["caseOutcomes"]["known-search"], "failed" if failed else "incomplete")
                self.assertEqual(report["caseOutcomes"]["exact-release"], "unrun")
                self.assertEqual(set(report["caseOutcomes"]), contract.CASES)

    def test_corrupt_or_oversized_progress_cannot_qualify_cleanup(self):
        """Untrusted progress must not turn a failed or interrupted run green."""
        for raw in ('{"phase":"PRIVATE_SENTINEL","cases":{}}', 'x'*8193):
            with tempfile.TemporaryDirectory() as temporary:
                private = Path(temporary) / "private"
                progress = private / "progress"
                progress.mkdir(parents=True)
                (progress / "cases.json").write_text(raw)
                report = {"cleanup": False}
                with patch.object(adk_runner.tools, "remove"), self.assertRaises(AssertionError):
                    adk_runner.cleanup([], private, report)
                self.assertFalse(report["cleanup"])
                self.assertFalse(private.exists())

    def test_unsupported_host_fails_before_native_tools_or_binary_execution(self):
        """Certificate setup cannot execute a Linux fixture on a different host."""
        for system, machine in (("Darwin", "x86_64"), ("Linux", "aarch64"), ("Linux", "unknown")):
            with self.subTest(system=system, machine=machine), patch.object(adk_runner, "local_daemon", return_value="amd64"), \
                    patch.object(adk_runner.platform, "system", return_value=system), patch.object(adk_runner.platform, "machine", return_value=machine), \
                    patch.object(adk_tools, "installed") as installed, self.assertRaisesRegex(RuntimeError, "native matching Linux"):
                adk_runner.check()
            installed.assert_not_called()

    def test_missing_installation_fails_offline_with_acquisition_instruction(self):
        """Normal execution never silently installs or contacts an image registry."""
        with tempfile.TemporaryDirectory() as temporary, patch.object(adk_tools, "STAMP", Path(temporary) / "missing.json"), \
                patch.object(adk_tools, "docker") as docker, self.assertRaisesRegex(RuntimeError, "Run make adk-tools-install first"):
            adk_tools.installed()
        docker.assert_not_called()

    def test_container_limits_clear_proxies_without_ram_or_ambient_credentials(self):
        """Only the caller's named container receives bounded execution settings."""
        with patch.object(adk_tools.os, "getuid", return_value=1000), patch.object(adk_tools.os, "getgid", return_value=1000):
            args = adk_tools.safe_args("owned-adk")
        self.assertEqual(args[:2], ["--name", "owned-adk"])
        for value in ("--pull=never", "--read-only", "--cap-drop=ALL", "--memory=512m", "HTTP_PROXY=", "HTTPS_PROXY="):
            self.assertIn(value, args)
        self.assertFalse(any("tmpfs" in s or "docker.sock" in s for s in args))
        with patch.object(adk_tools.os, "getuid", return_value=0), self.assertRaises(RuntimeError):
            adk_tools.safe_args("owned-adk")

    def test_native_inventory_rejects_source_and_version_drift(self):
        """Version labels alone do not establish unchanged native adapter code."""
        expected = {"adk": {"version": "2.13.0", "source_files": {"owned.py": "hash"}}, "mcp": {"version": "2.3.0"}}
        inventory = {"source_files": {"owned.py": "hash"}, "packages": [{"name": "google-adk", "version": "2.13.0"}, {"name": "mcp", "version": "2.3.0"}, {"name": "mcp-types", "version": "2.3.0"}]}
        adk_tools.verify_inventory(inventory, expected)
        for value in ({**inventory, "source_files": {"owned.py": "changed"}}, {**inventory, "packages": [{"name": "google-adk", "version": "0"}]}):
            with self.assertRaises((AssertionError, KeyError)):
                adk_tools.verify_inventory(value, expected)

    def test_cleanup_requires_independent_absence_and_rejects_uncertain_creation(self):
        """Only the owned directory and exact names participate in cleanup."""
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary).resolve()
            sentinel = root / "unrelated"
            sentinel.write_text("retained")
            for residue, uncertain in ((False, False), (False, True), (True, False)):
                private = root / "private"
                private.mkdir()
                report = {"cleanup": False, "creationUnconfirmed": uncertain}
                with patch.object(adk_runner.tools, "remove") as remove, patch.object(adk_runner, "docker", return_value=Result(0, "remaining" if residue else "", "")) as docker:
                    if residue:
                        with self.assertRaises(AssertionError):
                            adk_runner.cleanup(["owned-adk"], private, report)
                    else:
                        adk_runner.cleanup(["owned-adk"], private, report)
                    remove.assert_called_once_with(["owned-adk"])
                    self.assertIn("name=^/owned-adk$", docker.call_args.args)
                self.assertEqual(report["cleanup"], not residue and not uncertain)
                self.assertFalse(private.exists())
                self.assertEqual(sentinel.read_text(), "retained")


if __name__ == "__main__":
    unittest.main()
