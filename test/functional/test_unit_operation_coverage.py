"""Operation accounting is independent of workflow assertions and private data."""

import copy
import io
import json
import os
from pathlib import Path
import tempfile
from concurrent.futures import ThreadPoolExecutor
import unittest
from unittest.mock import patch

import operation_coverage as coverage
from http_client import ClientFailure
from federation import Client
import response_contract
import runner
import audit
import deadline


class OperationCoverageTests(unittest.TestCase):
    def setUp(self):
        coverage._streams.clear()
        # Make resolves contract/inventory identities from the repository root;
        # the Go test launcher starts Python in test/functional instead.
        previous = Path.cwd()
        os.chdir(Path(__file__).resolve().parents[2])
        self.addCleanup(os.chdir, previous)

    def test_closed_counter_protocol_and_outcomes(self):
        ids = ["getVersion"]
        row = {"operationId": "getVersion", "status": 200, "contract": "passed", "outcome": "positive", "count": 1}
        self.assertEqual(coverage.validate_rows([row], ids, []), [row])
        for change in ({"count": True}, {"count": 0}, {"count": coverage.MAX_COUNT+1}, {"outcome": "secret"},
                       {"status": 600}, {"status": True}, {"contract": "secret"}, {"operationId": "unknown"},
                       {"status": 0}, {"outcome": "transport"}, {"outcome": "negative"}, {"path": "private"}):
            with self.subTest(change=change), self.assertRaises(ClientFailure):
                coverage.validate_rows([dict(row, **change)], ids, [])
        with self.assertRaises(ClientFailure):
            coverage.validate_rows([row, row], ids, [])
        for invalid in ([], ["z", "a"], ["a", "a"], ["private/path"], [True]):
            with self.assertRaises(ClientFailure):
                coverage.valid_ids(invalid)
        for status, expected, label in ((0, (200,), "transport"), (503, (200,), "unexpected"),
                                        (503, (503,), "negative"), (304, (304,), "positive"),
                                        (202, (202,), "positive"), (200, None, "unchecked")):
            self.assertEqual(coverage.outcome(status, expected), label)

    def test_concurrent_checkpoints_are_bounded_and_scoped_to_steps(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            runner.write_document(root / "operations.json", ["getVersion"])
            env = {"CATALOG_FUNCTIONAL_OPERATION_COVERAGE": "on", "CATALOG_FUNCTIONAL_STATE_DIRECTORY": temporary}
            value = {"operationId": "getVersion", "status": 200, "contract": "passed"}
            with patch.dict(os.environ, env, clear=True):
                with ThreadPoolExecutor(max_workers=4) as pool:
                    list(pool.map(lambda _: coverage.record(value, (200,)), range(20)))
                first = coverage.collect(root, "default-off")
                self.assertEqual(first["rows"][0]["count"], 20)
                self.assertEqual(first["rows"][0]["step"], "startup-contract")
                runner.mark(root, "startup-contract")
                coverage.record(value, (200,))
                second = coverage.collect(root, "default-off")
                self.assertEqual(len(second["rows"]), 2)
                self.assertEqual(len(list(root.glob("operations-*.json"))), 2)
                self.assertEqual(coverage.validate_coverage(second, "default-off"), second)
                with patch.object(coverage, "MAX_COUNT", 1), self.assertRaises(ClientFailure):
                    coverage.record(value, (200,))
                with patch.object(coverage, "MAX_STREAMS", 1), self.assertRaises(ClientFailure):
                    coverage.collect(root, "default-off")
                with patch.object(runner, "write_document", side_effect=OSError("private")):
                    with self.assertRaisesRegex(ClientFailure, "^operation collection failed$"):
                        coverage.record(value, (200,))

    def test_missing_malformed_and_foreign_streams_fail_closed(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            runner.write_document(root / "operations.json", ["getVersion"])
            with self.assertRaises(ClientFailure):
                coverage.collect(root, "default-off")
            path = root / ("operations-" + "a" * 24 + ".json")
            for invalid in ({}, {"step": "foreign", "rows": []}, {"step": "startup-contract", "rows": [True]}):
                runner.write_document(path, invalid)
                with self.assertRaises(ClientFailure):
                    coverage.collect(root, "default-off")
            path.unlink()
            path.symlink_to(root / "operations.json")
            with self.assertRaises(ClientFailure):
                coverage.collect(root, "default-off")

    def test_process_streams_merge_without_losing_outcomes_or_exceeding_bounds(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            runner.write_document(root / "operations.json", ["getVersion"])
            env = {"CATALOG_FUNCTIONAL_OPERATION_COVERAGE": "on", "CATALOG_FUNCTIONAL_STATE_DIRECTORY": temporary}
            value = {"operationId": "getVersion", "status": 200, "contract": "passed"}
            with patch.dict(os.environ, env, clear=True):
                # Distinct process keys exercise separate checkpoints without
                # launching child processes or adding timing-sensitive waits.
                for pid in (101, 102):
                    with patch.object(coverage.os, "getpid", return_value=pid):
                        coverage.record(value, (200,))
                        coverage.record(value)
                        coverage.record(dict(value, status=0, contract="not-checked"), (200,))
            result = coverage.collect(root, "default-off")
            self.assertEqual(len(list(root.glob("operations-*.json"))), 2)
            self.assertEqual({row["outcome"]: row["count"] for row in result["rows"]},
                             {"positive": 2, "unchecked": 2, "transport": 2})
            self.assertEqual(coverage.validate_coverage(result, "default-off"), result)
            # Individually valid checkpoints must still fit the combined bound.
            with patch.object(coverage, "MAX_COUNT", 1), self.assertRaisesRegex(ClientFailure, "counter limit"):
                coverage.collect(root, "default-off")

    def test_recording_is_disabled_without_inputs_and_stops_after_final_step(self):
        with patch.dict(os.environ, {}, clear=True), patch.object(runner, "read_document") as read:
            coverage.record({"private": "unused"})
            read.assert_not_called()
            self.assertFalse(coverage.catalog_origin("http://127.0.0.1:12345"))
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            runner.write_document(root / "operations.json", ["getVersion"])
            for step in runner.selected_steps("default-off"):
                runner.mark(root, step)
            env = {"CATALOG_FUNCTIONAL_OPERATION_COVERAGE": "on", "CATALOG_FUNCTIONAL_STATE_DIRECTORY": temporary}
            with patch.dict(os.environ, env, clear=True), self.assertRaisesRegex(ClientFailure, "outside active step"):
                coverage.record({"operationId": "getVersion", "status": 200, "contract": "passed"}, (200,))
            self.assertEqual(list(root.glob("operations-*.json")), [])

    def test_api_selection_and_retry_classification_preserve_failures(self):
        env = {"CATALOG_FUNCTIONAL_OPERATION_COVERAGE": "on", "CATALOG_FUNCTIONAL_BASE_URL": "http://127.0.0.1:12345"}
        with patch.dict(os.environ, env, clear=True), patch("federation.observe_response") as observe:
            api = Client(env["CATALOG_FUNCTIONAL_BASE_URL"])
            fixture = Client("http://127.0.0.1:23456")
            self.assertTrue(api.catalog)
            self.assertFalse(fixture.catalog)
            with patch("federation.bounded_request", return_value=(503, {}, b'{}')):
                for retry in (False, True):
                    with self.assertRaises(AssertionError):
                        api.request("GET", "/readyz", retry_unavailable=retry)
                    self.assertEqual(observe.call_args.args[-1], (200, 503) if retry else (200,))
                observe.reset_mock()
                fixture.request("GET", "/readyz", expected=(503,))
                observe.assert_not_called()
            with patch("federation.bounded_request", return_value=(200, {}, b'{}')), patch("federation.validate_response") as validate:
                fixture.request("GET", "/api/v1/namespaces")
                validate.assert_called_once_with("GET", "/api/v1/namespaces", 200, {}, b'{}', collect=False)
                observe.assert_not_called()
            with self.assertRaises(ClientFailure):
                api.request("POST", "/api/v1/namespaces", retry_unavailable=True)
            with patch("federation.bounded_request", side_effect=ClientFailure("functional HTTP transport failed")):
                with self.assertRaises(ClientFailure):
                    api.request("GET", "/version")
                self.assertEqual(observe.call_args.args[2:5], (0, {}, b""))

    def test_validator_records_contract_failure_before_raising(self):
        value = {"operationId": "getVersion", "status": 200, "contract": "failed"}
        with patch.dict(os.environ, {"CATALOG_FUNCTIONAL_CONTRACT_DRIVER": "/owned/driver"}), \
                patch("response_contract.subprocess.run") as run, patch.object(coverage, "record") as record:
            run.return_value.returncode = 0
            run.return_value.stdout = json.dumps(value).encode()
            with self.assertRaisesRegex(ClientFailure, "contract mismatch"):
                response_contract.observe("GET", "/version", 200, {}, b"private", (200,))
            record.assert_called_once_with(value, (200,))
            self.assertNotIn("private", str(record.call_args))
            for raw in (b"x" * 4097, b"not-json"):
                run.return_value.stdout = raw
                with self.assertRaises(ClientFailure):
                    response_contract.observe("GET", "/version", 200, {}, b"private", (200,))

    def test_unobserved_peer_still_validates_without_recording(self):
        env = {"CATALOG_FUNCTIONAL_OPERATION_COVERAGE": "on", "CATALOG_FUNCTIONAL_CONTRACT_DRIVER": "/owned/driver"}
        with patch.dict(os.environ, env), patch("response_contract.subprocess.run") as run, patch.object(coverage, "record") as record:
            run.return_value.returncode = 0
            response_contract.validate("GET", "/api/v1/namespaces", 200, {}, b'{}', collect=False)
            self.assertEqual(run.call_args.args[0], ["/owned/driver", "--validate-stdin"])
            record.assert_not_called()
            run.return_value.returncode = 1
            with self.assertRaisesRegex(ClientFailure, "contract mismatch"):
                response_contract.validate("GET", "/api/v1/namespaces", 200, {}, b'{}', collect=False)

    def test_representative_missing_and_partial_output(self):
        value = {"operationId": "getControlOpenAPI", "status": 200, "contract": "passed", "expected": 200}
        with patch.dict(os.environ, {"CATALOG_FUNCTIONAL_CONTRACT_DRIVER": "/owned/driver"}), \
                patch("response_contract.subprocess.run") as run, patch.object(coverage, "record") as record:
            run.return_value.returncode = 0
            for raw in (b"", b"\n", b"x" * 16385):
                run.return_value.stdout = raw
                with self.assertRaisesRegex(ClientFailure, "representative observations"):
                    response_contract.representative("http://127.0.0.1:12345", "owned-document")
                record.assert_not_called()
            run.return_value.stdout = json.dumps(value).encode() + b"\n"
            run.return_value.returncode = 1
            with self.assertRaisesRegex(ClientFailure, "contracts failed"):
                response_contract.representative("http://127.0.0.1:12345", "owned-document")
            record.assert_called_once_with({k: v for k, v in value.items() if k != "expected"}, (200,))

    def test_audit_and_deadline_transport_failures_remain_failures(self):
        for module, call in ((audit, lambda: audit.request("http://127.0.0.1:12345", "GET", "/api/v1/namespaces", "fixture-token")),
                             (deadline, lambda: deadline.request(deadline.namespace_read("http://127.0.0.1:12345"), (200,)))):
            with self.subTest(module=module.__name__), patch.dict(os.environ, {"CATALOG_FUNCTIONAL_OPERATION_COVERAGE": "on"}), \
                    patch.object(module, "bounded_request", side_effect=ClientFailure("functional HTTP transport failed")), \
                    patch.object(module, "validate") as validate:
                with self.assertRaises(ClientFailure):
                    call()
                validate.assert_called_once_with("GET", "/api/v1/namespaces", 0, {}, b"", (200,))

    def report(self, profile="default-off"):
        inventory = runner.read_document("test/functional/coverage.json")
        ids = sorted(s[5:] for f in inventory["scenarios"] for s in f["surfaces"] if s.startswith("http:"))
        return {"formatVersion": 1, "runId": "functional-" + "a" * 12, "profile": profile,
                "status": "passed", "cleanup": "passed", "failureCode": "",
                "steps": [{"id": k, "status": "passed"} for k in runner.selected_steps(profile)],
                "source": {"commit": "b" * 40, "worktreeDigest": "sha256:" + "c" * 64},
                "contractDigest": runner.digest_files(["api/openapi/control-v1.yaml"]),
                "inventoryDigest": runner.digest_files(["test/functional/coverage.json"]),
                "configurationDigest": "sha256:" + "d" * 64,
                "servedVersion": {"service": "open-agentic-catalog", "version": "functional", "commit": "b" * 40, "sourceState": "unknown",
                                  "ardProfile": "ard-fixture", "aiCatalogProfile": "ai-catalog-fixture"},
                "images": {k: {"configurationDigest": "sha256:" + "e" * 64} for k in ("api", "worker")},
                "operationCoverage": {"formatVersion": 1, "denominator": ids,
                    "unobservedCollectors": ["smoke", "remote-cli", "shell-probes", "publication-report-sdk"],
                    "rows": [{"step": "startup-contract", "operationId": "getVersion", "status": 200,
                              "contract": "passed", "outcome": "positive", "count": 3}]}}

    def test_explicit_summary_unions_ids_and_refuses_incompatible_or_failed_evidence(self):
        with tempfile.TemporaryDirectory() as temporary:
            first, second = Path(temporary) / "first.json", Path(temporary) / "second.json"
            report = self.report()
            runner.write_document(first, report)
            summary = coverage.summarize([first, first])
            self.assertEqual(summary["expectedPositiveOperations"], 1)
            self.assertEqual(len(summary["runs"]), 1)
            self.assertEqual(summary["totalOperations"], len(report["operationCoverage"]["denominator"]))
            other = self.report("webhooks")
            other["runId"] = "functional-" + "f" * 12
            other["configurationDigest"] = "sha256:" + "f" * 64
            runner.write_document(second, other)
            combined = coverage.summarize([first, second])
            self.assertEqual(len(combined["runs"]), 2)
            self.assertEqual(combined["expectedPositiveOperations"], 1)
            additional = dict(other["operationCoverage"]["rows"][0], operationId="getCapabilities")
            other["operationCoverage"]["rows"].append(additional)
            runner.write_document(second, other)
            self.assertEqual(coverage.summarize([first, second])["expectedPositiveOperations"], 2)
            additional.update(status=401, outcome="negative")
            runner.write_document(second, other)
            combined = coverage.summarize([first, second])
            self.assertEqual(combined["expectedPositiveOperations"], 1)
            self.assertEqual(combined["observedOperations"], 2)
            self.assertTrue(next(v for v in combined["operations"] if v["operationId"] == "getCapabilities")["expectedNegative"])
            other["operationCoverage"]["rows"].pop()
            for mutation in (lambda r: r["source"].update(commit="f" * 40),
                             lambda r: r.update(contractDigest="sha256:" + "0" * 64),
                             lambda r: r.update(configurationDigest="private input"),
                             lambda r: r["operationCoverage"].update(rows=[]),
                             lambda r: r.update(servedVersion=None),
                             lambda r: r["servedVersion"].update(commit="f" * 40),
                             lambda r: r["servedVersion"].update(service="another-service"),
                             lambda r: r["servedVersion"].update(ardProfile="private/path"),
                             lambda r: r.update(runId=report["runId"]),
                             lambda r: r["operationCoverage"]["denominator"].append("unknown"),
                             lambda r: r["operationCoverage"]["rows"][0].update(count=-1)):
                changed = copy.deepcopy(other)
                mutation(changed)
                runner.write_document(second, changed)
                with self.assertRaises(ClientFailure):
                    coverage.summarize([first, second])
            for status, cleanup in (("failed", "passed"), ("incomplete", "not-run"), ("passed", "failed")):
                changed = copy.deepcopy(other)
                changed.update(status=status, cleanup=cleanup)
                runner.write_document(second, changed)
                result = coverage.summarize([second])
                self.assertEqual(result["status"], "failed")
                self.assertEqual(result["observedOperations"], 1)
                self.assertEqual(result["expectedPositiveOperations"], 0)
            malformed = copy.deepcopy(report)
            malformed["operationCoverage"]["rows"] = []
            malformed["smoke"] = {"status": "passed"}
            self.assertFalse(runner.finalize(malformed, list(runner.selected_steps("default-off")), "original-failure", True))
            self.assertEqual(malformed["failureCode"], "original-failure")
            for mutation in (lambda r: r.update(steps=[]),
                             lambda r: r["operationCoverage"]["rows"][0].update(contract="failed"),
                             lambda r: r["operationCoverage"]["rows"][0].update(outcome="unexpected")):
                changed = copy.deepcopy(other)
                mutation(changed)
                runner.write_document(second, changed)
                result = coverage.summarize([second])
                self.assertEqual(result["status"], "failed")
                self.assertEqual(result["expectedPositiveOperations"], 0)
            with patch.dict(os.environ, {"FUNCTIONAL_REPORTS": ""}, clear=True), patch("sys.stderr", new=io.StringIO()) as stderr:
                self.assertEqual(coverage.main(), 1)
                self.assertNotIn(temporary, stderr.getvalue())

    def test_profile_dispositions_and_failed_runs_do_not_hide_missing_coverage(self):
        with tempfile.TemporaryDirectory() as temporary:
            first, second = Path(temporary) / "default.json", Path(temporary) / "webhooks.json"
            default, webhooks = self.report(), self.report("webhooks")
            webhooks["runId"] = "functional-" + "f" * 12
            runner.write_document(first, default)
            summary = coverage.summarize([first])
            by_id = {row["operationId"]: row for row in summary["operations"]}
            self.assertEqual(summary["status"], "passed")
            self.assertIn("getCapabilities", summary["packagedGaps"])
            self.assertIn("getCurrentAccess", summary["packagedGaps"])
            self.assertEqual(by_id["getCurrentAccess"]["disposition"], "compose")
            self.assertFalse(by_id["getCurrentAccess"]["expectedPositive"])
            self.assertEqual(by_id["listWebhooks"]["disabledProfiles"], ["default-off"])
            self.assertNotIn("listWebhooks", summary["packagedGaps"])
            webhook_row = dict(webhooks["operationCoverage"]["rows"][0], operationId="listWebhooks")
            webhooks["operationCoverage"]["rows"] = [webhook_row]
            # A failed enabled run cannot excuse an operation as disabled, or
            # borrow a passing run's status to turn its observations into credit.
            webhooks["status"] = "failed"
            runner.write_document(second, webhooks)
            combined = coverage.summarize([first, second])
            self.assertEqual(combined["status"], "failed")
            self.assertEqual(combined["expectedPositiveOperations"], 1)
            self.assertEqual(combined["observedOperations"], 2)
            self.assertIn("listWebhooks", combined["packagedGaps"])
            with patch.dict(os.environ, {"FUNCTIONAL_REPORTS": f"{first} {second}"}, clear=True), patch("sys.stdout", new=io.StringIO()) as stdout:
                self.assertEqual(coverage.main(), 1)
                self.assertEqual(json.loads(stdout.getvalue()), combined)
            webhooks["status"] = "passed"
            runner.write_document(second, webhooks)
            combined = coverage.summarize([first, second])
            self.assertEqual(combined["expectedPositiveOperations"], 2)
            self.assertNotIn("listWebhooks", combined["packagedGaps"])
            self.assertEqual(combined["totalOperations"], summary["totalOperations"])
            # CLI success prints usable JSON; incomplete coverage is not a
            # failed workflow and must not silently change the exit convention.
            with patch.dict(os.environ, {"FUNCTIONAL_REPORTS": str(first)}, clear=True), patch("sys.stdout", new=io.StringIO()) as stdout:
                self.assertEqual(coverage.main(), 0)
                self.assertEqual(json.loads(stdout.getvalue()), summary)


if __name__ == "__main__":
    unittest.main()
