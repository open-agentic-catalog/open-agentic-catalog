"""Observation completion, rather than request start, owns acceptance deadlines."""
import unittest
from unittest.mock import patch

import runner


class DeadlineTests(unittest.TestCase):
    def test_readiness_accepts_only_success_before_deadline(self):
        for completed in (59.999, 60, 61):
            now = [0.0]
            def get(_url):
                now[0] = completed
                return b"ready"
            with self.subTest(completed=completed), patch.object(runner.time, "monotonic", side_effect=lambda: now[0]), patch.object(runner.time, "sleep") as sleep, patch.object(runner, "get", side_effect=get) as request:
                if completed < 60:
                    self.assertEqual(b"ready", runner.wait_http("owned"))
                else:
                    with self.assertRaisesRegex(runner.Failure, "fixture-readiness-deadline"):
                        runner.wait_http("owned")
                request.assert_called_once_with("owned")
                sleep.assert_not_called()

    def test_readiness_caps_sleep_and_stops_after_failed_observation(self):
        for failure in (runner.Failure("starting"), runner.http.ClientFailure("unavailable")):
            for completed in (59.9, 60, 61):
                now = [0.0]
                def get(_url):
                    now[0] = completed
                    raise failure
                def sleep(seconds):
                    self.assertAlmostEqual(.1, seconds)
                    now[0] += seconds
                with self.subTest(completed=completed, failure=type(failure)), patch.object(runner.time, "monotonic", side_effect=lambda: now[0]), patch.object(runner.time, "sleep", side_effect=sleep) as slept, patch.object(runner, "get", side_effect=get) as request:
                    with self.assertRaisesRegex(runner.Failure, "fixture-readiness-deadline"):
                        runner.wait_http("owned")
                    self.assertEqual(int(completed < 60), slept.call_count)
                    request.assert_called_once()

    def test_drain_accepts_only_complete_idle_observation_before_deadline(self):
        idle = {'open_agentic_catalog_jobs{state="' + state + '"}': 0 for state in ("queued", "running", "waiting")}
        idle['open_agentic_catalog_outbox_events{state="pending"}'] = 0
        for completed in (29.999, 30, 31):
            now = [0.0]
            def metrics(*_args, **_kwargs):
                now[0] = completed
                return idle
            with self.subTest(completed=completed), patch.object(runner.time, "monotonic", side_effect=lambda: now[0]), patch.object(runner.time, "sleep") as sleep, patch.object(runner, "metrics_sample", side_effect=metrics) as request:
                if completed < 30:
                    result = runner.drain_worker("owned")
                    self.assertEqual({"status": "passed", "elapsedSeconds": completed, "nonterminalJobs": 0, "pendingOutbox": 0}, result)
                else:
                    with self.assertRaisesRegex(runner.Failure, "worker-drain-incomplete"):
                        runner.drain_worker("owned")
                request.assert_called_once_with("owned", worker=True)
                sleep.assert_not_called()

    def test_drain_caps_sleep_and_preserves_missing_or_failed_observations(self):
        busy = {'open_agentic_catalog_jobs{state="' + state + '"}': 1 for state in ("queued", "running", "waiting")}
        busy['open_agentic_catalog_outbox_events{state="pending"}'] = 1
        with patch.object(runner.time, "monotonic", side_effect=[0, 0, 29.9, 30]), patch.object(runner.time, "sleep") as sleep, patch.object(runner, "metrics_sample", return_value=busy):
            with self.assertRaisesRegex(runner.Failure, "worker-drain-incomplete"):
                runner.drain_worker("owned")
            self.assertAlmostEqual(.1, sleep.call_args.args[0])
        for value, error, expected in [({}, None, "required-drain-signals-missing"), (None, runner.Failure("invalid-metric"), "invalid-metric")]:
            with self.subTest(expected=expected), patch.object(runner, "metrics_sample", return_value=value, side_effect=error), patch.object(runner.time, "sleep") as sleep:
                with self.assertRaisesRegex(runner.Failure, expected):
                    runner.drain_worker("owned")
                sleep.assert_not_called()
