"""Guard the namespace journey's request binding and retained-state assertions."""

import unittest
from unittest.mock import Mock, patch

import journeys


class NamespaceJourneyTests(unittest.TestCase):
    def test_conflicting_requests_cannot_replace_or_masquerade_as_replay(self):
        for fault in (None, "wrong-code", "replayed-conflict", "changed-replay", "changed-state", "ignored-create", "ignored-update", "changed-identity"):
            with self.subTest(fault=fault):
                created = {"id": "owned", "slug": "run-contract", "displayName": "Contract journey", "revision": 1}
                changed = {**created, "displayName": "Contract updated", "revision": 2}
                if fault == "ignored-create":
                    created = {**created, "slug": "wrong-slug"}
                if fault == "ignored-update":
                    changed = {**changed, "displayName": created["displayName"]}
                if fault == "changed-identity":
                    changed = {**changed, "id": "different"}
                original_headers = {"ETag": '"rev-1"'}
                changed_headers = {"ETag": '"rev-2"'}
                replay_headers = {**changed_headers, "Idempotency-Replayed": "true"}
                problem = {"code": "IDEMPOTENCY_KEY_REUSED"}
                conflict_headers = {}
                if fault == "wrong-code":
                    problem = {"code": "CONFLICT"}
                if fault == "replayed-conflict":
                    conflict_headers = {"Idempotency-Replayed": "true"}
                api, other = Mock(), Mock()
                api.request.side_effect = [
                    (created, original_headers), (created, original_headers), (created, original_headers),
                    (changed, changed_headers), (changed, replay_headers),
                    (problem, conflict_headers), (problem, conflict_headers),
                    (created if fault == "changed-replay" else changed, replay_headers),
                    ({"code": "PRECONDITION_FAILED"}, {}),
                    (created if fault == "changed-state" else changed, changed_headers),
                ]
                with patch.object(journeys, "Client", side_effect=[api, other]), patch.object(journeys, "audit_once") as audit:
                    if fault:
                        with self.assertRaises(AssertionError):
                            journeys.namespace_journey("http://127.0.0.1:1", "run")
                    else:
                        journeys.namespace_journey("http://127.0.0.1:1", "run")
                        calls = api.request.call_args_list
                        original = calls[3]
                        changed_body, changed_precondition = calls[5:7]
                        self.assertEqual(changed_body.args[3], (409,))
                        self.assertEqual(changed_body.args[4], original.kwargs["headers"])
                        self.assertNotEqual(changed_body.args[2], original.args[2])
                        self.assertEqual(changed_precondition.args[2], original.args[2])
                        self.assertEqual(changed_precondition.args[4]["If-Match"], '"rev-2"')
                        self.assertEqual(changed_precondition.args[4]["Idempotency-Key"], original.kwargs["headers"]["Idempotency-Key"])
                        self.assertEqual(calls[7], original)
                        self.assertEqual(audit.call_count, 2)

    def test_audit_requires_the_exact_event_not_just_one_result(self):
        event = {"action": "namespace.created", "targetType": "namespace", "targetId": "owned"}
        api = Mock()
        api.request.return_value = ({"items": [event]}, {})
        journeys.audit_once(api, "namespace.created", "namespace", "owned")
        for page in (
            {"items": []}, {"items": [event, event]}, {"items": [event], "nextCursor": "more"},
            *({"items": [{**event, field: "different"}]} for field in event),
        ):
            with self.subTest(page=page):
                api.request.return_value = (page, {})
                with self.assertRaises(AssertionError):
                    journeys.audit_once(api, "namespace.created", "namespace", "owned")


if __name__ == "__main__":
    unittest.main()
