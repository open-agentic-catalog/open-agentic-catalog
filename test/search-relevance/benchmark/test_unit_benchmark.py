"""Isolated protocol/scoring tests; no application, model, process or network."""

import copy
import importlib.util
import hashlib
import json
import os
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
from benchmark import protocol
from benchmark.benchmark import input_fingerprint, load_benchmark_inputs, render, require_inputs, run_benchmark, save_report


class BenchmarkTests(unittest.TestCase):
    def setUp(self):
        self.manifest, self.profiles, self.documents, self.queries = load_benchmark_inputs()

    def validate(self, manifest=None, queries=None, documents=None):
        protocol.validate(manifest or self.manifest, self.profiles, documents or self.documents, queries or self.queries)

    def test_reviewed_population_and_generator_agree(self):
        path = Path(__file__).with_name("generate_fixtures.py")
        spec = importlib.util.spec_from_file_location("reviewed_generator", path)
        generator = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(generator)
        self.assertEqual(generator.build_corpus(), self.documents)
        self.assertEqual(generator.build_queries(self.documents), self.queries)
        self.assertEqual({d["kind"]: sum(x["kind"] == d["kind"] for x in self.documents) for d in self.documents},
                         {"agent": 40, "mcp_server": 40, "skill": 40, "plugin": 40})
        tasks = [q for q in self.queries if q.get("agent_benchmark")]
        self.assertEqual(len(tasks), 16)
        self.assertEqual({protocol.stratum(q) for q in tasks}, {"answerable", "no_answer", "partial_only"})
        self.assertEqual(sum(protocol.stratum(q) == "no_answer" for q in tasks), 4)

    def test_bounded_protocol_rejects_zero_repeats_and_invalid_counts(self):
        for key, value in (("agent_repeats", 0), ("agent_repeats", True), ("agent_task_count", 17),
                           ("episode_seconds", 0), ("run_seconds", 999999), ("thresholds", [1.01, .5])):
            with self.subTest(key=key, value=value):
                manifest = copy.deepcopy(self.manifest)
                manifest[key] = value
                with self.assertRaises(ValueError):
                    self.validate(manifest=manifest)

    def test_query_bytes_and_negative_family_leakage_are_rejected(self):
        queries = copy.deepcopy(self.queries)
        queries[0]["text"] = "é" * 250
        self.validate(queries=queries)  # Valid exactly-500-byte control.
        queries[0]["text"] += "é"
        with self.assertRaisesRegex(ValueError, "UTF-8"):
            self.validate(queries=queries)
        queries = copy.deepcopy(self.queries)
        negative = next(q for q in queries if q["split"] == "test" and not q["relevant"])
        negative["intentFamily"] = "invoice"
        with self.assertRaisesRegex(ValueError, "intent family"):
            self.validate(queries=queries)

    def test_core_population_and_agent_subset_cannot_silently_change(self):
        documents = copy.deepcopy(self.documents)
        documents[3]["source"] = "indexed"
        with self.assertRaisesRegex(ValueError, "local releases"):
            self.validate(documents=documents)
        queries = copy.deepcopy(self.queries)
        next(q for q in queries if q.get("agent_benchmark"))["agent_benchmark"] = False
        with self.assertRaisesRegex(ValueError, "agent subset"):
            self.validate(queries=queries)

    def test_exact_partial_and_no_answer_have_distinct_denominators(self):
        queries = [
            {"id": "direct", "relevant": {"a": 2, "b": 1}, "category": "task", "intentFamily": "one"},
            {"id": "partial", "relevant": {"c": 1}, "category": "partial", "intentFamily": "two"},
            {"id": "empty", "relevant": {}, "category": "unrelated", "intentFamily": "three"},
            {"id": "exact", "relevant": {"a": 2}, "exact": True, "category": "exact", "intentFamily": "one"},
        ]
        result = protocol.measure(queries, {"direct": ["a", "b"], "partial": ["c"], "empty": ["wrong"], "exact": ["a"]})
        summary = result["summary"]
        self.assertEqual((summary["answerable"], summary["partial_only"], summary["no_answer"], summary["exact"]), (1, 1, 1, 1))
        self.assertEqual((summary["ndcg"], summary["recall"], summary["false_positives"]), (1, 1, 1))
        self.assertEqual(result["queries"][2]["metrics"]["ndcg"], None)
        with self.assertRaisesRegex(ValueError, "missing or extra"):
            protocol.measure(queries, {"direct": []})
        with self.assertRaisesRegex(ValueError, "duplicate"):
            protocol.metrics(["a", "a"], {"a": 2})

    @staticmethod
    def trial(name, ndcg=.6, recall=.8, threshold=.7, errors=0):
        return {"profile": name, "selected": {"ndcg": ndcg, "recall": recall, "threshold": threshold, "excess_false_positives": errors},
                "lexical": {"summary": {"ndcg": .5}}}

    def test_finalists_need_positive_gain_and_honest_diagnostic_fallback(self):
        selected = protocol.select_finalists([self.trial("equal", ndcg=.5)])
        self.assertEqual(selected, {"profiles": ["equal"], "diagnostic": True})
        selected = protocol.select_finalists([self.trial("good"), self.trial("adds-errors", ndcg=.99, errors=1), self.trial("equal", ndcg=.5)])
        self.assertEqual(selected, {"profiles": ["good"], "diagnostic": False})

    def test_model_ties_use_recall_then_profile_without_comparing_cutoff_scales(self):
        selected = protocol.select_finalists([self.trial("lower-recall", recall=.7), self.trial("looser", threshold=.6),
                                              self.trial("b", threshold=.8), self.trial("a", threshold=.8)])
        self.assertEqual(selected["profiles"], ["a", "b"])
        self.assertEqual(protocol.select_finalists([self.trial("a", threshold=.3), self.trial("z", threshold=.9)])["profiles"], ["a", "z"])

    def test_cutoff_ties_within_one_model_prefer_the_stricter_value(self):
        documents = self.documents[:4]
        first = documents[0]["id"]
        queries = [{"id": "direct", "text": "question", "split": "tune", "category": "task", "intentFamily": "one", "relevant": {first: 2}},
                   {"id": "empty", "text": "absent", "split": "tune", "category": "unrelated", "intentFamily": "two", "relevant": {}}]
        lexical = {q["id"]: {d["id"]: int(q["id"] == "direct" and d["id"] == first) for d in documents} for q in queries}
        similarities = {q["id"]: {d["id"]: 0 for d in documents} for q in queries}
        selected, _, _ = protocol.tuning_grid({"thresholds": [.2, .9]}, documents, queries, lexical, similarities)
        self.assertEqual(selected["threshold"], .9)
        self.assertEqual(selected["ndcg"], 1)
        queries[0]["split"] = "test"
        with self.assertRaisesRegex(ValueError, "tuning queries only"):
            protocol.tuning_grid({"thresholds": [.2, .9]}, documents, queries, lexical, similarities)

    def test_episode_requires_observed_search_read_and_no_execution_error(self):
        task = {"id": "t", "intentFamily": "family", "relevant": {"a": 2, "b": 1}}
        evidence = {"searched": True, "incomplete": False, "found": ["a", "b", "wrong"], "read": ["a", "b", "wrong"]}
        self.assertEqual(protocol.episode(task, evidence, [{"release_id": "a"}, {"release_id": "b"}])["outcome"], "successful_discovery")
        self.assertEqual(protocol.episode(task, evidence, [{"release_id": "a"}], "deadline_exceeded")["outcome"], "budget_exhausted")
        refused = protocol.episode(task, evidence, [], "model_recommended_unread_resource")
        self.assertEqual(refused["outcome"], "grounding_failure")
        self.assertFalse(refused["grounded"])
        self.assertEqual(protocol.episode(task, {**evidence, "searched": False}, [{"release_id": "a"}])["outcome"], "did_not_search")
        self.assertEqual(protocol.episode(task, {**evidence, "found": []}, [{"release_id": "a"}])["outcome"], "grounding_failure")
        self.assertEqual(protocol.episode(task, {**evidence, "read": []}, [{"release_id": "a"}])["outcome"], "grounding_failure")
        self.assertEqual(protocol.episode(task, evidence, [{"release_id": "a"}, {"release_id": "wrong"}])["outcome"], "wrong_selection")
        self.assertEqual(protocol.episode(task, evidence, [{"release_id": "a"}, {"release_id": "a"}])["outcome"], "grounding_failure")
        empty = {**task, "relevant": {}}
        self.assertEqual(protocol.episode(empty, evidence, [])["outcome"], "appropriate_abstention")
        self.assertEqual(protocol.episode(empty, {**evidence, "incomplete": True}, [])["outcome"], "incomplete_search")
        self.assertEqual(protocol.episode(empty, evidence, [{"release_id": "a"}])["outcome"], "unsupported_recommendation")
        self.assertEqual(protocol.episode({**task, "relevant": {"a": 1}}, evidence, [{"release_id": "a"}])["outcome"], "partial_usefulness")

    def test_agent_aggregate_requires_each_episode_and_keeps_errors(self):
        task = {"id": "t", "intentFamily": "family", "relevant": {"a": 2}}
        evidence = {"searched": True, "incomplete": False, "found": ["a"], "read": ["a"]}
        rows = [{**protocol.episode(task, evidence, [{"release_id": "a"}]), "repeat": 0},
                {**protocol.episode(task, evidence, [], "deadline_exceeded"), "repeat": 1}]
        result = protocol.aggregate_agent(rows, [task], 2)
        self.assertEqual(result["discovery_success_rate"], .5)
        self.assertEqual(result["errors"], 1)
        self.assertIsNone(result["abstention_rate"])
        for changed in (rows[:1], [rows[0], rows[0]], rows + rows):
            with self.assertRaisesRegex(ValueError, "episodes"):
                protocol.aggregate_agent(changed, [task], 2)

    def test_family_bootstrap_does_not_treat_repeats_as_independent(self):
        value = protocol.paired_interval([("a", 1), ("a", 1), ("a", 1), ("b", -1)], 17)
        self.assertEqual(value["families"], 2)
        self.assertEqual(value["mean_difference"], 0)
        self.assertEqual(value["ci95"], [-1, 1])
        self.assertIsNone(protocol.paired_interval([("a", 1)], 17)["ci95"])

    def test_fingerprint_and_frozen_mutations_are_rejected(self):
        expected = input_fingerprint()
        require_inputs(expected)
        with patch("benchmark.benchmark.input_fingerprint", return_value={"changed": "bytes"}):
            with self.assertRaisesRegex(ValueError, "inputs changed"):
                require_inputs(expected)
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "frozen.json"
            path.write_text('{}')
            import evaluation
            digest = evaluation.digest(path)
            require_inputs(expected, path, digest)
            path.write_text('{"changed":true}')
            with self.assertRaisesRegex(ValueError, "frozen experiment"):
                require_inputs(expected, path, digest)

    def test_invalid_report_values_produce_a_failed_receipt(self):
        with tempfile.TemporaryDirectory() as directory:
            directory = Path(directory)
            with self.assertRaisesRegex(ValueError, "invalid report observation"):
                save_report({"status": "complete", "selection": "full", "observation": object()}, directory)
            value = json.loads((directory / "report.json").read_text())
            self.assertEqual(value["status"], "failed")
            self.assertNotIn("quality", value)

    def test_interrupted_campaign_writes_failed_receipt_instead_of_running(self):
        from benchmark import runtime
        import runner
        with tempfile.TemporaryDirectory() as directory, patch.dict(os.environ):
            with patch.object(runtime, "preflight", return_value={"images": {}}), patch.object(runtime, "catalog_fixture", side_effect=KeyboardInterrupt), patch.object(runner, "OUTPUT", Path(directory)):
                with self.assertRaises(KeyboardInterrupt):
                    run_benchmark("smoke")
            reports = list(Path(directory).glob("benchmark/campaign-*/report.json"))
            self.assertEqual(len(reports), 1)
            value = json.loads(reports[0].read_text())
            self.assertEqual((value["status"], value["error"]), ("failed", "interrupted"))
            self.assertFalse(value["qualified_for_recommendation"])

    def test_observed_pages_require_actual_eligible_identity_and_stable_context(self):
        from benchmark.runtime import CatalogFixture
        fixture = object.__new__(CatalogFixture)
        fixture.arm, fixture.context, fixture.by_release = "bge-small-v1", None, {"actual-release": "fixture-a"}
        fixture.kind_by_release = {"actual-release": "skill"}
        page = {"tier": "hybrid", "profile": "sha256:" + "a" * 64, "generation": "sha256:" + "b" * 64,
                "partial": False, "liveCount": 0, "items": [{"id": "local:actual-release", "kind": "skill", "type": "local", "local": {"release": {"id": "actual-release"}}}]}
        fixture.observe_page(page)
        self.assertEqual(fixture.context["tier"], "hybrid")
        for change in ({"tier": "core"}, {"partialIndex": True}, {"fallbackReason": "provider_unavailable"}, {"generation": "changed"}):
            with self.subTest(change=change), self.assertRaises(ValueError):
                fixture.observe_page({**page, **change})
        changed = copy.deepcopy(page)
        changed["items"][0]["local"]["release"]["id"] = "excluded-release"
        with self.assertRaisesRegex(ValueError, "forbidden or unmapped"):
            fixture.observe_page(changed)
        fixture.observe_page(page, kinds=["skill"])
        with self.assertRaisesRegex(ValueError, "explicit kind filter"):
            fixture.observe_page(page, kinds=["agent"])
        changed = copy.deepcopy(page)
        changed["items"][0]["id"] = "local:wrong-alias"
        with self.assertRaisesRegex(ValueError, "identity or retained kind"):
            fixture.observe_page(changed)

    def test_reasoning_cache_verifies_pinned_closure_in_both_ollama_layouts(self):
        from benchmark import runtime
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            blobs = root / "models/blobs"
            blobs.mkdir(parents=True)
            def blob(data, media):
                digest = hashlib.sha256(data).hexdigest()
                (blobs / ("sha256-" + digest)).write_bytes(data)
                return {"mediaType": media, "digest": "sha256:" + digest, "size": len(data)}
            config = blob(b"{}", "config")
            weights = blob(b"weights", "model")
            original = {"config": config, "layers": [weights]}
            data = json.dumps(original).encode()
            descriptor = blob(data, "application/vnd.docker.distribution.manifest.v2+json")
            pin = {"digest": descriptor["digest"].removeprefix("sha256:")}
            manifest = root / "models/manifests/registry.ollama.ai/library/qwen2.5/7b"
            manifest.parent.mkdir(parents=True)
            with patch.object(runtime, "REASONER_CACHE", root):
                manifest.write_bytes(data)
                runtime.verify_reasoner(pin)
                manifest.write_text(json.dumps({"config": config, "layers": [weights, descriptor]}))
                runtime.verify_reasoner(pin)
                changed = {"config": config, "layers": [descriptor]}
                manifest.write_text(json.dumps(changed))
                with self.assertRaisesRegex(ValueError, "closure differs"):
                    runtime.verify_reasoner(pin)
                manifest.write_bytes(data)
                (blobs / weights["digest"].replace(":", "-")).write_bytes(b"altered")
                with self.assertRaisesRegex(ValueError, "model blob"):
                    runtime.verify_reasoner(pin)

    def test_failed_report_can_be_rendered_without_quality_results(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "report.md"
            render({"status": "failed", "selection": "full", "error": "interrupted"}, path)
            self.assertIn('interrupted', path.read_text())
            self.assertNotIn('100%', path.read_text())


if __name__ == "__main__":
    unittest.main()
