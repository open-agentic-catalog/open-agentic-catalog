"""Pure assertion/calibration checks; no HF imports, sockets or processes."""

import copy
import contextlib
import io
import json
import unittest
from unittest.mock import patch

import client_controls as controls
from client_contract import CONTROL_CASES, DISPOSITIONS, NAVIGATION_CASES, check_cases, redact
from common import Result
import navigation_qualification as nav
import qualification as q
import runner
from runner_test_helpers import passing_case


class Navigation(unittest.TestCase):
    def setUp(self):
        self.current = [
            dict(identifier="urn:air:fixture.example:readiness:agent", displayName="Order Agent", type="application/a2a-agent-card+json", data={"name": "Agent"}),
            dict(identifier="urn:air:fixture.example:readiness:mcp", displayName="Order MCP", type=nav.MCP, data={"name": "MCP"}),
            dict(identifier="urn:air:fixture.example:readiness:skill", displayName="Order Skill", type='text/markdown; profile="urn:air:agent-skills"', url="https://fixture.example/skill"),
        ]
        self.predecessor = copy.deepcopy(self.current)
        self.predecessor[2]["type"] = "application/agent-skills+md"
        self.registry = {"results": [dict(entry, score=90, source=nav.PUBLIC + "/ard/v1/") for entry in self.current]}
        self.cases = {case["name"]: case for case in nav.cases(self.current)}

    def payload(self, name):
        case = self.cases[name]
        manifest = case["url"] if case["url"].endswith(".json") else case["url"] + "/.well-known/ai-catalog.json"
        discovery = [dict(kind="catalog", url=manifest, status="error" if case.get("private") else "ok",
                          detail="HTTP 401" if case.get("private") else "")]
        if case.get("registry"):
            discovery.append(dict(kind="registry", url=nav.PUBLIC + "/ard/v1/search", status="ok", detail=""))
        entries = self.current if case.get("registry") else self.predecessor
        return {"discovered": discovery, "results": [dict(entry, score=90 if case.get("registry") else 50,
                source=nav.PUBLIC + "/ard/v1/search" if case.get("registry") else manifest)
                for entry in entries if entry["identifier"] in case["expected"]]}

    def validate(self, name, payload, **changes):
        entries = self.current if self.cases[name].get("registry") else self.predecessor
        return nav.validate(self.cases[name], Result(0, json.dumps(payload), ""), entries,
                            changes.get("registry", self.registry), self.current)

    def test_all_nine_expected_outcomes_and_distinct_profiles(self):
        self.assertEqual(set(self.cases), set(NAVIGATION_CASES))
        for name in self.cases:
            with self.subTest(name=name):
                self.validate(name, self.payload(name))
        with self.assertRaisesRegex(AssertionError, "type"):
            nav.validate(self.cases["navigate-origin"], Result(0, json.dumps(self.payload("navigate-origin")), ""),
                         self.current, self.registry, self.current)

    def test_result_and_discovery_faults_are_detected(self):
        faults = [lambda p: p["results"].pop(), lambda p: p["results"].append(p["results"][0]),
                  lambda p: p["results"][0].update(identifier="urn:air:other.example:agent:wrong"),
                  lambda p: p["results"][0].update(displayName="wrong"),
                  lambda p: p["results"][0].update(type="wrong"),
                  lambda p: p["results"][0].update(data={}),
                  lambda p: p["results"][0].update(url="https://wrong.example"),
                  lambda p: p["results"][0].update(source=nav.PUBLIC + "/ard/v1/"),
                  lambda p: p["results"][0].update(score=50),
                  lambda p: p["results"][0].update(score=True),
                  lambda p: p["discovered"].pop(),
                  lambda p: p["discovered"][1].update(status="error", detail="HTTP 500"),
                  lambda p: p["discovered"][1].update(url="https://other.example/search"),
                  lambda p: p.update(referrals=[{"url": "https://other.example"}]),
                  lambda p: p.update(pageToken="unexpected")]
        for index, fault in enumerate(faults):
            payload = self.payload("navigate-current")
            fault(payload)
            with self.subTest(fault=index), self.assertRaises((AssertionError, KeyError)):
                self.validate("navigate-current", payload)
        weak = copy.deepcopy(self.registry)
        weak["results"][0]["score"] = 50
        with self.assertRaisesRegex(AssertionError, "beat static"):
            self.validate("navigate-current", self.payload("navigate-current"), registry=weak)

    def test_private_success_empty_errors_and_leaks_are_not_accepted(self):
        for name in ("navigate-private-missing", "navigate-private-reader", "navigate-private-current"):
            for fault in (lambda p: p.update(discovered=[]),
                          lambda p: p["discovered"][0].update(status="ok", detail=""),
                          lambda p: p["discovered"][0].update(detail="HTTP 403"),
                          lambda p: p.update(results=[self.current[0]])):
                payload = self.payload(name)
                fault(payload)
                with self.subTest(name=name), self.assertRaises(AssertionError):
                    self.validate(name, payload)
        with self.assertRaisesRegex(AssertionError, "leaked"):
            nav.validate(self.cases["navigate-private-reader"], Result(0, json.dumps(self.payload("navigate-private-reader")), "Order Agent"),
                         self.predecessor, self.registry, self.current)

    def test_empty_cases_require_an_explicit_results_array(self):
        for name, case in self.cases.items():
            if case["expected"]:
                continue
            for fault in (lambda p: p.pop("results"), lambda p: p.update(results=None),
                          lambda p: p.update(results={}), lambda p: p.update(results="")):
                payload = self.payload(name)
                fault(payload)
                with self.subTest(name=name, payload=payload), self.assertRaises((AssertionError, KeyError)):
                    self.validate(name, payload)

    def test_command_is_explicit_bounded_and_continues_on_failure(self):
        with patch.object(nav, "run", return_value=Result(2, "", "local-reader failure")) as run:
            results = nav.qualify(self.current, self.predecessor, self.current, self.registry)
        self.assertEqual(len(results), 9)
        self.assertTrue(all(item["status"] == "fail" for item in results))
        for call in run.call_args_list:
            argv = call.args[0]
            self.assertTrue(argv[2].startswith((nav.PUBLIC, nav.PRIVATE)))
            self.assertIn("--no-follow-referrals", argv)
            self.assertEqual(call.kwargs["timeout"], 30)
        self.assertNotIn("local-reader", json.dumps(results))


class ReportsAndControls(unittest.TestCase):
    def test_inventory_and_report_rejections(self):
        cases = [passing_case(name) for name in DISPOSITIONS]
        self.assertEqual(len(cases), 35)
        self.assertEqual(check_cases(cases, DISPOSITIONS), 35)
        for invalid in ([], cases[:-1], [*cases[:-1], cases[0]], [None, *cases[1:]],
                        [dict(cases[0], name=[]), *cases[1:]],
                        [dict(cases[0], status="skipped"), *cases[1:]],
                        [dict(cases[0], disposition="unsupported"), *cases[1:]]):
            with self.subTest(invalid=invalid), self.assertRaises(RuntimeError):
                check_cases(invalid, DISPOSITIONS)
        cases = [dict(passing_case(name), http_status=200) if name in NAVIGATION_CASES else passing_case(name) for name in DISPOSITIONS]
        with self.assertRaisesRegex(RuntimeError, "diagnostics"):
            check_cases(cases, DISPOSITIONS)

    def test_control_subprocess_report_and_redaction(self):
        cases = [passing_case(name) for name in CONTROL_CASES]
        for code, raw in [(0, json.dumps({"cases": cases})), (1, json.dumps({"cases": cases})),
                          (0, "{}"), (0, "not-json"), (2, "{}")]:
            with patch.object(q, "run", return_value=Result(code, raw, "")):
                results = q.control_results()
            self.assertEqual(len(results), 5)
            self.assertEqual(all(item["status"] == "pass" for item in results), code == 0 and raw.startswith('{"cases"'))
        with patch.object(q, "run", side_effect=RuntimeError("local-reader timed out")):
            results = q.control_results()
        self.assertNotIn("local-reader", json.dumps(results))
        self.assertEqual(redact("local-reader"), "[test-token]")

    def test_preflight_rejects_missing_capability(self):
        options = "--max-depth --max-registries --max-per-source --timeout --token --json"
        for help_text, imports, success in [(options, "navigation-capabilities-ok", True),
                                          ("search only", "navigation-capabilities-ok", False),
                                          (options, "wrong", False)]:
            with patch.object(runner, "installation_probe", side_effect=[Result(0, help_text, ""), Result(0, imports, "")]):
                if success:
                    runner.navigation_preflight("owned-image")
                else:
                    with self.assertRaises(RuntimeError):
                        runner.navigation_preflight("owned-image")
        with patch.object(runner, "local_daemon"), patch.object(runner, "installed_image", return_value={"image": "owned"}), patch.object(runner, "navigation_preflight", side_effect=RuntimeError("missing command")), patch.object(runner, "fingerprint") as fingerprint:
            with self.assertRaises(RuntimeError):
                runner.check()
            fingerprint.assert_not_called()

    def test_control_assertions_and_continue_after_arbitrary_failure(self):
        payload = {"results": [dict(controls.FULL)]}
        controls.validate_full(payload)
        for field in controls.FULL:
            invalid = copy.deepcopy(payload)
            invalid["results"][0][field] = None
            with self.subTest(field=field), self.assertRaises(AssertionError):
                controls.validate_full(invalid)
        output = io.StringIO()
        with patch.object(controls, "decode_control", side_effect=RuntimeError("local-reader transport failure")), patch.object(controls, "credential_control", return_value={"checked": True}), contextlib.redirect_stdout(output):
            self.assertEqual(controls.qualify(), 1)
        cases = json.loads(output.getvalue())["cases"]
        self.assertEqual([case["status"] for case in cases], ["fail"] * 4 + ["pass"])
        self.assertNotIn("local-reader", output.getvalue())

    def test_typed_rejection_and_report_evidence(self):
        class ExpectedValidationError(ValueError):
            pass

        controls.validate_sparse(ExpectedValidationError("missing displayName and type"), ExpectedValidationError)
        for error in (None, RuntimeError("missing displayName and type"), ExpectedValidationError("HTTP 401"),
                      ExpectedValidationError("missing displayName")):
            with self.subTest(error=error), self.assertRaises(AssertionError):
                controls.validate_sparse(error, ExpectedValidationError)
        for name, changes in [("decode-search-sparse", {"accepted": True}),
                              ("decode-navigate-sparse", {"decoder_error": "TimeoutError"}),
                              ("decode-search-full", {"http_status": 500}),
                              ("decode-search-full", {"http_requests": True}),
                              ("navigate-private-reader", {"result_count": 1}),
                              ("navigate-current", {"discovered": []}),
                              ("navigate-current", {"discovered": passing_case("navigate-current")["discovered"][:1]}),
                              ("navigate-origin", {"discovered": [{"status": "ok"}]}),
                              ("navigate-origin", {"discovered": [dict(passing_case("navigate-origin")["discovered"][0], url="https://other.example")]}),
                              ("navigate-current", {"result_count": 0}),
                              ("navigate-origin", {"result_count": 1}),
                              ("navigate-mcp", {"result_count": 3}),
                              ("navigate-mcp", {"result_count": True}),
                              ("navigate-empty", {"result_count": 1}),
                              ("navigate-skill-kind", {"result_count": 1}),
                              ("navigate-credential-source", {"manifest_authorized": True}),
                              ("navigate-credential-source", {"other_owned_origin_registry_authorized": False}),
                              ("navigate-credential-source", {"source_replaced_with_queried_endpoint": False})]:
            with self.subTest(name=name, changes=changes), self.assertRaises(RuntimeError):
                check_cases([dict(passing_case(name), **changes)], [name])
