"""Isolated evidence controls; no native binary, listener, process or database."""

import copy
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

import agentfield_contract as c
import agentfield_tools as tools
from common import Result
import runner as shared


class AgentFieldEvidenceTests(unittest.TestCase):
    def setUp(self):
        self.entry = {"identifier": "urn:air:example.com:fixture:mcp", "type": "application/mcp-server-card+json",
            "displayName": "Inert metadata", "description": "No invocation", "data": {"name": "mcp", "precision": 9007199254740993},
            "version": "1.0", "tags": ["metadata"], "capabilities": ["inspect"], "representativeQueries": ["Find metadata"],
            "trustManifest": {"identity": "unverified", "identityType": "test", "evidence": "not retained"},
            "extensions": {"https://example.com/evidence": {"release": "sealed"}}}
        self.wire = {"results": [{**self.entry, "source": "https://original.test/ard/v1", "score": 83}], "pageToken": "actual-next-page"}
        self.native = {"results": [{"identifier": "urn:air:example.com:fixture:mcp", "type": "application/mcp-server-card+json",
                       "displayName": "Inert metadata", "description": "No invocation",
                       "data": {"name": "mcp", "precision": 9007199254740993}, "version": "1.0", "tags": ["metadata"],
                       "capabilities": ["inspect"], "representativeQueries": ["Find metadata"],
                       "trustManifest": {"identity": "unverified", "identityType": "test"}, "score": 83, "source": c.SOURCE}],
                       "sources": [{"url": c.SOURCE, "status": "ok"}]}
        self.imported = {"id": "ext_" + hashlib.sha256(self.entry["identifier"].encode()).hexdigest()[:16],
            "identifier": self.entry["identifier"], "type": self.entry["type"], "display_name": "Inert metadata",
            "source_registry": c.SOURCE, "description": "No invocation", "data": self.entry["data"],
            "representative_queries": ["Find metadata"], "trust_summary": "unverified", "imported_at": "2026-01-01T00:00:00Z"}
        self.dashboard = {"config": {"external_invocation_enabled": False}, "state": {"bindings": {}, "imports": [self.imported]},
            "summary": {"imported_resources": 1, "callable_external_resources": 0},
            "imports": [{"entry": self.imported, "status": "imported"}]}

    def test_native_search_retains_exact_supported_fields_and_reports_loss(self):
        result = c.check_search(self.wire, self.native, c.SOURCE)
        self.assertEqual(result["omittedFields"], ["extensions"])
        self.assertTrue(result["sourceReplaced"])
        self.assertTrue(result["upstreamContinuationPresent"])
        self.assertFalse(result["nativeContinuationPresent"])
        self.assertEqual(self.native["results"][0]["data"]["precision"], 9007199254740993)

    def test_native_defaults_and_optional_omissions_match_the_pinned_struct(self):
        entry = {"identifier": "urn:air:example.com:fixture:sparse", "type": "application/example", "displayName": "Sparse"}
        response = {"results": [{**entry, "source": c.SOURCE, "score": 1}], "sources": [{"url": c.SOURCE, "status": "ok"}]}
        c.check_search({"results": [{**entry, "source": c.SOURCE, "score": 1}]}, response, c.SOURCE)
        self.assertEqual(c.native_entry({**entry, "description": "", "tags": [], "version": ""}), entry)
        self.assertEqual(c.native_entry({**entry, "trustManifest": {}}), {**entry, "trustManifest": {"identity": ""}})
        self.assertEqual(c.native_entry({**entry, "trustManifest": None}), entry)
        self.assertEqual(c.native_entry({"identifier": entry["identifier"]}), {"identifier": entry["identifier"], "type": "", "displayName": ""})

    def test_present_raw_json_values_are_retained_in_search_and_import(self):
        for data in ({}, [], False, 0, None):
            with self.subTest(data=data):
                entry = {"identifier": "urn:air:example.com:fixture:json", "type": "application/json", "displayName": "Raw", "data": data,
                         "trustManifest": None}
                expected = {"identifier": entry["identifier"], "type": "application/json", "displayName": "Raw", "data": data}
                self.assertEqual(c.native_entry(entry), expected)
                saved = c.import_fields(entry, c.SOURCE)
                self.assertIn("data", saved)
                self.assertEqual(saved["data"], data)
                self.assertNotIn("trust_summary", saved)

    def test_empty_source_errors_changed_identity_fields_and_provenance_cannot_pass(self):
        for change in ("empty", "source-error", "identity", "source", "score", "data", "duplicate", "continuation", "referral", "extension"):
            value = copy.deepcopy(self.native)
            with self.subTest(change=change):
                if change == "empty":
                    value["results"] = []
                elif change == "source-error":
                    value["sources"][0]["status"] = "error"
                elif change in ("identity", "source", "score", "data"):
                    value["results"][0]["identifier" if change == "identity" else change] = "changed"
                elif change == "duplicate":
                    value["results"].append(copy.deepcopy(value["results"][0]))
                elif change in ("continuation", "referral"):
                    value["pageToken" if change == "continuation" else "referrals"] = "changed"
                else:
                    value["results"][0]["extensions"] = self.entry["extensions"]
                with self.assertRaises(AssertionError):
                    c.check_search(self.wire, value, c.SOURCE)
        with self.assertRaisesRegex(AssertionError, "empty upstream"):
            c.check_search({"results": []}, {"results": [], "sources": self.native["sources"]}, c.SOURCE)

    def test_persisted_subset_is_independent_of_returned_dashboard(self):
        self.assertEqual(c.check_dashboard(self.dashboard, [(self.entry, c.SOURCE)]), [self.imported])
        expected = dict(self.imported)
        expected.pop("imported_at")
        self.assertEqual(c.import_fields(self.entry, c.SOURCE), expected)
        self.assertNotIn("publisher", expected)  # Pinned importer does not recognize urn:air.
        self.assertNotIn("version", expected)
        self.assertEqual(c.import_fields({**self.entry, "identifier": "urn:ai:legacy.test:fixture"}, c.SOURCE)["publisher"], "legacy.test")

    def test_url_control_is_passive_metadata_with_no_invented_inline_data(self):
        entry = {key: value for key, value in self.entry.items() if key != "data"}
        entry["url"] = "https://127.0.0.1:8445/never-fetch"
        saved = c.import_fields(entry, c.SOURCE)
        self.assertEqual(saved["url"], entry["url"])
        self.assertNotIn("data", saved)

    def test_native_binding_wrong_membership_field_loss_or_readback_disagreement_fails(self):
        for change in ("callable", "enabled", "binding", "duplicate", "missing", "version", "source", "identity", "description", "data", "state", "time"):
            value = copy.deepcopy(self.dashboard)
            with self.subTest(change=change):
                if change == "callable":
                    value["imports"][0]["status"] = "callable"
                elif change == "enabled":
                    value["config"]["external_invocation_enabled"] = True
                elif change == "binding":
                    value["state"]["bindings"] = {self.imported["id"]: {"callable": False}}
                elif change == "duplicate":
                    value["imports"] *= 2
                elif change == "missing":
                    value["imports"] = []
                elif change == "state":
                    value["state"]["imports"] = []
                else:
                    key = {"source": "source_registry", "identity": "identifier", "time": "imported_at"}.get(change, change)
                    value["imports"][0]["entry"][key] = "" if change == "time" else "changed"
                with self.assertRaises(AssertionError):
                    c.check_dashboard(value, [(self.entry, c.SOURCE)])

    def test_refusals_require_the_intended_native_error(self):
        value = {"results": [], "sources": [{"url": c.SOURCE, "status": "error", "error": "503 Service Unavailable"}]}
        c.check_refusal(value, c.SOURCE, "error", "503")
        for changes in ({"results": [self.entry]}, {"sources": []}, {"sources": [{"url": c.SOURCE, "status": "ok"}]}):
            with self.subTest(changes=changes), self.assertRaises(AssertionError):
                c.check_refusal({**value, **changes}, c.SOURCE, "error", "503")
        with self.assertRaisesRegex(AssertionError, "cause changed"):
            c.check_refusal(value, c.SOURCE, "error", "certificate")
        for field, changed in (("url", c.CONTROL), ("status", "blocked"), ("error", "401 Unauthorized")):
            altered = copy.deepcopy(value)
            altered["sources"][0][field] = changed
            with self.subTest(field=field), self.assertRaises(AssertionError):
                c.check_refusal(altered, c.SOURCE, "error", "503")

    def test_identifier_only_import_key_does_not_distinguish_version_or_source(self):
        original = c.import_fields(self.entry, c.SOURCE)
        refreshed = c.import_fields({**self.entry, "version": "2.0", "description": "Explicit manual update"}, c.CONTROL)
        expected_id = "ext_" + hashlib.sha256(b"urn:air:example.com:fixture:mcp").hexdigest()[:16]
        self.assertEqual(original["id"], expected_id)
        self.assertEqual(refreshed["id"], expected_id)
        self.assertEqual(refreshed["source_registry"], c.CONTROL)
        self.assertEqual(refreshed["description"], "Explicit manual update")
        self.assertNotIn("version", refreshed)

    def test_partial_campaign_failed_cleanup_and_controls_cannot_qualify(self):
        value = {"phases": dict.fromkeys(c.PHASES, "passed"), "cleanup": "complete"}
        self.assertTrue(c.finish(value))
        for phase in c.PHASES:
            for status in ("unrun", "failed"):
                altered = copy.deepcopy(value)
                altered["phases"][phase] = status
                self.assertFalse(c.finish(altered))
        for changes in ({"error": "interrupted"}, {"cleanup": "failed"}, {"control": "metadata-mismatch"}):
            self.assertFalse(c.finish({**value, **changes}))


class AgentFieldAcquisitionTests(unittest.TestCase):
    def test_missing_or_changed_installation_never_acquires(self):
        with tempfile.TemporaryDirectory() as name, patch.object(tools, "OUTPUT", Path(name)), patch.object(tools, "STAMP", Path(name) / "tools.json"), patch.object(shared, "docker") as docker:
            with self.assertRaisesRegex(RuntimeError, "agentfield-tools-install"):
                tools.installed()
            docker.assert_not_called()
            tools.STAMP.write_text(json.dumps({"toolIdentity": "stale"}))
            with self.assertRaisesRegex(RuntimeError, "agentfield-tools-install"):
                tools.installed()
            docker.assert_not_called()

    def test_image_and_local_artifacts_are_rechecked(self):
        with tempfile.TemporaryDirectory() as name, patch.object(tools, "OUTPUT", Path(name)), patch.object(tools, "STAMP", Path(name) / "tools.json"), patch.object(tools, "identity", return_value="identity"), patch.object(tools, "pins", return_value={"revision": "pinned"}), patch.object(shared, "docker", return_value=Result(0, "sha256:image", "")) as docker:
            for name, contents in (("build.txt", "pinned"), ("dependency-licenses.csv", "module,https://example.com/LICENSE,MIT\n"), ("licenses.tar", "test notice bytes")):
                (tools.OUTPUT / name).write_text(contents)
            value = {"source": {"revision": "pinned"}, "toolIdentity": "identity", "image": "owned:image", "imageId": "sha256:image",
                     "artifacts": {name: tools.digest(tools.OUTPUT / name) for name in ("build.txt", "dependency-licenses.csv", "licenses.tar")}}
            tools.STAMP.write_text(json.dumps(value))
            self.assertEqual(tools.installed(), value)
            altered = copy.deepcopy(value)
            altered["source"]["revision"] = "different-native-source"
            tools.STAMP.write_text(json.dumps(altered))
            docker.reset_mock()
            with self.assertRaisesRegex(RuntimeError, "agentfield-tools-install"):
                tools.installed()
            docker.assert_not_called()
            for omitted in ("build.txt", "dependency-licenses.csv", "licenses.tar"):
                altered = copy.deepcopy(value)
                del altered["artifacts"][omitted]
                tools.STAMP.write_text(json.dumps(altered))
                docker.reset_mock()
                with self.subTest(omitted=omitted), self.assertRaisesRegex(RuntimeError, "agentfield-tools-install"):
                    tools.installed()
                docker.assert_not_called()
            altered = copy.deepcopy(value)
            altered["artifacts"]["../outside-output"] = "not-allowed"
            tools.STAMP.write_text(json.dumps(altered))
            docker.reset_mock()
            with self.assertRaisesRegex(RuntimeError, "agentfield-tools-install"):
                tools.installed()
            docker.assert_not_called()
            tools.STAMP.write_text(json.dumps(value))
            for missing in ("build.txt", "dependency-licenses.csv", "licenses.tar"):
                path = tools.OUTPUT / missing
                contents = path.read_bytes()
                path.unlink()
                try:
                    with self.subTest(missing=missing), self.assertRaisesRegex(RuntimeError, "agentfield-tools-install"):
                        tools.installed()
                finally:
                    path.write_bytes(contents)
            docker.return_value = Result(0, "sha256:other", "")
            with self.assertRaisesRegex(RuntimeError, "agentfield-tools-install"):
                tools.installed()
            docker.return_value = Result(0, "sha256:image", "")
            (tools.OUTPUT / "build.txt").write_text("changed")
            with self.assertRaisesRegex(RuntimeError, "agentfield-tools-install"):
                tools.installed()
            # A matching checksum alone does not establish the selected revision.
            value["artifacts"]["build.txt"] = tools.digest(tools.OUTPUT / "build.txt")
            tools.STAMP.write_text(json.dumps(value))
            with self.assertRaisesRegex(RuntimeError, "agentfield-tools-install"):
                tools.installed()

    def test_larger_reviewed_archives_still_enforce_each_bound_before_extraction(self):
        raw = io.BytesIO()
        with tarfile.open(fileobj=raw, mode="w:gz") as archive:
            for name in ("repo/LICENSE", "repo/go.mod"):
                member = tarfile.TarInfo(name)
                member.size = 1
                archive.addfile(member, io.BytesIO(b"x"))
        for options in ({"max_members": 1}, {"max_member_bytes": 0}, {"max_bytes": 1}):
            with self.subTest(options=options), tempfile.TemporaryDirectory() as name:
                target = Path(name)
                with self.assertRaisesRegex(ValueError, "bound exceeded"):
                    shared.extract_source(raw.getvalue(), target, "unused", **options)
                self.assertFalse(list(target.iterdir()))
