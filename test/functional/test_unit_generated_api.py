"""No optional imports or processes are needed to reject incomplete evidence."""

import copy
import re
from pathlib import Path
import tempfile
import unittest
from unittest import mock
import xml.etree.ElementTree as ET

import generated_api as generated
import runner


# Read the checked-in source without optional parsers or a compiled driver.
AUTHORITY = sorted(re.findall(r"^ +operationId: ([A-Za-z][A-Za-z0-9_]*)$", (generated.ROOT / "api/openapi/control-v1.yaml").read_text(), re.M))


def coverage():
    return {"formatVersion": 1, "denominator": AUTHORITY,
            "rows": [{"step": next(iter(runner.selected_steps("generated-api"))), "operationId": "getNamespace", "status": 200, "contract": "passed", "outcome": "positive", "count": 1}],
            "unobservedCollectors": ["smoke", "remote-cli", "shell-probes", "publication-report-sdk"]}


def evidence():
    return dict(tool="schemathesis-4.30.1", operations=list(generated.OPERATIONS), controls=list(generated.OPERATIONS),
                tests=len(generated.CASES) + 2, setup=dict(generated.SETUP), sequences={"completed": 20, "steps": dict.fromkeys(generated.KINDS, 20)}, calibrations=len(generated.CALIBRATIONS), requests=800, counts=dict.fromkeys(generated.CASES, generated.EXAMPLES), seed=1, lockDigest="a" * 64, omittedOperations=sorted(set(AUTHORITY) - set(generated.OPERATIONS)))


def authoring_evidence():
    return dict(operations=list(generated.AUTHORING_OPERATIONS), controls=list(generated.AUTHORING_OPERATIONS),
                tests=len(generated.AUTHORING_CASES) + 1, calibrations=len(generated.AUTHORING_CALIBRATIONS),
                requests=154, mutations=80, stateChecks=100, eventChecks=4, counts=dict.fromkeys(generated.AUTHORING_CASES, 20),
                readbacks={"getResource": 22, "getResourceVersion": 22, "getVersionRevision": 22}, omittedOperations=sorted(set(AUTHORITY) - set(generated.AUTHORING_OPERATIONS)), sqlCalibrations=["side-table-rejected-write", "audit-side-only", "outbox-side-only", "worker-bookkeeping"])


class GeneratedTests(unittest.TestCase):
    def test_inventory_and_exact_partitions_use_independent_authority(self):
        self.assertGreater(len(AUTHORITY), len(generated.OPERATIONS))
        for factory, validate in ((evidence, generated.valid_evidence), (authoring_evidence, generated.valid_authoring)):
            data = factory()
            self.assertTrue(validate(data, AUTHORITY))
            omitted = data["omittedOperations"]
            for names in (omitted[1:], sorted([*omitted, "unknownOperation"]), sorted([*omitted, omitted[0]]), sorted([*omitted, data["operations"][0]])):
                self.assertFalse(validate({**data, "omittedOperations": names}, AUTHORITY))
            self.assertFalse(validate({**data, "operations": data["operations"][::-1]}, AUTHORITY))
            for authority in (None, [], {}, [""], [True], AUTHORITY + [AUTHORITY[-1]], AUTHORITY[::-1]):
                self.assertFalse(validate(data, authority))
            future = sorted([*AUTHORITY, "futureOperation"])
            self.assertFalse(validate(data, future))
            auto = generated.omitted_operations(future, data["operations"])
            self.assertEqual(auto, sorted([*omitted, "futureOperation"]))
            self.assertTrue(validate({**data, "omittedOperations": auto}, future))
        generated.require_inventory(AUTHORITY[::-1], AUTHORITY)
        for loaded in (AUTHORITY[1:], AUTHORITY + [AUTHORITY[0]], AUTHORITY + ["unknownOperation"]):
            with self.assertRaises(AssertionError):
                generated.require_inventory(loaded, AUTHORITY)
        base = dict(profile="generated-api", operationCoverage=coverage(), generatedAPI={**evidence(), "authoring": authoring_evidence()}, images={"api": "owned"}, servedVersion={"version": "functional"}, smoke={"resourceCount": 3})
        steps = list(runner.selected_steps("generated-api"))
        for selection in (None, "authoring"):
            changed = copy.deepcopy(base)
            item = changed["generatedAPI"] if selection is None else changed["generatedAPI"][selection]
            item["omittedOperations"].pop()
            self.assertFalse(runner.finalize(changed, steps, None, True))
        changed = copy.deepcopy(base)
        del changed["operationCoverage"]
        self.assertFalse(runner.finalize(changed, steps, None, True))

    def test_authoring_snapshot_bounds_keys_and_stable_event_projection(self):
        from authoring_state import BOOKKEEPING, TABLES, checked_snapshot, created_delta, snapshot_sql
        sql = snapshot_sql()
        self.assertTrue(sql.startswith("BEGIN READ ONLY;"))
        self.assertIn("statement_timeout='3s'", sql)
        self.assertEqual(sql.count("LIMIT 1001"), len(TABLES))
        self.assertEqual(sql.count("CASE WHEN limits.ok THEN"), len(TABLES))
        self.assertIn("ORDER BY tenant_id,version_revision_id,run_number", sql)
        self.assertIn("ORDER BY tenant_id,id", sql)
        for field in BOOKKEEPING:
            self.assertIn("'" + field + "'", sql)
        before = {name: {"count": 1, "digest": "a" * 32} for name in TABLES}
        self.assertEqual(checked_snapshot(before), before)
        for item in ({"count": 1001, "digest": "a" * 32}, {"count": True, "digest": "a" * 32}, {"count": 1, "digest": None}):
            with self.assertRaises(AssertionError):
                checked_snapshot({**before, "document_blobs": item})
        for operation, changes in (("createResource", {"resources", "audit_events", "outbox_events"}),
                                   ("createResourceVersion", set(TABLES) - {"resources"})):
            after = copy.deepcopy(before)
            for name in changes:
                after[name] = {"count": 2, "digest": "b" * 32}
            created_delta(before, after, operation, blob=operation == "createResourceVersion")
            for name in TABLES:
                changed = copy.deepcopy(after)
                changed[name] = before[name] if name in changes else {"count": 2, "digest": "b" * 32}
                with self.subTest(name=name), self.assertRaises(AssertionError):
                    created_delta(before, changed, operation, blob=operation == "createResourceVersion")

    def test_creation_events_bind_action_target_tenant_request_and_payload(self):
        from authoring_state import AUDIT_ACTIONS, OUTBOX_TYPES, created_events
        vocabulary = (generated.ROOT / "internal/events/events.go").read_text()
        for action in AUDIT_ACTIONS:
            self.assertIn('"' + action.replace("_", ".") + '"', vocabulary)
        for operation, kind in (("createResource", "resource"), ("createResourceVersion", "resource_version")):
            action = kind + ".created"
            digest = "sha256:" + "a" * 64
            audit = {"id": "audit", "tenant_id": "tenant", "action": action, "target_type": kind, "target_id": "target", "request_id": "request", "actor": "member", "after_digest": digest, "occurred_at": "now"}
            outbox = {"id": "outbox", "tenant_id": "tenant", "event_type": "com.open.agentic.catalog." + action.replace("_", ".") + ".v1", "subject": "target", "data": {"action": action.replace("_", "."), "targetType": kind, "targetId": "target", "digest": digest}, "created_at": "now", "is_internal": False}
            self.assertIn(outbox["event_type"], OUTBOX_TYPES)
            value = {"audit": [audit], "outbox": [outbox]}
            created_events(value, operation, "target", "tenant", "request")
            for section, field, wrong in (("audit", "tenant_id", "foreign"), ("audit", "action", "other"), ("audit", "target_type", "other"), ("audit", "target_id", "other"), ("audit", "request_id", "other"), ("audit", "after_digest", "invalid"), ("outbox", "created_at", "later"), ("outbox", "subject", "other"), ("outbox", "event_type", "other"), ("outbox", "is_internal", True), ("outbox", "data", {})):
                bad = copy.deepcopy(value)
                bad[section][0][field] = wrong
                with self.subTest(section=section, field=field), self.assertRaises(AssertionError):
                    created_events(bad, operation, "target", "tenant", "request")
            for section in value:
                with self.assertRaises(AssertionError):
                    created_events({**value, section: []}, operation, "target", "tenant", "request")

    def test_tool_lock_requires_hashes_exact_pins_and_unique_packages(self):
        valid = "schemathesis==4.30.1 --hash=sha256:" + "a" * 64 + "\n"
        for raw in ("", "# no packages\n", "schemathesis==4.30.1\n", valid + valid,
                    valid.replace("4.30.1", "4.27.0"), valid + "requests>=2\n"):
            with self.subTest(raw=raw), mock.patch.object(Path, "read_text", return_value=raw), self.assertRaises(runner.Failure):
                generated.pins()
        with mock.patch.object(Path, "read_text", return_value="# pinned\n" + valid):
            self.assertEqual(generated.pins(), {"schemathesis": "4.30.1"})

    def test_native_report_requires_exact_complete_selection(self):
        valid = '<testsuites><testsuite><testcase name="control"/><testcase name="negative"/></testsuite></testsuites>'
        self.assertEqual(generated.junit(valid.encode(), ["control", "negative"]), 2)
        for raw in ("", valid.replace('name="negative"', 'name="control"'), valid.replace("<testsuite>", '<testsuite errors="1">'),
                    valid.replace('<testcase name="negative"/>', ''),
                    valid.replace('<testcase name="negative"/>', '<testcase name="negative"><skipped/></testcase>'),
                    valid.replace('<testcase name="negative"/>', '<testcase name="negative"><failure/></testcase>'),
                    '<!DOCTYPE x>' + valid, 'x' * (1024*1024+1)):
            with self.subTest(raw=raw[:50]), self.assertRaises((runner.Failure, ET.ParseError)):
                generated.junit(raw.encode(), ["control", "negative"])

    def test_missing_controls_incomplete_generation_and_budget_fail(self):
        self.assertTrue(generated.valid_evidence(evidence(), AUTHORITY))
        for field, value in (("operations", []), ("controls", []), ("tests", 0), ("calibrations", 0),
                             ("requests", True), ("requests", 200), ("setup", {}), ("setup", {"getResource": 3, "createResourceVersion": True}), ("lockDigest", ""), ("omittedOperations", []), ("requests", 1001), ("seed", -1), ("counts", {}), ("sequences", {}), ("sequences", {"completed": 20, "steps": {}})):
            with self.subTest(field=field):
                data = evidence()
                data[field] = value
                self.assertFalse(generated.valid_evidence(data, AUTHORITY))
        data = evidence()
        data["counts"][generated.CASES[0]] = 19
        self.assertFalse(generated.valid_evidence(data, AUTHORITY))
        data = evidence()
        data["counts"][generated.CASES[0]] = 21
        self.assertFalse(generated.valid_evidence(data, AUTHORITY))
        self.assertEqual(len(generated.CASES) * generated.EXAMPLES, 280)
        data = evidence()
        data["counts"]["unselected-positive"] = 20
        self.assertFalse(generated.valid_evidence(data, AUTHORITY))

    def test_profile_requires_evidence_all_steps_and_cleanup(self):
        base = dict(profile="generated-api", operationCoverage=coverage(), generatedAPI=evidence(), images={"api": "owned"}, servedVersion={"version": "functional"}, smoke={"resourceCount": 3})
        base["generatedAPI"]["authoring"] = authoring_evidence()
        steps = list(runner.selected_steps("generated-api"))
        self.assertTrue(runner.finalize(copy.deepcopy(base), steps, None, True))
        for completed, failure, cleanup in ((steps[:-1], None, True), (steps, "deadline", True), (steps, None, False)):
            self.assertFalse(runner.finalize(copy.deepcopy(base), completed, failure, cleanup))
        del base["generatedAPI"]
        self.assertFalse(runner.finalize(base, steps, None, True))

    def test_authoring_report_requires_both_generators_controls_readbacks_and_state_checks(self):
        data = authoring_evidence()
        self.assertTrue(generated.valid_authoring(data, AUTHORITY))
        for key, value in (("operations", []), ("controls", []), ("tests", 0), ("calibrations", 0),
                           ("counts", {}), ("requests", True), ("requests", 153), ("requests", 1001),
                           ("eventChecks", 3), ("eventChecks", True), ("sqlCalibrations", []), ("mutations", 79), ("mutations", 81), ("stateChecks", 99), ("stateChecks", 101),
                           ("readbacks", {}), ("omittedOperations", []), ("omittedOperations", ["createResource"])):
            with self.subTest(key=key, value=value):
                self.assertFalse(generated.valid_authoring({**data, key: value}, AUTHORITY))
        for name in generated.AUTHORING_CASES:
            self.assertFalse(generated.valid_authoring({**data, "counts": {**data["counts"], name: 19}}, AUTHORITY))
        for name in generated.AUTHORING_READS:
            self.assertFalse(generated.valid_authoring({**data, "readbacks": {**data["readbacks"], name: 0}}, AUTHORITY))
        base = dict(profile="generated-api", generatedAPI=evidence())
        self.assertFalse(runner.finalize(base, list(runner.selected_steps("generated-api")), None, True))

    def test_authoring_binding_preserves_missing_invalid_fields_and_does_not_mutate_source(self):
        from authoring_cases import bind_body, native
        body = {"namespaceId": "unbound", "canonicalIdentifier": "generated", "kind": "invalid", "extra": True}
        bound = bind_body("createResource", body, "owned", "distinct", lambda *_: True)
        self.assertEqual(bound, {**body, "namespaceId": "owned", "canonicalIdentifier": "urn:air:catalog.example.com:generated-authoring:distinct"})
        self.assertEqual(body["namespaceId"], "unbound")
        self.assertEqual(bind_body("createResource", {}, "owned", "distinct", lambda *_: True), {})
        bad = {"version": None, "representations": [{"url": "https://example.invalid"}], "Extra": 1}
        self.assertEqual(bind_body("createResourceVersion", bad, "owned", "distinct", lambda *_: False),
                         {**bad, "representations": native("distinct")})
        for body in (None, [], 0, "invalid"):
            self.assertEqual(bind_body("createResourceVersion", body, "owned", "distinct", lambda *_: True), body)

    def test_authoring_wire_checks_and_binary_reproducers_do_not_acquire_tools(self):
        from authoring_cases import reproducer_body, valid_wire_body
        class Validator:
            def is_valid(self, body):
                return body == {"version": "valid"}
        for raw in (b"", b"\x80", b"{", b"null", b"[]", None, b'{"version": null}', '{"version": "valid"}'.encode("utf-16")):
            with self.subTest(raw=raw):
                self.assertFalse(valid_wire_body(raw, Validator()))
        self.assertTrue(valid_wire_body(b'{"version": "valid"}', Validator()))
        self.assertEqual(reproducer_body(b"\x80"), {"encoding": "base64", "value": "gA=="})
        self.assertEqual(reproducer_body({"version": None}), {"version": None})

    def test_authoring_validator_errors_cannot_count_as_negative_evidence(self):
        from authoring_cases import valid_wire_body
        for error in (ValueError("invalid schema"), TypeError("validator failure"), UnicodeError("validator failure")):
            validator = mock.Mock()
            validator.is_valid.side_effect = error
            with self.subTest(error=error), self.assertRaises(type(error)):
                valid_wire_body(b'{"version": "valid"}', validator)
            validator.is_valid.assert_called_once_with({"version": "valid"})

    def test_authoring_state_checks_catch_new_resources_versions_and_orphan_revisions(self):
        from authoring_cases import unchanged
        from authoring_state import TABLES
        before = {name: {"count": 1, "digest": "original"} for name in TABLES}
        unchanged(before, copy.deepcopy(before))
        for name in before:
            for item in ({"count": 2, "digest": "original"}, {"count": 2, "digest": "changed"}, {"count": 1, "digest": "changed"}):
                with self.subTest(name=name, item=item), self.assertRaisesRegex(AssertionError, "changed stored state"):
                    unchanged(before, {**before, name: item})

    def test_authoring_readback_checks_identity_parent_kind_and_immutable_evidence(self):
        import hashlib
        import json
        from authoring_cases import native, resource_created, version_created
        body = {"namespaceId": "namespace", "canonicalIdentifier": "urn:air:example.com:owned:one", "kind": "agent"}
        resource = {**body, "id": "resource", "revision": 1, "tenantId": "tenant"}
        resource_created(body, resource, dict(resource), "tenant")
        for key, value in (("id", "other"), ("namespaceId", "other"), ("kind", "skill"), ("canonicalIdentifier", "other"), ("revision", 2)):
            with self.subTest(key=key), self.assertRaises(AssertionError):
                resource_created(body, resource, {**resource, key: value}, "tenant")
            wrong = {**resource, key: value}
            if key != "id":
                with self.subTest(key=key, matching_wrong_readback=True), self.assertRaises(AssertionError):
                    resource_created(body, wrong, dict(wrong), "tenant")
        wrong_tenant = {**resource, "tenantId": "foreign"}
        with self.assertRaisesRegex(AssertionError, "wrong Resource tenant"):
            resource_created(body, wrong_tenant, dict(wrong_tenant), "tenant")
        body = {"version": "distinct", "representations": native("distinct")}
        raw = json.dumps(body["representations"][0]["data"], sort_keys=True, separators=(",", ":")).encode()
        version = {"id": "version", "resourceId": "resource", "tenantId": "tenant", "version": "distinct", "revision": 1, "candidateRevisionId": "revision"}
        representation = {"role": "primary", "mediaType": "application/a2a-agent-card+json", "size": len(raw),
                          "contentDigest": "sha256:" + hashlib.sha256(raw).hexdigest(), "validationState": "valid"}
        revision = {"id": "revision", "resourceVersionId": "version", "tenantId": "tenant", "revisionNumber": 1,
                    "representations": [representation], "evidenceSetId": "evidence"}
        created = {"version": version, "revision": revision, "evidence": {"id": "evidence"}}
        version_created(body, "resource", created, dict(version), dict(revision), "tenant")
        for section, key, value in (("version", "resourceId", "other"), ("version", "version", "other"),
                                    ("version", "candidateRevisionId", "other"), ("version", "sealedRevisionId", "sealed"),
                                    ("revision", "resourceVersionId", "other"), ("revision", "revisionNumber", 2),
                                    ("revision", "tenantId", "other"), ("revision", "evidenceSetId", "other")):
            bad = copy.deepcopy(created)
            bad[section][key] = value
            with self.subTest(section=section, key=key), self.assertRaises(AssertionError):
                version_created(body, "resource", bad, bad["version"], bad["revision"], "tenant")
        for key, value in (("role", "metadata"), ("mediaType", "text/plain"), ("size", 0),
                           ("contentDigest", "sha256:" + "0" * 64), ("validationState", "invalid")):
            bad = copy.deepcopy(created)
            bad["revision"]["representations"][0][key] = value
            with self.subTest(key=key), self.assertRaises(AssertionError):
                version_created(body, "resource", bad, bad["version"], bad["revision"], "tenant")
        text_body = copy.deepcopy(body)
        text_body["representations"][0] = {"role": "primary", "mediaType": "application/agent-skills+md", "content": "inert control"}
        text_created = copy.deepcopy(created)
        text_representation = text_created["revision"]["representations"][0]
        text_representation.update(mediaType="application/agent-skills+md", size=len("inert control"), contentDigest="sha256:" + hashlib.sha256(b"inert control").hexdigest(), artifactId=hashlib.sha256(b"inert control").hexdigest())
        version_created(text_body, "resource", text_created, text_created["version"], text_created["revision"], "tenant")
        text_representation["artifactId"] = "0" * 64
        with self.assertRaisesRegex(AssertionError, "wrong retained document reference"):
            version_created(text_body, "resource", text_created, text_created["version"], text_created["revision"], "tenant")
        foreign = copy.deepcopy(created)
        foreign["version"]["tenantId"] = foreign["revision"]["tenantId"] = "foreign"
        with self.assertRaisesRegex(AssertionError, "wrong Version/revision tenant"):
            version_created(body, "resource", foreign, foreign["version"], foreign["revision"], "tenant")

    def test_authoring_reproducers_and_native_diagnostics_are_retained(self):
        with tempfile.TemporaryDirectory() as source, tempfile.TemporaryDirectory() as target:
            for name in ("generated-authoring.xml", "generated-authoring-calibration.xml"):
                Path(source, name).write_text('<testsuites><testsuite><testcase name="control"><failure>local-admin</failure></testcase></testsuite></testsuites>')
            path = Path(source, "generated-authoring-reproducer.json")
            valid = {"operation": "createResourceVersion", "mode": "negative", "body": {"version": None}}
            runner.write_document(path, valid)
            generated.retain(Path(source), Path(target))
            self.assertEqual(runner.read_document(Path(target, path.name)), valid)
            self.assertNotIn("local-admin", Path(target, "generated-authoring.xml").read_text())
            for key, value in (("operation", "deleteResource"), ("mode", "other")):
                runner.write_document(path, {**valid, key: value})
                with self.assertRaises(runner.Failure):
                    generated.retain(Path(source), Path(target))

    def test_evidence_redacts_every_owned_credential(self):
        with tempfile.TemporaryDirectory() as source, tempfile.TemporaryDirectory() as target:
            Path(source, "generated.xml").write_text('<testsuites><testsuite><testcase name="control"><failure message="local-admin">local-reader other-tenant-admin generated-no-reader</failure></testcase></testsuite></testsuites>')
            generated.retain(Path(source), Path(target))
            raw = Path(target, "generated.xml").read_text()
            for token in generated.TOKENS:
                self.assertNotIn(token, raw)
            self.assertIn("REDACTED", raw)

    def test_owned_seed_and_no_reader_config_are_private_projections(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            seed = [{"kind": kind, "resourceId": f"00000000-0000-7000-8000-{index:012d}", "identifier": "urn:air:example.com:owned:" + kind, "secret": "omit"}
                    for index, kind in enumerate(("agent", "mcp_server", "skill"), 1)]
            generated.retain_seed(path, seed)
            self.assertNotIn("secret", (path / "generated-seed.json").read_text())
            for items in ([], seed[:2], [seed[0]] * 3, [{**item, "resourceId": "not-a-uuid"} for item in seed]):
                with self.assertRaises(runner.Failure):
                    generated.retain_seed(path, items)
            environment = {"CATALOG_FUNCTIONAL_PROFILE": "generated-api", "CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY": directory}
            generated.prepare(environment)
            actor = runner.read_document(path / "generated-config.json")["auth"]["developmentTokens"]["generated-no-reader"]
            self.assertEqual(actor["roles"], ["service_client"])
            self.assertEqual(actor["tenantId"], "00000000-0000-7000-8000-000000000001")
            self.assertEqual((path / "generated-config.json").stat().st_mode & 0o777, 0o444)
            with self.assertRaises(runner.Failure):
                generated.prepare({**environment, "CATALOG_FUNCTIONAL_PROFILE": "default-off"})

    def test_read_reproducers_require_selected_operation_mode_and_query(self):
        with tempfile.TemporaryDirectory() as source, tempfile.TemporaryDirectory() as target:
            path = Path(source, "generated-reproducer.json")
            for name in ("listResources", "listResourceVersions"):
                valid = {"operation": name, "mode": "negative", "body": None, "query": {"limit": 0}, "status": 400}
                runner.write_document(path, valid)
                generated.retain(Path(source), Path(target))
                self.assertEqual(runner.read_document(Path(target, path.name)), valid)
                for key, value in (("operation", "getResource"), ("mode", "other"), ("query", []), ("status", True)):
                    runner.write_document(path, {**valid, key: value})
                    with self.assertRaises(runner.Failure):
                        generated.retain(Path(source), Path(target))

    def test_list_oracles_detect_wrong_filter_parent_order_and_continuation(self):
        from resource_lists import check_page
        older = {"id": "b", "kind": "skill", "updatedAt": "2026-01-01T00:00:00Z"}
        newer = {"id": "a", "kind": "agent", "updatedAt": "2026-01-02T00:00:00.123456789Z"}
        inventory = [older, newer]
        check_page("listResources", {"items": [newer, older]}, inventory)
        check_page("listResources", {"items": [{**newer, "updatedAt": "2026-01-02T00:00:00.123456Z"}]}, inventory, kind="agent")
        check_page("listResources", {"items": []}, inventory, kind="plugin")
        check_page("listResources", {"items": [newer], "nextCursor": "opaque"}, inventory, limit=1)
        check_page("listResources", {"items": [older]}, inventory, limit=1, offset=1)
        for page, kwargs in (({"items": []}, {"kind": "agent"}), ({"items": [older]}, {"kind": "agent"}),
                             ({"items": [older, newer]}, {}), ({"items": [newer]}, {"limit": 1}),
                             ({"items": [newer]}, {"limit": 1, "offset": 1})):
            with self.subTest(page=page), self.assertRaises(AssertionError):
                check_page("listResources", page, inventory, **kwargs)
        tied = [{**newer, "id": identifier} for identifier in ("a", "b")]
        check_page("listResources", {"items": tied[::-1]}, tied)
        versions = [{"id": "a", "resourceId": "owned", "version": "1.0.0"}, {"id": "b", "resourceId": "owned", "version": "0.0.0"}]
        check_page("listResourceVersions", {"items": versions}, versions, parent="owned")
        for page in ({"items": versions[::-1]}, {"items": [{**versions[0], "resourceId": "foreign"}]},
                     {"items": []}, {"items": versions, "nextCursor": "opaque"}):
            with self.assertRaises(AssertionError):
                check_page("listResourceVersions", page, versions, parent="owned")

    def test_resource_exact_filter_oracle_keeps_normalization_intersection_and_empty_results(self):
        from resource_lists import canonical_filter, check_page
        agent = {"id": "a", "kind": "agent", "canonicalIdentifier": "urn:air:example.com:Team:Agent", "updatedAt": "2026-01-01T00:00:00Z"}
        skill = {**agent, "id": "b", "kind": "skill", "canonicalIdentifier": "opaque:Case"}
        inventory = [agent, skill]
        normalized = " URN:AIR:EXAMPLE.COM.:Team:Agent "
        self.assertEqual(canonical_filter(normalized), agent["canonicalIdentifier"])
        self.assertEqual(canonical_filter("\x1copaque:Case"), "\x1copaque:Case")
        self.assertEqual(canonical_filter(" \u2028opaque:Case\u3000"), "opaque:Case")
        check_page("listResources", {"items": [agent]}, inventory, canonical_identifier=normalized, kind="agent", limit=1)
        check_page("listResources", {"items": []}, inventory, canonical_identifier=normalized, kind="skill")
        check_page("listResources", {"items": [skill]}, inventory, canonical_identifier="opaque:Case")
        check_page("listResources", {"items": []}, inventory, canonical_identifier="opaque:case")
        check_page("listResources", {"items": []}, inventory, canonical_identifier="0")
        for page, identifier in (({"items": inventory}, normalized), ({"items": [skill]}, normalized),
                                 ({"items": [agent], "nextCursor": "opaque"}, normalized), ({"items": [agent]}, "0")):
            with self.subTest(page=page), self.assertRaises(AssertionError):
                check_page("listResources", page, inventory, canonical_identifier=identifier)

    def test_sequence_oracles_keep_replay_exact_and_rejections_read_only(self):
        from namespace_sequence import check_reply, check_state, stored
        before = {"id": "owned", "displayName": "Before", "revision": 1, "updatedAt": "2026-01-01T00:00:00.123456789Z"}
        after = {**before, "displayName": "After", "revision": 2, "updatedAt": "2026-01-01T00:00:01.123456Z"}
        check_state("change", before, after, "After")
        self.assertEqual(stored(before), stored({**before, "updatedAt": "2026-01-01T00:00:00.123456Z"}))
        for kind in generated.KINDS:
            if kind == "change":
                continue
            check_state(kind, before, before)
            with self.assertRaisesRegex(AssertionError, "changed state"):
                check_state(kind, before, after)
        check_reply("replay", 200, after, '\"rev-2\"', (after, '\"rev-2\"'))
        with self.assertRaisesRegex(AssertionError, "replay changed"):
            check_reply("replay", 200, before, '\"rev-1\"', (after, '\"rev-2\"'))
        with self.assertRaisesRegex(AssertionError, "revision/ETag"):
            check_reply("change", 200, after, '\"rev-1\"')

    def test_sequence_evidence_and_reproducer_reject_missing_or_excess_steps(self):
        for counts in ({}, dict.fromkeys(generated.KINDS, 19), dict.fromkeys(generated.KINDS, 61), dict.fromkeys(generated.KINDS, 30)):
            data = evidence()
            data["sequences"]["steps"] = counts
            self.assertFalse(generated.valid_evidence(data, AUTHORITY))
        with tempfile.TemporaryDirectory() as source, tempfile.TemporaryDirectory() as target:
            path = Path(source, "generated-sequence-reproducer.json")
            runner.write_document(path, {"actions": list(generated.KINDS), "label": "owned"})
            generated.retain(Path(source), Path(target))
            self.assertTrue(Path(target, path.name).is_file())
            runner.write_document(path, {"actions": ["delete"], "label": "owned"})
            with self.assertRaises(runner.Failure):
                generated.retain(Path(source), Path(target))
