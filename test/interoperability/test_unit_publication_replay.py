"""Small synthetic controls; publisher data is unnecessary for unit testing."""

from hashlib import sha256
from http.client import HTTPMessage
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import publication_replay_runner as runner
from publication_replay import A2A, PATH, REGISTRY, check_read_audits, inventory, read_capture, replay_route


class PublicationInputsTest(unittest.TestCase):
    def test_missing_or_changed_capture_blocks_only_at_input_admission(self):
        with tempfile.TemporaryDirectory() as workspace:
            root = Path(workspace)
            capture = root / "capture.json"
            for case in ("missing", "changed"):
                with (
                    self.subTest(case=case),
                    patch.dict(runner.os.environ, DESVELA_CAPTURE=str(capture)),
                    patch.object(runner, "version", return_value="test"),
                    patch.object(runner, "local_daemon") as daemon,
                ):
                    if case == "changed":
                        capture.write_bytes(b'{"entries":[]}')
                    directory = root / case
                    self.assertFalse(runner.qualify("desvela", directory))
                    report = json.loads((directory / "report.json").read_text())
                    self.assertEqual(report["status"], "blocked")
                    self.assertEqual(report["phases"]["inputs"], "blocked")
                    self.assertTrue(all(value == "unrun" for name, value in report["phases"].items() if name != "inputs"))
                    self.assertEqual(report["cleanup"], "complete")
                    self.assertFalse((directory / "private").exists())
                    daemon.assert_not_called()

    def test_capture_requires_exact_bytes_even_for_equivalent_json(self):
        original = b'{"entries":[]}'
        pin = ("LOCAL_CAPTURE", len(original), sha256(original).hexdigest())
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "capture.json"
            with self.assertRaisesRegex(ValueError, "Set LOCAL_CAPTURE"):
                read_capture(path, pin)
            path.write_bytes(original)
            self.assertEqual(read_capture(path, pin), original)
            for changed in (b'{"entries":{}}', original + b'\n', original[:-1]):
                with self.subTest(changed=changed):
                    path.write_bytes(changed)
                    with self.assertRaisesRegex(ValueError, "size and SHA-256"):
                        read_capture(path, pin)

    def test_schema_valid_substitution_cannot_satisfy_the_recorded_inventory(self):
        selected = "urn:air:publisher.example:agents:first"
        feed = {"types": {A2A: 1, REGISTRY: 1}, "publisher": "publisher.example", "selected": selected}
        agent = {"identifier": selected, "type": A2A}
        registry = {"identifier": "urn:air:publisher.example:registry:self", "type": REGISTRY}

        def encoded(second):
            return json.dumps({"entries": [agent, second]}).encode()

        self.assertEqual(inventory(encoded(registry), feed), [agent, registry])
        for change in ({"identifier": selected}, {"identifier": "urn:air:other.example:registry:self"},
                       {"version": ""}, {"type": A2A}):
            with self.subTest(change=change), self.assertRaises(AssertionError):
                inventory(encoded(dict(registry, **change)), feed)


class PublicationRequestTest(unittest.TestCase):
    def test_only_anonymous_requests_can_read_the_manifest_or_count_as_a_fallback(self):
        headers = HTTPMessage()
        headers["Accept"] = "application/json"
        fallback = "/.well-known/ai-catalog.json"
        for mode in ("healthy", "malformed", "outage", "timeout", "oversize"):
            self.assertEqual(replay_route(PATH, headers, mode), "manifest")
            expected = "fallback" if mode in ("malformed", "outage") else "unexpected"
            self.assertEqual(replay_route(fallback, headers, mode), expected)
        self.assertEqual(replay_route(PATH + "?q=private", headers, "healthy"), "unexpected")
        for path in (PATH, fallback):
            for name in ("authorization", "Cookie", "proxy-Authorization", "X-API-Key"):
                for value in ("synthetic", ""):
                    with self.subTest(path=path, name=name, value=value):
                        credential = HTTPMessage()
                        credential[name] = value
                        self.assertEqual(replay_route(path, credential, "outage"), "unexpected")


class EvidenceAuditTest(unittest.TestCase):
    def test_each_read_requires_its_own_audit_for_the_returned_bytes_and_reader(self):
        downloads = {"current": ("observation", "digest", 9)}
        event = {"requestId": "current", "actor": "reader", "tenantId": "tenant",
                 "action": "evidence.raw_observation.read", "targetType": "observation", "targetId": "observation",
                 "details": {"facts": {"sizeBytes": 9, "contentDigest": "digest"}}}
        previous = dict(event, requestId="previous")
        check_read_audits(downloads, [previous, event], "reader", "tenant")
        for name, events in (("earlier read only", [previous]), ("duplicated", [event, event]),
                             ("another reader", [dict(event, actor="other")]),
                             ("wrong bytes", [dict(event, details={"facts": {"sizeBytes": 8, "contentDigest": "other"}})])):
            with self.subTest(name=name), self.assertRaises(AssertionError):
                check_read_audits(downloads, events, "reader", "tenant")


if __name__ == "__main__":
    unittest.main()
