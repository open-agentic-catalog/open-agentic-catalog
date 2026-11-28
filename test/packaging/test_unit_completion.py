"""Pure native completion decisions; no process, network or application fixtures."""
import io
import json
from pathlib import Path
import sys
from tempfile import TemporaryDirectory
from types import SimpleNamespace
import unittest
from unittest.mock import patch

import completion


class CompletionUnits(unittest.TestCase):
    def events(self, *rows):
        return "\n".join(json.dumps(row) for row in rows)

    def test_native_discovery_requires_cases_from_each_selected_module(self):
        fixture = (
            "import unittest\n"
            "class Owner(unittest.TestCase):\n"
            "    def test_value(self): pass\n"
        )
        modules = {
            "test_unit_outer": "test_unit_outer.py",
            "completion_nested.test_unit_outer": "completion_nested/test_unit_outer.py",
            "completion_nested.test_unit_other": "completion_nested/test_unit_other.py",
        }
        for empty in (None, *modules):
            with self.subTest(empty=empty), TemporaryDirectory() as tmp, \
                 patch.dict(sys.modules), patch.object(sys, "path", sys.path.copy()):
                root = Path(tmp)
                (root / "completion_nested").mkdir()
                (root / "completion_nested/__init__.py").touch()
                # Native discovery skips directories that are not Python packages.
                (root / "unselected").mkdir()
                (root / "unselected/test_unit_unused.py").touch()
                for name, path in modules.items():
                    (root / path).write_text("" if name == empty else fixture)
                if empty:
                    with self.assertRaises(RuntimeError) as error:
                        completion.python_suite(root, "test_unit_*.py")
                    self.assertEqual(str(error.exception), f"A required test file has no cases: {empty}")
                else:
                    suite, count = completion.python_suite(root, "test_unit_*.py")
                    self.assertEqual(count, len(modules))
                    self.assertCountEqual(
                        [case.id() for case in completion.cases(suite)],
                        [f"{name}.Owner.test_value" for name in modules],
                    )

    def test_python_discovery_distinguishes_no_matches_from_a_broken_import(self):
        with TemporaryDirectory() as tmp, patch.dict(sys.modules), patch.object(sys, "path", sys.path.copy()):
            root = Path(tmp)
            (root / "test_unit_healthy.py").write_text(
                "import unittest\nclass Healthy(unittest.TestCase):\n    def test_ok(self): pass\n"
            )
            with self.assertRaisesRegex(RuntimeError, "No unit tests discovered"):
                completion.python_suite(root, "test_missing_*.py")

            (root / "test_unit_broken.py").write_text("raise ImportError('fixture import failed')\n")
            with self.assertRaisesRegex(RuntimeError, "Python test discovery failed") as error:
                completion.python_suite(root, "test_unit_*.py")
            self.assertIn("test_unit_broken", str(error.exception))
            self.assertIn("fixture import failed", str(error.exception))

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

    def test_go_subtests_are_bound_to_their_top_level_test_in_the_same_package(self):
        name = "TestOwner"
        child = name + "/child"
        start = {"Package": "one", "Action": "run", "Test": name}
        passed = dict(start, Action="pass")
        child_start, child_pass = dict(start, Test=child), dict(passed, Test=child)
        done = {"Package": "one", "Action": "pass"}
        required = {("one", name), ("two", name)}

        def stream(*rows):
            # The same test is active in package two throughout package one's events.
            return self.events(dict(start, Package="two"), *rows, done,
                               dict(passed, Package="two"), dict(done, Package="two"))

        completion.require_go(stream(start, child_start, child_pass, passed), required)
        for label, rows in (
            ("early", (child_start, child_pass, start, passed)),
            ("late", (start, passed, child_start, child_pass)),
        ):
            with self.subTest(label=label), self.assertRaisesRegex(RuntimeError, "top-level test.*TestOwner/child"):
                completion.require_go(stream(*rows), required)
        with self.assertRaisesRegex(RuntimeError, "child test missing or incomplete: TestOwner"):
            completion.require_go(stream(start, child_start, passed, child_pass), required)

    def test_go_command_without_json_is_rejected_before_execution(self):
        args = ["completion.py", "go", "--required", "TestOwner", "--", "go", "test", "./..."]
        with patch.object(sys, "argv", args), patch.object(completion, "run_go") as run:
            with self.assertRaisesRegex(RuntimeError, "Expected native go test -json command"):
                completion.main()
            run.assert_not_called()

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
