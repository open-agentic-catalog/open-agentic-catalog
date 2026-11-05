"""Owned HTTP/TLS listeners and OpenSSL subprocesses: functional tests only."""
import asyncio
import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import os
from pathlib import Path
import socket
import ssl
import subprocess
import tempfile
import threading
import time
import unittest
from unittest.mock import Mock, patch

from catalog_sdk import Client, Config, SDKError
from catalog_sdk.generated.api.service_api import ServiceApi
from catalog_sdk.generated.api.discovery_api import DiscoveryApi


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    calls = []

    def log_message(self, *_):
        pass

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        self.server.calls.append((self.path, self.headers, body))
        if self.path == "/lost":
            self.connection.shutdown(socket.SHUT_RDWR)
            self.connection.close()
            self.close_connection = True
        else:
            self.do_GET(record=False)

    def do_GET(self, record=True):
        if record:
            self.server.calls.append((self.path, self.headers, b""))
        if self.path == "/api/v1/capabilities":
            self.send_response(self.server.scenario_status)
            self.send_header("Content-Type", "application/problem+json")
            self.send_header("Retry-After", "2")
            self.send_header("X-Request-ID", "typed-response")
            data = self.server.scenario_body
        elif self.path == "/redirect":
            self.send_response(302)
            self.send_header("Location", self.server.redirect)
            data = b""
        elif self.path == "/wait":
            self.send_response(200)
            self.send_header("Content-Length", "1")
            self.end_headers()
            self.server.body_waiting.set()
            self.connection.settimeout(5)
            try:
                if self.rfile.read(1) == b"":
                    self.server.client_closed.set()
            except TimeoutError:
                pass
            self.close_connection = True
            return
        elif self.path == "/slow":
            self.send_response(200)
            self.send_header("Content-Length", "20")
            self.send_header("X-Request-ID", "slow")
            self.end_headers()
            try:
                for _ in range(20):
                    self.wfile.write(b"x")
                    self.wfile.flush()
                    time.sleep(0.03)
            except (BrokenPipeError, ConnectionResetError):
                pass
            return
        elif self.path == "/truncate":
            self.send_response(200)
            self.send_header("Content-Length", "100")
            self.end_headers()
            self.wfile.write(b"short")
            self.wfile.flush()
            self.connection.shutdown(socket.SHUT_RDWR)
            self.close_connection = True
            return
        else:
            self.send_response(200)
            data = b"x" * 33 if self.path in ("/large", "/unknown-large") else b"ok"
            if self.path == "/unknown-large":
                self.send_header("Connection", "close")
                self.close_connection = True
            if self.path == "/cookie":
                self.send_header("Set-Cookie", "private=session; Path=/")
            if self.path == "/headers":
                self.send_header("X-Large", "x" * 40000)
            if self.path == "/encoding":
                self.send_header("Content-Encoding", "gzip")
            if self.path == "/version":
                self.send_header("Content-Type", "application/json")
                data = b'{"version":"0.1.0","commit":"unknown","sourceState":"unknown","service":"open-agentic-catalog","ardProfile":"v0.91","aiCatalogProfile":"v1"}'
                if self.server.null_version:
                    data = b"null"
            if self.path.startswith("/ard/v1/agents"):
                self.send_header("Content-Type", "application/json")
                self.send_header("ETag", '"anonymous-page"')
                data = json.dumps({"items":[{"identifier":"urn:air:example.com:ns:agent","displayName":"Agent","type":"application/json","data":{"exact":9007199254740993},"additional_properties":{"must":"stay nested"},"future":None,**({"@context":{"@version":1.1}} if self.server.ard_context_present else {})}],"total":2,"pageToken":"opaque-next"}).encode()
        if self.path != "/unknown-large":
            self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)


class TransportFunctional(unittest.TestCase):
    def setUp(self):
        self.servers = []

    def tearDown(self):
        for server, thread in reversed(self.servers):
            server.shutdown()
            server.server_close()
            thread.join(timeout=3)
            self.assertFalse(thread.is_alive())

    def server(self, context=None):
        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        server.calls = []
        server.redirect = ""
        server.null_version = False
        server.ard_context_present = True
        server.scenario_status = 503
        server.scenario_body = b""
        server.body_waiting = threading.Event()
        server.client_closed = threading.Event()
        if context:
            server.socket = context.wrap_socket(server.socket, server_side=True)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        self.servers.append((server, thread))
        return server, server.server_address[1]

    def test_no_redirect_proxy_cookies_or_mutation_retry(self):
        destination, target_port = self.server()
        server, port = self.server()
        server.redirect = f"http://127.0.0.1:{target_port}/credential-sink"
        provider = Mock(return_value="explicit")
        with self.assertRaises(SDKError) as caught:
            invalid = Client(Config(origin=f"http://127.0.0.1:{port}", allow_loopback_http="false", token_provider=provider))
            invalid.raw_sync("GET", "/version")
        self.assertEqual(caught.exception.code, "invalid_configuration")
        provider.assert_not_called()
        self.assertEqual(server.calls, [])
        c = Client(Config(origin=f"http://127.0.0.1:{port}", allow_loopback_http=True, token="explicit"))
        poison = f"http://127.0.0.1:{target_port}"
        with patch.dict(os.environ, {"HTTP_PROXY":poison, "HTTPS_PROXY":poison, "ALL_PROXY":poison, "NO_PROXY":""}):
            self.assertEqual(c.raw_sync("GET", "/redirect").status, 302)
            for body, headers in ((b"command", {"Idempotency-Key":"original"}), (b"", {})):
                with self.assertRaises(SDKError) as caught:
                    c.raw_sync("POST", "/lost", body=body, headers=headers)
                self.assertEqual(caught.exception.code, "transport_failed")
        self.assertEqual(len(destination.calls), 0)
        commands = [call for call in server.calls if call[0] == "/lost"]
        self.assertEqual(len(commands), 2)
        self.assertEqual([call[2] for call in commands], [b"command", b""])
        self.assertEqual(commands[0][1]["Idempotency-Key"], "original")
        self.assertNotIn("Idempotency-Key", commands[1][1])
        self.assertEqual(c.raw_sync("GET", "/cookie").headers["Set-Cookie"], "private=session; Path=/")
        self.assertEqual(c.raw_sync("GET", "/").data, b"ok")
        self.assertTrue(all("Cookie" not in headers for _, headers, _ in server.calls))

    def test_real_body_header_encoding_and_whole_call_deadline(self):
        _, port = self.server()
        c = Client(Config(origin=f"http://127.0.0.1:{port}", allow_loopback_http=True, response_bytes=32, timeout=0.15, phase_timeout=1.0))
        for path, code in (("/large", "response_too_large"), ("/unknown-large", "response_too_large"), ("/headers", "headers_too_large"), ("/encoding", "encoded_response"), ("/truncate", "transport_failed"), ("/slow", "deadline_exceeded")):
            with self.subTest(path=path), self.assertRaises(SDKError) as caught:
                c.raw_sync("GET", path)
            self.assertEqual(caught.exception.code, code)
            if path == "/slow":
                self.assertEqual(caught.exception.request_id, "slow")
                self.assertEqual(caught.exception.status, 200)
        self.assertEqual(c.raw_sync("GET", "/").data, b"ok")

    def test_generated_problem_hints_and_malformed_body_do_not_retry(self):
        server, port = self.server()
        client = Client(Config(origin=f"http://127.0.0.1:{port}", allow_loopback_http=True, token="explicit"))
        api = ServiceApi(client)
        for status in (429, 503):
            server.scenario_status = status
            server.scenario_body = json.dumps({"type":"about:blank","title":"Unavailable","status":status,"code":"DEPENDENCY_UNAVAILABLE","detail":"PRIVATE_BODY_SENTINEL","requestId":"typed-response"}).encode()
            response = api.get_capabilities_sync_with_http_info()
            self.assertEqual(response.status_code, status)
            self.assertEqual(response.data.code, "DEPENDENCY_UNAVAILABLE")
            self.assertEqual(response.headers["Retry-After"], "2")
            self.assertEqual(response.headers["X-Request-ID"], "typed-response")
            self.assertEqual(len(server.calls), 1 if status == 429 else 2)
        server.scenario_body = b"PRIVATE_INVALID_JSON_SENTINEL"
        with self.assertRaises(SDKError) as caught:
            api.get_capabilities_sync_with_http_info()
        self.assertEqual(caught.exception.code, "invalid_response")
        self.assertEqual(caught.exception.status, 503)
        self.assertEqual(caught.exception.request_id, "typed-response")
        self.assertNotIn("PRIVATE", str(caught.exception))
        self.assertEqual(len(server.calls), 3)
        self.assertEqual(api.get_version_sync_with_http_info().status_code, 200)
        self.assertEqual([path for path, _, _ in server.calls], ["/api/v1/capabilities"] * 3 + ["/version"])

    def test_real_cancellation_closes_connection_and_allows_next_request(self):
        server, port = self.server()
        client = Client(Config(origin=f"http://127.0.0.1:{port}", allow_loopback_http=True))
        async def run():
            task = asyncio.create_task(client.raw("GET", "/wait"))
            try:
                self.assertTrue(await asyncio.to_thread(server.body_waiting.wait, 3), "Server did not start the response")
                task.cancel()
                with self.assertRaises(asyncio.CancelledError):
                    await task
                self.assertTrue(await asyncio.to_thread(server.client_closed.wait, 3), "Cancellation did not close the connection")
                self.assertEqual((await client.raw("GET", "/")).data, b"ok")
            finally:
                task.cancel()
                await asyncio.gather(task, return_exceptions=True)
        asyncio.run(run())
        self.assertEqual([path for path, _, _ in server.calls], ["/wait", "/"])

    def test_verified_tls_private_ca_hostname_and_generated_sync_async(self):
        with tempfile.TemporaryDirectory(prefix="catalog-sdk-tls-") as directory:
            cert, key = Path(directory) / "cert.pem", Path(directory) / "key.pem"
            subprocess.run(["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-days", "1", "-subj", "/CN=localhost", "-addext", "subjectAltName=DNS:localhost", "-keyout", str(key), "-out", str(cert)], check=True, capture_output=True, timeout=20)
            context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            context.load_cert_chain(cert, key)
            server, port = self.server(context)
            origin = f"https://localhost:{port}"
            provider = Mock(return_value="current")
            for suffix in ("?", "#"):
                with self.subTest(origin_suffix=suffix):
                    with self.assertRaises(SDKError) as caught:
                        client = Client(Config(origin=origin + suffix, ca_file=str(cert), token_provider=provider))
                        ServiceApi(client).get_version_sync_with_http_info()
                    self.assertEqual(caught.exception.code, "invalid_configuration")
            provider.assert_not_called()
            self.assertEqual(server.calls, [])
            for config in (Config(origin=origin), Config(origin=f"https://127.0.0.1:{port}", ca_file=str(cert))):
                with self.assertRaises(SDKError):
                    Client(config).raw_sync("GET", "/version")
            c = Client(Config(origin=origin, ca_file=str(cert), token_provider=provider))
            self.assertEqual(ServiceApi(c).get_version_sync_with_http_info().data.version, "0.1.0")
            self.assertEqual(asyncio.run(ServiceApi(c).get_version_with_http_info()).data.version, "0.1.0")
            self.assertEqual(len(server.calls), 2)
            self.assertTrue(all(call[1]["Authorization"] == "Bearer current" for call in server.calls))
            server.null_version = True
            with self.assertRaises(SDKError) as caught:
                ServiceApi(c).get_version_sync_with_http_info()
            self.assertEqual(caught.exception.code, "invalid_response")
            self.assertEqual(caught.exception.status, 200)
            server.null_version = False
            self.assertEqual(ServiceApi(c).get_version_sync_with_http_info().data.version, "0.1.0")
            self.assertEqual(len(server.calls), 4)
            self.assertEqual(provider.call_count, 4)

    def test_anonymous_ard_page_keeps_nested_entry_and_continuation(self):
        server, port = self.server()
        client = Client(Config(origin=f"http://127.0.0.1:{port}", allow_loopback_http=True))
        for present in (True, False):
            with self.subTest(context_present=present):
                server.ard_context_present = present
                result = DiscoveryApi(client).list_ard_registry_entries_sync_with_http_info()
                self.assertEqual(result.status_code, 200)
                self.assertEqual(result.headers["ETag"], '"anonymous-page"')
                value = result.data.items[0].to_dict()
                self.assertEqual(value["data"]["exact"], 9007199254740993)
                self.assertEqual(value["additional_properties"], {"must":"stay nested"})
                self.assertIn("future", value)
                self.assertIsNone(value["future"])
                self.assertEqual("@context" in value, present)
                if present:
                    self.assertEqual(value["@context"], {"@version":1.1})
                self.assertEqual(result.data.page_token, "opaque-next")
        self.assertEqual(len(server.calls), 2)
        self.assertTrue(all("Authorization" not in headers for _, headers, _ in server.calls))
