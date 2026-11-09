"""Isolated checks for actual-cause selection and persisted Jaeger identities."""

import copy
import json
from pathlib import Path
import tempfile
import unittest

import runner
import tracing_viewer as viewer


from tracing_viewer_fixtures import PARENT, REPLAY_PARENT, fixture_case, stored_trace

class ViewerTests(unittest.TestCase):
    def test_selected_is_explicit(self):
        self.assertFalse(viewer.selected({}))
        self.assertTrue(viewer.selected({"CATALOG_TRACING_VIEWER": "on"}))
        with self.assertRaises(runner.Failure):
            viewer.selected({"CATALOG_TRACING_VIEWER": "yes"})
        self.assertFalse(viewer.browser_selected({}))
        self.assertTrue(viewer.browser_selected({"CATALOG_TRACING_VIEWER": "on", "CATALOG_TRACING_BROWSER": "on"}))
        for environment in ({"CATALOG_TRACING_BROWSER": "on"}, {"CATALOG_TRACING_VIEWER": "on", "CATALOG_TRACING_BROWSER": "true"}):
            with self.assertRaisesRegex(runner.Failure, "tracing-browser-selection"):
                viewer.browser_selected(environment)

    def test_browser_completion_binds_exact_handoff_controls_window_and_cleanup(self):
        case = fixture_case()
        window = [60000001, 130000000]
        result = {"status": "passed", "browser": "pinned", "diagnostics": "passed", "searchWindowMicros": [60000000, 180000000],
                  "attemptTraceId": case["attempt"]["traceId"], "linkedTraceId": case["enqueue"]["traceId"],
                  "highlightedSpanId": case["enqueue"]["spanId"], "parentSpanId": case["request"]["spanId"], "wrongLinkRefused": True,
                  "controls": [{"selection": "wrongInstallation", "count": 0}, {"selection": "wrongService", "count": 0}, {"selection": "correct", "count": 1}]}
        for fault in (None, "identity", "control", "window", "link", "empty", "process"):
            with self.subTest(fault=fault), tempfile.TemporaryDirectory() as temporary:
                directory = Path(temporary)
                environment = {"CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY": str(directory)}
                value = copy.deepcopy(result)
                if fault == "identity":
                    value["highlightedSpanId"] = "0" * 16
                elif fault == "control":
                    value["controls"][1]["count"] = 1
                elif fault == "window":
                    value["searchWindowMicros"] = window
                elif fault == "link":
                    del value["wrongLinkRefused"]
                elif fault == "empty":
                    value = []
                def command(arguments, env, seconds, *, capture):
                    self.assertEqual(arguments, ["node", "test/tracing/browser.mjs", str(directory / "tracing-browser-input.json")])
                    self.assertEqual((env, seconds, capture), (environment, 180, True))
                    self.assertEqual(json.loads((directory / "tracing-browser-input.json").read_text()),
                                     {"origin": "http://127.0.0.1:1234", "spans": case, "window": window})
                    if fault == "process":
                        raise runner.Failure("child-failed")
                    return json.dumps(value).encode()
                if fault:
                    with self.assertRaises(runner.Failure):
                        viewer.qualify_browser(environment, command, {"queryUrl": "http://127.0.0.1:1234"}, case, window)
                else:
                    self.assertEqual(viewer.qualify_browser(environment, command, {"queryUrl": "http://127.0.0.1:1234"}, case, window), result)
                self.assertFalse((directory / "tracing-browser-input.json").exists())

    def test_handoff_requires_actual_request_enqueue_and_attempt(self):
        case = fixture_case()
        args = (PARENT, "api-instance", "worker-instance", "owned", REPLAY_PARENT)
        self.assertEqual(viewer.handoff(list(case.values()), *args), case)
        for fault in ("missing enqueue", "duplicate enqueue", "wrong cause", "wrong installation", "old process", "parented attempt", "wrong request", "missing sync", "missing replay", "new replay enqueue", "wrong replay parent"):
            with self.subTest(fault=fault):
                bad = copy.deepcopy(case)
                if fault == "missing enqueue":
                    del bad["enqueue"]
                elif fault == "duplicate enqueue":
                    bad["another"] = dict(bad["enqueue"])
                elif fault == "wrong cause":
                    bad["attempt"]["links"][0]["spanId"] = "0" * 16
                elif fault == "wrong installation":
                    bad["enqueue"]["installation"] = "other"
                elif fault == "old process":
                    bad["attempt"]["instance"] = "old-worker"
                elif fault == "parented attempt":
                    bad["attempt"]["parentId"] = "d" * 16
                elif fault == "wrong request":
                    bad["request"]["name"] = "GET /api/v1/namespaces"
                elif fault == "missing sync":
                    del bad["sync"]
                elif fault == "missing replay":
                    del bad["replay"]
                elif fault == "new replay enqueue":
                    bad["replay enqueue"] = dict(bad["enqueue"], traceId=REPLAY_PARENT[3:35])
                elif fault == "wrong replay parent":
                    bad["replay"]["parentId"] = "5" * 16
                with self.assertRaises(runner.Failure):
                    viewer.handoff(list(bad.values()), *args)

    def test_backend_identity_and_link_are_not_submission_counts(self):
        case = fixture_case()
        values = [case["attempt"], case["sync"]]
        good = stored_trace(values)
        viewer.retained_trace(good, values)
        for fault in ("missing data", "wrong trace", "missing span", "duplicate span", "wrong instance", "wrong installation", "wrong role", "wrong parent", "wrong link", "unknown operation", "query errors"):
            with self.subTest(fault=fault):
                bad = copy.deepcopy(good)
                trace = bad["data"][0]
                if fault == "missing data":
                    bad["data"] = []
                elif fault == "wrong trace":
                    trace["traceID"] = "0" * 32
                elif fault == "missing span":
                    trace["spans"] = trace["spans"][1:]
                elif fault == "duplicate span":
                    trace["spans"].append(dict(trace["spans"][0]))
                elif fault in ("wrong instance", "wrong installation"):
                    trace["processes"]["worker-instance"]["tags"][0 if fault == "wrong instance" else 1]["value"] = "other"
                elif fault == "wrong role":
                    trace["processes"]["worker-instance"]["serviceName"] = "open-agentic-catalog-api"
                elif fault == "wrong parent":
                    trace["spans"][1]["references"][0]["spanID"] = "0" * 16
                elif fault == "wrong link":
                    trace["spans"][0]["references"][0]["spanID"] = "0" * 16
                elif fault == "unknown operation":
                    trace["spans"][0]["operationName"] = "synthetic attempt"
                elif fault == "query errors":
                    bad["errors"] = ["failed"]
                with self.assertRaises(runner.Failure):
                    viewer.retained_trace(bad, values)

    def test_search_results_require_valid_bounded_trace_ids(self):
        self.assertEqual(viewer.search_trace_ids({"summaries": []}), set())
        self.assertEqual(viewer.search_trace_ids({"summaries": [{"traceId": "a" * 32}]}), {"a" * 32})
        for value in ({}, {"summaries": None}, {"summaries": [], "error": {"message": "failed"}}, {"summaries": [{"traceId": "bad"}]},
                      {"summaries": [{"traceId": "a" * 32}] * 2}, {"summaries": [{"traceId": "a" * 32}] * 21}):
            with self.assertRaises(runner.Failure):
                viewer.search_trace_ids(value)

    def test_only_pinned_loopback_viewer_is_selected(self):
        good = {"id": "a" * 64, "image": "reviewed@sha256:" + "b" * 64, "ports": {
            "16686/tcp": [{"HostIp": "127.0.0.1", "HostPort": "16687"}], "4318/tcp": [{"HostIp": "127.0.0.1", "HostPort": "14319"}]}}
        self.assertEqual(viewer.viewer_identity(good, good["image"]), ["http://127.0.0.1:16687", "http://127.0.0.1:14319"])
        for fault in ("image", "public", "port", "multiple"):
            bad = json.loads(json.dumps(good))
            if fault == "image":
                bad["image"] = "other"
            elif fault == "public":
                bad["ports"]["16686/tcp"][0]["HostIp"] = "0.0.0.0"
            elif fault == "port":
                bad["ports"]["4318/tcp"][0]["HostPort"] = "0"
            else:
                bad["ports"]["4318/tcp"].append(dict(bad["ports"]["4318/tcp"][0]))
            with self.assertRaises(runner.Failure):
                viewer.viewer_identity(bad, good["image"])


if __name__ == "__main__":
    unittest.main()
