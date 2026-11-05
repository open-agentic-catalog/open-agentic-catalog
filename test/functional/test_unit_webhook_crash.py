"""Isolated false-pass controls for selected-event loss and evidence retention."""

import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

import runner
from publication_report_test_helpers import evidence as report_evidence
import webhook_crash as crash


def evidence():
    return {"selectionDigest": "a" * 64, "payloadDigest": "b" * 64, "attempts": 2,
            "workerExitCode": 137, "checks": list(crash.CHECKS)}


class CrashEvidenceTests(unittest.TestCase):
    def test_report_requires_complete_bounded_evidence(self):
        self.assertTrue(crash.valid_evidence(evidence()))
        for change in ({"attempts": True}, {"attempts": 1}, {"workerExitCode": 0},
                       {"checks": crash.CHECKS[:-1]}, {"secret": "private"},
                       {"payloadDigest": ""}, {"selectionDigest": None}):
            self.assertFalse(crash.valid_evidence(dict(evidence(), **change)))
        with tempfile.TemporaryDirectory() as root:
            path = Path(root)
            for value in ({"completed": runner.WEBHOOK_CASES},
                          {"completed": runner.WEBHOOK_CASES, "crash": dict(evidence(), attempts=1)}):
                runner.write_document(path / "webhooks.json", value)
                with self.assertRaisesRegex(runner.Failure, "crash-evidence"):
                    runner.webhook_evidence(path)
            value = {"completed": runner.WEBHOOK_CASES, "crash": evidence(), "publicationReport": report_evidence()}
            runner.write_document(path / "webhooks.json", value)
            self.assertEqual(runner.webhook_evidence(path), value)

    def test_receiver_requires_exact_identity_payload_and_attempt_phase(self):
        selection = {"endpointId": "owned", "source": "tenant", "eventId": "selected"}
        first = {"digest": "a" * 64, "duplicate": False, "outcome": "disconnected"}
        second = dict(first, duplicate=True, outcome="released")
        valid = dict(selection, released=True, attempts=[first, second])
        self.assertEqual(crash.check_receiver(valid, selection, "recovered", "a" * 64), "a" * 64)
        cases = [dict(valid, endpointId="other"), dict(valid, source="other"), dict(valid, eventId="unrelated"),
                 dict(valid, released=False), dict(valid, attempts=[]), dict(valid, attempts=[first]),
                 dict(valid, attempts=[first, dict(second, digest="b" * 64)]),
                 dict(valid, attempts=[first, dict(second, duplicate=False)]),
                 dict(valid, attempts=[dict(first, outcome="expired"), second]),
                 dict(valid, attempts=[first, dict(second, outcome="blocked")])]
        for value in cases:
            with self.subTest(value=value), self.assertRaises(runner.Failure):
                crash.check_receiver(value, selection, "recovered", "a" * 64)
        for phase, outcome in (("accepted", "blocked"), ("lost", "disconnected")):
            self.assertEqual(crash.check_receiver(dict(selection, released=False, attempts=[dict(first, outcome=outcome)]), selection, phase), "a" * 64)

    def test_pending_requires_unacknowledged_claim_without_saved_delivery(self):
        value = {"acknowledged": False, "leased": True, "leaseCleared": False,
                 "deliveryCount": 0, "leaseExpired": False}
        crash.check_pending(value, expired=False)
        for change in ({"acknowledged": True}, {"leased": False}, {"leaseCleared": True},
                       {"deliveryCount": 1}, {"leaseExpired": True}):
            with self.subTest(change=change), self.assertRaises(runner.Failure):
                crash.check_pending(dict(value, **change), expired=False)

    def test_snapshot_binds_all_database_reads_and_rejects_malformed_inputs(self):
        journey = mock.Mock()
        valid = {"digest": "a" * 64, "acknowledged": False, "leased": True,
                 "leaseCleared": False, "deliveryCount": 0, "leaseExpired": False}
        ids = [f"00000000-0000-7000-8000-{i:012d}" for i in (1, 2, 3)]
        journey.sql.return_value = json.dumps(valid)
        self.assertEqual(crash.snapshot(journey, *ids), valid)
        query = journey.sql.call_args.args[0]
        for text in ("d.tenant_id=e.tenant_id", "d.event_id=e.id", *ids):
            self.assertIn(text, query)
        with self.assertRaises(runner.Failure):
            crash.snapshot(journey, "'", *ids[1:])
        for change in ({"leaseExpired": 1}, {"deliveryCount": True}, {"acknowledged": 1}, {"digest": ""}):
            journey.sql.return_value = json.dumps(dict(valid, **change))
            with self.assertRaises(runner.Failure):
                crash.snapshot(journey, *ids)

    def test_full_control_cleans_up_and_rejects_incomplete_recovery(self):
        for fault in (None, "kill", "kill-and-release", "release", "accept-timeout", "persisted", "redelivery", "acknowledgement", "mutation", "other", "restart"):
            with self.subTest(fault=fault):
                journey = mock.Mock()
                ns = {"id": "namespace", "tenantId": "tenant", "revision": 1}
                journey.namespace.side_effect = [{"id": "other"}, ns]
                journey.other.request.side_effect = [({"retained": 1}, {}), ({"retained": 2 if fault == "other" else 1}, {})]
                journey.event_id.return_value = "event"
                before = [["api", 1, "original", 0], ["worker", 2, "original", 0]]
                after = [before[0], ["worker", 3, "lost" if fault == "restart" else "replacement", 0]]
                journey.process_identities.side_effect = [before, after]
                journey.env = {}
                endpoint = {"endpoint": {"id": "endpoint"}}
                selection = {"endpointId": "endpoint", "eventId": "event", "source": "https://github.com/open-agentic-catalog/open-agentic-catalog/tenants/tenant"}
                attempt = {"digest": "b" * 64, "duplicate": False, "outcome": "blocked"}
                receiver = [dict(selection, released=False, attempts=[]), dict(selection, released=False, attempts=[attempt]),
                            dict(selection, released=False, attempts=[dict(attempt, outcome="disconnected")]),
                            dict(selection, released=True, attempts=[dict(attempt, outcome="disconnected"),
                                 dict(attempt, duplicate=True, outcome="released", digest=("c" if fault == "redelivery" else "b") * 64)])]
                def receive(method, _path, *args, **_kwargs):
                    if method == "POST" and args[0]["action"] == "release" and fault in ("release", "kill-and-release"):
                        raise runner.Failure("release-failed")
                    if method == "GET" and fault == "accept-timeout":
                        return dict(selection, released=False, attempts=[]), {}
                    return (receiver.pop(0) if method == "GET" else None, {})
                journey.receiver.request.side_effect = receive
                def wait(_label, callback):
                    for _ in range(3):
                        value = callback()
                        if value:
                            return value
                    raise runner.Failure("timeout")
                journey.wait.side_effect = wait
                def compose(*args):
                    if args[0] == "kill" and fault in ("kill", "kill-and-release"):
                        raise runner.Failure("kill-failed")
                journey.compose.side_effect = compose
                pending = {"digest": "a" * 64, "acknowledged": False, "leased": True,
                           "leaseCleared": False, "deliveryCount": int(fault == "persisted"), "leaseExpired": False}
                final = dict(pending, acknowledged=True, leased=False, leaseCleared=fault != "acknowledgement", deliveryCount=1)
                observations = [dict(pending, leased=False, deliveryCount=0), pending, pending,
                                dict(pending, leaseExpired=True), dict(final, acknowledged=False), final]
                journey.delivery.return_value = {"tenantId": "tenant", "endpointId": "endpoint", "eventType": "com.open.agentic.catalog.namespace.created.v1", "attempt": 1, "responseStatus": 204}
                journey.api.request.return_value = (ns, {"ETag": '"rev-1"'})
                journey.sql.return_value = "2" if fault == "mutation" else "1"
                with mock.patch.object(crash, "snapshot", side_effect=copy.deepcopy(observations)), mock.patch.object(crash, "audit_once"), mock.patch.object(crash, "namespace_state", side_effect=lambda x: x), mock.patch.object(crash, "command", return_value=json.dumps({"Running": False, "ExitCode": 137, "StartedAt": "lost"}).encode()):
                    if fault:
                        with self.assertRaisesRegex(runner.Failure, "kill-failed" if fault == "kill-and-release" else ".*"):
                            crash.qualify(journey, endpoint)
                    else:
                        self.assertTrue(crash.valid_evidence(crash.qualify(journey, endpoint)))
                releases = [call for call in journey.receiver.request.call_args_list if len(call.args) == 3 and call.args[2].get("action") == "release"]
                self.assertEqual(len(releases), 1)
                if fault in ("kill", "kill-and-release", "release", "accept-timeout", "persisted"):
                    self.assertEqual(sum(call.args == ("start", "worker") for call in journey.compose.call_args_list), 1)
