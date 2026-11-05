import contextlib
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import io
import threading
import time
import unittest
from unittest import mock
import urllib.request

import http_client


class Handler(BaseHTTPRequestHandler):
    followed = 0

    def log_message(self, _format, *_arguments):
        pass

    def do_GET(self):
        if self.path == "/followed":
            type(self).followed += 1
        status = 304 if self.path == "/not-modified" else 302 if self.path == "/redirect" else 503 if self.path in ("/error", "/truncated-error") else 200
        self.send_response(status)
        if self.path in ("/truncated", "/truncated-error"):
            self.send_header("Content-Length", "100")
        if self.path == "/declared-large":
            self.send_header("Content-Length", str(http_client.MAX_RESPONSE + 1))
        if self.path == "/not-modified":
            self.send_header("Content-Length", str(http_client.MAX_RESPONSE + 1))
        if self.path == "/duplicate-length":
            self.send_header("Content-Length", "14")
            self.send_header("Content-Length", "14")
        if self.path == "/ambiguous-framing":
            self.send_header("Content-Length", "14")
            self.send_header("Transfer-Encoding", "chunked")
        if self.path == "/negative-length":
            self.send_header("Content-Length", "-1")
        if self.path == "/large-headers":
            for index in range(70):
                self.send_header("X-Test-" + str(index), "x" * 1024)
        if status == 302:
            self.send_header("Location", "/followed")
        self.end_headers()
        if status == 304:
            return
        try:
            if self.path == "/slow":
                # Keep data flowing beyond the one-second request budget.
                # A socket inactivity timeout alone must not stop this stream.
                for _ in range(500):
                    self.wfile.write(b"x")
                    self.wfile.flush()
                    time.sleep(0.01)
            else:
                self.wfile.write(b"private-secret" * (100 if self.path in ("/large", "/error") else 1))
        except (BrokenPipeError, ConnectionResetError):
            pass


class HTTPClientTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        cls.thread = threading.Thread(target=cls.server.serve_forever, daemon=True)
        cls.thread.start()
        cls.base = "http://127.0.0.1:" + str(cls.server.server_port)

    @classmethod
    def tearDownClass(cls):
        cls.server.shutdown()
        cls.server.server_close()
        cls.thread.join(timeout=2)

    def request(self, path):
        return http_client.request(urllib.request.Request(self.base + path))

    def test_success_and_error_bytes_are_bounded_before_decode(self):
        for path in ("/large", "/error"):
            with self.subTest(path=path), mock.patch.object(http_client, "MAX_RESPONSE", 64):
                with self.assertRaisesRegex(http_client.ClientFailure, "response limit") as error:
                    self.request(path)
                self.assertNotIn("private-secret", str(error.exception))
        status, _, raw = self.request("/ok")
        self.assertEqual((status, raw), (200, b"private-secret"))
        status, _, raw = self.request("/error")
        self.assertEqual(status, 503)
        self.assertTrue(raw)

    def test_redirect_does_not_forward_credentials(self):
        request = urllib.request.Request(self.base + "/redirect", headers={"Authorization": "Bearer private-secret"})
        before = Handler.followed
        with self.assertRaisesRegex(http_client.ClientFailure, "redirect refused"):
            http_client.request(request)
        self.assertEqual(Handler.followed, before)

    def test_truncated_success_and_error_bodies_are_not_accepted(self):
        for path in ("/truncated", "/truncated-error"):
            with self.subTest(path=path), self.assertRaisesRegex(http_client.ClientFailure, "incomplete response"):
                self.request(path)

    def test_declared_oversize_fails_before_reading(self):
        with self.assertRaisesRegex(http_client.ClientFailure, "response limit"):
            self.request("/declared-large")

    def test_ambiguous_framing_and_large_headers_are_rejected(self):
        for path in ("/duplicate-length", "/ambiguous-framing", "/negative-length"):
            with self.subTest(path=path), self.assertRaisesRegex(http_client.ClientFailure, "invalid framing"):
                self.request(path)
        with self.assertRaisesRegex(http_client.ClientFailure, "header limit"):
            self.request("/large-headers")

    def test_304_representation_length_does_not_require_a_body(self):
        status, _, raw = self.request("/not-modified")
        self.assertEqual((status, raw), (304, b""))

    def test_slow_trickle_has_elapsed_deadline(self):
        started = time.monotonic()
        with mock.patch.object(http_client, "REQUEST_SECONDS", 1):
            with self.assertRaises(http_client.ClientFailure):
                self.request("/slow")
        # Allow host scheduling margin, but finish before the five-second body.
        self.assertLess(time.monotonic() - started, 3)

    def test_json_and_transport_diagnostics_are_safe(self):
        for raw in (b'{"secret":"private-secret","secret":2}', b'private-secret', b'NaN', b'{}{}'):
            with self.assertRaisesRegex(http_client.ClientFailure, "JSON response invalid") as error:
                http_client.decode(raw)
            self.assertNotIn("private-secret", str(error.exception))
        with mock.patch.object(urllib.request.OpenerDirector, "open", side_effect=OSError("private-secret")), \
                contextlib.redirect_stderr(io.StringIO()) as terminal:
            with self.assertRaisesRegex(http_client.ClientFailure, "transport failed") as error:
                self.request("/ok")
        self.assertNotIn("private-secret", str(error.exception) + terminal.getvalue())


if __name__ == "__main__":
    unittest.main()
