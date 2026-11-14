"""Isolated evidence controls; never acquire or execute the independent peer."""

import copy
import unittest

from ard_registry_contract import PHASES, SELF, SOURCE, check_readback, entries_by_id, finish
import ard_registry_tools as tools


class RegistryContractTests(unittest.TestCase):
    def setUp(self):
        self.entries = [{"identifier": "urn:air:example.com:fixture:" + name, "displayName": name,
            "type": "application/mcp-server-card+json", "version": "1.0.0", "data": {"name": name},
            "extensions": {"https://example.com/metadata": {"release": name}}} for name in ("control", "subject")]
        self.page = {"items": [{**row, "source": SELF} for row in self.entries], "total": 2, "pageToken": None}
        self.search = {"results": [{**row, "source": SELF, "score": 42} for row in self.entries], "pageToken": None}
        self.snapshot = {"crawledAt": "2026-01-01T00:00:00Z", "entries": [{"entry": row, "origin": SOURCE} for row in self.entries]}

    def test_exact_original_metadata_and_distinct_provenance_pass(self):
        check_readback(self.entries, self.page, self.search, self.snapshot)
        # Native ordering differs between rank and List; membership must be exact.
        self.search["results"].reverse()
        check_readback(self.entries, self.page, self.search, self.snapshot)

    def test_lost_changed_extra_or_version_collapsed_members_fail(self):
        for target in ("page", "search", "snapshot"):
            for change in ("missing", "duplicate", "extension", "version", "source"):
                with self.subTest(target=target, change=change):
                    page, search, snapshot = copy.deepcopy((self.page, self.search, self.snapshot))
                    rows = page["items"] if target == "page" else search["results"] if target == "search" else snapshot["entries"]
                    entry = rows[0]["entry"] if target == "snapshot" else rows[0]
                    if change == "missing":
                        rows.pop()
                    elif change == "duplicate":
                        rows[1] = copy.deepcopy(rows[0])
                    elif change == "extension":
                        entry.pop("extensions")
                    elif change == "version":
                        entry["version"] = "2.0.0"
                    else:
                        rows[0]["origin" if target == "snapshot" else "source"] = "https://wrong.example/"
                    with self.assertRaises(AssertionError):
                        check_readback(self.entries, page, search, snapshot)

    def test_native_token_total_and_score_are_not_ignored(self):
        for total in (True, 0, 3):
            page = {**self.page, "total": total}
            with self.assertRaises(AssertionError):
                check_readback(self.entries, page, self.search, self.snapshot)
        for token in ("next", ""):
            with self.assertRaises(AssertionError):
                check_readback(self.entries, {**self.page, "pageToken": token}, self.search, self.snapshot)
        for score in (True, 0, 101, "42"):
            search = copy.deepcopy(self.search)
            search["results"][0]["score"] = score
            with self.assertRaises(AssertionError):
                check_readback(self.entries, self.page, search, self.snapshot)

    def test_empty_and_duplicate_identifiers_are_not_complete_transfer(self):
        for entries in ([], [{"identifier": ""}], [self.entries[0], self.entries[0]]):
            with self.assertRaises(AssertionError):
                entries_by_id(entries)

    def test_incomplete_failed_or_unclean_campaign_cannot_pass(self):
        report = {"cleanup": "complete", "phases": dict.fromkeys(PHASES, "passed")}
        self.assertTrue(finish(report))
        for phase in PHASES:
            for status in ("failed", "unrun"):
                altered = copy.deepcopy(report)
                altered["phases"][phase] = status
                self.assertFalse(finish(altered))
        self.assertFalse(finish({**report, "cleanup": "failed"}))
        self.assertFalse(finish({**report, "error": "interrupted"}))
        self.assertFalse(finish({**report, "control": "metadata-mismatch"}))

    def test_changed_package_manager_bytes_refused_before_execution(self):
        with self.assertRaisesRegex(ValueError, "integrity"):
            tools.verify_package(b"substituted tarball")


if __name__ == "__main__":
    unittest.main()
