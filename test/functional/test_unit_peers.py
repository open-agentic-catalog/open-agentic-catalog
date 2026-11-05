"""Focused peer evidence, pagination and invocation-input checks."""

import copy
import base64
from contextlib import redirect_stderr
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock, patch

import peers
import federation
import runner
import peer_pagination


class PeerTests(unittest.TestCase):
    def test_failed_page_clock_comparison_is_bounded_and_never_authenticates_a_token(self):
        previous = {"Date": "Thu, 22 Oct 2026 12:00:02 GMT"}
        failed = 1792670400
        payload = base64.urlsafe_b64encode(json.dumps({"e": failed + 902, "private": "private-secret"}).encode()).rstrip(b"=").decode()
        expected = {"responseDeltaSeconds": -2, "cursorExpiryMinusResponseDateSeconds": 902}
        for prefix in ("lc1.", "h1.", ""):
            token = prefix + payload + ".not-a-real-signature"
            with self.subTest(prefix=prefix):
                self.assertEqual(peer_pagination.cursor_clock(token, previous, failed), expected)
                error = AssertionError("functional federation request returned HTTP 400 (LIVE_CURSOR_INVALID)")
                error.response_date = failed
                client = Mock()
                client.request.side_effect = error
                with self.assertRaises(AssertionError) as caught:
                    peer_pagination.request_page(client, "/api/v1/catalog/search", {"cursor": token}, 2, previous_headers=previous)
                self.assertIs(caught.exception, error)
                self.assertEqual(error.search_page_failure["clock"], expected)
                self.assertNotIn("private", json.dumps(error.search_page_failure))
                self.assertNotIn(token, json.dumps(error.search_page_failure))
                client.request.assert_called_once()
        for token in ("private-token", "lc1.%%.signature", "lc1." + "a" * 4096 + ".signature",
                      "lc1." + base64.urlsafe_b64encode(b'{"e":1,"e":2}').decode().rstrip("=") + ".signature",
                      "lc1." + base64.urlsafe_b64encode(b'{"e":true}').decode().rstrip("=") + ".signature",
                      "lc1." + base64.urlsafe_b64encode(b'[]').decode().rstrip("=") + ".signature"):
            with self.subTest(token=token[:20]):
                self.assertIsNone(peer_pagination.cursor_clock(token, previous, failed))
        self.assertIsNone(peer_pagination.cursor_clock("lc1." + payload + ".signature", {}, failed))
        self.assertIsNone(peer_pagination.cursor_clock("lc1." + payload + ".signature", previous, None))
        self.assertIsNone(peer_pagination.cursor_clock("lc1." + payload + ".signature", previous, failed - 86401))

    def test_failed_page_preserves_position_and_original_error_without_retry_or_private_data(self):
        for public in (False, True):
            path = "/ard/v1/search" if public else "/api/v1/catalog/search"
            token_field = "pageToken" if public else "cursor"
            for page_number in (1, 2):
                for http in (False, True):
                    error = (AssertionError("functional federation request returned HTTP 400 (LIVE_WINDOW_CHANGED)")
                             if http else federation.ClientFailure("private-transport-error"))
                    client = Mock()
                    client.request.side_effect = error
                    body = {"privateQuery": "private-query", **({token_field: "private-token"} if page_number > 1 else {})}
                    with self.subTest(public=public, page=page_number, http=http), self.assertRaises(type(error)) as caught:
                        peer_pagination.request_page(client, path, body, page_number)
                    self.assertIs(caught.exception, error)
                    client.request.assert_called_once_with("POST", path, body)
                    expected = {"surface": "public" if public else "control", "page": page_number,
                                "completedPages": page_number - 1, "continuation": page_number > 1,
                                "httpFailure": {"status": 400, "category": "LIVE_WINDOW_CHANGED"} if http else None}
                    self.assertEqual(error.search_page_failure, expected)
                    self.assertNotIn("private", json.dumps(expected))
                    with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ, {"CATALOG_FUNCTIONAL_STATE_DIRECTORY": directory}), \
                            redirect_stderr(io.StringIO()):
                        peers.report_failure(error)
                        self.assertEqual(runner.read_document(Path(directory) / "search-page-failure.json"), expected)

    def test_successful_page_and_invalid_diagnostics_do_not_change_request_behavior(self):
        client = Mock()
        response = ({"items": []}, {})
        client.request.return_value = response
        self.assertIs(peer_pagination.request_page(client, "/api/v1/catalog/search", {"limit": 1}, 1), response)
        for page, body in ((0, {}), (True, {}), (65, {"cursor": "private"}), (2, {}), (1, None)):
            error = AssertionError("private-original")
            client.request.reset_mock()
            client.request.side_effect = error
            with self.subTest(page=page):
                with self.assertRaises(AssertionError) as caught:
                    peer_pagination.request_page(client, "/api/v1/catalog/search", body, page)
                self.assertIs(caught.exception, error)
                self.assertFalse(hasattr(error, "search_page_failure"))
                client.request.assert_called_once()

    def test_report_failure_preserves_all_reviewed_http_categories_and_rejects_arbitrary_text(self):
        for category in runner.HTTP_FAILURE_CATEGORIES:
            error = AssertionError(f"functional federation request returned HTTP 400 ({category})")
            output = io.StringIO()
            with self.subTest(category=category), patch.object(peers, "record_scenario_failure"), redirect_stderr(output):
                peers.report_failure(error)
            _, diagnostic = runner.http_failure_chunk(b"\n", output.getvalue().encode())
            self.assertEqual(diagnostic, {"status": 400, "category": category})
        for message in ("private-secret", "functional federation request returned HTTP 400 (LIVE_WINDOW_CHANGED) private-secret",
                        "private-secret\nfunctional federation request returned HTTP 400 (LIVE_WINDOW_CHANGED)"):
            output = io.StringIO()
            with patch.object(peers, "record_scenario_failure"), redirect_stderr(output):
                peers.report_failure(AssertionError(message))
            self.assertNotIn("private-secret", output.getvalue())
            self.assertNotIn("HTTP", output.getvalue())

    def test_walk_retains_a_successful_first_page_before_the_failed_continuation(self):
        scenario, client = Mock(spec=["stats", "assert_contacts"]), Mock()
        prefix = "peer-a.test:8443 POST /ard/v1/search none 200"
        scenario.stats.side_effect = [{}, {prefix: 1}, {prefix: 1}]
        error = AssertionError("functional federation request returned HTTP 400 (CATALOG_CURSOR_STALE)")
        client.request.side_effect = [({"items": [{"identifier": "a"}], "nextCursor": "private-token", "tier": "core"}, {}), error]
        with self.assertRaises(AssertionError) as caught:
            peer_pagination.walk(scenario, client, "/api/v1/catalog/search", {"limit": 1}, {"a", "b"}, tier="core")
        self.assertIs(caught.exception, error)
        self.assertEqual(error.search_page_failure, {"surface": "control", "page": 2, "completedPages": 1,
                                                   "continuation": True, "httpFailure": {"status": 400, "category": "CATALOG_CURSOR_STALE"}})
        self.assertEqual(client.request.call_count, 2)

    def test_stored_traversals_retain_the_failed_page_after_a_valid_control(self):
        for method in ("check_combined", "check_control_declarations"):
            scenario = object.__new__(peers.Peers)
            scenario.b_native = {"local"}
            scenario.b = Mock()
            error = AssertionError("functional federation request returned HTTP 400 (CATALOG_CURSOR_INVALID)")
            first = {"items": [{"identifier": "local", "type": "local"}], "durableTotal": 2,
                     "partial": False, "liveCount": 0, "nextCursor": "private-token"}
            scenario.b.request.side_effect = [(first, {}), error]
            with self.subTest(method=method), self.assertRaises(AssertionError) as caught:
                getattr(scenario, method)([{"identifier": "indexed"}])
            self.assertIs(caught.exception, error)
            self.assertEqual(error.search_page_failure["page"], 2)
            self.assertEqual(error.search_page_failure["completedPages"], 1)
            self.assertTrue(error.search_page_failure["continuation"])
            self.assertEqual(scenario.b.request.call_count, 2)

    @staticmethod
    def complete_report():
        images = {name: {"configurationDigest": "sha256:" + "a" * 64}
                  for name in ("api", "worker", "api-a", "worker-a", "peer-archives", "postgres", "postgres-a", "federation-fixture")}
        return {"profile": "peers", "images": images, "servedVersion": {"version": "functional"},
                "peerIdentityVerified": True, "peerArchiveInputs": {"demo-archives": "b" * 64, "archives/allowlist.json": "c" * 64},
                "peers": {"completed": list(peers.PHASES), "catalogs": 2, "publishedKinds": peers.KINDS},
                "peerInventories": {side: [{"identifier": "urn:air:catalog.example.com:peers-" + side + ":peer-" + side + "-" + name, "kind": kind}
                    for name, kind in zip(names, peers.KINDS, strict=True)]
                    for side, names in (("a", ("agent", "mcp", "skill", "plugin")), ("b", ("agent", "mcp_server", "skill", "plugin")))},
                "peerContacts": {"peer-a.test:8443 POST /ard/v1/search none 200": 1},
                "peerDrafts": {"kinds": peers.KINDS, "directions": ["ui-to-cli", "cli-to-ui"],
                    "newResources": 4, "newVersions": 4, "contentOnly": 2, "independentlyPublished": 1,
                    "resourceInvocations": 0, "cleanup": "passed"},
                "peerPagination": {"core": True, "hybrid": True, "liveOnly": True, "cli": True,
                    "browser": [{"mode": mode, "livePages": 2, "combinedPages": 2, "sameTokenRetry": True, "cleanup": "passed"} for mode in ("core", "hybrid")]},
                "operationCoverage": {"formatVersion": 1, "denominator": ["searchCatalog"],
                    "rows": [{"step": "federation-peers", "operationId": "searchCatalog", "status": 200, "contract": "passed", "outcome": "positive", "count": 1}],
                    "unobservedCollectors": ["smoke", "remote-cli", "shell-probes", "publication-report-sdk"]}}

    def test_final_report_rejects_incomplete_or_contradictory_peer_evidence(self):
        positive = self.complete_report()
        self.assertTrue(runner.finalize(copy.deepcopy(positive), list(runner.PEER_STEPS), "", True))
        for field in ("peerInventories", "peerContacts", "peerIdentityVerified", "peerArchiveInputs", "operationCoverage", "peerPagination", "peerDrafts"):
            report = copy.deepcopy(positive)
            del report[field]
            with self.subTest(missing=field):
                self.assertFalse(runner.finalize(report, list(runner.PEER_STEPS), "", True))
        for field, value in (("peerIdentityVerified", 1), ("peerInventories", {"a": [], "b": []}),
                             ("peerContacts", {"private-query": 1}), ("peerArchiveInputs", {"demo-archives": "bad"})):
            with self.subTest(field=field):
                self.assertFalse(runner.finalize({**copy.deepcopy(positive), field: value}, list(runner.PEER_STEPS), "", True))
        for service in ("worker-a", "peer-archives", "postgres-a"):
            report = copy.deepcopy(positive)
            report["images"][service]["configurationDigest"] = "sha256:" + "d" * 64
            with self.subTest(service=service):
                self.assertFalse(runner.finalize(report, list(runner.PEER_STEPS), "", True))
        report = copy.deepcopy(positive)
        report["peerInventories"]["b"][0]["identifier"] = "urn:wrong-but-same-count"
        self.assertFalse(runner.finalize(report, list(runner.PEER_STEPS), "", True))
        report = copy.deepcopy(positive)
        report["peerContractFailures"] = [{"operation": "searchCatalog", "phase": "live", "status": 200, "continuationUnavailable": True}]
        self.assertFalse(runner.finalize(report, list(runner.PEER_STEPS), "", True))
        self.assertEqual(report["failureCode"], "peer-contract-failure")
        report["peerContractFailures"] = []
        self.assertFalse(runner.finalize(report, list(runner.PEER_STEPS), "", True))
        self.assertEqual(report["failureCode"], "invalid-peer-contract-failures")

    def test_pagination_evidence_rejects_partial_or_repeated_modes(self):
        positive = self.complete_report()["peerPagination"]
        self.assertTrue(peer_pagination.valid_evidence(positive))
        for field in positive:
            value = copy.deepcopy(positive)
            del value[field]
            self.assertFalse(peer_pagination.valid_evidence(value))
        for field in ("core", "hybrid", "cli", "liveOnly"):
            self.assertFalse(peer_pagination.valid_evidence({**positive, field: 1}))
        for rows in ([], positive["browser"][:1], [positive["browser"][0]] * 2, list(reversed(positive["browser"]))):
            self.assertFalse(peer_pagination.valid_evidence({**positive, "browser": rows}))
        value = copy.deepcopy(positive)
        value["browser"][1]["sameTokenRetry"] = 1
        self.assertFalse(peer_pagination.valid_evidence(value))

    def test_client_walk_rejects_lost_repeated_and_oversized_results(self):
        for rows, token in ((["a", "b"], ""), (["a"], "lc1.same"), (["wrong"], ""), ([], "")):
            scenario, client = Mock(spec=["stats", "assert_contacts"]), Mock()
            prefix = "peer-a.test:8443 POST /ard/v1/search none 200"
            scenario.stats.side_effect = [{}, {prefix: 1}, {prefix: 1}, {prefix: 2}]
            client.request.return_value = ({"items": [{"identifier": x} for x in rows], "nextCursor": token, "tier": "core"}, {})
            with self.subTest(rows=rows, token=token), self.assertRaises(AssertionError):
                peer_pagination.walk(scenario, client, "/api/v1/catalog/search", {"limit": 1}, {"a", "b"}, tier="core")
        scenario, client = Mock(spec=["stats", "assert_contacts"]), Mock()
        prefix = "peer-a.test:8443 POST /ard/v1/search none 200"
        scenario.stats.side_effect = [{}, {prefix: 1}, {prefix: 1}, {prefix: 1}]
        client.request.side_effect = [({"items": [{"identifier": "a"}], "nextCursor": "lc1.next", "tier": "core"}, {}),
                                      ({"items": [{"identifier": "b"}], "tier": "core"}, {})]
        self.assertEqual(len(peer_pagination.walk(scenario, client, "/api/v1/catalog/search", {"limit": 1}, {"a", "b"}, tier="core")), 2)
        self.assertEqual(client.request.call_args.args[2], {"limit": 1, "cursor": "lc1.next"})

    def test_outage_control_restores_fault_and_reuses_exact_cursor(self):
        for public in (False, True):
            token_field, cursor_field = ("pageToken", "pageToken") if public else ("nextCursor", "cursor")
            body = {"query": {"text": "peer"}, "pageSize": 1} if public else {"text": "peer", "limit": 1}
            healthy = {"results" if public else "items": [{"identifier": "peer"}]}
            problem = {"errorCode": "DEPENDENCY_UNAVAILABLE"} if public else {"status": 503}
            for corrupt in (False, True):
                scenario = Mock(spec=["stats", "assert_contacts", "continuation_fault"])
                client = Mock()
                client.request.side_effect = [({token_field: "lc1.opaque"}, {}), (healthy, {}),
                                              ({**problem, **({token_field: "advanced"} if corrupt else {})}, {}), (healthy, {}), ({}, {})]
                with self.subTest(public=public, corrupt=corrupt):
                    if corrupt:
                        with self.assertRaisesRegex(AssertionError, "failed replay"):
                            peer_pagination.retry_control(scenario, client, "/search", body, token_field)
                    else:
                        peer_pagination.retry_control(scenario, client, "/search", body, token_field)
                        requests = client.request.call_args_list
                        self.assertEqual(requests[1].args[2], {**body, cursor_field: "lc1.opaque"})
                        self.assertEqual(requests[1].args, requests[3].args)
                    self.assertEqual(scenario.continuation_fault.call_args_list[-1].args, (False,))

    def test_semantic_restart_waits_for_ready_and_removes_only_owned_override(self):
        for enabled in (False, True):
            scenario = Mock(spec=["compose", "env", "b", "wait"])
            scenario.env = {"CATALOG_FUNCTIONAL_SUBNET": "192.0.2.0/24", "UNRELATED": "preserved", "CATALOG_SEMANTIC_SEARCH": "old"}
            scenario.b.request.side_effect = [federation.ClientFailure("not bound"), ({"status": "not_ready"}, {}), ({"status": "ready"}, {})]
            def wait(callback):
                self.assertFalse(callback())
                self.assertFalse(callback())
                self.assertTrue(callback())
            scenario.wait.side_effect = wait
            peer_pagination.semantic(scenario, enabled)
            self.assertEqual(scenario.compose.call_args_list[0].args, ("stop", "--timeout", "10", "api", "worker"))
            self.assertEqual(scenario.env["UNRELATED"], "preserved")
            if enabled:
                self.assertIn('"model": "peer-pagination"', scenario.env["CATALOG_SEMANTIC_SEARCH"])
            else:
                self.assertNotIn("CATALOG_SEMANTIC_SEARCH", scenario.env)
            self.assertEqual(scenario.compose.call_args_list[-1].args[-1], "http://127.0.0.1:9090/readyz")

    def test_startup_does_not_claim_unpublished_kinds(self):
        scenario = object.__new__(peers.Peers)
        scenario.completed = []
        with tempfile.TemporaryDirectory() as directory:
            scenario.state = Path(directory)
            scenario.passed("startup")
            progress = runner.read_document(scenario.state / "peers.json")
            self.assertEqual(progress["publishedKinds"], [])
            self.assertTrue(peers.valid_progress(progress))
            scenario.passed("seed")
            progress = runner.read_document(scenario.state / "peers.json")
            self.assertEqual(progress["publishedKinds"], peers.KINDS)
            self.assertTrue(peers.valid_progress(progress))

    def test_complete_phase_claim_cannot_replace_missing_peer_evidence(self):
        report = {"profile": "peers", "images": {"api": {}}, "servedVersion": {"version": "functional"},
                  "peers": {"completed": list(peers.PHASES), "catalogs": 2, "publishedKinds": peers.KINDS}}
        self.assertFalse(runner.finalize(report, list(runner.PEER_STEPS), "", True))
        self.assertEqual(report["status"], "failed")

    def test_source_removal_checks_raw_content_and_cross_tenant_denial(self):
        for corrupt in (False, True):
            with self.subTest(corrupt=corrupt):
                scenario = object.__new__(peers.Peers)
                scenario.b, scenario.other, scenario.b_private = Mock(), Mock(), Mock()
                scenario.change_source = Mock(return_value={"id": "source", "revision": 2})
                scenario.stats = Mock(return_value={})
                scenario.assert_contacts, scenario.check_adopted = Mock(), Mock()
                raw_reads = 0
                def request(method, path, _body=None, **_kwargs):
                    nonlocal raw_reads
                    if path == "/api/v1/observations/retained/raw":
                        raw_reads += 1
                        return ({"identifier": "changed" if corrupt and raw_reads > 1 else "original"}, {})
                    if path == "/api/v1/catalog/search":
                        return ({"items": []}, {})
                    return ({"id": "source", "revision": 2}, {"ETag": '"rev-2"'})
                scenario.b.request.side_effect = request
                with patch.object(peers, "observations", return_value=[{"id": "retained"}]), patch.object(peers, "snapshot_items", return_value=[{"id": "snapshot"}]), patch.object(peers, "mutate", side_effect=[({"digest": "preview"}, {}), (None, {})]):
                    if corrupt:
                        with self.assertRaisesRegex(AssertionError, "source removal changed retained raw evidence"):
                            scenario.remove_source({"id": "source", "revision": 1}, [])
                        scenario.check_adopted.assert_not_called()
                    else:
                        scenario.remove_source({"id": "source", "revision": 1}, [])
                        scenario.other.request.assert_any_call("GET", "/api/v1/observations/retained/raw", expected=(404,))
                        scenario.check_adopted.assert_called_once_with([])
                self.assertEqual(raw_reads, 2)

    def test_adopted_revision_guard_uses_read_envelope_and_rejects_overwrite(self):
        scenario = object.__new__(peers.Peers)
        scenario.b = Mock()
        scenario.check_no_adoption = Mock()
        revision = {"id": "retained", "contentSetDigest": "original", "adoption": {"snapshotId": "source-snapshot"}}
        adopted = [{"versionRevision": revision, "resource": {"id": "local-resource"}}]
        scenario.b.request.side_effect = [({"revision": copy.deepcopy(revision)}, {}), ({"id": "local-resource"}, {})]
        scenario.check_adopted(adopted)
        scenario.check_no_adoption.assert_called_once()
        scenario.b.request.side_effect = [({"revision": {**revision, "contentSetDigest": "overwritten"}}, {})]
        with self.assertRaisesRegex(AssertionError, "remote sync overwrote adopted revision"):
            scenario.check_adopted(adopted)
        scenario.b.request.side_effect = [({"revision": copy.deepcopy(revision)}, {}), ({"id": "overwritten-resource"}, {})]
        with self.assertRaisesRegex(AssertionError, "remote sync changed adopted local Resource"):
            scenario.check_adopted(adopted)

    def test_investigation_retains_schema_failure_and_still_fails_qualification(self):
        scenario = Mock()
        scenario.env = {"CATALOG_FUNCTIONAL_PEER_INVESTIGATE": "on"}
        scenario.completed = ["startup"]
        with tempfile.TemporaryDirectory() as directory:
            scenario.state = Path(directory)
            original = Mock(side_effect=peers.ClientFailure("functional response contract mismatch"))
            body = b'{"diagnostics":[{"status":"continuation_unavailable","message":"private response content"}]}'
            scenario.run.side_effect = lambda: peers.federation_module.observe_response("POST", "/api/v1/catalog/search", 200, {}, body, (200,))
            with patch.object(peers.federation_module, "observe_response", original):
                with self.assertRaisesRegex(runner.Failure, "peer-contract-failure"):
                    peers.run_observed(scenario)
                self.assertIs(peers.federation_module.observe_response, original)
            values = runner.read_document(scenario.state / "peer-contract-failures.json")
            self.assertTrue(peers.valid_contract_failures(values))
            self.assertEqual(values, [{"operation": "searchCatalog", "phase": "seed", "status": 200, "continuationUnavailable": True}])
            self.assertNotIn("private", (scenario.state / "peer-contract-failures.json").read_text())
            scenario.run.assert_called_once()

    def test_investigation_never_swallows_mutation_or_transport_failures(self):
        for path, message in (("/api/v1/resources", "functional response contract mismatch"),
                              ("/api/v1/catalog/search", "functional response validator failed")):
            scenario = Mock()
            scenario.env = {"CATALOG_FUNCTIONAL_PEER_INVESTIGATE": "on"}
            scenario.run.side_effect = lambda: peers.federation_module.observe_response("POST", path, 201, {}, b"{}", (201,))
            original = Mock(side_effect=peers.ClientFailure(message))
            with patch.object(peers.federation_module, "observe_response", original):
                with self.assertRaises(peers.ClientFailure):
                    peers.run_observed(scenario)
                self.assertIs(peers.federation_module.observe_response, original)
        scenario.env = {}
        scenario.run.side_effect = peers.ClientFailure("functional response contract mismatch")
        with self.assertRaises(peers.ClientFailure):
            peers.run_observed(scenario)

    def test_report_checkpoints_reject_private_or_incomplete_evidence(self):
        value = [{"operation": "searchCatalog", "phase": "live", "status": 200, "continuationUnavailable": True}]
        self.assertTrue(peers.valid_contract_failures(value))
        for change in ({"operation": "private"}, {"phase": "skipped"}, {"status": True}, {"status": 0},
                       {"continuationUnavailable": 1}, {"privateBody": "secret"}):
            self.assertFalse(peers.valid_contract_failures([{**value[0], **change}]))
        self.assertFalse(peers.valid_contract_failures(value * 33))
        self.assertFalse(peers.valid_contacts({"peer-a.test:8443 POST /ard/v1/search none 200": True}))
        self.assertFalse(peers.valid_contacts({"secret-query": 1}))
        self.assertTrue(peers.valid_contacts({"peer-a.test:8443 POST /ard/v1/search none 200": 1}))
        self.assertFalse(peers.valid_inventories({"a": [], "b": []}))

    def test_fault_setup_failure_still_restores_owned_peer(self):
        scenario = object.__new__(peers.Peers)
        scenario.b = Mock()
        scenario.b.request.return_value = ({"revision": 1, "lastCompleteSnapshotId": "complete"}, {})
        recover = Mock()
        with patch.object(peers, "observations", return_value=[]), patch.object(peers, "snapshot_items", return_value=[]):
            with self.assertRaisesRegex(AssertionError, "outage assertion"):
                scenario.failure_recovery({"id": "source"}, Mock(side_effect=AssertionError("outage assertion")), recover)
        recover.assert_called_once()

    def test_archive_preparation_uses_private_owned_native_inputs(self):
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            runner.write_document(directory / "peer-a.json", {"auth": {"mode": "development"}})
            (directory / "peer-a.json").chmod(0o444)
            environment = {"CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY": temp, "CATALOG_FUNCTIONAL_SUBNET": "172.31.1.240/28"}
            with patch.object(peers, "command") as command:
                peers.prepare_archives(environment)
            archives = directory / "archives"
            self.assertEqual(archives.stat().st_mode & 0o777, 0o700)
            manifest = runner.read_document(archives / "catalog.json")
            self.assertEqual(manifest["profiles"]["peers"], ["a-plugin", "b-plugin", "fresh-plugin", "a-plugin-v2"])
            self.assertEqual({r["representation"]["mediaType"] for r in manifest["resources"]}, {"application/agent-plugins+zip"})
            for resource in manifest["resources"]:
                native = runner.read_document(archives / resource["key"] / "plugin.json")
                self.assertEqual(native["name"], resource["key"])
                self.assertEqual(native["version"], resource["version"])
            actual = command.call_args.args[0]
            self.assertIn("172.31.1.252", actual)
            self.assertIn("https://172.31.1.252:8443", actual)
            self.assertEqual(runner.read_document(directory / "peer-a.json")["demoArchives"]["origin"], "https://172.31.1.252:8443")
            self.assertEqual((directory / "peer-a.json").stat().st_mode & 0o777, 0o444)

    def test_contact_checker_rejects_recursion_and_wrong_contact_count(self):
        scenario = object.__new__(peers.Peers)
        route = "peer-a.test:8443 POST /ard/v1/search "
        scenario.stats = Mock(return_value={route + "none 200": 3})
        scenario.assert_contacts({route + "none 200": 2}, peers.PUBLIC_A, 1)
        for counts in ({route + "none 200": 4}, {route + "none 200": 3, route + "auto 200": 1},
                       {route + "none 200": 3, "peer-b.test:8443 POST /ard/v1/search referrals 200": 1}):
            scenario.stats.return_value = counts
            with self.assertRaises(AssertionError):
                scenario.assert_contacts({route + "none 200": 2}, peers.PUBLIC_A, 1)

    def test_page_checker_requires_a_fresh_start_and_exact_continuation_outcome(self):
        scenario = object.__new__(peers.Peers)
        initial = "peer-a.test:8443 initial 200"
        continuation = "peer-a.test:8443 continuation 200"
        failed = "peer-a.test:8443 continuation 503"
        before = {initial: 3, continuation: 4, failed: 1}
        positive = {initial: 4, continuation: 5, failed: 1}
        scenario.page_stats = Mock(return_value=positive)
        scenario.assert_page_contacts(before)
        scenario.page_stats.return_value = {initial: 4, continuation: 4, failed: 2}
        scenario.assert_page_contacts(before, failed=True)
        scenario.page_stats.return_value = {initial: 4, continuation: 6, failed: 1}
        scenario.assert_page_contacts(before, continuations=2)
        for changed in ({initial: 3, continuation: 6, failed: 1},  # Starts with an old token.
                        {initial: 5, continuation: 5, failed: 1},
                        {initial: 4, continuation: 4, failed: 1},
                        {**positive, "private-a.test:8443 continuation 200": 1},
                        {initial: 4, continuation: 4, failed: 2}):
            scenario.page_stats.return_value = changed
            with self.subTest(changed=changed), self.assertRaises(AssertionError):
                scenario.assert_page_contacts(before)

    def test_paging_assertion_failure_restores_source_scope_and_fault_state(self):
        scenario = object.__new__(peers.Peers)
        source = {"id": "source", "scope": {"kinds": peers.KINDS},
                  "discoveryAssignments": [{"host": peers.PRIVATE_B, "audience": "authenticated"}]}
        scenario.public, scenario.b_private, scenario.b_public, scenario.b = Mock(), Mock(), Mock(), Mock()
        scenario.public.request.side_effect = [({"results": [{"identifier": "first"}], "pageToken": "opaque"}, {}),
                                              ({"results": [{"identifier": "later"}], "pageToken": "next"}, {}),
                                              ({"results": [{"identifier": "third"}]}, {})]
        scenario.b_private.request.return_value = ({"results": []}, {})
        scenario.b_public.request.return_value = ({"results": []}, {})
        scenario.stats, scenario.page_stats = Mock(return_value={}), Mock(return_value={})
        scenario.assert_contacts = Mock()
        scenario.change_source = Mock(side_effect=lambda _source, patch: {**source, **patch})
        scenario.continuation_fault = Mock()
        with patch.object(peers, "observations", return_value=[]), self.assertRaisesRegex(AssertionError, "omitted its later-page target"):
            scenario.check_live_paging(source, {"first", "later", "third"})
        self.assertEqual(scenario.change_source.call_count, 2)
        self.assertEqual([call.kwargs for call in scenario.continuation_fault.call_args_list], [{"page_size": 1}, {"page_size": 0}])
        self.assertEqual(scenario.change_source.call_args.args[1], {"scope": source["scope"]})

    def test_combined_search_rejects_repeated_ids_and_wrong_provenance(self):
        scenario = object.__new__(peers.Peers)
        scenario.b_native = {"local"}
        scenario.b_items = [{"resource": {"kind": "agent", "canonicalIdentifier": "local"}}]
        indexed = [{"kind": "plugin", "identifier": "remote"}]
        pages = [{"items": [{"identifier": "local", "type": "local"}], "durableTotal": 2, "partial": False, "nextCursor": "opaque"},
                 {"items": [{"identifier": "remote", "type": "indexed"}], "durableTotal": 2, "partial": False}]
        for mutation in ("repeat", "provenance", "total", "cycle"):
            values = copy.deepcopy(pages)
            if mutation == "repeat":
                values[1]["items"][0]["identifier"] = "local"
                values[1]["items"][0]["type"] = "local"
            elif mutation == "provenance":
                values[0]["items"][0]["type"] = "indexed"
            elif mutation == "total":
                values[0]["durableTotal"] = 3
            else:
                values[1]["nextCursor"] = "opaque"
            scenario.b = Mock()
            scenario.b.request.side_effect = [(value, {}) for value in values]
            with self.assertRaises(AssertionError):
                scenario.check_combined(indexed)

    def test_host_client_preserves_options_and_request_metadata(self):
        for host in (None, "public.fixture.test:8443"):
            for options, payload, expected in (({}, b'{"ok":true}', {"ok": True}),
                                               ({"decode_body": False}, b"\xffnot-json", b"\xffnot-json")):
                with self.subTest(host=host, options=options):
                    context = object()
                    client = peers.HostClient("https://127.0.0.1:8443", "test-token", host, context)
                    headers = {"Host": "caller.fixture.test", "X-Test": "kept"}
                    response_headers = {"Content-Type": "application/octet-stream", "ETag": '"fixture"'}
                    with patch.object(federation.coverage, "enabled", return_value=False), \
                         patch.object(federation, "bounded_request", return_value=(201, response_headers, payload)) as transport:
                        result, returned_headers = client.request("POST", "/fixture", {"value": 1}, (201,), headers, **options)
                    self.assertEqual(result, expected)
                    self.assertEqual(returned_headers, response_headers)
                    transport.assert_called_once()
                    request, actual_context = transport.call_args.args
                    self.assertIs(actual_context, context)
                    self.assertEqual(request.full_url, "https://127.0.0.1:8443/fixture")
                    self.assertEqual(request.get_method(), "POST")
                    self.assertEqual(request.data, b'{"value":1}')
                    self.assertEqual(dict(request.header_items()), {"Host": host or "caller.fixture.test", "X-test": "kept",
                                                                   "Authorization": "Bearer test-token", "Content-type": "application/json"})
                    self.assertEqual(headers, {"Host": "caller.fixture.test", "X-Test": "kept"})

    def test_host_client_forwards_readiness_classification_without_retrying(self):
        client = peers.HostClient("http://127.0.0.1:8080")
        client.catalog = True
        with patch.object(federation.coverage, "enabled", return_value=True), \
             patch.object(federation, "bounded_request", return_value=(503, {}, b"{}")) as transport, \
             patch.object(federation, "observe_response") as observe:
            with self.assertRaisesRegex(AssertionError, "HTTP 503"):
                client.request("GET", "/readyz", retry_unavailable=True, decode_body=False)
        transport.assert_called_once()
        observe.assert_called_once_with("GET", "/readyz", 503, {}, b"{}", (200, 503))

    def test_polling_rejects_callbacks_that_finish_at_or_after_either_deadline(self):
        scenario = object.__new__(peers.Peers)
        for shared_end, elapsed in ((600, 59), (600, 60), (600, 61), (5, 4), (5, 5), (5, 6)):
            with self.subTest(shared_end=shared_end, elapsed=elapsed):
                scenario.end = shared_end
                now = [0.0]
                def callback():
                    now[0] += elapsed
                    return "ready"
                with patch.object(peers.time, "monotonic", side_effect=lambda: now[0]), patch.object(peers.time, "sleep") as sleep:
                    if elapsed < min(60, shared_end):
                        self.assertEqual(scenario.wait(callback), "ready")
                    else:
                        with self.assertRaisesRegex(runner.Failure, "peer-state-timeout"):
                            scenario.wait(callback)
                    sleep.assert_not_called()

    def test_polling_bounds_sleep_and_supports_only_scoped_longer_wait(self):
        scenario = object.__new__(peers.Peers)
        scenario.end = 0.1
        now = [0.0]
        def sleep(seconds):
            self.assertLessEqual(seconds, 0.1)
            now[0] += seconds
        callback = Mock(return_value=None)
        with patch.object(peers.time, "monotonic", side_effect=lambda: now[0]), patch.object(peers.time, "sleep", side_effect=sleep):
            with self.assertRaisesRegex(runner.Failure, "peer-state-timeout"):
                scenario.wait(callback)
        callback.assert_called_once()
        scenario.end = 600
        now[0] = 0
        def ready():
            now[0] = 90
            return True
        with patch.object(peers.time, "monotonic", side_effect=lambda: now[0]):
            self.assertTrue(scenario.wait(ready, timeout=120))
        self.assertEqual(scenario.end, 600)
        with patch.object(peers.time, "monotonic", return_value=0):
            with self.assertRaisesRegex(ValueError, "fixture-error"):
                scenario.wait(Mock(side_effect=ValueError("fixture-error")))

    def test_registry_stops_at_page_budget(self):
        client = Mock()
        client.request.side_effect = [({"items": [{"identifier": str(page)}], "total": 100, "pageToken": "next-" + str(page)}, {}) for page in range(12)]
        with self.assertRaisesRegex(runner.Failure, "peer-list-page-limit"):
            peers.registry_members(client)
        self.assertEqual(client.request.call_count, 12)

    def test_phase_progress_is_ordered_and_never_marks_partial_work_complete(self):
        scenario = object.__new__(peers.Peers)
        scenario.completed = []
        with tempfile.TemporaryDirectory() as directory:
            scenario.state = Path(directory)
            with patch("builtins.print"):
                scenario.phase("startup")
            self.assertEqual(runner.read_document(scenario.state / "peers-phase.json"), {"phase": "startup"})
            self.assertFalse((scenario.state / "peers.json").exists())
            scenario.passed("startup")
            partial = runner.read_document(scenario.state / "peers.json")
            self.assertEqual(partial, {"completed": ["startup"], "catalogs": 2, "publishedKinds": []})
            self.assertFalse(peers.valid_evidence(partial))
            with self.assertRaisesRegex(AssertionError, "phase order"):
                scenario.phase("complete")
            with self.assertRaisesRegex(AssertionError, "progress mismatch"):
                scenario.passed("seed-skipped")
            self.assertEqual(runner.read_document(scenario.state / "peers.json"), partial)

    def test_search_rejects_same_count_wrong_identity_content_or_provenance(self):
        observation = {"identifier": "urn:peer", "kind": "agent", "version": "1", "versionPresent": True, "normalizedEntryDigest": "digest"}
        result = {key: observation[key] for key in ("identifier", "kind", "version", "versionPresent")}
        result.update(type="indexed", indexed={"normalizedEntryDigest": "digest", "sourceIds": ["source"], "sourceCount": 1,
                      "sourcesTruncated": False, "conflict": False, "sameSourceConflict": False})
        document = {"items": [result], "partial": False}
        peers.check_indexed_search(document, [observation], "source")
        for section, field, value in (("result", "identifier", "other"), ("result", "version", "2"), ("result", "kind", "skill"),
                                      ("result", "type", "local"), ("indexed", "normalizedEntryDigest", "stale"),
                                      ("indexed", "sourceIds", ["other"]), ("indexed", "sourceCount", 2), ("indexed", "conflict", True),
                                      ("document", "partial", True), ("document", "nextCursor", "unexpected")):
            with self.subTest(field=field):
                changed = copy.deepcopy(document)
                target = changed if section == "document" else changed["items"][0] if section == "result" else changed["items"][0]["indexed"]
                target[field] = value
                with self.assertRaises(AssertionError):
                    peers.check_indexed_search(changed, [observation], "source")

    def test_recovery_follows_live_job_or_starts_after_terminal_failure(self):
        for state, recovery in (("waiting", "valid"), ("succeeded", "valid"), ("failed", "valid"), ("cancelled", "valid"), ("dead_letter", "valid"),
                                ("succeeded", "unchanged"), ("succeeded", "error"), ("succeeded", "incomplete")):
            with self.subTest(state=state, recovery=recovery):
                scenario = object.__new__(peers.Peers)
                scenario.b = Mock()
                scenario.wait = lambda callback: callback()
                scenario.finish_job, scenario.synchronize = Mock(), Mock()
                retained = {"revision": 1, "lastCompleteSnapshotId": "complete"}
                recovered = {"revision": 1, "lastCompleteSnapshotId": "complete" if recovery == "unchanged" else "recovered"}
                if recovery == "error":
                    recovered["lastError"] = "upstream unavailable"
                scenario.b.request.side_effect = [(retained, {}), (retained, {}),
                    ({"items": [{"details": {"facts": {"jobId": "job", "jobAttempt": 1}}}]}, {}),
                    ({"id": "job", "state": state}, {}), (recovered, {})]
                fault, recover = Mock(), Mock()
                with patch.object(peers, "observations", return_value=[{"id": "retained"}]), \
                     patch.object(peers, "snapshot_items", side_effect=[[{"id": "complete"}], [{"id": "failed", "state": "failed", "enumerationComplete": False}],
                         [{"id": "recovered", "state": "complete", "enumerationComplete": recovery != "incomplete"}]]), \
                     patch.object(peers, "sync", return_value={"job": {"id": "job"}}):
                    if recovery == "valid":
                        scenario.failure_recovery({"id": "source"}, fault, recover)
                    else:
                        with self.assertRaisesRegex(AssertionError, "peer recovery"):
                            scenario.failure_recovery({"id": "source"}, fault, recover)
                fault.assert_called_once()
                recover.assert_called_once()
                audit_path = scenario.b.request.call_args_list[2].args[1]
                self.assertIn("targetType=source_snapshot", audit_path)
                self.assertIn("targetId=failed", audit_path)
                if state in ("waiting", "succeeded"):
                    scenario.finish_job.assert_called_once_with({"id": "job"})
                    scenario.synchronize.assert_not_called()
                else:
                    scenario.synchronize.assert_called_once_with({"id": "source"})
                    scenario.finish_job.assert_not_called()

    def test_failure_assertion_still_restores_peer(self):
        scenario = object.__new__(peers.Peers)
        scenario.b = Mock()
        scenario.b.request.return_value = ({"revision": 1, "lastCompleteSnapshotId": "complete"}, {})
        scenario.wait = lambda callback: callback()
        recover = Mock()
        with patch.object(peers, "observations", return_value=[{"id": "retained"}]), \
             patch.object(peers, "snapshot_items", side_effect=[[{"id": "complete"}], [{"id": "failed", "state": "failed", "enumerationComplete": True}]]), \
             patch.object(peers, "sync", return_value={"job": {"id": "job"}}):
            with self.assertRaisesRegex(AssertionError, "gained authority"):
                scenario.failure_recovery({"id": "source"}, Mock(), recover)
        recover.assert_called_once()

    def test_finish_job_rejects_terminal_failure_without_polling(self):
        scenario = object.__new__(peers.Peers)
        scenario.b = Mock()
        scenario.wait = lambda callback: callback()
        for state in ("failed", "cancelled", "dead_letter"):
            scenario.b.request.return_value = ({"id": "job", "state": state}, {})
            with self.assertRaisesRegex(runner.Failure, "peer-sync-terminal-failure"):
                scenario.finish_job({"id": "job"})

    def test_peer_ports_are_distinct_and_survive_inner_make(self):
        with patch.object(runner, "port", side_effect=[10001, 10001, 10002, 10002, 10003]):
            env = runner.compose_environment({"CATALOG_FUNCTIONAL_PROFILE": "peers", "CATALOG_FUNCTIONAL_COMMIT": "test"}, "abcdef123456", Path("/tmp/peer-fixture"))
        self.assertEqual(env["CATALOG_COMPOSE_HTTP_PORT"], "10001")
        self.assertEqual(env["CATALOG_COMPOSE_FIXTURE_PORT"], "10002")
        self.assertEqual(env["CATALOG_FUNCTIONAL_PEER_HTTP_PORT"], "10003")
        env.update(CATALOG_FUNCTIONAL_STATE_DIRECTORY="/tmp/peer-state", CATALOG_FUNCTIONAL_BASE_URL="http://127.0.0.1:10001",
                   CATALOG_FUNCTIONAL_FIXTURE_PORT="10002")
        for name in list(env):
            if name.startswith("CATALOG_COMPOSE_"):
                del env[name]
        scenario = peers.Peers(env)
        self.assertEqual(scenario.env["CATALOG_COMPOSE_HTTP_PORT"], "10001")
        self.assertEqual(scenario.env["CATALOG_COMPOSE_FIXTURE_PORT"], "10002")
        self.assertEqual(scenario.env["CATALOG_FUNCTIONAL_PEER_HTTP_PORT"], "10003")
        self.assertEqual(scenario.compose_args[-1], "open-agentic-catalog-functional-abcdef123456")
        # The child receives private launcher inputs after Make unexports
        # runtime settings; recreating a runtime must retain the initial policy.
        for setting, value in {"CATALOG_HTTP_REQUEST_TIMEOUT": "3s", "CATALOG_WORKER_SOURCE_MAINTENANCE_INTERVAL": "1m",
                               "CATALOG_WORKER_HOUSEKEEPING_INTERVAL": "1m", "CATALOG_WORKER_TERMINAL_JOB_RETENTION": "24h",
                               "CATALOG_WORKER_ACKNOWLEDGED_OUTBOX_RETENTION": "1h"}.items():
            self.assertEqual(scenario.env[setting], value)

    def test_evidence_requires_every_ordered_phase(self):
        evidence = {"completed": list(peers.PHASES), "catalogs": 2, "publishedKinds": peers.KINDS}
        self.assertTrue(peers.valid_evidence(evidence))
        for field, value in (("completed", []), ("completed", list(reversed(peers.PHASES))), ("catalogs", True), ("publishedKinds", ["agent"])):
            broken = {**evidence, field: value}
            self.assertFalse(peers.valid_evidence(broken))
        self.assertFalse(peers.valid_evidence({**evidence, "privateBody": "not allowed"}))
        report = self.complete_report()
        self.assertTrue(runner.finalize(report, list(runner.PEER_STEPS), "", True))
        self.assertEqual(report["qualification"], "two-catalog-federation")
        self.assertFalse(runner.finalize({**report, "peers": None}, list(runner.PEER_STEPS), "", True))
        self.assertFalse(runner.finalize(dict(report), list(runner.PEER_STEPS), "", False))

    def test_registry_consumes_exact_tokens_and_members(self):
        client = Mock()
        client.request.side_effect = [({"items": [{"identifier": "one"}], "total": 2, "pageToken": "opaque+/="}, {}),
                                      ({"items": [{"identifier": "two"}], "total": 2}, {})]
        self.assertEqual(len(peers.registry_members(client)), 2)
        self.assertIn("pageToken=opaque%2B%2F%3D", client.request.call_args.args[1])
        for pages in (
            [({"items": [{"identifier": "one"}, {"identifier": "one"}], "total": 2}, {})],
            [({"items": [], "total": 1}, {})],
            [({"items": [], "total": 2, "pageToken": "repeated"}, {})] * 2,
            [({"items": None, "total": 0}, {})],
        ):
            client.request.side_effect = pages
            with self.assertRaises(AssertionError):
                peers.registry_members(client)

    def test_prepare_scopes_credentials_and_preserves_base_config(self):
        root = Path(__file__).resolve().parents[2]
        config = runner.read_document(root / "config/postgres.json")
        original = copy.deepcopy(config)
        policy = {"privateProfiles": [{"name": "fixture", "origins": ["https://fixture"], "assignedTenantIds": [peers.TENANT]}]}
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            (directory / "credentials").mkdir()
            runner.write_document(directory / "policy.json", policy)
            read = peers.read_document
            with patch.object(peers, "read_document", side_effect=lambda path: copy.deepcopy(config) if str(path) == "config/postgres.json" else read(path)):
                peers.prepare({"CATALOG_FUNCTIONAL_PROFILE": "peers", "CATALOG_FUNCTIONAL_FIXTURE_DIRECTORY": temp})
            result = runner.read_document(directory / "peer-a.json")
            self.assertEqual(config, original)
            self.assertNotIn("local-admin", result["auth"]["developmentTokens"])
            self.assertIn("peer-local-reader", result["auth"]["developmentTokens"])
            self.assertEqual({h["host"] for h in result["discovery"]["hosts"]}, {peers.PUBLIC_A, peers.PRIVATE_A})
            credential = runner.read_document(directory / "credentials/peer-reader.json")
            self.assertEqual(credential["origin"], "https://private-a.test:8443")
            self.assertEqual(credential["tenantId"], peers.TENANT)
            self.assertEqual((directory / "peer-a.json").stat().st_mode & 0o777, 0o444)
        with self.assertRaises(runner.Failure):
            peers.prepare({"CATALOG_FUNCTIONAL_PROFILE": "default-off"})


class ForwardCorrectionEvidenceTests(unittest.TestCase):
    def saved(self, generation):
        raw = ('{"generation":"' + generation + '"}').encode()
        digest = "sha256:" + peers.hashlib.sha256(raw).hexdigest()
        snapshot = {"id": generation, "sourceId": "source", "sourceRevision": 1, "state": "complete", "enumerationComplete": True}
        occurrence = {"snapshotId": generation, "observationId": "observation-" + generation, "sourceId": "source",
                      "sourceRevision": 1, "exactEntryDigest": digest, "evidenceComplete": True,
                      "retrievalDigest": "sha256:" + "b" * 64, "entryLocation": "/entries/0"}
        return {"snapshot": snapshot, "observationId": occurrence["observationId"], "raw": raw, "occurrence": occurrence}

    def test_capture_requires_complete_bound_snapshot_bytes_and_occurrence(self):
        saved = self.saved("A")
        for fault in (None, "snapshot", "snapshot-id", "source-id", "observation-source", "observation-id", "digest", "occurrence", "revision", "source", "overflow", "missing"):
            scenario = object.__new__(peers.Peers)
            scenario.b = Mock()
            snapshot = copy.deepcopy(saved["snapshot"])
            occurrence = copy.deepcopy(saved["occurrence"])
            current = {"id": "source", "lastCompleteSnapshotId": "A"}
            observation = {"id": saved["observationId"], "sourceId": "source", "identifier": "entry", "exactEntryDigest": occurrence["exactEntryDigest"]}
            if fault == "snapshot":
                snapshot["enumerationComplete"] = False
            if fault == "digest":
                observation["exactEntryDigest"] = "sha256:" + "c" * 64
            if fault == "occurrence":
                occurrence["evidenceComplete"] = False
            if fault == "revision":
                occurrence["sourceRevision"] = 2
            if fault == "source":
                occurrence["sourceId"] = "other"
            if fault == "snapshot-id":
                snapshot["id"] = "other"
            if fault == "source-id":
                current["id"] = "other"
            if fault == "observation-source":
                observation["sourceId"] = "other"
            if fault == "observation-id":
                occurrence["observationId"] = "other"
            page = {"items": [occurrence], **({"nextCursor": "more"} if fault == "overflow" else {})}
            scenario.b.request.side_effect = [(current, {}), (snapshot, {}), (saved["raw"], {}), (page, {})]
            with self.subTest(fault=fault), patch.object(peers, "observations", return_value=[] if fault == "missing" else [observation]):
                if fault is None:
                    self.assertEqual(scenario.capture_correction_evidence({"id": "source"}, "entry"), saved)
                    self.assertTrue(scenario.b.request.call_args_list[2].kwargs["decode_body"] is False)
                else:
                    with self.assertRaises(AssertionError):
                        scenario.capture_correction_evidence({"id": "source"}, "entry")

    def test_correction_retains_exact_A_and_B_bytes_snapshot_and_provenance(self):
        retained, corrected = [self.saved("A"), self.saved("B")], self.saved("C")
        for fault in (None, "bytes", "snapshot", "occurrence", "overflow"):
            scenario = object.__new__(peers.Peers)
            scenario.b = Mock()
            replies = []
            for saved in retained:
                raw, snapshot, occurrence = saved["raw"], copy.deepcopy(saved["snapshot"]), copy.deepcopy(saved["occurrence"])
                if fault == "bytes":
                    raw += b" "
                if fault == "snapshot":
                    snapshot["sourceRevision"] = 2
                if fault == "occurrence":
                    occurrence["entryLocation"] = "/entries/1"
                replies += [(raw, {}), (snapshot, {}), ({"items": [occurrence], **({"nextCursor": "more"} if fault == "overflow" else {})}, {})]
            scenario.b.request.side_effect = replies
            with self.subTest(fault=fault):
                if fault is None:
                    scenario.check_correction_history(retained, corrected)
                    self.assertEqual(scenario.b.request.call_count, 6)
                else:
                    with self.assertRaisesRegex(AssertionError, "historical"):
                        scenario.check_correction_history(retained, corrected)
        for final in (retained[1], {**corrected, "raw": retained[0]["raw"]}):
            with self.assertRaisesRegex(AssertionError, "A/B/C"):
                scenario.check_correction_history(retained, final)


if __name__ == "__main__":
    unittest.main()
