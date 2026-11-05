import json
import unittest
from unittest.mock import patch

import runner


class AttributionTests(unittest.TestCase):
    def test_owned_resource_sample_is_closed_and_numeric(self):
        ids = {name: character * 64 for name, character in [("api", "a"), ("worker", "b"), ("postgres", "c")]}
        rows = [{"ID": value, "CPUPerc": "2.34%", "MemUsage": "1.5MiB / 256MiB", "PIDs": "12", "Name": "ignored-private-name"} for value in ids.values()]
        with patch.object(runner.local, "command", return_value=b"\n".join(json.dumps(row).encode() for row in rows)):
            result = runner.resource_sample(ids, {})
            self.assertEqual({"cpuPercent": 2.34, "workingSetBytes": 1572864, "memoryLimitBytes": 268435456, "pids": 12}, result["api"])
            self.assertNotIn("private", json.dumps(result))
        for invalid in [rows[:2], rows + [rows[0]], [None], [rows[0] | {"CPUPerc": "NaN%"}], [rows[0] | {"PIDs": 12}], [rows[0] | {"ID": "foreign"}]]:
            with patch.object(runner.local, "command", return_value=b"\n".join(json.dumps(row).encode() for row in invalid)), self.assertRaises(runner.Failure):
                runner.resource_sample(ids, {})
        for quantity in ["NaN", "-1B", "100TB", "1e3B", "100000000000GiB"]:
            with self.assertRaises(runner.Failure):
                runner.byte_quantity(quantity)

    def test_database_observer_uses_fixed_read_only_owner_query(self):
        value = dict.fromkeys(["databaseBytes", "readBlocks", "hitBlocks", "tempBytes", "insertedRows", "updatedRows", "deletedRows", "databaseStatsReset", "walBytes", "walStatsReset"], 1)
        with patch.object(runner.local, "command", return_value=json.dumps(value).encode()) as command:
            self.assertEqual(value, runner.database_sample(["owned-compose"], {}))
            self.assertIn("BEGIN READ ONLY", command.call_args.args[0][-1])
            self.assertIn("statement_timeout='2s'", command.call_args.args[0][-1])
        for invalid in [value | {"private": 1}, value | {"tempBytes": -1}, value | {"walBytes": True}, value | {"databaseBytes": 513 << 20}, {}]:
            with patch.object(runner.local, "command", return_value=json.dumps(invalid).encode()), self.assertRaises(runner.Failure):
                runner.database_sample([], {})

    def test_fixture_configuration_does_not_modify_retained_profile(self):
        baseline = runner.local.safety.read_document(runner.ROOT / "config/postgres.json")
        updated = runner.scenarios.configuration(baseline)
        self.assertNotIn("second-reader", baseline["auth"]["developmentTokens"])
        self.assertEqual(3, len(updated["discovery"]["hosts"]))
        self.assertEqual(baseline["worker"], updated["worker"])
        self.assertEqual("00000000-0000-7000-8000-000000000002", updated["auth"]["developmentTokens"]["second-reader"]["tenantId"])
        self.assertNotIn("scale-sparse", updated["auth"]["developmentTokens"])
        selected = runner.scenarios.configuration(baseline, scale=True)
        self.assertEqual(["service_client"], selected["auth"]["developmentTokens"]["scale-sparse"]["roleTemplates"])
        self.assertNotIn("roles", selected["auth"]["developmentTokens"]["scale-sparse"])
        self.assertNotIn("scale-sparse", baseline["auth"]["developmentTokens"])


if __name__ == "__main__":
    unittest.main()
