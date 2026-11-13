"""Isolated inventory and retained-evidence assertions; no acquisition or services."""

import base64
import copy
import hashlib
import json
from pathlib import Path
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from common import Result
import agentfinder_contract as contract
import agentfinder_runner as campaign
import agentfinder_tools as tools


class AgentFinderPreflightTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.raw = (tools.INPUTS / "ai-catalog.json").read_bytes()
        cls.document = json.loads(cls.raw)

    def test_exact_pinned_input_and_license(self):
        result = tools.installed()
        self.assertEqual(result["digest"], "7ca6ac36768ebffb05639084681d85334b3c8b72a8637264fb01fc43710853bb")
        self.assertEqual(result["size"], 1248214)
        self.assertEqual(result["unique_identities"], 1718)
        self.assertEqual(result["type_counts"], {"application/mcp-server+json": 647, "application/ai-skill": 925,
            "application/vnd.anthropic.claude-plugin+json": 69, "application/vnd.cursor.cursor-plugin+json": 22, "application/vnd.github.copilot-plugin": 55})

    def test_exact_byte_size_and_same_size_tamper_are_rejected(self):
        for raw in (self.raw + b" ", self.raw[:-1]):
            with self.assertRaisesRegex(ValueError, "size mismatch"):
                tools.verify_artifact_bytes(raw)
        altered = self.raw.replace(b'"specVersion": "1.0"', b'"specVersion": "2.0"', 1)
        self.assertNotEqual(altered, self.raw)
        self.assertEqual(len(altered), len(self.raw))
        with self.assertRaisesRegex(ValueError, "SHA-256 mismatch"):
            tools.verify_artifact_bytes(altered)

    def test_missing_or_corrupt_bundled_inputs_fail_without_acquisition(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            with patch.object(tools, "INPUTS", root):
                with self.assertRaisesRegex(RuntimeError, "missing bundled"):
                    tools.installed()
                (root / "ai-catalog.json").write_bytes(self.raw)
                (root / "LICENSE").write_bytes(b"changed notice")
                with self.assertRaisesRegex(ValueError, "digest mismatch: LICENSE"):
                    tools.installed()

    def test_inventory_preserves_version_absence_and_detects_actual_mutations(self):
        mutations = (
            ("duplicate", lambda d: d["entries"].__setitem__(1, copy.deepcopy(d["entries"][0]))),
            ("version", lambda d: next(e for e in d["entries"] if e["type"] == "application/ai-skill").update(version="")),
            ("version", lambda d: next(e for e in d["entries"] if e["type"] == "application/mcp-server+json").pop("version")),
            ("version", lambda d: next(e for e in d["entries"] if e["type"] == "application/mcp-server+json").update(version=None)),
            ("media", lambda d: d["entries"][0].update(type="application/unreviewed")),
            ("count", lambda d: d["entries"].pop()),
            ("URL-backed", lambda d: d["entries"][0].update(url="file:///not-a-publication")),
            ("envelope", lambda d: d.update(specVersion="2.0")),
        )
        tools.inventory(self.document)
        for message, mutate in mutations:
            with self.subTest(message=message):
                document = copy.deepcopy(self.document)
                mutate(document)
                with self.assertRaisesRegex(ValueError, message):
                    tools.inventory(document)

    def test_duplicate_json_members_reach_the_decoder(self):
        raw = b'{"specVersion":"1.0","entries":[],"entries":[]}'
        with patch.object(tools, "EXPECTED_SIZE", len(raw)), patch.object(tools, "EXPECTED_SHA256", hashlib.sha256(raw).hexdigest()):
            with self.assertRaisesRegex(ValueError, "duplicate JSON member"):
                tools.verify_artifact_bytes(raw)


class AgentFinderEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.entry = {"identifier": "urn:air:github.com:owned:skill", "type": "application/ai-skill", "url": "https://github.com/owned/SKILL.md", "metadata": {"sourceSet": "owned"}}
        self.source = "00000000-0000-7000-8000-000000000002"
        self.member = {"id": "00000000-0000-7000-8000-000000000003", "identifier": self.entry["identifier"],
            "tenantId": contract.TENANT, "sourceId": self.source, "mediaType": self.entry["type"], "versionPresent": False,
            "freshness": "current", "rawEntryAvailable": True, "compatibility": {"profileId": contract.LEGACY, "support": "unknown", "structural": "valid"}}
        self.raw = json.dumps(self.entry).encode()
        self.member["exactEntryDigest"] = contract.digest(self.raw)
        self.snapshot = {"id": "00000000-0000-7000-8000-000000000004", "sourceRevision": 1}
        self.row = {"id": self.member["id"], "entryLocation": "/entries/0", "entry": self.entry, "raw": base64.b64encode(self.raw).decode(),
            "tenantId": contract.TENANT, "sourceId": self.source, "snapshotId": self.snapshot["id"], "sourceRevision": 1,
            "retrievalDigest": "sha256:" + tools.EXPECTED_SHA256, "evidenceComplete": True, "versionPresent": False, "version": None,
            "exactEntryDigest": self.member["exactEntryDigest"]}

    def test_nonempty_membership_and_real_unsupported_disposition(self):
        with patch.object(tools, "TOTAL_ENTRIES", 1):
            contract.check_members([self.member], [self.entry], self.source)
            for field, value in (("sourceId", "another"), ("tenantId", "another"), ("identifier", "urn:air:github.com:wrong"),
                                 ("versionPresent", True), ("kind", "skill"), ("normalizedEntryDigest", "invented"),
                                 ("mediaType", "application/mcp-server+json"), ("rawEntryAvailable", False)):
                with self.subTest(field=field), self.assertRaises((AssertionError, KeyError)):
                    contract.check_members([{**self.member, field: value}], [self.entry], self.source)
            with self.assertRaisesRegex(AssertionError, "incomplete"):
                contract.check_members([], [self.entry], self.source)

    def test_raw_metadata_and_digests_are_bound(self):
        artifact = b'{"entries":[' + self.raw + b']}'
        contract.check_raw(self.raw, self.entry, self.member, artifact)
        for raw, entry, member, publication in ((self.raw, self.entry, self.member, b"wrong bytes"),
            (self.raw, {**self.entry, "metadata": {}}, self.member, artifact),
            (self.raw, self.entry, {**self.member, "exactEntryDigest": "wrong"}, artifact)):
            with self.assertRaises(AssertionError):
                contract.check_raw(raw, entry, member, publication)

    def test_every_sql_occurrence_must_bind_the_original_record(self):
        contract.check_sql_members([self.row], [self.entry], [self.member], self.source, self.snapshot)
        for field, value in (("snapshotId", self.source), ("sourceRevision", 2), ("retrievalDigest", "wrong"),
            ("entryLocation", "/entries/1"), ("entry", {}), ("id", self.source), ("evidenceComplete", False),
            ("versionPresent", True), ("version", "invented"), ("raw", base64.b64encode(b'{}').decode()),
            ("exactEntryDigest", "wrong")):
            with self.subTest(field=field), self.assertRaises((AssertionError, KeyError)):
                contract.check_sql_members([{**self.row, field: value}], [self.entry], [self.member], self.source, self.snapshot)
        with self.assertRaisesRegex(AssertionError, "incomplete SQL"):
            contract.check_sql_members([], [self.entry], [self.member], self.source, self.snapshot)

    def test_bindings_cannot_accept_empty_or_truthy_evidence(self):
        valid = dict.fromkeys(("source_bound", "inspection_bound", "connection_bound", "job_bound", "snapshot_bound", "audit_bound", "no_adoption"), True)
        contract.check_bindings(valid)
        for field in valid:
            for value in (False, None, 1):
                with self.subTest(field=field, value=value), self.assertRaises(AssertionError):
                    contract.check_bindings({**valid, field: value})
        with self.assertRaises(AssertionError):
            contract.check_bindings({})

    def test_missing_phases_failures_and_cleanup_cannot_be_reported_as_passed(self):
        report = {"phases": dict.fromkeys(contract.PHASES, "passed"), "cleanup": "complete"}
        self.assertTrue(contract.finish(report))
        for field in contract.PHASES:
            changed = copy.deepcopy(report)
            changed["phases"][field] = "unrun"
            self.assertFalse(contract.finish(changed))
        self.assertFalse(contract.finish({**report, "cleanup": "failed"}))
        self.assertFalse(contract.finish({**report, "error": "interrupted"}))

    def test_admin_pagination_requires_all_pages_and_unique_cursors(self):
        api = object.__new__(campaign.Catalog)
        api.request = Mock(side_effect=[({"items": [{"id": "one"}], "nextCursor": "next"}, {}), ({"items": [{"id": "two"}]}, {})])
        self.assertEqual(api.pages("/api/v1/observations"), [{"id": "one"}, {"id": "two"}])
        self.assertIn("cursor=next", api.request.call_args[0][0])
        api.request = Mock(return_value=({"items": [], "nextCursor": "same"}, {}))
        with self.assertRaisesRegex(AssertionError, "pagination cycle"):
            api.pages("/api/v1/observations")

    def test_optimized_python_is_rejected_before_fixture_creation(self):
        with patch.object(campaign.sys, "flags", SimpleNamespace(optimize=1)):
            with self.assertRaisesRegex(RuntimeError, "requires Python assertions"):
                campaign.run_qualification()

    def test_partial_client_reports_keep_outcomes_but_cannot_change_cleanup_ownership(self):
        report = {"project": "owned", "sourceCommit": "revision", "sourceDigest": "digest", "clientName": "owned-client", "cleanup": "unrun"}
        child = {**report, "phase": "retention", "phases": dict.fromkeys(contract.PHASES, "unrun"), "clientName": "unrelated", "cleanup": "complete"}
        child["phases"]["inspection"] = "passed"
        contract.merge_client(report, child)
        self.assertEqual(report["phases"]["inspection"], "passed")
        self.assertEqual(report["clientName"], "owned-client")
        self.assertEqual(report["cleanup"], "unrun")
        for changed in ({**child, "project": "other"}, {**child, "sourceDigest": "other"}, {**child, "phase": "invented"}, {**child, "phases": {}}):
            with self.assertRaises(AssertionError):
                contract.merge_client(report, changed)

    def test_cleanup_inventory_detects_a_stopped_owned_container(self):
        def listing(arguments, **_options):
            output = "stopped-owned-container" if arguments[1] == "container" and "--all" in arguments else ""
            return Result(0, output, "")
        with patch.object(campaign, "run", side_effect=listing) as execute:
            remaining = campaign.owned_resources("owned-project", {})
        self.assertEqual(remaining, {"container": "stopped-owned-container", "network": "", "volume": ""})
        for call in execute.call_args_list:
            self.assertIn("label=com.docker.compose.project=owned-project", call.args[0])

    def test_auxiliary_cleanup_timeout_does_not_skip_compose_or_image_cleanup(self):
        project = "owned-project"
        compose = ["docker", "compose", "-p", project]
        report = {"clientName": project + "-client"}
        calls = []
        def execute(arguments, **_options):
            calls.append(arguments)
            if arguments == ["docker", "rm", "-f", project + "-client"]:
                raise RuntimeError("synthetic auxiliary timeout")
            return Result(0, "", "")
        with tempfile.TemporaryDirectory() as name:
            private = Path(name) / "private"
            private.mkdir()
            with patch.object(campaign, "run", side_effect=execute):
                campaign.cleanup_owned(compose, {}, project, ("owned:app", "owned:fixture"), private, report)
            self.assertIn(compose + ["down", "--volumes", "--remove-orphans"], calls)
            self.assertIn(["docker", "image", "rm", "owned:fixture"], calls)
            self.assertEqual(report["cleanup"], "failed")
            self.assertIn("synthetic auxiliary timeout", report["cleanupError"])

    def test_cleanup_attempts_remaining_steps_after_teardown_or_image_failure(self):
        compose = ["docker", "compose", "-p", "owned-project"]
        down = compose + ["down", "--volumes", "--remove-orphans"]
        first_image = ["docker", "image", "rm", "owned:app"]
        for failure in (None, down, first_image):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as name:
                calls = []
                report = {}
                private = Path(name) / "private"
                private.mkdir()
                def execute(arguments, **_options):
                    calls.append(arguments)
                    if arguments == failure:
                        raise RuntimeError("synthetic cleanup failure")
                    return Result(0, "", "")
                with patch.object(campaign, "run", side_effect=execute):
                    campaign.cleanup_owned(compose, {}, "owned-project", ("owned:app", "owned:fixture"), private, report)
                self.assertIn(down, calls)
                self.assertIn(["docker", "image", "rm", "owned:fixture"], calls)
                self.assertFalse(private.exists())
                self.assertEqual(report["cleanup"], "failed" if failure else "complete")

    def test_cleanup_preserves_private_recovery_inputs_when_inventory_is_not_empty_or_known(self):
        for unavailable in (False, True):
            with self.subTest(unavailable=unavailable), tempfile.TemporaryDirectory() as name:
                private = Path(name) / "private"
                private.mkdir()
                (private / "policy.json").write_text("recovery configuration")
                report = {}
                def execute(arguments, **_options):
                    if arguments[:3] == ["docker", "container", "ls"]:
                        if unavailable:
                            raise RuntimeError("synthetic inventory failure")
                        return Result(0, "stopped-owned-container", "")
                    return Result(0, "", "")
                with patch.object(campaign, "run", side_effect=execute):
                    campaign.cleanup_owned(["docker", "compose"], {}, "owned-project", (), private, report)
                self.assertEqual(report["cleanup"], "failed")
                self.assertEqual((private / "policy.json").read_text(), "recovery configuration")

    def test_source_identity_includes_shared_transport_bootstrap_and_fixture_dependency(self):
        with tempfile.TemporaryDirectory() as name:
            root = Path(name)
            paths = tuple(root / path for path in ("test/interoperability/peer_wire.py", "test/interoperability/runner.py",
                "deployments/postgres/init.sql", "examples/webhookreceiver/verify.go", ".dockerignore", "LICENSE", "deployments/container/licenses.sh"))
            for path in paths:
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_bytes(b"original")
            with patch.object(campaign, "ROOT", root), patch.object(campaign.shared, "ROOT", root), patch.object(campaign.shared, "CATALOG_INPUTS", ()):
                baseline = campaign.source_fingerprint()
                for path in paths:
                    path.write_bytes(b"changed")
                    try:
                        self.assertNotEqual(campaign.source_fingerprint(), baseline, str(path.relative_to(root)))
                    finally:
                        path.write_bytes(b"original")

    def test_sql_ids_reject_injection_before_query_construction(self):
        self.assertEqual(contract.uuid_literal(self.source), self.source)
        for value in ("'; DELETE FROM observations; --", "", None):
            with self.assertRaises(AssertionError):
                contract.uuid_literal(value)


if __name__ == "__main__":
    unittest.main()
