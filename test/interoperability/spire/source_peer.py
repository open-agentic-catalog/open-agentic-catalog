"""Private TLS fixture. Normal reads forward to A; the nested test adds one same-origin parent."""
import base64
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import ssl
import time

CONTROL = Path("/controls")
MANIFEST = "/.well-known/ard.json"
HOST = "catalog-a.test:8443"


class Peer(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def reject(self):
        # Identify the standard predecessor probe without logging arbitrary paths, headers or bodies.
        path = self.path if self.path == "/.well-known/ai-catalog.json" else "other"
        print(json.dumps({"path": path, "status": 404, "authenticated": "Authorization" in self.headers,
                          "conditional": False, "vary": []}), flush=True)
        self.send_error(404)

    do_POST = do_HEAD = reject

    def do_GET(self):
        if self.path not in (MANIFEST, "/child.json", "/public.json"):
            self.reject()
            return
        headers = {key: self.headers[key] for key in ("Authorization", "If-None-Match", "If-Modified-Since", "Accept")
                   if key in self.headers}
        headers["Host"] = HOST
        status, body, selected = 503, b"", []
        accepted_expiry = None
        parent = self.path == MANIFEST and (CONTROL / "nested").exists()
        if self.path == "/public.json":
            status, selected = 200, [("Content-Type", "application/ard+json")]
            body = json.dumps({"entries": [{"identifier": "urn:air:example.org:default:public", "displayName": "Public Agent",
                "type": "application/a2a-agent-card+json", "url": "https://example.org/card"}]}).encode()
        elif not (CONTROL / "outage").exists() and not (parent and (CONTROL / "block-retry").exists()):
            upstream = http.client.HTTPConnection("api-a", 8080, timeout=5)
            try:
                upstream.request("GET", MANIFEST, headers=headers)
                reply = upstream.getresponse()
                status, body = reply.status, reply.read(1048577)
                if len(body) > 1048576:
                    raise ValueError("oversize fixture response")
                selected = [(k, v) for k, v in reply.getheaders()
                            if k.lower() in ("content-type", "etag", "vary", "cache-control", "www-authenticate")]
                if status == 200:
                    # A verified this JWT. Keep only its expiry to distinguish the two real issuances.
                    payload = headers["Authorization"].split(".")[1]
                    accepted_expiry = json.loads(base64.urlsafe_b64decode(payload + "=" * (-len(payload) % 4)))["exp"]
            finally:
                upstream.close()
            if parent and status == 200:
                document = json.loads(body)
                document["entries"] = [entry for entry in document["entries"] if entry["type"].startswith("application/ai-registry")] + [{"identifier": "urn:air:tutorial.catalog.example:catalog:child",
                    "displayName": "Published agents", "type": "application/ai-catalog+json",
                    "url": "https://" + HOST + "/child.json"}]
                body = json.dumps(document).encode()
                selected = [(k, v) for k, v in selected if k.lower() != "etag"]
                if (CONTROL / "hold").exists():
                    (CONTROL / "held").touch()
                    deadline = time.monotonic() + 18
                    while (CONTROL / "hold").exists() and time.monotonic() < deadline:
                        time.sleep(.05)
                    if (CONTROL / "hold").exists():
                        status, body = 503, b""
            if self.path == "/child.json" and status == 200:
                # The parent explicitly declares the predecessor AI Catalog format.
                # A supplied the entries; the nested test owns this representation.
                body = json.dumps({"specVersion": "1.0", "entries": json.loads(body)["entries"]}).encode()
                selected = [("Content-Type", "application/ai-catalog+json")]
        print(json.dumps({"path": self.path, "status": status, "authenticated": "Authorization" in headers,
                          "conditional": any(k.startswith("If-") for k in headers),
                          "acceptedExpiry": accepted_expiry, "hasETag": any(k.lower() == "etag" and v for k, v in selected),
                          "vary": [v for k, v in selected if k.lower() == "vary"]}), flush=True)
        self.send_response(status)
        for key, value in selected:
            self.send_header(key, value)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


def context(name):
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain("/tls/" + name + ".pem", "/tls/" + name + "-key.pem")
    return ctx


if __name__ == "__main__":
    server = ThreadingHTTPServer(("", 8443), Peer)
    current, replacement = context("current"), context("next")

    def select(sock, name, initial):
        if (CONTROL / "alternate-ca").exists():
            sock.context = replacement

    current.set_servername_callback(select)
    server.socket = current.wrap_socket(server.socket, server_side=True)
    server.serve_forever()
