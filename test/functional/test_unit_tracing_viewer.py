"""Isolated checks for actual-cause selection and persisted Jaeger identities."""

import copy
import json
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
