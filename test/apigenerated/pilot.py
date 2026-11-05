"""Schemathesis strategies against the original schema and owned PostgreSQL API."""

import json
import os
from pathlib import Path
import uuid
import unicodedata
from urllib.parse import parse_qs, urlsplit

from hypothesis import HealthCheck, Phase, assume, given, seed, settings, strategies as st
import pytest
from schemathesis.generation import GenerationMode
from schemathesis.specs.openapi.checks import content_type_conformance, response_schema_conformance, status_code_conformance

from support import Session, invalid_wire_query, schema, validate, write_counts
from namespace_sequence import KINDS, check_reply, check_state, stored
from resource_lists import canonical_filter, check_page, check_seed
from generated_api import EXAMPLES, OPERATIONS

SCHEMA = schema()
SESSION = Session(os.environ["CATALOG_FUNCTIONAL_BASE_URL"])
COUNTS = {}
NAMESPACE = {}
RESOURCES = []
VERSIONS = []
PARENT = None
CONTROLS_PASSED = False
SEED = int(os.environ["SCHEMATHESIS_SEED"])


def headers():
    return {"Authorization": "Bearer local-admin", "Idempotency-Key": str(uuid.uuid4())}


def call(name, *, body=None, query=None, identity="local-admin", etag=None, key=None, namespace_id=None, resource_id=None):
    operation = SCHEMA.find_operation_by_id(name)
    options = {"headers": {**headers(), "Authorization": "Bearer " + identity}}
    if key is not None:
        options["headers"]["Idempotency-Key"] = key
    if "{namespaceId}" in operation.path:
        options["path_parameters"] = {"namespaceId": namespace_id or NAMESPACE["id"]}
    if "{resourceId}" in operation.path:
        options["path_parameters"] = {"resourceId": resource_id or PARENT}
    if body is not None:
        options.update(body=body, media_type="application/json")
    if query is not None:
        options["query"] = query
    if etag:
        options["headers"]["If-Match"] = etag
    case = operation.Case(**options)
    response = case.call(base_url=SESSION.base, session=SESSION)
    return case, response


def checked(name, **kwargs):
    case, response = call(name, **kwargs)
    validate(case, response, "positive")
    return response


def test_controls():
    global CONTROLS_PASSED, PARENT
    created = checked("createNamespace", body={"slug": "generated-control", "displayName": "Generated control"})
    NAMESPACE.update(created.json())
    current = checked("getNamespace")
    assert current.json()["id"] == NAMESPACE["id"] and current.headers["etag"][0] == created.headers["etag"][0]
    changed = checked("updateNamespace", body={"displayName": "Updated generated control", "reason": "Owned generator fixture"}, etag=current.headers["etag"][0])
    assert changed.json()["displayName"] == "Updated generated control" and changed.json()["revision"] == 2
    assert checked("getNamespace").json()["revision"] == changed.json()["revision"]
    assert any(item["id"] == NAMESPACE["id"] for item in checked("listNamespaces").json()["items"])
    for name in ("browseCatalog", "searchCatalog"):
        values, cursor = [], None
        for _ in range(4):
            parameters = {"limit": 1}
            if cursor:
                parameters["cursor"] = cursor
            response = checked(name, **({"body": parameters} if name == "searchCatalog" else {"query": parameters}))
            page = response.json()
            assert len(page["items"]) == 1, "smoke seed not visible"
            values.extend(page["items"])
            cursor = page.get("nextCursor")
            if not cursor:
                break
        assert len(values) == 3 and not cursor, "missing continuation fixture"
    assert not checked("searchCatalog", body={"publisher": "generated-unmatched-publisher", "limit": 1}).json()["items"]
    for field in ("publisher", "text", "tags"):
        case, response = call("searchCatalog", body={field: ["\0"] if field == "tags" else "\0", "limit": 1})
        validate(case, response, "negative")
    for name, kwargs, status in (
        ("createNamespace", {"body": {"slug": "reader-denied", "displayName": "Denied"}, "identity": "local-reader"}, 403),
        ("getNamespace", {"identity": "other-tenant-admin"}, 404),
    ):
        case, response = call(name, **kwargs)
        # Response shape is still checked for the selected isolation controls.
        case.validate_response(response, checks=(status_code_conformance, content_type_conformance, response_schema_conformance))
        assert response.status_code == status
    seed_resources = json.loads(Path(os.environ["CATALOG_FUNCTIONAL_STATE_DIRECTORY"], "generated-seed.json").read_text())
    SESSION.resource_ids = {item["resourceId"] for item in seed_resources}
    for item in seed_resources:
        resource = checked("getResource", resource_id=item["resourceId"]).json()
        check_seed(resource, item)
        RESOURCES.append(resource)
    PARENT = next(item["id"] for item in RESOURCES if item["kind"] == "agent")
    SESSION.version_parent = PARENT
    initial = checked("listResourceVersions", query={"limit": 100}).json()
    assert len(initial["items"]) == 1 and not initial.get("nextCursor")
    first = initial["items"][0]
    assert first["resourceId"] == PARENT and first["version"] == "1.0.0"
    VERSIONS.append(first)
    # A smaller label created later makes UUID order distinguishable from label order.
    representation = {"role": "primary", "mediaType": "application/a2a-agent-card+json", "content": json.dumps({
        "protocolVersion": "0.3.0", "name": "Generated version prerequisite", "description": "Owned inert metadata",
        "url": "https://agent.example.com/a2a", "version": "0.0.0", "capabilities": {},
        "defaultInputModes": ["text"], "defaultOutputModes": ["text"],
        "skills": [{"id": "generated", "name": "Generated", "description": "Owned inert metadata", "tags": ["generated"]}]})}
    case, response = call("createResourceVersion", body={"version": "0.0.0", "representations": [representation]})
    case.validate_response(response, checks=(status_code_conformance, content_type_conformance, response_schema_conformance))
    assert response.status_code == 201
    second = response.json()["version"]
    assert second["resourceId"] == PARENT and second["version"] == "0.0.0" and first["id"] < second["id"]
    VERSIONS.append(second)
    for name, inventory in (("listResources", RESOURCES), ("listResourceVersions", VERSIONS)):
        for identity in ("local-admin", "local-reader"):
            check_page(name, checked(name, query={"limit": 100}, identity=identity).json(), inventory, limit=100, parent=PARENT)
        cursor = None
        first_cursor = None
        for offset in range(len(inventory)):
            page = checked(name, query={"limit": 1, **({"cursor": cursor} if cursor else {})}, identity="local-reader").json()
            check_page(name, page, inventory, limit=1, parent=PARENT, offset=offset)
            cursor = page.get("nextCursor")
            if offset == 0:
                first_cursor = cursor
        assert not cursor
        assert first_cursor, "nonempty continuation prerequisite missing"
        binding = {"kind": "plugin"} if name == "listResources" else {
            "resource_id": next(item["id"] for item in RESOURCES if item["id"] != PARENT)}
        case, response = call(name, identity="local-reader", query={"limit": 1, "cursor": first_cursor, **({"kind": binding["kind"]} if name == "listResources" else {})},
                              **(binding if name == "listResourceVersions" else {}))
        validate(case, response, "negative")
        case, response = call(name, query={"limit": 1}, identity="generated-no-reader")
        case.validate_response(response, checks=(status_code_conformance, content_type_conformance, response_schema_conformance))
        if name == "listResources":
            assert response.status_code == 200
            assert response.json()["items"] == [] and not response.json().get("nextCursor")
        else:
            assert response.status_code == 403 and response.json()["code"] == "FORBIDDEN"
    for kind in ("agent", "mcp_server", "skill", "plugin"):
        check_page("listResources", checked("listResources", query={"kind": kind, "limit": 1}).json(), RESOURCES, kind=kind, limit=1)
    foreign = checked("listResources", query={"limit": 100}, identity="other-tenant-admin").json()
    assert not foreign["items"] and not foreign.get("nextCursor")
    case, response = call("listResourceVersions", query={"limit": 1}, identity="other-tenant-admin")
    case.validate_response(response, checks=(status_code_conformance, content_type_conformance, response_schema_conformance))
    assert response.status_code == 404 and response.json()["code"] == "NOT_FOUND"
    # Exercise selected exact filters with real matching and empty controls;
    # generated requests retain the original schema and independent inventory.
    selected = RESOURCES[0]
    exact_query = {"canonicalIdentifier": " " + selected["canonicalIdentifier"] + " ", "kind": selected["kind"], "limit": 1}
    check_page("listResources", checked("listResources", query=exact_query).json(), RESOURCES,
               canonical_identifier=exact_query["canonicalIdentifier"], kind=selected["kind"], limit=1)
    check_page("listResources", checked("listResources", query={"canonicalIdentifier": "0"}).json(), RESOURCES, canonical_identifier="0")
    exact_namespaces = checked("listNamespaces", query={"slug": " " + NAMESPACE["slug"].upper() + " ", "displayName": changed.json()["displayName"], "limit": 1}).json()
    assert [item["id"] for item in exact_namespaces["items"]] == [NAMESPACE["id"]] and not exact_namespaces.get("nextCursor")
    assert not checked("listNamespaces", query={"slug": NAMESPACE["slug"], "displayName": "No matching generated name"}).json()["items"]
    CONTROLS_PASSED = True


@pytest.mark.parametrize("name,mode", [(name, mode) for name in OPERATIONS if name != "getNamespace" for mode in ("positive", "negative")],
                         ids=lambda item: item)
def test_generated(name, mode):
    assert CONTROLS_PASSED, "successful controls must precede generated traffic"
    operation = SCHEMA.find_operation_by_id(name)
    options = {"headers": headers()}
    if name == "updateNamespace":
        options["headers"]["If-Match"] = '"rev-1"'
        options["path_parameters"] = {"namespaceId": NAMESPACE["id"]}
    if name == "listResourceVersions":
        options["path_parameters"] = {"resourceId": PARENT}
    strategy = operation.as_strategy(generation_mode=GenerationMode(mode), **options)
    label = f"test_generated[{name}-{mode}]"
    COUNTS[label] = 0

    @seed(SEED)
    @settings(max_examples=EXAMPLES, deadline=None, database=None, phases=(Phase.generate, Phase.shrink),
              suppress_health_check=(HealthCheck.too_slow,))
    @given(strategy)
    def exercise(case):
        if mode == "negative" and name in {"listNamespaces", "browseCatalog", "listResources", "listResourceVersions"}:
            assume(invalid_wire_query(case, SESSION.base))
        before = checked("getNamespace") if name == "updateNamespace" else checked("listNamespaces", query={"limit": 100}) if name == "createNamespace" else None
        # Headers are fixture prerequisites, never a generated test component.
        case.headers.update(headers())
        if name == "updateNamespace":
            case.headers["If-Match"] = before.headers["etag"][0]
        response = case.call(base_url=SESSION.base, session=SESSION)
        try:
            if mode == "positive" and response.status_code == 400:
                # Schema syntax cannot express signed cursor binding or Unicode
                # whitespace normalization. Keep this narrow and explicit.
                data = case.body if isinstance(case.body, dict) else {}
                query = case.query or {}
                cursor = bool(data.get("cursor")) or "cursor" in query
                # The documented strict integer representation is narrower than
                # JSON Schema's mathematical integer (which includes 1.0).
                number = data.get("limit", query.get("limit"))
                literal = isinstance(number, float) and number.is_integer() and 1 <= number <= 100
                reason = name == "updateNamespace" and any(unicodedata.category(c) == "Cc" for c in data.get("reason", "").strip())
                unchanged = name == "updateNamespace" and all(data.get(k, before.json().get(k, "")).strip() == before.json().get(k, "") for k in ("displayName", "description"))
                whitespace = name in {"createNamespace", "updateNamespace"} and (
                    "displayName" in data and not data["displayName"].strip())
                blank_identifier = name == "listResources" and "canonicalIdentifier" in query and not canonical_filter(query["canonicalIdentifier"])
                assert cursor or whitespace or literal or reason or unchanged or blank_identifier, "schema-valid request rejected outside reviewed domain constraints"
                validate(case, response, "negative")
            elif mode == "positive" and response.status_code == 409 and name == "createNamespace":
                assert response.json()["code"] == "CONFLICT", "unexpected namespace conflict"
                # The exact generated slug must exist; do not allow arbitrary conflicts.
                page = checked("listNamespaces", query={"limit": 100}).json()
                assert any(item["slug"] == case.body["slug"] for item in page["items"])
                case.validate_response(response, checks=(status_code_conformance, content_type_conformance, response_schema_conformance))
            else:
                validate(case, response, mode)
            if response.status_code == 200 and name in {"listResources", "listResourceVersions"}:
                query = {key: values[0] for key, values in parse_qs(urlsplit(response.request.url).query, keep_blank_values=True).items()}
                assert not query.get("cursor"), "unbound generated cursor succeeded"
                check_page(name, response.json(), RESOURCES if name == "listResources" else VERSIONS,
                           limit=int(query.get("limit", 20)), kind=query.get("kind", ""),
                           canonical_identifier=query.get("canonicalIdentifier", ""), parent=PARENT)
            if response.status_code >= 400 and name in {"createNamespace", "updateNamespace"}:
                after = checked("getNamespace") if name == "updateNamespace" else checked("listNamespaces", query={"limit": 100})
                assert after.json() == before.json(), "rejected mutation changed governed state"
            COUNTS[label] += 1
        except BaseException:
            # Synthetic generated payload only. Never retain credentials, response
            # bodies or fixture identities. The next run supplies fresh prerequisites.
            payload = {"operation": name, "mode": mode, "body": case.body if isinstance(case.body, (dict, list, str, int, float, bool, type(None))) else None,
                       "query": case.query, "status": response.status_code}
            Path(os.environ["CATALOG_FUNCTIONAL_STATE_DIRECTORY"], "generated-reproducer.json").write_text(json.dumps(payload))
            raise
        finally:
            write_counts(SESSION, COUNTS, SCHEMA)

    exercise()
    assert COUNTS[label] > 0, "empty generation"


def test_sequences():
    assert CONTROLS_PASSED, "successful controls must precede sequences"
    completed = 0
    steps = dict.fromkeys(KINDS, 0)

    @seed(SEED)
    @settings(max_examples=20, deadline=None, database=None, phases=(Phase.generate, Phase.shrink))
    @given(st.permutations(KINDS), st.lists(st.sampled_from(KINDS), max_size=2),
           st.text(alphabet="abcdefghijklmnopqrstuvwxyz0123456789", min_size=1, max_size=20))
    def exercise(order, extra, label):
        nonlocal completed
        # Every example, including every shrink, creates its own state. UUIDs
        # provide isolation; generated order/text are the reproducible inputs.
        try:
            created = checked("createNamespace", body={"slug": "sequence-" + uuid.uuid4().hex, "displayName": "Sequence initial"})
            identifier = created.json()["id"]
            old_etag = created.headers["etag"][0]
            key = str(uuid.uuid4())
            first_body = {"displayName": "Sequence first " + label, "reason": "Generated sequence"}
            replay_case, first = call("updateNamespace", namespace_id=identifier, body=first_body, etag=old_etag, key=key)
            validate(replay_case, first, "positive")
            check_reply("change", first.status_code, first.json(), first.headers["etag"][0])
            check_state("change", created.json(), first.json(), first_body["displayName"])
            replay = (first.json(), first.headers["etag"][0])
            current = checked("getNamespace", namespace_id=identifier)
            assert stored(current.json()) == stored(first.json()) and current.headers["etag"][0] == replay[1]
            for index, kind in enumerate([*order, *extra]):
                before = current.json()
                body = {"displayName": "Sequence " + str(index) + " " + label, "reason": "Generated sequence"}
                if kind == "replay":
                    # Reuse the entire bound Case: method, path, body, actor,
                    # content type, query, original ETag and idempotency key.
                    case = replay_case
                    response = case.call(base_url=SESSION.base, session=SESSION)
                    assert response.content == first.content, "replay changed response bytes"
                    assert response.headers["content-type"] == first.headers["content-type"], "replay changed response media type"
                    assert response.request.body == first.request.body, "replay request serialization changed"
                else:
                    case, response = call("getNamespace" if kind == "tenant-read" else "updateNamespace",
                                          namespace_id=identifier, body=None if kind == "tenant-read" else body,
                                          etag=old_etag if kind == "stale" else current.headers["etag"][0],
                                          key=key if kind == "reuse" else None,
                                          identity="local-reader" if kind == "reader" else
                                          "other-tenant-admin" if kind.startswith("tenant-") else "local-admin")
                case.validate_response(response, checks=(status_code_conformance, content_type_conformance, response_schema_conformance))
                check_reply(kind, response.status_code, response.json(), response.headers.get("etag", [None])[0], replay)
                current = checked("getNamespace", namespace_id=identifier)
                check_state(kind, before, current.json(), body["displayName"])
                assert current.headers["etag"][0] == '"rev-' + str(current.json()["revision"]) + '"'
                if kind == "change":
                    assert stored(response.json()) == stored(current.json()) and response.headers["etag"] == current.headers["etag"]
                steps[kind] += 1
            completed += 1
        except AssertionError:
            Path(os.environ["CATALOG_FUNCTIONAL_STATE_DIRECTORY"], "generated-sequence-reproducer.json").write_text(
                json.dumps({"actions": [*order, *extra], "label": label}))
            raise
        finally:
            write_counts(SESSION, COUNTS, SCHEMA, {"completed": completed, "steps": steps})

    exercise()
    assert completed == 20 and all(count >= 20 for count in steps.values())
