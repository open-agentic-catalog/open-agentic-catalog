"""Peer trust selection and evidence units; no services or subprocesses."""

import base64
import copy
import hashlib
import unittest
from unittest.mock import Mock, patch

import peer_trust as t


class PeerTrust(unittest.TestCase):
    def test_explicit_selection(self):
        with patch.dict(t.os.environ, {}, clear=True):
            self.assertFalse(t.selected())
        with patch.dict(t.os.environ, {"INTEROPERABILITY_TRUST_VERIFICATION": "on"}, clear=True):
            self.assertTrue(t.selected())
        self.assertFalse(t.selected("off"))
        self.assertTrue(t.selected("on"))
        for value in ("", "true", "ON", " on ", None):
            if value is not None:
                with self.subTest(value=value), self.assertRaises(ValueError):
                    t.selected(value)

    def test_configuration_only_uses_public_fixture_authority(self):
        cfg = t.configuration("https://owned.test", "publisher.test", "private", "/keys")
        self.assertTrue(cfg["enabled"])
        policy = cfg["policies"][0]
        self.assertEqual(policy["evidenceOrigin"], "https://owned.test")
        self.assertEqual(policy["publisherDomains"], ["publisher.test"])
        self.assertEqual(policy["registrationKeys"], [{"id": "ans-ra-signer", "file": "/keys/ra.pem"}])
        self.assertEqual(policy["logKeyFiles"], ["/keys/tl.pem"])

    def test_target_preserves_version_presence_and_exact_evidence(self):
        row = dict(id="observation", identifier="urn:air:publisher.test:agents:a", versionPresent=False, mediaType="application/a2a-agent-card+json", exactEntryDigest="sha256:exact")
        entry = {"url": "https://owned.test/card"}
        result = t.target({"revision": 7}, row, entry)
        self.assertNotIn("version", result)
        self.assertEqual(result["observationId"], "observation")
        self.assertEqual(result["sourceRevision"], 7)
        self.assertEqual(result["delivery"], "url")
        self.assertEqual(t.target({"revision": 7}, {**row, "versionPresent": True, "version": "1.0"}, {"data": {}})["version"], "1.0")
        with self.assertRaises(AssertionError):
            t.target({"revision": 7}, row, {**entry, "trustManifest": {}})

    def test_empty_manifest_is_not_an_assessment(self):
        request = Mock(side_effect=[({"items": []}, {}), ({"revision": 1}, {}), ({"items": []}, {})])
        result = t.qualify(request, "source", empty=True)
        self.assertEqual(result["disposition"], "not-applicable")
        self.assertTrue(all(call.args[0] != t.PREFIX + "source/trust-assessments" for call in request.call_args_list))
        request = Mock(return_value=({"items": [{"id": "implicit", "kind": "source.trust_verify"}]}, {}))
        with self.assertRaisesRegex(AssertionError, "ordinary"):
            t.no_implicit_jobs(request)

    def test_negative_result_requires_exact_bytes_and_no_manufactured_trust(self):
        entry = b'{"identifier":"urn:air:owned.test:agent:a"}'
        digest = "sha256:" + hashlib.sha256(entry).hexdigest()
        assessment = {key: {"state": "not_assessed"} for key in ("producer", "log", "checkpoint", "inclusion", "status", "target")}
        assessment.update(currentRegistration=False, receipt={"state": "unavailable", "code": "receipt_missing"})
        result = dict(current=False, applicability="negative_or_insufficient", result=dict(sourceId="source", exactEntryDigest=digest,
            evidence={"entry": base64.b64encode(entry).decode()}, assessment=assessment,
            softwareTrust="not_assessed", artifactIntegrity="not_assessed", rights="not_assessed"))
        t.validate_assessment(result, "source", digest)
        for path, value in ((["current"], True), (["applicability"], "current"), (["result", "sourceId"], "other"),
                            (["result", "rights"], "verified"), (["result", "evidence", "entry"], "eA=="),
                            (["result", "evidence", "receipt"], "eA=="), (["result", "assessment", "log", "state"], "valid"),
                            (["result", "assessment", "receipt", "code"], "other")):
            changed = copy.deepcopy(result)
            parent = changed
            for key in path[:-1]:
                parent = parent[key]
            parent[path[-1]] = value
            with self.subTest(path=path), self.assertRaises(AssertionError):
                t.validate_assessment(changed, "source", digest)
