import copy
import json
from pathlib import Path
import unittest

import queries


class QueryContractTests(unittest.TestCase):
    def setUp(self):
        self.dashboard = json.loads((Path(__file__).resolve().parents[2] / "deployments/monitoring/dashboards/catalog.json").read_text())

    def test_fixture_uses_actual_expressions_and_all_panels(self):
        document = queries.fixture(self.dashboard)
        expected = {queries.expression(panel["targets"][0]["expr"]) for panel in self.dashboard["panels"]}
        actual = {case["expr"] for group in document["tests"] for case in group["promql_expr_test"]}
        self.assertTrue(expected <= actual)
        self.assertEqual(32, len(expected))
        self.assertEqual({"datasource", "deployment", "role"}, {item["name"] for item in self.dashboard["templating"]["list"]})
        for panel in self.dashboard["panels"]:
            self.assertEqual("${datasource}", panel["datasource"]["uid"])
            self.assertIn("${deployment:regex}", panel["targets"][0]["expr"])
            self.assertIn("${role:regex}", panel["targets"][0]["expr"])
            self.assertNotIn("or vector(0)", panel["targets"][0]["expr"])

    def test_expression_rejects_unknown_or_unbounded_variables(self):
        for raw, deployment, role in [("$unknown", "local", "api"), ("up", ".*", "api"), ("up", "local", ".*")]:
            with self.assertRaises(ValueError):
                queries.expression(raw, deployment, role)
        changed = copy.deepcopy(self.dashboard)
        changed["panels"][0]["targets"][0]["expr"] = "$newVariable"
        with self.assertRaises(ValueError):
            queries.fixture(changed)
        changed = copy.deepcopy(self.dashboard)
        changed["panels"].append(changed["panels"][0])
        with self.assertRaises(ValueError):
            queries.fixture(changed)


if __name__ == "__main__":
    unittest.main()
