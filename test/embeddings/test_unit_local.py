"""Local selection and HTTP-boundary tests without Docker, models or a database."""

import copy
import unittest
from unittest import mock
import urllib.request

import local
import semantic


class LocalInputsTests(unittest.TestCase):
    def test_semantic_refuses_missing_image_before_setup(self):
        environment = {"CATALOG_EMBEDDING_IMAGE": "fixture@sha256:" + "a" * 64}
        with mock.patch.object(semantic, "fixture_environment", return_value=environment), \
                mock.patch.object(semantic, "command", side_effect=RuntimeError("missing offline image")) as command, \
                mock.patch.object(semantic, "available_port") as port, \
                mock.patch.object(semantic.tempfile, "TemporaryDirectory") as directory:
            with self.assertRaisesRegex(RuntimeError, "missing offline image"):
                semantic.qualify()
        command.assert_called_once_with(["docker", "image", "inspect", "--format={{.Id}}", environment["CATALOG_EMBEDDING_IMAGE"]], environment)
        port.assert_not_called()
        directory.assert_not_called()

    def test_semantic_forwards_installed_browser_without_caller_credentials(self):
        supplied = {"PATH": "/installed/tools", "HOME": "/fixture/home", "PLAYWRIGHT_BROWSERS_PATH": "/installed/browsers",
                    "CATALOG_SDK_TOKEN": "unrelated-secret", "CATALOG_SEMANTIC_SEARCH": "caller-provider",
                    "CATALOG_ENV_FILE": "/caller/deployment.env"}
        inputs = local.inputs
        with mock.patch.dict(semantic.os.environ, supplied, clear=True), \
                mock.patch.object(semantic.local, "inputs", side_effect=lambda environment: inputs(environment, verify=False)):
            environment = semantic.fixture_environment()
        self.assertEqual(environment["PLAYWRIGHT_BROWSERS_PATH"], "/installed/browsers")
        self.assertEqual(environment["PATH"], "/installed/tools")
        self.assertEqual(environment["CATALOG_ENV_FILE"], "/dev/null")
        self.assertNotIn("CATALOG_SDK_TOKEN", environment)
        self.assertNotIn("CATALOG_SEMANTIC_SEARCH", environment)

    def test_offline_selection_does_not_enable_runtime(self):
        for name in ("minilm-l6-v1", "bge-small-v1"):
            result = local.inputs({"EMBEDDING_PROFILE": name}, verify=False)
            self.assertIn("@sha256:", result["CATALOG_EMBEDDING_IMAGE"])
            self.assertEqual(40, len(result["CATALOG_EMBEDDING_REVISION"]))
            self.assertNotIn("CATALOG_SEMANTIC_SEARCH", result)
        with self.assertRaises(ValueError):
            local.inputs({"EMBEDDING_PROFILE": "../unreviewed"}, verify=False)

    def test_fixture_redirect_cannot_forward_credentials(self):
        request = urllib.request.Request("http://127.0.0.1:1234/api/v1/capabilities", headers={"Authorization": "Bearer synthetic"})
        self.assertIsNone(semantic.NoRedirect().redirect_request(request, None, 302, "Found", {}, "https://other.invalid"))

    def test_index_wait_fails_immediately_when_owned_worker_stops(self):
        action = mock.Mock()
        guard = mock.Mock(side_effect=RuntimeError("owned worker stopped"))
        with self.assertRaisesRegex(RuntimeError, "owned worker stopped"):
            semantic.wait_until(action, lambda value: True, guard=guard)
        action.assert_not_called()
        guard.assert_called_once_with()

    def test_wait_accepts_success_only_before_deadline(self):
        for elapsed in (0.5, 1.0, 1.5):
            with self.subTest(elapsed=elapsed):
                clock = [0.0]

                def observe():
                    clock[0] = elapsed
                    return "ready"

                with mock.patch.object(semantic.time, "monotonic", side_effect=lambda: clock[0]), \
                        mock.patch.object(semantic.time, "sleep") as sleep:
                    if elapsed < 1:
                        self.assertEqual(semantic.wait_until(observe, lambda value: value == "ready", seconds=1), "ready")
                    else:
                        with self.assertRaisesRegex(RuntimeError, "did not converge"):
                            semantic.wait_until(observe, lambda value: value == "ready", seconds=1)
                sleep.assert_not_called()

    def test_wait_caps_sleep_and_does_not_observe_after_expiry(self):
        for cause in (None, OSError("transient fixture failure")):
            with self.subTest(cause=cause):
                clock = [0.0]

                def observe():
                    clock[0] = 0.9
                    if cause is not None:
                        raise cause
                    return "pending"

                def advance(delay):
                    clock[0] += delay

                with mock.patch.object(semantic.time, "monotonic", side_effect=lambda: clock[0]), \
                        mock.patch.object(semantic.time, "sleep", side_effect=advance) as sleep:
                    action = mock.Mock(side_effect=observe)
                    with self.assertRaisesRegex(RuntimeError, "did not converge"):
                        semantic.wait_until(action, lambda value: False, seconds=1)
                action.assert_called_once_with()
                self.assertEqual(sleep.call_count, 1)
                self.assertAlmostEqual(sleep.call_args.args[0], 0.1)

    def test_wait_recovers_from_transient_failure_within_deadline(self):
        for pending in ("pending", OSError("temporary connection failure"), RuntimeError("temporary status failure")):
            with self.subTest(pending=pending):
                clock = [0.0]

                def advance(delay):
                    clock[0] += delay

                action = mock.Mock(side_effect=[pending, "ready"])
                guard = mock.Mock()
                with mock.patch.object(semantic.time, "monotonic", side_effect=lambda: clock[0]), \
                        mock.patch.object(semantic.time, "sleep", side_effect=advance) as sleep:
                    self.assertEqual(semantic.wait_until(action, lambda value: value == "ready", seconds=1, guard=guard), "ready")
                self.assertEqual(action.call_count, 2)
                self.assertEqual(guard.call_count, 2)
                sleep.assert_called_once_with(0.25)

    def test_slow_guard_and_predicate_cannot_pass_expired_wait(self):
        for slow in ("guard", "predicate"):
            with self.subTest(slow=slow):
                clock = [0.0]

                def expire(*_args):
                    clock[0] = 1
                    return True

                action = mock.Mock(return_value="ready")
                with mock.patch.object(semantic.time, "monotonic", side_effect=lambda: clock[0]), \
                        mock.patch.object(semantic.time, "sleep") as sleep, \
                        self.assertRaisesRegex(RuntimeError, "did not converge"):
                    semantic.wait_until(action, expire if slow == "predicate" else lambda value: True,
                                        seconds=1, guard=expire if slow == "guard" else None)
                self.assertEqual(action.call_count, 0 if slow == "guard" else 1)
                sleep.assert_not_called()


class SemanticResultsTests(unittest.TestCase):
    def setUp(self):
        self.item = {"identifier": "urn:air:example.test:default:one", "id": "local:release-one",
                     "type": "local", "local": {"release": {"id": "release-one"}}}
        self.page = {"tier": "hybrid", "profile": "sha256:" + "a" * 64, "durableTotal": 3,
                     "liveCount": 0, "items": [self.item]}

    def test_control_page_requires_nonempty_exact_local_membership(self):
        self.assertEqual(semantic.control_page(self.page, 3, 1), {self.item["identifier"]: "local:release-one"})
        changes = [{"items": []}, {"durableTotal": 2}, {"tier": "core"}, {"profile": "wrong"},
                   {"liveCount": 1}, {"partial": True}, {"partialIndex": True}, {"fallbackReason": "indexing"}]
        for change in changes:
            with self.subTest(change=change), self.assertRaises(RuntimeError):
                semantic.control_page(self.page | change, 3, 1)
        for change in ({"type": "indexed"}, {"local": None}, {"id": "local:other"}):
            with self.subTest(change=change), self.assertRaises(RuntimeError):
                semantic.control_page(self.page | {"items": [self.item | change]}, 3, 1)
        for other in (self.item, self.item | {"identifier": "urn:air:example.test:default:two"}):
            with self.subTest(other=other), self.assertRaisesRegex(RuntimeError, "repeated"):
                semantic.control_page(self.page | {"items": [self.item, other]}, 3, 2)

    def test_lexical_and_empty_controls_have_explicit_mode(self):
        page = self.page | {"tier": "core", "profile": "core-lexical-v1", "fallbackReason": "provider_unavailable"}
        self.assertEqual(semantic.control_page(page, 3, 1, "core"), {self.item["identifier"]: "local:release-one"})
        self.assertEqual(semantic.control_page(page | {"items": [], "durableTotal": 0}, 0, 0, "core"), {})
        with self.assertRaises(RuntimeError):
            semantic.control_page(page | {"profile": self.page["profile"]}, 3, 1, "core")

    def test_public_pages_require_exact_sizes_unique_identities_and_termination(self):
        headers = {"Catalog-Search-Tier": "hybrid"}
        page = {"results": [{"identifier": self.item["identifier"]}], "pageToken": "next"}
        self.assertEqual(semantic.public_page(page, headers, 1, True), {self.item["identifier"]})
        terminal = {"results": page["results"]}
        self.assertEqual(semantic.public_page(terminal, headers, 1, False), {self.item["identifier"]})
        for change in ({"results": []}, {"results": page["results"] * 2}, {"pageToken": ""}, {"tier": "hybrid"}):
            with self.subTest(change=change), self.assertRaises(RuntimeError):
                semantic.public_page(page | change, headers, 1, True)
        with self.assertRaises(RuntimeError):
            semantic.public_page(page | {"results": page["results"] * 2}, headers, 2, True)
        with self.assertRaises(RuntimeError):
            semantic.public_page(page, headers, 1, False)
        for change in ({"Catalog-Search-Tier": "core"}, {"Catalog-Search-Partial-Index": "true"}):
            with self.subTest(change=change), self.assertRaises(RuntimeError):
                semantic.public_page(page, headers | change, 1, True)
        self.assertEqual(semantic.public_page(terminal, {"Catalog-Search-Tier": "core"}, 1, False, "core"), {self.item["identifier"]})

    def test_facet_counts_cannot_pass_on_shape_alone(self):
        facets = {"kind": {"buckets": [{"value": "agent", "count": 1}, {"value": "skill", "count": 2}]}}
        semantic.require_facets(facets, "kind", {"agent": 1, "skill": 2})
        bad = []
        for buckets in ([], [{"value": "agent", "count": 2}, {"value": "skill", "count": 1}],
                        facets["kind"]["buckets"] + [facets["kind"]["buckets"][0]]):
            bad.append({"kind": {"buckets": buckets}})
        overflow = copy.deepcopy(facets)
        overflow["kind"]["otherCount"] = 1
        bad.extend([overflow, facets | {"tag": {"buckets": []}}])
        for value in bad:
            with self.subTest(value=value), self.assertRaises(RuntimeError):
                semantic.require_facets(value, "kind", {"agent": 1, "skill": 2})


if __name__ == "__main__":
    unittest.main()
