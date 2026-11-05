"""Scale dashboard oracles with an inert HTTP transport."""
import copy
import json
import unittest
from unittest.mock import Mock

import runner
import scale


class OverviewTests(unittest.TestCase):
    def population(self, large=False):
        expected = {"resources": 11000 if large else 900, "agent": 225, "mcp_server": 225,
                    "skill": 225, "plugin": 225, "published": 900, "namespaces": 111,
                    "publishers": 2, "indexed": 10000 if large else 100, "sources": 3,
                    "human": 12, "service": 1, "roles": 14, "assignments": 134,
                    "feedback": 0, "ratings": 0, "sparseResources": 179}
        def count(n):
            return {"value": min(n, 10000), "limited": n > 10000}
        scope = {"tenantId": "00000000-0000-7000-8000-000000000001", "scope": "current_access", "observedAt": "2026-10-25T12:00:00Z", "selectionLimit": 10000}
        resources = {"total": count(expected["resources"])}
        if not large:
            resources["kinds"] = [{"kind": kind, "count": 225} for kind in ("agent", "mcp_server", "skill", "plugin")]
        values = [resources, {"published": count(900)}, {"count": count(111)}, {"count": count(2)},
                  {"limited": True} if large else {"limited": False, "count": 100}, {}, {},
                  {"total": count(3)}, {},
                  {"human": {"total": count(12)}, "service": {"total": count(1)}, "activeRoles": {"total": count(14)}, "activeAssignments": count(134)},
                  {"open": count(0)}, {"total": count(0)}]
        rows = [(200, {"Cache-Control": "no-store"}, scope | value) for value in values]
        rows[6][2]["selectionLimit"] = 64
        rows.extend([(200, {}, {"namespaceGrants": [{"namespaceId": str(n)} for n in range(100)], "nextCursor": "next"}),
                     (200, {"cache-control": "no-store"}, scope | {"total": count(179)})])
        return {"overviewExpected": expected, "sparseNamespace": "105"}, rows

    def check(self, inventory, rows):
        http = Mock(decode=json.loads)
        http.request.side_effect = [(status, headers, json.dumps(value).encode()) for status, headers, value in rows]
        result = scale.overview("http://owned", http, runner.Failure, inventory)
        self.assertEqual(14, http.request.call_count)
        self.assertTrue(all(call.args[0].get_method() == "GET" for call in http.request.call_args_list))
        self.assertTrue(all(call.args[0].full_url.startswith("http://owned/api/v1/") for call in http.request.call_args_list))
        self.assertEqual(14, len(result["encodedBytes"]))
        return result

    def test_complete_and_limited_counts_and_sparse_scope(self):
        for large in (False, True):
            self.assertEqual("passed", self.check(*self.population(large))["status"])

    def test_bad_setup_fails_before_http(self):
        inventory, rows = self.population()
        for expected in ({}, inventory["overviewExpected"] | {"resources": True}, inventory["overviewExpected"] | {"private": 1}):
            with self.assertRaisesRegex(runner.Failure, "inventory-invalid"):
                self.check(inventory | {"overviewExpected": expected}, rows)

    def test_wrong_counts_scope_cache_and_sparse_navigation_fail(self):
        cases = [(0, "status", 403), (0, "headers", {}), (0, "tenantId", "other"),
                 (0, "scope", "all"), (0, "total", {"value": 899, "limited": False}),
                 (0, "total", {"value": 900, "limited": 0}),
                 (0, "kinds", [{"kind": "agent", "count": 900}]),
                 (4, "count", 107), (12, "nextCursor", ""), (12, "namespaceGrants", [{"namespaceId": "105"}] * 100),
                 (13, "total", {"value": 900, "limited": False}), (13, "headers", {}),
                 (13, "scope", "all"), (0, "padding", "x" * 131072), (0, "selectionLimit", 20000), (6, "selectionLimit", 10000)]
        for index, field, value in cases:
            with self.subTest(index=index, field=field):
                inventory, rows = self.population()
                status, headers, document = copy.deepcopy(rows[index])
                if field == "status":
                    status = value
                elif field == "headers":
                    headers = value
                else:
                    document[field] = value
                rows[index] = status, headers, document
                with self.assertRaises(runner.Failure):
                    self.check(inventory, rows)
