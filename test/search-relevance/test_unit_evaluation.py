"""Independent examples protect metric math, eligibility and held-out discipline."""

import copy
import json
import math
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import evaluation as ev


class EvaluationTests(unittest.TestCase):
    """Use tiny known answers rather than snapshotting the implementation output."""

    def test_fixture_completeness(self):
        manifest, documents, queries = ev.fixtures()
        self.assertEqual(len(documents), 80)
        self.assertEqual(len(queries), 60)
        self.assertEqual(sum(q["split"] == "test" for q in queries), 40)
        self.assertEqual(sum(q["split"] == "test" and not q["relevant"] for q in queries), 8)
        self.assertEqual(manifest["dimensions"], 384)
        self.assertEqual({d["kind"] for d in documents if d["public"]}, ev.KINDS)

    def test_metric_math_and_denominators(self):
        self.assertEqual(ev.metrics(["a", "b"], {"a": 2, "b": 1})["ndcg"], 1)
        reverse = ev.metrics(["b", "a"], {"a": 2, "b": 1})
        self.assertAlmostEqual(reverse["ndcg"], (1+3/math.log2(3))/(3+1/math.log2(3)))
        self.assertEqual(reverse["recall"], 1)
        missed = ev.metrics(["x", "a"], {"a": 2, "b": 1})
        self.assertEqual(missed["recall"], 0.5)
        self.assertEqual(ev.metrics([], {"a": 2})["ndcg"], 0)
        self.assertIsNone(ev.metrics([], {})["ndcg"])
        self.assertTrue(ev.metrics(["a"], {})["false_positive"])
        totals = ev.summarize([missed, ev.metrics([], {}), ev.metrics(["a"], {})])
        self.assertEqual((totals["answerable"], totals["no_answer"], totals["false_positives"]), (1, 2, 1))
        self.assertEqual(totals["recall"], 0.5)
        self.assertIsNone(ev.summarize([])["ndcg"])
        self.assertEqual(ev.metrics([str(i) for i in range(11)], {"10": 2})["recall"], 0)
        with self.assertRaises(ValueError):
            ev.metrics(["a", "a"], {"a": 2})

    def test_vector_checks(self):
        self.assertAlmostEqual(ev.cosine([1, 0], [1, 1]), 1/math.sqrt(2))
        self.assertEqual(ev.vector([1, 0], 2), [1, 0])
        for values in ([0, 0], [2, 0], [True, 0], [math.nan, 0], [math.inf, 0], [1e100, 0], [1]):
            with self.subTest(values=values), self.assertRaises(ValueError):
                ev.vector(values, 2)
        for left, right in (([], []), ([1], [1, 0]), ([0], [0])):
            with self.assertRaises(ValueError):
                ev.cosine(left, right)

    def test_scope_independent_candidates_exact_and_ties(self):
        _, documents, _ = ev.fixtures()
        docs = copy.deepcopy(documents[:4])
        for i, d in enumerate(docs):
            d.update(id=str(i), identifier="urn:air:catalog.example.test:demo:"+str(i), name="Same", public=i>=2)
        query = {"text": docs[0]["identifier"]}
        lexical = {"0": 100, "1": 0, "2": 40, "3": 40}
        similarities = {"0": 0.1, "1": 0.9, "2": 0.9, "3": 0.2}
        rank = ev.rankings(docs, query, "control", lexical, similarities, 0.8, 60)
        self.assertEqual(rank["semantic"], ["1", "2"])
        self.assertIn("1", rank["hybrid"])
        self.assertEqual(rank["hybrid"][0], "0")
        self.assertEqual(rank["lexical"], ["0", "2", "3"])
        public = ev.rankings(docs, query, "public", lexical, similarities, 0.8, 60)
        self.assertNotIn("0", public["hybrid"])
        self.assertNotIn("1", public["hybrid"])
        self.assertEqual(ev.eligible(docs, {"source": "indexed"}, "public"), [])
        self.assertEqual(len(ev.eligible(docs, {"kind": "plugin"}, "control")), 1)
        docs[1]["identifier"] = docs[0]["identifier"]
        with self.assertRaises(ValueError):
            ev.rankings(docs, query, "control", lexical, similarities, 0.8, 60)

    def test_metadata_projection_excludes_private_and_control_fields(self):
        _, docs, _ = ev.fixtures()
        document = dict(docs[0], secret="DO NOT SEND", feedback="private", tenant="tenant-abc")
        text = ev.projection(document)
        for forbidden in ("DO NOT SEND", "tenant-abc", document["identifier"], "Source:"):
            self.assertNotIn(forbidden, text)
        self.assertTrue(text.startswith("Name: "))
        shuffled = dict(document, tags=list(reversed(document["tags"])) + document["tags"])
        self.assertEqual(text, ev.projection(shuffled))

    def test_tuning_rejects_heldout_and_empty_splits(self):
        manifest, docs, queries = ev.fixtures()
        for selected in (queries, []):
            with self.subTest(empty=not selected), self.assertRaisesRegex(ValueError, "only the tuning split"):
                ev.tune(manifest, docs, selected, {}, {})

    def test_duplicate_json_and_split_leakage(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)/"input.json"
            path.write_text('{"x":1,"x":2}')
            with self.assertRaises(ValueError):
                ev.load_json(path)
        manifest, documents, queries = ev.fixtures()
        bad = copy.deepcopy(queries)
        next(q for q in bad if q["split"] == "test" and q["relevant"])["family"] = queries[0]["family"]
        with patch.object(ev, "load_json", side_effect=[manifest, documents, bad]), self.assertRaisesRegex(ValueError, "intent family"):
            ev.fixtures()
        bad = copy.deepcopy(queries)
        bad[0]["relevant"]["not-a-document"] = 2
        with patch.object(ev, "load_json", side_effect=[manifest, documents, bad]), self.assertRaisesRegex(ValueError, "judgments"):
            ev.fixtures()
        self.assertEqual(json.loads(json.dumps(manifest)), manifest)

    def test_real_tuning_preserves_no_answer_then_quality_and_stricter_ties(self):
        """Known scores exercise actual ranking and metrics, not mocked summaries."""
        _, documents, _ = ev.fixtures()
        document = dict(documents[0], public=True, source="local")
        identifier = document["id"]
        queries = [
            {"id": "answer", "text": "task", "split": "tune", "relevant": {identifier: 2}, "kind": document["kind"]},
            {"id": "negative", "text": "unsupported task", "split": "tune", "relevant": {}},
        ]
        lexical = {surface: {q["id"]: {identifier: 0} for q in queries} for surface in ("control", "public")}
        scores = {"answer": {identifier: 0.9}, "negative": {identifier: 0.7}}
        cutoff, grid = ev.tune({"thresholds": [0.5, 0.8, 0.85, 1.01], "rrf_k": 60}, [document], queries, lexical, scores)
        self.assertEqual(cutoff, 0.85)
        self.assertEqual([(row["excess_false_positives"], row["ndcg"]) for row in grid], [(2, 1), (0, 1), (0, 1), (0, 0)])
        measured = ev.measure([document], queries, "public", lexical["public"], scores, cutoff, 60)
        self.assertEqual(measured["per_kind"][document["kind"]]["hybrid"]["recall"], 1)
        self.assertEqual(measured["queries"][1]["rankings"]["hybrid"], [])

    def test_fixture_validation_rejects_incomplete_or_contaminated_inputs(self):
        """Each mutation invalidates a distinct population or judgment boundary."""
        manifest, documents, queries = ev.fixtures()
        cases = [
            ("empty", "unbounded or empty fixture"),
            ("duplicate-document", "duplicate document identity"),
            ("missing-kind", "all four kinds"),
            ("document-text", "invalid document text"),
            ("duplicate-query", "duplicate query"),
            ("split", "invalid query or split"),
            ("kind", "invalid kind"),
            ("source", "invalid source"),
            ("negative-judgment", "no-answer query has relevant"),
            ("representative", "copied from representative"),
            ("filtered-kinds", "each split needs explicit queries"),
        ]
        for failure, message in cases:
            docs, qs = copy.deepcopy(documents), copy.deepcopy(queries)
            if failure == "empty":
                docs.clear()
            elif failure == "duplicate-document":
                docs[1]["identifier"] = docs[0]["identifier"]
            elif failure == "missing-kind":
                docs = [d for d in docs if d["kind"] != "plugin"]
            elif failure == "document-text":
                docs[0]["description"] = ""
            elif failure == "duplicate-query":
                qs[1]["id"] = qs[0]["id"]
            elif failure in ("split", "kind", "source"):
                qs[0][failure] = "unknown"
            elif failure == "negative-judgment":
                next(q for q in qs if q["family"] == "no-answer")["relevant"] = {docs[0]["id"]: 2}
            elif failure == "representative":
                next(q for q in qs if q["split"] == "test")["text"] = docs[0]["representativeQueries"][0]
            else:
                for q in qs:
                    q.pop("kind", None)
            with self.subTest(failure=failure), self.assertRaisesRegex(ValueError, message):
                ev.validate_fixtures(manifest, docs, qs)

    def test_fixture_judgments_and_markers_are_unambiguous(self):
        manifest, documents, queries = ev.fixtures()
        for failure in ("public", "exact-type", "exact-identity", "scope", "empty", "duplicate-text"):
            docs, qs = copy.deepcopy(documents), copy.deepcopy(queries)
            query = next(q for q in qs if q.get("exact"))
            if failure == "public":
                docs[0]["public"] = "false"
            elif failure == "exact-type":
                query["exact"] = "false"
            elif failure == "exact-identity":
                query["text"] = "urn:air:wrong.example:demo:missing"
            elif failure == "scope":
                doc = next(d for d in docs if d["id"] in query["relevant"])
                query["kind"] = next(k for k in sorted(ev.KINDS) if k != doc["kind"])
            elif failure == "empty":
                query["relevant"] = {}
            else:
                tune = next(q for q in qs if q["split"] == "tune" and not q["relevant"])
                heldout = next(q for q in qs if q["split"] == "test" and not q["relevant"])
                heldout["text"] = "  " + tune["text"].upper() + "  "
            with self.subTest(failure=failure), patch.object(ev, "load_json", side_effect=[manifest, docs, qs]), self.assertRaises(ValueError):
                ev.fixtures()

    def test_invalid_manifest_and_nonfinite_json(self):
        manifest, _, _ = ev.fixtures()
        for update in ({"files": {"../outside": "a"*64}}, {"dimensions": 0}, {"dimensions": 384.0}, {"thresholds": [0.7, 0.6]}, {"rrf_k": 0}, {"rrf_k": True}, {"tei_image": "floating:latest"}, {"minimum_ndcg_gain": 0}):
            with self.subTest(update=update), self.assertRaises(ValueError):
                ev.validate_manifest(dict(manifest, **update))
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)/"input.json"
            path.write_text('{"x": NaN}')
            with self.assertRaises(ValueError):
                ev.load_json(path)

    def test_acceptance_does_not_hide_failed_quality(self):
        result = {"summary": {"lexical": {"ndcg": 0.2, "recall": 0.5, "false_positives": 0},
                              "hybrid": {"ndcg": 0.8, "recall": 0.9, "false_positives": 1}}, "exact_failures": []}
        verdict = ev.acceptance(result, 0.05)
        self.assertTrue(verdict["useful_gain"])
        self.assertFalse(verdict["no_answer_preserved"])


if __name__ == "__main__":
    unittest.main()
