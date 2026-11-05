"""Actual HF HTTP controls, executed only inside the explicitly selected tool image."""

from contextlib import contextmanager
from http.server import BaseHTTPRequestHandler, HTTPServer
import json
import sys
import threading

from client_contract import CONTROL_CASES, outcome, redact

# Both shapes satisfy pinned ARD SearchResultItem, unlike full ArdEntry validation.
SPARSE = {"identifier": "urn:air:fixture.example:agent:one", "score": 90,
          "source": "https://fixture.example/registry"}
FULL = dict(SPARSE, displayName="One", type="application/a2a-agent-card+json", data={"name": "One"})
TOKEN = "local-reader"


def validate_full(response, *, source=None):
    """The full control must preserve the exact identifying metadata and wire score."""
    assert len(response["results"]) == 1 and not response.get("referrals"), "unexpected control results"
    expected = FULL if source is None else dict(FULL, source=source)
    for field, value in expected.items():
        assert response["results"][0].get(field) == value, f"control changed {field}"
    assert type(response["results"][0]["score"]) is int, "control score is not an integer"


def validate_sparse(error, expected_type):
    """Only the documented full-entry validation failure characterizes this gap."""
    assert isinstance(error, expected_type), "unexpected sparse decoding outcome"
    assert "displayName" in str(error) and "type" in str(error), "sparse rejection did not identify full-entry requirements"


class ControlServer(HTTPServer):
    """A serial loopback server; stalled requests cannot prevent owned cleanup."""

    def get_request(self):
        connection, address = super().get_request()
        connection.settimeout(2)
        return connection, address

    def handle_error(self, _request, _client_address):
        self.errors.append("control request failed")


class Handler(BaseHTTPRequestHandler):
    """Serve only a fixed route table and capture authorization presence, never bytes."""

    def log_message(self, *_args):
        pass

    def do_GET(self):
        self.respond()

    def do_POST(self):
        self.respond()

    def respond(self):
        body = None
        status = 200
        payload = self.server.routes.get((self.command, self.path))
        if payload is None:
            status, payload = 404, {"error": "unselected route"}
        if self.command == "POST":
            try:
                length = int(self.headers.get("Content-Length", "0"))
                if not 0 < length <= 8192 or self.headers.get("Transfer-Encoding"):
                    raise ValueError("request bound")
                body = json.loads(self.rfile.read(length))
            except (OSError, ValueError):
                status, payload = 400, {"error": "invalid bounded request"}
        authorization = self.headers.get("Authorization")
        self.server.seen.append({"method": self.command, "path": self.path, "body": body, "status": status,
                                 "authorization": "absent" if authorization is None else "synthetic" if authorization == "Bearer " + TOKEN else "unexpected"})
        raw = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)


@contextmanager
def local_http(routes):
    """Own one listener and thread, also closing them when a control assertion fails."""
    server = ControlServer(("127.0.0.1", 0), Handler)
    server.routes, server.seen, server.errors = routes, [], []
    server.origin = "http://127.0.0.1:" + str(server.server_port)
    thread = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.05}, daemon=True)
    started = False
    try:
        thread.start()
        started = True
        yield server
    finally:
        if started:
            server.shutdown()
        server.server_close()
        if started:
            thread.join(timeout=4)
            if thread.is_alive():
                raise RuntimeError("control listener did not stop")


def validate_exchange(server, query, *, token=False):
    """Require the intended successful POST, not an unrelated transport failure."""
    assert not server.errors and len(server.seen) == 1, "unexpected HTTP control exchanges"
    seen = server.seen[0]
    assert (seen["method"], seen["path"], seen["status"], seen["authorization"]) == (
        "POST", "/search", 200, "synthetic" if token else "absent"), "incorrect HTTP control exchange"
    body = seen["body"]
    assert body["query"]["text"] == query and body.get("federation") == "none", "incorrect control query"
    assert body.get("pageSize") == 1, "incorrect control limit"


def decode_control(client, shape):
    """Import and execute unmodified upstream decoders only in the pinned image."""
    from discover.cli import _registry_search
    from discover.models import SearchQuery, SearchRequest
    from discover.navigation import search_registry
    from pydantic import ValidationError
    from typer import BadParameter

    with local_http({("POST", "/search"): {"results": [FULL if shape == "full" else SPARSE]}}) as server:
        error = None
        response = None
        try:
            if client == "search":
                response = _registry_search(server.origin, shape, limit=1, federation="none").response
            else:
                response = search_registry(server.origin + "/search", SearchRequest(
                    query=SearchQuery(text=shape), federation="none", pageSize=1), timeout=2)
        except (BadParameter, ValidationError) as caught:
            error = caught
        validate_exchange(server, shape)
        if shape == "full":
            assert error is None and response is not None, "full control rejected"
            validate_full(response.model_dump(exclude_none=True))
        else:
            expected = BadParameter if client == "search" else ValidationError
            assert response is None, "sparse control unexpectedly decoded"
            validate_sparse(error, expected)
        return {"http_status": 200, "http_requests": len(server.seen), "accepted": shape == "full",
                "decoder_error": type(error).__name__ if error is not None else None}


def credential_control():
    """Characterize token forwarding and source rewriting between two owned origins."""
    from discover.navigation import navigate

    with local_http({("POST", "/search"): {"results": [FULL]}}) as registry:
        descriptor = {"identifier": "urn:air:fixture.example:registry:one", "displayName": "Owned Registry",
                      "type": "application/ai-registry+json", "url": registry.origin}
        with local_http({("GET", "/.well-known/ard.json"): {"entries": [descriptor]}}) as manifest:
            report = navigate(manifest.origin + "/.well-known/ard.json", "one", token=TOKEN, timeout=2,
                              max_registries=1, max_depth=0, limit=1, follow_referrals=False)
            payload = report.model_dump()
            assert not manifest.errors and manifest.seen == [dict(method="GET", path="/.well-known/ard.json",
                body=None, status=200, authorization="absent")], "manifest authorization behavior changed"
            validate_exchange(registry, "one", token=True)
            expected_discovery = [dict(kind="catalog", url=manifest.origin + "/.well-known/ard.json", status="ok", detail=""),
                                  dict(kind="registry", url=registry.origin + "/search", status="ok", detail="")]
            assert payload["discovered"] == expected_discovery, "credential control discovery failed"
            validate_full(payload, source=registry.origin + "/search")
    return {"manifest_authorized": False, "other_owned_origin_registry_authorized": True,
            "source_replaced_with_queried_endpoint": True, "http_requests": 2}


def qualify():
    """Collect every control outcome; arbitrary exceptions never count as expected rejection."""
    cases = []
    for name in CONTROL_CASES:
        item = outcome(name)
        try:
            if name == "navigate-credential-source":
                item.update(credential_control())
            else:
                _, client, shape = name.split("-")
                item.update(decode_control(client, shape))
        except Exception as error:
            item.update(status="fail", detail=redact(str(error))[:6000])
        cases.append(item)
    print(json.dumps({"cases": cases}))
    return int(any(case["status"] != "pass" for case in cases))


if __name__ == "__main__":
    sys.exit(qualify())
