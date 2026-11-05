"""Report and exception validation uses only local values, never an analyzer."""

import copy
import json
import unittest

import dead_code


MODULE = "example.com/catalog"


def finding(symbol="Unused"):
    return [{"Name": "lib", "Path": MODULE + "/lib", "Funcs": [{
        "Name": symbol, "Position": {"File": "lib/code.go", "Line": 7, "Col": 2},
        "Generated": False, "Marker": False,
    }]}]


def exception():
    return {"package": MODULE + "/lib", "symbol": "Unused", "scopes": ["production-amd64", "production-arm64"], "reason": "Shared contract inventory used across package tests."}


class DeadCodePolicyTests(unittest.TestCase):
    def test_clean_and_finding_reports(self):
        self.assertEqual(dead_code.parse_findings("null\n", MODULE), {})
        self.assertEqual(dead_code.parse_findings("[]\n", MODULE), {})
        self.assertEqual(dead_code.parse_findings(json.dumps(finding()), MODULE), {(MODULE + "/lib", "Unused"): "lib/code.go:7:2"})

    def test_invalid_reports_do_not_mean_clean(self):
        invalid = ["", " ", "nul", "null trailing", "{}", "false", "[null]", "[{}]", '{"a":1,"a":2}']
        for raw in invalid:
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                dead_code.parse_findings(raw, MODULE)
        cases = []
        for key, value in [("Name", ""), ("Path", "other/lib"), ("Path", MODULE + "/../lib"), ("Funcs", []), ("Funcs", {})]:
            data = finding()
            data[0][key] = value
            cases.append(data)
        for key, value in [("Name", ""), ("Name", []), ("Position", None), ("Generated", True), ("Marker", 0)]:
            data = finding()
            data[0]["Funcs"][0][key] = value
            cases.append(data)
        for key, value in [("File", "/private/code.go"), ("File", "../code.go"), ("File", "code.txt"), ("Line", 0), ("Line", True), ("Col", "2")]:
            data = finding()
            data[0]["Funcs"][0]["Position"][key] = value
            cases.append(data)
        data = finding()
        data[0]["Funcs"].append(copy.deepcopy(data[0]["Funcs"][0]))
        cases.extend([data, finding() + finding()])
        for data in cases:
            with self.subTest(data=data), self.assertRaises(ValueError):
                dead_code.parse_findings(json.dumps(data), MODULE)

    def test_exceptions_are_exact_scoped_and_not_a_hidden_baseline(self):
        entries = dead_code.parse_exceptions(json.dumps([exception()]), MODULE)
        reports = {scope[0]: {} for scope in dead_code.SCOPES}
        found = dead_code.parse_findings(json.dumps(finding()), MODULE)
        reports["production-amd64"] = found
        reports["production-arm64"] = found
        output, failures = dead_code.review(reports, entries)
        self.assertEqual(failures, 0)
        self.assertEqual(output.count("[retained:"), 2)
        self.assertIn(exception()["reason"], output)
        # Test reachability cannot waive a production finding or a stale scope.
        reports["production-arm64"] = {}
        reports["unit"] = found
        output, failures = dead_code.review(reports, entries)
        self.assertEqual(failures, 2)
        self.assertIn("stale exception: production-arm64", output)
        self.assertIn("[unreviewed]", output)
        reordered = dict(reversed(list(reports.items())))
        self.assertEqual(dead_code.review(reordered, entries), (output, failures))
        del reports["combined"]
        with self.assertRaisesRegex(ValueError, "incomplete"):
            dead_code.review(reports, entries)

    def test_invalid_duplicate_and_unknown_exceptions_fail(self):
        cases = [None, {}, [None], [exception(), exception()]]
        for key, value in [("package", "elsewhere/lib"), ("symbol", "*"), ("reason", " "), ("scopes", []), ("scopes", ["missing"]), ("scopes", [None]), ("scopes", ["unit", "unit"]), ("extra", True)]:
            entry = exception()
            entry[key] = value
            cases.append([entry])
        for data in cases:
            with self.subTest(data=data), self.assertRaises(ValueError):
                dead_code.parse_exceptions(json.dumps(data), MODULE)

    def test_discovery_requires_the_module_and_exact_shipped_mains(self):
        rows = [f"{MODULE}/{main}\tmain\t{MODULE}" for main in sorted(dead_code.MAINS)]
        rows.append(f"{MODULE}/orphan\torphan\t{MODULE}")
        valid = "\n".join(rows)
        self.assertEqual(len(dead_code.discover(valid, MODULE, True)), 4)
        self.assertEqual(len(dead_code.discover(rows[-1], MODULE, False)), 1)
        invalid = ["", valid + "\n" + rows[-1], "\n".join(rows[1:]), valid.replace("\t" + MODULE, "\tnested/module"), valid + f"\n{MODULE}/examples/tool\tmain\t{MODULE}"]
        for raw in invalid:
            with self.subTest(raw=raw), self.assertRaises(ValueError):
                dead_code.discover(raw, MODULE, True)
