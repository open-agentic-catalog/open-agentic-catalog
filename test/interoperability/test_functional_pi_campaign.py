"""Local HTTP boundaries for the Pi observer; never start Pi or a Catalog."""

from http.client import HTTPConnection
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import importlib.util
import json
from pathlib import Path
import threading
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("pi_campaign_fixture", Path(__file__).parent / "pi-ard/campaign.py")
fixture = importlib.util.module_from_spec(spec)
spec.loader.exec_module(fixture)


class CampaignFunctional(unittest.TestCase):
    def test_observer_preserves_native_post_bytes_and_reply_without_a_response_adapter(self):
        seen = []
        raw = b'{"results":[{"identifier":"urn:air:fixture.example:item:one"}]}\n'

        def upstream(method, path, body):
            seen.append((method, path, body))
            return 200, {"Content-Type": "application/json", "ETag": '"unchanged"'}, raw

        observations = []
        with patch.object(fixture, "exchange", upstream):
            listener = fixture.server(observations, "/private", port=0)
            thread = threading.Thread(target=listener.serve_forever)
            thread.start()
            client = HTTPConnection("127.0.0.1", listener.server_port, timeout=3)
            body = b'{"query": {"text": "exact spaces"}, "pageSize": 1}'
            try:
                client.request("POST", "/ard/v1/search", body=body, headers={"Content-Type": "application/json"})
                response = client.getresponse()
                self.assertEqual(response.status, 200)
                self.assertEqual(response.getheader("ETag"), '"unchanged"')
                self.assertEqual(response.read(), raw)
                self.assertEqual(seen, [("POST", "/ard/v1/search", body)])
                self.assertEqual(observations[0]["request"], json.loads(body))
                self.assertEqual(observations[0]["response"].encode(), raw)
            finally:
                client.close()
                listener.shutdown()
                listener.server_close()
                thread.join(timeout=3)
                self.assertFalse(thread.is_alive())

    def test_direct_reader_normalizes_header_names_and_keeps_exact_body(self):
        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *_args):
                pass

            def do_GET(self):
                self.send_response(200)
                self.send_header("ETag", '"exact"')
                self.send_header("Content-Length", "3")
                self.end_headers()
                self.wfile.write(b"{}\n")

        listener = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=listener.serve_forever)
        thread.start()
        try:
            with patch.object(
                fixture,
                "HTTPConnection",
                lambda *_args, **_kwargs: HTTPConnection("127.0.0.1", listener.server_port, timeout=3),
            ):
                status, headers, raw = fixture.exchange("GET", "/owned")
            self.assertEqual(status, 200)
            self.assertEqual(headers["etag"], '"exact"')
            self.assertEqual(raw, b"{}\n")
        finally:
            listener.shutdown()
            listener.server_close()
            thread.join(timeout=3)
            self.assertFalse(thread.is_alive())
