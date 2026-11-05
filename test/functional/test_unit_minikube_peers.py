"""Independent membership oracle cases, without servers or compiled tools."""
import copy
from contextlib import redirect_stderr
import io
import json
import unittest
from unittest.mock import Mock, patch

from minikube_peers import control_page, main, validate_members, validate_search_sources


class MinikubePeerTests(unittest.TestCase):
    def test_failed_cluster_page_retains_position_and_a_closed_main_diagnostic(self):
        client = Mock()
        error = AssertionError("functional federation request returned HTTP 400 (CATALOG_CURSOR_INVALID)")
        client.request.side_effect = [({"items": [{"identifier": "first"}], "partial": False,
                                       "durableTotal": 2, "nextCursor": "private-token"}, {}), error]
        with self.assertRaises(AssertionError) as caught:
            control_page(client, {"first", "second"})
        self.assertIs(caught.exception, error)
        self.assertEqual(client.request.call_count, 2)
        expected = {"surface": "control", "page": 2, "completedPages": 1, "continuation": True,
                    "httpFailure": {"status": 400, "category": "CATALOG_CURSOR_INVALID"}}
        self.assertEqual(error.search_page_failure, expected)
        output = io.StringIO()
        with patch("minikube_peers.sys.stdin", io.StringIO("{}")), \
                patch("minikube_peers.sys.argv", ["minikube_peers.py", "lifecycle"]), \
                patch("minikube_peers.run", side_effect=error), redirect_stderr(output), self.assertRaises(SystemExit):
            main()
        line = next(line for line in output.getvalue().splitlines() if line.startswith("Search page failure: "))
        self.assertEqual(json.loads(line.removeprefix("Search page failure: ")), expected)
        self.assertNotIn("private-token", output.getvalue())

    def test_main_reports_closed_http_categories_without_private_exception_text(self):
        errors = [(AssertionError(f"functional federation request returned HTTP {status} ({category})"), category)
                  for status, category in ((400, "CATALOG_CURSOR_STALE"), (400, "LIVE_WINDOW_CHANGED"), (503, "REQUEST_DEADLINE_EXCEEDED"))]
        errors += [(AssertionError("private-secret"), None),
                   (AssertionError("functional federation request returned HTTP 400 (PUBLIC_CURSOR_STALE) private-secret"), None)]
        for error, category in errors:
            diagnostic = io.StringIO()
            with self.subTest(category=category), patch("minikube_peers.sys.stdin", io.StringIO("{}")), \
                    patch("minikube_peers.sys.argv", ["minikube_peers.py", "lifecycle"]), \
                    patch("minikube_peers.run", side_effect=error), redirect_stderr(diagnostic):
                with self.assertRaises(SystemExit) as result:
                    main()
                self.assertEqual(result.exception.code, 1)
            self.assertNotIn("private-secret", diagnostic.getvalue())
            if category:
                self.assertIn("(" + category + ")", diagnostic.getvalue())
            else:
                self.assertNotIn("HTTP", diagnostic.getvalue())

    def test_indexed_search_requires_exact_source_evidence(self):
        validate_search_sources([{"indexed": {"sourceIds": ["owned"]}}], "owned")
        for invalid in ([], [{"indexed": {"sourceIds": []}}],
                        [{"indexed": {"sourceIds": ["other"]}}],
                        [{"indexed": {"sourceIds": ["owned", "other"]}}]):
            with self.subTest(rows=invalid), self.assertRaises(AssertionError):
                validate_search_sources(invalid, "owned")

    def test_membership_rejects_duplicates_omissions_and_wrong_provenance(self):
        rows = [{"id": "one", "identifier": "urn:a", "sourceId": "owned", "freshness": "current"},
                {"id": "two", "identifier": "urn:b", "sourceId": "owned", "freshness": "current"}]
        validate_members(rows, {"urn:a", "urn:b"}, "owned")
        for invalid in (rows[:1], rows + rows[:1], [rows[0], rows[0]],
                        [{**rows[0], "sourceId": "other"}, rows[1]],
                        [{**rows[0], "freshness": "stale"}, rows[1]],
                        [rows[0], {**rows[1], "id": "one"}],
                        [rows[0], {**rows[1], "identifier": "urn:other"}]):
            with self.subTest(rows=invalid), self.assertRaises(AssertionError):
                validate_members(copy.deepcopy(invalid), {"urn:a", "urn:b"}, "owned")


if __name__ == "__main__":
    unittest.main()
