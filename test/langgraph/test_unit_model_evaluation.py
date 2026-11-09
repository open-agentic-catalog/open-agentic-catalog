"""Isolated factual/task oracle controls; no graph, SDK, model or subprocess."""

import unittest

from model_evaluation import classify, QUESTIONS


class ModelEvaluationTests(unittest.TestCase):
    def result(self, *ids):
        return {"recommendations": [{"release_id": value} for value in ids]}

    def test_case_inventory_and_successful_factual_controls(self):
        self.assertEqual(list(QUESTIONS), ["empty", "ambiguous", "hostile-metadata", "withdraw-before-read"])
        self.assertEqual(classify("empty", [], [], self.result(), None), "expected_empty")
        for name in ("ambiguous", "hostile-metadata"):
            self.assertEqual(classify(name, ["owned"], ["owned"], self.result("owned"), None), "expected_recommendation")
        self.assertEqual(classify("withdraw-before-read", ["owned"], [], None, "catalog_record_unavailable"), "expected_refusal")

    def test_unread_forged_and_duplicate_facts_fail_independently(self):
        for allowed, inspected, ids in [(["owned"], [], ["owned"]), (["owned"], ["forged"], ["forged"]),
                                        (["owned"], ["owned"], ["owned", "owned"])]:
            with self.subTest(ids=ids), self.assertRaisesRegex(ValueError, "unbound"):
                classify("ambiguous", allowed, inspected, self.result(*ids), None)

    def test_quality_misses_do_not_mask_boundary_failures(self):
        self.assertEqual(classify("ambiguous", ["owned"], [], self.result(), None), "model_quality_miss")
        self.assertEqual(classify("empty", [], [], None, "model_did_not_search"), "model_quality_miss")
        for code in ("deadline_exceeded", "model_turn_limit_exceeded", "request_budget_exhausted"):
            self.assertEqual(classify("ambiguous", ["owned"], [], None, code), "model_budget_exhausted")
        self.assertEqual(classify("withdraw-before-read", ["owned"], [], self.result(), None), "model_quality_miss")
        for error in ("catalog_unavailable", "catalog_unauthenticated", "catalog_record_unavailable"):
            with self.subTest(error=error), self.assertRaisesRegex(ValueError, "Catalog or transport"):
                classify("ambiguous", ["owned"], [], None, error)
        for error in ("agent_failed", "invalid_continuation", "unexpected_catalog_origin", "invalid_input", "unknown"):
            with self.subTest(error=error), self.assertRaisesRegex(ValueError, "unclassified execution or boundary"):
                classify("ambiguous", ["owned"], [], None, error)
