"""Restore controls use scripted ports only, without Docker or a model."""

import copy
import unittest
from unittest import mock

import semantic_restore as restore


class RestorePreparationTests(unittest.TestCase):
    def test_baseline_requires_real_semantic_only_expected_identities(self):
        expected = {"a": "local:1", "b": "local:2", "c": "local:3"}
        restore.validate_baseline(expected, {"a": "local:1"}, {})
        for actual, hybrid, lexical in (({}, {}, {}), (expected, {}, {}),
                (expected, {"a": "wrong"}, {}), (expected, {"extra": "local:4"}, {}),
                (expected, {"a": "local:1"}, {"a": "local:1"})):
            with self.subTest(actual=actual, hybrid=hybrid, lexical=lexical), self.assertRaisesRegex(RuntimeError, "baseline-missing"):
                restore.validate_baseline(actual, hybrid, lexical)

    def test_missing_baseline_never_stops_or_writes_a_database(self):
        run = mock.Mock()
        with self.assertRaisesRegex(RuntimeError, "baseline-missing"):
            restore.prepare(run, ["docker", "compose", "-p", "owned"], {}, "/unused", {"expected": {}, "hybrid": {}, "lexical": {}})
        run.assert_not_called()

    def test_shared_page_acceptance_rejects_integrity_and_missing_termination(self):
        page = {"formatVersion": 1, "checkSetVersion": 7, "recoveryPoint": "point", "tenantId": "tenant",
                "schemaVersion": 27, "privilegeManifestVersion": 12, "traversal": "continued", "integrity": "valid",
                "processed": 7, "decodedBytes": 100, "findings": [], "assuranceBoundary": "test", "continuation": "next"}
        terminal = {**page, "traversal": "complete", "continuation": ""}
        self.assertEqual(restore.pages.verify_chain(mock.Mock(side_effect=[page, terminal]), "tenant", "point", 27, 12), (2, set()))
        with self.assertRaisesRegex(ValueError, "pagination-exercise"):
            restore.pages.verify_chain(mock.Mock(return_value=terminal), "tenant", "point", 27, 12)
        self.assertEqual(restore.pages.verify_chain(mock.Mock(return_value=terminal), "tenant", "point", 27, 12, minimum_pages=1), (1, set()))
        for field, value in (("integrity", "invalid"), ("tenantId", "other"), ("schemaVersion", 28),
                             ("findings", [{"code": "UNKNOWN"}])):
            bad = copy.deepcopy(terminal)
            bad[field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                restore.pages.verify_chain(mock.Mock(side_effect=[page, bad]), "tenant", "point", 27, 12)

    def test_archive_helpers_do_not_import_another_runner_when_error_type_is_explicit(self):
        compose = mock.Mock(side_effect=[b"/tmp/catalog-archive.0123456789\n", b""])
        with restore.archive_tools.temporary_archive(compose, "restored", error_type=RuntimeError) as archive:
            self.assertEqual(archive, "/tmp/catalog-archive.0123456789/archive.dump")
        compose.assert_called_with("exec", "-T", "restored", "rm", "-r", "--", "/tmp/catalog-archive.0123456789")
        with self.assertRaisesRegex(RuntimeError, "invalid-container-archive"):
            with restore.archive_tools.temporary_archive(mock.Mock(return_value=b"/\n"), "restored", error_type=RuntimeError):
                self.fail("unsafe archive accepted")


class RestoredRuntimeTests(unittest.TestCase):
    def setUp(self):
        import semantic
        self.control_page = semantic.control_page
        self.expected = {"agent": "local:1", "mcp": "local:2", "skill": "local:3"}
        self.profile = "sha256:" + "a" * 64
        self.baseline = {"expected": self.expected, "hybrid": {"agent": "local:1"},
                         "profile": self.profile, "query": {"text": "purchase assistance", "limit": 3},
                         "documents": {"/api/v1/resources/resource": {"document": {"id": "resource"}, "etag": '"rev-1"'}}}
        self.identifier = "00000000-0000-7000-8000-000000000123"
        self.job = {"id": self.identifier, "kind": "semantic.reindex", "state": "succeeded",
                    "result": {"type": "semantic_index", "generationId": self.identifier, "members": 3}}

    def page(self, identities):
        return {"tier": "hybrid", "profile": self.profile, "durableTotal": len(identities),
                "partial": False, "partialIndex": False, "liveCount": 0,
                "items": [{"identifier": name, "id": identifier, "type": "local",
                           "local": {"release": {"id": identifier.removeprefix("local:")}}} for name, identifier in identities.items()]}

    def response(self, origin, path, body=None, token="local-admin", **kwargs):
        if kwargs.get("expected") in (403, 404):
            return {}, {}
        if path == "/api/v1/catalog/search":
            identities = self.expected if body["text"] == "order" else self.baseline["hybrid"]
            if token in ("local-template-service-client", "other-tenant-admin"):
                identities = {}
            return self.page(identities), {}
        if path == "/api/v1/catalog:reindex" or path.startswith("/api/v1/jobs/"):
            return self.job, {}
        if path == "/api/v1/capabilities":
            return {"search": {"indexing": {"state": "ready", "jobId": self.identifier, "indexed": 3, "failed": 0}}}, {}
        saved = self.baseline["documents"][path]
        return saved["document"], {"Etag": saved["etag"]}

    def test_restored_search_checks_both_readers_and_both_denied_identities(self):
        request = mock.Mock(side_effect=self.response)
        result = restore.verify_search("owned", self.baseline, request, self.control_page)
        self.assertEqual(result, {"administrator": {"agent": "local:1"}, "reader": {"agent": "local:1"}})
        self.assertEqual([call.kwargs.get("token") for call in request.call_args_list[:4]],
                         ["local-admin", "local-reader", "local-template-service-client", "other-tenant-admin"])
        for corruption in ("identity", "profile", "denial"):
            def bad(*args, **kwargs):
                page, headers = self.response(*args, **kwargs)
                if corruption == "identity" and page.get("items"):
                    page["items"][0]["identifier"] = "wrong"
                if corruption == "profile":
                    page["profile"] = "sha256:" + "b" * 64
                if corruption == "denial" and kwargs.get("token") == "other-tenant-admin":
                    page = self.page({"agent": "local:1"})
                return page, headers
            with self.subTest(corruption=corruption), self.assertRaises(RuntimeError):
                restore.verify_search("owned", self.baseline, bad, self.control_page)

    def test_document_comparison_preserves_payload_etag_and_cross_tenant_denial(self):
        request = mock.Mock(side_effect=self.response)
        restore.verify_documents("owned", self.baseline, request)
        request.assert_called_with("owned", "/api/v1/resources/resource", token="other-tenant-admin", expected=404)
        for value, headers in (({"id": "changed"}, {"ETag": '"rev-1"'}), ({"id": "resource"}, {"ETag": '"rev-2"'})):
            with self.subTest(value=value, headers=headers), self.assertRaisesRegex(RuntimeError, "document-changed"):
                restore.verify_documents("owned", self.baseline, mock.Mock(return_value=(value, headers)))

    def test_reindex_requires_success_exact_job_generation_and_member_count(self):
        restore.verify_reindex_job(self.job, self.identifier)
        for field, value in (("state", "failed"), ("id", "other"), ("kind", "other"),
                             ("result", {}), ("result", {**self.job["result"], "generationId": "other"}),
                             ("result", {**self.job["result"], "members": 0})):
            with self.subTest(field=field, value=value), self.assertRaisesRegex(RuntimeError, "not-complete"):
                restore.verify_reindex_job({**self.job, field: value}, self.identifier)

    def test_unverified_restore_never_starts_runtimes(self):
        run = mock.Mock()
        for report in ({}, {"stage": "restored-before-runtime", "sourceStopped": True, "destinationVerified": False}):
            with self.assertRaisesRegex(RuntimeError, "not-verified"):
                restore.resume(run, [], {}, report, "owned", None, None, None)
        run.assert_not_called()

    def test_resume_tests_restored_vectors_before_worker_and_keeps_source_dependencies_stopped(self):
        events = []
        def run(args, environment, **kwargs):
            if "up" in args:
                self.assertIn("--no-deps", args)
                self.assertIn("test/embeddings/semantic_restored_runtime.compose.yaml", args)
                events.append(args[-1])
            return ""
        def request(*args, **kwargs):
            events.append(args[1])
            return self.response(*args, **kwargs)
        def wait(action, predicate):
            value = action()
            self.assertTrue(predicate(value))
            return value
        report = {"stage": "restored-before-runtime", "sourceStopped": True, "destinationVerified": True, "baseline": self.baseline}
        value = restore.resume(run, ["compose", "-p", "owned-project"], {}, report, "owned", request, wait, self.control_page)
        self.assertEqual(value["stage"], "restored-search-and-reindex-complete")
        self.assertEqual(value["searchBeforeWorker"], value["searchAfterReindex"])
        self.assertLess(events.index("/api/v1/catalog/search"), events.index("worker"))
        self.assertTrue(value["authoritativeDocumentsUnchanged"])

    def test_original_restart_or_early_worker_blocks_qualification(self):
        report = {"stage": "restored-before-runtime", "sourceStopped": True, "destinationVerified": True, "baseline": self.baseline}
        for service in ("postgres", "worker"):
            def run(args, environment, **kwargs):
                return "unexpected-container" if "ps" in args and args[-1] == service else ""
            with self.subTest(service=service), self.assertRaises(RuntimeError):
                restore.resume(run, ["compose", "-p", "owned"], {}, report, "owned", None, None, None)

    def test_cleanup_attempts_image_removal_and_all_resource_checks_after_down_failure(self):
        calls = []
        def run(args, environment, **kwargs):
            calls.append(args)
            if "down" in args:
                raise RuntimeError("synthetic failure")
            return ""
        with self.assertRaisesRegex(RuntimeError, "cleanup-failed"):
            restore.cleanup(run, ["compose", "-p", "owned"], {"IMAGE": "owned-image"})
        self.assertIn(["docker", "image", "rm", "owned-image"], calls)
        self.assertEqual([args[1] for args in calls[2:]], ["container", "volume", "network"])
        self.assertIn("--all", calls[2])
        for kind in ("container", "volume", "network"):
            with self.subTest(kind=kind), self.assertRaisesRegex(RuntimeError, "owned-resources-remain"):
                restore.cleanup(lambda args, env, **kw: "leftover" if args[:2] == ["docker", kind] else "", ["compose", "-p", "owned"], {"IMAGE": "owned-image"})

    def test_resume_rejects_failed_job_or_another_active_generation(self):
        report = {"stage": "restored-before-runtime", "sourceStopped": True, "destinationVerified": True, "baseline": self.baseline}
        for fault in ("failed-job", "wrong-generation", "invalid-receipt"):
            def request(*args, **kwargs):
                value, headers = self.response(*args, **kwargs)
                if fault == "failed-job" and args[1].startswith("/api/v1/jobs/"):
                    value = {**value, "state": "failed"}
                if fault == "wrong-generation" and args[1] == "/api/v1/capabilities":
                    value = copy.deepcopy(value)
                    value["search"]["indexing"]["jobId"] = "other"
                if fault == "invalid-receipt" and args[1] == "/api/v1/catalog:reindex" and kwargs.get("expected") == 202:
                    value = {"id": "not-a-job", "kind": "semantic.reindex"}
                return value, headers
            def wait(action, predicate):
                value = action()
                self.assertTrue(predicate(value))
                return value
            with self.subTest(fault=fault), self.assertRaises(RuntimeError):
                restore.resume(mock.Mock(return_value=""), ["compose", "-p", "owned"], {}, report, "owned", request, wait, self.control_page)

    def test_cleanup_continues_after_image_and_inspection_errors(self):
        calls = []
        def run(args, environment, **kwargs):
            calls.append(args)
            if args[:3] == ["docker", "image", "rm"] or args[:3] == ["docker", "container", "ls"]:
                raise RuntimeError("synthetic cleanup failure")
            return ""
        with self.assertRaisesRegex(RuntimeError, "inspect-owned-resources,remove-owned-resources"):
            restore.cleanup(run, ["compose", "-p", "owned"], {"IMAGE": "owned-image"})
        self.assertEqual([args[1] for args in calls[2:]], ["container", "volume", "network"])
        self.assertTrue(all("label=com.docker.compose.project=owned" in args for args in calls[2:]))
