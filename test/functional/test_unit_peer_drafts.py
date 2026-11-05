"""Pure checks for complete, private two-Catalog draft evidence."""

import copy
import unittest

import peer_drafts
from runner import Failure


class PeerDraftEvidenceTests(unittest.TestCase):
    def test_failed_child_projects_only_closed_stages(self):
        for stage in peer_drafts.STAGES:
            with self.subTest(stage=stage), self.assertRaisesRegex(Failure, "^peer-draft-" + stage + "$"):
                peer_drafts.failed_stage('{"stage":"' + stage + '"}')
        for raw in ('{"stage":"private-token"}', '{"stage":"cli-import","token":"private"}',
                    '{"stage":[]}', '[]', 'null', 'not JSON', b'\xff'):
            self.assertIsNone(peer_drafts.failed_stage(raw))

    def test_failure_checkpoint_rejects_private_or_unrecognized_fields(self):
        for stage in peer_drafts.STAGES:
            self.assertTrue(peer_drafts.valid_failure({"stage": stage}))
            self.assertTrue(peer_drafts.valid_failure({"stage": stage, "line": 234}))
        for value in (None, [], {"stage": []}, {"stage": "token"},
                      {"stage": "cli-import", "token": "private"}):
            self.assertFalse(peer_drafts.valid_failure(value))
        for line in (None, True, "234", -1, 0, 10000):
            self.assertFalse(peer_drafts.valid_failure({"stage": "cli-import", "line": line}))

    def test_requires_every_direction_kind_and_boundary_without_private_fields(self):
        valid = {"kinds": ["agent", "mcp_server", "skill", "plugin"],
                 "directions": ["ui-to-cli", "cli-to-ui"], "newResources": 4,
                 "newVersions": 4, "contentOnly": 2, "independentlyPublished": 1,
                 "resourceInvocations": 0, "cleanup": "passed"}
        self.assertTrue(peer_drafts.valid_evidence(valid))
        for field in valid:
            missing = copy.deepcopy(valid)
            del missing[field]
            with self.subTest(missing=field):
                self.assertFalse(peer_drafts.valid_evidence(missing))
        for field, value in (("kinds", valid["kinds"][:-1]),
                             ("kinds", ["agent"] * 4),
                             ("directions", ["ui-to-cli"]),
                             ("newResources", 3), ("newVersions", 3),
                             ("contentOnly", 0), ("independentlyPublished", 0),
                             ("independentlyPublished", True), ("resourceInvocations", False),
                             ("resourceInvocations", 1), ("cleanup", "failed"),
                             ("token", "private-fixture-token")):
            with self.subTest(field=field, value=value):
                self.assertFalse(peer_drafts.valid_evidence({**valid, field: value}))
        for invalid in (None, [], True, {}):
            self.assertFalse(peer_drafts.valid_evidence(invalid))
