"""Real-tool authoring response/state calibrations over an owned HTTP server."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import threading

import pytest
from schemathesis.core.failures import FailureGroup

from support import Session, authoring_exchange, schema
from authoring_cases import resource_created

ID = "00000000-0000-7000-8000-000000000001"
BODY = {"namespaceId": ID, "canonicalIdentifier": "urn:air:catalog.example.com:owned:calibration", "kind": "agent"}
RESOURCE = {**BODY, "id": ID, "tenantId": ID, "createdBy": "calibration", "revision": 1,
            "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-01T00:00:00Z"}


@pytest.fixture
def fixture():
    class Handler(BaseHTTPRequestHandler):
        mode = "valid"
        revision = 1

        def reply(self, status, body):
            raw = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/problem+json" if status == 400 else "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

        def do_GET(self):
            self.reply(200, {**RESOURCE, "revision": type(self).revision, **self.tenant()})

        def tenant(self):
            return {"tenantId": "00000000-0000-7000-8000-000000000002"} if self.mode == "tenant" else {}

        def do_POST(self):
            self.rfile.read(int(self.headers["Content-Length"]))
            if self.mode in {"rejected", "write"}:
                if self.mode == "write":
                    type(self).revision += 1
                self.reply(400, {"type": "about:blank", "title": "Invalid input", "status": 400, "code": "INVALID_ARGUMENT",
                                 "detail": "Owned calibration", "instance": "/api/v1/resources", "requestId": "calibration"})
            else:
                self.reply(201, {**RESOURCE, **self.tenant(), **({"kind": "unsupported"} if self.mode == "response" else {})})

        def log_message(self, *_args):
            pass

    server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    try:
        yield Handler, "http://127.0.0.1:" + str(server.server_port)
    finally:
        server.shutdown()
        server.server_close()
        thread.join(timeout=2)
        assert not thread.is_alive()


def exercise(fixture, mode):
    handler, base = fixture
    handler.mode = mode
    operation = schema().find_operation_by_id("createResource")
    positive = mode in {"valid", "response", "tenant"}
    body = BODY if positive else {**BODY, "kind": "unsupported"}
    case = operation.Case(body=body, media_type="application/json", headers={"Idempotency-Key": "calibration"})
    with Session(base, "authoring") as session:
        session.resource_ids.add(ID)
        def read():
            return session.get(base + "/api/v1/resources/" + ID).json()
        authoring_exchange(case, session, "positive" if positive else "negative", read,
                          lambda sent, created: resource_created(sent, created, read(), ID))


def test_authoring_valid(fixture):
    exercise(fixture, "valid")
    exercise(fixture, "rejected")


def test_authoring_invalid_acceptance(fixture):
    with pytest.raises(FailureGroup, match="invalid input was not rejected") as caught:
        exercise(fixture, "acceptance")
    assert len(caught.value.exceptions) == 1 and caught.value.exceptions[0].__class__.__name__ == "CustomFailure"


def test_authoring_response_defect(fixture):
    with pytest.raises(FailureGroup, match="Response violates schema"):
        exercise(fixture, "response")
    # The Version envelope has its own schema, checked by the same real tool.
    _, base = fixture
    operation = schema().find_operation_by_id("createResourceVersion")
    case = operation.Case(body={}, media_type="application/json", path_parameters={"resourceId": ID})
    with Session(base, "authoring") as session:
        session.version_parent = ID
        with pytest.raises(FailureGroup, match="Response violates schema"):
            authoring_exchange(case, session, "positive", None, lambda *_: None)
    # Matching response/readback documents must still belong to the bound tenant.
    with pytest.raises(AssertionError, match="wrong Resource tenant"):
        exercise(fixture, "tenant")


def test_authoring_rejected_write(fixture):
    with pytest.raises(AssertionError, match="rejected authoring mutation changed stored state"):
        exercise(fixture, "write")


def test_authoring_wire_binding():
    from jsonschema_rs import Draft202012Validator
    import requests
    from authoring_cases import bind_body, native, valid_wire_body
    value = schema()
    for name, source, repaired, invalid in (
            ("createResource", "CreateResource", {**BODY, "namespaceId": "invalid"}, {**BODY, "kind": None}),
            ("createResourceVersion", "CreateVersion", {"version": "valid", "representations": None}, {"version": None, "representations": native("valid")})):
        raw = value.raw_schema
        definition = raw["components"]["schemas"][source]
        def validator(part):
            return Draft202012Validator({**part, "components": raw["components"]}, validate_formats=True)
        check = validator(definition)
        fields = {key: validator(part) for key, part in definition["properties"].items()}
        assert not check.is_valid(repaired)
        utf16 = json.dumps(bind_body(name, repaired, ID, "valid", lambda key, item: fields[key].is_valid(item))).encode("utf-16")
        assert check.is_valid(json.loads(utf16)), "encoding control must otherwise satisfy the schema"
        for body, expected in ((repaired, True), (invalid, False), (b"\x80", False), (b"{", False), (None, False),
                               (utf16, False)):
            bound = bind_body(name, body, ID, "valid", lambda key, item: fields[key].is_valid(item))
            case = value.find_operation_by_id(name).Case(body=bound, media_type="application/json",
                path_parameters={"resourceId": ID} if name == "createResourceVersion" else None)
            prepared = requests.Request(**case.as_transport_kwargs(base_url="http://127.0.0.1:1234")).prepare()
            assert valid_wire_body(prepared.body, check) == expected


def test_authoring_transport(fixture):
    import requests
    _, base = fixture
    with Session(base, "authoring") as session:
        for method, path in (("GET", "/api/v1/resources"), ("POST", "/api/v1/sources"),
                             ("POST", "/api/v1/resources/" + ID + "/versions"),
                             ("GET", "/api/v1/resources/" + ID), ("GET", "/api/v1/resource-versions/" + ID),
                             ("GET", "/api/v1/version-revisions/" + ID), ("DELETE", "/api/v1/resources")):
            with pytest.raises(AssertionError, match="unselected authoring operation"):
                session.send(requests.Request(method, base + path).prepare())
        assert session.count == 0
        with pytest.raises(AssertionError, match="unowned request origin"):
            session.post("http://example.invalid/api/v1/resources", json=BODY)
        session.authoring_creates = 100
        with pytest.raises(AssertionError, match="authoring creation budget"):
            session.post(base + "/api/v1/resources", json=BODY)
        session.authoring_creates = 0
        session.deadline = 0
        with pytest.raises(AssertionError, match="request/time budget"):
            session.post(base + "/api/v1/resources", json=BODY)
