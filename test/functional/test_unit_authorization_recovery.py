"""Isolated assertion controls; SQL and recovery are exercised by real gates."""

import copy
import unittest
from unittest import mock

import authorization_recovery as policy


class PolicyRecoveryTests(unittest.TestCase):
    def test_every_retained_path_scope_and_replay_is_required(self):
        receipt = {"namespace": "scope", "body": {"namespaceId": "scope"},
                   "key": {"Idempotency-Key": "owned"}, "raw": b'{"id":"resource"}',
                   "resource": {"id": "resource"}, "group": "group",
                   "relation": {"id": "relation", "state": "active"},
                   "paths": {"/role": {"id": "role", "revision": 3}},
                   "assignmentQueries": {"/assignments?membershipId=member&roleDefinitionId=editor&limit=2": {"id": "revoked", "state": "revoked"}}}
        for fault in ("", "policy", "revocation", "member", "cursor", "lost-grant",
                      "broader-scope", "revived-edit", "domain-field", "assignment-cursor", "bytes", "replay", "etag"):
            with self.subTest(fault=fault):
                values = copy.deepcopy(list(receipt["paths"].values()))
                assignments = {"items": copy.deepcopy(list(receipt["assignmentQueries"].values()))}
                members = {"items": [receipt["relation"]]}
                decisions = {"authorizationRevision": 1, "catalogAllowed": [True, False, False]}
                raw = receipt["raw"]
                headers = {"ETag": '"rev-1"', "Idempotency-Replayed": "true"}
                if fault == "policy":
                    values[0]["revision"] = 2
                if fault == "revocation":
                    assignments["items"][0]["state"] = "active"
                if fault == "member":
                    members["items"] = []
                if fault == "cursor":
                    members["nextCursor"] = "unread"
                if fault == "assignment-cursor":
                    assignments["nextCursor"] = "unread"
                if fault == "lost-grant":
                    decisions["catalogAllowed"][0] = False
                if fault == "broader-scope":
                    decisions["catalogAllowed"][1] = True
                if fault == "revived-edit":
                    decisions["catalogAllowed"][2] = True
                if fault == "domain-field":
                    decisions = {"allowed": [True, False, False]}
                if fault == "bytes":
                    raw = b'{"id":"other"}'
                if fault == "replay":
                    headers.pop("Idempotency-Replayed")
                if fault == "etag":
                    headers["ETag"] = '"rev-2"'
                api = mock.Mock(base_url="http://127.0.0.1:18080")
                api.request.side_effect = [(value, {}) for value in values + [assignments, members]]
                reader = mock.Mock()
                reader.request.side_effect = [(decisions, {}), (raw, headers)]
                with mock.patch.object(policy, "Client", return_value=reader), mock.patch.object(policy, "audit_once") as audit:
                    if fault:
                        with self.assertRaises(AssertionError):
                            policy.verify(api, receipt)
                        audit.assert_not_called()
                    else:
                        policy.verify(api, receipt)
                        audit.assert_called_once_with(api, "resource.created", "resource", "resource")
                        self.assertEqual(reader.request.call_args_list[1], mock.call(
                            "POST", "/api/v1/resources", receipt["body"], (201,), receipt["key"], decode_body=False))


if __name__ == "__main__":
    unittest.main()
