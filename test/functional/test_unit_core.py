import base64
import copy
import hashlib
import io
import unittest
from unittest.mock import Mock, patch
import zipfile

import core
import operations


class CoreJourneyTests(unittest.TestCase):
    def test_compact_browse_binds_complete_exact_record(self):
        summary = {section: {"id": section} for section in
                   ("resource", "version", "versionRevision", "listing", "listingRevision", "release", "evidence")}
        summary["versionRevision"]["contentSetDigest"] = "sha256:fixture"
        summary["evidence"].update(trust={"state": "not_assessed"}, legalRights={"state": "not_assessed"})
        exact = copy.deepcopy(summary)
        exact["versionRevision"]["representations"] = [{"data": {"retained": True}}]
        exact["evidence"].update(findings=[{"code": "RETAINED"}], trust={"state": "not_assessed", "reason": "detail only"})
        api = Mock()
        api.request.return_value = exact, {}
        self.assertEqual(core.exact_record(api, summary), exact)
        api.request.assert_called_once_with("GET", "/api/v1/catalog/resources/release")
        for section in summary:
            changed = copy.deepcopy(summary)
            changed[section]["id"] = "wrong"
            with self.subTest(section=section), self.assertRaises(AssertionError):
                core.exact_record(api, changed)
        for changed in (dict(summary, versionRevision=exact["versionRevision"]), dict(summary, evidence=exact["evidence"])):
            with self.assertRaisesRegex(AssertionError, "document bodies"):
                core.exact_record(api, changed)
        exact["versionRevision"]["representations"] = []
        with self.assertRaisesRegex(AssertionError, "lost retained documents"):
            core.exact_record(api, summary)

    def test_plugin_admission_rejects_changed_readback(self):
        resource = {"id": "plugin", "kind": "plugin", "createdAt": "2026-09-12T00:00:00Z", "updatedAt": "2026-09-12T00:00:00Z"}
        for difference in ({"kind": "skill"}, {"id": "different"}, {"createdAt": "2026-09-12T00:00:01Z"}):
            with self.subTest(difference=difference), patch.object(core, "mutate", return_value=(resource, {})):
                api = Mock()
                api.request.return_value = dict(resource, **difference), {}
                with self.assertRaisesRegex(AssertionError, "readback changed"):
                    core.plugin_admission(api, Mock(), {"resource": {"namespaceId": "namespace"}}, "run")

    def test_plugin_admission_detects_partial_writes_before_cleanup(self):
        resource = {"id": "plugin", "kind": "plugin", "createdAt": "2026-09-12T00:00:00.123456789Z", "updatedAt": "2026-09-12T00:00:00.123456789Z"}
        for page in ({"items": []}, {"items": [{"id": "unexpected"}]}, {"items": [], "nextCursor": "unexpected"}):
            with self.subTest(page=page), patch.object(core, "mutate", return_value=(resource, {"ETag": '"one"'})) as mutate:
                api, other = Mock(), Mock()

                def request(method, path, *args, **kwargs):
                    if method == "GET" and path.endswith("/versions"):
                        return page, {}
                    return dict(resource, createdAt="2026-09-12T00:00:00.123456Z", updatedAt="2026-09-12T00:00:00.123456Z"), {}

                api.request.side_effect = request
                if page == {"items": []}:
                    core.plugin_admission(api, other, {"resource": {"namespaceId": "namespace"}}, "run")
                    self.assertEqual(mutate.call_args.args[1:3], ("POST", "/api/v1/resources/plugin:delete"))
                else:
                    with self.assertRaisesRegex(AssertionError, "persisted a version"):
                        core.plugin_admission(api, other, {"resource": {"namespaceId": "namespace"}}, "run")
                    self.assertEqual(mutate.call_count, 1)

    def test_history_checks_known_content_and_preserves_filters(self):
        api = Mock()
        expected = [{"id": "one", "digest": "first"}, {"id": "two", "digest": "second"}]
        api.request.side_effect = [({"items": [expected[0]], "nextCursor": "next"}, {}), ({"items": [expected[1]]}, {})]
        self.assertEqual(core.history(api, "/history", expected, state="approved"), expected)
        self.assertEqual(api.request.call_args_list[0].args, ("GET", "/history?state=approved&limit=1"))
        self.assertEqual(api.request.call_args_list[1].args, ("GET", "/history?state=approved&limit=1&cursor=next"))

    def test_history_rejects_missing_duplicate_wrong_content_and_unbounded_pages(self):
        expected = [{"id": "one", "digest": "first"}, {"id": "two", "digest": "second"}]
        for pages in (
            [{"items": [expected[0]]}],
            [{"items": [expected[0]], "nextCursor": "next"}, {"items": [expected[0]]}],
            [{"items": [{"id": "one", "digest": "wrong"}], "nextCursor": "next"}, {"items": [expected[1]]}],
            [{"items": [expected[1]], "nextCursor": "next"}, {"items": [expected[0]]}],
            [{"items": expected}],
            [{"items": [], "nextCursor": "same"}] * 3,
            [{"items": [], "nextCursor": str(i)} for i in range(3)],
        ):
            with self.subTest(pages=pages):
                api = Mock()
                api.request.side_effect = [(page, {}) for page in pages]
                with self.assertRaises(AssertionError):
                    core.history(api, "/history", expected)
        api = Mock()
        api.request.return_value = ({"items": []}, {})
        self.assertEqual(core.history(api, "/history", []), [])
        with self.assertRaises(AssertionError):
            core.history(api, "/history", [{"id": str(i)} for i in range(9)])

    def test_replay_binds_body_and_validator(self):
        api = Mock()
        api.request.return_value = ({"id": "owned"}, {"Etag": '"rev-1"', "Idempotency-Replayed": "true"})
        core.mutate(api, "POST", "/api/v1/namespaces", {}, 201)
        self.assertEqual(api.request.call_args_list[0], api.request.call_args_list[1])
        for changed in (({"id": "different"}, {"ETag": '"rev-1"', "Idempotency-Replayed": "true"}),
                        ({"id": "owned"}, {"ETag": '"rev-2"', "Idempotency-Replayed": "true"}),
                        ({"id": "owned"}, {"ETag": '"rev-1"'})):
            api.request.side_effect = [({"id": "owned"}, {"ETag": '"rev-1"'}), changed]
            with self.assertRaises(AssertionError):
                core.mutate(api, "POST", "/api/v1/namespaces", {}, 201)

    def test_pagination_cannot_loop_or_lie_about_total(self):
        api = Mock()
        api.request.side_effect = [({"items": [{"id": "one"}], "total": 2, "nextCursor": "next"}, {}), ({"items": [{"id": "two"}], "total": 2}, {})]
        self.assertEqual(core.records(api), [{"id": "one"}, {"id": "two"}])
        for page in ({"items": [], "total": 1}, {"items": [], "total": 1, "nextCursor": "loop"}, {"items": [1, 2], "total": 2}):
            api.request.side_effect = None
            api.request.return_value = (page, {})
            with self.assertRaises(AssertionError):
                core.records(api)

    def test_archive_is_only_inert_input(self):
        self.assertNotEqual(core.skill_package(), core.skill_package(revised=True))
        with zipfile.ZipFile(io.BytesIO(base64.b64decode(core.skill_package()))) as archive:
            self.assertEqual(archive.namelist(), ["safe-skill/SKILL.md", "safe-skill/scripts/run.sh"])
            self.assertIn(b"must-not-run", archive.read("safe-skill/scripts/run.sh"))
            self.assertNotIn(b"compatibility:", archive.read("safe-skill/SKILL.md"))
        with zipfile.ZipFile(io.BytesIO(base64.b64decode(core.skill_package(invalid=True)))) as archive:
            self.assertIn(b'compatibility: ""\n', archive.read("safe-skill/SKILL.md"))
            self.assertEqual(len(archive.namelist()), 2)

    def test_blocked_package_rejects_false_success_drift_and_partial_inventory(self):
        encoded = core.skill_package(invalid=True)
        archive = base64.b64decode(encoded)
        value = {"evidence": {"state": "blocked", "findings": [{"code": "SKILL_COMPATIBILITY_INVALID", "severity": "error",
                  "path": "/representations/0/package/SKILL.md/frontmatter/compatibility"}]},
                 "revision": {"representations": [{"contentDigest": "sha256:" + hashlib.sha256(archive).hexdigest(), "size": len(archive),
                              "validationState": "invalid", "package": {"structureState": "failed", "fileCount": 0, "files": []}}]}}
        core.blocked_package(value, encoded)
        for keys, replacement in (
            (("evidence", "state"), "complete"),
            (("evidence", "findings"), []),
            (("evidence", "findings", 0, "path"), "/unrelated"),
            (("evidence", "findings", 0, "severity"), "warning"),
            (("revision", "representations", 0, "contentDigest"), "sha256:" + "0" * 64),
            (("revision", "representations", 0, "size"), len(archive) + 1),
            (("revision", "representations", 0, "validationState"), "valid"),
            (("revision", "representations", 0, "package", "structureState"), "passed"),
            (("revision", "representations", 0, "package", "fileCount"), 1),
            (("revision", "representations", 0, "package", "files"), [{"path": "partial"}]),
            (("revision", "representations", 0, "normalizedFacts"), {"name": "partial"}),
            (("packageData",), encoded),
        ):
            with self.subTest(keys=keys):
                changed = copy.deepcopy(value)
                target = changed
                for key in keys[:-1]:
                    target = target[key]
                target[keys[-1]] = replacement
                with self.assertRaises(AssertionError):
                    core.blocked_package(changed, encoded)

    def test_historical_observation_is_exact_and_bounded(self):
        api = Mock()
        api.request.return_value = ({"items": [{"id": "other"}, {"id": "owned"}]}, {})
        self.assertEqual(core.historical_observation(api, "owned"), {"id": "owned"})
        for page in ({"items": []}, {"items": [{"id": "owned"}] * 2}, {"items": [{"id": "owned"}], "nextCursor": "more"}):
            api.request.return_value = (page, {})
            with self.assertRaises(AssertionError):
                core.historical_observation(api, "owned")

    def test_worker_contract_rejects_leaks_and_false_recovery(self):
        value = {"status": "ready", "role": "worker", "state": "running", "ready": True, "degraded": False,
                 "components": dict.fromkeys(operations.COMPONENTS, "running"), "dependencies": {"postgresql": True},
                 "version": "functional", "commit": "commit", "sourceState": "unknown", "instanceId": "00000000-0000-7000-8000-000000000001", "startedAt": "2026-09-01T12:00:00Z", "observedAt": "2026-09-01T11:00:00Z", "uptimeSeconds": 1, "housekeeping": {"interval": "1m", "terminalJobRetention": "24h", "acknowledgedOutboxRetention": "1h", "batchLimit": 100}}
        operations.check_status(value, "commit", True)
        for field, wrong in (("extra", "private-secret"), ("ready", False), ("commit", "wrong"), ("dependencies", {"postgresql": False}), ("components", {"private-id": "running"})):
            changed = copy.deepcopy(value)
            changed[field] = wrong
            with self.assertRaises(AssertionError):
                operations.check_status(changed, "commit", True)
        value.update(status="not_ready", ready=False, degraded=True, dependencies={"postgresql": False})
        operations.check_status(value, "commit", False)
        value["components"]["housekeeper"] = "degraded"
        operations.check_status(value, "commit", False)
        value.update(status="degraded", ready=True, dependencies={"postgresql": True})
        operations.check_status(value, "commit", True)
        for component, state in (("housekeeper", "stopped"), ("job_worker", "starting"), ("operations_server", "degraded")):
            changed = copy.deepcopy(value)
            changed["components"][component] = state
            with self.assertRaises(AssertionError):
                operations.check_status(changed, "commit", True)
        for field, wrong in (("status", "ready"), ("degraded", False)):
            with self.assertRaises(AssertionError):
                operations.check_status(value | {field: wrong}, "commit", True)
