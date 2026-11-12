"""Unit tests for the Catalog discovery & LangGraph recommendation benchmark."""

import contextlib
from pathlib import Path
import sys
import tempfile
import unittest
from unittest.mock import patch

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))
import evaluation as ev
import runner

from benchmark import (
    load_benchmark_inputs,
    select_finalists,
    BenchmarkAgentObserver,
    evaluate_agent_episode,
    aggregate_agent_benchmark,
    generate_markdown_report,
)


class BenchmarkTests(unittest.TestCase):
    """Verify benchmark data integrity, finalist selection, and agent evaluation logic."""

    def test_benchmark_inputs_and_fixtures(self):
        manifest, profiles, documents, queries = load_benchmark_inputs()
        self.assertEqual(len(documents), 160)
        self.assertEqual(len(queries), 96)
        self.assertEqual(len(profiles), 4)

        # Check document kinds (40 each)
        for k in ev.KINDS:
            self.assertEqual(sum(d["kind"] == k for d in documents), 40)

        # Check split counts (32 tune, 64 test)
        self.assertEqual(sum(q["split"] == "tune" for q in queries), 32)
        self.assertEqual(sum(q["split"] == "test" for q in queries), 64)

        # Check agent benchmark tasks
        agent_tasks = [q for q in queries if q.get("agent_benchmark")]
        self.assertEqual(len(agent_tasks), 16)
        for at in agent_tasks:
            self.assertEqual(at["split"], "test")

    def test_split_isolation_and_no_leakage(self):
        _, _, documents, queries = load_benchmark_inputs()
        tune_families = {q["intentFamily"] for q in queries if q["split"] == "tune"}
        test_families = {q["intentFamily"] for q in queries if q["split"] == "test"}
        self.assertEqual(tune_families & test_families, set(), "Intent family must not cross tune/test split")

        tune_texts = {ev.query_key(q["text"]) for q in queries if q["split"] == "tune"}
        test_texts = {ev.query_key(q["text"]) for q in queries if q["split"] == "test"}
        self.assertEqual(tune_texts & test_texts, set(), "Query text must not cross tune/test split")

    def test_finalist_selection_logic(self):
        def make_trial(profile_name, excess_fp, ndcg_val, lex_ndcg=0.5):
            return {
                "profile": profile_name,
                "threshold": 0.7,
                "grid": [{"threshold": 0.7, "excess_false_positives": excess_fp, "ndcg": ndcg_val}],
                "results": {"control": {"summary": {"lexical": {"ndcg": lex_ndcg}}}},
            }

        # Candidate with 0 excess FP and higher nDCG should win
        trials = [
            make_trial("prof-a", excess_fp=1, ndcg_val=0.8),
            make_trial("prof-b", excess_fp=0, ndcg_val=0.65),
            make_trial("prof-c", excess_fp=0, ndcg_val=0.75),
            make_trial("prof-d", excess_fp=0, ndcg_val=0.70),
        ]
        finalists = select_finalists(trials, max_finalists=2)
        self.assertEqual(finalists, ["prof-c", "prof-d"])

        # Fallback when no candidate qualifies (all have negative gain or excess FP)
        poor_trials = [
            make_trial("prof-x", excess_fp=2, ndcg_val=0.3, lex_ndcg=0.6),
            make_trial("prof-y", excess_fp=1, ndcg_val=0.4, lex_ndcg=0.6),
        ]
        fallback_finalists = select_finalists(poor_trials, max_finalists=2)
        self.assertEqual(len(fallback_finalists), 1)
        self.assertEqual(fallback_finalists[0], "prof-y")

    def test_agent_observer_and_grounding_eval(self):
        observer = BenchmarkAgentObserver(None, "hybrid_bge")
        observer.record_search("test query", ["skill"], [{"release_id": "weather-skill"}, {"release_id": "weather-agent"}])
        observer.record_read("weather-skill", success=True)
        observer.close()

        task_query = {
            "id": "test-weather-task",
            "family": "weather",
            "relevant": {"weather-skill": 2, "weather-agent": 2},
        }

        # 1. Successful grounded recommendation
        eval_success = evaluate_agent_episode(task_query, observer, [{"release_id": "weather-skill"}])
        self.assertEqual(eval_success["outcome"], "successful_discovery")
        self.assertTrue(eval_success["grounding_valid"])

        # 2. Grounding failure (recommended unread ID)
        eval_ungrounded = evaluate_agent_episode(task_query, observer, [{"release_id": "weather-agent"}])
        self.assertEqual(eval_ungrounded["outcome"], "grounding_failure")
        self.assertFalse(eval_ungrounded["grounding_valid"])

        # 3. Wrong selection (irrelevant ID recommended)
        eval_wrong = evaluate_agent_episode(task_query, observer, [{"release_id": "weather-skill"}, {"release_id": "unknown-skill"}])
        self.assertEqual(eval_wrong["outcome"], "grounding_failure")  # unread unknown-skill

        # 4. Appropriate abstention on no-answer query
        no_answer_task = {
            "id": "test-unrelated-astronomy",
            "family": "no-answer",
            "relevant": {},
        }
        eval_abstain = evaluate_agent_episode(no_answer_task, observer, [])
        self.assertEqual(eval_abstain["outcome"], "appropriate_abstention")

        # 5. Unsupported recommendation on no-answer query
        eval_unsupported = evaluate_agent_episode(no_answer_task, observer, [{"release_id": "weather-skill"}])
        self.assertEqual(eval_unsupported["outcome"], "unsupported_recommendation")

    def test_aggregate_agent_benchmark(self):
        episodes = [
            {"arm": "lexical", "is_no_answer": False, "outcome": "successful_discovery", "grounding_valid": True, "duration_seconds": 1.2, "searches_count": 1, "reads_count": 1},
            {"arm": "lexical", "is_no_answer": False, "outcome": "missed_opportunity", "grounding_valid": True, "duration_seconds": 1.0, "searches_count": 1, "reads_count": 0},
            {"arm": "lexical", "is_no_answer": True, "outcome": "appropriate_abstention", "grounding_valid": True, "duration_seconds": 0.8, "searches_count": 1, "reads_count": 0},
            {"arm": "hybrid", "is_no_answer": False, "outcome": "successful_discovery", "grounding_valid": True, "duration_seconds": 1.5, "searches_count": 1, "reads_count": 2},
            {"arm": "hybrid", "is_no_answer": True, "outcome": "appropriate_abstention", "grounding_valid": True, "duration_seconds": 0.9, "searches_count": 1, "reads_count": 0},
        ]
        summary = aggregate_agent_benchmark(episodes)
        self.assertIn("lexical", summary)
        self.assertIn("hybrid", summary)
        self.assertEqual(summary["lexical"]["discovery_success_rate"], 0.5)
        self.assertEqual(summary["lexical"]["abstention_rate"], 1.0)
        self.assertEqual(summary["hybrid"]["discovery_success_rate"], 1.0)
        self.assertEqual(summary["hybrid"]["abstention_rate"], 1.0)

    def test_markdown_report_formatting(self):
        report = {
            "status": "complete",
            "started": "2026-11-11T12:00:00Z",
            "finalists": ["bge-small-v1", "granite-278m-v1"],
            "fixed_query_results": {
                "bge-small-v1": {
                    "recommendation": "implement",
                    "results": {
                        "control": {
                            "summary": {
                                "lexical": {"ndcg": 0.65, "recall": 0.70},
                                "hybrid": {"ndcg": 0.82, "recall": 0.88},
                            }
                        }
                    }
                }
            },
            "agent_results": {
                "summary": {
                    "lexical": {
                        "total_episodes": 48,
                        "discovery_success_rate": 0.75,
                        "abstention_rate": 1.0,
                        "grounding_failure_count": 0,
                        "mean_duration_seconds": 1.85,
                    }
                }
            }
        }
        with tempfile.TemporaryDirectory() as td:
            out_file = Path(td) / "report.md"
            generate_markdown_report(report, out_file)
            content = out_file.read_text()
            self.assertIn("# Catalog Discovery & Agent Recommendation Benchmark Report", content)
            self.assertIn("bge-small-v1", content)
            self.assertIn("0.8200", content)

    def test_end_to_end_benchmark_mocked_execution(self):
        """Verify the full run_benchmark execution pipeline with mocked provider."""
        from benchmark import run_benchmark

        manifest, profiles, documents, queries = load_benchmark_inputs()
        fake_lexical = {s: {q["id"]: {d["id"]: 0.5 for d in documents} for q in queries} for s in ("control", "public")}

        import urllib.error

        def mock_embed(origin, prof, texts, timings):
            return [[1.0] + [0.0] * (prof["dimensions"] - 1) for _ in texts]

        import email.message

        def mock_request(origin, path, payload=None):
            if payload and "word " in str(payload.get("input")):
                raise urllib.error.HTTPError("http://127.0.0.1", 422, "overlength", email.message.Message(), None)
            return {}

        with tempfile.TemporaryDirectory() as td:
            with patch.object(runner, "OUTPUT", Path(td)), \
                 patch.object(runner, "baseline", return_value=fake_lexical), \
                 patch.object(runner, "verify_model"), \
                 patch.object(runner, "start_tei", return_value=("http://127.0.0.1:8080", {"version": "1.9.4"})), \
                 patch.object(runner, "embed", side_effect=mock_embed), \
                 patch.object(runner, "request", side_effect=mock_request), \
                 patch.object(runner, "pgvector_parity", return_value={"verified": True}), \
                 patch.object(runner, "command", return_value="{}"), \
                 patch.object(runner, "owned_resources", return_value=contextlib.nullcontext({"network": "net", "tei": "tei", "pg": "pg"})):

                report = run_benchmark(skip_agent=False)
                self.assertEqual(report["status"], "complete")
                self.assertIn("finalists", report)
                self.assertTrue(len(report["finalists"]) > 0)
                self.assertIn("agent_results", report)
                self.assertIn("fixed_query_results", report)


if __name__ == "__main__":
    unittest.main()
