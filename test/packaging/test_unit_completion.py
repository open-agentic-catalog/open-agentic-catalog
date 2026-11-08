"""Pure native completion decisions; no process, network or application fixtures."""
import io
import json
from pathlib import Path
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import completion


class CompletionUnits(unittest.TestCase):
    def events(self, *rows):
        return "\n".join(json.dumps(row) for row in rows)

    def test_each_required_go_name_needs_start_success_and_package_completion(self):
        rows = [{"Action": action, "Package": "fixture", **({"Test": name} if name else {})}
                for action, name in (("run", "TestOne"), ("pass", "TestOne"),
                                     ("run", "TestTwo"), ("pass", "TestTwo"), ("pass", None))]
        required = {(None, "TestOne"), (None, "TestTwo")}
        completion.require_go(self.events(*rows), required)
        for index in range(len(rows)):
            with self.subTest(removed=index), self.assertRaisesRegex(RuntimeError, "missing or incomplete"):
                completion.require_go(self.events(*(row for i, row in enumerate(rows) if i != index)), required)
        for outcome in ("skip", "fail"):
            altered = [dict(row, Action=outcome) if row.get("Test") == "TestTwo" and row["Action"] == "pass" else row for row in rows]
            with self.assertRaisesRegex(RuntimeError, "failed or skipped"):
                completion.require_go(self.events(*altered), required)
        with self.assertRaisesRegex(RuntimeError, "No required"):
            completion.require_go(self.events(*rows), set())

    def test_discovery_keeps_native_package_identity_not_output_lookalikes(self):
        raw = self.events({"Package": "one", "Output": "TestOne\n"},
                          {"Package": "two", "Output": "TestOne\n"},
                          {"Package": "one", "Output": "BenchmarkOne\n"},
                          {"Package": "one", "Output": "ok one 0.1s\n"})
        self.assertEqual(completion.go_discovery(raw), {("one", "TestOne"), ("two", "TestOne")})
        with self.assertRaisesRegex(RuntimeError, "No required"):
            completion.go_discovery(self.events({"Package": "one", "Output": "ok one\n"}))
        with self.assertRaises(ValueError):
            completion.require_go('{"Action":', {(None, "TestOne")})

    def test_required_python_completion_refuses_skips_and_partial_run(self):
        for success, skipped, count, expected in ((True, [], 2, []), (True, [("case", "reason")], 2, []),
                                                 (True, [], 1, []), (False, [], 2, []), (True, [], 2, [("case", "expected failure")])):
            result = SimpleNamespace(wasSuccessful=lambda: success, skipped=skipped, testsRun=count,
                                     expectedFailures=expected, unexpectedSuccesses=[])
            with patch.object(completion, "python_suite", return_value=(object(), 2)), \
                 patch.object(completion.unittest, "TextTestRunner") as runner:
                runner.return_value.run.return_value = result
                if success and not skipped and count == 2 and not expected:
                    completion.run_python(Path("fixture"), "test_unit_*.py")
                else:
                    with self.assertRaisesRegex(RuntimeError, "failed, skipped or did not complete"):
                        completion.run_python(Path("fixture"), "test_unit_*.py")

    def test_go_duplicate_contradictory_and_incomplete_children_refuse_completion(self):
        def event(action, name=None):
            return {"Action":action,"Package":"fixture", **({"Test":name} if name else {})}
        run, passed, done = event("run", "TestOne"), event("pass", "TestOne"), event("pass")
        for rows in ((passed, run, done), (done, run, passed), (run, passed, passed, done),
                     (run, run, passed, done), (run, passed, done, done),
                     (run, event("run", "TestOne/child"), passed, done),
                     (run, event("run", "TestOne/child"), event("skip", "TestOne/child"), passed, done)):
            with self.subTest(rows=rows), self.assertRaisesRegex(RuntimeError, "Required Go"):
                completion.require_go(self.events(*rows), {(None,"TestOne")})
        completion.require_go(self.events(run, event("run", "TestOne/child"), event("pass", "TestOne/child"), passed, done), {(None,"TestOne")})

    def test_nested_suite_cases_preserve_native_case_identities(self):
        first, second = unittest.FunctionTestCase(lambda: None), unittest.FunctionTestCase(lambda: None)
        self.assertEqual(list(completion.cases(unittest.TestSuite([first, unittest.TestSuite([second])]))), [first, second])

    def test_go_process_failure_preserves_native_diagnostics_and_refuses_admission(self):
        result = SimpleNamespace(returncode=1, stdout="native stdout", stderr="native stderr")
        with patch.object(completion.subprocess, "run", return_value=result), \
             patch.object(completion, "require_go") as admit, \
             patch.object(completion.sys, "stdout", io.StringIO()) as stdout, \
             patch.object(completion.sys, "stderr", io.StringIO()) as stderr:
            with self.assertRaisesRegex(RuntimeError, "selection failed"):
                completion.run_go(["go", "test", "-json"], {(None, "TestOne")})
            self.assertEqual(stdout.getvalue(), "native stdout")
            self.assertEqual(stderr.getvalue(), "native stderr")
            admit.assert_not_called()
