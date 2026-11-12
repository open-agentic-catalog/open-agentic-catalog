"""Service-free regression for exact credential-sync evidence selection."""

import unittest
from unittest import mock

import credentials


class CredentialRevocationTests(unittest.TestCase):
    def test_waits_only_for_successful_reads_of_the_previous_binding(self):
        for delays in (0, 2):
            with self.subTest(delays=delays):
                replies, etags, keys = [], [], []
                for index in range(delays + 1):
                    etag, key = f'"rev-{index + 1}"', f"attempt-{index + 1}"
                    etags.append(etag)
                    keys.append(key)
                    result = {"status": "succeeded", "credentialRevision": "two"} if index < delays else {"code": "DEPENDENCY_UNAVAILABLE"}
                    replies.extend([({}, {"ETag": etag}), (result, {})])
                api = mock.Mock()
                api.request.side_effect = replies
                with mock.patch.object(credentials, "key", side_effect=keys), \
                        mock.patch.object(credentials.time, "monotonic", return_value=0), \
                        mock.patch.object(credentials.time, "sleep") as sleep:
                    credentials.wait_revoked(api, "/source", "two")
                posts = [c for c in api.request.call_args_list if c.args[0] == "POST"]
                self.assertEqual(posts, [mock.call("POST", "/source:test", {}, (200, 503), {"Idempotency-Key": key, "If-Match": etag})
                                         for key, etag in zip(keys, etags, strict=True)])
                self.assertEqual(api.request.call_count, 2 * (delays + 1))
                self.assertEqual(sleep.call_count, delays)

    def test_unexpected_result_or_uncertain_command_outcome_is_not_retried(self):
        for result in ({"status": "failed"}, {"status": "succeeded", "credentialRevision": "one"},
                       {"code": "OTHER"}, RuntimeError("synthetic transport failure")):
            with self.subTest(result=result):
                api = mock.Mock()
                api.request.side_effect = [({}, {"ETag": '"rev-1"'}), result if isinstance(result, Exception) else (result, {})]
                with mock.patch.object(credentials.time, "sleep") as sleep:
                    with self.assertRaises((AssertionError, RuntimeError)):
                        credentials.wait_revoked(api, "/source", "two")
                self.assertEqual(api.request.call_count, 2)
                sleep.assert_not_called()

    def test_readable_binding_at_the_deadline_fails_without_sleeping(self):
        api = mock.Mock()
        api.request.side_effect = [({}, {"ETag": '"rev-1"'}), ({"status": "succeeded", "credentialRevision": "two"}, {})]
        with mock.patch.object(credentials.time, "monotonic", side_effect=[0, 16]), \
                mock.patch.object(credentials.time, "sleep") as sleep:
            with self.assertRaisesRegex(AssertionError, "revoked binding stayed readable"):
                credentials.wait_revoked(api, "/source", "two")
        self.assertEqual(api.request.call_count, 2)
        sleep.assert_not_called()


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
