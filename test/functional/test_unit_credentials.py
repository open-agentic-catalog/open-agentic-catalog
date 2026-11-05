"""Service-free regression for exact credential-sync evidence selection."""

import unittest
from unittest import mock

import credentials


class CredentialSnapshotTests(unittest.TestCase):
    def test_completed_job_selects_exact_snapshot_despite_tied_history(self):
        # Clock clamping can produce equal timestamps; UUID tie-break order is
        # not completion order. The old credential revision sorts first here.
        old = {"id": "snapshot-z", "sourceId": "source", "state": "complete", "credentialRevision": "one", "startedAt": "2026-09-12T00:00:00Z"}
        current = dict(old, id="snapshot-a", credentialRevision="two")
        api = mock.Mock()
        routes = {
            "/api/v1/federation/sources/source/snapshots?limit=50": {"items": [old, current]},
            "/api/v1/federation/snapshots/snapshot-a": current,
        }
        api.request.side_effect = lambda method, path: (routes[path], {})
        job = {"result": {"type": "source_snapshot", "sourceId": "source", "snapshotId": "snapshot-a", "state": "complete"}}
        with mock.patch.object(credentials, "wait_job", return_value=job) as wait:
            self.assertEqual(credentials.completed_snapshot(api, "source", "job"), current)
            wait.assert_called_once_with(api, "job", {"succeeded"})
        api.request.assert_called_once_with("GET", "/api/v1/federation/snapshots/snapshot-a")

    def test_wrong_job_outcome_cannot_select_evidence(self):
        result = {"type": "source_snapshot", "sourceId": "source", "snapshotId": "snapshot", "state": "complete"}
        for change in ({"type": "publication_snapshot"}, {"sourceId": "other"}, {"state": "incomplete"}):
            with self.subTest(change=change), mock.patch.object(credentials, "wait_job", return_value={"result": dict(result, **change)}):
                api = mock.Mock()
                with self.assertRaisesRegex(AssertionError, "different outcome"):
                    credentials.completed_snapshot(api, "source", "job")
                api.request.assert_not_called()

    def test_snapshot_identity_and_completion_are_checked(self):
        result = {"type": "source_snapshot", "sourceId": "source", "snapshotId": "snapshot", "state": "complete"}
        for change in ({"id": "other"}, {"sourceId": "other"}, {"state": "incomplete"}):
            with self.subTest(change=change), mock.patch.object(credentials, "wait_job", return_value={"result": result}):
                api = mock.Mock()
                api.request.return_value = dict(id="snapshot", sourceId="source", state="complete") | change, {}
                with self.assertRaisesRegex(AssertionError, "identity changed"):
                    credentials.completed_snapshot(api, "source", "job")
