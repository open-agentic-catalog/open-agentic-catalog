"""Native import evidence checks with synthetic documents; no processes or services."""

import base64
import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
import urllib.parse
from unittest.mock import Mock

import agntcy_imports as imports
from common import Result


def output(*, total=1, imported=0, skipped=0, failed=1,
           extra='code = InvalidArgument desc = record validation failed: Unknown attribute at "modules[0].artifact"'):
    return Result(0, f"Total records:   {total}\nImported:        {imported}\nSkipped:         {skipped}\nFailed:          {failed}\n{extra}\n", "")


class ImportEvidence(unittest.TestCase):
    def test_skill_admission_requires_both_missing_fields_and_the_artifact_error(self):
        artifact = 'code = InvalidArgument desc = record validation failed: Unknown attribute at "modules[0].artifact"'
        fields = ['Required attribute "modules[0].data.' + name + '" is missing' for name in ("source_locator", "validation")]
        with tempfile.TemporaryDirectory() as directory:
            cids = Path(directory) / "cids"
            self.assertEqual(imports.import_rejection(output(extra=artifact + " ".join(fields)), cids, kind="agent-skill")["returned_cids"], [])
            for diagnostic in (artifact, artifact + fields[0], artifact + fields[1], " ".join(fields)):
                with self.subTest(diagnostic=diagnostic), self.assertRaises(AssertionError):
                    imports.import_rejection(output(extra=diagnostic), cids, kind="agent-skill")

    def test_federation_checks_advertised_collections_and_refuses_foreign_destinations(self):
        origin = "https://owned.test:8443"
        counts = {imports.A2A: 4, imports.MCP: 2, "application/agent-skills+md": 0, "application/agent-skills+gzip": 0}
        manifest = {"entries": [], "collections": [dict(mediaType=media, url=origin + "/v1/agents?" + urllib.parse.urlencode({"filter": "type=" + media})) for media in counts]}
        replies = [(dict(results=[{}] * count, totalCount=count, nextPageToken=""), {}) for count in counts.values()]
        api = Mock()
        api.document.side_effect = [(manifest, {}), *replies]
        result = imports.nonempty_eligibility(api, origin)
        self.assertEqual(result["disposition"], "unsupported_nonempty")
        self.assertEqual([row["native_members"] for row in result["collections"]], [4, 2, 0, 0])
        self.assertEqual(api.document.call_count, 5)
        for change in (lambda m: m.update(entries=[{}]),
                       lambda m: m["collections"][0].update(url="https://public.test/v1/agents"),
                       lambda m: m["collections"][0].update(url=origin + "/another-path"),
                       lambda m: m["collections"][0].update(url=origin + "/v1/agents?filter=unexpected")):
            modified = copy.deepcopy(manifest)
            change(modified)
            api.document.reset_mock()
            api.document.side_effect = [(modified, {})]
            with self.subTest(change=change), self.assertRaises(AssertionError):
                imports.nonempty_eligibility(api, origin)
            self.assertEqual(api.document.call_count, 1)
        api.document.side_effect = [(manifest, {}), ({"items": []}, {})]
        with self.assertRaisesRegex(AssertionError, "envelope changed"):
            imports.nonempty_eligibility(api, origin)
        # A same-length object is not a native List's results array.
        api.document.side_effect = [(manifest, {}), (dict(results={str(i): {} for i in range(4)}, totalCount=4, nextPageToken=""), {}), *replies[1:]]
        with self.assertRaisesRegex(AssertionError, "membership changed"):
            imports.nonempty_eligibility(api, origin)

    def test_mcp_registry_input_and_ide_output_are_not_conflated_with_server_cards(self):
        source = imports.read_json(imports.ROOT / "samples/cards/everything.registry.json")
        profiles = imports.mcp_profiles(source)
        self.assertEqual(profiles["registry"], [])
        self.assertTrue(profiles["server_card"])
        self.assertTrue(all(imports.mcp_profiles({"servers": {"example": {"command": "unused"}}}).values()))

    def test_exit_zero_with_failed_record_is_not_a_successful_import(self):
        with tempfile.TemporaryDirectory() as directory:
            cids = Path(directory) / "cids"
            result = imports.import_rejection(output(), cids)
            self.assertEqual(result["disposition"], "unsupported_native_schema")
            self.assertEqual(result["returned_cids"], [])
            for invalid in (output(imported=1, failed=0), output(skipped=1, failed=0),
                            output(extra="Unavailable: connection refused"), output(extra="artifact: model disabled"),
                            Result(1, output().stdout, "failure")):
                with self.subTest(invalid=invalid), self.assertRaises(AssertionError):
                    imports.import_rejection(invalid, cids)
            cids.write_text("unexpected-cid\n")
            with self.assertRaisesRegex(AssertionError, "returned a CID"):
                imports.import_rejection(output(), cids)

    def test_counts_must_be_unique_complete_and_consistent_with_mode(self):
        self.assertEqual(imports.summary(output())["Failed"], 1)
        dry = output(failed=0, extra="This was a dry run.")
        self.assertEqual(imports.summary(dry, dry=True)["Total records"], 1)
        self.assertEqual(imports.summary(output(total=0, failed=0), total=0)["Failed"], 0)
        for invalid, dry_mode in ((output(total=2), False), (output(imported=1), False),
                                  (Result(0, output().stdout + "Failed: 1\n", ""), False),
                                  (Result(0, output().stdout.replace("Skipped:", "skip:"), ""), False),
                                  (dry, False), (output(), True), (output(failed=1, extra="This was a dry run."), True)):
            with self.subTest(invalid=invalid, dry=dry_mode), self.assertRaises(AssertionError):
                imports.summary(invalid, dry=dry_mode)

    def test_preservation_compares_input_and_original_artifact_not_only_record_roundtrip(self):
        for kind in ("a2a", "mcp", "agent-skill"):
            original = b"---\nname: example\n---\n\nKeep this entire instruction body.\n" if kind == "agent-skill" else {"name": "selected", "unknown": {"preserve": True}}
            raw = original if kind == "agent-skill" else json.dumps(original).encode()
            data = {"skill_file": "SKILL.md"} if kind == "agent-skill" else {"card_data" if kind == "a2a" else "mcp_data": original}
            record = {"modules": [{"name": "example", "data": data, "artifact": {"data": base64.b64encode(raw).decode(), "media_type": "example",
                "size": len(raw), "digest": "sha256:" + hashlib.sha256(raw).hexdigest()}}]}
            self.assertEqual(imports.original_artifact(record, kind, original)["module"], "example")
            changed = copy.deepcopy(record)
            changed["modules"][0]["artifact"]["data"] = base64.b64encode(b"{}" if kind != "agent-skill" else b"---\nname: example\n---\n").decode()
            changed_raw = base64.b64decode(changed["modules"][0]["artifact"]["data"])
            changed["modules"][0]["artifact"].update(size=len(changed_raw), digest="sha256:" + hashlib.sha256(changed_raw).hexdigest())
            with self.subTest(kind=kind), self.assertRaises(AssertionError):
                imports.original_artifact(changed, kind, original)
            changed = copy.deepcopy(record)
            changed["modules"][0]["data"] = {"skill_file": "OTHER.md"} if kind == "agent-skill" else {"card_data" if kind == "a2a" else "mcp_data": {"name": "changed"}}
            with self.subTest(kind=kind, path="module data"), self.assertRaises(AssertionError):
                imports.original_artifact(changed, kind, original)
            changed = copy.deepcopy(record)
            changed["modules"].append(changed["modules"][0])
            with self.assertRaisesRegex(AssertionError, "module count"):
                imports.original_artifact(changed, kind, original)
            for field, bad in (("size", 0), ("digest", "sha256:" + "0" * 64)):
                changed = copy.deepcopy(record)
                changed["modules"][0]["artifact"][field] = bad
                with self.subTest(kind=kind, field=field), self.assertRaises(AssertionError):
                    imports.original_artifact(changed, kind, original)
