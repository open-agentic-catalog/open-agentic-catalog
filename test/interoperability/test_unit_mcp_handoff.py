"""Isolated selector/report assertions; no processes, listeners or external tools."""

import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

from common import Result
import mcp_catalog as catalog
import catalog_control
import mcp_handoff as h
import mcp_runner as runner


def record_fixture():
    card, registry = h.documents()
    reps = h.representations(card, registry)
    for r, profile in zip(reps, (h.CARD_PROFILE, h.REGISTRY_PROFILE), strict=True):
        r.update(profileId=profile, validationState="valid", contentDigest=h.digest(r["data"]), size=len(h.canonical(r["data"])))
    record = dict(resource=dict(id="resource", kind="mcp_server", canonicalIdentifier=h.IDENTIFIER),
                  version=dict(id="version", version="2.0.0", resourceId="resource", sealedRevisionId="revision"),
                  versionRevision=dict(id="revision", resourceVersionId="version", contentSetDigest="bound-content", representations=reps, evidenceSetId="evidence"),
                  listing=dict(id="listing", currentReleaseId="release"),
                  listingRevision=dict(id="listing-revision", versionRevisionId="revision", listingId="listing",
                                       displayName="Owned MCP", description="Qualification", tags=[h.TAG], publisherId="publisher"),
                  release=dict(id="release", listingRevisionId="listing-revision", versionRevisionId="revision", listingId="listing", state="published"),
                  evidence=dict(id="evidence", subjectDigest="bound-content", state="complete", findings=[{"code": "MCP_SERVER_CARD_EXPERIMENTAL"}]))
    expected = {key: record[key]["id"] for key in ("resource", "version", "versionRevision", "listingRevision", "release")}
    expected.update(listing="listing", contentSetDigest="bound-content")
    return record, expected


class Selection(unittest.TestCase):
    def test_exact_mapping_ignores_distribution_and_inert_hints(self):
        record, _ = record_fixture()
        record["versionRevision"]["representations"].reverse()
        primary = next(r for r in record["versionRevision"]["representations"] if r["role"] == "primary")
        primary["data"]["_meta"]["io.example/install-hint"] = {"command": "never-execute"}
        config = h.map_client(record, "a" * 64)
        self.assertEqual(config, {"mcpServers": {"handoff": {"type": "http", "url": "https://127.0.0.1:9443/mcp",
            "headers": {"X-MCP-Test-Key": "a" * 64}, "protocolEra": "legacy", "connectionTimeout": 10000, "requestTimeout": 10000, "roots": []}}})
        self.assertNotIn("a" * 64, str(record))
        self.assertNotIn("never-execute", str(config))

    def test_unsupported_selections_fail_before_config_creation(self):
        changes = {
            "duplicate primary": lambda reps: reps.append(copy.deepcopy(reps[0])),
            "no primary": lambda reps: reps[0].update(role="documentation"),
            "wrong media": lambda reps: reps[0].update(mediaType=h.REGISTRY),
            "wrong profile": lambda reps: reps[0].update(profileId=h.REGISTRY_PROFILE),
            "invalid representation": lambda reps: reps[0].update(validationState="invalid"),
            "no remotes": lambda reps: reps[0]["data"].pop("remotes"),
            "ambiguous remotes": lambda reps: reps[0]["data"]["remotes"].append(copy.deepcopy(reps[0]["data"]["remotes"][0])),
            "sse": lambda reps: reps[0]["data"]["remotes"][0].update(type="sse"),
            "local transport": lambda reps: reps[0]["data"]["remotes"][0].update(type="stdio"),
            "HTTP": lambda reps: reps[0]["data"]["remotes"][0].update(url="http://127.0.0.1:9443/mcp"),
            "other origin": lambda reps: reps[0]["data"]["remotes"][0].update(url="https://example.invalid/mcp"),
            "other path": lambda reps: reps[0]["data"]["remotes"][0].update(url="https://127.0.0.1:9443/draft-mcp"),
            "query": lambda reps: reps[0]["data"]["remotes"][0].update(url=h.ENDPOINT + "?key=secret"),
            "template": lambda reps: reps[0]["data"]["remotes"][0].update(url="https://127.0.0.1:9443/{path}"),
            "unused variables": lambda reps: reps[0]["data"]["remotes"][0].update(variables={"path": {"isRequired": True}}),
            "missing wire": lambda reps: reps[0]["data"]["remotes"][0].pop("supportedProtocolVersions"),
            "unsupported wire": lambda reps: reps[0]["data"]["remotes"][0].update(supportedProtocolVersions=["2026-07-28"]),
            "runtime version drift": lambda reps: reps[0]["data"].update(version="2026.8.31"),
            "no header": lambda reps: reps[0]["data"]["remotes"][0].pop("headers"),
            "extra header": lambda reps: reps[0]["data"]["remotes"][0]["headers"].append({"name": "Authorization"}),
        }
        for field, value in (("name", "Authorization"), ("isSecret", False), ("isRequired", False),
                             ("value", "preset"), ("default", "preset"), ("variables", {}), ("choices", []), ("format", "filepath")):
            changes["header " + field] = lambda reps, f=field, v=value: reps[0]["data"]["remotes"][0]["headers"][0].update({f: v})
        for name, change in changes.items():
            with self.subTest(name=name):
                record, _ = record_fixture()
                change(record["versionRevision"]["representations"])
                with patch.object(h, "client_config") as config, self.assertRaises(AssertionError):
                    h.map_client(record, "a" * 64)
                config.assert_not_called()

    def test_lookup_uses_search_release_and_checks_immutable_references(self):
        record, expected = record_fixture()
        item = dict(id="search-result", type="local", kind="mcp_server", versionPresent=True, version="2.0.0", local={section: {"id": record[section]["id"]} for section in
                         ("resource", "version", "versionRevision", "listing", "listingRevision", "release", "evidence")},
                    **{key: record["listingRevision"][key] for key in ("displayName", "description", "tags")}, publisher="publisher")
        search = {"data": {"partial": False, "items": [item]}}
        item["local"]["versionRevision"].update(contentSetDigest=expected["contentSetDigest"], evidenceSetId=record["evidence"]["id"])
        read = Mock(return_value={"data": record})
        self.assertEqual(h.select_record(search, read, expected), record)
        read.assert_called_once_with("release")
        for section in item["local"]:
            changed = copy.deepcopy(item)
            changed["local"][section]["id"] = "wrong-summary"
            with self.subTest(summary=section), self.assertRaises(AssertionError):
                h.select_record({"data": {"partial": False, "items": [changed]}}, read, expected)
        changed = copy.deepcopy(item)
        changed["description"] = "different display"
        with self.assertRaises(AssertionError):
            h.select_record({"data": {"partial": False, "items": [changed]}}, read, expected)
        for field, value in (("partial", True), ("items", []), ("items", [item, item]), ("nextCursor", "more")):
            with self.subTest(field=field), self.assertRaises(AssertionError):
                h.select_record({"data": dict(search["data"], **{field: value})}, read, expected)
        for field in ("resource", "version", "versionRevision", "listingRevision", "release"):
            changed = copy.deepcopy(record)
            changed[field]["id"] = "unselected"
            with self.subTest(field=field), self.assertRaises(AssertionError):
                h.select_record(search, lambda _: {"data": changed}, expected)
        changed = copy.deepcopy(record)
        changed["versionRevision"]["representations"][0]["data"]["remotes"][0]["url"] += "/draft"
        with self.assertRaises(AssertionError):
            h.validate_record(changed, expected)
        for field in ("size", "contentDigest", "profileId"):
            changed = copy.deepcopy(record)
            changed["versionRevision"]["representations"][1][field] = "wrong"
            with self.subTest(field=field), self.assertRaises(AssertionError):
                h.validate_record(changed, expected)

    def test_projection_cannot_relabel_registry_or_include_draft(self):
        record, _ = record_fixture()
        record["listingRevision"].update(displayName="Owned MCP", description="Qualification", tags=[h.TAG])
        entry = dict(identifier=h.IDENTIFIER, metadata={"catalogReleaseId": "release"}, type=h.CARD,
                     version="2.0.0", data=h.documents()[0], displayName="Owned MCP", description="Qualification", tags=[h.TAG])
        own = dict(identifier="urn:air:catalog.example.com:registry:self", type="application/ai-registry+json", metadata={"catalogReleaseId": "registry:self"})
        h.projection({"entries": [own, entry]}, record)
        for update in ({"type": h.REGISTRY}, {"metadata": {"catalogReleaseId": "draft"}}, {"data": h.documents()[1]}, {"url": h.ENDPOINT}):
            with self.subTest(update=update), self.assertRaises(AssertionError):
                h.projection({"entries": [own, dict(entry, **update)]}, record)

    def test_governed_case_inventory_is_exact(self):
        good = dict.fromkeys(h.CASES, True)
        h.check_cases(good)
        for broken in ({}, dict(good, unexpected=True), dict(good, governance=False), dict(good, governance=1)):
            with self.assertRaises(AssertionError):
                h.check_cases(broken)

    def test_complete_report_requires_both_phases_and_no_metadata_invocation(self):
        direct = dict(requests=22, emptyRoots=0, denied=1, forwarded=21, authorization=0, otherTools=0, http={"GET": 4, "POST": 17},
                      methods={"tools/call": 2, "tools/list": 3},
                      initialize=[{"protocolVersion": "2025-11-25", "capabilities": {"roots": {}}} for _ in range(4)])
        after = copy.deepcopy(direct)
        after.update(requests=37, forwarded=36, http={"GET": 7, "POST": 29}, methods={"tools/call": 3, "tools/list": 5},
                     initialize=direct["initialize"] + copy.deepcopy(direct["initialize"][:3]))
        cases = {name: {"exit": 0, "outcome": result} for name, result in (
            ("initialize", "connected"), ("tools-list", "listed"), ("echo", "echoed"),
            ("handoff-initialize", "connected"), ("handoff-tools-list", "listed"), ("handoff-echo", "echoed"))}
        cases.update({"missing-key": {"exit": 3, "outcome": "authentication-required"}, "tool-error": {"exit": 5, "outcome": "tool-error"}})
        final = copy.deepcopy(after)
        for case in ("wrong-key", "catalog-key", "bearer-forwarding"):
            final["requests"] += 1
            final["denied"] += 1
            if case == "bearer-forwarding":
                final["authorization"] += 1
            cases[case] = dict(exit=3, outcome="authentication-required", observations=copy.deepcopy(final))
        cases["untrusted-ca"] = dict(exit=4, outcome="tls-rejected", observations=copy.deepcopy(final))
        cases["unavailable"] = dict(exit=4, outcome="unavailable", observations=None)
        denials = {case: dict(api_status=status, api_code=code, cli_exit=1, outcome="denied", handoff_created=False,
                             observations=copy.deepcopy(direct)) for case, status, code in
                   (("missing", 401, "UNAUTHENTICATED"), ("invalid", 401, "UNAUTHENTICATED"), ("wrong-tenant", 404, "NOT_FOUND"))}
        for case, denial in denials.items():
            denial["cli_error"] = ("catalogctl: remote: INTERNAL: remote: supply a nonempty bearer token of at most 16 KiB through CATALOG_REMOTE_TOKEN or a token file"
                                   if case == "missing" else json.dumps(dict(status=denial["api_status"], code=denial["api_code"], requestId="fixture")))
        report = dict(scope="mcp-governed-handoff", cases=cases, cleanup=True, governed_cases=dict.fromkeys(h.CASES, True),
                      direct_observations=direct, metadata_observations=copy.deepcopy(direct), handoff_observations=after, observations=final,
                      catalog_denials=denials, wrapper_stopped=True, post_failure_integrity=True,
                      binaries=dict.fromkeys(("fixture", "catalogd", "catalogctl"), "a"*64),
                      connection={"url": h.ENDPOINT, "type": "http"},
                      selection=dict(versionRevision="original", draftRevision="later", cardDigest=h.digest(h.documents()[0]),
                                     registryDigest=h.digest(h.documents()[1]), profiles={h.CARD: h.CARD_PROFILE, h.REGISTRY: h.REGISTRY_PROFILE}))
        h.complete(report)
        changes = (
            lambda r: r["cases"].pop("handoff-echo"),
            lambda r: r["cases"]["handoff-echo"].update(exit=5),
            lambda r: r["governed_cases"].pop("revision-binding"),
            lambda r: r["metadata_observations"].update(forwarded=22),
            lambda r: r["handoff_observations"].update(otherTools=1),
            lambda r: r["handoff_observations"].update(authorization=1),
            lambda r: r["handoff_observations"]["methods"].update({"tools/call": 2}),
            lambda r: r["selection"].update(draftRevision="original"),
            lambda r: r["selection"]["profiles"].update({h.CARD: h.REGISTRY_PROFILE}),
            lambda r: r.update(cleanup=False),
            lambda r: r["catalog_denials"].pop("missing"),
            lambda r: r["catalog_denials"]["wrong-tenant"].update(api_status=403),
            lambda r: r["catalog_denials"]["invalid"].update(handoff_created=True),
            lambda r: r["catalog_denials"]["missing"].update(cli_error="unrelated failure"),
            lambda r: r["catalog_denials"]["invalid"]["observations"].update(requests=23),
            lambda r: r["cases"].pop("untrusted-ca"),
            lambda r: r["cases"]["catalog-key"].update(exit=0),
            lambda r: r["cases"]["wrong-key"]["observations"].update(forwarded=99),
            lambda r: r["cases"]["untrusted-ca"]["observations"].update(requests=99),
            lambda r: r["cases"]["unavailable"].update(observations={}),
            lambda r: r.update(wrapper_stopped=False),
            lambda r: r.update(post_failure_integrity=False),
        )
        for change in changes:
            broken = copy.deepcopy(report)
            change(broken)
            with self.assertRaises(AssertionError):
                h.complete(broken)

    def test_native_failures_must_identify_the_intended_boundary(self):
        examples = (("wrong-key", 3, "auth_required", ""), ("catalog-key", 3, "auth_required", ""),
                    ("bearer-forwarding", 3, "auth_required", ""),
                    ("untrusted-ca", 4, "unreachable", "unable to verify the first certificate"),
                    ("unavailable", 4, "unreachable", "connect ECONNREFUSED 127.0.0.1:9443"))
        for case, status, code, cause in examples:
            error = json.dumps({"error": {"code": code, "cause": cause}})
            with self.subTest(case=case):
                self.assertEqual(h.failure_result(case, Result(status, "", error))["exit"], status)
                for failed in (Result(0, "", error), Result(125, "", error), Result(status, '{"result":{}}', error),
                               Result(status, "", ""),
                               Result(status, "", '{"error":{"code":"unexpected"}}')):
                    with self.assertRaises(AssertionError):
                        h.failure_result(case, failed)
        for case in ("untrusted-ca", "unavailable"):
            with self.assertRaises(AssertionError):
                h.failure_result(case, Result(4, "", '{"error":{"code":"unreachable","cause":"unrelated DNS failure"}}'))

    def test_catalog_denials_distinguish_local_preflight_and_http_errors(self):
        missing = "catalogctl: remote: INTERNAL: remote: supply a nonempty bearer token of at most 16 KiB through CATALOG_REMOTE_TOKEN or a token file"
        cases = {"missing": missing, "invalid": '{"status":401,"code":"UNAUTHENTICATED","requestId":"one"}',
                 "wrong-tenant": '{"status":404,"code":"NOT_FOUND","requestId":"two"}'}
        for case, error in cases.items():
            h.catalog_denial(case, Result(1, "", error))
            for result in (Result(0, "", error), Result(1, '{"data":{}}', error), Result(1, "", '{"status":403,"code":"FORBIDDEN"}')):
                with self.subTest(case=case), self.assertRaises(AssertionError):
                    h.catalog_denial(case, result)


class CatalogBoundary(unittest.TestCase):
    def test_denial_workflow_preserves_http_and_cli_authorities(self):
        with tempfile.TemporaryDirectory() as directory:
            api = catalog.Catalog(Path(directory), "owned-catalog")
            for case, actor, status, code in (("missing", None, 401, "UNAUTHENTICATED"),
                                             ("invalid", "invalid", 401, "UNAUTHENTICATED"),
                                             ("wrong-tenant", "other-tenant-reader", 404, "NOT_FOUND")):
                problem = dict(status=status, code=code, detail="not disclosed", instance="/api/v1/catalog/resources/release")
                # Native CLI diagnostics omit the requested URL/instance.
                error = ("catalogctl: remote: INTERNAL: remote: supply a nonempty bearer token of at most 16 KiB through CATALOG_REMOTE_TOKEN or a token file"
                         if case == "missing" else json.dumps(dict(status=status, code=code, detail="not disclosed", requestId="one")))
                with self.subTest(case=case), patch.object(api, "request", return_value=(problem, "")) as request, \
                     patch.object(api, "cli_result", return_value=Result(1, "", error)) as cli, \
                     patch.object(api, "cli", return_value={"data": {"operations": ["catalog.search"]}}) as capabilities:
                    result = api.denial(case, "release")
                    self.assertFalse(result["handoff_created"])
                    request.assert_called_once_with("/api/v1/catalog/resources/release", actor=actor, status=status)
                    cli.assert_called_once_with("record", "--id", "release", actor=actor)
                    if case == "wrong-tenant":
                        capabilities.assert_called_once_with("capabilities", actor="other-tenant-reader")
                    else:
                        capabilities.assert_not_called()

    def test_synthetic_wrong_tenant_is_reader_and_invalid_token_is_not_registered(self):
        with tempfile.TemporaryDirectory() as directory:
            api = catalog.Catalog(Path(directory), "owned-catalog")
            api.configuration()
            actors = json.loads((Path(directory) / "config.json").read_text())["auth"]["developmentTokens"]
            reader, other = (actors[api.tokens[name]] for name in ("local-reader", "other-tenant-reader"))
            self.assertNotIn("roles", other)
            self.assertEqual(other["roleTemplates"], ["reader", "discovery_reader", "feedback_member"])
            self.assertEqual(other["roleTemplates"], reader["roleTemplates"])
            self.assertNotEqual(other["tenantId"], reader["tenantId"])
            self.assertNotIn(api.tokens["invalid"], actors)
            self.assertEqual(len(set(api.tokens.values())), len(api.tokens))

    def test_container_mounts_cannot_broaden_client_access(self):
        with tempfile.TemporaryDirectory() as directory:
            private = Path(directory)
            info = dict(HostConfig=dict(NetworkMode="container:owned", ReadonlyRootfs=True, Privileged=False,
                        CapDrop=["ALL"], SecurityOpt=["no-new-privileges"], PortBindings={}, Memory=512*1024*1024,
                        PidsLimit=128, LogConfig={"Config": {"max-size": "1m"}}),
                        NetworkSettings={"Ports": {}}, Config={"User": "1001:1001"},
                        Mounts=[dict(Type="bind", Source=str(private / "clients"), Destination="/client", RW=False)])
            with patch.object(runner.os, "getuid", return_value=1001), patch.object(runner.os, "getgid", return_value=1001):
                with patch.object(runner, "docker", return_value=Result(0, json.dumps([info]), "")):
                    self.assertEqual(runner.isolation("client", "container:owned", private, (("clients", "/client"),))["bind_destinations"], ["/client"])
                for source, writable in ((private, False), (private / "catalog", False), (private / "clients", True)):
                    info["Mounts"][0].update(Source=str(source), RW=writable)
                    with patch.object(runner, "docker", return_value=Result(0, json.dumps([info]), "")), self.assertRaises(AssertionError):
                        runner.isolation("client", "container:owned", private, (("clients", "/client"),))

    def test_commands_keep_credentials_in_private_files_and_preserve_preconditions(self):
        with tempfile.TemporaryDirectory() as directory:
            api = catalog.Catalog(Path(directory), "owned-catalog")
            api.tokens = {"local-admin": "synthetic-private-token"}
            result = Result(0, json.dumps({"status": 201, "etag": '"rev-2"', "body": {"id": "resource"}}), "")
            with patch.object(catalog_control, "docker", return_value=result) as docker:
                body, etag = api.request("/api/v1/resources", {"kind": "mcp_server"}, etag='"rev-1"', status=201)
            self.assertEqual(body, {"id": "resource"})
            self.assertEqual(etag, '"rev-2"')
            request_path = Path(directory) / "request.json"
            request = json.loads(request_path.read_text())
            self.assertEqual(request_path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(request["token"], "synthetic-private-token")
            self.assertEqual(request["etag"], '"rev-1"')
            self.assertTrue(request["key"].startswith("mcp-"))
            self.assertEqual(request["body"], {"kind": "mcp_server"})
            self.assertEqual(docker.call_args.args, ("exec", "owned-catalog", "/commands/fixture", "catalog-request"))
            self.assertNotIn("synthetic-private-token", str(docker.call_args))

    def test_native_failures_and_credential_leak_cannot_be_read_as_success(self):
        results = [Result(1, "{}", "failed"), Result(0, "not JSON", ""),
                   Result(0, '{"status":401,"body":{},"etag":""}', ""),
                   Result(0, '{"status":200,"body":{"unexpected":"private-token"},"etag":""}', "")]
        with tempfile.TemporaryDirectory() as directory:
            api = catalog.Catalog(Path(directory), "owned-catalog")
            api.tokens = {"local-admin": "private-token", "local-reader": "private-reader"}
            for result in results:
                with self.subTest(result=result.code), patch.object(catalog_control, "docker", return_value=result), self.assertRaises((AssertionError, ValueError)):
                    api.request("/api/v1/capabilities")
            with patch.object(catalog_control, "docker", return_value=Result(1, '{"data":{"id":"looks-successful"}}', "denied")), self.assertRaises(AssertionError):
                api.cli("record", "--id", "release")
