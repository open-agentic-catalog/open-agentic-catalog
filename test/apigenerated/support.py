"""Scoped HTTP transport and operation-specific generated request oracles."""

import json
import os
import re
from pathlib import Path
import sys
import time
from urllib.parse import parse_qs, urlsplit

import requests
import schemathesis
import yaml

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "functional"))
from generated_api import MAX_REQUESTS, OPERATIONS, SETUP, omitted_operations  # noqa: E402

ROOT = Path(__file__).resolve().parents[2]


def schema():
    path = ROOT / "api/openapi/control-v1.yaml"
    # Only local references are permitted, including references outside the pilot.
    def check(node):
        if isinstance(node, dict):
            for key, item in node.items():
                if key == "$ref":
                    assert item.startswith("#/"), "external schema reference"
                check(item)
        elif isinstance(node, list):
            for item in node:
                check(item)
    check(yaml.safe_load(path.read_text()))
    value = schemathesis.openapi.from_path(str(path))
    if os.environ.get("CATALOG_FUNCTIONAL_OPERATION_COVERAGE") == "on":
        from runner import read_document
        from generated_api import require_inventory
        ids = [result.ok().definition.raw["operationId"] for result in value.get_all_operations()]
        require_inventory(ids, read_document(Path(os.environ["CATALOG_FUNCTIONAL_STATE_DIRECTORY"]) / "operations.json"))
    return value


class Session(requests.Session):
    """One owned origin, sequential requests, no proxy/redirects, hard budgets."""

    def __init__(self, base, selection="pilot"):
        super().__init__()
        parsed = urlsplit(base)
        assert parsed.scheme == "http" and parsed.hostname == "127.0.0.1" and parsed.port and not parsed.path
        self.base = base
        assert selection in {"pilot", "authoring"}
        self.selection = selection
        self.trust_env = False
        self.count = 0
        self.creates = 0
        self.setup = dict.fromkeys(SETUP, 0)
        self.resource_ids = set()
        self.version_parent = None
        self.version_ids = set()
        self.revision_ids = set()
        self.authoring_creates = 0
        self.deadline = time.monotonic() + 150

    def send(self, request, **kwargs):
        parsed = urlsplit(request.url)
        assert f"{parsed.scheme}://{parsed.netloc}" == self.base, "unowned request origin"
        assert not parsed.fragment and not parsed.username
        selected = {("GET", "/api/v1/namespaces"), ("POST", "/api/v1/namespaces"),
                    ("POST", "/api/v1/catalog/search"), ("GET", "/api/v1/catalog/resources"), ("GET", "/api/v1/resources")}
        namespace_path = re.fullmatch(r"/api/v1/namespaces/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}", parsed.path)
        resource_path = re.fullmatch(r"/api/v1/resources/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})(/versions)?", parsed.path)
        setup = None
        if resource_path and request.method == "GET" and not resource_path[2] and resource_path[1] in self.resource_ids:
            setup = "getResource"
        if resource_path and request.method == "POST" and resource_path[2] and resource_path[1] == self.version_parent:
            setup = "createResourceVersion"
        versions = resource_path and resource_path[2] and request.method == "GET" and resource_path[1] in self.resource_ids
        if self.selection == "authoring":
            version = re.fullmatch(r"/api/v1/resource-versions/([0-9a-f-]{36})", parsed.path)
            revision = re.fullmatch(r"/api/v1/version-revisions/([0-9a-f-]{36})", parsed.path)
            allowed = ((request.method, parsed.path) in {("POST", "/api/v1/resources"), ("POST", "/api/v1/namespaces")}
                       or request.method == "GET" and resource_path and resource_path[1] in self.resource_ids and not resource_path[2]
                       or request.method == "POST" and resource_path and resource_path[2] and resource_path[1] == self.version_parent
                       or request.method == "GET" and version and version[1] in self.version_ids
                       or request.method == "GET" and revision and revision[1] in self.revision_ids)
            assert allowed, "unselected authoring operation"
            setup = None
            if request.method == "POST" and (parsed.path == "/api/v1/resources" or resource_path):
                self.authoring_creates += 1
                assert self.authoring_creates <= 100, "authoring creation budget"
        else:
            assert ((request.method, parsed.path) in selected or (namespace_path and request.method in {"GET", "PATCH"})
                    or versions or setup), "unselected operation"
        body = request.body.encode("utf-8") if isinstance(request.body, str) else request.body or b""
        assert len(request.url) <= 65536 and len(body) <= 1024 * 1024, "request byte budget"
        assert sum(len(k) + len(v) for k, v in request.headers.items()) <= 65536, "request header budget"
        self.count += 1
        if setup:
            self.setup[setup] += 1
            assert self.setup[setup] <= SETUP[setup], "setup request budget"
        if request.method == "POST" and parsed.path == "/api/v1/namespaces":
            self.creates += 1
            assert self.creates <= 100, "namespace creation budget"
        assert self.count <= MAX_REQUESTS and time.monotonic() < self.deadline, "request/time budget"
        kwargs.update(timeout=5, allow_redirects=False, stream=True)
        response = super().send(request, **kwargs)
        try:
            assert sum(len(k) + len(v) for k, v in response.headers.items()) <= 65536, "response header budget"
            body = bytearray()
            for chunk in response.iter_content(8192):
                body.extend(chunk)
                assert len(body) <= 2 * 1024 * 1024 and time.monotonic() < self.deadline, "response/time budget"
            response._content = bytes(body)
            response._content_consumed = True
            assert not 300 <= response.status_code < 400, "redirect rejected"
            return response
        finally:
            response.close()


def validate(case, response, mode):
    # Only response-side Schemathesis checks: its permissive negative status
    # allowlist is not our body-validation oracle.
    from schemathesis.specs.openapi.checks import content_type_conformance, response_schema_conformance, status_code_conformance
    def expected_status(_ctx, reply, _case):
        if mode == "negative":
            assert reply.status_code == 400 and reply.json()["code"] == "INVALID_ARGUMENT", "invalid input was not rejected by validation"
        else:
            expected = 201 if case.operation.definition.raw["operationId"] in {"createNamespace", "createResource", "createResourceVersion"} else 200
            assert reply.status_code == expected, "valid control failed"
    case.validate_response(response, checks=(status_code_conformance, content_type_conformance, response_schema_conformance, expected_status))
    from operation_coverage import record
    record({"operationId": case.operation.definition.raw["operationId"], "status": response.status_code, "contract": "passed"}, (response.status_code,))


def write_counts(session, counts, schema_value, sequences=None):
    omitted = omitted_operations([result.ok().definition.raw["operationId"] for result in schema_value.get_all_operations()], OPERATIONS)
    target = Path(os.environ["CATALOG_FUNCTIONAL_STATE_DIRECTORY"]) / "generated-counts.json"
    target.write_text(json.dumps({"requests": session.count, "setup": session.setup, "counts": counts, "omittedOperations": omitted, "sequences": sequences}))


def authoring_exchange(case, session, mode, snapshot, read_success):
    """Use the same response and retained-state checks in tests and calibrations."""
    from authoring_cases import unchanged
    before = snapshot() if mode == "negative" else None
    response = case.call(base_url=session.base, session=session)
    session.last_authoring_refusal = False
    try:
        validate(case, response, mode)
        session.last_authoring_refusal = mode == "negative" and response.status_code == 400 and response.json()["code"] == "INVALID_ARGUMENT"
    finally:
        if before is not None:
            unchanged(before, snapshot())
    if mode == "positive":
        read_success(case.body, response.json())
    return response


def invalid_wire_query(case, base):
    """Negativity must survive Requests' query serialization; never repair it."""
    from jsonschema_rs import Draft202012Validator
    prepared = requests.Request(**case.as_transport_kwargs(base_url=base)).prepare()
    query = parse_qs(urlsplit(prepared.url).query, keep_blank_values=True)
    parameters = {p.name: p.definition["schema"] for p in case.operation.iter_parameters() if p.location.value == "query"}
    for key, values in query.items():
        if key not in parameters or len(values) != 1:
            return True
        value = values[0]
        parameter = parameters[key]
        if parameter.get("type") == "integer":
            if not re.fullmatch(r"[+-]?[0-9]+", value):
                return True
            value = int(value)
        if not Draft202012Validator(parameter, validate_formats=True).is_valid(value):
            return True
    return False
