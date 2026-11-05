"""Serve only the owned Catalog manifest over TLS, with a local request counter."""

import http.client
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import ssl

UPSTREAM = "catalog.open-agentic-catalog-e2e.svc.cluster.local"
PATH = "/.well-known/ard.json"
LIMIT = 1024 * 1024


def manifest():
    connection = http.client.HTTPConnection(UPSTREAM, 8080, timeout=5)
    try:
        connection.request(
            "GET", PATH, headers={"Host": "127.0.0.1", "Accept": "application/json"}
        )
        response = connection.getresponse()
        raw = response.read(LIMIT + 1)
        if len(raw) > LIMIT:
            raise ValueError("Catalog manifest exceeds fixture bound")
        return response.status, response.getheader("Content-Type", ""), raw
    finally:
        connection.close()


class Handler(BaseHTTPRequestHandler):
    requests = 0

    def do_GET(self):
        if self.path == "/requests":
            status, media, raw = (
                200,
                "application/json",
                json.dumps({"requests": type(self).requests}).encode(),
            )
        elif self.path == PATH:
            type(self).requests += 1
            try:
                status, media, raw = manifest()
            except (OSError, ValueError, http.client.HTTPException):
                status, media, raw = 502, "text/plain", b"owned upstream unavailable"
        else:
            status, media, raw = 404, "text/plain", b"not found"
        self.send_response(status)
        self.send_header("Content-Type", media)
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def setup(self):
        super().setup()
        self.connection.settimeout(8)

    def log_message(self, *_):
        pass


def main():
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.minimum_version = ssl.TLSVersion.TLSv1_2
    context.load_cert_chain("/tls/catalog-origin.crt", "/tls/catalog-origin.key")
    with HTTPServer(("0.0.0.0", 8443), Handler) as server:
        server.socket = context.wrap_socket(server.socket, server_side=True)
        server.serve_forever()


if __name__ == "__main__":
    main()
