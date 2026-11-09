"""Service-free checks for profile isolation, fresh judgments and model selection."""

import contextlib
import copy
import io
import json
from pathlib import Path
import signal
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import urllib.error

import comparison as comp
import evaluation as ev
import runner


class ComparisonTests(unittest.TestCase):
    """Exercise orchestration with fakes; no weights, Docker or database required."""

    def test_versioned_inputs_and_preprocessing(self):
        _, profiles, docs, queries = comp.inputs()
        self.assertEqual(len(docs), 80)
        self.assertEqual(len(queries), 86)
        for split in ("tune", "test"):
            selected = [q for q in queries if q["split"] == split]
            self.assertEqual(len(selected), 43)
            self.assertEqual(sum(q["category"] == "hard-negative" for q in selected), 4)
            self.assertEqual(sum(not q["relevant"] for q in selected), 6)
        bge, mini = profiles.values()
        self.assertEqual((bge["pooling"], bge["max_input_length"]), ("cls", 512))
        self.assertEqual((mini["pooling"], mini["max_input_length"]), ("mean", 256))
        self.assertNotEqual(bge["query_prefix"], mini["query_prefix"])
        self.assertEqual(comp.cache("bge-small-v1"), runner.MODEL)
        self.assertNotEqual(comp.cache("minilm-l6-v1"), runner.MODEL)
        with self.assertRaises(ValueError):
            comp.cache("../outside")

    def test_split_validation_includes_hard_negatives_and_old_queries(self):
        _, _, _, queries = comp.inputs()
        original = ev.load_json(ev.ROOT / "queries.json")
        bad = copy.deepcopy(queries)
        negative = next(q for q in bad if q["split"] == "test" and q["category"] == "hard-negative")
        negative["intentFamily"] = "sql"
        with self.assertRaisesRegex(ValueError, "negative intent family"):
            comp.validate_followup(bad, original)
        bad = copy.deepcopy(queries)
        bad[0]["text"] = "  " + original[0]["text"].upper() + " "
        with self.assertRaisesRegex(ValueError, "inspected"):
            comp.validate_followup(bad, original)
        with self.assertRaisesRegex(ValueError, "category"):
            comp.validate_followup([q for q in queries if q["category"] != "partial"], original)
        bad = copy.deepcopy(queries)
        for q in bad:
            q["relevant"] = {k: 2 for k in q["relevant"]}
        with self.assertRaisesRegex(ValueError, "graded"):
            comp.validate_followup(bad, original)

    def test_invalid_profile_and_fingerprint(self):
        manifest, _, _ = ev.fixtures()
        for changes in ({"model": "../escape/model"}, {"model": "../escape"}, {"model": "models/.."}, {"revision": "main"}, {"dimensions": 0}, {"dimensions": True}, {"pooling": "unknown"}, {"max_input_length": -1}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                ev.validate_manifest(dict(manifest, **changes))
        ev.validate_manifest(dict(manifest, dimensions=768))
        hashes = comp.fingerprint()
        self.assertIn("test/search-relevance/comparison-v2/queries.json", hashes)
        self.assertIn("deployments/embeddings/profiles/minilm-l6-v1.json", hashes)
        self.assertIn("internal/publication/manager.go", hashes)
        with patch.object(comp.ev, "digest", return_value="changed"), self.assertRaisesRegex(ValueError, "corpus"):
            comp.inputs()

    def test_actual_comparison_inputs_reject_representative_whitespace_aliases(self):
        _, _, documents, queries = comp.inputs()
        representative = documents[0]["representativeQueries"][0]
        original_loader = ev.load_json
        for text in (representative, representative.upper(), "  " + representative + "  ",
                     "\t" + "  \t".join(representative.split()) + "\n"):
            selected = copy.deepcopy(queries)
            next(q for q in selected if q["split"] == "test" and not q["relevant"])["text"] = text

            def load(path):
                return selected if Path(path) == comp.EXPERIMENT / "queries.json" else original_loader(path)

            with self.subTest(text=text), patch.object(ev, "load_json", side_effect=load), self.assertRaisesRegex(ValueError, "held-out query copied"):
                comp.inputs()
        selected = copy.deepcopy(queries)
        independent = "  Which volcanic glass suppliers are supported here?  "
        next(q for q in selected if q["split"] == "test" and not q["relevant"])["text"] = independent
        with patch.object(ev, "load_json", side_effect=load):
            *_, admitted = comp.inputs()
        self.assertIn(independent, [q["text"] for q in admitted])
        self.assertNotIn(independent, [q["text"] for q in queries])

    def test_profile_cache_parent_symlink_is_rejected_before_writes(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "other").mkdir()
            (root / "models").symlink_to(root / "other", target_is_directory=True)
            model = root / "models/minilm"
            with self.assertRaisesRegex(ValueError, "symlink"):
                runner.install({"files": {}}, model)
            self.assertFalse((root / "other/minilm").exists())
            with self.assertRaisesRegex(ValueError, "symlink"):
                runner.verify_model({"files": {}}, model)

    def test_selection_prioritizes_no_answer_before_quality_and_stable_ties(self):
        def trial(name, errors, quality):
            return {"profile": name, "threshold": 0.7, "grid": [{"threshold": 0.7, "excess_false_positives": errors, "ndcg": quality}]}
        self.assertEqual(comp.select_candidate([trial("a", 1, 1), trial("b", 0, 0.5)]), "b")
        self.assertEqual(comp.select_candidate([trial("a", 0, 0.5), trial("b", 0, 0.6)]), "b")
        self.assertEqual(comp.select_candidate([trial("b", 0, 0.5), trial("a", 0, 0.5)]), "a")

    def test_no_inference_on_wrong_split(self):
        _, profiles, docs, queries = comp.inputs()
        with patch.object(runner, "start_tei") as start:
            with self.assertRaisesRegex(ValueError, "tuning only"):
                comp.tuning_trial("bge-small-v1", profiles["bge-small-v1"], docs, queries, {}, Path("unused"))
            with self.assertRaisesRegex(ValueError, "held-out only"):
                comp.qualify("bge-small-v1", profiles["bge-small-v1"], docs, queries, {}, 0.8, {}, Path("unused"))
            start.assert_not_called()

    def test_model_specific_prefix_and_token_rejection(self):
        _, profiles, docs, queries = comp.inputs()
        for profile in profiles.values():
            with patch.object(runner, "embed", side_effect=[[[1, 0]], [[0, 1]]]) as embed:
                vectors, query_vectors = comp.encode("owned", profile, docs[:1], queries[:1], [])
                self.assertEqual(embed.call_args_list[0].args[2], [ev.projection(docs[0])])
                self.assertEqual(embed.call_args_list[1].args[2], [profile["query_prefix"] + queries[0]["text"]])
                self.assertEqual(comp.similarities(docs[:1], queries[:1], vectors, query_vectors)[queries[0]["id"]][docs[0]["id"]], 0)
            error = urllib.error.HTTPError("owned", 422, "overlength", {}, None)
            with patch.object(runner, "request", side_effect=error) as request:
                comp.reject_overlength("owned", profile)
                self.assertEqual(len(request.call_args.args[2]["input"].split()), profile["max_input_length"] + 10)
            with patch.object(runner, "request", return_value={}), self.assertRaisesRegex(ValueError, "silently"):
                comp.reject_overlength("owned", profile)
            with patch.object(runner, "request", side_effect=urllib.error.HTTPError("owned", 500, "failed", {}, None)), self.assertRaises(urllib.error.HTTPError):
                comp.reject_overlength("owned", profile)

    def test_both_candidates_tune_before_one_frozen_heldout_run(self):
        for failure in (None, "inputs", "qualification", "frozen"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temporary:
                stages = []
                def tune(name, profile, documents, queries, lexical, directory):
                    self.assertEqual({q["split"] for q in queries}, {"tune"})
                    self.assertFalse((directory / "frozen.json").exists())
                    stages.append(name)
                    return {"profile": name, "threshold": 0.8, "grid": [{"threshold": 0.8, "excess_false_positives": 0, "ndcg": 0.5}]}, {}
                def qualify(name, profile, documents, queries, lexical, threshold, docs, directory):
                    self.assertEqual(stages, ["bge-small-v1", "minilm-l6-v1"])
                    self.assertEqual({q["split"] for q in queries}, {"test"})
                    self.assertEqual(name, "bge-small-v1")
                    frozen = ev.load_json(directory / "frozen.json")
                    self.assertEqual((frozen["profile"], frozen["threshold"]), (name, threshold))
                    if failure == "qualification":
                        raise RuntimeError("qualification failed")
                    if failure == "frozen":
                        runner.save(directory / "frozen.json", {})
                    return {"recommendation": "retain-lexical", "cleanup": "complete"}
                hashes = [{"fixed": "hash"}, {"fixed": "hash"}, {"fixed": "changed" if failure == "inputs" else "hash"}, {"fixed": "hash"}]
                with patch.object(runner, "OUTPUT", Path(temporary)), patch.object(runner, "command", return_value="revision"), patch.object(runner, "baseline", return_value={}), patch.object(comp, "tuning_trial", side_effect=tune), patch.object(comp, "qualify", side_effect=qualify) as heldout, patch.object(comp, "fingerprint", side_effect=hashes), contextlib.redirect_stdout(io.StringIO()):
                    if failure:
                        with self.assertRaises((ValueError, RuntimeError)):
                            comp.evaluate()
                    else:
                        self.assertEqual(comp.evaluate()["recommendation"], "retain-lexical")
                report = ev.load_json(next(Path(temporary).glob("*/report.json")))
                self.assertEqual(report["status"], "failed" if failure else "complete")
                self.assertEqual(heldout.call_count, 0 if failure == "inputs" else 1)

    def test_tuning_trial_records_real_metrics_and_only_marks_success_after_cleanup(self):
        """Fake the provider, retaining encoding, ranking, tuning and owned cleanup."""
        _, profiles, docs, queries = comp.inputs()
        name = "minilm-l6-v1"
        document = dict(docs[0], public=True, source="local")
        query = dict(queries[0], kind=document["kind"], relevant={document["id"]: 2})
        lexical = {s: {query["id"]: {document["id"]: 0}} for s in comp.SURFACES}
        for failure in (None, "startup", "overlength", "integrity", "cleanup"):
            with self.subTest(failure=failure), tempfile.TemporaryDirectory() as temporary, contextlib.ExitStack() as stack:
                directory = Path(temporary)
                verify = stack.enter_context(patch.object(runner, "verify_model", side_effect=[None, ValueError("integrity")] if failure == "integrity" else None))
                stack.enter_context(patch.object(runner, "start_tei", side_effect=RuntimeError("startup") if failure == "startup" else None, return_value=("owned", {"version": "1.9.4"})))
                stack.enter_context(patch.object(comp, "reject_overlength", side_effect=ValueError("overlength") if failure == "overlength" else None))
                embed = stack.enter_context(patch.object(runner, "embed", side_effect=[[[1, 0]], [[1, 0]]]))
                cleanup = stack.enter_context(patch.object(runner.subprocess, "run", return_value=subprocess.CompletedProcess([], 1 if failure == "cleanup" else 0, "", "cleanup")))
                if failure:
                    with self.assertRaisesRegex((RuntimeError, ValueError), failure):
                        comp.tuning_trial(name, profiles[name], [document], [query], lexical, directory)
                    self.assertFalse((directory / (name + "-tuning.json")).exists())
                else:
                    result, vectors = comp.tuning_trial(name, profiles[name], [document], [query], lexical, directory)
                    self.assertEqual(vectors, {document["id"]: [1, 0]})
                    self.assertEqual(result["cleanup"], "complete")
                    self.assertEqual(result["results"]["public"]["summary"]["hybrid"]["ndcg"], 1)
                    self.assertEqual(ev.load_json(directory / (name + "-tuning.json")), result)
                    self.assertEqual(ev.load_json(directory / (name + "-vectors.json"))["queries"], {query["id"]: [1, 0]})
                    self.assertEqual(verify.call_count, 2)
                self.assertEqual(cleanup.call_count, 3)
                self.assertTrue((directory / (name + "-resources.json")).exists())
                if failure in ("startup", "overlength"):
                    embed.assert_not_called()

    def test_cli_keeps_acquisition_separate_from_evaluation(self):
        _, profiles, _, _ = comp.inputs()
        with patch("sys.argv", ["comparison.py", "install"]), patch.object(runner, "install") as install, patch.object(comp, "evaluate") as evaluate:
            comp.main()
            self.assertEqual([call.args for call in install.call_args_list], [(profile, comp.cache(name)) for name, profile in profiles.items()])
            evaluate.assert_not_called()
        previous = signal.getsignal(signal.SIGTERM)
        self.addCleanup(signal.signal, signal.SIGTERM, previous)
        report = {"status": "complete", "profile": "minilm-l6-v1", "threshold": 0.5, "recommendation": "retain-lexical", "acceptance": {}, "internal": "not in summary"}
        output = io.StringIO()
        with patch("sys.argv", ["comparison.py", "evaluate"]), patch.object(runner, "install") as install, patch.object(comp, "evaluate", return_value=report), contextlib.redirect_stdout(output):
            comp.main()
            install.assert_not_called()
        self.assertEqual(json.loads(output.getvalue()), {key: value for key, value in report.items() if key != "internal"})
        with self.assertRaisesRegex(KeyboardInterrupt, "signal"):
            signal.getsignal(signal.SIGTERM)(signal.SIGTERM, None)

    def test_input_change_during_loading_stops_before_any_trial(self):
        with patch.object(comp, "fingerprint", side_effect=[{"input": "before"}, {"input": "after"}]), patch.object(runner, "baseline") as baseline, patch.object(comp, "tuning_trial") as trial, self.assertRaisesRegex(ValueError, "while loading"):
            comp.evaluate()
        baseline.assert_not_called()
        trial.assert_not_called()

    def test_reported_selection_strategy_must_match_implemented_policy(self):
        experiment = ev.load_json(comp.EXPERIMENT / "experiment.json")
        with patch.object(comp.ev, "load_json", return_value=dict(experiment, selection="highest-score-ignore-no-answer")), self.assertRaisesRegex(ValueError, "comparison definition"):
            comp.inputs()

    def test_restarted_document_mismatch_stops_before_heldout_inference(self):
        _, profiles, docs, queries = comp.inputs()
        query = next(q for q in queries if q["split"] == "test")
        document = docs[0]
        with tempfile.TemporaryDirectory() as directory, patch.object(runner, "verify_model"), patch.object(runner, "owned_resources", return_value=contextlib.nullcontext({"tei": "owned"})), patch.object(runner, "start_tei", return_value=("owned", {})), patch.object(comp, "encode", return_value=({document["id"]: [0, 1]}, {})) as encode, patch.object(comp, "encode_queries") as query_inference, patch.object(runner, "pgvector_parity") as sql, self.assertRaisesRegex(ValueError, "changed document vectors"):
            comp.qualify("minilm-l6-v1", profiles["minilm-l6-v1"], [document], [query], {}, 0.5, {document["id"]: [1, 0]}, Path(directory))
        self.assertEqual(encode.call_args.args[3], [])
        query_inference.assert_not_called()
        sql.assert_not_called()

    def test_qualification_gates_numeric_checks_and_optional_resource_data(self):
        _, profiles, _, _ = comp.inputs()
        profile = profiles["minilm-l6-v1"]
        documents = [{"id": "d"}]
        queries = [{"id": "q", "text": "query", "split": "test"}]
        for outcome in ("pass", "kind-ndcg", "kind-recall", "single-drift", "sql-failure"):
            metrics = {"summary": {"lexical": {"ndcg": 0.5, "recall": 0.5, "false_positives": 0}, "hybrid": {"ndcg": 0.7, "recall": 0.7, "false_positives": 0}},
                       "per_kind": {"agent": {"lexical": {"answerable": 1, "ndcg": 0.5, "recall": 0.5},
                                              "hybrid": {"ndcg": 0.4 if outcome == "kind-ndcg" else 0.7, "recall": 0.4 if outcome == "kind-recall" else 0.7}},
                                    "skill": {"lexical": {"answerable": 0, "ndcg": None, "recall": None}, "hybrid": {"ndcg": None, "recall": None}}}, "exact_failures": []}
            single_vector = [0, 1] if outcome == "single-drift" else [1, 0]
            def command(args):
                return "128" if "memory.peak" in args[-1] and outcome == "pass" else "{}"
            with self.subTest(outcome=outcome), tempfile.TemporaryDirectory() as directory, patch.object(runner, "verify_model"), patch.object(runner, "owned_resources", return_value=contextlib.nullcontext({"tei": "owned"})), patch.object(runner, "start_tei", return_value=("owned", {})), patch.object(comp, "encode", return_value=({"d": [1, 0]}, {})), patch.object(comp, "encode_queries", return_value={"q": [1, 0]}), patch.object(comp.ev, "measure", return_value=metrics), patch.object(runner, "embed", return_value=[single_vector]), patch.object(runner, "command", side_effect=command), patch.object(runner, "pgvector_parity", return_value={"verified": True}) as sql:
                if outcome == "sql-failure":
                    sql.side_effect = ValueError("parity differs")
                if outcome in {"single-drift", "sql-failure"}:
                    with self.assertRaisesRegex(ValueError, "single/batch" if outcome == "single-drift" else "parity"):
                        comp.qualify("minilm-l6-v1", profile, documents, queries, dict.fromkeys(comp.SURFACES, {}), 0.5, {"d": [1, 0]}, Path(directory))
                    self.assertFalse((Path(directory) / "heldout-vectors.json").exists())
                    if outcome == "single-drift":
                        sql.assert_not_called()
                else:
                    result = comp.qualify("minilm-l6-v1", profile, documents, queries, dict.fromkeys(comp.SURFACES, {}), 0.5, {"d": [1, 0]}, Path(directory))
                    self.assertEqual(result["recommendation"], "implement" if outcome == "pass" else "retain-lexical")
                    self.assertEqual(result["peak_memory_bytes"], 128 if outcome == "pass" else None)
                    self.assertEqual(result["cleanup"], "complete")
                    self.assertEqual(result["acceptance"]["control"]["kinds_preserved"], outcome == "pass")

    def test_minilm_launcher_checks_its_actual_server_profile(self):
        _, profiles, _, _ = comp.inputs()
        profile = profiles["minilm-l6-v1"]
        info = {"served_model_name": profile["model"], "model_sha": profile["revision"], "model_dtype": "float32", "max_input_length": 256, "auto_truncate": False, "model_type": {"embedding": {"pooling": "mean"}}, "version": "1.9.4"}
        try:
            with patch.object(runner, "command", side_effect=["", "", '{"owned":{"IPAddress":"172.20.0.2"}}']) as command, patch.object(runner, "request", return_value=info):
                runner.start_tei(profile, {"network": "owned", "tei": "owned-tei"}, Path("/test-only/minilm"))
                argv = command.call_args_list[1].args[0]
                self.assertEqual(argv[argv.index("--pooling") + 1], "mean")
                self.assertIn("type=bind,src=/test-only/minilm,dst=/model,readonly", argv)
        finally:
            runner.OWNED_ORIGINS.clear()


if __name__ == "__main__":
    unittest.main()
