"""Native Search assertion calibrations, without tools, subprocesses or sockets."""

import copy
from email.message import Message
import io
import json
import unittest
from unittest.mock import patch
import urllib.error

from client_contract import NATIVE_CASES, NATIVE_FILTERS, PUBLIC, check_cases
from common import Result
import native_search as native
import qualification
from runner_test_helpers import passing_case


class NativeSearch(unittest.TestCase):
    def setUp(self):
        self.entries = [
            dict(identifier="urn:air:fixture.example:readiness:agent", displayName="Order Agent", type=native.AGENT,
                 description="Orders", tags=["orders"], metadata={"license": "Apache-2.0", "exact": 9007199254740993},
                 **{"http://purl.org/dc/terms/subject": ["developer tools"]}, data={"name": "Agent"}),
            dict(identifier="urn:air:fixture.example:readiness:mcp", displayName="Order MCP", type=native.MCP, data={"name": "MCP"}),
            dict(identifier="urn:air:fixture.example:readiness:skill", displayName="Order Skill", type='text/markdown; profile="urn:air:agent-skills"', url="https://fixture.example/skill"),
        ]
        self.baseline = self.payload(self.entries)
        self.calls = []

    @staticmethod
    def problem(status=400, body=None, media="application/json"):
        headers = Message()
        headers["Content-Type"] = media
        if body is None:
            body = {"errorCode": "INVALID_ARGUMENT", "message": native.ERROR_MESSAGE}
        return urllib.error.HTTPError(PUBLIC + "/ard/v1/search", status, "synthetic refusal", headers,
                                      io.BytesIO(json.dumps(body).encode()))

    def payload(self, entries):
        return {"results": [dict(copy.deepcopy(entry), score=93, source=PUBLIC + "/ard/v1/") for entry in entries]}

    def search(self, filters, size, token):
        self.calls.append((copy.deepcopy(filters), size, token))
        if token is not None and filters:
            raise self.problem()
        if size == 1:
            index = {None: 0, "second": 1, "third": 2}[token]
            payload = self.payload(self.entries[index:index + 1])
            if index < 2:
                payload["pageToken"] = ("second", "third")[index]
            return payload
        if native.NEGATIVE in filters.values():
            return self.payload([])
        if "data.skills.id" in filters:
            return self.payload(self.entries[:1])
        if "data.remotes.type" in filters:
            return self.payload(self.entries[1:2])
        return self.payload(self.entries)

    def qualify(self, search=None):
        return native.qualify(self.entries, self.baseline, search or self.search)

    def test_complete_inventory_conjunctions_and_token_forwarding(self):
        phases = []
        cases = native.qualify(self.entries, self.baseline, self.search, phases.append)
        self.assertEqual(check_cases(cases, NATIVE_CASES), 8)
        self.assertEqual(len(self.calls), 17)
        self.assertEqual(phases, ["native-pagination-page-1", "native-pagination-page-2", "native-pagination-page-3"])
        offset = 0
        for filters in NATIVE_FILTERS.values():
            self.assertEqual(self.calls[offset], (filters, 10, None))
            for index, field in enumerate(filters, start=1):
                actual, size, token = self.calls[offset + index]
                self.assertEqual((size, token), (10, None))
                self.assertEqual(set(actual), set(filters))
                self.assertEqual([key for key in filters if actual[key] != filters[key]], [field])
                self.assertEqual(actual[field], native.NEGATIVE)
            offset += len(filters) + 1
        self.assertEqual(self.calls[-5:], [({}, 1, None), ({}, 1, "second"), ({}, 1, "third"),
                                          ({}, 1, None), ({"metadata.license": "MIT"}, 1, "second")])

    def test_every_silently_dropped_conjunct_is_caught(self):
        for family, filters in NATIVE_FILTERS.items():
            for field in filters:
                def dropped(query, size, token):
                    if size == 10 and set(query) == set(filters) and query[field] == native.NEGATIVE:
                        return self.search(filters, size, token)
                    return self.search(query, size, token)
                with self.subTest(field=field):
                    cases = {case["name"]: case for case in self.qualify(dropped)}
                    self.assertEqual(cases[f"native-{family}-contradictions"]["status"], "fail")
                    self.assertEqual(cases[f"native-{family}-match"]["status"], "pass")

    def test_metadata_membership_source_scores_and_envelopes_are_preserved(self):
        faults = [lambda p: p["results"].pop(), lambda p: p["results"].append(p["results"][0]),
                  lambda p: p["results"][0].update(identifier="urn:air:wrong.example:one"),
                  lambda p: p["results"][0].update(url="https://unexpected.example"),
                  lambda p: p["results"][0].update(source="https://wrong.example"),
                  lambda p: p["results"][0].update(score=True), lambda p: p["results"][0].update(score=101),
                  lambda p: p.update(referrals=[{"identifier": "unexpected"}]),
                  lambda p: p.update(pageToken="unexpected"), lambda p: p.update(results=None)]
        for field in self.entries[0]:
            faults.append(lambda p, field=field: p["results"][0].update({field: None}))
        for fault in faults:
            payload = copy.deepcopy(self.baseline)
            fault(payload)
            with self.subTest(payload=payload), self.assertRaises((AssertionError, TypeError, KeyError)):
                native.validate(payload, [entry["identifier"] for entry in self.entries], self.entries)

    def test_page_faults_cannot_pass_or_loop(self):
        faults = [lambda p: p.update(results=[]), lambda p: p["results"][0].update(score=10),
                  lambda p: p.update(pageToken="second"), lambda p: p.pop("pageToken"),
                  lambda p: p.update(pageToken=""), lambda p: p["results"].append(p["results"][0]),
                  lambda p: p.update(results=self.payload(self.entries[:1])["results"])]
        for fault in faults:
            def broken(filters, size, token):
                payload = self.search(filters, size, token)
                if not filters and token == "second":
                    fault(payload)
                return payload
            with self.subTest(fault=fault):
                cases = {case["name"]: case for case in self.qualify(broken)}
                self.assertEqual(cases["native-pagination"]["status"], "fail")
                self.assertLessEqual(cases["native-pagination"]["native_calls"], 3)
                self.assertEqual(cases["native-changed-filter-token"]["status"], "pass")
        for token in ("unexpected", "", 1):
            def terminal(filters, size, previous):
                payload = self.search(filters, size, previous)
                if previous == "third":
                    payload["pageToken"] = token
                return payload
            self.assertEqual(self.qualify(terminal)[-2]["status"], "fail")
        def missing_first(filters, size, token):
            payload = self.search(filters, size, token)
            if size == 1 and token is None:
                payload.pop("pageToken")
            return payload
        self.assertEqual([case["status"] for case in self.qualify(missing_first)[-2:]], ["fail", "fail"])

    def test_changed_filter_requires_the_actual_http_problem_and_closes_body(self):
        failures = [self.problem(status=401), self.problem(body={"errorCode": "INTERNAL", "message": native.ERROR_MESSAGE}),
                    self.problem(body={"errorCode": "INVALID_ARGUMENT", "message": "different error"}),
                    self.problem(media="text/html"), ValueError("decoder failure")]
        for error in failures:
            def broken(filters, size, token):
                if filters and token:
                    raise error
                return self.search(filters, size, token)
            with self.subTest(error=error):
                self.assertEqual(self.qualify(broken)[-1]["status"], "fail")
                if isinstance(error, urllib.error.HTTPError):
                    self.assertTrue(error.closed)
        def accepted(filters, size, token):
            return self.payload([]) if filters and token else self.search(filters, size, token)
        self.assertEqual(self.qualify(accepted)[-1]["status"], "fail")
        for body in ("not an ARD object", {"errorCode": "INVALID_ARGUMENT", "message": "x" * 8193}):
            error = self.problem(body=body)
            def oversized(filters, size, token):
                if filters and token:
                    raise error
                return self.search(filters, size, token)
            self.assertEqual(self.qualify(oversized)[-1]["status"], "fail")
            self.assertTrue(error.closed)

    def test_child_inventory_exit_status_timeout_and_redaction(self):
        cases = [passing_case(name) for name in NATIVE_CASES]
        for code, payload, success in [(0, {"cases": cases}, True), (1, {"cases": cases}, False),
                                       (0, {"cases": cases[:-1]}, False), (0, {}, False)]:
            with patch.object(qualification, "run", return_value=Result(code, json.dumps(payload), "")) as run:
                actual = qualification.child_results("native_search.py", NATIVE_CASES)
            self.assertEqual(all(case["status"] == "pass" for case in actual), success)
            self.assertEqual(run.call_args.kwargs["timeout"], 180)
        with patch.object(qualification, "run", side_effect=RuntimeError("local-reader timeout")):
            actual = qualification.child_results("native_search.py", NATIVE_CASES)
        self.assertEqual(len(actual), 8)
        self.assertNotIn("local-reader", json.dumps(actual))

    def test_invalid_smoke_fixture_stops_before_native_calls(self):
        duplicate = [self.entries[0], self.entries[0], self.entries[2]]
        missing_kind = [dict(self.entries[0], type="application/json"), *self.entries[1:]]
        for entries in (self.entries[:2], duplicate, missing_kind):
            with self.subTest(entries=entries), patch.object(self, "search") as search:
                with self.assertRaisesRegex(AssertionError, "smoke"):
                    native.qualify(entries, self.payload(entries), search)
                search.assert_not_called()
        baseline = copy.deepcopy(self.baseline)
        baseline["results"][0]["metadata"]["license"] = "changed"
        with patch.object(self, "search") as search, self.assertRaisesRegex(AssertionError, "changed metadata"):
            native.qualify(self.entries, baseline, search)
        search.assert_not_called()

    def test_progress_write_failure_rejects_traversal_without_skipping_later_outcomes(self):
        def failed_progress(_phase):
            raise PermissionError("local-reader cannot write progress")
        cases = native.qualify(self.entries, self.baseline, self.search, failed_progress)
        self.assertEqual(cases[-2]["status"], "fail")
        self.assertEqual(cases[-2]["native_calls"], 0)
        self.assertIn("cannot write progress", cases[-2]["detail"])
        self.assertNotIn("local-reader", json.dumps(cases))
        self.assertTrue(all(case["status"] == "pass" for case in [*cases[:6], cases[-1]]))

    def test_unexpected_filter_http_failures_close_bodies_and_preserve_remaining_cases(self):
        errors = []
        def failing(filters, size, token):
            if size == 10:
                error = self.problem(status=503)
                errors.append(error)
                raise error
            return self.search(filters, size, token)
        cases = self.qualify(failing)
        self.assertTrue(all(case["status"] == "fail" for case in cases[:6]))
        self.assertEqual([case["status"] for case in cases[-2:]], ["pass", "pass"])
        self.assertTrue(all(error.closed for error in errors))

    def test_contradictory_report_fields_cannot_pass(self):
        for case in self.qualify():
            for field in (key for key in case if key not in ("name", "status", "disposition")):
                invalid = dict(case, **{field: None})
                with self.subTest(name=case["name"], field=field), self.assertRaises(RuntimeError):
                    check_cases([invalid if item["name"] == case["name"] else item for item in self.qualify()], NATIVE_CASES)
        cases = self.qualify()
        cases[0]["result_counts"] = [True]
        with self.assertRaises(RuntimeError):
            check_cases(cases, NATIVE_CASES)
