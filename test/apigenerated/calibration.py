"""Real-tool controls over an owned listener, including deliberate defects."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import threading

import pytest

from support import Session, schema, validate


@pytest.fixture
def fixture():
    class Handler(BaseHTTPRequestHandler):
        mode = "valid"
        page = None

        def do_GET(self):
            body = {"items": []} if self.mode != "response" else {"items": "wrong"}
            if self.page is not None:
                body = self.page
            raw = json.dumps(body).encode()
            status = {"authentication": 401, "status": 201}.get(self.mode, 200)
            if self.mode in {"authentication", "status"}:
                body = {"type": "about:blank", "title": "Owned calibration", "status": 400 if self.mode == "status" else status, "code": "UNAUTHENTICATED", "detail": "Owned negative", "instance": "/api/v1/namespaces", "requestId": "calibration"}
                raw = json.dumps(body).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/problem+json" if self.mode in {"authentication", "status"} else "application/json")
            self.send_header("Content-Length", str(len(raw)))
            self.end_headers()
            self.wfile.write(raw)

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
    operation = schema().find_operation_by_id("listNamespaces")
    case = operation.Case(query={"limit": -1} if mode == "acceptance" else {"limit": 1})
    with Session(base) as session:
        response = case.call(base_url=base, session=session)
    validate(case, response, "negative" if mode == "acceptance" else "positive")


def test_valid(fixture):
    import os
    from generated_api import OPERATIONS, require_inventory
    from runner import command
    authority = json.loads(command([os.environ["CATALOG_FUNCTIONAL_CONTRACT_DRIVER"], "--operations"], dict(os.environ), 15, capture=True, limit=65536))
    loaded = [result.ok().definition.raw["operationId"] for result in schema().get_all_operations()]
    require_inventory(loaded, authority)
    omitted = next(name for name in authority if name not in OPERATIONS)
    with pytest.raises(AssertionError, match="operation inventory differs"):
        require_inventory([name for name in loaded if name != omitted], authority)
    exercise(fixture, "valid")


def test_invalid_acceptance(fixture):
    from schemathesis.core.failures import FailureGroup
    with pytest.raises(FailureGroup, match="invalid input was not rejected"):
        exercise(fixture, "acceptance")


def test_response_defect(fixture):
    from schemathesis.core.failures import FailureGroup
    with pytest.raises(FailureGroup, match="Response violates schema"):
        exercise(fixture, "response")
    # A 201 Problem matches the default response schema, but is not a valid
    # success for this GET. The exact-status oracle must catch it independently.
    with pytest.raises(FailureGroup, match="valid control failed") as caught:
        exercise(fixture, "status")
    assert len(caught.value.exceptions) == 1 and caught.value.exceptions[0].__class__.__name__ == "CustomFailure"


def test_authentication_loss(fixture):
    from schemathesis.core.failures import FailureGroup
    with pytest.raises(FailureGroup, match="valid control failed"):
        exercise(fixture, "authentication")


def test_transport_bounds(fixture):
    import requests
    _, base = fixture
    with Session(base) as session:
        for method, url in (("GET", "http://example.invalid/api/v1/namespaces"),
                            ("DELETE", base + "/api/v1/namespaces"),
                            ("GET", base + "/api/v1/catalog/search"),
                            ("POST", base + "/api/v1/resources"),
                            ("GET", base + "/api/v1/resources/00000000-0000-7000-8000-000000000001"),
                            ("POST", base + "/api/v1/resources/00000000-0000-7000-8000-000000000001/versions")):
            with pytest.raises(AssertionError):
                session.send(requests.Request(method, url).prepare())
        with pytest.raises(AssertionError, match="request byte budget"):
            session.post(base + "/api/v1/namespaces", data=b"x" * (1024*1024+1))
        owned = "00000000-0000-7000-8000-000000000001"
        session.resource_ids = {owned}
        session.version_parent = owned
        # Narrow setup permissions admit the exact owned paths, never arbitrary lifecycle work.
        for _ in range(3):
            session.get(base + "/api/v1/resources/" + owned)
        with pytest.raises(AssertionError, match="setup request budget"):
            session.get(base + "/api/v1/resources/" + owned)
        session.post(base + "/api/v1/resources/" + owned + "/versions", json={})
        with pytest.raises(AssertionError, match="setup request budget"):
            session.post(base + "/api/v1/resources/" + owned + "/versions", json={})
        session.deadline = 0
        with pytest.raises(AssertionError, match="request/time budget"):
            session.get(base + "/api/v1/namespaces")


def test_schema_keywords():
    # Exercise the pinned validator's 2020-12 semantics, not just YAML loading.
    from jsonschema_rs import Draft202012Validator
    raw = schema().raw_schema
    def validator(name):
        return Draft202012Validator({"$ref": "#/components/schemas/" + name, "components": raw["components"]}, validate_formats=True)
    create = validator("CreateNamespace")
    assert create.is_valid({"slug": "valid", "displayName": "🙂" * 160})
    for value in ({"slug": "valid", "displayName": "🙂" * 161}, {"slug": "bad_label", "displayName": "valid"},
                  {"slug": "valid", "displayName": "bad\0"}, {"slug": "valid", "displayName": None},
                  {"slug": "valid", "displayName": "valid", "extra": True}):
        assert not create.is_valid(value)
    patch = validator("PatchNamespace")
    assert patch.is_valid({"reason": "Review", "description": "Changed"})
    assert not patch.is_valid({"reason": "Review"})
    assert not patch.is_valid({"reason": " " , "description": "Changed"})
    identifier = Draft202012Validator({"type": "string", "format": "uuid"}, validate_formats=True)
    assert identifier.is_valid("00000000-0000-7000-8000-000000000001")
    assert not identifier.is_valid("bad-uuid")
    search = validator("CatalogSearch")
    assert search.is_valid({"publisher": "generated-unmatched-publisher", "limit": 1})
    for field in ("text", "publisher"):
        assert not search.is_valid({field: "\0", "limit": 1})
        assert search.is_valid({field: "literal \\u0000", "limit": 1})
    assert not search.is_valid({"tags": ["\0"], "limit": 1})
    assert search.is_valid({"tags": ["literal \\u0000"], "limit": 1})


def test_query_serialization():
    from support import invalid_wire_query
    for name in ("browseCatalog", "listResources", "listResourceVersions"):
        operation = schema().find_operation_by_id(name)
        options = {"path_parameters": {"resourceId": "00000000-0000-7000-8000-000000000001"}} if name == "listResourceVersions" else {}
        for value in ({"cursor": None}, {"cursor": ""}, {"limit": 1}, {"limit": "1"}, {"limit": None}):
            assert not invalid_wire_query(operation.Case(query=value, **options), "http://127.0.0.1:1234")
        for value in ({"limit": 0}, {"kind": "unsupported"}, {"extra": "value"}, {"limit": [1, 2]}, {"limit": 1.0}, {"limit": True}):
            assert invalid_wire_query(operation.Case(query=value, **options), "http://127.0.0.1:1234")
        case = operation.Case(query={"kind": "agent", "limit": 1}, **options)
        assert invalid_wire_query(case, "http://127.0.0.1:1234") == (name == "listResourceVersions")


def test_sequence_oracles():
    from namespace_sequence import check_reply, check_state
    before = {"id": "owned", "displayName": "Before", "revision": 1, "updatedAt": "2026-01-01"}
    after = {**before, "displayName": "After", "revision": 2}
    check_state("change", before, after, "After")
    check_reply("replay", 200, after, '"rev-2"', (after, '"rev-2"'))
    for kind, status, code in (("reuse", 409, "IDEMPOTENCY_KEY_REUSED"), ("stale", 412, "PRECONDITION_FAILED")):
        check_reply(kind, status, {"code": code}, None)
        with pytest.raises(AssertionError, match="incorrect sequence rejection"):
            check_reply(kind, 200, after, '"rev-2"')
    with pytest.raises(AssertionError, match="replay changed"):
        check_reply("replay", 200, before, '"rev-1"', (after, '"rev-2"'))
    for kind in ("replay", "reuse", "stale", "reader", "tenant-read", "tenant-write"):
        check_state(kind, before, before)
        with pytest.raises(AssertionError, match="changed state"):
            check_state(kind, before, after)


def test_list_oracles(fixture):
    from resource_lists import check_page
    handler, base = fixture
    tenant = "00000000-0000-7000-8000-000000000001"
    first = {"id": "00000000-0000-7000-8000-000000000010", "tenantId": tenant, "namespaceId": tenant,
             "canonicalIdentifier": "urn:air:example.com:generated:first", "kind": "agent", "createdBy": "calibration",
             "revision": 1, "createdAt": "2026-01-01T00:00:00Z", "updatedAt": "2026-01-02T00:00:00Z"}
    second = {**first, "id": "00000000-0000-7000-8000-000000000011", "kind": "skill", "updatedAt": "2026-01-01T00:00:00Z"}
    version = {"id": second["id"], "tenantId": tenant, "resourceId": first["id"], "version": "1.0.0", "revision": 1,
               "createdAt": first["createdAt"], "updatedAt": first["updatedAt"]}
    cases = (
        ("listResources", {"items": [first]}, [first, second], {"canonical_identifier": " URN:AIR:EXAMPLE.COM.:generated:first ", "kind": "agent"}, False),
        ("listResources", {"items": []}, [first, second], {"canonical_identifier": "0"}, False),
        ("listResources", {"items": [first]}, [first, second], {"canonical_identifier": "0"}, True),
        ("listResources", {"items": [first, second]}, [first, second], {"canonical_identifier": first["canonicalIdentifier"], "kind": "agent"}, True),
        ("listResources", {"items": [first]}, [first, second], {"kind": "agent"}, False),
        ("listResources", {"items": [second]}, [first, second], {"kind": "agent"}, True),
        ("listResources", {"items": []}, [first, second], {"kind": "agent"}, True),
        ("listResources", {"items": [second, first]}, [first, second], {}, True),
        ("listResources", {"items": [first]}, [first, second], {"limit": 1}, True),
        ("listResources", {"items": [first], "nextCursor": "opaque"}, [first, second], {"limit": 1}, False),
        ("listResources", {"items": [first]}, [first, second], {"limit": 1, "offset": 1}, True),
        ("listResourceVersions", {"items": [version]}, [version], {"parent": first["id"]}, False),
        ("listResourceVersions", {"items": [{**version, "resourceId": second["id"]}]}, [version], {"parent": first["id"]}, True),
        ("listResourceVersions", {"items": [version], "nextCursor": "opaque"}, [version], {"parent": first["id"]}, True),
    )
    for name, page, inventory, kwargs, defective in cases:
        handler.page = page
        operation = schema().find_operation_by_id(name)
        options = {"path_parameters": {"resourceId": first["id"]}} if name == "listResourceVersions" else {}
        case = operation.Case(query={key: value for key, value in kwargs.items() if key in {"kind", "limit"}}, **options)
        with Session(base) as session:
            session.resource_ids = {first["id"]}
            response = case.call(base_url=base, session=session)
        validate(case, response, "positive")  # Every defect has a schema-valid HTTP 200.
        if defective:
            with pytest.raises(AssertionError, match="incorrect list"):
                check_page(name, response.json(), inventory, **kwargs)
        else:
            check_page(name, response.json(), inventory, **kwargs)
