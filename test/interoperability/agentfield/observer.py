"""Owned TLS observation proxy and inert TCP trap; never installed in either product."""

import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import socketserver
import ssl
import threading

LIMIT = 2 << 20
LOCK = threading.Lock()
STATE = {"requests": [], "trapConnections": 0, "trapCalibrated": False, "tlsConnections": 0, "unavailable": False}
CONTROL_ENTRY = {
    "identifier": "urn:air:catalog.example.com:agentfield:control",
    "type": "application/mcp-server-card+json", "displayName": "Native benign control",
    "description": "Inert native control, never callable", "url": "https://127.0.0.1:8445/never-fetch",
    "version": "1.2.3", "tags": ["control-tag"], "capabilities": ["metadata-only"],
    "representativeQueries": ["Find native control"],
    "trustManifest": {"identity": "unverified-control", "identityType": "test-only"},
    "unknownEvidence": "not-preserved-by-native-reader",
}


class Trap(socketserver.ThreadingTCPServer):
    allow_reuse_address = True
    daemon_threads = True


class TrapHandler(socketserver.BaseRequestHandler):
    def handle(self):
        with LOCK:
            STATE["trapConnections"] += 1


class TLS(ThreadingHTTPServer):
    daemon_threads = True

    def get_request(self):
        sock, address = self.socket.accept()
        sock.settimeout(8)
        with LOCK:
            STATE["tlsConnections"] += 1
        try:
            return self.context.wrap_socket(sock, server_side=True), address
        except BaseException:
            sock.close()
            raise


class Handler(BaseHTTPRequestHandler):
    """Forward only fixed discovery routes, retaining original request/response JSON."""

    def log_message(self, *_):
        pass

    def setup(self):
        super().setup()
        self.connection.settimeout(8)

    def reply(self, status, raw):
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def do_GET(self):
        if self.server.server_port == 9012 and self.path == "/state":
            with LOCK:
                raw = json.dumps(STATE).encode()
            self.reply(200, raw)
        else:
            self.reply(404, b"{}")

    def do_POST(self):
        size = int(self.headers.get("Content-Length", "0"))
        if not 0 < size <= 65536:
            self.reply(413, b"{}")
            return
        raw = self.rfile.read(size)
        if self.server.server_port == 9012 and self.path == "/calibrate":
            assert json.loads(raw) == {"resetCalibration": True}
            with LOCK:
                assert STATE["trapConnections"] == 1 and not STATE["trapCalibrated"]
                STATE["trapConnections"] = 0
                STATE["trapCalibrated"] = True
            self.reply(200, b"{}")
            return
        if self.server.server_port == 9012 and self.path == "/fault":
            fields = json.loads(raw)
            assert set(fields) == {"unavailable"} and type(fields["unavailable"]) is bool
            with LOCK:
                STATE["unavailable"] = fields["unavailable"]
            self.reply(200, b"{}")
            return
        host = self.headers.get("Host")
        body = json.loads(raw)
        with LOCK:
            unavailable = STATE["unavailable"]
            if len(STATE["requests"]) >= 64:
                self.reply(429, b"{}")
                return
        status, response = 404, b"{}"
        if host in ("peer-a.test:8444", "private-a.test:8444"):
            if self.path == "/control-registry/search":
                entry = CONTROL_ENTRY
                if body["query"]["text"] == "shape-sparse":
                    entry = {"identifier": CONTROL_ENTRY["identifier"] + "-sparse"}
                elif body["query"]["text"] == "shape-empty-data":
                    entry = {"identifier": CONTROL_ENTRY["identifier"] + "-empty", "type": "application/json",
                             "displayName": "Empty inline metadata", "data": {}, "trustManifest": None}
                elif body["query"]["text"] == "control-new-version":
                    entry = {**CONTROL_ENTRY, "version": "2.0.0", "description": "Explicitly refreshed native control"}
                status, response = 200, json.dumps({"results": [{**entry, "score": 87,
                    "source": "https://reported-source.test/"}], "pageToken": "not-followed",
                    "referrals": [{"identifier": "urn:air:catalog.example.com:agentfield:referral"}]}).encode()
            elif self.path == "/ard/v1/search":
                if unavailable:
                    status, response = 503, b'{"error":"owned availability control"}'
                else:
                    connection = http.client.HTTPConnection("127.0.0.1", 8080, timeout=8)
                    try:
                        connection.request("POST", self.path, raw, {"Host": host, "Content-Type": "application/json"})
                        result = connection.getresponse()
                        status, response = result.status, result.read(LIMIT + 1)
                        assert len(response) <= LIMIT, "owned response bound"
                    finally:
                        connection.close()
        observation = {"host": host, "path": self.path, "body": body,
                       "authorizationPresent": "Authorization" in self.headers,
                       "status": status, "response": json.loads(response)}
        with LOCK:
            STATE["requests"].append(observation)
        self.reply(status, response)


def main():
    certificate = Path("/fixture")
    tls = TLS(("0.0.0.0", 8444), Handler)
    tls.context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    tls.context.minimum_version = ssl.TLSVersion.TLSv1_2
    tls.context.load_cert_chain(certificate / "server.pem", certificate / "server-key.pem")
    control = ThreadingHTTPServer(("127.0.0.1", 9012), Handler)
    trap = Trap(("0.0.0.0", 8445), TrapHandler)
    for server in (tls, trap):
        threading.Thread(target=server.serve_forever, daemon=True).start()
    control.serve_forever()


if __name__ == "__main__":
    main()
