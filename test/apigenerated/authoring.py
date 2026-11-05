"""Separate bounded Resource/Version creation selection on the owned fixture."""

import json
import hashlib
import os
from pathlib import Path
import re
import shlex
import time
import uuid

from hypothesis import HealthCheck, Phase, assume, given, seed, settings, strategies as st
from jsonschema_rs import Draft202012Validator
import pytest
import requests
from schemathesis.generation import GenerationMode

from support import Session, authoring_exchange, schema, validate
from authoring_cases import bind_body, native, reproducer_body, resource_created, valid_wire_body, version_created
from generated_api import AUTHORING_CASES, AUTHORING_OPERATIONS, AUTHORING_READS, EXAMPLES, omitted_operations
from runner import command
from authoring_state import CORE, checked_snapshot, created_delta, created_events, snapshot_sql
from authoring_cases import unchanged

SCHEMA = schema()
SESSION = Session(os.environ["CATALOG_FUNCTIONAL_BASE_URL"], "authoring")
COUNTS = {}
READS = dict.fromkeys(AUTHORING_READS, 0)
NAMESPACE = None
TENANT = None
CONTROL = False
STATE_CHECKS = 0
MUTATIONS = 0
EVENT_CHECKS = 0


def headers():
    return {"Authorization": "Bearer local-admin", "Idempotency-Key": str(uuid.uuid4())}


def call(name, body=None, **path):
    operation = SCHEMA.find_operation_by_id(name)
    case = operation.Case(headers=headers(), path_parameters=path or None,
                          **({"body": body, "media_type": "application/json"} if body is not None else {}))
    response = case.call(base_url=SESSION.base, session=SESSION)
    validate(case, response, "positive")
    if name in AUTHORING_OPERATIONS:
        request_ids = response.headers.get("x-request-id", [])
        assert len(request_ids) == 1 and request_ids[0], "creation response lacks one request ID"
        verify_events(name, response.json(), request_ids[0])
    if name in READS:
        READS[name] += 1
    return response.json()


def sql_query(sql):
    """Use only the supervisor-owned database; never a supplied DSN."""
    assert os.environ["CATALOG_FUNCTIONAL_PROFILE"] == "generated-api"
    suffix = os.environ["CATALOG_FUNCTIONAL_SUFFIX"]
    assert re.fullmatch(r"[a-f0-9]{12}", suffix), "unowned SQL fixture"
    argv = shlex.split(os.environ.get("CATALOG_FUNCTIONAL_COMPOSE_COMMAND", "docker compose"))
    raw = command([*argv, "--env-file", "/dev/null", "-f", "compose.yaml", "-f", "test/functional/compose.yaml",
                   "-f", "test/functional/generated-api.compose.yaml", "-p", "open-agentic-catalog-functional-" + suffix,
                   "exec", "-T", "postgres", "psql", "-X", "-qAt", "-U", "catalog", "-d", "catalog", "-v", "ON_ERROR_STOP=1", "-c", sql],
                  dict(os.environ), min(5, SESSION.deadline - time.monotonic()), capture=True, limit=65536)
    assert time.monotonic() < SESSION.deadline, "authoring state/time budget"
    return raw


def verify_events(operation, created, request_id):
    global EVENT_CHECKS
    target = created["id"] if operation == "createResource" else created["version"]["id"]
    assert re.fullmatch(r"[a-f0-9-]{36}", target)
    value = json.loads(sql_query("BEGIN READ ONLY; SET LOCAL statement_timeout='3s'; SELECT json_build_object("
        f"'audit',(SELECT json_agg(r) FROM (SELECT * FROM audit_events WHERE tenant_id='{TENANT}' AND target_id='{target}' ORDER BY id LIMIT 2) r),"
        f"'outbox',(SELECT json_agg(r) FROM (SELECT * FROM outbox_events WHERE tenant_id='{TENANT}' AND subject='{target}' ORDER BY id LIMIT 2) r)); COMMIT;"))
    created_events(value, operation, target, TENANT, request_id)
    EVENT_CHECKS += 1


def snapshot():
    global STATE_CHECKS
    value = checked_snapshot(json.loads(sql_query(snapshot_sql())))
    STATE_CHECKS += 1
    return value


def read_success(name, body, created):
    if name == "createResource":
        SESSION.resource_ids.add(created["id"])
        resource_created(body, created, call("getResource", resourceId=created["id"]), TENANT)
    else:
        version, revision = created["version"], created["revision"]
        SESSION.version_ids.add(version["id"])
        SESSION.revision_ids.add(revision["id"])
        current = call("getResourceVersion", versionId=version["id"])
        exact = call("getVersionRevision", revisionId=revision["id"])
        assert exact["namespaceId"] == NAMESPACE, "wrong revision namespace"
        assert exact["evidence"]["id"] == created["evidence"]["id"], "wrong readback evidence reference"
        version_created(body, SESSION.version_parent, created, current, exact["revision"], TENANT)


def validators(name):
    source = "CreateResource" if name == "createResource" else "CreateVersion"
    raw = SCHEMA.raw_schema
    definition = raw["components"]["schemas"][source]
    def validator(value):
        return Draft202012Validator({**value, "components": raw["components"]}, validate_formats=True)
    return validator(definition), {key: validator(value) for key, value in definition["properties"].items()}


def wire_valid(case, validator):
    prepared = requests.Request(**case.as_transport_kwargs(base_url=SESSION.base)).prepare()
    assert prepared.headers["Content-Type"] == "application/json"
    return valid_wire_body(prepared.body, validator)


def test_authoring_controls():
    global NAMESPACE, TENANT, CONTROL
    namespace = call("createNamespace", {"slug": "generated-authoring", "displayName": "Generated authoring"})
    NAMESPACE, TENANT = namespace["id"], namespace["tenantId"]
    assert TENANT == "00000000-0000-7000-8000-000000000001", "wrong fixture tenant"
    body = {"namespaceId": NAMESPACE, "kind": "agent", "canonicalIdentifier": "urn:air:catalog.example.com:generated-authoring:control"}
    before_parent = snapshot()
    parent = call("createResource", body)
    read_success("createResource", body, parent)
    created_delta(before_parent, snapshot(), "createResource")
    SESSION.version_parent = parent["id"]
    before_version = snapshot()
    body = {"version": "control", "representations": native("control")}
    created = call("createResourceVersion", body, resourceId=SESSION.version_parent)
    read_success("createResourceVersion", body, created)
    after_version = snapshot()
    created_delta(before_version, after_version, "createResourceVersion")
    # Qualify the database oracle with an actual committed creation.
    before = snapshot()
    body = {"namespaceId": NAMESPACE, "kind": "skill", "canonicalIdentifier": "urn:air:catalog.example.com:generated-authoring:state-control"}
    extra = call("createResource", body)
    read_success("createResource", body, extra)
    after = snapshot()
    created_delta(before, after, "createResource")
    before_skill = snapshot()
    original_parent = SESSION.version_parent
    SESSION.version_parent = extra["id"]
    body = {"version": "blob-control", "representations": [{"role": "primary", "mediaType": "application/agent-skills+md",
        "content": "---\nname: generated-authoring-control\ndescription: Inert local blob retention control\n---\n# Control\nRead this fixture.\n"}]}
    created = call("createResourceVersion", body, resourceId=SESSION.version_parent)
    read_success("createResourceVersion", body, created)
    created_delta(before_skill, snapshot(), "createResourceVersion", blob=True)
    SESSION.version_parent = original_parent
    for name, body in (("createResource", {"namespaceId": NAMESPACE, "kind": "Agent", "canonicalIdentifier": "urn:air:catalog.example.com:generated-authoring:invalid"}),
                       ("createResourceVersion", {"version": "bad-role", "representations": [{**native("bad-role")[0], "role": "unsupported"}]})):
        case = SCHEMA.find_operation_by_id(name).Case(headers=headers(), body=body, media_type="application/json",
                    path_parameters={"resourceId": SESSION.version_parent} if name == "createResourceVersion" else None)
        assert not wire_valid(case, validators(name)[0])
        authoring_exchange(case, SESSION, "negative", snapshot, None)
    # Deliberate fixture write: a clean 400 must still trip the retained-state
    # assertion if only a previously unobserved side table changes.
    invalid = {"namespaceId": NAMESPACE, "kind": "Agent", "canonicalIdentifier": "urn:air:catalog.example.com:generated-authoring:side-only"}
    case = SCHEMA.find_operation_by_id("createResource").Case(headers=headers(), body=invalid, media_type="application/json")
    first = None
    def inject_before_readback():
        nonlocal first
        if first is None:
            first = snapshot()
            return first
        raw = json.dumps({"calibration": "side-only-" + uuid.uuid4().hex}).encode()
        digest = hashlib.sha256(raw).hexdigest()
        sql_query("BEGIN; SET LOCAL statement_timeout='3s'; INSERT INTO document_blobs "
                  "(tenant_id,id,media_type,content_digest,size_bytes,content,created_at) VALUES ("
                  f"'{TENANT}','{digest}','application/json','sha256:{digest}',{len(raw)},decode('{raw.hex()}','hex'),clock_timestamp()); COMMIT;")
        after_insert = snapshot()
        assert all(first[name] == after_insert[name] for name in CORE), "side-only calibration changed core rows"
        return after_insert
    with pytest.raises(AssertionError, match="rejected authoring mutation changed stored state"):
        authoring_exchange(case, SESSION, "negative", inject_before_readback, None)
    assert SESSION.last_authoring_refusal, "side-only calibration did not receive a clean refusal"
    # Clone valid immutable evidence with a fresh event identity. These writes
    # calibrate the projection only; they are not alleged application defects.
    for table, target in (("audit_events", "target_id"), ("outbox_events", "subject")):
        before_event = snapshot()
        replacement = json.dumps({"id": str(uuid.uuid4())})
        if table == "outbox_events":
            replacement = json.dumps({"id": str(uuid.uuid4()), "cleanup_after": None, "dispatch_after": None,
                                      "lease_owner": None, "lease_expires_at": None})
        inserted = json.loads(sql_query("BEGIN; SET LOCAL statement_timeout='3s'; WITH inserted AS ("
            f"INSERT INTO {table} SELECT (jsonb_populate_record(NULL::{table},to_jsonb(original)||'{replacement}'::jsonb)).* "
            f"FROM {table} original WHERE tenant_id='{TENANT}' AND {target}='{extra['id']}' LIMIT 1 RETURNING id) "
            "SELECT count(*) FROM inserted; COMMIT;"))
        assert inserted == 1, "side-only event calibration did not insert one row"
        after_event = snapshot()
        assert after_event[table]["count"] == before_event[table]["count"] + 1 and after_event[table]["digest"] != before_event[table]["digest"], "event projection missed committed evidence"
        assert all(before_event[name] == after_event[name] for name in before_event if name != table), "event calibration changed unrelated rows"
        with pytest.raises(AssertionError, match="rejected authoring mutation changed stored state"):
            unchanged(before_event, after_event)
    # The worker can mark an acknowledged event for later cleanup. Only this
    # legal bookkeeping field changes; event identity and payload remain fixed.
    before_mark = snapshot()
    marked = json.loads(sql_query("BEGIN; SET LOCAL statement_timeout='3s'; WITH changed AS ("
        "UPDATE outbox_events SET cleanup_after=clock_timestamp()+interval '1 hour' "
        f"WHERE tenant_id='{TENANT}' AND subject='{parent['id']}' AND event_type='com.open.agentic.catalog.resource.created.v1' "
        "AND published_at IS NOT NULL AND cleanup_after IS NULL RETURNING id) SELECT count(*) FROM changed; COMMIT;"))
    assert marked == 1, "worker-bookkeeping control did not mark one acknowledged event"
    unchanged(before_mark, snapshot())
    CONTROL = True


@pytest.mark.parametrize("name,mode", [(name, mode) for name in AUTHORING_OPERATIONS for mode in ("positive", "negative")])
def test_authoring(name, mode):
    global MUTATIONS
    assert CONTROL, "successful authoring controls must precede generated traffic"
    operation = SCHEMA.find_operation_by_id(name)
    validator, fields = validators(name)
    strategy = operation.as_strategy(generation_mode=GenerationMode(mode), headers=headers(), query={}, cookies={},
                path_parameters={"resourceId": SESSION.version_parent} if name == "createResourceVersion" else {})
    label = f"test_authoring[{name}-{mode}]"
    COUNTS[label] = 0
    # AIR name segments permit 64 bytes. Version labels permit 128 characters.
    remaining = 30 if name == "createResource" else 94

    @seed(int(os.environ["SCHEMATHESIS_SEED"]))
    @settings(max_examples=EXAMPLES, deadline=None, database=None, phases=(Phase.generate, Phase.shrink),
              suppress_health_check=(HealthCheck.too_slow, HealthCheck.filter_too_much))
    @given(strategy, st.one_of(st.just("x" * remaining), st.text(alphabet="abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._", max_size=remaining)))
    def exercise(case, fragment):
        global MUTATIONS
        # A fresh prefix prevents collisions. Hypothesis varies the remaining
        # label, including the 128-character Version boundary.
        distinct = "g" + uuid.uuid4().hex + "-" + fragment
        case.body = bind_body(name, case.body, NAMESPACE, distinct, lambda key, value: fields[key].is_valid(value))
        case.headers.update(headers())
        case.media_type = "application/json"
        valid = wire_valid(case, validator)
        # Binding can repair generated negatives. Only still-invalid wire bodies
        # reach the API or contribute to the rejection/example counts.
        assume(valid == (mode == "positive"))
        MUTATIONS += 1
        assert MUTATIONS <= 100, "authoring mutation budget"
        try:
            authoring_exchange(case, SESSION, mode, snapshot, lambda body, created: read_success(name, body, created))
            COUNTS[label] += 1
        except BaseException:
            Path(os.environ["CATALOG_FUNCTIONAL_STATE_DIRECTORY"], "generated-authoring-reproducer.json").write_text(
                json.dumps({"operation": name, "mode": mode, "body": reproducer_body(case.body)}))
            raise
        finally:
            omitted = omitted_operations([result.ok().definition.raw["operationId"] for result in SCHEMA.get_all_operations()], AUTHORING_OPERATIONS)
            Path(os.environ["CATALOG_FUNCTIONAL_STATE_DIRECTORY"], "generated-authoring-counts.json").write_text(json.dumps({
                "operations": list(AUTHORING_OPERATIONS), "controls": list(AUTHORING_OPERATIONS) if CONTROL else [],
                "counts": COUNTS, "requests": SESSION.count, "stateChecks": STATE_CHECKS,
                "readbacks": READS, "mutations": MUTATIONS, "eventChecks": EVENT_CHECKS, "omittedOperations": omitted,
                "sqlCalibrations": ["side-table-rejected-write", "audit-side-only", "outbox-side-only", "worker-bookkeeping"] if CONTROL else []}))
    exercise()
    assert COUNTS[label] == EXAMPLES and label in AUTHORING_CASES
