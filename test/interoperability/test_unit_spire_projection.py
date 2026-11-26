"""Exercise the Minikube renewal assertions with independently lagging API and worker reads."""
import base64
import json
from pathlib import Path
import sys
import unittest
from unittest.mock import Mock, patch

# The qualification normally runs as a script with sibling imports.
with patch.object(sys, "path", [str(Path(__file__).parent / "spire"), *sys.path]), patch.dict(sys.modules):
    import kubernetes_renewal as campaign


class InitialProjectionTest(unittest.TestCase):
    def setUp(self):
        self.k = Mock()
        self.api = Mock()
        self.old = "catalog-a-reader-100-" + "a" * 32
        self.current = "catalog-a-reader-200-" + "b" * 32
        metadata = {"revision": self.current, "secretFile": self.current + ".token",
                    "tenantId": campaign.TENANTS["b"], "origin": campaign.A, "expiresAt": "1970-01-01T00:03:20Z"}
        self.k.kubectl.return_value = b"\n".join([
            base64.b64encode(json.dumps(metadata).encode()), (self.old + ".token").encode(),
            (self.current + ".token").encode(), b"catalog-a-reader.json", b"unrelated"])
        self.sleep = self.enterContext(patch.object(campaign.time, "sleep"))

    def test_source_created_once_from_the_current_inspection(self):
        earlier = {"id": "stale", "credentialRevision": self.old}
        current = {"id": "selected", "credentialRevision": self.current}
        self.api.request.side_effect = [(earlier, None), (current, None), ({"id": "source"}, None)]
        self.assertEqual(campaign.connect_projected(self.k, self.api), "source")
        requests = [call.args for call in self.api.request.call_args_list]
        self.assertEqual([call[1] for call in requests], ["/api/v1/federation/source-inspections"] * 2 +
                         ["/api/v1/federation/sources"])
        self.assertEqual(requests[-1][2]["inspectionId"], "selected")
        self.sleep.assert_called_once()

    def test_consumer_lag_requires_both_to_report_the_current_revision(self):
        stale = {"credentialRevision": self.old, "snapshotId": "stale"}
        current = {"credentialRevision": self.current, "snapshotId": "complete"}
        with patch.object(campaign, "tested_revision", side_effect=[self.old, self.current, self.current]), \
             patch.object(campaign, "indexed_snapshot", side_effect=[stale, current]) as sync:
            self.assertEqual(campaign.synchronize_projected(self.k, self.api, "source", "agent"),
                             (self.current, 200, current))
        self.assertEqual(sync.call_count, 2)
        self.assertEqual(self.sleep.call_count, 2)

    def test_foreign_revision_and_failed_job_are_never_retried(self):
        self.api.request.return_value = ({"id": "inspection", "credentialRevision": "unrelated"}, None)
        with self.assertRaisesRegex(ValueError, "unknown credential revision"):
            campaign.connect_projected(self.k, self.api)
        self.api.request.assert_called_once()
        with patch.object(campaign, "tested_revision", return_value=self.current), \
             patch.object(campaign, "indexed_snapshot", side_effect=ValueError("Sync failed")), \
             self.assertRaisesRegex(ValueError, "Sync failed"):
            campaign.synchronize_projected(self.k, self.api, "source", "agent")
        self.sleep.assert_not_called()

    def test_stalled_projection_stops_at_deadline(self):
        sample = Mock(return_value=None)
        with patch.object(campaign.time, "monotonic", side_effect=[10, 10, 130]), \
             self.assertRaisesRegex(TimeoutError, "did not advance"):
            campaign.await_projection(sample)
        sample.assert_called_once()
