"""Inert trust qualification helpers; no listeners, native tools or subprocesses."""

import base64
import copy
import json
import unittest
from unittest.mock import patch

from ans import trust


def positive_result():
    assessment = {key: {"state": "valid"} for key in ("receipt", "producer", "log", "checkpoint", "inclusion", "target", "status")}
    assessment.update(currentRegistration=True, schemaVersion="V1", registrationKeyFingerprint="a" * 64, logKeyFingerprint="b" * 64, envelopeLogKeyFingerprint="b" * 64, subject="subject", logId="event")
    return {"current": True, "applicability": "current", "result": {"assessment": assessment}}


class TrustUnits(unittest.TestCase):
    def test_native_admin_credential_is_limited_to_exact_producer_registration(self):
        peer = trust.native.Native.__new__(trust.native.Native)
        peer.settings = {"key": "ra-only", "tl_key": "tl-only", "tl": "http://127.0.0.1:1234", "ra": "http://127.0.0.1:2345"}
        peer.captures = []
        peer.http = unittest.mock.Mock()
        response = unittest.mock.MagicMock(status=200)
        response.__enter__.return_value = response
        response.read.return_value = b"{}"
        peer.http.open.return_value = response
        peer.request("tl", "/internal/v1/producer-keys", {"public_key_pem": "public"}, auth=True)
        request = peer.http.open.call_args.args[0]
        self.assertEqual(request.get_header("Authorization"), "Bearer tl-only")
        self.assertEqual(request.full_url, "http://127.0.0.1:1234/internal/v1/producer-keys")
        for destination, path, body in (("tl", "/checkpoint", {}), ("finder-edge", "/internal/v1/producer-keys", {}),
                                        ("tl", "/internal/v1/producer-keys", None), ("tl", "/internal/v1/producer-keys?other=1", {})):
            with self.subTest(destination=destination, path=path), self.assertRaises(ValueError):
                peer.request(destination, path, body, auth=True)
        self.assertEqual(peer.http.open.call_count, 1)

    def test_signer_fingerprints_require_actual_old_new_bindings(self):
        result = positive_result()
        trust.signer_fingerprints(result, "a" * 64, "b" * 64, "b" * 64)
        for field in ("registrationKeyFingerprint", "logKeyFingerprint", "envelopeLogKeyFingerprint"):
            changed = copy.deepcopy(result)
            changed["result"]["assessment"][field] = "c" * 64
            with self.subTest(field=field), self.assertRaises(AssertionError):
                trust.signer_fingerprints(changed, "a" * 64, "b" * 64, "b" * 64)

    def test_history_binding_rejects_a_different_tree_or_unsigned_shape(self):
        root = base64.b64encode(bytes(32)).decode()
        text = "owned-ans\n3\n" + root + "\n"
        row = {"logSize": 3, "rootHash": root, "originName": "owned-ans", "checkpointText": text, "signatures": [{"signatureType": "c2sp"}]}
        raw = json.dumps({"checkpoints": [row]}).encode()
        trust.history_binding(raw, 3, root, text)
        self.assertEqual(trust.checkpoint((text + "\n— native signature\n").encode()), (3, root, text))
        for field, value in (("logSize", 4), ("rootHash", "wrong"), ("originName", "wrong"), ("checkpointText", text[:-1]), ("signatures", [])):
            changed = {**row, field: value}
            with self.subTest(field=field), self.assertRaises(AssertionError):
                trust.history_binding(json.dumps({"checkpoints": [changed]}), 3, root, text)
        for rows in ([], [row, row]):
            with self.assertRaises(AssertionError):
                trust.history_binding(json.dumps({"checkpoints": rows}), 3, root, text)

    def test_history_request_sequence_cannot_scan_retry_or_fetch_status_after_failure(self):
        paths = ["/v1/agents/subject/receipt", "/checkpoint", "/v1/log/checkpoint/history?fromSize=3&toSize=3&limit=1"]
        rows = [{"path": path, "authorizationPresent": False, "cookiePresent": False} for path in paths]
        trust.history_requests(rows, "subject", 3, positive=False)
        status = {"path": "/v1/agents/subject/status-token", "authorizationPresent": False, "cookiePresent": False}
        trust.history_requests([*rows, status], "subject", 3, positive=True)
        for changed in (rows[:-1], [*rows, rows[-1]], [*rows, status], [rows[1], rows[0], rows[2]],
                        [*rows[:2], {**rows[2], "path": "/v1/log/checkpoint/history?limit=100"}],
                        [{**rows[0], "authorizationPresent": True}, *rows[1:]]):
            with self.subTest(rows=changed), self.assertRaises(AssertionError):
                trust.history_requests(changed, "subject", 3, positive=False)

    def test_current_requires_every_independent_check_and_full_identity(self):
        result = positive_result()
        trust.positive(result)
        for key in ("receipt", "producer", "log", "checkpoint", "inclusion", "target", "status"):
            bad = copy.deepcopy(result)
            bad["result"]["assessment"][key]["state"] = "unavailable"
            with self.subTest(key=key), self.assertRaises(AssertionError):
                trust.positive(bad)
        for key in ("registrationKeyFingerprint", "logKeyFingerprint", "envelopeLogKeyFingerprint", "subject", "logId"):
            bad = copy.deepcopy(result)
            bad["result"]["assessment"][key] = ""
            with self.subTest(key=key), self.assertRaises(AssertionError):
                trust.positive(bad)
        with self.assertRaises(AssertionError):
            trust.positive(result, "V2")
        result["current"] = False
        with self.assertRaises(AssertionError):
            trust.positive(result)

    def test_evidence_inventory_cannot_hide_overflow_or_credentials(self):
        peer = unittest.mock.Mock()
        document = {"overflow": False, "requests": [{"authorizationPresent": False, "cookiePresent": False}]}
        peer.document.return_value = document
        self.assertEqual(len(trust.evidence(peer)), 1)
        document["overflow"] = True
        with self.assertRaises(AssertionError):
            trust.evidence(peer)
        document["overflow"] = False
        for key in ("authorizationPresent", "cookiePresent"):
            document["requests"][0][key] = True
            with self.subTest(key=key), self.assertRaises(AssertionError):
                trust.evidence(peer)
            document["requests"][0][key] = False

    def test_selection_uses_server_digest_and_requires_exact_unambiguous_membership(self):
        row = {"sourceRevision": 7, "identifier": "urn:air:alpha.ans.test:agents:alpha", "versionPresent": True, "version": "1.0.0", "exactEntryDigest": "sha256:original", "entry": {"type": trust.native.MCP}}
        api = unittest.mock.Mock()
        document = {"partial": False, "results": [row]}
        api.call.return_value = document, {}
        request = trust.select(api, {"source_id": "source"})
        self.assertEqual(request["exactEntryDigest"], row["exactEntryDigest"])
        self.assertEqual(request["sourceRevision"], 7)
        for partial, rows in ((True, [row]), (False, []), (False, [row, row])):
            document.update(partial=partial, results=rows)
            with self.subTest(partial=partial, count=len(rows)), self.assertRaises(AssertionError):
                trust.select(api, {"source_id": "source"})

    def test_failed_jobs_do_not_return_empty_success_results(self):
        api = unittest.mock.Mock()
        api.call.return_value = {"state": "failed"}, {}
        with patch.object(trust, "wait_for", return_value={"state": "failed"}) as wait:
            final = trust.completed(api, {"source_id": "source"}, "job", success=False)
        self.assertEqual(final["state"], "failed")
        self.assertEqual(api.call.call_args.kwargs["expected"], 409)
        self.assertIn("terminal", wait.call_args.args[0])
