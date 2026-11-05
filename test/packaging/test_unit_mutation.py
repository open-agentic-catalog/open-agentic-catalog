"""Pure checks for mutation event classification, identities and configuration."""

import copy
import json
from pathlib import Path
import tempfile
import unittest

import mutation as m
from test_unit_property import events as property_events, PACKAGE as PROPERTY_PACKAGE, NAME as PROPERTY_NAME

PACKAGE = "example.com/policy"
SOURCE = "package policy\nfunc Allowed(n int) bool { return n >= 5 }\n"


def events(actions):
    return b"".join((json.dumps(dict(Package=PACKAGE, **e)) + "\n").encode() for e in actions)


def test_events(result="pass", output=""):
    return events([{"Action": "start"}, {"Action": "run", "Test": "TestPolicy"},
                   {"Action": "output", "Output": output}, {"Action": result, "Test": "TestPolicy"},
                   {"Action": "fail" if result == "fail" else "pass"}])


def report_fixture(bucket="escaped"):
    row = {"checksum": "a" * 32, "mutator": {"mutatorName": "expression/comparison",
           "originalFilePath": "policy.go", "originalStartLine": 2},
           "diff": "--- Original\n+++ New\n@@ -2 +2 @@\n-old\n+new\n", "processOutput": ""}
    data = {"sources": {"policy.go": SOURCE}, "stats": {"totalMutantsCount": 1}}
    for key, field in [("killed", "killed"), ("escaped", "escaped"), ("skipped", "skipped"), ("errored", "error"), ("notCovered", "notCovered")]:
        data[key] = [row] if key == bucket else []
        data["stats"][field + "Count"] = int(key == bucket)
    record = dict(file="policy.go", sourceSHA256=m.sha(SOURCE.encode()), mutantSHA256="b" * 64,
                  diffSHA256=m.sha(row["diff"].encode()), status="unassessed", failedTests=[])
    return data, record


class MutationEventsTests(unittest.TestCase):
    def test_rapid_completion_and_invalid_generators_are_not_mutation_catches(self):
        self.assertEqual(m.classify(property_events(), PROPERTY_PACKAGE, 0, property_tests=[PROPERTY_NAME]), ("survived", []))
        self.assertEqual(m.classify(property_events(20), PROPERTY_PACKAGE, 0, property_tests=[PROPERTY_NAME])[0], "error")
        for message in ("[rapid] only generated 0 valid tests from 1000 total", "[rapid] ignoring fail file: missing",
                        "[rapid] flaky test, can not reproduce a failure"):
            self.assertEqual(m.classify(property_events(terminal="fail", message=message), PROPERTY_PACKAGE, 1,
                                        property_tests=[PROPERTY_NAME])[0], "error")
        self.assertEqual(m.classify(property_events(terminal="fail", message="[rapid] failed after 2 tests: wrong result"),
                                    PROPERTY_PACKAGE, 1, property_tests=[PROPERTY_NAME])[0], "caught")

    def test_passing_baseline_catch_survivor_and_panic(self):
        self.assertEqual(m.classify(test_events(), PACKAGE, 0, baseline=True), ("passed", []))
        self.assertEqual(m.classify(test_events(), PACKAGE, 0), ("survived", []))
        for output in ("assertion failed", "panic: mutation broke invariant"):
            self.assertEqual(m.classify(test_events("fail", output), PACKAGE, 1), ("caught", ["TestPolicy"]))
        self.assertEqual(m.classify(test_events("fail"), PACKAGE, 1, baseline=True)[0], "error")

    def test_timeouts_signals_and_skips_are_not_catches(self):
        for output, status in [("panic: test timed out after 10s", "timeout"), ("signal: killed\n", "error"),
                               ("panic: test timed out after 10s\nsignal: killed\n", "error")]:
            self.assertEqual(m.classify(test_events("fail", output), PACKAGE, 1)[0], status)
        self.assertEqual(m.classify(test_events("skip"), PACKAGE, 0, baseline=True)[0], "error")
        self.assertEqual(m.classify(test_events("skip"), PACKAGE, 0)[0], "survived")
        self.assertEqual(m.classify(test_events(), PACKAGE, -9)[0], "error")

    def test_build_and_launch_failure_never_count_as_test_failure(self):
        raw = b'{"Action":"build-fail","ImportPath":"example.com/policy"}\n'
        self.assertEqual(m.classify(raw, PACKAGE, 1)[0], "error")
        for output in ("missing executor", "panic: init failed"):
            raw = events([{"Action": "start"}, {"Action": "output", "Output": output}, {"Action": "fail"}])
            self.assertEqual(m.classify(raw, PACKAGE, 1)[0], "error")

    def test_test_events_before_package_start_never_count_as_results(self):
        for result, code in (("pass", 0), ("fail", 1)):
            raw = events([{"Action": "run", "Test": "TestPolicy"},
                          {"Action": result, "Test": "TestPolicy"},
                          {"Action": "start"}, {"Action": result}])
            for baseline in (False, True):
                with self.subTest(result=result, baseline=baseline), self.assertRaises(ValueError):
                    m.classify(raw, PACKAGE, code, baseline=baseline)

    def test_malformed_partial_or_wrong_package_events_fail(self):
        raw = test_events()
        for invalid in (b"", raw[:-1], b"{}\n", raw.replace(PACKAGE.encode(), b"wrong"),
                        raw + events([{"Action": "pass"}]), raw + events([{"Action": "output", "Output": "late"}]), raw.replace(b'"run"', b'"unknown"'),
                        events([{"Action": "start"}, {"Action": "fail", "Test": "never-ran"}, {"Action": "fail"}]),
                        b'{"Action":"start","Action":"pass"}\n'):
            with self.subTest(raw=invalid), self.assertRaises(ValueError):
                m.classify(invalid, PACKAGE, 0)
        self.assertEqual(m.classify(events([{"Action": "start"}, {"Action": "run", "Test": "TestPolicy"}, {"Action": "pass"}]), PACKAGE, 0)[0], "error")


class MutationReportTests(unittest.TestCase):
    def test_selected_sources_reject_files_outside_discovery_and_generated_code(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for package, files in m.SCOPES.items():
                with self.subTest(package=package):
                    for name in files:
                        path = root / name
                        path.parent.mkdir(parents=True, exist_ok=True)
                        path.write_text(SOURCE)
                    listed = dict(ImportPath="example.com/" + package[2:],
                                  GoFiles=[Path(f).name for f in files], TestGoFiles=["separately_named_test.go"])
                    self.assertEqual(m.selected_sources(listed, files, root), dict.fromkeys(files, SOURCE))
                    for changed in (dict(GoFiles=[]), dict(TestGoFiles=[]), dict(CgoFiles=["cgo.go"]),
                                    dict(Incomplete=True)):
                        with self.assertRaises(ValueError):
                            m.selected_sources(dict(listed, **changed), files, root)
                    (root / files[0]).write_text("// Code generated by fixture. DO NOT EDIT.\n" + SOURCE)
                    with self.assertRaises(ValueError):
                        m.selected_sources(listed, files, root)

    def test_inventory_to_outcome_binds_native_and_hook_evidence(self):
        data, record = report_fixture()
        original = m.reconcile(data, [record], {"policy.go": SOURCE})
        for bucket, status in [("killed", "caught"), ("escaped", "survived")]:
            data, record = report_fixture(bucket)
            record.update(status=status, failedTests=["TestPolicy"] if status == "caught" else [])
            result = m.reconcile(data, [record], {"policy.go": SOURCE}, original)
            self.assertEqual(next(iter(result.values()))["status"], status)

    def test_missing_duplicate_unknown_and_changed_evidence_fails(self):
        data, record = report_fixture()
        original = m.reconcile(data, [record], {"policy.go": SOURCE})
        record["status"] = "survived"
        cases = [(data, []), (data, [record, record])]
        for change in [dict(file="other.go"), dict(status="caught"), dict(mutantSHA256="c" * 64),
                       dict(sourceSHA256="0" * 64), dict(diffSHA256="d" * 64)]:
            cases.append((data, [dict(record, **change)]))
        for native, hooks in cases:
            with self.subTest(hooks=hooks), self.assertRaises(ValueError):
                m.reconcile(native, hooks, {"policy.go": SOURCE}, original)
        for change in (lambda d: d["sources"].clear(), lambda d: d["sources"].update({"extra.go": SOURCE}),
                       lambda d: d["stats"].update(totalMutantsCount=2),
                       lambda d: d["escaped"][0].update(checksum="c" * 32),
                       lambda d: d["escaped"][0]["mutator"].update(originalStartLine=0),
                       lambda d: d["escaped"][0]["mutator"].update(mutatorName="statement/remove")):
            broken = copy.deepcopy(data)
            change(broken)
            with self.assertRaises(ValueError):
                m.reconcile(broken, [record], {"policy.go": SOURCE}, original)

    def test_errors_uncovered_unexplained_skips_and_zero_assessed_fail(self):
        data, record = report_fixture()
        original = m.reconcile(data, [record], {"policy.go": SOURCE})
        for bucket, status in [("errored", "error"), ("notCovered", "uncovered"), ("skipped", "timeout")]:
            data, record = report_fixture(bucket)
            record["status"] = status
            with self.assertRaises(ValueError):
                m.reconcile(data, [record], {"policy.go": SOURCE}, original)
        self.assertRaises(ValueError, m.decode, '{"x":1,"x":2}')
        self.assertRaises(ValueError, m.decode, '{"x":NaN}')

    def test_environment_removes_test_runtime_and_discovery_overrides(self):
        env = m.environment({"PATH": "/bin", "HOME": "/home/test", "GOPROXY": "https://proxy.golang.org",
            "GOFLAGS": "-run=none -tags=integration", "GOWORK": "evil", "GOPACKAGESDRIVER": "evil",
            "GIT_DIR": "elsewhere", "GODEBUG": "evil", "GOROOT": "evil", "GOOS": "other",
            "PYTHONOPTIMIZE": "2", "PYTHONPATH": "evil", "CATALOG_PROCESS_COVERAGE_DIR": "/tmp/coverage",
            "DATABASE_URL": "private"}, "go1.27.0")
        for key in ("GIT_DIR", "GODEBUG", "GOROOT", "GOOS", "PYTHONPATH", "DATABASE_URL", "CATALOG_PROCESS_COVERAGE_DIR"):
            self.assertNotIn(key, env)
        self.assertEqual(env["GOFLAGS"], "-mod=readonly")
        self.assertEqual(env["PYTHONOPTIMIZE"], "0")
        self.assertEqual(env["GOWORK"], "off")
        self.assertEqual(env["GOPACKAGESDRIVER"], "off")
