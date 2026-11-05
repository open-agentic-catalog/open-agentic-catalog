"""Real local HTTP exchanges for forwarding and querying a test viewer."""

import base64
import contextlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import threading
import unittest
import urllib.parse
from unittest import mock

import runner
from http_client import ClientFailure
import tracing_viewer as viewer
from tracing_viewer_fixtures import fixture_case, stored_trace


class ViewerHTTPTests(unittest.TestCase):
    def test_relay_preserves_bytes_and_queries_exact_ids(self):
        for fault in ("", "partial visibility", "partial rejection", "ingest failure", "query failure", "redirect", "ignored filters", "filter failure", "filter not found"):
            with self.subTest(fault=fault), contextlib.ExitStack() as stack:
                case = fixture_case()
                payload = b"opaque fixture bytes, transport-only control"
                requests = []

                class Handler(BaseHTTPRequestHandler):
                    def log_message(self, *_args):
                        pass

                    def do_POST(self):
                        requests.append(("POST", self.path))
                        self.server.parent.assertEqual(self.path, "/v1/traces")
                        self.server.parent.assertEqual(self.headers.get("Content-Type"), "application/x-protobuf")
                        for name in ("Authorization", "Traceparent", "Tracestate", "Baggage"):
                            self.server.parent.assertIsNone(self.headers.get(name))
                        self.server.parent.assertEqual(self.rfile.read(int(self.headers["Content-Length"])), payload)
                        self.send_response(503 if fault == "ingest failure" else 302 if fault == "redirect" else 200)
                        if fault == "redirect":
                            self.send_header("Location", "/unexpected")
                        self.end_headers()
                        if fault in ("ingest failure", "redirect"):
                            self.wfile.write(b"private-failure-marker")
                        else:
                            self.wfile.write(b"\x0a\x02\x08\x01" if fault == "partial rejection" else b"\x0a\x00")

                    def do_GET(self):
                        requests.append(("GET", self.path))
                        parsed = urllib.parse.urlsplit(self.path)
                        filter_fault = parsed.path == "/api/v3/trace-summaries" and fault in ("filter failure", "filter not found")
                        self.send_response((404 if fault == "filter not found" else 503) if filter_fault else 500 if fault == "query failure" else 200)
                        self.end_headers()
                        if filter_fault:
                            self.wfile.write(b"private-failure-marker")
                            return
                        if parsed.path == "/api/v3/trace-summaries":
                            query = urllib.parse.parse_qs(parsed.query)
                            tags = json.loads(query["query.attributes"][0])
                            self.server.parent.assertEqual(query["query.searchDepth"], ["20"])
                            self.server.parent.assertLess(query["query.startTimeMin"][0], query["query.startTimeMax"][0])
                            matches = [span for span in case.values() if span["service"] == query["query.serviceName"][0]
                                       and span["name"] == query["query.operationName"][0] and span["installation"] == tags["service.namespace"]
                                       and span["instance"] == tags["service.instance.id"]]
                            if fault == "ignored filters":
                                matches = [case["request"]]
                            response = {"summaries": [{"traceId": span["traceId"]} for span in matches]}
                        else:
                            values = ((case["request"], case["enqueue"]) if parsed.path == "/api/traces/" + "a" * 32
                                      else (case["attempt"], case["sync"]) if parsed.path == "/api/traces/" + "e" * 32 else (case["replay"],))
                            if fault == "partial visibility" and sum(method == "GET" for method, _ in requests) == 1:
                                values = values[:1]
                            response = stored_trace(values)
                        self.wfile.write(json.dumps(response).encode())

                endpoints = []
                for _ in range(2):
                    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
                    server.parent = self
                    thread = threading.Thread(target=server.serve_forever, daemon=True)
                    thread.start()
                    stack.callback(server.server_close)
                    stack.callback(thread.join)
                    stack.callback(server.shutdown)
                    endpoints.append("http://127.0.0.1:" + str(server.server_address[1]))
                backend = {"queryUrl": endpoints[0], "otlpUrl": endpoints[1]}
                with mock.patch.object(viewer, "owned_viewer", return_value=backend):
                    args = ({}, None, {"batches": [base64.b64encode(payload).decode()]}, case,
                            {"id": "job", "kind": "source.sync", "state": "succeeded"}, {"id": "source"},
                            {"instanceId": "api-instance"}, {"instanceId": "worker-instance"})
                    if fault not in ("", "partial visibility"):
                        with self.assertRaises((runner.Failure, ClientFailure)) as error:
                            viewer.qualify(*args)
                        self.assertNotIn("private-failure-marker", str(error.exception))
                        if fault in ("filter failure", "filter not found"):
                            self.assertEqual(str(error.exception), "tracing-viewer-filter-query-failed")
                        elif fault == "ignored filters":
                            self.assertEqual(str(error.exception), "tracing-viewer-filter-mismatch")
                    else:
                        evidence = viewer.qualify(*args)
                        self.assertEqual(evidence["status"], "passed")
                        self.assertEqual(evidence["spans"], case)
                        self.assertEqual(sum(method == "GET" for method, _ in requests), 10 if fault == "partial visibility" else 9)
                        self.assertEqual(len(evidence["filterControls"]), 2)
                        for control in evidence["filterControls"]:
                            self.assertEqual(control["wrongInstallation"], 0)
                            self.assertEqual(control["wrongService"], 0)
                        self.assertEqual(evidence["filterControls"][0]["correct"], 2)
                        self.assertEqual(evidence["filterControls"][1]["correct"], 1)
                self.assertNotIn(("POST", "/unexpected"), requests)


if __name__ == "__main__":
    unittest.main()
